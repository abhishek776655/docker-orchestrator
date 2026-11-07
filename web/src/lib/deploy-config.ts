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
  // With scope "one": the service to deploy together with everything it
  // depends on (only_service).
  onlyService: string
  noCache: boolean
  // Ignore where services were placed by earlier deploys (fresh).
  fresh: boolean
  // Placements changed in the plan step: service -> node (pin_nodes).
  placements: Record<string, string>
}

export function defaultConfig(summary: ComposeSummary): DeployConfig {
  return {
    project: sanitizeProject(summary.name ?? ''),
    workingDir: '',
    hostServices: [],
    scope: 'all',
    onlyService: '',
    noCache: false,
    fresh: false,
    placements: {},
  }
}

export function sanitizeProject(raw: string): string {
  return raw
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, '-')
    .replace(/^[^a-z0-9]+/, '')
}

const PROJECT_RE = /^[a-z0-9][a-z0-9_-]*$/

/** The service plus everything it depends on, through any number of levels. */
export function dependencyClosure(details: ServiceInfo[], name: string): string[] {
  const byName = new Map(details.map((d) => [d.name, d]))
  const seen = new Set<string>()
  const walk = (n: string) => {
    if (seen.has(n) || !byName.has(n)) return
    seen.add(n)
    byName.get(n)!.dependsOn.forEach(walk)
  }
  walk(name)
  return details.filter((d) => seen.has(d.name)).map((d) => d.name)
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
  if (config.scope === 'one' && !summary.services.includes(config.onlyService)) {
    errors.onlyService = 'Pick the service to deploy.'
  }
  return errors
}

/** Services in scope for this deploy, in compose order. */
export function servicesInScope(summary: ComposeSummary, config: DeployConfig): string[] {
  if (config.scope === 'one' && summary.services.includes(config.onlyService)) {
    return dependencyClosure(summary.details, config.onlyService)
  }
  return summary.services
}
