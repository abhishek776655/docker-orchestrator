package controlplane

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

func TestComposeDeployKeepsSchedulingPastAFailure(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	n := model.Node{ID: "n1", Address: "10.0.0.9", Architecture: "arm64", Labels: map[string]string{}, Capabilities: map[string]bool{},
		Availability: model.Available, HeartbeatAt: time.Now(),
		Allocatable: model.Resources{CPUMillis: 4000, MemoryBytes: 8 << 30}}
	if err := s.UpsertNode(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	svc := New(s)

	yaml := `
services:
  a:
    image: alpine
    deploy: {resources: {limits: {memory: 256M}}}
  huge:
    image: alpine
    deploy: {resources: {limits: {memory: 900G}}}
  z:
    image: alpine
    deploy: {resources: {limits: {memory: 256M}}}
`
	_, results, err := svc.ComposeDeploy(context.Background(), "kg", t.TempDir(), []byte(yaml), DeployOptions{})
	if err == nil || !strings.Contains(err.Error(), "1 of 3 services could not be scheduled") || !strings.Contains(err.Error(), "huge") {
		t.Fatalf("err = %v, want one failure naming huge", err)
	}
	placed := map[string]bool{}
	for _, r := range results {
		if r != nil && r.Workload != nil {
			placed[r.Workload.Service] = true
		}
	}
	if !placed["a"] || !placed["z"] {
		t.Fatalf("services after the failing one must still be scheduled; placed = %v", placed)
	}
}

func TestAddingAServiceDoesNotResubmitItsRunningDependencies(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	n := model.Node{ID: "n1", Address: "10.0.0.9", Architecture: "arm64", Labels: map[string]string{}, Capabilities: map[string]bool{},
		Availability: model.Available, HeartbeatAt: time.Now(), Allocatable: model.Resources{CPUMillis: 4000, MemoryBytes: 8 << 30}}
	if err := s.UpsertNode(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	svc := New(s)
	ctx := context.Background()
	const yaml = `
services:
  db:
    image: alpine
  api:
    image: alpine
    depends_on: [db]
`
	// db is up and running with a container.
	if _, _, err := svc.ComposeDeploy(ctx, "shop", t.TempDir(), []byte(yaml), DeployOptions{OnlyService: "db"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateStatus(ctx, "shop/db/latest", model.StatusReady, "", "container-1"); err != nil {
		t.Fatal(err)
	}

	// Adding api pulls db into the deploy only as a dependency.
	_, results, err := svc.ComposeDeploy(ctx, "shop", t.TempDir(), []byte(yaml), DeployOptions{OnlyService: "api", KeepRunning: []string{"db"}})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := svc.GetWorkload(ctx, "shop/db/latest")
	if db.Status != model.StatusReady || db.ContainerID != "container-1" {
		t.Fatalf("db = %s with container %q, want untouched (ready, container-1)", db.Status, db.ContainerID)
	}
	if api, err := svc.GetWorkload(ctx, "shop/api/latest"); err != nil || api.Status != model.StatusAssigned {
		t.Fatalf("api = %+v, err %v; want it added", api, err)
	}
	if len(results) != 1 {
		t.Fatalf("%d results, want only the new service", len(results))
	}

	// Without KeepRunning the old behavior stands: db is submitted again.
	if _, _, err := svc.ComposeDeploy(ctx, "shop", t.TempDir(), []byte(yaml), DeployOptions{OnlyService: "api"}); err != nil {
		t.Fatal(err)
	}
	db, _ = svc.GetWorkload(ctx, "shop/db/latest")
	if db.Status != model.StatusAssigned || db.ContainerID != "" {
		t.Fatalf("a normal deploy submits db again; got %s with container %q", db.Status, db.ContainerID)
	}
}
