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
haco start --web
```

It prints the web console URL (`http://localhost:8099/`) and a one-time join
command. The control-plane machine joins the cluster as a node by itself.

**3. Join the other machines** by running the join command on each:

```bash
haco agent join '<token>'
```

**4. Deploy a Compose project** from the console's **Deploy** page, or:

```bash
cd ~/projects/myapp                      # the folder with your Compose file
haco plan                                # dry run: where each service goes and why
haco deploy                              # build, place, start, show live status
```

The project is named after the folder (`myapp`); use `-p` to name it
something else, or `-f` for a Compose file with another name.

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

`./install.sh` installs `controlplane`, `agent` and `haco` into
`$(go env GOPATH)/bin` and, when npm is available, builds the web console
into `~/.haco/web`. Make sure that bin folder is on your `PATH`.

### Prebuilt binaries (no Go needed)

Each tagged release has `haco_<version>_<os>_<arch>.tar.gz` for macOS and
Linux on arm64 and amd64, with `haco`, `agent` and `controlplane` inside,
plus `haco-web.tar.gz` for the web console and `checksums.txt`.

```bash
tar -xzf haco_<version>_darwin_arm64.tar.gz -C /usr/local/bin haco agent controlplane
mkdir -p ~/.haco/web && tar -xzf haco-web.tar.gz -C ~/.haco/web   # control-plane machine only
haco version
```

Releases are cut by pushing a tag: `git tag v0.1.0 && git push origin v0.1.0`
(`.github/workflows/release.yml` runs GoReleaser with `.goreleaser.yaml`).
A binary built from source reports its version as `dev`.

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

**Web console** (`haco start --web`)
- Dashboard with what needs attention, a notification bell for new failures.
- Cluster topology: the control-plane machine in the middle, nodes around
  it with their OS, state and temperature, and machines on the network that
  have not joined yet.
- Deploy wizard: upload a Compose file, pick the project folder, choose
  what to keep on the host, then review the plan and dependency graph,
  change placements and deploy with live status.
- Project, service and node pages; a searchable, paginated event history.

## Setting up a cluster

- Add a machine you can SSH into with one command (needs Docker running on
  it; macOS or Linux):

  ```bash
  haco node add user@192.168.1.20 [--ssh-key ~/.ssh/id_ed25519] [--port 22]
  ```

  It copies the agent (the one next to your `haco` when the machine has the
  same OS and CPU, otherwise the release archive for `--version <tag>`,
  checked against `checksums.txt`), joins with a one-time token and runs
  the agent as a service: a systemd user unit on Linux (plus `loginctl
  enable-linger`, which may need `sudo`), a LaunchAgent on macOS (the user
  must be logged in). Logs: `~/.haco/logs/agent.log` on macOS,
  `journalctl --user -u haco-agent` on Linux.
- Join tokens expire after 10 minutes. Get a fresh one any time from
  **Add node** in the console or with `haco node join-token`.
- Keep the control plane running across reboots and logouts, instead of
  starting it by hand:

  ```bash
  haco start --service [--web] [--registry]
  haco service install agent --env-file keys.env   # a joined worker, same idea
  haco service status controlplane
  haco service uninstall controlplane
  ```

  A LaunchAgent on macOS, a systemd user unit on Linux; it restarts if the
  process dies. Every `HACO_*` variable in your shell, plus the lines of
  `--env-file` (e.g. the keys your Compose `${VARS}` read), are copied into
  the service file (mode 0600), so run the install again to change them.
  While it is installed, `haco stop-cluster` stops it and `haco start`
  starts it again. On Linux, `sudo loginctl enable-linger $USER` keeps it
  running after logout.

- After a machine has joined once, restart its agent without a token:

  ```bash
  haco agent run
  ```

- Send a local image to nodes that can't pull it (a private image, a local
  build, no registry):

  ```bash
  haco image push myapp:dev --node laptop-2 --node mini
  haco image push myapp:dev --all
  ```

  It streams `docker save` to each node's agent, which runs `docker load`;
  the image then shows in the node's cached images and the scheduler
  prefers that node. Nodes that already have it are skipped, and so are
  nodes with a different CPU architecture unless you add `--any-arch`.

- `haco plan` never changes anything; use it to check placements before
  a deploy. `haco deploy` shows live status until every service is ready
  or failed.

## Everyday commands

