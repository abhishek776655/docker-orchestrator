package build

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/compose"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

func TestConsumeDockerStreamReturnsEmbeddedBuildError(t *testing.T) {
	err := consumeDockerStream(strings.NewReader(`{"stream":"building\n"}{"error":"exec format error","errorDetail":{"message":"exec format error"}}`), "build")
	if err == nil || !strings.Contains(err.Error(), "exec format error") {
		t.Fatalf("consumeDockerStream() error = %v, want embedded daemon error", err)
	}
}

func TestValidateImagePlatformRejectsWrongBuiltArchitecture(t *testing.T) {
	err := validateImagePlatform(types.ImageInspect{Os: "linux", Architecture: "arm64"}, "linux/amd64")
	if err == nil || !strings.Contains(err.Error(), "linux/arm64") {
		t.Fatalf("validateImagePlatform() error = %v, want actual platform mismatch", err)
	}
}

func TestBuildPullsBaseForRequestedPlatform(t *testing.T) {
	contextDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte("FROM alpine:3.20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	transport := buildRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := "{}"
		switch {
		case req.URL.Path == "/v1.41/build":
			if got := req.URL.Query().Get("platform"); got != "linux/amd64" {
				t.Fatalf("build platform = %q, want linux/amd64", got)
			}
			if got := req.URL.Query().Get("pull"); got != "1" {
				t.Fatalf("build pull = %q, want 1 to refresh the platform-specific base", got)
			}
			body = `{"stream":"built"}`
		case strings.HasSuffix(req.URL.Path, "/images/registry/project-client:latest/json"):
			body = `{"Id":"sha256:amd64-image","Os":"linux","Architecture":"amd64"}`
		case strings.HasSuffix(req.URL.Path, "/images/registry/project-client/push"):
			body = `{"status":"pushed"}`
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
	svc := New(dockerClient, "registry")
	_, err = svc.Build(context.Background(), "project", "latest", compose.BuildSpec{
		Service:    "client",
		Context:    contextDir,
		Dockerfile: "Dockerfile",
		Platform:   "linux/amd64",
	}, false)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
}

type buildRoundTripFunc func(*http.Request) (*http.Response, error)

func (f buildRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestTarContextPreservesEmptyDirectories covers a real bug: a directory
// with no files of its own (e.g. Postgres's pg_notify, which must exist,
// empty, for the server to start at all) used to get silently dropped —
// TarContext only ever wrote an entry for the files it walked past, never
// for a directory itself, so an empty one left nothing in the tarball to
// recreate it on extraction. Fixed by emitting a tar.TypeDir header for
// every directory, not just ones containing files.
func TestTarContextPreservesEmptyDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "empty-subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "has-a-file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	tarball, err := TarContext(dir)
	if err != nil {
		t.Fatalf("TarContext: %v", err)
	}
	data, err := io.ReadAll(tarball)
	if err != nil {
		t.Fatalf("read tarball: %v", err)
	}

	var sawEmptyDir bool
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar entry: %v", err)
		}
		if strings.TrimSuffix(hdr.Name, "/") == "empty-subdir" {
			if hdr.Typeflag != tar.TypeDir {
				t.Fatalf("empty-subdir entry type = %v, want TypeDir", hdr.Typeflag)
			}
			sawEmptyDir = true
		}
	}
	if !sawEmptyDir {
		t.Fatalf("expected a tar entry for the empty subdirectory, found none")
	}
}
