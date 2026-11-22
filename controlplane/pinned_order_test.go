package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

func TestPinnedServicesClaimTheirNodeBeforeUnpinnedOnesAreScheduled(t *testing.T) {
	svc, s := twoNodeService(t)
	ctx := context.Background()
	for _, id := range []string{"n1", "n2"} {
		n := model.Node{ID: id, Address: "10.0.0.1", Architecture: "arm64", Labels: map[string]string{}, Capabilities: map[string]bool{},
			Availability: model.Available, HeartbeatAt: time.Now(), Allocatable: model.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30}}
		if err := s.UpsertNode(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	// The unpinned service comes first in the file and would win n1 on the
	// tie; the pinned one needs n1's memory.
	free := model.Workload{Project: "p", Service: "free", Revision: "latest", Image: "x", Resources: model.Resources{CPUMillis: 100, MemoryBytes: 3 << 30}}
	pinned := model.Workload{Project: "p", Service: "pinned", Revision: "latest", Image: "x", Resources: model.Resources{CPUMillis: 100, MemoryBytes: 3 << 30},
		Constraints: model.Constraints{PinnedNodeID: "n1"}}

	decisions, err := svc.dryRunSchedule(ctx, []model.Workload{free, pinned}, false)
	if err != nil {
		t.Fatalf("both fit when the pinned one goes first: %v", err)
	}
	if len(decisions) != 2 || decisions[0].WorkloadKey != "p/free/latest" || decisions[1].WorkloadKey != "p/pinned/latest" {
		t.Fatalf("decisions must keep the file's order, got %+v", decisions)
	}
	if decisions[1].Selected != "n1" || decisions[0].Selected != "n2" {
		t.Fatalf("pinned on %s and free on %s, want pinned on n1 and free on n2", decisions[1].Selected, decisions[0].Selected)
	}
}

func TestSchedulingOrderPutsPinnedFirstKeepingEachGroupInOrder(t *testing.T) {
	ws := []model.Workload{
		{Service: "a"},
		{Service: "b", Constraints: model.Constraints{PinnedNodeID: "n1"}},
		{Service: "c"},
		{Service: "d", Storage: model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: "n2"}},
	}
	got := schedulingOrder(ws)
	want := []int{1, 3, 0, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestUnpinnedServicesAvoidTheNodeAlreadyCarryingPinnedOnes(t *testing.T) {
	svc, s := twoNodeService(t)
	ctx := context.Background()
	// Six services pinned to n1 and already running; they declare no CPU or memory.
	for _, name := range []string{"p1", "p2", "p3", "p4", "p5", "p6"} {
		r := record("shop", name, "n1", model.StatusReady)
		r.Constraints.PinnedNodeID = "n1"
		if err := s.CreateWorkload(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	free := model.Workload{Project: "shop", Service: "free", Revision: "latest", Image: "x"}
	decisions, err := svc.dryRunSchedule(ctx, []model.Workload{free}, false)
	if err != nil {
		t.Fatal(err)
	}
	if decisions[0].Selected != "n2" {
		t.Fatalf("free service went to %s, want n2: n1 already carries six pinned services", decisions[0].Selected)
	}
}

// A service that is not pinned while others are must still get its source:
// the pin used to wipe its bind mount, leaving an empty folder over the code.
func TestUnpinnedServiceStillGetsItsSourceWhenOthersArePinned(t *testing.T) {
	svc, s := twoNodeService(t)
	ctx := context.Background()
	for _, id := range []string{"n1", "n2"} {
		n := model.Node{ID: id, Address: "10.0.0.1", Architecture: "arm64", Labels: map[string]string{}, Capabilities: map[string]bool{"buildx": true},
			Availability: model.Available, HeartbeatAt: time.Now(), Allocatable: model.Resources{CPUMillis: 4000, MemoryBytes: 8 << 30}}
		if err := s.UpsertNode(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	for _, d := range []string{"app", "lib"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, d, "a.py"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "Dockerfile"), []byte("FROM alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := "services:\n  app:\n    build: ./app\n    volumes:\n      - ./app:/app\n      - ./lib:/lib\n  other:\n    build: ./app\n    volumes:\n      - ./lib:/lib\n  db:\n    image: alpine\n"
	// db is pinned to a node (as `keep on host` does for the host); app and
	// other are not.
	if _, _, err := svc.ComposeDeploy(ctx, "p", dir, []byte(yaml), DeployOptions{PinNodes: map[string][]string{"n1": {"db"}}}); err != nil {
		t.Fatal(err)
	}
	rec, err := svc.GetWorkload(ctx, "p/app/latest")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range rec.Volumes {
		if v.RemoteDirKey == "" && v.HostPath == "" && v.SeedDirKey == "" {
			t.Errorf("volume %s of an unpinned service came out as an empty folder", v.ContainerPath)
		}
	}
	if len(rec.Volumes) != 2 {
		t.Fatalf("%d volumes, want 2", len(rec.Volumes))
	}
}
