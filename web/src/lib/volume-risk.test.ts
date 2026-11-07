import { describe, expect, it } from 'vitest'
import type { ApiWorkload, Decision, PlanResponse, PlanWorkload } from './api'
import { keepVolumePlacements, volumeRisks } from './volume-risk'

function pw(service: string, persistent: boolean, pinned = ''): PlanWorkload {
  return {
    Project: 'shop', Service: service, Revision: 'latest', Image: 'x', Resources: { CPUMillis: 0, MemoryBytes: 0 },
    Constraints: { HostPorts: null, GPU: { Required: false, Vendor: '', VRAMBytes: 0 }, PinnedNodeID: '' }, PortBindings: null,
    Storage: { Class: persistent ? 'node_local_persistent' : 'ephemeral', PinnedNodeID: pinned }, DependsOn: null, DependsOnHealthy: null,
  }
}
const dec = (service: string, Selected: string, Warnings: string[] | null = null): Decision => ({
  WorkloadKey: `shop/${service}/latest`, Candidates: null, Rejections: null, Scores: null, Selected, Warnings,
})
const running = (service: string, NodeID: string): ApiWorkload => ({
  Project: 'shop', Service: service, Revision: 'latest', Image: 'x', Resources: { CPUMillis: 0, MemoryBytes: 0 }, DesiredState: 'running', Status: 'ready', StatusReason: '', NodeID,
})

const plan = (workloads: PlanWorkload[], decisions: Decision[]): PlanResponse => ({
  plan: { project: 'shop', workloads, builds: null, networking: { mode: 'lan', supported: true }, warnings: null, rejections: null },
  decisions,
})

describe('volumeRisks', () => {
  it('flags a stateful service the plan moves away from its data', () => {
    const r = volumeRisks(plan([pw('db', true), pw('api', false)], [dec('db', 'nuc'), dec('api', 'nuc')]), [running('db', 'mbp'), running('api', 'mbp')])
    expect(r).toEqual([{ service: 'db', from: 'mbp', to: 'nuc', reason: 'moved' }])
  })

  it('flags the scheduler fresh-volume warning as node down', () => {
    const r = volumeRisks(plan([pw('db', true, 'gone')], [dec('db', 'nuc', ['pinned node unavailable; scheduling with a fresh volume (previous data on the pinned node was not migrated)'])]), [])
    expect(r).toEqual([{ service: 'db', from: 'gone', to: 'nuc', reason: 'node-down' }])
  })

  it('ignores a first deploy and a service that stays put', () => {
    expect(volumeRisks(plan([pw('db', true)], [dec('db', 'nuc')]), [])).toEqual([])
    expect(volumeRisks(plan([pw('db', true)], [dec('db', 'mbp')]), [running('db', 'mbp')])).toEqual([])
  })
})

describe('keepVolumePlacements', () => {
  it('pins moved services back to their data and leaves node-down ones', () => {
    expect(keepVolumePlacements([{ service: 'db', from: 'mbp', to: 'nuc', reason: 'moved' }, { service: 'q', from: 'gone', to: 'nuc', reason: 'node-down' }], { api: 'nuc' })).toEqual({ api: 'nuc', db: 'mbp' })
  })
})
