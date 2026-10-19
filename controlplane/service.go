// Package controlplane implements the M2 control-plane API: node
// registration, workload submission (scheduled via the M1 simulator),
// status queries, and the agent's pull-based assignment feed.
package controlplane

import (
	"context"
	"fmt"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/scheduler"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

// Service orchestrates the scheduler (M1) over persisted node/workload
// state (M2). Every call rebuilds a fresh Scheduler from the database so
// there is exactly one source of truth (SRS NFR-001).
type Service struct {
	store *store.Store
}

func New(s *store.Store) *Service { return &Service{store: s} }

// RegisterNode upserts a node's identity and capacity.
func (svc *Service) RegisterNode(ctx context.Context, n model.Node) error {
	n.Availability = model.Available
	n.HeartbeatAt = time.Now()
	return svc.store.UpsertNode(ctx, n)
}

// Heartbeat refreshes a node's last-seen timestamp.
func (svc *Service) Heartbeat(ctx context.Context, nodeID string) error {
	return svc.store.Heartbeat(ctx, nodeID)
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
	cpuMem, ports, err := svc.store.ActiveReservations(ctx)
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
