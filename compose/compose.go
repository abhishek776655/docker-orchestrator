// Package compose implements the M4 Compose importer: parse a supported
// subset of a Docker Compose file, validate it, and produce a normalized
// workload graph plus any build work needed (SRS §7, FR-002, FR-003,
// UC-02). Unsupported or ambiguous configuration is reported as a
// rejection or warning — never silently dropped (SRS design principle,
// compose-compatibility-matrix.md).
package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// maxInlineBindFileBytes caps how large a single-file bind mount
// (compose.go's case 1) can be before it's embedded directly into the
// workload (VolumeMount.FileContent) and carried to whichever node runs
// it. Generous for what this case actually covers — small config/
// credential files — while still refusing something that would bloat the
// workload record (stored in SQLite, sent on every assignments poll) if a
// large file were mistakenly bind-mounted as a single file.
const maxInlineBindFileBytes = 1 << 20 // 1 MiB

// BuildSpec is the build work one service needs before it can be
// scheduled, when it has a `build:` block instead of a pre-built `image:`
// (SRS 7.1 build.context/dockerfile/args/platform).
type BuildSpec struct {
	Service    string
	Context    string
	Dockerfile string
	Args       map[string]string
	Platform   string
	// SSH maps each build.ssh ID to its host key path; "" means the
	// compose `default` entry.
	SSH map[string]string
}

// Plan is the importer's output: a dry-run deployment plan (SRS UC-02,
// FR-023) that can be inspected before anything is built or scheduled.
// A non-empty Rejections means the plan must not be applied (FR-003).
type Plan struct {
	Project    string                `json:"project"`
	Workloads  []model.Workload      `json:"workloads"`
	Builds     []BuildSpec           `json:"builds"`
	Networking NetworkRecommendation `json:"networking"`
	Warnings   []string              `json:"warnings"`
	Rejections []string              `json:"rejections"`
}

type NetworkMode string

const (
	NetworkModeLAN          NetworkMode = "lan"
	NetworkModeSwarmOverlay NetworkMode = "swarm_overlay"
)

// NetworkRecommendation explains which data path can preserve the
// service URLs in a Compose project. LAN mode relies on equal published
// and container ports; Swarm overlay mode is recommended when host routing
// cannot preserve those URLs, but is not provisioned by this prototype.
type NetworkRecommendation struct {
	Mode      NetworkMode `json:"mode"`
	Supported bool        `json:"supported"`
	Reasons   []string    `json:"reasons,omitempty"`
}

func (p *Plan) Valid() bool { return len(p.Rejections) == 0 }

// CanDeploy reports whether the plan is valid and the prototype has the
// network mode required to preserve its service URLs.
func (p *Plan) CanDeploy() bool {
	return p.Valid() && p.Networking.Supported
}

