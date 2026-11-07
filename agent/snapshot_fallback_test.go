package agent

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

func snapshotGoneAgent(t *testing.T) (*Agent, store.WorkloadRecord) {
	t.Helper()
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"error":"no stashed volume snapshot"}`)), Request: r}, nil
	})
	a := &Agent{
		cfg:    Config{NodeID: "node-a", ControlPlaneURL: "https://controlplane"},
		docker: &Docker{nodeID: "node-a", dataDir: t.TempDir()},
		http:   &http.Client{Transport: transport},
	}
	rec := store.WorkloadRecord{Workload: model.Workload{
		Project: "demo", Service: "api", Revision: "latest",
		Volumes: []model.VolumeMount{{ContainerPath: "/app", RemoteDirKey: "demo/api/latest:0"}},
	}}
	return a, rec
}

func TestFetchVolumeSnapshotsReusesLocalCopyWhenControlPlaneLostIt(t *testing.T) {
	a, rec := snapshotGoneAgent(t)
	local := a.docker.VolumeHostPath(rec.Key(), "/app")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "app.py"), []byte("deployed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.fetchVolumeSnapshots(context.Background(), rec); err != nil {
		t.Fatalf("fetchVolumeSnapshots = %v, want the existing local copy reused", err)
	}
	if got, _ := os.ReadFile(filepath.Join(local, "app.py")); string(got) != "deployed" {
		t.Fatalf("local copy changed: %q", got)
	}
}

func TestFetchVolumeSnapshotsFailsClearlyWithoutLocalCopy(t *testing.T) {
	a, rec := snapshotGoneAgent(t)
	err := a.fetchVolumeSnapshots(context.Background(), rec)
	if err == nil || !strings.Contains(err.Error(), "redeploy") {
		t.Fatalf("err = %v, want a clear redeploy message", err)
	}
}
