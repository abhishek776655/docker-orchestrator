# M6 — Reconciliation and Operations

## Goal

Detect node/container failure, recover stateless workloads safely, never
duplicate a stateful writer, and make the system's state inspectable
(SRS milestone M6, §6.3, §13).

## Implemented

- [x] `controlplane.RunReconciler`: periodic loop (`HACO_RECONCILE_INTERVAL`,
      default 5s) that calls the already-existing but previously-unused
      `store.MarkStaleNodesUnavailable` (`HACO_HEARTBEAT_TIMEOUT`, default
      15s) — FR-015 is now actually enforced, not just implemented and
      dormant.
- [x] `store.WorkloadsOnUnavailableNodes` + `reconcileOnce`: for every
      non-terminal workload stuck on a now-unavailable node —
  - **Ephemeral**: rescheduled onto a healthy node via the same M1
    scheduler used for new submissions (one scheduler instance per tick,
    reused across workloads, so two orphans in the same tick can't be
    double-booked onto the same freed capacity).
  - **NodeLocalPersistent**: never relocated. Marked `failed` with an
    explicit reason. This is the FR-017 safety property M5 built the
    storage model around — a reconciler that "helpfully" restarted it
    elsewhere would be the exact bug the spec calls out.
- [x] `GET /v1/events`: the audit trail (already written to the `events`
      table since M2) is now actually queryable (FR-024).
- [x] `GET /v1/metrics`: node-availability and workload-status counts — a
      JSON summary, not a Prometheus exporter (see limitations).
- [x] Agent-side orphan cleanup: containers are now labeled
      (`haco.managed`, `haco.workload.key`) at creation. Every poll cycle,
      the agent lists its own managed containers and removes any whose
      workload is no longer assigned to it — e.g. the reconciler moved it
      elsewhere while this node was down, and the node later recovered
      (SRS UC-08: "reconciles actual state without blindly duplicating
      workload instances").
- [x] **Liveness self-healing** (found while testing, not originally
      planned): the agent only re-verified `pending`→`ready` transitions,
      never that an already-`ready` container was *still* running. Fixed:
      every poll, a `running`/`ready` workload's container is checked via
      `Docker.IsRunning`; if it's gone, the agent transparently
      re-executes (pull/create/start again) — SRS §6.3's "safe retries."

## Verified end-to-end (this machine, two simulated nodes)

1. Submitted an ephemeral `web` workload and a `node_local_persistent` `db`
   workload, both landing on node-a.
2. Killed node-a's agent process (simulating node loss). After the
   heartbeat timeout, `GET /v1/nodes` showed node-a `unavailable`.
3. Within one reconcile tick: `web` was reassigned to node-b and reached
   `ready` there; `db` was marked `failed` with
   `"node node-a unavailable; stateful workload is not relocated without
   verified data access"` — **not** restarted anywhere. `GET /v1/events`
   showed the full sequence (`reassigned` → `pulling` → ... → `ready` on
   node-b; `failed` for `db`).
4. Restarted node-a's agent (simulating node recovery): it correctly
   identified `web`'s container as an orphan (no longer assigned to it)
   and removed it.
5. **This exposed a real bug** in the local single-Docker-daemon
   simulation: node-b's "ready" container and node-a's "orphan" container
   were, physically, the *same* container (both nodes share one daemon
   here), so node-a's correct cleanup also removed node-b's live instance
   — while the control plane still reported `node-b / ready`.
   `docker ps` showed nothing for it, but the API disagreed with reality.
6. The **liveness self-healing fix above exists because of that finding**:
   restarting node-b's agent logged
   `"demo6/web/r1 reported ready but its container is gone; re-executing"`
   and the container came back — `docker ps` confirmed `Up`.

## Known M6 limitations

- **Single Docker daemon, not two hosts** (same limitation as M3/M4/M5):
  this is also what exposed the shared-container collision in step 5
  above. In a real two-host deployment this cannot happen — node-a's
  orphan and node-b's live container would be on physically separate
  machines, and node-a's cleanup would never touch node-b's container.
  The collision was a genuine artifact of the simulation method, not the
  reconciliation logic, but the liveness-recheck it led to is a real and
  independently valuable fix.
- **Liveness recheck is poll-interval granularity** (`HACO_RECONCILE
  _INTERVAL`/the agent's own poll period, 2s default) — not an
  instant crash notification.
- **No automatic node draining/cordon** (spec explicitly marks this
  optional follow-up work for M6).
- **Metrics are a JSON summary, not Prometheus** — SRS §13.2 names
  Prometheus + Grafana; wiring an actual exporter is deferred.
- A rescheduled ephemeral workload gets a **fresh container** (no state
  carried over), which is correct for ephemeral storage but worth being
  explicit about: this is not "live migration" (explicitly a non-goal,
  SRS §3.2).

## Exit criterion

Met: a lost node's stateless workload is recovered predictably on another
node; its stateful workload is never silently duplicated; the whole
sequence is visible via `/v1/events`.
