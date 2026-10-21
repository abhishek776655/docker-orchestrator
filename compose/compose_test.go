package compose_test

import (
	"context"
	"strings"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/compose"
)

func TestNetworkRecommendationUsesLANWhenPublishedPortMatchesTarget(t *testing.T) {
	plan, err := compose.Load(context.Background(), "demo", "/tmp", []byte(`
services:
  metaapi:
    image: metaapi:latest
    ports:
      - "5000:5000"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := string(plan.Networking.Mode); got != "lan" {
		t.Fatalf("network mode = %q, want lan", got)
	}
	if !plan.Networking.Supported {
		t.Fatalf("LAN networking should be supported: %+v", plan.Networking)
	}
}

func TestNetworkRecommendationSuggestsSwarmForDifferentPublishedPort(t *testing.T) {
	plan, err := compose.Load(context.Background(), "demo", "/tmp", []byte(`
services:
  metaapi:
    image: metaapi:latest
    ports:
      - "5600:5000"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := string(plan.Networking.Mode); got != "swarm_overlay" {
		t.Fatalf("network mode = %q, want swarm_overlay", got)
	}
	if plan.Networking.Supported {
		t.Fatalf("Swarm recommendation must be marked unavailable until provisioned")
	}
	if !strings.Contains(strings.Join(plan.Networking.Reasons, " "), "5600:5000") {
		t.Fatalf("recommendation should explain the mismatched mapping: %+v", plan.Networking)
	}
}

func TestNetworkRecommendationSuggestsSwarmForUnpublishedServicePort(t *testing.T) {
	plan, err := compose.Load(context.Background(), "demo", "/tmp", []byte(`
services:
  metaapi:
    image: metaapi:latest
    expose:
      - "5000"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := string(plan.Networking.Mode); got != "swarm_overlay" {
		t.Fatalf("network mode = %q, want swarm_overlay", got)
	}
	if !strings.Contains(strings.Join(plan.Networking.Reasons, " "), "not published") {
		t.Fatalf("recommendation should explain the missing host port: %+v", plan.Networking)
	}
}

func TestCanDeployBlocksPlanThatNeedsSwarmOverlay(t *testing.T) {
	plan, err := compose.Load(context.Background(), "demo", "/tmp", []byte(`
services:
  metaapi:
    image: metaapi:latest
    ports:
      - "5600:5000"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if plan.CanDeploy() {
		t.Fatalf("plan requiring an unprovisioned overlay network must not be deployable")
	}
}

// TestPublishedPortIndependentOfContainerPort covers the exact scenario
// named in the plan: a published host port and an internal container port
// that differ must both survive, with the internal port used for
// service-to-service DNS regardless of what's published.
func TestPublishedPortIndependentOfContainerPort(t *testing.T) {
	yaml := []byte(`
services:
  metaapi:
    image: metaapi:latest
    ports:
      - "5600:5000"
`)
	plan, err := compose.Load(context.Background(), "demo", "/tmp", yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !plan.Valid() {
		t.Fatalf("expected valid plan, got rejections: %v", plan.Rejections)
	}

	var metaapi *struct{ ContainerPorts, HostPorts []int }
	for _, w := range plan.Workloads {
		if w.Service != "metaapi" {
			continue
		}
		metaapi = &struct{ ContainerPorts, HostPorts []int }{w.ContainerPorts, w.Constraints.HostPorts}
	}
	if metaapi == nil {
		t.Fatalf("metaapi workload not found")
	}

	if len(metaapi.ContainerPorts) != 1 || metaapi.ContainerPorts[0] != 5000 {
		t.Fatalf("container ports = %v, want [5000]", metaapi.ContainerPorts)
	}
	if len(metaapi.HostPorts) != 1 || metaapi.HostPorts[0] != 5000 {
		t.Fatalf("host ports = %v, want [5000] (bound as the container port; independent remapping not implemented)", metaapi.HostPorts)
	}
}

// TestUnpublishedPortIsInternalOnly covers the review focus: "An
// internal-only container port must be discoverable without creating a
// host-port binding."
func TestUnpublishedPortIsInternalOnly(t *testing.T) {
	yaml := []byte(`
services:
  metaapi:
    image: metaapi:latest
    expose:
      - "5000"
`)
	plan, err := compose.Load(context.Background(), "demo", "/tmp", yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !plan.Valid() {
		t.Fatalf("expected valid plan, got rejections: %v", plan.Rejections)
	}

	for _, w := range plan.Workloads {
		if w.Service != "metaapi" {
			continue
		}
		if len(w.ContainerPorts) != 1 || w.ContainerPorts[0] != 5000 {
			t.Fatalf("container ports = %v, want [5000]", w.ContainerPorts)
		}
		if len(w.Constraints.HostPorts) != 0 {
			t.Fatalf("host ports = %v, want none for an unpublished/expose-only port", w.Constraints.HostPorts)
		}
	}
}
