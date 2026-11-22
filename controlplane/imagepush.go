package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

// imagePushTimeout bounds how long one push waits for every node to load it.
const imagePushTimeout = 15 * time.Minute

// ImageLoad is one pending `docker load` handed to a node's agent.
type ImageLoad struct {
	ID    string `json:"id"`
	Image string `json:"image"`
}

// imagePush is one uploaded `docker save` archive on its way to some nodes.
// The archive lives in a temp file, never in memory, so a multi-GB image
// costs disk, not RAM.
type imagePush struct {
	id      string
	image   string
	path    string
	mu      sync.Mutex
	waiting map[string]bool   // nodes that have not reported yet
	handed  map[string]bool   // nodes whose agent already took the instruction
	results map[string]string // node -> "" for loaded, else the error
	done    chan struct{}
}

type imagePushState struct {
	mu     sync.Mutex
	pushes map[string]*imagePush
}

func (s *imagePushState) get(id string) *imagePush {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pushes[id]
}

func (s *imagePushState) add(p *imagePush) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pushes == nil {
		s.pushes = map[string]*imagePush{}
	}
	s.pushes[p.id] = p
}

func (s *imagePushState) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pushes, id)
}

// take returns the loads this node's agent has not been told about yet.
func (s *imagePushState) take(node string) []ImageLoad {
	s.mu.Lock()
	pushes := make([]*imagePush, 0, len(s.pushes))
	for _, p := range s.pushes {
		pushes = append(pushes, p)
	}
	s.mu.Unlock()
	var out []ImageLoad
	for _, p := range pushes {
		p.mu.Lock()
		if p.waiting[node] && !p.handed[node] {
			p.handed[node] = true
			out = append(out, ImageLoad{ID: p.id, Image: p.image})
		}
		p.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// report records one node's result; the push finishes with the last one.
func (p *imagePush) report(node, errText string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.waiting[node] {
		return false
	}
	delete(p.waiting, node)
	p.results[node] = errText
	if len(p.waiting) == 0 {
		close(p.done)
	}
	return true
}

// PushImage stores the archive from r and waits for the given nodes to load
// it. The result maps each node to "" (loaded) or an error message.
func (svc *Service) PushImage(ctx context.Context, image string, nodes []string, r io.Reader) (map[string]string, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return nil, errors.New("no image name given")
	}
	if len(nodes) == 0 {
		return nil, errors.New("no nodes given")
	}
	known, err := svc.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, n := range known {
		have[n.ID] = true
	}
	waiting := map[string]bool{}
	for _, n := range nodes {
		if !have[n] {
			return nil, fmt.Errorf("%w: node %q", store.ErrNotFound, n)
		}
		waiting[n] = true
	}

	f, err := os.CreateTemp("", "haco-image-push-*.tar")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return nil, fmt.Errorf("receive image archive: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	var idBytes [8]byte
	_, _ = rand.Read(idBytes[:])
	p := &imagePush{
		id: hex.EncodeToString(idBytes[:]), image: image, path: path,
		waiting: waiting, handed: map[string]bool{}, results: map[string]string{}, done: make(chan struct{}),
	}
	svc.imagePushes.add(p)
	defer svc.imagePushes.remove(p.id)

	select {
	case <-p.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(imagePushTimeout):
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for n := range p.waiting {
		p.results[n] = "timed out waiting for the node to load the image (is its agent running and reachable?)"
	}
	out := make(map[string]string, len(p.results))
	for n, e := range p.results {
		out[n] = e
	}
	return out, nil
}

// handleImagePush: POST /v1/admin/images/push?image=REF&node=A&node=B with
// the `docker save` archive as the body.
func (svc *Service) handleImagePush(w http.ResponseWriter, r *http.Request) {
	results, err := svc.PushImage(r.Context(), r.URL.Query().Get("image"), r.URL.Query()["node"], r.Body)
	switch {
	case err == nil:
		failed := false
		for node, e := range results {
			if e != "" {
				failed = true
				svc.store.RecordNodeEvent(r.Context(), node, "image_load_failed", fmt.Sprintf("could not load image %s: %s", r.URL.Query().Get("image"), e))
			} else {
				svc.store.RecordNodeEvent(r.Context(), node, "image_loaded", "loaded image "+r.URL.Query().Get("image"))
			}
		}
		status := http.StatusOK
		if failed {
			status = http.StatusBadGateway
		}
		writeJSON(w, status, map[string]any{"image": r.URL.Query().Get("image"), "results": results})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}

// handleImageLoad serves a pending archive to the agent that must load it.
func (svc *Service) handleImageLoad(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if authenticated := authenticatedNode(r); authenticated != "" && authenticated != node {
		writeError(w, http.StatusForbidden, fmt.Errorf("credential is not authorized for node %q", node))
		return
	}
	p := svc.imagePushes.get(r.PathValue("id"))
	if p == nil {
		writeError(w, http.StatusNotFound, errors.New("no such image push"))
		return
	}
	p.mu.Lock()
	allowed := p.waiting[node]
	p.mu.Unlock()
	if !allowed {
		writeError(w, http.StatusForbidden, fmt.Errorf("image push is not waiting on node %q", node))
		return
	}
	f, err := os.Open(p.path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/x-tar")
	if st, err := f.Stat(); err == nil {
		http.ServeContent(w, r, "", st.ModTime(), f)
		return
	}
	_, _ = io.Copy(w, f)
}

// handleImageLoaded records an agent's result for one push.
func (svc *Service) handleImageLoaded(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if authenticated := authenticatedNode(r); authenticated != "" && authenticated != node {
		writeError(w, http.StatusForbidden, fmt.Errorf("credential is not authorized for node %q", node))
		return
	}
	var res struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&res); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p := svc.imagePushes.get(res.ID)
	if p == nil || !p.report(node, res.Error) {
		writeError(w, http.StatusNotFound, errors.New("no such image push waiting on this node"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
