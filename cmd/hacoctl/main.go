// Command hacoctl is a thin CLI over the control-plane HTTP API, so using
// the platform doesn't require hand-assembling JSON with curl. It has no
// state of its own — every subcommand is a single HTTP call.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
	"github.com/abhishek/hardware-aware-container-orchestrator/agentjoin"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	url := envOr("HACO_CONTROLPLANE_URL", "http://localhost:8099")

	var err error
	switch os.Args[1] {
	case "deploy":
		err = deploy(url, os.Args[2:])
	case "plan":
		err = plan(url, os.Args[2:])
	case "build":
		err = buildCmd(url, os.Args[2:])
	case "nodes":
		err = nodes(url)
	case "node":
		err = node(url, os.Args[2:])
	case "workloads":
		err = workloads(url, os.Args[2:])
	case "projects":
		err = projects(url, os.Args[2:])
	case "tui":
		err = runTUI(url, os.Args[2:])
	case "containers":
		err = containers(url, os.Args[2:])
	case "services":
		err = services(url, os.Args[2:])
	case "status":
		err = status(url, os.Args[2:])
	case "logs":
		err = logs(url, os.Args[2:])
	case "stop":
		err = stop(url, os.Args[2:])
	case "remove-workload":
		err = removeWorkload(url, os.Args[2:])
	case "remove-all-workloads":
		err = removeAllWorkloads(url, os.Args[2:])
	case "events":
		err = getOptionalProject(url, "/v1/events", os.Args[2:])
	case "metrics":
		err = getOptionalProject(url, "/v1/metrics", os.Args[2:])
	case "join-token":
		err = printJoinCommand(url)
	case "revoke-node":
		if len(os.Args) != 3 {
			err = fmt.Errorf("usage: hacoctl revoke-node <node-id>")
		} else {
			err = post(url, "/v1/admin/nodes/"+neturl.PathEscape(os.Args[2])+"/revoke", []byte(`{}`))
		}
	case "stop-containers":
		err = stopContainers(url, os.Args[2:])
	case "teardown":
		err = teardown(url, os.Args[2:])
	case "start":
		err = start(url, os.Args[2:])
	case "stop-all":
		err = stopAll(os.Args[2:])
	case "agent":
		err = agentCmd(os.Args[2:])
	case "remove-node":
		err = removeNode(url, os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `hacoctl: a thin CLI over the control-plane API (HACO_CONTROLPLANE_URL, default http://localhost:8099)

Usage:
  hacoctl build  <project> <docker-compose.yml>   just build the build: service images, no scheduling or containers (--no-cache forces a full rebuild; --service <name> scopes to that service's dependencies; --pin-host <svc,...> below)
  hacoctl deploy <project> <docker-compose.yml>   build, schedule, and run a Compose project, then show a live status table until every workload is ready/failed/stopped (like 'docker compose up -d'); prompts if a pinned service needs a fresh volume (--yes skips it); --no-cache forces a full rebuild; --service <name> deploys only that service and its dependencies; --pin-host <svc,...> below
  hacoctl plan   <project> <docker-compose.yml>   dry-run the same, no side effects (table; add --json for raw; --service <name> to scope the same way; --pin-host <svc,...> below)
                                                   --pin-host <service,...>: override storage affinity for this call only, no Compose file changes needed — only the named services (e.g. a database, and whichever service you're actively live-editing) pin to this control-plane host; every other service schedules anywhere, even one that bind-mounts its own source (overrides compose.go's own auto-pin heuristic)
  hacoctl status [project] [service] [revision]   table of every workload (optionally filtered to one project), or one workload's full detail once service is named (revision defaults to "latest")
  hacoctl logs   <project> <service> <revision>   latest captured container log tail
  hacoctl stop   <project> <service> <revision>   stop the workload and remove its container
  hacoctl remove-workload <project> <service> [revision]   hard-delete that workload's record entirely (revision defaults to "latest"); distinct from stop, which keeps a stopped row
  hacoctl remove-all-workloads [project]          hard-delete every workload record, optionally scoped to one project (--yes skips confirmation)
  hacoctl nodes                                   list registered nodes
  hacoctl node <id>                               one node's full detail
  hacoctl node cordon <id>                        block new workloads from scheduling onto this node (existing ones keep running)
  hacoctl node uncordon <id>                      make this node eligible for new workloads again
  hacoctl workloads [project]                     table of every workload, optionally filtered to one project
  hacoctl projects                                list distinct projects with a quick workload-status breakdown
  hacoctl tui [project]                           read-only auto-refreshing terminal dashboard (nodes/workloads/containers/events/metrics/services/projects tabs)
  hacoctl containers [project]                    node/workload/container join view, optionally filtered to one project
  hacoctl services <project> [service]            resolve a logical service name to its current endpoints
  hacoctl events [project]                        recent audit events, optionally filtered to one project
  hacoctl metrics [project]                       node/workload counts, optionally filtered to one project's workloads
  hacoctl join-token                              print a fresh one-time agent join command
  hacoctl revoke-node <node-id>                   revoke that agent credential (node stays listed, permanently unavailable)
  hacoctl remove-node <node-id> [--force]         hard-delete that node record and its credential (refused if it still has a non-terminal workload assigned, unless --force)
  hacoctl stop-containers [project]               stop every workload's container, optionally filtered to one project (nodes stay joined; --yes skips confirmation)
  hacoctl teardown                                stop every workload and revoke every node credential (--yes skips confirmation)
  hacoctl start                                   start the control plane in the background, with the local build registry on by default (--foreground to run attached, --no-registry to disable the registry, --logs to tail its log right after starting, --tui to launch the dashboard once ready instead — not combinable with each other or --foreground)
  hacoctl stop-all                                stop the control plane process started by 'hacoctl start'
  hacoctl agent join <token>                      join this machine as a worker node (one-time token from 'hacoctl start')
  hacoctl agent run                                run this machine's already-joined node (reuses the saved credential, no token needed)`)
}

