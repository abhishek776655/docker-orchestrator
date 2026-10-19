# M2 — Single-Node Execution

## Goal

Submit a workload through a real control-plane API and have it actually run as a
container on one real node, via the real Docker Engine API (SRS milestone M2).

## Implemented

- [x] Control-plane HTTP API (`controlplane/`): register node, heartbeat, submit
      workload, get workload status, agent assignment feed, agent status reports.
- [x] PostgreSQL metadata store (`store/`): nodes, workloads, events; reservations
      are derived by summing non-terminal workloads per node rather than kept as
      a separate table, so there is one source of truth (NFR-001).
- [x] M1 scheduler wired in: every submit rebuilds a `scheduler.Scheduler` from
      current DB state (`store.ListNodes` + `store.ActiveReservations`) and seeds
      it via the new `Scheduler.SeedReservation`, so placement decisions always
      reflect persisted state, not in-memory drift.
- [x] Node agent (`agent/`): registers using real Docker-reported CPU/memory
      capacity, heartbeats, polls for assignments (pull model — no inbound RPC
      in M2), pulls images, creates/starts containers, reports
      `pulling → starting → running → ready` (or `failed` with a reason) back to
      the control plane.
- [x] Idempotent container reuse: `Docker.EnsureRunning` names containers
      deterministically from the workload key and reuses an existing one instead
      of creating a duplicate (FR-009).
- [x] `cmd/controlplane`, `cmd/agent` entrypoints.

## Verified end-to-end (this machine)

1. Postgres via `docker run ... postgres:16-alpine` on `:55432`.
2. `bin/controlplane` (`:8099`), `bin/agent` (against local Docker Desktop socket).
3. `POST /v1/workloads` with a real `nginx:alpine` image → scheduler selected the
   sole node → agent pulled the image, created/started `haco-demo-web-r1`, and
   the workload reached `status: ready` with a real container ID.
4. `docker exec` into the container confirmed nginx itself serves traffic
   internally.

## Known M2 simplifications (intentionally deferred, not bugs)

- Host ports are bound 1:1 to the same container port (no separate
  container-port field in the model yet). Revisit once the Compose importer
  (M4) supplies real port mappings.
- No real readiness probe — a running container is immediately marked `ready`.
  Healthcheck handling is part of Compose import (M4).
- Agent uses pull-based polling, not push RPC; node identity is a plain string,
  not mTLS. Both are explicitly deferred to M3 per the SRS open-decisions list.
- Single node only; M3 adds the second node, service discovery, and gateway.

## Exit criterion

Met: submit a workload via the API, it runs through the platform end-to-end on
one real Docker host, and status is observable throughout.
