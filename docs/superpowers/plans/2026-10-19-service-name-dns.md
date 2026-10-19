# Service-Name DNS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let workloads communicate using Compose-style URLs such as `META_API_URL=http://metaapi:5000/`, without application-managed node addresses or injected endpoint variables.

**Architecture:** Add an internal DNS resolver to the control plane that derives A records from healthy service instances stored in PostgreSQL. Agents configure containers to query that resolver and retain Compose container ports as internal service ports; the registry maps each logical service to a healthy node address and its internal port. A minimal two-service Compose fixture proves a service on one node resolves and reaches `metaapi:5000` on another.

**Tech Stack:** Go, `github.com/miekg/dns`, PostgreSQL, Docker Engine API, Docker Compose.

**Spec:** `docs/contracts/core-contracts.md`, `docs/compose-compatibility-matrix.md`, `docs/planning/m3-two-node-connectivity.md`, and user-approved requirement: Compose service URLs must use logical service names only.

## Global Constraints

- Preserve existing application values such as `META_API_URL=http://metaapi:5000/`; never replace them with node IPs or `*_HOST`/`*_PORT` endpoint variables.
- Support exactly two trusted private-network nodes for MVP; do not add custom overlay networking.
- Register only healthy/ready instances in DNS answers.
- Treat container port and optional published host/gateway port as separate fields.
- Keep databases and internal services unexposed unless explicitly requested.
- Use a project-scoped DNS suffix (`<service>.<project>.haco`) and a per-workload search domain so `metaapi` resolves within that project.
- The agent must still receive one bootstrap control-plane URL; service discovery after joining requires no manual LAN address.

## Review Focus

- Unknown service names must return NXDOMAIN, never a stale or arbitrary service endpoint.
- A failed or waiting workload must disappear from DNS answers immediately.
- Services from another project must not resolve through the short name in this project.
- A service with multiple healthy replicas must return each ready address deterministically.
- An internal-only container port must be discoverable without creating a host-port binding.

---

### Task 1: Extend the service-endpoint contract

**Files:**
- Modify: `model/model.go`
- Modify: `store/store.go`
- Modify: `controlplane/api.go`
- Test: `store/store_test.go`

**Interfaces:**
- Produces: `model.ServiceInstance{Project, Service, NodeID, Address, ContainerPort, Status}`.
- Produces: `Store.ListReadyServiceInstances(ctx, project, service) ([]model.ServiceInstance, error)`.

- [ ] **Step 1: Write failing store tests** for one ready instance, a non-ready instance exclusion, and an internal port without any host-published port.
- [ ] **Step 2: Run the store tests** against disposable PostgreSQL and verify they fail because the internal port is absent from the workload/service model.
- [ ] **Step 3: Add `ContainerPorts []int` to `model.Workload` and `ContainerPort int` to `model.ServiceInstance`**, persist them in the workloads table, and return only ready instances with their internal ports.
- [ ] **Step 4: Run the store tests** and verify ready endpoints include internal port `5000` while non-ready endpoints are absent.
- [ ] **Step 5: Commit** the endpoint-contract change.

### Task 2: Implement registry-backed DNS resolution

**Files:**
- Create: `discovery/dns.go`
- Create: `discovery/dns_test.go`
- Modify: `cmd/controlplane/main.go`
- Modify: `go.mod`

**Interfaces:**
- Consumes: `store.Store.ListReadyServiceInstances`.
- Produces: `discovery.NewServer(store *store.Store, suffix string) *Server` and `Server.ListenAndServe(ctx, addr string) error`.

- [ ] **Step 1: Write failing DNS tests** for `metaapi.demo.haco` resolving to a ready service address, an unknown service returning NXDOMAIN, and deterministic multi-record answers.
- [ ] **Step 2: Run `go test ./discovery -run TestResolver -v`** and verify the package/test fails before the resolver exists.
- [ ] **Step 3: Implement the UDP DNS handler** using `miekg/dns`; accept exact A-record names under `.haco.`, query the registry, use a short TTL, and return only ready endpoints.
- [ ] **Step 4: Start the DNS server from `cmd/controlplane`** with `HACO_DNS_LISTEN_ADDR` and default suffix `haco`.
- [ ] **Step 5: Run resolver tests and `go test ./...`** to verify names, NXDOMAIN, and deterministic answers.
- [ ] **Step 6: Commit** the resolver.

