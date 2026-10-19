# M5 — Storage and Dev Workflow

## Goal

Make node-local persistent storage a real, enforced safety property instead
of just a label, and give storage capacity/permission problems a validation
path (SRS milestone M5, §10).

## Scope decision: dev-workflow source-sync deferred

SRS §7.3 explicitly allows deferring development-mode source synchronization
("It may be deferred from the earliest MVP, but bind-mounted services must
be flagged until supported" — and FR-020 is a SHOULD, not a MUST). M5
spends its effort on the safety-critical half (persistent storage) instead:
bind mounts are already rejected with an actionable message by the M4
Compose importer, which satisfies "bind-mounted services must be flagged."
File-watching source-sync itself is **not implemented** and is left for a
later milestone — this is a recorded decision, not a silent gap.

## Implemented

- [x] `model.VolumeMount` + `Workload.Volumes`: a workload can declare
      container-path mounts that need real persistence, not just a storage
      class label.
- [x] `agent.Docker.VolumeHostPath`: deterministically derives a workload's
      on-disk directory from its key and container path — never a
      user-supplied host path. The same workload always resolves to the
      same directory, so recreating its container (crash, image update,
      agent restart) reuses existing data instead of starting empty
      (FR-016, FR-017).
- [x] `agent.Docker.prepareVolumes`: creates the host directory (validates
      permissions by trying), bind-mounts it into the container via
      `mount.Mount`, and warns if the backing filesystem is over 90% full
      (SRS §10.2: "capacity, mount permissions... validated where
      possible").
- [x] Compose importer wires a named volume + `x-haco-pin-node` directly
      into a real `VolumeMount`, not just a storage-class label (M4 only
      recorded the pin; M5 makes it actually mount).
- [x] [Backup and restore procedure](../operations/backup-and-restore.md)
      documented for both the control-plane PostgreSQL metadata and
      node-local volumes (SRS NFR-011) — manual, not automated, as the spec
      allows.

## Verified end-to-end (this machine)

1. Submitted a Redis workload with `storage_class: node_local_persistent`,
   pinned to node-a, with a `/data` volume mount.
2. Wrote a key, triggered a save — confirmed the real file
   (`~/.haco/volumes/node-a/demo5_db_r1/_data/dump.rdb`) exists **on the
   host**, outside the container.
3. `docker rm -f` the container (simulating node/container loss), reset its
   status to `assigned` so the agent would recreate it.
4. Agent created a **new** container (different container ID) using the
   same derived host path.
5. `redis-cli get greeting` → `hello-from-m5`. Data survived container
   recreation because the mount path is derived from the workload identity,
   not from container state.

## Known M5 limitations

- No automated backup scheduling — procedure is manual (by design, matches
  SRS §10.2's allowance).
- No disk-quota enforcement, only a >90%-full warning after the fact.
- No shared/replicated storage classes (SRS §10.1 "Shared persistent" and
  "Replicated/application-managed" remain explicitly future capabilities).
- Source-sync for dev-mode bind mounts: not implemented, deferred per the
  scope decision above.
- A node-local volume's data cannot migrate to another node automatically —
  by design (SRS: no writer on a different node without verified data
  access); see the backup/restore doc for the manual path.

## Exit criterion

Met: a stateful workload's data remains safely pinned to its node's actual
on-disk location across container recreation, and the storage procedure to
back it up/restore it is written down.
