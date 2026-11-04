// Package store persists control-plane state in an embedded SQLite
// database (SRS 5.1 "Metadata Store", NFR-001: recover desired state after
// restart). Only the control plane ever touches it — a single process, no
// concurrent writers — so Postgres's main strength (multi-writer
// concurrency) was never actually exercised here; SQLite removes the
// separate-database bootstrap step entirely. M1's in-memory scheduler
// state has no equivalent here; reservations are derived by summing
// non-terminal workloads per node, not stored separately, so there is one
// source of truth.
package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// ErrNotFound is returned (wrapped) by any lookup that found nothing,
// instead of the caller needing to know which SQL driver's not-found
// sentinel to check for.
var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS nodes (
	id            text PRIMARY KEY,
	address       text NOT NULL DEFAULT '',
	architecture  text NOT NULL,
	labels        text NOT NULL DEFAULT '{}',
	capabilities  text NOT NULL DEFAULT '{}',
	cpu_millis    integer NOT NULL,
	memory_bytes  integer NOT NULL,
	availability  text NOT NULL,
	disabled_by_admin integer NOT NULL DEFAULT 0,
	heartbeat_at  text NOT NULL,
	gpus          text NOT NULL DEFAULT '[]',
	thermal       text NOT NULL DEFAULT '{"Known": false}',
	cached_images text NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS workloads (
	key                  text PRIMARY KEY,
	project              text NOT NULL,
	service              text NOT NULL,
	revision             text NOT NULL,
	image                text NOT NULL,
	command              text NOT NULL DEFAULT '[]',
	env                  text NOT NULL DEFAULT '{}',
	cpu_millis           integer NOT NULL,
	memory_bytes         integer NOT NULL,
	architecture         text NOT NULL DEFAULT '',
	host_ports           text NOT NULL DEFAULT '[]',
	container_ports      text NOT NULL DEFAULT '[]',
	port_bindings        text NOT NULL DEFAULT '[]',
	storage_class        text NOT NULL,
	storage_pinned_node  text NOT NULL DEFAULT '',
	storage_allow_fresh_volume integer NOT NULL DEFAULT 0,
	constraint_pinned_node text NOT NULL DEFAULT '',
	skip_pull            integer NOT NULL DEFAULT 0,
	remote_build         text NOT NULL DEFAULT '',
	volumes              text NOT NULL DEFAULT '[]',
	depends_on           text NOT NULL DEFAULT '[]',
	depends_on_healthy   text NOT NULL DEFAULT '[]',
	health_check         text NOT NULL DEFAULT '',
	desired_state        text NOT NULL,
	status               text NOT NULL,
	status_reason        text NOT NULL DEFAULT '',
	node_id              text NOT NULL DEFAULT '',
	container_id         text NOT NULL DEFAULT '',
	created_at           text NOT NULL,
	updated_at           text NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
	id           integer PRIMARY KEY AUTOINCREMENT,
	workload_key text NOT NULL,
	node_id      text NOT NULL DEFAULT '',
	type         text NOT NULL,
	message      text NOT NULL,
	created_at   text NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_join_tokens (
	secret_hash text PRIMARY KEY,
	expires_at  text NOT NULL,
	consumed_at text
);

CREATE TABLE IF NOT EXISTS agent_credentials (
	node_id     text PRIMARY KEY,
	secret_hash text NOT NULL,
	revoked_at  text
);
`

// Store wraps an embedded SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and ensures
// the schema exists. path may be ":memory:" for tests.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single connection sidesteps SQLite's single-writer model entirely —
	// this control plane is low-throughput enough that serializing every
	// call costs nothing, and it avoids "database is locked" errors that
	// busy_timeout alone doesn't fully eliminate under concurrent access.
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure sqlite: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	// Additive migration for databases created before this column existed;
	// SQLite has no "ADD COLUMN IF NOT EXISTS", so the error from a column
	// that's already there is expected and ignored.
	_, _ = db.ExecContext(ctx, `ALTER TABLE workloads ADD COLUMN storage_allow_fresh_volume integer NOT NULL DEFAULT 0`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE workloads ADD COLUMN constraint_pinned_node text NOT NULL DEFAULT ''`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE workloads ADD COLUMN skip_pull integer NOT NULL DEFAULT 0`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE workloads ADD COLUMN remote_build text NOT NULL DEFAULT ''`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE workloads ADD COLUMN depends_on_healthy text NOT NULL DEFAULT '[]'`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE workloads ADD COLUMN health_check text NOT NULL DEFAULT ''`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE workloads ADD COLUMN port_bindings text NOT NULL DEFAULT '[]'`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE nodes ADD COLUMN disabled_by_admin integer NOT NULL DEFAULT 0`)
	return &Store{db: db}, nil
}

