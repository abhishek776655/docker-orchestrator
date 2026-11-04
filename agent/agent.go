// Package agent implements the M2 node agent lifecycle (SRS 6.2):
// register, heartbeat, poll for assignments, execute through the local
// Docker Engine API, and report observed state back to the control
// plane. It never accepts inbound RPC in M2 — the control plane pushes
// nothing; the agent pulls, which keeps M2 single-node wiring simple and
// is upgraded to push-based assignment in M3 (SRS M3 scope).
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

type Config struct {
	NodeID          string
	Address         string
	ControlPlaneURL string
	HeartbeatPeriod time.Duration
	PollPeriod      time.Duration
	Credential      string
	TLSFingerprint  string
	// Role marks this agent as the one co-located with the control plane
	// ("host"); empty for every ordinary worker. It is published as a node
	// label so the web console can draw the host at the center.
	Role string
}

// nodeLabels are the descriptive labels every agent registers with: the OS
// it runs on (runtime.GOOS: darwin, linux, windows) and, for the
// control-plane host agent, its role. Informational only: no scheduling
// constraint reads them.
func (c Config) nodeLabels() map[string]string {
	labels := map[string]string{"os": runtime.GOOS}
	if c.Role != "" {
		labels["haco.io/role"] = c.Role
	}
	return labels
}

// Agent is one running node agent instance.
type Agent struct {
	cfg    Config
	docker *Docker
	http   *http.Client
}

func New(cfg Config, docker *Docker) *Agent {
	if cfg.HeartbeatPeriod == 0 {
		cfg.HeartbeatPeriod = 5 * time.Second
	}
	if cfg.PollPeriod == 0 {
		cfg.PollPeriod = 2 * time.Second
	}
	return &Agent{cfg: cfg, docker: docker, http: pinnedHTTPClient(cfg.TLSFingerprint, 10*time.Second)}
}

func pinnedHTTPClient(fingerprint string, timeout time.Duration) *http.Client {
	if fingerprint == "" {
		return &http.Client{Timeout: timeout}
	}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // VerifyConnection below pins the complete leaf certificate.
		VerifyConnection: func(cs tls.ConnectionState) error {
			if err := verifyPinnedCertificate(cs, fingerprint); err != nil {
				return err
			}
			if !time.Now().Before(cs.PeerCertificates[0].NotAfter) || time.Now().Before(cs.PeerCertificates[0].NotBefore) {
				return fmt.Errorf("control-plane TLS certificate is outside its validity period")
			}
			return nil
		},
	}}}
}

func verifyPinnedCertificate(cs tls.ConnectionState, fingerprint string) error {
	if len(cs.PeerCertificates) == 0 {
		return fmt.Errorf("server supplied no TLS certificate")
	}
	sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), fingerprint) {
		return fmt.Errorf("control-plane TLS certificate fingerprint mismatch")
	}
	return nil
}

// Run registers the node and then loops heartbeat + assignment polling
// until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	if a.cfg.Credential == "" || a.cfg.TLSFingerprint == "" || !strings.HasPrefix(a.cfg.ControlPlaneURL, "https://") {
		return fmt.Errorf("agent must be joined with a credential-pinned HTTPS connection")
	}
	if err := a.register(ctx); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	log.Printf("agent %s registered with control plane at %s", a.cfg.NodeID, a.cfg.ControlPlaneURL)

	heartbeat := time.NewTicker(a.cfg.HeartbeatPeriod)
	poll := time.NewTicker(a.cfg.PollPeriod)
	defer heartbeat.Stop()
	defer poll.Stop()

	// Heartbeats get their own goroutine. pollAndExecute can block for
	// minutes on an image pull or build; if the heartbeat shared its loop, a
	// busy but healthy node would go silent, be marked unavailable after the
	// heartbeat timeout, and have its workloads rescheduled elsewhere.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				if err := a.heartbeat(ctx); err != nil {
					log.Printf("heartbeat failed: %v", err)
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			if err := a.pollAndExecute(ctx); err != nil {
				log.Printf("poll/execute failed: %v", err)
			}
		}
	}
}

