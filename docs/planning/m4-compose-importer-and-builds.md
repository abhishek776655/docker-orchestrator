# M4 — Compose Importer and Builds

## Goal

Import a supported Compose subset, build services with a `build:` block
centrally and push them to a registry, and deploy the resulting multi-service
project across the two-node cluster (SRS milestone M4, §7).

## Implemented

- [x] `compose/` package: parses a Compose file with `compose-spec/compose-go/v2`
      and converts each service into a `model.Workload` plus, where relevant,
      a `BuildSpec` — never a hand-rolled YAML parser (SRS 5.1 "Go module
      using a Compose parser/library").
- [x] Validation against the supported subset
      ([compose-compatibility-matrix.md](../compose-compatibility-matrix.md)):
      `image`, `build.context/dockerfile/args` (SSH/secrets flagged
      unsupported, not silently dropped), `ports`, `environment`/`env_file`,
      `depends_on`, `healthcheck` (placeholder detection), `volumes` (bind →
      reject, named → require `x-haco-pin-node`), `cpus`/`mem_limit`,
      `platform`, `restart` (recorded, not enforced), `secrets` (flagged,
      not delivered), `extra_hosts` (reject). Unsupported or ambiguous
      configuration is reported, never dropped (FR-003).
- [x] Cross-service validation (`validateGraph`): undefined `depends_on`
      references, dependency cycles (with the cycle path), and host-port
      conflicts within one project — exactly the "Required validation" list
      in the compat matrix.
- [x] `build/` package: builds an image via the Docker Engine API (not full
      BuildKit — see limitations) and pushes it to a registry (FR-004).
- [x] `POST /v1/compose/plan`: dry-run — parses and validates only, returns
      the full plan (workloads, builds, warnings, rejections) with no side
      effects (UC-02, FR-023).
- [x] `POST /v1/compose/deploy`: plans, builds every service with a `build:`
      block, substitutes the pushed image reference, then submits every
      workload through the same M1/M2/M3 scheduling path — refuses to apply
      anything if the plan has rejections.
- [x] Local OCI registry for dev (`registry:2` on `:5050`) — push and pull
      round-trip verified directly before wiring it in.

## Verified end-to-end (this machine, two simulated nodes)

A two-service Compose project: `cache` (Redis, pinned to no node explicitly —
given a deliberately large CPU reservation) and `web` (built from a local
Dockerfile, `depends_on: cache`):

1. `POST /v1/compose/plan` → valid plan, one warning (published-port ≠
   container-port, the documented M2 simplification), one `BuildSpec` for
   `web`.
2. `POST /v1/compose/deploy` → `web` was built from source, pushed to
   `localhost:5050/demo4-web:latest`, and both workloads were scheduled.
   `cache`'s large CPU reservation left node-a with less headroom, so the
   scheduler naturally placed `web` on node-b — no manual node pin needed,
   unlike M3's demo.
3. Both reached `status: ready`. The built image's own startup log printed
   `CACHE_HOST=host.docker.internal CACHE_PORT=16390` — resolved and
   injected by the M3 dependency mechanism, now driven by a Compose-derived
   workload instead of a hand-written API call.
4. `redis-cli -h $CACHE_HOST -p $CACHE_PORT ping` from inside the built
   `web` container → `PONG`.

## Known M4 simplifications / honest limitations

- **Build uses the plain Docker Engine API, not BuildKit.** `build.ssh` and
  `build.secrets` are parsed and flagged with a warning but not honored — a
  build needing them will fail. Real BuildKit secret/SSH mounts (SRS §11)
  are deferred.
- **No image digest pinning.** Images are tagged `<registry>/<project>-<service>:<revision>`
  with `Revision` hard-coded to `"latest"` — no content-addressed
  versioning yet (SRS 7.1 "pin to digest after resolution where practical").
- **Env/profile interpolation is minimal.** `${VAR}` substitution has no
  shell-environment fallback; only `environment:` and `env_file:` values are
  used. `.env` file auto-loading is not implemented.
- **Node affinity for named volumes needs `x-haco-pin-node`**, a
  project-specific Compose extension — there is no generic "prefer this
  node" placement hint in the model otherwise, by design (hard constraints
  and scoring are supposed to decide that, not a label).
- Everything already documented as a limitation in
  [M2](m2-single-node-execution.md) and [M3](m3-two-node-connectivity.md)
  still applies (1:1 host-port simplification, single local Docker daemon
  standing in for two hosts, no gateway, no DNS).

## Exit criterion

Met: a small Compose project — one built-from-source service depending on
one pre-built service — deploys across the two-node cluster end-to-end, with
the dependency's endpoint resolved and injected automatically.
