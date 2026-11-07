// Package controlplane implements the M2 control-plane API: node
// registration, workload submission (scheduled via the M1 simulator),
// status queries, and the agent's pull-based assignment feed.
package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
	"github.com/abhishek/hardware-aware-container-orchestrator/build"
	"github.com/abhishek/hardware-aware-container-orchestrator/compose"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/scheduler"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

// Service orchestrates the scheduler (M1) over persisted node/workload
// state (M2). Every call rebuilds a fresh Scheduler from the database so
// there is exactly one source of truth (SRS NFR-001).
type Service struct {
	store       *store.Store
	builder     *build.Service // nil disables build-based deploys (image-only workloads still work)
	joinManager *JoinManager

	// buildContexts holds a tarball per workload key for registry-optional
	// builds scheduled onto a node other than this control plane's own
	// host (RemoteBuild on the workload): there's no registry to pull from,
	// so the target agent fetches its build context from here and builds
	// the image itself. In-memory only, matching this project's single
	// control-plane-process design — a restart between deploy and the
	// agent's fetch would mean redeploying, an accepted limitation.
	buildContextsMu sync.Mutex
	buildContexts   map[string][]byte
	// buildSSH holds BuildKit SSH-mount keys (ssh ID -> private key) per
	// workload key, guarded by buildContextsMu. Memory only, on purpose:
	// key material must never reach the SQLite store.
	buildSSH map[string]map[string][]byte

	liveLogs liveLogRelay
	shutdown shutdownState
	renames  renameState
	usage    usageState
	// nodeReqs queues restarts and image deletions for agents' control polls.
	nodeReqs nodeRequests

	// volumeSnapshots holds a directory bind mount's content as a tarball,
	// keyed by workload-key + volume index — the same in-memory, fetch-
	// once idea as buildContexts, but for a directory mount
	// (VolumeMount.RemoteDirKey) a service isn't explicitly pinned to this
	// control-plane host for (resolveVolumeSnapshots), rather than a
	// Docker build context.
	volumeSnapshotsMu sync.Mutex
	volumeSnapshots   map[string][]byte
}

func New(s *store.Store) *Service { return &Service{store: s} }

// WithBuilder enables POST /v1/compose/deploy to build services that
// declare a `build:` block (SRS 5.1 "Build Service").
func (svc *Service) WithBuilder(b *build.Service) *Service {
	svc.builder = b
	return svc
}

func (svc *Service) WithJoinManager(m *JoinManager) *Service {
	svc.joinManager = m
	return svc
}

// ComposePlan parses and validates a Compose file without applying
// anything (SRS UC-02, FR-023), and dry-run schedules every workload
// against current node/reservation state so the plan shows where each
// service would land without persisting or starting anything.
func (svc *Service) ComposePlan(ctx context.Context, project, workingDir string, yaml []byte, onlyService string, pinHost *[]string, pinNodes map[string][]string, fresh bool, force []string) (*compose.Plan, []*model.Decision, error) {
	plan, err := compose.Load(ctx, project, workingDir, yaml)
	if err != nil {
		return nil, nil, err
	}
	if pins, ok := compose.MergePinOverride(pinHost, pinNodes); ok {
		compose.ApplyPinOverride(plan, pins)
	}
	compose.MarkForced(plan, force)
	if onlyService != "" {
		plan, err = filterToClosure(plan, onlyService)
		if err != nil {
			return nil, nil, err
		}
	}
	if !plan.Valid() {
		return plan, nil, nil
	}

	decisions, err := svc.dryRunSchedule(ctx, plan.Workloads, fresh)
	return plan, decisions, err
}

