package controlplane

import (
	"context"
	"log"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/scheduler"
)

// RunReconciler periodically marks stale nodes unavailable and reconciles
// the workloads that were assigned to them (SRS §6.3, FR-015, FR-017): a
// stateless workload is rescheduled onto a healthy node; a workload with
// node-local persistent state is left exactly where it is and marked
// failed, never silently started on a second node. It runs until ctx is
// cancelled.
func (svc *Service) RunReconciler(ctx context.Context, interval, heartbeatTimeout time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := svc.reconcileOnce(ctx, heartbeatTimeout); err != nil {
				log.Printf("reconcile: %v", err)
			}
		}
	}
}

func (svc *Service) reconcileOnce(ctx context.Context, heartbeatTimeout time.Duration) error {
	if err := svc.store.MarkStaleNodesUnavailable(ctx, heartbeatTimeout); err != nil {
		return err
	}

	orphaned, err := svc.store.WorkloadsOnUnavailableNodes(ctx)
	if err != nil {
		return err
	}
	if len(orphaned) == 0 {
		return nil
	}

	nodes, err := svc.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	cpuMem, ports, err := svc.store.ActiveReservations(ctx, "")
	if err != nil {
		return err
	}

	// One scheduler instance for the whole tick: Schedule() reserves
	// capacity on the node it selects, so reusing the same instance across
	// every orphaned workload this tick prevents two of them from being
	// double-booked onto the same freshly-freed capacity.
	sched := scheduler.New(nodes)
	for nodeID, res := range cpuMem {
		sched.SeedReservation(nodeID, res, ports[nodeID])
	}

	for _, wl := range orphaned {
		if wl.Storage.Class == model.NodeLocalPersistent {
			// Node affinity for persistent state must never be broken
			// automatically (SRS §6.3, FR-017): report the failure, do
			// not relocate.
			if err := svc.store.UpdateStatus(ctx, wl.Key(), model.StatusFailed, "node "+wl.NodeID+" unavailable; stateful workload is not relocated without verified data access", ""); err != nil {
				log.Printf("reconcile: mark failed %s: %v", wl.Key(), err)
			}
			continue
		}

		decision, err := sched.Schedule(wl.Workload)
		if err != nil {
			// No healthy node can take it right now; leave it assigned
			// to the dead node and retry on the next tick rather than
			// failing it outright — the node may come back, or capacity
			// may free up.
			log.Printf("reconcile: no eligible node for %s yet: %v", wl.Key(), err)
			continue
		}
		if err := svc.store.Reassign(ctx, wl.Key(), decision.Selected); err != nil {
			log.Printf("reconcile: reassign %s: %v", wl.Key(), err)
			continue
		}
		log.Printf("reconcile: rescheduled %s from %s to %s", wl.Key(), wl.NodeID, decision.Selected)
	}
	return nil
}
