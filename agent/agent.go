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

	for _, rec := range assignments {
		if rec.Status == model.StatusRunning || rec.Status == model.StatusReady {
			continue // already brought up; nothing to do until reconciliation (M6) needs it
		}
		a.execute(ctx, rec)
	}
	return nil
}

// execute drives one workload through pulling -> starting -> running ->
// ready, reporting each transition (SRS 6.2, contracts "Runtime state").
func (a *Agent) execute(ctx context.Context, rec store.WorkloadRecord) {
	key := rec.Key()

	a.report(ctx, key, model.StatusPulling, "", "")
	if err := a.docker.PullImage(ctx, rec.Image); err != nil {
		a.report(ctx, key, model.StatusFailed, "pull failed: "+err.Error(), "")
		return
	}

	a.report(ctx, key, model.StatusStarting, "", "")
	containerID, err := a.docker.EnsureRunning(ctx, rec.Workload)
	if err != nil {
		a.report(ctx, key, model.StatusFailed, "start failed: "+err.Error(), "")
		return
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
