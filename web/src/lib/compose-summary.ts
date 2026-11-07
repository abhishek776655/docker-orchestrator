import { parse } from 'yaml'

export interface ServiceInfo {
  name: string
  build: boolean
  gpu: boolean
  // Keeps data in a named volume. Config files bind-mounted from the project
  // folder do not count: they are rewritten on every start.
  stateful: boolean
  dependsOn: string[]
}

export interface ComposeSummary {
  // The top-level `name:` of the file, when it has one.
  name?: string
  services: string[]
  details: ServiceInfo[]
  builds: number
  gpu: number
  volumes: number
  ports: number
  dependencies: number
}

export type ComposeResult = { ok: true; summary: ComposeSummary } | { ok: false; error: string }

type Obj = Record<string, unknown>

const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v)

function asList(v: unknown): unknown[] {
  return Array.isArray(v) ? v : []
}

// A service wants a GPU when it reserves a device with the "gpu" capability,
// sets the short `gpus` key, or selects the nvidia runtime.
function needsGpu(svc: Obj): boolean {
  if (svc.gpus !== undefined || svc.runtime === 'nvidia') return true
  const deploy = isObj(svc.deploy) ? svc.deploy : {}
  const resources = isObj(deploy.resources) ? deploy.resources : {}
  const reservations = isObj(resources.reservations) ? resources.reservations : {}
  return asList(reservations.devices).some((d) => isObj(d) && asList(d.capabilities).includes('gpu'))
}

function dependencyNames(svc: Obj): string[] {
  const dep = svc.depends_on
  if (Array.isArray(dep)) return dep.filter((d): d is string => typeof d === 'string')
  if (isObj(dep)) return Object.keys(dep)
  return []
}

// "data:/var/lib/db" (short form, source with no path prefix) and
// `type: volume` (long form) are named volumes. "./cfg:/app" and "/host:/app"
// are bind mounts.
function isNamedVolume(entry: unknown): boolean {
  if (typeof entry === 'string') {
    const source = entry.split(':')[0]
    return entry.includes(':') && source !== '' && !/^[./~]/.test(source)
  }
  return isObj(entry) && entry.type === 'volume'
}

/**
 * Reads just enough of a Compose file to describe it before it is planned:
 * how many services, builds, GPU users, volumes, published ports and
 * dependencies. The control plane does the real validation when it plans.
 */
export function summarizeCompose(text: string): ComposeResult {
  if (text.trim() === '') return { ok: false, error: 'The file is empty.' }
  let doc: unknown
  try {
    doc = parse(text)
  } catch (e) {
    const message = e instanceof Error ? e.message.split('\n')[0] : 'unknown error'
    return { ok: false, error: `This is not valid YAML: ${message}` }
  }
  if (!isObj(doc) || !isObj(doc.services) || Object.keys(doc.services).length === 0) {
    return { ok: false, error: 'No services found. A Compose file needs a top-level "services" section.' }
  }
  const summary: ComposeSummary = {
    name: typeof doc.name === 'string' ? doc.name : undefined,
    services: [],
    details: [],
    builds: 0,
    gpu: 0,
    volumes: 0,
    ports: 0,
    dependencies: 0,
  }
  for (const [name, raw] of Object.entries(doc.services)) {
    const svc = isObj(raw) ? raw : {}
    const volumes = asList(svc.volumes)
    const info: ServiceInfo = {
      name,
      build: svc.build !== undefined,
      gpu: needsGpu(svc),
      stateful: volumes.some(isNamedVolume),
      dependsOn: dependencyNames(svc),
    }
    summary.services.push(name)
    summary.details.push(info)
    if (info.build) summary.builds++
    if (info.gpu) summary.gpu++
    summary.volumes += volumes.length
    summary.ports += asList(svc.ports).length
    summary.dependencies += info.dependsOn.length
  }
  return { ok: true, summary }
}

export function describeSummary(s: ComposeSummary): string {
  const parts = [`${s.services.length} ${s.services.length === 1 ? 'service' : 'services'}`]
  if (s.builds) parts.push(`${s.builds} ${s.builds === 1 ? 'build' : 'builds'}`)
  if (s.gpu) parts.push(`${s.gpu} ${s.gpu === 1 ? 'needs' : 'need'} a GPU`)
  return parts.join(' · ')
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
