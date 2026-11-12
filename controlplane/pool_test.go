package controlplane

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunBoundedLimitsConcurrencyAndRunsEverything(t *testing.T) {
	var running, peak, done atomic.Int32
	var jobs []func() error
	for i := 0; i < 12; i++ {
		jobs = append(jobs, func() error {
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			running.Add(-1)
			done.Add(1)
			return nil
		})
	}
	if err := runBounded(3, jobs); err != nil {
		t.Fatal(err)
	}
	if done.Load() != 12 {
		t.Fatalf("ran %d jobs, want 12", done.Load())
	}
	if peak.Load() > 3 || peak.Load() < 2 {
		t.Fatalf("peak concurrency %d, want 2..3", peak.Load())
	}
}

func TestRunBoundedRunsAllJobsAndJoinsErrorsInOrder(t *testing.T) {
	var ran atomic.Int32
	jobs := []func() error{
		func() error { ran.Add(1); return errors.New("first") },
		func() error { ran.Add(1); return nil },
		func() error { ran.Add(1); return errors.New("third") },
	}
	err := runBounded(1, jobs)
	if ran.Load() != 3 {
		t.Fatalf("ran %d, want all 3 despite a failure", ran.Load())
	}
	if err == nil || err.Error() != "first\nthird" {
		t.Fatalf("err = %v", err)
	}
}

func TestDeployParallelismEnv(t *testing.T) {
	t.Setenv("HACO_DEPLOY_PARALLELISM", "")
	if deployParallelism() != defaultDeployParallelism {
		t.Fatal("default expected")
	}
	t.Setenv("HACO_DEPLOY_PARALLELISM", "7")
	if deployParallelism() != 7 {
		t.Fatal("override expected")
	}
}
