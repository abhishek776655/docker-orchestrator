package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

func record(project, service, node string, status model.Status, dependsOn ...string) store.WorkloadRecord {
	return store.WorkloadRecord{
		Workload: model.Workload{Project: project, Service: service, Revision: "latest", Image: service + ":latest", DependsOn: dependsOn, DesiredState: model.DesiredRunning},
		Status:   status,
		NodeID:   node,
	}
}

func TestNodeReturningRecoversUnreachableWorkloadAndRestartsDependents(t *testing.T) {
	svc, s := shutdownTestService(t, map[string]model.Availability{"sleepy": model.Available, "steady": model.Available})
	ctx := context.Background()
	db := record("shop", "db", "sleepy", model.StatusReady)
	db.Storage = model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: "sleepy"}
	api := record("shop", "api", "steady", model.StatusReady, "db")
	web := record("shop", "web", "steady", model.StatusReady, "api")
	other := record("shop", "cache", "steady", model.StatusReady)
	for _, r := range []store.WorkloadRecord{db, api, web, other} {
		if err := s.CreateWorkload(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	// "sleepy" stops heartbeating while "steady" keeps going.
	time.Sleep(5 * time.Millisecond)
	if err := s.Heartbeat(ctx, "steady", model.Thermal{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileOnce(ctx, 3*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetWorkload(ctx, "shop/db/latest")
	if got.Status != model.StatusUnreachable || got.NodeID != "sleepy" {
		t.Fatalf("db = %s on %s, want unreachable and still assigned to sleepy", got.Status, got.NodeID)
	}

	// It answers again.
	if err := s.Heartbeat(ctx, "sleepy", model.Thermal{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.recoverUnreachable(ctx, "sleepy"); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.GetWorkload(ctx, "shop/db/latest")
	if got.Status != model.StatusAssigned || got.NodeID != "sleepy" {
		t.Fatalf("db = %s on %s, want assigned on sleepy so its agent adopts the container", got.Status, got.NodeID)
	}
	for _, dep := range []string{"api", "web"} {
		r, _ := svc.GetWorkload(ctx, "shop/"+dep+"/latest")
		if r.StatusReason != "restart requested" {
			t.Errorf("%s = %s (%s), want a restart because db was unreachable", dep, r.Status, r.StatusReason)
		}
	}
	cache, _ := svc.GetWorkload(ctx, "shop/cache/latest")
	if cache.Status != model.StatusReady {
		t.Errorf("cache does not depend on db and must be left alone, got %s", cache.Status)
	}
}

func TestStatelessWorkloadWaitsBeforeMovingToAnotherNode(t *testing.T) {
	svc, s := shutdownTestService(t, map[string]model.Availability{"sleepy": model.Available, "steady": model.Available})
	svc.SetRescheduleAfter(time.Hour)
	ctx := context.Background()
	if err := s.CreateWorkload(ctx, record("shop", "web", "sleepy", model.StatusReady)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := s.Heartbeat(ctx, "steady", model.Thermal{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileOnce(ctx, 3*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetWorkload(ctx, "shop/web/latest")
	if got.Status != model.StatusUnreachable || got.NodeID != "sleepy" {
		t.Fatalf("web = %s on %s, want unreachable and not yet moved", got.Status, got.NodeID)
	}

	svc.SetRescheduleAfter(time.Nanosecond)
	if err := s.Heartbeat(ctx, "steady", model.Thermal{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileOnce(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.GetWorkload(ctx, "shop/web/latest")
	if got.NodeID != "steady" {
		t.Fatalf("web on %s after the wait, want it moved to steady", got.NodeID)
	}
}

func TestOldFailedStatefulWorkloadIsRecoveredToo(t *testing.T) {
	svc, s := shutdownTestService(t, map[string]model.Availability{"n1": model.Available})
	ctx := context.Background()
	r := record("shop", "db", "n1", model.StatusFailed)
	r.StatusReason = "node n1 unavailable; stateful workload is not relocated without verified data access"
	if err := s.CreateWorkload(ctx, r); err != nil {
		t.Fatal(err)
	}
	// CreateWorkload may reset the reason; set it the way an old control plane did.
	if err := s.UpdateStatus(ctx, r.Key(), model.StatusFailed, r.StatusReason, ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.recoverUnreachable(ctx, "n1"); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetWorkload(ctx, r.Key())
	if got.Status != model.StatusAssigned {
		t.Fatalf("status = %s, want an old 'failed: node unavailable' workload resumed", got.Status)
	}
}
