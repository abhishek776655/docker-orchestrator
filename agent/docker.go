package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"golang.org/x/sys/unix"

	"github.com/abhishek/hardware-aware-container-orchestrator/dockerutil"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// Labels attached to every container this agent creates, so reconciliation
// can find them without parsing container names (SRS §6.3 "reconcile
// actual state without blindly duplicating workload instances").
const (
	managedLabel = "haco.managed"
	keyLabel     = "haco.workload.key"
	nodeLabel    = "haco.node.id"
)

// Docker wraps the subset of the Docker Engine API the M2 agent needs:
// capacity probing, image pull, and container lifecycle (SRS 6.2).
// Agents talk to Docker exclusively over this local client; the socket
// itself is never exposed to the LAN (SRS §11).
type Docker struct {
	cli     *client.Client
	nodeID  string // scopes managed-container labels/filters (see ManagedContainers)
	dataDir string // root for node-local persistent volumes (SRS §10)
}

// NewDocker builds a Docker client. nodeID scopes every container this
// agent creates and every ManagedContainers/StopAndRemove call to this
// node specifically — essential whenever multiple simulated nodes share
// one Docker daemon (this project's local dev/test setup, documented in
// the M3-M7 docs): without it, one node's orphan-cleanup pass can remove
// another node's live container, since labels without a node scope look
func NewDocker(nodeID, dataDir string) (*Docker, error) {
	cli, err := dockerutil.NewClient()
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Docker{cli: cli, nodeID: nodeID, dataDir: dataDir}, nil
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
// validated where possible"). It returns legacy `-v`-style bind strings
// (HostConfig.Binds), not the newer HostConfig.Mounts API: Docker
// Desktop's Mounts path has a documented bug where its bind-source
// existence check sometimes doesn't see a directory created moments
// earlier (surfaced by this exact workflow — create the volume dir, then
// immediately start a container into it), even though the same path
// mounts fine via `-v`/Binds.
func (d *Docker) prepareVolumes(w model.Workload) ([]string, []string, error) {
	if w.Storage.Class != model.NodeLocalPersistent {
		if len(w.Volumes) > 0 {
			return nil, nil, fmt.Errorf("volumes declared but storage class is %q, not node_local_persistent", w.Storage.Class)
		}
		return nil, nil, nil
	}

	var binds []string
	var warnings []string
	for _, v := range w.Volumes {
		hostPath := d.VolumeHostPath(w.Key(), v.ContainerPath)
		if err := os.MkdirAll(hostPath, 0o755); err != nil {
			return nil, nil, fmt.Errorf("create volume directory %s: %w", hostPath, err)
		}
		if pct, err := diskUsedPercent(hostPath); err == nil && pct > 90 {
			warnings = append(warnings, fmt.Sprintf("volume %s is %.0f%% full", hostPath, pct))
		}
		bind := hostPath + ":" + v.ContainerPath
		if v.ReadOnly {
			bind += ":ro"
		}
		binds = append(binds, bind)
	}
	return binds, warnings, nil
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
func (d *Docker) CapacityAndArchitecture(ctx context.Context) (model.Resources, string, error) {
	info, err := d.cli.Info(ctx)
	if err != nil {
		return model.Resources{}, "", fmt.Errorf("docker info: %w", err)
	}
	return model.Resources{
		CPUMillis:   int64(info.NCPU) * 1000,
		MemoryBytes: info.MemTotal,
	}, normalizeDockerArchitecture(info.Architecture), nil
}

func normalizeDockerArchitecture(architecture string) string {
	switch strings.ToLower(architecture) {
	case "amd64", "x86_64", "x86-64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return strings.ToLower(architecture)
	}
}

// CachedImages lists the image references already present on this node's
// Docker daemon, used as a scheduling hint (image cache locality, SRS
// §9.3) — never a hard requirement, since any node can still pull.
func (d *Docker) CachedImages(ctx context.Context) ([]string, error) {
	images, err := d.cli.ImageList(ctx, types.ImageListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	var tags []string
	for _, img := range images {
		tags = append(tags, img.RepoTags...)
	}
	return tags, nil
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
func (d *Docker) EnsureRunning(ctx context.Context, w model.Workload, extraHosts ...string) (containerID string, warnings []string, err error) {
	name := ContainerName(w.Key())

	binds, warnings, err := d.prepareVolumes(w)
	if err != nil {
		return "", nil, fmt.Errorf("prepare volumes: %w", err)
	}

	existing, err := d.cli.ContainerInspect(ctx, name)
	if err == nil {
		image, _, err := d.cli.ImageInspectWithRaw(ctx, w.Image)
		if err != nil {
			return "", warnings, fmt.Errorf("inspect workload image %s: %w", w.Image, err)
		}
		if !containerImageMatches(existing.Image, image.ID) || !containerHostsMatch(existing.HostConfig, extraHosts) {
			// The workload key (and therefore its container name) stays the
			// same across redeploys. A mutable tag such as :latest can now
			// point at a different image, so remove the old container before
			// creating one from the image the agent just pulled.
			if err := d.cli.ContainerRemove(ctx, existing.ID, types.ContainerRemoveOptions{Force: true}); err != nil {
				return "", warnings, fmt.Errorf("remove container using stale image: %w", err)
			}
		} else {
			if !existing.State.Running {
				if err := d.cli.ContainerStart(ctx, existing.ID, types.ContainerStartOptions{}); err != nil {
					return "", warnings, fmt.Errorf("restart existing container: %w", err)
				}
			}
			return existing.ID, warnings, nil
		}
	}
	if err != nil && !client.IsErrNotFound(err) {
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
				nodeLabel:    d.nodeID,
			},
		},
		&container.HostConfig{
			PortBindings: bindings,
			Binds:        binds,
			ExtraHosts:   extraHosts,
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

func containerImageMatches(containerImageID, desiredImageID string) bool {
	return containerImageID != "" && containerImageID == desiredImageID
}

func containerHostsMatch(hostConfig *container.HostConfig, expected []string) bool {
	if hostConfig == nil {
		return len(expected) == 0
	}
	return len(hostConfig.DNS) == 0 && len(hostConfig.DNSSearch) == 0 && slices.Equal(hostConfig.ExtraHosts, expected)
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
	f := filters.NewArgs(
		filters.Arg("label", managedLabel+"=true"),
		filters.Arg("label", nodeLabel+"="+d.nodeID),
	)
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