func printJoinCommand(baseURL string) error {
	resp, err := http.Post(baseURL+"/v1/admin/join-token", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("control plane returned %s", resp.Status)
	}
	var bundle agentjoin.Bundle
	if err := json.NewDecoder(resp.Body).Decode(&bundle); err != nil {
		return err
	}
	encoded, err := agentjoin.Encode(bundle)
	if err != nil {
		return err
	}
	fmt.Printf("hacoctl agent join '%s'\n", encoded)
	return nil
}

// plan calls /v1/compose/plan and renders a human-readable table by
// default (raw JSON is for the API/future UI, not the terminal); pass
// --json to get the same raw response hacoctl used to always print.
func plan(baseURL string, args []string) error {
	jsonOut := false
	onlyService := ""
	var pinHost *[]string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--service":
			i++
			if i >= len(args) {
				return fmt.Errorf("--service requires a value")
			}
			onlyService = args[i]
		case "--pin-host":
			i++
			if i >= len(args) {
				return fmt.Errorf("--pin-host requires a value")
			}
			list := splitPinHost(args[i])
			pinHost = &list
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) != 2 {
		return fmt.Errorf("usage: hacoctl plan <project> <docker-compose.yml> [--json] [--service <name>] [--pin-host <service,...>]")
	}
	project, file := rest[0], rest[1]

	yaml, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	workingDir, err := filepath.Abs(filepath.Dir(file))
	if err != nil {
		return err
	}
	if jsonOut {
		body, _ := json.Marshal(map[string]any{
			"project":      project,
			"working_dir":  workingDir,
			"yaml":         string(yaml),
			"only_service": onlyService,
			"pin_host":     pinHost,
		})
		return post(baseURL, "/v1/compose/plan", body)
	}

	out, err := fetchPlan(baseURL, project, workingDir, yaml, onlyService, pinHost)
	if err != nil {
		return err
	}
	renderPlanTable(project, out)
	if len(out.Plan.Rejections) > 0 {
		return fmt.Errorf("plan is invalid (%d rejection(s))", len(out.Plan.Rejections))
	}
	return nil
}

// splitPinHost parses --pin-host's comma-separated service list, trimming
// whitespace and dropping empty entries (so "a, b," and "" both behave
// sensibly — the latter means "pin nothing, schedule everything anywhere").
func splitPinHost(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

type planView struct {
	Plan struct {
		Workloads []struct {
			Service   string `json:"Service"`
			Image     string `json:"Image"`
			Resources struct {
				CPUMillis   int64 `json:"CPUMillis"`
				MemoryBytes int64 `json:"MemoryBytes"`
			} `json:"Resources"`
			Storage struct {
				Class        string `json:"Class"`
				PinnedNodeID string `json:"PinnedNodeID"`
			} `json:"Storage"`
			Constraints struct {
				HostPorts    []int  `json:"HostPorts"`
				PinnedNodeID string `json:"PinnedNodeID"`
			} `json:"Constraints"`
		} `json:"workloads"`
		Warnings   []string `json:"warnings"`
		Rejections []string `json:"rejections"`
	} `json:"plan"`
	Decisions []struct {
		WorkloadKey string   `json:"WorkloadKey"`
		Selected    string   `json:"Selected"`
		Warnings    []string `json:"Warnings"`
		Scores      []struct {
			NodeID string  `json:"NodeID"`
			Total  float64 `json:"Total"`
		} `json:"Scores"`
		Rejections []struct {
			NodeID string `json:"NodeID"`
			Reason string `json:"Reason"`
		} `json:"Rejections"`
	} `json:"decisions"`
}

// fetchPlan POSTs to /v1/compose/plan and returns the decoded response.
// Shared by `plan` (display) and `deploy` (fresh-volume consent check).
func fetchPlan(baseURL, project, workingDir string, yaml []byte, onlyService string, pinHost *[]string) (planView, error) {
	body, _ := json.Marshal(map[string]any{
		"project":      project,
		"working_dir":  workingDir,
		"yaml":         string(yaml),
		"only_service": onlyService,
		"pin_host":     pinHost,
	})
	resp, err := http.Post(baseURL+"/v1/compose/plan", "application/json", bytes.NewReader(body))
	if err != nil {
		return planView{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return planView{}, err
	}
	if resp.StatusCode == http.StatusUnprocessableEntity {
		// An invalid plan (rejections) is an expected, renderable outcome,
		// not a hard failure — decode it like a 200 so renderPlanTable can
		// show the rejections instead of this dumping raw JSON.
		var out planView
		if err := json.Unmarshal(data, &out); err == nil {
			return out, nil
		}
	}
	if resp.StatusCode >= 300 {
		var errBody struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &errBody) == nil && errBody.Error != "" {
			return planView{}, fmt.Errorf("%s", errBody.Error)
		}
		return planView{}, fmt.Errorf("control plane returned %s: %s", resp.Status, string(data))
	}
	var out planView
	if err := json.Unmarshal(data, &out); err != nil {
		return planView{}, fmt.Errorf("decode plan response: %w", err)
	}
	return out, nil
}

// freshVolumeWarnings collects every decision warning that flags a pinned
// workload being rescheduled onto a brand-new, empty volume (the scheduler
// emits these — see scheduler.Schedule), keyed by the service name so the
// consent prompt can name exactly which service is affected.
func freshVolumeWarnings(v planView) map[string][]string {
	byKey := map[string]string{}
	for _, wl := range v.Plan.Workloads {
		byKey[wl.Service] = wl.Service
	}
	out := map[string][]string{}
	for _, d := range v.Decisions {
		if len(d.Warnings) == 0 {
			continue
		}
		parts := strings.Split(d.WorkloadKey, "/")
		service := d.WorkloadKey
		if len(parts) >= 2 {
			service = parts[1]
		}
		for _, w := range d.Warnings {
			if strings.Contains(w, "fresh volume") {
				out[service] = append(out[service], w)
			}
		}
	}
	return out
}

// buildCmd just builds the image(s) a Compose file's build: services need
// — no scheduling, no containers started — for pre-warming an image or
// checking a build succeeds independent of being ready to deploy.
// --no-cache forces a full rebuild even if nothing in the build context
// looks different to Docker's cache; --service scopes to one service's
// dependency closure the same way deploy/plan do, though only the
// build: services within that closure actually get built.
func buildCmd(baseURL string, args []string) error {
	noCache := false
	onlyService := ""
	var pinHost *[]string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-cache":
			noCache = true
		case "--service":
			i++
			if i >= len(args) {
				return fmt.Errorf("--service requires a value")
			}
			onlyService = args[i]
		case "--pin-host":
			i++
			if i >= len(args) {
				return fmt.Errorf("--pin-host requires a value")
			}
			list := splitPinHost(args[i])
			pinHost = &list
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) != 2 {
		return fmt.Errorf("usage: hacoctl build <project> <docker-compose.yml> [--no-cache] [--service <name>] [--pin-host <service,...>]")
	}
	project, file := rest[0], rest[1]

	yaml, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	workingDir, err := filepath.Abs(filepath.Dir(file))
	if err != nil {
		return err
	}

	body, _ := json.Marshal(map[string]any{
		"project":      project,
		"working_dir":  workingDir,
		"yaml":         string(yaml),
		"no_cache":     noCache,
		"only_service": onlyService,
		"pin_host":     pinHost,
	})
	return post(baseURL, "/v1/compose/build", body)
}

