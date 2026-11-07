package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

func shutdownTestService(t *testing.T, nodes map[string]model.Availability) (*Service, *store.Store) {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for id, avail := range nodes {
		n := model.Node{ID: id, Address: "10.0.0.9", Architecture: "arm64", Labels: map[string]string{}, Capabilities: map[string]bool{}, Availability: avail, HeartbeatAt: time.Now()}
		if err := s.UpsertNode(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
	return New(s), s
}

func nodeAvailability(t *testing.T, s *store.Store, id string) model.Availability {
	t.Helper()
	nodes, err := s.ListNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.ID == id {
			return n.Availability
		}
	}
	t.Fatalf("node %s not found", id)
	return ""
}

// answerShutdowns plays every agent: confirms each node once it's asked.
func answerShutdowns(svc *Service, order chan<- string, stop <-chan struct{}) {
	answered := map[string]bool{}
	for {
		select {
		case <-stop:
			return
		default:
		}
		for _, id := range []string{"remote-1", agent.HostNodeID()} {
			if !answered[id] && svc.shutdownRequested(id) {
				answered[id] = true
				order <- id
				svc.confirmNodeShutdown(id, NodeShutdownResult{Stopped: []string{"demo/" + id + "/latest"}})
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestShutdownStopsRemoteNodesBeforeHostAndPausesThem(t *testing.T) {
	host := agent.HostNodeID()
	svc, s := shutdownTestService(t, map[string]model.Availability{"remote-1": model.Available, host: model.Available, "gone": model.Unavailable})
	order := make(chan string, 2)
	stop := make(chan struct{})
	defer close(stop)
	go answerShutdowns(svc, order, stop)

	report, err := svc.ShutdownCluster(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first, second := <-order, <-order; first != "remote-1" || second != host {
		t.Fatalf("shutdown order = %s, %s; want remote nodes before this host", first, second)
	}
	if len(report.Nodes) != 2 || len(report.Nodes["remote-1"].Stopped) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if _, ok := report.NotReached["gone"]; !ok {
		t.Fatalf("an unavailable node must be reported as not reached: %+v", report.NotReached)
	}
	if got := nodeAvailability(t, s, "remote-1"); got != model.Paused {
		t.Fatalf("remote-1 availability = %s, want paused", got)
	}
}

func TestShutdownReportsNodeThatNeverConfirms(t *testing.T) {
	old := nodeShutdownTimeout
	nodeShutdownTimeout = 50 * time.Millisecond
	t.Cleanup(func() { nodeShutdownTimeout = old })
	svc, _ := shutdownTestService(t, map[string]model.Availability{"silent": model.Available})
	report, err := svc.ShutdownCluster(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := report.NotReached["silent"]; !ok {
		t.Fatalf("report = %+v, want silent listed as not reached", report)
	}
}

func TestOnlyAskedNodeCanConfirmShutdown(t *testing.T) {
	svc, _ := shutdownTestService(t, nil)
	if svc.confirmNodeShutdown("intruder", NodeShutdownResult{}) {
		t.Fatal("a node that was never asked must not be able to confirm")
	}
}

func TestDeploysRefusedDuringShutdown(t *testing.T) {
	svc, _ := shutdownTestService(t, nil)
	called := false
	h := svc.refuseDuringShutdown(func(w http.ResponseWriter, r *http.Request) { called = true })
	if _, err := svc.ShutdownCluster(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/v1/compose/deploy", nil))
	if called || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, called = %v; want 503 without running the handler", rec.Code, called)
	}
	if _, err := svc.ShutdownCluster(context.Background()); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("second shutdown err = %v, want ErrShuttingDown", err)
	}
}

func TestReconcilerHoldsPausedNodeButFailsOverCrashedOne(t *testing.T) {
	svc, s := shutdownTestService(t, map[string]model.Availability{"paused-node": model.Available, "crashed-node": model.Available})
	ctx := context.Background()
	for _, n := range []string{"paused-node", "crashed-node"} {
		rec := store.WorkloadRecord{
			Workload: model.Workload{Project: "demo", Service: n, Revision: "latest", Image: "db:latest",
				Storage: model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: n}, DesiredState: model.DesiredRunning},
			Status: model.StatusReady, NodeID: n,
		}
		if err := s.CreateWorkload(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PauseNode(ctx, "paused-node"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	// A timeout of 1ns makes both heartbeats stale.
	if err := svc.reconcileOnce(ctx, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if got := nodeAvailability(t, s, "paused-node"); got != model.Paused {
		t.Fatalf("paused node became %s; a clean stop must not be treated as a crash", got)
	}
	paused, _ := svc.GetWorkload(ctx, "demo/paused-node/latest")
	if paused.Status != model.StatusReady {
		t.Fatalf("workload on paused node = %s (%s), want untouched ready", paused.Status, paused.StatusReason)
	}
	crashed, _ := svc.GetWorkload(ctx, "demo/crashed-node/latest")
	if crashed.Status != model.StatusFailed {
		t.Fatalf("workload on crashed node = %s, want today's failover (failed)", crashed.Status)
	}
	if err := s.Heartbeat(ctx, "paused-node", model.Thermal{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := nodeAvailability(t, s, "paused-node"); got != model.Available {
		t.Fatalf("returning agent left node %s, want available", got)
	}
}