// Close releases the database connection.
func (s *Store) Close() error { return s.db.Close() }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

// likeEscape escapes SQL LIKE's wildcard characters in a value that will
// be used as a literal prefix (a project name), so a project named e.g.
// "foo%bar" or "a_b" can't accidentally match more than itself.
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func (s *Store) CreateJoinToken(ctx context.Context, secretHash string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_join_tokens (secret_hash, expires_at) VALUES (?, ?)`, secretHash, formatTime(expiresAt))
	return err
}

// ConsumeJoinToken atomically accepts a live token at most once.
func (s *Store) ConsumeJoinToken(ctx context.Context, secretHash string, now time.Time) (bool, error) {
	var consumed string
	nowStr := formatTime(now)
	err := s.db.QueryRowContext(ctx, `UPDATE agent_join_tokens SET consumed_at = ? WHERE secret_hash = ? AND consumed_at IS NULL AND expires_at > ? RETURNING secret_hash`, nowStr, secretHash, nowStr).Scan(&consumed)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Store) CreateAgentCredential(ctx context.Context, nodeID, secretHash string) error {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_credentials (node_id, secret_hash, revoked_at) VALUES (?, ?, NULL)
		ON CONFLICT (node_id) DO UPDATE SET secret_hash = excluded.secret_hash, revoked_at = NULL WHERE agent_credentials.revoked_at IS NOT NULL
	`, nodeID, secretHash)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("node %q already has an active agent credential; revoke it before joining again", nodeID)
	}
	return nil
}

func (s *Store) VerifyAgentCredential(ctx context.Context, nodeID, secretHash string) (bool, error) {
	var found string
	err := s.db.QueryRowContext(ctx, `SELECT secret_hash FROM agent_credentials WHERE node_id = ? AND revoked_at IS NULL`, nodeID).Scan(&found)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return subtle.ConstantTimeCompare([]byte(found), []byte(secretHash)) == 1, nil
}

func (s *Store) RevokeAgentCredential(ctx context.Context, nodeID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_credentials SET revoked_at = ? WHERE node_id = ?`, formatTime(time.Now()), nodeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("no agent credential for node %q", nodeID)
	}
	return nil
}

// DeleteNode hard-removes a node and its agent credential, unlike
// RevokeAgentCredential which only blocks future auth and leaves the row
// (so `hacoctl nodes` keeps showing it). No table references node_id as a
// foreign key, so this is safe without touching workload/event history —
// those rows just keep whatever node_id they already recorded.
func (s *Store) DeleteNode(ctx context.Context, nodeID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, nodeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("no node %q", nodeID)
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM agent_credentials WHERE node_id = ?`, nodeID)
	return nil
}

