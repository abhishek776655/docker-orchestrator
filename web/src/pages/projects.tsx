import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { deriveProjects, statusTone } from '@/lib/cluster'
import { formatMem } from '@/lib/memory'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'

export function ProjectsPage() {
  const workloadsQ = useQuery({ queryKey: ['workloads'], queryFn: api.workloads, refetchInterval: 3000 })
  const projects = useMemo(() => deriveProjects(workloadsQ.data ?? []), [workloadsQ.data])
  const services = projects.reduce((sum, p) => sum + p.services, 0)

  return (
    <main className="mx-auto flex max-w-[1180px] flex-col gap-6 p-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="m-0 text-2xl font-semibold tracking-tight">Projects</h1>
          <p className="m-0 mt-1 text-muted-foreground">
            {projects.length} projects · {services} services
          </p>
        </div>
        <Button asChild>
          <Link to="/deploy">New deploy</Link>
        </Button>
      </div>

      <Card className="overflow-x-auto">
        <table className="w-full min-w-[480px] border-collapse">
          <thead>
            <tr className="text-left text-xs text-muted-foreground">
              <th className="px-4 py-3 font-medium">Project</th>
              <th className="px-4 py-3 font-medium">Services</th>
              <th className="px-4 py-3 font-medium">Status</th>
              <th className="px-4 py-3 font-medium">RAM used</th>
              <th className="px-4 py-3 font-medium">Revision</th>
            </tr>
          </thead>
          <tbody>
            {projects.map((p) => (
              <tr key={p.name} className="border-t border-muted">
                <td className="px-4 py-3.5 font-mono font-medium">
                  <Link to={`/projects/${encodeURIComponent(p.name)}`} className="underline">
                    {p.name}
                  </Link>
                </td>
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
                <td className="whitespace-nowrap px-4 py-3.5 font-mono text-xs">{formatMem(p.memLiveGiB)}</td>
                <td className="px-4 py-3.5 font-mono">{p.revisions.join(', ')}</td>
              </tr>
            ))}
            {projects.length === 0 && (
              <tr className="border-t border-muted">
                <td colSpan={5} className="px-4 py-6 text-center text-muted-foreground">
                  {workloadsQ.isPending ? 'Loading…' : 'No projects deployed yet.'}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Card>
    </main>
  )
}
