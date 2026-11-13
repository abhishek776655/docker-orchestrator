import { describe, expect, it } from 'vitest'
import type { Decision, PlanResponse, PlanWorkload } from './api'
import { defaultConfig, type DeployConfig } from './deploy-config'
import { parse } from 'yaml'
import { buildRequest, candidateTags, derivePlan, liveState, nodesUsed, pinsFor, pruneToScope, requestLabel, stagesOf, tiedWithPick, type Candidate } from './plan'

describe('candidate tags when comparing nodes', () => {
  // The case from the service page: the scheduler picks the EliteBook on a
  // tie, but mysql runs on the Mac where its data is.
  const elite: Candidate = { node: 'elitebook', outcome: 'rejected', rejectedBy: 'volume', inUse: false, score: 0.93, detail: 'volume data is on mac', isPick: true }
  const mac: Candidate = { node: 'mac', outcome: 'selected', inUse: true, score: 0.93, detail: 'Highest score', isPick: false }
  const refused: Candidate = { node: 'm1', outcome: 'rejected', rejectedBy: 'scheduler', inUse: false, detail: 'host port 3306 already reserved', isPick: false }
  const labels = (c: Candidate) => candidateTags(c, 'mac').map((t) => t.label)

  it('says only selected or rejected, with the scheduler pick noted when it was overridden', () => {
    expect(labels(elite)).toEqual(['rejected', "scheduler's pick"])
    expect(labels(refused)).toEqual(['rejected'])
  })
  it('marks where the service runs now', () => {
    expect(labels(mac)).toEqual(['selected', 'runs here now'])
  })
  it('never labels a node eligible', () => {
    expect(candidateTags({ ...mac, node: 'other', outcome: 'rejected', inUse: false }, 'mac').map((t) => t.label)).toEqual(['rejected'])
  })
  it('reports nodes tied with the pick, ignoring refused ones', () => {
    expect(tiedWithPick([elite, mac, { ...refused, score: 0.93 }])).toEqual(['mac'])
    expect(tiedWithPick([elite, { ...mac, score: 0.7 }])).toEqual([])
  })
})

function wl(service: string, over: Partial<PlanWorkload> = {}): PlanWorkload {
  return {
    Project: 'shop', Service: service, Revision: 'latest', Image: `${service}:1`,
    Resources: { CPUMillis: 500, MemoryBytes: 2 * 1024 ** 3 },
    Constraints: { HostPorts: null, GPU: { Required: false, Vendor: '', VRAMBytes: 0 }, PinnedNodeID: '' },
    PortBindings: null, Storage: { Class: 'ephemeral', PinnedNodeID: '' },
    DependsOn: null, DependsOnHealthy: null,
    ...over,
  }
}

function dec(service: string, selected: string, others: string[] = []): Decision {
  const score = (id: string, total: number) => ({ NodeID: id, CPUFit: total, MemoryFit: total, ThermalFit: 1, CacheFit: 0, Total: total })
  return {
    WorkloadKey: `shop/${service}/latest`, Candidates: [selected, ...others], Selected: selected, Warnings: null,
    Scores: [score(selected, 0.8), ...others.map((o, i) => score(o, 0.6 - i * 0.1))],
    Rejections: [{ NodeID: 'nuc-old', Reason: 'node nuc-old is disabled' }],
  }
}

const base: PlanResponse = {
  plan: {
    project: 'shop',
    workloads: [wl('db'), wl('api', { DependsOn: ['db'], DependsOnHealthy: ['db'] }), wl('web', { DependsOn: ['api'] })],
    builds: [{ Service: 'api', Context: './api' }],
    networking: { mode: 'lan', supported: true },
    warnings: null, rejections: null,
  },
  decisions: [dec('db', 'mbp-m3', ['gpu-box']), dec('api', 'mbp-m3', ['gpu-box']), dec('web', 'mbp-m3')],
}