### Task 3: Configure workloads for service-name DNS

**Files:**
- Modify: `agent/docker.go`
- Modify: `agent/agent.go`
- Modify: `cmd/agent/main.go`
- Create: `agent/docker_test.go`

**Interfaces:**
- Consumes: `agent.Config{DNSAddress string, DNSSuffix string}`.
- Produces: Docker container configuration with `HostConfig.DNS` and `HostConfig.DNSSearch`.

- [ ] **Step 1: Write failing agent configuration tests** asserting a workload in project `demo` receives the configured resolver and search domain `demo.haco`.
- [ ] **Step 2: Run `go test ./agent -run TestContainerDNSConfig -v`** and verify it fails because Docker configuration lacks DNS settings.
- [ ] **Step 3: Add DNS resolver address/suffix to agent configuration** and configure Docker DNS plus the project search domain when creating containers.
- [ ] **Step 4: Remove `resolveDependencies` endpoint-variable injection** while retaining dependency-readiness gating; dependency resolution must not mutate workload environment values.
- [ ] **Step 5: Run agent tests and `go test ./...`** to verify no `*_HOST`/`*_PORT` injection remains.
- [ ] **Step 6: Commit** the agent DNS configuration change.

### Task 4: Preserve Compose internal ports and exposure rules

**Files:**
- Modify: `compose/compose.go`
- Create: `compose/compose_test.go`
- Modify: `docs/compose-compatibility-matrix.md`

**Interfaces:**
- Produces: `model.Workload.ContainerPorts` from every Compose port target.
- Produces: host bindings only from an explicit exposed/published-port policy.

- [ ] **Step 1: Write failing importer tests** for `metaapi` with `5600:5000`, asserting internal port `5000` is retained independently of published port `5600`.
- [ ] **Step 2: Run `go test ./compose -run TestPortMapping -v`** and verify it fails under the current 1:1 binding behavior.
- [ ] **Step 3: Convert Compose port mappings into separate internal container ports and optional published ports**, retaining only the internal port for DNS/service-to-service traffic.
- [ ] **Step 4: Update the compatibility matrix** to describe the distinction and remove the documented 1:1 limitation.
- [ ] **Step 5: Run compose tests and `go test ./...`**.
- [ ] **Step 6: Commit** the Compose-port update.

### Task 5: Add an executable two-node Compose test fixture

**Files:**
- Create: `examples/service-dns/docker-compose.yml`
- Create: `examples/service-dns/metaapi/Dockerfile`
- Create: `examples/service-dns/metaapi/server.go`
- Create: `examples/service-dns/client/Dockerfile`
- Create: `examples/service-dns/client/check.sh`
- Create: `docs/testing/service-name-dns.md`

**Interfaces:**
- Consumes: `META_API_URL=http://metaapi:5000/` supplied in the client service environment.
- Produces: a Compose project valid for `POST /v1/compose/plan` and deployable through `POST /v1/compose/deploy`.

- [ ] **Step 1: Add a failing end-to-end test script** that requests `http://metaapi:5000/` from the client and expects `metaapi-ok`; initially it must fail because DNS is not configured.
- [ ] **Step 2: Create the minimal Compose fixture**: `metaapi` listens internally on `5000`; `client` has only `META_API_URL=http://metaapi:5000/` and no endpoint-variable configuration.
- [ ] **Step 3: Document real two-host setup**: control-plane URL bootstrap, DNS port reachability, registry reachability, and one agent per Linux/Docker host.
- [ ] **Step 4: Run the fixture against two registered agents**, verify DNS inside `client`, HTTP response `metaapi-ok`, and absence of `META_API_HOST`/`META_API_PORT` overrides.
- [ ] **Step 5: Run `go test ./...` and the fixture script**, record actual output in the test document.
- [ ] **Step 6: Commit** the fixture and documentation.

## Self-review

- Every application-facing connection uses a service name and container port, not a node address.
- DNS is backed by the existing registry and contains only ready service instances.
- The existing two-node/private-network constraint is preserved.
- Docker host-port exposure is separated from internal service discovery.
- The test fixture proves the exact requested `META_API_URL=http://metaapi:5000/` behavior.
