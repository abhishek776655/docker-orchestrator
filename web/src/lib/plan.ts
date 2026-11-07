import { parseDocument } from 'yaml'
import type { ComposeRequest, Decision, PlanResponse } from './api'
import { summarizeCompose } from './compose-summary'
import { dependencyClosure, type DeployConfig } from './deploy-config'

/**
 * The file with only the chosen service and what it depends on. The control
 * plane reads every service (env files, build contexts) before it narrows the
 * deploy to one, so a service you did not pick could still fail the plan.
 * Returns the text unchanged when everything is in scope or it cannot be read.
 */
export function pruneToScope(yaml: string, config: DeployConfig): string {
  if (config.scope !== 'one') return yaml
  const parsed = summarizeCompose(yaml)
  if (!parsed.ok || !parsed.summary.services.includes(config.onlyService)) return yaml
  const keep = new Set(dependencyClosure(parsed.summary.details, config.onlyService))
  if (keep.size === parsed.summary.services.length) return yaml
  const doc = parseDocument(yaml)
  const services = doc.get('services', true)
  if (!services || typeof (services as { items?: unknown }).items === 'undefined') return yaml
  for (const name of parsed.summary.services) if (!keep.has(name)) doc.deleteIn(['services', name])
  return doc.toString()
}

/** The request the control plane's plan and deploy endpoints take, from the choices made in steps 2 and 3. */
export function buildRequest(yaml: string, config: DeployConfig, withPlacements: boolean): ComposeRequest {
  const pinNodes: Record<string, string[]> = {}
  if (withPlacements) {
    for (const [service, node] of Object.entries(config.placements)) (pinNodes[node] ??= []).push(service)
  }
  return {
    project: config.project,
    working_dir: config.workingDir.trim(),
    yaml: pruneToScope(yaml, config),
    no_cache: config.noCache,
    only_service: config.scope === 'one' ? config.onlyService : '',
    pin_host: config.hostServices.length > 0 ? config.hostServices : null,
    pin_nodes: pinNodes,
    fresh: config.fresh,
  }
}

export interface Candidate {
  node: string
  outcome: 'selected' | 'eligible' | 'rejected'
  score?: number
  detail: string
  isPick: boolean // the scheduler's own choice
  factors?: { label: string; value: number }[] // each 0 (worst) to 1 (best)
}

export interface PlanService {
  name: string
  source: string
  cpuCores: number
  memGiB: number
  stage: number // 0-based
  deps: { name: string; healthy: boolean }[]
  gpu: boolean
  build: boolean
  pinnedTo: string // node its volume or constraint pins it to, or ""
  ports: number[]
  node: string // where it will run, with any change applied
  score: number | null
  pickNode: string // where the scheduler would put it unaided
  changed: boolean
  kept: boolean // kept on the host in Configuration
  warnings: string[]
  candidates: Candidate[]
}

const GIB = 1024 ** 3

/** Longest dependency chain below each service: 0 for services that wait on nothing. */
export function stagesOf(workloads: { Service: string; DependsOn?: string[] | null }[]): Map<string, number> {
  const deps = new Map(workloads.map((w) => [w.Service, w.DependsOn ?? []]))
  const memo = new Map<string, number>()
  const visiting = new Set<string>()
  const stage = (name: string): number => {
    const known = memo.get(name)
    if (known !== undefined) return known
    if (visiting.has(name)) return 0 // a cycle is rejected by the planner; do not loop here
    visiting.add(name)
    const d = (deps.get(name) ?? []).filter((x) => deps.has(x))
    const s = d.length ? 1 + Math.max(...d.map(stage)) : 0
    visiting.delete(name)
    memo.set(name, s)
    return s
  }
  for (const w of workloads) stage(w.Service)
  return memo
}

function candidatesOf(base: Decision | undefined, selected: string): Candidate[] {
  if (!base) return []
  const eligible = [...(base.Scores ?? [])]
    .sort((a, b) => b.Total - a.Total)
    .map((s): Candidate => ({
      node: s.NodeID,
      outcome: s.NodeID === selected ? 'selected' : 'eligible',
      score: s.Total,
      detail: `CPU ${s.CPUFit.toFixed(2)} · memory ${s.MemoryFit.toFixed(2)} · thermal ${s.ThermalFit.toFixed(2)} · image cache ${s.CacheFit.toFixed(2)}`,
      isPick: s.NodeID === base.Selected,
      factors: [
        { label: 'CPU fit', value: s.CPUFit },
        { label: 'Memory fit', value: s.MemoryFit },
        { label: 'Thermal fit', value: s.ThermalFit },
        { label: 'Image cache', value: s.CacheFit },
      ],
    }))
  const rejected = (base.Rejections ?? []).map((r): Candidate => ({ node: r.NodeID, outcome: 'rejected', detail: r.Reason, isPick: false }))
  return [...eligible, ...rejected]
}

