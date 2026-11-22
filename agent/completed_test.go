package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/docker/docker/client"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

// depAgent answers /v1/services/<project> with the given instances and
// records every status report.
func depAgent(t *testing.T, instances []model.ServiceInstance) (*Agent, *[]string, *string) {
	t.Helper()
	var reports []string
	var servicesQuery string
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		switch {
		case strings.HasPrefix(req.URL.Path, "/v1/services/"):
			servicesQuery = req.URL.RawQuery
			b, _ := json.Marshal(instances)
			body = string(b)
		case strings.HasSuffix(req.URL.Path, "/v1/agent/status"):
			var r struct{ Key, Status, Reason string }
			data, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(data, &r)
			reports = append(reports, r.Key+"="+r.Status+": "+r.Reason)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	return &Agent{cfg: Config{NodeID: "n", ControlPlaneURL: "http://cp"}, http: &http.Client{Transport: transport}}, &reports, &servicesQuery
}

func depInst(service string, st model.Status) model.ServiceInstance {
	return model.ServiceInstance{Project: "demo", Service: service, NodeID: "n", Address: "10.0.0.1", Status: st}
}

func TestDependentWaitsForTheJobToCompleteNotJustRun(t *testing.T) {
	cases := []struct {
		name      string
		migrate   model.Status
		wantReady bool
		wantFail  string
	}{
		{"still running", model.StatusRunning, false, ""},
		{"ready is not completed", model.StatusReady, false, ""},
		{"completed", model.StatusCompleted, true, ""},
		{"failed", model.StatusFailed, false, "migrate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _, query := depAgent(t, []model.ServiceInstance{depInst("db", model.StatusReady), depInst("migrate", c.migrate)})
			_, ready, failedDep, err := a.resolveDependencies(context.Background(), "demo",
				[]string{"db", "migrate"}, []string{"db"}, []string{"migrate"})
			if err != nil {
				t.Fatal(err)
			}
			if ready != c.wantReady || failedDep != c.wantFail {
				t.Fatalf("ready=%v failedDep=%q, want ready=%v failedDep=%q", ready, failedDep, c.wantReady, c.wantFail)
			}
			if *query != "failed=1" {
				t.Fatalf("query = %q, a completed dependency needs the failed instances listed too", *query)
			}
		})
	}
}

func TestPlainDependencyDoesNotAskForFailedInstances(t *testing.T) {
	a, _, query := depAgent(t, []model.ServiceInstance{depInst("db", model.StatusRunning)})
	_, ready, failedDep, err := a.resolveDependencies(context.Background(), "demo", []string{"db"}, nil, nil)
	if err != nil || !ready || failedDep != "" || *query != "" {
		t.Fatalf("ready=%v failedDep=%q query=%q err=%v", ready, failedDep, *query, err)
	}
}

func TestAJobThatIsNotPlacedYetIsWaitedForNotFailed(t *testing.T) {
	a, _, _ := depAgent(t, nil)
	_, ready, failedDep, err := a.resolveDependencies(context.Background(), "demo", []string{"migrate"}, nil, []string{"migrate"})
	if err != nil || ready || failedDep != "" {
		t.Fatalf("ready=%v failedDep=%q err=%v", ready, failedDep, err)
	}
}

func TestSplitAssignmentsDoesNotRerunACompletedJobButKeepsItsContainer(t *testing.T) {
	done := store.WorkloadRecord{Workload: model.Workload{Project: "demo", Service: "migrate", Revision: "latest"}, Status: model.StatusCompleted, NodeID: "n"}
	run := store.WorkloadRecord{Workload: model.Workload{Project: "demo", Service: "api", Revision: "latest"}, Status: model.StatusRunning, NodeID: "n"}
	owned, executable := splitAssignments([]store.WorkloadRecord{done, run})
	if !owned[done.Key()] {
		t.Fatal("a completed job must stay owned or cleanup deletes its container and logs")
	}
	if len(executable) != 1 || executable[0].Key() != run.Key() {
		t.Fatalf("executable = %v, a completed job must not run again", workloadKeys(executable))
	}
}

// exitedAgent has a Docker daemon whose container for demo/migrate has
// stopped with the given exit code.
func exitedAgent(t *testing.T, exitCode int) (*Agent, *[]string) {
	t.Helper()
	a, reports, _ := depAgent(t, nil)
	dockerTransport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		switch {
		case strings.HasSuffix(req.URL.Path, "/containers/haco-demo-migrate-latest/json"):
			body = `{"Id":"c1","State":{"Running":false,"Restarting":false,"ExitCode":` + strconv.Itoa(exitCode) + `}}`
		case strings.HasSuffix(req.URL.Path, "/containers/c1/logs"):
			body = ""
		default:
			t.Errorf("unexpected docker request %s %s", req.Method, req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	cli, err := client.NewClientWithOpts(client.WithHost("http://docker.test"), client.WithVersion("1.41"), client.WithHTTPClient(&http.Client{Transport: dockerTransport}))
	if err != nil {
		t.Fatal(err)
	}
	a.docker = &Docker{cli: cli, nodeID: "n"}
	return a, reports
}

func jobRecord(status model.Status) store.WorkloadRecord {
	return store.WorkloadRecord{
		Workload: model.Workload{Project: "demo", Service: "migrate", Revision: "latest", Runtime: model.Runtime{Restart: model.RestartPolicy{Name: "no"}}},
		Status:   status, NodeID: "n", ContainerID: "c1",
	}
}

func TestAJobThatExitsZeroIsReportedCompleted(t *testing.T) {
	a, reports := exitedAgent(t, 0)
	a.handle(context.Background(), jobRecord(model.StatusRunning))
	if len(*reports) != 1 || !strings.HasPrefix((*reports)[0], "demo/migrate/latest=completed") {
		t.Fatalf("reports = %v, want completed", *reports)
	}
}

func TestAJobThatExitsNonZeroIsReportedFailed(t *testing.T) {
	a, reports := exitedAgent(t, 3)
	a.handle(context.Background(), jobRecord(model.StatusRunning))
	if len(*reports) != 1 || !strings.HasPrefix((*reports)[0], "demo/migrate/latest=failed") || !strings.Contains((*reports)[0], "code 3") {
		t.Fatalf("reports = %v, want failed with code 3", *reports)
	}
}