func (a *Agent) register(ctx context.Context) error {
	capacity, architecture, err := a.docker.CapacityAndArchitecture(ctx)
	if err != nil {
		return err
	}
	cachedImages, err := a.docker.CachedImages(ctx)
	if err != nil {
		log.Printf("list cached images failed: %v", err)
	}
	body := map[string]any{
		"id":            a.cfg.NodeID,
		"address":       a.cfg.Address,
		"architecture":  architecture,
		"labels":        a.cfg.nodeLabels(),
		"capabilities":  map[string]bool{},
		"cpu_millis":    capacity.CPUMillis,
		"memory_bytes":  capacity.MemoryBytes,
		"gpus":          collectGPUs(ctx),
		"thermal":       collectThermal(),
		"cached_images": cachedImages,
	}
	return a.post(ctx, "/v1/nodes", body, nil)
}

func (a *Agent) heartbeat(ctx context.Context) error {
	cachedImages, err := a.docker.CachedImages(ctx)
	if err != nil {
		log.Printf("list cached images failed: %v", err)
	}
	body := map[string]any{
		"thermal":       collectThermal(),
		"cached_images": cachedImages,
	}
	return a.post(ctx, "/v1/nodes/"+a.cfg.NodeID+"/heartbeat", body, nil)
}

func (a *Agent) pollAndExecute(ctx context.Context) error {
	var assignments []store.WorkloadRecord
	if err := a.get(ctx, "/v1/agent/"+a.cfg.NodeID+"/assignments", &assignments); err != nil {
		return err
	}

	assignedHere, executable := splitAssignments(assignments)
	for _, rec := range executable {
		if rec.Status == model.StatusRunning || rec.Status == model.StatusReady {
			alive, err := a.docker.IsRunning(ctx, rec.Key())
			if err != nil {
				log.Printf("liveness check failed for %s: %v", rec.Key(), err)
				continue
			}
			if alive {
				// Still waiting on its own healthcheck (StatusRunning, not
				// yet Ready — covers both a Compose healthcheck and one
				// baked into the image, since HealthStatus doesn't
				// distinguish the two) — re-check every tick instead of
				// treating "alive" as "nothing to do", the same as
				// StatusWaitingDependencies already does for cross-
				// workload gating. This calls checkHealth directly, not
				// the full execute() pipeline: re-running execute() would
				// also redo PullImage/EnsureRunning/WaitRunning every tick
				// for as long as health is pending, uselessly re-pulling
				// an unchanged image over and over. Once truly Ready,
				// rec.Status stops matching StatusRunning and this stops.
				if rec.Status == model.StatusRunning {
					a.checkHealth(ctx, rec.Key(), rec.ContainerID)
				}
				continue
			}
			log.Printf("%s reported %s but its container is gone; re-executing (SRS safe retries)", rec.Key(), rec.Status)
		}
		a.execute(ctx, rec)
	}

	a.cleanupOrphans(ctx, assignedHere)
	return nil
}

// splitAssignments keeps failed workloads in the ownership set so cleanup
// does not delete their exited containers (and diagnostic logs), while
// preventing terminal failures from being retried on every poll. Stopped
// workloads are excluded by the control-plane assignment query.
func splitAssignments(records []store.WorkloadRecord) (map[string]bool, []store.WorkloadRecord) {
	owned := make(map[string]bool, len(records))
	executable := make([]store.WorkloadRecord, 0, len(records))
	for _, rec := range records {
		owned[rec.Key()] = true
		if rec.Status == model.StatusFailed {
			continue
		}
		executable = append(executable, rec)
	}
	return owned, executable
}

// cleanupOrphans removes any locally running container whose workload is
// no longer assigned to this node — e.g. the control plane reconciler
// rescheduled it elsewhere after a heartbeat gap, and this node later came
// back. Without this, a recovered node would keep running a stale,
// unmanaged instance alongside the new one (SRS §6.3, UC-08: "reconciles
// actual state without blindly duplicating workload instances").
func (a *Agent) cleanupOrphans(ctx context.Context, assignedHere map[string]bool) {
	managed, err := a.docker.ManagedContainers(ctx)
	if err != nil {
		log.Printf("list managed containers failed: %v", err)
		return
	}
	for _, c := range managed {
		if assignedHere[c.WorkloadKey] {
			continue
		}
		log.Printf("removing orphaned container for %s (no longer assigned to %s)", c.WorkloadKey, a.cfg.NodeID)
		if err := a.docker.StopAndRemove(ctx, c.ContainerID); err != nil {
			log.Printf("remove orphan %s failed: %v", c.ContainerID, err)
		}
	}
}

