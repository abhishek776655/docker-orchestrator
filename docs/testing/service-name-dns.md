# Testing: Service-Name DNS

Fixture: [`examples/service-dns/`](../../examples/service-dns). Two services:
`metaapi` (`5000:5000`, so LAN DNS can return the node IP while the URL keeps
port 5000) and
`client`, whose only configuration is `META_API_URL=http://metaapi:5000/` —
no `*_HOST`/`*_PORT` variables, no node address, exactly the requirement
this plan exists for.

## Earlier single-host trial

```bash
# Control plane with the DNS resolver and build service enabled
HACO_LISTEN_ADDR=:8099 HACO_DNS_LISTEN_ADDR=:8053 HACO_REGISTRY_ADDR=localhost:5050 ./bin/controlplane

# One agent
HACO_CONTROLPLANE_URL=http://localhost:8099 HACO_NODE_ID=node-a HACO_NODE_ADDRESS=host.docker.internal ./bin/agent

curl -X POST http://localhost:8099/v1/compose/plan   -d @svcdns_deploy.json   # valid, no rejections
curl -X POST http://localhost:8099/v1/compose/deploy -d @svcdns_deploy.json  # both built, scheduled
```

That earlier trial used `expose: 5000`, before the Compose networking
recommendation was added. The current fixture uses `5000:5000`, which should
recommend `networking.mode: lan`; the current fixture still needs to be run
against the updated analyzer on two hosts.

The application's environment remains
`META_API_URL: http://metaapi:5000/`; endpoint variables are not injected.

`docker logs` on the `client` container: `curl: (6) Could not resolve host:
metaapi` — confirming container-native resolution was **not** wired up in
this run, for the reason below.

## Why full container-native resolution isn't exercised on this machine

Two compounding limits of running everything on one Mac, both explained
here rather than left as an unexplained failure:

1. **Port 53 is unavailable.** Docker's container-level DNS setting
   (`HostConfig.DNS`) always has the container query port 53 on the
   configured server — there's no way to point it at a custom port. Port 53
   on this machine is already bound by macOS's own resolver
   (`mDNSResponder`), and binding it ourselves would require `sudo` and
   would disrupt the host's own DNS — not something to do to a developer's
   machine. `agent.NewDocker` detects this (any configured DNS address
   whose port isn't `53`) and skips container DNS configuration entirely,
   logging why, rather than silently producing a broken `resolv.conf`.
2. **The simulated two-node addressing doesn't resolve from every vantage
   point.** `host.docker.internal` is a Docker-injected hostname that only
   resolves *from inside a container* — not from the control plane's own
   resolver process, which runs as a plain host process (same limitation
   class as the M3/M6 docs' "one shared Docker daemon standing in for two
   real hosts"). A real multi-node deployment doesn't have this problem:
   every node has one real, universally-routable IP.

## What was verified end-to-end in the earlier trial

Both limitations are specific to this single-Mac setup, not the resolver
logic, so the resolver itself was verified directly:

```bash
$ dig @127.0.0.1 -p 8053 metaapi2.svcdns.haco A +short
127.0.0.2
```

A workload pinned to `node-b` (registered with a real IP, `127.0.0.2`, not
a Docker-only hostname) resolved correctly through the live resolver,
backed by the real PostgreSQL-stored registry — the exact mechanism
`client` would have used had it run on a host where port 53 and the node
addressing were both real. This, plus `discovery`'s 6 unit tests
(NXDOMAIN for unknown/not-ready services, project scoping, deterministic
multi-replica answers — see `discovery/dns_test.go`), is the verification
this plan's mechanism actually got.

**A bug was found and fixed during this verification**: the resolver
originally built A records directly from `ServiceInstance.Address` via
`dns.NewRR`, which requires a literal IP — a hostname like
`host.docker.internal` caused `dns.NewRR` to fail silently (logged, record
dropped), so every query against a hostname-addressed node returned an
empty, incorrectly-successful answer instead of surfacing the problem.
Fixed with `resolveToIP`, which resolves a non-IP address via
`net.LookupIP` before building the record — correct for the production
case (always real IPs) and transparent about failure when the host
genuinely can't be resolved.

## Running this for real

For the current HTTPS/token-based onboarding and two-machine acceptance
steps, follow [`agent-join.md`](agent-join.md). The fixture publishes
`5000:5000`, allowing the client to retain `http://metaapi:5000/` over the
direct-LAN path. If Compose host and container ports differ, the analyzer
recommends Swarm overlay networking, which this prototype does not yet
provision.
