# Roadmap (post-M7)

Captured from discussion on 2026-10-20, not yet built. Nothing here changes
the M0-M7 delivery path or its acceptance criteria — this is what comes
after, prioritized loosely in the order discussed.

## 1. Storage: Postgres → embedded SQLite

Only the control plane ever touches the metadata store — it's a single
process, no concurrent writers — so Postgres's main strength (multi-writer
concurrency) isn't being used. Switching to an embedded, pure-Go SQLite
driver (no cgo) removes an entire bootstrap step (no `docker run postgres`)
with no loss of correctness at this scale.

- Replace `pgxpool` with a SQLite driver in `store/store.go`; same schema,
  same query surface, same `ON CONFLICT` semantics (SQLite supports
  `INSERT ... ON CONFLICT DO UPDATE` natively).
- `jsonb` columns become `text` (SQLite has no native JSON type, but
  `json_extract` exists if ever needed for querying inside them — not
  required today since all JSON columns are read back whole, not queried).
- DB file path becomes a config value (e.g. `HACO_DB_PATH`, default
  `~/.haco/control-plane.db`), replacing `HACO_POSTGRES_DSN`.
- `docs/operations/backup-and-restore.md` gets rewritten for file-copy
  backup instead of `pg_dump`/`pg_restore`.
- Revisit only if/when multiple control-plane replicas are ever wanted —
  out of scope for now, and not part of this project's design (single
  control plane is itself a documented MVP assumption, §18).

## 2. Bootstrap/CLI ergonomics

- **Auto-detect the Docker socket** in both `agent` and `controlplane`
  binaries, instead of requiring `DOCKER_HOST` to be set manually
  (currently needed on macOS/Docker Desktop, source of repeated friction
  in testing). Try the standard `/var/run/docker.sock` first, then
  Docker Desktop's per-user socket paths, then respect `DOCKER_HOST` if
  explicitly set (never override an explicit value).
- **Single bootstrap command for the control plane** — one invocation that
  starts Postgres... no, post-SQLite-migration there's no DB container to
  start at all, so this mostly becomes "does the registry need to run."
  Concretely: `./bin/controlplane` with no registry configured should just
  work (build/push disabled, as today); a flag or env var should let it
  *optionally* auto-start a local `registry:2` container itself via the
  Docker API, rather than requiring a separate manual `docker run`.
- **Single command for the agent** — already true today
  (`./bin/agent`), this item is really "no DOCKER_HOST friction" (covered
  above) plus auto-detecting the control-plane URL when it's running on
  the same host (`localhost:8099` default is already there).
- **`hacoctl deploy <compose.yml>`** is already a single command — covered,
  done.

## 3. `hacoctl` command surface

Already built: `deploy`, `plan`, `status`, `nodes`, `events`, `metrics`.

Still needed:
- `hacoctl workloads [project]` — list endpoint doesn't exist yet
  (`store` only supports single-key lookup); needs `ListWorkloads`.
- `hacoctl containers` — a join view: node, project/service, container ID,
  status, in one table. Derivable entirely from existing workload rows +
  node list, no new storage needed.
- `hacoctl node <id>` — single-node detail.
- `hacoctl node cordon <id>` / `hacoctl node uncordon <id>` — see §5.
- `hacoctl stop <project> <service> <revision>` — sets `desired_state:
  stopped`; needs the agent to actually honor `DesiredState` by stopping
  the container, which it doesn't check today (it only ever drives
  *toward* running).
- `hacoctl logs <project> <service> <revision>` — needs a new agent-side
  endpoint to stream/return `docker logs` for a workload's container, and
  a way for the control plane to proxy/route to the right node's agent
  (agents currently only push state *out*, nothing calls back into them).
- `hacoctl services <project> <service>` — registry lookup; endpoint
  already exists (`GET /v1/services/...`), just needs the CLI wrapper.

## 4. Registry-optional builds

Two genuinely different cases, not one flag:

- **Single-machine / shared-daemon case** (covers all current local
  testing): build centrally as today, skip push entirely, agent uses the
  already-built local image tag directly since it's the same Docker
  daemon. Cheap — mostly just making "no registry configured" skip the
  push step instead of erroring.
