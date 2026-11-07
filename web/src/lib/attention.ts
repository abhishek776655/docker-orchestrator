import type { ApiEvent, ApiWorkload } from './api'
import { eventTone, offlineHint, type NodeView } from './cluster'

export interface Tiles {
  schedulable: number
  nodes: number
  ready: number
  running: number // desired running
  failed: number
  inProgress: number
}

/** The dashboard's four counts. */
export function tileCounts(nodes: NodeView[], workloads: ApiWorkload[]): Tiles {
  const desired = workloads.filter((w) => w.DesiredState === 'running')
  return {
    schedulable: nodes.filter((n) => n.state === 'available' || n.state === 'throttling').length,
    nodes: nodes.length,
    ready: desired.filter((w) => w.Status === 'ready').length,
    running: desired.length,
    failed: desired.filter((w) => w.Status === 'failed').length,
    inProgress: desired.filter((w) => !['ready', 'failed', 'stopped'].includes(w.Status)).length,
  }
}

export interface AttentionItem {
  key: string
  tone: 'bad' | 'warn' | 'off'
  title: string
  detail: string
  to: string
}

/**
 * What an operator should look at, worst first: failed workloads, nodes
 * without a heartbeat, nodes running hot, then cordoned nodes.
 */
export function deriveAttention(nodes: NodeView[], workloads: ApiWorkload[]): AttentionItem[] {
  const failed = workloads
    .filter((w) => w.DesiredState === 'running' && w.Status === 'failed')
    .map((w): AttentionItem => ({
      key: `w:${w.Project}/${w.Service}/${w.Revision}`,
      tone: 'bad',
      title: `${w.Project}/${w.Service}`,
      detail: w.StatusReason || 'Failed with no reason given.',
      to: `/projects/${encodeURIComponent(w.Project)}/services/${encodeURIComponent(w.Service)}`,
    }))
  const node = (state: NodeView['state'], tone: AttentionItem['tone'], detail: (n: NodeView) => string) =>
    nodes
      .filter((n) => n.state === state)
      .map((n): AttentionItem => ({ key: `n:${state}:${n.id}`, tone, title: n.name, detail: detail(n), to: `/nodes/${encodeURIComponent(n.id)}` }))
  return [
    ...failed,
    ...node('offline', 'bad', offlineHint),
    ...node('throttling', 'warn', (n) => `Running hot at ${Math.round(n.thermal.TemperatureC)} °C. New workloads prefer cooler nodes.`),
    ...node('cordoned', 'off', () => 'Cordoned: no new workloads are placed here.'),
  ]
}

/**
 * Problems newer than the last one the operator saw: failures and warnings.
 * "waiting for dependencies" is left out: a waiting service repeats it every
 * few seconds, and waiting is normal during a deploy.
 */
export function isAlert(e: ApiEvent): boolean {
  const tone = eventTone(e)
  return tone === 'bad' || (tone === 'warn' && !e.Message.startsWith('waiting_dependencies'))
}

export function unseenCount(events: ApiEvent[], seenId: number): number {
  return events.filter((e) => e.ID > seenId && isAlert(e)).length
}

/** The newest event ID, the baseline that marks everything as seen. */
export function newestId(events: ApiEvent[]): number {
  return events.reduce((m, e) => Math.max(m, e.ID), 0)
}