// deploy dry-run plans first so it can ask for explicit consent before a
// pinned workload with x-haco-allow-fresh-volume gets rescheduled onto a
// brand-new, empty volume — that's a data-loss trade-off the user opted a
// service into ahead of time, but it still deserves a live confirmation
// at the moment it's actually about to happen, not just silent action.
func deploy(baseURL string, args []string) error {
	yes, noCache := false, false
	onlyService := ""
	var pinHost *[]string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--yes", "-y":
			yes = true
		case "--no-cache":
			noCache = true
		case "--service":
			i++
			if i >= len(args) {
				return fmt.Errorf("--service requires a value")
			}
			onlyService = args[i]
		case "--pin-host":
			i++
			if i >= len(args) {
				return fmt.Errorf("--pin-host requires a value")
			}
			list := splitPinHost(args[i])
			pinHost = &list
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) != 2 {
		return fmt.Errorf("usage: hacoctl deploy <project> <docker-compose.yml> [--yes] [--no-cache] [--service <name>] [--pin-host <service,...>]")
	}
	project, file := rest[0], rest[1]

	yaml, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	workingDir, err := filepath.Abs(filepath.Dir(file))
	if err != nil {
		return err
	}

	if planned, err := fetchPlan(baseURL, project, workingDir, yaml, onlyService, pinHost); err == nil {
		if warnings := freshVolumeWarnings(planned); len(warnings) > 0 {
			fmt.Println("WARNING: the following pinned services will get a brand-new, empty volume — data on their current node will NOT be migrated:")
			for service, msgs := range warnings {
				for _, m := range msgs {
					fmt.Printf("  - %s: %s\n", service, m)
				}
			}
			if !yes && !confirm("Continue and accept the data loss?") {
				return fmt.Errorf("deploy aborted: fresh-volume consent declined")
			}
		}
	}
	// A plan fetch failure here (e.g. nothing schedulable yet) isn't fatal
	// to the consent check — deploy below will surface the real error.

	fmt.Printf("Connecting to control plane at %s...\n", baseURL)
	body, _ := json.Marshal(map[string]any{
		"project":      project,
		"working_dir":  workingDir,
		"yaml":         string(yaml),
		"no_cache":     noCache,
		"only_service": onlyService,
		"pin_host":     pinHost,
	})
	return deployAndWatch(baseURL, project, body)
}

// deployResponse is the shape of POST /v1/compose/deploy's body, enough to
// pull out which workloads it actually submitted — the live-status table
// below follows those (and only those), the same set `--service` scoped.
type deployResponse struct {
	Results []struct {
		Workload *struct {
			Project  string `json:"Project"`
			Service  string `json:"Service"`
			Revision string `json:"Revision"`
		} `json:"Workload"`
	} `json:"results"`
}