// filterToClosure narrows plan to onlyService plus every service it
// transitively depends on (walking DependsOn), so `hacoctl deploy
// --service X` (or `plan --service X`) runs only what X actually needs —
// not the whole Compose file — while still respecting its real dependency
// graph rather than just that one container in isolation.
func filterToClosure(plan *compose.Plan, onlyService string) (*compose.Plan, error) {
	byService := make(map[string]model.Workload, len(plan.Workloads))
	for _, wl := range plan.Workloads {
		byService[wl.Service] = wl
	}
	if _, ok := byService[onlyService]; !ok {
		return nil, fmt.Errorf("service %q not found in compose file", onlyService)
	}

	keep := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if keep[name] {
			return
		}
		keep[name] = true
		for _, dep := range byService[name].DependsOn {
			visit(dep)
		}
	}
	visit(onlyService)

	filtered := *plan
	filtered.Workloads = nil
	for _, wl := range plan.Workloads {
		if keep[wl.Service] {
			filtered.Workloads = append(filtered.Workloads, wl)
		}
	}
	filtered.Builds = nil
	for _, b := range plan.Builds {
		if keep[b.Service] {
			filtered.Builds = append(filtered.Builds, b)
		}
	}

	// Rejections/warnings/networking reasons from compose.Load are
	// computed over the WHOLE file; keep only what's actually relevant to
	// the filtered subset — an unrelated, non-deployed service's env_file
	// typo or port remap shouldn't block deploying just this one. A
	// per-service message is prefixed "service: ..."; a cross-service
	// graph message (host-port conflicts, cycles, undefined depends_on)
	// has no such prefix and is dropped here, then recomputed fresh
	// against just the filtered workloads below — a conflict with a
	// service that isn't actually being deployed alongside this one isn't
	// a real conflict.
	filtered.Rejections = filterMessagesByService(plan.Rejections, keep)
	filtered.Rejections = append(filtered.Rejections, compose.ValidateGraph(filtered.Workloads)...)
	filtered.Warnings = filterMessagesByService(plan.Warnings, keep)
	filtered.Networking.Reasons = filterMessagesByService(plan.Networking.Reasons, keep)
	if len(filtered.Networking.Reasons) == 0 {
		filtered.Networking.Mode = compose.NetworkModeLAN
		filtered.Networking.Supported = true
	}

	return &filtered, nil
}

// filterMessagesByService keeps a "service: ..." prefixed message only
// when that service is in keep. A message with no recognizable "name:"
// prefix (every cross-service graph message, and the one global
// configs/secrets warning) is dropped here rather than attributed to a
// guess; callers that need the cross-service checks recompute them fresh
// against the filtered workload set instead.
func filterMessagesByService(messages []string, keep map[string]bool) []string {
	var out []string
	for _, msg := range messages {
		name, _, ok := strings.Cut(msg, ":")
		if ok && keep[name] {
			out = append(out, msg)
		}
	}
	return out
}

// dryRunSchedule schedules every workload in order against a single
// in-memory Scheduler seeded from current reservations, without
// persisting anything — each call's reservation feeds the next, the same
// way sequential Submit calls would via the database.
func (svc *Service) dryRunSchedule(ctx context.Context, workloads []model.Workload, fresh bool) ([]*model.Decision, error) {
	nodes, err := svc.store.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	// Seed once, excluding every workload in this batch's own existing
	// reservation (so replanning an already-deployed service doesn't
	// double-count its current CPU/memory/ports against itself): Schedule()
	// itself reserves into the shared scheduler as it goes, so re-seeding
	// per workload here would double-count already-active reservations.
	keys := make([]string, len(workloads))
	for i, w := range workloads {
		keys[i] = w.Key()
	}
	cpuMem, ports, err := svc.store.ActiveReservationsExcluding(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("load reservations: %w", err)
	}
	sched := scheduler.New(nodes)
	for nodeID, res := range cpuMem {
		sched.SeedReservation(nodeID, res, ports[nodeID])
	}

	var decisions []*model.Decision
	for _, w := range workloads {
		if !fresh && w.Storage.Class == model.NodeLocalPersistent && w.Storage.PinnedNodeID == "" {
			// Mirror Submit()'s existing-pin reuse so planning a redeploy of
			// an unpinned service shows where it would actually land (its
			// existing volume's node), not a fresh pick — unless --fresh
			// is set, mirroring Submit's own fresh bypass, so `hacoctl plan
			// --fresh` can preview what `deploy --fresh` would actually do.
			if existing, err := svc.store.GetWorkload(ctx, w.Key()); err == nil {
				w.Storage.PinnedNodeID = existing.Storage.PinnedNodeID
			}
		}
		decision, err := sched.Schedule(w)
		decisions = append(decisions, decision)
		if err != nil {
			return decisions, fmt.Errorf("schedule %s: %w", w.Service, err)
		}
	}
	return decisions, nil
}

