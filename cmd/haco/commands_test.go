package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
)

func TestParseComposeArgs(t *testing.T) {
	cases := []struct {
		name     string
		cmd      string
		args     []string
		services string
		project  string
		file     string
		legacy   bool
	}{
		{name: "services only", cmd: "deploy", args: []string{"web", "db"}, services: "web,db"},
		{name: "older project and file", cmd: "deploy", args: []string{"myproj", "docker-compose.yml"}, project: "myproj", file: "docker-compose.yml", legacy: true},
		{name: "flags and a service", cmd: "plan", args: []string{"-f", "x.yml", "-p", "demo", "api"}, services: "api", project: "demo", file: "x.yml"},
		{name: "equals form", cmd: "plan", args: []string{"--file=x.yml", "--project=demo"}, project: "demo", file: "x.yml"},
		{name: "repeated --service", cmd: "build", args: []string{"--service", "a", "b"}, services: "a,b"},
		{name: "-f stops the older form", cmd: "deploy", args: []string{"myproj", "-f", "x.yml"}, services: "myproj", file: "x.yml"},
	}
	for _, c := range cases {
		o, err := parseComposeArgs(c.cmd, c.args, "json", "fresh", "no-cache", "yes")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if o.services != c.services || o.project != c.project || o.file != c.file || o.legacy != c.legacy {
			t.Errorf("%s: got services=%q project=%q file=%q legacy=%v", c.name, o.services, o.project, o.file, o.legacy)
		}
	}
}

func TestParseComposeArgsKeepsFlagsAndPins(t *testing.T) {
	o, err := parseComposeArgs("deploy", []string{"--no-cache", "-y", "--fresh", "--pin-host", "db, cache", "--pin-node", "n1=api,web"}, "yes", "no-cache", "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if !o.noCache || !o.yes || !o.fresh {
		t.Fatalf("flags lost: %+v", o)
	}
	if o.pinHost == nil || !reflect.DeepEqual(*o.pinHost, []string{"db", "cache"}) {
		t.Fatalf("pinHost = %v", o.pinHost)
	}
	if !reflect.DeepEqual(o.pinNodes["n1"], []string{"api", "web"}) {
		t.Fatalf("pinNodes = %v", o.pinNodes)
	}
}

func TestParseComposeArgsRejectsUnknownAndUnsupportedFlags(t *testing.T) {
	if _, err := parseComposeArgs("deploy", []string{"--bogus"}, "yes"); err == nil {
		t.Error("unknown flag should fail")
	}
	if _, err := parseComposeArgs("build", []string{"--fresh"}, "no-cache"); err == nil || !strings.Contains(err.Error(), "haco build") {
		t.Errorf("--fresh is not a build flag; err = %v", err)
	}
	if _, err := parseComposeArgs("plan", []string{"-f"}, "json"); err == nil {
		t.Error("a flag missing its value should fail")
	}
}

func TestResolveDefaultsFromTheCurrentFolder(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "My_App-1!")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("COMPOSE_PROJECT_NAME", "")

	project, file, workingDir, _, err := composeOpts{}.resolve("plan")
	if err != nil {
		t.Fatal(err)
	}
	if project != "my_app-1" {
		t.Errorf("project = %q, want the folder name as Compose spells it", project)
	}
	if filepath.Base(file) != "docker-compose.yml" || filepath.Base(workingDir) != "My_App-1!" {
		t.Errorf("file = %q, workingDir = %q", file, workingDir)
	}

	// compose.yaml comes before docker-compose.yml, as in Compose.
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, file, _, _, _ := (composeOpts{}).resolve("plan"); filepath.Base(file) != "compose.yaml" {
		t.Errorf("file = %q, want compose.yaml first", file)
	}

	t.Setenv("COMPOSE_PROJECT_NAME", "fromenv")
	if project, _, _, _, _ := (composeOpts{}).resolve("plan"); project != "fromenv" {
		t.Errorf("project = %q, want COMPOSE_PROJECT_NAME", project)
	}
	if project, _, _, _, _ := (composeOpts{project: "explicit"}).resolve("plan"); project != "explicit" {
		t.Errorf("project = %q, want -p to win", project)
	}
}

func TestResolveWithoutAComposeFileNamesWhatItLookedFor(t *testing.T) {
	t.Chdir(t.TempDir())
	_, _, _, _, err := composeOpts{}.resolve("plan")
	if err == nil || !strings.Contains(err.Error(), "docker-compose.yml") || !strings.Contains(err.Error(), "-f") {
		t.Fatalf("err = %v", err)
	}
}

func TestEveryAliasPointsAtARealCommand(t *testing.T) {
	for old, a := range aliases {
		argv := a.to(nil)
		if len(argv) == 0 || findCommand(argv[0]) == nil {
			t.Errorf("alias %q rewrites to %v, which is not a command", old, argv)
		}
		if findCommand(old) != nil {
			t.Errorf("alias %q is also a real command", old)
		}
	}
}

