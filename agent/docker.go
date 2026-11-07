package agent

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
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

// RemoveVolumes deletes every volume directory of one workload on this
// node. It refuses while a container for that workload exists, running or
// not, so data is never pulled from under a container.
func (d *Docker) RemoveVolumes(ctx context.Context, workloadKey string) error {
	managed, err := d.ManagedContainers(ctx)
	if err != nil {
		return err
	}
	for _, c := range managed {
		if c.WorkloadKey == workloadKey {
			return fmt.Errorf("a container for %s still exists on this node", workloadKey)
		}
	}
	sanitize := strings.NewReplacer("/", "_", ":", "_")
	dir := filepath.Join(d.dataDir, sanitize.Replace(workloadKey))
	if filepath.Dir(dir) != filepath.Clean(d.dataDir) {
		return fmt.Errorf("refusing to delete %s: outside the volume root", dir)
	}
	return os.RemoveAll(dir)
}

// ExtractTar writes a tar stream's contents into dst, creating it if
// needed — the agent-side half of a directory bind mount's content
// transfer (VolumeMount.RemoteDirKey): the control plane tars up the
// directory once at deploy time (build.TarContext), this unpacks it here
// on whichever node actually runs the workload. Re-extracting into the
// same directory on every (re)start is intentional — it keeps the local
// copy in sync with whatever was stashed for the current deploy, the same
// way PullImage re-pulls on every execute() rather than assuming a local
// copy is already current.
func ExtractTar(dst string, r io.Reader) error {
	// Self-heal a stale file left behind by an agent build that predates
	// this field (a container path that used to be a single-file bind
	// mount, say, now needs a directory) — os.MkdirAll fails if dst
	// already exists as a regular file.
	if info, err := os.Stat(dst); err == nil && !info.IsDir() {
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("remove stale file at %s: %w", dst, err)
		}
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		path := filepath.Join(dst, hdr.Name)
		if !strings.HasPrefix(path, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("tar entry %q escapes destination directory", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("write %s: %w", path, err)
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
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
		switch {
		case v.HostPath != "":
			// A literal directory host path compose.go resolved and
			// validated at plan time (x-haco-bind-host-path, a source-code
			// live-mount) — never a managed directory under d.dataDir.
			// Re-checked here too, since plan-time validation ran on
			// whichever process handled that request; this is the process
			// that actually starts the container.
			if _, err := os.Stat(v.HostPath); err != nil {
				return nil, nil, fmt.Errorf("bind mount host path %s: %w", v.HostPath, err)
			}
			hostPath = v.HostPath
		case v.FileContent != nil:
			// A single-file bind mount's content, carried with the
			// workload itself rather than a host-specific path (see
			// VolumeMount.FileContent) — materialize it here, on whichever
			// node actually ended up running this workload. Rewritten on
			// every (re)start so a redeploy with updated file content
			// (e.g. a changed config value) always takes effect, the same
			// as any other part of the workload spec.
			if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
				return nil, nil, fmt.Errorf("create directory for bind mount file %s: %w", hostPath, err)
			}
			// Self-heal a stale directory left behind by an agent build that
			// predates this field (the old default path for this exact
			// container path was an empty managed directory) — os.WriteFile
			// can't write a file where a directory already exists.
			if info, err := os.Stat(hostPath); err == nil && info.IsDir() {
				if err := os.RemoveAll(hostPath); err != nil {
					return nil, nil, fmt.Errorf("remove stale directory at bind mount file path %s: %w", hostPath, err)
				}
			}
			if err := os.WriteFile(hostPath, v.FileContent, 0o644); err != nil {
				return nil, nil, fmt.Errorf("write bind mount file %s: %w", hostPath, err)
			}
		default:
			if err := os.MkdirAll(hostPath, 0o755); err != nil {
				return nil, nil, fmt.Errorf("create volume directory %s: %w", hostPath, err)
			}
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

// RemoveImage deletes one image reference. Docker refuses an image that a
// container still uses; that is left as an error rather than forced.
func (d *Docker) RemoveImage(ctx context.Context, ref string) error {
	_, err := d.cli.ImageRemove(ctx, ref, types.ImageRemoveOptions{PruneChildren: true})
	return err
}

// PullImage pulls an image reference, draining the progress stream.
// architecture, when non-empty ("amd64"/"arm64"), requests that platform's
// manifest explicitly instead of letting Docker default to this daemon's
// own native platform — needed for a workload scheduled here via emulation
// (an arm64 node running an amd64-only image under Rosetta/qemu): without
// it, Docker silently pulls nothing for an image with no native-arch
// manifest, and container creation later fails with a confusing "no such
// image" instead of a clear pull error.
func (d *Docker) PullImage(ctx context.Context, ref, architecture string) error {
	opts := types.ImagePullOptions{}
	if architecture != "" {
		opts.Platform = "linux/" + architecture
	}
	rc, err := d.cli.ImagePull(ctx, ref, opts)
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer rc.Close()
	_, err = io.Copy(io.Discard, rc)
	return err
}

// HasImage reports whether ref already exists in this daemon's local image
// store — used to skip a redundant local build on every poll once a
// RemoteBuild workload's image already exists (the same idempotency
// PullImage gets for free from Docker's own pull-is-a-no-op-if-current
// behavior; a local build has no such shortcut, so the agent checks
// itself).
func (d *Docker) HasImage(ctx context.Context, ref string) bool {
	_, _, err := d.cli.ImageInspectWithRaw(ctx, ref)
	return err == nil
}

const buildIDLabel = "haco.build.id"

// ImageBuildID returns the haco.build.id label of a local image, or "" if
// the image is missing or was built without one.
func (d *Docker) ImageBuildID(ctx context.Context, ref string) string {
	img, _, err := d.cli.ImageInspectWithRaw(ctx, ref)
	if err != nil || img.Config == nil {
		return ""
	}
	return img.Config.Labels[buildIDLabel]
}

// BuildImageWithSSH builds through `docker buildx build` (BuildKit), the
// only builder that supports RUN --mount=type=ssh; the Engine API's legacy
// builder used by BuildImage rejects that syntax outright. sshKeys maps
// each SSH mount ID to its private key, written to a 0600 temp file that
// is removed when the build ends.
func (d *Docker) BuildImageWithSSH(ctx context.Context, tarball io.Reader, ref string, spec model.RemoteBuildSpec, sshKeys map[string]string) error {
	tmp, err := os.MkdirTemp("", "haco-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	contextDir := filepath.Join(tmp, "context")
	if err := ExtractTar(contextDir, tarball); err != nil {
		return fmt.Errorf("unpack build context: %w", err)
	}
	keyDir := filepath.Join(tmp, "ssh")
	if err := os.Mkdir(keyDir, 0o700); err != nil {
		return err
	}
	keyPaths := make(map[string]string, len(sshKeys))
	for id, key := range sshKeys {
		p := filepath.Join(keyDir, "key-"+strconv.Itoa(len(keyPaths)))
		if err := os.WriteFile(p, []byte(key), 0o600); err != nil {
			return err
		}
		keyPaths[id] = p
	}
	for _, id := range spec.SSHIDs {
		if _, ok := keyPaths[id]; !ok {
			return fmt.Errorf("control plane sent no key for build.ssh %q", id)
		}
	}

	dockerBin, err := dockerCLI()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, dockerBin, buildxArgs(contextDir, ref, spec, keyPaths)...)
	// Build-arg values travel via the environment (`--build-arg NAME` with
	// no value reads it from there) so secrets such as SSH_PRIVATE_KEY
	// never appear in the process list.
	cmd.Env = os.Environ()
	for k, v := range spec.Args {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out := &tailBuffer{max: 20}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buildx build %s: %w\n--- last build output ---\n%s", ref, err, out.String())
	}
	built, _, err := d.cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		return fmt.Errorf("inspect built image %s: %w", ref, err)
	}
	return validateBuiltPlatform(built, spec.Platform)
}

func buildxArgs(contextDir, ref string, spec model.RemoteBuildSpec, keyPaths map[string]string) []string {
	dockerfile := spec.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	args := []string{"buildx", "build", "--load", "--pull", "--progress=plain",
		"-f", filepath.Join(contextDir, dockerfile), "-t", ref}
	if spec.Platform != "" {
		args = append(args, "--platform", spec.Platform)
	}
	if spec.NoCache {
		args = append(args, "--no-cache")
	}
	if spec.BuildID != "" {
		args = append(args, "--label", buildIDLabel+"="+spec.BuildID)
	}
	ids := make([]string, 0, len(keyPaths))
	for id := range keyPaths {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		args = append(args, "--ssh", id+"="+keyPaths[id])
	}
	names := make([]string, 0, len(spec.Args))
	for k := range spec.Args {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		args = append(args, "--build-arg", k)
	}
	return append(args, contextDir)
}

// dockerCLI finds the docker binary, including Docker Desktop's bundled
// one for an agent started without it on PATH.
func dockerCLI() (string, error) {
	if p, err := exec.LookPath("docker"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/usr/local/bin/docker", "/opt/homebrew/bin/docker", "/Applications/Docker.app/Contents/Resources/bin/docker"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("docker CLI not found; build.ssh builds need `docker buildx` on this node")
}

// tailBuffer keeps the last max lines written to it.
type tailBuffer struct {
	max     int
	lines   []string
	partial string
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	parts := strings.Split(t.partial+string(p), "\n")
	t.partial = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		if l = strings.TrimSpace(l); l != "" {
			t.lines = append(t.lines, l)
		}
	}
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	lines := append([]string(nil), t.lines...)
	if s := strings.TrimSpace(t.partial); s != "" {
		lines = append(lines, s)
	}
	if len(lines) > t.max {
		lines = lines[len(lines)-t.max:]
	}
	return strings.Join(lines, "\n")
}

// BuildImage builds a RemoteBuild workload's image from a tarball fetched
// from the control plane (registry-optional builds, multi-machine case) —
// a plain Docker Engine API build, same mechanics as build.Service.Build
// but against this agent's own daemon, and never pushed anywhere (there is
// no registry in this flow by definition).
func (d *Docker) BuildImage(ctx context.Context, tarball io.Reader, ref string, spec model.RemoteBuildSpec) error {
	dockerfile, platform := spec.Dockerfile, spec.Platform
	args := map[string]*string{}
	for k, v := range spec.Args {
		v := v
		args[k] = &v
	}
	var labels map[string]string
	if spec.BuildID != "" {
		labels = map[string]string{buildIDLabel: spec.BuildID}
	}
	resp, err := d.cli.ImageBuild(ctx, tarball, types.ImageBuildOptions{
		Dockerfile: dockerfile,
		BuildArgs:  args,
		Tags:       []string{ref},
		Remove:     true,
		PullParent: true,
		Platform:   platform,
		NoCache:    spec.NoCache,
		Labels:     labels,
	})
	if err != nil {
		return fmt.Errorf("build %s: %w", ref, err)
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(resp.Body)
	// tail keeps the last few lines of Docker's own build log stream (what
	// `docker build` prints to the terminal) so a failure's error message
	// can show the actual command output — e.g. git/ssh's real stderr
	// ("Permission denied (publickey)", "Host key verification failed") —
	// instead of just "returned a non-zero code: N", which says nothing
	// about *why* the command failed.
	const tailLines = 20
	var tail []string
	for {
		var message struct {
			Stream      string `json:"stream"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := decoder.Decode(&message); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("read build %s output: %w", ref, err)
		}
		if message.Stream != "" {
			for _, line := range strings.Split(message.Stream, "\n") {
				if line = strings.TrimSpace(line); line != "" {
					tail = append(tail, line)
					if len(tail) > tailLines {
						tail = tail[1:]
					}
				}
			}
		}
		detail := message.ErrorDetail.Message
		if detail == "" {
			detail = message.Error
		}
		if detail != "" {
			if len(tail) > 0 {
				return fmt.Errorf("build %s: %s\n--- last build output ---\n%s", ref, detail, strings.Join(tail, "\n"))
			}
			return fmt.Errorf("build %s: %s", ref, detail)
		}
	}
	built, _, err := d.cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		return fmt.Errorf("inspect built image %s: %w", ref, err)
	}
	return validateBuiltPlatform(built, platform)
}

func validateBuiltPlatform(image types.ImageInspect, requested string) error {
	if requested == "" {
		return nil
	}
	parts := strings.SplitN(requested, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid requested platform %q", requested)
	}
	actual := strings.ToLower(image.Os + "/" + image.Architecture)
	expected := strings.ToLower(parts[0] + "/" + parts[1])
	if actual != expected {
		return fmt.Errorf("built image platform is %s, requested %s", actual, expected)
	}
	return nil
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
	// PortBindings carries the real container-port -> host-port pair for
	// every published port (independent remapping); a workload with none
	// set (e.g. submitted directly via the raw API rather than Compose)
	// falls back to the original host==container assumption using
	// Constraints.HostPorts alone.
	if len(w.PortBindings) > 0 {
		for _, pb := range w.PortBindings {
			containerPort, err := nat.NewPort("tcp", strconv.Itoa(pb.ContainerPort))
			if err != nil {
				return "", warnings, fmt.Errorf("container port %d: %w", pb.ContainerPort, err)
			}
			exposed[containerPort] = struct{}{}
			bindings[containerPort] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(pb.HostPort)}}
		}
	} else {
		for _, p := range w.Constraints.HostPorts {
			port, err := nat.NewPort("tcp", strconv.Itoa(p))
			if err != nil {
				return "", warnings, fmt.Errorf("port %d: %w", p, err)
			}
			exposed[port] = struct{}{}
			bindings[port] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(p)}}
		}
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
			Healthcheck: healthConfigFor(w.HealthCheck),
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

// healthConfigFor translates a workload's Compose-derived healthcheck into
// the Docker Engine API's own HealthConfig. nil when the workload has no
// compose-level healthcheck — ContainerCreate then inherits whatever
// HEALTHCHECK the image itself declares (Docker's own default when
// Config.Healthcheck is nil), so an image-baked-in healthcheck still
// works exactly as it did before this field existed; it's only a compose
// `healthcheck:` block that needs explicit translation.
func healthConfigFor(hc *model.HealthCheck) *container.HealthConfig {
	if hc == nil {
		return nil
	}
	return &container.HealthConfig{
		Test:        hc.Test,
		Interval:    hc.Interval,
		Timeout:     hc.Timeout,
		Retries:     hc.Retries,
		StartPeriod: hc.StartPeriod,
	}
}

// HealthStatus returns the container's Docker-reported health status
// (types.Starting, types.Healthy, or types.Unhealthy) and when it started,
// or status "" if it has no healthcheck configured at all — a workload
// with no model.Workload.HealthCheck and no image-baked-in one either.
func (d *Docker) HealthStatus(ctx context.Context, containerID string) (status string, startedAt time.Time, err error) {
	info, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", time.Time{}, err
	}
	if info.State != nil {
		startedAt, _ = time.Parse(time.RFC3339Nano, info.State.StartedAt)
	}
	if info.State == nil || info.State.Health == nil {
		return "", startedAt, nil
	}
	return info.State.Health.Status, startedAt, nil
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
	Running     bool
}

// StopContainer stops a container gracefully (SIGTERM, then SIGKILL after
// 30 s so databases get time to flush) and keeps it, so a later start
// reuses it as-is.
func (d *Docker) StopContainer(ctx context.Context, containerID string) error {
	timeout := 30
	return d.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
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
		out = append(out, ManagedContainer{WorkloadKey: c.Labels[keyLabel], ContainerID: c.ID, Running: c.State == "running"})
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

// ContainerMemory returns the current memory use, in bytes, of every
// running container this agent manages, keyed by workload key. It matches
// `docker stats`: usage minus reclaimable page cache.
func (d *Docker) ContainerMemory(ctx context.Context) (map[string]int64, error) {
	managed, err := d.ManagedContainers(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(managed))
	for _, c := range managed {
		if !c.Running || c.WorkloadKey == "" {
			continue
		}
		resp, err := d.cli.ContainerStatsOneShot(ctx, c.ContainerID)
		if err != nil {
			continue
		}
		var stats types.StatsJSON
		err = json.NewDecoder(resp.Body).Decode(&stats)
		resp.Body.Close()
		if err != nil {
			continue
		}
		out[c.WorkloadKey] = int64(memoryInUse(stats.MemoryStats))
	}
	return out, nil
}

// memoryInUse is the Docker CLI's own calculation (cgroup v1 and v2).
func memoryInUse(m types.MemoryStats) uint64 {
	if v, ok := m.Stats["total_inactive_file"]; ok && v < m.Usage {
		return m.Usage - v
	}
	if v, ok := m.Stats["inactive_file"]; ok && v < m.Usage {
		return m.Usage - v
	}
	return m.Usage
}
