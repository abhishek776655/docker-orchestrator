package dockerutil

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestBuildxArgsKeepSecretsOutOfArgv(t *testing.T) {
	args := BuildxArgs(BuildxOptions{
		ContextDir: "/tmp/ctx",
		Dockerfile: "Dockerfile",
		Tag:        "proj-svc:latest",
		Platform:   "linux/amd64",
		NoCache:    true,
		Labels:     map[string]string{"haco.build.id": "id1"},
		Args:       map[string]string{"SSH_PRIVATE_KEY": "-----BEGIN SECRET-----", "WORKING_ENV": "develop"},
		SSH:        map[string]string{"default": "/tmp/ssh/f-0"},
		Secrets:    map[string]string{"npmrc": "/tmp/ssh/f-1"},
	})
	joined := strings.Join(args, " ")
	for _, want := range []string{"buildx build", "--load", "--no-cache", "--platform linux/amd64", "--ssh default=/tmp/ssh/f-0", "--secret id=npmrc,src=/tmp/ssh/f-1", "--label haco.build.id=id1", "--build-arg SSH_PRIVATE_KEY", "--build-arg WORKING_ENV", "-t proj-svc:latest", "-f /tmp/ctx/Dockerfile"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	if strings.Contains(joined, "BEGIN SECRET") || strings.Contains(joined, "develop") {
		t.Fatalf("build-arg values must come from the environment, not argv: %v", args)
	}
	if args[len(args)-1] != "/tmp/ctx" || !slices.Contains(args, "--pull") {
		t.Fatalf("context must be last and --pull set: %v", args)
	}
}

func TestWriteBuildFilesArePrivate(t *testing.T) {
	dir := t.TempDir() + "/s"
	ssh, secrets, err := WriteBuildFiles(dir, map[string][]byte{"default": []byte("k")}, map[string][]byte{"npmrc": []byte("tok")})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{ssh["default"], secrets["npmrc"]} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: err=%v mode=%v, want 0600", p, err, info)
		}
	}
	if b, _ := os.ReadFile(secrets["npmrc"]); string(b) != "tok" {
		t.Fatalf("secret content = %q", b)
	}
}

func TestTailBufferKeepsLastLines(t *testing.T) {
	b := &TailBuffer{Max: 2}
	_, _ = b.Write([]byte("one\ntwo\nthr"))
	_, _ = b.Write([]byte("ee\nfour"))
	if got := b.String(); got != "three\nfour" {
		t.Fatalf("tail = %q", got)
	}
}