// execute drives one workload through pulling -> starting -> running ->
// ready, reporting each transition. Dependencies are gated on registry
// state and mapped to node addresses in the container's /etc/hosts; the
// Compose environment itself stays unchanged.
func (a *Agent) execute(ctx context.Context, rec store.WorkloadRecord) {
	key := rec.Key()

	extraHosts, ready, err := a.resolveDependencies(ctx, rec.Project, rec.DependsOn, rec.DependsOnHealthy)
	if err != nil {
		a.report(ctx, key, model.StatusFailed, "dependency resolution failed: "+err.Error(), "")
		return
	}
	if !ready {
		a.report(ctx, key, model.StatusWaitingDependencies, "waiting for: "+strings.Join(rec.DependsOn, ", "), "")
		return
	}

	if rec.RemoteBuild != nil {
		if !a.docker.HasImage(ctx, rec.Image) {
			a.report(ctx, key, model.StatusBuilding, "", "")
			if err := a.buildRemote(ctx, rec); err != nil {
				a.report(ctx, key, model.StatusFailed, "remote build failed: "+err.Error(), "")
				return
			}
		}
	} else if !rec.SkipPull {
		a.report(ctx, key, model.StatusPulling, "", "")
		if err := a.docker.PullImage(ctx, rec.Image, rec.Constraints.Architecture); err != nil {
			a.report(ctx, key, model.StatusFailed, "pull failed: "+err.Error(), "")
			return
		}
	}

	a.report(ctx, key, model.StatusStarting, "", "")
	containerID, volWarnings, err := a.docker.EnsureRunning(ctx, rec.Workload, extraHosts...)
	if err != nil {
		a.report(ctx, key, model.StatusFailed, "start failed: "+err.Error(), "")
		return
	}
	for _, w := range volWarnings {
		log.Printf("%s: %s", key, w)
	}

	if err := a.docker.WaitRunning(ctx, containerID, 30*time.Second); err != nil {
		logs, logErr := a.docker.ContainerLogs(ctx, containerID)
		if logErr != nil {
			log.Printf("collect logs for %s failed: %v", key, logErr)
		}
		a.reportWithLogs(ctx, key, model.StatusFailed, "did not reach running: "+err.Error(), containerID, logs)
		return
	}

	a.report(ctx, key, model.StatusRunning, "", containerID)
	a.checkHealth(ctx, key, containerID)
}

// checkHealth makes ONE non-blocking check of the container's Docker-
// reported health status (from either a Compose healthcheck or one baked
// into the image — HealthStatus doesn't distinguish) and reports Ready,
// Failed, or neither accordingly. It deliberately does not loop/block
// waiting for health to pass: execute() runs inside pollAndExecute's
// single-threaded poll loop, so blocking here would stall every other
// workload on this node for as long as the healthcheck takes. Instead,
// pollAndExecute re-invokes execute() (and so this) on every tick for as
// long as the workload stays StatusRunning with a HealthCheck configured —
// the same non-blocking-retry pattern StatusWaitingDependencies already
// uses for cross-workload gating. A container with no healthcheck at all
// reports status "" and is treated as immediately ready, same as before
// this existed; "still starting" (not yet healthy, not yet unhealthy) is
// left exactly as is — reported Running stays Running, re-checked next
// tick — and a check stuck past a generous safety bound past the
// container's own start time is treated as failed rather than waiting
// forever, as a backstop against a wedged check Docker's own
// Retries-based unhealthy transition should normally already catch.
func (a *Agent) checkHealth(ctx context.Context, key, containerID string) {
	status, startedAt, err := a.docker.HealthStatus(ctx, containerID)
	if err != nil {
		log.Printf("check health for %s failed: %v", key, err)
		return
	}
	switch status {
	case "", "healthy":
		a.report(ctx, key, model.StatusReady, "", containerID)
	case "unhealthy":
		a.report(ctx, key, model.StatusFailed, "container reported unhealthy", containerID)
	default: // "starting", or any other in-progress value
		if !startedAt.IsZero() && time.Since(startedAt) > 10*time.Minute {
			a.report(ctx, key, model.StatusFailed, fmt.Sprintf("did not become healthy within 10m (last status: %s)", status), containerID)
		}
		// Otherwise: leave it at Running, already reported above; the next
		// poll tick checks again.
	}
}