// Load parses and validates a Compose file's bytes into a Plan. workingDir
// is used to resolve relative build contexts and env_file paths.
func Load(ctx context.Context, project, workingDir string, yaml []byte) (*Plan, error) {
	details := types.ConfigDetails{
		WorkingDir:  workingDir,
		ConfigFiles: []types.ConfigFile{{Filename: "docker-compose.yml", Content: yaml}},
		Environment: interpolationEnvironment(workingDir),
	}

	proj, err := loader.LoadWithContext(ctx, details, loader.WithSkipValidation)
	if err != nil {
		return nil, fmt.Errorf("parse compose file: %w", err)
	}
	proj.Name = project


	plan := &Plan{
		Project: project,
		Networking: NetworkRecommendation{
			Mode:      NetworkModeLAN,
			Supported: true,
		},
	}

	if len(proj.Configs) > 0 || len(proj.Secrets) > 0 {
		plan.Warnings = append(plan.Warnings, "top-level configs/secrets are declared but not yet delivered to containers; pass equivalent values via environment for now")
	}

	names := make([]string, 0, len(proj.Services))
	for name := range proj.Services {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic plan output regardless of map order

	// Collected up front so convertService's bind-mount handling can check
	// a mount's source against every service's build context, not just its
	// own — a cross-service shared-library mount (service A bind-mounting
	// service B's source tree) is still a build context, just not A's own.
	var buildContexts []string
	// bindSourceUsers counts, across every service, how many distinct
	// services bind-mount each directory source — a directory referenced
	// by 2+ services (e.g. ./protos:/protos in six different services)
	// is almost certainly shared real content someone actually maintains,
	// the same signal a build-context match is for a live-reload source
	// tree, just independent of anyone building from it. A source used by
	// only one service doesn't get this treatment: that's the ordinary
	// single-service managed-data-directory case.
	bindSourceUsers := map[string]map[string]bool{}
	for _, name := range names {
		for _, v := range proj.Services[name].Volumes {
			if v.Type != "bind" {
				continue
			}
			if bindSourceUsers[v.Source] == nil {
				bindSourceUsers[v.Source] = map[string]bool{}
			}
			bindSourceUsers[v.Source][name] = true
		}
	}
	sharedBindDirs := map[string]bool{}
	for source, users := range bindSourceUsers {
		if len(users) >= 2 {
			sharedBindDirs[source] = true
		}
	}
	for _, name := range names {
		if b := proj.Services[name].Build; b != nil && b.Context != "" {
			// A build context equal to the project root itself (`context: .`
			// in the compose file, common when a Dockerfile needs a
			// top-level file) makes matchesAnyBuildContext's "is inside a
			// build context" check meaningless: every bind mount anywhere
			// in the whole project is trivially "inside" the root, so a
			// single root-context service would force the live-reload
			// treatment (and so RemoteDirKey instead of the ordinary
			// managed-volume SeedDirKey path) onto every other service's
			// data directories too, mysql's ./mysql-data included.
			if filepath.Clean(b.Context) == filepath.Clean(workingDir) {
				continue
			}
			buildContexts = append(buildContexts, b.Context)
		}
	}

	// unpublishedPortIssues holds each service's unpublished-port problems
	// without committing them to the plan yet — a service with at least one
	// other published port is presumed reachable through that one (its
	// unpublished port is almost always something internal-only, like a
	// database's cluster/gossip port, that no dependent actually calls
	// cross-host); only a service with NO published ports at all, that some
	// other service actually depends_on, is a real cross-host reachability
	// concern worth forcing the whole plan into an unprovisioned Swarm
	// overlay for.
	unpublishedPortIssues := map[string][]string{}
	hasPublishedPort := map[string]bool{}
	for _, name := range names {
		svc := proj.Services[name]
		analyzeServicePorts(&plan.Networking, unpublishedPortIssues, hasPublishedPort, name, svc)
		wl, build, warnings, rejections := convertService(project, workingDir, name, svc, buildContexts, sharedBindDirs)
		plan.Warnings = append(plan.Warnings, warnings...)
		plan.Rejections = append(plan.Rejections, rejections...)
		plan.Workloads = append(plan.Workloads, wl)
		if build != nil {
			plan.Builds = append(plan.Builds, *build)
		}
	}

	neededByDependent := map[string]bool{}
	for _, wl := range plan.Workloads {
		for _, dep := range wl.DependsOn {
			neededByDependent[dep] = true
		}
	}
	for _, name := range names {
		if !neededByDependent[name] || hasPublishedPort[name] {
			continue
		}
		for _, reason := range unpublishedPortIssues[name] {
			recommendOverlay(&plan.Networking, reason)
		}
	}

	plan.Rejections = append(plan.Rejections, ValidateGraph(plan.Workloads)...)
	return plan, nil
}

// interpolationEnvironment builds the variable set compose-go uses to
// resolve every ${VAR} reference in the Compose file (env_file paths
// aside — those are resolved later, in convertService). Real `docker
// compose` sources this from two places: a .env file next to the Compose
// file in the project directory, and the calling shell's own environment,
// with the shell taking precedence — Load previously passed an always-
// empty map here, so every such reference silently resolved to "" instead
// (visible as compose-go's own "variable is not set" warnings), which is
// how a real secret like SSH_PRIVATE_KEY ended up blank in a build arg
// even when it was genuinely exported before starting the control plane.
func interpolationEnvironment(workingDir string) types.Mapping {
	env := types.Mapping{}
	if dotEnv, err := dotenv.Read(filepath.Join(workingDir, ".env")); err == nil {
		for k, v := range dotEnv {
			env[k] = v
		}
	}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

func analyzeServicePorts(networking *NetworkRecommendation, unpublishedPortIssues map[string][]string, hasPublishedPort map[string]bool, service string, svc types.ServiceConfig) {
	publishedTargets := map[uint32]bool{}
	for _, p := range svc.Ports {
		target := int(p.Target)
		if target == 0 {
			continue
		}
		if p.Published == "" {
			unpublishedPortIssues[service] = append(unpublishedPortIssues[service], fmt.Sprintf("%s: container port %d is not published; LAN routing needs host port %d", service, target, target))
			continue
		}
		if _, err := strconv.Atoi(p.Published); err != nil {
			recommendOverlay(networking, fmt.Sprintf("%s: published port %q cannot be used for direct LAN routing", service, p.Published))
			continue
		}
		// A remapped host port (published != target) is fully supported:
		// the agent binds the real host port to the real container port
		// independently (model.Workload.PortBindings), so this no longer
		// needs a Swarm overlay recommendation the way it used to.
		publishedTargets[p.Target] = true
		hasPublishedPort[service] = true
	}
	for _, exposed := range svc.Expose {
		target, err := strconv.ParseUint(exposed, 10, 32)
		if err != nil || target == 0 || publishedTargets[uint32(target)] {
			continue
		}
		unpublishedPortIssues[service] = append(unpublishedPortIssues[service], fmt.Sprintf("%s: exposed container port %d is not published; LAN routing needs host port %d", service, target, target))
	}
}

func recommendOverlay(networking *NetworkRecommendation, reason string) {
	networking.Mode = NetworkModeSwarmOverlay
	networking.Supported = false
	for _, existing := range networking.Reasons {
		if existing == reason {
			return
		}
	}
	networking.Reasons = append(networking.Reasons, reason)
}

func convertService(project, workingDir, name string, svc types.ServiceConfig, buildContexts []string, sharedBindDirs map[string]bool) (model.Workload, *BuildSpec, []string, []string) {
	var warnings, rejections []string
	buildPlatform := "linux/amd64"
	if _, ok := platformArch(svc.Platform); ok {
		buildPlatform = svc.Platform
	}

	wl := model.Workload{
		Project:      project,
		Service:      name,
		Revision:     "latest", // no content-addressed revisioning yet; see M4 doc
		DesiredState: model.DesiredRunning,
	}

	var build *BuildSpec
	switch {
	case svc.Image != "" && svc.Build == nil:
		wl.Image = svc.Image
	case svc.Build != nil:
		dockerfile := svc.Build.Dockerfile
		if dockerfile == "" {
			dockerfile = "Dockerfile"
		}
		build = &BuildSpec{
			Service:    name,
			Context:    svc.Build.Context,
			Dockerfile: dockerfile,
			Args:       map[string]string{},
			Platform:   buildPlatform,
		}
		for k, v := range svc.Build.Args {
			if v != nil {
				build.Args[k] = *v
			}
		}
		if len(svc.Build.SSH) > 0 {
			build.SSH = map[string]string{}
			for _, k := range svc.Build.SSH {
				build.SSH[k.ID] = k.Path
			}
		}
		if len(svc.Build.Secrets) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: build.secrets are declared but BuildKit secret mounts are not implemented yet; the build will fail for anything requiring them (SRS §11, §18.1)", name))
		}
		// Caught here rather than left to surface mid-deploy: a missing
		// build context (typo'd path, directory never committed, wrong
		// working dir) is knowable with no Docker call at all, so `hacoctl
		// plan` should reject it up front instead of `deploy` failing after
		// everything else already scheduled.
		if info, err := os.Stat(build.Context); err != nil {
			rejections = append(rejections, fmt.Sprintf("%s: build context %q does not exist", name, build.Context))
		} else if !info.IsDir() {
			rejections = append(rejections, fmt.Sprintf("%s: build context %q is not a directory", name, build.Context))
		} else if _, err := os.Stat(filepath.Join(build.Context, build.Dockerfile)); err != nil {
			rejections = append(rejections, fmt.Sprintf("%s: Dockerfile %q not found in build context %q", name, build.Dockerfile, build.Context))
		}
	default:
		rejections = append(rejections, fmt.Sprintf("%s: service has neither image nor build", name))
	}

	if len(svc.Command) > 0 {
		wl.Command = []string(svc.Command)
	}

	wl.Env = map[string]string{}
	for _, ef := range svc.EnvFiles {
		// compose-go already resolves env_file paths to absolute (the same
		// way it resolves build.context), so joining workingDir again here
		// would double it up — only join for the (defensive) case of a
		// still-relative path, which shouldn't happen in practice but costs
		// nothing to handle correctly.
		path := ef.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(workingDir, path)
		}
		fileEnv, err := parseEnvFile(path)
		if err != nil {
			if ef.Required {
				rejections = append(rejections, fmt.Sprintf("%s: required env_file %s: %v", name, ef.Path, err))
			} else {
				warnings = append(warnings, fmt.Sprintf("%s: optional env_file %s unreadable: %v", name, ef.Path, err))
			}
			continue
		}
		for k, v := range fileEnv {
			wl.Env[k] = v
		}
	}
	for k, v := range svc.Environment {
		if v == nil {
			warnings = append(warnings, fmt.Sprintf("%s: environment key %q has no value and shell-env passthrough is not supported; it will be empty", name, k))
			continue
		}
		wl.Env[k] = *v
	}

	// Every declared port is an internal container port, usable for
	// service-to-service DNS discovery whether or not it's ever published
	// (SRS review focus: "An internal-only container port must be
	// discoverable without creating a host-port binding"). Only a port
	// with an explicit `published:` value gets a host binding at all —
	// bare `ports: ["5000"]` or `expose:` is internal-only, not an
	// implicit host exposure.
	var hostPorts, containerPorts []int
	var portBindings []model.PortBinding
	for _, p := range svc.Ports {
		target := int(p.Target)
		if target == 0 {
			continue
		}
		containerPorts = append(containerPorts, target)
		if p.Published == "" {
			continue
		}
		pub, err := strconv.Atoi(p.Published)
		if err != nil {
			continue
		}
		// Published can legitimately differ from the container's own
		// listening port (e.g. publish 5600 for a container listening on
		// 5000) — the host port is what the scheduler reserves/conflict-
		// checks and what the agent binds externally; the container port
		// is what the container (and service-name DNS discovery) actually
		// uses internally. They're tracked as an explicit pair
		// (PortBinding) so the agent can bind them independently instead
		// of assuming they're the same number.
		hostPorts = append(hostPorts, pub)
		portBindings = append(portBindings, model.PortBinding{ContainerPort: target, HostPort: pub})
	}
	for _, e := range svc.Expose {
		if p, err := strconv.Atoi(e); err == nil {
			containerPorts = append(containerPorts, p)
		}
	}
	wl.Constraints.HostPorts = hostPorts
	wl.ContainerPorts = containerPorts
	wl.PortBindings = portBindings

	for dep, cfg := range svc.DependsOn {
		wl.DependsOn = append(wl.DependsOn, dep)
		if cfg.Condition == types.ServiceConditionHealthy {
			wl.DependsOnHealthy = append(wl.DependsOnHealthy, dep)
		}
	}
	sort.Strings(wl.DependsOn)
	sort.Strings(wl.DependsOnHealthy)

	if svc.HealthCheck != nil && !svc.HealthCheck.Disable {
		if isPlaceholderHealthCheck(svc.HealthCheck.Test) {
			warnings = append(warnings, fmt.Sprintf("%s: healthcheck %v looks like a placeholder and does not validate real readiness", name, []string(svc.HealthCheck.Test)))
		}
		// Carried through regardless of the placeholder warning above —
		// even a trivial check is a real Docker healthcheck that
		// DependsOnHealthy can wait on; it just won't catch much.
		hc := &model.HealthCheck{Test: []string(svc.HealthCheck.Test)}
		if svc.HealthCheck.Interval != nil {
			hc.Interval = time.Duration(*svc.HealthCheck.Interval)
		}
		if svc.HealthCheck.Timeout != nil {
			hc.Timeout = time.Duration(*svc.HealthCheck.Timeout)
		}
		if svc.HealthCheck.Retries != nil {
			hc.Retries = int(*svc.HealthCheck.Retries)
		}
		if svc.HealthCheck.StartPeriod != nil {
			hc.StartPeriod = time.Duration(*svc.HealthCheck.StartPeriod)
		}
		wl.HealthCheck = hc
	}

	pinHost, _ := svc.Extensions["x-haco-pin-host"].(bool)
	placeAnywhere, _ := svc.Extensions["x-haco-place-anywhere"].(bool)
	bindHostPath, _ := svc.Extensions["x-haco-bind-host-path"].(bool)

	// forcedPinNode accumulates across every volume this service declares
	// — a service's final storage affinity must be one coherent decision
	// for the whole workload, not whichever volume happens to be declared
	// last in the YAML. Only an explicit x-haco-pin-host forces a pin
	// here (always to this same control-plane host, so multiple such
	// volumes on one service can never actually disagree) — a directory
	// bind mount's literal content alone no longer forces anything; see
	// the comment below on cases 2-4 for why.
	wl.Storage.Class = model.Ephemeral
	hasVolume := false
	var forcedPinNode string
	for _, v := range svc.Volumes {
		switch v.Type {
		case "bind", "volume":
			hasVolume = true
			if v.Type == "bind" {
				// Honoring the bind mount's real content, in detection
				// order — each check only applies when earlier ones didn't
				// match:
				//   1. Source is a single file: there's no sensible "managed
				//      empty file" substitute, so this is never ambiguous and
				//      needs no flag at all — its content travels with the
				//      workload (below) rather than needing a host pin.
				//   2. Source is a directory that IS (or is inside) some
				//      service's build context — anywhere in the compose
				//      file, not just this service's own — which is almost
				//      always a source-code live-mount, including a
				//      cross-service shared-library mount (service A
				//      bind-mounting service B's source tree).
				//   3. Source is a directory bind-mounted by 2+ distinct
				//      services — real shared content someone maintains
				//      (e.g. a shared proto/models directory several
				//      services read from), not anyone's build context,
				//      but just as clearly not a throwaway data directory
				//      either. A source only one service mounts doesn't
				//      get this treatment — see case 4 below for that.
				//   4. Explicit x-haco-bind-host-path: true — the manual
				//      escape hatch for anything 1-3 don't catch (e.g. a
				//      directory that isn't anyone's build context or
				//      shared by multiple services, like a one-off
				//      init-scripts directory).
				// Anything else falls through to the managed-volume default.
				//
				// x-haco-place-anywhere opts a service out of cases 2-4:
				// a compose file commonly bind-mounts every service's own
				// source by convention (devcontainer boilerplate) even
				// though only one or two are actually being live-edited at
				// a time — forcing every such service onto one fixed host
				// would defeat scheduling across the fleet. With it set,
				// that bind mount's host path is ignored like any other
				// data volume and the service is free to land (and then
				// stay pinned to) whichever node it's actually scheduled
				// on.
				//
				// Cases 2-4 keep the real content either way — but only as
				// a literal, truly-live bind mount when this service is
				// explicitly pinned to this control-plane host
				// (x-haco-pin-host/--pin-host), since that's the only case
				// where the literal path is guaranteed to exist where the
				// workload actually runs. Otherwise (no pin, or pinned to
				// a different node) the directory's current content is
				// tarred up once at deploy time and shipped to wherever
				// it lands instead (resolveVolumeSnapshots in
				// controlplane/service.go, VolumeMount.RemoteDirKey) — the
				// same idea as a remote build context, just for a
				// directory mount. That trades true live-reload for free
				// scheduling: a further host edit after deploy needs a
				// redeploy to show up, since it's a snapshot, not a
				// continuous sync.
				//
				// Case 1 (single-file source, e.g. ./config.json) doesn't
				// need any of that: the file is small enough to read once
				// here and carry its content with the workload itself
				// (VolumeMount.FileContent) — like transferring a build
				// context to whichever node actually builds it — so the
				// agent that ends up running this workload, wherever that
				// is, materializes the same file locally before binding.
				// No pin needed at all for this mount.
				if isRegularFile(v.Source) {
					data, err := os.ReadFile(v.Source)
					if err != nil {
						rejections = append(rejections, fmt.Sprintf("%s: read bind mount file %q: %v", name, v.Source, err))
						continue
					}
					if len(data) > maxInlineBindFileBytes {
						rejections = append(rejections, fmt.Sprintf("%s: bind mount file %q is %d bytes, over the %d-byte limit for traveling with the workload — use x-haco-bind-host-path and pin this service to a host instead", name, v.Source, len(data), maxInlineBindFileBytes))
						continue
					}
					wl.Volumes = append(wl.Volumes, model.VolumeMount{ContainerPath: v.Target, ReadOnly: v.ReadOnly, FileContent: data})
					continue
				}

				reason := ""
				switch {
				case placeAnywhere:
					// skip cases 2-4 below
				case matchesAnyBuildContext(v.Source, buildContexts):
					reason = "a bind mount whose source is a service's build context"
				case sharedBindDirs[v.Source]:
					reason = "a bind mount whose source directory is shared by multiple services"
				case bindHostPath:
					reason = "x-haco-bind-host-path"
				}
				if reason != "" {
					hostPath, probs := validateDirSource(name, v.Source)
					if len(probs) > 0 {
						rejections = append(rejections, probs...)
						continue
					}
					wl.Volumes = append(wl.Volumes, model.VolumeMount{ContainerPath: v.Target, ReadOnly: v.ReadOnly, HostPath: hostPath})
					if pinHost {
						forcedPinNode = agent.HostNodeID()
					}
					continue
				}
			}
			// Otherwise, a bind mount's host path (v.Source) is
			// intentionally ignored going forward and it's treated exactly
			// like a named volume: only v.Target (the container path)
			// matters, and prepareVolumes creates and manages its own
			// directory per (workload key, container path) regardless of
			// how that path was declared in compose. Correct for a data
			// directory (mysql/postgres/redis/etc. are meant to start
			// empty); x-haco-bind-host-path above is the escape hatch when
			// the literal path matters instead (a source-code live-mount).
			//
			// It's still worth adopting as a one-time seed, though: if
			// this exact host directory already has real content (the
			// stack was run outside the orchestrator before — e.g. a
			// plain `docker compose up` left real data in ./mysql-data),
			// starting the managed volume from that instead of empty
			// means the orchestrator doesn't silently lose pre-existing
			// data on its first-ever deploy. SeedDirKey momentarily holds
			// the raw candidate path here — service.go's
			// resolveVolumeSeeds converts it to an actual stash key (or
			// clears it, if there's nothing worth seeding) before the
			// workload is ever persisted or sent to an agent.
			wl.Volumes = append(wl.Volumes, model.VolumeMount{ContainerPath: v.Target, ReadOnly: v.ReadOnly, SeedDirKey: candidateSeedPath(v)})
		default:
			warnings = append(warnings, fmt.Sprintf("%s: volume type %q is not evaluated for storage affinity", name, v.Type))
		}
	}
	if hasVolume {
		// Node affinity, in priority order, decided once for the whole
		// service regardless of how many volumes it declares:
		//   1. A case-2/3 directory bind mount above — always this
		//      control-plane host, forced, not a default.
		//   2. x-haco-pin-node: <id>    — explicit node.
		//   3. x-haco-pin-host: true    — explicit control-plane host.
		//   4. default (nothing set, same as x-haco-place-anywhere) — no
		//      affinity at all: the scheduler picks freely on first
		//      deploy, then the control plane locks the pin to wherever
		//      it landed (service.go Submit), so later redeploys stay on
		//      that same data. Nothing pins a stateful service to the
		//      control-plane host just because it's stateful — that's
		//      the user's call (--pin-host/--pin-node, or these same
		//      extensions), never an implicit default.
		pinned := forcedPinNode
		if pinned == "" {
			pinned, _ = svc.Extensions["x-haco-pin-node"].(string)
		}
		if pinned == "" && pinHost {
			pinned = agent.HostNodeID()
		}
		allowFresh, _ := svc.Extensions["x-haco-allow-fresh-volume"].(bool)
		wl.Storage = model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: pinned, AllowFreshVolumeOnReschedule: allowFresh}
	}

	// x-haco-pin-host also works standalone, for a service with no volume
	// at all that should still always run on the control-plane host (e.g.
	// something that needs to reach a host-local resource) — a general
	// node-affinity constraint, independent of storage.
	if pinHost && wl.Storage.Class == model.Ephemeral {
		wl.Constraints.PinnedNodeID = agent.HostNodeID()
	}

	wl.Resources.CPUMillis = 250
	if svc.CPUS > 0 {
		wl.Resources.CPUMillis = int64(svc.CPUS * 1000)
	}
	// No mem_limit in the compose file means no cap at all in real Docker
	// Compose — MemoryBytes 0 carries that through (Docker's own "no limit"
	// value, and the scheduler reserves nothing for it). A hardcoded
	// fallback here (previously 128MiB) silently OOM-kills any real service
	// that needs more than that, with no error beyond a bare SIGKILL.
	if svc.MemLimit > 0 {
		wl.Resources.MemoryBytes = int64(svc.MemLimit)
	}

	// No explicit platform: leave Architecture unset (no hard constraint) so
	// the workload can schedule onto whatever node is available, rather than
	// assuming amd64 — wrong on an arm64-only fleet (e.g. Apple Silicon
	// nodes) and needlessly restrictive everywhere else.
	if svc.Platform != "" {
		if arch, ok := platformArch(svc.Platform); ok {
			wl.Constraints.Architecture = arch
		} else {
			warnings = append(warnings, fmt.Sprintf("%s: platform %q not recognized; no architecture constraint applied", name, svc.Platform))
		}
	}

	if len(svc.ExtraHosts) > 0 {
		rejections = append(rejections, fmt.Sprintf("%s: extra_hosts/host-gateway has no platform equivalent yet", name))
	}

	if svc.Restart != "" {
		warnings = append(warnings, fmt.Sprintf("%s: restart policy %q is recorded but not enforced by the agent yet (SRS FR restart: partial)", name, svc.Restart))
	}

	if len(svc.Secrets) > 0 {
		warnings = append(warnings, fmt.Sprintf("%s: secrets are declared but not delivered through a secret store yet; pass equivalent values via environment for now", name))
	}

	return wl, build, warnings, rejections
}