// deployAndWatch submits the deploy, then — like `docker compose up -d` —
// switches into a live, in-place-redrawn table of each submitted
// workload's status until every one reaches a terminal state (ready,
// failed, stopped) or the watch times out. Falls back to the old raw-JSON
// dump if the response doesn't parse as expected or submitted nothing.
func deployAndWatch(baseURL, project string, body []byte) error {
	resp, err := http.Post(baseURL+"/v1/compose/deploy", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 300 {
		var pretty bytes.Buffer
		if json.Indent(&pretty, data, "", "  ") == nil {
			fmt.Println(pretty.String())
		} else {
			fmt.Println(string(data))
		}
		return fmt.Errorf("control plane returned %s", resp.Status)
	}

	var dr deployResponse
	var keys []string
	if json.Unmarshal(data, &dr) == nil {
		for _, r := range dr.Results {
			if r.Workload != nil {
				keys = append(keys, r.Workload.Project+"/"+r.Workload.Service+"/"+r.Workload.Revision)
			}
		}
	}
	if len(keys) == 0 {
		var pretty bytes.Buffer
		if json.Indent(&pretty, data, "", "  ") == nil {
			fmt.Println(pretty.String())
		} else {
			fmt.Println(string(data))
		}
		return nil
	}
	fmt.Println("Connected. Deploy submitted; watching workload status...")
	return watchDeploy(baseURL, project, keys)
}

// terminalStatuses are the states watchDeploy stops waiting on — once
// every submitted workload reaches one of these, the deploy is done
// (successfully or not); anything else (assigned, pulling, building,
// starting, running, waiting_dependencies) is still in progress.
var terminalStatuses = map[string]bool{"ready": true, "failed": true, "stopped": true}

const (
	ansiReset  = "\033[0m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiGray   = "\033[90m"
)

// colorStatus wraps status in an ANSI color matching its meaning: green
// once it's actually running, red once it's failed, gray once it's a
// deliberate stop, yellow for everything still in progress in between.
func colorStatus(status string) string {
	switch status {
	case "ready", "running":
		return ansiGreen + status + ansiReset
	case "failed":
		return ansiRed + status + ansiReset
	case "stopped":
		return ansiGray + status + ansiReset
	default:
		return ansiYellow + status + ansiReset
	}
}

func watchDeploy(baseURL, project string, keys []string) error {
	sort.Strings(keys)
	deadline := time.Now().Add(2 * time.Minute)
	lastLines := 0
	announcedNodes := map[string]bool{}

	serviceW := len("SERVICE")
	for _, key := range keys {
		if s := len(strings.SplitN(key, "/", 3)[1]); s > serviceW {
			serviceW = s
		}
	}

	for {
		var all []workloadRecordView
		if err := fetchJSON(baseURL, "/v1/workloads?project="+neturl.QueryEscape(project), &all); err != nil {
			return err
		}
		byKey := make(map[string]workloadRecordView, len(all))
		for _, w := range all {
			byKey[w.Project+"/"+w.Service+"/"+w.Revision] = w
		}

		// New nodes discovered this tick are announced as their own
		// scrolling log lines (like "connecting"/"connected" output),
		// never erased by the table redraw below — so skip erasing the
		// previous frame this one time; it stays as history above.
		var newNodes []string
		for _, key := range keys {
			if w, ok := byKey[key]; ok && w.NodeID != "" && !announcedNodes[w.NodeID] {
				announcedNodes[w.NodeID] = true
				newNodes = append(newNodes, w.NodeID)
			}
		}
		for _, n := range newNodes {
			fmt.Printf("Connecting to node %s... connected.\n", n)
		}
		if len(newNodes) > 0 {
			lastLines = 0
		}

		// Columns are padded manually (not via tabwriter) on the PLAIN
		// text width before coloring, since tabwriter has no notion of
		// ANSI escape bytes and would misalign anything after a colored
		// cell.
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "%-*s  %-13s  %-21s  %s\n", serviceW, "SERVICE", "NODE", "STATUS", "REASON")
		done := true
		for _, key := range keys {
			service := strings.SplitN(key, "/", 3)[1]
			node, status, reason := "-", "pending", "-"
			if w, ok := byKey[key]; ok {
				node, status, reason = orDash(w.NodeID), w.Status, orDash(w.StatusReason)
			}
			// Color is matched against the raw status, then padding is
			// appended after the reset code — coloring the already-padded
			// string would never match (trailing spaces break the switch).
			pad := 21 - len(status)
			if pad < 0 {
				pad = 0
			}
			coloredStatus := colorStatus(status) + strings.Repeat(" ", pad)
			fmt.Fprintf(&buf, "%-*s  %-13s  %s  %s\n", serviceW, service, node, coloredStatus, reason)
			if !terminalStatuses[status] {
				done = false
			}
		}

		if lastLines > 0 {
			fmt.Printf("\033[%dA\033[J", lastLines)
		}
		fmt.Print(buf.String())
		lastLines = strings.Count(buf.String(), "\n")

		if done {
			return nil
		}
		if time.Now().After(deadline) {
			fmt.Println("(timed out waiting for all workloads to reach a terminal state; check `hacoctl workloads` for current status)")
			return nil
		}
		time.Sleep(time.Second)
	}
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	var line string
	fmt.Fscanln(os.Stdin, &line)
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func renderPlanTable(project string, v planView) {
	decisionByKey := map[string]int{}
	for i, d := range v.Decisions {
		decisionByKey[d.WorkloadKey] = i
	}

	fmt.Printf("plan: %s (%d service(s))\n\n", project, len(v.Plan.Workloads))

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tIMAGE\tCPU\tMEM\tSTORAGE\tPORTS\tNODE\tWHY")
	for _, wl := range v.Plan.Workloads {
		storage := wl.Storage.Class
		if wl.Storage.PinnedNodeID != "" {
			storage = fmt.Sprintf("%s (pinned: %s)", storage, wl.Storage.PinnedNodeID)
		}
		if wl.Constraints.PinnedNodeID != "" {
			storage = fmt.Sprintf("%s (host-pinned: %s)", storage, wl.Constraints.PinnedNodeID)
		}
		ports := make([]string, len(wl.Constraints.HostPorts))
		for i, p := range wl.Constraints.HostPorts {
			ports[i] = strconv.Itoa(p)
		}

		node, why := "-", "-"
		key := project + "/" + wl.Service + "/latest"
		if idx, ok := decisionByKey[key]; ok {
			d := v.Decisions[idx]
			if d.Selected != "" {
				node = d.Selected
				if wl.Storage.PinnedNodeID != "" || wl.Constraints.PinnedNodeID != "" {
					why = "pinned"
				} else if len(d.Scores) > 0 {
					sort.Slice(d.Scores, func(i, j int) bool { return d.Scores[i].Total > d.Scores[j].Total })
					why = fmt.Sprintf("score %.2f", d.Scores[0].Total)
				}
			} else if len(d.Rejections) > 0 {
				node = "UNSCHEDULABLE"
				reasons := make([]string, len(d.Rejections))
				for i, r := range d.Rejections {
					reasons[i] = fmt.Sprintf("%s: %s", r.NodeID, r.Reason)
				}
				why = strings.Join(reasons, "; ")
			}
		}

		image := wl.Image
		if image == "" {
			image = "(built)"
		}
		cpu := fmt.Sprintf("%dm", wl.Resources.CPUMillis)
		mem := fmt.Sprintf("%dMi", wl.Resources.MemoryBytes/1024/1024)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			wl.Service, image, cpu, mem, storage, strings.Join(ports, ","), node, why)
	}
	tw.Flush()

	if len(v.Plan.Warnings) > 0 {
		fmt.Println("\nwarnings:")
		for _, w := range v.Plan.Warnings {
			fmt.Println("  -", w)
		}
	}
	if len(v.Plan.Rejections) > 0 {
		fmt.Println("\nrejections:")
		for _, r := range v.Plan.Rejections {
			fmt.Println("  -", r)
		}
	}
}

type workloadRecordView struct {
	Project        string `json:"Project"`
	Service        string `json:"Service"`
	Revision       string `json:"Revision"`
	DesiredState   string `json:"DesiredState"`
	Status         string `json:"Status"`
	StatusReason   string `json:"StatusReason"`
	NodeID         string `json:"NodeID"`
	ContainerID    string `json:"ContainerID"`
	ContainerPorts []int  `json:"ContainerPorts"`
	Constraints    struct {
		HostPorts []int `json:"HostPorts"`
	} `json:"Constraints"`
}

func fetchJSON(baseURL, path string, out any) error {
	resp, err := http.Get(baseURL + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("control plane returned %s: %s", resp.Status, string(data))
	}
	return json.Unmarshal(data, out)
}

// workloads lists every workload, optionally filtered to one project, as a
// table (SERVICE/REVISION/NODE/STATUS/REASON) — raw per-workload detail is
// still available via `hacoctl status`.
func workloads(baseURL string, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: hacoctl workloads [project]")
	}
	path := "/v1/workloads"
	if len(args) == 1 {
		path += "?project=" + neturl.QueryEscape(args[0])
	}
	var out []workloadRecordView
	if err := fetchJSON(baseURL, path, &out); err != nil {
		return err
	}
	if len(out) == 0 {
		fmt.Println("no workloads")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tSERVICE\tREVISION\tNODE\tSTATUS\tREASON")
	for _, w := range out {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", w.Project, w.Service, w.Revision, orDash(w.NodeID), w.Status, orDash(w.StatusReason))
	}
	tw.Flush()
	return nil
}

type projectSummaryView struct {
	Project  string         `json:"Project"`
	Total    int            `json:"Total"`
	ByStatus map[string]int `json:"ByStatus"`
}

// projects lists every distinct project (derived from existing workload
// rows — there's no dedicated projects table) with a quick status
// breakdown, so you don't have to run `hacoctl workloads` with no filter
// and eyeball the PROJECT column.
func projects(baseURL string, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: hacoctl projects")
	}
	var out []projectSummaryView
	if err := fetchJSON(baseURL, "/v1/projects", &out); err != nil {
		return err
	}
	if len(out) == 0 {
		fmt.Println("no projects")
		return nil
	}
	tw2 := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw2, "PROJECT\tWORKLOADS\tREADY\tFAILED\tSTOPPED\tOTHER")
	for _, p := range out {
		ready, failed, stopped := p.ByStatus["ready"], p.ByStatus["failed"], p.ByStatus["stopped"]
		other := p.Total - ready - failed - stopped
		fmt.Fprintf(tw2, "%s\t%d\t%d\t%d\t%d\t%d\n", p.Project, p.Total, ready, failed, stopped, other)
	}
	tw2.Flush()
	return nil
}

