package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/docker/docker/client"
)

// loadFixture wires an Agent to a fake control plane and a fake Docker
// daemon through one transport. loadBody is what `/images/load` answers;
// present says whether the image exists afterwards.
func loadFixture(t *testing.T, loadBody string, present bool) (a *Agent, loaded *string, reported *map[string]string) {
	t.Helper()
	var got string
	rep := map[string]string{}
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, `{}`
		switch {
		case req.URL.Host == "controlplane" && req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/image-load/abc"):
			body = "TAR-ARCHIVE"
		case req.URL.Host == "controlplane" && strings.HasSuffix(req.URL.Path, "/image-loaded"):
			data, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(data, &rep)
			status, body = http.StatusNoContent, ""
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/images/load"):
			data, _ := io.ReadAll(req.Body)
			got = string(data)
			body = loadBody
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/images/myapp:dev/json"):
			if !present {
				status, body = http.StatusNotFound, `{"message":"No such image"}`
			}
		default:
			t.Errorf("unexpected request: %s %s%s", req.Method, req.URL.Host, req.URL.Path)
			status = http.StatusInternalServerError
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	cli, err := client.NewClientWithOpts(client.WithHost("http://docker.test"), client.WithVersion("1.41"), client.WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	a = &Agent{
		cfg:    Config{NodeID: "node-a", ControlPlaneURL: "http://controlplane"},
		docker: &Docker{cli: cli},
		http:   &http.Client{Transport: transport},
	}
	return a, &got, &rep
}

func TestLoadImagesStreamsTheArchiveIntoDockerAndReports(t *testing.T) {
	a, loaded, reported := loadFixture(t, `{"stream":"Loaded image: myapp:dev\n"}`, true)
	a.loadImages(context.Background(), []loadImage{{ID: "abc", Image: "myapp:dev"}})
	if *loaded != "TAR-ARCHIVE" {
		t.Fatalf("docker received %q", *loaded)
	}
	if (*reported)["id"] != "abc" || (*reported)["error"] != "" {
		t.Fatalf("reported %v", *reported)
	}
}

func TestLoadImagesReportsADockerLoadError(t *testing.T) {
	a, _, reported := loadFixture(t, `{"errorDetail":{"message":"no space left"},"error":"no space left"}`, false)
	a.loadImages(context.Background(), []loadImage{{ID: "abc", Image: "myapp:dev"}})
	if !strings.Contains((*reported)["error"], "no space left") {
		t.Fatalf("reported %v", *reported)
	}
}

func TestLoadImagesReportsWhenTheTagIsMissingAfterLoad(t *testing.T) {
	a, _, reported := loadFixture(t, `{"stream":"Loaded image ID: sha256:1\n"}`, false)
	a.loadImages(context.Background(), []loadImage{{ID: "abc", Image: "myapp:dev"}})
	if !strings.Contains((*reported)["error"], "myapp:dev") {
		t.Fatalf("reported %v", *reported)
	}
}
