package controlplane

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
	"github.com/abhishek/hardware-aware-container-orchestrator/compose"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// TestResolveVolumeSnapshotsTarsUpUnpinnedDirectoryMounts covers the
// control-plane half of transferring a directory bind mount to whichever
// node actually runs it: a workload NOT pinned to this control-plane host
// gets its directory volume tarred up and stashed (HostPath cleared,
// RemoteDirKey set) so the assigned node can fetch and extract it; a
// workload that IS pinned here keeps its real, live HostPath untouched.
func TestResolveVolumeSnapshotsTarsUpUnpinnedDirectoryMounts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('hi')"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := agent.HostNodeID()

	plan := &compose.Plan{Workloads: []model.Workload{
		{
			Project: "demo", Service: "unpinned", Revision: "latest",
			Storage: model.StoragePolicy{Class: model.NodeLocalPersistent},
			Volumes: []model.VolumeMount{{ContainerPath: "/app", HostPath: dir}},
		},
		{
			Project: "demo", Service: "pinned", Revision: "latest",
			Storage: model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: host},
			Volumes: []model.VolumeMount{{ContainerPath: "/app", HostPath: dir}},
		},
	}}

	svc := &Service{}
	if err := svc.resolveVolumeSnapshots(plan); err != nil {
		t.Fatalf("resolveVolumeSnapshots: %v", err)
	}

	unpinned := plan.Workloads[0]
	if unpinned.Volumes[0].HostPath != "" {
		t.Fatalf("unpinned: HostPath = %q, want cleared", unpinned.Volumes[0].HostPath)
	}
	if unpinned.Volumes[0].RemoteDirKey == "" {
		t.Fatalf("unpinned: expected a RemoteDirKey")
	}
	data, ok := svc.FetchVolumeSnapshot(unpinned.Volumes[0].RemoteDirKey)
	if !ok || len(data) == 0 {
		t.Fatalf("expected a stashed, non-empty tarball for %q", unpinned.Volumes[0].RemoteDirKey)
	}

	pinned := plan.Workloads[1]
	if pinned.Volumes[0].HostPath != dir {
		t.Fatalf("pinned: HostPath = %q, want the real live path %q unchanged", pinned.Volumes[0].HostPath, dir)
	}
	if pinned.Volumes[0].RemoteDirKey != "" {
		t.Fatalf("pinned: expected no RemoteDirKey (uses the literal live mount instead)")
	}
}

// TestResolveVolumeSeedsStashesCandidateAndReplacesKey covers the
// control-plane half of one-time managed-volume seeding: a volume
// carrying a raw candidate path in SeedDirKey (compose.go's
// candidateSeedPath) gets that path tarred, stashed, and replaced with
// the actual fetchable stash key.
func TestResolveVolumeSeedsStashesCandidateAndReplacesKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ibdata1"), []byte("pretend data"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := &compose.Plan{Workloads: []model.Workload{{
		Project: "demo", Service: "mysql", Revision: "latest",
		Storage: model.StoragePolicy{Class: model.NodeLocalPersistent},
		Volumes: []model.VolumeMount{{ContainerPath: "/var/lib/mysql", SeedDirKey: dir}},
	}}}

	svc := &Service{}
	if err := svc.resolveVolumeSeeds(plan); err != nil {
		t.Fatalf("resolveVolumeSeeds: %v", err)
	}

	v := plan.Workloads[0].Volumes[0]
	if v.SeedDirKey == "" || v.SeedDirKey == dir {
		t.Fatalf("SeedDirKey = %q, want it replaced with a stash key (not the raw path, not empty)", v.SeedDirKey)
	}
	data, ok := svc.FetchVolumeSnapshot(v.SeedDirKey)
	if !ok || len(data) == 0 {
		t.Fatalf("expected a stashed, non-empty seed tarball for %q", v.SeedDirKey)
	}
}
