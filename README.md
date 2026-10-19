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

## Running the M2 prototype

```bash
# 1. Metadata store
docker run -d --name haco-postgres -e POSTGRES_PASSWORD=haco -e POSTGRES_DB=haco -p 55432:5432 postgres:16-alpine

# 2. Control plane
go build -o bin/controlplane ./cmd/controlplane
HACO_LISTEN_ADDR=:8099 ./bin/controlplane &

# 3. Node agent (uses your local Docker daemon)
go build -o bin/agent ./cmd/agent
HACO_CONTROLPLANE_URL=http://localhost:8099 HACO_NODE_ID=node-local ./bin/agent &

# 4. Submit a workload
curl -X POST http://localhost:8099/v1/workloads -H 'Content-Type: application/json' -d '{
  "project": "demo", "service": "web", "revision": "r1",
  "image": "nginx:alpine", "cpu_millis": 250, "memory_bytes": 67108864,
  "architecture": "amd64", "host_ports": [18080], "storage_class": "ephemeral"
}'

# 5. Check status
curl http://localhost:8099/v1/workloads/demo/web/r1
```

## Explicit non-goals for the first release

Kubernetes compatibility, transparent container migration, arbitrary database failover, custom overlay networking, and GPU-aware placement are deferred until the two-node core is proven.
