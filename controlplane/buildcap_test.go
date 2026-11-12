package controlplane

import (
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/compose"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

func TestRequireCapabilityAddsOnce(t *testing.T) {
	plan := &compose.Plan{Workloads: []model.Workload{{Service: "api"}, {Service: "db"}}}
	requireCapability(plan, "api", "buildx")
	requireCapability(plan, "api", "buildx")
	if got := plan.Workloads[0].Constraints.RequiredCapabilities; len(got) != 1 || got[0] != "buildx" {
		t.Fatalf("api capabilities = %v", got)
	}
	if len(plan.Workloads[1].Constraints.RequiredCapabilities) != 0 {
		t.Fatal("db must not get the capability")
	}
}
