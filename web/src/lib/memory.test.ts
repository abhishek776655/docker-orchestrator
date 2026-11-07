import { describe, expect, it } from 'vitest'
import type { ApiWorkload } from './api'
import { formatMem, liveMemGiB } from './memory'

const MIB = 1024 ** 2

function wl(used?: number): ApiWorkload {
  return {
    Project: 'p', Service: 's', Revision: 'latest', Image: '', Resources: { CPUMillis: 0, MemoryBytes: 0 },
    DesiredState: 'running', Status: 'ready', StatusReason: '', NodeID: 'n', MemoryUsedBytes: used,
  }
}

describe('liveMemGiB', () => {
  it('sums the workloads that reported and ignores the rest', () => {
    expect(liveMemGiB([wl(512 * MIB), wl(), wl(512 * MIB)])).toBe(1)
  })
  it('is unknown, not zero, when nothing reported', () => {
    expect(liveMemGiB([wl(), wl()])).toBeNull()
    expect(liveMemGiB([])).toBeNull()
  })
  it('keeps a real zero', () => {
    expect(liveMemGiB([wl(0)])).toBe(0)
  })
})

describe('formatMem', () => {
  it('uses MiB below 1 GiB and a dash when unknown', () => {
    expect(formatMem(null)).toBe('—')
    expect(formatMem(0.25)).toBe('256 MiB')
    expect(formatMem(2.5)).toBe('2.5 GiB')
  })
})
