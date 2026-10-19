# M1 — Scheduler Simulator Backlog

## Goal

Prove deterministic, explainable placement for two simulated nodes without Docker, networking, or a persistent database.

## Ordered work

- [x] Create the Go module and packages for `model`, `scheduler`, and `scheduler/testdata`.
- [x] Implement typed models for nodes, allocatable resources, workloads, constraints, reservations, and decisions.
- [x] Implement hard eligibility filters for node availability, architecture, labels/capabilities, CPU, memory, storage affinity, and host ports.
- [x] Implement stable candidate scoring with explicit tie breaking by node ID.
- [x] Implement reservation accounting so sequential scheduling cannot overcommit a node.
- [x] Produce dry-run plans that list candidates, exclusions, score factors, selection, and warnings.
- [x] Add table-driven unit tests for every filter and score tie.
- [x] Add two-node integration-style simulator fixtures covering stateless placement, storage pinning, resource exhaustion, architecture mismatch, and host-port conflict.
- [x] Add a reproducibility test that runs the same plan repeatedly and asserts identical results.

Implemented in [`model/`](../../model/model.go) and [`scheduler/`](../../scheduler/scheduler.go); scoring is CPU/memory headroom fit only (GPU/thermal/network/cache deferred to M7 per spec v0.2 §9.3).

## M1 exit criteria

Given two simulated nodes, a valid workload is placed within two seconds; an invalid placement is rejected with an actionable reason; and every result is deterministic and inspectable.
