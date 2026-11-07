import { describe, expect, it } from 'vitest'
import { paginate } from './paginate'

const list = Array.from({ length: 53 }, (_, i) => i + 1)

describe('paginate', () => {
  it('slices one page and reports the range', () => {
    expect(paginate(list, 2, 25)).toMatchObject({ items: list.slice(25, 50), page: 2, pages: 3, from: 26, to: 50, total: 53 })
  })
  it('keeps the short last page', () => {
    expect(paginate(list, 3, 25)).toMatchObject({ items: [51, 52, 53], from: 51, to: 53 })
  })
  it('snaps an out-of-range page into range', () => {
    expect(paginate(list, 9, 25).page).toBe(3)
    expect(paginate(list, 0, 25).page).toBe(1)
  })
  it('handles an empty list', () => {
    expect(paginate([], 1, 25)).toMatchObject({ items: [], page: 1, pages: 1, from: 0, to: 0, total: 0 })
  })
})
