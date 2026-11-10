package compose_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
	"github.com/abhishek/hardware-aware-container-orchestrator/compose"
	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

func loadPlan(t *testing.T, dir, yaml string) *compose.Plan {
	t.Helper()
	plan, err := compose.Load(context.Background(), "demo", dir, []byte(yaml))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return plan
}

func workload(t *testing.T, plan *compose.Plan, service string) model.Workload {
	t.Helper()
	for _, w := range plan.Workloads {
		if w.Service == service {
			return w
		}
	}
	t.Fatalf("no workload %q in plan", service)
	return model.Workload{}
}

func TestPWDIsTheProjectFolder(t *testing.T) {
	dir := t.TempDir()
	plan := loadPlan(t, dir, `
services:
  app:
    image: app
    environment:
      CONFIG: ${PWD}/docker-compose.yml
`)
	if got, want := workload(t, plan, "app").Env["CONFIG"], dir+"/docker-compose.yml"; got != want {
		t.Fatalf("CONFIG = %q, want %q", got, want)
	}
}

func TestWorkingDirIsCarried(t *testing.T) {
	plan := loadPlan(t, t.TempDir(), `
services:
  docs:
    image: antora
    working_dir: /antora
`)
	if got := workload(t, plan, "docs").Runtime.WorkingDir; got != "/antora" {
		t.Fatalf("working dir = %q", got)
	}
	for _, w := range plan.Warnings {
		if strings.Contains(w, "working_dir") {
			t.Fatalf("unexpected warning: %s", w)
		}
	}
}

func TestRestartPolicies(t *testing.T) {
	plan := loadPlan(t, t.TempDir(), `
services:
  a:
    image: a
    restart: unless-stopped
  b:
    image: b
    restart: on-failure:3
  c:
    image: c
    restart: "no"
  d:
    image: d
    deploy:
      restart_policy:
        condition: on-failure
        max_attempts: 5
  e:
    image: e
`)
	cases := map[string]model.RestartPolicy{
		"a": {Name: "unless-stopped"},
		"b": {Name: "on-failure", MaxRetries: 3},
		"c": {Name: "no"},
		"d": {Name: "on-failure", MaxRetries: 5},
		"e": {},
	}
	for svc, want := range cases {
		if got := workload(t, plan, svc).Runtime.Restart; got != want {
			t.Errorf("%s restart = %+v, want %+v", svc, got, want)
		}
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", plan.Warnings)
	}
}

func TestExtraHostsHostGatewayIsPassedThrough(t *testing.T) {
	plan := loadPlan(t, t.TempDir(), `
services:
  events:
    image: events
    extra_hosts:
      - "host.docker.internal:host-gateway"
      - "db.local:10.0.0.5"
`)
	if !plan.Valid() {
		t.Fatalf("rejected: %v", plan.Rejections)
	}
	got := workload(t, plan, "events").Runtime.ExtraHosts
	want := []string{"db.local:10.0.0.5", "host.docker.internal:host-gateway"}
	if !slices.Equal(got, want) {
		t.Fatalf("extra hosts = %v, want %v", got, want)
	}
}

func TestSecretsAndConfigsBecomeFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "db_password.txt"), []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("API_TOKEN", "tok")
	plan := loadPlan(t, dir, `
services:
  metaapi:
    image: metaapi
    secrets:
      - db_password
      - source: api_token
        target: token
    configs:
      - source: app_conf
        target: /etc/app.conf
secrets:
  db_password:
    file: ./db_password.txt
  api_token:
    environment: API_TOKEN
configs:
  app_conf:
    content: "debug=true"
`)
	if !plan.Valid() {
		t.Fatalf("rejected: %v", plan.Rejections)
	}
	wl := workload(t, plan, "metaapi")
	got := map[string]string{}
	for _, v := range wl.Volumes {
		got[v.ContainerPath] = string(v.FileContent)
		if !v.ReadOnly {
			t.Errorf("%s should be read-only", v.ContainerPath)
		}
	}
	want := map[string]string{
		"/run/secrets/db_password": "hunter2",
		"/run/secrets/token":       "tok",
		"/etc/app.conf":            "debug=true",
	}
	for path, content := range want {
		if got[path] != content {
			t.Errorf("%s = %q, want %q", path, got[path], content)
		}
	}
	if wl.Storage.Class != model.Ephemeral {
		t.Errorf("secrets alone should not make the service stateful: %v", wl.Storage.Class)
	}
}

