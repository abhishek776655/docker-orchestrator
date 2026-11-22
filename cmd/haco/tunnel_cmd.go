package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/tunnel"
)

// exitCodeError makes haco exit with a command's own exit code, quietly.
type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return "exit status " + strconv.Itoa(e.code) }

// dialTunnel opens an interactive session through the control plane: an
// HTTP upgrade that becomes a raw stream to the agent on the right node.
func dialTunnel(baseURL string, q neturl.Values) (io.ReadWriteCloser, error) {
	u, err := neturl.Parse(baseURL)
	if err != nil || u.Scheme != "http" {
		return nil, fmt.Errorf("interactive sessions need an http control-plane URL, got %q", baseURL)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		return nil, err
	}
	req := "POST /v1/admin/tunnel?" + q.Encode() + " HTTP/1.1\r\nHost: " + u.Host +
		"\r\nConnection: Upgrade\r\nUpgrade: " + tunnel.Upgrade + "\r\nContent-Length: 0\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer conn.Close()
		body, _ := io.ReadAll(resp.Body)
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("%s", e.Error)
		}
		return nil, fmt.Errorf("control plane returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return &struct {
		io.Reader
		io.Writer
		io.Closer
	}{br, conn, conn}, nil
}

// ---- haco exec ----------------------------------------------------------

const execUsage = "usage: haco exec <project> <service> [-t | -T] [--] [command ...]   (default command: sh)"

// execCmd runs a command (or a shell) inside a service's container, on
// whichever node runs it.
func execCmd(baseURL string, args []string) error {
	var positional []string
	tty := isTerminal(os.Stdin) && isTerminal(os.Stdout)
	i := 0
parse:
	for ; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			i++
			break parse
		case a == "-t":
			tty = true
		case a == "-T":
			tty = false
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s\n%s", a, execUsage)
		case len(positional) < 2:
			positional = append(positional, a)
		default:
			break parse // the command starts here
		}
	}
	if len(positional) < 2 {
		return fmt.Errorf("%s", execUsage)
	}
	cmd := args[i:]

	q := neturl.Values{"kind": {tunnel.KindExec}, "project": {positional[0]}, "service": {positional[1]}, "cmd": cmd}
	if tty {
		q.Set("tty", "1")
		if cols, rows, ok := terminalSize(); ok {
			q.Set("cols", strconv.Itoa(cols))
			q.Set("rows", strconv.Itoa(rows))
		}
	}
	rwc, err := dialTunnel(baseURL, q)
	if err != nil {
		return err
	}
	defer rwc.Close()

	restore := func() {}
	if tty {
		restore = makeRaw()
	}
	defer restore() // also runs before the exit below

	var wmu sync.Mutex
	send := func(typ byte, p []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return tunnel.WriteFrame(rwc, typ, p)
	}
	go func() { // stdin to the command
		buf := make([]byte, 16*1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if send(tunnel.FrameData, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				_ = send(tunnel.FrameEOF, nil)
				return
			}
		}
	}()
	if tty {
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				if cols, rows, ok := terminalSize(); ok {
					_ = send(tunnel.FrameResize, tunnel.ResizePayload(cols, rows))
				}
			}
		}()
	}

	for {
		typ, p, err := tunnel.ReadFrame(rwc)
		if err != nil {
			restore()
			return fmt.Errorf("the session ended unexpectedly (the node or the control plane went away)")
		}
		switch typ {
		case tunnel.FrameData:
			_, _ = os.Stdout.Write(p)
		case tunnel.FrameExit:
			code, _ := strconv.Atoi(string(p))
			restore()
			if code != 0 {
				return exitCodeError{code}
			}
			return nil
		}
	}
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// makeRaw puts the terminal in raw mode with stty (no extra dependency) and
// returns the function that puts it back.
func makeRaw() func() {
	get := exec.Command("stty", "-g")
	get.Stdin = os.Stdin
	saved, err := get.Output()
	if err != nil {
		return func() {}
	}
	raw := exec.Command("stty", "raw", "-echo")
	raw.Stdin = os.Stdin
	if raw.Run() != nil {
		return func() {}
	}
	var once sync.Once
	restore := func() {
		once.Do(func() {
			r := exec.Command("stty", strings.TrimSpace(string(saved)))
			r.Stdin = os.Stdin
			_ = r.Run()
		})
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		if _, ok := <-sig; ok {
			restore()
			os.Exit(1)
		}
	}()
	return restore
}

