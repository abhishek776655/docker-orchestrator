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
			// amd64-on-arm64 is not a mismatch (arm64 nodes are assumed to
			// run amd64 via emulation); only an arm64 workload on an amd64
			// node has no such path.
			name: "architecture mismatch",
			node: node("n1", "amd64", 4000, 8_000_000_000),
			workload: func() model.Workload {
				w := testdata.SmallWorkload("p", "s")
				w.Constraints.Architecture = "arm64"
				return w
			}(),
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

// TestArm64WorkloadNeverLandsOnAmd64Node: an arm64-only workload must never
// land on an amd64 node — amd64 hosts have no general arm64 emulation, so
// this direction of mismatch is still a hard rejection.
func TestArm64WorkloadNeverLandsOnAmd64Node(t *testing.T) {
	arm := node("node-arm", "arm64", 4000, 8_000_000_000)
	amd := node("node-amd", "amd64", 4000, 8_000_000_000)
	s := scheduler.New([]model.Node{arm, amd})

	w := testdata.SmallWorkload("p", "s")
	w.Constraints.Architecture = "arm64"
	decision, err := s.Schedule(w)
	if err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	if decision.Selected != "node-arm" {
		t.Fatalf("expected node-arm, got %s", decision.Selected)
	}
	found := false
	for _, r := range decision.Rejections {
		if r.NodeID == "node-amd" && strings.Contains(r.Reason, "architecture mismatch") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected architecture mismatch rejection for node-amd, got %+v", decision.Rejections)
	}
}

