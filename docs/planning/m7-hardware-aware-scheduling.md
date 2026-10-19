# M7 — Hardware-Aware Scheduling

## Goal

Collect real hardware telemetry (CPU/memory already existed; GPU, thermal,
image-cache locality added here) and use it as scoring/hard-filter signals,
so a placement decision explains its hardware-based preferences (SRS
milestone M7, §9.2–9.5).

## Implemented

- [x] `model.Node` gains `GPUs []GPU`, `Thermal`, `CachedImages` — telemetry
      that looks identical whether a node genuinely has none or the agent
      simply can't detect it, by design (SRS §9.4/§9.5: unknown must never
      be scored as a failure).
- [x] `model.Constraints.GPU` (`GPURequirement{Required, Vendor,
      VRAMBytes}`): a hard filter. `scheduler.rejectGPU` rejects any node
      without a matching-vendor GPU or with insufficient free VRAM —
      matches the SRS §17 acceptance criterion verbatim ("a workload with a
      hard GPU requirement is never placed on a node without a compatible
      GPU"), including VRAM reservation accounting via
      `SeedVRAMReservation`/`reserve` so sequential GPU placements can't
      overcommit one node's VRAM within a scheduling session.
- [x] Scoring (`scheduler.score`) adds `ThermalFit` and `CacheFit` to the
      existing CPU/memory headroom fit, with `DefaultWeights{CPU: 0.4,
      Memory: 0.4, Thermal: 0.1, Cache: 0.1}`. GPU has no scoring weight —
      it's a hard filter only (FR-022's scope: GPU scheduling *after*
      CPU/memory is reliable, not full GPU-aware scoring).
  - `thermalFit`: 1.0 (neutral) whenever `Thermal.Known == false` — this is
    the concrete enforcement of SRS §9.5, not just a comment. A reported
    `Throttling` node scores 0; otherwise fit degrades linearly from 60°C
    to 90°C.
  - `cacheFit`: 1.0 when the workload's image is already present on the
    node (`Node.CachedImages`), 0 otherwise — saves pull time without ever
    being a hard requirement.
- [x] `agent/telemetry.go`: real, best-effort collection, not simulated —
  - **Thermal**: reads `/sys/class/thermal/thermal_zone*/temp` on Linux;
    `Known: false` everywhere else (this dev machine is macOS, so every
    live run here genuinely reports unknown, not a mocked value).
  - **GPU**: shells out to `nvidia-smi` if present; absent or failing
    (this machine, Docker Desktop, no NVIDIA GPU) → empty list, not an
    error.
  - **Cached images**: `Docker.CachedImages` via the real Docker Engine
    `ImageList` API.
  - All three are sent on registration and refreshed every heartbeat (SRS
    6.2 "send heartbeat/resource snapshot").
- [x] `store`/API wiring: `nodes` table gains `gpus`, `thermal`,
      `cached_images` jsonb columns; `POST /v1/nodes` and `POST
      /v1/nodes/{id}/heartbeat` both accept and persist telemetry.
- [x] 6 new scheduler tests: GPU hard filter (device absent, insufficient
      VRAM), thermal preference (hot/throttling loses to cool), unknown
      thermal is neutral not penalized, cache-locality preference — plus
      all 9 pre-existing M1 scheduler tests still pass unchanged.

## Verified end-to-end (this machine)

- A real agent registered with genuine telemetry: `GPUs: null` (no NVIDIA
  GPU — correctly detected, not assumed), `Thermal.Known: false` (no Linux
  thermal sysfs on macOS), and a real `CachedImages` list pulled live from
  this machine's Docker daemon (45+ actual image tags).
- A submitted workload's `Decision.Scores` showed `ThermalFit: 1` for a
  node whose thermal telemetry is unknown — live proof of the "never
  penalize unknown" rule, not just a unit test assertion.

## Known M7 limitations

- **GPU VRAM reservation isn't persisted across control-plane restarts**
  (unlike CPU/memory, which are re-derived from `ActiveReservations` on
  every submit). There's no real GPU in this environment to exercise that
  path against, so building the persistence column now would be
  speculative; the hard-filter and within-session reservation accounting
  are both implemented and tested.
- **No live multi-node cache-fit proof on this machine**: the local
  simulation's two "nodes" share one Docker daemon (same limitation
  documented in M3–M6), so they always report identical cached images —
  the signal is proven by unit test (`TestCacheFitPrefersNodeWithImageAlready`)
  instead of a live two-node divergence.
- **GPU detection is NVIDIA-only** (`nvidia-smi`). AMD/Intel GPU discovery
  is not implemented.
- **Network locality** (SRS §9.3's `w_network * network_locality` term) has
  no real signal in this prototype and is left out of `Weights` entirely
  rather than faked with a constant.
- Thermal throttling detection is a crude threshold (≥95°C) in the absence
  of a portable, driver-independent throttle signal across hardware — SRS
  §9.5 explicitly prefers an explicit indicator "where available," which
  this dev environment has no way to provide.

## Exit criterion

Met: a placement decision's `ScoreFactors` explains CPU, memory, thermal,
and cache-locality preferences per node, and a GPU-requiring workload is
provably (hard filter + VRAM check, both tested) never placed on an
incompatible node.