// buildRemote fetches the build-context tarball the control plane stashed
// for this workload (resolveBuilds, controlplane/service.go) and builds
// the image itself — registry-optional builds, multi-machine case: there
// is no registry to pull rec.Image from, so this agent's own Docker daemon
// is the only one that will ever produce it.
func (a *Agent) buildRemote(ctx context.Context, rec store.WorkloadRecord) error {
	path := fmt.Sprintf("/v1/agent/%s/build-context/%s/%s/%s", a.cfg.NodeID, rec.Project, rec.Service, rec.Revision)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.ControlPlaneURL+path, nil)
	if err != nil {
		return err
	}
	resp, tarball, err := a.doRaw(req)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("fetch build context: control plane returned %s: %s", resp.Status, string(tarball))
	}
	return a.docker.BuildImage(ctx, bytes.NewReader(tarball), rec.Image, rec.RemoteBuild.Dockerfile, rec.RemoteBuild.Args, rec.RemoteBuild.Platform)
}

// resolveDependencies returns Docker extra-host entries for all assigned
// project services, while preserving depends_on readiness gating.
// healthyDeps is the subset of deps declared `condition: service_healthy`
// — those need an instance that's actually StatusReady (passed its own
// healthcheck, see waitHealthy), not merely StatusRunning; every other dep
// is satisfied by either.
func (a *Agent) resolveDependencies(ctx context.Context, project string, deps, healthyDeps []string) ([]string, bool, error) {
	var instances []model.ServiceInstance
	if err := a.get(ctx, "/v1/services/"+project, &instances); err != nil {
		return nil, false, err
	}
	entries, err := projectServiceHostEntries(instances)
	if err != nil {
		return nil, false, err
	}
	needsHealthy := make(map[string]bool, len(healthyDeps))
	for _, d := range healthyDeps {
		needsHealthy[d] = true
	}
	running, readyOnly := make(map[string]bool, len(instances)), make(map[string]bool, len(instances))
	for _, instance := range instances {
		if instance.Status == model.StatusReady {
			readyOnly[instance.Service] = true
			running[instance.Service] = true
		} else if instance.Status == model.StatusRunning {
			running[instance.Service] = true
		}
	}
	for _, dep := range deps {
		if needsHealthy[dep] {
			if !readyOnly[dep] {
				return nil, false, nil
			}
		} else if !running[dep] {
			return nil, false, nil
		}
	}
	return entries, true, nil
}

func projectServiceHostEntries(instances []model.ServiceInstance) ([]string, error) {
	addresses := make(map[string]string, len(instances))
	for _, instance := range instances {
		if _, exists := addresses[instance.Service]; exists {
			continue // use one instance per name in this simple LAN implementation
		}
		if net.ParseIP(instance.Address) == nil {
			return nil, fmt.Errorf("service %q has invalid node address %q", instance.Service, instance.Address)
		}
		addresses[instance.Service] = instance.Address
	}
	services := make([]string, 0, len(addresses))
	for service := range addresses {
		services = append(services, service)
	}
	sort.Strings(services)
	entries := make([]string, 0, len(services))
	for _, service := range services {
		entries = append(entries, service+":"+addresses[service])
	}
	return entries, nil
}

func (a *Agent) report(ctx context.Context, key string, status model.Status, reason, containerID string) {
	a.reportWithLogs(ctx, key, status, reason, containerID, "")
}

func (a *Agent) reportWithLogs(ctx context.Context, key string, status model.Status, reason, containerID, logs string) {
	body := map[string]any{"key": key, "status": status, "reason": reason, "container_id": containerID, "logs": logs}
	if err := a.post(ctx, "/v1/agent/status", body, nil); err != nil {
		log.Printf("report status %s=%s failed: %v", key, status, err)
	}
}

func (a *Agent) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.ControlPlaneURL+path, nil)
	if err != nil {
		return err
	}
	return a.do(req, out)
}

func (a *Agent) post(ctx context.Context, path string, body any, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.ControlPlaneURL+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return a.do(req, out)
}

func (a *Agent) do(req *http.Request, out any) error {
	if a.cfg.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.Credential)
		req.Header.Set("X-HACO-Node-ID", a.cfg.NodeID)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: status %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// doRaw is do without JSON decoding or a non-2xx short-circuit — for
// fetching a binary body (a build-context tarball) where the caller needs
// the response body either way, to report the error text on failure.
func (a *Agent) doRaw(req *http.Request) (*http.Response, []byte, error) {
	if a.cfg.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.Credential)
		req.Header.Set("X-HACO-Node-ID", a.cfg.NodeID)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return resp, data, nil
}
