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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
