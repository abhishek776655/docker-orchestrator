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

// TestOwnSourceBindMountUnpinnedByDefaultKeepsHostPath covers the new
// default: a service with no x-haco-pin-host/x-haco-place-anywhere at all
// is unpinned, same as place-anywhere — but unlike place-anywhere, it
// keeps the volume's real literal HostPath (for resolveVolumeSnapshots to
// tar up at deploy time, or for a literal live mount if later pinned to
// host some other way), rather than discarding it for an empty managed
// directory.
func TestOwnSourceBindMountUnpinnedByDefaultKeepsHostPath(t *testing.T) {
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
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !plan.Valid() {
		t.Fatalf("expected valid plan, got rejections: %v", plan.Rejections)
	}
	app := plan.Workloads[0]
	if app.Storage.PinnedNodeID != "" {
		t.Fatalf("pinned node = %q, want unpinned by default", app.Storage.PinnedNodeID)
	}
	if len(app.Volumes) != 1 || app.Volumes[0].HostPath == "" {
		t.Fatalf("volumes = %+v, want the literal host path kept (for resolveVolumeSnapshots)", app.Volumes)
	}
}

// TestXHacoPinHostGivesOwnSourceBindMountLiveReload covers the other side:
// with x-haco-pin-host explicitly set, a directory bind mount's literal
// path is both preserved AND the service is pinned there — a true live
// bind mount, since that's the only case where the literal path is
// guaranteed to exist where the workload actually runs.
func TestXHacoPinHostGivesOwnSourceBindMountLiveReload(t *testing.T) {
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
    x-haco-pin-host: true
    volumes:
      - "./app:/app"
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	app := plan.Workloads[0]
	if app.Storage.PinnedNodeID == "" {
		t.Fatalf("expected x-haco-pin-host to pin this service")
	}
	if len(app.Volumes) != 1 || app.Volumes[0].HostPath == "" {
		t.Fatalf("volumes = %+v, want the literal host path for a true live bind mount", app.Volumes)
	}
}

// TestSingleFileBindMountTravelsWithWorkloadUnpinned covers the real-world
// firebase-emulator case: a single-file bind mount (e.g.
// ./firebase.json:/app/firebase.json) has no safe "empty managed
// directory" substitute, but its content is small enough to carry with
// the workload itself (VolumeMount.FileContent) rather than forcing a
// host pin like a directory source-code live-mount does — the same idea
// as shipping a build context to whichever node actually builds it.
func TestSingleFileBindMountTravelsWithWorkloadUnpinned(t *testing.T) {
	workingDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workingDir, "config.json"), []byte(`{"key":"value"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
services:
  app:
    image: app:latest
    volumes:
      - "./config.json:/app/config.json"
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
	if app.Storage.PinnedNodeID != "" {
		t.Fatalf("pinned node = %q, want unpinned: the file travels with the workload", app.Storage.PinnedNodeID)
	}
	if len(app.Volumes) != 1 || app.Volumes[0].HostPath != "" {
		t.Fatalf("volumes = %+v, want no literal host path", app.Volumes)
	}
	if string(app.Volumes[0].FileContent) != `{"key":"value"}` {
		t.Fatalf("file content = %q, want the real file's bytes", app.Volumes[0].FileContent)
	}
}

// TestSingleFileBindMountOverLimitIsRejected covers the safety valve for
// maxInlineBindFileBytes: something too large to sensibly carry with the
// workload record should be a clear plan-time rejection, not a silently
// truncated or oversized workload.
func TestSingleFileBindMountOverLimitIsRejected(t *testing.T) {
	workingDir := t.TempDir()
	oversized := make([]byte, 2<<20) // 2 MiB, over the 1 MiB cap
	if err := os.WriteFile(filepath.Join(workingDir, "big.bin"), oversized, 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
services:
  app:
    image: app:latest
    volumes:
      - "./big.bin:/app/big.bin"
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if plan.Valid() {
		t.Fatalf("expected an oversized single-file bind mount to be rejected")
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
    x-haco-pin-host: true
    volumes:
      - "./frontendapi:/app"
  auth:
    build:
      context: "./auth"
    x-haco-pin-host: true
    volumes:
      - "./auth:/app"
  mysql:
    image: mysql:8.0
    x-haco-pin-host: true
    volumes:
      - mysql-data:/var/lib/mysql
volumes:
  mysql-data:
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Before the override: frontendapi, auth and mysql are all pinned
	// because this file explicitly opts each in with x-haco-pin-host — a
	// directory bind mount or a managed data volume with no such opt-in is
	// unpinned by default now (the user's call, not an implicit rule).
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

// TestApplyHostPinOverrideKeepsSingleFileBindMountContent covers the
// real-world firebase-emulator case: a service with only single-file bind
// mounts (e.g. ./firebase.json:/app/firebase.json) is unpinned from the
// start (the file travels with the workload), and ApplyHostPinOverride
// must not disturb its FileContent even when the service isn't named in
// --pin-host — there's no HostPath to clear, and nothing should be.
func TestApplyHostPinOverrideKeepsSingleFileBindMountContent(t *testing.T) {
	workingDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workingDir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
services:
  firebase:
    image: firebase:latest
    volumes:
      - "./config.json:/app/config.json"
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	compose.ApplyHostPinOverride(plan, []string{"some-other-service"})

	firebase := plan.Workloads[0]
	if firebase.Storage.PinnedNodeID != "" {
		t.Fatalf("pinned node = %q, want unpinned", firebase.Storage.PinnedNodeID)
	}
	if len(firebase.Volumes) != 1 || len(firebase.Volumes[0].FileContent) == 0 {
		t.Fatalf("volumes = %+v, want the file content preserved", firebase.Volumes)
	}
}

// TestLoadInterpolatesFromDotEnvAndProcessEnvironment covers a real bug: a
// $-referenced build arg (e.g. an SSH deploy key) must resolve from the
// project's own .env file and the calling process's environment, exactly
// like real `docker compose` — not silently blank out, which is what an
// always-empty interpolation environment did before.
func TestLoadInterpolatesFromDotEnvAndProcessEnvironment(t *testing.T) {
	workingDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workingDir, ".env"), []byte("FROM_DOTENV=dotenv-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FROM_PROCESS_ENV", "process-value")

	yaml := []byte(`
services:
  app:
    image: app:latest
    environment:
      - A=${FROM_DOTENV}
      - B=${FROM_PROCESS_ENV}
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(plan.Workloads) != 1 {
		t.Fatalf("workloads = %d, want 1", len(plan.Workloads))
	}
	env := plan.Workloads[0].Env
	if env["A"] != "dotenv-value" {
		t.Fatalf("A = %q, want value from .env file", env["A"])
	}
	if env["B"] != "process-value" {
		t.Fatalf("B = %q, want value from process environment", env["B"])
	}
}

