import { api } from './api'
import { loadDeploy, saveDeploy, type SavedDeploy } from './saved-deploys'

/**
 * Which Compose file a project action works from. The control plane keeps no
 * copy, so the console used to rely on the text saved in this browser at the
 * last deploy, which goes stale as soon as the file on disk is edited. Now the
 * file is read again from the project folder on the control-plane machine,
 * and the saved copy is only the fallback.
 */
export interface ComposeSource {
  text: string
  name: string
  from: 'disk' | 'saved'
  /** The file on disk differs from the copy saved at the last deploy. */
  changed: boolean
  /** One line saying where the text came from. */
  note: string
}

/** Decides between what was read from disk and the saved copy. Pure, for testing. */
export function chooseSource(saved: SavedDeploy, disk: { name: string; yaml: string } | null, problem?: string): ComposeSource {
  if (disk) {
    const changed = disk.yaml !== saved.file.text
    return {
      text: disk.yaml,
      name: disk.name,
      from: 'disk',
      changed,
      note: changed ? `${disk.name} read from disk; it changed since the last deploy.` : `${disk.name} read from disk.`,
    }
  }
  return {
    text: saved.file.text,
    name: saved.file.name,
    from: 'saved',
    changed: false,
    note: problem ? `${problem} Using the copy saved at the last deploy.` : 'No project folder is saved, so using the copy saved at the last deploy.',
  }
}

/**
 * The project's Compose file as it is now: read from the saved project
 * folder, else the copy saved in this browser. null when this browser never
 * deployed the project. A changed file also replaces the saved copy, so
 * Edit configuration and later actions start from it too.
 */
export async function loadComposeSource(project: string): Promise<ComposeSource | null> {
  const saved = loadDeploy(project)
  if (!saved) return null
  const dir = saved.config.workingDir?.trim()
  if (!dir) return chooseSource(saved, null)
  try {
    const disk = await api.readCompose(dir)
    const source = chooseSource(saved, { name: disk.name, yaml: disk.yaml })
    if (source.changed) saveDeploy(project, { ...saved, file: { name: disk.name, size: disk.yaml.length, text: disk.yaml } })
    return source
  } catch (e) {
    return chooseSource(saved, null, `Could not read ${dir}: ${(e as Error).message}.`)
  }
}