// DeployOptions carries hacoctl deploy's optional flags through the API.
type DeployOptions struct {
	// NoCache forces every build in this deploy to bypass Docker's build
	// cache, for when a service's tag already exists locally but its
	// source changed in a way the cache wouldn't detect.
	NoCache bool
	// OnlyService, when set, deploys just that service plus everything it
	// transitively depends on (see filterToClosure) instead of the whole
	// Compose file.
	OnlyService string
	// PinHost, when non-nil, pins the named services to this control-plane
	// host specifically (see compose.ApplyPinOverride, via MergePinOverride
	// combining this with PinNodes). nil (and an empty PinNodes) means no
	// override — the Compose file's own x-haco-* annotations (or their
	// defaults) decide affinity as usual. Set, either field frees every
	// other workload to schedule anywhere, even one compose.go's own
	// bind-mount heuristics would otherwise have pinned to host.
	PinHost *[]string
	// PinNodes pins each named service to an explicit node ID, for pinning
	// something to a worker node rather than the control-plane host.
	PinNodes map[string][]string
	// Force lists services whose PinNodes placement overrides the
	// scheduler's hard filters (see model.Constraints.Forced).
	Force []string
	// Fresh, when true, skips reusing any currently-unpinned service's
	// existing stored pin — the scheduler re-picks a node for it from
	// scratch, abandoning whatever data lives on its current node. Has no
	// effect on an explicitly pinned service (PinHost/PinNodes, or the
	// Compose file's own x-haco-pin-node/x-haco-pin-host).
	Fresh bool
}

// ComposeDeploy plans a Compose file, builds any services that need it,
// and submits every workload through the normal scheduler path. It stops
// at the first rejection — nothing is applied for an invalid plan
// (FR-003).
func (svc *Service) ComposeDeploy(ctx context.Context, project, workingDir string, yaml []byte, opts DeployOptions) (*compose.Plan, []*SubmitResult, error) {
	plan, err := compose.Load(ctx, project, workingDir, yaml)
	if err != nil {
		return nil, nil, err
	}
	if pins, ok := compose.MergePinOverride(opts.PinHost, opts.PinNodes); ok {
		compose.ApplyPinOverride(plan, pins)
	}
	compose.MarkForced(plan, opts.Force)
	if opts.OnlyService != "" {
		plan, err = filterToClosure(plan, opts.OnlyService)
		if err != nil {
			return nil, nil, err
		}
	}
	if !plan.Valid() {
		return plan, nil, fmt.Errorf("compose plan for %s has %d rejection(s)", project, len(plan.Rejections))
	}
	if !plan.Networking.Supported {
		return plan, nil, fmt.Errorf("compose plan recommends %s networking, which is not provisioned yet: %s", plan.Networking.Mode, strings.Join(plan.Networking.Reasons, "; "))
	}
	if err := svc.resolveVolumeSnapshots(plan); err != nil {
		return plan, nil, err
	}
	if err := svc.resolveVolumeSeeds(plan); err != nil {
		return plan, nil, err
	}

	images, remoteBuilds, err := svc.resolveBuilds(ctx, project, plan, opts.NoCache)
	if err != nil {
		return plan, nil, err
	}

	var results []*SubmitResult
	for _, wl := range plan.Workloads {
		if ref, ok := images[wl.Service]; ok {
			wl.Image = ref
			if rb, ok := remoteBuilds[wl.Service]; ok {
				wl.RemoteBuild = rb
				wl.SkipPull = true
			}
		}
		result, err := svc.Submit(ctx, wl, opts.Fresh)
		results = append(results, result)
		if err != nil {
			return plan, results, fmt.Errorf("schedule %s: %w", wl.Service, err)
		}
	}
	return plan, results, nil
}

