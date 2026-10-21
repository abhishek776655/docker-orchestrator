package agent

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

func TestCaptureContainerLogStreamMergesAndBoundsOutput(t *testing.T) {
	var stream bytes.Buffer
	if _, err := stdcopy.NewStdWriter(&stream, stdcopy.Stdout).Write([]byte("stdout line\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := stdcopy.NewStdWriter(&stream, stdcopy.Stderr).Write([]byte("stderr line\n")); err != nil {
		t.Fatal(err)
	}

	got, truncated, err := captureContainerLogStream(&stream, 18)
	if err != nil {
		t.Fatalf("capture logs: %v", err)
	}
	if got != " line\nstderr line\n" {
		t.Fatalf("captured logs = %q, want bounded combined output", got)
	}
	if !truncated {
		t.Fatal("capture did not report that the log output was truncated")
	}
}

func TestCaptureContainerLogStreamPreservesCompleteOutput(t *testing.T) {
	var stream bytes.Buffer
	if _, err := stdcopy.NewStdWriter(&stream, stdcopy.Stdout).Write([]byte("client failed\n")); err != nil {
		t.Fatal(err)
	}

	got, truncated, err := captureContainerLogStream(&stream, 1024)
	if err != nil {
		t.Fatalf("capture logs: %v", err)
	}
	if got != "client failed\n" || truncated {
		t.Fatalf("capture = (%q, truncated=%v), want complete logs", got, truncated)
	}
}

func TestDockerContainerLogsRequestsTailAndDemultiplexesStreams(t *testing.T) {
	var stream bytes.Buffer
	if _, err := stdcopy.NewStdWriter(&stream, stdcopy.Stdout).Write([]byte("stdout line\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := stdcopy.NewStdWriter(&stream, stdcopy.Stderr).Write([]byte("stderr line\n")); err != nil {
		t.Fatal(err)
	}
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/containers/container-1/logs") {
			t.Fatalf("Docker request path = %q", req.URL.Path)
		}
		query, err := url.ParseQuery(req.URL.RawQuery)
		if err != nil {
			return nil, err
		}
		if query.Get("tail") != containerLogTailLines || query.Get("stdout") != "1" || query.Get("stderr") != "1" {
			t.Fatalf("Docker log query = %v", query)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(stream.Bytes())), Request: req}, nil
	})
	dockerClient, err := client.NewClientWithOpts(client.WithHost("http://docker.test"), client.WithVersion("1.41"), client.WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	d := &Docker{cli: dockerClient}
	got, err := d.ContainerLogs(context.Background(), "container-1")
	if err != nil {
		t.Fatalf("container logs: %v", err)
	}
	if got != "stdout line\nstderr line\n" {
		t.Fatalf("logs = %q, want merged stdout and stderr", got)
	}
}
