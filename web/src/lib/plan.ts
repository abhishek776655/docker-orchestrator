import { parseDocument } from 'yaml'
import type { ApiWorkload, ComposeRequest, Decision, PlanResponse } from './api'
import { summarizeCompose } from './compose-summary'
import { dependencyClosure, pickedServices, scopeRoots, type DeployConfig } from './deploy-config'

/**
 * The file narrowed to the chosen service and what it depends on. The control
 * plane reads every service (env files, secrets, health checks) before it
 * narrows the deploy, so one you did not pick could still fail the plan.
 *
 * The others are not deleted outright: they stay as stubs with only their
 * image, build and volumes. The control plane decides what a bind mount is
 * (source code to ship, or a data folder) partly by whether other services
 * mount or build from the same folder, so without the stubs a folder shared
 * with other services looks private and ends up as an empty volume.
 * Returns the text unchanged when everything is in scope or it cannot be read.
 */
export function pruneToScope(yaml: string, config: DeployConfig): string {
  if (config.scope !== 'one') return yaml
  const parsed = summarizeCompose(yaml)
  if (!parsed.ok) return yaml
  const roots = scopeRoots(parsed.summary, config)
  if (roots.length === 0) return yaml
  const keep = new Set(dependencyClosure(parsed.summary.details, roots))
  if (keep.size === parsed.summary.services.length) return yaml
  const doc = parseDocument(yaml)
  const services = doc.get('services', true)
  if (!services || typeof (services as { items?: unknown }).items === 'undefined') return yaml
  for (const name of parsed.summary.services) {
    if (keep.has(name)) continue
    const original = doc.getIn(['services', name]) as { toJSON?: () => Record<string, unknown> } | undefined
    const full = original?.toJSON?.() ?? {}
    const stub: Record<string, unknown> = {}
    for (const field of ['image', 'build', 'volumes']) if (full[field] !== undefined) stub[field] = full[field]
    if (Object.keys(stub).length === 0) doc.deleteIn(['services', name])
    else doc.setIn(['services', name], doc.createNode(stub))
  }
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
    only_service: config.scope === 'one' ? pickedServices(config).join(',') : '',
    pin_host: config.hostServices.length > 0 ? config.hostServices : null,
    pin_nodes: pinNodes,
    fresh: config.fresh,
    force: withPlacements ? (config.forced ?? []).filter((s) => s in config.placements) : [],
  }
}

