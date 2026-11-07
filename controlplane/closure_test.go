package controlplane

import (
	"sort"
	"strings"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/compose"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

func closurePlan() *compose.Plan {
	return &compose.Plan{Workloads: []model.Workload{
		{Service: "frontend", DependsOn: []string{"api"}},
		{Service: "api", DependsOn: []string{"db"}},
		{Service: "worker", DependsOn: []string{"queue", "db"}},
		{Service: "db"},
		{Service: "queue"},
		{Service: "unrelated"},
	}}
}

func services(p *compose.Plan) string {
	var names []string
	for _, w := range p.Workloads {
		names = append(names, w.Service)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func TestFilterToClosureUnionsSeveralServices(t *testing.T) {
	got, err := filterToClosure(closurePlan(), "frontend, worker")
	if err != nil {
		t.Fatal(err)
	}
	if s := services(got); s != "api,db,frontend,queue,worker" {
		t.Fatalf("services = %s, want both closures with shared db once and nothing unrelated", s)
	}
}

func TestFilterToClosureSingleServiceUnchanged(t *testing.T) {
	got, err := filterToClosure(closurePlan(), "api")
	if err != nil {
		t.Fatal(err)
	}
	if s := services(got); s != "api,db" {
		t.Fatalf("services = %s", s)
	}
}

func TestFilterToClosureRejectsUnknownService(t *testing.T) {
	if _, err := filterToClosure(closurePlan(), "api,nope"); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("err = %v, want the unknown name reported", err)
	}
}
