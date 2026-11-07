import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, CheckCircle2, ChevronRight } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { api } from '@/lib/api'
import { deriveAttention, tileCounts, type AttentionItem } from '@/lib/attention'
import { deriveNodes, deriveProjects, eventTone, sinceLabel, statusTone } from '@/lib/cluster'
import { cn } from '@/lib/utils'

function Tile({ label, value, of, note, tone, to }: { label: string; value: number; of?: number; note: string; tone?: 'bad' | 'warn'; to: string }) {
  return (
    <Link to={to} className="no-underline">
      <Card className="flex h-full flex-col gap-1 p-4 transition-colors hover:bg-muted/40">
        <span className="text-xs text-muted-foreground">{label}</span>
        <span className={cn('text-[28px] font-semibold leading-tight', tone === 'bad' && value > 0 && 'text-bad-fg', tone === 'warn' && value > 0 && 'text-warn-fg')}>
          {value}
          {of !== undefined && <span className="text-base font-normal text-muted-foreground"> / {of}</span>}
        </span>
        <span className="text-[13px] text-muted-foreground">{note}</span>
      </Card>
    </Link>
  )
}

const DOT: Record<AttentionItem['tone'], string> = { bad: 'bg-bad', warn: 'bg-warn', off: 'bg-off' }

export function DashboardPage() {
  const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: api.nodes, refetchInterval: 3000 })
  const workloadsQ = useQuery({ queryKey: ['workloads'], queryFn: api.workloads, refetchInterval: 3000 })
  const eventsQ = useQuery({ queryKey: ['events', 200], queryFn: () => api.events(200), refetchInterval: 5000 })
  const workloads = useMemo(() => workloadsQ.data ?? [], [workloadsQ.data])
  const nodes = useMemo(() => deriveNodes(nodesQ.data ?? [], workloads), [nodesQ.data, workloads])
  const tiles = tileCounts(nodes, workloads)
  const attention = deriveAttention(nodes, workloads)
  const projects = deriveProjects(workloads)
  const latest = (eventsQ.data ?? []).slice(0, 6)
  const loading = nodesQ.isPending || workloadsQ.isPending

  return (
    <main className="mx-auto flex max-w-[1180px] flex-col gap-6 p-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="m-0 text-2xl font-semibold tracking-tight">Dashboard</h1>
          <p className="m-0 mt-1 text-muted-foreground">
            {loading ? 'Loading…' : attention.length ? `${attention.length} ${attention.length === 1 ? 'thing needs' : 'things need'} attention.` : 'Everything is running.'}
          </p>
        </div>
        <Button asChild>
          <Link to="/deploy">New deploy</Link>
        </Button>
      </div>

      <section aria-label="Summary" className="grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-4">
        <Tile label="Schedulable nodes" value={tiles.schedulable} of={tiles.nodes} note="Taking new workloads" to="/nodes" />
        <Tile label="Workloads ready" value={tiles.ready} of={tiles.running} note="Of those meant to run" to="/projects" />
        <Tile label="Failed" value={tiles.failed} tone="bad" note={tiles.failed ? 'See Needs attention' : 'None'} to="/events" />
        <Tile label="In progress" value={tiles.inProgress} tone="warn" note="Building, starting or waiting" to="/projects" />
      </section>

      <div className="flex flex-wrap items-start gap-6">
        <section aria-label="Needs attention" className="flex min-w-0 flex-[1_1_480px] flex-col gap-3">
          <h2 className="m-0 text-base font-semibold">Needs attention</h2>
          <Card className="overflow-hidden">
            {attention.length === 0 ? (
              <div className="flex items-center gap-3 px-4 py-6 text-muted-foreground">
                <CheckCircle2 className="size-5 text-ok" aria-hidden="true" />
                {loading ? 'Loading…' : 'Nothing needs attention.'}
              </div>
            ) : (
              <ul className="m-0 list-none p-0">
                {attention.map((a) => (
                  <li key={a.key} className="border-b border-muted last:border-b-0">
                    <Link to={a.to} className="flex items-start gap-3 px-4 py-3 no-underline hover:bg-muted/40">
                      <span aria-hidden="true" className={cn('mt-1.5 size-2 flex-none rounded-full', DOT[a.tone])} />
                      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                        <span className="font-mono text-sm font-medium text-foreground">{a.title}</span>
                        <span className="text-[13px] text-muted-foreground [overflow-wrap:anywhere]">{a.detail}</span>
                      </span>
                      <ChevronRight className="mt-1 size-4 flex-none text-muted-foreground" aria-hidden="true" />
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <div className="mt-3 flex items-end justify-between">
            <h2 className="m-0 text-base font-semibold">Projects</h2>
            <Link to="/projects" className="text-[13px] underline">All projects</Link>
          </div>
          <Card className="overflow-hidden">
            <ul className="m-0 list-none p-0">
              {projects.map((p) => (
                <li key={p.name} className="border-b border-muted last:border-b-0">
                  <Link to={`/projects/${encodeURIComponent(p.name)}`} className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-4 py-3 no-underline hover:bg-muted/40">
                    <span className="font-mono text-sm font-medium text-foreground">{p.name}</span>
                    <span className="text-[13px] text-muted-foreground">{p.services} services</span>
                    <span className="ml-auto flex flex-wrap gap-1.5">
                      {p.statusCounts.map((s) => (
                        <Badge key={s.status} tone={statusTone(s.status)}>{s.count} {s.status.replace(/_/g, ' ')}</Badge>
                      ))}
                    </span>
                  </Link>
                </li>
              ))}
              {projects.length === 0 && <li className="px-4 py-6 text-center text-[13px] text-muted-foreground">{loading ? 'Loading…' : 'No projects running.'}</li>}
            </ul>
          </Card>
        </section>

        <section aria-label="Latest events" className="flex min-w-0 flex-[1_1_320px] flex-col gap-3">
          <div className="flex items-end justify-between">
            <h2 className="m-0 text-base font-semibold">Latest events</h2>
            <Link to="/events" className="text-[13px] underline">View all</Link>
          </div>
          <Card>
            <ul className="m-0 list-none p-0">
              {latest.map((e) => (
                <li key={e.ID} className="flex flex-col gap-1 border-b border-muted px-4 py-3 last:border-b-0">
                  <div className="flex flex-wrap items-center gap-2 text-xs">
                    <Badge tone={eventTone(e)} className="rounded">{e.Type}</Badge>
                    <span className="font-mono text-muted-foreground">{sinceLabel(e.CreatedAt)}</span>
                  </div>
                  <span className="truncate font-mono text-xs text-muted-foreground">{e.WorkloadKey || e.NodeID}</span>
                  <span className="text-[13px] [overflow-wrap:anywhere]">{e.Message}</span>
                </li>
              ))}
              {latest.length === 0 && (
                <li className="flex items-center gap-2 px-4 py-6 text-[13px] text-muted-foreground">
                  <AlertTriangle className="size-4" aria-hidden="true" />
                  {eventsQ.isPending ? 'Loading…' : 'No events yet.'}
                </li>
              )}
            </ul>
          </Card>
        </section>
      </div>
    </main>
  )
}
