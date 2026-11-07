package controlplane

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

func liveLogTestService(t *testing.T) *Service {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if err := s.UpsertNode(ctx, model.Node{ID: "node-b", Address: "10.0.0.2", Architecture: "arm64", Labels: map[string]string{}, Capabilities: map[string]bool{}, Availability: model.Available, HeartbeatAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rec := store.WorkloadRecord{
		Workload: model.Workload{Project: "demo", Service: "api", Revision: "latest", Image: "api:latest", Storage: model.StoragePolicy{Class: model.Ephemeral}, DesiredState: model.DesiredRunning},
		Status:   model.StatusReady,
		NodeID:   "node-b",
	}
	if err := s.CreateWorkload(ctx, rec); err != nil {
		t.Fatal(err)
	}
	return New(s)
}

func TestLiveLogsRelayedFromAssignedNode(t *testing.T) {
	svc := liveLogTestService(t)
	go func() {
		for {
			if reqs := svc.TakeLiveLogRequests("node-b"); len(reqs) > 0 {
				if svc.DeliverLiveLogs("node-a", LiveLogResponse{ID: reqs[0].ID, Logs: "spoofed"}) {
					t.Error("another node must not be able to answer node-b's request")
				}
				if reqs[0].Key != "demo/api/latest" || reqs[0].Tail != 50 {
					t.Errorf("request = %+v", reqs[0])
				}
				svc.DeliverLiveLogs("node-b", LiveLogResponse{ID: reqs[0].ID, Logs: "hello\n"})
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	logs, err := svc.RequestLiveLogs(context.Background(), "demo/api/latest", 50)
	if err != nil || logs != "hello\n" {
		t.Fatalf("logs = %q, err = %v", logs, err)
	}
	if reqs := svc.TakeLiveLogRequests("node-b"); len(reqs) != 0 {
		t.Fatalf("request left queued after answer: %+v", reqs)
	}
}

func TestLiveLogsTimeOutWhenNodeNeverAnswers(t *testing.T) {
	old := liveLogTimeout
	liveLogTimeout = 50 * time.Millisecond
	t.Cleanup(func() { liveLogTimeout = old })
	svc := liveLogTestService(t)
	_, err := svc.RequestLiveLogs(context.Background(), "demo/api/latest", 10)
	if !errors.Is(err, errLiveLogsUnavailable) {
		t.Fatalf("err = %v, want errLiveLogsUnavailable", err)
	}
	if reqs := svc.TakeLiveLogRequests("node-b"); len(reqs) != 0 {
		t.Fatalf("timed-out request still queued: %+v", reqs)
	}
}

func TestLiveLogsAgentErrorIsReported(t *testing.T) {
	svc := liveLogTestService(t)
	go func() {
		for {
			if reqs := svc.TakeLiveLogRequests("node-b"); len(reqs) > 0 {
				svc.DeliverLiveLogs("node-b", LiveLogResponse{ID: reqs[0].ID, Error: "no container for demo/api/latest"})
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	_, err := svc.RequestLiveLogs(context.Background(), "demo/api/latest", 10)
	if !errors.Is(err, errLiveLogsUnavailable) {
		t.Fatalf("err = %v, want errLiveLogsUnavailable", err)
	}
}
