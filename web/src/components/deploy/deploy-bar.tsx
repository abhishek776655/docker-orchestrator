import { Link } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import type { LiveState, PlanService } from '@/lib/plan'
import { cn } from '@/lib/utils'

interface Props {
  services: PlanService[]
  states: Map<string, LiveState>
  nodes: number
  stages: number
  deployed: boolean
  deploying: boolean
  error: string | null
  blocked: string | null // why Deploy is unavailable, when it is
  onBack: () => void
  fresh: boolean // the plan shown is already a fresh one
  onDeploy: (fresh: boolean) => void
}

const count = (services: PlanService[], states: Map<string, LiveState>, ...want: LiveState[]) =>
  services.filter((s) => want.includes(states.get(s.name) ?? 'planned')).length

export function DeployBar({ services, states, nodes, stages, deployed, deploying, error, blocked, fresh, onBack, onDeploy }: Props) {
  const total = services.length
  const ready = count(services, states, 'ready')
  const progress = count(services, states, 'progress')
  const failed = count(services, states, 'failed')
  const waiting = count(services, states, 'waiting', 'planned')
  const pct = (n: number) => (total ? (n / total) * 100 : 0)

  return (
    <section
      aria-label="Deploy"
      className="sticky bottom-0 z-[5] flex flex-col gap-3 rounded-[10px] border border-border bg-card px-5 py-4 shadow-[0_-4px_16px_rgba(24,24,27,0.08)]"
    >
      {error && (
        <p role="alert" className="m-0 rounded-md border border-bad/30 bg-bad-soft p-3 text-[13px] text-bad-fg">
          {error}
        </p>
      )}

      {!deployed && (
        <div className="flex flex-wrap items-center justify-between gap-3">
          <span className="flex flex-col leading-snug">
            <span className="font-semibold">{blocked ? 'Cannot deploy yet' : 'Ready to deploy'}</span>
            <span className="text-[13px] text-muted-foreground">
              {blocked ?? `${total} services across ${nodes} ${nodes === 1 ? 'node' : 'nodes'} in ${stages} ${stages === 1 ? 'stage' : 'stages'}`}
            </span>
          </span>
          <span className="flex flex-wrap gap-2">
            <Button type="button" variant="outline" onClick={onBack}>
              Back
            </Button>
            {!fresh && (
              <Button
                type="button"
                variant="outline"
                onClick={() => onDeploy(true)}
                disabled={!!blocked || deploying}
                title="Ignore earlier placements. Services may land on different nodes than this plan shows."
              >
                Deploy fresh
              </Button>
            )}
            <Button type="button" onClick={() => onDeploy(fresh)} disabled={!!blocked || deploying}>
              {deploying ? 'Deploying…' : `Deploy ${total} services${fresh ? ' (fresh)' : ''}`}
            </Button>
          </span>
        </div>
      )}

      {deployed && (
        <>
          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
            <span className="flex flex-col leading-snug">
              <span className="font-semibold">Deploying</span>
              <span className="text-[13px] text-muted-foreground">
                {ready} ready · {progress} in progress · {failed} failed · {waiting} waiting
              </span>
            </span>
            <Button asChild>
              <Link to="/projects">Open projects</Link>
            </Button>
          </div>
          <div role="progressbar" aria-label="Deploy progress" aria-valuemin={0} aria-valuemax={total} aria-valuenow={ready} className="flex h-2.5 overflow-hidden rounded-full bg-off-soft">
            <div className="bg-ok" style={{ width: `${pct(ready)}%` }} />
            <div className="bg-warn" style={{ width: `${pct(progress)}%` }} />
            <div className="bg-bad" style={{ width: `${pct(failed)}%` }} />
          </div>
          <ul aria-label="Stage progress" className="m-0 flex list-none flex-wrap gap-2 p-0">
            {Array.from({ length: stages }, (_, i) => {
              const inStage = services.filter((s) => s.stage === i)
              const done = inStage.filter((s) => states.get(s.name) === 'ready').length
              return (
                <li
                  key={i}
                  className={cn(
                    'rounded-full px-2.5 py-1 text-xs font-medium',
                    done === inStage.length ? 'bg-ok-soft text-ok-fg' : done > 0 ? 'bg-warn-soft text-warn-fg' : 'bg-off-soft text-off-fg',
                  )}
                >
                  Stage {i + 1} · {done}/{inStage.length}
                </li>
              )
            })}
          </ul>
        </>
      )}
    </section>
  )
}