// resolveBuilds determines each build: service's final image reference.
// With a registry configured, it builds and pushes centrally now (as
// before) so any node can pull it. With no registry, an image can only
// ever run on whichever Docker daemon actually builds it, so instead of
// building here it packages the build context now and attaches a
// RemoteBuildSpec to that service's workload: whichever agent the
// scheduler assigns it to fetches the context and builds the image itself,
// right before first use (registry-optional builds, multi-machine case —
// this needs no Docker access on the control plane at all, since
// build.TarContext is pure file I/O).
func (svc *Service) resolveBuilds(ctx context.Context, project string, plan *compose.Plan, noCache bool) (map[string]string, map[string]*model.RemoteBuildSpec, error) {
	images := map[string]string{}
	remote := map[string]*model.RemoteBuildSpec{}
	if len(plan.Builds) == 0 {
		return images, remote, nil
	}
	workloadByService := make(map[string]model.Workload, len(plan.Workloads))
	for _, wl := range plan.Workloads {
		workloadByService[wl.Service] = wl
	}
	registry := ""
	if svc.builder != nil {
		registry = svc.builder.Registry()
	}
	for _, b := range plan.Builds {
		if registry != "" {
			if len(b.SSH) > 0 {
				return images, remote, fmt.Errorf("service %s uses build.ssh, which needs BuildKit on the building node; start the control plane without --registry so the assigned agent builds it", b.Service)
			}
			if svc.builder == nil {
				return images, remote, fmt.Errorf("service %s requires a build but no builder is configured", b.Service)
			}
			if b.Platform == "" {
				// One central image is pulled by every node, so it can't be
				// "native" to each of them: build it for this machine and
				// only place the service on nodes that can run that.
				b.Platform = "linux/" + runtime.GOARCH
				requireArchitecture(plan, b.Service, runtime.GOARCH)
			}
			ref, err := svc.builder.Build(ctx, project, "latest", b, noCache)
			if err != nil {
				return images, remote, fmt.Errorf("build %s: %w", b.Service, err)
			}
			images[b.Service] = ref
			continue
		}
		wl, ok := workloadByService[b.Service]
		if !ok {
			continue
		}
		tarball, err := build.TarBuildContext(b.Context, b.Dockerfile)
		if err != nil {
			return images, remote, fmt.Errorf("package build context for %s: %w", b.Service, err)
		}
		data, err := io.ReadAll(tarball)
		if err != nil {
			return images, remote, fmt.Errorf("read build context for %s: %w", b.Service, err)
		}
		sshKeys, err := resolveBuildSSHKeys(b)
		if err != nil {
			return images, remote, err
		}
		spec := &model.RemoteBuildSpec{Dockerfile: b.Dockerfile, Args: b.Args, Platform: b.Platform, NoCache: noCache}
		for id := range sshKeys {
			spec.SSHIDs = append(spec.SSHIDs, id)
		}
		sort.Strings(spec.SSHIDs)
		spec.BuildID = buildID(spec, data)
		if noCache {
			// A no-cache deploy must rebuild even when nothing changed, so
			// its ID can never match the image already on the node.
			spec.BuildID += "-nocache-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		}
		images[b.Service] = fmt.Sprintf("%s-%s:%s", project, b.Service, wl.Revision)
		remote[b.Service] = spec
		svc.stashBuildContext(wl.Key(), data)
		svc.stashBuildSSH(wl.Key(), sshKeys)
	}
	return images, remote, nil
}

// requireArchitecture restricts a service to nodes able to run arch.
func requireArchitecture(plan *compose.Plan, service, arch string) {
	for i := range plan.Workloads {
		if plan.Workloads[i].Service == service && plan.Workloads[i].Constraints.Architecture == "" {
			plan.Workloads[i].Constraints.Architecture = arch
		}
	}
}

// resolveBuildSSHKeys loads the private key behind each build.ssh entry:
// an explicit `id=path` reads that file; compose's `default` (the builder
// host's ssh-agent under plain `docker compose`) uses SSH_PRIVATE_KEY from
// this control plane's environment, since an agent socket can't be
// forwarded to another node's build.
func resolveBuildSSHKeys(b compose.BuildSpec) (map[string][]byte, error) {
	if len(b.SSH) == 0 {
		return nil, nil
	}
	keys := make(map[string][]byte, len(b.SSH))
	for id, path := range b.SSH {
		if path != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("service %s: read build.ssh key %q: %w", b.Service, id, err)
			}
			keys[id] = data
			continue
		}
		key := os.Getenv("SSH_PRIVATE_KEY")
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("service %s: build.ssh %q has no key path and SSH_PRIVATE_KEY is not set in the control plane's environment", b.Service, id)
		}
		if !strings.HasSuffix(key, "\n") {
			key += "\n" // OpenSSH rejects a key file without a trailing newline
		}
		keys[id] = []byte(key)
	}
	return keys, nil
}

// buildID fingerprints everything that affects the built image.
func buildID(spec *model.RemoteBuildSpec, context []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "dockerfile=%s\nplatform=%s\n", spec.Dockerfile, spec.Platform)
	argKeys := make([]string, 0, len(spec.Args))
	for k := range spec.Args {
		argKeys = append(argKeys, k)
	}
	sort.Strings(argKeys)
	for _, k := range argKeys {
		fmt.Fprintf(h, "arg %s=%s\n", k, spec.Args[k])
	}
	for _, id := range spec.SSHIDs {
		fmt.Fprintf(h, "ssh %s\n", id)
	}
	h.Write(context)
	return hex.EncodeToString(h.Sum(nil))[:20]
}

