package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// command is one entry in haco's command list: what `haco help` prints
// and what main dispatches to.
type command struct {
	name    string
	group   string
	summary string // one short line for `haco help`
	detail  string // full usage and flags for `haco help <name>`
	run     func(baseURL string, args []string) error
}

// Groups, in the order `haco help` lists them.
const (
	groupProject = "Project"
	groupCluster = "Cluster"
	groupNodes   = "Nodes"
	groupAgent   = "This machine as a node"
	groupViews   = "Views"
)

var groupOrder = []string{groupProject, groupCluster, groupNodes, groupAgent, groupViews}

var groupNotes = map[string]string{
	groupProject: "-f defaults to the Compose file in this folder, -p to that folder's name",
}

// composeFlagHelp is the flag block plan, build and deploy share.
const composeFlagHelp = `  [service...]            only these services and what they depend on (also --service a,b)
  -f, --file <path>       Compose file (default: compose.yaml, compose.yml, docker-compose.yaml
                          or docker-compose.yml in this folder)
  -p, --project <name>    project name (default: the Compose file's folder name; also reads
                          COMPOSE_PROJECT_NAME)
  --pin-host <svc,...>    for this call only, pin just these services to this machine (say, a
                          database and the service you are editing); all others schedule anywhere
  --pin-node <node>=<svc,...>
                          the same, but pin the services to that node (repeatable)`