// isRegularFile reports whether path is an existing, absolute path to a
// (non-directory) file — the single-file bind-mount auto-detection.
func isRegularFile(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// candidateSeedPath returns v's resolved source path when it's worth
// offering as a one-time seed for a managed volume — a bind mount (not a
// named volume, which has no host-side directory at all) whose host
// directory genuinely exists and has at least one entry. An empty or
// missing directory has nothing worth adopting, so there's nothing to
// stash/transfer for it.
func candidateSeedPath(v types.ServiceVolumeConfig) string {
	if v.Type != "bind" || v.Source == "" || !filepath.IsAbs(v.Source) {
		return ""
	}
	entries, err := os.ReadDir(v.Source)
	if err != nil || len(entries) == 0 {
		return ""
	}
	return v.Source
}

// matchesAnyBuildContext reports whether path is equal to, or a
// subdirectory of, any entry in contexts — the cross-service build-context
// bind-mount auto-detection. contexts is every service's build.context in
// the whole compose file, not just the current service's own.
func matchesAnyBuildContext(path string, contexts []string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	for _, ctx := range contexts {
		ctx = filepath.Clean(ctx)
		if clean == ctx {
			return true
		}
		if rel, err := filepath.Rel(ctx, clean); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// bindLiteralHostPath honors a *directory* bind mount's literal host path
// instead of substituting a managed directory — the build-context-source
// heuristic and the explicit x-haco-bind-host-path escape hatch (a
// single-file source never reaches here; see its own handling just above,
// which embeds the file's content with the workload instead of pinning).
// reason names which one fired, for the rejection messages below. Only
// correct when the workload actually runs on this control plane's own
// machine (the filesystem that path was resolved on), so the pin is
// forced, not defaulted; a conflicting x-haco-pin-node is a plan-time
// rejection rather than silently overridden. The caller never reaches
// this function at all when x-haco-place-anywhere is set — that flag opts
// out of literal-host-path treatment entirely instead.
// validateDirSource checks that a directory bind mount's source resolves
// to a real, absolute path on this control-plane host — the one place
// compose.Load() ever runs, so this is the only machine whose filesystem
// it can check. Unlike the old bindLiteralHostPath, it no longer rejects
// an explicit x-haco-pin-node to a different node: that's fine now, since
// the content travels there as a tarball snapshot (resolveVolumeSnapshots)
// instead of requiring a literal bind mount.
func validateDirSource(name, source string) (string, []string) {
	if !filepath.IsAbs(source) {
		return "", []string{fmt.Sprintf("%s: bind mount source %q did not resolve to an absolute path", name, source)}
	}
	if _, err := os.Stat(source); err != nil {
		return "", []string{fmt.Sprintf("%s: bind mount host path %q does not exist on this control-plane host", name, source)}
	}
	return source, nil
}

func isPlaceholderHealthCheck(test types.HealthCheckTest) bool {
	joined := strings.ToLower(strings.Join(test, " "))
	return strings.Contains(joined, "exit 0")
}

func platformArch(platform string) (string, bool) {
	switch platform {
	case "linux/amd64":
		return "amd64", true
	case "linux/arm64":
		return "arm64", true
	default:
		return "", false
	}
}

func parseEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out, nil
}

// validateGraph checks cross-service rules the compat matrix requires:
// undefined dependency references, dependency cycles, and host-port
// conflicts within the same project (compose-compatibility-matrix.md
// "Required validation").
// ApplyHostPinOverride is ApplyPinOverride for the common case: every named
// service pins to this control-plane host specifically. Supplied per
// `hacoctl deploy/plan/build --pin-host <service,...>` call.
func ApplyHostPinOverride(plan *Plan, pinHost []string) {
	if len(pinHost) == 0 {
		ApplyPinOverride(plan, nil)
		return
	}
	ApplyPinOverride(plan, map[string][]string{agent.HostNodeID(): pinHost})
}

// ApplyPinOverride replaces every workload's computed storage affinity with
// a single, explicit CLI-level decision: pins maps a node ID to the
// services that must land there (e.g. the one actually being live-edited,
// plus stateful services like a database, pinned to the control-plane
// host; another service pinned to a specific worker node instead). Every
// workload not named in any of pins' service lists is freed to schedule
// anywhere, even one compose.go's own bind-mount/data-volume heuristics
// would otherwise have pinned to host (the common devcontainer pattern
// where every service bind-mounts its own source, even though only a few
// are actually being developed at once). This needs no x-haco-*
// annotations in the Compose file itself — it's an override supplied per
// `hacoctl deploy/plan/build --pin-host`/`--pin-node` call.
//
// A freed workload loses any literal bind-mount HostPath too (same as
// x-haco-place-anywhere): that path was only ever valid on this one host,
// so once the workload is free to land elsewhere it must fall back to a
// normal agent-managed volume instead. A single-file bind mount is
// unaffected either way — its content already travels with the workload
// (VolumeMount.FileContent, set at plan time), not a literal HostPath.
func ApplyPinOverride(plan *Plan, pins map[string][]string) {
	nodeByService := map[string]string{}
	for node, services := range pins {
		for _, s := range services {
			if s = strings.TrimSpace(s); s != "" {
				nodeByService[s] = node
			}
		}
	}
	for i := range plan.Workloads {
		wl := &plan.Workloads[i]
		if node, ok := nodeByService[wl.Service]; ok {
			wl.Constraints.PinnedNodeID = node
			if wl.Storage.Class == model.NodeLocalPersistent {
				wl.Storage.PinnedNodeID = node
			}
			continue
		}
		if wl.Storage.Class == model.NodeLocalPersistent {
			wl.Storage.PinnedNodeID = ""
		}
		for j := range wl.Volumes {
			wl.Volumes[j].HostPath = ""
		}
	}
}

// MergePinOverride combines `--pin-host`'s service list (pinned to this
// control-plane host) with `--pin-node`'s explicit node assignments into
// the single map ApplyPinOverride expects, resolving "this host" to its
// real node ID. ok is false when neither was supplied at all, meaning no
// override should be applied and compose.go's own affinity decisions
// should stand.
func MergePinOverride(pinHost *[]string, pinNodes map[string][]string) (pins map[string][]string, ok bool) {
	if pinHost == nil && len(pinNodes) == 0 {
		return nil, false
	}
	merged := map[string][]string{}
	for node, services := range pinNodes {
		merged[node] = append(merged[node], services...)
	}
	if pinHost != nil {
		host := agent.HostNodeID()
		merged[host] = append(merged[host], *pinHost...)
	}
	return merged, true
}

// ValidateGraph checks cross-service concerns that only make sense across
// the whole set being validated together: undefined depends_on references,
// dependency cycles, and host-port conflicts. Exported so a caller that
// filters down to a dependency closure (hacoctl deploy/plan --service) can
// re-run it on just that subset — a port conflict between two services
// that aren't both actually being deployed together isn't a real conflict.
func ValidateGraph(workloads []model.Workload) []string {
	var problems []string

	byService := map[string]model.Workload{}
	for _, w := range workloads {
		byService[w.Service] = w
	}

	for _, w := range workloads {
		for _, dep := range w.DependsOn {
			if _, ok := byService[dep]; !ok {
				problems = append(problems, fmt.Sprintf("%s: depends_on references undefined service %q", w.Service, dep))
			}
		}
	}

	if cycle := findCycle(workloads); cycle != nil {
		problems = append(problems, fmt.Sprintf("dependency cycle detected: %s", strings.Join(cycle, " -> ")))
	}

	portOwner := map[int]string{}
	for _, w := range workloads {
		for _, p := range w.Constraints.HostPorts {
			if owner, taken := portOwner[p]; taken && owner != w.Service {
				problems = append(problems, fmt.Sprintf("host port %d is requested by both %s and %s", p, owner, w.Service))
				continue
			}
			portOwner[p] = w.Service
		}
	}

	return problems
}

func findCycle(workloads []model.Workload) []string {
	graph := map[string][]string{}
	for _, w := range workloads {
		graph[w.Service] = w.DependsOn
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var path []string

	var visit func(node string) []string
	visit = func(node string) []string {
		color[node] = gray
		path = append(path, node)
		for _, next := range graph[node] {
			switch color[next] {
			case gray:
				cycleStart := 0
				for i, n := range path {
					if n == next {
						cycleStart = i
						break
					}
				}
				return append(append([]string{}, path[cycleStart:]...), next)
			case white:
				if found := visit(next); found != nil {
					return found
				}
			}
		}
		path = path[:len(path)-1]
		color[node] = black
		return nil
	}

	names := make([]string, 0, len(graph))
	for n := range graph {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if color[n] == white {
			if cycle := visit(n); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}
