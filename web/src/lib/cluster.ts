import type { Tone } from '@/components/ui/badge'
import type { ApiEvent, ApiGPU, ApiNode, ApiWorkload } from './api'
import { liveMemGiB } from './memory'

export type NodeState = 'available' | 'throttling' | 'cordoned' | 'offline'
export type OsKind = 'darwin' | 'linux' | 'windows' | 'unknown'

export interface NodeView {
  id: string
  name: string // display name, or the ID when none is set
  limits: { cpu: number; mem: number } // cores / GiB; 0 = no cap
  detected: { cpu: number; mem: number }
  address: string
  arch: string
  os: OsKind
  isHost: boolean
  state: NodeState
  labels: Record<string, string>
  cpuUsed: number
  cpuCap: number
  memUsed: number
  memCap: number
  /** RAM its containers actually use right now, GiB; null when unknown. */
  memLive: number | null
  gpus: ApiGPU[]
  thermal: ApiNode['Thermal']
  heartbeatAgeSec: number | null
  workloads: ApiWorkload[]
  cachedImages: string[]
}

const GIB = 1024 ** 3

export const LABEL_OS = 'os'
export const LABEL_ROLE = 'haco.io/role'

function parseOs(raw: string | undefined): OsKind {
  switch ((raw ?? '').toLowerCase()) {
    case 'darwin':
    case 'macos':
      return 'darwin'
    case 'linux':
      return 'linux'
    case 'windows':
      return 'windows'
    default:
      return 'unknown'
  }
}

function nodeState(n: ApiNode): NodeState {
  if (n.DisabledByAdmin || n.Availability === 'disabled') return 'cordoned'
  if (n.Availability !== 'available') return 'offline'
  return n.Thermal?.Known && n.Thermal.Throttling ? 'throttling' : 'available'
}

// Matches store.ActiveReservations: every workload placed on the node that
// has not ended (failed or stopped) holds its CPU and memory request.
function holdsReservation(w: ApiWorkload) {
  return w.NodeID !== '' && w.Status !== 'failed' && w.Status !== 'stopped'
}

export function deriveNodes(nodes: ApiNode[], workloads: ApiWorkload[], now = Date.now()): NodeView[] {
  return nodes.map((n) => {
    const mine = workloads.filter((w) => w.NodeID === n.ID && holdsReservation(w))
    const heartbeat = Date.parse(n.HeartbeatAt)
    const labels = n.Labels ?? {}
    return {
      id: n.ID,
      name: n.DisplayName || n.ID,
      limits: { cpu: (n.Limits?.CPUMillis ?? 0) / 1000, mem: (n.Limits?.MemoryBytes ?? 0) / GIB },
      detected: { cpu: (n.Detected?.CPUMillis ?? n.Allocatable.CPUMillis) / 1000, mem: (n.Detected?.MemoryBytes ?? n.Allocatable.MemoryBytes) / GIB },
      address: n.Address,
      arch: n.Architecture,
      os: parseOs(labels[LABEL_OS]),
      isHost: labels[LABEL_ROLE] === 'host',
      state: nodeState(n),
      labels,
      cpuUsed: mine.reduce((sum, w) => sum + w.Resources.CPUMillis, 0) / 1000,
      cpuCap: n.Allocatable.CPUMillis / 1000,
      memUsed: mine.reduce((sum, w) => sum + w.Resources.MemoryBytes, 0) / GIB,
      memCap: n.Allocatable.MemoryBytes / GIB,
      memLive: liveMemGiB(mine),
      gpus: n.GPUs ?? [],
      thermal: n.Thermal ?? { Known: false, TemperatureC: 0, Throttling: false },
      heartbeatAgeSec: Number.isNaN(heartbeat) || heartbeat <= 0 ? null : Math.max(0, Math.round((now - heartbeat) / 1000)),
      workloads: workloads.filter((w) => w.NodeID === n.ID),
      cachedImages: n.CachedImages ?? [],
    }
  })
}

