# M3 — Two-Node Connectivity

## Goal

Prove that an application scheduled on one node can reach a dependency
scheduled on a different node by stable logical service name, with no
hard-coded node address — without needing a third node (SRS milestone M3,
acceptance criterion in §17).

## Implemented

- [x] `model.Workload.DependsOn`: logical service names a workload needs
      ready before it starts (SRS 7.1 `depends_on`, FR-014, UC-05).
- [x] `model.ServiceInstance` + `store.ListServiceInstances` /
      `GET /v1/services/{project}/{service}`: the service registry (SRS
      8.1–8.2). There is no separate registry table — a `ready` workload
      row joined to its node's address *is* the registry entry, keeping
      one source of truth (same principle as M2's derived reservations).
- [x] Dependency gating + endpoint injection in the agent
      (`agent/agent.go: resolveDependencies`): before executing a
      workload, the agent resolves every `DependsOn` entry against the
      registry. If any dependency has no ready instance yet, the
      workload is marked `waiting_dependencies` and retried next poll
      (UC-05). Once all are ready, `<SERVICE>_HOST` / `<SERVICE>_PORT`
      env vars are injected — resolved live, at execution time, never
      hard-coded (UC-06, FR-012).
- [x] `model.Workload.Command`: optional entrypoint override, needed
      because of the M2 1:1 host-port simplification — an image whose
      default command listens on its own fixed port (e.g. Redis on 6379)
      needs telling to listen on the port the scheduler actually reserved.

## Verified end-to-end (this machine, two simulated nodes)

Two agent processes against the same control plane, deliberately given
distinct node identity/address to stand in for two hosts (see
"Limitations" below):

```
HACO_NODE_ID=node-a HACO_NODE_ADDRESS=host.docker.internal ./bin/agent
HACO_NODE_ID=node-b HACO_NODE_ADDRESS=127.0.0.2             ./bin/agent
```

1. Submitted `cache` (Redis), pinned to `node-a`.
2. Submitted `backend` (nginx), pinned to `node-b`, `depends_on: ["cache"]`.
3. `backend` reached `status: ready` only after `cache` did.
4. `GET /v1/services/demo3/cache` → `{"NodeID":"node-a","Address":"host.docker.internal","Port":16389,...}`.
5. Inside the `backend` container (node-b): `CACHE_HOST=host.docker.internal`,
   `CACHE_PORT=16389` — injected, not configured by hand.
6. `redis-cli -h "$CACHE_HOST" -p "$CACHE_PORT" ping` from inside `backend`
   → `PONG`. Real TCP connectivity across the two simulated nodes, resolved
   entirely through the registry.

## Known M3 simplifications / honest limitations

- **Single Docker daemon, not two hosts.** There is no second physical or
  virtual machine available in this environment, so "two nodes" are two
  agent processes against one Docker Engine, given distinct
  `HACO_NODE_ID`/`HACO_NODE_ADDRESS` values. The mechanism proven (registry
  lookup → injected endpoint → real TCP connection) is identical to the
  real multi-host case; only the network path is a local stand-in. A real
  two-host run needs each node's `HACO_NODE_ADDRESS` set to its actual
  routable LAN IP — no code change.
- **No DNS.** Resolution is the registry's generated-runtime-configuration
  path (SRS 8.3 second option), not the DNS-mechanism option. Chosen
  because it is far less code for an MVP; still an open decision per
  §18.1. A DNS-based resolver would remove the "recreate container to pick
  up a changed endpoint" limitation below.
- **No gateway yet.** Caddy/Traefik integration (SRS 8.4) is deferred —
  M3 proves internal TCP connectivity, not the HTTP gateway/dev-endpoint
  piece of the milestone. Gateway choice is still an open decision
  (§18.1).
- **Env is injected once, at container creation.** If a dependency's
  endpoint changes later (e.g. it's rescheduled), already-running
  dependents are not updated — they'd need to be recreated. This matches
  the "generated runtime configuration" tradeoff explained to the user
  earlier in this session; DNS resolution would avoid it.
- Host-port-to-container-port is still 1:1 (M2 simplification); workloads
  whose image doesn't default to that port need `Command` to tell it to
  (as done here for Redis).

## Exit criterion

Met: an application on one simulated node reaches a dependency on another
by logical service name, with the endpoint resolved at runtime — no
hard-coded node IP anywhere in the workload submission.
