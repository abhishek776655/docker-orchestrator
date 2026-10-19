// Package store persists control-plane state in PostgreSQL (SRS 5.1
// "Metadata Store", NFR-001: recover desired state after restart). M1's
// in-memory scheduler state has no equivalent here; reservations are
// derived by summing non-terminal workloads per node, not stored
// separately, so there is one source of truth.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

const schema = `
CREATE TABLE IF NOT EXISTS nodes (
	id            text PRIMARY KEY,
	address       text NOT NULL DEFAULT '',
	architecture  text NOT NULL,
	labels        jsonb NOT NULL DEFAULT '{}',
	capabilities  jsonb NOT NULL DEFAULT '{}',
	cpu_millis    bigint NOT NULL,
	memory_bytes  bigint NOT NULL,
	availability  text NOT NULL,
	heartbeat_at  timestamptz NOT NULL,
	gpus          jsonb NOT NULL DEFAULT '[]',
	thermal       jsonb NOT NULL DEFAULT '{"Known": false}',
	cached_images jsonb NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS workloads (
	key                  text PRIMARY KEY,
	project              text NOT NULL,
	service              text NOT NULL,
	revision             text NOT NULL,
	image                text NOT NULL,
	command              jsonb NOT NULL DEFAULT '[]',
	env                  jsonb NOT NULL DEFAULT '{}',
	cpu_millis           bigint NOT NULL,
	memory_bytes         bigint NOT NULL,
	architecture         text NOT NULL DEFAULT '',
	host_ports           jsonb NOT NULL DEFAULT '[]',
	container_ports      jsonb NOT NULL DEFAULT '[]',
	storage_class        text NOT NULL,
	storage_pinned_node  text NOT NULL DEFAULT '',
	volumes              jsonb NOT NULL DEFAULT '[]',
	depends_on           jsonb NOT NULL DEFAULT '[]',
	desired_state        text NOT NULL,
	status               text NOT NULL,
	status_reason        text NOT NULL DEFAULT '',
	node_id              text NOT NULL DEFAULT '',
	container_id         text NOT NULL DEFAULT '',
	created_at           timestamptz NOT NULL DEFAULT now(),
	updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS events (
	id           bigserial PRIMARY KEY,
	workload_key text NOT NULL,
	node_id      text NOT NULL DEFAULT '',
	type         text NOT NULL,
	message      text NOT NULL,
	created_at   timestamptz NOT NULL DEFAULT now()
);
`

// Store wraps a PostgreSQL connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres and ensures the schema exists.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the connection pool.
func (s *Store) Close() { s.pool.Close() }

// UpsertNode registers a node or refreshes its heartbeat/capacity,
// including hardware telemetry (SRS §9.2, M7).
func (s *Store) UpsertNode(ctx context.Context, n model.Node) error {
	labels, _ := json.Marshal(n.Labels)
	caps, _ := json.Marshal(n.Capabilities)
	gpus, _ := json.Marshal(n.GPUs)
	thermal, _ := json.Marshal(n.Thermal)
	cachedImages, _ := json.Marshal(n.CachedImages)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO nodes (id, address, architecture, labels, capabilities, cpu_millis, memory_bytes, availability, heartbeat_at, gpus, thermal, cached_images)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE SET
			address = EXCLUDED.address,
			architecture = EXCLUDED.architecture,
			labels = EXCLUDED.labels,
			capabilities = EXCLUDED.capabilities,
			cpu_millis = EXCLUDED.cpu_millis,
			memory_bytes = EXCLUDED.memory_bytes,
			availability = EXCLUDED.availability,
			heartbeat_at = EXCLUDED.heartbeat_at,
			gpus = EXCLUDED.gpus,
			thermal = EXCLUDED.thermal,
			cached_images = EXCLUDED.cached_images
	`, n.ID, n.Address, n.Architecture, labels, caps, n.Allocatable.CPUMillis, n.Allocatable.MemoryBytes, string(n.Availability), n.HeartbeatAt, gpus, thermal, cachedImages)
	return err
}

// Heartbeat updates a node's last-seen timestamp and refreshes its
// telemetry snapshot (SRS 6.2 "send heartbeat/resource snapshot").
func (s *Store) Heartbeat(ctx context.Context, nodeID string, thermal model.Thermal, cachedImages []string) error {
	thermalJSON, _ := json.Marshal(thermal)
	cachedJSON, _ := json.Marshal(cachedImages)
	tag, err := s.pool.Exec(ctx, `
		UPDATE nodes SET heartbeat_at = now(), availability = 'available', thermal = $2, cached_images = $3
		WHERE id = $1
	`, nodeID, thermalJSON, cachedJSON)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("unknown node %q", nodeID)
	}
	return nil
}

// MarkStaleNodesUnavailable excludes nodes from new placements once their
// heartbeat is older than timeout (SRS FR-015).
func (s *Store) MarkStaleNodesUnavailable(ctx context.Context, timeout time.Duration) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE nodes SET availability = 'unavailable'
		WHERE availability = 'available' AND heartbeat_at < now() - $1::interval
	`, timeout.String())
	return err
}

