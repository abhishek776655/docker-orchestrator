import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Pager } from '@/components/pager'
import { Badge } from '@/components/ui/badge'
import { Card } from '@/components/ui/card'
import { api } from '@/lib/api'
import { eventTone, sinceLabel } from '@/lib/cluster'
import { paginate } from '@/lib/paginate'
import { cn } from '@/lib/utils'

type Filter = 'all' | 'bad' | 'warn'

const FILTERS: { id: Filter; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'bad', label: 'Failures' },
  { id: 'warn', label: 'Warnings' },
]

// The control plane returns the newest events first; 1000 is a few hours of
// history on a busy cluster, which is what the page is for.
const FETCH = 1000

function serviceLink(key: string) {
  const [project, service] = key.split('/')
  return project && service ? `/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}` : null
}

export function EventsPage() {
  const [filter, setFilter] = useState<Filter>('all')
  const [query, setQuery] = useState('')
  const [page, setPage] = useState(1)
  const [size, setSize] = useState(25)
  const eventsQ = useQuery({ queryKey: ['events', FETCH], queryFn: () => api.events(FETCH), refetchInterval: 5000 })
  const all = useMemo(() => eventsQ.data ?? [], [eventsQ.data])
  const q = query.trim().toLowerCase()
  const shown = useMemo(
    () => all.filter((e) => (filter === 'all' || eventTone(e) === filter) && (!q || `${e.WorkloadKey} ${e.NodeID} ${e.Type} ${e.Message}`.toLowerCase().includes(q))),
    [all, filter, q],
  )
  const current = paginate(shown, page, size)
  const reset = () => setPage(1)

  return (
    <main className="mx-auto flex max-w-[1180px] flex-col gap-6 p-6">
      <div>
        <h1 className="m-0 text-2xl font-semibold tracking-tight">Events</h1>
        <p className="m-0 mt-1 text-muted-foreground">The {all.length} newest events from the control plane and agents. Refreshes every 5 seconds.</p>
      </div>

      <Card className="overflow-hidden">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2.5 border-b border-border px-4 py-3">
          <input
            type="search"
            value={query}
            onChange={(e) => { setQuery(e.target.value); reset() }}
            placeholder="Search workloads, nodes or messages"
            aria-label="Search events"
            className="min-h-11 min-w-0 flex-[1_1_260px] rounded-md border border-zinc-300 bg-card px-3"
          />
          <div role="group" aria-label="Filter events" className="flex flex-wrap gap-1.5">
            {FILTERS.map((f) => (
              <button
                key={f.id}
                type="button"
                aria-pressed={filter === f.id}
                onClick={() => { setFilter(f.id); reset() }}
                className={cn('min-h-11 rounded-full border px-3.5 text-[13px] font-medium', filter === f.id ? 'border-primary bg-primary text-primary-foreground' : 'border-zinc-300 bg-card text-zinc-700 hover:bg-muted')}
              >
                {f.label} <span className="font-mono opacity-80">{all.filter((e) => f.id === 'all' || eventTone(e) === f.id).length}</span>
              </button>
            ))}
          </div>
        </div>

        <div className="overflow-x-auto">
          <table className="w-full min-w-[820px] border-collapse">
            <thead>
              <tr className="text-left text-xs text-muted-foreground">
                {['When', 'Type', 'Workload', 'Node', 'Message'].map((h) => <th key={h} className="px-4 py-3 font-medium">{h}</th>)}
              </tr>
            </thead>
            <tbody>
              {current.items.map((e) => {
                const link = e.WorkloadKey ? serviceLink(e.WorkloadKey) : null
                return (
                  <tr key={e.ID} className="border-t border-muted align-top">
                    <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-muted-foreground" title={new Date(e.CreatedAt).toLocaleString()}>{sinceLabel(e.CreatedAt)}</td>
                    <td className="px-4 py-3"><Badge tone={eventTone(e)} className="rounded">{e.Type}</Badge></td>
                    <td className="px-4 py-3 font-mono text-[13px]">{link ? <Link to={link} className="underline">{e.WorkloadKey}</Link> : e.WorkloadKey || '—'}</td>
                    <td className="px-4 py-3 font-mono text-[13px]">{e.NodeID ? <Link to={`/nodes/${encodeURIComponent(e.NodeID)}`} className="underline">{e.NodeID}</Link> : '—'}</td>
                    <td className="px-4 py-3 text-[13px] [overflow-wrap:anywhere]">{e.Message}</td>
                  </tr>
                )
              })}
              {current.items.length === 0 && (
                <tr className="border-t border-muted">
                  <td colSpan={5} className="px-4 py-8 text-center text-muted-foreground">{eventsQ.isPending ? 'Loading…' : 'No events match.'}</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <Pager page={current} size={size} noun="events" onPage={setPage} onSize={(s) => { setSize(s); reset() }} />
      </Card>
    </main>
  )
}
