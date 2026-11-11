// Command haco is a thin CLI over the control-plane HTTP API, so using
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
		return
	}
	name, args := os.Args[1], os.Args[2:]
	switch name {
	case "help", "--help", "-h":
		if err := printHelp(os.Stdout, args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	// An older command name still works, with a note on stderr.
	if a, ok := aliases[name]; ok {
		fmt.Fprintf(os.Stderr, "haco: %s\n", renameNotice(name, a))
		argv := a.to(args)
		name, args = argv[0], argv[1:]
	}
	cmd := findCommand(name)
	if cmd == nil {
		fmt.Fprintf(os.Stderr, "haco: unknown command %q; run 'haco help' to list commands\n", name)
		os.Exit(1)
	}
	if wantsHelp(args) {
		_ = printHelp(os.Stdout, []string{cmd.name})
		return
	}

	url := envOr("HACO_CONTROLPLANE_URL", "http://localhost:8099")
	if err := cmd.run(url, args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
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
	fmt.Printf("haco agent join '%s'\n", encoded)
	return nil
}

// plan calls /v1/compose/plan and renders a human-readable table by
// default (raw JSON is for the API/future UI, not the terminal); pass
// --json to get the same raw response haco used to always print.
func plan(baseURL string, args []string) error {
	o, err := parseComposeArgs("plan", args, "json", "fresh")
	if err != nil {
		return err
	}
	project, _, workingDir, yaml, err := o.resolve("plan")
	if err != nil {
		return err
	}
	if o.jsonOut {
		body, _ := json.Marshal(map[string]any{
			"project":      project,
			"working_dir":  workingDir,
			"yaml":         string(yaml),
			"only_service": o.services,
			"pin_host":     o.pinHost,
			"pin_nodes":    o.pinNodes,
			"fresh":        o.fresh,
		})
		return post(baseURL, "/v1/compose/plan", body)
	}

	out, err := fetchPlan(baseURL, project, workingDir, yaml, o.services, o.pinHost, o.pinNodes, o.fresh)
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

// parsePinNode parses --pin-node's "<node-id>=<service,...>" value.
func parsePinNode(raw string) (node string, services []string, err error) {
	node, list, ok := strings.Cut(raw, "=")
	node = strings.TrimSpace(node)
	if !ok || node == "" {
		return "", nil, fmt.Errorf("--pin-node value %q must be <node-id>=<service,...>", raw)
	}
	return node, splitPinHost(list), nil
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
func fetchPlan(baseURL, project, workingDir string, yaml []byte, onlyService string, pinHost *[]string, pinNodes map[string][]string, fresh bool) (planView, error) {
	body, _ := json.Marshal(map[string]any{
		"project":      project,
		"working_dir":  workingDir,
		"yaml":         string(yaml),
		"only_service": onlyService,
		"pin_host":     pinHost,
		"pin_nodes":    pinNodes,
		"fresh":        fresh,
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
	o, err := parseComposeArgs("build", args, "no-cache")
	if err != nil {
		return err
	}
	project, _, workingDir, yaml, err := o.resolve("build")
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"project":      project,
		"working_dir":  workingDir,
		"yaml":         string(yaml),
		"no_cache":     o.noCache,
		"only_service": o.services,
		"pin_host":     o.pinHost,
		"pin_nodes":    o.pinNodes,
	})
	return post(baseURL, "/v1/compose/build", body)
}

// deploy dry-run plans first so it can ask for explicit consent before a
// pinned workload with x-haco-allow-fresh-volume gets rescheduled onto a
// brand-new, empty volume — that's a data-loss trade-off the user opted a
// service into ahead of time, but it still deserves a live confirmation
// at the moment it's actually about to happen, not just silent action.
func deploy(baseURL string, args []string) error {
	o, err := parseComposeArgs("deploy", args, "yes", "no-cache", "fresh")
	if err != nil {
		return err
	}
	project, _, workingDir, yaml, err := o.resolve("deploy")
	if err != nil {
		return err
	}
	yes, noCache, fresh := o.yes, o.noCache, o.fresh
	onlyService, pinHost, pinNodes := o.services, o.pinHost, o.pinNodes

	if planned, err := fetchPlan(baseURL, project, workingDir, yaml, onlyService, pinHost, pinNodes, fresh); err == nil {
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
		if fresh {
			if services := unpinnedPersistentServices(planned); len(services) > 0 {
				fmt.Println("WARNING: --fresh will let the scheduler freely re-pick a node for each of these currently-unpinned services, abandoning whatever data is on their current node:")
				for _, s := range services {
					fmt.Printf("  - %s\n", s)
				}
				if !yes && !confirm("Continue and accept the data loss?") {
					return fmt.Errorf("deploy aborted: fresh-deploy consent declined")
				}
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
		"pin_nodes":    pinNodes,
		"fresh":        fresh,
	})
	return deployAndWatch(baseURL, project, body)
}

// unpinnedPersistentServices lists every node_local_persistent workload in
// planned that carries no pin of its own — exactly the set --fresh would
// affect, since a pinned workload (explicit extension or --pin-host/
// --pin-node) ignores --fresh entirely.
func unpinnedPersistentServices(planned planView) []string {
	var out []string
	for _, w := range planned.Plan.Workloads {
		if w.Storage.Class == "node_local_persistent" && w.Storage.PinnedNodeID == "" {
			out = append(out, w.Service)
		}
	}
	return out
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
			fmt.Println("(timed out waiting for all workloads to reach a terminal state; check `haco ps` for current status)")
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

// ps lists every service with its node and status, or one service's full
// detail once a project and a service are named. Every deploy replaces the
// same project/service/latest workload in place, so there is no revision to
// show or type.
func ps(baseURL string, args []string) error {
	switch len(args) {
	case 0, 1:
		return psTable(baseURL, args)
	case 2:
		return get(baseURL, "/v1/workloads/"+neturl.PathEscape(args[0])+"/"+neturl.PathEscape(args[1])+"/latest")
	case 3: // the older `status <project> <service> <revision>`
		return get(baseURL, "/v1/workloads/"+neturl.PathEscape(args[0])+"/"+neturl.PathEscape(args[1])+"/"+neturl.PathEscape(args[2]))
	default:
		return fmt.Errorf("usage: haco ps [project] [service]")
	}
}

func psTable(baseURL string, args []string) error {
	path := "/v1/workloads"
	if len(args) == 1 {
		path += "?project=" + neturl.QueryEscape(args[0])
	}
	var out []workloadRecordView
	if err := fetchJSON(baseURL, path, &out); err != nil {
		return err
	}
	if len(out) == 0 {
		fmt.Println("no services")
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].Service < out[j].Service
	})
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tSERVICE\tNODE\tSTATUS\tPORTS\tCONTAINER\tREASON")
	for _, w := range out {
		cid := w.ContainerID
		if len(cid) > 12 {
			cid = cid[:12]
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", w.Project, w.Service, orDash(w.NodeID), w.Status,
			intListOrDash(w.Constraints.HostPorts), orDash(cid), orDash(w.StatusReason))
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
// breakdown, so you don't have to run `haco workloads` with no filter
// and eyeball the PROJECT column.
func projects(baseURL string, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: haco projects")
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
// (labels, cached images) stays on `haco node <id>`.
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
// node dispatches the `haco node <command>` family: everything that acts
// on one node, plus join-token, which makes a new one. A bare
// `haco node <id>` still shows the node, as before.
func node(baseURL string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: haco node show|join-token|cordon|uncordon|rename|limit|revoke|rm ... (haco help node)")
	}
	rest := args[1:]
	switch args[0] {
	case "show":
		return nodeDetail(baseURL, rest)
	case "join-token":
		return printJoinCommand(baseURL)
	case "cordon", "uncordon":
		if len(rest) != 1 {
			return fmt.Errorf("usage: haco node %s <id>", args[0])
		}
		return post(baseURL, "/v1/admin/nodes/"+neturl.PathEscape(rest[0])+"/"+args[0], []byte(`{}`))
	case "rename":
		return setNodeName(baseURL, rest)
	case "limit":
		return setNodeLimits(baseURL, rest)
	case "revoke":
		if len(rest) != 1 {
			return fmt.Errorf("usage: haco node revoke <id>")
		}
		return post(baseURL, "/v1/admin/nodes/"+neturl.PathEscape(rest[0])+"/revoke", []byte(`{}`))
	case "rm":
		return removeNode(baseURL, rest)
	default:
		if len(args) == 1 {
			fmt.Fprintf(os.Stderr, "haco: \"haco node <id>\" is now \"haco node show <id>\" (the old form still works)\n")
			return nodeDetail(baseURL, args)
		}
		return fmt.Errorf("unknown node command %q (haco help node)", args[0])
	}
}

// setNodeName sets a node's display name. The node ID is the machine's
// permanent identity and never changes; only the name shown does.
func setNodeName(baseURL string, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: haco node rename <node-id> <name>   (\"\" clears the name)")
	}
	id, name := args[0], strings.Join(args[1:], " ")
	body, _ := json.Marshal(map[string]string{"name": name})
	if err := postQuiet(baseURL, "/v1/admin/nodes/"+neturl.PathEscape(id)+"/name", body); err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		fmt.Printf("node %s now shows its ID\n", id)
	} else {
		fmt.Printf("node %s is now named %q (its ID stays %s)\n", id, name, id)
	}
	return nil
}

// setNodeLimits caps how much CPU and memory the scheduler may reserve on a
// node, below what its Docker reports.
func setNodeLimits(baseURL string, args []string) error {
	usage := fmt.Errorf("usage: haco node limit <node-id> [--cpus N] [--memory SIZE] | haco node limit <node-id> --clear   (SIZE like 8GiB, 512MiB, 4G)")
	if len(args) < 2 {
		return usage
	}
	id := args[0]
	var cpu, mem int64
	set := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--clear":
			set = true
		case "--cpus":
			if i+1 >= len(args) {
				return usage
			}
			v, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil || v <= 0 {
				return fmt.Errorf("--cpus %q: want a positive number of cores, e.g. 4 or 2.5", args[i+1])
			}
			cpu, set, i = int64(v*1000), true, i+1
		case "--memory":
			if i+1 >= len(args) {
				return usage
			}
			v, err := parseByteSize(args[i+1])
			if err != nil {
				return fmt.Errorf("--memory %q: %w", args[i+1], err)
			}
			mem, set, i = v, true, i+1
		default:
			return usage
		}
	}
	if !set {
		return usage
	}
	body, _ := json.Marshal(map[string]int64{"cpu_millis": cpu, "memory_bytes": mem})
	if err := postQuiet(baseURL, "/v1/admin/nodes/"+neturl.PathEscape(id)+"/limits", body); err != nil {
		return err
	}
	describe := func(v int64, f func(int64) string) string {
		if v == 0 {
			return "no limit"
		}
		return f(v)
	}
	fmt.Printf("node %s: CPU %s, memory %s. The scheduler reserves no more than this there; running workloads are not moved.\n", id,
		describe(cpu, func(v int64) string { return fmt.Sprintf("%.2g cores", float64(v)/1000) }),
		describe(mem, func(v int64) string { return fmt.Sprintf("%.1f GiB", float64(v)/(1<<30)) }))
	return nil
}

// parseByteSize reads sizes like 8GiB, 8G, 512MiB, 512M or a plain byte count.
func parseByteSize(s string) (int64, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	units := []struct {
		suffix string
		mult   float64
	}{{"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10}, {"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}}
	mult := 1.0
	for _, u := range units {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSuffix(t, u.suffix), u.mult
			break
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("want a size like 8GiB or 512MiB")
	}
	return int64(v * mult), nil
}

func nodeDetail(baseURL string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: haco node show <id>")
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
		return fmt.Errorf("usage: haco services <project> [service]")
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

// teardown (haco cluster reset) stops every workload (so agents remove their containers) and
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
		return fmt.Errorf("cluster reset aborted")
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

// shutdown stops the whole cluster in order — every node's containers and
// agent, then the control plane — so that `haco start` and
// `haco agent run` bring the same services back on the same nodes.
func shutdown(baseURL string, args []string) error {
	yes := false
	for _, a := range args {
		if a == "--yes" || a == "-y" {
			yes = true
		}
	}
	var nodeList []nodeView
	if err := fetchJSON(baseURL, "/v1/nodes", &nodeList); err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	var wl []workloadRecordView
	if err := fetchJSON(baseURL, "/v1/workloads", &wl); err != nil {
		return fmt.Errorf("list workloads: %w", err)
	}
	perNode := map[string]int{}
	for _, w := range wl {
		if w.Status == "running" || w.Status == "ready" {
			perNode[w.NodeID]++
		}
	}
	fmt.Println("This will stop:")
	for _, n := range nodeList {
		switch n.Availability {
		case "available", "disabled":
			fmt.Printf("  %-14s %d running service(s) and its agent\n", n.ID, perNode[n.ID])
		default:
			fmt.Printf("  %-14s skipped: node is %s (can't be reached)\n", n.ID, n.Availability)
		}
	}
	fmt.Println("  and the control plane.")
	fmt.Println("Containers are stopped, not removed; `haco start` here and `haco agent run` on each other node bring them back.")
	if !yes && !confirm("Shut down the cluster?") {
		return fmt.Errorf("shutdown aborted")
	}

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Post(baseURL+"/v1/admin/shutdown", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("control plane returned %s: %s", resp.Status, string(data))
	}
	var report struct {
		Nodes map[string]struct {
			Stopped []string `json:"stopped"`
			Errors  []string `json:"errors"`
		} `json:"nodes"`
		NotReached map[string]string `json:"not_reached"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("decode shutdown report: %w", err)
	}
	ids := make([]string, 0, len(report.Nodes))
	for id := range report.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := report.Nodes[id]
		fmt.Printf("%s: stopped %d container(s)\n", id, len(r.Stopped))
		for _, e := range r.Errors {
			fmt.Printf("  error: %s\n", e)
		}
	}
	for id, why := range report.NotReached {
		fmt.Printf("%s: NOT stopped: %s\n", id, why)
	}
	for i := 0; i < 20 && controlPlaneUp(baseURL); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if controlPlaneUp(baseURL) {
		fmt.Println("control plane is still up; stop it with `haco stop-cluster`")
		return nil
	}
	_ = os.Remove(filepath.Join(envOr("HACO_DATA_DIR", filepath.Join(homeDir(), ".haco")), "controlplane.pid"))
	fmt.Println("control plane stopped. Bring everything back with `haco start` here, then `haco agent run` on each other node.")
	return nil
}

// start launches the control-plane binary (found next to haco, in
// $PATH, or via HACO_CONTROLPLANE_BIN) so standing up a node doesn't
// require knowing about a separate `controlplane` binary at all. It
// no-ops if the control plane is already reachable, and by default
// detaches into the background with its own log file; --foreground
// runs it attached to this terminal instead. Without a registry (the
// default) each node builds its own images from the shipped context;
// --registry turns on the local build/push registry (HACO_REGISTRY_ADDR=auto)
// unless the caller already set HACO_REGISTRY_ADDR.
func start(baseURL string, args []string) error {
	foreground, registry, showLogs, showTUI, web := false, false, false, false, false
	for _, a := range args {
		switch a {
		case "--web":
			web = true
		case "--foreground", "-f":
			foreground = true
		case "--registry":
			registry = true
		case "--no-registry":
			registry = false // the default now; still accepted so old scripts keep working
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
		return fmt.Errorf("--foreground and --tui can't be combined (the control plane's own foreground output and the TUI both need the terminal); drop --foreground, haco start --tui already waits for readiness")
	}

	dataDir := envOr("HACO_DATA_DIR", filepath.Join(homeDir(), ".haco"))
	logPath := filepath.Join(dataDir, "controlplane.log")

	webDir := ""
	if web {
		dir, err := webConsoleDir(dataDir)
		if err != nil {
			return err
		}
		webDir = dir
	}

	if controlPlaneUp(baseURL) {
		fmt.Printf("control plane already running at %s\n", baseURL)
		if web {
			if webConsoleUp(baseURL) {
				fmt.Printf("web console: %s/\n", strings.TrimRight(baseURL, "/"))
			} else {
				fmt.Println("the running control plane was started without --web; run 'haco stop-cluster' then 'haco start --web' to serve the console")
			}
		}
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
	// Only --web serves the console; drop an inherited HACO_WEB_DIR otherwise.
	env = withoutEnv(env, "HACO_WEB_DIR")
	if web {
		env = append(env, "HACO_WEB_DIR="+webDir)
		fmt.Printf("web console: %s/ (once the control plane is up)\n", strings.TrimRight(baseURL, "/"))
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
			if web {
				fmt.Printf("web console ready at %s/\n", strings.TrimRight(baseURL, "/"))
			}
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

// stopControlPlane stops the background control-plane process `start` launched,
// using the pid file it wrote — the counterpart to `start`, distinct from
// `stop` (which stops services) and `cluster reset` (which stops workloads
// and revokes nodes but leaves the control plane itself running).
func stopControlPlane(args []string) error {
	dataDir := envOr("HACO_DATA_DIR", filepath.Join(homeDir(), ".haco"))
	pidPath := filepath.Join(dataDir, "controlplane.pid")

	data, err := os.ReadFile(pidPath)
	if err != nil {
		return fmt.Errorf("no pid file at %s (was the control plane started with `haco start`?): %w", pidPath, err)
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

// webConsoleDir finds the built web console: HACO_WEB_DIR when set, else
// <data dir>/web, which ./install.sh fills when npm is available.
func webConsoleDir(dataDir string) (string, error) {
	dir := os.Getenv("HACO_WEB_DIR")
	if dir == "" {
		dir = filepath.Join(dataDir, "web")
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return "", fmt.Errorf("no built web console in %s; run ./install.sh with npm installed (it builds web/ there), or set HACO_WEB_DIR to a folder holding a built console", dir)
	}
	return dir, nil
}

// webConsoleUp reports whether the running control plane serves the
// console, which it only does when it was started with --web.
func webConsoleUp(baseURL string) bool {
	client := http.Client{Timeout: time.Second}
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html")
}

func withoutEnv(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
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
// `haco start --logs`, so there's no need for a separate terminal/
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

// controlPlaneBinary finds the controlplane executable: next to haco's
// own binary (the common case after install.sh, or a `go build` into the
// same bin/ directory), then $PATH, so `haco start` needs no separate
// knowledge of where it lives.
func controlPlaneBinary() (string, error) {
	return findSiblingBinary("controlplane", "HACO_CONTROLPLANE_BIN")
}

func agentBinary() (string, error) {
	return findSiblingBinary("agent", "HACO_AGENT_BIN")
}

// findSiblingBinary locates another binary from this project: an env var
// override first, then next to haco's own binary (the common case
// after install.sh, or a `go build` into the same bin/ directory), then
// $PATH — the same resolution controlPlaneBinary used before `haco
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
	return "", fmt.Errorf("%s binary not found next to haco or in PATH (set %s)", name, envVar)
}

// agentCmd runs `haco agent run` / `haco agent join <token>` by
// exec'ing the actual agent binary in the foreground, inheriting stdio —
// so a worker node never needs to know the agent binary exists
// separately, the same way `haco start` hides the controlplane binary.
func agentCmd(args []string) error {
	if len(args) == 0 || (args[0] != "run" && args[0] != "join") {
		return fmt.Errorf("usage: haco agent run | haco agent join <token>")
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

// projectAndServices reads "<project> [service...]". A trailing "latest" is
// dropped: the older commands took a revision there, and there has only
// ever been "latest".
func projectAndServices(usage string, args []string) (string, []string, error) {
	pos := args
	if len(pos) >= 3 && pos[len(pos)-1] == "latest" {
		pos = pos[:len(pos)-1]
	}
	for _, a := range pos {
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			return "", nil, fmt.Errorf("unknown flag %s\nusage: %s", a, usage)
		}
	}
	if len(pos) < 1 {
		return "", nil, fmt.Errorf("usage: %s", usage)
	}
	return pos[0], pos[1:], nil
}

// workloadsFor lists a project's workloads, narrowed to the named services
// when there are any.
func workloadsFor(baseURL, project string, services []string) ([]workloadRecordView, error) {
	var all []workloadRecordView
	if err := fetchJSON(baseURL, "/v1/workloads?project="+neturl.QueryEscape(project), &all); err != nil {
		return nil, err
	}
	var inProject []workloadRecordView
	for _, w := range all {
		if w.Project == project {
			inProject = append(inProject, w)
		}
	}
	if len(inProject) == 0 {
		return nil, fmt.Errorf("no services in project %q", project)
	}
	if len(services) == 0 {
		return inProject, nil
	}
	var out []workloadRecordView
	for _, name := range services {
		found := false
		for _, w := range inProject {
			if w.Service == name {
				out = append(out, w)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("project %q has no service %q", project, name)
		}
	}
	return out, nil
}

// resume starts stopped (or failed) services again where they last ran.
func resume(baseURL string, args []string) error {
	project, services, err := projectAndServices("haco resume <project> [service...]", args)
	if err != nil {
		return err
	}
	wl, err := workloadsFor(baseURL, project, services)
	if err != nil {
		return err
	}
	return eachWorkload(baseURL, wl, "start", func(w workloadRecordView) bool { return w.Status == "stopped" || w.Status == "failed" }, "nothing is stopped")
}

// restart recreates running services' containers on their current nodes.
func restart(baseURL string, args []string) error {
	project, services, err := projectAndServices("haco restart <project> [service...]", args)
	if err != nil {
		return err
	}
	wl, err := workloadsFor(baseURL, project, services)
	if err != nil {
		return err
	}
	return eachWorkload(baseURL, wl, "restart", func(w workloadRecordView) bool { return w.Status != "stopped" }, "nothing is running; use haco resume")
}

func eachWorkload(baseURL string, wl []workloadRecordView, action string, want func(workloadRecordView) bool, none string) error {
	done, failed := 0, 0
	for _, w := range wl {
		if !want(w) {
			continue
		}
		path := fmt.Sprintf("/v1/workloads/%s/%s/%s/%s", neturl.PathEscape(w.Project), neturl.PathEscape(w.Service), neturl.PathEscape(w.Revision), action)
		if err := postQuiet(baseURL, path, []byte(`{}`)); err != nil {
			fmt.Printf("%s %s/%s: %v\n", action, w.Project, w.Service, err)
			failed++
			continue
		}
		fmt.Printf("%s %s/%s\n", action, w.Project, w.Service)
		done++
	}
	if done == 0 && failed == 0 {
		fmt.Println(none)
		return nil
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d could not %s", failed, done+failed, action)
	}
	return nil
}

// stop stops the named services, a whole project, or (--all) everything.
// The service keeps its record, node and data, so resume starts it again.
func stop(baseURL string, args []string) error {
	yes, rest := hasFlag(args, "--yes", "-y")
	all, rest := hasFlag(rest, "--all")
	if all {
		if len(rest) > 0 {
			return fmt.Errorf("--all stops every project, so it takes no project or services")
		}
		if !yes && !confirm("This will stop EVERY service of EVERY project. Continue?") {
			return fmt.Errorf("aborted")
		}
		return stopAllWorkloads(baseURL, "")
	}
	project, services, err := projectAndServices("haco stop <project> [service...] | haco stop --all", rest)
	if err != nil {
		return err
	}
	wl, err := workloadsFor(baseURL, project, services)
	if err != nil {
		return err
	}
	return eachWorkload(baseURL, wl, "stop", func(w workloadRecordView) bool { return w.Status != "stopped" }, "nothing is running")
}

// rm hard-deletes workload records, unlike stop, which keeps a stopped row.
// Naming services deletes just those; naming only a project, or --all,
// deletes more, so those ask first.
func rm(baseURL string, args []string) error {
	yes, rest := hasFlag(args, "--yes", "-y")
	all, rest := hasFlag(rest, "--all")
	if all {
		if len(rest) > 0 {
			return fmt.Errorf("--all removes every project, so it takes no project or services")
		}
		if !yes && !confirm("This will permanently delete EVERY workload record (not just stop them). Continue?") {
			return fmt.Errorf("aborted")
		}
		return del(baseURL, "/v1/workloads")
	}
	project, services, err := projectAndServices("haco rm <project> [service...] | haco rm --all", rest)
	if err != nil {
		return err
	}
	if len(services) == 0 {
		if !yes && !confirm(fmt.Sprintf("This will permanently delete every workload record in project %q (not just stop them). Continue?", project)) {
			return fmt.Errorf("aborted")
		}
		return del(baseURL, "/v1/workloads?project="+neturl.QueryEscape(project))
	}
	wl, err := workloadsFor(baseURL, project, services)
	if err != nil {
		return err
	}
	failed := 0
	for _, w := range wl {
		path := fmt.Sprintf("/v1/workloads/%s/%s/%s", neturl.PathEscape(w.Project), neturl.PathEscape(w.Service), neturl.PathEscape(w.Revision))
		if err := delQuiet(baseURL, path); err != nil {
			fmt.Printf("remove %s/%s: %v\n", w.Project, w.Service, err)
			failed++
			continue
		}
		fmt.Printf("removed %s/%s\n", w.Project, w.Service)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d could not be removed", failed, len(wl))
	}
	return nil
}

// cluster holds the cluster-wide operations.
func cluster(baseURL string, args []string) error {
	if len(args) == 0 || args[0] != "reset" {
		return fmt.Errorf("usage: haco cluster reset [--yes]")
	}
	return teardown(baseURL, args[1:])
}

func logs(baseURL string, args []string) error {
	return fetchLogs(http.DefaultClient, baseURL, args, os.Stdout)
}

const logsUsage = "usage: haco logs <project> <service> [--tail N] [-f]"

const followWindow = 1000

func fetchLogs(client *http.Client, baseURL string, args []string, output io.Writer) error {
	var positional []string
	tail, follow := 200, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-f" || a == "--follow":
			follow = true
		case a == "--tail" || a == "-n":
			if i+1 >= len(args) {
				return fmt.Errorf(logsUsage)
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				return fmt.Errorf("--tail needs a positive number, got %q", args[i+1])
			}
			tail = n
			i++
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s\n%s", a, logsUsage)
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) < 2 || len(positional) > 3 {
		return fmt.Errorf(logsUsage)
	}
	revision := "latest"
	if len(positional) == 3 {
		revision = positional[2]
	}
	path := fmt.Sprintf("/v1/workloads/%s/%s/%s/logs?tail=",
		neturl.PathEscape(positional[0]), neturl.PathEscape(positional[1]), neturl.PathEscape(revision))

	var last string
	window := tail
	for {
		text, live, err := getLogs(client, baseURL+path+strconv.Itoa(window))
		// --tail only shapes the first screen; after that each poll needs a
		// window wide enough to hold everything logged since the previous
		// one, or lines between polls are silently skipped.
		window = max(tail, followWindow)
		if err != nil {
			return err
		}
		lines := strings.SplitAfter(text, "\n")
		start := 0
		if last != "" {
			// Docker timestamps every line, so the last line printed marks
			// where new output begins; if it scrolled out of the window,
			// print the whole window.
			for i := len(lines) - 1; i >= 0; i-- {
				if lines[i] == last {
					start = i + 1
					break
				}
			}
		}
		for _, l := range lines[start:] {
			if strings.HasSuffix(l, "\n") {
				_, _ = io.WriteString(output, l)
				last = l
			}
		}
		if !follow {
			if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
				_, _ = io.WriteString(output, lines[n-1])
			}
			return nil
		}
		if !live {
			return fmt.Errorf("container isn't running; nothing to follow")
		}
		time.Sleep(2 * time.Second)
	}
}

// getLogs returns the log text and whether it came live from the node (as
// opposed to the tail captured when the container failed to start).
func getLogs(client *http.Client, url string) (string, bool, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", false, fmt.Errorf("control plane returned %s: %s", resp.Status, string(data))
	}
	live := resp.Header.Get("X-Haco-Logs-Source") != "captured"
	if !live {
		if reason := resp.Header.Get("X-Haco-Live-Error"); reason != "" {
			fmt.Fprintf(os.Stderr, "(live logs unavailable: %s)\n", reason)
		}
		fmt.Fprintln(os.Stderr, "(showing logs captured when the container failed to start)")
	}
	return string(data), live, nil
}

func get(baseURL, path string) error {
	resp, err := http.Get(baseURL + path)
	if err != nil {
		return err
	}
	return printResponse(resp)
}

// getOptionalProject is get with an optional single "project" arg appended
// as a query param — shared by `haco events [project]` and `haco
// metrics [project]`.
func getOptionalProject(baseURL, path string, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: haco %s [project]", strings.TrimPrefix(path, "/v1/"))
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
		return fmt.Errorf("usage: haco node rm <id> [--force]")
	}
	path := "/v1/admin/nodes/" + neturl.PathEscape(rest[0])
	if force {
		path += "?force=true"
	}
	return del(baseURL, path)
}

// delQuiet is del without printing the response body.
func delQuiet(baseURL, path string) error {
	req, err := http.NewRequest(http.MethodDelete, baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return nil
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

// addService appends a --service value to the comma-separated scope, so the
// flag can be repeated (--service api --service worker) or given a list
// (--service api,worker); the control plane deploys the union of their
// dependency closures.
func addService(list, value string) string {
	value = strings.Trim(strings.TrimSpace(value), ",")
	if value == "" {
		return list
	}
	if list == "" {
		return value
	}
	return list + "," + value
}
