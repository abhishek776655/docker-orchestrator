import { useState } from 'react'
import { ChevronRight } from 'lucide-react'
import { Badge, type Tone } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { requestLabel, type LiveState, type PlanService } from '@/lib/plan'
import { cn } from '@/lib/utils'

const COLS = '56px 130px minmax(120px, 1fr) 130px 170px 230px 110px 44px'

const STATE_TONE: Record<LiveState, Tone> = { ready: 'ok', progress: 'warn', failed: 'bad', waiting: 'off', stopped: 'off', planned: 'off' }
const STATE_LABEL: Record<LiveState, string> = { ready: 'ready', progress: 'starting', failed: 'failed', waiting: 'waiting', stopped: 'stopped', planned: 'planned' }

type Filter = 'all' | 'changed' | 'warn' | 'build' | 'gpu' | 'pin'

const FILTERS: { id: Filter; label: string; test: (s: PlanService) => boolean }[] = [
  { id: 'all', label: 'All', test: () => true },
  { id: 'changed', label: 'Changed', test: (s) => s.changed },
  { id: 'warn', label: 'Warnings', test: (s) => s.warnings.length > 0 },
  { id: 'build', label: 'Builds', test: (s) => s.build },
  { id: 'gpu', label: 'GPU', test: (s) => s.gpu },
  { id: 'pin', label: 'Pinned', test: (s) => s.pinnedTo !== '' || s.kept },
]

interface Props {
  services: PlanService[]
  states: Map<string, LiveState>
  locked: boolean // true once deployed: placements can no longer change
  onUse: (service: string, node: string) => void
  onReset: (service: string) => void
}

function Chip({ children, tone = 'off' }: { children: string; tone?: Tone }) {
  return (
    <Badge tone={tone} className="rounded px-2 py-0.5 text-[11px]">
      {children}
    </Badge>
  )
}

