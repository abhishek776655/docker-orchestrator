package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/tunnel"
)

// tunnelLoop waits for the control plane to name sessions (exec, forward)
// that need this node and serves each in its own goroutine. The request is a
// long poll the control plane answers the moment a session is queued.
func (a *Agent) tunnelLoop(ctx context.Context) {
	for ctx.Err() == nil {
		specs, err := a.pollTunnelRequests(ctx)
		if err != nil {
			// An older control plane has no such endpoint; either way, don't
			// hammer it.
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, spec := range specs {
			go a.serveTunnel(ctx, spec)
		}
	}
}

func (a *Agent) pollTunnelRequests(ctx context.Context) ([]tunnel.Spec, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.ControlPlaneURL+"/v1/agent/"+a.cfg.NodeID+"/tunnel-requests", nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.doStream(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("control plane returned %s", resp.Status)
	}
	var specs []tunnel.Spec
	if err := json.NewDecoder(resp.Body).Decode(&specs); err != nil {
		return nil, err
	}
	return specs, nil
}

// attachTunnel joins a session: an HTTP upgrade to the control plane, which
// then carries the session's bytes.
func (a *Agent) attachTunnel(ctx context.Context, id string) (io.ReadWriteCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.ControlPlaneURL+"/v1/agent/"+a.cfg.NodeID+"/tunnel/"+id, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", tunnel.Upgrade)
	resp, err := a.doStream(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		msg := errorBody(resp)
		resp.Body.Close()
		return nil, fmt.Errorf("control plane returned %s: %s", resp.Status, msg)
	}
	rwc, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		return nil, errors.New("the connection did not switch protocols")
	}
	return rwc, nil
}

func (a *Agent) serveTunnel(ctx context.Context, spec tunnel.Spec) {
	rwc, err := a.attachTunnel(ctx, spec.ID)
	if err != nil {
		log.Printf("session %s: %v", spec.ID, err)
		return
	}
	defer rwc.Close()
	switch spec.Kind {
	case tunnel.KindExec:
		sess, err := a.docker.StartExec(ctx, spec)
		if err != nil {
			_ = tunnel.WriteFrame(rwc, tunnel.FrameData, []byte("haco: "+err.Error()+"\r\n"))
			_ = tunnel.WriteFrame(rwc, tunnel.FrameExit, []byte("126"))
			return
		}
		runExec(ctx, rwc, sess)
	case tunnel.KindForward:
		target, err := a.docker.DialContainerPort(ctx, spec.Key, spec.Port)
		if err != nil {
			log.Printf("session %s: forward %s:%d: %v", spec.ID, spec.Key, spec.Port, err)
			return // closing tells the CLI the connection failed
		}
		defer target.Close()
		pipe(rwc, target)
	}
}

// pipe copies both ways until either side ends, then closes both.
func pipe(a, b io.ReadWriteCloser) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { a.Close(); b.Close() }) }
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); closeBoth(); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); closeBoth(); done <- struct{}{} }()
	<-done
	<-done
}

// execSession is one running `docker exec`, as runExec sees it.
type execSession interface {
	io.Reader // the command's output, stdout and stderr merged
	io.Writer // its stdin
	CloseWrite() error
	Resize(cols, rows int)
	ExitCode(ctx context.Context) (int, error)
	Close()
}

// runExec carries an exec session over a framed stream: input frames to the
// command, its output back as data frames, and the exit code last.
func runExec(ctx context.Context, rwc io.ReadWriteCloser, sess execSession) {
	defer sess.Close()
	var wmu sync.Mutex
	send := func(typ byte, p []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return tunnel.WriteFrame(rwc, typ, p)
	}

	go func() { // CLI to command
		for {
			typ, p, err := tunnel.ReadFrame(rwc)
			if err != nil {
				_ = sess.CloseWrite()
				return
			}
			switch typ {
			case tunnel.FrameData:
				if _, err := sess.Write(p); err != nil {
					return
				}
			case tunnel.FrameEOF:
				_ = sess.CloseWrite()
			case tunnel.FrameResize:
				if cols, rows, ok := tunnel.ParseResize(p); ok {
					sess.Resize(cols, rows)
				}
			}
		}
	}()

	buf := make([]byte, 32*1024)
	for {
		n, err := sess.Read(buf)
		if n > 0 {
			if sendErr := send(tunnel.FrameData, buf[:n]); sendErr != nil {
				return
			}
		}
		if err != nil {
			break
		}
	}
	code, err := sess.ExitCode(ctx)
	if err != nil {
		code = 255
	}
	_ = send(tunnel.FrameExit, []byte(strconv.Itoa(code)))
}

// directDial tries to reach ip:port from this process (works on Linux, where
// container addresses are routable; not through Docker Desktop's VM).
func directDial(ctx context.Context, ip string, port int) (net.Conn, error) {
	d := net.Dialer{Timeout: 1500 * time.Millisecond}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
}

func isNoSuchContainer(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such container")
}
