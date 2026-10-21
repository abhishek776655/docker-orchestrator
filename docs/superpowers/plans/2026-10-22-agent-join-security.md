# Agent Join Security Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow a node agent to join with a one-time command, then use a unique credential over certificate-pinned HTTPS.

**Architecture:** Keep operator API access local and expose a separate TLS listener for agent routes. The control plane persists TLS identity and hashed bootstrap/agent secrets; the join bundle carries the endpoint, certificate fingerprint, and one-time secret. Agent credentials are stored locally under the existing HACO data directory.

**Tech Stack:** Go `net/http` + `crypto/tls`/`crypto/x509`, PostgreSQL via pgx, existing Go test suite.

**Spec:** `docs/superpowers/specs/2026-10-22-agent-join-security-design.md`

## Global Constraints

- Bootstrap token is high entropy, short-lived, one-use, stored only as a verifier, atomically consumed, and never logged.
- Agent verifies the pinned control-plane certificate before sending the bootstrap token.
- Every agent API request uses HTTPS and its unique credential; node credentials are bound to node IDs.
- Unauthenticated operator endpoints stay loopback-only by default.
- Preserve Compose service-name DNS and existing `META_API_URL=http://metaapi:5000/` behavior.

## Review Focus

- Invalid or replayed bootstrap token: reject without issuing credentials; cover in join-token tests.
- Wrong certificate pin: fail before token submission; cover with TLS client tests.
- Credential used for another node ID: reject; cover authorization middleware tests.
- Control-plane restart: preserve TLS identity and credential verifiers; cover store/TLS persistence tests.
- Remote unauthenticated operator request: reject; cover listener/middleware tests.

---

### Task 1: Persist bootstrap and node credentials

**Files:**
- Modify: `store/store.go`
- Test: `store/store_test.go`
- Create: `controlplane/auth.go`
- Test: `controlplane/auth_test.go`

**Interfaces:**
- Produce atomic `ConsumeJoinToken(ctx, tokenHash, now) (bool, error)`, `CreateJoinToken(ctx, tokenHash, nodeID, expiresAt) error`, `CreateAgentCredential(ctx, nodeID, tokenHash) error`, `VerifyAgentCredential(ctx, nodeID, tokenHash) (bool, error)`, and `RevokeAgentCredential(ctx, nodeID) error` store methods.
- Hash secrets with SHA-256 over random 256-bit values; compare fixed-size hashes in constant time in process.

- [ ] Write tests for successful one-time consume, expired/replayed token rejection, credential verification, cross-node rejection, and revocation.
- [ ] Run `go test ./store ./controlplane` and confirm the new tests fail because the APIs/schema are missing.
- [ ] Add idempotent PostgreSQL tables and the store methods; consume tokens transactionally.
- [ ] Run `go test ./store ./controlplane` and confirm pass (integration tests may skip if PostgreSQL is unavailable; unit tests must run).

### Task 2: Create persistent TLS identity and join bundles

**Files:**
- Create: `controlplane/tls.go`
- Test: `controlplane/tls_test.go`
- Modify: `cmd/controlplane/main.go`
- Modify: `controlplane/api.go`

**Interfaces:**
- `LoadOrCreateTLSIdentity(dir string) (tls.Certificate, string, error)` returns a persistent server identity and SHA-256 certificate fingerprint.
- `JoinBundle` contains HTTPS endpoint, fingerprint, random secret, and expiry; encoding uses URL-safe base64 JSON.
- Add loopback-only operator actions for minting a join bundle and revoking one node credential.

- [ ] Test certificate/key persistence across repeated loads and stable fingerprint.
- [ ] Test join bundle encode/decode and expiry.
- [ ] Run focused tests and confirm failures.
- [ ] Implement identity persistence with restrictive file permissions, random token generation, startup command printing, and operator management actions.
- [ ] Run `go test ./controlplane ./cmd/controlplane` and confirm pass.

### Task 3: Redeem join token from the agent CLI

**Files:**
- Create: `agent/join.go`
- Test: `agent/join_test.go`
- Modify: `cmd/agent/main.go`

**Interfaces:**
- `Join(ctx, bundle, nodeID, credentialPath) (AgentConfig, error)` verifies the pinned server certificate, redeems the one-use token, stores the returned node credential with owner-only permissions, and returns runtime config.
- CLI supports `haco-agent join <token>` and continues running the agent after successful join.

- [ ] Test rejection of wrong pin, invalid response, and already-consumed token; test credential file mode and contents.
- [ ] Run focused test and confirm failures.
- [ ] Implement join command and pin-verifying HTTPS transport; avoid logging token or credential values.
- [ ] Run `go test ./agent ./cmd/agent` and confirm pass.

### Task 4: Protect all agent API traffic with HTTPS credentials

**Files:**
- Modify: `controlplane/api.go`
- Modify: `controlplane/service.go`
- Modify: `agent/agent.go`
- Modify: `cmd/controlplane/main.go`
- Test: `controlplane/api_test.go`, `agent/agent_test.go`

**Interfaces:**
- Agent TLS mux exposes only join redemption, node register/heartbeat, assignment/status, and service-instance lookup routes.
- Middleware authenticates `Authorization: Bearer <credential>` and binds requests to that credential's node ID; path mismatch is forbidden.
- Existing operator API remains bound to loopback by default. Agent listener defaults to `0.0.0.0:8443` with persistent TLS identity.

- [ ] Test unauthenticated and invalid credentials are rejected; valid credential succeeds; cross-node heartbeat/assignment/status fails; remote operator access is denied.
- [ ] Run focused tests and confirm failures.
- [ ] Implement authenticated agent mux and make agent HTTP requests carry bearer credential through pinned TLS transport.
- [ ] Run `go test ./controlplane ./agent ./cmd/controlplane ./cmd/agent` and confirm pass.

### Task 5: Document and verify two-machine LAN onboarding

**Files:**
- Modify: `README.md`
- Modify: `docs/testing/service-name-dns.md`
- Create: `docs/testing/agent-join.md`

- [ ] Document Machine A startup, copied join command, Machine B join, HTTPS/DNS/registry/workload firewall ports, credential revocation/rejoin, and Compose `5000:5000` LAN smoke test.
- [ ] Add automated tests for token secret redaction and ensure `go test ./...` passes.
- [ ] Run `go test ./...` and record output. Do not claim a real two-host LAN test unless one is available and actually run.
