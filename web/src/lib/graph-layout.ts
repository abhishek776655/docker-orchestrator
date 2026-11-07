export interface GraphItem {
  name: string
  stage: number // 0-based start stage
  deps: { name: string; healthy: boolean }[]
}

export const NODE_W = 168
export const NODE_H = 60
const GAP_X = 96
const PITCH = 88 // node height plus a 28px corridor between rows
const PAD_X = 40
const PAD_Y = 52

export type Point = [number, number]

export interface GraphEdge {
  from: string // the service that is waited for
  to: string // the service that waits
  healthy: boolean
  points: Point[]
  head: Point[]
}

export interface GraphLayout {
  width: number
  height: number
  nodes: Record<string, { left: number; top: number }>
  labels: { left: number; text: string }[]
  edges: GraphEdge[]
}

/**
 * Lays services out left to right by start stage, one row per service within a
 * stage. Every node sits on a shared row grid, so a long edge between
 * non-adjacent stages can run along the corridor above its target's row without
 * crossing any card.
 */
export function layoutGraph(items: GraphItem[]): GraphLayout {
  const stageCount = items.reduce((m, i) => Math.max(m, i.stage + 1), 0)
  const slot: Record<string, number> = {}
  const perStage: number[] = Array(stageCount).fill(0)
  for (const it of items) slot[it.name] = perStage[it.stage]++

  const left = (stage: number) => PAD_X + stage * (NODE_W + GAP_X)
  const top = (name: string) => PAD_Y + slot[name] * PITCH
  const byName = new Map(items.map((i) => [i.name, i]))

  const nodes: GraphLayout['nodes'] = {}
  for (const it of items) nodes[it.name] = { left: left(it.stage), top: top(it.name) }

  const edges: GraphEdge[] = []
  let e = 0
  for (const to of items) {
    for (const dep of to.deps) {
      const from = byName.get(dep.name)
      if (!from) continue
      const x1 = nodes[from.name].left + NODE_W
      const y1 = nodes[from.name].top + NODE_H / 2
      const x2 = nodes[to.name].left
      const y2 = nodes[to.name].top + NODE_H / 2
      const xa = x1 + 16 + (e % 7) * 10
      let points: Point[]
      if (to.stage - from.stage === 1) {
        points = [[x1, y1], [xa, y1], [xa, y2], [x2 - 8, y2]]
      } else {
        const xb = x2 - 16 - ((e * 3) % 7) * 10
        const yc = top(to.name) - 14 + ((e % 3) - 1) * 4
        points = [[x1, y1], [xa, y1], [xa, yc], [xb, yc], [xb, y2], [x2 - 8, y2]]
      }
      edges.push({ from: from.name, to: to.name, healthy: dep.healthy, points, head: [[x2 - 8, y2 - 4], [x2 - 8, y2 + 4], [x2 - 1, y2]] })
      e++
    }
  }

  const maxSlots = perStage.reduce((m, n) => Math.max(m, n), 0)
  return {
    width: PAD_X * 2 + stageCount * NODE_W + Math.max(0, stageCount - 1) * GAP_X,
    height: PAD_Y + Math.max(0, maxSlots - 1) * PITCH + NODE_H + PAD_X,
    nodes,
    labels: Array.from({ length: stageCount }, (_, s) => ({ left: left(s), text: `Stage ${s + 1}` })),
    edges,
  }
}

export interface Trace {
  up: Set<string> // everything the target waits for, through any number of stages
  down: Set<string> // everything that waits for the target
}

export function traceFrom(items: GraphItem[], target: string): Trace {
  const up = new Set<string>()
  const down = new Set<string>()
  const byName = new Map(items.map((i) => [i.name, i]))
  const walkUp = (n: string) => {
    for (const d of byName.get(n)?.deps ?? []) {
      if (!up.has(d.name)) {
        up.add(d.name)
        walkUp(d.name)
      }
    }
  }
  const walkDown = (n: string) => {
    for (const it of items) {
      if (it.deps.some((d) => d.name === n) && !down.has(it.name)) {
        down.add(it.name)
        walkDown(it.name)
      }
    }
  }
  walkUp(target)
  walkDown(target)
  return { up, down }
}

/** An edge is on the target's path when it feeds into the target or any of its ancestors, or leaves the target or any of its descendants. */
export function edgeOnPath(edge: GraphEdge, target: string, t: Trace): boolean {
  const upSet = t.up.has(edge.to) || edge.to === target
  const downSet = t.down.has(edge.from) || edge.from === target
  return upSet || downSet
}
