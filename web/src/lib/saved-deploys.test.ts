import { describe, expect, it } from 'vitest'
import { defaultConfig } from './deploy-config'
import { deployLink, loadDeploy, saveDeploy, type SavedDeploy } from './saved-deploys'

function memory(initial?: string) {
  let value = initial ?? null
  return { getItem: () => value, setItem: (_: string, v: string) => void (value = v) }
}

const deploy = (name: string): SavedDeploy => ({
  file: { name, size: 10, text: 'services: {}' },
  config: { ...defaultConfig({ services: [], details: [], builds: 0, gpu: 0, volumes: 0, ports: 0, dependencies: 0 }), project: name },
  at: '2026-11-07T10:00:00Z',
})

describe('saved deploys', () => {
  it('returns what was saved, per project', () => {
    const s = memory()
    saveDeploy('shop', deploy('shop'), s)
    saveDeploy('blog', deploy('blog'), s)
    expect(loadDeploy('shop', s)?.file.name).toBe('shop')
    expect(loadDeploy('blog', s)?.file.name).toBe('blog')
    expect(loadDeploy('nope', s)).toBeNull()
  })

  it('survives unreadable storage', () => {
    expect(loadDeploy('shop', memory('{not json'))).toBeNull()
    expect(loadDeploy('shop', { getItem: () => { throw new Error('blocked') } })).toBeNull()
    expect(() => saveDeploy('shop', deploy('shop'), { getItem: () => null, setItem: () => { throw new Error('full') } })).not.toThrow()
  })

  it('ignores a malformed record', () => {
    expect(loadDeploy('shop', memory('{"shop":{"file":1}}'))).toBeNull()
  })
})

describe('deployLink', () => {
  it('opens configuration for edit, the plan for replan, and a fresh plan for redeploy', () => {
    expect(deployLink('shop', 'edit')).toBe('/deploy?project=shop&step=2')
    expect(deployLink('shop', 'replan')).toBe('/deploy?project=shop&step=3')
    expect(deployLink('shop', 'fresh')).toBe('/deploy?project=shop&step=3&fresh=1')
  })
})
