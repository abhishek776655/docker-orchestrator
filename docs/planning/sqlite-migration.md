# Storage migration: PostgreSQL → embedded SQLite

Roadmap item 1 (see [roadmap.md](roadmap.md)), implemented 2026-11-01.

## Why

Only the control plane ever touches the metadata store — one process, no
concurrent writers. Postgres's main strength (multi-writer concurrency)
was never actually being used, and it cost a separate container to run
and operate. SQLite, embedded and pure-Go (no cgo, `modernc.org/sqlite`),
removes that entire bootstrap step with no loss of correctness at this
scale.

## What changed

- `store/store.go`: `pgx`/`pgxpool` → `database/sql` + the SQLite driver.
  Same schema shape (`jsonb` → `text`, `timestamptz` → `text` storing
  RFC3339Nano, `bigserial` → `INTEGER PRIMARY KEY AUTOINCREMENT`),
  placeholders `$1` → `?`, `now()` calls replaced with Go-side
  `time.Now()` bound as a parameter (SQLite has no `now()` function).
  `ON CONFLICT ... DO UPDATE ... WHERE` and `RETURNING` both work
  unchanged — SQLite has supported both since 3.35 (2021), which
  `modernc.org/sqlite` bundles.
- `Store.Open` now takes a file path instead of a Postgres DSN; opens with
  `SetMaxOpenConns(1)` (this control plane is low-throughput enough that
  serializing every call costs nothing, and it sidesteps SQLite's
  single-writer model entirely rather than fighting it with retries) plus
  `PRAGMA journal_mode=WAL` and `PRAGMA busy_timeout=5000`.
- New `store.ErrNotFound` sentinel, so `controlplane/api.go` no longer
  needs to import the driver package directly to check for a missing row
  (it previously imported `pgx` just for `pgx.ErrNoRows` — a layering
  violation this migration also fixed).
- `cmd/controlplane/main.go`: `HACO_POSTGRES_DSN` → `HACO_DB_PATH`
  (default `~/.haco/control-plane.db`).
- `store/store_test.go`: the 6 existing integration tests (nodes,
  workloads, service discovery, logs, agent join/credentials) no longer
  need `HACO_TEST_POSTGRES_DSN` to be set and skip otherwise — each test
  now opens its own SQLite file in `t.TempDir()`, so they always run, no
  external service required. This is a real quality improvement
  independent of the storage choice: these tests existed but were
  silently skipped in CI/local runs without a Postgres instance around.
- Docs: `docs/operations/backup-and-restore.md` rewritten for file-copy
  backup (`cp` or `sqlite3 .backup`) instead of `pg_dump`/`pg_restore`;
  README's dependency/bootstrap steps no longer mention a Postgres
  container.

## Verified

- Full test suite (`go test ./...`), including all 6 store integration
  tests, passes with no skips.
- Live end-to-end: SQLite-backed control plane, real mTLS agent join,
  Compose build + deploy (`examples/mvp-demo`) against a real Docker
  daemon, both services reaching `ready`, cross-service connectivity
  confirmed (`api-ok, cache reachable`), events showing correctly
  round-tripped RFC3339Nano timestamps.

## Not changed by this migration

Everything about the control plane's behavior, API surface, and
reconciliation logic — this is purely a storage-engine swap. If multiple
control-plane replicas are ever wanted, that's a bigger redesign (this
project's single-control-plane assumption, SRS §18, predates and is
independent of the storage choice) and would be the point to revisit
Postgres or another networked database.
