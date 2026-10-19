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
