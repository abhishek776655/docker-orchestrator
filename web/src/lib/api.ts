// Wire types mirror the Go structs the control plane encodes. Most of them
// carry no json tags, so field names are the Go names (ID, CPUMillis, ...).
// Go encodes nil slices and maps as null, so those fields are nullable here.

export interface ApiGPU {
  Vendor: string
  Model: string
  VRAMTotalBytes: number
  VRAMFreeBytes: number
}

export interface ApiNode {
  ID: string // the machine's permanent identity; never changes
  DisplayName?: string // operator's name for it, "" when unset
  Address: string
  Architecture: string
  Labels: Record<string, string> | null
  Capabilities: Record<string, boolean> | null
  Allocatable: { CPUMillis: number; MemoryBytes: number } // detected, capped by Limits
  Detected?: { CPUMillis: number; MemoryBytes: number }
  Limits?: { CPUMillis: number; MemoryBytes: number } // 0 = no cap (haco node limit)
  Availability: 'available' | 'unavailable' | 'disabled'
  DisabledByAdmin: boolean
  HeartbeatAt: string
  GPUs: ApiGPU[] | null
  Thermal: { Known: boolean; TemperatureC: number; Throttling: boolean }
  CachedImages: string[] | null
}

export interface ApiWorkload {
  Project: string
  Service: string
  Revision: string
  Image: string
  Resources: { CPUMillis: number; MemoryBytes: number }
  DesiredState: 'running' | 'stopped'
  Status: string
  StatusReason: string
  NodeID: string
  /** Live memory use reported by the node; absent when unknown. */
  MemoryUsedBytes?: number
  DependsOn?: string[] | null
  DependsOnHealthy?: string[] | null
  ContainerID?: string
  // Durations are nanoseconds (Go's time.Duration); 0 means "not set".
  HealthCheck?: { Test: string[] | null; Interval: number; Timeout: number; Retries: number; StartPeriod: number } | null
  Constraints?: {
    Architecture: string
    RequiredLabels: Record<string, string> | null
    RequiredCapabilities: string[] | null
    HostPorts: number[] | null
    GPU: { Required: boolean; Vendor: string; VRAMBytes: number }
    PinnedNodeID: string
  }
  PortBindings?: { ContainerPort: number; HostPort: number }[] | null
  ContainerPorts?: number[] | null
  Storage?: { Class: string; PinnedNodeID: string }
}

// A ready (or starting) instance of a service, as `GET /v1/services/{project}`.
export interface ApiInstance {
  Project: string
  Service: string
  NodeID: string
  Address: string
  ContainerPort: number
  Status: string
}

export interface ApiEvent {
  ID: number
  WorkloadKey: string
  NodeID: string
  Type: string
  Message: string
  CreatedAt: string
}

export interface JoinBundle {
  endpoint: string
  fingerprint: string
  dns_suffix: string
  secret: string
  expires_at: string
}

// A machine seen on the control plane's network that has not joined.
export interface Discovered {
  address: string
  hostname?: string
  mac?: string // missing when the device was found by probing rather than ARP
}

// What `POST /v1/compose/plan` and `/v1/compose/deploy` take. See
// controlplane/api.go (composeRequest).
export interface ComposeRequest {
  project: string
  working_dir: string
  yaml: string
  no_cache: boolean
  only_service: string
  // null means no override; a list pins those services to the control-plane host.
  pin_host: string[] | null
  // node id -> services pinned to that node.
  pin_nodes: Record<string, string[]>
  fresh: boolean
  // Services whose pin_nodes node must be used even if the scheduler rejects it.
  force?: string[]
}

export interface PlanWorkload {
  Project: string
  Service: string
  Revision: string
  Image: string
  Resources: { CPUMillis: number; MemoryBytes: number }
  Constraints: {
    HostPorts: number[] | null
    GPU: { Required: boolean; Vendor: string; VRAMBytes: number }
    PinnedNodeID: string
  }
  PortBindings: { ContainerPort: number; HostPort: number }[] | null
  Storage: { Class: string; PinnedNodeID: string }
  DependsOn: string[] | null
  DependsOnHealthy: string[] | null
}

