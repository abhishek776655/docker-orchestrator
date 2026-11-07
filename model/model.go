// Package model defines the core scheduling types shared by the simulator
// and (later) the real scheduler: nodes, workloads, resources, and the
// recorded outcome of a placement attempt.
package model

import "time"

// Availability is the node's eligibility for new placements.
type Availability string

const (
	Available   Availability = "available"
	Unavailable Availability = "unavailable"
	Disabled    Availability = "disabled"
)

// StorageClass mirrors the spec's storage classes (see SRS 10.1). Only
// Ephemeral and NodeLocalPersistent are relevant to the M1 simulator.
type StorageClass string

const (
	Ephemeral           StorageClass = "ephemeral"
	NodeLocalPersistent StorageClass = "node_local_persistent"
)

// Resources is a CPU/memory request pair. Values are in millicores and
// bytes so they stay integral and comparable.
type Resources struct {
	CPUMillis   int64
	MemoryBytes int64
}

// Node is a scheduling candidate. Allocatable is capacity already reduced
// by system-reserved headroom (SRS 9.2) — the scheduler never re-derives
// that reservation itself.
type Node struct {
	ID           string
	Address      string
	Architecture string
	Labels       map[string]string
	Capabilities map[string]bool
	Allocatable  Resources
	Availability Availability
	// DisabledByAdmin is true once an operator has cordoned this node
	// (POST .../cordon), independent of heartbeat-driven Availability — so
	// a heartbeat or re-registration never silently un-cordons it. The
	// scheduler needs no separate check for this: cordoning also sets
	// Availability to Disabled, which the existing "!= Available" hard
	// filter already excludes.
	DisabledByAdmin bool
	HeartbeatAt     time.Time
	// GPUs is empty on a node with no GPU, or where GPU telemetry isn't
	// available — both look identical to the scheduler (SRS §9.2/§9.4).
	GPUs []GPU
	// Thermal is a best-effort signal, not a universal threshold (SRS
	// §9.5): Known is false when the host exposes no thermal sensors the
	// agent can read, and the scheduler must then treat it as neutral,
	// never as a rejection.
	Thermal Thermal
	// CachedImages are image references already present on this node's
	// Docker daemon, used only as a scoring signal (image cache locality,
	// SRS §9.3) — never a hard filter, since any node can still pull.
	CachedImages []string
}

// GPU is one discovered accelerator device (SRS §9.2, §9.4).
type GPU struct {
	Vendor         string
	Model          string
	VRAMTotalBytes int64
	VRAMFreeBytes  int64
}

// Thermal is a best-effort hardware signal (SRS §9.5): "Thermal data
// should be treated as a signal, not a universal temperature threshold."
type Thermal struct {
	Known        bool
	TemperatureC float64
	Throttling   bool
}

// GPURequirement is a workload's hard GPU constraint (SRS §9.2, UC
// "a workload with a hard GPU requirement is never placed on a node
// without a compatible GPU"). A workload with no requirement (the zero
// value has Required == false) is scheduled by CPU/memory alone.
type GPURequirement struct {
	Required  bool
	Vendor    string // empty matches any vendor
	VRAMBytes int64
}

// RemoteBuildSpec is the subset of compose.BuildSpec an agent needs to
// build an image itself, once it has the tarball — a self-contained copy
// rather than a model dependency on the compose package.
type RemoteBuildSpec struct {
	Dockerfile string            `json:"dockerfile"`
	Args       map[string]string `json:"args"`
	Platform   string            `json:"platform"`
	NoCache    bool              `json:"no_cache,omitempty"`
	// BuildID fingerprints this build's inputs and is stamped on the image
	// as a label; the agent rebuilds whenever its local image carries a
	// different one, so a source change actually reaches the node instead
	// of being skipped because an image with the same tag already exists.
	BuildID string `json:"build_id,omitempty"`
	// SSHIDs names the BuildKit SSH mounts (RUN --mount=type=ssh) this build
	// needs. Only the IDs live here: the key material is held in control-
	// plane memory and fetched over the build-ssh endpoint, never persisted.
	SSHIDs []string `json:"ssh_ids,omitempty"`
}

// StoragePolicy pins a workload to a node when it owns node-local
// persistent state (SRS 10.2): the scheduler must never place such a
// workload anywhere but PinnedNodeID.
type StoragePolicy struct {
	Class        StorageClass
	PinnedNodeID string // required when Class == NodeLocalPersistent

	// AllowFreshVolumeOnReschedule opts a pinned workload out of the
	// default safety rule (FR-017: pinned workloads are never auto-moved).
	// When true and the pinned node is unavailable, the scheduler may
	// place the workload on a different node with a brand-new, empty
	// volume — the pinned node's existing data is not migrated and is
	// effectively lost. Off by default; only safe for data that is
	// disposable (caches, scratch state).
	AllowFreshVolumeOnReschedule bool
}

// Constraints are the hard eligibility requirements for a workload
// PortBinding pairs one published container port with the host port it's
// bound to — see Workload.PortBindings.
type PortBinding struct {
	ContainerPort int
	HostPort      int
}

