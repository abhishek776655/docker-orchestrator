package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

// openTestStore connects to a disposable PostgreSQL instance named by
// HACO_TEST_POSTGRES_DSN and skips the test if it isn't set — these are
// integration tests against the real schema, not unit tests.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("HACO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("HACO_TEST_POSTGRES_DSN not set; skipping store integration test")
	}
	s, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(s.Close)
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
