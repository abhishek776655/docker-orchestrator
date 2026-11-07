package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/compose"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

func remoteBuildPlan(t *testing.T, ssh map[string]string) *compose.Plan {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &compose.Plan{
		Workloads: []model.Workload{{Project: "demo", Service: "api", Revision: "latest"}},
		Builds:    []compose.BuildSpec{{Service: "api", Context: dir, Dockerfile: "Dockerfile", Args: map[string]string{"A": "1"}, SSH: ssh}},
	}
}

func TestResolveBuildsStashesSSHKeyOutsideTheWorkload(t *testing.T) {
	t.Setenv("SSH_PRIVATE_KEY", "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----")
	svc := &Service{}
	_, remote, err := svc.resolveBuilds(context.Background(), "demo", remoteBuildPlan(t, map[string]string{"default": ""}), false)
	if err != nil {
		t.Fatalf("resolveBuilds: %v", err)
	}
	spec := remote["api"]
	if spec == nil || len(spec.SSHIDs) != 1 || spec.SSHIDs[0] != "default" {
		t.Fatalf("remote spec = %+v, want SSHIDs [default]", spec)
	}
	keys, ok := svc.FetchBuildSSH("demo/api/latest")
	if !ok || !strings.HasPrefix(string(keys["default"]), "-----BEGIN OPENSSH") || !strings.HasSuffix(string(keys["default"]), "\n") {
		t.Fatalf("stashed keys = %q, want the env key with a trailing newline", keys)
	}
}

func TestResolveBuildsRejectsDefaultSSHWithoutKey(t *testing.T) {
	t.Setenv("SSH_PRIVATE_KEY", "")
	svc := &Service{}
	_, _, err := svc.resolveBuilds(context.Background(), "demo", remoteBuildPlan(t, map[string]string{"default": ""}), false)
	if err == nil || !strings.Contains(err.Error(), "SSH_PRIVATE_KEY") {
		t.Fatalf("err = %v, want a clear SSH_PRIVATE_KEY error", err)
	}
}

func TestResolveBuildsBuildIDStableUnlessNoCache(t *testing.T) {
	plan := remoteBuildPlan(t, nil)
	svc := &Service{}
	_, first, err := svc.resolveBuilds(context.Background(), "demo", plan, false)
	if err != nil {
		t.Fatal(err)
	}
	_, second, _ := svc.resolveBuilds(context.Background(), "demo", plan, false)
	if first["api"].BuildID == "" || first["api"].BuildID != second["api"].BuildID {
		t.Fatalf("build IDs %q / %q, want identical non-empty IDs for identical inputs", first["api"].BuildID, second["api"].BuildID)
	}
	_, noCache, _ := svc.resolveBuilds(context.Background(), "demo", plan, true)
	if noCache["api"].BuildID == first["api"].BuildID || !noCache["api"].NoCache {
		t.Fatalf("no-cache spec = %+v, want NoCache and a fresh build ID", noCache["api"])
	}
	if err := os.WriteFile(filepath.Join(plan.Builds[0].Context, "app.py"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, changed, _ := svc.resolveBuilds(context.Background(), "demo", plan, false)
	if changed["api"].BuildID == first["api"].BuildID {
		t.Fatal("build ID unchanged after a source edit; the node would keep the stale image")
	}
}
