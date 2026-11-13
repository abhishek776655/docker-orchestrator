package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

// nodeRequests holds work for a node's agent that the agent picks up on its
// next control poll (about once a second): containers to recreate and
// images to delete. In memory only: a request lost to a control-plane
// restart is simply asked for again.
type nodeRequests struct {
	mu      sync.Mutex
	restart map[string][]string // node -> workload keys
	images  map[string][]string // node -> image references
	volumes map[string][]string // node -> workload keys whose volume data to delete
}

func (q *nodeRequests) add(target *map[string][]string, node string, items ...string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if *target == nil {
		*target = map[string][]string{}
	}
	seen := map[string]bool{}
	for _, v := range (*target)[node] {
		seen[v] = true
	}
	for _, v := range items {
		if !seen[v] {
			(*target)[node] = append((*target)[node], v)
			seen[v] = true
		}
	}
}

// take hands a node's pending requests to its agent and forgets them.
func (q *nodeRequests) take(node string) (restart, images, volumes []string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	restart, images, volumes = q.restart[node], q.images[node], q.volumes[node]
	delete(q.restart, node)
	delete(q.images, node)
	delete(q.volumes, node)
	return restart, images, volumes
}

func (svc *Service) requests() *nodeRequests { return &svc.nodeReqs }

// RestartWorkload recreates a workload's container on the node it runs on.
func (svc *Service) RestartWorkload(ctx context.Context, key string) error {
	node, err := svc.store.RestartWorkload(ctx, key)
	if err != nil {
		return err
	}
	if node != "" {
		q := svc.requests()
		q.add(&q.restart, node, key)
	}
	return nil
}

// ErrNoRoom is returned when a stopped workload cannot start where it last
// ran because something else took its port or capacity.
var ErrNoRoom = errors.New("no room on its node")

// StartWorkload runs a stopped workload again on the node it last ran on.
// It checks first that the node still has the port and capacity free, since
// other workloads may have been placed there while it was stopped.
func (svc *Service) StartWorkload(ctx context.Context, key string) error {
	rec, err := svc.store.GetWorkload(ctx, key)
	if err != nil {
		return err
	}
	if rec.Status != model.StatusStopped && rec.Status != model.StatusFailed {
		return nil // already running or on its way
	}
	if rec.NodeID == "" {
		return fmt.Errorf("%w: %s was never placed; deploy it instead", ErrNoRoom, key)
	}
	nodes, err := svc.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	var node *model.Node
	for i := range nodes {
		if nodes[i].ID == rec.NodeID {
			node = &nodes[i]
		}
	}
	if node == nil {
		return fmt.Errorf("%w: node %s is gone; redeploy to place it elsewhere", ErrNoRoom, rec.NodeID)
	}
	cpuMem, ports, err := svc.store.ActiveReservations(ctx, key)
	if err != nil {
		return err
	}
	for _, p := range rec.Constraints.HostPorts {
		if holder, taken := ports[node.ID][p]; taken {
			return fmt.Errorf("%w: host port %d on %s is now used by %s", ErrNoRoom, p, node.ID, holder)
		}
	}
	used := cpuMem[node.ID]
	if node.Allocatable.CPUMillis-used.CPUMillis < rec.Resources.CPUMillis {
		return fmt.Errorf("%w: %s has no CPU left for it", ErrNoRoom, node.ID)
	}
	if node.Allocatable.MemoryBytes-used.MemoryBytes < rec.Resources.MemoryBytes {
		return fmt.Errorf("%w: %s has no memory left for it", ErrNoRoom, node.ID)
	}
	return svc.store.StartWorkload(ctx, key)
}

// RemoveNodeImages asks a node's agent to delete images from its Docker.
// Results come back as node events.
func (svc *Service) RemoveNodeImages(ctx context.Context, nodeID string, images []string) error {
	nodes, err := svc.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, n := range nodes {
		found = found || n.ID == nodeID
	}
	if !found {
		return fmt.Errorf("%w: node %q", store.ErrNotFound, nodeID)
	}
	var clean []string
	for _, im := range images {
		if im = strings.TrimSpace(im); im != "" {
			clean = append(clean, im)
		}
	}
	if len(clean) == 0 {
		return errors.New("no images given")
	}
	q := svc.requests()
	q.add(&q.images, nodeID, clean...)
	return nil
}

// ErrVolumeInUse is returned when a workload still runs on the node whose
// volume data would be deleted.
var ErrVolumeInUse = errors.New("volume is in use")

