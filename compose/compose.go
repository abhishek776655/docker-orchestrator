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

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// BuildSpec is the build work one service needs before it can be
// scheduled, when it has a `build:` block instead of a pre-built `image:`
// (SRS 7.1 build.context/dockerfile/args).
type BuildSpec struct {
	Service    string
	Context    string
	Dockerfile string
	Args       map[string]string
}

// Plan is the importer's output: a dry-run deployment plan (SRS UC-02,
// FR-023) that can be inspected before anything is built or scheduled.
// A non-empty Rejections means the plan must not be applied (FR-003).
type Plan struct {
	Project    string
	Workloads  []model.Workload
	Builds     []BuildSpec
	Warnings   []string
	Rejections []string
}

func (p *Plan) Valid() bool { return len(p.Rejections) == 0 }

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

	plan := &Plan{Project: project}

	if len(proj.Configs) > 0 || len(proj.Secrets) > 0 {
		plan.Warnings = append(plan.Warnings, "top-level configs/secrets are declared but not yet delivered to containers; pass equivalent values via environment for now")
	}

	names := make([]string, 0, len(proj.Services))
	for name := range proj.Services {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic plan output regardless of map order

	for _, name := range names {
		svc := proj.Services[name]
		wl, build, warnings, rejections := convertService(project, workingDir, name, svc)
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

func convertService(project, workingDir, name string, svc types.ServiceConfig) (model.Workload, *BuildSpec, []string, []string) {
	var warnings, rejections []string

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
		}
		for k, v := range svc.Build.Args {
			if v != nil {
				build.Args[k] = *v
			}
		}
		if len(svc.Build.SSH) > 0 || len(svc.Build.Secrets) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: build.ssh/build.secrets are declared but BuildKit secret/SSH mounts are not implemented yet; the build will fail for anything requiring private access (SRS §11, §18.1)", name))
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
			warnings = append(warnings, fmt.Sprintf("%s: published port %d will be bound to the host as container port %d; independent host/container port remapping is not implemented yet (see M2/M7 simplifications)", name, pub, target))
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

	wl.Storage.Class = model.Ephemeral
	for _, v := range svc.Volumes {
		switch v.Type {
		case "bind":
			rejections = append(rejections, fmt.Sprintf("%s: bind mount %s:%s requires an explicit node-path or source-sync policy, which M4 does not implement yet; remove it or pin the service with x-haco-pin-node and accept node-local semantics", name, v.Source, v.Target))
		case "volume":
			pinned, ok := svc.Extensions["x-haco-pin-node"].(string)
			if !ok || pinned == "" {
				rejections = append(rejections, fmt.Sprintf("%s: named volume %s needs node affinity; set x-haco-pin-node: <node-id> on this service", name, v.Target))
				continue
			}
			wl.Storage = model.StoragePolicy{Class: model.NodeLocalPersistent, PinnedNodeID: pinned}
			wl.Volumes = append(wl.Volumes, model.VolumeMount{ContainerPath: v.Target, ReadOnly: v.ReadOnly})
		default:
			warnings = append(warnings, fmt.Sprintf("%s: volume type %q is not evaluated for storage affinity", name, v.Type))
		}
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
