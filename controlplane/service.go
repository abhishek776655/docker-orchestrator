// Package controlplane implements the M2 control-plane API: node
// registration, workload submission (scheduled via the M1 simulator),
// status queries, and the agent's pull-based assignment feed.
package controlplane

import (
	"context"
	"fmt"
	"time"

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
	store   *store.Store
	builder *build.Service // nil disables build-based deploys (image-only workloads still work)
}

func New(s *store.Store) *Service { return &Service{store: s} }

// WithBuilder enables POST /v1/compose/deploy to build services that
// declare a `build:` block (SRS 5.1 "Build Service").
func (svc *Service) WithBuilder(b *build.Service) *Service {
	svc.builder = b
	return svc
}

// ComposePlan parses and validates a Compose file without applying
// anything (SRS UC-02, FR-023).
func (svc *Service) ComposePlan(ctx context.Context, project, workingDir string, yaml []byte) (*compose.Plan, error) {
	return compose.Load(ctx, project, workingDir, yaml)
}

// ComposeDeploy plans a Compose file, builds any services that need it,
// and submits every workload through the normal scheduler path. It stops
// at the first rejection — nothing is applied for an invalid plan
// (FR-003).
func (svc *Service) ComposeDeploy(ctx context.Context, project, workingDir string, yaml []byte) (*compose.Plan, []*SubmitResult, error) {
	plan, err := compose.Load(ctx, project, workingDir, yaml)
	if err != nil {
		return nil, nil, err
	}
	if !plan.Valid() {
		return plan, nil, fmt.Errorf("compose plan for %s has %d rejection(s)", project, len(plan.Rejections))
	}

	builtImages := map[string]string{}
	for _, b := range plan.Builds {
		if svc.builder == nil {
			return plan, nil, fmt.Errorf("service %s requires a build but no builder is configured", b.Service)
		}
		ref, err := svc.builder.Build(ctx, project, "latest", b)
		if err != nil {
			return plan, nil, fmt.Errorf("build %s: %w", b.Service, err)
		}
		builtImages[b.Service] = ref
	}

	var results []*SubmitResult
	for _, wl := range plan.Workloads {
		if ref, ok := builtImages[wl.Service]; ok {
			wl.Image = ref
		}
		result, err := svc.Submit(ctx, wl)
		results = append(results, result)
		if err != nil {
			return plan, results, fmt.Errorf("schedule %s: %w", wl.Service, err)
		}
	}
	return plan, results, nil
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
func (svc *Service) Submit(ctx context.Context, w model.Workload) (*SubmitResult, error) {
	nodes, err := svc.store.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	cpuMem, ports, err := svc.store.ActiveReservations(ctx, w.Key())
	if err != nil {
		return nil, fmt.Errorf("load reservations: %w", err)
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

// AssignedWorkloads is the agent's pull feed for one node.
func (svc *Service) AssignedWorkloads(ctx context.Context, nodeID string) ([]store.WorkloadRecord, error) {
	return svc.store.AssignedWorkloads(ctx, nodeID)
}

// ReportStatus records an agent-observed status transition (SRS 6.2).
func (svc *Service) ReportStatus(ctx context.Context, key string, status model.Status, reason, containerID string) error {
	return svc.store.UpdateStatus(ctx, key, status, reason, containerID)
}

// ListEvents returns the most recent audit/history records (SRS FR-024).
func (svc *Service) ListEvents(ctx context.Context, limit int) ([]store.Event, error) {
	return svc.store.ListEvents(ctx, limit)
}

// Metrics returns a minimal operational summary (SRS §13).
func (svc *Service) Metrics(ctx context.Context) (*store.Metrics, error) {
	return svc.store.Metrics(ctx)
}

// ListServiceInstances resolves a logical service name to its current
// ready endpoints, from any node (SRS 8.1-8.2, UC-06, FR-010, FR-011).
func (svc *Service) ListServiceInstances(ctx context.Context, project, service string) ([]model.ServiceInstance, error) {
	return svc.store.ListReadyServiceInstances(ctx, project, service)
}