export function PlanTable({ services, states, locked, onUse, onReset }: Props) {
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState<Filter>('all')
  const [open, setOpen] = useState<Set<string>>(new Set())
  const q = query.trim().toLowerCase()
  const active = FILTERS.find((f) => f.id === filter)!
  const rows = services.filter((s) => active.test(s) && (!q || `${s.name} ${s.source} ${s.node}`.toLowerCase().includes(q)))

  const toggle = (name: string) =>
    setOpen((cur) => {
      const next = new Set(cur)
      if (!next.delete(name)) next.add(name)
      return next
    })

  return (
    <Card className="overflow-hidden">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2.5 border-b border-border px-4 py-3">
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search services, images or nodes"
          aria-label="Search the plan"
          className="min-h-11 min-w-0 flex-[1_1_260px] rounded-md border border-zinc-300 bg-card px-3"
        />
        <div role="group" aria-label="Filter the plan" className="flex flex-wrap gap-1.5">
          {FILTERS.map((f) => (
            <button
              key={f.id}
              type="button"
              aria-pressed={filter === f.id}
              onClick={() => setFilter(f.id)}
              className={cn(
                'min-h-11 rounded-full border px-3.5 text-[13px] font-medium',
                filter === f.id ? 'border-primary bg-primary text-primary-foreground' : 'border-zinc-300 bg-card text-zinc-700 hover:bg-muted',
              )}
            >
              {f.label} <span className="font-mono opacity-80">{services.filter(f.test).length}</span>
            </button>
          ))}
        </div>
      </div>

      <div className="overflow-x-auto">
        <div role="table" aria-label="Deployment plan" className="min-w-[1040px]">
          <div role="row" className="grid min-h-10 items-center border-b border-border bg-background px-4 text-xs font-medium text-muted-foreground" style={{ gridTemplateColumns: COLS }}>
            {['Stage', 'Service', 'Source', 'Requests', 'Constraints', 'Placed on', 'Status', ''].map((h, i) => (
              <span key={i} role="columnheader">
                {h}
              </span>
            ))}
          </div>
          <div className="max-h-[560px] overflow-y-auto">
            {rows.map((s) => {
              const isOpen = open.has(s.name)
              const state = states.get(s.name) ?? 'planned'
              const canChange = !s.kept && s.pinnedTo === '' && !locked
              return (
                <div key={s.name} className="border-b border-muted">
                  <div role="row" className="grid min-h-[52px] items-center px-4" style={{ gridTemplateColumns: COLS }}>
                    <span>
                      <span className="rounded bg-muted px-2 py-0.5 font-mono text-xs text-zinc-700">S{s.stage + 1}</span>
                    </span>
                    <span className="truncate font-mono font-medium">{s.name}</span>
                    <span className="truncate pr-3 font-mono text-xs text-muted-foreground">{s.source}</span>
                    <span className="font-mono text-xs">{requestLabel(s)}</span>
                    <span className="flex flex-wrap gap-1 pr-2">
                      {s.gpu && <Chip tone="info">GPU</Chip>}
                      {s.pinnedTo && <Chip>{`pinned ${s.pinnedTo}`}</Chip>}
                      {s.build && <Chip tone="warn">build</Chip>}
                      {s.ports.map((p) => (
                        <Chip key={p}>{`port ${p}`}</Chip>
                      ))}
                      {s.kept && <Chip tone="info">on host</Chip>}
                    </span>
                    <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 pr-2 text-[13px]">
                      <span className="font-mono font-medium">{s.node || 'unplaced'}</span>
                      {s.score !== null && <span className="font-mono text-xs text-muted-foreground">{s.score.toFixed(2)}</span>}
                      {s.changed && <Chip tone="info">changed</Chip>}
                      {canChange && (
                        <button type="button" onClick={() => toggle(s.name)} className="min-h-11 px-1.5 text-[13px] font-medium text-ok underline">
                          Change
                        </button>
                      )}
                    </span>
                    <span>
                      <Badge tone={STATE_TONE[state]}>{STATE_LABEL[state]}</Badge>
                    </span>
                    <button
                      type="button"
                      onClick={() => toggle(s.name)}
                      aria-expanded={isOpen}
                      aria-label={`${isOpen ? 'Hide' : 'Show'} placement details for ${s.name}`}
                      className="flex size-11 items-center justify-center text-zinc-700"
                    >
                      <ChevronRight className={cn('size-4 transition-transform', isOpen && 'rotate-90')} aria-hidden="true" />
                    </button>
                  </div>

                  {isOpen && (
                    <div className="flex flex-col gap-2.5 bg-background px-4 pb-4 pl-20 pt-1">
                      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 text-[13px]">
                        <span>
                          <span className="text-muted-foreground">Waits for:</span>{' '}
                          {s.deps.length ? s.deps.map((d) => `${d.name} (${d.healthy ? 'healthy' : 'started'})`).join(', ') : 'Nothing. It starts first.'}
                        </span>
                        {s.changed && !locked && (
                          <Button type="button" variant="outline" className="min-h-11 text-[13px]" onClick={() => onReset(s.name)}>
                            Reset to the scheduler's choice
                          </Button>
                        )}
                      </div>
                      {s.kept && <p className="m-0 text-[13px] text-muted-foreground">Kept on the host machine in Configuration. Change it there.</p>}
                      {s.warnings.map((w) => (
                        <p key={w} className="m-0 rounded-md bg-warn-soft px-3 py-2 text-[13px] text-warn-fg">
                          {w}
                        </p>
                      ))}
                      <div role="table" aria-label="Candidate nodes" className="overflow-hidden rounded-lg border border-border bg-card">
                        {s.candidates.length === 0 && <div className="p-3 text-[13px] text-muted-foreground">No scheduling details for this service.</div>}
                        {s.candidates.map((c) => (
                          <div
                            key={c.node}
                            role="row"
                            className={cn(
                              'grid min-h-12 items-center gap-2 border-b border-muted px-3 py-1 text-[13px]',
                              c.isPick && 'bg-ok-soft/40 shadow-[inset_3px_0_0_var(--color-ok)]',
                            )}
                            style={{ gridTemplateColumns: '130px 110px 60px 1fr 110px' }}
                          >
                            <span className="truncate font-mono">{c.node}</span>
                            <span>
                              <Badge tone={c.outcome === 'selected' ? 'ok' : c.outcome === 'eligible' ? 'info' : 'bad'} className="text-[11px]">
                                {c.outcome}
                              </Badge>
                            </span>
                            <span className="font-mono">{c.score !== undefined ? c.score.toFixed(2) : '-'}</span>
                            <span className="text-zinc-700">
                              {c.isPick && <span className="mr-1.5 inline-block rounded bg-ok px-2 py-0.5 text-[11px] font-semibold text-white">★ Scheduler's pick</span>}
                              {c.detail}
                            </span>
                            <span className="text-right">
                              {c.outcome !== 'rejected' && canChange && (
                                <Button
                                  type="button"
                                  variant="outline"
                                  className="min-h-11 px-3 text-[13px]"
                                  disabled={c.outcome === 'selected'}
                                  onClick={() => onUse(s.name, c.node)}
                                >
                                  {c.outcome === 'selected' ? 'Selected' : 'Use here'}
                                </Button>
                              )}
                            </span>
                          </div>
                        ))}
                      </div>
                    </div>
                  )}
                </div>
              )
            })}
            {rows.length === 0 && <div className="px-4 py-8 text-center text-muted-foreground">No services match this filter.</div>}
          </div>
        </div>
      </div>
    </Card>
  )
}
