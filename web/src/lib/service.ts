import type { ApiEvent, ApiInstance, ApiWorkload } from './api'

const GIB = 1024 ** 3

export interface ServiceDetail {
  project: string
  name: string
  revisions: { revision: string; stopped: boolean }[]
  revision: string // the one shown
  status: string
  reason: string
  desired: 'running' | 'stopped'
  node: string
  image: string
  cpuCores: number
  memGiB: number
  ports: { container: number; host: number }[]
  /** How sibling services reach it: "name:port", or "" when it publishes none. */
  internalName: string
  instances: ApiInstance[]
  dependsOn: { name: string; healthy: boolean }[]
  neededBy: { name: string; healthy: boolean; status: string }[]
  health: HealthView | null
  /** Why it runs where it does: facts the scheduler had to respect. */
  placement: { label: string; value: string; tone: 'hard' | 'soft' }[]
}

export interface HealthView {
  command: string
  timing: string
}

export function durationLabel(ns: number): string {
  const s = ns / 1e9
  if (s >= 3600 && s % 3600 === 0) return `${s / 3600}h`
  if (s >= 60 && s % 60 === 0) return `${s / 60}m`
  return `${Number.isInteger(s) ? s : s.toFixed(1)}s`
}

export function healthView(hc: ApiWorkload['HealthCheck']): HealthView | null {
  if (!hc || !hc.Test || hc.Test.length === 0 || hc.Test[0] === 'NONE') return null
  const [kind, ...rest] = hc.Test
  const command = kind === 'CMD' || kind === 'CMD-SHELL' ? rest.join(' ') : hc.Test.join(' ')
  const parts = [
    hc.Interval > 0 && `every ${durationLabel(hc.Interval)}`,
    hc.Timeout > 0 && `timeout ${durationLabel(hc.Timeout)}`,
    hc.Retries > 0 && `${hc.Retries} retries`,
    hc.StartPeriod > 0 && `${durationLabel(hc.StartPeriod)} start period`,
  ].filter(Boolean)
  return { command, timing: parts.length ? parts.join(' · ') : 'default timing' }
}

function placementFacts(w: ApiWorkload): ServiceDetail['placement'] {
  const facts: ServiceDetail['placement'] = []
  const c = w.Constraints
  if (c?.PinnedNodeID) facts.push({ label: 'Pinned to node', value: c.PinnedNodeID, tone: 'hard' })
  if (w.Storage?.PinnedNodeID) {
    facts.push({ label: 'Volume data lives on', value: w.Storage.PinnedNodeID, tone: 'hard' })
  }
  if (c?.Architecture) facts.push({ label: 'Architecture', value: c.Architecture, tone: 'hard' })
  if (c?.GPU.Required) {
    const vram = c.GPU.VRAMBytes > 0 ? `, ${(c.GPU.VRAMBytes / GIB).toFixed(0)} GB VRAM` : ''
    facts.push({ label: 'Needs a GPU', value: `${c.GPU.Vendor || 'any vendor'}${vram}`, tone: 'hard' })
  }
  if (c?.HostPorts?.length) facts.push({ label: 'Host ports', value: c.HostPorts.join(', '), tone: 'hard' })
  for (const [k, v] of Object.entries(c?.RequiredLabels ?? {})) facts.push({ label: 'Node label', value: `${k}=${v}`, tone: 'hard' })
  if (c?.RequiredCapabilities?.length) facts.push({ label: 'Node capabilities', value: c.RequiredCapabilities.join(', '), tone: 'hard' })
  return facts
}

/**
 * One service at one revision, with what surrounds it: its instances, what it
 * waits for and what waits for it. With no revision given, the newest running
 * one is used (or the newest of all when none is running).
 */
