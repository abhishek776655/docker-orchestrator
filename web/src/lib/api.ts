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
  ID: string
  Address: string
  Architecture: string
  Labels: Record<string, string> | null
  Capabilities: Record<string, boolean> | null
  Allocatable: { CPUMillis: number; MemoryBytes: number }
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
  events: (limit = 8) => request<ApiEvent[] | null>(`/v1/events?limit=${limit}`).then((v) => v ?? []),
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
