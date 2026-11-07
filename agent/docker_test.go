package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/docker/docker/client"
)

func TestEnsureRunningReplacesContainerWhenMutableImageTagChanges(t *testing.T) {
	removedOld := false
	createdNew := false
	var createdHostConfig struct {
		ExtraHosts []string `json:"ExtraHosts"`
		DNS        []string `json:"Dns"`
		DNSSearch  []string `json:"DnsSearch"`
	}
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := "{}"
		switch {
		case strings.HasSuffix(req.URL.Path, "/containers/haco-service-dns-client-latest/json"):
			body = `{"Id":"old-container","Image":"sha256:old-image","State":{"Running":true}}`
		case strings.HasSuffix(req.URL.Path, "/images/registry/service-dns-client:latest/json"):
			body = `{"Id":"sha256:new-image"}`
		case req.Method == http.MethodDelete && strings.HasSuffix(req.URL.Path, "/containers/old-container"):
			removedOld = true
			status = http.StatusNoContent
			body = ""
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/containers/create"):
			if !removedOld {
				t.Fatal("created replacement before removing stale container")
			}
			var request struct {
				HostConfig json.RawMessage `json:"HostConfig"`
			}
			if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(request.HostConfig, &createdHostConfig); err != nil {
				t.Fatal(err)
			}
			createdNew = true
			body = `{"Id":"new-container","Warnings":[]}`
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/containers/new-container/start"):
			status = http.StatusNoContent
			body = ""
		default:
			t.Fatalf("unexpected Docker API request: %s %s", req.Method, req.URL)
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	dockerClient, err := client.NewClientWithOpts(
		client.WithHost("http://docker.test"),
		client.WithVersion("1.41"),
		client.WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		t.Fatal(err)
	}
	d := &Docker{cli: dockerClient, nodeID: "node-b"}
	w := model.Workload{
		Project:  "service-dns",
		Service:  "client",
		Revision: "latest",
		Image:    "registry/service-dns-client:latest",
	}

	got, _, err := d.EnsureRunning(context.Background(), w, "metaapi:192.168.1.11")
	if err != nil {
		t.Fatalf("EnsureRunning() error = %v", err)
	}
	if got != "new-container" || !removedOld || !createdNew {
		t.Fatalf("EnsureRunning() = %q, removed old = %t, created new = %t; want replacement", got, removedOld, createdNew)
	}
	if len(createdHostConfig.ExtraHosts) != 1 || createdHostConfig.ExtraHosts[0] != "metaapi:192.168.1.11" {
		t.Fatalf("container ExtraHosts = %v, want [metaapi:192.168.1.11]", createdHostConfig.ExtraHosts)
	}
	if len(createdHostConfig.DNS) != 0 || len(createdHostConfig.DNSSearch) != 0 {
		t.Fatalf("container DNS overrides = %v / %v, want default Docker DNS", createdHostConfig.DNS, createdHostConfig.DNSSearch)
	}
}

// TestExtractTarRoundTripsDirectoryContent covers the agent-side half of
// a directory bind mount's content transfer (VolumeMount.RemoteDirKey):
// what the control plane packages (build.TarContext, not imported here to
// avoid an import cycle back through compose) must come back out intact
// here, including a nested subdirectory, matching a real source tree's
// shape.
func TestExtractTarRoundTripsDirectoryContent(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range []struct{ name, content string }{
		{"top.txt", "top-level"},
		{"sub/nested.txt", "nested"},
	} {
		hdr := &tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "extracted")
	if err := ExtractTar(dst, &buf); err != nil {
		t.Fatalf("ExtractTar: %v", err)
	}

	top, err := os.ReadFile(filepath.Join(dst, "top.txt"))
	if err != nil || string(top) != "top-level" {
		t.Fatalf("top.txt = %q, err = %v, want \"top-level\"", top, err)
	}
	nested, err := os.ReadFile(filepath.Join(dst, "sub", "nested.txt"))
	if err != nil || string(nested) != "nested" {
		t.Fatalf("sub/nested.txt = %q, err = %v, want \"nested\"", nested, err)
	}
}

// TestExtractTarRejectsPathEscape covers the path-traversal guard: a
// malicious or corrupt tar entry trying to write outside dst must be
// rejected, not silently written there.
func TestExtractTarRejectsPathEscape(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{Name: "../escape.txt", Mode: 0o644, Size: int64(len("gotcha"))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("gotcha")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "dst")
	if err := ExtractTar(dst, &buf); err == nil {
		t.Fatalf("expected ExtractTar to reject a path-escaping tar entry")
	}
}

// TestPrepareVolumesHealsStaleDirectoryAtFileContentPath covers the other
// direction of the same real bug: an agent upgraded from a build that
// predates VolumeMount.FileContent may have left an empty managed
// directory at a container path that's now a single-file bind mount.
// os.WriteFile fails when that path already exists as a directory;
// prepareVolumes must remove it first rather than erroring out forever.
func TestPrepareVolumesHealsStaleDirectoryAtFileContentPath(t *testing.T) {
	d := &Docker{nodeID: "node-a", dataDir: t.TempDir()}
	w := model.Workload{
		Project: "demo", Service: "firebase", Revision: "latest",
		Storage: model.StoragePolicy{Class: model.NodeLocalPersistent},
		Volumes: []model.VolumeMount{{ContainerPath: "/app/firebase.json", FileContent: []byte("{}")}},
	}
	hostPath := d.VolumeHostPath(w.Key(), "/app/firebase.json")
	if err := os.MkdirAll(hostPath, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := d.prepareVolumes(w); err != nil {
		t.Fatalf("prepareVolumes: %v", err)
	}

	info, err := os.Stat(hostPath)
	if err != nil || info.IsDir() {
		t.Fatalf("hostPath = %+v, err = %v, want a regular file", info, err)
	}
	got, err := os.ReadFile(hostPath)
	if err != nil || string(got) != "{}" {
		t.Fatalf("content = %q, err = %v, want \"{}\"", got, err)
	}
}

// TestExtractTarHealsStaleFileAtDestination covers a real bug: an agent
// upgraded from a build that predates VolumeMount.RemoteDirKey/SeedDirKey
// may have left a single-file bind mount (VolumeMount.FileContent) at a
// container path that now needs a directory instead. os.MkdirAll alone
// fails when dst already exists as a regular file; ExtractTar must remove
// it first rather than erroring out forever.
func TestExtractTarHealsStaleFileAtDestination(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.WriteFile(dst, []byte("stale single-file content"), 0o644); err != nil {
		t.Fatal(err)
	}

	tarball := tarballWithOneFile(t, "real.txt", "real content")
	if err := ExtractTar(dst, bytes.NewReader(tarball)); err != nil {
		t.Fatalf("ExtractTar: %v", err)
	}

	info, err := os.Stat(dst)
	if err != nil || !info.IsDir() {
		t.Fatalf("dst = %+v, err = %v, want a directory", info, err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "real.txt"))
	if err != nil || string(got) != "real content" {
		t.Fatalf("real.txt = %q, err = %v, want \"real content\"", got, err)
	}
}

// TestExtractTarToleratesUnwritableExistingFiles covers a Linux-only
// failure: a root-run container wrote files (Python's __pycache__/*.pyc)
// into the bind-mounted volume dir, and the non-root agent's next
// re-extract died with "permission denied". A read-only file in a writable
// directory must be replaced; one in a directory the agent can't modify
// must be left alone rather than failing the whole extract.
func TestExtractTarToleratesUnwritableExistingFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	dst := filepath.Join(t.TempDir(), "vol")
	if err := os.MkdirAll(filepath.Join(dst, "locked"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"ro.txt", "locked/a.pyc"} {
		if err := os.WriteFile(filepath.Join(dst, f), []byte("old"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dst, "locked"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(dst, "locked"), 0o755) })

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range []string{"ro.txt", "locked/a.pyc", "other.txt"} {
		if err := tw.WriteHeader(&tar.Header{Name: f, Mode: 0o644, Size: 3}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte("new"))
	}
	tw.Close()

	if err := ExtractTar(dst, &buf); err != nil {
		t.Fatalf("ExtractTar: %v", err)
	}
	want := map[string]string{"ro.txt": "new", "locked/a.pyc": "old", "other.txt": "new"}
	for f, w := range want {
		got, err := os.ReadFile(filepath.Join(dst, f))
		if err != nil || string(got) != w {
			t.Errorf("%s = %q, err = %v, want %q", f, got, err, w)
		}
	}
}
