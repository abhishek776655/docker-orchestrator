import type { ApiEvent, ApiWorkload } from './api'
import type { GraphNodeInfo } from '@/components/deploy/dependency-graph'
import { liveMemGiB } from './memory'
import { liveState, stagesOf } from './plan'

const GIB = 1024 ** 3

export interface ServiceRow {
  name: string
  revision: string
  status: string
  reason: string
  node: string // "" while unplaced
  cpuCores: number
  memGiB: number
  /** RAM in use right now, GiB; null when unknown. */
  memLiveGiB: number | null
  waitsFor: { name: string; healthy: boolean }[]
  stopped: boolean
}

export interface ProjectDetail {
  name: string
  revisions: string[]
  revision: string // the one shown
  services: ServiceRow[]
  statusCounts: { status: string; count: number }[]
  cpuCores: number
  memGiB: number
  memLiveGiB: number | null
  nodes: string[]
  stages: number
  /** True when no service is desired running, so the rows are the stopped ones. */
  allStopped: boolean
}

/**
 * One project's workloads, for one revision. Desired-running workloads are the
 * project; when none are running the stopped ones are shown so the page still
 * has something to say. With no revision given, the newest one is used.
 */
export function deriveProject(name: string, workloads: ApiWorkload[], revision?: string): ProjectDetail | null {
  const mine = workloads.filter((w) => w.Project === name)
  if (mine.length === 0) return null
  const running = mine.filter((w) => w.DesiredState === 'running')
  const pool = running.length > 0 ? running : mine
  const revisions = [...new Set(pool.map((w) => w.Revision))].sort()
  const shown = revision && revisions.includes(revision) ? revision : revisions[revisions.length - 1]
  const list = pool.filter((w) => w.Revision === shown)

  const services = list
    .map((w): ServiceRow => {
      const healthy = new Set(w.DependsOnHealthy ?? [])
      return {
        name: w.Service,
        revision: w.Revision,
        status: w.Status,
        reason: w.StatusReason,
        node: w.NodeID,
        cpuCores: w.Resources.CPUMillis / 1000,
        memGiB: w.Resources.MemoryBytes / GIB,
        memLiveGiB: liveMemGiB([w]),
        waitsFor: (w.DependsOn ?? []).map((d) => ({ name: d, healthy: healthy.has(d) })),
        stopped: w.DesiredState === 'stopped',
      }
    })
    .sort((a, b) => a.name.localeCompare(b.name))

  const counts = new Map<string, number>()
  for (const s of services) counts.set(s.status, (counts.get(s.status) ?? 0) + 1)
  const stages = stagesOf(list.map((w) => ({ Service: w.Service, DependsOn: w.DependsOn })))

  return {
    name,
    revisions,
    revision: shown,
    services,
    statusCounts: [...counts.entries()].map(([status, count]) => ({ status, count })),
    cpuCores: services.reduce((n, s) => n + s.cpuCores, 0),
    memGiB: services.reduce((n, s) => n + s.memGiB, 0),
    memLiveGiB: liveMemGiB(list),
    nodes: [...new Set(services.map((s) => s.node).filter(Boolean))].sort(),
    stages: [...stages.values()].reduce((m, s) => Math.max(m, s + 1), 0),
    allStopped: running.length === 0,
  }
}

/** The dependency graph's nodes, with the live status as each card's line. */
export function graphNodesOf(project: ProjectDetail): GraphNodeInfo[] {
  const stages = stagesOf(project.services.map((s) => ({ Service: s.name, DependsOn: s.waitsFor.map((d) => d.name) })))
  const known = new Set(project.services.map((s) => s.name))
  return project.services.map((s) => ({
    name: s.name,
    stage: stages.get(s.name) ?? 0,
    deps: s.waitsFor.filter((d) => known.has(d.name)),
    state: liveState(s.status),
    line: s.status.replace(/_/g, ' '),
  }))
}

/** What is stuck, in one sentence: failed services and what waits behind them. */
export function blockedSummary(project: ProjectDetail): string {
  const failed = project.services.filter((s) => s.status === 'failed').map((s) => s.name)
  if (failed.length === 0) {
    const waiting = project.services.filter((s) => s.status === 'waiting_dependencies')
    return waiting.length ? `${waiting.length} ${waiting.length === 1 ? 'service is' : 'services are'} waiting for what they depend on.` : 'Nothing is blocked.'
  }
  const blocked = new Set<string>()
  const walk = (name: string) => {
    for (const s of project.services) {
      if (s.waitsFor.some((d) => d.name === name) && !blocked.has(s.name) && !failed.includes(s.name)) {
        blocked.add(s.name)
        walk(s.name)
      }
    }
  }
  failed.forEach(walk)
  const f = `${failed.join(', ')} failed`
  return blocked.size ? `${f}, so ${[...blocked].join(', ')} cannot start.` : `${f}.`
}

export interface EventGroup {
  event: ApiEvent // the newest of the run
  count: number
}

/**
 * Events about this project, newest first. A service that keeps reporting the
 * same message (waiting on its dependencies, say) would fill the list, so
 * consecutive identical events fold into one with a count.
 */
export function projectEvents(name: string, events: ApiEvent[], limit = 8): EventGroup[] {
  const mine = events.filter((e) => e.WorkloadKey.startsWith(`${name}/`)).sort((a, b) => b.ID - a.ID)
  const groups: EventGroup[] = []
  for (const e of mine) {
    const last = groups[groups.length - 1]
    if (last && last.event.WorkloadKey === e.WorkloadKey && last.event.Message === e.Message && last.event.Type === e.Type) last.count++
    else groups.push({ event: e, count: 1 })
  }
  return groups.slice(0, limit)
}
