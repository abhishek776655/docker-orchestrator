package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

func TestNeedsRemoteBuild(t *testing.T) {
	spec := model.RemoteBuildSpec{BuildID: "abc"}
	cases := []struct {
		name     string
		spec     model.RemoteBuildSpec
		hasImage bool
		localID  string
		want     bool
	}{
		{"missing image", spec, false, "", true},
		{"same inputs", spec, true, "abc", false},
		{"source changed", spec, true, "old", true},
		{"image built before build IDs existed", spec, true, "", true},
		{"legacy spec without build ID keeps old behavior", model.RemoteBuildSpec{}, true, "", false},
	}
	for _, c := range cases {
		if got := needsRemoteBuild(c.spec, c.hasImage, c.localID); got != c.want {
			t.Errorf("%s: needsRemoteBuild = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBuildxArgsKeepSecretsOutOfArgv(t *testing.T) {
	spec := model.RemoteBuildSpec{
		Dockerfile: "Dockerfile",
		Platform:   "linux/amd64",
		NoCache:    true,
		BuildID:    "id1",
		Args:       map[string]string{"SSH_PRIVATE_KEY": "-----BEGIN SECRET-----", "WORKING_ENV": "develop"},
		SSHIDs:     []string{"default"},
	}
	args := buildxArgs("/tmp/ctx", "proj-svc:latest", spec, map[string]string{"default": "/tmp/ssh/key-0"})
	joined := strings.Join(args, " ")
	for _, want := range []string{"buildx build", "--load", "--no-cache", "--platform linux/amd64", "--ssh default=/tmp/ssh/key-0", "--label haco.build.id=id1", "--build-arg SSH_PRIVATE_KEY", "--build-arg WORKING_ENV", "-t proj-svc:latest", "-f /tmp/ctx/Dockerfile"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	if strings.Contains(joined, "BEGIN SECRET") || strings.Contains(joined, "develop") {
		t.Fatalf("build-arg values must come from the environment, not argv: %v", args)
	}
	if args[len(args)-1] != "/tmp/ctx" {
		t.Fatalf("context dir must be the last arg, got %v", args)
	}
	if !slices.Contains(args, "--pull") {
		t.Fatalf("missing --pull: %v", args)
	}
}

func TestTailBufferKeepsLastLines(t *testing.T) {
	b := &tailBuffer{max: 2}
	_, _ = b.Write([]byte("one\ntwo\nthr"))
	_, _ = b.Write([]byte("ee\nfour"))
	if got := b.String(); got != "three\nfour" {
		t.Fatalf("tail = %q, want %q", got, "three\nfour")
	}
}
