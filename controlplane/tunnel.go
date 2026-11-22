package controlplane

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
	"github.com/abhishek/hardware-aware-container-orchestrator/tunnel"
)

// How long a session waits for its node's agent to pick it up, and how long
// the agent's request poll is held open.
const (
	tunnelAttachTimeout = 15 * time.Second
	tunnelPollHold      = 25 * time.Second
)

// tunnelSession is one interactive session waiting for, or joined to, an
// agent. The agent's connection arrives on attach.
type tunnelSession struct {
	spec   tunnel.Spec
	node   string
	attach chan io.ReadWriteCloser
}

type tunnelState struct {
	mu       sync.Mutex
	sessions map[string]*tunnelSession
	queue    map[string][]tunnel.Spec // node -> specs the agent has not fetched yet
	wake     map[string]chan struct{} // node -> poke for a held request poll
}

func (s *tunnelState) wakeCh(node string) chan struct{} {
	if s.wake == nil {
		s.wake = map[string]chan struct{}{}
	}
	ch, ok := s.wake[node]
	if !ok {
		ch = make(chan struct{}, 1)
		s.wake[node] = ch
	}
	return ch
}

// enqueue registers a session and tells the node's agent about it.
func (s *tunnelState) enqueue(sess *tunnelSession) {
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = map[string]*tunnelSession{}
		s.queue = map[string][]tunnel.Spec{}
	}
	s.sessions[sess.spec.ID] = sess
	s.queue[sess.node] = append(s.queue[sess.node], sess.spec)
	ch := s.wakeCh(sess.node)
	s.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *tunnelState) take(node string) []tunnel.Spec {
	s.mu.Lock()
	defer s.mu.Unlock()
	specs := s.queue[node]
	delete(s.queue, node)
	return specs
}

func (s *tunnelState) drop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	delete(s.sessions, id)
	if sess == nil {
		return
	}
	kept := s.queue[sess.node][:0:0]
	for _, sp := range s.queue[sess.node] {
		if sp.ID != id {
			kept = append(kept, sp)
		}
	}
	if len(kept) == 0 {
		delete(s.queue, sess.node)
	} else {
		s.queue[sess.node] = kept
	}
}

func (s *tunnelState) get(id string) *tunnelSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// pollSpecs holds the request open until the node has a session to serve.
func (s *tunnelState) pollSpecs(ctx context.Context, node string, hold time.Duration) []tunnel.Spec {
	s.mu.Lock()
	ch := s.wakeCh(node)
	s.mu.Unlock()
	timer := time.NewTimer(hold)
	defer timer.Stop()
	for {
		if specs := s.take(node); len(specs) > 0 {
			return specs
		}
		select {
		case <-ch:
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

func wantsTunnelUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), tunnel.Upgrade) &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// hijack takes over the connection and answers 101, returning a stream that
// includes any bytes the HTTP reader had already buffered.
func hijack(w http.ResponseWriter) (io.ReadWriteCloser, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("this connection cannot be upgraded")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: "+tunnel.Upgrade+"\r\nConnection: Upgrade\r\n\r\n"); err != nil {
		conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		conn.Close()
		return nil, err
	}
	return &hijacked{Conn: conn, r: rw.Reader}, nil
}

type hijacked struct {
	net.Conn
	r *bufio.Reader
}

func (h *hijacked) Read(p []byte) (int, error) { return h.r.Read(p) }

// splice copies both ways until either side ends, then closes both.
func splice(a, b io.ReadWriteCloser) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { a.Close(); b.Close() }) }
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); closeBoth(); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); closeBoth(); done <- struct{}{} }()
	<-done
	<-done
}

// OpenTunnel creates a session for workload key on its node and waits for
// the node's agent to join it. The caller owns the returned stream.
func (svc *Service) OpenTunnel(ctx context.Context, spec tunnel.Spec) (*tunnelSession, error) {
	rec, err := svc.store.GetWorkload(ctx, spec.Key)
	if err != nil {
		return nil, err
	}
	if rec.NodeID == "" || (rec.Status != model.StatusRunning && rec.Status != model.StatusReady) {
		return nil, fmt.Errorf("%w: %s is %s, not running", ErrNotRunning, spec.Key, rec.Status)
	}
	var id [8]byte
	_, _ = rand.Read(id[:])
	spec.ID = hex.EncodeToString(id[:])
	sess := &tunnelSession{spec: spec, node: rec.NodeID, attach: make(chan io.ReadWriteCloser, 1)}
	svc.tunnels.enqueue(sess)
	return sess, nil
}