// containers is a join view over workloads + nodes: which container is
// actually running where, derived entirely from existing data (no new
// storage). Stopped/failed workloads have no live container, so they're
// filtered out here — hacoctl workloads keeps the full history including
// those.
func containers(baseURL string, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: hacoctl containers [project]")
	}
	path := "/v1/workloads"
	if len(args) == 1 {
		path += "?project=" + neturl.QueryEscape(args[0])
	}
	var all []workloadRecordView
	if err := fetchJSON(baseURL, path, &all); err != nil {
		return err
	}
	w := make([]workloadRecordView, 0, len(all))
	for _, rec := range all {
		if rec.Status == "stopped" || rec.Status == "failed" {
			continue
		}
		w = append(w, rec)
	}
	if len(w) == 0 {
		fmt.Println("no containers")
		return nil
	}
	sort.Slice(w, func(i, j int) bool {
		if w[i].NodeID != w[j].NodeID {
			return w[i].NodeID < w[j].NodeID
		}
		return w[i].Project+w[i].Service < w[j].Project+w[j].Service
	})
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tPROJECT\tSERVICE\tCONTAINER ID\tSTATUS\tPORTS\tINTERNAL URL")
	for _, rec := range w {
		cid := rec.ContainerID
		if len(cid) > 12 {
			cid = cid[:12]
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			orDash(rec.NodeID), rec.Project, rec.Service, orDash(cid), rec.Status,
			intListOrDash(rec.Constraints.HostPorts), internalURLs(rec.Service, rec.ContainerPorts))
	}
	tw.Flush()
	return nil
}

// intListOrDash renders a list of ports as "9090,9091", or "-" when empty
// (an internal-only service with no published host port).
func intListOrDash(ports []int) string {
	if len(ports) == 0 {
		return "-"
	}
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ",")
}

// internalURLs renders a workload's service-name-based, container-internal
// addresses ("cache:6380") — the same host:port other services reach it at
// via extra_hosts-based discovery, whether or not any port is published.
func internalURLs(service string, containerPorts []int) string {
	if len(containerPorts) == 0 {
		return "-"
	}
	parts := make([]string, len(containerPorts))
	for i, p := range containerPorts {
		parts[i] = fmt.Sprintf("%s:%d", service, p)
	}
	return strings.Join(parts, ",")
}

type nodeView struct {
	ID           string            `json:"ID"`
	Address      string            `json:"Address"`
	Architecture string            `json:"Architecture"`
	Labels       map[string]string `json:"Labels"`
	Allocatable  struct {
		CPUMillis   int64 `json:"CPUMillis"`
		MemoryBytes int64 `json:"MemoryBytes"`
	} `json:"Allocatable"`
	Availability string    `json:"Availability"`
	HeartbeatAt  time.Time `json:"HeartbeatAt"`
	Thermal      struct {
		Known bool `json:"Known"`
	} `json:"Thermal"`
	CachedImages []string `json:"CachedImages"`
}

// nodes prints a table of the main per-node facts — the full detail
// (labels, cached images) stays on `hacoctl node <id>`.
func nodes(baseURL string) error {
	var out []nodeView
	if err := fetchJSON(baseURL, "/v1/nodes", &out); err != nil {
		return err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tSTATUS\tADDRESS\tARCH\tCPU (m)\tMEMORY (Mi)\tHEARTBEAT")
	for _, n := range out {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n",
			n.ID, n.Availability, orDash(n.Address), orDash(n.Architecture),
			n.Allocatable.CPUMillis, n.Allocatable.MemoryBytes/1024/1024,
			n.HeartbeatAt.Format(time.RFC3339))
	}
	tw.Flush()
	return nil
}

