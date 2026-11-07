package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDispatchRunsDifferentWorkloadsInParallelButNeverOneTwice(t *testing.T) {
	a := &Agent{}
	release := make(chan struct{})
	var running, peak int32
	work := func() {
		n := atomic.AddInt32(&running, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		<-release
		atomic.AddInt32(&running, -1)
	}

	if !a.dispatch("p/build/latest", work) || !a.dispatch("p/db/latest", work) {
		t.Fatal("different workloads must both be dispatched")
	}
	// The next poll tick sees the same workloads again while they're busy.
	if a.dispatch("p/build/latest", work) {
		t.Fatal("a workload still being handled must not be started again")
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&peak) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := atomic.LoadInt32(&peak); got != 2 {
		t.Fatalf("peak concurrency = %d, want 2 (one slow workload must not block another)", got)
	}
	close(release)
	a.inflight.Wait()
	if !a.dispatch("p/build/latest", func() {}) {
		t.Fatal("a finished workload must be dispatchable again")
	}
	a.inflight.Wait()
}

func TestHeavySlotsCapConcurrentBuilds(t *testing.T) {
	a := &Agent{heavy: make(chan struct{}, 2)}
	var running, peak int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.withHeavySlot(context.Background(), func() error {
				n := atomic.AddInt32(&running, 1)
				for {
					p := atomic.LoadInt32(&peak)
					if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				atomic.AddInt32(&running, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&peak); got != 2 {
		t.Fatalf("peak concurrent builds = %d, want the limit of 2", got)
	}
}

func TestHeavySlotWaitGivesUpOnCancel(t *testing.T) {
	a := &Agent{heavy: make(chan struct{}, 1)}
	a.heavy <- struct{}{} // the only slot is taken
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	err := a.withHeavySlot(ctx, func() error { ran = true; return nil })
	if !errors.Is(err, context.Canceled) || ran {
		t.Fatalf("err = %v, ran = %v; want a cancelled wait that never runs the build", err, ran)
	}
}

func TestHeavySlotsDefaultAndOverride(t *testing.T) {
	t.Setenv("HACO_AGENT_PARALLEL_BUILDS", "")
	if got := cap(heavySlotsFromEnv()); got != defaultHeavySlots {
		t.Fatalf("default slots = %d", got)
	}
	t.Setenv("HACO_AGENT_PARALLEL_BUILDS", "1")
	if got := cap(heavySlotsFromEnv()); got != 1 {
		t.Fatalf("override slots = %d, want 1", got)
	}
}
