package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"golang.org/x/sys/unix"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// Labels attached to every container this agent creates, so reconciliation
// can find them without parsing container names (SRS §6.3 "reconcile
// actual state without blindly duplicating workload instances").
const (
	managedLabel = "haco.managed"
	keyLabel     = "haco.workload.key"
)

// Docker wraps the subset of the Docker Engine API the M2 agent needs:
// capacity probing, image pull, and container lifecycle (SRS 6.2).
// Agents talk to Docker exclusively over this local client; the socket
// itself is never exposed to the LAN (SRS §11).
type Docker struct {
	cli     *client.Client
	dataDir string // root for node-local persistent volumes (SRS §10)
}

func NewDocker(dataDir string) (*Docker, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Docker{cli: cli, dataDir: dataDir}, nil
}

// VolumeHostPath derives the on-disk directory for one of a workload's
// node-local persistent mounts, deterministically from the workload key
// and container path — never from a user-supplied host path. The same
// workload always resolves to the same directory, so recreating its
// container (after a crash, an image update, or an agent restart) reuses
// the existing data instead of starting from empty (SRS FR-016, FR-017).
func (d *Docker) VolumeHostPath(workloadKey, containerPath string) string {
	sanitize := strings.NewReplacer("/", "_", ":", "_")
	return filepath.Join(d.dataDir, sanitize.Replace(workloadKey), sanitize.Replace(containerPath))
}

// prepareVolumes ensures each node-local persistent mount's host
// directory exists and reports a warning if the filesystem backing it is
// nearly full (SRS §10.2: "Storage capacity, mount permissions... must be
// validated where possible").
func (d *Docker) prepareVolumes(w model.Workload) ([]mount.Mount, []string, error) {
	if w.Storage.Class != model.NodeLocalPersistent {
		if len(w.Volumes) > 0 {
			return nil, nil, fmt.Errorf("volumes declared but storage class is %q, not node_local_persistent", w.Storage.Class)
		}
		return nil, nil, nil
	}

	var mounts []mount.Mount
	var warnings []string
	for _, v := range w.Volumes {
		hostPath := d.VolumeHostPath(w.Key(), v.ContainerPath)
		if err := os.MkdirAll(hostPath, 0o755); err != nil {
			return nil, nil, fmt.Errorf("create volume directory %s: %w", hostPath, err)
		}
		if pct, err := diskUsedPercent(hostPath); err == nil && pct > 90 {
			warnings = append(warnings, fmt.Sprintf("volume %s is %.0f%% full", hostPath, pct))
		}
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   hostPath,
			Target:   v.ContainerPath,
			ReadOnly: v.ReadOnly,
		})
	}
	return mounts, warnings, nil
}

func diskUsedPercent(path string) (float64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	total := float64(stat.Blocks) * float64(stat.Bsize)
	free := float64(stat.Bavail) * float64(stat.Bsize)
	if total == 0 {
		return 0, nil
	}
	return (total - free) / total * 100, nil
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
func (d *Docker) EnsureRunning(ctx context.Context, w model.Workload) (containerID string, warnings []string, err error) {
	name := ContainerName(w.Key())

	mounts, warnings, err := d.prepareVolumes(w)
	if err != nil {
		return "", nil, fmt.Errorf("prepare volumes: %w", err)
	}

	existing, err := d.cli.ContainerInspect(ctx, name)
	if err == nil {
		if !existing.State.Running {
			if err := d.cli.ContainerStart(ctx, existing.ID, types.ContainerStartOptions{}); err != nil {
				return "", warnings, fmt.Errorf("restart existing container: %w", err)
			}
		}
		return existing.ID, warnings, nil
	}
	if !client.IsErrNotFound(err) {
		return "", warnings, fmt.Errorf("inspect %s: %w", name, err)
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
			return "", warnings, fmt.Errorf("port %d: %w", p, err)
		}
		exposed[port] = struct{}{}
		bindings[port] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(p)}}
	}

	resp, err := d.cli.ContainerCreate(ctx,
		&container.Config{
			Image:        w.Image,
			Cmd:          w.Command,
			Env:          env,
			ExposedPorts: exposed,
			Labels: map[string]string{
				managedLabel: "true",
				keyLabel:     w.Key(),
			},
		},
		&container.HostConfig{
			PortBindings: bindings,
			Mounts:       mounts,
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
		return "", warnings, fmt.Errorf("create container: %w", err)
	}
	if err := d.cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return "", warnings, fmt.Errorf("start container: %w", err)
	}
	return resp.ID, warnings, nil
}

// ManagedContainer is one container this agent created, identified by the
// workload key it was created for.
type ManagedContainer struct {
	WorkloadKey string
	ContainerID string
}

// ManagedContainers lists every container this agent has created (via its
// labels), so the agent can reconcile them against the control plane's
// current assignment and remove any that no longer belong here (SRS §6.3,
// UC-08).
func (d *Docker) ManagedContainers(ctx context.Context) ([]ManagedContainer, error) {
	f := filters.NewArgs(filters.Arg("label", managedLabel+"=true"))
	containers, err := d.cli.ContainerList(ctx, types.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, fmt.Errorf("list managed containers: %w", err)
	}
	out := make([]ManagedContainer, 0, len(containers))
	for _, c := range containers {
		out = append(out, ManagedContainer{WorkloadKey: c.Labels[keyLabel], ContainerID: c.ID})
	}
	return out, nil
}

// IsRunning reports whether a workload's container currently exists and is
// running, by its deterministic name. Used to detect a container that
// disappeared or crashed after the agent already reported it ready, so it
// can be safely recreated (SRS §6.3 "safe retries").
func (d *Docker) IsRunning(ctx context.Context, workloadKey string) (bool, error) {
	info, err := d.cli.ContainerInspect(ctx, ContainerName(workloadKey))
	if client.IsErrNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.State.Running, nil
}

// StopAndRemove force-removes a container this agent no longer owns.
func (d *Docker) StopAndRemove(ctx context.Context, containerID string) error {
	return d.cli.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{Force: true})
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