// TestApplyPinOverridePinsServicesToDifferentNodes covers --pin-node:
// different services pinned to different explicit nodes in the same call,
// with every other service still freed to schedule anywhere.
func TestApplyPinOverridePinsServicesToDifferentNodes(t *testing.T) {
	yaml := []byte(`
services:
  mysql:
    image: mysql:8
    volumes:
      - mysql-data:/var/lib/mysql
  metaapi:
    image: metaapi:latest
  rabbitmq:
    image: rabbitmq:3
volumes:
  mysql-data:
`)
	plan, err := compose.Load(context.Background(), "demo", "/tmp", yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	compose.ApplyPinOverride(plan, map[string][]string{
		"node-a": {"mysql"},
		"node-b": {"metaapi"},
	})

	byService := map[string]model.Workload{}
	for _, w := range plan.Workloads {
		byService[w.Service] = w
	}
	if got := byService["mysql"].Constraints.PinnedNodeID; got != "node-a" {
		t.Fatalf("mysql pinned node = %q, want node-a", got)
	}
	if got := byService["mysql"].Storage.PinnedNodeID; got != "node-a" {
		t.Fatalf("mysql storage pinned node = %q, want node-a", got)
	}
	if got := byService["metaapi"].Constraints.PinnedNodeID; got != "node-b" {
		t.Fatalf("metaapi pinned node = %q, want node-b", got)
	}
	if got := byService["rabbitmq"].Constraints.PinnedNodeID; got != "" {
		t.Fatalf("rabbitmq pinned node = %q, want unpinned", got)
	}
}

// TestManagedVolumeIsUnpinnedByDefault covers a deliberate change: a
// node_local_persistent service with a plain managed data volume (no
// x-haco-pin-node/x-haco-pin-host) is no longer auto-pinned to the
// control-plane host just because it's stateful. Affinity is the user's
// call (an explicit extension, or --pin-host/--pin-node at deploy time),
// never an implicit default — it schedules freely like
// x-haco-place-anywhere, then locks to wherever it lands on redeploy.
func TestManagedVolumeIsUnpinnedByDefault(t *testing.T) {
	yaml := []byte(`
services:
  mysql:
    image: mysql:8.0
    volumes:
      - mysql-data:/var/lib/mysql
volumes:
  mysql-data:
`)
	plan, err := compose.Load(context.Background(), "demo", "/tmp", yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(plan.Workloads) != 1 {
		t.Fatalf("workloads = %d, want 1", len(plan.Workloads))
	}
	mysql := plan.Workloads[0]
	if mysql.Storage.Class != model.NodeLocalPersistent {
		t.Fatalf("storage class = %v, want node_local_persistent", mysql.Storage.Class)
	}
	if mysql.Storage.PinnedNodeID != "" {
		t.Fatalf("pinned node = %q, want unpinned by default", mysql.Storage.PinnedNodeID)
	}
}

// TestXHacoPinHostStillPinsExplicitly covers the opt-in: x-haco-pin-host
// still pins a managed-volume service to the control-plane host when the
// user actually asks for it.
func TestXHacoPinHostStillPinsExplicitly(t *testing.T) {
	yaml := []byte(`
services:
  mysql:
    image: mysql:8.0
    x-haco-pin-host: true
    volumes:
      - mysql-data:/var/lib/mysql
volumes:
  mysql-data:
`)
	plan, err := compose.Load(context.Background(), "demo", "/tmp", yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if plan.Workloads[0].Storage.PinnedNodeID == "" {
		t.Fatalf("expected x-haco-pin-host to pin to the control-plane host")
	}
}

// TestMultiVolumePinSurvivesLaterGenericVolume covers a real bug: a
// service's own-source bind mount (build-context match, forces a host
// pin) was getting silently undone by a LATER, unrelated managed volume
// in the same service overwriting the whole Storage field — "last volume
// wins" instead of one coherent decision for the service. Order must not
// matter.
func TestMultiVolumePinSurvivesLaterGenericVolume(t *testing.T) {
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
    x-haco-pin-host: true
    volumes:
      - "./app:/app"
      - shared-models:/usr/local/src/models
volumes:
  shared-models:
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !plan.Valid() {
		t.Fatalf("expected valid plan, got rejections: %v", plan.Rejections)
	}
	app := plan.Workloads[0]
	if app.Storage.PinnedNodeID == "" {
		t.Fatalf("expected x-haco-pin-host's host pin to survive the later generic volume")
	}
	if len(app.Volumes) != 2 {
		t.Fatalf("volumes = %+v, want 2", app.Volumes)
	}
	if app.Volumes[0].HostPath == "" {
		t.Fatalf("app's own source volume should keep its literal host path")
	}
	if app.Volumes[1].HostPath != "" {
		t.Fatalf("the unrelated managed volume should not get a literal host path")
	}
}

// TestSharedBindDirectoryAcrossServicesIsPreserved covers the real
// ./protos bug: a directory bind-mounted by 2+ services, but nobody's
// build context, used to silently fall back to an empty managed
// directory, discarding real shared content every one of those services
// actually needed at runtime (e.g. shared .proto files). It must now keep
// the literal host path available (for resolveVolumeSnapshots to tar up,
// or for a literal live mount if explicitly pinned to host), the same as
// a build-context match — but, with no x-haco-pin-host, it's unpinned by
// default, same as every other directory bind-mount case now.
func TestSharedBindDirectoryAcrossServicesIsPreserved(t *testing.T) {
	workingDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workingDir, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingDir, "shared", "thing.proto"), []byte("message Thing {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
services:
  a:
    image: a:latest
    volumes:
      - "./shared:/shared"
  b:
    image: b:latest
    volumes:
      - "./shared:/shared"
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !plan.Valid() {
		t.Fatalf("expected valid plan, got rejections: %v", plan.Rejections)
	}
	for _, w := range plan.Workloads {
		if len(w.Volumes) != 1 || w.Volumes[0].HostPath == "" {
			t.Fatalf("%s: volumes = %+v, want the shared directory's literal host path preserved", w.Service, w.Volumes)
		}
		if w.Storage.PinnedNodeID != "" {
			t.Fatalf("%s: pinned node = %q, want unpinned (no x-haco-pin-host set)", w.Service, w.Storage.PinnedNodeID)
		}
	}
}

// TestSingleServiceBindDirectoryStaysManaged covers the control case: a
// directory bind-mounted by only one service (not a build context, not
// shared) is still the ordinary managed-data-directory default — the new
// shared-directory heuristic must not over-trigger on every directory
// bind mount.
func TestSingleServiceBindDirectoryStaysManaged(t *testing.T) {
	workingDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workingDir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
services:
  db:
    image: postgres:14
    volumes:
      - "./data:/var/lib/postgresql/data"
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	db := plan.Workloads[0]
	if db.Storage.PinnedNodeID != "" {
		t.Fatalf("pinned node = %q, want unpinned (single-service directory bind stays managed)", db.Storage.PinnedNodeID)
	}
	if len(db.Volumes) != 1 || db.Volumes[0].HostPath != "" {
		t.Fatalf("volumes = %+v, want a managed volume with no literal host path", db.Volumes)
	}
}

// TestRootBuildContextDoesNotCaptureUnrelatedBindMounts covers a real bug:
// a service with `build.context: .` (the project root, common when a
// Dockerfile needs a top-level file) made matchesAnyBuildContext's "is
// inside a build context" check true for literally every bind mount in
// the whole project — so an unrelated service's own data directory (e.g.
// mysql's ./mysql-data) got force-treated as a live-reload source mount
// (RemoteDirKey) instead of the ordinary managed-volume path (SeedDirKey),
// which then 404s at deploy time since nothing ever stashes a snapshot
// for it.
func TestBuildSSHIsCarriedNotWarned(t *testing.T) {
	workingDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workingDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
services:
  api:
    build:
      context: .
      ssh:
        - default
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(plan.Builds) != 1 {
		t.Fatalf("builds = %+v", plan.Builds)
	}
	if path, ok := plan.Builds[0].SSH["default"]; !ok || path != "" {
		t.Fatalf("build SSH = %+v, want {default: \"\"}", plan.Builds[0].SSH)
	}
	for _, w := range plan.Warnings {
		if strings.Contains(w, "build.ssh") {
			t.Fatalf("unexpected build.ssh warning now that it's supported: %q", w)
		}
	}
}

func TestRootBuildContextDoesNotCaptureUnrelatedBindMounts(t *testing.T) {
	workingDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workingDir, "mysql-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
services:
  rootbuilt:
    build:
      context: .
  mysql:
    image: mysql:8.0
    volumes:
      - "./mysql-data:/var/lib/mysql"
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var mysql model.Workload
	for _, wl := range plan.Workloads {
		if wl.Service == "mysql" {
			mysql = wl
		}
	}
	if len(mysql.Volumes) != 1 || mysql.Volumes[0].RemoteDirKey != "" || mysql.Volumes[0].HostPath != "" {
		t.Fatalf("mysql volumes = %+v, want an ordinary managed volume (no RemoteDirKey/HostPath) unaffected by rootbuilt's root build context", mysql.Volumes)
	}
}

// TestManagedVolumeWithExistingDataGetsSeedCandidate covers the one-time
// seed case: a managed data volume (single-service, not a build-context/
// shared-dir match) whose compose-declared host directory already has
// real content — e.g. mysql-data from a previous plain `docker compose
// up` — carries that path as a seed candidate (SeedDirKey momentarily
// holds the raw path; resolveVolumeSeeds in controlplane converts it to
// a stash key before the workload is ever persisted).
func TestManagedVolumeWithExistingDataGetsSeedCandidate(t *testing.T) {
	workingDir := t.TempDir()
	dataDir := filepath.Join(workingDir, "mysql-data")
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "ibdata1"), []byte("pretend innodb file"), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
services:
  mysql:
    image: mysql:8.0
    volumes:
      - "./mysql-data:/var/lib/mysql"
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	mysql := plan.Workloads[0]
	if len(mysql.Volumes) != 1 || mysql.Volumes[0].SeedDirKey != dataDir {
		t.Fatalf("volumes = %+v, want SeedDirKey = %q", mysql.Volumes, dataDir)
	}
	if mysql.Volumes[0].HostPath != "" {
		t.Fatalf("volumes = %+v, want no literal HostPath (seed is one-time, not a live mount)", mysql.Volumes)
	}
}

// TestManagedVolumeWithEmptyOrMissingDataHasNoSeedCandidate covers the
// common case — no pre-existing local data at all (an empty directory, or
// one that was never created) — which must not produce a seed candidate;
// there's nothing worth tarring and transferring.
func TestManagedVolumeWithEmptyOrMissingDataHasNoSeedCandidate(t *testing.T) {
	workingDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workingDir, "empty-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
services:
  empty:
    image: a:latest
    volumes:
      - "./empty-data:/data"
  missing:
    image: b:latest
    volumes:
      - "./never-created:/data"
`)
	plan, err := compose.Load(context.Background(), "demo", workingDir, yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, w := range plan.Workloads {
		if len(w.Volumes) != 1 || w.Volumes[0].SeedDirKey != "" {
			t.Fatalf("%s: volumes = %+v, want no seed candidate", w.Service, w.Volumes)
		}
	}
}
