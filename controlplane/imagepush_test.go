package controlplane

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

// agentLoads plays a node's agent: take the instruction, read the archive
// the way the endpoint would, then report.
func agentLoads(t *testing.T, svc *Service, node, wantBody, errText string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if loads := svc.imagePushes.take(node); len(loads) > 0 {
			p := svc.imagePushes.get(loads[0].ID)
			if p == nil || loads[0].Image != "myapp:dev" {
				t.Errorf("load = %+v", loads[0])
				return
			}
			// The same instruction is not handed out twice.
			if again := svc.imagePushes.take(node); len(again) != 0 {
				t.Errorf("handed out twice: %+v", again)
			}
			if got := readFile(t, p.path); got != wantBody {
				t.Errorf("archive = %q, want %q", got, wantBody)
			}
			if !p.report(node, errText) {
				t.Error("report refused")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("node %s never got a load instruction", node)
}

func TestPushImageWaitsForEveryNodeAndReportsEach(t *testing.T) {
	svc := liveLogTestService(t) // has node-b
	go agentLoads(t, svc, "node-b", "TARDATA", "")
	res, err := svc.PushImage(context.Background(), "myapp:dev", []string{"node-b"}, strings.NewReader("TARDATA"))
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := res["node-b"]; !ok || e != "" {
		t.Fatalf("results = %v", res)
	}
}

func TestPushImageReportsANodeFailure(t *testing.T) {
	svc := liveLogTestService(t)
	go agentLoads(t, svc, "node-b", "X", "no space left on device")
	res, err := svc.PushImage(context.Background(), "myapp:dev", []string{"node-b"}, strings.NewReader("X"))
	if err != nil || res["node-b"] != "no space left on device" {
		t.Fatalf("res=%v err=%v", res, err)
	}
}

func TestPushImageRejectsUnknownNodeAndEmptyInput(t *testing.T) {
	svc := liveLogTestService(t)
	if _, err := svc.PushImage(context.Background(), "myapp:dev", []string{"ghost"}, strings.NewReader("x")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
	if _, err := svc.PushImage(context.Background(), "", []string{"node-b"}, strings.NewReader("x")); err == nil {
		t.Fatal("empty image name should fail")
	}
	if _, err := svc.PushImage(context.Background(), "myapp:dev", nil, strings.NewReader("x")); err == nil {
		t.Fatal("no nodes should fail")
	}
}

func TestPushImageStopsWaitingWhenTheCallerGoesAway(t *testing.T) {
	svc := liveLogTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := svc.PushImage(ctx, "myapp:dev", []string{"node-b"}, strings.NewReader("x"))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PushImage kept waiting after cancel")
	}
	if loads := svc.imagePushes.take("node-b"); len(loads) != 0 {
		t.Fatalf("a cancelled push left an instruction behind: %+v", loads)
	}
}

func TestOnlyTheTargetNodeIsAskedToLoad(t *testing.T) {
	svc := liveLogTestService(t)
	go func() {
		time.Sleep(100 * time.Millisecond)
		if loads := svc.imagePushes.take("node-other"); len(loads) != 0 {
			t.Errorf("node-other got %+v", loads)
		}
		agentLoads(t, svc, "node-b", "Y", "")
	}()
	if _, err := svc.PushImage(context.Background(), "myapp:dev", []string{"node-b"}, strings.NewReader("Y")); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Error(err)
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return string(b)
}