// ErrNotRunning is returned when a session targets a workload that is not up.
var ErrNotRunning = errors.New("workload is not running")

// handleTunnelOpen: POST /v1/admin/tunnel?kind=exec|forward&project=&service=
// [&revision=][&cmd=..&cmd=..][&tty=1&cols=&rows=][&port=] with an Upgrade.
func (svc *Service) handleTunnelOpen(w http.ResponseWriter, r *http.Request) {
	if !wantsTunnelUpgrade(r) {
		writeError(w, http.StatusUpgradeRequired, fmt.Errorf("send Connection: Upgrade and Upgrade: %s", tunnel.Upgrade))
		return
	}
	q := r.URL.Query()
	revision := q.Get("revision")
	if revision == "" {
		revision = "latest"
	}
	spec := tunnel.Spec{
		Kind: q.Get("kind"),
		Key:  strings.Join([]string{q.Get("project"), q.Get("service"), revision}, "/"),
		Cmd:  q["cmd"],
		TTY:  q.Get("tty") == "1",
	}
	spec.Cols, _ = strconv.Atoi(q.Get("cols"))
	spec.Rows, _ = strconv.Atoi(q.Get("rows"))
	spec.Port, _ = strconv.Atoi(q.Get("port"))
	switch spec.Kind {
	case tunnel.KindExec:
		if len(spec.Cmd) == 0 {
			spec.Cmd = []string{"sh"}
		}
	case tunnel.KindForward:
		if spec.Port < 1 || spec.Port > 65535 {
			writeError(w, http.StatusBadRequest, errors.New("port must be 1-65535"))
			return
		}
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown session kind %q", spec.Kind))
		return
	}
	if q.Get("project") == "" || q.Get("service") == "" {
		writeError(w, http.StatusBadRequest, errors.New("project and service are required"))
		return
	}

	sess, err := svc.OpenTunnel(r.Context(), spec)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
		return
	case errors.Is(err, ErrNotRunning):
		writeError(w, http.StatusConflict, err)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer svc.tunnels.drop(sess.spec.ID)

	var agentConn io.ReadWriteCloser
	select {
	case agentConn = <-sess.attach:
	case <-time.After(tunnelAttachTimeout):
		writeError(w, http.StatusGatewayTimeout, fmt.Errorf("node %s did not answer in %s (is its agent running and up to date?)", sess.node, tunnelAttachTimeout))
		return
	case <-r.Context().Done():
		return
	}
	adminConn, err := hijack(w)
	if err != nil {
		agentConn.Close()
		return
	}
	splice(adminConn, agentConn)
}

// handleTunnelRequests: GET /v1/agent/{node}/tunnel-requests, held open
// until a session needs the node or the hold time passes.
func (svc *Service) handleTunnelRequests(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if authenticated := authenticatedNode(r); authenticated != "" && authenticated != node {
		writeError(w, http.StatusForbidden, fmt.Errorf("credential is not authorized for node %q", node))
		return
	}
	hold := tunnelPollHold
	if v, err := strconv.Atoi(r.URL.Query().Get("wait")); err == nil && v >= 0 && time.Duration(v)*time.Second < hold {
		hold = time.Duration(v) * time.Second
	}
	specs := svc.tunnels.pollSpecs(r.Context(), node, hold)
	if specs == nil {
		specs = []tunnel.Spec{}
	}
	writeJSON(w, http.StatusOK, specs)
}

// handleTunnelAttach: POST /v1/agent/{node}/tunnel/{id} with an Upgrade; the
// agent joins the session it was told about.
func (svc *Service) handleTunnelAttach(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("node")
	if authenticated := authenticatedNode(r); authenticated != "" && authenticated != node {
		writeError(w, http.StatusForbidden, fmt.Errorf("credential is not authorized for node %q", node))
		return
	}
	if !wantsTunnelUpgrade(r) {
		writeError(w, http.StatusUpgradeRequired, fmt.Errorf("send Connection: Upgrade and Upgrade: %s", tunnel.Upgrade))
		return
	}
	sess := svc.tunnels.get(r.PathValue("id"))
	if sess == nil || sess.node != node {
		writeError(w, http.StatusNotFound, errors.New("no such session for this node"))
		return
	}
	conn, err := hijack(w)
	if err != nil {
		return
	}
	select {
	case sess.attach <- conn:
	default:
		conn.Close() // already joined
	}
}