func (svc *Service) stashBuildSSH(key string, keys map[string][]byte) {
	svc.buildContextsMu.Lock()
	defer svc.buildContextsMu.Unlock()
	if svc.buildSSH == nil {
		svc.buildSSH = map[string]map[string][]byte{}
	}
	if len(keys) == 0 {
		delete(svc.buildSSH, key)
		return
	}
	svc.buildSSH[key] = keys
}

// FetchBuildSSH returns the BuildKit SSH keys stashed for a workload key.
func (svc *Service) FetchBuildSSH(key string) (map[string][]byte, bool) {
	svc.buildContextsMu.Lock()
	defer svc.buildContextsMu.Unlock()
	keys, ok := svc.buildSSH[key]
	return keys, ok
}

// stashBuildContext holds a build-context tarball for an agent to fetch
// (see Service.buildContexts' doc comment for why this is in-memory only).
func (svc *Service) stashBuildContext(key string, tarball []byte) {
	svc.buildContextsMu.Lock()
	defer svc.buildContextsMu.Unlock()
	if svc.buildContexts == nil {
		svc.buildContexts = map[string][]byte{}
	}
	svc.buildContexts[key] = tarball
}

// FetchBuildContext returns the tarball stashed for a workload key, for
// the agent-facing endpoint that serves it to whichever node fetches it.
func (svc *Service) FetchBuildContext(key string) ([]byte, bool) {
	svc.buildContextsMu.Lock()
	defer svc.buildContextsMu.Unlock()
	data, ok := svc.buildContexts[key]
	return data, ok
}

// stashVolumeSnapshot holds a directory bind mount's tarball for an agent
// to fetch (see Service.volumeSnapshots' doc comment for why this is
// in-memory only).
func (svc *Service) stashVolumeSnapshot(key string, tarball []byte) {
	svc.volumeSnapshotsMu.Lock()
	defer svc.volumeSnapshotsMu.Unlock()
	if svc.volumeSnapshots == nil {
		svc.volumeSnapshots = map[string][]byte{}
	}
	svc.volumeSnapshots[key] = tarball
}

// FetchVolumeSnapshot returns the tarball stashed for a volume key, for
// the agent-facing endpoint that serves it to whichever node fetches it.
func (svc *Service) FetchVolumeSnapshot(key string) ([]byte, bool) {
	svc.volumeSnapshotsMu.Lock()
	defer svc.volumeSnapshotsMu.Unlock()
	data, ok := svc.volumeSnapshots[key]
	return data, ok
}

// resolveVolumeSnapshots tars up every directory bind mount in plan whose
// owning workload is NOT pinned to this control-plane host (the only
// place such a HostPath is actually valid), and stashes it for whichever
// node the workload lands on to fetch — the same transfer idea as a
// remote build context, just for a directory mount instead of a Docker
// build (compose.go's own doc comment on cases 2-4 explains why). A
// workload explicitly pinned here keeps its real, live HostPath as-is.
// Only meaningful at actual deploy time — a plan/build dry run has
// nothing to fetch it, so this is never called from ComposePlan or
// ComposeBuild.
func (svc *Service) resolveVolumeSnapshots(plan *compose.Plan) error {
	host := agent.HostNodeID()
	for i := range plan.Workloads {
		wl := &plan.Workloads[i]
		if wl.Storage.PinnedNodeID == host {
			continue
		}
		for j := range wl.Volumes {
			v := &wl.Volumes[j]
			if v.HostPath == "" {
				continue
			}
			tarball, err := build.TarContext(v.HostPath)
			if err != nil {
				return fmt.Errorf("package volume %s for %s: %w", v.ContainerPath, wl.Service, err)
			}
			data, err := io.ReadAll(tarball)
			if err != nil {
				return fmt.Errorf("read volume tarball %s for %s: %w", v.ContainerPath, wl.Service, err)
			}
			key := fmt.Sprintf("%s:%d", wl.Key(), j)
			svc.stashVolumeSnapshot(key, data)
			v.HostPath = ""
			v.RemoteDirKey = key
		}
	}
	return nil
}