// RemoveVolumes asks a node's agent to delete the volume data of the given
// workloads. Refused for a workload that is meant to be running on that
// node: stop it first. Deleting data on a node it no longer runs on (left
// behind after a move) is always allowed.
func (svc *Service) RemoveVolumes(ctx context.Context, nodeID string, keys []string) error {
	nodes, err := svc.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, n := range nodes {
		found = found || n.ID == nodeID
	}
	if !found {
		return fmt.Errorf("%w: node %q", store.ErrNotFound, nodeID)
	}
	if len(keys) == 0 {
		return errors.New("no workloads given")
	}
	for _, k := range keys {
		rec, err := svc.store.GetWorkload(ctx, k)
		if errors.Is(err, store.ErrNotFound) {
			continue // record gone; its data may still be on disk
		}
		if err != nil {
			return err
		}
		if rec.NodeID == nodeID && rec.Status != model.StatusStopped && rec.Status != model.StatusFailed {
			return fmt.Errorf("%w: %s is %s on %s; stop it first", ErrVolumeInUse, k, rec.Status, nodeID)
		}
	}
	q := svc.requests()
	q.add(&q.volumes, nodeID, keys...)
	return nil
}

func (svc *Service) handleRemoveVolumes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Workloads []string `json:"workloads"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	err := svc.RemoveVolumes(r.Context(), r.PathValue("id"), req.Workloads)
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "requested", "count": len(req.Workloads)})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, ErrVolumeInUse):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}

// handleVolumesRemoved records what a node's agent deleted, as node events.
func (svc *Service) handleVolumesRemoved(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if authenticated := authenticatedNode(r); authenticated != "" && authenticated != node {
		writeError(w, http.StatusForbidden, fmt.Errorf("credential is not authorized for node %q", node))
		return
	}
	var res struct {
		Removed []string          `json:"removed"`
		Failed  map[string]string `json:"failed"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&res); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	for _, k := range res.Removed {
		svc.store.RecordNodeEvent(r.Context(), node, "volume_removed", "deleted volume data of "+k)
	}
	for k, why := range res.Failed {
		svc.store.RecordNodeEvent(r.Context(), node, "volume_remove_failed", fmt.Sprintf("could not delete volume data of %s: %s", k, why))
	}
	w.WriteHeader(http.StatusNoContent)
}

func workloadKeyFrom(r *http.Request) string {
	return strings.Join([]string{r.PathValue("project"), r.PathValue("service"), r.PathValue("revision")}, "/")
}

func (svc *Service) handleRestartWorkload(w http.ResponseWriter, r *http.Request) {
	err := svc.RestartWorkload(r.Context(), workloadKeyFrom(r))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
}

func (svc *Service) handleStartWorkload(w http.ResponseWriter, r *http.Request) {
	err := svc.StartWorkload(r.Context(), workloadKeyFrom(r))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"status": "starting"})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, ErrNoRoom):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func (svc *Service) handleSetNodeName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > 64 {
		writeError(w, http.StatusBadRequest, errors.New("name is longer than 64 characters"))
		return
	}
	if err := svc.store.SetNodeDisplayName(r.Context(), r.PathValue("id"), name); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "name": name})
}

func (svc *Service) handleSetNodeLimits(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CPUMillis   int64 `json:"cpu_millis"`
		MemoryBytes int64 `json:"memory_bytes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.CPUMillis < 0 || req.MemoryBytes < 0 {
		writeError(w, http.StatusBadRequest, errors.New("limits cannot be negative"))
		return
	}
	limits := model.Resources{CPUMillis: req.CPUMillis, MemoryBytes: req.MemoryBytes}
	if err := svc.store.SetNodeLimits(r.Context(), r.PathValue("id"), limits); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "cpu_millis": limits.CPUMillis, "memory_bytes": limits.MemoryBytes})
}

func (svc *Service) handleRemoveNodeImages(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Images []string `json:"images"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	err := svc.RemoveNodeImages(r.Context(), r.PathValue("id"), req.Images)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "requested", "count": len(req.Images)})
}

// handleImagesRemoved records what a node's agent deleted, as node events.
func (svc *Service) handleImagesRemoved(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if authenticated := authenticatedNode(r); authenticated != "" && authenticated != node {
		writeError(w, http.StatusForbidden, fmt.Errorf("credential is not authorized for node %q", node))
		return
	}
	var res struct {
		Removed []string          `json:"removed"`
		Failed  map[string]string `json:"failed"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&res); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	for _, im := range res.Removed {
		svc.store.RecordNodeEvent(r.Context(), node, "image_removed", "removed image "+im)
	}
	for im, why := range res.Failed {
		svc.store.RecordNodeEvent(r.Context(), node, "image_remove_failed", fmt.Sprintf("could not remove image %s: %s", im, why))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (svc *Service) handleMoveWorkload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Node              string `json:"node"`
		Force             bool   `json:"force"`
		AcceptEmptyVolume bool   `json:"accept_empty_volume"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := svc.MoveWorkload(r.Context(), workloadKeyFrom(r), req.Node, req.Force, req.AcceptEmptyVolume)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, result)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, ErrMoveNeedsEmptyVolume), errors.Is(err, ErrNotMovable):
		writeError(w, http.StatusConflict, err)
	case result != nil && result.Decision != nil:
		// The scheduler refused the node: 409 with the reasons, so the
		// caller can offer to force it.
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "decision": result.Decision})
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}
