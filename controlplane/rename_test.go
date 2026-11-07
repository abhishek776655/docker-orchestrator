package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

func addPinnedWorkload(t *testing.T, s *store.Store, node string) {
	t.Helper()
	rec := store.WorkloadRecord{
		Workload: model.Workload{Project: "demo", Service: "db", Revision: "latest", Image: "postgres",
			Storage:      model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: node},
			Constraints:  model.Constraints{PinnedNodeID: node},
			DesiredState: model.DesiredRunning},
		Status: model.StatusReady, NodeID: node,
	}
	if err := s.CreateWorkload(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
}

// actAsAgent plays the node's agent: waits to be told, then commits (or
// refuses with reason).
func actAsAgent(svc *Service, node, reason string) {
	for i := 0; i < 2000; i++ {
		if newID := svc.pendingRenameFor(node); newID != "" {
			if reason != "" {
				svc.abortRename(node, errors.New(reason))
				return
			}
			_ = svc.commitRename(context.Background(), node, newID)
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestRenameNodeMovesEverythingInTheDatabase(t *testing.T) {
	svc, s := shutdownTestService(t, map[string]model.Availability{"192-168-1-12": model.Available})
	addPinnedWorkload(t, s, "192-168-1-12")
	go actAsAgent(svc, "192-168-1-12", "")

	if err := svc.RenameNode(context.Background(), "192-168-1-12", "office-mac-7f3a"); err != nil {
		t.Fatal(err)
	}
	if got := nodeAvailability(t, s, "office-mac-7f3a"); got != model.Paused {
		t.Fatalf("renamed node availability = %s, want paused until its agent reconnects", got)
	}
	wl, err := svc.GetWorkload(context.Background(), "demo/db/latest")
	if err != nil {
		t.Fatal(err)
	}
	if wl.NodeID != "office-mac-7f3a" || wl.Storage.PinnedNodeID != "office-mac-7f3a" || wl.Constraints.PinnedNodeID != "office-mac-7f3a" {
		t.Fatalf("workload still references the old node: node=%s storage=%s constraint=%s", wl.NodeID, wl.Storage.PinnedNodeID, wl.Constraints.PinnedNodeID)
	}
}

func TestRenameNodeRefusedByAgentChangesNothing(t *testing.T) {
	svc, s := shutdownTestService(t, map[string]model.Availability{"n1": model.Available})
	go actAsAgent(svc, "n1", "ID fixed by HACO_NODE_ID")
	err := svc.RenameNode(context.Background(), "n1", "n2")
	if err == nil || !strings.Contains(err.Error(), "HACO_NODE_ID") {
		t.Fatalf("err = %v, want the agent's reason", err)
	}
	if got := nodeAvailability(t, s, "n1"); got != model.Available {
		t.Fatalf("n1 availability = %s, want untouched", got)
	}
}

func TestRenameNodeRequiresRunningAgentAndFreeName(t *testing.T) {
	svc, _ := shutdownTestService(t, map[string]model.Availability{"up": model.Available, "taken": model.Available, "down": model.Unavailable})
	ctx := context.Background()
	if err := svc.RenameNode(ctx, "down", "new"); err == nil || !strings.Contains(err.Error(), "agent must be running") {
		t.Fatalf("offline node err = %v", err)
	}
	if err := svc.RenameNode(ctx, "up", "taken"); !errors.Is(err, store.ErrNodeIDTaken) {
		t.Fatalf("taken name err = %v", err)
	}
	if err := svc.RenameNode(ctx, "up", "bad name!"); err == nil {
		t.Fatal("invalid ID accepted")
	}
	if err := svc.RenameNode(ctx, "missing", "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing node err = %v", err)
	}
}

func TestRenameCommitOnlyForPendingRename(t *testing.T) {
	svc, _ := shutdownTestService(t, map[string]model.Availability{"n1": model.Available})
	if err := svc.commitRename(context.Background(), "n1", "n2"); err == nil {
		t.Fatal("a node must not be able to rename itself without a pending request")
	}
}
