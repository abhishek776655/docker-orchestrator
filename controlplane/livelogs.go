package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Agents only ever call the control plane, so live logs are relayed: a
// caller queues a request for the node running the workload, that node's
// agent picks it up on its next log poll, reads the container's logs and
// posts them back, and the waiting caller gets them.

var liveLogTimeout = 15 * time.Second

// LiveLogRequest is what an agent receives: which workload's container to
// read and how many trailing lines.
type LiveLogRequest struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Tail int    `json:"tail"`
}

// LiveLogResponse is what an agent posts back.
type LiveLogResponse struct {
	ID    string `json:"id"`
	Logs  string `json:"logs"`
	Error string `json:"error,omitempty"`
}

type liveLogWaiter struct {
	node string
	ch   chan LiveLogResponse
}

type liveLogRelay struct {
	mu      sync.Mutex
	pending map[string][]LiveLogRequest
	waiters map[string]liveLogWaiter
}

var errLiveLogsUnavailable = errors.New("live logs unavailable")

// RequestLiveLogs asks the node running key for its container's last tail
// lines and waits for the answer.
func (svc *Service) RequestLiveLogs(ctx context.Context, key string, tail int) (string, error) {
	rec, err := svc.GetWorkload(ctx, key)
	if err != nil {
		return "", err
	}
	if rec.NodeID == "" {
		return "", fmt.Errorf("%w: workload %q is not assigned to a node", errLiveLogsUnavailable, key)
	}
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return "", err
	}
	req := LiveLogRequest{ID: hex.EncodeToString(idBytes), Key: key, Tail: tail}
	ch := make(chan LiveLogResponse, 1)

	relay := &svc.liveLogs
	relay.mu.Lock()
	if relay.pending == nil {
		relay.pending = map[string][]LiveLogRequest{}
		relay.waiters = map[string]liveLogWaiter{}
	}
	relay.pending[rec.NodeID] = append(relay.pending[rec.NodeID], req)
	relay.waiters[req.ID] = liveLogWaiter{node: rec.NodeID, ch: ch}
	relay.mu.Unlock()

	defer func() {
		relay.mu.Lock()
		delete(relay.waiters, req.ID)
		queue := relay.pending[rec.NodeID]
		for i, r := range queue {
			if r.ID == req.ID {
				relay.pending[rec.NodeID] = append(queue[:i:i], queue[i+1:]...)
				break
			}
		}
		relay.mu.Unlock()
	}()

	timer := time.NewTimer(liveLogTimeout)
	defer timer.Stop()
	select {
	case resp := <-ch:
		if resp.Error != "" {
			return "", fmt.Errorf("%w: node %s: %s", errLiveLogsUnavailable, rec.NodeID, resp.Error)
		}
		return resp.Logs, nil
	case <-timer.C:
		return "", fmt.Errorf("%w: node %s did not answer within %s (offline, or running an agent without live-log support)", errLiveLogsUnavailable, rec.NodeID, liveLogTimeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// TakeLiveLogRequests hands a node every request queued for it.
func (svc *Service) TakeLiveLogRequests(node string) []LiveLogRequest {
	relay := &svc.liveLogs
	relay.mu.Lock()
	defer relay.mu.Unlock()
	reqs := relay.pending[node]
	delete(relay.pending, node)
	return reqs
}

// DeliverLiveLogs hands an agent's answer to its waiting caller. A node can
// only answer its own requests.
func (svc *Service) DeliverLiveLogs(node string, resp LiveLogResponse) bool {
	relay := &svc.liveLogs
	relay.mu.Lock()
	w, ok := relay.waiters[resp.ID]
	relay.mu.Unlock()
	if !ok || w.node != node {
		return false
	}
	select {
	case w.ch <- resp:
	default:
	}
	return true
}

func (svc *Service) handleTakeLiveLogRequests(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if authenticated := authenticatedNode(r); authenticated != "" && authenticated != node {
		writeError(w, http.StatusForbidden, fmt.Errorf("credential is not authorized for node %q", node))
		return
	}
	reqs := svc.TakeLiveLogRequests(node)
	if reqs == nil {
		reqs = []LiveLogRequest{}
	}
	writeJSON(w, http.StatusOK, reqs)
}

func (svc *Service) handleDeliverLiveLogs(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if authenticated := authenticatedNode(r); authenticated != "" && authenticated != node {
		writeError(w, http.StatusForbidden, fmt.Errorf("credential is not authorized for node %q", node))
		return
	}
	var resp LiveLogResponse
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&resp); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !svc.DeliverLiveLogs(node, resp) {
		writeError(w, http.StatusNotFound, fmt.Errorf("no pending live-log request %q for node %q", resp.ID, node))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