export interface PlanData {
  project: string
  workloads: PlanWorkload[] | null
  builds: { Service: string; Context: string }[] | null
  networking: { mode: string; supported: boolean; reasons?: string[] } // lower-case: this struct has json tags
  warnings: string[] | null
  rejections: string[] | null
}

export interface ScoreFactors {
  NodeID: string
  CPUFit: number
  MemoryFit: number
  ThermalFit: number
  CacheFit: number
  Total: number
}

export interface Decision {
  WorkloadKey: string
  Candidates: string[] | null
  Rejections: { NodeID: string; Reason: string }[] | null
  Scores: ScoreFactors[] | null
  Selected: string
  Warnings: string[] | null
}

export interface PlanResponse {
  plan: PlanData
  decisions: Decision[] | null
}

export interface DirListing {
  path: string
  parent: string
  home: string
  dirs: string[]
  has_compose: boolean
  compose_file: string
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(`${status} ${message}`)
  }
}

async function postJson(path: string, body: unknown): Promise<Response> {
  return fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
}

const OUTDATED =
  'The control plane is an older version without this action. Run ./install.sh, then haco stop-cluster and haco start --web (and haco agent run on other machines).'

async function errorFrom(res: Response): Promise<ApiError> {
  // An older control plane routes unknown POST actions to a GET-only path.
  if (res.status === 405) return new ApiError(405, OUTDATED)
  let detail = res.statusText
  try {
    const body = (await res.json()) as { error?: string; plan?: PlanData }
    if (body.error) detail = body.error
    else if (body.plan?.rejections?.length) detail = body.plan.rejections.join('; ')
  } catch {
    // body was not JSON; keep the status text
  }
  return new ApiError(res.status, detail)
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init)
  if (!res.ok) {
    let detail = res.statusText
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) detail = body.error
    } catch {
      // body was not JSON; keep the status text
    }
    throw new Error(`${res.status} ${detail}`)
  }
  return (await res.json()) as T
}

