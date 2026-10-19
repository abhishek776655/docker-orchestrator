package agent

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// Docker wraps the subset of the Docker Engine API the M2 agent needs:
// capacity probing, image pull, and container lifecycle (SRS 6.2).
// Agents talk to Docker exclusively over this local client; the socket
// itself is never exposed to the LAN (SRS §11).
type Docker struct {
	cli *client.Client
}

func NewDocker() (*Docker, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Docker{cli: cli}, nil
}

// Capacity reports host CPU/memory as seen by the Docker daemon, used as
// the node's allocatable resources until real hardware telemetry (M7)
// exists.
func (d *Docker) Capacity(ctx context.Context) (model.Resources, error) {
	info, err := d.cli.Info(ctx)
	if err != nil {
		return model.Resources{}, fmt.Errorf("docker info: %w", err)
	}
	return model.Resources{
		CPUMillis:   int64(info.NCPU) * 1000,
		MemoryBytes: info.MemTotal,
	}, nil
}

// PullImage pulls an image reference, draining the progress stream.
func (d *Docker) PullImage(ctx context.Context, ref string) error {
	rc, err := d.cli.ImagePull(ctx, ref, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer rc.Close()
	_, err = io.Copy(io.Discard, rc)
	return err
}

// ContainerName is the deterministic name for a workload's container, so
// the agent can recognize and reuse an already-running instance after a
// restart instead of creating a duplicate (SRS FR-009: idempotent /
// recoverable operations).
func ContainerName(key string) string {
	return "haco-" + strings.NewReplacer("/", "-").Replace(key)
}

// EnsureRunning creates and starts the workload's container if it does
// not already exist, or reuses it if it does (idempotent per SRS FR-009).
// Host ports are bound 1:1 to the same container port; the model has no
// separate container-port field yet, so this is an M2 simplification to
// revisit once the Compose importer (M4) supplies real port mappings.
func (d *Docker) EnsureRunning(ctx context.Context, w model.Workload) (containerID string, err error) {
	name := ContainerName(w.Key())

	existing, err := d.cli.ContainerInspect(ctx, name)
	if err == nil {
		if !existing.State.Running {
			if err := d.cli.ContainerStart(ctx, existing.ID, types.ContainerStartOptions{}); err != nil {
				return "", fmt.Errorf("restart existing container: %w", err)
			}
		}
		return existing.ID, nil
	}
	if !client.IsErrNotFound(err) {
		return "", fmt.Errorf("inspect %s: %w", name, err)
	}

	env := make([]string, 0, len(w.Env))
	for k, v := range w.Env {
		env = append(env, k+"="+v)
	}

	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, p := range w.Constraints.HostPorts {
		port, err := nat.NewPort("tcp", strconv.Itoa(p))
		if err != nil {
			return "", fmt.Errorf("port %d: %w", p, err)
		}
		exposed[port] = struct{}{}
		bindings[port] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(p)}}
	}

	resp, err := d.cli.ContainerCreate(ctx,
		&container.Config{
			Image:        w.Image,
			Env:          env,
			ExposedPorts: exposed,
		},
		&container.HostConfig{
			PortBindings: bindings,
			Resources: container.Resources{
				NanoCPUs: w.Resources.CPUMillis * 1_000_000,
				Memory:   w.Resources.MemoryBytes,
			},
		},
		&network.NetworkingConfig{},
		nil,
		name,
	)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	if err := d.cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("start container: %w", err)
	}
	return resp.ID, nil
}

// WaitRunning polls container state until it reports running or the
// context/timeout expires.
func (d *Docker) WaitRunning(ctx context.Context, containerID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		info, err := d.cli.ContainerInspect(ctx, containerID)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", containerID, err)
		}
		if info.State.Running {
			return nil
		}
		if info.State.Status == "exited" || info.State.Status == "dead" {
			return fmt.Errorf("container %s exited: %s", containerID, info.State.Error)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("container %s did not reach running within %s", containerID, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
