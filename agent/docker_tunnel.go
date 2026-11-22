package agent

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/abhishek/hardware-aware-container-orchestrator/tunnel"
)

// dockerExec is a `docker exec` as an execSession.
type dockerExec struct {
	d      *Docker
	id     string
	hr     types.HijackedResponse
	out    io.Reader
	closeW func()
}

// StartExec runs spec.Cmd inside the workload's container, attached.
func (d *Docker) StartExec(ctx context.Context, spec tunnel.Spec) (execSession, error) {
	name := ContainerName(spec.Key)
	created, err := d.cli.ContainerExecCreate(ctx, name, types.ExecConfig{
		AttachStdin: true, AttachStdout: true, AttachStderr: true,
		Tty: spec.TTY, Cmd: spec.Cmd, Env: []string{"TERM=xterm-256color"},
	})
	if err != nil {
		if isNoSuchContainer(err) {
			return nil, fmt.Errorf("%s has no running container on this node", spec.Key)
		}
		return nil, fmt.Errorf("create exec: %w", err)
	}
	hr, err := d.cli.ContainerExecAttach(ctx, created.ID, types.ExecStartCheck{Tty: spec.TTY})
	if err != nil {
		return nil, fmt.Errorf("start exec: %w", err)
	}
	s := &dockerExec{d: d, id: created.ID, hr: hr}
	if spec.TTY {
		s.out = hr.Reader
		if spec.Cols > 0 && spec.Rows > 0 {
			s.Resize(spec.Cols, spec.Rows)
		}
	} else {
		// Without a TTY Docker multiplexes stdout and stderr; merge them.
		pr, pw := io.Pipe()
		go func() {
			_, err := stdcopy.StdCopy(pw, pw, hr.Reader)
			pw.CloseWithError(err)
		}()
		s.out = pr
	}
	return s, nil
}

func (e *dockerExec) Read(p []byte) (int, error)  { return e.out.Read(p) }
func (e *dockerExec) Write(p []byte) (int, error) { return e.hr.Conn.Write(p) }
func (e *dockerExec) CloseWrite() error           { return e.hr.CloseWrite() }
func (e *dockerExec) Close()                      { e.hr.Close() }

func (e *dockerExec) Resize(cols, rows int) {
	_ = e.d.cli.ContainerExecResize(context.Background(), e.id, types.ResizeOptions{Width: uint(cols), Height: uint(rows)})
}

func (e *dockerExec) ExitCode(ctx context.Context) (int, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := e.d.cli.ContainerExecInspect(ctx, e.id)
		if err != nil {
			return 0, err
		}
		if !info.Running {
			return info.ExitCode, nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("exec still running")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// DialContainerPort opens a TCP connection to port inside the workload's
// container: straight to its address on the project network when this
// process can route there (Linux), otherwise through a throwaway helper
// container on that network (Docker Desktop, where containers sit in a VM).
func (d *Docker) DialContainerPort(ctx context.Context, key string, port int) (io.ReadWriteCloser, error) {
	info, err := d.cli.ContainerInspect(ctx, ContainerName(key))
	if err != nil {
		if isNoSuchContainer(err) {
			return nil, fmt.Errorf("%s has no container on this node", key)
		}
		return nil, err
	}
	if info.State == nil || !info.State.Running {
		return nil, fmt.Errorf("%s is not running", key)
	}
	netName, ip := "", ""
	if info.NetworkSettings != nil {
		for name, ep := range info.NetworkSettings.Networks {
			if ep != nil && ep.IPAddress != "" && (ip == "" || name == projectNetworkName(projectOf(key))) {
				netName, ip = name, ep.IPAddress
			}
		}
	}
	if ip == "" {
		return nil, fmt.Errorf("%s has no network address", key)
	}
	if conn, err := directDial(ctx, ip, port); err == nil {
		return conn, nil
	}
	return d.dialThroughHelper(ctx, netName, ip, port)
}

func projectOf(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i]
		}
	}
	return key
}

type helperConn struct {
	d  *Docker
	id string
	hr types.HijackedResponse
	r  io.Reader
}

func (h *helperConn) Read(p []byte) (int, error)  { return h.r.Read(p) }
func (h *helperConn) Write(p []byte) (int, error) { return h.hr.Conn.Write(p) }
func (h *helperConn) Close() error {
	h.hr.Close()
	_ = h.d.cli.ContainerRemove(context.Background(), h.id, types.ContainerRemoveOptions{Force: true})
	return nil
}

func (d *Docker) dialThroughHelper(ctx context.Context, netName, ip string, port int) (io.ReadWriteCloser, error) {
	image := forwarderImage()
	if !d.HasImage(ctx, image) {
		if err := d.PullImage(ctx, image, ""); err != nil {
			return nil, fmt.Errorf("the helper image %s is missing and could not be pulled: %w", image, err)
		}
	}
	created, err := d.cli.ContainerCreate(ctx,
		&container.Config{
			Image: image, Cmd: []string{"nc", ip, strconv.Itoa(port)},
			AttachStdin: true, AttachStdout: true, OpenStdin: true, StdinOnce: true,
			Labels: map[string]string{"haco.tunnel-helper": "1"},
		},
		&container.HostConfig{NetworkMode: container.NetworkMode(netName), AutoRemove: true},
		&network.NetworkingConfig{}, nil, "")
	if err != nil {
		return nil, fmt.Errorf("create helper: %w", err)
	}
	hr, err := d.cli.ContainerAttach(ctx, created.ID, types.ContainerAttachOptions{Stream: true, Stdin: true, Stdout: true})
	if err != nil {
		_ = d.cli.ContainerRemove(ctx, created.ID, types.ContainerRemoveOptions{Force: true})
		return nil, fmt.Errorf("attach helper: %w", err)
	}
	if err := d.cli.ContainerStart(ctx, created.ID, types.ContainerStartOptions{}); err != nil {
		hr.Close()
		_ = d.cli.ContainerRemove(ctx, created.ID, types.ContainerRemoveOptions{Force: true})
		return nil, fmt.Errorf("start helper: %w", err)
	}
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, io.Discard, hr.Reader)
		pw.CloseWithError(err)
	}()
	return &helperConn{d: d, id: created.ID, hr: hr, r: pr}, nil
}
