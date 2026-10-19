# M0 — Design Baseline

## Objective

Freeze the safety-critical contracts needed to build and test a two-node orchestrator without revisiting fundamental behavior during implementation.

## Exit criteria

- [ ] The project name and repository layout are approved.
- [ ] The initial gateway, registry, and service-discovery choices are recorded.
- [ ] Workload, node, placement, and service-instance contracts are agreed.
- [ ] The supported Compose subset and validation behavior are documented.
- [ ] CPU/memory reservation semantics and system-reserved capacity are defined.
- [ ] Node identity, authentication, and secret-redaction expectations are defined.
- [ ] PostgreSQL backup and restore procedure has an owner and a documented test.

## Initial assumptions to validate

| Area | Proposed baseline | Decision needed |
| --- | --- | --- |
| Cluster | Exactly two trusted Linux/Docker nodes | Confirm target OS/version and network range |
| Control plane | Go HTTP API + PostgreSQL | Confirm deployment host |
| Agent transport | Authenticated RPC using mTLS | Select bootstrap/certificate rotation approach |
| Registry | Local OCI registry for development | Confirm local versus existing registry |
| Gateway | Caddy | Confirm Caddy or Traefik |
| Discovery | Generated service endpoint configuration for M3 | Decide whether internal DNS is needed in M3 |
| Storage | Node-local persistent volumes with affinity | Confirm host-path and ownership policy |
| Dev mounts | Deferred source synchronization | Confirm deferment or select mechanism |

## Decision sequence

1. Approve the target environment and security boundary.
2. Choose gateway, registry, and discovery baseline.
3. Approve resource and storage semantics.
4. Approve contracts and Compose compatibility behavior.
5. Convert the M1 backlog into implementation issues.
