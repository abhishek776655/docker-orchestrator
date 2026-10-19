# Core Contracts (M0)

These contracts are implementation-neutral. API paths and Protocol Buffer messages will be derived from them after M1.

## Node

| Field | Meaning |
| --- | --- |
| `id` | Stable, unique node identity |
| `address` | Private, routable agent address |
| `architecture` | Runtime architecture, initially `linux/amd64` |
| `capabilities` | Labels and supported runtime features |
| `allocatable` | Capacity after system reservations |
| `heartbeat_at` | Last authenticated heartbeat |
| `availability` | `available`, `unavailable`, or `disabled` |

## Workload

| Field | Meaning |
| --- | --- |
| `project`, `service`, `revision` | Stable logical identity and revision |
| `image_digest` | Immutable OCI image reference |
| `resources` | CPU/memory requests and limits |
| `constraints` | Architecture, labels, storage affinity, and required capabilities |
| `dependencies` | Services and readiness conditions |
| `storage_policy` | Ephemeral or node-local persistent |
| `exposure` | Internal, gateway HTTP, or explicitly published port |
| `desired_state` | `running` or `stopped` |

## Placement decision

Every decision must retain the workload revision, candidate nodes, rejection reasons, scoring inputs, reservation, selected node, timestamp, and correlation ID.

Hard rejection examples: incompatible architecture, insufficient allocatable CPU/memory, unavailable node, missing required label/capability, conflicting host port, or storage affinity mismatch.

## Runtime state

Desired state is the controller's intent. Observed state is agent-reported evidence. The controller must never infer success solely from a timed-out request.

`pending → assigned → pulling → starting → running → ready` is the normal progression. Failure states must include a machine-readable reason and a human-readable diagnostic reference.

## Service instance

Only ready instances may be published for traffic.

| Field | Meaning |
| --- | --- |
| `project`, `service`, `instance_id` | Logical and concrete identity |
| `node_id`, `endpoint`, `protocol` | Reachable destination |
| `health`, `readiness`, `last_seen` | Eligibility information |
