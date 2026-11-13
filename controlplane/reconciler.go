package controlplane

import (
	"context"
	"log"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/scheduler"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
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

	// Nodes that answered again since the last tick: take their workloads
	// back (the heartbeat does this at once too; this is the safety net).
	if err := svc.recoverUnreachable(ctx, ""); err != nil {
		log.Printf("reconcile: recover unreachable workloads: %v", err)
	}

	orphaned, err := svc.store.WorkloadsOnUnavailableNodes(ctx)
	if err != nil {
		return err
	}
	if len(orphaned) == 0 {
		return nil
	}

	nodes, err := svc.listNodesForScheduling(ctx)
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
	if err := svc.seedWorkloadCounts(ctx, sched, nil); err != nil {
		return err
	}
	lastSeen := make(map[string]time.Time, len(nodes))
	for _, n := range nodes {
		lastSeen[n.ID] = n.HeartbeatAt
	}

	for _, wl := range orphaned {
		// The node going quiet is not a failure of the workload: its
		// container may still be running (sleeping laptop, screen off). Mark
		// it unreachable and keep the assignment; it is taken back as soon as
		// the node answers (recoverUnreachable).
		if wl.Status != model.StatusUnreachable {
			reason := "node " + wl.NodeID + " is not responding; waiting for it to come back"
			if err := svc.store.UpdateStatus(ctx, wl.Key(), model.StatusUnreachable, reason, ""); err != nil {
				log.Printf("reconcile: mark unreachable %s: %v", wl.Key(), err)
			}
		}
		if wl.Storage.Class == model.NodeLocalPersistent && !wl.Storage.AllowFreshVolumeOnReschedule {
			// Node affinity for persistent state must never be broken
			// automatically (SRS §6.3, FR-017): keep it where it is and wait
			// for the node, never start it on a second one.
			continue
		}
		// Give the node a while to come back before moving anything: a
		// stateless service started elsewhere would leave the old copy
		// running until the node returns.
		if svc.rescheduleAfter > 0 && time.Since(lastSeen[wl.NodeID]) < svc.rescheduleAfter {
			continue
		}

		// sched.Schedule relaxes the storage-affinity check itself when
		// AllowFreshVolumeOnReschedule is set and the pinned node is down
		// (see scheduler.Schedule), so this call is the same for both
		// ephemeral and opted-in pinned workloads.
		decision, err := sched.Schedule(wl.Workload)
		if err != nil {
			// No healthy node can take it right now; leave it assigned
			// to the dead node and retry on the next tick rather than
			// failing it outright — the node may come back, or capacity
			// may free up.
			log.Printf("reconcile: no eligible node for %s yet: %v", wl.Key(), err)
			continue
		}

		if wl.Storage.Class == model.NodeLocalPersistent {
			if err := svc.store.ReassignWithFreshVolume(ctx, wl.Key(), decision.Selected); err != nil {
				log.Printf("reconcile: reassign %s: %v", wl.Key(), err)
				continue
			}
			log.Printf("reconcile: rescheduled %s from %s to %s onto a FRESH VOLUME (previous data on %s not migrated)", wl.Key(), wl.NodeID, decision.Selected, wl.NodeID)
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

// SetRescheduleAfter sets how long a node must stay silent before the
// stateless services on it are started on another node. Until then they are
// shown as unreachable and wait for it. Zero moves them at once.
func (svc *Service) SetRescheduleAfter(d time.Duration) { svc.rescheduleAfter = d }

// recoverUnreachable takes workloads back on a node that answers again
// (nodeID "" means every available node): they go to assigned, so the node's
// agent adopts the container that kept running or starts it again, and the
// services that depend on them are restarted, since their connections to the
// recovered service are stale.
func (svc *Service) recoverUnreachable(ctx context.Context, nodeID string) error {
	recovered, err := svc.store.RecoverUnreachable(ctx, nodeID)
	if err != nil {
		return err
	}
	if len(recovered) == 0 {
		return nil
	}
	back := map[string]bool{}
	projects := map[string]bool{}
	for _, rec := range recovered {
		log.Printf("reconcile: node %s is back; resuming %s", rec.NodeID, rec.Key())
		back[rec.Key()] = true
		projects[rec.Project] = true
	}
	for project := range projects {
		all, err := svc.store.ListWorkloads(ctx, project)
		if err != nil {
			return err
		}
		for _, rec := range dependentsToRestart(recovered, all, project) {
			if back[rec.Key()] {
				continue
			}
			log.Printf("reconcile: restarting %s because a service it depends on was unreachable", rec.Key())
			if err := svc.RestartWorkload(ctx, rec.Key()); err != nil {
				log.Printf("reconcile: restart %s: %v", rec.Key(), err)
			}
		}
	}
	return nil
}

// dependentsToRestart returns the running workloads of project that depend,
// directly or through other services, on any recovered workload and are not
// themselves among the recovered ones.
func dependentsToRestart(recovered, all []store.WorkloadRecord, project string) []store.WorkloadRecord {
	need := map[string]bool{}
	for _, r := range recovered {
		if r.Project == project {
			need[r.Service] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, w := range all {
			if need[w.Service] {
				continue
			}
			for _, d := range w.DependsOn {
				if need[d] {
					need[w.Service] = true
					changed = true
					break
				}
			}
		}
	}
	recoveredKey := map[string]bool{}
	for _, r := range recovered {
		recoveredKey[r.Key()] = true
	}
	var out []store.WorkloadRecord
	for _, w := range all {
		if !need[w.Service] || recoveredKey[w.Key()] || w.DesiredState != model.DesiredRunning {
			continue
		}
		switch w.Status {
		case model.StatusRunning, model.StatusReady, model.StatusStarting, model.StatusWaitingDependencies:
			out = append(out, w)
		}
	}
	return out
}
