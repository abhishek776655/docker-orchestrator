import { Check } from 'lucide-react'
import { cn } from '@/lib/utils'

export interface StepInfo {
  title: string
  sub: string
  locked?: boolean
}

interface Props {
  steps: StepInfo[]
  current: number // 1-based
  onSelect: (step: number) => void
}

export function Stepper({ steps, current, onSelect }: Props) {
  return (
    <nav aria-label="Deploy steps">
      <ol className="m-0 flex list-none flex-wrap items-center gap-x-2 gap-y-3 p-0">
        {steps.map((s, i) => {
          const n = i + 1
          const done = n < current
          const active = n === current
          return (
            <li key={s.title} className="flex min-w-0 flex-[1_1_240px] items-center gap-2">
              <button
                type="button"
                disabled={s.locked}
                aria-current={active ? 'step' : undefined}
                onClick={() => onSelect(n)}
                className={cn(
                  'flex min-h-[60px] min-w-0 flex-1 items-center gap-3 rounded-[10px] border py-1.5 pl-2.5 pr-3.5 text-left',
                  active ? 'border-primary bg-card' : 'border-border bg-background',
                  s.locked ? 'cursor-not-allowed opacity-50' : 'hover:bg-card',
                )}
              >
                <span
                  aria-hidden="true"
                  className={cn(
                    'flex size-8 shrink-0 items-center justify-center rounded-full text-sm font-semibold',
                    done ? 'bg-ok text-white' : active ? 'bg-primary text-primary-foreground' : 'bg-off-soft text-off-fg',
                  )}
                >
                  {done ? <Check className="size-4" /> : n}
                </span>
                <span className="flex min-w-0 flex-col leading-snug">
                  <span className="font-semibold">{s.title}</span>
                  <span className="truncate text-xs text-muted-foreground">{s.sub}</span>
                </span>
              </button>
              {n < steps.length && <span aria-hidden="true" className={cn('h-0.5 w-5 shrink-0', done ? 'bg-ok' : 'bg-zinc-300')} />}
            </li>
          )
        })}
      </ol>
    </nav>
  )
}