// nodeDetail prints one node's full detail; there's no dedicated
// single-node endpoint yet, so this filters the existing node list.
// node dispatches `hacoctl node <id>` (detail) and the cordon/uncordon
// subcommands — kept under one verb since they all operate on "a node",
// the same way `hacoctl node <id>` already reads as "this node".
func node(baseURL string, args []string) error {
	switch {
	case len(args) == 2 && args[0] == "cordon":
		return post(baseURL, "/v1/admin/nodes/"+neturl.PathEscape(args[1])+"/cordon", []byte(`{}`))
	case len(args) == 2 && args[0] == "uncordon":
		return post(baseURL, "/v1/admin/nodes/"+neturl.PathEscape(args[1])+"/uncordon", []byte(`{}`))
	default:
		return nodeDetail(baseURL, args)
	}
}

func nodeDetail(baseURL string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: hacoctl node <id> | hacoctl node cordon|uncordon <id>")
	}
	var nodes []nodeView
	if err := fetchJSON(baseURL, "/v1/nodes", &nodes); err != nil {
		return err
	}
	for _, n := range nodes {
		if n.ID != args[0] {
			continue
		}
		fmt.Printf("id:           %s\n", n.ID)
		fmt.Printf("address:      %s\n", n.Address)
		fmt.Printf("architecture: %s\n", n.Architecture)
		fmt.Printf("availability: %s\n", n.Availability)
		fmt.Printf("heartbeat:    %s\n", n.HeartbeatAt.Format(time.RFC3339))
		fmt.Printf("allocatable:  %dm CPU, %dMi memory\n", n.Allocatable.CPUMillis, n.Allocatable.MemoryBytes/1024/1024)
		fmt.Printf("thermal:      known=%v\n", n.Thermal.Known)
		if len(n.Labels) > 0 {
			fmt.Printf("labels:       %v\n", n.Labels)
		}
		fmt.Printf("cached images (%d):\n", len(n.CachedImages))
		for _, img := range n.CachedImages {
			fmt.Printf("  - %s\n", img)
		}
		return nil
	}
	return fmt.Errorf("no node %q", args[0])
}

type serviceInstanceView struct {
	Project       string `json:"Project"`
	Service       string `json:"Service"`
	NodeID        string `json:"NodeID"`
	Address       string `json:"Address"`
	ContainerPort int    `json:"ContainerPort"`
	Status        string `json:"Status"`
}

