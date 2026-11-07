import { describe, expect, it } from 'vitest'
import { edgeOnPath, layoutGraph, traceFrom, NODE_H, NODE_W, type GraphItem } from './graph-layout'

const dep = (name: string, healthy = false) => ({ name, healthy })
const ITEMS: GraphItem[] = [
  { name: 'db', stage: 0, deps: [] },
  { name: 'cache', stage: 0, deps: [] },
  { name: 'api', stage: 1, deps: [dep('db', true), dep('cache')] },
  { name: 'web', stage: 2, deps: [dep('api', true)] },
  { name: 'admin', stage: 2, deps: [dep('api'), dep('db')] }, // db is two stages back
]

describe('layoutGraph', () => {
  it('puts each stage in its own column and each service in its own row', () => {
    const l = layoutGraph(ITEMS)
    expect(l.nodes.db.left).toBeLessThan(l.nodes.api.left)
    expect(l.nodes.api.left).toBeLessThan(l.nodes.web.left)
    expect(l.nodes.db.top).not.toBe(l.nodes.cache.top)
    expect(l.nodes.web.left).toBe(l.nodes.admin.left)
    expect(l.labels.map((x) => x.text)).toEqual(['Stage 1', 'Stage 2', 'Stage 3'])
  })

  it('makes one edge per dependency and routes a long one through the corridor above its target', () => {
    const l = layoutGraph(ITEMS)
    expect(l.edges).toHaveLength(5)
    const adjacent = l.edges.find((x) => x.from === 'db' && x.to === 'api')!
    const long = l.edges.find((x) => x.from === 'db' && x.to === 'admin')!
    expect(adjacent.points).toHaveLength(4)
    expect(long.points).toHaveLength(6)
    const corridorY = long.points[2][1]
    expect(corridorY).toBeLessThan(l.nodes.admin.top) // above the target's row
    expect(corridorY).toBeGreaterThan(l.nodes.admin.top - 28) // inside the corridor, not in the row above
  })

  it('ends every edge at its target card and keeps the healthy flag', () => {
    const l = layoutGraph(ITEMS)
    for (const edge of l.edges) {
      const to = l.nodes[edge.to]
      const last = edge.points[edge.points.length - 1]
      expect(last[0]).toBe(to.left - 8)
      expect(last[1]).toBe(to.top + NODE_H / 2)
    }
    expect(l.edges.find((x) => x.from === 'db' && x.to === 'api')!.healthy).toBe(true)
    expect(l.width).toBeGreaterThan(3 * NODE_W)
  })

  it('handles an empty plan', () => {
    const l = layoutGraph([])
    expect(l.edges).toEqual([])
    expect(l.labels).toEqual([])
  })
})

describe('traceFrom and edgeOnPath', () => {
  it('finds what a service waits for and what waits for it', () => {
    const t = traceFrom(ITEMS, 'api')
    expect([...t.up].sort()).toEqual(['cache', 'db'])
    expect([...t.down].sort()).toEqual(['admin', 'web'])
  })

  it('marks only the edges on the target path', () => {
    const l = layoutGraph(ITEMS)
    const t = traceFrom(ITEMS, 'web')
    const onPath = l.edges.filter((e) => edgeOnPath(e, 'web', t)).map((e) => `${e.from}>${e.to}`).sort()
    expect(onPath).toEqual(['api>web', 'cache>api', 'db>api'])
  })
})
