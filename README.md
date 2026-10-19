# Hardware-Aware Container Orchestrator

A two-node, self-hostable platform that deploys a validated subset of Docker Compose workloads across Linux Docker hosts.

The first delivery is a deterministic scheduler simulator. It establishes placement safety and explainability before the project controls real containers.

## Delivery order

1. M0: architecture decisions and contracts
2. M1: scheduler simulator
3. M2: single-node execution
4. M3: two-node discovery, connectivity, and gateway
5. M4-M7: Compose builds, storage/dev workflow, operations, and hardware-aware scoring

## Planning documents

- [M0 baseline](docs/planning/m0-baseline.md)
- [Initial architecture decisions](docs/adr/0001-initial-architecture-decisions.md)
- [Core contracts](docs/contracts/core-contracts.md)
- [Compose compatibility matrix](docs/compose-compatibility-matrix.md)
- [M1 scheduler backlog](docs/planning/m1-scheduler-simulator.md)
- [M2 single-node execution](docs/planning/m2-single-node-execution.md)
- [M3 two-node connectivity](docs/planning/m3-two-node-connectivity.md)
- [M4 Compose importer and builds](docs/planning/m4-compose-importer-and-builds.md)
- [M5 storage and dev workflow](docs/planning/m5-storage-and-dev-workflow.md)
- [M6 reconciliation and operations](docs/planning/m6-reconciliation-and-operations.md)
- [Backup and restore](docs/operations/backup-and-restore.md)
- [Service-name DNS plan](docs/superpowers/plans/2026-10-19-service-name-dns.md) · [testing notes](docs/testing/service-name-dns.md)
- [M7 hardware-aware scheduling](docs/planning/m7-hardware-aware-scheduling.md)

## Running the MVP

This brings up the full M0-M7 path — control plane, two simulated nodes,
a registry, and a real Compose deployment with a build, a dependency, and
persistent storage. See [examples/mvp-demo](examples/mvp-demo) for the
Compose file used below.

> **One machine, two simulated nodes.** There's no second physical/VM host
> here, so "two nodes" are two agent processes against the same Docker
> daemon, told apart by `HACO_NODE_ID`/`HACO_NODE_ADDRESS`. On real
> hardware, point each agent at its own host's Docker socket and use that
> host's real IP for `HACO_NODE_ADDRESS` — nothing else changes. Known
> limitations of this single-daemon simulation (and the few others, like
> the 1:1 host/container port binding and service-name DNS needing a free
> port 53) are documented in the `docs/planning/m2`...`m7` files.

### 1. Dependencies

```bash
docker run -d --name haco-postgres -e POSTGRES_PASSWORD=haco -e POSTGRES_DB=haco -p 55432:5432 postgres:16-alpine
docker run -d --name haco-registry -p 5050:5000 registry:2
```

### 2. Build the binaries

```bash
go build -o bin/controlplane ./cmd/controlplane
go build -o bin/agent ./cmd/agent
```

### 3. Start the control plane

```bash
HACO_LISTEN_ADDR=:8099 \
HACO_REGISTRY_ADDR=localhost:5050 \
./bin/controlplane &
```

Logs confirm the build service, reconciler, and DNS resolver all started.

### 4. Register two nodes

```bash
HACO_CONTROLPLANE_URL=http://localhost:8099 HACO_NODE_ID=node-a HACO_NODE_ADDRESS=host.docker.internal ./bin/agent &
HACO_CONTROLPLANE_URL=http://localhost:8099 HACO_NODE_ID=node-b HACO_NODE_ADDRESS=127.0.0.2         ./bin/agent &

curl -s http://localhost:8099/v1/nodes | python3 -m json.tool
```

(On real hosts, each command runs on its own machine, no `DOCKER_HOST`
override needed, and `HACO_NODE_ADDRESS` is that host's real IP.)

### 5. Validate the Compose project (dry run, no side effects)

```bash
cd examples/mvp-demo
python3 -c "
import json
print(json.dumps({'project': 'mvpdemo', 'working_dir': '$(pwd)', 'yaml': open('docker-compose.yml').read()}))
" > /tmp/mvpdemo.json

curl -s -X POST http://localhost:8099/v1/compose/plan -d @/tmp/mvpdemo.json | python3 -m json.tool
```

Check `Rejections` is empty before continuing — a non-empty list means
something in the Compose file isn't supported yet (see
[compose-compatibility-matrix.md](docs/compose-compatibility-matrix.md)).

### 6. Deploy it

```bash
curl -s -X POST http://localhost:8099/v1/compose/deploy -d @/tmp/mvpdemo.json | python3 -m json.tool
```

This builds `api` from source, pushes it to the local registry, and
schedules both services — `cache` (Redis, pinned with node-local
persistent storage) and `api` (depends on `cache`, gated until it's
ready).

### 7. Check it's running

```bash
curl -s http://localhost:8099/v1/workloads/mvpdemo/cache/latest | python3 -m json.tool
curl -s http://localhost:8099/v1/workloads/mvpdemo/api/latest   | python3 -m json.tool
curl -s http://localhost:8099/v1/services/mvpdemo/cache          # the service registry
curl -s http://localhost:9090/                                   # the deployed api, directly
```

### 8. Try the operational endpoints

```bash
curl -s http://localhost:8099/v1/events   | python3 -m json.tool   # audit trail
curl -s http://localhost:8099/v1/metrics  | python3 -m json.tool   # node/workload counts
```

### 9. Prove reconciliation (optional)

Kill `node-a`'s agent process, wait ~15s past the heartbeat timeout, and
watch `cache` (pinned, stateful) get marked `failed` — never silently
duplicated — while any ephemeral workload on it gets rescheduled to
`node-b` automatically. `GET /v1/events` shows the whole sequence.

### 10. Clean up

```bash
pkill -f bin/controlplane; pkill -f bin/agent
docker rm -f haco-mvpdemo-cache-latest haco-mvpdemo-api-latest haco-postgres haco-registry
```

### A smaller single-call example

For a one-off workload without Compose:

```bash
curl -X POST http://localhost:8099/v1/workloads -H 'Content-Type: application/json' -d '{
  "project": "demo", "service": "web", "revision": "r1",
  "image": "nginx:alpine", "cpu_millis": 250, "memory_bytes": 67108864,
  "architecture": "amd64", "container_ports": [80], "host_ports": [80]
}'
curl http://localhost:8099/v1/workloads/demo/web/r1
```

## Explicit non-goals for the first release

Kubernetes compatibility, transparent container migration, arbitrary database failover, custom overlay networking, and GPU-aware placement are deferred until the two-node core is proven.