/**
 * Joins the unaided plan (the scheduler's own choices and the comparison of every
 * node) with the effective plan (where services will actually run once changes
 * are applied). They are the same plan when nothing was changed.
 */
export function derivePlan(base: PlanResponse, effective: PlanResponse | null, config: DeployConfig): PlanService[] {
  const workloads = base.plan.workloads ?? []
  const stages = stagesOf(workloads)
  const baseBy = new Map((base.decisions ?? []).map((d) => [d.WorkloadKey, d]))
  const effBy = new Map(((effective ?? base).decisions ?? []).map((d) => [d.WorkloadKey, d]))
  const builds = new Map((base.plan.builds ?? []).map((b) => [b.Service, b]))

  return workloads.map((w) => {
    const key = `${w.Project}/${w.Service}/${w.Revision}`
    const bd = baseBy.get(key)
    const ed = effBy.get(key)
    const node = ed?.Selected ?? ''
    const healthy = new Set(w.DependsOnHealthy ?? [])
    const build = builds.get(w.Service)
    return {
      name: w.Service,
      source: build ? `build ${build.Context}` : w.Image,
      cpuCores: w.Resources.CPUMillis / 1000,
      memGiB: w.Resources.MemoryBytes / GIB,
      stage: stages.get(w.Service) ?? 0,
      deps: (w.DependsOn ?? []).map((name) => ({ name, healthy: healthy.has(name) })),
      gpu: w.Constraints.GPU.Required,
      build: !!build,
      pinnedTo: w.Constraints.PinnedNodeID || w.Storage.PinnedNodeID || '',
      ports: (w.PortBindings ?? []).map((p) => p.HostPort),
      node,
      score: (ed?.Scores ?? []).find((s) => s.NodeID === node)?.Total ?? null,
      pickNode: bd?.Selected ?? node,
      changed: !!bd && !!ed && bd.Selected !== ed.Selected,
      kept: config.hostServices.includes(w.Service),
      warnings: [...(ed?.Warnings ?? [])],
      candidates: candidatesOf(bd, node),
    }
  })
}

/** "0.3 cores · 128 MiB": small requests read better in MiB than as 0.0 GiB. */
export function requestLabel(s: Pick<PlanService, 'cpuCores' | 'memGiB'>): string {
  const mem = s.memGiB === 0 ? 'no memory request' : s.memGiB >= 1 ? `${s.memGiB.toFixed(1)} GiB` : `${Math.round(s.memGiB * 1024)} MiB`
  return `${s.cpuCores.toFixed(1)} cores · ${mem}`
}

/**
 * The placements to send once the user has changed some. Re-planning with only
 * the changed services pinned would let the scheduler quietly move other
 * services too (every placement shifts the scores of the rest), so every other
 * service is pinned to the node the plan already showed for it. Services kept
 * on the host are pinned by pin_host, not here.
 */
export function pinsFor(base: PlanResponse, config: DeployConfig): Record<string, string> {
  const picks: Record<string, string> = {}
  for (const d of base.decisions ?? []) {
    const service = d.WorkloadKey.split('/')[1]
    if (d.Selected && service && !config.hostServices.includes(service)) picks[service] = d.Selected
  }
  return { ...picks, ...config.placements }
}

export function nodesUsed(services: PlanService[]): number {
  return new Set(services.map((s) => s.node).filter(Boolean)).size
}

export type LiveState = 'ready' | 'progress' | 'failed' | 'waiting' | 'stopped' | 'planned'

/** Collapses the agent-reported workload status into the few states the plan shows. */
export function liveState(status: string | undefined): LiveState {
  switch (status) {
    case undefined:
      return 'planned'
    case 'ready':
      return 'ready'
    case 'failed':
      return 'failed'
    case 'stopped':
      return 'stopped'
    case 'waiting_dependencies':
      return 'waiting'
    default:
      return 'progress' // pending, assigned, pulling, building, starting, running
  }
}
