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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	case "nodes":
		err = nodes(url)
	case "node":
		err = nodeDetail(url, os.Args[2:])
	case "workloads":
		err = workloads(url, os.Args[2:])
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
	case "events":
		err = get(url, "/v1/events")
	case "metrics":
		err = get(url, "/v1/metrics")
	case "join-token":
		err = printJoinCommand(url)
	case "revoke-node":
		if len(os.Args) != 3 {
			err = fmt.Errorf("usage: hacoctl revoke-node <node-id>")
		} else {
			err = post(url, "/v1/admin/nodes/"+neturl.PathEscape(os.Args[2])+"/revoke", []byte(`{}`))
		}
	case "teardown":
		err = teardown(url, os.Args[2:])
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
  hacoctl deploy <project> <docker-compose.yml>   build, schedule, and run a Compose project (prompts if a pinned service needs a fresh volume; --yes skips it)
  hacoctl plan   <project> <docker-compose.yml>   dry-run the same, no side effects (table; add --json for raw)
  hacoctl status <project> <service> <revision>   one workload's current status
  hacoctl logs   <project> <service> <revision>   latest captured container log tail
  hacoctl stop   <project> <service> <revision>   stop the workload and remove its container
  hacoctl nodes                                   list registered nodes
  hacoctl node <id>                               one node's full detail
  hacoctl workloads [project]                     table of every workload, optionally filtered to one project
  hacoctl containers                              node/workload/container join view
  hacoctl services <project> [service]            resolve a logical service name to its current endpoints
  hacoctl events                                  recent audit events
  hacoctl metrics                                 node/workload counts
  hacoctl join-token                              print a fresh one-time agent join command
  hacoctl revoke-node <node-id>                   revoke that agent credential
  hacoctl teardown                                stop every workload and revoke every node credential (--yes skips confirmation)`)
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
	fmt.Printf("./bin/agent join '%s'\n", encoded)
	return nil
}

// plan calls /v1/compose/plan and renders a human-readable table by
// default (raw JSON is for the API/future UI, not the terminal); pass
// --json to get the same raw response hacoctl used to always print.
func plan(baseURL string, args []string) error {
	jsonOut := false
	var rest []string
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) != 2 {
		return fmt.Errorf("usage: hacoctl plan <project> <docker-compose.yml> [--json]")
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
		body, _ := json.Marshal(map[string]string{
			"project":     project,
			"working_dir": workingDir,
			"yaml":        string(yaml),
		})
		return post(baseURL, "/v1/compose/plan", body)
	}

	out, err := fetchPlan(baseURL, project, workingDir, yaml)
	if err != nil {
		return err
	}
	renderPlanTable(project, out)
	return nil
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
				HostPorts []int `json:"HostPorts"`
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
func fetchPlan(baseURL, project, workingDir string, yaml []byte) (planView, error) {
	body, _ := json.Marshal(map[string]string{
		"project":     project,
		"working_dir": workingDir,
		"yaml":        string(yaml),
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

// deploy dry-run plans first so it can ask for explicit consent before a
// pinned workload with x-haco-allow-fresh-volume gets rescheduled onto a
// brand-new, empty volume — that's a data-loss trade-off the user opted a
// service into ahead of time, but it still deserves a live confirmation
// at the moment it's actually about to happen, not just silent action.
func deploy(baseURL string, args []string) error {
	yes := false
	var rest []string
	for _, a := range args {
		if a == "--yes" || a == "-y" {
			yes = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) != 2 {
		return fmt.Errorf("usage: hacoctl deploy <project> <docker-compose.yml> [--yes]")
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

	if planned, err := fetchPlan(baseURL, project, workingDir, yaml); err == nil {
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

	body, _ := json.Marshal(map[string]string{
		"project":     project,
		"working_dir": workingDir,
		"yaml":        string(yaml),
	})
	return post(baseURL, "/v1/compose/deploy", body)
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
				if wl.Storage.PinnedNodeID != "" {
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
	Project      string `json:"Project"`
	Service      string `json:"Service"`
	Revision     string `json:"Revision"`
	DesiredState string `json:"DesiredState"`
	Status       string `json:"Status"`
	StatusReason string `json:"StatusReason"`
	NodeID       string `json:"NodeID"`
	ContainerID  string `json:"ContainerID"`
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

// containers is a join view over workloads + nodes: which container is
// running where, derived entirely from existing data (no new storage).
func containers(baseURL string, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: hacoctl containers")
	}
	var w []workloadRecordView
	if err := fetchJSON(baseURL, "/v1/workloads", &w); err != nil {
		return err
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
	fmt.Fprintln(tw, "NODE\tWORKLOAD\tCONTAINER ID\tSTATUS")
	for _, rec := range w {
		workload := fmt.Sprintf("%s/%s/%s", rec.Project, rec.Service, rec.Revision)
		cid := rec.ContainerID
		if len(cid) > 12 {
			cid = cid[:12]
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", orDash(rec.NodeID), workload, orDash(cid), rec.Status)
	}
	tw.Flush()
	return nil
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
func nodeDetail(baseURL string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: hacoctl node <id>")
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

func status(baseURL string, args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: hacoctl status <project> <service> <revision>")
	}
	return get(baseURL, "/v1/workloads/"+args[0]+"/"+args[1]+"/"+args[2])
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

	var wl []workloadRecordView
	if err := fetchJSON(baseURL, "/v1/workloads", &wl); err != nil {
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

	own := envOr("HACO_HOST_NODE_ID", agent.DefaultHostNodeID())
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

func post(baseURL, path string, body []byte) error {
	resp, err := http.Post(baseURL+path, "application/json", bytes.NewReader(body))
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
