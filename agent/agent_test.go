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
