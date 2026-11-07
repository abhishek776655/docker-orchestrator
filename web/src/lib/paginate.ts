export interface Page<T> {
  items: T[]
  page: number // 1-based, clamped into range
  pages: number // at least 1
  from: number // 1-based index of the first item shown, 0 when empty
  to: number
  total: number
}

/** One page of a list. An out-of-range page snaps to the nearest real one. */
export function paginate<T>(all: T[], page: number, size: number): Page<T> {
  const total = all.length
  const pages = Math.max(1, Math.ceil(total / size))
  const p = Math.min(Math.max(1, Math.floor(page) || 1), pages)
  const start = (p - 1) * size
  const items = all.slice(start, start + size)
  return { items, page: p, pages, from: total ? start + 1 : 0, to: start + items.length, total }
}
