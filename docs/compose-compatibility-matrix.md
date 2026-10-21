# Compose Compatibility Matrix — MVP

| Compose feature | MVP behavior | Notes |
| --- | --- | --- |
| `image` | Supported | Resolve/pin to digest where practical |
| `build.context`, `dockerfile`, `args` | Supported | Central build produces immutable image |
| `build.ssh`, build secrets | Supported | BuildKit mounts only; never image layers |
| `environment`, `env_file` | Supported | Resolve at deploy time; redact secrets |
| `ports` | Supported with validation | Every declared port becomes an internal `ContainerPort`, discoverable via service-name DNS whether or not it is published. Only a port with an explicit `published:` value also becomes a host binding (`HostPorts`); `expose:`-only or unpublished ports never create one. The host binding still uses the container port's own number — independent host/container port remapping (e.g. publishing `5600` for a container listening on `5000`) is not implemented yet. Detect planned node/binding conflicts. |
| `depends_on` conditions | Supported | Validate graph and gate readiness |
| `healthcheck` | Supported | Warn for obvious placeholder checks |
| named volumes | Supported with policy | Node-local by default; creates affinity |
| bind mounts | Warning/block initially | Require explicit node-path or source-sync policy |
| `networks` | Supported as logical membership | Maps to discovery/connectivity policy |
| `secrets` | Supported | Deliver through protected secret handling |
| CPU/memory limits | Supported | Translate according to approved semantics |
| `restart` | Partial | Map only documented policies |
| `platform` | Supported | Must match node/image architecture |
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

- `lan`: every declared service port is published on the same host port as
  its container port. The LAN DNS method can preserve URLs such as
  `http://metaapi:5000/`; host ports must be unique on each selected node.
- `swarm_overlay`: a port is unpublished or maps to a different host port
  than its container port. A cross-host LAN lookup cannot preserve the
  service URL's port, so the plan recommends an attachable Swarm overlay.
  The current prototype does not provision Swarm; Compose deployment is
  blocked with the recommendation and reasons rather than misdeploying it.

This analysis checks declared `ports` and `expose` entries. Services with no
declared ports cannot be validated for cross-host reachability by this rule.
