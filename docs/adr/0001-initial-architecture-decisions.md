# ADR-0001: Initial Architecture Decisions

**Status:** Proposed  
**Date:** 2026-10-19

## Context

The platform must safely operate a minimum two-node Docker cluster. MVP behavior must not rely on quorum, a third node, custom overlay networking, or automated storage failover.

## Decisions

1. The control plane and node agents will be implemented in Go.
2. Agents execute containers exclusively through the local Docker Engine API; the Docker socket is never exposed to the LAN.
3. PostgreSQL persists desired state, inventory, placements, service records, and audit events.
4. The scheduler applies hard eligibility filters before deterministic scoring and records every rejection/selection reason.
5. Workload operations are idempotent or reconciled from observed state after an ambiguous failure.
6. M3 networking uses trusted, routable node addresses and a health-aware service registry. Custom overlay networking is deferred.
7. Persistent host paths and local volumes create node affinity. Stateful workloads are never automatically moved when data availability is uncertain.
8. Gateway exposure is opt-in and applies only to explicitly exposed HTTP/HTTPS services.
9. Compose support is allowlisted; unsupported semantics block deployment or produce an explicit warning according to the compatibility matrix.
10. Build secrets use BuildKit secret/SSH mounts and are excluded from runtime images, metadata responses, and logs.

## Open choices

- Select Caddy or Traefik as the first gateway integration.
- Select local or existing OCI registry.
- Decide between generated endpoint configuration and internal DNS for M3.
- Define certificate bootstrap, rotation, and recovery.
- Define CPU/memory request, limit, and reservation semantics.
- Choose or defer development source synchronization.

## Consequences

The initial system favors correctness and explainability over broad compatibility. A two-node cluster can schedule, run, discover, and route workloads, but it cannot claim transparent failover for node-local state.
