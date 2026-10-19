// Package model defines the core scheduling types shared by the simulator
// and (later) the real scheduler: nodes, workloads, resources, and the
// recorded outcome of a placement attempt.
package model

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
	Architecture string
	Labels       map[string]string
	Capabilities map[string]bool
	Allocatable  Resources
	Availability Availability
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
	Project     string
	Service     string
	Revision    string
	Resources   Resources
	Constraints Constraints
	Storage     StoragePolicy
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
