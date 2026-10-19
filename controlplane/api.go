package controlplane

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// NewHandler builds the control-plane HTTP API (SRS 5.1 "Control Plane
// API"). Paths and payload shapes are intentionally simple/unfrozen, per
// SRS 12.2 — a real OpenAPI contract comes once M2 is proven.
func NewHandler(svc *Service) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/nodes", svc.handleRegisterNode)
	mux.HandleFunc("POST /v1/nodes/{id}/heartbeat", svc.handleHeartbeat)
	mux.HandleFunc("GET /v1/nodes", svc.handleListNodes)

	mux.HandleFunc("POST /v1/workloads", svc.handleSubmit)
	mux.HandleFunc("GET /v1/workloads/{key...}", svc.handleGetWorkload)

	mux.HandleFunc("GET /v1/agent/{node}/assignments", svc.handleAssignments)
	mux.HandleFunc("POST /v1/agent/status", svc.handleReportStatus)

	return logRequests(mux)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		log.Printf("%s %s", r.Method, r.URL.Path)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

type registerNodeRequest struct {
	ID           string            `json:"id"`
	Address      string            `json:"address"`
	Architecture string            `json:"architecture"`
	Labels       map[string]string `json:"labels"`
	Capabilities map[string]bool   `json:"capabilities"`
	CPUMillis    int64             `json:"cpu_millis"`
	MemoryBytes  int64             `json:"memory_bytes"`
}

func (svc *Service) handleRegisterNode(w http.ResponseWriter, r *http.Request) {
	var req registerNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	n := model.Node{
		ID:           req.ID,
		Address:      req.Address,
		Architecture: req.Architecture,
		Labels:       req.Labels,
		Capabilities: req.Capabilities,
		Allocatable:  model.Resources{CPUMillis: req.CPUMillis, MemoryBytes: req.MemoryBytes},
	}
	if err := svc.RegisterNode(r.Context(), n); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (svc *Service) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := svc.Heartbeat(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (svc *Service) handleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := svc.ListNodes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

type submitRequest struct {
	Project             string            `json:"project"`
	Service             string            `json:"service"`
	Revision            string            `json:"revision"`
	Image               string            `json:"image"`
	Env                 map[string]string `json:"env"`
	CPUMillis           int64             `json:"cpu_millis"`
	MemoryBytes         int64             `json:"memory_bytes"`
	Architecture        string            `json:"architecture"`
	HostPorts           []int             `json:"host_ports"`
	StorageClass        string            `json:"storage_class"`
	StoragePinnedNodeID string            `json:"storage_pinned_node_id"`
}

func (svc *Service) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req submitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	storageClass := model.Ephemeral
	if req.StorageClass != "" {
		storageClass = model.StorageClass(req.StorageClass)
	}
	wl := model.Workload{
		Project:  req.Project,
		Service:  req.Service,
		Revision: req.Revision,
		Image:    req.Image,
		Env:      req.Env,
		Resources: model.Resources{
			CPUMillis:   req.CPUMillis,
			MemoryBytes: req.MemoryBytes,
		},
		Constraints: model.Constraints{
			Architecture: req.Architecture,
			HostPorts:    req.HostPorts,
		},
		Storage: model.StoragePolicy{
			Class:        storageClass,
			PinnedNodeID: req.StoragePinnedNodeID,
		},
		DesiredState: model.DesiredRunning,
	}

	result, err := svc.Submit(r.Context(), wl)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    err.Error(),
			"decision": result.Decision,
		})
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (svc *Service) handleGetWorkload(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	rec, err := svc.GetWorkload(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (svc *Service) handleAssignments(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("node")
	recs, err := svc.AssignedWorkloads(r.Context(), nodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, recs)
}

type reportStatusRequest struct {
	Key         string       `json:"key"`
	Status      model.Status `json:"status"`
	Reason      string       `json:"reason"`
	ContainerID string       `json:"container_id"`
}

func (svc *Service) handleReportStatus(w http.ResponseWriter, r *http.Request) {
	var req reportStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := svc.ReportStatus(r.Context(), req.Key, req.Status, req.Reason, req.ContainerID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
