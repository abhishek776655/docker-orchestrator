// Package model defines the core scheduling types shared by the simulator
// and (later) the real scheduler: nodes, workloads, resources, and the
// recorded outcome of a placement attempt.
package model

import "time"

// Availability is the node's eligibility for new placements.
type Availability string

const (
	Available   Availability = "available"
	Unavailable Availability = "unavailable"
	Disabled    Availability = "disabled"
)

// StorageClass mirrors the spec's storage classes (see SRS 10.1). Only
// Ephemeral and NodeLocalPersistent are relevant to the M1 simulator.
type StorageClass string

const (
	Ephemeral           StorageClass = "ephemeral"
	NodeLocalPersistent StorageClass = "node_local_persistent"
)

// Resources is a CPU/memory request pair. Values are in millicores and
// bytes so they stay integral and comparable.
type Resources struct {
	CPUMillis   int64
	MemoryBytes int64
}

// Node is a scheduling candidate. Allocatable is capacity already reduced
// by system-reserved headroom (SRS 9.2) — the scheduler never re-derives
// that reservation itself.
type Node struct {
	ID           string
	Address      string
	Architecture string
	Labels       map[string]string
	Capabilities map[string]bool
	Allocatable  Resources
	Availability Availability
	HeartbeatAt  time.Time
}

// StoragePolicy pins a workload to a node when it owns node-local
// persistent state (SRS 10.2): the scheduler must never place such a
// workload anywhere but PinnedNodeID.
type StoragePolicy struct {
	Class        StorageClass
	PinnedNodeID string // required when Class == NodeLocalPersistent
}

// Constraints are the hard eligibility requirements for a workload
// (SRS 9.1 step 2). Any node failing one of these is rejected before
// scoring, regardless of score.
type Constraints struct {
	Architecture         string
	RequiredLabels       map[string]string
	RequiredCapabilities []string
	HostPorts            []int
}

// Workload is a normalized placement request (SRS 12.1).
type Workload struct {
	Project  string
	Service  string
	Revision string
	Image    string
	// Command overrides the image's default entrypoint arguments when
	// non-empty (e.g. telling an app to listen on the specific host port
	// this workload was assigned, given the M2/M3 1:1 host-port
	// simplification in agent/docker.go).
	Command     []string
	Env         map[string]string
	Resources   Resources
	Constraints Constraints
	// ContainerPorts are service listening ports for internal, name-based
	// communication. They are independent of optional host-published ports.
	ContainerPorts []int
	Storage        StoragePolicy
	Volumes        []VolumeMount
	DesiredState   DesiredState
	// DependsOn lists logical service names (another workload's Service
	// field) that must have at least one ready instance before this
	// workload is started (SRS 7.1 depends_on, FR-014, UC-05).
	DependsOn []string
}

// VolumeMount is one node-local persistent mount for a workload with
// Storage.Class == NodeLocalPersistent (SRS §10). The host-side path is
// never specified directly — it is derived deterministically by the agent
// from the workload key (see agent.VolumeHostPath), so the same workload
// always lands on the same on-disk data no matter how many times its
// container is recreated (FR-016, FR-017).
type VolumeMount struct {
	ContainerPath string `json:"container_path"`
	ReadOnly      bool   `json:"read_only"`
}

// DesiredState is the controller's intent for a workload, independent of
// its observed runtime status (SRS 12.1 "Runtime state").
type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
)

// Status is the agent-observed lifecycle state of a workload (SRS 12.1:
// "pending -> assigned -> pulling -> starting -> running -> ready").
// Failure states carry a machine-readable reason alongside Status.
type Status string

const (
	StatusPending  Status = "pending"
	StatusAssigned Status = "assigned"
	StatusPulling  Status = "pulling"
	StatusStarting Status = "starting"
	StatusRunning  Status = "running"
	StatusReady    Status = "ready"
	StatusFailed   Status = "failed"
	StatusStopped  Status = "stopped"
	// StatusWaitingDependencies means the agent picked up the assignment
	// but at least one entry in DependsOn has no ready instance yet
	// (SRS UC-05, FR-014). The agent re-checks on its next poll.
	StatusWaitingDependencies Status = "waiting_dependencies"
)

// ServiceInstance is one reachable, ready endpoint for a logical service,
// derived live from workload + node state rather than stored separately
// (SRS 8.1-8.2, UC-06).
type ServiceInstance struct {
	Project string
	Service string
	NodeID  string
	Address string
	// ContainerPort is the internal port applications use with the service
	// name, for example metaapi:5000 — independent of any published host
	// port, and discoverable even when the workload publishes none.
	ContainerPort int
	Status        Status
}

// Key returns the stable identity used to key reservations and decisions.
func (w Workload) Key() string {
	return w.Project + "/" + w.Service + "/" + w.Revision
}

// Reservation is the capacity and ports a scheduled workload holds on a
// node. Reservations accumulate per node so sequential scheduling calls
// cannot overcommit (SRS FR-006, NFR-002).
type Reservation struct {
	NodeID      string
	WorkloadKey string
	Resources   Resources
	HostPorts   []int
}

// Rejection explains why one candidate node was excluded.
type Rejection struct {
	NodeID string
	Reason string
}

// ScoreFactors is the per-node breakdown behind a score, kept so every
// decision is explainable (SRS 9.3, FR-007).
type ScoreFactors struct {
	NodeID    string
	CPUFit    float64
	MemoryFit float64
	Total     float64
}

// Decision is the full, inspectable outcome of one scheduling attempt
// (SRS 12.1 Placement, FR-007, FR-023).
type Decision struct {
	WorkloadKey string
	Candidates  []string
	Rejections  []Rejection
	Scores      []ScoreFactors
	Selected    string
	Warnings    []string
}
