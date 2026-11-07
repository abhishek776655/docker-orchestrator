import { describe, expect, it } from 'vitest'
import type { ApiEvent, ApiWorkload } from './api'
import { deriveAttention, newestId, tileCounts, unseenCount } from './attention'
import type { NodeView } from './cluster'

function node(id: string, state: NodeView['state'], temp = 50): NodeView {
  return {
    id, name: id, address: '10.0.0.1', arch: 'arm64', os: 'darwin', isHost: false, state, labels: {},
    limits: { cpu: 0, mem: 0 }, detected: { cpu: 8, mem: 16 }, cpuUsed: 0, cpuCap: 8, memUsed: 0, memCap: 16, memLive: null, gpus: [],
    thermal: { Known: true, TemperatureC: temp, Throttling: state === 'throttling' }, heartbeatAgeSec: 120, workloads: [], cachedImages: [],
  }
}

function wl(service: string, Status: string, DesiredState: 'running' | 'stopped' = 'running', StatusReason = ''): ApiWorkload {
  return { Project: 'shop', Service: service, Revision: 'latest', Image: 'x', Resources: { CPUMillis: 0, MemoryBytes: 0 }, DesiredState, Status, StatusReason, NodeID: 'a' }
}

const ev = (ID: number, Message: string): ApiEvent => ({ ID, WorkloadKey: 'shop/api/latest', NodeID: '', Type: 'status', Message, CreatedAt: '' })

describe('tileCounts', () => {
  it('counts schedulable nodes and workload states', () => {
    const t = tileCounts([node('a', 'available'), node('b', 'throttling'), node('c', 'offline')], [wl('a', 'ready'), wl('b', 'failed'), wl('c', 'building'), wl('d', 'stopped', 'stopped')])
    expect(t).toEqual({ schedulable: 2, nodes: 3, ready: 1, running: 3, failed: 1, inProgress: 1 })
  })
})

describe('deriveAttention', () => {
  it('orders failures, then offline, throttling and cordoned nodes', () => {
    const items = deriveAttention(
      [node('cord', 'cordoned'), node('hot', 'throttling', 81), node('gone', 'offline'), node('ok', 'available')],
      [wl('api', 'failed', 'running', 'exit 1'), wl('old', 'failed', 'stopped')],
    )
    expect(items.map((i) => i.title)).toEqual(['shop/api', 'gone', 'hot', 'cord'])
    expect(items[0]).toMatchObject({ tone: 'bad', detail: 'exit 1', to: '/projects/shop/services/api' })
    expect(items[2].detail).toContain('81 °C')
  })
  it('is empty when all is well', () => {
    expect(deriveAttention([node('ok', 'available')], [wl('api', 'ready')])).toEqual([])
  })
})

describe('unseenCount', () => {
  const events = [ev(1, 'failed: x'), ev(2, 'ready: '), ev(3, 'waiting_dependencies: db'), ev(4, 'failed: y')]
  it('counts only problems newer than the last seen, not routine waiting', () => {
    expect(unseenCount(events, 0)).toBe(2)
    expect(unseenCount(events, 2)).toBe(1)
    expect(unseenCount(events, newestId(events))).toBe(0)
  })
})