// WorkloadsOnUnavailableNodes returns every non-terminal workload currently
// assigned to a node the heartbeat timeout has marked unavailable — the
// reconciler's input for deciding what to reschedule or fail (SRS §6.3,
// FR-015).
func (s *Store) WorkloadsOnUnavailableNodes(ctx context.Context) ([]WorkloadRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT w.project, w.service, w.revision, w.image, w.command, w.env, w.cpu_millis, w.memory_bytes, w.architecture, w.host_ports, w.container_ports, w.storage_class, w.storage_pinned_node, w.volumes, w.depends_on, w.desired_state, w.status, w.status_reason, w.node_id, w.container_id
		FROM workloads w
		JOIN nodes n ON n.id = w.node_id
		WHERE n.availability = 'unavailable' AND w.status NOT IN ('failed', 'stopped')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WorkloadRecord
	for rows.Next() {
		rec, err := scanWorkloadRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

// Reassign moves a workload to a different node and resets it to
// "assigned" so the new node's agent picks it up fresh (SRS §6.3
// reconciliation: "issue safe corrective actions").
func (s *Store) Reassign(ctx context.Context, key, newNodeID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE workloads SET node_id = $2, status = 'assigned', status_reason = '', container_id = '', updated_at = now()
		WHERE key = $1
	`, key, newNodeID)
	if err == nil {
		_, _ = s.pool.Exec(ctx, `INSERT INTO events (workload_key, node_id, type, message) VALUES ($1, $2, 'reassigned', 'rescheduled after node loss')`, key, newNodeID)
	}
	return err
}

// Event is one audit/history record (SRS FR-024).
type Event struct {
	ID          int64
	WorkloadKey string
	NodeID      string
	Type        string
	Message     string
	CreatedAt   time.Time
}

// ListEvents returns the most recent events, newest first (SRS §13, FR-024).
func (s *Store) ListEvents(ctx context.Context, limit int) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workload_key, node_id, type, message, created_at
		FROM events ORDER BY id DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.WorkloadKey, &e.NodeID, &e.Type, &e.Message, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Metrics is a minimal operational summary (SRS §13 "required visibility"),
// a JSON endpoint rather than a Prometheus exporter — see M6 doc for why.
type Metrics struct {
	NodesByAvailability map[string]int `json:"nodes_by_availability"`
	WorkloadsByStatus   map[string]int `json:"workloads_by_status"`
}

func (s *Store) Metrics(ctx context.Context) (*Metrics, error) {
	m := &Metrics{NodesByAvailability: map[string]int{}, WorkloadsByStatus: map[string]int{}}

	rows, err := s.pool.Query(ctx, `SELECT availability, count(*) FROM nodes GROUP BY availability`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var c int
		if err := rows.Scan(&k, &c); err != nil {
			rows.Close()
			return nil, err
		}
		m.NodesByAvailability[k] = c
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `SELECT status, count(*) FROM workloads GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var c int
		if err := rows.Scan(&k, &c); err != nil {
			rows.Close()
			return nil, err
		}
		m.WorkloadsByStatus[k] = c
	}
	rows.Close()

	return m, rows.Err()
}

// ListNodes returns every known node.
func (s *Store) ListNodes(ctx context.Context) ([]model.Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, address, architecture, labels, capabilities, cpu_millis, memory_bytes, availability, heartbeat_at, gpus, thermal, cached_images FROM nodes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []model.Node
	for rows.Next() {
		var n model.Node
		var labels, caps, gpus, thermal, cachedImages []byte
		var availability string
		if err := rows.Scan(&n.ID, &n.Address, &n.Architecture, &labels, &caps, &n.Allocatable.CPUMillis, &n.Allocatable.MemoryBytes, &availability, &n.HeartbeatAt, &gpus, &thermal, &cachedImages); err != nil {
			return nil, err
		}
		n.Availability = model.Availability(availability)
		n.Labels = map[string]string{}
		_ = json.Unmarshal(labels, &n.Labels)
		capBools := map[string]bool{}
		_ = json.Unmarshal(caps, &capBools)
		n.Capabilities = capBools
		_ = json.Unmarshal(gpus, &n.GPUs)
		_ = json.Unmarshal(thermal, &n.Thermal)
		_ = json.Unmarshal(cachedImages, &n.CachedImages)
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// ActiveReservations sums the resources and host ports held by every
// non-terminal workload per node, so the scheduler can be rebuilt from
// the database without a separate reservation table.
func (s *Store) ActiveReservations(ctx context.Context) (map[string]model.Resources, map[string][]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT node_id, cpu_millis, memory_bytes, host_ports FROM workloads
		WHERE node_id != '' AND status NOT IN ('failed', 'stopped')
	`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	cpuMem := map[string]model.Resources{}
	ports := map[string][]int{}
	for rows.Next() {
		var nodeID string
		var res model.Resources
		var hostPortsJSON []byte
		if err := rows.Scan(&nodeID, &res.CPUMillis, &res.MemoryBytes, &hostPortsJSON); err != nil {
			return nil, nil, err
		}
		acc := cpuMem[nodeID]
		acc.CPUMillis += res.CPUMillis
		acc.MemoryBytes += res.MemoryBytes
		cpuMem[nodeID] = acc

		var p []int
		_ = json.Unmarshal(hostPortsJSON, &p)
		ports[nodeID] = append(ports[nodeID], p...)
	}
	return cpuMem, ports, rows.Err()
}

// WorkloadRecord is a workload plus its control-plane–assigned runtime
// fields.
type WorkloadRecord struct {
	model.Workload
	Status       model.Status
	StatusReason string
	NodeID       string
	ContainerID  string
}

// CreateWorkload inserts a newly submitted, scheduled workload.
func (s *Store) CreateWorkload(ctx context.Context, w WorkloadRecord) error {
	command, _ := json.Marshal(w.Command)
	env, _ := json.Marshal(w.Env)
	hostPorts, _ := json.Marshal(w.Constraints.HostPorts)
	containerPorts, _ := json.Marshal(w.ContainerPorts)
	volumes, _ := json.Marshal(w.Volumes)
	dependsOn, _ := json.Marshal(w.DependsOn)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO workloads (key, project, service, revision, image, command, env, cpu_millis, memory_bytes, architecture, host_ports, container_ports, storage_class, storage_pinned_node, volumes, depends_on, desired_state, status, status_reason, node_id, container_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
	`, w.Key(), w.Project, w.Service, w.Revision, w.Image, command, env, w.Resources.CPUMillis, w.Resources.MemoryBytes, w.Constraints.Architecture, hostPorts, containerPorts, string(w.Storage.Class), w.Storage.PinnedNodeID, volumes, dependsOn, string(w.DesiredState), string(w.Status), w.StatusReason, w.NodeID, w.ContainerID)
	return err
}

// UpdateStatus transitions a workload's observed status (SRS 12.1
// "Runtime state"; FR-008, FR-009).
func (s *Store) UpdateStatus(ctx context.Context, key string, status model.Status, reason, containerID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE workloads SET status = $2, status_reason = $3, container_id = COALESCE(NULLIF($4, ''), container_id), updated_at = now()
		WHERE key = $1
	`, key, string(status), reason, containerID)
	if err == nil {
		_, _ = s.pool.Exec(ctx, `INSERT INTO events (workload_key, type, message) VALUES ($1, 'status', $2)`, key, string(status)+": "+reason)
	}
	return err
}

// GetWorkload returns one workload by key.
func (s *Store) GetWorkload(ctx context.Context, key string) (*WorkloadRecord, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT project, service, revision, image, command, env, cpu_millis, memory_bytes, architecture, host_ports, container_ports, storage_class, storage_pinned_node, volumes, depends_on, desired_state, status, status_reason, node_id, container_id
		FROM workloads WHERE key = $1
	`, key)
	return scanWorkload(row)
}

// AssignedWorkloads returns workloads assigned to a node that the agent
// has not yet finished bringing to a terminal running/ready/failed state
// — this is the agent's pull-based assignment feed (SRS 6.2).
func (s *Store) AssignedWorkloads(ctx context.Context, nodeID string) ([]WorkloadRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT project, service, revision, image, command, env, cpu_millis, memory_bytes, architecture, host_ports, container_ports, storage_class, storage_pinned_node, volumes, depends_on, desired_state, status, status_reason, node_id, container_id
		FROM workloads WHERE node_id = $1 AND status NOT IN ('failed', 'stopped')
	`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WorkloadRecord
	for rows.Next() {
		rec, err := scanWorkloadRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

// ListReadyServiceInstances returns every ready instance of a logical
// service, derived live from workloads joined to their node's address
// (SRS 8.1-8.2: "The registry maps service identity to one or more actual
// endpoints"). There is no separate registry table — a ready workload row
// plus its node's address *is* the registry entry. Only the internal
// container port is returned: DNS-based service discovery (M7) resolves to
// this port regardless of whether the workload also publishes a host port.
func (s *Store) ListReadyServiceInstances(ctx context.Context, project, service string) ([]model.ServiceInstance, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT w.node_id, n.address, w.container_ports, w.status
		FROM workloads w
		JOIN nodes n ON n.id = w.node_id
		WHERE w.project = $1 AND w.service = $2 AND w.status IN ('running', 'ready')
	`, project, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.ServiceInstance
	for rows.Next() {
		var nodeID, address, status string
		var containerPortsJSON []byte
		if err := rows.Scan(&nodeID, &address, &containerPortsJSON, &status); err != nil {
			return nil, err
		}
		var ports []int
		_ = json.Unmarshal(containerPortsJSON, &ports)
		if len(ports) == 0 {
			continue // no internal port declared; nothing for a dependent to connect to
		}
		out = append(out, model.ServiceInstance{
			Project:       project,
			Service:       service,
			NodeID:        nodeID,
			Address:       address,
			ContainerPort: ports[0],
			Status:        model.Status(status),
		})
	}
	return out, rows.Err()
}

type scannable interface {
	Scan(dest ...any) error
}

func scanWorkload(row scannable) (*WorkloadRecord, error) {
	return scanWorkloadRow(row)
}

func scanWorkloadRow(row scannable) (*WorkloadRecord, error) {
	var rec WorkloadRecord
	var command, env, hostPorts, containerPorts, volumes, dependsOn []byte
	var storageClass, status string
	if err := row.Scan(&rec.Project, &rec.Service, &rec.Revision, &rec.Image, &command, &env, &rec.Resources.CPUMillis, &rec.Resources.MemoryBytes, &rec.Constraints.Architecture, &hostPorts, &containerPorts, &storageClass, &rec.Storage.PinnedNodeID, &volumes, &dependsOn, (*string)(&rec.DesiredState), &status, &rec.StatusReason, &rec.NodeID, &rec.ContainerID); err != nil {
		return nil, err
	}
	rec.Status = model.Status(status)
	rec.Storage.Class = model.StorageClass(storageClass)
	rec.Env = map[string]string{}
	_ = json.Unmarshal(command, &rec.Command)
	_ = json.Unmarshal(env, &rec.Env)
	_ = json.Unmarshal(hostPorts, &rec.Constraints.HostPorts)
	_ = json.Unmarshal(containerPorts, &rec.ContainerPorts)
	_ = json.Unmarshal(volumes, &rec.Volumes)
	_ = json.Unmarshal(dependsOn, &rec.DependsOn)
	return &rec, nil
}
