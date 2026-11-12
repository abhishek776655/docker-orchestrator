package controlplane

import (
	"context"
	"sync"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
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

// busyPortState holds, per node, the host ports held by containers haco
// doesn't manage, as the agent last reported them. In memory only: it
// describes the node right now, and the next heartbeat refreshes it.
type busyPortState struct {
	mu     sync.Mutex
	byNode map[string]map[int]string
}

// recordBusyPorts stores a node's report. nil means the agent didn't send
// one (an older agent), which leaves what is known untouched.
func (svc *Service) recordBusyPorts(node string, ports map[int]string) {
	if ports == nil {
		return
	}
	svc.busy.mu.Lock()
	defer svc.busy.mu.Unlock()
	if svc.busy.byNode == nil {
		svc.busy.byNode = map[string]map[int]string{}
	}
	svc.busy.byNode[node] = ports
}

// listNodesForScheduling is ListNodes plus each node's reported busy ports,
// so the scheduler skips a node whose port is already taken by something
// outside haco.
func (svc *Service) listNodesForScheduling(ctx context.Context) ([]model.Node, error) {
	nodes, err := svc.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	svc.busy.mu.Lock()
	defer svc.busy.mu.Unlock()
	for i := range nodes {
		nodes[i].BusyPorts = svc.busy.byNode[nodes[i].ID]
	}
	return nodes, nil
}
