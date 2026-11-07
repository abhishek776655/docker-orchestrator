package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

// openTestStore opens a fresh SQLite database in a temp directory — a
// real file-backed database exercising the actual schema and driver, not
// an in-memory stand-in, but needing no external service to run. Each
// test gets its own file, so these can run in parallel or repeatedly
// without ever needing to be skipped.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testNode(id string) model.Node {
	return model.Node{
		ID:           id,
		Address:      "10.0.0.1",
		Architecture: "amd64",
		Labels:       map[string]string{},
		Capabilities: map[string]bool{},
		Allocatable:  model.Resources{CPUMillis: 4000, MemoryBytes: 8_000_000_000},
		Availability: model.Available,
		HeartbeatAt:  time.Now(),
	}
}

// TestListReadyServiceInstancesReturnsInternalPort covers the discovery
// contract this plan exists for: a ready instance resolves to its internal
// container port even when it publishes no host port at all.
func TestListReadyServiceInstancesReturnsInternalPort(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	node := testNode("node-dns-a")
	if err := s.UpsertNode(ctx, node); err != nil {
		t.Fatalf("upsert node: %v", err)
	}

	revision := time.Now().Format("20060102150405.000000000")
	wl := store.WorkloadRecord{
		Workload: model.Workload{
			Project:        "demo",
			Service:        "metaapi",
			Revision:       revision,
			Image:          "metaapi:latest",
			ContainerPorts: []int{5000},
			Storage:        model.StoragePolicy{Class: model.Ephemeral},
			DesiredState:   model.DesiredRunning,
		},
		Status: model.StatusReady,
		NodeID: node.ID,
	}
	if err := s.CreateWorkload(ctx, wl); err != nil {
		t.Fatalf("create workload: %v", err)
	}

	instances, err := s.ListReadyServiceInstances(ctx, "demo", "metaapi")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(instances))
	}
	got := instances[0]
	if got.ContainerPort != 5000 {
		t.Fatalf("container port = %d, want 5000", got.ContainerPort)
	}
	if got.Address != node.Address {
		t.Fatalf("address = %q, want %q", got.Address, node.Address)
	}
	if got.NodeID != node.ID {
		t.Fatalf("node id = %q, want %q", got.NodeID, node.ID)
	}
}

