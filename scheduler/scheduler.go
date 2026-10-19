// Package scheduler implements the M1 scheduler simulator: hard
// eligibility filters, deterministic scoring, and reservation accounting
// over in-memory nodes. It has no dependency on Docker, a database, or
// the network (SRS milestone M1).
package scheduler

import (
	"fmt"
	"sort"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// Weights controls deterministic scoring (SRS 9.3). CPU and memory fit
// are the only signals in M1; GPU/thermal/network/cache fit are deferred
// to M7.
type Weights struct {
	CPU    float64
	Memory float64
}

// DefaultWeights weighs CPU and memory headroom equally.
var DefaultWeights = Weights{CPU: 0.5, Memory: 0.5}

type nodeState struct {
	node        model.Node
	reservedCPU int64
	reservedMem int64
	usedPorts   map[int]bool
}

// Scheduler holds cluster state: the known nodes and the reservations
// accumulated from prior Schedule calls. It is not safe for concurrent
// use without external synchronization.
type Scheduler struct {
	weights Weights
	nodes   map[string]*nodeState
	order   []string // insertion order, used to make iteration deterministic
}

// New creates a scheduler over the given nodes using the default weights.
func New(nodes []model.Node) *Scheduler {
	return NewWithWeights(nodes, DefaultWeights)
}

// NewWithWeights creates a scheduler with explicit scoring weights.
func NewWithWeights(nodes []model.Node, weights Weights) *Scheduler {
	s := &Scheduler{
		weights: weights,
		nodes:   make(map[string]*nodeState, len(nodes)),
	}
	for _, n := range nodes {
		s.nodes[n.ID] = &nodeState{node: n, usedPorts: make(map[int]bool)}
		s.order = append(s.order, n.ID)
	}
	// Deterministic regardless of input order.
	sort.Strings(s.order)
	return s
}

// SeedReservation pre-loads capacity and ports already held on a node by
// workloads scheduled in a previous process (e.g. recovered from
// persistent storage at startup, SRS NFR-001). It must be called before
// any Schedule call on this instance.
func (s *Scheduler) SeedReservation(nodeID string, res model.Resources, hostPorts []int) {
	ns, ok := s.nodes[nodeID]
	if !ok {
		return
	}
	ns.reservedCPU += res.CPUMillis
	ns.reservedMem += res.MemoryBytes
	for _, p := range hostPorts {
		ns.usedPorts[p] = true
	}
}

// Schedule evaluates a workload against every known node: hard filters
// first, then deterministic scoring, then reservation of the winning
// node's capacity. It returns a full decision trail even on rejection so
// callers can inspect why (SRS FR-007, FR-023).
func (s *Scheduler) Schedule(w model.Workload) (*model.Decision, error) {
	decision := &model.Decision{WorkloadKey: w.Key()}

	var eligible []*nodeState
	for _, id := range s.order {
		ns := s.nodes[id]
		if reason, ok := s.reject(ns, w); ok {
			decision.Rejections = append(decision.Rejections, model.Rejection{NodeID: id, Reason: reason})
			continue
		}
		decision.Candidates = append(decision.Candidates, id)
		eligible = append(eligible, ns)
	}

	if len(eligible) == 0 {
		return decision, fmt.Errorf("no eligible node for workload %s", w.Key())
	}

	for _, ns := range eligible {
		decision.Scores = append(decision.Scores, s.score(ns, w))
	}
	sort.Slice(decision.Scores, func(i, j int) bool {
		a, b := decision.Scores[i], decision.Scores[j]
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		return a.NodeID < b.NodeID // explicit tie-break (SRS M1 backlog)
	})

	selected := decision.Scores[0].NodeID
	decision.Selected = selected
	s.reserve(s.nodes[selected], w)

	return decision, nil
}

// reject returns a hard-rejection reason for ns, or ok=false if ns is
// eligible (SRS 9.1 step 2).
func (s *Scheduler) reject(ns *nodeState, w model.Workload) (string, bool) {
	n := ns.node

	if n.Availability != model.Available {
		return fmt.Sprintf("node %s is %s", n.ID, n.Availability), true
	}

	if w.Constraints.Architecture != "" && w.Constraints.Architecture != n.Architecture {
		return fmt.Sprintf("architecture mismatch: node has %s, workload requires %s", n.Architecture, w.Constraints.Architecture), true
	}

	for k, v := range w.Constraints.RequiredLabels {
		if n.Labels[k] != v {
			return fmt.Sprintf("missing required label %s=%s", k, v), true
		}
	}

	for _, cap := range w.Constraints.RequiredCapabilities {
		if !n.Capabilities[cap] {
			return fmt.Sprintf("missing required capability %s", cap), true
		}
	}

	if w.Storage.Class == model.NodeLocalPersistent && w.Storage.PinnedNodeID != "" && w.Storage.PinnedNodeID != n.ID {
		return fmt.Sprintf("storage affinity mismatch: data pinned to %s", w.Storage.PinnedNodeID), true
	}

	if n.Allocatable.CPUMillis-ns.reservedCPU < w.Resources.CPUMillis {
		return "insufficient allocatable CPU", true
	}
	if n.Allocatable.MemoryBytes-ns.reservedMem < w.Resources.MemoryBytes {
		return "insufficient allocatable memory", true
	}

	for _, p := range w.Constraints.HostPorts {
		if ns.usedPorts[p] {
			return fmt.Sprintf("host port %d already reserved", p), true
		}
	}

	return "", false
}

// score computes CPU/memory headroom fit for one eligible node (SRS 9.3).
// Fit is the fraction of allocatable capacity that would remain free
// after placing the workload; higher is better.
func (s *Scheduler) score(ns *nodeState, w model.Workload) model.ScoreFactors {
	n := ns.node

	cpuFit := headroomFit(n.Allocatable.CPUMillis, ns.reservedCPU, w.Resources.CPUMillis)
	memFit := headroomFit(n.Allocatable.MemoryBytes, ns.reservedMem, w.Resources.MemoryBytes)

	return model.ScoreFactors{
		NodeID:    n.ID,
		CPUFit:    cpuFit,
		MemoryFit: memFit,
		Total:     s.weights.CPU*cpuFit + s.weights.Memory*memFit,
	}
}

func headroomFit(allocatable, reserved, request int64) float64 {
	free := allocatable - reserved
	if allocatable <= 0 {
		return 0
	}
	return float64(free-request) / float64(allocatable)
}

// reserve records the winning workload's resources and ports against the
// node so subsequent Schedule calls cannot overcommit it.
func (s *Scheduler) reserve(ns *nodeState, w model.Workload) {
	ns.reservedCPU += w.Resources.CPUMillis
	ns.reservedMem += w.Resources.MemoryBytes
	for _, p := range w.Constraints.HostPorts {
		ns.usedPorts[p] = true
	}
}
