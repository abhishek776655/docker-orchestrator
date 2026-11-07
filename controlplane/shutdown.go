package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// Cluster shutdown, relayed the same way as live logs: the control plane
// flags each node, that node's agent sees the flag on its 1 s control poll,
// stops (not removes) its containers, marks them to restart on return,
// reports back and exits. Remote nodes go first, this host's own node
// last, then the control plane process exits.

var nodeShutdownTimeout = 2 * time.Minute

// ErrShuttingDown is returned for operations refused during a shutdown.
var ErrShuttingDown = errors.New("cluster is shutting down")

// NodeShutdownResult is what a node's agent reports after stopping.
type NodeShutdownResult struct {
	Stopped []string `json:"stopped"`
	Errors  []string `json:"errors,omitempty"`
}

// ShutdownReport is the per-node outcome returned to `hacoctl shutdown`.
type ShutdownReport struct {
	Nodes map[string]NodeShutdownResult `json:"nodes"`
	// NotReached lists nodes that couldn't be told: already unavailable
	// or paused, or they never answered. Their containers keep running.
	NotReached map[string]string `json:"not_reached,omitempty"`
}

type shutdownState struct {
	mu        sync.Mutex
	active    bool
	requested map[string]bool
	done      map[string]chan NodeShutdownResult
	onExit    func()
}

// WithShutdownHook sets what runs once a cluster shutdown has finished (the
// control plane binary passes a func that ends its own process).
func (svc *Service) WithShutdownHook(fn func()) *Service {
	svc.shutdown.onExit = fn
	return svc
}

// ShuttingDown reports whether a cluster shutdown has started.
func (svc *Service) ShuttingDown() bool {
	svc.shutdown.mu.Lock()
	defer svc.shutdown.mu.Unlock()
	return svc.shutdown.active
}

// ShutdownCluster stops every reachable node's containers and agent:
// remote nodes first, this host's node last.
func (svc *Service) ShutdownCluster(ctx context.Context) (*ShutdownReport, error) {
	st := &svc.shutdown
	st.mu.Lock()
	if st.active {
		st.mu.Unlock()
		return nil, fmt.Errorf("%w already", ErrShuttingDown)
	}
	st.active = true
	st.requested = map[string]bool{}
	st.done = map[string]chan NodeShutdownResult{}
	st.mu.Unlock()

	nodes, err := svc.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	report := &ShutdownReport{Nodes: map[string]NodeShutdownResult{}, NotReached: map[string]string{}}
	host := agent.HostNodeID()
	var remote []string
	hostReachable := false
	for _, n := range nodes {
		switch n.Availability {
		case model.Available, model.Disabled:
			if n.ID == host {
				hostReachable = true
			} else {
				remote = append(remote, n.ID)
			}
		default:
			report.NotReached[n.ID] = fmt.Sprintf("node is %s; its containers, if any, are still running", n.Availability)
		}
	}
	sort.Strings(remote)

	svc.shutdownNodes(ctx, remote, report)
	if hostReachable {
		svc.shutdownNodes(ctx, []string{host}, report)
	}
	if len(report.NotReached) == 0 {
		report.NotReached = nil
	}
	return report, nil
}

func (svc *Service) shutdownNodes(ctx context.Context, ids []string, report *ShutdownReport) {
	if len(ids) == 0 {
		return
	}
	st := &svc.shutdown
	chans := make(map[string]chan NodeShutdownResult, len(ids))
	st.mu.Lock()
	for _, id := range ids {
		ch := make(chan NodeShutdownResult, 1)
		st.requested[id] = true
		st.done[id] = ch
		chans[id] = ch
	}
	st.mu.Unlock()

	deadline := time.NewTimer(nodeShutdownTimeout)
	defer deadline.Stop()
	for _, id := range ids {
		select {
		case res := <-chans[id]:
			report.Nodes[id] = res
			if err := svc.store.PauseNode(context.WithoutCancel(ctx), id); err != nil {
				log.Printf("shutdown: pause %s: %v", id, err)
			}
		case <-deadline.C:
			report.NotReached[id] = fmt.Sprintf("agent did not confirm within %s; its containers may still be running", nodeShutdownTimeout)
		case <-ctx.Done():
			report.NotReached[id] = "shutdown request was cancelled"
		}
	}
}

// Exit runs the shutdown hook, if one is set.
func (svc *Service) Exit() {
	if svc.shutdown.onExit != nil {
		svc.shutdown.onExit()
	}
}

func (svc *Service) shutdownRequested(node string) bool {
	svc.shutdown.mu.Lock()
	defer svc.shutdown.mu.Unlock()
	return svc.shutdown.requested[node]
}

// confirmNodeShutdown hands a node's result to the waiting shutdown; only a
// node that was actually asked can confirm.
func (svc *Service) confirmNodeShutdown(node string, res NodeShutdownResult) bool {
	st := &svc.shutdown
	st.mu.Lock()
	ch, ok := st.done[node]
	st.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- res:
	default:
	}
	return true
}

func (svc *Service) refuseDuringShutdown(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.ShuttingDown() {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("%w; run `hacoctl start` again once it has stopped", ErrShuttingDown))
			return
		}
		next(w, r)
	}
}

func (svc *Service) handleShutdown(w http.ResponseWriter, r *http.Request) {
	report, err := svc.ShutdownCluster(r.Context())
	if errors.Is(err, ErrShuttingDown) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	// Let the response reach hacoctl before the process goes away.
	go func() {
		time.Sleep(500 * time.Millisecond)
		svc.Exit()
	}()
}

// handleAgentControl is the agent's 1 s poll for cluster-level instructions.
func (svc *Service) handleAgentControl(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if authenticated := authenticatedNode(r); authenticated != "" && authenticated != node {
		writeError(w, http.StatusForbidden, fmt.Errorf("credential is not authorized for node %q", node))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"shutdown": svc.shutdownRequested(node)})
}

func (svc *Service) handleShutdownDone(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if authenticated := authenticatedNode(r); authenticated != "" && authenticated != node {
		writeError(w, http.StatusForbidden, fmt.Errorf("credential is not authorized for node %q", node))
		return
	}
	var res NodeShutdownResult
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&res); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !svc.confirmNodeShutdown(node, res) {
		writeError(w, http.StatusNotFound, fmt.Errorf("node %q was not asked to shut down", node))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (svc *Service) handlePauseNode(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("id")
	if authenticated := authenticatedNode(r); authenticated != "" && authenticated != node {
		writeError(w, http.StatusForbidden, fmt.Errorf("credential is not authorized for node %q", node))
		return
	}
	if err := svc.store.PauseNode(r.Context(), node); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