func TestActiveReservationsNameEachPortHolder(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	n := testNode("node-res")
	if err := s.UpsertNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	for _, svc := range []struct {
		name   string
		port   int
		status model.Status
	}{{"metaapi", 5600, model.StatusReady}, {"old", 7000, model.StatusStopped}} {
		wl := store.WorkloadRecord{
			Workload: model.Workload{Project: "demo", Service: svc.name, Revision: "latest", Image: "x",
				Constraints: model.Constraints{HostPorts: []int{svc.port}}, Storage: model.StoragePolicy{Class: model.Ephemeral}, DesiredState: model.DesiredRunning},
			Status: svc.status, NodeID: n.ID,
		}
		if err := s.CreateWorkload(ctx, wl); err != nil {
			t.Fatal(err)
		}
	}
	_, ports, err := s.ActiveReservations(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := ports[n.ID][5600]; got != "demo/metaapi/latest" {
		t.Fatalf("holder of 5600 = %q, want demo/metaapi/latest", got)
	}
	if _, held := ports[n.ID][7000]; held {
		t.Fatal("a stopped workload must not hold its port")
	}
}

func TestListProjectServiceTargetsIncludesPortlessService(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	node := testNode("node-portless")
	if err := s.UpsertNode(ctx, node); err != nil {
		t.Fatalf("upsert node: %v", err)
	}
	wl := store.WorkloadRecord{
		Workload: model.Workload{
			Project:      "demo",
			Service:      "db",
			Revision:     "latest",
			Image:        "postgres:14-alpine",
			Storage:      model.StoragePolicy{Class: model.Ephemeral},
			DesiredState: model.DesiredRunning,
		},
		Status: model.StatusReady,
		NodeID: node.ID,
	}
	if err := s.CreateWorkload(ctx, wl); err != nil {
		t.Fatalf("create workload: %v", err)
	}
	targets, err := s.ListProjectServiceTargets(ctx, "demo")
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	if len(targets) != 1 || targets[0].Service != "db" || targets[0].Status != model.StatusReady {
		t.Fatalf("targets = %+v, want the ready port-less db service", targets)
	}
}

// TestListReadyServiceInstancesExcludesNonReady covers the review focus:
// "A failed or waiting workload must disappear from DNS answers
// immediately."
func TestListReadyServiceInstancesExcludesNonReady(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	node := testNode("node-dns-b")
	if err := s.UpsertNode(ctx, node); err != nil {
		t.Fatalf("upsert node: %v", err)
	}

	wl := store.WorkloadRecord{
		Workload: model.Workload{
			Project:        "demo",
			Service:        "pending-svc",
			Revision:       time.Now().Format("20060102150405.000000000"),
			Image:          "pending:latest",
			ContainerPorts: []int{9000},
			Storage:        model.StoragePolicy{Class: model.Ephemeral},
			DesiredState:   model.DesiredRunning,
		},
		Status: model.StatusAssigned,
		NodeID: node.ID,
	}
	if err := s.CreateWorkload(ctx, wl); err != nil {
		t.Fatalf("create workload: %v", err)
	}

	instances, err := s.ListReadyServiceInstances(ctx, "demo", "pending-svc")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 0 {
		t.Fatalf("expected no instances for a non-ready workload, got %d", len(instances))
	}
}

func TestAssignedWorkloadsIncludesFailedWorkloadForLogInspection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	node := testNode("node-failed-assignment")
	if err := s.UpsertNode(ctx, node); err != nil {
		t.Fatalf("upsert node: %v", err)
	}
	revision := time.Now().Format("20060102150405.000000000")
	wl := store.WorkloadRecord{
		Workload: model.Workload{
			Project:      "demo",
			Service:      "failed-client",
			Revision:     revision,
			Image:        "client:latest",
			Storage:      model.StoragePolicy{Class: model.Ephemeral},
			DesiredState: model.DesiredRunning,
		},
		Status:       model.StatusFailed,
		StatusReason: "container exited",
		NodeID:       node.ID,
		ContainerID:  "failed-container-id",
	}
	if err := s.CreateWorkload(ctx, wl); err != nil {
		t.Fatalf("create failed workload: %v", err)
	}

	assignments, err := s.AssignedWorkloads(ctx, node.ID)
	if err != nil {
		t.Fatalf("list assignments: %v", err)
	}
	if len(assignments) != 1 {
		t.Fatalf("assignment count = %d, want 1 so failed container is retained", len(assignments))
	}
	if assignments[0].Status != model.StatusFailed || assignments[0].ContainerID != "failed-container-id" {
		t.Fatalf("failed assignment = %#v; want failed status and preserved container ID", assignments[0])
	}
}

func TestWorkloadLogsReturnsMostRecentlyCapturedTail(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	node := testNode("node-log-tail")
	if err := s.UpsertNode(ctx, node); err != nil {
		t.Fatalf("upsert node: %v", err)
	}
	revision := time.Now().Format("20060102150405.000000000")
	wl := store.WorkloadRecord{
		Workload: model.Workload{
			Project:      "demo",
			Service:      "logged-client",
			Revision:     revision,
			Image:        "client:latest",
			Storage:      model.StoragePolicy{Class: model.Ephemeral},
			DesiredState: model.DesiredRunning,
		},
		Status: model.StatusFailed,
		NodeID: node.ID,
	}
	if err := s.CreateWorkload(ctx, wl); err != nil {
		t.Fatalf("create workload: %v", err)
	}
	if err := s.AppendWorkloadLogs(ctx, wl.Key(), "first attempt logs"); err != nil {
		t.Fatalf("append first logs: %v", err)
	}
	if err := s.AppendWorkloadLogs(ctx, wl.Key(), "latest attempt logs"); err != nil {
		t.Fatalf("append latest logs: %v", err)
	}

	got, err := s.GetWorkloadLogs(ctx, wl.Key())
	if err != nil {
		t.Fatalf("get workload logs: %v", err)
	}
	if got != "latest attempt logs" {
		t.Fatalf("workload logs = %q, want latest attempt logs", got)
	}
}

