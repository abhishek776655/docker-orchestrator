# Agent join and two-machine LAN test

This prototype uses a one-time join command to deliver the control plane's
LAN endpoint and TLS certificate pin. The agent redeems the short-lived
token over HTTPS, saves its own credential under `~/.haco/agent-credentials/`
with mode `0600`, then registers and runs using authenticated HTTPS.

## Machine A: start control plane

Both machines need the `agent` binary; Machine A also needs `controlplane`
and `hacoctl`:

```bash
go build -o bin/controlplane ./cmd/controlplane
go build -o bin/agent ./cmd/agent
go build -o bin/hacoctl ./cmd/hacoctl
```

Start PostgreSQL and the registry as usual. This Docker setting is needed
only when HACO is building/pushing images to the development registry over
plain HTTP. Configure Docker on **both** machines to trust Machine A's
registry address, for example the `HACO_REGISTRY_ADDR=auto resolved to ...`
line printed by the control plane (default registry port `5050`):

```json
{"insecure-registries": ["<A_LAN_IP>:5050"]}
```

Restart Docker after changing its daemon config. Then start the control
plane on Machine A:

The agent join URL is **not** the registry address: it uses HTTPS port
`8443`. The registry uses its own port (normally `5050`).

```bash
HACO_LISTEN_ADDR=127.0.0.1:8099 \
HACO_AGENT_LISTEN_ADDR=0.0.0.0:8443 \
HACO_REGISTRY_ADDR=auto \
HACO_DNS_LISTEN_ADDR=0.0.0.0:53 \
./bin/controlplane
```

It automatically selects a LAN address, creates/persists its TLS identity
under `~/.haco/tls/`, and prints a one-use command resembling:

```bash
./bin/agent join '<encoded-one-time-token>'
```

The token expires after 10 minutes. The operator API stays local on
`127.0.0.1:8099`; agent HTTPS is on TCP `8443`; service DNS is UDP `53`.
Binding DNS port 53 may require root or `setcap cap_net_bind_service`.
If another resolver owns it, stop that service or use a Linux control-plane
host where port 53 is available. Container DNS cannot use a custom DNS port.

## Join agents

Copy `bin/agent` to Machine B (the same binary can run on Machine A).
Run the printed command once on Machine A and once on Machine B. The same
token cannot be used twice, so on Machine A get a second one for Machine B:

```bash
./bin/hacoctl join-token
```

Copy the newly printed command to Machine B and run it there. The command
contains the discovered endpoint and certificate fingerprint; neither
machine needs a separately typed control-plane IP. The default node ID is
the machine hostname. To choose one explicitly, set `HACO_NODE_ID` before
running `join`, for example `HACO_NODE_ID=node-b ./bin/agent join '<token>'`.

The join process stores its node credential locally and stays running. To
restart an already joined agent, run:

```bash
./bin/agent run
```

To revoke a credential and rejoin that node with a fresh command:

```bash
./bin/hacoctl revoke-node <node-id>
./bin/hacoctl join-token
```

Run `hacoctl` locally on Machine A. It communicates with the loopback-only
operator API. Never paste the join token into tickets or chat; it authorizes
one node until redeemed or expired.

## Verify cross-node service traffic

Allow these ports between the machines: TCP `8443` (agent API), UDP `53`
(service DNS), TCP `5050` (development registry), and workload host ports
published by Compose. TCP `8099` remains local to Machine A. The Compose
service must publish the same port it listens on, such as `5000:5000`, for
direct-LAN routing while retaining `http://metaapi:5000/`.

Deploy `examples/service-dns/docker-compose.yml` from Machine A with the
existing Compose plan/deploy commands. Confirm the plan reports
`networking.mode: "lan"`, and confirm `metaapi` and `client` are running on
different hosts; otherwise the test does not exercise a cross-host path.
Then check the client logs for `metaapi-ok`. If needed, inspect from inside
the client container:

```bash
getent hosts metaapi
curl -v http://metaapi:5000/
```

The resolved address should be the LAN IP of the host running `metaapi`.
The client keeps `META_API_URL=http://metaapi:5000/`; HACO supplies the DNS
search suffix and resolves the service to its node's address. A connection
failure after DNS succeeds usually means TCP `5000` is blocked or the
service's published host port does not match its container port.

## Limits of the first version

- The control plane's advertised LAN address is auto-detected. Hosts with
  multiple active interfaces can set `HACO_AGENT_ADVERTISE_HOST` explicitly
  to select the reachable address.
- Changing the control-plane LAN address requires newly joined agents (or
  updating their saved credential configuration); the server certificate
  pin itself remains stable across restart.
- The token secures agent onboarding and the agent API. The separate
  operator API remains unauthenticated but loopback-only.
- A real two-host LAN test cannot be simulated by the one-host Go unit test
  suite; this guide is the acceptance procedure for that test.
