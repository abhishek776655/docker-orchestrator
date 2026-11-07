package controlplane

import (
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

func usageRec(service, node string) store.WorkloadRecord {
	return store.WorkloadRecord{Workload: model.Workload{Project: "p", Service: service, Revision: "latest"}, NodeID: node}
}

func TestWithMemoryTrustsOnlyFreshReportsFromAssignedNode(t *testing.T) {
	svc := &Service{}
	svc.recordMemory("node-a", map[string]int64{"p/api/latest": 300, "p/db/latest": 900})
	svc.recordMemory("node-b", map[string]int64{"p/db/latest": 1})

	recs := []store.WorkloadRecord{usageRec("api", "node-a"), usageRec("db", "node-a"), usageRec("worker", "node-a"), usageRec("cache", "")}
	svc.withMemory(recs)
	if recs[0].MemoryUsedBytes == nil || *recs[0].MemoryUsedBytes != 300 {
		t.Fatalf("api = %v, want 300", recs[0].MemoryUsedBytes)
	}
	if recs[1].MemoryUsedBytes == nil || *recs[1].MemoryUsedBytes != 900 {
		t.Fatalf("db = %v, want node-a's 900, not node-b's report", recs[1].MemoryUsedBytes)
	}
	if recs[2].MemoryUsedBytes != nil || recs[3].MemoryUsedBytes != nil {
		t.Fatal("workloads with no report must stay unknown")
	}

	old := usageMaxAge
	usageMaxAge = 0
	t.Cleanup(func() { usageMaxAge = old })
	time.Sleep(time.Millisecond)
	stale := []store.WorkloadRecord{usageRec("api", "node-a")}
	svc.withMemory(stale)
	if stale[0].MemoryUsedBytes != nil {
		t.Fatal("a stale report must read as unknown")
	}
}