// commandTable lists every command. It is a function, not a variable, so
// help can read it without an initialization cycle.
func commandTable() []command {
	return []command{
		{
			name: "plan", group: groupProject,
			summary: "preview where each service would run; changes nothing",
			detail: `Usage: haco plan [service...] [-f file] [-p name] [flags]

Previews a deploy without changing anything: the node each service would land on, why, and any
warnings or rejections.

` + composeFlagHelp + `
  --json                  print the raw API response instead of a table
  --fresh                 preview what 'deploy --fresh' would do instead of reusing existing nodes

The older form 'haco plan <project> <file>' still works.`,
			run: plan,
		},
		{
			name: "build", group: groupProject,
			summary: "build the images only; nothing is scheduled or started",
			detail: `Usage: haco build [service...] [-f file] [-p name] [flags]

Builds the images of services that have a build: block, without scheduling or starting anything.

` + composeFlagHelp + `
  --no-cache              force a full rebuild

The older form 'haco build <project> <file>' still works.`,
			run: buildCmd,
		},
		{
			name: "deploy", group: groupProject,
			summary: "build, schedule and start a project, then watch it come up",
			detail: `Usage: haco deploy [service...] [-f file] [-p name] [flags]

Builds, schedules and starts a Compose project (like 'docker compose up -d'), then shows a live
status table until every service is ready, failed or stopped. Redeploying replaces what is running.

` + composeFlagHelp + `
  --no-cache              force a full rebuild
  --fresh                 let every currently unpinned service be rescheduled freely instead of
                          reusing its node; abandons its data there (asks first)
  -y, --yes               answer yes to the data-loss questions

Asks first when a pinned service needs a fresh volume.
The older form 'haco deploy <project> <file>' still works.`,
			run: deploy,
		},
		{
			name: "ps", group: groupProject,
			summary: "list services with their node and status",
			detail: `Usage: haco ps [project] [service]

With no arguments, lists every service of every project: node, status, published ports,
container and, for a failure, the reason. With a project, only that project's services.
With a project and a service, prints that one service's full detail as JSON.`,
			run: ps,
		},
		{
			name: "logs", group: groupProject,
			summary: "show a service's logs; -f follows them",
			detail: `Usage: haco logs <project> <service> [--tail N] [-f]

Shows the container's logs from whichever node runs it. If the container failed to start, shows the
tail captured at that moment.

  --tail N, -n N          lines to show (default 200)
  -f, --follow            keep following new output`,
			run: logs,
		},
		{
			name: "stop", group: groupProject,
			summary: "stop a project's services (resume starts them again)",
			detail: `Usage: haco stop <project> [service...]
       haco stop --all [--yes]

Stops the named services, or every service of the project. The container is removed but the
service keeps its node and data, so 'haco resume' starts it again where it was.

  --all                   stop every service of every project (asks first)
  -y, --yes               skip the question`,
			run: stop,
		},
		{
			name: "resume", group: groupProject,
			summary: "start stopped services again where they last ran",
			detail: `Usage: haco resume <project> [service...]

Starts stopped or failed services again on the nodes they last ran on. No rebuild, no rescheduling.`,
			run: resume,
		},
		{
			name: "restart", group: groupProject,
			summary: "recreate the containers of running services in place",
			detail: `Usage: haco restart <project> [service...]

Recreates the containers of running services on their current nodes.`,
			run: restart,
		},
		{
			name: "rm", group: groupProject,
			summary: "delete services' records (more than stopping them)",
			detail: `Usage: haco rm <project> [service...]
       haco rm --all [--yes]

Permanently deletes the workload records of the named services. With no services, deletes every
record of the project; with --all, every record of every project. Stop keeps a record; rm does not.

  --all                   delete every record of every project
  -y, --yes               skip the question asked before deleting a whole project or everything`,
			run: rm,
		},
		{
			name: "projects", group: groupProject,
			summary: "list projects with a status summary",
			detail:  `Usage: haco projects`,
			run:     func(u string, a []string) error { return projects(u, a) },
		},
		{
			name: "services", group: groupProject,
			summary: "show where a service name currently resolves",
			detail: `Usage: haco services <project> [service]

Resolves a logical service name to its current endpoints: node, address, port and status.`,
			run: services,
		},

		{
			name: "start", group: groupCluster,
			summary: "start the control plane in the background",
			detail: `Usage: haco start [--web] [--registry] [--foreground] [--logs | --tui]

Starts the control plane in the background. Each node builds its own images.

  --web                   also serve the web console and print its URL
  --registry              run the local build registry instead of building on each node
  -f, --foreground        run attached to this terminal
  --logs                  tail the control plane's log right after starting
  --tui                   open the terminal dashboard once ready

--logs, --tui and --foreground cannot be combined.`,
			run: start,
		},
		{
			name: "stop-cluster", group: groupCluster,
			summary: "stop only the control plane started by 'haco start'",
			detail: `Usage: haco stop-cluster

Stops the control plane process that 'haco start' launched. Services keep running on their nodes.
To stop everything, use 'haco shutdown'.`,
			run: func(_ string, a []string) error { return stopControlPlane(a) },
		},
		{
			name: "shutdown", group: groupCluster,
			summary: "stop everything: services, agents, then the control plane",
			detail: `Usage: haco shutdown [--yes]

Stops the whole cluster in order: every node's containers (stopped, not removed) and agent, then
the control plane. 'haco start' and 'haco agent run' bring the same services back.

  -y, --yes               skip the question`,
			run: shutdown,
		},
		{
			name: "cluster", group: groupCluster,
			summary: "cluster-wide operations (cluster reset, cluster refresh)",
			detail: `Usage: haco cluster reset|refresh [--yes]

  reset                   stop every service and revoke every node's credential (this machine's own
                          node is kept): the 'start completely clean again' command
  refresh                 use after this machine's IP changed: revoke every node (this machine's
                          too), drop its saved credential and stop the control plane. Services keep
                          running. 'haco start' then re-joins this machine at its new address; every
                          other node joins again with a fresh token

  -y, --yes               skip the question`,
			run: cluster,
		},

		{
			name: "nodes", group: groupNodes,
			summary: "list the nodes",
			detail:  `Usage: haco nodes`,
			run:     func(u string, _ []string) error { return nodes(u) },
		},
		{
			name: "node", group: groupNodes,
			summary: "inspect and manage one node (show, cordon, rename, ...)",
			detail: `Usage: haco node <command> [args]

  show <id>               one node's full detail ('haco node <id>' also works)
  join-token              print a one-time command that joins another machine
  cordon <id>             keep new services off this node; running ones continue
  uncordon <id>           allow new services again
  rename <id> <name>      set a display name ("" clears it); the ID never changes
  limit <id> --cpus N --memory SIZE
                          cap what the scheduler may reserve, e.g. --cpus 4 --memory 8GiB
                          (--clear removes the cap)
  revoke <id>             revoke the node's credential; it stays listed but can't come back
  rm <id> [--force]       delete the node and its credential; refused while it still has
                          running services unless --force`,
			run: node,
		},

		{
			name: "agent", group: groupAgent,
			summary: "join this machine to a cluster, or run its agent",
			detail: `Usage: haco agent join <token>
       haco agent run

  join <token>            join this machine as a worker node (a one-time token from 'haco start'
                          or 'haco node join-token')
  run                     run this machine's already joined node; reuses the saved credential`,
			run: func(_ string, a []string) error { return agentCmd(a) },
		},

		{
			name: "tui", group: groupViews,
			summary: "terminal dashboard that refreshes by itself",
			detail: `Usage: haco tui [project]

A read-only dashboard with tabs for nodes, workloads, containers, events, metrics, services and
projects.`,
			run: runTUI,
		},
		{
			name: "events", group: groupViews,
			summary: "recent audit events",
			detail:  `Usage: haco events [project]`,
			run:     func(u string, a []string) error { return getOptionalProject(u, "/v1/events", a) },
		},
		{
			name: "metrics", group: groupViews,
			summary: "node and workload counts",
			detail:  `Usage: haco metrics [project]`,
			run:     func(u string, a []string) error { return getOptionalProject(u, "/v1/metrics", a) },
		},
	}
}

