# M5 — Storage and Dev Workflow

## Goal

Make node-local persistent storage a real, enforced safety property instead
of just a label, and give storage capacity/permission problems a validation
path (SRS milestone M5, §10).

## Scope decision: dev-workflow source-sync deferred

SRS §7.3 explicitly allows deferring development-mode source synchronization
("It may be deferred from the earliest MVP, but bind-mounted services must
be flagged until supported" — and FR-020 is a SHOULD, not a MUST). M5
spends its effort on the safety-critical half (persistent storage) instead.

Update (post-roadmap item 4/5 era): a bind mount is no longer rejected
outright. The M4 Compose importer now treats a plain bind mount's host
path as ignored and manages it exactly like a named volume (node-local,
pinned) — correct for a data directory meant to start empty, which
satisfies "flagged until supported" for that case by making it behave
safely rather than by refusing it.

A *source-code* bind mount (live-reloading code from the literal host
path) is now also supported, opt-in via `x-haco-bind-host-path: true` on
the service: `model.VolumeMount.HostPath` carries the literal, validated
absolute path through to `agent.Docker.prepareVolumes`, which bind-mounts
it directly instead of substituting a managed directory. This is not full
file-watching source-sync (no remote rsync, no watching a path on a
*different* machine than the control plane) — it only works when the
workload is pinned to the control-plane's own host, enforced as a hard
plan-time rejection otherwise (conflicting `x-haco-pin-node`,
`x-haco-place-anywhere`, or a host path that doesn't exist). Genuine
cross-machine source-sync remains **not implemented**.

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
- Cross-machine source-sync for dev-mode bind mounts: not implemented.
  Single-machine live-mount (`x-haco-bind-host-path`) is supported — see
  the scope decision above — but there's no mechanism to sync or watch a
  path that lives on a different machine than the control plane.
- A node-local volume's data cannot migrate to another node automatically —
  by design (SRS: no writer on a different node without verified data
  access); see the backup/restore doc for the manual path.

## Exit criterion

Met: a stateful workload's data remains safely pinned to its node's actual
on-disk location across container recreation, and the storage procedure to
back it up/restore it is written down.
