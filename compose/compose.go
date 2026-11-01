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

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// BuildSpec is the build work one service needs before it can be
// scheduled, when it has a `build:` block instead of a pre-built `image:`
// (SRS 7.1 build.context/dockerfile/args/platform).
type BuildSpec struct {
	Service    string
	Context    string
	Dockerfile string
	Args       map[string]string
	Platform   string
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
		Environment: types.Mapping{},
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
	for _, name := range names {
		if b := proj.Services[name].Build; b != nil && b.Context != "" {
			buildContexts = append(buildContexts, b.Context)
		}
	}

	for _, name := range names {
		svc := proj.Services[name]
		analyzeServicePorts(&plan.Networking, name, svc)
		wl, build, warnings, rejections := convertService(project, workingDir, name, svc, buildContexts)
		plan.Warnings = append(plan.Warnings, warnings...)
		plan.Rejections = append(plan.Rejections, rejections...)
		plan.Workloads = append(plan.Workloads, wl)
		if build != nil {
			plan.Builds = append(plan.Builds, *build)
		}
	}

	plan.Rejections = append(plan.Rejections, validateGraph(plan.Workloads)...)
	return plan, nil
}

func analyzeServicePorts(networking *NetworkRecommendation, service string, svc types.ServiceConfig) {
	publishedTargets := map[uint32]bool{}
	for _, p := range svc.Ports {
		target := int(p.Target)
		if target == 0 {
			continue
		}
		if p.Published == "" {
			recommendOverlay(networking, fmt.Sprintf("%s: container port %d is not published; LAN routing needs host port %d", service, target, target))
			continue
		}
		published, err := strconv.Atoi(p.Published)
		if err != nil {
			recommendOverlay(networking, fmt.Sprintf("%s: published port %q cannot be used for direct LAN routing", service, p.Published))
			continue
		}
		if published != target {
			recommendOverlay(networking, fmt.Sprintf("%s: port mapping %d:%d changes the port in the service URL; use a Swarm overlay to preserve the container port", service, published, target))
			continue
		}
		publishedTargets[p.Target] = true
	}
	for _, exposed := range svc.Expose {
		target, err := strconv.ParseUint(exposed, 10, 32)
		if err != nil || target == 0 || publishedTargets[uint32(target)] {
			continue
		}
		recommendOverlay(networking, fmt.Sprintf("%s: exposed container port %d is not published; LAN routing needs host port %d", service, target, target))
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

func convertService(project, workingDir, name string, svc types.ServiceConfig, buildContexts []string) (model.Workload, *BuildSpec, []string, []string) {
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
		if len(svc.Build.SSH) > 0 || len(svc.Build.Secrets) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: build.ssh/build.secrets are declared but BuildKit secret/SSH mounts are not implemented yet; the build will fail for anything requiring private access (SRS §11, §18.1)", name))
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
		fileEnv, err := parseEnvFile(filepath.Join(workingDir, ef.Path))
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
	for _, p := range svc.Ports {
		target := int(p.Target)
		if target == 0 {
			continue
		}
		containerPorts = append(containerPorts, target)
		if p.Published == "" {
			continue
		}
		if pub, err := strconv.Atoi(p.Published); err == nil && pub != target {
			warnings = append(warnings, fmt.Sprintf("%s: published port %d differs from container port %d; Compose plan recommends Swarm overlay networking", name, pub, target))
		}
		hostPorts = append(hostPorts, target)
	}
	for _, e := range svc.Expose {
		if p, err := strconv.Atoi(e); err == nil {
			containerPorts = append(containerPorts, p)
		}
	}
	wl.Constraints.HostPorts = hostPorts
	wl.ContainerPorts = containerPorts

	for dep := range svc.DependsOn {
		wl.DependsOn = append(wl.DependsOn, dep)
	}
	sort.Strings(wl.DependsOn)

	if svc.HealthCheck != nil && !svc.HealthCheck.Disable && isPlaceholderHealthCheck(svc.HealthCheck.Test) {
		warnings = append(warnings, fmt.Sprintf("%s: healthcheck %v looks like a placeholder and does not validate real readiness", name, []string(svc.HealthCheck.Test)))
	}

	pinHost, _ := svc.Extensions["x-haco-pin-host"].(bool)
	placeAnywhere, _ := svc.Extensions["x-haco-place-anywhere"].(bool)
	bindHostPath, _ := svc.Extensions["x-haco-bind-host-path"].(bool)

	wl.Storage.Class = model.Ephemeral
	for _, v := range svc.Volumes {
		switch v.Type {
		case "bind", "volume":
			if v.Type == "bind" {
				// Honoring the literal host path, in detection order —
				// each check only applies when earlier ones didn't match:
				//   1. Source is a single file: there's no sensible "managed
				//      empty file" substitute, so this is never ambiguous and
				//      needs no flag at all.
				//   2. Source is a directory that IS (or is inside) some
				//      service's build context — anywhere in the compose
				//      file, not just this service's own — which is almost
				//      always a source-code live-mount, including a
				//      cross-service shared-library mount (service A
				//      bind-mounting service B's source tree).
				//   3. Explicit x-haco-bind-host-path: true — the manual
				//      escape hatch for anything 1 and 2 don't catch (e.g. a
				//      directory that isn't anyone's build context, like a
				//      one-off init-scripts directory).
				// Anything else falls through to the managed-volume default.
				reason := ""
				switch {
				case isRegularFile(v.Source):
					reason = "a bind mount whose source is a single file"
				case matchesAnyBuildContext(v.Source, buildContexts):
					reason = "a bind mount whose source is a service's build context"
				case bindHostPath:
					reason = "x-haco-bind-host-path"
				}
				if reason != "" {
					storage, mount, probs := bindLiteralHostPath(name, reason, v, svc, placeAnywhere)
					if len(probs) > 0 {
						rejections = append(rejections, probs...)
						continue
					}
					wl.Storage = storage
					wl.Volumes = append(wl.Volumes, mount)
					continue
				}
			}
			// Otherwise, a bind mount's host path (v.Source) is
			// intentionally ignored and it's treated exactly like a named
			// volume: only v.Target (the container path) matters, and
			// prepareVolumes creates and manages its own directory per
			// (workload key, container path) regardless of how that path
			// was declared in compose. Correct for a data directory
			// (mysql/postgres/redis/etc. are meant to start empty);
			// x-haco-bind-host-path above is the escape hatch when the
			// literal path matters instead (a source-code live-mount).
			//
			// Node affinity, in priority order:
			//   1. x-haco-pin-node: <id>      — explicit node, as before.
			//   2. x-haco-place-anywhere: true — opt out of any default; the
			//      scheduler picks freely on first deploy, then the control
			//      plane locks the pin to wherever it landed (service.go
			//      Submit), so later redeploys stay on that same data.
			//   3. default (nothing set, or x-haco-pin-host: true) — pin to
			//      this control plane's own host machine. A stateful
			//      service's volume living on a predictable, always-on node
			//      is a safer default than an arbitrary worker.
			pinned, _ := svc.Extensions["x-haco-pin-node"].(string)
			if pinned == "" && !placeAnywhere {
				pinned = agent.HostNodeID()
			}
			allowFresh, _ := svc.Extensions["x-haco-allow-fresh-volume"].(bool)
			wl.Storage = model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: pinned, AllowFreshVolumeOnReschedule: allowFresh}
			wl.Volumes = append(wl.Volumes, model.VolumeMount{ContainerPath: v.Target, ReadOnly: v.ReadOnly})
		default:
			warnings = append(warnings, fmt.Sprintf("%s: volume type %q is not evaluated for storage affinity", name, v.Type))
		}
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
	wl.Resources.MemoryBytes = 134_217_728 // 128MiB default
	if svc.MemLimit > 0 {
		wl.Resources.MemoryBytes = int64(svc.MemLimit)
	}

	wl.Constraints.Architecture = "amd64"
	if svc.Platform != "" {
		if arch, ok := platformArch(svc.Platform); ok {
			wl.Constraints.Architecture = arch
		} else {
			warnings = append(warnings, fmt.Sprintf("%s: platform %q not recognized, defaulting architecture constraint to amd64", name, svc.Platform))
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

// bindLiteralHostPath honors a bind mount's literal host path instead of
// substituting a managed directory — shared by the explicit
// x-haco-bind-host-path escape hatch and the two automatic heuristics
// above it in precedence. reason names which one fired, for the rejection
// messages below. Only correct when the workload actually runs on this
// control plane's own machine (the filesystem that path was resolved on),
// so the pin is forced, not defaulted, and a conflicting x-haco-pin-node
// or x-haco-place-anywhere is a plan-time rejection rather than silently
// overridden.
func bindLiteralHostPath(name, reason string, v types.ServiceVolumeConfig, svc types.ServiceConfig, placeAnywhere bool) (model.StoragePolicy, model.VolumeMount, []string) {
	if placeAnywhere {
		return model.StoragePolicy{}, model.VolumeMount{}, []string{
			fmt.Sprintf("%s: %s requires a literal host path, which only works on one specific machine, but x-haco-place-anywhere is also set", name, reason),
		}
	}
	pinned, _ := svc.Extensions["x-haco-pin-node"].(string)
	host := agent.HostNodeID()
	if pinned != "" && pinned != host {
		return model.StoragePolicy{}, model.VolumeMount{}, []string{
			fmt.Sprintf("%s: %s requires this control-plane host (%s), but x-haco-pin-node is %q", name, reason, host, pinned),
		}
	}
	hostPath := v.Source
	if !filepath.IsAbs(hostPath) {
		return model.StoragePolicy{}, model.VolumeMount{}, []string{
			fmt.Sprintf("%s: bind mount source %q did not resolve to an absolute path", name, v.Source),
		}
	}
	if _, err := os.Stat(hostPath); err != nil {
		return model.StoragePolicy{}, model.VolumeMount{}, []string{
			fmt.Sprintf("%s: bind mount host path %q does not exist on this control-plane host", name, hostPath),
		}
	}
	storage := model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: host}
	mount := model.VolumeMount{ContainerPath: v.Target, ReadOnly: v.ReadOnly, HostPath: hostPath}
	return storage, mount, nil
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
func validateGraph(workloads []model.Workload) []string {
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
