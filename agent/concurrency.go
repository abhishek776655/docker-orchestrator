package agent

import (
	"context"
	"os"
	"strconv"
	"sync"
)

// Workloads on a node are handled concurrently, one goroutine each, so a
// long build or pull no longer holds up an unrelated service's plain start.
// Three rules keep that safe:
//   - one goroutine per workload at a time (keyLocks): the poll runs every
//     couple of seconds and must not start a workload that is still being
//     started from an earlier tick;
//   - builds and pulls share a small limit (heavySlots) so a small machine
//     is not swamped by several at once; container starts are cheap and
//     not limited;
//   - shutdown and rename wait for in-flight work (inflight) before they
//     stop or remove containers.

// defaultHeavySlots is how many builds/pulls run at once on a node unless
// HACO_AGENT_PARALLEL_BUILDS says otherwise.
const defaultHeavySlots = 3

type keyLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (k *keyLocks) get(key string) *sync.Mutex {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[string]*sync.Mutex{}
	}
	l, ok := k.m[key]
	if !ok {
		l = &sync.Mutex{}
		k.m[key] = l
	}
	return l
}

func heavySlotsFromEnv() chan struct{} {
	n := defaultHeavySlots
	if v, err := strconv.Atoi(os.Getenv("HACO_AGENT_PARALLEL_BUILDS")); err == nil && v > 0 {
		n = v
	}
	return make(chan struct{}, n)
}

// withHeavySlot runs fn while holding one build/pull slot, waiting for one
// to free up. A nil slot channel (agents built without New, e.g. in tests)
// means no limit.
func (a *Agent) withHeavySlot(ctx context.Context, fn func() error) error {
	if a.heavy != nil {
		select {
		case a.heavy <- struct{}{}:
			defer func() { <-a.heavy }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fn()
}

// dispatch handles one workload in its own goroutine unless it is already
// being handled, and reports whether it started one.
func (a *Agent) dispatch(key string, fn func()) bool {
	l := a.locks.get(key)
	if !l.TryLock() {
		return false
	}
	a.inflight.Add(1)
	go func() {
		defer a.inflight.Done()
		defer l.Unlock()
		fn()
	}()
	return true
}
