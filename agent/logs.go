package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types"
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