// UpsertNode registers a node or refreshes its heartbeat/capacity,
// including hardware telemetry (SRS §9.2, M7).
func (s *Store) UpsertNode(ctx context.Context, n model.Node) error {
	labels, _ := json.Marshal(n.Labels)
	caps, _ := json.Marshal(n.Capabilities)
	gpus, _ := json.Marshal(n.GPUs)
	thermal, _ := json.Marshal(n.Thermal)
	cachedImages, _ := json.Marshal(n.CachedImages)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO nodes (id, address, architecture, labels, capabilities, cpu_millis, memory_bytes, availability, heartbeat_at, gpus, thermal, cached_images)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (id) DO UPDATE SET
			address = excluded.address,
			architecture = excluded.architecture,
			labels = excluded.labels,
			capabilities = excluded.capabilities,
			cpu_millis = excluded.cpu_millis,
			memory_bytes = excluded.memory_bytes,
			-- A re-registering agent (e.g. restarted) always proposes
			-- Available; a cordoned node must stay cordoned until an
			-- explicit uncordon, not silently un-cordon on re-join.
			availability = CASE WHEN disabled_by_admin = 1 THEN availability ELSE excluded.availability END,
			heartbeat_at = excluded.heartbeat_at,
			gpus = excluded.gpus,
			thermal = excluded.thermal,
			cached_images = excluded.cached_images
	`, n.ID, n.Address, n.Architecture, labels, caps, n.Allocatable.CPUMillis, n.Allocatable.MemoryBytes, string(n.Availability), formatTime(n.HeartbeatAt), gpus, thermal, cachedImages)
	return err
}

// Heartbeat updates a node's last-seen timestamp and refreshes its
// telemetry snapshot (SRS 6.2 "send heartbeat/resource snapshot").
func (s *Store) Heartbeat(ctx context.Context, nodeID string, thermal model.Thermal, cachedImages []string) error {
	thermalJSON, _ := json.Marshal(thermal)
	cachedJSON, _ := json.Marshal(cachedImages)
	res, err := s.db.ExecContext(ctx, `
		UPDATE nodes SET heartbeat_at = ?, thermal = ?, cached_images = ?,
			availability = CASE WHEN disabled_by_admin = 1 THEN availability ELSE 'available' END
		WHERE id = ?
	`, formatTime(time.Now()), thermalJSON, cachedJSON, nodeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("unknown node %q", nodeID)
	}
	return nil
}

// CordonNode marks a node disabled-by-admin: Availability flips to
// Disabled (the scheduler's existing "!= Available" filter already
// excludes it from every new placement) and disabled_by_admin=1 so
// neither Heartbeat nor a re-registering agent can silently undo it.
// Existing workloads already running there are left alone — cordon blocks
// new scheduling, it does not evict (drain is a separate, explicit act:
// stop or redeploy those workloads elsewhere yourself).
func (s *Store) CordonNode(ctx context.Context, nodeID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE nodes SET availability = 'disabled', disabled_by_admin = 1 WHERE id = ?`, nodeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("unknown node %q", nodeID)
	}
	return nil
}

