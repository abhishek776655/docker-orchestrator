import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { deriveNodes, deriveProjects, eventTone, sinceLabel, statusTone } from '@/lib/cluster'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { AddNodeDialog } from '@/components/add-node-dialog'
import { Topology } from '@/components/topology/topology'

const POLL_MS = 3000

export function ClusterPage() {
  const [adding, setAdding] = useState(false)
  const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: api.nodes, refetchInterval: POLL_MS })
  const workloadsQ = useQuery({ queryKey: ['workloads'], queryFn: api.workloads, refetchInterval: POLL_MS })
  const eventsQ = useQuery({ queryKey: ['events'], queryFn: () => api.events(50), refetchInterval: 5000 })

  const nodes = useMemo(() => deriveNodes(nodesQ.data ?? [], workloadsQ.data ?? []), [nodesQ.data, workloadsQ.data])
  const projects = useMemo(() => deriveProjects(workloadsQ.data ?? []), [workloadsQ.data])

  const schedulable = nodes.filter((n) => n.state === 'available' || n.state === 'throttling').length
  const workloads = (workloadsQ.data ?? []).filter((w) => w.DesiredState === 'running')
  const failed = workloads.filter((w) => w.Status === 'failed').length

  return (
    <main className="mx-auto flex max-w-[1180px] flex-col gap-6 p-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="m-0 text-2xl font-semibold tracking-tight">Cluster topology</h1>
          <p className="m-0 mt-1 text-muted-foreground">
            {nodes.length} nodes · {schedulable} schedulable · {workloads.length} workloads · {failed} failed. Hover a machine for
            details, click to open it.
          </p>
        </div>
        <Button asChild>
          <Link to="/deploy">New deploy</Link>
        </Button>
      </div>

      {nodesQ.isError && (
        <p role="alert" className="m-0 rounded-md border border-bad/30 bg-bad-soft p-3 text-sm text-bad-fg">
          Cannot reach the control plane: {nodesQ.error.message}. Is it running on port 8099?
        </p>
      )}

      <Card className="overflow-hidden">
        <div className="flex flex-wrap items-center justify-between gap-x-5 gap-y-2 border-b border-border px-4 py-3 text-xs text-muted-foreground">
          <span className="text-sm font-medium text-foreground">Control plane to agents</span>
          <span className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
            <Legend color="bg-ok" label="available" />
            <Legend color="bg-warn" label="throttling" />
            <Legend color="bg-off" label="cordoned" />
            <Legend color="bg-bad" label="no heartbeat" />
          </span>
        </div>
        <Topology nodes={nodes} onAddNode={() => setAdding(true)} />
      </Card>

      <div className="flex flex-wrap items-start gap-6">
        <section className="flex min-w-0 flex-[999_1_560px] flex-col gap-3">
          <h2 className="m-0 text-base font-semibold">Projects</h2>
          <Card className="overflow-x-auto">
            <table className="w-full min-w-[480px] border-collapse">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  <th className="px-4 py-3 font-medium">Project</th>
                  <th className="px-4 py-3 font-medium">Services</th>
                  <th className="px-4 py-3 font-medium">Status</th>
                  <th className="px-4 py-3 font-medium">Revision</th>
                </tr>
              </thead>
              <tbody>
                {projects.map((p) => (
                  <tr key={p.name} className="border-t border-muted">
                    <td className="px-4 py-3.5 font-mono font-medium">{p.name}</td>
                    <td className="px-4 py-3.5">{p.services}</td>
                    <td className="px-4 py-3.5">
                      <div className="flex flex-wrap gap-1.5">
                        {p.statusCounts.map((s) => (
                          <Badge key={s.status} tone={statusTone(s.status)}>
                            {s.count} {s.status}
                          </Badge>
                        ))}
                      </div>
                    </td>
                    <td className="px-4 py-3.5 font-mono">{p.revisions.join(', ')}</td>
                  </tr>
                ))}
                {projects.length === 0 && (
                  <tr className="border-t border-muted">
                    <td colSpan={4} className="px-4 py-6 text-center text-muted-foreground">
                      {workloadsQ.isPending ? 'Loading…' : 'No projects deployed yet.'}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </Card>
        </section>

        <section className="flex min-w-0 flex-[1_1_340px] flex-col gap-3">
          <h2 className="m-0 text-base font-semibold">Recent events</h2>
          <Card>
            <ul
              className="m-0 max-h-[420px] list-none overflow-y-auto p-0 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-primary"
              tabIndex={0}
              aria-label="Recent events, scrollable"
            >
              {(eventsQ.data ?? []).map((e) => (
                <li key={e.ID} className="flex flex-col gap-1 border-b border-muted px-4 py-3 last:border-b-0">
                  <div className="flex items-center gap-2 text-xs">
                    <Badge tone={eventTone(e)} className="rounded">
                      {e.Type}
                    </Badge>
                    <span className="font-mono text-muted-foreground">{sinceLabel(e.CreatedAt)}</span>
                  </div>
                  <span>{e.Message}</span>
                </li>
              ))}
              {(eventsQ.data ?? []).length === 0 && (
                <li className="px-4 py-6 text-center text-muted-foreground">{eventsQ.isPending ? 'Loading…' : 'No events yet.'}</li>
              )}
            </ul>
          </Card>
        </section>
      </div>

      <AddNodeDialog open={adding} onOpenChange={setAdding} />
    </main>
  )
}

function Legend({ color, label }: { color: string; label: string }) {
  return (
    <span className="flex items-center gap-1.5">
      <span aria-hidden="true" className={`size-2 rounded-full ${color}`} />
      {label}
    </span>
  )
}
