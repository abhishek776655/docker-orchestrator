import { describe, expect, it } from 'vitest'
import type { ApiEvent, ApiInstance, ApiWorkload } from './api'
import { deriveService, durationLabel, healthView, lifecycle } from './service'

function w(service: string, over: Partial<ApiWorkload> = {}): ApiWorkload {
  return {
    Project: 'shop', Service: service, Revision: 'r1', Image: `${service}:1`, Resources: { CPUMillis: 1500, MemoryBytes: 2 * 1024 ** 3 },
    DesiredState: 'running', Status: 'ready', StatusReason: '', NodeID: 'mbp', ...over,
  }
}

const list: ApiWorkload[] = [
  w('db', { PortBindings: [{ ContainerPort: 5432, HostPort: 5432 }], Storage: { Class: 'node_local_persistent', PinnedNodeID: 'nuc' } }),
  w('api', { DependsOn: ['db'], DependsOnHealthy: ['db'], Status: 'waiting_dependencies' }),
  w('web', { DependsOn: ['db', 'api'] }),
  w('db', { Revision: 'r0', DesiredState: 'stopped', Status: 'stopped' }),
]

const inst: ApiInstance = { Project: 'shop', Service: 'db', NodeID: 'mbp', Address: '10.0.0.2', ContainerPort: 5432, Status: 'ready' }

describe('deriveService', () => {
  it('returns null for an unknown service', () => {
    expect(deriveService('shop', 'nope', list, [])).toBeNull()
  })

  it('defaults to the running revision and lists the others', () => {
    const s = deriveService('shop', 'db', list, [inst])!
    expect(s.revision).toBe('r1')
    expect(s.revisions).toEqual([{ revision: 'r0', stopped: true }, { revision: 'r1', stopped: false }])
    expect(deriveService('shop', 'db', list, [], 'r0')!.status).toBe('stopped')
  })

  it('reads ports, instances, dependants and the reason it is pinned', () => {
    const s = deriveService('shop', 'db', list, [inst])!
    expect(s.internalName).toBe('db:5432')
    expect(s.instances).toHaveLength(1)
    expect(s.neededBy.map((n) => [n.name, n.healthy])).toEqual([['api', true], ['web', false]])
    expect(s.placement).toEqual([{ label: 'Volume data lives on', value: 'nuc', tone: 'hard' }])
    expect(s.cpuCores).toBe(1.5)
  })

  it('says which dependencies must be healthy', () => {
    expect(deriveService('shop', 'api', list, [])!.dependsOn).toEqual([{ name: 'db', healthy: true }])
  })
})

describe('healthView', () => {
  it('reads the command and timing', () => {
    expect(healthView({ Test: ['CMD-SHELL', 'curl -f http://x'], Interval: 10e9, Timeout: 3e9, Retries: 5, StartPeriod: 30e9 })).toEqual({
      command: 'curl -f http://x',
      timing: 'every 10s · timeout 3s · 5 retries · 30s start period',
    })
  })
  it('returns null when there is none', () => {
    expect(healthView(null)).toBeNull()
    expect(healthView({ Test: ['NONE'], Interval: 0, Timeout: 0, Retries: 0, StartPeriod: 0 })).toBeNull()
  })
  it('formats durations', () => {
    expect([durationLabel(5e9), durationLabel(120e9), durationLabel(1.5e9)]).toEqual(['5s', '2m', '1.5s'])
  })
})

describe('lifecycle', () => {
  const ev = (ID: number, state: string, CreatedAt = `t${ID}`, key = 'shop/api/r1'): ApiEvent => ({ ID, WorkloadKey: key, NodeID: '', Type: 'status', Message: `${state}: `, CreatedAt })

  it('marks what happened, where it is now and what is ahead', () => {
    const steps = lifecycle('shop/api/r1', 'starting', '', [ev(1, 'pending'), ev(2, 'assigned'), ev(3, 'pulling'), ev(4, 'starting'), ev(5, 'ready', 'x', 'shop/db/r1')])
    expect(steps.map((s) => [s.label, s.state])).toEqual([
      ['pending', 'done'], ['assigned', 'done'], ['pulling', 'done'], ['starting', 'now'], ['running', 'todo'], ['ready', 'todo'],
    ])
    expect(steps[0].time).toBe('t1')
  })

  it('shows a ready service as finished, not in progress', () => {
    const steps = lifecycle('shop/api/r1', 'ready', '', [ev(1, 'pending'), ev(2, 'ready')])
    expect(steps.every((x) => x.state === 'done' || x.state === 'todo')).toBe(true)
    expect(steps[steps.length - 1]).toMatchObject({ label: 'ready', state: 'done' })
  })

  it('ends on the failure with its reason', () => {
    const steps = lifecycle('shop/api/r1', 'failed', 'exit 1', [ev(1, 'pending'), ev(2, 'assigned'), ev(3, 'failed')])
    expect(steps[steps.length - 1]).toMatchObject({ label: 'failed', state: 'bad', note: 'exit 1' })
    expect(steps.find((s) => s.label === 'assigned')!.state).toBe('done')
  })

  it('shows waiting on dependencies as the current step', () => {
    const steps = lifecycle('shop/api/r1', 'waiting_dependencies', '', [ev(1, 'pending'), ev(2, 'waiting_dependencies')])
    expect(steps.find((s) => s.state === 'now')!.label).toBe('waiting_dependencies')
  })
})
