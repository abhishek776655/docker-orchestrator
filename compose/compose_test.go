package compose_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/compose"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
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

func TestNetworkRecommendationUsesLANForRemappedPublishedPort(t *testing.T) {
	// A remapped published port (host != container) is fully supported via
	// Workload.PortBindings (the agent binds the real host port to the real
	// container port independently), so this no longer needs Swarm overlay.
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
	if got := string(plan.Networking.Mode); got != "lan" {
		t.Fatalf("network mode = %q, want lan", got)
	}
	if !plan.Networking.Supported {
		t.Fatalf("LAN networking should be supported: %+v", plan.Networking)
	}
}

func TestNetworkRecommendationIgnoresUnpublishedPortNoOneDependsOn(t *testing.T) {
	// metaapi's unpublished port 5000 is never a cross-host reachability
	// concern here since nothing else in the plan depends_on metaapi — an
	// internal-only port (e.g. a database's cluster/gossip port) shouldn't
	// force the whole plan into an unprovisioned Swarm overlay just because
	// it happens to exist.
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
	if got := string(plan.Networking.Mode); got != "lan" {
		t.Fatalf("network mode = %q, want lan: %+v", got, plan.Networking)
	}
}

func TestNetworkRecommendationIgnoresUnpublishedPortWhenServiceHasAnotherPublished(t *testing.T) {
	// Mirrors the real-world qdrant case: 6333/6334 are published (and
	// that's what frontendapi actually calls via QDRANT_HOST), 6335 is an
	// internal-only cluster/gossip port nothing depends on reaching. A
	// dependent on the service through its published ports shouldn't force
	// the whole plan into Swarm overlay over the extra unpublished one.
	plan, err := compose.Load(context.Background(), "demo", "/tmp", []byte(`
services:
  qdrant:
    image: qdrant/qdrant:latest
    ports:
      - "6333:6333"
      - "6334:6334"
    expose:
      - "6333"
      - "6334"
      - "6335"
  frontend:
    image: frontend:latest
    depends_on:
      - qdrant
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := string(plan.Networking.Mode); got != "lan" {
		t.Fatalf("network mode = %q, want lan: %+v", got, plan.Networking)
	}
}

func TestNetworkRecommendationSuggestsSwarmForUnpublishedPortADependentNeeds(t *testing.T) {
	plan, err := compose.Load(context.Background(), "demo", "/tmp", []byte(`
services:
  metaapi:
    image: metaapi:latest
    expose:
      - "5000"
  frontend:
    image: frontend:latest
    depends_on:
      - metaapi
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

func TestCanDeployAllowsRemappedPublishedPort(t *testing.T) {
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
	if !plan.CanDeploy() {
		t.Fatalf("a remapped published port is fully supported and should be deployable: %+v", plan.Networking)
	}
}

func TestComposeBuildDefaultsPlatformWithNoArchitectureConstraint(t *testing.T) {
	// No explicit platform: the build itself still defaults to linux/amd64
	// (docker build's own default), but no hard Architecture constraint is
	// placed on the workload — it should schedule onto whatever node is
	// available (e.g. an arm64-only fleet), not just an amd64 one.
	plan, err := compose.Load(context.Background(), "demo", "/tmp", []byte(`
services:
  app:
    build: .
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	assertBuildPlatform(t, plan, "linux/amd64", "")
}

func TestComposeBuildUsesExplicitPlatform(t *testing.T) {
	plan, err := compose.Load(context.Background(), "demo", "/tmp", []byte(`
services:
  app:
    platform: linux/arm64
    build: .
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	assertBuildPlatform(t, plan, "linux/arm64", "arm64")
}

func assertBuildPlatform(t *testing.T, plan *compose.Plan, wantPlatform, wantArchitecture string) {
	t.Helper()
	if len(plan.Builds) != 1 {
		t.Fatalf("builds = %d, want 1", len(plan.Builds))
	}
	if got := plan.Workloads[0].Constraints.Architecture; got != wantArchitecture {
		t.Fatalf("scheduled architecture = %q, want %q", got, wantArchitecture)
	}
	data, err := json.Marshal(plan.Builds)
	if err != nil {
		t.Fatal(err)
	}
	var builds []struct {
		Platform string
	}
	if err := json.Unmarshal(data, &builds); err != nil {
		t.Fatal(err)
	}
	if got := builds[0].Platform; got != wantPlatform {
		t.Fatalf("build platform = %q, want %q", got, wantPlatform)
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

	var metaapi *model.Workload
	for i := range plan.Workloads {
		if plan.Workloads[i].Service == "metaapi" {
			metaapi = &plan.Workloads[i]
		}
	}
	if metaapi == nil {
		t.Fatalf("metaapi workload not found")
	}

	if len(metaapi.ContainerPorts) != 1 || metaapi.ContainerPorts[0] != 5000 {
		t.Fatalf("container ports = %v, want [5000]", metaapi.ContainerPorts)
	}
	// The real, published host port (independent remapping) must be what the
	// scheduler reserves and conflict-checks — not the container port.
	if len(metaapi.Constraints.HostPorts) != 1 || metaapi.Constraints.HostPorts[0] != 5600 {
		t.Fatalf("host ports = %v, want [5600]", metaapi.Constraints.HostPorts)
	}
	if len(metaapi.PortBindings) != 1 || metaapi.PortBindings[0].ContainerPort != 5000 || metaapi.PortBindings[0].HostPort != 5600 {
		t.Fatalf("port bindings = %+v, want [{ContainerPort:5000 HostPort:5600}]", metaapi.PortBindings)
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

// TestPlaceAnywhereOptsOutOfSourceBindMountPin covers the common
// devcontainer pattern where every service bind-mounts its own source for
// live-reload even though only one or two are actually being developed at
// a time: x-haco-place-anywhere must let such a service schedule freely
// instead of forcing it onto the control-plane host.
func TestPlaceAnywhereOptsOutOfSourceBindMountPin(t *testing.T) {
	workingDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workingDir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingDir, "app", "Dockerfile"), []byte("FROM scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
services:
  app:
    build:
      context: "./app"
    volumes:
      - "./app:/app"
    x-haco-place-anywhere: true
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !plan.Valid() {
		t.Fatalf("expected valid plan, got rejections: %v", plan.Rejections)
	}

	var app *model.Workload
	for i := range plan.Workloads {
		if plan.Workloads[i].Service == "app" {
			app = &plan.Workloads[i]
		}
	}
	if app == nil {
		t.Fatalf("app workload not found")
	}
	if app.Storage.Class != model.NodeLocalPersistent {
		t.Fatalf("storage class = %v, want node_local_persistent", app.Storage.Class)
	}
	if app.Storage.PinnedNodeID != "" {
		t.Fatalf("pinned node = %q, want unpinned (place-anywhere)", app.Storage.PinnedNodeID)
	}
	if len(app.Volumes) != 1 || app.Volumes[0].HostPath != "" {
		t.Fatalf("volumes = %+v, want one managed volume with no literal host path", app.Volumes)
	}
}

// TestApplyHostPinOverride covers the no-compose-file-edits alternative to
// x-haco-place-anywhere: a CLI-level --pin-host list where only the named
// services stay pinned to the control-plane host (e.g. a database and
// whatever's being actively live-edited) and everything else — even a
// service compose.go's own bind-mount heuristic would normally pin — is
// freed to schedule anywhere.
func TestApplyHostPinOverride(t *testing.T) {
	workingDir := t.TempDir()
	for _, svc := range []string{"frontendapi", "auth"} {
		dir := filepath.Join(workingDir, svc)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	yaml := []byte(`
services:
  frontendapi:
    build:
      context: "./frontendapi"
    volumes:
      - "./frontendapi:/app"
  auth:
    build:
      context: "./auth"
    volumes:
      - "./auth:/app"
  mysql:
    image: mysql:8.0
    volumes:
      - mysql-data:/var/lib/mysql
volumes:
  mysql-data:
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Before the override: both frontendapi and auth are auto-pinned (their
	// own source bind-mount), same as mysql's managed data volume.
	byService := map[string]*model.Workload{}
	for i := range plan.Workloads {
		byService[plan.Workloads[i].Service] = &plan.Workloads[i]
	}
	for _, svc := range []string{"frontendapi", "auth", "mysql"} {
		if byService[svc].Storage.PinnedNodeID == "" {
			t.Fatalf("%s: expected auto-pinned before override", svc)
		}
	}

	compose.ApplyHostPinOverride(plan, []string{"mysql"})

	if byService["mysql"].Storage.PinnedNodeID == "" {
		t.Fatalf("mysql: expected to stay pinned (named in --pin-host)")
	}
	if byService["frontendapi"].Storage.PinnedNodeID != "" {
		t.Fatalf("frontendapi: expected unpinned (not named in --pin-host)")
	}
	if byService["auth"].Storage.PinnedNodeID != "" {
		t.Fatalf("auth: expected unpinned (not named in --pin-host)")
	}
	if len(byService["frontendapi"].Volumes) != 1 || byService["frontendapi"].Volumes[0].HostPath != "" {
		t.Fatalf("frontendapi: expected its literal bind-mount host path cleared once unpinned, got %+v", byService["frontendapi"].Volumes)
	}
}