// resolveVolumeSeeds converts every volume's candidate seed path
// (compose.go's candidateSeedPath, momentarily held in SeedDirKey as a raw
// path) into an actual stashed tarball the assigned agent can fetch — or
// clears it if there's nothing to seed. A distinct key namespace
// ("...:seed:N") from resolveVolumeSnapshots' RemoteDirKey keys, since a
// single volume could in principle need both in different deploys and
// they must never collide.
func (svc *Service) resolveVolumeSeeds(plan *compose.Plan) error {
	for i := range plan.Workloads {
		wl := &plan.Workloads[i]
		for j := range wl.Volumes {
			v := &wl.Volumes[j]
			if v.SeedDirKey == "" {
				continue
			}
			sourcePath := v.SeedDirKey
			v.SeedDirKey = ""
			tarball, err := build.TarContext(sourcePath)
			if err != nil {
				return fmt.Errorf("package volume seed %s for %s: %w", v.ContainerPath, wl.Service, err)
			}
			data, err := io.ReadAll(tarball)
			if err != nil {
				return fmt.Errorf("read volume seed tarball %s for %s: %w", v.ContainerPath, wl.Service, err)
			}
			key := fmt.Sprintf("%s:seed:%d", wl.Key(), j)
			svc.stashVolumeSnapshot(key, data)
			v.SeedDirKey = key
		}
	}
	return nil
}

// buildImages builds every build: service in plan centrally and
// immediately, regardless of registry/placement — used only by
// ComposeBuild ("just build it now"), where there's no scheduling decision
// to defer to, unlike resolveBuilds (used by ComposeDeploy).
func (svc *Service) buildImages(ctx context.Context, project string, plan *compose.Plan, noCache bool) (map[string]string, error) {
	built := map[string]string{}
	for _, b := range plan.Builds {
		if svc.builder == nil {
			return built, fmt.Errorf("service %s requires a build but no builder is configured", b.Service)
		}
		ref, err := svc.builder.Build(ctx, project, "latest", b, noCache)
		if err != nil {
			return built, fmt.Errorf("build %s: %w", b.Service, err)
		}
		built[b.Service] = ref
	}
	return built, nil
}

// BuildResult is one service's outcome from ComposeBuild.
type BuildResult struct {
	Service string `json:"service"`
	Image   string `json:"image"`
}

// ComposeBuild builds every build: service in a Compose file — optionally
// scoped to one service's dependency closure via opts.OnlyService, and
// forcing a cache-bypassing rebuild via opts.NoCache — without scheduling
// or starting anything. Useful for pre-warming images, or just checking a
// build succeeds, independent of whether you're ready to deploy.
func (svc *Service) ComposeBuild(ctx context.Context, project, workingDir string, yaml []byte, opts DeployOptions) (*compose.Plan, []BuildResult, error) {
	plan, err := compose.Load(ctx, project, workingDir, yaml)
	if err != nil {
		return nil, nil, err
	}
	if pins, ok := compose.MergePinOverride(opts.PinHost, opts.PinNodes); ok {
		compose.ApplyPinOverride(plan, pins)
	}
	if opts.OnlyService != "" {
		plan, err = filterToClosure(plan, opts.OnlyService)
		if err != nil {
			return nil, nil, err
		}
	}
	if !plan.Valid() {
		return plan, nil, fmt.Errorf("compose plan for %s has %d rejection(s)", project, len(plan.Rejections))
	}

	builtImages, err := svc.buildImages(ctx, project, plan, opts.NoCache)
	if err != nil {
		return plan, nil, err
	}
	results := make([]BuildResult, 0, len(builtImages))
	for _, b := range plan.Builds {
		if ref, ok := builtImages[b.Service]; ok {
			results = append(results, BuildResult{Service: b.Service, Image: ref})
		}
	}
	return plan, results, nil
}

// DeleteNode hard-removes a node record (and its credential), unlike
// revoke which only blocks future auth and leaves the row behind.
// DeleteNode refuses to hard-delete a node that still has a non-terminal
// workload assigned to it, unless force is set: the reconciler finds
// workloads needing rescue via an INNER JOIN against the nodes table
// (WorkloadsOnUnavailableNodes), so a workload whose node_id points at a
// row that no longer exists at all becomes invisible to it forever —
// stuck in whatever status it was in, never rescheduled. force is for the
// operator who's certain nothing there matters anymore (e.g. the node's
// gone for good and its workloads are already known-dead); it does not
// itself stop or reschedule anything, the orphaned rows just stay as they
// are, same as any other already-terminal workload.
func (svc *Service) DeleteNode(ctx context.Context, nodeID string, force bool) error {
	if !force {
		assigned, err := svc.store.AssignedWorkloads(ctx, nodeID)
		if err != nil {
			return fmt.Errorf("check assigned workloads: %w", err)
		}
		var active []string
		for _, w := range assigned {
			if w.Status != model.StatusFailed {
				active = append(active, w.Key())
			}
		}
		if len(active) > 0 {
			return fmt.Errorf("%w: node %q still has %d non-terminal workload(s) assigned (%s); stop them first or pass force", ErrNodeHasActiveWorkloads, nodeID, len(active), strings.Join(active, ", "))
		}
	}
	return svc.store.DeleteNode(ctx, nodeID)
}

