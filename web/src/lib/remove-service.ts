import type { ApiWorkload } from './api'

/** What removing services would delete, including the services that need them. */
export interface RemovalPlan {
  remove: string[] // the services picked
  alsoRemoved: string[] // services that depend on them, directly or through others
}

/**
 * A service that depends on a removed one would wait for it forever, so it
 * is removed too. Dependents are found through any number of levels.
 */
export function removalPlan(workloads: Pick<ApiWorkload, 'Service' | 'DependsOn'>[], picked: string[]): RemovalPlan {
  const gone = new Set(picked)
  for (let changed = true; changed; ) {
    changed = false
    for (const w of workloads) {
      if (gone.has(w.Service)) continue
      if ((w.DependsOn ?? []).some((d) => gone.has(d))) {
        gone.add(w.Service)
        changed = true
      }
    }
  }
  const order = workloads.map((w) => w.Service)
  const sorted = (names: string[]) => [...new Set(names)].sort((a, b) => order.indexOf(a) - order.indexOf(b))
  return {
    remove: sorted(picked),
    alsoRemoved: sorted([...gone].filter((s) => !picked.includes(s))),
  }
}

/**
 * The services that depend on this one, directly or through others. They are
 * the ones removed with it, and the ones restarted when it moves to another
 * node.
 */
export function dependentsOf(workloads: Pick<ApiWorkload, 'Service' | 'DependsOn'>[], service: string): string[] {
  return removalPlan(workloads, [service]).alsoRemoved
}