func terminalSize() (cols, rows int, ok bool) {
	c := exec.Command("stty", "size")
	c.Stdin = os.Stdin
	out, err := c.Output()
	if err != nil {
		return 0, 0, false
	}
	return parseSttySize(string(out))
}

func parseSttySize(s string) (cols, rows int, ok bool) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, 0, false
	}
	r, err1 := strconv.Atoi(f[0])
	c, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || r <= 0 || c <= 0 {
		return 0, 0, false
	}
	return c, r, true
}

// ---- haco port-forward --------------------------------------------------

const portForwardUsage = "usage: haco port-forward <project> <service> [local:]remote [[local:]remote ...]\n       haco port-forward <project> --all      (every service's ports, on the same local numbers)"

// portMapping is one local listener and the container port it reaches.
type portMapping struct {
	Service string
	Local   int
	Remote  int
}

// parsePortSpec reads "5432" or "15432:5432" (local:remote).
func parsePortSpec(s string) (local, remote int, err error) {
	l, r, hasLocal := strings.Cut(s, ":")
	if !hasLocal {
		l, r = s, s
	}
	local, err1 := strconv.Atoi(l)
	remote, err2 := strconv.Atoi(r)
	if err1 != nil || err2 != nil || local < 0 || local > 65535 || remote < 1 || remote > 65535 {
		return 0, 0, fmt.Errorf("%q is not a port or local:remote ports", s)
	}
	return local, remote, nil
}

// containerPortsOf lists every port a service exposes, published or not.
func containerPortsOf(in model.ServiceInstance) []int {
	seen := map[int]bool{}
	var out []int
	for _, p := range in.Ports {
		if p.ContainerPort > 0 && !seen[p.ContainerPort] {
			seen[p.ContainerPort] = true
			out = append(out, p.ContainerPort)
		}
	}
	if len(out) == 0 && in.ContainerPort > 0 {
		out = append(out, in.ContainerPort)
	}
	sort.Ints(out)
	return out
}

// portForward listens on local ports and relays each connection to the
// service's container, wherever it runs, until interrupted.
func portForward(baseURL string, args []string) error {
	var positional []string
	all := false
	for _, a := range args {
		switch {
		case a == "--all":
			all = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s\n%s", a, portForwardUsage)
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) == 0 {
		return fmt.Errorf("%s", portForwardUsage)
	}
	project := positional[0]
	var maps []portMapping
	if all {
		if len(positional) != 1 {
			return fmt.Errorf("%s", portForwardUsage)
		}
		var instances []model.ServiceInstance
		if err := fetchJSON(baseURL, "/v1/services/"+neturl.PathEscape(project), &instances); err != nil {
			return err
		}
		for _, in := range instances {
			for _, p := range containerPortsOf(in) {
				maps = append(maps, portMapping{Service: in.Service, Local: p, Remote: p})
			}
		}
		if len(maps) == 0 {
			return fmt.Errorf("project %q has no running service with a port to forward", project)
		}
	} else {
		if len(positional) < 3 {
			return fmt.Errorf("%s", portForwardUsage)
		}
		for _, spec := range positional[2:] {
			local, remote, err := parsePortSpec(spec)
			if err != nil {
				return err
			}
			if local == 0 {
				local = remote
			}
			maps = append(maps, portMapping{Service: positional[1], Local: local, Remote: remote})
		}
	}

	var listeners []net.Listener
	for i := range maps {
		m := &maps[i]
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(m.Local)))
		if err != nil && all {
			l, err = net.Listen("tcp", "127.0.0.1:0") // that number is taken: any free one
		}
		if err != nil {
			for _, open := range listeners {
				open.Close()
			}
			return fmt.Errorf("listen on 127.0.0.1:%d: %w", m.Local, err)
		}
		m.Local = l.Addr().(*net.TCPAddr).Port
		listeners = append(listeners, l)
		fmt.Printf("Forwarding 127.0.0.1:%d -> %s/%s:%d\n", m.Local, project, m.Service, m.Remote)
		go acceptForwards(baseURL, project, *m, l)
	}
	fmt.Println("Press Ctrl-C to stop.")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	for _, l := range listeners {
		l.Close()
	}
	return nil
}

