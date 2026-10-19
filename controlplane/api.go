package controlplane

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

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

	mux.HandleFunc("GET /v1/services/{project}/{service}", svc.handleListServiceInstances)

	mux.HandleFunc("POST /v1/compose/plan", svc.handleComposePlan)
	mux.HandleFunc("POST /v1/compose/deploy", svc.handleComposeDeploy)

	mux.HandleFunc("GET /v1/events", svc.handleListEvents)
	mux.HandleFunc("GET /v1/metrics", svc.handleMetrics)

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
	GPUs         []model.GPU       `json:"gpus"`
	Thermal      model.Thermal     `json:"thermal"`
	CachedImages []string          `json:"cached_images"`
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
		GPUs:         req.GPUs,
		Thermal:      req.Thermal,
		CachedImages: req.CachedImages,
	}
	if err := svc.RegisterNode(r.Context(), n); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

type heartbeatRequest struct {
	Thermal      model.Thermal `json:"thermal"`
	CachedImages []string      `json:"cached_images"`
}

func (svc *Service) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req heartbeatRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // telemetry is optional; a bare heartbeat still counts

	if err := svc.Heartbeat(r.Context(), id, req.Thermal, req.CachedImages); err != nil {
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
	Project             string               `json:"project"`
	Service             string               `json:"service"`
	Revision            string               `json:"revision"`
	Image               string               `json:"image"`
	Command             []string             `json:"command"`
	Env                 map[string]string    `json:"env"`
	CPUMillis           int64                `json:"cpu_millis"`
	MemoryBytes         int64                `json:"memory_bytes"`
	Architecture        string               `json:"architecture"`
	HostPorts           []int                `json:"host_ports"`
	ContainerPorts      []int                `json:"container_ports"`
	StorageClass        string               `json:"storage_class"`
	StoragePinnedNodeID string               `json:"storage_pinned_node_id"`
	Volumes             []model.VolumeMount  `json:"volumes"`
	DependsOn           []string             `json:"depends_on"`
	GPU                 model.GPURequirement `json:"gpu"`
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
		Command:  req.Command,
		Env:      req.Env,
		Resources: model.Resources{
			CPUMillis:   req.CPUMillis,
			MemoryBytes: req.MemoryBytes,
		},
		Constraints: model.Constraints{
			Architecture: req.Architecture,
			HostPorts:    req.HostPorts,
			GPU:          req.GPU,
		},
		ContainerPorts: req.ContainerPorts,
		Storage: model.StoragePolicy{
			Class:        storageClass,
			PinnedNodeID: req.StoragePinnedNodeID,
		},
		Volumes:      req.Volumes,
		DesiredState: model.DesiredRunning,
		DependsOn:    req.DependsOn,
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

func (svc *Service) handleListServiceInstances(w http.ResponseWriter, r *http.Request) {
	instances, err := svc.ListServiceInstances(r.Context(), r.PathValue("project"), r.PathValue("service"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, instances)
}

type composeRequest struct {
	Project    string `json:"project"`
	WorkingDir string `json:"working_dir"`
	Yaml       string `json:"yaml"`
}

func (svc *Service) handleComposePlan(w http.ResponseWriter, r *http.Request) {
	var req composeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	plan, err := svc.ComposePlan(r.Context(), req.Project, req.WorkingDir, []byte(req.Yaml))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status := http.StatusOK
	if !plan.Valid() {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, plan)
}

func (svc *Service) handleComposeDeploy(w http.ResponseWriter, r *http.Request) {
	var req composeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	plan, results, err := svc.ComposeDeploy(r.Context(), req.Project, req.WorkingDir, []byte(req.Yaml))
	if err != nil {
		status := http.StatusInternalServerError
		if plan != nil && !plan.Valid() {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "plan": plan, "results": results})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"plan": plan, "results": results})
}

func (svc *Service) handleListEvents(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	events, err := svc.ListEvents(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (svc *Service) handleMetrics(w http.ResponseWriter, r *http.Request) {
	m, err := svc.Metrics(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
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