export function deriveService(
  project: string,
  service: string,
  workloads: ApiWorkload[],
  instances: ApiInstance[],
  revision?: string,
): ServiceDetail | null {
  const mine = workloads.filter((w) => w.Project === project && w.Service === service)
  if (mine.length === 0) return null
  const revisions = [...new Set(mine.map((w) => w.Revision))].sort()
  const running = mine.filter((w) => w.DesiredState === 'running').map((w) => w.Revision).sort()
  const shown = revision && revisions.includes(revision) ? revision : (running[running.length - 1] ?? revisions[revisions.length - 1])
  const w = mine.find((x) => x.Revision === shown)!

  const healthyDeps = new Set(w.DependsOnHealthy ?? [])
  const siblings = workloads.filter((x) => x.Project === project && x.Revision === shown && x.Service !== service)
  const ports = (w.PortBindings ?? []).map((p) => ({ container: p.ContainerPort, host: p.HostPort }))
  const firstPort = ports[0]?.container ?? w.ContainerPorts?.[0]

  return {
    project,
    name: service,
    revisions: revisions.map((r) => ({ revision: r, stopped: mine.find((x) => x.Revision === r)!.DesiredState === 'stopped' })),
    revision: shown,
    status: w.Status,
    reason: w.StatusReason,
    desired: w.DesiredState,
    node: w.NodeID,
    image: w.Image,
    cpuCores: w.Resources.CPUMillis / 1000,
    memGiB: w.Resources.MemoryBytes / GIB,
    ports,
    internalName: firstPort ? `${service}:${firstPort}` : '',
    instances: instances.filter((i) => i.Service === service),
    dependsOn: (w.DependsOn ?? []).map((name) => ({ name, healthy: healthyDeps.has(name) })),
    neededBy: siblings
      .filter((x) => (x.DependsOn ?? []).includes(service))
      .map((x) => ({ name: x.Service, healthy: (x.DependsOnHealthy ?? []).includes(service), status: x.Status }))
      .sort((a, b) => a.name.localeCompare(b.name)),
    health: healthView(w.HealthCheck),
    placement: placementFacts(w),
  }
}

export interface LifecycleStep {
  label: string
  state: 'done' | 'now' | 'todo' | 'bad'
  time: string // ISO, or "" when it has not happened
  note: string
}

const PATH = ['pending', 'waiting_dependencies', 'assigned', 'pulling', 'building', 'starting', 'running', 'ready']
// Steps shown even when never seen. pulling and building only show when they happened.
const ALWAYS = new Set(['pending', 'assigned', 'starting', 'running', 'ready'])

const NOTE: Record<string, string> = {
  pending: 'Submitted and waiting to be scheduled.',
  waiting_dependencies: 'Waiting for the services it depends on.',
  assigned: 'The scheduler picked a node.',
  pulling: 'The node is pulling the image.',
  building: 'The node is building the image from the shipped context.',
  starting: 'Container created, waiting for it to run.',
  running: 'Process is up. The healthcheck has not passed yet.',
  ready: 'Healthcheck passes. Services waiting for this one can start.',
  failed: 'The workload failed.',
  stopped: 'Stopped by an operator.',
}

/**
 * The path a workload takes, from submitted to ready, read from its status
 * events: what happened and when, where it is now, and what is still ahead.
 */
export function lifecycle(key: string, status: string, reason: string, events: ApiEvent[]): LifecycleStep[] {
  const seen = new Map<string, ApiEvent>()
  for (const e of [...events].filter((x) => x.WorkloadKey === key).sort((a, b) => a.ID - b.ID)) {
    const state = e.Message.split(':')[0].trim()
    if (state) seen.set(state, e)
  }
  const note = (state: string) => {
    const e = seen.get(state)
    const detail = e ? e.Message.slice(e.Message.indexOf(':') + 1).trim() : ''
    return detail || NOTE[state] || ''
  }
  const at = PATH.indexOf(status)
  const terminal = status === 'failed' || status === 'stopped'
  const steps: LifecycleStep[] = []
  for (const [i, state] of PATH.entries()) {
    if (!ALWAYS.has(state) && !seen.has(state) && state !== status) continue
    const wasSeen = seen.has(state)
    steps.push({
      label: state,
      state: state === status ? (status === 'ready' ? 'done' : 'now') : wasSeen || (at > i && !terminal) ? 'done' : 'todo',
      time: seen.get(state)?.CreatedAt ?? '',
      note: state === status || wasSeen ? note(state) : NOTE[state],
    })
  }
  if (terminal) {
    steps.push({ label: status, state: status === 'failed' ? 'bad' : 'now', time: seen.get(status)?.CreatedAt ?? '', note: reason || note(status) })
  }
  return steps
}
