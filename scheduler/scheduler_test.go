package scheduler_test

import (
	"strings"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/scheduler"
	"github.com/abhishek/hardware-aware-container-orchestrator/scheduler/testdata"
)

func node(id, arch string, cpu, mem int64) model.Node {
	return model.Node{
		ID:           id,
		Architecture: arch,
		Labels:       map[string]string{},
		Capabilities: map[string]bool{},
		Allocatable:  model.Resources{CPUMillis: cpu, MemoryBytes: mem},
		Availability: model.Available,
	}
}

// TestHardFilters is table-driven over every rejection reason the
// scheduler must enforce before scoring (SRS 9.1 step 2).
func TestHardFilters(t *testing.T) {
	cases := []struct {
		name       string
		node       model.Node
		workload   model.Workload
		wantReject string
	}{
		{
			name: "unavailable node",
			node: func() model.Node {
				n := node("n1", "amd64", 4000, 8_000_000_000)
				n.Availability = model.Unavailable
				return n
			}(),
			workload:   testdata.SmallWorkload("p", "s"),
			wantReject: "unavailable",
		},
		{
			name:       "architecture mismatch",
			node:       node("n1", "arm64", 4000, 8_000_000_000),
			workload:   testdata.SmallWorkload("p", "s"),
			wantReject: "architecture mismatch",
		},
		{
			name: "missing required label",
			node: node("n1", "amd64", 4000, 8_000_000_000),
			workload: func() model.Workload {
				w := testdata.SmallWorkload("p", "s")
				w.Constraints.RequiredLabels = map[string]string{"zone": "east"}
				return w
			}(),
			wantReject: "required label",
		},
		{
			name: "missing required capability",
			node: node("n1", "amd64", 4000, 8_000_000_000),
			workload: func() model.Workload {
				w := testdata.SmallWorkload("p", "s")
				w.Constraints.RequiredCapabilities = []string{"gpu"}
				return w
			}(),
			wantReject: "required capability",
		},
		{
			name: "storage affinity mismatch",
			node: node("n1", "amd64", 4000, 8_000_000_000),
			workload: func() model.Workload {
				w := testdata.SmallWorkload("p", "s")
				w.Storage = model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: "n2"}
				return w
			}(),
			wantReject: "storage affinity",
		},
		{
			name:       "insufficient cpu",
			node:       node("n1", "amd64", 100, 8_000_000_000),
			workload:   testdata.SmallWorkload("p", "s"),
			wantReject: "insufficient allocatable CPU",
		},
		{
			name:       "insufficient memory",
			node:       node("n1", "amd64", 4000, 1_000_000),
			workload:   testdata.SmallWorkload("p", "s"),
			wantReject: "insufficient allocatable memory",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := scheduler.New([]model.Node{tc.node})
			decision, err := s.Schedule(tc.workload)
			if err == nil {
				t.Fatalf("expected rejection, got selection %s", decision.Selected)
			}
			if len(decision.Rejections) != 1 {
				t.Fatalf("expected exactly one rejection, got %+v", decision.Rejections)
			}
			if !strings.Contains(decision.Rejections[0].Reason, tc.wantReject) {
				t.Fatalf("reason %q does not contain %q", decision.Rejections[0].Reason, tc.wantReject)
			}
		})
	}
}

// TestScoringPrefersMoreHeadroom checks that, all else equal, the node
// with more free capacity wins (SRS 9.3).
func TestScoringPrefersMoreHeadroom(t *testing.T) {
	busy := node("node-a", "amd64", 4000, 8_000_000_000)
	free := node("node-b", "amd64", 4000, 8_000_000_000)
	s := scheduler.New([]model.Node{busy, free})

	// Pre-load node-a so it has less headroom than node-b.
	if _, err := s.Schedule(func() model.Workload {
		w := testdata.SmallWorkload("p", "filler")
		w.Resources = model.Resources{CPUMillis: 3000, MemoryBytes: 1_000_000}
		w.Storage = model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: "node-a"}
		return w
	}()); err != nil {
		t.Fatalf("seed schedule failed: %v", err)
	}

	decision, err := s.Schedule(testdata.SmallWorkload("p", "s"))
	if err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	if decision.Selected != "node-b" {
		t.Fatalf("expected node-b (more headroom), got %s", decision.Selected)
	}
}

// TestTieBreakIsNodeID checks the explicit deterministic tie-break
// required by the M1 backlog: equal scores resolve by ascending node ID.
func TestTieBreakIsNodeID(t *testing.T) {
	s := scheduler.New(testdata.TwoEqualNodes())
	decision, err := s.Schedule(testdata.SmallWorkload("p", "s"))
	if err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	if decision.Selected != "node-a" {
		t.Fatalf("expected tie-break to select node-a, got %s", decision.Selected)
	}
}

// TestReservationPreventsOvercommit schedules workloads until a node's
// capacity is exhausted and checks the next request is rejected rather
// than silently overcommitted (SRS FR-006, NFR-002).
func TestReservationPreventsOvercommit(t *testing.T) {
	n := node("solo", "amd64", 1000, 2_000_000_000)
	s := scheduler.New([]model.Node{n})

	fits := testdata.SmallWorkload("p", "a") // 500 millicores
	fits.Constraints.Architecture = "amd64"
	if _, err := s.Schedule(fits); err != nil {
		t.Fatalf("first schedule should fit: %v", err)
	}

	overcommits := testdata.SmallWorkload("p", "b") // another 500 millicores: exactly at the edge
	if _, err := s.Schedule(overcommits); err != nil {
		t.Fatalf("second schedule should still fit exactly: %v", err)
	}

	tooMuch := testdata.SmallWorkload("p", "c")
	decision, err := s.Schedule(tooMuch)
	if err == nil {
		t.Fatalf("expected rejection after capacity exhausted, got selection %s", decision.Selected)
	}
	if len(decision.Rejections) != 1 || !strings.Contains(decision.Rejections[0].Reason, "insufficient allocatable CPU") {
		t.Fatalf("expected CPU exhaustion rejection, got %+v", decision.Rejections)
	}
}