// UncordonNode clears disabled_by_admin and optimistically marks the node
// Available again; if its heartbeat is actually stale, the next reconcile
// tick's MarkStaleNodesUnavailable corrects that within one interval.
func (s *Store) UncordonNode(ctx context.Context, nodeID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE nodes SET availability = 'available', disabled_by_admin = 0 WHERE id = ?`, nodeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("unknown node %q", nodeID)
	}
	return nil
}

// MarkStaleNodesUnavailable excludes nodes from new placements once their
// heartbeat is older than timeout (SRS FR-015).
func (s *Store) MarkStaleNodesUnavailable(ctx context.Context, timeout time.Duration) error {
	cutoff := formatTime(time.Now().Add(-timeout))
	_, err := s.db.ExecContext(ctx, `
		UPDATE nodes SET availability = 'unavailable'
		WHERE availability = 'available' AND heartbeat_at < ?
	`, cutoff)
	return err
}

// WorkloadsOnUnavailableNodes returns every non-terminal workload currently
// assigned to a node the heartbeat timeout has marked unavailable — the
// reconciler's input for deciding what to reschedule or fail (SRS §6.3,
// FR-015).
func (s *Store) WorkloadsOnUnavailableNodes(ctx context.Context) ([]WorkloadRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.project, w.service, w.revision, w.image, w.command, w.env, w.cpu_millis, w.memory_bytes, w.architecture, w.host_ports, w.container_ports, w.port_bindings, w.storage_class, w.storage_pinned_node, w.storage_allow_fresh_volume, w.constraint_pinned_node, w.skip_pull, w.remote_build, w.volumes, w.depends_on, w.depends_on_healthy, w.health_check, w.desired_state, w.status, w.status_reason, w.node_id, w.container_id
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
	now := formatTime(time.Now())
	_, err := s.db.ExecContext(ctx, `
		UPDATE workloads SET node_id = ?, status = 'assigned', status_reason = '', container_id = '', updated_at = ?
		WHERE key = ?
	`, newNodeID, now, key)
	if err == nil {
		_, _ = s.db.ExecContext(ctx, `INSERT INTO events (workload_key, node_id, type, message, created_at) VALUES (?, ?, 'reassigned', 'rescheduled after node loss', ?)`, key, newNodeID, now)
	}
	return err
}

// ReassignWithFreshVolume is Reassign for a pinned workload that opted
// into FR-017's waiver (model.StoragePolicy.AllowFreshVolumeOnReschedule):
// it also moves the storage pin itself to the new node, since the new
// node starts a brand-new, empty volume rather than the old one's data.
func (s *Store) ReassignWithFreshVolume(ctx context.Context, key, newNodeID string) error {
	now := formatTime(time.Now())
	_, err := s.db.ExecContext(ctx, `
		UPDATE workloads SET node_id = ?, storage_pinned_node = ?, status = 'assigned', status_reason = '', container_id = '', updated_at = ?
		WHERE key = ?
	`, newNodeID, newNodeID, now, key)
	if err == nil {
		_, _ = s.db.ExecContext(ctx, `INSERT INTO events (workload_key, node_id, type, message, created_at) VALUES (?, ?, 'reassigned', 'rescheduled onto a fresh volume after pinned node loss (previous data not migrated)', ?)`, key, newNodeID, now)
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

// ListEvents returns the most recent events, newest first (SRS §13,
// FR-024), optionally filtered to one project's workloads (workload_key is
// "project/service/revision", so this matches the "project/" prefix).
func (s *Store) ListEvents(ctx context.Context, limit int, project string) ([]Event, error) {
	query := `SELECT id, workload_key, node_id, type, message, created_at FROM events`
	var args []any
	if project != "" {
		query += ` WHERE workload_key LIKE ? ESCAPE '\'`
		args = append(args, likeEscape(project)+`/%`)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var createdAt string
		if err := rows.Scan(&e.ID, &e.WorkloadKey, &e.NodeID, &e.Type, &e.Message, &createdAt); err != nil {
			return nil, err
		}
		e.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AppendWorkloadLogs persists a bounded container log tail as a workload event.
func (s *Store) AppendWorkloadLogs(ctx context.Context, key, logs string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO events (workload_key, type, message, created_at) VALUES (?, 'container_logs', ?, ?)
	`, key, logs, formatTime(time.Now()))
	return err
}

// GetWorkloadLogs returns the most recently captured container log tail.
func (s *Store) GetWorkloadLogs(ctx context.Context, key string) (string, error) {
	var logs string
	err := s.db.QueryRowContext(ctx, `
		SELECT message FROM events
		WHERE workload_key = ? AND type = 'container_logs'
		ORDER BY id DESC LIMIT 1
	`, key).Scan(&logs)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: no container logs for workload %q", ErrNotFound, key)
	}
	return logs, err
}

// ProjectSummary is one distinct project known to the control plane (any
// project a workload has ever been submitted under), with a per-status
// workload-count breakdown.
type ProjectSummary struct {
	Project  string
	Total    int
	ByStatus map[string]int
}

// ListProjects lists every distinct project with a workload row, newest
// shown first isn't tracked (no created_at aggregate here) so this is
// alphabetical — there's no dedicated projects table; a project only
// exists implicitly as whatever workloads were deployed under that name.
func (s *Store) ListProjects(ctx context.Context) ([]ProjectSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project, status, count(*) FROM workloads GROUP BY project, status ORDER BY project
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var order []string
	byProject := map[string]*ProjectSummary{}
	for rows.Next() {
		var project, status string
		var count int
		if err := rows.Scan(&project, &status, &count); err != nil {
			return nil, err
		}
		ps, ok := byProject[project]
		if !ok {
			ps = &ProjectSummary{Project: project, ByStatus: map[string]int{}}
			byProject[project] = ps
			order = append(order, project)
		}
		ps.ByStatus[status] = count
		ps.Total += count
	}
	out := make([]ProjectSummary, 0, len(order))
	for _, p := range order {
		out = append(out, *byProject[p])
	}
	return out, rows.Err()
}

// Metrics is a minimal operational summary (SRS §13 "required visibility"),
// a JSON endpoint rather than a Prometheus exporter — see M6 doc for why.
type Metrics struct {
	NodesByAvailability map[string]int `json:"nodes_by_availability"`
	WorkloadsByStatus   map[string]int `json:"workloads_by_status"`
}

// Metrics reports node availability (always global — nodes aren't owned by
// one project) and workload-status counts, optionally scoped to one
// project's workloads (same "project/" workload_key prefix match as
// ListEvents).
func (s *Store) Metrics(ctx context.Context, project string) (*Metrics, error) {
	m := &Metrics{NodesByAvailability: map[string]int{}, WorkloadsByStatus: map[string]int{}}

	rows, err := s.db.QueryContext(ctx, `SELECT availability, count(*) FROM nodes GROUP BY availability`)
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

	query := `SELECT status, count(*) FROM workloads`
	var args []any
	if project != "" {
		query += ` WHERE key LIKE ? ESCAPE '\'`
		args = append(args, likeEscape(project)+`/%`)
	}
	query += ` GROUP BY status`
	rows, err = s.db.QueryContext(ctx, query, args...)
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, address, architecture, labels, capabilities, cpu_millis, memory_bytes, availability, disabled_by_admin, heartbeat_at, gpus, thermal, cached_images FROM nodes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []model.Node
	for rows.Next() {
		var n model.Node
		var labels, caps, gpus, thermal, cachedImages []byte
		var availability, heartbeatAt string
		if err := rows.Scan(&n.ID, &n.Address, &n.Architecture, &labels, &caps, &n.Allocatable.CPUMillis, &n.Allocatable.MemoryBytes, &availability, &n.DisabledByAdmin, &heartbeatAt, &gpus, &thermal, &cachedImages); err != nil {
			return nil, err
		}
		n.Availability = model.Availability(availability)
		n.HeartbeatAt, err = parseTime(heartbeatAt)
		if err != nil {
			return nil, err
		}
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
// excludeKey is typically the workload about to be (re)scheduled: a
// redeploy must not see its own prior attempt's reservation as taken,
// since that attempt is about to be replaced, not added to (see
// CreateWorkload's ON CONFLICT). Pass "" to exclude nothing.
func (s *Store) ActiveReservations(ctx context.Context, excludeKey string) (map[string]model.Resources, map[string][]int, error) {
	return s.ActiveReservationsExcluding(ctx, []string{excludeKey})
}

// ActiveReservationsExcluding is ActiveReservations generalized to a whole
// batch of keys at once — used when dry-run planning several workloads
// together, so each one's own existing (pre-replan) reservation doesn't
// double-count against the very placement being recomputed for it.
func (s *Store) ActiveReservationsExcluding(ctx context.Context, excludeKeys []string) (map[string]model.Resources, map[string][]int, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(excludeKeys)), ",")
	args := make([]any, len(excludeKeys))
	for i, k := range excludeKeys {
		args[i] = k
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT node_id, cpu_millis, memory_bytes, host_ports FROM workloads
		WHERE node_id != '' AND status NOT IN ('failed', 'stopped') AND key NOT IN (`+placeholders+`)
	`, args...)
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
	portBindings, _ := json.Marshal(w.PortBindings)
	volumes, _ := json.Marshal(w.Volumes)
	dependsOn, _ := json.Marshal(w.DependsOn)
	dependsOnHealthy, _ := json.Marshal(w.DependsOnHealthy)
	var remoteBuild string
	if w.RemoteBuild != nil {
		b, _ := json.Marshal(w.RemoteBuild)
		remoteBuild = string(b)
	}
	var healthCheck string
	if w.HealthCheck != nil {
		b, _ := json.Marshal(w.HealthCheck)
		healthCheck = string(b)
	}
	now := formatTime(time.Now())
	// ON CONFLICT: redeploying the same project/service/revision (the
	// common case — rerunning `hacoctl deploy`, or retrying after a
	// previous attempt got stuck) must replace that workload's spec and
	// restart its lifecycle, not fail with a duplicate-key error or leave
	// stale fields behind. container_id is reset so the agent doesn't
	// mistake a new spec for an already-running old container.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workloads (key, project, service, revision, image, command, env, cpu_millis, memory_bytes, architecture, host_ports, container_ports, port_bindings, storage_class, storage_pinned_node, storage_allow_fresh_volume, constraint_pinned_node, skip_pull, remote_build, volumes, depends_on, depends_on_healthy, health_check, desired_state, status, status_reason, node_id, container_id, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (key) DO UPDATE SET
			image = excluded.image,
			command = excluded.command,
			env = excluded.env,
			cpu_millis = excluded.cpu_millis,
			memory_bytes = excluded.memory_bytes,
			architecture = excluded.architecture,
			host_ports = excluded.host_ports,
			container_ports = excluded.container_ports,
			port_bindings = excluded.port_bindings,
			storage_class = excluded.storage_class,
			storage_pinned_node = excluded.storage_pinned_node,
			storage_allow_fresh_volume = excluded.storage_allow_fresh_volume,
			constraint_pinned_node = excluded.constraint_pinned_node,
			skip_pull = excluded.skip_pull,
			remote_build = excluded.remote_build,
			volumes = excluded.volumes,
			depends_on = excluded.depends_on,
			depends_on_healthy = excluded.depends_on_healthy,
			health_check = excluded.health_check,
			desired_state = excluded.desired_state,
			status = excluded.status,
			status_reason = excluded.status_reason,
			node_id = excluded.node_id,
			container_id = '',
			updated_at = excluded.updated_at
	`, w.Key(), w.Project, w.Service, w.Revision, w.Image, command, env, w.Resources.CPUMillis, w.Resources.MemoryBytes, w.Constraints.Architecture, hostPorts, containerPorts, portBindings, string(w.Storage.Class), w.Storage.PinnedNodeID, w.Storage.AllowFreshVolumeOnReschedule, w.Constraints.PinnedNodeID, w.SkipPull, remoteBuild, volumes, dependsOn, dependsOnHealthy, healthCheck, string(w.DesiredState), string(w.Status), w.StatusReason, w.NodeID, w.ContainerID, now, now)
	return err
}

