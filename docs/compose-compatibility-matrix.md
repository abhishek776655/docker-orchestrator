# Compose Compatibility Matrix — MVP

| Compose feature | MVP behavior | Notes |
| --- | --- | --- |
| `image` | Supported | Resolve/pin to digest where practical |
| `build.context`, `dockerfile`, `args` | Supported | The context's `.dockerignore` is honored exactly as `docker build` does (the Dockerfile and `.dockerignore` are always kept), so a `context: .` at a monorepo root ships only what it says. With `--no-registry`, the assigned node builds; its image carries a `haco.build.id` fingerprint of the build inputs and is rebuilt whenever that changes, so a source edit reaches the node on the next deploy. `--no-cache` forces a full rebuild there too. File permissions (e.g. an executable entrypoint script) are preserved. |
| `build.ssh` | Supported with `--no-registry` | Builds that declare `ssh:` run through `docker buildx` (BuildKit) on the assigned node so `RUN --mount=type=ssh` works. `default` uses `SSH_PRIVATE_KEY` from the control plane's environment (an ssh-agent socket can't be forwarded to another node); `id=path` reads that key file. Keys are held in control-plane memory and served only to the assigned node — never stored in SQLite or written into image layers. With a registry configured this is rejected with a clear error. |
| build secrets | Not yet | Warned at plan time |
| `environment`, `env_file` | Supported | Resolve at deploy time; redact secrets |
| `ports` | Supported with validation | Every declared port becomes an internal `ContainerPort`, discoverable via service-name DNS whether or not it is published. Only a port with an explicit `published:` value also becomes a host binding; the real published value is what's reserved/conflict-checked (`Constraints.HostPorts`), and independent host/container port remapping is fully supported — publishing `5600` for a container listening on `5000` binds the agent's container to host port `5600` while the container itself still listens on `5000` (`Workload.PortBindings` pairs the two explicitly). Detect planned node/binding conflicts. |
| `depends_on` conditions | Supported | Validates the graph and actually gates on the declared condition: `service_started` (default) is satisfied once a dependency is running or ready, `service_healthy` requires it to have actually passed its own healthcheck (StatusReady), not merely be running. `service_completed_successfully` is not evaluated (treated like service_started). |
| `healthcheck` | Supported | Translated into the container's real Docker `HealthConfig` (not just parsed for the placeholder warning below); the agent polls `docker inspect`'s reported health status (non-blocking, one check per poll tick — see M4 doc) and only transitions the workload to ready once healthy, which is what makes `service_healthy` actually differ from `service_started`. Still warns for an obvious placeholder check (e.g. `exit 0`), since that passes trivially and validates nothing. An image's own baked-in `HEALTHCHECK` (no compose `healthcheck:` block) is also honored the same way. |
| named volumes | Supported with policy | Node-local persistent, but unpinned by default — the scheduler places it freely on first deploy, then locks future redeploys to wherever it landed (same as `x-haco-place-anywhere`). Pinning to a specific node is the user's explicit call: `x-haco-pin-host`/`x-haco-pin-node` in the Compose file, or `--pin-host`/`--pin-node` at deploy time. Nothing pins a service to the control-plane host just because it's stateful. |
| bind mounts (single-file source) | Supported, automatic | A bind mount whose source is an existing file (not a directory) under 1MiB is read once at plan time and its content travels with the workload itself (no literal host path, no pin needed) — the agent that ends up running it, wherever that is, materializes the same file locally before binding, the same idea as shipping a build context to whichever node builds it. A larger file is rejected at plan time instead (use `x-haco-bind-host-path` and pin the service to a host for that). |
| bind mounts (build-context source) | Supported, automatic | A bind mount whose source is (or is inside) any service's build context — anywhere in the file, including a different service's — is inferred as a source-code live-mount |
| bind mounts (shared across services) | Supported, automatic | A directory bind-mounted by 2+ distinct services, but not a build context, is inferred as real shared content someone maintains (e.g. a shared proto/models directory) — a directory only one service mounts stays the ordinary managed-data-directory default instead |
| bind mounts (other directory, source-code live-mount) | Supported via `x-haco-bind-host-path: true` | Catches a directory the two automatic cases above don't (e.g. a shared-library or init-scripts directory that isn't any service's build context) |
| directory bind-mount affinity | Supported, deploy-time | Any of the three directory cases above is **unpinned by default**: its content is tarred up once at deploy time and shipped to whichever node the service actually lands on (`VolumeMount.RemoteDirKey`, fetched/extracted by that node's agent) — the same idea as a remote build context, just for a directory mount. Only an explicit `x-haco-pin-host: true` (or `--pin-host`/`--pin-node` pinning it to this control-plane host specifically) gets a true, literal, live bind mount instead — the only case where the literal path is guaranteed to exist where the workload runs, and so the only case where a further host edit shows up without a redeploy. Pinned to a *different* explicit node still gets the tarball snapshot, not a literal mount. |
| managed volume one-time seed | Supported, automatic | A plain managed data volume (e.g. `./mysql-data:/var/lib/mysql`, not matching any of the directory cases above) whose compose-declared host directory already has real content — from running the stack outside the orchestrator before — gets that content adopted as the managed volume's starting point, once (`VolumeMount.SeedDirKey`, same tarball-transfer mechanism). Only on the very first creation of that managed directory: once it exists (seeded or not), the managed copy is the sole source of truth forever after — a later change to the original directory is never picked up again. An empty or missing directory produces no seed; there's nothing worth transferring. |
| bind mounts (other directory, data dir) | Supported with policy | Host path is ignored and treated as a managed node-local volume, same as a named volume — correct for a data directory meant to start empty, since it's never read back |
| `networks` | Supported as logical membership | Maps to discovery/connectivity policy |
| `secrets` | Supported | Deliver through protected secret handling |
| CPU/memory limits | Supported | `mem_limit` becomes the container's hard limit; no `mem_limit` means no limit, same as Docker Compose (it used to default to 128MiB, which silently OOM-killed databases). |
| `restart` | Partial | Map only documented policies |
| `platform` | Supported | No `platform:` means no hard architecture constraint (schedules onto whatever node is available) rather than assuming amd64. An explicit `platform:` must match the node's own architecture, except an arm64 node is treated as amd64-capable too (Docker Desktop's Rosetta/qemu emulation on Apple Silicon); the reverse isn't assumed. |
| `extra_hosts`, `host-gateway` | Reject or warn | No implicit equivalent |
| unsupported fields | Reject | Never silently ignore semantic changes |

