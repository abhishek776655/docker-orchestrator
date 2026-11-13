import { describe, expect, it } from 'vitest'
import { dependentsOf, removalPlan } from './remove-service'

const w = (Service: string, DependsOn: string[] = []) => ({ Service, DependsOn })
const stack = [w('db'), w('cache'), w('api', ['db']), w('worker', ['api', 'cache']), w('web', ['api'])]

describe('removalPlan', () => {
  it('removes only the service when nothing depends on it', () => {
    expect(removalPlan(stack, ['web'])).toEqual({ remove: ['web'], alsoRemoved: [] })
  })
  it('also removes services that depend on it, through any number of levels', () => {
    expect(removalPlan(stack, ['db'])).toEqual({ remove: ['db'], alsoRemoved: ['api', 'worker', 'web'] })
  })
  it('does not list a picked service as an extra, and keeps unrelated services', () => {
    expect(removalPlan(stack, ['api', 'web'])).toEqual({ remove: ['api', 'web'], alsoRemoved: ['worker'] })
  })
  it('copes with a missing dependency list', () => {
    expect(removalPlan([{ Service: 'a' }, { Service: 'b', DependsOn: null }], ['a'])).toEqual({ remove: ['a'], alsoRemoved: [] })
  })
})

describe('dependentsOf', () => {
  it('lists what depends on a service, through other services too', () => {
    expect(dependentsOf(stack, 'db')).toEqual(['api', 'worker', 'web'])
    expect(dependentsOf(stack, 'web')).toEqual([])
  })
})
