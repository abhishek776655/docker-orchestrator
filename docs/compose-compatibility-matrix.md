# Compose Compatibility Matrix — MVP

| Compose feature | MVP behavior | Notes |
| --- | --- | --- |
| `image` | Supported | Resolve/pin to digest where practical |
| `build.context`, `dockerfile`, `args` | Supported | Central build produces immutable image |
| `build.ssh`, build secrets | Supported | BuildKit mounts only; never image layers |
| `environment`, `env_file` | Supported | Resolve at deploy time; redact secrets |
| `ports` | Supported with validation | Every declared port becomes an internal `ContainerPort`, discoverable via service-name DNS whether or not it is published. Only a port with an explicit `published:` value also becomes a host binding; the real published value is what's reserved/conflict-checked (`Constraints.HostPorts`), and independent host/container port remapping is fully supported — publishing `5600` for a container listening on `5000` binds the agent's container to host port `5600` while the container itself still listens on `5000` (`Workload.PortBindings` pairs the two explicitly). Detect planned node/binding conflicts. |
| `depends_on` conditions | Supported | Validates the graph and actually gates on the declared condition: `service_started` (default) is satisfied once a dependency is running or ready, `service_healthy` requires it to have actually passed its own healthcheck (StatusReady), not merely be running. `service_completed_successfully` is not evaluated (treated like service_started). |
| `healthcheck` | Supported | Translated into the container's real Docker `HealthConfig` (not just parsed for the placeholder warning below); the agent polls `docker inspect`'s reported health status (non-blocking, one check per poll tick — see M4 doc) and only transitions the workload to ready once healthy, which is what makes `service_healthy` actually differ from `service_started`. Still warns for an obvious placeholder check (e.g. `exit 0`), since that passes trivially and validates nothing. An image's own baked-in `HEALTHCHECK` (no compose `healthcheck:` block) is also honored the same way. |
| named volumes | Supported with policy | Node-local by default; creates affinity |
| bind mounts (single-file source) | Supported, automatic | A bind mount whose source is an existing file (not a directory) always honors the literal host path — there's no sensible "managed empty file" substitute, so this is never ambiguous and needs no flag |
| bind mounts (build-context source) | Supported, automatic | A bind mount whose source is (or is inside) any service's build context — anywhere in the file, including a different service's — is inferred as a source-code live-mount and honors the literal host path, same as `x-haco-bind-host-path` |
| bind mounts (other directory, source-code live-mount) | Supported via `x-haco-bind-host-path: true` | Honors the literal host path (live code reload) for a directory the two automatic cases above don't catch (e.g. a shared-library or init-scripts directory that isn't any service's build context); only works when the service is pinned to the control-plane's own host machine, enforced at plan time |
| bind mounts (other directory, data dir) | Supported with policy | Host path is ignored and treated as a managed node-local volume, same as a named volume — correct for a data directory meant to start empty, since it's never read back |
| `networks` | Supported as logical membership | Maps to discovery/connectivity policy |
| `secrets` | Supported | Deliver through protected secret handling |
| CPU/memory limits | Supported | Translate according to approved semantics |
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