## Required validation

- Undefined dependencies and dependency cycles.
- Missing build contexts or required files.
- Unsupported features and ambiguous mount behavior.
- Host-port conflicts on the planned node.
- Missing stateful-storage policy.
- Architecture and resource incompatibilities.

## Cross-host network recommendation

`POST /v1/compose/plan` also returns `networking.mode`:

- `lan`: every declared, published service port can be reached directly on
  its node — independent host/container remapping (`PortBindings`) means
  this no longer requires the host port to equal the container port; only
  an unpublished port (declared via `expose` with no `published:` value)
  falls outside this.
- `swarm_overlay`: a service some other service actually `depends_on` has
  **no published ports at all** and at least one declared port that's
  unpublished (`expose`-only, or no `published:` value) — so there's no
  host binding whatsoever for that dependent to reach it from another node.
  A service with at least one *other* published port is presumed reachable
  through it, so an additional unpublished port (e.g. a database's
  internal-only cluster/gossip port, like qdrant's 6335 alongside its
  published 6333/6334) is never flagged on its own. Likewise, a service
  nothing else depends on is never flagged regardless of its ports. A
  cross-host LAN lookup a dependent actually needs has nothing to connect
  to, so the plan recommends an attachable Swarm overlay. The current
  prototype does not provision Swarm; Compose deployment is blocked with
  the recommendation and reasons rather than misdeploying it.

This analysis checks declared `ports` and `expose` entries. Services with no
declared ports cannot be validated for cross-host reachability by this rule.
