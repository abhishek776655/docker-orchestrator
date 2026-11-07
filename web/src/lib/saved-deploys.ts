import type { DeployConfig } from './deploy-config'

// The control plane keeps no copy of the Compose file a project was deployed
// from, so the console remembers the last one it deployed, per project, in this
// browser. That is what lets the project page redeploy, edit the configuration
// or replan without asking for the file again. Projects deployed from the CLI,
// or from another browser, have no saved copy.

const KEY = 'haco.deploys.v1'

export interface SavedDeploy {
  file: { name: string; size: number; text: string }
  config: DeployConfig
  at: string // ISO time of the deploy
}

type Store = Record<string, SavedDeploy>

function read(storage: Pick<Storage, 'getItem'>): Store {
  try {
    const raw = storage.getItem(KEY)
    const parsed = raw ? (JSON.parse(raw) as unknown) : {}
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed) ? (parsed as Store) : {}
  } catch {
    return {}
  }
}

export function loadDeploy(project: string, storage: Pick<Storage, 'getItem'> = localStorage): SavedDeploy | null {
  try {
    const found = read(storage)[project]
    return found && typeof found.file?.text === 'string' && found.config ? found : null
  } catch {
    return null
  }
}

export function saveDeploy(project: string, deploy: SavedDeploy, storage: Pick<Storage, 'getItem' | 'setItem'> = localStorage): void {
  try {
    storage.setItem(KEY, JSON.stringify({ ...read(storage), [project]: deploy }))
  } catch {
    // storage unavailable or full: remembering is a convenience
  }
}

/** The link that opens the Deploy page ready for one of the project's actions. */
export function deployLink(project: string, action: 'edit' | 'replan' | 'fresh'): string {
  const step = action === 'edit' ? 2 : 3
  const p = new URLSearchParams({ project, step: String(step) })
  if (action === 'fresh') p.set('fresh', '1')
  return `/deploy?${p.toString()}`
}
