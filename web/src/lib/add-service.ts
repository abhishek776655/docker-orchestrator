import type { ServiceInfo } from './compose-summary'
import type { Candidate } from './plan'
import { dependencyClosure, type DeployConfig } from './deploy-config'

/** What adding services would touch, so the user sees it before anything starts. */
export interface AddPreview {
  deploy: string[] // the services picked
  alsoNeeded: string[] // dependencies that are not running yet; they start too
  reused: string[] // dependencies already running; left where they are
  untouched: string[] // every other running service
}

/** Services in the Compose file that the project does not run yet. */
export function addableServices(details: ServiceInfo[], deployed: string[]): ServiceInfo[] {
  const have = new Set(deployed)
  return details.filter((d) => !have.has(d.name))
}

export function previewAdd(details: ServiceInfo[], picked: string[], deployed: string[]): AddPreview {
  const have = new Set(deployed)
  const chosen = new Set(picked)
  const closure = dependencyClosure(details, picked)
  const others = closure.filter((n) => !chosen.has(n))
  const reused = others.filter((n) => have.has(n))
  return {
    deploy: closure.filter((n) => chosen.has(n)),
    alsoNeeded: others.filter((n) => !have.has(n)),
    reused,
    untouched: deployed.filter((n) => !reused.includes(n) && !chosen.has(n)),
  }
}

/**
 * The config for a deploy that adds only these services. Running
 * dependencies are pinned to the node they already run on, so deploying
 * them again as part of the closure cannot move them.
 */
export function addConfig(base: DeployConfig, picked: string[], preview: AddPreview, runningNodes: Record<string, string>): DeployConfig {
  const placements: Record<string, string> = {}
  for (const s of [...preview.deploy, ...preview.alsoNeeded]) if (base.placements[s]) placements[s] = base.placements[s]
  for (const s of preview.reused) if (runningNodes[s]) placements[s] = runningNodes[s]
  return { ...base, scope: 'one', onlyServices: picked, fresh: false, placements, forced: [] }
}

/** One plain sentence saying why the service landed on its node. */
export function whyPlaced(candidates: Candidate[]): string {
  const chosen = candidates.find((c) => c.outcome === 'selected')
  if (!chosen) return ''
  if (!chosen.isPick) return 'You chose this node.'
  const scored = candidates.filter((c) => c.score !== undefined).length
  if (scored <= 1) return 'The only node that can take it.'
  return `Best score of the ${scored} nodes that can take it (${(chosen.score ?? 0).toFixed(2)}).`
}

/** A node the user chose for a service instead of the scheduler's pick. */
export interface Override {
  node: string
  forced: boolean // the scheduler or the volume rules rejected it
}

/**
 * The config with the user's node choices applied. Only services that are
 * part of this add count; a choice left over for one that is no longer
 * picked is ignored.
 */
export function withOverrides(config: DeployConfig, overrides: Record<string, Override>, services: string[]): DeployConfig {
  const placements = { ...config.placements }
  const forced: string[] = []
  for (const name of services) {
    const o = overrides[name]
    if (!o) continue
    placements[name] = o.node
    if (o.forced) forced.push(name)
  }
  return { ...config, placements, forced }
}