// StopWorkload records an admin-requested stop: desired_state and status
// both flip to "stopped", which drops the workload from AssignedWorkloads
// (status <> 'stopped') so the next agent poll no longer sees it assigned —
// the existing orphan-cleanup path then stops and removes its container.
func (s *Store) StopWorkload(ctx context.Context, key string) error {
	now := formatTime(time.Now())
	res, err := s.db.ExecContext(ctx, `
		UPDATE workloads SET desired_state = ?, status = ?, status_reason = '', updated_at = ?
		WHERE key = ?
	`, string(model.DesiredStopped), string(model.StatusStopped), now, key)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: workload %q", ErrNotFound, key)
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO events (workload_key, type, message, created_at) VALUES (?, 'status', ?, ?)`, key, "stopped: requested by operator", now)
	return nil
}

// DeleteWorkload hard-removes a workload record entirely — distinct from
// StopWorkload, which leaves a historical "stopped" row behind. If the
// workload was still actively assigned to a node, its container isn't
// orphaned forever the way a deleted node's workloads are
// (store.DeleteNode's whole concern): the owning agent's own
// AssignedWorkloads poll simply stops finding this key in its assignment
// list and its existing cleanupOrphans path stops and removes the
// container on its next poll, the same as any other no-longer-assigned
// container.
func (s *Store) DeleteWorkload(ctx context.Context, key string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workloads WHERE key = ?`, key)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: workload %q", ErrNotFound, key)
	}
	return nil
}

