# Backup and Restore

Documented per SRS NFR-011: backups and restore procedures must exist before
any non-disposable environment is used, even though automated backup is out
of MVP scope (SRS §10.2).

## Control-plane metadata (embedded SQLite)

The control plane's entire desired-state view (nodes, workloads, events,
agent join tokens/credentials) is in one SQLite file — only the control
plane process ever touches it, so there's no separate database service to
back up. There is no in-process state that isn't also in this file (SRS
NFR-001), so backing it up is sufficient to recover the control plane
after loss.

Default path: `~/.haco/control-plane.db` (override with `HACO_DB_PATH`).
WAL mode is enabled, so a backup must also capture the `-wal` and `-shm`
sidecar files if they exist alongside it at backup time.

**Backup** (stop the control plane first, or use SQLite's own backup
command for a consistent copy of a live database):

```bash
# simplest, control plane stopped:
cp ~/.haco/control-plane.db{,-wal,-shm} /path/to/backups/ 2>/dev/null

# or, live, via SQLite's own atomic backup (needs the sqlite3 CLI):
sqlite3 ~/.haco/control-plane.db ".backup /path/to/backups/control-plane-$(date +%Y%m%d-%H%M%S).db"
```

**Restore**:

```bash
pkill -f bin/controlplane
cp /path/to/backups/control-plane-20261019-225300.db ~/.haco/control-plane.db
rm -f ~/.haco/control-plane.db-wal ~/.haco/control-plane.db-shm
```

After restoring, restart the control plane and every agent so agents
re-register against the restored node list (SRS NFR-001: "recover its
desired-state view from persistent metadata after restart"). Agent
credentials in the restored file must match what's on disk at
`~/.haco/agent-credentials/`; a credential issued after the backup was
taken will fail to authenticate until the agent rejoins with a fresh
token.

## Node-local persistent volumes (application data)

Each node-local persistent workload's data lives under a deterministic host
directory: `<HACO_DATA_DIR>/<project>_<service>_<revision>/<sanitized container path>`
(see `agent.Docker.VolumeHostPath`). `HACO_DATA_DIR` defaults to
`~/.haco/volumes/<node-id>` and is printed in the agent's startup log.

**Backup** one workload's volume (do this while the container is stopped, or
accept a crash-consistent snapshot if the application doesn't support a hot
backup):

```bash
tar -C "$HACO_DATA_DIR/demo5_db_r1" -czf demo5-db-r1-$(date +%Y%m%d).tar.gz .
```

**Restore** to the same node (the path must match exactly, since placement
and data location are tied together by design — SRS §10.2, FR-016/FR-017):

```bash
mkdir -p "$HACO_DATA_DIR/demo5_db_r1/_data"
tar -C "$HACO_DATA_DIR/demo5_db_r1" -xzf demo5-db-r1-20261019.tar.gz
```

Restoring to a *different* node is a data-migration operation, not a backup
restore — it requires manually updating the workload's
`storage_pinned_node_id` and is out of scope for the MVP (SRS: "the platform
must not start another writer on a different node without verified data
access", §6.3).

## What is not automated

- Scheduled/periodic backups of either the database or volumes.
- Point-in-time recovery (only whole-file snapshots).
- Cross-node volume replication or migration.

These are explicitly out of MVP scope (SRS §3.2, §10.2) but the above gives
every environment a tested, written procedure before it holds real data.