// (SRS 9.1 step 2). Any node failing one of these is rejected before
// scoring, regardless of score.
type Constraints struct {
	Architecture         string
	RequiredLabels       map[string]string
	RequiredCapabilities []string
	HostPorts            []int
	GPU                  GPURequirement

	// PinnedNodeID is a general node-affinity hard requirement, independent
	// of StoragePolicy.PinnedNodeID (which is specifically about
	// persistent-volume data locality and participates in the fresh-volume
	// reschedule waiver). Empty means no affinity.
	PinnedNodeID string
}

// Workload is a normalized placement request (SRS 12.1).
type Workload struct {
	Project  string
	Service  string
	Revision string
	Image    string
	// SkipPull is set by the control plane when Image was built locally
	// with no registry configured (registry-optional builds): such an
	// image was never pushed anywhere and has no registry prefix, so a
	// pull would always fail even though the image already exists on this
	// agent's own Docker daemon. False for every normally-pulled image,
	// including a registry-pushed build, where a pull must still happen
	// to pick up a newer build under the same mutable tag.
	SkipPull bool
	// RemoteBuild, when non-nil, tells the agent to build Image itself from
	// a build-context tarball it fetches from the control plane, instead of
	// pulling it — registry-optional builds, multi-machine case: this
	// workload was scheduled onto a node other than the control plane's own
	// host, so there is no way to get the image there except building it on
	// that node directly (no registry, and the image was never built
	// centrally). nil for every other workload, including a
	// local-only-built one that stays on the control-plane host (SkipPull
	// alone covers that case — no network transfer needed since it's the
	// same Docker daemon that built it).
	RemoteBuild *RemoteBuildSpec
	// Command overrides the image's default entrypoint arguments when
	// non-empty (e.g. telling an app to listen on a specific container
	// port it was assigned).
	Command     []string
	Env         map[string]string
	Resources   Resources
	Constraints Constraints
	// ContainerPorts are service listening ports for internal, name-based
	// communication. They are independent of optional host-published ports.
	ContainerPorts []int
	// PortBindings is the explicit container-port -> host-port pair for
	// every published port — independent host/container remapping (e.g.
	// publish 5600 for a container listening on 5000). Constraints.
	// HostPorts (just the host port numbers) is what the scheduler
	// reserves/conflict-checks; PortBindings is what the agent needs to
	// actually bind each one to the right container port, since they can
	// differ. A workload with no entry here for an otherwise-published
	// port (e.g. built directly via the raw submit API rather than
	// Compose) falls back to binding host port == container port, the
	// original M2 simplification.
	PortBindings []PortBinding
	Storage      StoragePolicy
	Volumes        []VolumeMount
	DesiredState   DesiredState
	// DependsOn lists logical service names (another workload's Service
	// field) that must have at least one ready instance before this
	// workload is started (SRS 7.1 depends_on, FR-014, UC-05).
	DependsOn []string
	// DependsOnHealthy is the subset of DependsOn declared with
	// `condition: service_healthy` rather than the default
	// service_started — those require an instance to have actually passed
	// its own Docker healthcheck (status ready), not merely be running;
	// every other entry in DependsOn is satisfied by running or ready.
	DependsOnHealthy []string
	// HealthCheck is this workload's own Docker healthcheck, when its
	// Compose service declares one and it isn't disabled — nil otherwise.
	// When set, the agent waits for Docker to report it healthy before
	// transitioning the workload to StatusReady, instead of doing so
	// immediately after the container starts running; that's also what
	// lets DependsOnHealthy mean something for whatever depends on this
	// workload.
	HealthCheck *HealthCheck
}

// HealthCheck is the Docker-native subset of a Compose healthcheck: block
// the control plane actually needs to pass to the container on creation
// and to interpret `docker inspect`'s reported status afterward.
type HealthCheck struct {
	Test        []string
	Interval    time.Duration
	Timeout     time.Duration
	Retries     int
	StartPeriod time.Duration
}

