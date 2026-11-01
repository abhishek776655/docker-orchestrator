package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
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
