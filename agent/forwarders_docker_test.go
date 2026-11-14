package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// TestForwarderReachesAServiceByNameOnARealDocker is an opt-in check against
// the local Docker daemon (HACO_DOCKER_TEST=1). It stands in for "a service on
// another node" with any HTTP server reachable at HACO_DOCKER_TEST_TARGET
// (host:port, on an address other containers can reach), creates the
// forwarder, and calls it by name from a container on the project network.
// HACO_FORWARDER_IMAGE may name an image that is already present locally.
func TestForwarderReachesAServiceByNameOnARealDocker(t *testing.T) {
	if os.Getenv("HACO_DOCKER_TEST") != "1" {
		t.Skip("set HACO_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	target := os.Getenv("HACO_DOCKER_TEST_TARGET")
	if target == "" {
		t.Skip("set HACO_DOCKER_TEST_TARGET=host:port of an HTTP server")
	}
	host, port, _ := strings.Cut(target, ":")
	hostPort := 0
	for _, r := range port {
		hostPort = hostPort*10 + int(r-'0')
	}

	ctx := context.Background()
	d, err := NewDocker("itest-node", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const project = "forwarder-itest"
	t.Cleanup(func() {
		_ = d.RemoveForwardersExcept(ctx, map[string]bool{})
		_ = exec.Command("docker", "network", "rm", projectNetworkName(project)).Run()
	})

	instances := []model.ServiceInstance{
		{Service: "metaapi", NodeID: "other-node", Address: host, ContainerPort: 5000, Ports: []model.PortBinding{{ContainerPort: 5000, HostPort: hostPort}}, Status: model.StatusReady},
	}
	forwards, _ := forwardsFor("itest-node", instances)
	if err := d.SyncForwarders(ctx, project, groupForwards(forwards)); err != nil {
		t.Fatalf("SyncForwarders: %v", err)
	}
	time.Sleep(2 * time.Second)

	out, err := exec.Command("docker", "run", "--rm", "--network", projectNetworkName(project), forwarderImage(),
		"wget", "-q", "-T", "10", "-O", "-", "http://metaapi:5000/status").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("calling metaapi:5000 from the project network: err=%v out=%q", err, out)
	}

	// An unchanged desired state leaves the forwarder alone; a changed one recreates it.
	before, _ := d.listForwarders(ctx, project)
	if err := d.SyncForwarders(ctx, project, groupForwards(forwards)); err != nil {
		t.Fatal(err)
	}
	after, _ := d.listForwarders(ctx, project)
	for name, c := range before {
		if after[name].ID != c.ID {
			t.Fatalf("forwarder %s was recreated although nothing changed", name)
		}
	}
	if err := d.SyncForwarders(ctx, project, nil); err != nil {
		t.Fatal(err)
	}
	if left, _ := d.listForwarders(ctx, project); len(left) != 0 {
		t.Fatalf("%d forwarders left after the service went away", len(left))
	}
}

// TestSameNodeServicesReachEachOtherByNameOnARealDocker starts a workload
// through EnsureRunning and calls it by its service name from another
// container on the project network, with no published port and no address
// entry: what Docker Compose gives you for free.
func TestSameNodeServicesReachEachOtherByNameOnARealDocker(t *testing.T) {
	if os.Getenv("HACO_DOCKER_TEST") != "1" {
		t.Skip("set HACO_DOCKER_TEST=1 to run against the local Docker daemon")
	}
	ctx := context.Background()
	d, err := NewDocker("itest-node", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const project = "samenode-itest"
	w := model.Workload{
		Project: project, Service: "api", Revision: "latest",
		Image:   forwarderImage(),
		Command: []string{"sh", "-c", "mkdir -p /www && echo hello-from-api > /www/index.html && httpd -f -p 8000 -h /www"},
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", ContainerName(w.Key())).Run()
		_ = exec.Command("docker", "network", "rm", projectNetworkName(project)).Run()
	})
	if _, _, err := d.EnsureRunning(ctx, w); err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	time.Sleep(2 * time.Second)
	out, err := exec.Command("docker", "run", "--rm", "--network", projectNetworkName(project), forwarderImage(),
		"wget", "-q", "-T", "10", "-O", "-", "http://api:8000/").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "hello-from-api") {
		t.Fatalf("calling api:8000 on the project network: err=%v out=%q", err, out)
	}
}