// TestResourceExhaustionAcrossTwoNodes mirrors the M1 backlog fixture:
// a workload that fits on neither of two nodes must be rejected with a
// reason recorded for each.
func TestResourceExhaustionAcrossTwoNodes(t *testing.T) {
	s := scheduler.New(testdata.TwoEqualNodes())
	huge := testdata.SmallWorkload("p", "huge")
	huge.Resources = model.Resources{CPUMillis: 100_000, MemoryBytes: 1}

	decision, err := s.Schedule(huge)
	if err == nil {
		t.Fatalf("expected rejection, got selection %s", decision.Selected)
	}
	if len(decision.Candidates) != 0 {
		t.Fatalf("expected no candidates, got %v", decision.Candidates)
	}
	if len(decision.Rejections) != 2 {
		t.Fatalf("expected a rejection reason for each node, got %+v", decision.Rejections)
	}
}

// TestStoragePinningOverridesHeadroom mirrors the M1 backlog fixture:
// node affinity for persistent storage wins even when another node has
// more free capacity.
func TestStoragePinningOverridesHeadroom(t *testing.T) {
	nodes := testdata.TwoEqualNodes()
	s := scheduler.New(nodes)

	// Give node-b more headroom by consuming some of node-a's capacity
	// with an unrelated, unpinned workload first.
	filler := testdata.SmallWorkload("p", "filler")
	filler.Resources = model.Resources{CPUMillis: 3000, MemoryBytes: 1_000_000}
	filler.Storage = model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: "node-a"}
	if _, err := s.Schedule(filler); err != nil {
		t.Fatalf("seed schedule failed: %v", err)
	}

	pinned := testdata.SmallWorkload("p", "db")
	pinned.Storage = model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: "node-a"}

	decision, err := s.Schedule(pinned)
	if err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	if decision.Selected != "node-a" {
		t.Fatalf("expected storage affinity to pin to node-a, got %s", decision.Selected)
	}
}

// TestArchitectureMismatchAcrossTwoNodes mirrors the M1 backlog fixture:
// a workload requiring amd64 must never land on an arm64 node even if
// it is the only other candidate.
func TestArchitectureMismatchAcrossTwoNodes(t *testing.T) {
	arm := node("node-arm", "arm64", 4000, 8_000_000_000)
	amd := node("node-amd", "amd64", 4000, 8_000_000_000)
	s := scheduler.New([]model.Node{arm, amd})

	decision, err := s.Schedule(testdata.SmallWorkload("p", "s"))
	if err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	if decision.Selected != "node-amd" {
		t.Fatalf("expected node-amd, got %s", decision.Selected)
	}
	found := false
	for _, r := range decision.Rejections {
		if r.NodeID == "node-arm" && strings.Contains(r.Reason, "architecture mismatch") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected architecture mismatch rejection for node-arm, got %+v", decision.Rejections)
	}
}

// TestHostPortConflictAcrossTwoNodes mirrors the M1 backlog fixture:
// once a host port is reserved on a node, a second workload requiring
// the same port must be excluded from that node.
func TestHostPortConflictAcrossTwoNodes(t *testing.T) {
	s := scheduler.New(testdata.TwoEqualNodes())

	first := testdata.SmallWorkload("p", "a")
	first.Constraints.HostPorts = []int{8080}
	first.Storage = model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: "node-a"}
	if _, err := s.Schedule(first); err != nil {
		t.Fatalf("seed schedule failed: %v", err)
	}

	second := testdata.SmallWorkload("p", "b")
	second.Constraints.HostPorts = []int{8080}
	second.Storage = model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: "node-a"}

	decision, err := s.Schedule(second)
	if err == nil {
		t.Fatalf("expected rejection, got selection %s", decision.Selected)
	}
	found := false
	for _, r := range decision.Rejections {
		if r.NodeID == "node-a" && strings.Contains(r.Reason, "already reserved") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected host port conflict on node-a, got %+v", decision.Rejections)
	}
}

// TestReproducibility runs the same plan repeatedly against fresh,
// identical scheduler instances and asserts identical results every
// time (SRS M1 backlog: reproducibility test).
func TestReproducibility(t *testing.T) {
	var first *model.Decision
	for i := 0; i < 20; i++ {
		s := scheduler.New(testdata.TwoEqualNodes())
		decision, err := s.Schedule(testdata.SmallWorkload("p", "s"))
		if err != nil {
			t.Fatalf("schedule failed: %v", err)
		}
		if first == nil {
			first = decision
			continue
		}
		if decision.Selected != first.Selected {
			t.Fatalf("non-deterministic selection: %s vs %s", decision.Selected, first.Selected)
		}
		if len(decision.Scores) != len(first.Scores) {
			t.Fatalf("non-deterministic score count")
		}
		for j := range decision.Scores {
			if decision.Scores[j] != first.Scores[j] {
				t.Fatalf("non-deterministic score ordering at %d: %+v vs %+v", j, decision.Scores[j], first.Scores[j])
			}
		}
	}
}
