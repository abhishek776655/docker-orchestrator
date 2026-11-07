# haco: hardware-aware container orchestrator

Run a Docker Compose project across the machines you already have. haco
turns a few laptops, desktops or small servers on one network into a single
cluster: you hand it a `docker-compose.yml`, and it places every service on
the machine that fits it best, by CPU, memory, GPU, temperature, cached
images and where its data already lives. Then it builds, starts and watches
the services in dependency order.

No Kubernetes, no YAML rewrite, no registry to run. One control plane, one
agent per machine, a CLI and a web console.

## Quick start

You need Docker on every machine and Go 1.26+ to build (Node.js and npm
only for the web console). All machines must be on the same network.

**1. Install** on every machine you want in the cluster:

```bash
git clone https://github.com/abhishek776655/docker-orchestrator.git
cd docker-orchestrator
./install.sh
```

**2. Start the control plane** on one machine:

```bash
hacoctl start --web
```

It prints the web console URL (`http://localhost:8099/`) and a one-time join
command. The control-plane machine joins the cluster as a node by itself.

**3. Join the other machines** by running the join command on each:

```bash
hacoctl agent join '<token>'
```

**4. Deploy a Compose project** from the console's **Deploy** page, or:

```bash
hacoctl plan myapp ./docker-compose.yml     # dry run: where each service goes and why
hacoctl deploy myapp ./docker-compose.yml   # build, place, start, show live status
```