export interface Candidate {
  node: string
  // Two outcomes only. selected: the node this service will run on, with the
  // reason it was picked. rejected: every other node, with the reason it was not.
  outcome: 'selected' | 'rejected'
  // Why a node was rejected: the scheduler refused it, its volume data is on
  // another node (it would start empty), or another node scored higher.
  rejectedBy?: 'scheduler' | 'volume' | 'score'
  inUse: boolean // the node this service will run on
  score?: number
  detail: string // the reason for the outcome
  warning?: string // on the selected node: something to know, such as an empty volume
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
  forced: boolean // placed on a node the scheduler rejected, by choice
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

function candidatesOf(base: Decision | undefined, selected: string, dataNode: string): Candidate[] {
  if (!base) return []
  const volumeElsewhere = (node: string) => (dataNode && node !== dataNode ? `volume data is on ${dataNode}; here it starts with an empty volume` : '')
  const scored = [...(base.Scores ?? [])].sort((a, b) => b.Total - a.Total)
  const selectedScore = scored.find((s) => s.NodeID === selected)?.Total
  const eligible = scored.map((s): Candidate => {
    const volume = volumeElsewhere(s.NodeID)
    const inUse = s.NodeID === selected
    const isPick = s.NodeID === base.Selected
    const fit = `CPU ${s.CPUFit.toFixed(2)} · memory ${s.MemoryFit.toFixed(2)} · thermal ${s.ThermalFit.toFixed(2)} · image cache ${s.CacheFit.toFixed(2)}${s.SpreadFit === undefined ? '' : ` · spread ${s.SpreadFit.toFixed(2)}`}`
    let detail: string
    let rejectedBy: Candidate['rejectedBy']
    if (inUse) {
      detail = isPick ? `Highest score. ${fit}` : `Chosen by you. ${fit}`
    } else if (volume) {
      rejectedBy = 'volume'
      detail = volume
    } else {
      rejectedBy = 'score'
      detail =
        selectedScore === undefined
          ? fit
          : s.Total.toFixed(2) === selectedScore.toFixed(2)
            ? `Same score as ${selected}; the scheduler breaks ties by node name. ${fit}`
            : s.Total > selectedScore
              ? `Scored higher (${s.Total.toFixed(2)} vs ${selectedScore.toFixed(2)}), but ${selected} was chosen instead. ${fit}`
              : `Lower score than ${selected} (${s.Total.toFixed(2)} vs ${selectedScore.toFixed(2)}). ${fit}`
    }
    return {
      node: s.NodeID,
      outcome: inUse ? 'selected' : 'rejected',
      rejectedBy,
      inUse,
      score: s.Total,
      detail,
      warning: inUse && volume ? volume : undefined,
      isPick,
      factors: [
        { label: 'CPU fit', value: s.CPUFit },
        { label: 'Memory fit', value: s.MemoryFit },
        { label: 'Thermal fit', value: s.ThermalFit },
        { label: 'Image cache', value: s.CacheFit },
        ...(s.SpreadFit === undefined ? [] : [{ label: 'Spread', value: s.SpreadFit }]),
      ],
    }
  })
  const refused = (base.Rejections ?? []).map((r): Candidate => {
    const inUse = r.NodeID === selected
    return {
      node: r.NodeID,
      outcome: inUse ? 'selected' : 'rejected',
      rejectedBy: inUse ? undefined : 'scheduler',
      inUse,
      detail: inUse ? `Chosen by you although the scheduler rejected it: ${r.Reason}` : r.Reason,
      warning: inUse ? r.Reason : undefined,
      isPick: false,
    }
  })
  return [...eligible, ...refused]
}

export type CandidateTag = { label: string; tone: 'ok' | 'info' | 'bad' | 'warn' }

/**
 * The labels a node gets when comparing placements: selected or rejected, the
 * scheduler's own pick when the user moved the service elsewhere, and where
 * the service runs now.
 */
export function candidateTags(c: Candidate, runsOn: string): CandidateTag[] {
  const tags: CandidateTag[] = [c.outcome === 'selected' ? { label: 'selected', tone: 'ok' } : { label: 'rejected', tone: 'bad' }]
  if (c.isPick && c.outcome !== 'selected') tags.push({ label: "scheduler's pick", tone: 'info' })
  if (runsOn && c.node === runsOn) tags.push({ label: 'runs here now', tone: 'info' })
  return tags
}

/**
 * The nodes that scored exactly as well as the scheduler's pick. The
 * scheduler breaks such ties by node name, so the pick among them is not a
 * real preference.
 */
export function tiedWithPick(candidates: Candidate[]): string[] {
  const pick = candidates.find((c) => c.isPick)
  if (!pick || pick.score === undefined) return []
  return candidates.filter((c) => !c.isPick && c.rejectedBy !== 'scheduler' && c.score !== undefined && c.score.toFixed(2) === pick.score!.toFixed(2)).map((c) => c.node)
}

/**
 * Joins the unaided plan (the scheduler's own choices and the comparison of every
 * node) with the effective plan (where services will actually run once changes
 * are applied). They are the same plan when nothing was changed.
 */
export function derivePlan(base: PlanResponse, effective: PlanResponse | null, config: DeployConfig, current: ApiWorkload[] = []): PlanService[] {
  const workloads = base.plan.workloads ?? []
  const now = new Map(current.map((w) => [`${w.Project}/${w.Service}/${w.Revision}`, w]))
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
    // Where its volume data lives, for a service with a node-local volume:
    // the node it runs on now, else the node its data is pinned to.
    const existing = now.get(key)
    const persistent = w.Storage.Class === 'node_local_persistent' || existing?.Storage?.Class === 'node_local_persistent'
    const dataNode = persistent ? existing?.NodeID || w.Storage.PinnedNodeID || '' : ''
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
      forced: (config.forced ?? []).includes(w.Service) && config.placements[w.Service] === node,
      kept: config.hostServices.includes(w.Service),
      warnings: [...(ed?.Warnings ?? [])],
      candidates: candidatesOf(bd, node, dataNode),
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