Run these from the folder that holds your Compose file. `-f` defaults to the
Compose file there and `-p` (the project name) to the folder's name, as in
`docker compose`; pass either to use something else.

| Task | Command |
| --- | --- |
| Start the control plane (with the web console) | `haco start --web` |
| Stop the control plane only | `haco stop-cluster` |
| Dry-run a deploy | `haco plan` |
| Deploy or redeploy | `haco deploy` |
| Deploy one service and what it depends on | `haco deploy <service>` |
| Let the scheduler place everything again | `haco deploy --fresh` |
| Keep services on the control-plane machine | `--pin-host db,cache` on plan or deploy |
| Pin services to a node | `--pin-node <node-id>=api,web` on plan or deploy |
| Another file or project name | `haco deploy -f other.yml -p myproject` |
| Services and where they run | `haco ps [project]` |
| Projects | `haco projects` |
| Live logs | `haco logs <project> <service> -f` |
| Every service's logs, name-prefixed | `haco logs <project> [-f]` |
| A shell or command in a container | `haco exec <project> <service> [-- cmd ...]` |
| Reach a service's port from this machine | `haco port-forward <project> <service> [local:]remote` or `--all` |
| Stop / start again / restart | `haco stop <project> [service...]`, `haco resume <project> [service...]`, `haco restart <project> [service...]` |
| Remove a project's services | `haco rm <project>` (or `haco rm <project> <service>`) |
| Nodes | `haco nodes`, `haco node show <id>` |
| Cordon / uncordon a node | `haco node cordon <id>`, `haco node uncordon <id>` |
| Name a node | `haco node rename <id> "Studio Mac"` |
| Cap a node's resources | `haco node limit <id> --cpus 4 --memory 8GiB` |
| Remove a node | `haco node rm <id>` |
| Terminal dashboard | `haco tui` |
| Audit events | `haco events [project]` |
| Shut the whole cluster down | `haco shutdown` |
| Stop everything and revoke every node | `haco cluster reset` |

The older command names (`status`, `workloads`, `containers`, `stop-all`, `teardown`,
`join-token`, `revoke-node`, `remove-node`, `remove-workload`, `remove-all-workloads`,
`stop-containers`) and the `haco deploy <project> <file>` form still work for now and
print what to use instead.

Run `haco help` for every command, and `haco help <command>` for its flags.

## Compose extensions

Optional `x-haco-*` keys on a service adjust placement without changing how
Docker reads the file:

| Key | Effect |
| --- | --- |
| `x-haco-pin-host: true` | Always run on the control-plane machine |
| `x-haco-pin-node: <node-id>` | Always run on that node |
| `x-haco-place-anywhere: true` | Opt out of the automatic bind-mount handling; place it like any other service |
| `x-haco-allow-fresh-volume: true` | If its data's node is lost, reschedule onto an empty volume instead of failing |
| `x-haco-group: <name>` | Services sharing a name talk constantly; they are kept together (see below) |
| `x-haco-bind-host-path: true` | Also ship other bind-mounted folders with the service (mounted live when it is pinned to the control-plane machine) |

Services that talk to each other constantly (the Kafka family — Kafka,
ZooKeeper, Schema Registry, ksql — found by image name, or any services you
give the same `x-haco-group`) are pinned to the control-plane machine
together by default, because splitting them across Wi-Fi costs a network hop
per message. `--unpin <service,...>` on `plan` and `deploy` lets the scheduler
place one elsewhere; the plan then warns what it is separated from, and the
scheduler prefers the node that already holds the rest of its group. An
explicit `--pin-host` list means you made your own choice, so the default
steps aside.

## How it works

```
             operator: haco / web console
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
| `HACO_CONTROLPLANE_URL` | `http://localhost:8099` | Where `haco` finds the control plane |
| `HACO_LISTEN_ADDR` | `127.0.0.1:8099` | Operator API and web console address |
| `HACO_AGENT_LISTEN_ADDR` | `:8443` | Agent API (mTLS) |
| `HACO_DATA_DIR` | `~/.haco` | Database, credentials, TLS identity, volumes |
| `HACO_STASH_DIR` | system temp dir | Where the control plane keeps the tarballs (build contexts, volume seeds, snapshots) agents fetch; they live on disk, not in memory, and are removed at the next start |
| `HACO_ADOPT_DOCKER_VOLUMES` | on | Set to `0` to stop seeding a named volume from the Docker volume `docker compose up` would have created |
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