export interface ProjectView {
  name: string
  services: number
  revisions: string[]
  statusCounts: { status: string; count: number }[]
  /** RAM in use right now across the project, GiB; null when unknown. */
  memLiveGiB: number | null
}

export function deriveProjects(workloads: ApiWorkload[]): ProjectView[] {
  const byProject = new Map<string, ApiWorkload[]>()
  // Stopped projects stay listed: they still have records (and pins) that
  // can block a redeploy, so hiding them leaves nothing to inspect or remove.
  for (const w of workloads) {
    byProject.set(w.Project, [...(byProject.get(w.Project) ?? []), w])
  }
  return [...byProject.entries()]
    .map(([name, list]) => {
      const counts = new Map<string, number>()
      for (const w of list) counts.set(w.Status, (counts.get(w.Status) ?? 0) + 1)
      return {
        name,
        services: new Set(list.map((w) => w.Service)).size,
        revisions: [...new Set(list.map((w) => w.Revision))].sort(),
        statusCounts: [...counts.entries()].map(([status, count]) => ({ status, count })),
        memLiveGiB: liveMemGiB(list),
      }
    })
    .sort((a, b) => a.name.localeCompare(b.name))
}

export function statusTone(status: string): Tone {
  if (status === 'ready') return 'ok'
  if (status === 'failed') return 'bad'
  if (status === 'stopped') return 'off'
  return 'warn'
}

export function stateTone(state: NodeState): Tone {
  switch (state) {
    case 'available':
      return 'ok'
    case 'throttling':
      return 'warn'
    case 'offline':
      return 'bad'
    default:
      return 'off'
  }
}

export function eventTone(e: ApiEvent): Tone {
  // The control plane records most events as type "status" with the state in
  // the message ("failed: ..."), so read both.
  const text = `${e.Type} ${e.Message}`.toLowerCase()
  if (text.includes('fail') || text.includes('error')) return 'bad'
  if (text.includes('thermal') || text.includes('throttl') || text.includes('warn') || text.includes('waiting')) return 'warn'
  if (text.includes('sched') || text.includes('place')) return 'info'
  if (text.includes('ready')) return 'ok'
  return 'off'
}

// "offline" claims the machine is down. All the control plane knows is that
// heartbeats stopped, and the machine can be up while its agent is busy or
// not running, so say that instead.
export const STATE_LABEL: Record<NodeState, string> = {
  available: 'available',
  throttling: 'throttling',
  cordoned: 'cordoned',
  offline: 'no heartbeat',
}

export function offlineHint(n: NodeView): string {
  const since = n.heartbeatAgeSec === null ? 'No heartbeat yet.' : `No heartbeat for ${ageLabel(n.heartbeatAgeSec)}.`
  const busy = n.workloads.find((w) => w.Status === 'building' || w.Status === 'pulling')
  return busy
    ? `${since} It is ${busy.Status} ${busy.Project}/${busy.Service}. Agents built before the heartbeat fix stop heartbeating during a build.`
    : `${since} The machine may be up while its agent is stopped or unreachable.`
}

export const OS_LABEL: Record<OsKind, string> = {
  darwin: 'macOS',
  linux: 'Linux',
  windows: 'Windows',
  unknown: 'OS unknown',
}

export function ageLabel(seconds: number | null): string {
  if (seconds === null) return 'never'
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`
  return `${Math.floor(seconds / 86400)}d`
}

export function sinceLabel(iso: string, now = Date.now()): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return ''
  return `${ageLabel(Math.max(0, Math.round((now - t) / 1000)))} ago`
}

export function thermalLabel(n: NodeView): string {
  if (!n.thermal.Known) return 'No sensor'
  return `${Math.round(n.thermal.TemperatureC)} °C · ${n.thermal.Throttling ? 'throttling' : 'cool'}`
}

export function gpuLabel(g: ApiGPU): string {
  const free = (g.VRAMFreeBytes / GIB).toFixed(1)
  const total = (g.VRAMTotalBytes / GIB).toFixed(0)
  return `${g.Model} · ${free} / ${total} GB free`
}
