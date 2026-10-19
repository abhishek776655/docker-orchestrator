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

// Weights controls deterministic scoring (SRS 9.3). GPU fit is not
// weighted: GPU is a hard filter only (SRS FR-022, §9.4) — once a node is
// GPU-eligible, its score comes from the same CPU/memory/thermal/cache
// signals as any other workload. Network locality (§9.3) has no real
// signal to weight in this prototype and is left at zero.
type Weights struct {
	CPU     float64
	Memory  float64
	Thermal float64
	Cache   float64
}

// DefaultWeights favors CPU/memory headroom, with thermal and image-cache
// locality as smaller tie-breaking signals (SRS §9.3, M7).
var DefaultWeights = Weights{CPU: 0.4, Memory: 0.4, Thermal: 0.1, Cache: 0.1}

type nodeState struct {
	node         model.Node
	reservedCPU  int64
	reservedMem  int64
	reservedVRAM int64
	usedPorts    map[int]bool
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

// SeedVRAMReservation pre-loads VRAM already held on a node by a
// previously scheduled GPU workload, the GPU analogue of SeedReservation.
func (s *Scheduler) SeedVRAMReservation(nodeID string, vramBytes int64) {
	if ns, ok := s.nodes[nodeID]; ok {
		ns.reservedVRAM += vramBytes
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

	if w.Constraints.GPU.Required {
		if reason, ok := rejectGPU(n, ns, w.Constraints.GPU); ok {
			return reason, true
		}
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

// rejectGPU checks a workload's hard GPU requirement against a node's
// discovered devices (SRS §9.2, §9.4): "a workload with a hard GPU
// requirement is never placed on a node without a compatible GPU."
func rejectGPU(n model.Node, ns *nodeState, req model.GPURequirement) (string, bool) {
	var freeVRAM int64
	found := false
	for _, g := range n.GPUs {
		if req.Vendor != "" && g.Vendor != req.Vendor {
			continue
		}
		found = true
		freeVRAM += g.VRAMFreeBytes
	}
	if !found {
		if req.Vendor != "" {
			return fmt.Sprintf("no %s GPU on node %s", req.Vendor, n.ID), true
		}
		return fmt.Sprintf("no GPU on node %s", n.ID), true
	}
	if freeVRAM-ns.reservedVRAM < req.VRAMBytes {
		return fmt.Sprintf("insufficient free VRAM on node %s", n.ID), true
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
	thermalFit := thermalFit(n.Thermal)
	cacheFit := cacheFit(n.CachedImages, w.Image)

	return model.ScoreFactors{
		NodeID:     n.ID,
		CPUFit:     cpuFit,
		MemoryFit:  memFit,
		ThermalFit: thermalFit,
		CacheFit:   cacheFit,
		Total:      s.weights.CPU*cpuFit + s.weights.Memory*memFit + s.weights.Thermal*thermalFit + s.weights.Cache*cacheFit,
	}
}

// thermalFit is neutral (1.0, "fine") whenever the node reports no thermal
// telemetry — unknown must never be scored as bad (SRS §9.5: "not a
// universal temperature threshold"). A node actively reporting throttling
// scores 0; otherwise fit degrades linearly above 60°C, floored at 0.
func thermalFit(t model.Thermal) float64 {
	if !t.Known {
		return 1.0
	}
	if t.Throttling {
		return 0.0
	}
	const warn, critical = 60.0, 90.0
	if t.TemperatureC <= warn {
		return 1.0
	}
	if t.TemperatureC >= critical {
		return 0.0
	}
	return 1.0 - (t.TemperatureC-warn)/(critical-warn)
}

// cacheFit rewards a node that already has the workload's image pulled,
// saving pull time (SRS §9.3 "image cache locality").
func cacheFit(cached []string, image string) float64 {
	for _, c := range cached {
		if c == image {
			return 1.0
		}
	}
	return 0.0
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
	if w.Constraints.GPU.Required {
		ns.reservedVRAM += w.Constraints.GPU.VRAMBytes
	}
	for _, p := range w.Constraints.HostPorts {
		ns.usedPorts[p] = true
	}
}