Details on each step are under [Install](#install) and
[Setting up a cluster](#setting-up-a-cluster) below.

## Requirements

- Docker on every machine (Docker Desktop, Colima, Rancher Desktop or plain
  Docker on Linux). The socket is found automatically.
- Go 1.26 or newer to build.
- Node.js and npm, only for the web console.
- Machines on the same network. The control plane listens on
  `127.0.0.1:8099` for operators and on port `8443` for agents.

## Install

`./install.sh` installs `controlplane`, `agent` and `hacoctl` into
`$(go env GOPATH)/bin` and, when npm is available, builds the web console
into `~/.haco/web`. Make sure that bin folder is on your `PATH`.

## Features

**Scheduling**
- Places services by hard rules first (architecture, GPU, free CPU and
  memory, host ports, cordoned or offline nodes, pins), then scores the
  rest on CPU fit, memory fit, thermal headroom and image cache.
- Explains every decision: each node's score, or the reason it was rejected.
- Respects `depends_on` and `condition: service_healthy`; services start in
  stages.
- Keeps stateful services next to their data. A service with a named volume
  stays on the node that holds it, and every console deploy warns before a
  service would start on an empty volume.
- Operator overrides: keep services on the control-plane machine, pin a
  service to a node, place a service on a node the scheduler rejected, or
  cap how much of a node the scheduler may use.

**Compose support**
- Builds `build:` services on the node that runs them, from the shipped
  build context (honors `.dockerignore`, build args and `build.ssh`). An
  optional local registry is available with `--registry`.
- Images, ports, environment, env files, healthchecks, named volumes and
  bind mounts. Bind-mounted folders are copied to whichever node runs the
  service.
- Service names resolve across machines, so `http://auth:5100` works from
  any node.
- See the [Compose compatibility matrix](docs/compose-compatibility-matrix.md)
  for the exact subset.

**Operations**
- Machines join with a one-time token over mutual TLS; each keeps a
  permanent node ID and can be given a display name.
- Heartbeats and a reconciler: workloads on a lost node are rescheduled,
  while stateful ones are never silently duplicated.
- Start, stop, restart, redeploy and remove projects or single services;
  live logs from whichever node runs a container.
- Node maintenance: cordon, rename, resource limits, delete cached images
  or old volume data, revoke or remove a node.
- A clean cluster shutdown that brings the same services back on restart.

**Web console** (`hacoctl start --web`)
- Dashboard with what needs attention, a notification bell for new failures.
- Cluster topology: the control-plane machine in the middle, nodes around
  it with their OS, state and temperature, and machines on the network that
  have not joined yet.
- Deploy wizard: upload a Compose file, pick the project folder, choose
  what to keep on the host, then review the plan and dependency graph,
  change placements and deploy with live status.
- Project, service and node pages; a searchable, paginated event history.

## Setting up a cluster

- Join tokens expire after 10 minutes. Get a fresh one any time from
  **Add node** in the console or with `hacoctl join-token`.
- After a machine has joined once, restart its agent without a token:

  ```bash
  hacoctl agent run
  ```

- `hacoctl plan` never changes anything; use it to check placements before
  a deploy. `hacoctl deploy` shows live status until every service is ready
  or failed.

## Everyday commands

| Task | Command |
| --- | --- |
| Start the control plane (with the web console) | `hacoctl start --web` |
| Stop the control plane | `hacoctl stop-all` |
| Dry-run a deploy | `hacoctl plan <project> <compose.yml>` |
| Deploy or redeploy | `hacoctl deploy <project> <compose.yml>` |
| Deploy one service and what it depends on | `hacoctl deploy <project> <compose.yml> --service <name>` |
| Let the scheduler place everything again | `hacoctl deploy <project> <compose.yml> --fresh` |
| Keep services on the control-plane machine | `--pin-host db,cache` on plan or deploy |
| Pin services to a node | `--pin-node <node-id>=api,web` on plan or deploy |
| Projects and workloads | `hacoctl projects`, `hacoctl status [project]` |
| Live logs | `hacoctl logs <project> <service> -f` |
| Stop / start again / restart | `hacoctl stop <project> <service> latest`, `hacoctl resume <project> [service]`, `hacoctl restart <project> [service]` |
| Remove a project | `hacoctl remove-all-workloads <project>` |
| Nodes | `hacoctl nodes`, `hacoctl node <id>` |
| Cordon / uncordon a node | `hacoctl node cordon <id>`, `hacoctl node uncordon <id>` |
| Name a node | `hacoctl node rename <id> "Studio Mac"` |
| Cap a node's resources | `hacoctl node limit <id> --cpus 4 --memory 8GiB` |
| Remove a node | `hacoctl remove-node <id>` |
| Terminal dashboard | `hacoctl tui` |
| Audit events | `hacoctl events [project]` |
| Shut the whole cluster down | `hacoctl shutdown` |

Run `hacoctl help` for every command and flag.

## Compose extensions

Optional `x-haco-*` keys on a service adjust placement without changing how
Docker reads the file:

| Key | Effect |
| --- | --- |
| `x-haco-pin-host: true` | Always run on the control-plane machine |
| `x-haco-pin-node: <node-id>` | Always run on that node |
| `x-haco-place-anywhere: true` | Opt out of the automatic bind-mount handling; place it like any other service |
| `x-haco-allow-fresh-volume: true` | If its data's node is lost, reschedule onto an empty volume instead of failing |
| `x-haco-bind-host-path: true` | Also ship other bind-mounted folders with the service (mounted live when it is pinned to the control-plane machine) |

## How it works

```
             operator: hacoctl / web console
                          |
                          v
   +-------------------- control plane --------------------+
   |  Compose importer -> scheduler -> SQLite state         |
   |  reconciler, build context store, service registry     |
   +--------------------------------------------------------+
            ^ mTLS                ^ mTLS               ^ mTLS
            |                     |                    |
        agent (host)          agent (node)         agent (node)
        Docker                Docker               Docker
```

- The **control plane** parses the Compose file, schedules each service and
  stores desired state in an embedded SQLite file (`~/.haco/control-plane.db`).
- Each **agent** heartbeats with its capacity, temperature and cached
  images, polls for its assigned workloads, builds or pulls images, runs
  the containers and reports status back.
- The **reconciler** watches heartbeats and moves work off lost nodes.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `HACO_CONTROLPLANE_URL` | `http://localhost:8099` | Where `hacoctl` finds the control plane |
| `HACO_LISTEN_ADDR` | `127.0.0.1:8099` | Operator API and web console address |
| `HACO_AGENT_LISTEN_ADDR` | `:8443` | Agent API (mTLS) |
| `HACO_DATA_DIR` | `~/.haco` | Database, credentials, TLS identity, volumes |
| `HACO_NODE_ID` | saved in `~/.haco/node-id` | Override this machine's node ID |
| `HACO_REGISTRY_ADDR` | unset | Use a build registry (`auto` starts a local one) |

## Developing

```bash
go test ./...
```

```bash
cd web && npm install && npm test && npm run dev
```

The dev server proxies `/v1` to `http://127.0.0.1:8099` (override with `HACO_API`).

## Documentation

- [Compose compatibility matrix](docs/compose-compatibility-matrix.md)
- [Joining machines over a LAN](docs/testing/agent-join.md)
- [Service-name DNS](docs/testing/service-name-dns.md)
- [Backup and restore](docs/operations/backup-and-restore.md)
- [Architecture decisions](docs/adr/0001-initial-architecture-decisions.md) and [core contracts](docs/contracts/core-contracts.md)
- [Roadmap](docs/planning/roadmap.md) and the milestone plans in [docs/planning](docs/planning)

## Not goals

Kubernetes compatibility, live container migration, database failover and
custom overlay networks are out of scope. haco aims to be the simplest way
to run one Compose project across a handful of machines you own.

## License

[MIT](LICENSE) © 2026 Abhishek Pandey