// ErrNodeHasActiveWorkloads is DeleteNode's refusal when force isn't set
// and the node still has a non-terminal workload assigned — distinct from
// store.ErrNotFound (unknown node) so the API layer can map it to a
// different status code.
var ErrNodeHasActiveWorkloads = errors.New("node has active workloads")

// CordonNode blocks a node from receiving any new workload placement
// without touching what's already running there (see store.CordonNode).
func (svc *Service) CordonNode(ctx context.Context, nodeID string) error {
	return svc.store.CordonNode(ctx, nodeID)
}

// UncordonNode reverses CordonNode, making the node eligible for new
// placements again.
func (svc *Service) UncordonNode(ctx context.Context, nodeID string) error {
	return svc.store.UncordonNode(ctx, nodeID)
}

// RegisterNode upserts a node's identity and capacity.
func (svc *Service) RegisterNode(ctx context.Context, n model.Node) error {
	n.Availability = model.Available
	n.HeartbeatAt = time.Now()
	return svc.store.UpsertNode(ctx, n)
}

// Heartbeat refreshes a node's last-seen timestamp.
func (svc *Service) Heartbeat(ctx context.Context, nodeID string, thermal model.Thermal, cachedImages []string) error {
	return svc.store.Heartbeat(ctx, nodeID, thermal, cachedImages)
}

// ListNodes returns every known node.
func (svc *Service) ListNodes(ctx context.Context) ([]model.Node, error) {
	return svc.store.ListNodes(ctx)
}

// SubmitResult is returned to the API caller: the scheduling decision
// trail plus the persisted workload, so a rejection is still inspectable
// (SRS FR-007, FR-023).
type SubmitResult struct {
	Decision *model.Decision
	Workload *store.WorkloadRecord
}

// Submit schedules a workload against current node/reservation state and
// persists it as "assigned" on success.
// Submit schedules and persists one workload. fresh, when true, skips
// reusing an unpinned workload's existing stored pin (see below) —
// `hacoctl deploy --fresh` for rescheduling an entire project from
// scratch, abandoning whatever data lives on each currently-unpinned
// service's existing node. It has no effect on a workload that already
// carries an explicit pin (x-haco-pin-node/x-haco-pin-host in the Compose
// file, or --pin-host/--pin-node at deploy time) — only the "no pin at
// all, so reuse wherever it already landed" path is affected.
func (svc *Service) Submit(ctx context.Context, w model.Workload, fresh bool) (*SubmitResult, error) {
	nodes, err := svc.store.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	cpuMem, ports, err := svc.store.ActiveReservations(ctx, w.Key())
	if err != nil {
		return nil, fmt.Errorf("load reservations: %w", err)
	}

	if !fresh && w.Storage.Class == model.NodeLocalPersistent && w.Storage.PinnedNodeID == "" {
		// x-haco-place-anywhere: the compose file itself carries no pin, so
		// on a redeploy, reuse wherever this workload is already pinned
		// (its data lives there) instead of letting the scheduler pick a
		// fresh node and strand the existing volume.
		if existing, err := svc.store.GetWorkload(ctx, w.Key()); err == nil {
			w.Storage.PinnedNodeID = existing.Storage.PinnedNodeID
		}
	}

	sched := scheduler.New(nodes)
	for nodeID, res := range cpuMem {
		sched.SeedReservation(nodeID, res, ports[nodeID])
	}

	decision, schedErr := sched.Schedule(w)
	result := &SubmitResult{Decision: decision}
	if schedErr != nil {
		return result, schedErr
	}

	if w.DesiredState == "" {
		w.DesiredState = model.DesiredRunning
	}
	if w.Storage.Class == model.NodeLocalPersistent && w.Storage.PinnedNodeID == "" {
		// First-ever placement of an unpinned persistent-volume workload:
		// lock it to wherever it actually landed, the same way a storage
		// pin normally works, so its volume has a stable, discoverable home
		// from here on rather than being re-picked on every redeploy.
		w.Storage.PinnedNodeID = decision.Selected
	}
	if w.Storage.Class == model.NodeLocalPersistent && w.Storage.AllowFreshVolumeOnReschedule && w.Storage.PinnedNodeID != decision.Selected {
		// Schedule() relaxed affinity because the pinned node was down;
		// the pin itself moves to wherever it actually landed, which gets
		// a brand-new empty volume (see decision.Warnings for the notice).
		w.Storage.PinnedNodeID = decision.Selected
	}
	rec := store.WorkloadRecord{
		Workload: w,
		Status:   model.StatusAssigned,
		NodeID:   decision.Selected,
	}
	if err := svc.store.CreateWorkload(ctx, rec); err != nil {
		return result, fmt.Errorf("persist workload: %w", err)
	}
	result.Workload = &rec
	return result, nil
}