// DeleteWorkloads hard-removes every workload record, or (project != "")
// just those under that project, returning how many were deleted.
func (s *Store) DeleteWorkloads(ctx context.Context, project string) (int64, error) {
	query := `DELETE FROM workloads`
	var args []any
	if project != "" {
		query += ` WHERE key LIKE ? ESCAPE '\'`
		args = append(args, likeEscape(project)+`/%`)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UpdateStatus transitions a workload's observed status (SRS 12.1
// "Runtime state"; FR-008, FR-009).
func (s *Store) UpdateStatus(ctx context.Context, key string, status model.Status, reason, containerID string) error {
	now := formatTime(time.Now())
	_, err := s.db.ExecContext(ctx, `
		UPDATE workloads SET status = ?, status_reason = ?, container_id = COALESCE(NULLIF(?, ''), container_id), updated_at = ?
		WHERE key = ?
	`, string(status), reason, containerID, now, key)
	if err == nil {
		_, _ = s.db.ExecContext(ctx, `INSERT INTO events (workload_key, type, message, created_at) VALUES (?, 'status', ?, ?)`, key, string(status)+": "+reason, now)
	}
	return err
}

// GetWorkload returns one workload by key.
func (s *Store) GetWorkload(ctx context.Context, key string) (*WorkloadRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT project, service, revision, image, command, env, cpu_millis, memory_bytes, architecture, host_ports, container_ports, port_bindings, storage_class, storage_pinned_node, storage_allow_fresh_volume, constraint_pinned_node, skip_pull, remote_build, volumes, depends_on, depends_on_healthy, health_check, desired_state, status, status_reason, node_id, container_id
		FROM workloads WHERE key = ?
	`, key)
	rec, err := scanWorkload(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: workload %q", ErrNotFound, key)
	}
	return rec, err
}

// ListWorkloads returns every workload, optionally filtered to one
// project (empty string means all projects), ordered for stable display.
func (s *Store) ListWorkloads(ctx context.Context, project string) ([]WorkloadRecord, error) {
	query := `
		SELECT project, service, revision, image, command, env, cpu_millis, memory_bytes, architecture, host_ports, container_ports, port_bindings, storage_class, storage_pinned_node, storage_allow_fresh_volume, constraint_pinned_node, skip_pull, remote_build, volumes, depends_on, depends_on_healthy, health_check, desired_state, status, status_reason, node_id, container_id
		FROM workloads
	`
	var args []any
	if project != "" {
		query += " WHERE project = ?"
		args = append(args, project)
	}
	query += " ORDER BY project, service, revision"

	rows, err := s.db.QueryContext(ctx, query, args...)
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

// AssignedWorkloads returns workloads assigned to a node, including failed
// workloads so the agent can retain their containers for log inspection.
// Stopped workloads are excluded because their containers should be cleaned
// up. This is the agent's pull-based assignment feed (SRS 6.2).
func (s *Store) AssignedWorkloads(ctx context.Context, nodeID string) ([]WorkloadRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project, service, revision, image, command, env, cpu_millis, memory_bytes, architecture, host_ports, container_ports, port_bindings, storage_class, storage_pinned_node, storage_allow_fresh_volume, constraint_pinned_node, skip_pull, remote_build, volumes, depends_on, depends_on_healthy, health_check, desired_state, status, status_reason, node_id, container_id
		FROM workloads WHERE node_id = ? AND status <> 'stopped'
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
// container port is returned: service discovery resolves to this port
// regardless of whether the workload also publishes a host port.
func (s *Store) ListReadyServiceInstances(ctx context.Context, project, service string) ([]model.ServiceInstance, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.node_id, n.address, w.container_ports, w.status
		FROM workloads w
		JOIN nodes n ON n.id = w.node_id
		WHERE w.project = ? AND w.service = ? AND w.status IN ('running', 'ready')
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

// ListProjectServiceTargets returns reachable service targets for workloads
// assigned to a node in a project. Unlike ListReadyServiceInstances, it
// includes assigned/starting workloads so containers can get names for all
// Compose services at creation time, independent of depends_on.
func (s *Store) ListProjectServiceTargets(ctx context.Context, project string) ([]model.ServiceInstance, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.service, w.node_id, n.address, w.container_ports, w.status
		FROM workloads w
		JOIN nodes n ON n.id = w.node_id
		WHERE w.project = ? AND w.desired_state = 'running'
		  AND w.node_id IS NOT NULL AND w.node_id != '' AND w.status NOT IN ('failed', 'stopped')
		ORDER BY w.service,
		         CASE WHEN w.status IN ('running', 'ready') THEN 0 ELSE 1 END,
		         w.node_id
	`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.ServiceInstance
	for rows.Next() {
		var service, nodeID, address, status string
		var containerPortsJSON []byte
		if err := rows.Scan(&service, &nodeID, &address, &containerPortsJSON, &status); err != nil {
			return nil, err
		}
		var ports []int
		_ = json.Unmarshal(containerPortsJSON, &ports)
		if len(ports) == 0 {
			continue
		}
		out = append(out, model.ServiceInstance{
			Project: project, Service: service, NodeID: nodeID,
			Address: address, ContainerPort: ports[0], Status: model.Status(status),
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
	var command, env, hostPorts, containerPorts, portBindings, volumes, dependsOn, dependsOnHealthy []byte
	var storageClass, status, remoteBuild, healthCheck string
	if err := row.Scan(&rec.Project, &rec.Service, &rec.Revision, &rec.Image, &command, &env, &rec.Resources.CPUMillis, &rec.Resources.MemoryBytes, &rec.Constraints.Architecture, &hostPorts, &containerPorts, &portBindings, &storageClass, &rec.Storage.PinnedNodeID, &rec.Storage.AllowFreshVolumeOnReschedule, &rec.Constraints.PinnedNodeID, &rec.SkipPull, &remoteBuild, &volumes, &dependsOn, &dependsOnHealthy, &healthCheck, (*string)(&rec.DesiredState), &status, &rec.StatusReason, &rec.NodeID, &rec.ContainerID); err != nil {
		return nil, err
	}
	rec.Status = model.Status(status)
	rec.Storage.Class = model.StorageClass(storageClass)
	rec.Env = map[string]string{}
	_ = json.Unmarshal(command, &rec.Command)
	_ = json.Unmarshal(env, &rec.Env)
	_ = json.Unmarshal(hostPorts, &rec.Constraints.HostPorts)
	_ = json.Unmarshal(containerPorts, &rec.ContainerPorts)
	_ = json.Unmarshal(portBindings, &rec.PortBindings)
	_ = json.Unmarshal(volumes, &rec.Volumes)
	_ = json.Unmarshal(dependsOn, &rec.DependsOn)
	_ = json.Unmarshal(dependsOnHealthy, &rec.DependsOnHealthy)
	if remoteBuild != "" {
		rec.RemoteBuild = &model.RemoteBuildSpec{}
		_ = json.Unmarshal([]byte(remoteBuild), rec.RemoteBuild)
	}
	if healthCheck != "" {
		rec.HealthCheck = &model.HealthCheck{}
		_ = json.Unmarshal([]byte(healthCheck), rec.HealthCheck)
	}
	return &rec, nil
}
