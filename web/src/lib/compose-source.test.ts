import { describe, expect, it } from 'vitest'
import { chooseSource } from './compose-source'
import type { SavedDeploy } from './saved-deploys'

const saved = { file: { name: 'docker-compose.yml', size: 3, text: 'old' }, config: {}, at: '' } as unknown as SavedDeploy

describe('chooseSource', () => {
  it('prefers the file on disk and says when it changed', () => {
    const s = chooseSource(saved, { name: 'docker-compose.yml', yaml: 'new' })
    expect(s).toMatchObject({ text: 'new', from: 'disk', changed: true })
    expect(s.note).toContain('changed since the last deploy')
  })
  it('reports an unchanged file without calling it changed', () => {
    expect(chooseSource(saved, { name: 'docker-compose.yml', yaml: 'old' })).toMatchObject({ from: 'disk', changed: false })
  })
  it('falls back to the saved copy and says why', () => {
    const s = chooseSource(saved, null, 'Could not read /x: not found.')
    expect(s).toMatchObject({ text: 'old', from: 'saved', changed: false })
    expect(s.note).toContain('Could not read /x')
    expect(chooseSource(saved, null).note).toContain('No project folder')
  })
})
