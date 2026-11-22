package agent

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/tunnel"
)

// fakeExec is a command that upper-cases its stdin, then exits with code.
type fakeExec struct {
	in      *io.PipeReader
	inW     *io.PipeWriter
	out     *io.PipeReader
	outW    *io.PipeWriter
	code    int
	mu      sync.Mutex
	resized [][2]int
	closed  bool
}

func newFakeExec(code int) *fakeExec {
	f := &fakeExec{code: code}
	f.in, f.inW = io.Pipe()
	f.out, f.outW = io.Pipe()
	go func() {
		data, _ := io.ReadAll(f.in)
		f.outW.Write(bytes.ToUpper(data))
		f.outW.Close()
	}()
	return f
}

func (f *fakeExec) Read(p []byte) (int, error)  { return f.out.Read(p) }
func (f *fakeExec) Write(p []byte) (int, error) { return f.inW.Write(p) }
func (f *fakeExec) CloseWrite() error           { return f.inW.Close() }
func (f *fakeExec) Resize(c, r int) {
	f.mu.Lock()
	f.resized = append(f.resized, [2]int{c, r})
	f.mu.Unlock()
}
func (f *fakeExec) ExitCode(context.Context) (int, error) { return f.code, nil }
func (f *fakeExec) Close()                                { f.mu.Lock(); f.closed = true; f.mu.Unlock() }

func TestExecFramesCarryInputOutputResizeAndTheExitCode(t *testing.T) {
	cli, srv := net.Pipe()
	sess := newFakeExec(7)
	done := make(chan struct{})
	go func() { runExec(context.Background(), srv, sess); srv.Close(); close(done) }()

	tunnel.WriteFrame(cli, tunnel.FrameResize, tunnel.ResizePayload(100, 30))
	tunnel.WriteFrame(cli, tunnel.FrameData, []byte("hello "))
	tunnel.WriteFrame(cli, tunnel.FrameData, []byte("world"))
	tunnel.WriteFrame(cli, tunnel.FrameEOF, nil)

	var out strings.Builder
	code := ""
	for {
		typ, p, err := tunnel.ReadFrame(cli)
		if err != nil {
			t.Fatalf("read: %v (out so far %q)", err, out.String())
		}
		if typ == tunnel.FrameData {
			out.Write(p)
		}
		if typ == tunnel.FrameExit {
			code = string(p)
			break
		}
	}
	if out.String() != "HELLO WORLD" || code != "7" {
		t.Fatalf("output %q exit %q", out.String(), code)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runExec did not finish after the command exited")
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.resized) != 1 || sess.resized[0] != [2]int{100, 30} || !sess.closed {
		t.Fatalf("resized=%v closed=%v", sess.resized, sess.closed)
	}
}

func TestExecEndsInputWhenTheCLIDisconnects(t *testing.T) {
	cli, srv := net.Pipe()
	sess := newFakeExec(0)
	done := make(chan struct{})
	go func() { runExec(context.Background(), srv, sess); close(done) }()
	tunnel.WriteFrame(cli, tunnel.FrameData, []byte("abc"))
	cli.Close() // Ctrl-C, closed terminal, lost connection
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a vanished CLI must close the command's stdin so it can finish")
	}
}

func TestPipeCopiesBothWaysAndClosesBothEnds(t *testing.T) {
	a1, a2 := net.Pipe() // CLI side
	b1, b2 := net.Pipe() // container side
	done := make(chan struct{})
	go func() { pipe(a2, b1); close(done) }()

	go a1.Write([]byte("select 1"))
	buf := make([]byte, 8)
	if _, err := io.ReadFull(b2, buf); err != nil || string(buf) != "select 1" {
		t.Fatalf("container got %q %v", buf, err)
	}
	go b2.Write([]byte("1 row"))
	buf = make([]byte, 5)
	if _, err := io.ReadFull(a1, buf); err != nil || string(buf) != "1 row" {
		t.Fatalf("client got %q %v", buf, err)
	}
	b2.Close() // the database hangs up
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pipe did not end when one side closed")
	}
	if _, err := a1.Read(make([]byte, 1)); err == nil {
		t.Fatal("the other side must be closed too")
	}
}

func TestProjectOfKey(t *testing.T) {
	if projectOf("shop/api/latest") != "shop" || projectOf("solo") != "solo" {
		t.Fatal("projectOf")
	}
}
