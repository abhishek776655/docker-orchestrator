import type { ApiWorkload, PlanResponse } from './api'

export interface VolumeRisk {
  service: string
  from: string // node holding the data now, "" when unknown
  to: string // node the plan puts it on
  // node-down: the node holding its data is unavailable, so the scheduler
  // gave it a fresh volume. moved: the plan places it away from its data.
  reason: 'node-down' | 'moved'
}

const PERSISTENT = 'node_local_persistent'

/**
 * Services with a node-local volume that this plan would start on an empty
 * volume: either the scheduler said so (its data's node is down), or the
 * plan puts it on a different node than the one it runs on now.
 */
export function volumeRisks(plan: PlanResponse, existing: ApiWorkload[]): VolumeRisk[] {
  const workloads = plan.plan.workloads ?? []
  const byKey = new Map(workloads.map((w) => [`${w.Project}/${w.Service}/${w.Revision}`, w]))
  const now = new Map(existing.map((w) => [`${w.Project}/${w.Service}/${w.Revision}`, w]))
  const risks: VolumeRisk[] = []
  for (const d of plan.decisions ?? []) {
    const w = byKey.get(d.WorkloadKey)
    if (!w || !d.Selected) continue
    const current = now.get(d.WorkloadKey)
    const dataNode = current?.NodeID || w.Storage.PinnedNodeID
    if ((d.Warnings ?? []).some((m) => m.includes('fresh volume'))) {
      risks.push({ service: w.Service, from: dataNode, to: d.Selected, reason: 'node-down' })
      continue
    }
    const persistent = w.Storage.Class === PERSISTENT || current?.Storage?.Class === PERSISTENT
    if (persistent && dataNode && dataNode !== d.Selected) {
      risks.push({ service: w.Service, from: dataNode, to: d.Selected, reason: 'moved' })
    }
  }
  return risks.sort((a, b) => a.service.localeCompare(b.service))
}

/** Placements that put each moved service back on the node holding its data. */
export function keepVolumePlacements(risks: VolumeRisk[], placements: Record<string, string>): Record<string, string> {
  const next = { ...placements }
  for (const r of risks) if (r.reason === 'moved' && r.from) next[r.service] = r.from
  return next
}