func TestAliasRewrites(t *testing.T) {
	cases := []struct {
		old  string
		args []string
		want []string
	}{
		{"stop-all", nil, []string{"stop-cluster"}},
		{"teardown", []string{"--yes"}, []string{"cluster", "reset", "--yes"}},
		{"join-token", nil, []string{"node", "join-token"}},
		{"revoke-node", []string{"n1"}, []string{"node", "revoke", "n1"}},
		{"remove-node", []string{"n1", "--force"}, []string{"node", "rm", "n1", "--force"}},
		{"status", []string{"demo", "api"}, []string{"ps", "demo", "api"}},
		{"workloads", nil, []string{"ps"}},
		{"containers", []string{"demo"}, []string{"ps", "demo"}},
		{"stop-containers", nil, []string{"stop", "--all"}},
		{"stop-containers", []string{"demo", "-y"}, []string{"stop", "demo", "--yes"}},
		{"remove-all-workloads", nil, []string{"rm", "--all"}},
		{"remove-all-workloads", []string{"--yes", "demo"}, []string{"rm", "demo", "--yes"}},
		{"remove-workload", []string{"demo", "api"}, []string{"rm", "demo", "api"}},
	}
	for _, c := range cases {
		got := aliases[c.old].to(c.args)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %v -> %v, want %v", c.old, c.args, got, c.want)
		}
	}
}

func TestHelpListsEveryCommandWithShortLines(t *testing.T) {
	var overview bytes.Buffer
	printOverview(&overview)
	for _, c := range commandTable() {
		if c.summary == "" || c.detail == "" || c.run == nil {
			t.Errorf("command %q is missing a summary, detail or handler", c.name)
		}
		if !strings.Contains(overview.String(), "  "+c.name+" ") {
			t.Errorf("overview does not list %q", c.name)
		}
		var detail bytes.Buffer
		if err := printHelp(&detail, []string{c.name}); err != nil {
			t.Errorf("help %s: %v", c.name, err)
		}
		for _, line := range strings.Split(detail.String(), "\n") {
			if len(line) > 100 {
				t.Errorf("help %s has a %d-character line: %q", c.name, len(line), line)
			}
		}
	}
	for _, line := range strings.Split(overview.String(), "\n") {
		if len(line) > 100 {
			t.Errorf("overview has a %d-character line: %q", len(line), line)
		}
	}
	for old := range aliases {
		if strings.Contains(overview.String(), "  "+old+" ") {
			t.Errorf("the overview should not list the old name %q", old)
		}
	}
}