func TestAgentJoinTokenIsOneTimeAndCredentialCanBeRevoked(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.CreateJoinToken(ctx, "hash-join", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ConsumeJoinToken(ctx, "hash-join", now); err != nil || !ok {
		t.Fatalf("first consume = %v, %v; want true, nil", ok, err)
	}
	if ok, err := s.ConsumeJoinToken(ctx, "hash-join", now); err != nil || ok {
		t.Fatalf("replay consume = %v, %v; want false, nil", ok, err)
	}
	if err := s.CreateJoinToken(ctx, "hash-expired", now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ConsumeJoinToken(ctx, "hash-expired", now); err != nil || ok {
		t.Fatalf("expired consume = %v, %v; want false, nil", ok, err)
	}
	if err := s.CreateAgentCredential(ctx, "node-a", "hash-agent"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.VerifyAgentCredential(ctx, "node-a", "hash-agent"); err != nil || !ok {
		t.Fatalf("credential verification = %v, %v; want true, nil", ok, err)
	}
	if ok, err := s.VerifyAgentCredential(ctx, "node-b", "hash-agent"); err != nil || ok {
		t.Fatalf("cross-node verification = %v, %v; want false, nil", ok, err)
	}
	if err := s.CreateAgentCredential(ctx, "node-a", "replacement-hash"); err == nil {
		t.Fatal("replacing an active credential unexpectedly succeeded")
	}
	if ok, err := s.VerifyAgentCredential(ctx, "node-a", "hash-agent"); err != nil || !ok {
		t.Fatalf("active credential after rejected replacement = %v, %v; want true, nil", ok, err)
	}
	if err := s.RevokeAgentCredential(ctx, "node-a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.VerifyAgentCredential(ctx, "node-a", "hash-agent"); err != nil || ok {
		t.Fatalf("revoked verification = %v, %v; want false, nil", ok, err)
	}
	if err := s.CreateAgentCredential(ctx, "node-a", "replacement-hash"); err != nil {
		t.Fatalf("rejoin after revocation: %v", err)
	}
	if ok, err := s.VerifyAgentCredential(ctx, "node-a", "replacement-hash"); err != nil || !ok {
		t.Fatalf("replacement after revocation = %v, %v; want true, nil", ok, err)
	}
}

func TestNodeLimitsCapAllocatableAndSurviveReRegistration(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	n := testNode("node-limit")
	n.Allocatable = model.Resources{CPUMillis: 8000, MemoryBytes: 16 << 30}
	if err := s.UpsertNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeLimits(ctx, n.ID, model.Resources{CPUMillis: 4000}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeDisplayName(ctx, n.ID, "Studio Mac"); err != nil {
		t.Fatal(err)
	}
	// A restarted agent registers again with its detected capacity.
	if err := s.UpsertNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := nodes[0]
	if got.Allocatable.CPUMillis != 4000 || got.Allocatable.MemoryBytes != 16<<30 {
		t.Fatalf("allocatable = %+v, want CPU capped to 4000 and memory uncapped", got.Allocatable)
	}
	if got.Detected.CPUMillis != 8000 || got.DisplayName != "Studio Mac" {
		t.Fatalf("detected = %+v, name = %q", got.Detected, got.DisplayName)
	}
	if err := s.SetNodeLimits(ctx, "missing", model.Resources{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestStartWorkloadRunsAStoppedOneOnItsNode(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	n := testNode("node-start")
	if err := s.UpsertNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	wl := store.WorkloadRecord{
		Workload: model.Workload{Project: "demo", Service: "api", Revision: "latest", Image: "x", Storage: model.StoragePolicy{Class: model.Ephemeral}, DesiredState: model.DesiredRunning},
		Status:   model.StatusReady, NodeID: n.ID,
	}
	if err := s.CreateWorkload(ctx, wl); err != nil {
		t.Fatal(err)
	}
	key := wl.Workload.Key()
	if err := s.StopWorkload(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := s.StartWorkload(ctx, key); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetWorkload(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusAssigned || got.DesiredState != model.DesiredRunning || got.NodeID != n.ID {
		t.Fatalf("after start: status %s desired %s node %s", got.Status, got.DesiredState, got.NodeID)
	}
	assigned, _ := s.AssignedWorkloads(ctx, n.ID)
	if len(assigned) != 1 {
		t.Fatalf("the node's agent should see it again, got %d assigned", len(assigned))
	}
}