// GetWorkload returns one workload's current status.
func (svc *Service) GetWorkload(ctx context.Context, key string) (*store.WorkloadRecord, error) {
	return svc.store.GetWorkload(ctx, key)
}

// ListWorkloads returns every workload, optionally filtered to one project.
func (svc *Service) ListWorkloads(ctx context.Context, project string) ([]store.WorkloadRecord, error) {
	return svc.store.ListWorkloads(ctx, project)
}

// ListProjects lists every distinct project with a per-status workload
// count, for `hacoctl projects` — there's no need to run `hacoctl
// workloads` with no filter and eyeball the PROJECT column.
func (svc *Service) ListProjects(ctx context.Context) ([]store.ProjectSummary, error) {
	return svc.store.ListProjects(ctx)
}

// AssignedWorkloads is the agent's pull feed for one node.
func (svc *Service) AssignedWorkloads(ctx context.Context, nodeID string) ([]store.WorkloadRecord, error) {
	return svc.store.AssignedWorkloads(ctx, nodeID)
}

// StopWorkload sets a workload's desired state to stopped; the agent
// that owns it removes the container on its next assignment poll.
func (svc *Service) StopWorkload(ctx context.Context, key string) error {
	return svc.store.StopWorkload(ctx, key)
}

// DeleteWorkload hard-removes one workload's record (see
// store.DeleteWorkload's doc comment for why this is safe even for an
// actively-assigned workload, unlike DeleteNode).
func (svc *Service) DeleteWorkload(ctx context.Context, key string) error {
	return svc.store.DeleteWorkload(ctx, key)
}

// DeleteWorkloads hard-removes every workload record, or (project != "")
// just those under that project, returning how many were deleted.
func (svc *Service) DeleteWorkloads(ctx context.Context, project string) (int64, error) {
	return svc.store.DeleteWorkloads(ctx, project)
}

// ReportStatus records an agent-observed status transition (SRS 6.2).
func (svc *Service) ReportStatus(ctx context.Context, key string, status model.Status, reason, containerID string) error {
	return svc.store.UpdateStatus(ctx, key, status, reason, containerID)
}

// ReportWorkloadLogs stores a node agent's bounded container log tail.
func (svc *Service) ReportWorkloadLogs(ctx context.Context, key, logs string) error {
	return svc.store.AppendWorkloadLogs(ctx, key, logs)
}

// GetWorkloadLogs returns the latest captured log tail for a workload.
func (svc *Service) GetWorkloadLogs(ctx context.Context, key string) (string, error) {
	return svc.store.GetWorkloadLogs(ctx, key)
}

// ListEvents returns the most recent audit/history records (SRS FR-024).
func (svc *Service) ListEvents(ctx context.Context, limit int, project string) ([]store.Event, error) {
	return svc.store.ListEvents(ctx, limit, project)
}

// Metrics returns a minimal operational summary (SRS §13).
func (svc *Service) Metrics(ctx context.Context, project string) (*store.Metrics, error) {
	return svc.store.Metrics(ctx, project)
}

// ListServiceInstances resolves a logical service name to its current
// ready endpoints, from any node (SRS 8.1-8.2, UC-06, FR-010, FR-011).
func (svc *Service) ListServiceInstances(ctx context.Context, project, service string) ([]model.ServiceInstance, error) {
	return svc.store.ListReadyServiceInstances(ctx, project, service)
}

// ListProjectServices returns endpoint records for assigned, active services in a project.
func (svc *Service) ListProjectServices(ctx context.Context, project string) ([]model.ServiceInstance, error) {
	return svc.store.ListProjectServiceTargets(ctx, project)
}