// TestAmd64WorkloadCanLandOnArm64NodeViaEmulation: an arm64 node (Docker
// Desktop on Apple Silicon) is assumed capable of running an amd64 workload
// through its built-in emulation (Rosetta/qemu), so it must not be rejected
// as an eligible candidate just because it's the only one available.
func TestAmd64WorkloadCanLandOnArm64NodeViaEmulation(t *testing.T) {
	arm := node("node-arm", "arm64", 4000, 8_000_000_000)
	s := scheduler.New([]model.Node{arm})

	decision, err := s.Schedule(testdata.SmallWorkload("p", "s"))
	if err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	if decision.Selected != "node-arm" {
		t.Fatalf("expected node-arm (via emulation), got %s; rejections: %+v", decision.Selected, decision.Rejections)
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

// TestGPUWorkloadNeverPlacedOnNodeWithoutGPU covers the M7 acceptance
// criterion verbatim: "a workload with a hard GPU requirement is never
// placed on a node without a compatible GPU."
func TestGPUWorkloadNeverPlacedOnNodeWithoutGPU(t *testing.T) {
	cpuOnly := node("node-cpu", "amd64", 4000, 8_000_000_000)
	withGPU := node("node-gpu", "amd64", 4000, 8_000_000_000)
	withGPU.GPUs = []model.GPU{{Vendor: "nvidia", Model: "A100", VRAMTotalBytes: 40_000_000_000, VRAMFreeBytes: 40_000_000_000}}

	s := scheduler.New([]model.Node{cpuOnly, withGPU})
	w := testdata.SmallWorkload("p", "s")
	w.Constraints.GPU = model.GPURequirement{Required: true, Vendor: "nvidia", VRAMBytes: 8_000_000_000}

	decision, err := s.Schedule(w)
	if err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	if decision.Selected != "node-gpu" {
		t.Fatalf("expected node-gpu, got %s", decision.Selected)
	}
	found := false
	for _, r := range decision.Rejections {
		if r.NodeID == "node-cpu" && strings.Contains(r.Reason, "no") && strings.Contains(r.Reason, "GPU") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected GPU rejection for node-cpu, got %+v", decision.Rejections)
	}
}

// TestGPUWorkloadRejectsInsufficientVRAM checks the VRAM headroom check,
// not just device presence.
func TestGPUWorkloadRejectsInsufficientVRAM(t *testing.T) {
	small := node("node-gpu", "amd64", 4000, 8_000_000_000)
	small.GPUs = []model.GPU{{Vendor: "nvidia", Model: "T4", VRAMTotalBytes: 16_000_000_000, VRAMFreeBytes: 16_000_000_000}}

	s := scheduler.New([]model.Node{small})
	w := testdata.SmallWorkload("p", "s")
	w.Constraints.GPU = model.GPURequirement{Required: true, Vendor: "nvidia", VRAMBytes: 40_000_000_000}

	decision, err := s.Schedule(w)
	if err == nil {
		t.Fatalf("expected rejection, got selection %s", decision.Selected)
	}
	if len(decision.Rejections) != 1 || !strings.Contains(decision.Rejections[0].Reason, "VRAM") {
		t.Fatalf("expected VRAM rejection, got %+v", decision.Rejections)
	}
}

// TestThermalFitPrefersCoolerNode checks the M7 exit criterion: "Placement
// decision explains hardware-based preferences" — a hot/throttling node
// loses to an equal-capacity cool one, and the reason is visible in
// ScoreFactors.
func TestThermalFitPrefersCoolerNode(t *testing.T) {
	hot := node("node-hot", "amd64", 4000, 8_000_000_000)
	hot.Thermal = model.Thermal{Known: true, Throttling: true, TemperatureC: 95}
	cool := node("node-cool", "amd64", 4000, 8_000_000_000)
	cool.Thermal = model.Thermal{Known: true, TemperatureC: 40}

	s := scheduler.New([]model.Node{hot, cool})
	decision, err := s.Schedule(testdata.SmallWorkload("p", "s"))
	if err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	if decision.Selected != "node-cool" {
		t.Fatalf("expected node-cool (thermally healthy), got %s", decision.Selected)
	}
	for _, sc := range decision.Scores {
		if sc.NodeID == "node-hot" && sc.ThermalFit != 0 {
			t.Fatalf("expected node-hot's ThermalFit to be 0 while throttling, got %v", sc.ThermalFit)
		}
		if sc.NodeID == "node-cool" && sc.ThermalFit != 1 {
			t.Fatalf("expected node-cool's ThermalFit to be 1, got %v", sc.ThermalFit)
		}
	}
}

// TestUnknownThermalIsNeutralNotPenalized covers SRS §9.5: "not a universal
// temperature threshold" — a node with no thermal telemetry must never
// score worse than one that actively reports being fine.
func TestUnknownThermalIsNeutralNotPenalized(t *testing.T) {
	unknown := node("node-unknown", "amd64", 4000, 8_000_000_000) // Thermal zero value: Known=false
	known := node("node-known", "amd64", 4000, 8_000_000_000)
	known.Thermal = model.Thermal{Known: true, TemperatureC: 40}

	s := scheduler.New([]model.Node{unknown, known})
	decision, err := s.Schedule(testdata.SmallWorkload("p", "s"))
	if err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	for _, sc := range decision.Scores {
		if sc.ThermalFit != 1 {
			t.Fatalf("expected ThermalFit 1 for both unknown and known-healthy nodes, got %s=%v", sc.NodeID, sc.ThermalFit)
		}
	}
	// Equal on every factor -> tie-break by node ID, same guarantee as M1.
	if decision.Selected != "node-known" {
		t.Fatalf("expected tie-break to select node-known, got %s", decision.Selected)
	}
}

// TestCacheFitPrefersNodeWithImageAlready checks image-cache locality
// scoring (SRS §9.3).
func TestCacheFitPrefersNodeWithImageAlready(t *testing.T) {
	cold := node("node-cold", "amd64", 4000, 8_000_000_000)
	warm := node("node-warm", "amd64", 4000, 8_000_000_000)
	warm.CachedImages = []string{"myapp:latest"}

	s := scheduler.New([]model.Node{cold, warm})
	w := testdata.SmallWorkload("p", "s")
	w.Image = "myapp:latest"

	decision, err := s.Schedule(w)
	if err != nil {
		t.Fatalf("schedule failed: %v", err)
	}
	if decision.Selected != "node-warm" {
		t.Fatalf("expected node-warm (has the image cached), got %s", decision.Selected)
	}
}

func portRejection(t *testing.T, d *model.Decision, nodeID string) string {
	t.Helper()
	for _, r := range d.Rejections {
		if r.NodeID == nodeID {
			return r.Reason
		}
	}
	t.Fatalf("no rejection for %s: %+v", nodeID, d.Rejections)
	return ""
}

func TestPortConflictNamesHolderAndHintsAtProjectName(t *testing.T) {
	s := scheduler.New([]model.Node{node("node-a", "arm64", 4000, 8_000_000_000), node("node-b", "arm64", 4000, 8_000_000_000)})
	s.SeedReservation("node-a", model.Resources{}, map[int]string{5600: "dockerizelocalhost/metaapi/latest"})

	sameService := testdata.SmallWorkload("dockerize-localhost", "metaapi")
	sameService.Constraints.HostPorts = []int{5600}
	d, err := s.Schedule(sameService)
	if err != nil {
		t.Fatal(err)
	}
	reason := portRejection(t, d, "node-a")
	if !strings.Contains(reason, "reserved by dockerizelocalhost/metaapi/latest") || !strings.Contains(reason, `project "dockerizelocalhost"`) {
		t.Fatalf("reason = %q, want the holder and a project-name hint", reason)
	}
	if d.Selected != "node-b" {
		t.Fatalf("selected %s, want node-b", d.Selected)
	}

	other := testdata.SmallWorkload("dockerize-localhost", "gateway")
	other.Constraints.HostPorts = []int{5600}
	d2, _ := s.Schedule(other)
	reason = portRejection(t, d2, "node-a")
	if !strings.Contains(reason, "reserved by dockerizelocalhost/metaapi/latest") || strings.Contains(reason, "use that project name") {
		t.Fatalf("reason = %q, want the holder but no hint for an unrelated service", reason)
	}
}

func TestPortConflictWithinOneBatchNamesEarlierService(t *testing.T) {
	s := scheduler.New([]model.Node{node("node-a", "arm64", 4000, 8_000_000_000)})
	first := testdata.SmallWorkload("p", "api")
	first.Constraints.HostPorts = []int{8080}
	if _, err := s.Schedule(first); err != nil {
		t.Fatal(err)
	}
	second := testdata.SmallWorkload("p", "admin")
	second.Constraints.HostPorts = []int{8080}
	d, _ := s.Schedule(second)
	if reason := portRejection(t, d, "node-a"); !strings.Contains(reason, "reserved by "+first.Key()) {
		t.Fatalf("reason = %q, want it to name %s", reason, first.Key())
	}
}

func TestForcedPlacementOverridesRejection(t *testing.T) {
	small := node("small", "amd64", 100, 1<<30)
	big := node("big", "amd64", 8000, 16<<30)
	s := scheduler.New([]model.Node{small, big})
	w := testdata.SmallWorkload("p", "api")
	w.Resources.CPUMillis = 2000
	w.Constraints.PinnedNodeID = "small"

	if _, err := s.Schedule(w); err == nil {
		t.Fatal("unforced: want a rejection, small has too little CPU")
	}
	w.Constraints.Forced = true
	d, err := s.Schedule(w)
	if err != nil {
		t.Fatalf("forced: %v", err)
	}
	if d.Selected != "small" {
		t.Fatalf("selected %s, want small", d.Selected)
	}
	if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "despite: insufficient allocatable CPU") {
		t.Fatalf("warnings = %v", d.Warnings)
	}

	w.Constraints.PinnedNodeID = "missing"
	if _, err := s.Schedule(w); err == nil {
		t.Fatal("forcing onto an unknown node must fail")
	}
}

func TestSchedulerSkipsNodeWithPortHeldOutsideHaco(t *testing.T) {
	nodes := []model.Node{
		{ID: "busy", Availability: model.Available, Allocatable: model.Resources{CPUMillis: 4000, MemoryBytes: 8 << 30}, BusyPorts: map[int]string{5672: "container dockerize-rabbitmq-1"}},
		{ID: "free", Availability: model.Available, Allocatable: model.Resources{CPUMillis: 4000, MemoryBytes: 8 << 30}},
	}
	w := model.Workload{Project: "p", Service: "rabbitmq", Revision: "latest", Constraints: model.Constraints{HostPorts: []int{5672}}}
	d, err := scheduler.New(nodes).Schedule(w)
	if err != nil || d.Selected != "free" {
		t.Fatalf("selected %q, err %v; want the node without the port in use", d.Selected, err)
	}

	_, err = scheduler.New(nodes[:1]).Schedule(w)
	if err == nil || !strings.Contains(err.Error(), "port 5672 is in use on this node by container dockerize-rabbitmq-1") {
		t.Fatalf("err = %v, want the holder named", err)
	}
}

func TestSpreadFavorsTheNodeCarryingFewerServices(t *testing.T) {
	nodes := []model.Node{node("busy", "arm64", 4000, 8<<30), node("quiet", "arm64", 4000, 8<<30)}
	s := scheduler.New(nodes)
	// Six services already sit on "busy". They reserve no CPU or memory, so
	// headroom alone would see the two nodes as identical.
	s.SeedWorkloadCount("busy", 6)
	d, err := s.Schedule(model.Workload{Project: "p", Service: "api", Revision: "latest", Image: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Selected != "quiet" {
		t.Fatalf("selected %s, want quiet: the busy node already carries six services", d.Selected)
	}
	var busy, quiet float64
	for _, sc := range d.Scores {
		if sc.NodeID == "busy" {
			busy = sc.SpreadFit
		} else {
			quiet = sc.SpreadFit
		}
	}
	if !(quiet == 1 && busy < 0.5) {
		t.Fatalf("spread fit busy=%.2f quiet=%.2f, want quiet 1.00 and busy under 0.50", busy, quiet)
	}
}

func TestSpreadCountsEachServiceScheduledInThisBatch(t *testing.T) {
	s := scheduler.New([]model.Node{node("a", "arm64", 4000, 8<<30), node("b", "arm64", 4000, 8<<30)})
	placed := map[string]int{}
	for i := 0; i < 8; i++ {
		d, err := s.Schedule(model.Workload{Project: "p", Service: string(rune('a' + i)), Revision: "latest", Image: "x"})
		if err != nil {
			t.Fatal(err)
		}
		placed[d.Selected]++
	}
	if placed["a"] != 4 || placed["b"] != 4 {
		t.Fatalf("placed %v, want an even split of identical services", placed)
	}
}
