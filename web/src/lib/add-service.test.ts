import { describe, expect, it } from 'vitest'
import type { ServiceInfo } from './compose-summary'
import { defaultConfig } from './deploy-config'
import { addConfig, addableServices, previewAdd, whyPlaced, withOverrides } from './add-service'
import type { Candidate } from './plan'

const svc = (name: string, dependsOn: string[] = []): ServiceInfo => ({ name, build: false, gpu: false, stateful: false, dependsOn })
const details = [svc('db'), svc('cache'), svc('api', ['db']), svc('worker', ['api', 'cache']), svc('web', ['api'])]

describe('adding services to a running project', () => {
  it('offers only services the project does not run yet', () => {
    expect(addableServices(details, ['db', 'api']).map((d) => d.name)).toEqual(['cache', 'worker', 'web'])
  })

  it('separates new services, dependencies to start, running dependencies and everything left alone', () => {
    const p = previewAdd(details, ['worker'], ['db', 'api', 'web'])
    expect(p).toEqual({ deploy: ['worker'], alsoNeeded: ['cache'], reused: ['db', 'api'], untouched: ['web'] })
  })

  it('leaves everything alone when the new service needs nothing', () => {
    expect(previewAdd(details, ['cache'], ['db', 'api'])).toEqual({ deploy: ['cache'], alsoNeeded: [], reused: [], untouched: ['db', 'api'] })
  })

  it('pins running dependencies, direct or through another service, to where they run so they cannot move', () => {
    const base = { ...defaultConfig({ services: [], details: [], builds: 0, gpu: 0, volumes: 0, ports: 0, dependencies: 0 }), placements: { worker: 'mac-b', stale: 'x' } }
    const preview = previewAdd(details, ['worker'], ['db', 'api'])
    const c = addConfig(base, ['worker'], preview, { api: 'mac-a', db: 'mac-a' })
    expect(c).toMatchObject({ scope: 'one', onlyServices: ['worker'], fresh: false, forced: [] })
    expect(c.placements).toEqual({ worker: 'mac-b', api: 'mac-a', db: 'mac-a' })
  })
})

describe('whyPlaced', () => {
  const sel = (over: Partial<Candidate> = {}): Candidate => ({ node: 'a', outcome: 'selected', inUse: true, isPick: true, score: 0.8, detail: '', ...over })
  const rej = (): Candidate => ({ node: 'b', outcome: 'rejected', rejectedBy: 'score', inUse: false, isPick: false, score: 0.5, detail: '' })

  it('says the best score when several nodes could take it', () => {
    expect(whyPlaced([sel(), rej()])).toBe('Best score of the 2 nodes that can take it (0.80).')
  })
  it('says it is the only node when the rest were refused', () => {
    expect(whyPlaced([sel(), { ...rej(), score: undefined, rejectedBy: 'scheduler' }])).toBe('The only node that can take it.')
  })
  it('says so when the user picked the node', () => {
    expect(whyPlaced([sel({ isPick: false }), rej()])).toBe('You chose this node.')
  })
})

describe('withOverrides', () => {
  const base = { ...defaultConfig({ services: [], details: [], builds: 0, gpu: 0, volumes: 0, ports: 0, dependencies: 0 }), placements: { api: 'mac-a' }, forced: [] as string[] }

  it('applies the chosen nodes and marks the rejected ones as forced', () => {
    const c = withOverrides(base, { worker: { node: 'mac-b', forced: false }, cache: { node: 'hp', forced: true } }, ['worker', 'cache'])
    expect(c.placements).toEqual({ api: 'mac-a', worker: 'mac-b', cache: 'hp' })
    expect(c.forced).toEqual(['cache'])
  })

  it('ignores a choice for a service that is no longer part of the add', () => {
    const c = withOverrides(base, { stale: { node: 'hp', forced: true } }, ['worker'])
    expect(c.placements).toEqual({ api: 'mac-a' })
    expect(c.forced).toEqual([])
  })
})