func findCommand(name string) *command {
	for _, c := range commandTable() {
		if c.name == name {
			c := c
			return &c
		}
	}
	return nil
}

// alias is an older command name that still works. It rewrites the old
// invocation into the new command line.
type alias struct {
	to      func(args []string) []string // the new command line, without "haco"
	newName string                       // what the notice tells people to type
}

func hasFlag(args []string, names ...string) (bool, []string) {
	found := false
	var rest []string
	for _, a := range args {
		hit := false
		for _, n := range names {
			if a == n {
				hit = true
			}
		}
		if hit {
			found = true
		} else {
			rest = append(rest, a)
		}
	}
	return found, rest
}

func prefix(words ...string) func([]string) []string {
	return func(args []string) []string { return append(append([]string{}, words...), args...) }
}

// scopedOrAll turns "[project] [--yes]" into "<cmd> <project>" or
// "<cmd> --all", keeping --yes.
func scopedOrAll(cmd string) func([]string) []string {
	return func(args []string) []string {
		yes, rest := hasFlag(args, "--yes", "-y")
		out := []string{cmd}
		if len(rest) == 0 {
			out = append(out, "--all")
		} else {
			out = append(out, rest...)
		}
		if yes {
			out = append(out, "--yes")
		}
		return out
	}
}

// aliases maps every renamed command to its new form.
var aliases = map[string]alias{
	"status":               {prefix("ps"), "haco ps"},
	"workloads":            {prefix("ps"), "haco ps"},
	"containers":           {prefix("ps"), "haco ps"},
	"stop-containers":      {scopedOrAll("stop"), "haco stop <project>"},
	"remove-workload":      {prefix("rm"), "haco rm <project> <service>"},
	"remove-all-workloads": {scopedOrAll("rm"), "haco rm <project> (or haco rm --all)"},
	"stop-all":             {prefix("stop-cluster"), "haco stop-cluster"},
	"teardown":             {prefix("cluster", "reset"), "haco cluster reset"},
	"join-token":           {prefix("node", "join-token"), "haco node join-token"},
	"revoke-node":          {prefix("node", "revoke"), "haco node revoke <id>"},
	"remove-node":          {prefix("node", "rm"), "haco node rm <id>"},
}

// renameNotice is printed on stderr when an old name is used.
func renameNotice(old string, a alias) string {
	return fmt.Sprintf("%q is now %q (the old name still works for now)", old, a.newName)
}

func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}

// usage prints the command overview.
func usage() { printOverview(os.Stdout) }

func printOverview(w io.Writer) {
	fmt.Fprintln(w, "haco: run a Docker Compose project across the machines you already have")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: haco <command> [args]     haco help <command> shows a command's flags")
	byGroup := map[string][]command{}
	for _, c := range commandTable() {
		byGroup[c.group] = append(byGroup[c.group], c)
	}
	for _, g := range groupOrder {
		fmt.Fprintln(w)
		if note := groupNotes[g]; note != "" {
			fmt.Fprintf(w, "%s (%s)\n", g, note)
		} else {
			fmt.Fprintln(w, g)
		}
		for _, c := range byGroup[g] {
			fmt.Fprintf(w, "  %-13s %s\n", c.name, c.summary)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The control plane is at $HACO_CONTROLPLANE_URL (default http://localhost:8099).")
}

// printHelp implements `haco help [command]`.
func printHelp(w io.Writer, args []string) error {
	if len(args) == 0 {
		printOverview(w)
		return nil
	}
	name := args[0]
	if a, ok := aliases[name]; ok {
		fmt.Fprintf(w, "%s\n\n", renameNotice(name, a))
		name = a.to(nil)[0]
	}
	c := findCommand(name)
	if c == nil {
		return fmt.Errorf("unknown command %q; run 'haco help' to list commands", args[0])
	}
	fmt.Fprintln(w, strings.TrimSpace(c.detail))
	return nil
}
