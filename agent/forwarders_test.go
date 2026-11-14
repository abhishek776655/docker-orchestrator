package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

func inst(service, node, addr string, ports ...model.PortBinding) model.ServiceInstance {
	cp := 0
	if len(ports) > 0 {
		cp = ports[0].ContainerPort
	}
	return model.ServiceInstance{Service: service, NodeID: node, Address: addr, ContainerPort: cp, Ports: ports, Status: model.StatusReady}
}

func pb(container, host int) model.PortBinding {
	return model.PortBinding{ContainerPort: container, HostPort: host}
}

func TestForwardsOnlyForServicesOnOtherNodes(t *testing.T) {
	got, skipped := forwardsFor("a", []model.ServiceInstance{
		inst("frontendapi", "a", "192.168.1.7", pb(4000, 4000)), // here: reached by alias, no forwarder
		inst("metaapi", "b", "192.168.1.18", pb(5000, 5600)),
		inst("mysql", "b", "192.168.1.18", pb(3306, 3306)),
	})
	want := []forward{
		{Service: "metaapi", ListenPort: 5000, Target: "192.168.1.18:5600"},
		{Service: "mysql", ListenPort: 3306, Target: "192.168.1.18:3306"},
	}
	if !reflect.DeepEqual(got, want) || len(skipped) != 0 {
		t.Fatalf("forwards = %+v skipped = %v, want %+v", got, skipped, want)
	}
}

func TestForwardsReportServicesThatPublishNothing(t *testing.T) {
	got, skipped := forwardsFor("a", []model.ServiceInstance{{Service: "db", NodeID: "b", Address: "192.168.1.18", ContainerPort: 5432}})
	if len(got) != 0 || !reflect.DeepEqual(skipped, []string{"db"}) {
		t.Fatalf("forwards = %+v skipped = %v, want db skipped", got, skipped)
	}
}

func TestGroupingPutsClashingPortsInSeparateForwarders(t *testing.T) {
	forwards, _ := forwardsFor("a", []model.ServiceInstance{
		inst("metaapi", "b", "192.168.1.18", pb(5000, 5600)),
		inst("data-explore-api", "b", "192.168.1.18", pb(5000, 5400)), // also 5000
		inst("mysql", "b", "192.168.1.18", pb(3306, 3306)),
		inst("redis", "b", "192.168.1.18", pb(6379, 6379)),
		inst("events", "b", "192.168.1.18", pb(3000, 3030)),
		inst("frontend-v2", "b", "192.168.1.18", pb(3000, 3000)), // also 3000
	})
	groups := groupForwards(forwards)
	if len(groups) != 2 {
		t.Fatalf("%d forwarders, want 2: the 5000 and 3000 clashes need a second one", len(groups))
	}
	seen := map[string]int{}
	for i, g := range groups {
		ports := map[int]bool{}
		for _, f := range g.Forwards {
			if ports[f.ListenPort] {
				t.Fatalf("forwarder %d listens on %d twice", i, f.ListenPort)
			}
			ports[f.ListenPort] = true
			seen[f.Service]++
		}
	}
	for _, svc := range []string{"metaapi", "data-explore-api", "mysql", "redis", "events", "frontend-v2"} {
		if seen[svc] != 1 {
			t.Errorf("%s is relayed %d times, want once", svc, seen[svc])
		}
	}
}

func TestAServiceKeepsAllItsPortsInOneForwarder(t *testing.T) {
	forwards, _ := forwardsFor("a", []model.ServiceInstance{
		inst("neo4j", "b", "192.168.1.18", pb(7474, 7474), pb(7687, 7687)),
		inst("other", "b", "192.168.1.18", pb(7474, 9000)), // clashes with neo4j's 7474
	})
	groups := groupForwards(forwards)
	for _, g := range groups {
		has := map[int]bool{}
		for _, f := range g.Forwards {
			if f.Service == "neo4j" {
				has[f.ListenPort] = true
			}
		}
		if len(has) == 1 {
			t.Fatalf("neo4j's ports are split across forwarders: %+v", groups)
		}
	}
}

func TestForwarderCommandAndHash(t *testing.T) {
	g := forwarderGroup{Services: []string{"metaapi", "mysql"}, Forwards: []forward{
		{Service: "metaapi", ListenPort: 5000, Target: "192.168.1.18:5600"},
		{Service: "mysql", ListenPort: 3306, Target: "192.168.1.18:3306"},
	}}
	cmd := g.command()
	if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		t.Fatalf("command = %v", cmd)
	}
	for _, want := range []string{"nc -ll -p 5000 -e nc 192.168.1.18 5600 &", "nc -ll -p 3306 -e nc 192.168.1.18 3306 &", "wait"} {
		if !strings.Contains(cmd[2], want) {
			t.Errorf("script %q is missing %q", cmd[2], want)
		}
	}
	a, b := g.hash("busybox:1.36"), g.hash("busybox:1.36")
	if a != b {
		t.Fatal("hash must be stable")
	}
	g.Forwards[0].Target = "192.168.1.19:5600" // the service moved
	if g.hash("busybox:1.36") == a {
		t.Fatal("hash must change when a target changes, so the forwarder is recreated")
	}
}

func TestProjectNetworkName(t *testing.T) {
	for in, want := range map[string]string{"haco-test": "haco-haco-test", "My Shop!": "haco-my-shop-"} {
		if got := projectNetworkName(in); got != want {
			t.Errorf("projectNetworkName(%q) = %q, want %q", in, got, want)
		}
	}
}
