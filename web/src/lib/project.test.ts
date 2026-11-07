import { describe, expect, it } from 'vitest'
import type { ApiEvent, ApiWorkload } from './api'
import { blockedSummary, deriveProject, graphNodesOf, projectEvents } from './project'

function w(service: string, over: Partial<ApiWorkload> & { DependsOn?: string[] | null; DependsOnHealthy?: string[] | null } = {}): ApiWorkload {
  return {
    Project: 'shop', Service: service, Revision: 'latest', Image: 'x', Resources: { CPUMillis: 500, MemoryBytes: 1024 ** 3 },
    DesiredState: 'running', Status: 'ready', StatusReason: '', NodeID: 'mbp', ...over,
  } as ApiWorkload
}

const list = [
  w('db'),
  w('api', { DependsOn: ['db'], DependsOnHealthy: ['db'], Status: 'failed', StatusReason: 'exit 1', NodeID: 'nuc' }),
  w('web', { DependsOn: ['api'], Status: 'waiting_dependencies', NodeID: '' }),
  w('other', { Project: 'blog' }),
]

describe('deriveProject', () => {
  it('returns null for an unknown project', () => {
    expect(deriveProject('nope', list)).toBeNull()
  })

  it('summarises services, resources, nodes and stages', () => {
    const p = deriveProject('shop', list)!
    expect(p.services.map((s) => s.name)).toEqual(['api', 'db', 'web'])
    expect(p.cpuCores).toBe(1.5)
    expect(p.memGiB).toBe(3)
    expect(p.nodes).toEqual(['mbp', 'nuc'])
    expect(p.stages).toBe(3)
    expect(p.statusCounts).toEqual(expect.arrayContaining([{ status: 'ready', count: 1 }, { status: 'failed', count: 1 }]))
    expect(p.services.find((s) => s.name === 'api')!.waitsFor).toEqual([{ name: 'db', healthy: true }])
  })

  it('shows stopped services when none is running', () => {
    const p = deriveProject('shop', [w('db', { DesiredState: 'stopped', Status: 'stopped' })])!
    expect(p.allStopped).toBe(true)
    expect(p.services).toHaveLength(1)
  })

  it('picks a revision and defaults to the newest', () => {
    const two = [w('db', { Revision: 'r1' }), w('db', { Revision: 'r2' })]
    expect(deriveProject('shop', two)!.revision).toBe('r2')
    expect(deriveProject('shop', two, 'r1')!.revision).toBe('r1')
    expect(deriveProject('shop', two, 'zzz')!.revision).toBe('r2')
  })
})

describe('graphNodesOf', () => {
  it('numbers stages and uses the status as the line', () => {
    const nodes = graphNodesOf(deriveProject('shop', list)!)
    expect(nodes.map((n) => [n.name, n.stage, n.line, n.state])).toEqual([
      ['api', 1, 'failed', 'failed'],
      ['db', 0, 'ready', 'ready'],
      ['web', 2, 'waiting dependencies', 'waiting'],
    ])
  })
})

describe('blockedSummary', () => {
  it('names what a failure holds back', () => {
    expect(blockedSummary(deriveProject('shop', list)!)).toBe('api failed, so web cannot start.')
  })

  it('says nothing is blocked when all is well', () => {
    expect(blockedSummary(deriveProject('shop', [w('db')])!)).toBe('Nothing is blocked.')
  })
})

describe('projectEvents', () => {
  const ev = (ID: number, WorkloadKey: string): ApiEvent => ({ ID, WorkloadKey, NodeID: '', Type: 'status', Message: 'm', CreatedAt: '' })
  it('keeps only this project, newest first', () => {
    const out = projectEvents('shop', [ev(1, 'shop/db/latest'), ev(3, 'blog/x/latest'), ev(2, 'shop/api/latest'), ev(4, 'shopping/a/b')])
    expect(out.map((g) => g.event.ID)).toEqual([2, 1])
  })

  it('folds consecutive repeats into one with a count', () => {
    const out = projectEvents('shop', [ev(1, 'shop/db/latest'), ev(2, 'shop/db/latest'), ev(3, 'shop/db/latest'), ev(4, 'shop/api/latest')])
    expect(out.map((g) => [g.event.ID, g.count])).toEqual([[4, 1], [3, 3]])
  })
})
