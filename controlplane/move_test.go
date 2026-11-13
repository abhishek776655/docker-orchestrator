package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

func twoNodeService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	svc, s := shutdownTestService(t, map[string]model.Availability{"n1": model.Available, "n2": model.Available})
	for _, id := range []string{"n1", "n2"} {
		n := model.Node{ID: id, Address: "10.0.0." + id[1:], Architecture: "arm64", Labels: map[string]string{}, Capabilities: map[string]bool{},
			Availability: model.Available, HeartbeatAt: time.Now(), Allocatable: model.Resources{CPUMillis: 4000, MemoryBytes: 8 << 30}}
		if err := s.UpsertNode(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
	return svc, s
}

func TestMoveRunsTheServiceOnTheNewNodeAndRestartsDependents(t *testing.T) {
	svc, s := twoNodeService(t)
	ctx := context.Background()
	for _, r := range []store.WorkloadRecord{
		record("shop", "api", "n1", model.StatusReady),
		record("shop", "web", "n1", model.StatusReady, "api"),
		record("shop", "cache", "n1", model.StatusReady),
	} {
		if err := s.CreateWorkload(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	res, err := svc.MoveWorkload(ctx, "shop/api/latest", "n2", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.From != "n1" || res.To != "n2" || len(res.Restarted) != 1 || res.Restarted[0] != "web" {
		t.Fatalf("result = %+v, want moved n1 -> n2 and web restarted", res)
	}
	api, _ := svc.GetWorkload(ctx, "shop/api/latest")
	if api.NodeID != "n2" || api.Status != model.StatusAssigned || api.ContainerID != "" {
		t.Fatalf("api = %s on %s (container %q), want assigned on n2 with no container", api.Status, api.NodeID, api.ContainerID)
	}
	web, _ := svc.GetWorkload(ctx, "shop/web/latest")
	if web.NodeID != "n1" || web.StatusReason != "restart requested" {
		t.Fatalf("web = %s on %s (%s), want restarted in place on n1", web.Status, web.NodeID, web.StatusReason)
	}
	cache, _ := svc.GetWorkload(ctx, "shop/cache/latest")
	if cache.Status != model.StatusReady {
		t.Fatalf("cache does not depend on api and must be untouched, got %s", cache.Status)
	}
}

func TestMoveNeedsAnAcceptedEmptyVolumeForAStatefulService(t *testing.T) {
	svc, s := twoNodeService(t)
	ctx := context.Background()
	db := record("shop", "db", "n1", model.StatusReady)
	db.Storage = model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: "n1"}
	if err := s.CreateWorkload(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MoveWorkload(ctx, "shop/db/latest", "n2", false, false); !errors.Is(err, ErrMoveNeedsEmptyVolume) {
		t.Fatalf("err = %v, want ErrMoveNeedsEmptyVolume", err)
	}
	if got, _ := svc.GetWorkload(ctx, "shop/db/latest"); got.NodeID != "n1" {
		t.Fatalf("refused move must change nothing, db is on %s", got.NodeID)
	}
	if _, err := svc.MoveWorkload(ctx, "shop/db/latest", "n2", false, true); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetWorkload(ctx, "shop/db/latest")
	if got.NodeID != "n2" || got.Storage.PinnedNodeID != "n2" {
		t.Fatalf("db on %s with its volume pinned to %s, want both n2", got.NodeID, got.Storage.PinnedNodeID)
	}
}

func TestMoveRefusesWhatCannotBeMoved(t *testing.T) {
	svc, s := twoNodeService(t)
	ctx := context.Background()
	if err := s.CreateWorkload(ctx, record("shop", "api", "n1", model.StatusReady)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MoveWorkload(ctx, "shop/api/latest", "n1", false, false); !errors.Is(err, ErrNotMovable) {
		t.Errorf("same node: err = %v, want ErrNotMovable", err)
	}
	if _, err := svc.MoveWorkload(ctx, "shop/api/latest", "", false, false); !errors.Is(err, ErrNotMovable) {
		t.Errorf("no node: err = %v, want ErrNotMovable", err)
	}
	if _, err := svc.MoveWorkload(ctx, "shop/ghost/latest", "n2", false, false); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown service: err = %v, want ErrNotFound", err)
	}
	if err := s.StopWorkload(ctx, "shop/api/latest"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MoveWorkload(ctx, "shop/api/latest", "n2", false, false); !errors.Is(err, ErrNotMovable) {
		t.Errorf("stopped: err = %v, want ErrNotMovable", err)
	}
}

func TestMoveToANodeTheSchedulerRejectsNeedsForce(t *testing.T) {
	svc, s := twoNodeService(t)
	ctx := context.Background()
	api := record("shop", "api", "n1", model.StatusReady)
	api.Constraints.HostPorts = []int{8080}
	if err := s.CreateWorkload(ctx, api); err != nil {
		t.Fatal(err)
	}
	svc.recordBusyPorts("n2", map[int]string{8080: "container other"})
	res, err := svc.MoveWorkload(ctx, "shop/api/latest", "n2", false, false)
	if err == nil || res == nil || res.Decision == nil {
		t.Fatalf("err = %v, result = %+v; want a rejection with the scheduler's reasons", err, res)
	}
	if got, _ := svc.GetWorkload(ctx, "shop/api/latest"); got.NodeID != "n1" {
		t.Fatalf("a rejected move must change nothing, api is on %s", got.NodeID)
	}
	if _, err := svc.MoveWorkload(ctx, "shop/api/latest", "n2", true, false); err != nil {
		t.Fatalf("forced move: %v", err)
	}
}
