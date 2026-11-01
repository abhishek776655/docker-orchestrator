# Roadmap (post-M7)

Captured from discussion on 2026-10-20, not yet built. Nothing here changes
the M0-M7 delivery path or its acceptance criteria — this is what comes
after, prioritized loosely in the order discussed.

## 1. Storage: Postgres → embedded SQLite — ✅ done

See [sqlite-migration.md](sqlite-migration.md) for what actually shipped.

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

## 2. Bootstrap/CLI ergonomics — ✅ done

See [bootstrap-ergonomics.md](bootstrap-ergonomics.md) for what actually
shipped (including a real Docker Desktop volume-mount bug found and fixed
along the way).

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

## 3. `hacoctl` command surface — ✅ done

Built: `deploy`, `plan`, `status`, `nodes`, `node <id>`, `workloads`,
`containers`, `services`, `stop`, `stop-containers`, `logs`, `events`,
`metrics`, `teardown`, `start`/`stop-all` (control-plane lifecycle),
`revoke-node`/`remove-node`.

Also now built (§5): `hacoctl node cordon <id>` / `uncordon <id>`.

## 4. Registry-optional builds — ✅ done (both cases, unified)

Originally scoped as two different cases; they ended up being the same
mechanism, so there's no special-casing between "single machine" and
"multi-machine" at all:

- With a registry configured, `ComposeDeploy` still builds centrally and
  pushes (`build.Service.Build`) exactly as before — any node pulls it
  normally.
- With **no registry**, a build is never done centrally at all, regardless
  of where the workload is scheduled, including the control-plane's own
  host. Instead (`Service.resolveBuilds`): the control plane packages the
  build context (`build.TarContext` — pure file I/O, no Docker client
  needed at all) and stashes it in memory, keyed by workload key; the
  workload gets a deterministic `<project>-<service>:<revision>` image
  name and `model.Workload.RemoteBuild` (Dockerfile/args/platform). The
  scheduler places it on whichever node scores best — **no host-pin
  requirement of any kind** — since the image is never expected to exist
  anywhere before that node builds it.
- The agent that actually gets the assignment (`agent.Agent.buildRemote`)
  fetches the tarball from a new authenticated endpoint
  (`GET /v1/agent/{node}/build-context/{project}/{service}/{revision}`,
  which checks the requesting node is the workload's actual assigned
  node) and builds the image itself (`agent.Docker.BuildImage`) — status
  `building`, a `StatusPulling` counterpart. `HasImage` skips a redundant
  rebuild once it's already there (e.g. the next poll, or a restart).
- Accepted limitation, as originally scoped: if the workload is later
  rescheduled to a *different* node (M6 reconciliation), that node has no
  image either and goes through the same fetch-and-build path again from
  the same stashed tarball — no cross-node image cache without a
  registry, by definition. The tarball itself is in-memory-only on the
  control plane (matches the project's single-control-plane-process
  design) — a control-plane restart between deploy and the agent's first
  fetch means redeploying.
- Also added as part of this: a missing/invalid `build.context` or
  `Dockerfile` is a plan-time rejection, not a deploy-time build
  failure; `hacoctl start` defaults the registry *on*
  (`HACO_REGISTRY_ADDR=auto`), so remote-build is opt-in via
  `--no-registry`, not the default.

## 5. Node active/passive (cordon) — ✅ done

Built as cordon only, not drain: `POST /v1/admin/nodes/{id}/cordon` /
`/uncordon` (`hacoctl node cordon|uncordon <id>`) flip `Availability` to
`disabled`, which the scheduler's existing "!= available" hard filter
already excludes from every new placement — no scheduler change needed.
A new `nodes.disabled_by_admin` column survives `Heartbeat` and
re-registration (both now `CASE WHEN disabled_by_admin THEN ... ELSE
...`), so a cordoned node can't silently un-cordon itself. Existing
workloads on a cordoned node are left running untouched (pure cordon,
k8s-style) — forcibly moving them is a separate, explicit act (`hacoctl
stop` / redeploy), not automatic.

## 6. Deploy preview (plan with placement, not just validation) — ✅ done (core)

`POST /v1/compose/plan` now runs a real dry-run scheduling pass per
service and returns each one's full `Decision` (candidates, scores,
selected node, rejection reasons); `hacoctl plan` renders it as a table.
Still open:
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
