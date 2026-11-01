# Bootstrap/CLI ergonomics

Roadmap item 2 (see [roadmap.md](roadmap.md)), implemented 2026-11-01.

## Why

Every milestone's testing in this repo hit the same friction repeatedly:
macOS Docker Desktop doesn't use the historical default socket path, so
`DOCKER_HOST` had to be set by hand on every single command, and the local
registry needed a separate manual `docker run` before the control plane
could build anything.

## What changed

- New `dockerutil` package (`dockerutil.NewClient`): if `DOCKER_HOST` is
  set explicitly, it's always respected unchanged — auto-detection never
  overrides an explicit value. Otherwise it probes, in order, the socket
  paths real installs actually use (`/var/run/docker.sock`, Docker
  Desktop's per-user socket, Rancher Desktop, Colima, rootless Docker's
  per-UID socket) and uses the first one that both exists and answers a
  ping. Wired into both `agent/docker.go` and
  `cmd/controlplane/main.go` (the build service's client) — the two
  places that previously called `client.FromEnv` directly.
- `cmd/controlplane/registry.go`: `ensureLocalRegistry` makes
  `./bin/controlplane` with a registry configured genuinely a single
  command. It inspects for an existing `haco-registry` container,
  reuses/restarts it if present, or pulls `registry:2` and creates it
  fresh, bound to the configured port on every interface.
- This runs whenever the registry address points at *this* machine —
  `localhost`, `127.0.0.1`, or the control plane's own detected LAN
  address (the `HACO_REGISTRY_ADDR=auto` case) — not just the `auto`
  case as originally scoped in the roadmap: `localhost:5050` is just as
  clearly local as the auto-detected-IP case, and broadening the check
  covers both without extra configuration. A genuinely remote,
  operator-owned registry (`registry.example.com`) is never touched.

## A real bug found and fixed along the way

Verifying this end-to-end (fresh `~/.haco`, no `DOCKER_HOST`, no
pre-existing registry container) surfaced a pre-existing, unrelated bug:
node-local persistent volumes failed to mount with
`bind source path does not exist`, even though the directory genuinely
existed on the host (confirmed with `ls`, and a plain `docker run -v
<path>:/data` mounted it fine). This is a documented Docker Desktop quirk
in its newer `HostConfig.Mounts` API: its bind-source existence check
sometimes doesn't see a directory created moments earlier — exactly
this workflow (create the volume directory, then immediately start a
container into it). Fixed by switching `agent/docker.go`'s volume mounts
from `HostConfig.Mounts` (`mount.Mount{Type: mount.TypeBind, ...}`) to
the legacy `HostConfig.Binds` (`"<host>:<container>[:ro]"` strings), which
doesn't have this issue. No behavior change otherwise — same mount
semantics, same read-only handling.

## Verified

Full test suite passes (including two new `dockerutil` tests). Live,
twice, from a completely clean state (no `DOCKER_HOST`, no `~/.haco`, no
registry container):

1. `HACO_REGISTRY_ADDR=auto ./bin/controlplane` — auto-detected the
   Docker socket, auto-started `haco-registry`, printed a join token.
2. `./bin/agent join '<token>'` — no `DOCKER_HOST` needed.
3. `./bin/hacoctl deploy mvpdemo examples/mvp-demo/docker-compose.yml` —
   both services reached `ready`, cross-service connectivity confirmed,
   data confirmed persisted through the `Binds` fix.
4. Repeated with `HACO_REGISTRY_ADDR=localhost:5050` (the non-"auto"
   case) to confirm the broadened local-host detection also triggers
   auto-start correctly.

## Not done by this item

- `HACO_CONTROLPLANE_URL` for the agent still needs to be told explicitly
  when joining a *second* machine (unavoidable — see the earlier
  discussion in this project's history: nothing on a fresh machine can
  discover where the control plane lives without either a shared
  convention like `.local` mDNS hostnames, which may or may not already
  work depending on the OS, or a LAN broadcast discovery protocol, which
  this item didn't build).