// VolumeMount is one node-local persistent mount for a workload with
// Storage.Class == NodeLocalPersistent (SRS §10). The host-side path is
// never specified directly — it is derived deterministically by the agent
// from the workload key (see agent.VolumeHostPath), so the same workload
// always lands on the same on-disk data no matter how many times its
// container is recreated (FR-016, FR-017).
type VolumeMount struct {
	ContainerPath string `json:"container_path"`
	ReadOnly      bool   `json:"read_only"`
	// HostPath, when non-empty, is a literal absolute path on the node's
	// own filesystem to bind-mount directly — a source-code live-mount
	// (x-haco-bind-host-path), as opposed to the normal case (empty) where
	// the agent manages its own directory for this mount. Only correct
	// when the workload is pinned to the same machine this path was
	// resolved on; compose.go enforces that at plan time.
	HostPath string `json:"host_path,omitempty"`
	// FileContent, when non-empty, is the literal bytes of a single-file
	// bind mount (e.g. a small ./config.json the container reads) read
	// once at plan time and carried with the workload itself — unlike
	// HostPath, this travels with the workload to whichever node it's
	// actually scheduled on, so this kind of mount never needs to pin a
	// workload to one specific host the way a directory live-mount does.
	// The agent writes it to its own managed file path before binding.
	// Mutually exclusive with HostPath.
	FileContent []byte `json:"file_content,omitempty"`
	// RemoteDirKey, when non-empty, names a directory-content tarball the
	// control plane has stashed for this volume (a directory bind mount
	// too large/dynamic to embed as FileContent, e.g. an application
	// source tree a service's Dockerfile doesn't copy in and relies on
	// entirely via the mount) — the agent fetches it over
	// GET .../volume-context/... and extracts it into its own managed
	// directory before binding, the same transfer idea as a remote build
	// context, just for a directory mount instead of a Docker build. Only
	// set when this workload is NOT pinned to the control-plane host the
	// literal directory lives on — a service explicitly pinned there
	// (x-haco-pin-host/--pin-host) gets the real HostPath instead, a true
	// live bind mount, since only that case can actually offer live-reload
	// (the snapshot this key points to is frozen at deploy time).
	// Mutually exclusive with HostPath and FileContent.
	RemoteDirKey string `json:"remote_dir_key,omitempty"`
	// SeedDirKey, when non-empty, names a one-time seed tarball the control
	// plane has stashed for this volume — the ordinary managed-volume case
	// (e.g. ./mysql-data:/var/lib/mysql), where the compose-declared host
	// directory already has real content (from running this stack outside
	// the orchestrator before) that's worth adopting as this managed
	// volume's starting point. Unlike RemoteDirKey, the agent only ever
	// fetches and extracts this ONCE: if its own managed directory for this
	// volume doesn't exist yet. Once created (seeded or not), the managed
	// copy is the sole source of truth from then on — a later change to
	// the original compose-declared directory is never picked up again,
	// and neither is this key reused on a later redeploy.
	SeedDirKey string `json:"seed_dir_key,omitempty"`
}

// DesiredState is the controller's intent for a workload, independent of
// its observed runtime status (SRS 12.1 "Runtime state").
type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
)

// Status is the agent-observed lifecycle state of a workload (SRS 12.1:
// "pending -> assigned -> pulling -> starting -> running -> ready").
// Failure states carry a machine-readable reason alongside Status.
type Status string

const (
	StatusPending  Status = "pending"
	StatusAssigned Status = "assigned"
	StatusPulling  Status = "pulling"
	// StatusBuilding is StatusPulling's counterpart for a RemoteBuild
	// workload: the agent is fetching its build context and building the
	// image itself instead of pulling one (registry-optional builds,
	// multi-machine case).
	StatusBuilding Status = "building"
	StatusStarting Status = "starting"
	StatusRunning  Status = "running"
	StatusReady    Status = "ready"
	StatusFailed   Status = "failed"
	StatusStopped  Status = "stopped"
	// StatusWaitingDependencies means the agent picked up the assignment
	// but at least one entry in DependsOn has no ready instance yet
	// (SRS UC-05, FR-014). The agent re-checks on its next poll.
	StatusWaitingDependencies Status = "waiting_dependencies"
)

// ServiceInstance is one reachable, ready endpoint for a logical service,
// derived live from workload + node state rather than stored separately
// (SRS 8.1-8.2, UC-06).
type ServiceInstance struct {
	Project string
	Service string
	NodeID  string
	Address string
	// ContainerPort is the internal port applications use with the service
	// name, for example metaapi:5000 — independent of any published host
	// port, and discoverable even when the workload publishes none.
	ContainerPort int
	Status        Status
}

// Key returns the stable identity used to key reservations and decisions.
func (w Workload) Key() string {
	return w.Project + "/" + w.Service + "/" + w.Revision
}

// Reservation is the capacity and ports a scheduled workload holds on a
// node. Reservations accumulate per node so sequential scheduling calls
// cannot overcommit (SRS FR-006, NFR-002).
type Reservation struct {
	NodeID      string
	WorkloadKey string
	Resources   Resources
	HostPorts   []int
}

// Rejection explains why one candidate node was excluded.
type Rejection struct {
	NodeID string
	Reason string
}

// ScoreFactors is the per-node breakdown behind a score, kept so every
// decision is explainable (SRS 9.3, FR-007).
type ScoreFactors struct {
	NodeID    string
	CPUFit    float64
	MemoryFit float64
	// ThermalFit and CacheFit are hardware-aware signals (SRS §9.3, M7):
	// 1.0 when the node runs cool / already has the image, degrading
	// toward 0 otherwise. ThermalFit is neutral (1.0) when the node
	// reports no thermal telemetry — unknown is never penalized (§9.5).
	ThermalFit float64
	CacheFit   float64
	Total      float64
}

// Decision is the full, inspectable outcome of one scheduling attempt
// (SRS 12.1 Placement, FR-007, FR-023).
type Decision struct {
	WorkloadKey string
	Candidates  []string
	Rejections  []Rejection
	Scores      []ScoreFactors
	Selected    string
	Warnings    []string
}