describe('buildRequest', () => {
  const config: DeployConfig = { ...defaultConfig({ services: [], details: [], builds: 0, gpu: 0, volumes: 0, ports: 0, dependencies: 0 }), project: 'shop', workingDir: ' /srv/shop ' }

  it('maps the configuration onto the compose request', () => {
    const r = buildRequest('services: {}', { ...config, hostServices: ['db'], scope: 'one', onlyServices: ['web'], noCache: true, fresh: true }, false)
    expect(r).toMatchObject({ project: 'shop', working_dir: '/srv/shop', no_cache: true, only_service: 'web', pin_host: ['db'], pin_nodes: {}, fresh: true })
  })

  it('sends no host pin when nothing is kept on the host', () => {
    expect(buildRequest('y', config, false).pin_host).toBeNull()
    expect(buildRequest('y', config, false).only_service).toBe('')
  })

  it('sends forced services only for placements that exist', () => {
    const c = { ...config, placements: { api: 'gpu-box' }, forced: ['api', 'web'] }
    expect(buildRequest('y', c, true).force).toEqual(['api'])
    expect(buildRequest('y', c, false).force).toEqual([])
  })

  it('groups placement changes by node, only when asked', () => {
    const c = { ...config, placements: { api: 'gpu-box', web: 'gpu-box', db: 'jetson-orin' } }
    expect(buildRequest('y', c, true).pin_nodes).toEqual({ 'gpu-box': ['api', 'web'], 'jetson-orin': ['db'] })
    expect(buildRequest('y', c, false).pin_nodes).toEqual({})
  })
})

describe('pruneToScope', () => {
  const file = `services:
  db: { image: pg }
  api: { image: api, depends_on: [db] }
  dbt:
    build: ./dbt
    env_file: ./dbt/.env
`
  const config = { ...defaultConfig({ services: [], details: [], builds: 0, gpu: 0, volumes: 0, ports: 0, dependencies: 0 }), project: 'shop' }

  it('keeps the whole file when everything is in scope', () => {
    expect(pruneToScope(file, config)).toBe(file)
  })

  it('keeps every picked service and their dependencies, and names them all in the request', () => {
    const picked = { ...config, scope: 'one' as const, onlyServices: ['api', 'dbt'] }
    expect(Object.keys(parse(pruneToScope(file, picked)).services)).toEqual(['db', 'api', 'dbt'])
    expect(buildRequest(file, picked, false).only_service).toBe('api,dbt')
  })

  it('reduces services outside the chosen one to stubs, so a failing env file or health check cannot break the plan', () => {
    const out = parse(pruneToScope(file, { ...config, scope: 'one', onlyServices: ['api'] }))
    expect(Object.keys(out.services)).toEqual(['db', 'api', 'dbt'])
    expect(out.services.dbt).toEqual({ build: './dbt' })
    expect(out.services.api.depends_on).toEqual(['db'])
  })

  it('keeps the bind mounts of the other services, because shared folders must still look shared', () => {
    const shared = `services:
  validation:
    build: ./validation
    volumes: ['./validation:/app', './creds:/creds']
    env_file: ./validation/.env
  metaapi:
    build: ./metaapi
    volumes: ['./creds:/creds']
    env_file: ./metaapi/.env
    healthcheck: { test: exit 0 }
`
    const out = parse(pruneToScope(shared, { ...config, scope: 'one', onlyServices: ['validation'] }))
    expect(out.services.metaapi).toEqual({ build: './metaapi', volumes: ['./creds:/creds'] })
  })

  it('drops a service that has no image, build or volumes to keep', () => {
    const odd = `services:
  api: { image: api }
  helper: { command: sleep 1 }
`
    expect(Object.keys(parse(pruneToScope(odd, { ...config, scope: 'one', onlyServices: ['api'] })).services)).toEqual(['api'])
  })

  it('leaves text it cannot read alone', () => {
    expect(pruneToScope('not: [valid', { ...config, scope: 'one', onlyServices: ['api'] })).toBe('not: [valid')
  })

  it('is what the request carries', () => {
    const r = buildRequest(file, { ...config, scope: 'one', onlyServices: ['db'] }, false)
    const sent = parse(r.yaml).services
    expect(sent.db).toEqual({ image: 'pg' })
    expect(sent.api).toEqual({ image: 'api' }) // a stub: no depends_on, nothing the plan could trip on
    expect(r.only_service).toBe('db')
  })
})

describe('stagesOf', () => {
  it('numbers services by their longest dependency chain', () => {
    const s = stagesOf(base.plan.workloads!)
    expect([s.get('db'), s.get('api'), s.get('web')]).toEqual([0, 1, 2])
  })

  it('does not loop on a cycle', () => {
    const s = stagesOf([wl('a', { DependsOn: ['b'] }), wl('b', { DependsOn: ['a'] })])
    expect(s.size).toBe(2)
  })
})