func acceptForwards(baseURL, project string, m portMapping, l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			q := neturl.Values{"kind": {tunnel.KindForward}, "project": {project}, "service": {m.Service}, "port": {strconv.Itoa(m.Remote)}}
			rwc, err := dialTunnel(baseURL, q)
			if err != nil {
				fmt.Fprintf(os.Stderr, "forward %d -> %s:%d: %v\n", m.Local, m.Service, m.Remote, err)
				return
			}
			defer rwc.Close()
			relay(c, rwc)
		}()
	}
}

// relay copies both ways until one side ends, then closes both.
func relay(a, b io.ReadWriteCloser) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { a.Close(); b.Close() }) }
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); closeBoth(); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); closeBoth(); done <- struct{}{} }()
	<-done
	<-done
}

// ---- haco logs <project> (every service) --------------------------------

// prefixWriter writes complete lines to out, each led by prefix, holding a
// partial line back until it is finished. Safe for use from many goroutines
// when they share one mutex.
type prefixWriter struct {
	mu     *sync.Mutex
	out    io.Writer
	prefix string
	buf    bytes.Buffer
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			break
		}
		line := w.buf.Next(i + 1)
		w.mu.Lock()
		_, _ = io.WriteString(w.out, w.prefix)
		_, _ = w.out.Write(line)
		w.mu.Unlock()
	}
	return len(p), nil
}

// flush writes a trailing partial line.
func (w *prefixWriter) flush() {
	if w.buf.Len() == 0 {
		return
	}
	w.buf.WriteByte('\n')
	_, _ = w.Write(nil)
}

var logColors = []string{"36", "33", "35", "32", "34", "31", "96", "93", "95", "92"}

// projectLogs shows every service's logs with a name prefix, like `docker
// compose logs`; with -f they stream together until interrupted.
func projectLogs(baseURL string, project string, flags []string) error {
	var all []workloadRecordView
	if err := fetchJSON(baseURL, "/v1/workloads?project="+neturl.QueryEscape(project), &all); err != nil {
		return err
	}
	var services []string
	seen := map[string]bool{}
	for _, w := range all {
		if w.DesiredState == "stopped" || w.NodeID == "" || seen[w.Service] {
			continue
		}
		seen[w.Service] = true
		services = append(services, w.Service)
	}
	sort.Strings(services)
	if len(services) == 0 {
		return fmt.Errorf("project %q has no services with logs", project)
	}
	width := 0
	for _, s := range services {
		width = max(width, len(s))
	}
	color := isTerminal(os.Stdout)
	var mu sync.Mutex
	follow := false
	for _, f := range flags {
		if f == "-f" || f == "--follow" {
			follow = true
		}
	}

	run := func(i int, svc string) error {
		prefix := fmt.Sprintf("%-*s | ", width, svc)
		if color {
			prefix = "\033[" + logColors[i%len(logColors)] + "m" + prefix + "\033[0m"
		}
		w := &prefixWriter{mu: &mu, out: os.Stdout, prefix: prefix}
		defer w.flush()
		return fetchLogs(http.DefaultClient, baseURL, append([]string{project, svc}, flags...), w)
	}
	if !follow {
		for i, svc := range services {
			if err := run(i, svc); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", svc, err)
			}
		}
		return nil
	}
	var wg sync.WaitGroup
	for i, svc := range services {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := run(i, svc); err != nil {
				mu.Lock()
				fmt.Fprintf(os.Stderr, "%s: %v\n", svc, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return nil
}
