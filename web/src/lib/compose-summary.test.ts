import { describe, expect, it } from 'vitest'
import { describeSummary, formatBytes, summarizeCompose } from './compose-summary'

const SAMPLE = `
name: ml-infer
services:
  embedder:
    build: ./embedder
    ports:
      - "8081:8081"
    deploy:
      resources:
        reservations:
          devices:
            - capabilities: [gpu]
  reranker:
    image: python:3.11-slim
    gpus: all
  gateway:
    image: ml-gateway:latest
    volumes:
      - ./cfg.json:/app/cfg.json
    depends_on:
      embedder:
        condition: service_healthy
      reranker:
        condition: service_started
`

describe('summarizeCompose', () => {
  it('counts services, builds, GPU users, volumes, ports and dependencies', () => {
    const r = summarizeCompose(SAMPLE)
    expect(r.ok && { ...r.summary, details: undefined }).toEqual({
      name: 'ml-infer',
      services: ['embedder', 'reranker', 'gateway'],
      details: undefined,
      builds: 1,
      gpu: 2,
      volumes: 1,
      ports: 1,
      dependencies: 2,
    })
    expect(r.ok && r.summary.details.map((d) => [d.name, d.build, d.gpu, d.stateful])).toEqual([
      ['embedder', true, true, false],
      ['reranker', false, true, false],
      ['gateway', false, false, false],
    ])
  })

  it('counts list-style depends_on', () => {
    const r = summarizeCompose('services:\n  a:\n    image: x\n  b:\n    image: y\n    depends_on: [a]\n')
    expect(r.ok && r.summary.dependencies).toBe(1)
  })

  it('rejects an empty file', () => {
    expect(summarizeCompose('  \n')).toEqual({ ok: false, error: 'The file is empty.' })
  })

  it('rejects YAML that does not parse', () => {
    const r = summarizeCompose('services: [unclosed')
    expect(r.ok).toBe(false)
    if (!r.ok) expect(r.error).toMatch(/not valid YAML/)
  })

  it('rejects a file with no services section', () => {
    const r = summarizeCompose('version: "3"\nvolumes:\n  data: {}\n')
    expect(r.ok).toBe(false)
    if (!r.ok) expect(r.error).toMatch(/No services found/)
  })

  it('treats a service with no body as a service', () => {
    const r = summarizeCompose('services:\n  lonely:\n')
    expect(r.ok && r.summary.services).toEqual(['lonely'])
  })
})

describe('describeSummary and formatBytes', () => {
  it('describes counts with correct plurals and omits zeros', () => {
    const base = { details: [], volumes: 0, ports: 0, dependencies: 0 }
    expect(describeSummary({ ...base, services: ['a'], builds: 0, gpu: 0 })).toBe('1 service')
    expect(describeSummary({ ...base, services: ['a', 'b'], builds: 1, gpu: 2 })).toBe('2 services · 1 build · 2 need a GPU')
  })

  it('formats sizes', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(2150)).toBe('2.1 KB')
    expect(formatBytes(3 * 1024 * 1024)).toBe('3.0 MB')
  })
})
