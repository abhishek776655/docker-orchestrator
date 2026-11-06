package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

func tarballWithOneFile(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestFetchVolumeSeedsExtractsOnlyWhenManagedDirMissing covers the
// one-time-seed contract: a workload's first run (no managed directory
// yet) fetches and extracts the seed; a later run, where the managed
// directory already exists, must never fetch it again — the managed copy
// is already the source of truth, even if it's since diverged from
// whatever the seed snapshot contained.
func TestFetchVolumeSeedsExtractsOnlyWhenManagedDirMissing(t *testing.T) {
	fetches := 0
	tarball := tarballWithOneFile(t, "ibdata1", "seed content")
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		fetches++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(tarball)), Request: r}, nil
	})

	dataDir := t.TempDir()
	a := &Agent{
		cfg:    Config{NodeID: "node-a", ControlPlaneURL: "https://controlplane"},
		docker: &Docker{nodeID: "node-a", dataDir: dataDir},
		http:   &http.Client{Transport: transport},
	}
	rec := store.WorkloadRecord{Workload: model.Workload{
		Project: "demo", Service: "mysql", Revision: "latest",
		Volumes: []model.VolumeMount{{ContainerPath: "/var/lib/mysql", SeedDirKey: "demo/mysql/latest:seed:0"}},
	}}

	if err := a.fetchVolumeSeeds(context.Background(), rec); err != nil {
		t.Fatalf("first fetchVolumeSeeds: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("fetches = %d, want 1 (first run, directory didn't exist)", fetches)
	}
	managedDir := a.docker.VolumeHostPath(rec.Key(), "/var/lib/mysql")
	seeded, err := os.ReadFile(filepath.Join(managedDir, "ibdata1"))
	if err != nil || string(seeded) != "seed content" {
		t.Fatalf("seeded file = %q, err = %v, want \"seed content\"", seeded, err)
	}

	// Simulate the managed volume having since diverged (real DB writes).
	if err := os.WriteFile(filepath.Join(managedDir, "ibdata1"), []byte("real data, not the seed"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := a.fetchVolumeSeeds(context.Background(), rec); err != nil {
		t.Fatalf("second fetchVolumeSeeds: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("fetches = %d, want still 1 (directory already exists, must not re-seed)", fetches)
	}
	untouched, err := os.ReadFile(filepath.Join(managedDir, "ibdata1"))
	if err != nil || string(untouched) != "real data, not the seed" {
		t.Fatalf("file = %q, err = %v, want the real data left untouched", untouched, err)
	}
}
