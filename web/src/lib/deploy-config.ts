import type { ComposeSummary, ServiceInfo } from './compose-summary'

// The choices made in step 2. They map onto the control plane's compose
// request: project, working_dir, no_cache, only_service, pin_host and fresh.
export interface DeployConfig {
  project: string
  // Folder on the control-plane machine that `build:` contexts and relative
  // bind mounts resolve against. A browser upload carries only the YAML.
  workingDir: string
  // Services to keep on the control-plane machine (pin_host).
  hostServices: string[]
  scope: 'all' | 'one'
  // With scope "one": the services picked to deploy; everything they depend
  // on is added automatically (only_service, comma-separated). Only the
  // picked ones are stored, so dropping a service drops dependencies that
  // nothing else needs.
  onlyServices: string[]
  noCache: boolean
  // Ignore where services were placed by earlier deploys (fresh).
  fresh: boolean
  // Placements changed in the plan step: service -> node (pin_nodes).
  placements: Record<string, string>
  // Services in placements whose node the scheduler rejected and the user
  // chose anyway (force): the rejection becomes a warning.
  forced: string[]
}

export function defaultConfig(summary: ComposeSummary): DeployConfig {
  return {
    project: sanitizeProject(summary.name ?? ''),
    workingDir: '',
    hostServices: [],
    scope: 'all',
    onlyServices: [],
    noCache: false,
    fresh: false,
    placements: {},
    forced: [],
  }
}

export function sanitizeProject(raw: string): string {
  return raw
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, '-')
    .replace(/^[^a-z0-9]+/, '')
}

const PROJECT_RE = /^[a-z0-9][a-z0-9_-]*$/

/** The services plus everything they depend on, through any number of levels, in compose order. */
export function dependencyClosure(details: ServiceInfo[], names: string | string[]): string[] {
  const byName = new Map(details.map((d) => [d.name, d]))
  const seen = new Set<string>()
  const walk = (n: string) => {
    if (seen.has(n) || !byName.has(n)) return
    seen.add(n)
    byName.get(n)!.dependsOn.forEach(walk)
  }
  ;(typeof names === 'string' ? [names] : names).forEach(walk)
  return details.filter((d) => seen.has(d.name)).map((d) => d.name)
}

/**
 * The services picked for a scoped deploy that exist in this file. Accepts a
 * draft saved before several could be picked ({ onlyService: "api" }).
 */
export function scopeRoots(summary: ComposeSummary, config: DeployConfig): string[] {
  return pickedServices(config).filter((s) => summary.services.includes(s))
}

/** The picked services as stored, including a draft's single legacy one. */
export function pickedServices(config: DeployConfig): string[] {
  const legacy = (config as { onlyService?: string }).onlyService
  return config.onlyServices ?? (legacy ? [legacy] : [])
}

/** For each dependency pulled in by the picked services, which picked ones need it. */
export function neededBy(details: ServiceInfo[], roots: string[]): Map<string, string[]> {
  const out = new Map<string, string[]>()
  for (const r of roots) {
    for (const dep of dependencyClosure(details, r)) {
      if (dep === r) continue
      out.set(dep, [...(out.get(dep) ?? []), r])
    }
  }
  return out
}

/**
 * The picked services after a click in the list. `shown` is what the list
 * showed checked (picked services and their dependencies); `next` is what it
 * reports after the click. A newly checked service becomes picked; an
 * unchecked one stops being picked (its dependencies follow, unless another
 * picked service still needs them).
 */
export function nextRoots(roots: string[], shown: string[], next: string[]): string[] {
  const added = next.filter((n) => !shown.includes(n))
  const removed = shown.filter((n) => !next.includes(n))
  return [...roots.filter((r) => !removed.includes(r)), ...added.filter((a) => !roots.includes(a))]
}

export type ConfigErrors = Partial<Record<'project' | 'workingDir' | 'onlyService', string>>

export function validateConfig(summary: ComposeSummary, config: DeployConfig): ConfigErrors {
  const errors: ConfigErrors = {}
  if (!PROJECT_RE.test(config.project)) {
    errors.project = 'Use lowercase letters, numbers, dashes and underscores, starting with a letter or number.'
  }
  const dir = config.workingDir.trim()
  if (summary.builds > 0 && dir === '') {
    errors.workingDir = `${summary.builds === 1 ? 'One service builds' : `${summary.builds} services build`} from source, so the control plane needs the project folder.`
  } else if (dir !== '' && !dir.startsWith('/')) {
    errors.workingDir = 'Use an absolute path, starting with /.'
  }
  if (config.scope === 'one' && scopeRoots(summary, config).length === 0) {
    errors.onlyService = 'Pick at least one service to deploy.'
  }
  return errors
}

/** Services in scope for this deploy, in compose order. */
export function servicesInScope(summary: ComposeSummary, config: DeployConfig): string[] {
  const roots = config.scope === 'one' ? scopeRoots(summary, config) : []
  return roots.length > 0 ? dependencyClosure(summary.details, roots) : summary.services
}
