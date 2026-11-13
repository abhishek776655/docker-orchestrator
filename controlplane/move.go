package controlplane

import (
	"context"
	"errors"
	"fmt"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

// ErrMoveNeedsEmptyVolume: the service keeps data in a node-local volume,
// which stays on the node it was created on. Moving it means starting with an
// empty volume, which the operator has to accept.
var ErrMoveNeedsEmptyVolume = errors.New("moving needs the operator to accept an empty volume")

// ErrNotMovable: the service can't be moved as it is right now.
var ErrNotMovable = errors.New("service cannot be moved")

// MoveResult is what MoveWorkload did.
type MoveResult struct {
	From      string          `json:"from"`
	To        string          `json:"to"`
	Decision  *model.Decision `json:"decision,omitempty"`
	Restarted []string        `json:"restarted"` // dependents restarted to pick up the new address
}

// MoveWorkload runs a service on another node: the old node's agent removes
// its container, the new node's agent starts it, and the services that
// depend on it are restarted so their address entries point at the new node
// (an address is resolved when a container starts, not afterwards).
//
// force places it even if the scheduler rejects the node (a busy port, not
// enough memory). acceptEmptyVolume is required for a service with a
// node-local volume: the data stays on the old node.
func (svc *Service) MoveWorkload(ctx context.Context, key, node string, force, acceptEmptyVolume bool) (*MoveResult, error) {
	rec, err := svc.store.GetWorkload(ctx, key)
	if err != nil {
		return nil, err
	}
	if node == "" {
		return nil, fmt.Errorf("%w: choose a node", ErrNotMovable)
	}
	if rec.NodeID == node {
		return nil, fmt.Errorf("%w: it already runs on %s", ErrNotMovable, node)
	}
	if rec.DesiredState != model.DesiredRunning || rec.Status == model.StatusStopped {
		return nil, fmt.Errorf("%w: it is stopped; start it first, then move it", ErrNotMovable)
	}

	w := rec.Workload
	if w.Storage.Class == model.NodeLocalPersistent {
		if !acceptEmptyVolume {
			return nil, fmt.Errorf("%w: its data is on %s; on %s it would start with an empty volume", ErrMoveNeedsEmptyVolume, rec.NodeID, node)
		}
		w.Storage.PinnedNodeID = node // the volume's home is now the new node
	}
	// What the new node has to fetch from this control plane: it holds those
	// copies in memory only, so after a restart there is nothing to send.
	for _, v := range w.Volumes {
		if v.RemoteDirKey != "" {
			if _, ok := svc.FetchVolumeSnapshot(v.RemoteDirKey); !ok {
				return nil, fmt.Errorf("%w: the control plane no longer holds the source snapshot for %s (it restarted since the deploy), so another node can't get it; redeploy the service instead", ErrNotMovable, v.ContainerPath)
			}
		}
	}
	if w.RemoteBuild != nil {
		if _, ok := svc.FetchBuildContext(key); !ok {
			return nil, fmt.Errorf("%w: the control plane no longer holds its build context (it restarted since the deploy), so another node can't build it; redeploy the service instead", ErrNotMovable)
		}
	}

	from := rec.NodeID
	w.Constraints.PinnedNodeID = node
	w.Constraints.Forced = force
	result, err := svc.Submit(ctx, w, false)
	if err != nil {
		failed := &MoveResult{From: from, To: node}
		if result != nil {
			failed.Decision = result.Decision
		}
		return failed, err
	}
	_ = svc.store.UpdateStatus(ctx, key, model.StatusAssigned, fmt.Sprintf("moved from %s to %s by operator", from, node), "")

	out := &MoveResult{From: from, To: node, Decision: result.Decision, Restarted: []string{}}
	all, err := svc.store.ListWorkloads(ctx, rec.Project)
	if err != nil {
		return out, err
	}
	for _, d := range dependentsToRestart([]store.WorkloadRecord{*rec}, all, rec.Project) {
		if err := svc.RestartWorkload(ctx, d.Key()); err != nil {
			return out, fmt.Errorf("restart %s: %w", d.Service, err)
		}
		out.Restarted = append(out.Restarted, d.Service)
	}
	return out, nil
}