export const api = {
  nodes: () => request<ApiNode[] | null>('/v1/nodes').then((v) => v ?? []),
  workloads: () => request<ApiWorkload[] | null>('/v1/workloads').then((v) => v ?? []),
  discovered: () =>
    request<{ devices: Discovered[] | null; hint?: string }>('/v1/discovered').then((v) => ({ devices: v.devices ?? [], hint: v.hint })),
  events: (limit = 8) => request<ApiEvent[] | null>(`/v1/events?limit=${limit}`).then((v) => v ?? []),
  // A 422 still carries the plan (with its rejections), so it is a result, not an error.
  composePlan: async (req: ComposeRequest): Promise<PlanResponse> => {
    const res = await postJson('/v1/compose/plan', req)
    if (!res.ok && res.status !== 422) throw await errorFrom(res)
    return (await res.json()) as PlanResponse
  },
  composeDeploy: async (req: ComposeRequest): Promise<void> => {
    const res = await postJson('/v1/compose/deploy', req)
    if (!res.ok) throw await errorFrom(res)
  },
  // Stops the container and keeps a "stopped" row.
  stopWorkload: async (project: string, service: string, revision: string): Promise<void> => {
    const path = [project, service, revision].map(encodeURIComponent).join('/')
    const res = await postJson(`/v1/workloads/${path}/stop`, {})
    if (!res.ok) throw await errorFrom(res)
  },
  // Deletes every workload record of one project; agents then remove the
  // containers on their next poll. An empty name would delete every project's
  // records, so it is refused here.
  removeProject: async (project: string): Promise<number> => {
    if (project.trim() === '') throw new Error('A project name is required.')
    const res = await fetch(`/v1/workloads?project=${encodeURIComponent(project)}`, { method: 'DELETE' })
    if (!res.ok) throw await errorFrom(res)
    return ((await res.json()) as { count: number }).count
  },
  instances: (project: string) =>
    request<ApiInstance[] | null>(`/v1/services/${encodeURIComponent(project)}`).then((v) => v ?? []),
  // A text tail of the container's logs; live from the node when it answers.
  logs: async (project: string, service: string, revision: string, tail = 200): Promise<string> => {
    const path = [project, service, revision].map(encodeURIComponent).join('/')
    const res = await fetch(`/v1/workloads/${path}/logs?tail=${tail}`)
    if (!res.ok) throw await errorFrom(res)
    return res.text()
  },
  // Deletes one workload record; its node removes the container on its next poll.
  removeWorkload: async (project: string, service: string, revision: string): Promise<void> => {
    const path = [project, service, revision].map(encodeURIComponent).join('/')
    const res = await fetch(`/v1/workloads/${path}`, { method: 'DELETE' })
    if (!res.ok) throw await errorFrom(res)
  },
  // Node admin. Cordon stops new placements; revoke cuts the agent's credential.
  nodeAction: async (id: string, action: 'cordon' | 'uncordon' | 'revoke'): Promise<void> => {
    const res = await postJson(`/v1/admin/nodes/${encodeURIComponent(id)}/${action}`, {})
    if (!res.ok) throw await errorFrom(res)
  },
  // Refused with 409 while workloads still run there, unless forced.
  removeNode: async (id: string, force = false): Promise<void> => {
    const res = await fetch(`/v1/admin/nodes/${encodeURIComponent(id)}${force ? '?force=true' : ''}`, { method: 'DELETE' })
    if (!res.ok) throw await errorFrom(res)
  },
  // Only the display name changes; the node ID is permanent.
  setNodeName: async (id: string, name: string): Promise<void> => {
    const res = await postJson(`/v1/admin/nodes/${encodeURIComponent(id)}/name`, { name })
    if (!res.ok) throw await errorFrom(res)
  },
  // The node's agent deletes them on its next control poll; results arrive as events.
  removeNodeImages: async (id: string, images: string[]): Promise<void> => {
    const res = await postJson(`/v1/admin/nodes/${encodeURIComponent(id)}/images/remove`, { images })
    if (!res.ok) throw await errorFrom(res)
  },
  // Deletes workloads' volume data on one node; refused (409) while a workload still runs there.
  removeVolumes: async (nodeId: string, workloads: string[]): Promise<void> => {
    const res = await postJson(`/v1/admin/nodes/${encodeURIComponent(nodeId)}/volumes/remove`, { workloads })
    if (!res.ok) throw await errorFrom(res)
  },
  // Start runs a stopped workload again on its last node; restart recreates a running one's container.
  workloadAction: async (project: string, service: string, revision: string, action: 'start' | 'restart'): Promise<void> => {
    const path = [project, service, revision].map(encodeURIComponent).join('/')
    const res = await postJson(`/v1/workloads/${path}/${action}`, {})
    if (!res.ok) throw await errorFrom(res)
  },
  // Folders on the control-plane machine, for the project-folder picker.
  listDirs: async (path: string): Promise<DirListing> => {
    const res = await fetch(`/v1/fs/dirs?path=${encodeURIComponent(path)}`)
    if (!res.ok) throw await errorFrom(res)
    return (await res.json()) as DirListing
  },
  // The compose file directly inside a folder on the control-plane machine,
  // with that folder's absolute path (which a browser upload can't provide).
  readCompose: async (dir: string): Promise<{ dir: string; name: string; yaml: string }> => {
    const res = await fetch(`/v1/fs/compose?dir=${encodeURIComponent(dir)}`)
    if (!res.ok) throw await errorFrom(res)
    return (await res.json()) as { dir: string; name: string; yaml: string }
  },
  issueJoinToken: () =>
    request<JoinBundle>('/v1/admin/join-token', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
    }),
}

// Same encoding as agentjoin.Encode: base64url (no padding) of the bundle JSON.
export function encodeJoinBundle(bundle: JoinBundle): string {
  const bytes = new TextEncoder().encode(JSON.stringify(bundle))
  let binary = ''
  bytes.forEach((b) => (binary += String.fromCharCode(b)))
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}
