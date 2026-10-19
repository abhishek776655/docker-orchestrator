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
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	return &Agent{cfg: cfg, docker: docker, http: &http.Client{Timeout: 10 * time.Second}}
}

// Run registers the node and then loops heartbeat + assignment polling
// until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.register(ctx); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	log.Printf("agent %s registered with control plane at %s", a.cfg.NodeID, a.cfg.ControlPlaneURL)

	heartbeat := time.NewTicker(a.cfg.HeartbeatPeriod)
	poll := time.NewTicker(a.cfg.PollPeriod)
	defer heartbeat.Stop()
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-heartbeat.C:
			if err := a.heartbeat(ctx); err != nil {
				log.Printf("heartbeat failed: %v", err)
			}
		case <-poll.C:
			if err := a.pollAndExecute(ctx); err != nil {
				log.Printf("poll/execute failed: %v", err)
			}
		}
	}
}

func (a *Agent) register(ctx context.Context) error {
	capacity, err := a.docker.Capacity(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{
		"id":           a.cfg.NodeID,
		"address":      a.cfg.Address,
		"architecture": "amd64",
		"labels":       map[string]string{},
		"capabilities": map[string]bool{},
		"cpu_millis":   capacity.CPUMillis,
		"memory_bytes": capacity.MemoryBytes,
	}
	return a.post(ctx, "/v1/nodes", body, nil)
}

func (a *Agent) heartbeat(ctx context.Context) error {
	return a.post(ctx, "/v1/nodes/"+a.cfg.NodeID+"/heartbeat", nil, nil)
}

func (a *Agent) pollAndExecute(ctx context.Context) error {
	var assignments []store.WorkloadRecord
	if err := a.get(ctx, "/v1/agent/"+a.cfg.NodeID+"/assignments", &assignments); err != nil {
		return err
	}

	assignedHere := make(map[string]bool, len(assignments))
	for _, rec := range assignments {
		assignedHere[rec.Key()] = true
		if rec.Status == model.StatusRunning || rec.Status == model.StatusReady {
			alive, err := a.docker.IsRunning(ctx, rec.Key())
			if err != nil {
				log.Printf("liveness check failed for %s: %v", rec.Key(), err)
				continue
			}
			if alive {
				continue
			}
			log.Printf("%s reported %s but its container is gone; re-executing (SRS safe retries)", rec.Key(), rec.Status)
		}
		a.execute(ctx, rec)
	}

	a.cleanupOrphans(ctx, assignedHere)
	return nil
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
// ready, reporting each transition (SRS 6.2, contracts "Runtime state").
// DependsOn is resolved and gated first (SRS 7.1 depends_on, FR-014,
// UC-05): a dependency's env is injected only once it has a ready
// instance, resolved live via the control plane's service registry
// (UC-06) rather than hard-coded — this is how a workload scheduled on
// one node reaches a dependency placed on another (SRS FR-012).
func (a *Agent) execute(ctx context.Context, rec store.WorkloadRecord) {
	key := rec.Key()

	depEnv, ready, err := a.resolveDependencies(ctx, rec.Project, rec.DependsOn)
	if err != nil {
		a.report(ctx, key, model.StatusFailed, "dependency resolution failed: "+err.Error(), "")
		return
	}
	if !ready {
		a.report(ctx, key, model.StatusWaitingDependencies, "waiting for: "+strings.Join(rec.DependsOn, ", "), "")
		return
	}
	for k, v := range depEnv {
		if rec.Env == nil {
			rec.Env = map[string]string{}
		}
		rec.Env[k] = v
	}

	a.report(ctx, key, model.StatusPulling, "", "")
	if err := a.docker.PullImage(ctx, rec.Image); err != nil {
		a.report(ctx, key, model.StatusFailed, "pull failed: "+err.Error(), "")
		return
	}

	a.report(ctx, key, model.StatusStarting, "", "")
	containerID, volWarnings, err := a.docker.EnsureRunning(ctx, rec.Workload)
	if err != nil {
		a.report(ctx, key, model.StatusFailed, "start failed: "+err.Error(), "")
		return
	}
	for _, w := range volWarnings {
		log.Printf("%s: %s", key, w)
	}

	if err := a.docker.WaitRunning(ctx, containerID, 30*time.Second); err != nil {
		a.report(ctx, key, model.StatusFailed, "did not reach running: "+err.Error(), containerID)
		return
	}

	a.report(ctx, key, model.StatusRunning, "", containerID)
	// M2 has no real readiness probe yet (healthcheck handling is part of
	// the Compose importer, M4); a running container is immediately ready.
	a.report(ctx, key, model.StatusReady, "", containerID)
}

// resolveDependencies looks up every dependency's current ready instance
// via the control plane's service registry and returns the env vars to
// inject (<SERVICE>_HOST, <SERVICE>_PORT). ready is false if any
// dependency currently has no ready instance.
func (a *Agent) resolveDependencies(ctx context.Context, project string, deps []string) (map[string]string, bool, error) {
	env := map[string]string{}
	for _, dep := range deps {
		var instances []model.ServiceInstance
		if err := a.get(ctx, "/v1/services/"+project+"/"+dep, &instances); err != nil {
			return nil, false, err
		}
		if len(instances) == 0 {
			return nil, false, nil
		}
		name := strings.ToUpper(strings.NewReplacer("-", "_").Replace(dep))
		env[name+"_HOST"] = instances[0].Address
		env[name+"_PORT"] = fmt.Sprint(instances[0].Port)
	}
	return env, true, nil
}

func (a *Agent) report(ctx context.Context, key string, status model.Status, reason, containerID string) {
	body := map[string]any{"key": key, "status": status, "reason": reason, "container_id": containerID}
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
