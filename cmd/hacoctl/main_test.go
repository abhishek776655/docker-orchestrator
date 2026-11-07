package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLogsFetchesAndPrintsWorkloadLogTail(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/workloads/service-dns/client/latest/logs" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader("client failed to resolve metaapi\n")),
			Request:    r,
		}, nil
	})
	var output bytes.Buffer
	client := &http.Client{Transport: transport}
	callErr := fetchLogs(client, "http://controlplane", []string{"service-dns", "client", "latest"}, &output)
	if callErr != nil {
		t.Fatalf("logs command: %v", callErr)
	}
	if output.String() != "client failed to resolve metaapi\n" {
		t.Fatalf("command output = %q, want raw log text", output.String())
	}
}

func TestLogsDefaultsRevisionAndPassesTail(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/workloads/demo/api/latest/logs" || r.URL.Query().Get("tail") != "30" {
			t.Errorf("request = %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Haco-Logs-Source": []string{"live"}}, Body: io.NopCloser(strings.NewReader("a\nb\n")), Request: r}, nil
	})
	var out bytes.Buffer
	if err := fetchLogs(&http.Client{Transport: transport}, "http://cp", []string{"demo", "api", "--tail", "30"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "a\nb\n" {
		t.Fatalf("output = %q", out.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestUnpinnedPersistentServicesListsOnlyCandidatesForFresh covers the
// --fresh warning: only a node_local_persistent workload with no pin of
// its own is affected (and so worth warning about) — an explicitly pinned
// one, or an ephemeral one, ignores --fresh entirely.
func TestUnpinnedPersistentServicesListsOnlyCandidatesForFresh(t *testing.T) {
	var planned planView
	planned.Plan.Workloads = []struct {
		Service   string `json:"Service"`
		Image     string `json:"Image"`
		Resources struct {
			CPUMillis   int64 `json:"CPUMillis"`
			MemoryBytes int64 `json:"MemoryBytes"`
		} `json:"Resources"`
		Storage struct {
			Class        string `json:"Class"`
			PinnedNodeID string `json:"PinnedNodeID"`
		} `json:"Storage"`
		Constraints struct {
			HostPorts    []int  `json:"HostPorts"`
			PinnedNodeID string `json:"PinnedNodeID"`
		} `json:"Constraints"`
	}{
		{Service: "unpinned-db"},
		{Service: "pinned-db"},
		{Service: "cache"},
	}
	planned.Plan.Workloads[0].Storage.Class = "node_local_persistent"
	planned.Plan.Workloads[1].Storage.Class = "node_local_persistent"
	planned.Plan.Workloads[1].Storage.PinnedNodeID = "192-168-1-11"
	planned.Plan.Workloads[2].Storage.Class = "ephemeral"

	got := unpinnedPersistentServices(planned)
	if len(got) != 1 || got[0] != "unpinned-db" {
		t.Fatalf("unpinnedPersistentServices = %v, want [unpinned-db]", got)
	}
}
