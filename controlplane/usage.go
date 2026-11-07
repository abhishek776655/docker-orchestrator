package controlplane

import (
	"sync"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

// Live container memory, as reported with each node's heartbeat. Kept in
// memory only: it changes every few seconds, so persisting it would churn
// the database, and after a restart it refills within one sample interval.

// usageMaxAge is how old a node's report may be before its numbers are
// treated as unknown rather than shown stale.
var usageMaxAge = time.Minute

type nodeMemory struct {
	at      time.Time
	byWorkl map[string]int64
}

type usageState struct {
	mu     sync.Mutex
	byNode map[string]nodeMemory
}

func (svc *Service) recordMemory(node string, readings map[string]int64) {
	if readings == nil {
		return
	}
	svc.usage.mu.Lock()
	defer svc.usage.mu.Unlock()
	if svc.usage.byNode == nil {
		svc.usage.byNode = map[string]nodeMemory{}
	}
	svc.usage.byNode[node] = nodeMemory{at: time.Now(), byWorkl: readings}
}

// withMemory attaches each workload's live memory use, trusting only a
// fresh report from the node the workload is actually assigned to.
func (svc *Service) withMemory(recs []store.WorkloadRecord) {
	svc.usage.mu.Lock()
	defer svc.usage.mu.Unlock()
	for i := range recs {
		recs[i].MemoryUsedBytes = nil
		report, ok := svc.usage.byNode[recs[i].NodeID]
		if !ok || time.Since(report.at) > usageMaxAge {
			continue
		}
		if v, ok := report.byWorkl[recs[i].Key()]; ok {
			v := v
			recs[i].MemoryUsedBytes = &v
		}
	}
}