// services resolves a logical service name to its current endpoints
// (the API already exists — GET /v1/services/...; this is just the CLI
// wrapper, table-rendered like everything else here).
func services(baseURL string, args []string) error {
	if len(args) != 1 && len(args) != 2 {
		return fmt.Errorf("usage: hacoctl services <project> [service]")
	}
	path := "/v1/services/" + neturl.PathEscape(args[0])
	if len(args) == 2 {
		path += "/" + neturl.PathEscape(args[1])
	}
	var out []serviceInstanceView
	if err := fetchJSON(baseURL, path, &out); err != nil {
		return err
	}
	if len(out) == 0 {
		fmt.Println("no ready instances")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tNODE\tADDRESS\tPORT\tSTATUS")
	for _, si := range out {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", si.Service, si.NodeID, si.Address, si.ContainerPort, si.Status)
	}
	tw.Flush()
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// status is a table of everything with no/one argument (just like
// `hacoctl workloads`, which it shares its implementation with), or one
// workload's full detail once you name a service. Revision defaults to
// "latest" since that's the only one anything in this codebase ever
// produces today — there's no content-addressed revisioning yet (every
// deploy replaces the same project/service/latest workload in place), so
// in practice it never needs to be spelled out.
func status(baseURL string, args []string) error {
	switch len(args) {
	case 0, 1:
		return workloads(baseURL, args)
	case 2:
		return get(baseURL, "/v1/workloads/"+args[0]+"/"+args[1]+"/latest")
	case 3:
		return get(baseURL, "/v1/workloads/"+args[0]+"/"+args[1]+"/"+args[2])
	default:
		return fmt.Errorf("usage: hacoctl status [project] [service] [revision]")
	}
}

// stopContainers stops every workload (so each agent removes its
// container) without touching node credentials — the "clear the board but
// keep nodes joined" command. teardown below reuses it as its first half.
func stopContainers(baseURL string, args []string) error {
	yes := false
	var rest []string
	for _, a := range args {
		if a == "--yes" || a == "-y" {
			yes = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) > 1 {
		return fmt.Errorf("usage: hacoctl stop-containers [project] [--yes]")
	}
	project := ""
	target := "EVERY workload's container"
	if len(rest) == 1 {
		project = rest[0]
		target = fmt.Sprintf("every workload's container in project %q", project)
	}
	if !yes && !confirm(fmt.Sprintf("This will stop %s. Continue?", target)) {
		return fmt.Errorf("aborted")
	}
	return stopAllWorkloads(baseURL, project)
}

// stopAllWorkloads stops every non-stopped workload, or (project != "")
// just those under that project — teardown calls it unscoped ("") since
// node-credential revocation isn't a per-project concept either way.
func stopAllWorkloads(baseURL, project string) error {
	path := "/v1/workloads"
	if project != "" {
		path += "?project=" + neturl.QueryEscape(project)
	}
	var wl []workloadRecordView
	if err := fetchJSON(baseURL, path, &wl); err != nil {
		return fmt.Errorf("list workloads: %w", err)
	}
	for _, w := range wl {
		if w.Status == "stopped" {
			continue
		}
		path := fmt.Sprintf("/v1/workloads/%s/%s/%s/stop",
			neturl.PathEscape(w.Project), neturl.PathEscape(w.Service), neturl.PathEscape(w.Revision))
		if err := postQuiet(baseURL, path, []byte(`{}`)); err != nil {
			fmt.Printf("stop %s/%s/%s: %v\n", w.Project, w.Service, w.Revision, err)
			continue
		}
		fmt.Printf("stopped %s/%s/%s\n", w.Project, w.Service, w.Revision)
	}
	return nil
}

// teardown stops every workload (so agents remove their containers) and
// revokes every node's agent credential — the "start completely clean
// again" command, built from the existing per-workload stop and
// per-node revoke endpoints rather than a new bulk server-side operation.
func teardown(baseURL string, args []string) error {
	yes := false
	for _, a := range args {
		if a == "--yes" || a == "-y" {
			yes = true
		}
	}
	if !yes && !confirm("This will stop EVERY workload and revoke EVERY node's credential. Continue?") {
		return fmt.Errorf("teardown aborted")
	}

	if err := stopAllWorkloads(baseURL, ""); err != nil {
		return err
	}

	own := agent.HostNodeID()
	var nodeList []nodeView
	if err := fetchJSON(baseURL, "/v1/nodes", &nodeList); err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	for _, n := range nodeList {
		if n.ID == own {
			fmt.Printf("skipped %s (this machine's own node)\n", n.ID)
			continue
		}
		if err := postQuiet(baseURL, "/v1/admin/nodes/"+neturl.PathEscape(n.ID)+"/revoke", []byte(`{}`)); err != nil {
			fmt.Printf("revoke %s: %v\n", n.ID, err)
			continue
		}
		fmt.Printf("revoked %s\n", n.ID)
	}
	return nil
}

// start launches the control-plane binary (found next to hacoctl, in
// $PATH, or via HACO_CONTROLPLANE_BIN) so standing up a node doesn't
// require knowing about a separate `controlplane` binary at all. It
// no-ops if the control plane is already reachable, and by default
// detaches into the background with its own log file; --foreground
// runs it attached to this terminal instead. It also defaults the local
// build/push registry on (HACO_REGISTRY_ADDR=auto) unless the caller
// already set HACO_REGISTRY_ADDR or passed --no-registry, so a `build:`
// service in a compose file works without that env var being set by hand
// every time.
func start(baseURL string, args []string) error {
	foreground, registry, showLogs, showTUI := false, true, false, false
	for _, a := range args {
		switch a {
		case "--foreground", "-f":
			foreground = true
		case "--no-registry":
			registry = false
		case "--logs":
			showLogs = true
		case "--tui":
			showTUI = true
		}
	}
	if showLogs && showTUI {
		return fmt.Errorf("--logs and --tui can't be combined (the TUI takes over the terminal; run them separately)")
	}
	if foreground && showTUI {
		return fmt.Errorf("--foreground and --tui can't be combined (the control plane's own foreground output and the TUI both need the terminal); drop --foreground, hacoctl start --tui already waits for readiness")
	}

	dataDir := envOr("HACO_DATA_DIR", filepath.Join(homeDir(), ".haco"))
	logPath := filepath.Join(dataDir, "controlplane.log")

	if controlPlaneUp(baseURL) {
		fmt.Printf("control plane already running at %s\n", baseURL)
		if showLogs {
			return tailLogFile(logPath)
		}
		if showTUI {
			return runTUI(baseURL, nil)
		}
		return nil
	}

	bin, err := controlPlaneBinary()
	if err != nil {
		return err
	}

	env := os.Environ()
	if registry && os.Getenv("HACO_REGISTRY_ADDR") == "" {
		env = append(env, "HACO_REGISTRY_ADDR=auto")
	}

	if foreground {
		cmd := exec.Command(bin)
		cmd.Env = env
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		return cmd.Run()
	}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(bin)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = logFile, logFile
	// Detach into its own session so it survives this process exiting,
	// the same way `nohup ... &` would.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start control plane: %w", err)
	}
	pid := cmd.Process.Pid
	pidPath := filepath.Join(dataDir, "controlplane.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		fmt.Printf("warning: write pid file: %v\n", err)
	}
	_ = cmd.Process.Release()
	fmt.Printf("control plane starting (pid %d), logs: %s\n", pid, logPath)

	for i := 0; i < 20; i++ {
		if controlPlaneUp(baseURL) {
			fmt.Printf("control plane ready at %s\n", baseURL)
			if showLogs {
				return tailLogFile(logPath)
			}
			if showTUI {
				return runTUI(baseURL, nil)
			}
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("control plane did not become ready within 10s; check %s", logPath)
}

// stopAll stops the background control-plane process `start` launched,
// using the pid file it wrote — the counterpart to `start`, distinct from
// `stop` (which stops one workload) and `teardown` (which stops workloads
// and revokes nodes but leaves the control plane itself running).
func stopAll(args []string) error {
	dataDir := envOr("HACO_DATA_DIR", filepath.Join(homeDir(), ".haco"))
	pidPath := filepath.Join(dataDir, "controlplane.pid")

	data, err := os.ReadFile(pidPath)
	if err != nil {
		return fmt.Errorf("no pid file at %s (was the control plane started with `hacoctl start`?): %w", pidPath, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("invalid pid file %s: %w", pidPath, err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop control plane (pid %d): %w", pid, err)
	}
	_ = os.Remove(pidPath)
	fmt.Printf("control plane stopped (pid %d)\n", pid)
	return nil
}

func controlPlaneUp(baseURL string) bool {
	client := http.Client{Timeout: time.Second}
	resp, err := client.Get(baseURL + "/v1/nodes")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// tailLogFile streams new output appended to the control-plane log file —
// `hacoctl start --logs`, so there's no need for a separate terminal/
// command just to watch what the control plane is doing. Starts from the
// file's current end (only output from this point on, not the whole
// history across every previous start) and polls rather than using a
// filesystem-watch dependency; blocks until interrupted (Ctrl+C).
func tailLogFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	fmt.Printf("Tailing %s (Ctrl+C to stop)...\n", path)
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n])
		}
		if err != nil && err != io.EOF {
			return err
		}
		if n == 0 {
			time.Sleep(300 * time.Millisecond)
		}
	}
}

// controlPlaneBinary finds the controlplane executable: next to hacoctl's
// own binary (the common case after install.sh, or a `go build` into the
// same bin/ directory), then $PATH, so `hacoctl start` needs no separate
// knowledge of where it lives.
func controlPlaneBinary() (string, error) {
	return findSiblingBinary("controlplane", "HACO_CONTROLPLANE_BIN")
}

func agentBinary() (string, error) {
	return findSiblingBinary("agent", "HACO_AGENT_BIN")
}

// findSiblingBinary locates another binary from this project: an env var
// override first, then next to hacoctl's own binary (the common case
// after install.sh, or a `go build` into the same bin/ directory), then
// $PATH — the same resolution controlPlaneBinary used before `hacoctl
// agent run`/`join` needed the same logic for the agent binary.
func findSiblingBinary(name, envVar string) (string, error) {
	if p := os.Getenv(envVar); p != "" {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s binary not found next to hacoctl or in PATH (set %s)", name, envVar)
}

// agentCmd runs `hacoctl agent run` / `hacoctl agent join <token>` by
// exec'ing the actual agent binary in the foreground, inheriting stdio —
// so a worker node never needs to know the agent binary exists
// separately, the same way `hacoctl start` hides the controlplane binary.
func agentCmd(args []string) error {
	if len(args) == 0 || (args[0] != "run" && args[0] != "join") {
		return fmt.Errorf("usage: hacoctl agent run | hacoctl agent join <token>")
	}
	bin, err := agentBinary()
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	return cmd.Run()
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

// postQuiet is post without pretty-printing the response body — used in
// teardown's loops, which print their own one-line-per-item summary.
func postQuiet(baseURL, path string, body []byte) error {
	resp, err := http.Post(baseURL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", resp.Status, string(data))
	}
	return nil
}

func stop(baseURL string, args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: hacoctl stop <project> <service> <revision>")
	}
	path := fmt.Sprintf("/v1/workloads/%s/%s/%s/stop",
		neturl.PathEscape(args[0]), neturl.PathEscape(args[1]), neturl.PathEscape(args[2]))
	return post(baseURL, path, []byte(`{}`))
}

// removeWorkload hard-deletes one workload record — distinct from stop,
// which just marks it stopped and leaves the historical row behind.
// Revision defaults to "latest" (the only one anything produces today),
// same convenience hacoctl status already offers.
func removeWorkload(baseURL string, args []string) error {
	if len(args) != 2 && len(args) != 3 {
		return fmt.Errorf("usage: hacoctl remove-workload <project> <service> [revision]")
	}
	revision := "latest"
	if len(args) == 3 {
		revision = args[2]
	}
	path := fmt.Sprintf("/v1/workloads/%s/%s/%s",
		neturl.PathEscape(args[0]), neturl.PathEscape(args[1]), neturl.PathEscape(revision))
	return del(baseURL, path)
}

// removeAllWorkloads hard-deletes every workload record, or (project
// given) just those under that project — the destructive counterpart to
// hacoctl stop-containers, which only marks them stopped.
func removeAllWorkloads(baseURL string, args []string) error {
	yes := false
	var rest []string
	for _, a := range args {
		if a == "--yes" || a == "-y" {
			yes = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) > 1 {
		return fmt.Errorf("usage: hacoctl remove-all-workloads [project] [--yes]")
	}
	project := ""
	target := "EVERY workload record"
	if len(rest) == 1 {
		project = rest[0]
		target = fmt.Sprintf("every workload record in project %q", project)
	}
	if !yes && !confirm(fmt.Sprintf("This will permanently delete %s (not just stop them). Continue?", target)) {
		return fmt.Errorf("aborted")
	}
	path := "/v1/workloads"
	if project != "" {
		path += "?project=" + neturl.QueryEscape(project)
	}
	return del(baseURL, path)
}

func logs(baseURL string, args []string) error {
	return fetchLogs(http.DefaultClient, baseURL, args, os.Stdout)
}

func fetchLogs(client *http.Client, baseURL string, args []string, output io.Writer) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: hacoctl logs <project> <service> <revision>")
	}
	path := fmt.Sprintf("/v1/workloads/%s/%s/%s/logs",
		neturl.PathEscape(args[0]), neturl.PathEscape(args[1]), neturl.PathEscape(args[2]))
	resp, err := client.Get(baseURL + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("control plane returned %s: %s", resp.Status, string(data))
	}
	_, err = io.Copy(output, resp.Body)
	return err
}

func get(baseURL, path string) error {
	resp, err := http.Get(baseURL + path)
	if err != nil {
		return err
	}
	return printResponse(resp)
}

// getOptionalProject is get with an optional single "project" arg appended
// as a query param — shared by `hacoctl events [project]` and `hacoctl
// metrics [project]`.
func getOptionalProject(baseURL, path string, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: hacoctl %s [project]", strings.TrimPrefix(path, "/v1/"))
	}
	if len(args) == 1 {
		path += "?project=" + neturl.QueryEscape(args[0])
	}
	return get(baseURL, path)
}

func post(baseURL, path string, body []byte) error {
	resp, err := http.Post(baseURL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	return printResponse(resp)
}

// removeNode hard-deletes a node, refused by the control plane if it
// still has a non-terminal workload assigned — --force overrides that
// (the workload rows are left as-is, just no longer rescuable by the
// reconciler once the node row is gone, so only force past this when
// you're sure nothing there matters anymore).
func removeNode(baseURL string, args []string) error {
	force := false
	var rest []string
	for _, a := range args {
		if a == "--force" {
			force = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: hacoctl remove-node <node-id> [--force]")
	}
	path := "/v1/admin/nodes/" + neturl.PathEscape(rest[0])
	if force {
		path += "?force=true"
	}
	return del(baseURL, path)
}

func del(baseURL, path string) error {
	req, err := http.NewRequest(http.MethodDelete, baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	return printResponse(resp)
}

func printResponse(resp *http.Response) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var pretty bytes.Buffer
	if json.Indent(&pretty, data, "", "  ") == nil {
		fmt.Println(pretty.String())
	} else {
		fmt.Println(string(data))
	}

	if resp.StatusCode >= 300 {
		return fmt.Errorf("control plane returned %s", resp.Status)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
