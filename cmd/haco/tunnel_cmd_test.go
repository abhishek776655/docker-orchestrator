package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/tunnel"
)

func TestParsePortSpec(t *testing.T) {
	for in, want := range map[string][2]int{"5432": {5432, 5432}, "15432:5432": {15432, 5432}, "0:80": {0, 80}} {
		l, r, err := parsePortSpec(in)
		if err != nil || l != want[0] || r != want[1] {
			t.Errorf("%q -> %d,%d,%v", in, l, r, err)
		}
	}
	for _, bad := range []string{"", "abc", "80:", ":80", "70000", "1:0", "-5"} {
		if _, _, err := parsePortSpec(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestParseSttySize(t *testing.T) {
	if c, r, ok := parseSttySize("40 120\n"); !ok || c != 120 || r != 40 {
		t.Fatalf("%d %d %v", c, r, ok)
	}
	for _, bad := range []string{"", "x y", "0 0", "40"} {
		if _, _, ok := parseSttySize(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestContainerPortsOfCoversPublishedAndInternalPorts(t *testing.T) {
	published := model.ServiceInstance{Ports: []model.PortBinding{{ContainerPort: 5000, HostPort: 5600}, {ContainerPort: 9000, HostPort: 9000}, {ContainerPort: 5000, HostPort: 5601}}}
	if got := containerPortsOf(published); !reflect.DeepEqual(got, []int{5000, 9000}) {
		t.Fatalf("published: %v", got)
	}
	internal := model.ServiceInstance{ContainerPort: 5432}
	if got := containerPortsOf(internal); !reflect.DeepEqual(got, []int{5432}) {
		t.Fatalf("internal: %v", got)
	}
	if got := containerPortsOf(model.ServiceInstance{}); len(got) != 0 {
		t.Fatalf("none: %v", got)
	}
}

func TestPrefixWriterLeadsEveryLineAndHoldsPartialOnes(t *testing.T) {
	var mu sync.Mutex
	var out bytes.Buffer
	w := &prefixWriter{mu: &mu, out: &out, prefix: "api | "}
	w.Write([]byte("one\ntw"))
	w.Write([]byte("o\nthree"))
	if out.String() != "api | one\napi | two\n" {
		t.Fatalf("before flush: %q", out.String())
	}
	w.flush()
	if out.String() != "api | one\napi | two\napi | three\n" {
		t.Fatalf("after flush: %q", out.String())
	}
}

func TestPrefixWritersFromManyServicesNeverSplitALine(t *testing.T) {
	var mu sync.Mutex
	var out bytes.Buffer
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := &prefixWriter{mu: &mu, out: &out, prefix: name + " | "}
			for i := 0; i < 200; i++ {
				w.Write([]byte("line " + strings.Repeat(name, 10) + "\n"))
			}
		}()
	}
	wg.Wait()
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		p := l[:1]
		if l != p+" | line "+strings.Repeat(p, 10) {
			t.Fatalf("interleaved line: %q", l)
		}
	}
}

func TestLogFlagTakesValue(t *testing.T) {
	if !logFlagTakesValue([]string{"proj", "--tail", "50"}, "50") || logFlagTakesValue([]string{"proj", "-f"}, "proj") {
		t.Fatal("logFlagTakesValue")
	}
}

// tunnelEcho is a control plane stand-in that accepts the upgrade and
// answers what it is sent with a fixed reply.
func tunnelEcho(t *testing.T, reply string, status int, body string, seen *neturl.Values) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.URL.Query()
		}
		if status != http.StatusSwitchingProtocols {
			w.WriteHeader(status)
			io.WriteString(w, body)
			return
		}
		conn, rw, _ := w.(http.Hijacker).Hijack()
		defer conn.Close()
		io.WriteString(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: "+tunnel.Upgrade+"\r\nConnection: Upgrade\r\n\r\n")
		rw.Flush()
		line, _ := bufio.NewReader(rw).ReadString('\n')
		io.WriteString(conn, reply+strings.ToUpper(line))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDialTunnelReturnsAStreamOnUpgrade(t *testing.T) {
	var seen neturl.Values
	srv := tunnelEcho(t, "ok:", http.StatusSwitchingProtocols, "", &seen)
	rwc, err := dialTunnel(srv.URL, neturl.Values{"kind": {"forward"}, "project": {"shop"}, "service": {"db"}, "port": {"5432"}})
	if err != nil {
		t.Fatal(err)
	}
	defer rwc.Close()
	io.WriteString(rwc, "ping\n")
	got, _ := io.ReadAll(rwc)
	if string(got) != "ok:PING\n" {
		t.Fatalf("got %q", got)
	}
	if seen.Get("kind") != "forward" || seen.Get("service") != "db" || seen.Get("port") != "5432" {
		t.Fatalf("server saw %v", seen)
	}
}

func TestDialTunnelSurfacesTheControlPlanesReason(t *testing.T) {
	srv := tunnelEcho(t, "", http.StatusConflict, `{"error":"workload is not running: shop/db/latest is stopped"}`, nil)
	_, err := dialTunnel(srv.URL, neturl.Values{})
	if err == nil || !strings.Contains(err.Error(), "shop/db/latest is stopped") {
		t.Fatalf("err = %v", err)
	}
	if _, err := dialTunnel("https://cp.example", nil); err == nil {
		t.Fatal("https control-plane URLs are not supported for sessions")
	}
}

func TestExitCodeErrorIsRecognizable(t *testing.T) {
	var err error = exitCodeError{3}
	var ee exitCodeError
	if !errors.As(err, &ee) || ee.code != 3 {
		t.Fatal("errors.As")
	}
}
