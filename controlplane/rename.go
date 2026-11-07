package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/agentjoin"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

// Renaming a node touches both sides: the database (here) and the node's
// own data folder, credential and container labels (its agent). The agent
// is told on its control poll, stops and removes its containers, asks to
// commit; only once the database transaction has succeeded does it move
// its local files and restart under the new ID.

var nodeRenameTimeout = 5 * time.Minute

var errRenameInvalid = errors.New("invalid rename")

type pendingRename struct {
	newID string
	done  chan error
}

type renameState struct {
	mu      sync.Mutex
	pending map[string]*pendingRename
}

// RenameNode asks the node's running agent to move to newID and waits
// until the database side has committed.
func (svc *Service) RenameNode(ctx context.Context, oldID, newID string) error {
	if !agentjoin.ValidNodeID(newID) {
		return fmt.Errorf("%w: %q is not a valid node ID (letters, digits, '-', '_', '.')", errRenameInvalid, newID)
	}
	if newID == oldID {
		return fmt.Errorf("%w: node is already called %q", errRenameInvalid, oldID)
	}
	nodes, err := svc.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	var found *model.Node
	for i := range nodes {
		switch nodes[i].ID {
		case newID:
			return fmt.Errorf("%w: %q", store.ErrNodeIDTaken, newID)
		case oldID:
			found = &nodes[i]
		}
	}
	if found == nil {
		return fmt.Errorf("%w: node %q", store.ErrNotFound, oldID)
	}
	if found.Availability != model.Available && found.Availability != model.Disabled {
		return fmt.Errorf("%w: node %q is %s; its agent must be running to move its own data folder and credential (run `hacoctl agent run` on that machine first)", errRenameInvalid, oldID, found.Availability)
	}

	p := &pendingRename{newID: newID, done: make(chan error, 1)}
	st := &svc.renames
	st.mu.Lock()
	if st.pending == nil {
		st.pending = map[string]*pendingRename{}
	}
	if _, busy := st.pending[oldID]; busy {
		st.mu.Unlock()
		return fmt.Errorf("%w: a rename of %q is already in progress", errRenameInvalid, oldID)
	}
	st.pending[oldID] = p
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		delete(st.pending, oldID)
		st.mu.Unlock()
	}()

	timer := time.NewTimer(nodeRenameTimeout)
	defer timer.Stop()
	select {
	case err := <-p.done:
		return err
	case <-timer.C:
		return fmt.Errorf("node %q's agent did not complete the rename within %s; nothing was renamed", oldID, nodeRenameTimeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (svc *Service) pendingRenameFor(node string) string {
	svc.renames.mu.Lock()
	defer svc.renames.mu.Unlock()
	if p, ok := svc.renames.pending[node]; ok {
		return p.newID
	}
	return ""
}

// commitRename runs the database transaction for a rename the node's agent
// is ready for, and wakes the waiting caller.
func (svc *Service) commitRename(ctx context.Context, oldID, newID string) error {
	svc.renames.mu.Lock()
	p, ok := svc.renames.pending[oldID]
	svc.renames.mu.Unlock()
	if !ok || p.newID != newID {
		return fmt.Errorf("%w: no pending rename of %q to %q", errRenameInvalid, oldID, newID)
	}
	err := svc.store.RenameNode(ctx, oldID, newID)
	select {
	case p.done <- err:
	default:
	}
	return err
}

func (svc *Service) abortRename(oldID string, err error) {
	svc.renames.mu.Lock()
	p, ok := svc.renames.pending[oldID]
	svc.renames.mu.Unlock()
	if ok {
		select {
		case p.done <- err:
		default:
		}
	}
}

func (svc *Service) handleRenameNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NewID string `json:"new_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	oldID := r.PathValue("id")
	err := svc.RenameNode(r.Context(), oldID, req.NewID)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"old_id": oldID, "new_id": req.NewID})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrNodeIDTaken), errors.Is(err, errRenameInvalid):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func (svc *Service) handleRenameCommit(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if authenticated := authenticatedNode(r); authenticated != "" && authenticated != node {
		writeError(w, http.StatusForbidden, fmt.Errorf("credential is not authorized for node %q", node))
		return
	}
	var req struct {
		NewID string `json:"new_id"`
		// Error, when set, means the agent can't rename; nothing changes
		// and the caller is told why right away.
		Error string `json:"error,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Error != "" {
		svc.abortRename(node, fmt.Errorf("%w: node %q's agent refused: %s", errRenameInvalid, node, req.Error))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := svc.commitRename(r.Context(), node, req.NewID); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
