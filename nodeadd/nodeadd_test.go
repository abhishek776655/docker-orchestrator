package nodeadd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	uname   string
	failOn  string // substring of a script that should fail
	scripts []string
	copied  []string
}

func (f *fakeRunner) Run(_ context.Context, script string) (string, error) {
	f.scripts = append(f.scripts, script)
	if f.failOn != "" && strings.Contains(script, f.failOn) {
		return "", errors.New("boom")
	}
	if script == "uname -sm" {
		return f.uname, nil
	}
	return "", nil
}

func (f *fakeRunner) Copy(_ context.Context, local, remote string) error {
	f.copied = append(f.copied, local+" -> "+remote)
	return nil
}

func opts(resolved *[]Platform) Options {
	return Options{
		JoinToken: "tok'en",
		ResolveBinary: func(p Platform) (string, error) {
			*resolved = append(*resolved, p)
			return "/tmp/agent-bin", nil
		},
	}
}

func TestParseUname(t *testing.T) {
	for in, want := range map[string]Platform{
		"Darwin arm64\n": {"darwin", "arm64"},
		"Linux x86_64\n": {"linux", "amd64"},
		"Linux aarch64":  {"linux", "arm64"},
	} {
		got, err := ParseUname(in)
		if err != nil || got != want {
			t.Fatalf("ParseUname(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "FreeBSD amd64", "Linux riscv64", "Linux"} {
		if _, err := ParseUname(bad); err == nil {
			t.Fatalf("ParseUname(%q) should fail", bad)
		}
	}
}

func TestAddLinuxInstallsSystemdUserService(t *testing.T) {
	r := &fakeRunner{uname: "Linux x86_64\n"}
	var resolved []Platform
	if err := Add(context.Background(), r, opts(&resolved)); err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0] != (Platform{"linux", "amd64"}) {
		t.Fatalf("resolved = %v", resolved)
	}
	all := strings.Join(r.scripts, "\n")
	for _, want := range []string{
		"docker info",
		`join 'tok'\''en' --no-run`, // token is shell-quoted
		"haco-agent.service",
		"ExecStart=%h/.haco/bin/agent run",
		"systemctl --user enable haco-agent",
		"enable-linger",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("scripts missing %q", want)
		}
	}
	if len(r.copied) != 1 || !strings.HasSuffix(r.copied[0], "~/.haco/bin/agent.new") {
		t.Fatalf("copied = %v", r.copied)
	}
}

func TestAddMacInstallsLaunchAgent(t *testing.T) {
	r := &fakeRunner{uname: "Darwin arm64\n"}
	var resolved []Platform
	if err := Add(context.Background(), r, opts(&resolved)); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(r.scripts, "\n")
	for _, want := range []string{"co.haco.agent.plist", "launchctl bootstrap", "<key>KeepAlive</key><true/>", "Docker.app"} {
		if !strings.Contains(all, want) {
			t.Errorf("scripts missing %q", want)
		}
	}
	if strings.Contains(all, "systemctl") {
		t.Error("mac install must not use systemctl")
	}
}

func TestAddStopsBeforeCopyingWhenDockerIsMissing(t *testing.T) {
	r := &fakeRunner{uname: "Linux x86_64\n", failOn: "docker info"}
	var resolved []Platform
	err := Add(context.Background(), r, opts(&resolved))
	if err == nil || !strings.Contains(err.Error(), "Docker") {
		t.Fatalf("err = %v, want a Docker message", err)
	}
	if len(r.copied) != 0 || len(resolved) != 0 {
		t.Fatalf("nothing should be resolved or copied; copied=%v resolved=%v", r.copied, resolved)
	}
}

func TestAddRejectsUnsupportedTarget(t *testing.T) {
	r := &fakeRunner{uname: "FreeBSD amd64\n"}
	var resolved []Platform
	if err := Add(context.Background(), r, opts(&resolved)); err == nil || len(r.copied) != 0 {
		t.Fatalf("err = %v copied = %v", err, r.copied)
	}
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func releaseServer(t *testing.T, archive []byte, sum string) *httptest.Server {
	name := "haco_1.2.3_linux_arm64.tar.gz"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.2.3/checksums.txt":
			fmt.Fprintf(w, "%s  %s\n", sum, name)
		case "/v1.2.3/" + name:
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestResolveDownloadsAndVerifiesTheRelease(t *testing.T) {
	archive := tarGz(t, map[string]string{"README.md": "x", "agent": "AGENT-BYTES"})
	h := sha256.Sum256(archive)
	srv := releaseServer(t, archive, hex.EncodeToString(h[:]))
	defer srv.Close()

	// A platform that is never this machine's, so the local sibling is skipped.
	src := BinarySource{Version: "1.2.3", ReleaseBase: srv.URL, TempDir: t.TempDir(), Client: srv.Client()}
	path, err := src.Resolve(Platform{"linux", "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "AGENT-BYTES" {
		t.Fatalf("agent = %q", got)
	}
	if st, _ := os.Stat(path); st.Mode()&0o111 == 0 {
		t.Fatal("agent is not executable")
	}
}

func TestResolveRefusesAChecksumMismatch(t *testing.T) {
	archive := tarGz(t, map[string]string{"agent": "AGENT-BYTES"})
	srv := releaseServer(t, archive, strings.Repeat("0", 64))
	defer srv.Close()
	src := BinarySource{Version: "v1.2.3", ReleaseBase: srv.URL, TempDir: t.TempDir(), Client: srv.Client()}
	if _, err := src.Resolve(Platform{"linux", "arm64"}); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
}

func TestResolveExplicitAndMissingVersion(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "agent")
	os.WriteFile(bin, []byte("x"), 0o755)
	if p, err := (BinarySource{Explicit: bin}).Resolve(Platform{"linux", "arm64"}); err != nil || p != bin {
		t.Fatalf("explicit: %q %v", p, err)
	}
	if _, err := (BinarySource{Explicit: bin + "-nope"}).Resolve(Platform{"linux", "arm64"}); err == nil {
		t.Fatal("missing explicit binary should fail")
	}
	// Not this machine's platform, no version: tells the user what to pass.
	_, err := (BinarySource{}).Resolve(Platform{"linux", "riscv-not-real"})
	if err == nil || !strings.Contains(err.Error(), "--agent-binary") {
		t.Fatalf("err = %v", err)
	}
}
