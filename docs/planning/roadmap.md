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

Built: `deploy`, `plan`, `build`, `status`, `nodes`, `node <id>`,
`workloads`, `projects`, `containers [project]`, `services`, `stop`,
`stop-containers [project]`, `logs`, `events [project]`,
`metrics [project]`, `teardown`, `start`/`stop-all`/`agent run`/`agent
join` (process lifecycle), `revoke-node`/`remove-node`.

Project filtering is consistent across the board now: `containers`,
`events`, and `metrics` all gained an optional `[project]` filter
(matching `workloads`'s existing one) — `events`/`metrics` needed real
store-layer filtering (`workload_key LIKE 'project/%'`), not just CLI
plumbing. `projects` is new: lists every distinct project (derived from
existing workload rows, no new storage) with a per-status workload
count, so you don't have to run `hacoctl workloads` with no filter and
eyeball the PROJECT column. `stop-containers [project]` can now target
one project; `teardown` stays global/unscoped since node-credential
revocation isn't a per-project concept either way.

Also now built (§5): `hacoctl node cordon <id>` / `uncordon <id>`.

Known gap, not yet addressed: `hacoctl logs` is a one-shot failure-tail
snapshot, not a live follow/stream — streaming would need agent-initiated
push, since agents never accept inbound connections today (M3 scope).

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

## 9. `hacoctl tui` — ✅ done (v1, read-only)

Added ahead of item 8's full web UI, as a terminal equivalent: a
Bubble Tea + Bubbles + Lipgloss (charmbracelet) dashboard,
`cmd/hacoctl/tui.go`. `hacoctl tui [project]` — project scoping is a
start-time argument only, not interactive filtering.

- **Tabs** (tab/shift+tab or ←/→ to switch): Nodes, Workloads,
  Containers, Events, Metrics, Services, Projects — mirrors the
  existing CLI table commands, minus anything action-oriented
  (deliberately **no** stop/cordon/deploy/revoke from inside the TUI
  in v1 — that's an explicit, separate v2). Each tab reuses the same
  view structs and `fetchJSON` helper the equivalent CLI command
  already built (`nodeView`, `workloadRecordView`,
  `serviceInstanceView`, `projectSummaryView`,
  `intListOrDash`/`internalURLs` for the containers tab), plus two new
  ones (`eventView`, `metricsView`) for the two commands that didn't
  already have a view struct (`hacoctl events`/`metrics` just dump raw
  JSON).
- **Auto-refresh**: polls every 2s via `tea.Tick` + one `tea.Cmd` per
  tab switch/refresh, off the UI goroutine — a slow or unreachable
  control plane shows "disconnected, retrying..." in the status bar
  instead of freezing keypresses or crashing (the same non-blocking-
  poll principle already applied to the agent's dependency/health
  checks in item 3's follow-up fixes).
- **Keybinds**: ↑/↓ or j/k to move the table selection, `r` to force
  an immediate refresh, `q`/ctrl+c to quit.
- Smoke-tested (`cmd/hacoctl/tui_test.go`): model construction/Init/
  Update/View don't panic, and `fetchTabData` shapes every tab's
  columns/rows correctly against mocked JSON. A separate opt-in
  integration test (`tui_live_test.go`, gated on
  `HACO_TUI_LIVE_TEST=1`) exercises the same against a real running
  control plane — not part of the normal `go test ./...` run.

## 10. Real-file test fixes — ✅ done

Found by actually running `hacoctl plan`/`deploy` against a real,
39-service Compose file (not a synthetic one):

- **`env_file` path doubling**: `compose.go` joined `workingDir` onto
  `env_file` paths that compose-go had already resolved to absolute
  (same as `build.context`), corrupting most of them. Fixed with an
  `IsAbs` guard.
- **`--service` scoping didn't narrow validation**: `ComposePlan`/
  `ComposeDeploy`/`ComposeBuild` checked whole-file validity before
  calling `filterToClosure`, so a globally-invalid file could never be
  narrowed to a valid subset. Fixed by filtering first and having
  `filterToClosure` recompute rejections/warnings/networking for just
  the filtered subset (`compose.ValidateGraph`, newly exported).
- **Host/container port remapping**: a `published:target` pair with
  `published != target` (e.g. `5600:5000`) was silently treated as a
  Swarm-overlay-only case, and worse, the scheduler's port-conflict
  check was fed the *container* port instead of the real published
  one — causing false positive "port conflict" rejections between
  services whose real host ports never collided. Fixed with a new
  `model.Workload.PortBindings []PortBinding` (container port ↔ host
  port pairs), populated by `compose.go` and consumed by
  `agent.Docker.EnsureRunning` to bind each port independently; falls
  back to host==container when empty (e.g. the raw `/v1/workloads`
  submit API, not updated to populate it). `networking.mode` no longer
  recommends `swarm_overlay` for a remapped-but-published port — only
  for a genuinely unpublished one.
- **Architecture defaults/compatibility**: a service with no `platform:` no
  longer gets a hardcoded `amd64` scheduling requirement — it schedules
  onto whatever node is available. An arm64 node is also treated as able to
  run an explicit amd64 workload (`scheduler.architectureCompatible`),
  since Docker Desktop on Apple Silicon runs amd64 images via Rosetta/qemu
  emulation; the reverse direction (arm64 workload, amd64 node) is still a
  hard rejection. Found/fixed while live-testing `--service frontendapi`
  against the real file on an arm64-only (Apple Silicon) two-node fleet.