func TestHelpForAnOldNameSaysWhatReplacedIt(t *testing.T) {
	var out bytes.Buffer
	if err := printHelp(&out, []string{"stop-all"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "haco stop-cluster") || !strings.Contains(out.String(), "Usage: haco stop-cluster") {
		t.Fatalf("help = %q", out.String())
	}
	if err := printHelp(&out, []string{"nope"}); err == nil {
		t.Error("an unknown command should fail")
	}
}

// fakeControlPlane records every request and answers from a fixed workload list.
type fakeControlPlane struct {
	mu       sync.Mutex
	requests []string
	server   *httptest.Server
}

func newFakeControlPlane(t *testing.T) *fakeControlPlane {
	t.Helper()
	f := &fakeControlPlane{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		f.mu.Unlock()
		if r.Method == http.MethodGet && r.URL.Path == "/v1/workloads" {
			fmt.Fprint(w, `[
			 {"Project":"demo","Service":"api","Revision":"latest","Status":"ready","NodeID":"n1"},
			 {"Project":"demo","Service":"db","Revision":"latest","Status":"running","NodeID":"n2"},
			 {"Project":"demo","Service":"old","Revision":"latest","Status":"stopped","NodeID":"n1"},
			 {"Project":"other","Service":"web","Revision":"latest","Status":"ready","NodeID":"n1"}]`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeControlPlane) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		// Listing workloads is how every command looks things up; it changes nothing.
		if r != "GET /v1/workloads" && !strings.HasPrefix(r, "GET /v1/workloads?") {
			out = append(out, r)
		}
	}
	return out
}

func TestProjectCommandsActOnTheRightServices(t *testing.T) {
	cases := []struct {
		name string
		run  func(string, []string) error
		args []string
		want []string
	}{
		{"stop one service", stop, []string{"demo", "api"}, []string{"POST /v1/workloads/demo/api/latest/stop"}},
		{"stop a project skips the stopped one", stop, []string{"demo"}, []string{"POST /v1/workloads/demo/api/latest/stop", "POST /v1/workloads/demo/db/latest/stop"}},
		{"stop with the older revision argument", stop, []string{"demo", "api", "latest"}, []string{"POST /v1/workloads/demo/api/latest/stop"}},
		{"stop two services", stop, []string{"demo", "db", "api"}, []string{"POST /v1/workloads/demo/db/latest/stop", "POST /v1/workloads/demo/api/latest/stop"}},
		{"stop everything", stop, []string{"--all", "--yes"}, []string{"POST /v1/workloads/demo/api/latest/stop", "POST /v1/workloads/demo/db/latest/stop", "POST /v1/workloads/other/web/latest/stop"}},
		{"resume starts only the stopped", resume, []string{"demo"}, []string{"POST /v1/workloads/demo/old/latest/start"}},
		{"restart skips the stopped", restart, []string{"demo"}, []string{"POST /v1/workloads/demo/api/latest/restart", "POST /v1/workloads/demo/db/latest/restart"}},
		{"rm one service", rm, []string{"demo", "api"}, []string{"DELETE /v1/workloads/demo/api/latest"}},
		{"rm with the older revision argument", rm, []string{"demo", "api", "latest"}, []string{"DELETE /v1/workloads/demo/api/latest"}},
		{"rm a project", rm, []string{"demo", "--yes"}, []string{"DELETE /v1/workloads?project=demo"}},
		{"rm everything", rm, []string{"--all", "--yes"}, []string{"DELETE /v1/workloads"}},
		{"node revoke", node, []string{"revoke", "n1"}, []string{"POST /v1/admin/nodes/n1/revoke"}},
		{"node cordon", node, []string{"cordon", "n1"}, []string{"POST /v1/admin/nodes/n1/cordon"}},
		{"node rm force", node, []string{"rm", "n1", "--force"}, []string{"DELETE /v1/admin/nodes/n1?force=true"}},
		{"ps detail", ps, []string{"demo", "api"}, []string{"GET /v1/workloads/demo/api/latest"}},
	}
	for _, c := range cases {
		f := newFakeControlPlane(t)
		if err := c.run(f.server.URL, c.args); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := f.calls(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %v\nwant %v", c.name, got, c.want)
		}
	}
}

func TestProjectCommandsRejectBadInput(t *testing.T) {
	f := newFakeControlPlane(t)
	for name, err := range map[string]error{
		"stop unknown service": stop(f.server.URL, []string{"demo", "nope"}),
		"stop unknown project": stop(f.server.URL, []string{"ghost"}),
		"stop no arguments":    stop(f.server.URL, nil),
		"stop --all + project": stop(f.server.URL, []string{"--all", "demo", "--yes"}),
		"rm unknown flag":      rm(f.server.URL, []string{"demo", "--bogus"}),
		"resume no arguments":  resume(f.server.URL, nil),
		"node revoke no id":    node(f.server.URL, []string{"revoke"}),
		"node unknown command": node(f.server.URL, []string{"frobnicate", "n1"}),
		"cluster unknown":      cluster(f.server.URL, []string{"nope"}),
	} {
		if err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if got := f.calls(); len(got) != 0 {
		t.Errorf("bad input must not change anything, but sent %v", got)
	}
}

func TestClusterRefreshRevokesEveryNodeAndDropsHostCredential(t *testing.T) {
	own := agent.HostNodeID()
	var mu sync.Mutex
	var revoked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/nodes":
			fmt.Fprintf(w, `[{"id":%q},{"id":"worker-1"}]`, own)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/revoke"):
			mu.Lock()
			revoked = append(revoked, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/admin/nodes/"), "/revoke"))
			mu.Unlock()
			fmt.Fprint(w, `{}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	hostCred := filepath.Join(dir, own+".json")
	if err := os.WriteFile(hostCred, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stopped := false
	if err := refreshCluster(server.URL, []string{"--yes"}, dir, func([]string) error { stopped = true; return nil }); err != nil {
		t.Fatal(err)
	}
	sort.Strings(revoked)
	want := []string{"worker-1", own}
	sort.Strings(want)
	if !slices.Equal(revoked, want) {
		t.Fatalf("revoked %v, want %v (the host's own node too)", revoked, want)
	}
	if _, err := os.Stat(hostCred); !os.IsNotExist(err) {
		t.Fatalf("host credential file should be removed, stat err = %v", err)
	}
	if !stopped {
		t.Fatal("control plane should be stopped so `haco start` re-detects the address")
	}
}

func TestOtherNodeReasonsExplainEveryUnusedNode(t *testing.T) {
	var v planView
	err := json.Unmarshal([]byte(`{"decisions":[{"WorkloadKey":"p/rabbitmq/latest","Selected":"mac-b",
		"Scores":[{"NodeID":"mac-b","Total":0.8},{"NodeID":"mac-c","Total":0.6}],
		"Rejections":[{"NodeID":"host","Reason":"host port 5672 is in use on this node by container x (not managed by haco)"},{"NodeID":"hp","Reason":"node hp is unavailable"}]}]}`), &v)
	if err != nil {
		t.Fatal(err)
	}
	d := v.Decisions[0]
	got := strings.Join(otherNodeReasons(d.Selected, d.Scores, d.Rejections), "\n")
	for _, want := range []string{"host ruled out: host port 5672 is in use", "hp ruled out: node hp is unavailable", "mac-c eligible but scored lower (0.60 vs 0.80)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "mac-b") {
		t.Errorf("the chosen node must not be listed as unused:\n%s", got)
	}
	if otherNodeReasons("", d.Scores, d.Rejections) != nil {
		t.Error("an unplaced service gets no extra notes")
	}
}
