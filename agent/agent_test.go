package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
	"github.com/docker/docker/client"
)

func TestRegisterReportsDockerDaemonArchitecture(t *testing.T) {
	var registered map[string]any
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.HasSuffix(req.URL.Path, "/info"):
			body = `{"NCPU":2,"MemTotal":1024,"Architecture":"aarch64"}`
		case strings.HasSuffix(req.URL.Path, "/images/json"):
			body = `[]`
		case req.URL.Path == "/v1/nodes":
			data, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(data, &registered); err != nil {
				return nil, err
			}
			body = `{}`
		default:
			t.Fatalf("unexpected request path %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	dockerClient, err := client.NewClientWithOpts(client.WithHost("http://docker.test"), client.WithVersion("1.41"), client.WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{
		cfg:    Config{NodeID: "node-arm", Address: "192.168.1.20", ControlPlaneURL: "https://controlplane"},
		docker: &Docker{cli: dockerClient},
		http:   &http.Client{Transport: transport},
	}
	if err := a.register(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := registered["architecture"]; got != "arm64" {
		t.Fatalf("registered architecture = %v, want arm64", got)
	}
}

func TestSplitAssignmentsKeepsFailedWorkloadOwnedButDoesNotRetry(t *testing.T) {
	failed := store.WorkloadRecord{
		Workload: model.Workload{Project: "service-dns", Service: "client", Revision: "latest"},
		Status:   model.StatusFailed,
		NodeID:   "node-b",
	}
	running := store.WorkloadRecord{
		Workload: model.Workload{Project: "service-dns", Service: "metaapi", Revision: "latest"},
		Status:   model.StatusRunning,
		NodeID:   "node-b",
	}

	owned, executable := splitAssignments([]store.WorkloadRecord{failed, running})
	if !owned[failed.Key()] {
		t.Fatalf("failed workload %q is not retained as owned", failed.Key())
	}
	if len(executable) != 1 || executable[0].Key() != running.Key() {
		t.Fatalf("executable assignments = %v, want only %q", workloadKeys(executable), running.Key())
	}
}

func TestContainerImageMatchesDetectsUpdatedImageBehindSameTag(t *testing.T) {
	if containerImageMatches("sha256:old-image", "sha256:new-image") {
		t.Fatal("container with old image ID must not be reused after redeploy")
	}
	if !containerImageMatches("sha256:same-image", "sha256:same-image") {
		t.Fatal("container with the desired image ID should be reusable")
	}
}

func workloadKeys(records []store.WorkloadRecord) []string {
	keys := make([]string, 0, len(records))
	for _, rec := range records {
		keys = append(keys, rec.Key())
	}
	return keys
}