func TestUndefinedSecretFailsToLoad(t *testing.T) {
	_, err := compose.Load(context.Background(), "demo", t.TempDir(), []byte(`
services:
  app:
    image: app
    secrets:
      - source: missing
secrets:
  other:
    content: x
`))
	if err == nil || !strings.Contains(err.Error(), "undefined secret") {
		t.Fatalf("err = %v, want undefined secret", err)
	}
}

func TestSecretFromUnsetEnvironmentIsRejected(t *testing.T) {
	plan := loadPlan(t, t.TempDir(), `
services:
  app:
    image: app
    secrets: [token]
secrets:
  token:
    environment: HACO_TEST_UNSET_VARIABLE
`)
	if plan.Valid() {
		t.Fatal("expected a rejection for an unset secret variable")
	}
}

const socketYAML = `
services:
  v2:
    image: v2
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
  web:
    image: web
`

func TestDockerSocketServiceIsPinnedToHost(t *testing.T) {
	plan := loadPlan(t, t.TempDir(), socketYAML)
	if !plan.Valid() {
		t.Fatalf("rejected: %v", plan.Rejections)
	}
	wl := workload(t, plan, "v2")
	if wl.Storage.PinnedNodeID != agent.HostNodeID() {
		t.Fatalf("storage pin = %q, want control-plane host", wl.Storage.PinnedNodeID)
	}
	if len(wl.Volumes) != 1 || !wl.Volumes[0].DockerSocket || wl.Volumes[0].HostPath != "/var/run/docker.sock" {
		t.Fatalf("volumes = %+v", wl.Volumes)
	}
}

func TestPinOverridesNeverFreeDockerSocketService(t *testing.T) {
	plan := loadPlan(t, t.TempDir(), socketYAML)
	// Pinning only another service frees every unnamed one, except this.
	compose.ApplyHostPinOverride(plan, []string{"web"})
	wl := workload(t, plan, "v2")
	if wl.Storage.PinnedNodeID != agent.HostNodeID() || wl.Volumes[0].HostPath == "" {
		t.Fatalf("socket service was freed: %+v", wl)
	}

	plan = loadPlan(t, t.TempDir(), socketYAML)
	compose.ApplyPinOverride(plan, map[string][]string{"other-node": {"v2"}})
	wl = workload(t, plan, "v2")
	if wl.Constraints.PinnedNodeID != agent.HostNodeID() {
		t.Fatalf("socket service moved to %q", wl.Constraints.PinnedNodeID)
	}
	var warned bool
	for _, w := range plan.Warnings {
		warned = warned || strings.Contains(w, "stays on the control-plane host")
	}
	if !warned {
		t.Fatalf("no warning about the ignored pin: %v", plan.Warnings)
	}
}

func TestUnsupportedKeysAreNamed(t *testing.T) {
	plan := loadPlan(t, t.TempDir(), `
services:
  app:
    image: app
    user: "1000"
    privileged: true
    container_name: my-app
    labels:
      a: b
`)
	var found string
	for _, w := range plan.Warnings {
		if strings.Contains(w, "not supported yet") {
			found = w
		}
	}
	if !strings.Contains(found, "privileged") || !strings.Contains(found, "user") {
		t.Fatalf("warning = %q, want privileged and user named", found)
	}
	if strings.Contains(found, "container_name") || strings.Contains(found, "labels") {
		t.Fatalf("harmless keys should not be named: %q", found)
	}
}

func TestPlainServiceHasNoIgnoredKeyWarning(t *testing.T) {
	plan := loadPlan(t, t.TempDir(), `
services:
  app:
    image: app
    command: ["serve"]
    ports: ["8080:8080"]
    environment:
      A: b
    networks: [default]
    healthcheck:
      test: ["CMD", "true"]
`)
	for _, w := range plan.Warnings {
		if strings.Contains(w, "not supported yet") {
			t.Fatalf("unexpected warning: %s", w)
		}
	}
}

func TestDeployResourceLimitsApply(t *testing.T) {
	plan := loadPlan(t, t.TempDir(), `
services:
  app:
    image: app
    deploy:
      resources:
        limits:
          cpus: "1.5"
          memory: 512M
`)
	wl := workload(t, plan, "app")
	if wl.Resources.CPUMillis != 1500 || wl.Resources.MemoryBytes != 512<<20 {
		t.Fatalf("resources = %+v", wl.Resources)
	}
}