- **Real multi-machine, no registry**: the build has to happen *on the
  target node*, because that's the only Docker daemon that will ever see
  the result. Needs:
  - The **agent**, not the control plane, invokes the build.
  - The build context (source tree) has to reach that agent — a new
    endpoint to ship the tarball from wherever `hacoctl deploy` was run
    (or wherever the control plane keeps it) to the target agent over
    HTTP.
  - A real limitation to accept: if a workload is ever rescheduled to a
    *different* node later (M6 reconciliation), that node won't have the
    image and would need to rebuild from scratch — no cross-node image
    cache without a registry, by definition.

## 5. Node active/passive (cordon/drain)

Mostly already modeled — `model.Node.Availability` has `disabled`, and the
scheduler's hard filter already excludes anything not `available`. What's
missing:
- `POST /v1/nodes/{id}/cordon` / `/uncordon` — a manual admin action,
  which doesn't exist; availability today only ever comes from heartbeats.
- A separate `disabled_by_admin` flag, independent of heartbeat-driven
  availability — otherwise the very next heartbeat would silently
  un-cordon the node, since `Heartbeat()` unconditionally sets
  `availability = 'available'` today.
- Scheduler and reconciler both need to treat "cordoned" as a hard
  exclude regardless of heartbeat freshness.
- This is exactly the "cordon/drain — later operational enhancement"
  already named as deferred in `docs/contracts/core-contracts.md`.

## 6. Deploy preview (plan with placement, not just validation)

- `POST /v1/compose/plan` currently validates and builds the workload
  list but never runs the scheduler, so it can't show *where* things
  would land. Extend it to also run a dry-run scheduling pass per service
  (same scheduler as deploy, nothing persisted) and return each one's
  full `Decision` — candidates, scores, selected node, rejection reasons.
- **Manual override ("freeze")**: a per-service forced-node option the UI
  sends back before confirming deploy. This needs a new constraint,
  distinct from today's storage-pinning (`StoragePolicy.PinnedNodeID`,
  which is specifically about persistent-volume node affinity) — a
  general "place this exactly here" override that still respects hard
  filters (so you can't force a workload onto a node that genuinely lacks
  the resources — forcing bypasses scoring, not safety).
- **"Leave it to the scheduler"** is just today's default — no change.

## 7. Resource-driven rebalancing

Explicitly not built, and explicitly out of scope per the spec itself
("transparent live migration of running containers between hosts" is a
listed non-goal, §3.2). Worth naming precisely what exists vs. doesn't:

- **New workloads** already go to whichever node scores best right now —
  this part is automatic.
- **Already-running workloads never move** just because another node
  later becomes free — the scheduler only ever runs once, at submission.
- The only thing that moves an *existing* workload today is the M6
  reconciler, and only when its node goes fully unavailable (not just
  full) — and even then, only ephemeral workloads; stateful ones are
  deliberately never auto-moved (FR-017).
- If real rebalancing is wanted later: a periodic pass that asks "is
  there a materially better placement for this workload now," and even
  then it would have to stop-and-recreate elsewhere (no checkpoint/restore
  exists), not truly live-migrate. Worth deciding deliberately, not
  backing into — this reintroduces exactly the kind of risk the spec's
  non-goal was written to avoid for stateful workloads in particular.

## 8. Browser-based dashboard

A web UI over the existing (and above-extended) HTTP API, likely served
directly by the control plane (`go:embed` a static bundle) rather than a
separately deployed frontend, to keep the single-binary bootstrap story
intact. Pages, derived directly from what's discussed above:

- **Nodes**: list with availability, telemetry (CPU/mem/GPU/thermal), a
  cordon/uncordon toggle per node (§5).
- **Workloads**: table of everything, status, which node, filter by
  project; actions to stop/redeploy; needs §3's list endpoint.
- **Node detail**: what's running on this node specifically, resource
  usage.
- **Deploy flow**: paste/upload a Compose file → preview screen showing
  per-service placement (§6) → optionally override/freeze per service →
  confirm, or leave it to the scheduler.
- **Events**: timeline view of the audit trail.
- **Metrics**: node/workload counts, maybe simple charts over time (would
  need the metrics endpoint to retain history, which it doesn't today —
  it's a live snapshot, not a time series).
- **Service registry**: look up what a logical service name currently
  resolves to.

This is the largest single item here — everything above it is
prerequisite groundwork (list endpoints, cordon API, preview-with-
placement API) the UI would actually call.
