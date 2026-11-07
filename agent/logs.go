package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"strconv"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

const (
	containerLogTailLines = "1000"
	containerLogMaxBytes  = 64 * 1024
)

type logTailBuffer struct {
	buffer    bytes.Buffer
	maxBytes  int
	truncated bool
}

func (b *logTailBuffer) Write(p []byte) (int, error) {
	originalLen := len(p)
	if len(p) >= b.maxBytes {
		b.buffer.Reset()
		_, err := b.buffer.Write(p[len(p)-b.maxBytes:])
		b.truncated = true
		return originalLen, err
	}
	if excess := b.buffer.Len() + len(p) - b.maxBytes; excess > 0 {
		current := b.buffer.Bytes()
		remaining := append([]byte(nil), current[excess:]...)
		b.buffer.Reset()
		_, _ = b.buffer.Write(remaining)
		b.truncated = true
	}
	_, err := b.buffer.Write(p)
	return originalLen, err
}

// ContainerLogs returns the last 1000 lines, capped to 64 KiB so an
// application cannot flood the control-plane database with unbounded logs.
func (d *Docker) ContainerLogs(ctx context.Context, containerID string) (string, error) {
	stream, err := d.cli.ContainerLogs(ctx, containerID, types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Tail:       containerLogTailLines,
	})
	if err != nil {
		return "", fmt.Errorf("read logs for container %s: %w", containerID, err)
	}
	defer stream.Close()

	logs, truncated, err := captureContainerLogStream(stream, containerLogMaxBytes)
	if err != nil {
		return "", fmt.Errorf("decode logs for container %s: %w", containerID, err)
	}
	if truncated {
		logs = "[truncated: showing last 64 KiB of up to 1000 lines]\n" + logs
	}
	return logs, nil
}

func captureContainerLogStream(stream io.Reader, maxBytes int) (string, bool, error) {
	if maxBytes <= 0 {
		return "", false, fmt.Errorf("maximum log size must be positive")
	}
	buffer := &logTailBuffer{maxBytes: maxBytes}
	if _, err := stdcopy.StdCopy(buffer, buffer, stream); err != nil {
		return "", false, err
	}
	return buffer.buffer.String(), buffer.truncated, nil
}

type liveLogRequest struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Tail int    `json:"tail"`
}

type liveLogResponse struct {
	ID    string `json:"id"`
	Logs  string `json:"logs"`
	Error string `json:"error,omitempty"`
}

// serveLiveLogRequests answers every `hacoctl logs` request queued for this
// node with the matching container's current log tail.
func (a *Agent) serveLiveLogRequests(ctx context.Context) {
	var reqs []liveLogRequest
	if err := a.get(ctx, "/v1/agent/"+a.cfg.NodeID+"/log-requests", &reqs); err != nil {
		return // older control plane or transient error; heartbeat already reports connectivity
	}
	for _, req := range reqs {
		resp := liveLogResponse{ID: req.ID}
		logs, err := a.docker.WorkloadLogs(ctx, req.Key, req.Tail)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Logs = logs
		}
		if err := a.post(ctx, "/v1/agent/"+a.cfg.NodeID+"/log-response", resp, nil); err != nil {
			log.Printf("send live logs for %s: %v", req.Key, err)
		}
	}
}

// WorkloadLogs reads the last tail lines of a workload's container on this
// node, found by its deterministic name, capped like ContainerLogs.
func (d *Docker) WorkloadLogs(ctx context.Context, workloadKey string, tail int) (string, error) {
	if tail <= 0 {
		tail = 200
	}
	stream, err := d.cli.ContainerLogs(ctx, ContainerName(workloadKey), types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Tail:       strconv.Itoa(tail),
	})
	if client.IsErrNotFound(err) {
		return "", fmt.Errorf("no container for %s on this node (stopped, removed, or never started)", workloadKey)
	}
	if err != nil {
		return "", fmt.Errorf("read logs for %s: %w", workloadKey, err)
	}
	defer stream.Close()
	logs, truncated, err := captureContainerLogStream(stream, liveLogMaxBytes)
	if err != nil {
		return "", fmt.Errorf("decode logs for %s: %w", workloadKey, err)
	}
	if truncated {
		logs = "[truncated: showing the last 96 KiB]\n" + logs
	}
	return logs, nil
}

// JSON escaping can grow this up to ~6x (ANSI colour codes become \u001b),
// which the control plane's 1 MiB response limit still covers.
const liveLogMaxBytes = 96 * 1024
