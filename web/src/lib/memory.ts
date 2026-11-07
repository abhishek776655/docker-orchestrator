import type { ApiWorkload } from './api'

const GIB = 1024 ** 3

/**
 * Live memory in use across these workloads, in GiB, from the agents'
 * latest reports. null when none of them has a fresh reading, so the UI can
 * say "unknown" instead of a misleading 0.
 */
export function liveMemGiB(workloads: ApiWorkload[]): number | null {
  let total = 0
  let known = false
  for (const w of workloads) {
    if (typeof w.MemoryUsedBytes === 'number') {
      total += w.MemoryUsedBytes
      known = true
    }
  }
  return known ? total / GIB : null
}

/** "1.4 GiB", "312 MiB", or "—" when unknown. */
export function formatMem(gib: number | null): string {
  if (gib === null) return '—'
  if (gib < 1) return `${Math.round(gib * 1024)} MiB`
  return `${gib.toFixed(1)} GiB`
}
