# Agent Join and Secure Control-Plane Connection

## Intent

Let an operator connect a node without manually copying the control plane's
LAN address into agent configuration, while ensuring that only an authorized
agent can register and that agent traffic is encrypted and authenticated.

The control plane prints a ready-to-copy command such as
`haco-agent join <token>`. The token carries the control-plane endpoint and
the information needed to authenticate that endpoint. The agent redeems the
token once and receives a separate, persistent credential for normal
operation.

## User flow

1. Start the control plane. It detects a LAN-reachable address and prints a
   one-time join command. The command includes a high-entropy secret and
   expires after a short period.
2. Run that command on another node. The agent extracts the endpoint and
   server identity pin, connects over HTTPS, verifies the control plane, and
   redeems the secret.
3. The control plane consumes the token and returns a unique credential for
   that node. The agent stores it with restrictive local permissions.
4. Registration, heartbeats, assignment polling, and status reports use
   HTTPS plus the node credential. Reusing the join token fails.

The same join command works on the control-plane host for its local agent.
An operator may explicitly override the advertised endpoint when automatic
interface selection is ambiguous or a VPN/NAT setup requires it.

## Goals

- Eliminate manual control-plane IP entry for the normal same-LAN setup.
- Authenticate and encrypt every agent-to-control-plane request.
- Make bootstrap credentials short-lived and one-use; never log their value.
- Give each node a revocable credential independent of other nodes.
- Keep the application service-name DNS behavior and workload endpoint
  configuration unchanged.
- Preserve the existing control-plane CLI/API workflows that are not agent
  traffic, unless they expose an agent-only operation.

## Non-goals

- LAN discovery without any user-provided connection information; the join
  command itself carries the endpoint.
- Automatic traversal of NAT, VPNs, or firewalls, or public internet access.
- Full user/admin authentication for the Compose/deployment API.
- A general certificate authority, certificate rotation system, or
  enterprise identity integration in the first version.
- Replacing the existing HTTP API with a new RPC protocol.

## Security and behavior requirements

- Generate a cryptographically random, short-lived, single-use bootstrap
  secret. Store only a verifier/hash server-side; compare in constant time,
  consume atomically, and reject expired or replayed values.
- Bind the join bundle to the control-plane HTTPS endpoint and a stable
  server certificate/public-key fingerprint. The agent must verify this pin
  before sending the bootstrap secret; never provide an insecure-skip-verify
  mode as a normal path.
- Persist the control-plane TLS identity so restart does not silently change
  the pin. Print token material only to the startup terminal; do not put the
  secret in logs, API responses, or process arguments beyond the requested
  join command.
- Issue a unique random agent credential after redemption. Store its
  verifier/hash in PostgreSQL and the usable secret in a node-local file
  readable only by the agent account. Never store usable credentials in the
  node registry record or log them.
- Require the agent credential on node registration, heartbeat, assignment,
  and status-report endpoints. Bind authorization to the authenticated
  credential's node ID; a node must not impersonate a different node by
  changing a URL path.
- Keep unauthenticated operator endpoints (deployment, workload management,
  events, metrics, and node listing) off the LAN by default. The initial
  implementation should expose them only on loopback, or otherwise require
  an explicit operator choice to expose them; agent endpoints remain
  reachable over the authenticated TLS listener. Do not accidentally make
  the unauthenticated control-plane API remotely reachable as part of
  enabling agent connectivity.
- Provide an operator action to revoke a node credential and a way to mint
  another join command. Revoked/expired credentials fail closed.
- Use TLS for all agent/control-plane traffic. DNS traffic remains ordinary
  LAN DNS in this iteration; it carries names, not agent credentials.
- Document that firewall rules must allow the control-plane HTTPS port, DNS
  UDP/53, registry access, and workload ports required by the selected LAN
  networking mode.

## Compatibility and migration

Today, agent traffic is unauthenticated HTTP and the M0 baseline already
lists authenticated transport as an unresolved decision. The implementation
must not silently retain a production fallback to unauthenticated HTTP.
Local development may use explicit test configuration, but it must be
visibly opt-in and covered separately. Existing agents need to be upgraded
and joined before the authenticated control plane rejects legacy requests.
The design should document the upgrade order and behavior for existing node
records.

## Open implementation choices (builder may settle safely)

- Concrete TLS implementation: persisted self-signed server certificate
  pinned in the join bundle is the initial recommendation.
- Token and agent-credential lifetime/revocation representation and schema
  migration details.
- CLI spelling and token encoding, provided the printed command is copyable,
  secret-safe, and can be parsed without shell ambiguity.
- Credential file location and restrictive permissions, respecting the
  existing `HACO_DATA_DIR` convention.
- Advertised-address selection details and an explicit override for
  multi-interface hosts.

## Acceptance checks

- Control-plane startup prints a usable one-time join command containing a
  reachable endpoint without requiring manual IP lookup in the normal LAN
  case.
- An agent joins from another LAN machine using only that command; no
  separate control-plane IP or DNS address is entered.
- The agent rejects a server with the wrong certificate/public-key pin
  before submitting the token.
- A successful token can be redeemed once only; expiry, replay, and random
  invalid tokens fail.
- Normal agent requests use HTTPS and per-node authentication; missing,
  invalid, and cross-node credentials are rejected.
- Revoking a node credential prevents further agent API access while a new
  join command can authorize a replacement credential.
- Existing Compose service-name DNS behavior remains intact, and a
  cross-node `http://metaapi:5000/` request succeeds when the LAN path and
  matching published host port are available.
- Tests cover token lifecycle, TLS identity verification, route
  authorization, secret redaction, persistence/restart behavior, and the
  documented two-machine onboarding flow.
