import { describe, expect, it } from 'vitest'
import { summarizeCompose } from './compose-summary'
import { defaultConfig, dependencyClosure, neededBy, nextRoots, sanitizeProject, scopeRoots, servicesInScope, validateConfig, type DeployConfig } from './deploy-config'

const FILE = `
name: Store Front
services:
  db:
    image: postgres
    volumes:
      - data:/var/lib/postgresql/data
  cache:
    image: redis
  api:
    build: ./api
    volumes:
      - ./cfg.json:/app/cfg.json
    depends_on: [db, cache]
  web:
    image: nginx
    depends_on:
      api:
        condition: service_healthy
`

function summary() {
  const r = summarizeCompose(FILE)
  if (!r.ok) throw new Error(r.error)
  return r.summary
}

describe('compose details', () => {
  it('marks named volumes as stateful and bind-mounted config files as not', () => {
    const byName = Object.fromEntries(summary().details.map((d) => [d.name, d]))
    expect(byName.db.stateful).toBe(true)
    expect(byName.api.stateful).toBe(false)
    expect(byName.api.build).toBe(true)
    expect(byName.web.dependsOn).toEqual(['api'])
  })
})

describe('defaultConfig and sanitizeProject', () => {
  it('turns the compose name into a valid project name', () => {
    expect(sanitizeProject('Store Front')).toBe('store-front')
    expect(defaultConfig(summary()).project).toBe('store-front')
    expect(sanitizeProject('--Weird__Name!')).toBe('weird__name-')
  })
})

describe('dependencyClosure and servicesInScope', () => {
  it('follows dependencies through every level', () => {
    expect(dependencyClosure(summary().details, 'web')).toEqual(['db', 'cache', 'api', 'web'])
    expect(dependencyClosure(summary().details, 'cache')).toEqual(['cache'])
  })

  it('returns every service for scope "all" and the closure for "one"', () => {
    const s = summary()
    const base = defaultConfig(s)
    expect(servicesInScope(s, base)).toEqual(['db', 'cache', 'api', 'web'])
    expect(servicesInScope(s, { ...base, scope: 'one', onlyServices: ['api'] })).toEqual(['db', 'cache', 'api'])
  })
})

describe('validateConfig', () => {
  it('requires a project folder when something builds, and an absolute one', () => {
    const s = summary()
    const base = { ...defaultConfig(s), project: 'store' }
    expect(validateConfig(s, base).workingDir).toMatch(/builds from source/)
    expect(validateConfig(s, { ...base, workingDir: 'relative/dir' }).workingDir).toMatch(/absolute/)
    expect(validateConfig(s, { ...base, workingDir: '/Users/me/store' })).toEqual({})
  })

  it('does not require a folder when nothing builds', () => {
    const r = summarizeCompose('services:\n  a:\n    image: x\n')
    if (!r.ok) throw new Error(r.error)
    expect(validateConfig(r.summary, { ...defaultConfig(r.summary), project: 'p' })).toEqual({})
  })

  it('rejects a bad project name and a missing single service', () => {
    const s = summary()
    const errors = validateConfig(s, { ...defaultConfig(s), project: 'Bad Name', workingDir: '/x', scope: 'one', onlyServices: [] })
    expect(errors.project).toBeDefined()
    expect(errors.onlyService).toBeDefined()
  })
})

describe('several picked services', () => {
  // db and cache have no dependencies; api needs both; web needs api.
  it('deploys the union of their dependencies, in compose order', () => {
    const s = summary()
    expect(servicesInScope(s, { ...defaultConfig(s), scope: 'one', onlyServices: ['cache', 'db'] })).toEqual(['db', 'cache'])
    expect(dependencyClosure(s.details, ['web', 'cache'])).toEqual(['db', 'cache', 'api', 'web'])
  })

  it('says which picked services need each dependency', () => {
    const s = summary()
    const needs = neededBy(s.details, ['web', 'api'])
    expect(needs.get('db')).toEqual(['web', 'api'])
    expect(needs.get('api')).toEqual(['web'])
    expect(needs.has('web')).toBe(false)
  })

  it('clicking a service picks it; its dependencies come along and drop with it', () => {
    const s = summary()
    let roots: string[] = []
    const shown = () => dependencyClosure(s.details, roots)
    roots = nextRoots(roots, shown(), [...shown(), 'api'])
    expect(roots).toEqual(['api'])
    expect(shown()).toEqual(['db', 'cache', 'api'])
    roots = nextRoots(roots, shown(), shown().filter((n) => n !== 'api'))
    expect(roots).toEqual([])
    expect(shown()).toEqual([])
  })

  it('unpicking a service keeps a dependency another picked service still needs', () => {
    const s = summary()
    const roots = ['web', 'api']
    const shown = dependencyClosure(s.details, roots)
    const after = nextRoots(roots, shown, shown.filter((n) => n !== 'api'))
    expect(after).toEqual(['web'])
    expect(dependencyClosure(s.details, after)).toContain('api')
  })

  it('reads a draft saved with a single onlyService', () => {
    const s = summary()
    const legacy = { ...defaultConfig(s), scope: 'one', onlyService: 'api' } as unknown as DeployConfig
    delete (legacy as Partial<DeployConfig>).onlyServices
    expect(scopeRoots(s, legacy)).toEqual(['api'])
  })
})
