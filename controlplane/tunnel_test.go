package controlplane

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/tunnel"
)

func tunnelServer(t *testing.T) (*Service, *httptest.Server) {
	t.Helper()
	svc := liveLogTestService(t) // demo/api/latest is ready on node-b
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/admin/tunnel", svc.handleTunnelOpen)
	mux.HandleFunc("GET /v1/agent/{node}/tunnel-requests", svc.handleTunnelRequests)
	mux.HandleFunc("POST /v1/agent/{node}/tunnel/{id}", svc.handleTunnelAttach)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return svc, srv
}

// upgrade sends an Upgrade request and returns the raw stream on a 101, or
// the response when the server refused.
func upgrade(t *testing.T, srv *httptest.Server, method, path string) (io.ReadWriteCloser, *http.Response) {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	reqText := "" + method + " " + path + " HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: " + tunnel.Upgrade + "\r\nContent-Length: 0\r\n\r\n"
	if _, err := conn.Write([]byte(reqText)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, resp
	}
	return &struct {
		io.Reader
		io.Writer
		io.Closer
	}{br, conn, conn}, resp
}

func TestTunnelSplicesTheCLIToTheAgentBothWays(t *testing.T) {
	svc, srv := tunnelServer(t)

	// The agent: learns of the session from its request poll, then joins it.
	agentGot := make(chan string, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/v1/agent/node-b/tunnel-requests?wait=5")
		if err != nil {
			t.Error(err)
			return
		}
		var specs []tunnel.Spec
		_ = json.NewDecoder(resp.Body).Decode(&specs)
		resp.Body.Close()
		if len(specs) != 1 || specs[0].Kind != tunnel.KindForward || specs[0].Port != 5432 || specs[0].Key != "demo/api/latest" {
			t.Errorf("specs = %+v", specs)
			return
		}
		conn, r := upgrade(t, srv, "POST", "/v1/agent/node-b/tunnel/"+specs[0].ID)
		if conn == nil {
			t.Errorf("agent attach refused: %s", r.Status)
			return
		}
		defer conn.Close()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Error(err)
			return
		}
		agentGot <- string(buf)
		conn.Write([]byte("pong!"))
	}()

	q := url.Values{"kind": {"forward"}, "project": {"demo"}, "service": {"api"}, "port": {"5432"}}
	cli, resp := upgrade(t, srv, "POST", "/v1/admin/tunnel?"+q.Encode())
	if cli == nil {
		t.Fatalf("CLI side refused: %s", resp.Status)
	}
	defer cli.Close()
	cli.Write([]byte("ping!"))
	select {
	case got := <-agentGot:
		if got != "ping!" {
			t.Fatalf("agent received %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent never received the CLI's bytes")
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(cli, buf); err != nil || string(buf) != "pong!" {
		t.Fatalf("CLI received %q, %v", buf, err)
	}
	// Both ends are released once the agent hangs up.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		svc.tunnels.mu.Lock()
		n := len(svc.tunnels.sessions)
		svc.tunnels.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the finished session was not forgotten")
}

func TestTunnelRefusesWhatCannotRun(t *testing.T) {
	_, srv := tunnelServer(t)
	cases := []struct {
		name, query string
		want        int
	}{
		{"no such service", "kind=forward&project=demo&service=ghost&port=80", http.StatusNotFound},
		{"bad kind", "kind=teleport&project=demo&service=api", http.StatusBadRequest},
		{"forward without a port", "kind=forward&project=demo&service=api", http.StatusBadRequest},
		{"missing service", "kind=exec&project=demo", http.StatusBadRequest},
	}
	for _, c := range cases {
		conn, resp := upgrade(t, srv, "POST", "/v1/admin/tunnel?"+c.query)
		if conn != nil {
			conn.Close()
			t.Fatalf("%s: got a session", c.name)
		}
		if resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
	// A plain request with no Upgrade is told what to send.
	resp, err := http.Post(srv.URL+"/v1/admin/tunnel?kind=exec&project=demo&service=api", "", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("no-upgrade status = %d", resp.StatusCode)
	}
}

func TestOnlyTheTargetNodeMayJoinASession(t *testing.T) {
	svc, srv := tunnelServer(t)
	sess, err := svc.OpenTunnel(t.Context(), tunnel.Spec{Kind: tunnel.KindExec, Key: "demo/api/latest"})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.tunnels.drop(sess.spec.ID)
	conn, resp := upgrade(t, srv, "POST", "/v1/agent/node-other/tunnel/"+sess.spec.ID)
	if conn != nil {
		conn.Close()
		t.Fatal("another node joined the session")
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestRequestPollReturnsAtOnceWhenASessionIsQueued(t *testing.T) {
	svc, _ := tunnelServer(t)
	got := make(chan []tunnel.Spec, 1)
	go func() { got <- svc.tunnels.pollSpecs(t.Context(), "node-b", 5*time.Second) }()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	sess, _ := svc.OpenTunnel(t.Context(), tunnel.Spec{Kind: tunnel.KindExec, Key: "demo/api/latest"})
	defer svc.tunnels.drop(sess.spec.ID)
	select {
	case specs := <-got:
		if len(specs) != 1 || time.Since(start) > time.Second {
			t.Fatalf("specs=%v after %v", specs, time.Since(start))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the held poll did not wake for a new session")
	}
	if specs := svc.tunnels.pollSpecs(t.Context(), "node-b", 50*time.Millisecond); len(specs) != 0 {
		t.Fatalf("a fetched session was handed out twice: %v", specs)
	}
}