describe('derivePlan', () => {
  const config = defaultConfig({ services: [], details: [], builds: 0, gpu: 0, volumes: 0, ports: 0, dependencies: 0 })

  it('describes each service from the unaided plan when nothing changed', () => {
    const rows = derivePlan(base, null, config)
    const api = rows.find((r) => r.name === 'api')!
    expect(api).toMatchObject({ source: 'build ./api', build: true, stage: 1, node: 'mbp-m3', pickNode: 'mbp-m3', changed: false, cpuCores: 0.5, memGiB: 2 })
    expect(api.deps).toEqual([{ name: 'db', healthy: true }])
    expect(api.candidates.map((c) => [c.node, c.outcome, c.isPick])).toEqual([
      ['mbp-m3', 'selected', true],
      ['gpu-box', 'rejected', false],
      ['nuc-old', 'rejected', false],
    ])
  })

  it('selects the chosen node with a warning when its volume data is elsewhere, and rejects the rest with reasons', () => {
    const stateful: PlanResponse = { ...base, plan: { ...base.plan, workloads: [wl('db', { Storage: { Class: 'node_local_persistent', PinnedNodeID: '' } })] }, decisions: [dec('db', 'gpu-box', ['mbp-m3'])] }
    const now = [{ Project: 'shop', Service: 'db', Revision: 'latest', Image: 'x', Resources: { CPUMillis: 0, MemoryBytes: 0 }, DesiredState: 'running' as const, Status: 'ready', StatusReason: '', NodeID: 'mbp-m3' }]
    const db = derivePlan(stateful, null, config, now)[0]
    expect(db.candidates.map((c) => [c.node, c.outcome, c.inUse])).toEqual([
      ['gpu-box', 'selected', true],
      ['mbp-m3', 'rejected', false],
      ['nuc-old', 'rejected', false],
    ])
    expect(db.candidates[0].warning).toContain('volume data is on mbp-m3')
    expect(db.candidates[1].detail).toMatch(/Lower score than gpu-box|Same score as gpu-box|Scored higher/)
    expect(db.candidates[2].rejectedBy).toBe('scheduler')
  })

  it('does not call a higher-scoring node lower when the user chose another', () => {
    const moved: PlanResponse = { ...base, decisions: [dec('db', 'mbp-m3'), dec('api', 'gpu-box'), dec('web', 'mbp-m3')] }
    const api = derivePlan(base, moved, config).find((r) => r.name === 'api')!
    const pick = api.candidates.find((c) => c.isPick)!
    expect(pick.outcome).toBe('rejected')
    expect(pick.detail).not.toMatch(/^Lower score/)
  })

  it('shows the changed node as selected and keeps the scheduler pick marked', () => {
    const moved: PlanResponse = { ...base, decisions: [dec('db', 'mbp-m3'), dec('api', 'gpu-box'), dec('web', 'mbp-m3')] }
    const api = derivePlan(base, moved, config).find((r) => r.name === 'api')!
    expect(api).toMatchObject({ node: 'gpu-box', pickNode: 'mbp-m3', changed: true })
    expect(api.candidates.find((c) => c.isPick)!.node).toBe('mbp-m3')
    expect(api.candidates.find((c) => c.outcome === 'selected')!.node).toBe('gpu-box')
    expect(nodesUsed(derivePlan(base, moved, config))).toBe(2)
  })
})

describe('pinsFor', () => {
  const config = defaultConfig({ services: [], details: [], builds: 0, gpu: 0, volumes: 0, ports: 0, dependencies: 0 })

  it('pins every other service to its shown node so only the user change moves', () => {
    expect(pinsFor(base, { ...config, placements: { api: 'gpu-box' } })).toEqual({ db: 'mbp-m3', api: 'gpu-box', web: 'mbp-m3' })
  })

  it('leaves services kept on the host to pin_host', () => {
    expect(pinsFor(base, { ...config, hostServices: ['db'], placements: { api: 'gpu-box' } })).toEqual({ api: 'gpu-box', web: 'mbp-m3' })
  })
})

describe('requestLabel', () => {
  it('says so when no memory is requested', () => {
    expect(requestLabel({ cpuCores: 0.25, memGiB: 0 })).toBe('0.3 cores · no memory request')
  })

  it('shows small memory in MiB and large in GiB', () => {
    expect(requestLabel({ cpuCores: 0.25, memGiB: 0.125 })).toBe('0.3 cores · 128 MiB')
    expect(requestLabel({ cpuCores: 2.4, memGiB: 3 })).toBe('2.4 cores · 3.0 GiB')
  })
})

describe('liveState', () => {
  it('collapses workload statuses', () => {
    expect(liveState(undefined)).toBe('planned')
    expect(liveState('ready')).toBe('ready')
    expect(liveState('building')).toBe('progress')
    expect(liveState('running')).toBe('progress')
    expect(liveState('waiting_dependencies')).toBe('waiting')
    expect(liveState('failed')).toBe('failed')
  })
})
