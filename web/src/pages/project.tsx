import { useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { summarizeCompose } from '@/lib/compose-summary'
import { defaultConfig } from '@/lib/deploy-config'
import { buildRequest } from '@/lib/plan'
import { loadComposeSource } from '@/lib/compose-source'
import { VolumeRiskDialog } from '@/components/deploy/volume-risk-dialog'
import { AddServiceDialog } from '@/components/project/add-service-dialog'
import { RemoveServiceDialog } from '@/components/project/remove-service-dialog'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { ChevronDown } from 'lucide-react'
import { keepVolumePlacements, volumeRisks, type VolumeRisk } from '@/lib/volume-risk'
import { DependencyGraph } from '@/components/deploy/dependency-graph'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { api } from '@/lib/api'
import { eventTone, sinceLabel, statusTone } from '@/lib/cluster'
import { blockedSummary, deriveProject, graphNodesOf, projectEvents } from '@/lib/project'
import { formatMem } from '@/lib/memory'
import { requestLabel } from '@/lib/plan'
import { deployLink, loadDeploy } from '@/lib/saved-deploys'

function Tile({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <Card className="flex flex-col gap-1 p-4">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="text-2xl font-semibold">{value}</span>
      {note && <span className="truncate text-[13px] text-muted-foreground">{note}</span>}
    </Card>
  )
}

export function ProjectPage() {
  const { name = '' } = useParams()
  const [revision, setRevision] = useState<string>()
  const workloadsQ = useQuery({ queryKey: ['workloads'], queryFn: api.workloads, refetchInterval: 3000 })
  const eventsQ = useQuery({ queryKey: ['events', 200], queryFn: () => api.events(200), refetchInterval: 5000 })

  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [confirm, setConfirm] = useState<'stop' | 'remove' | 'redeploy' | 'fresh' | 'start' | 'restart' | 'rebuild' | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)
  const [removingServices, setRemovingServices] = useState(false)
  const done = (message: string) => {
    setConfirm(null)
    setNotice(message)
    void queryClient.invalidateQueries({ queryKey: ['workloads'] })
  }

  // Redeploy straight from the Compose file and settings this browser saved
  // at the last deploy. Normal keeps services where they run; fresh lets the
  // scheduler place them again.
  // pins: extra service -> node placements (from "Replan keeping volumes").
  // force: deploy even though services would start on empty volumes.
  type RedeployInput = { fresh: boolean; pins?: Record<string, string>; force?: boolean }
  const [risks, setRisks] = useState<{ list: VolumeRisk[]; input: RedeployInput } | null>(null)
  const redeploy = useMutation({
    mutationFn: async (input: RedeployInput): Promise<VolumeRisk[]> => {
      const saved = loadDeploy(name)
      const source = await loadComposeSource(name)
      if (!saved || !source) throw new Error('No saved Compose file for this project. Use Edit configuration to upload it.')
      const parsed = summarizeCompose(source.text)
      if (!parsed.ok) throw new Error(parsed.error)
      const base = { ...defaultConfig(parsed.summary), ...saved.config }
      const config = { ...base, fresh: input.fresh, placements: { ...(input.fresh ? {} : base.placements), ...input.pins } }
      const req = buildRequest(source.text, config, true)
      if (!input.force) {
        const [planned, current] = await Promise.all([api.composePlan(req), api.workloads()])
        const list = volumeRisks(planned, current)
        if (list.length) return list
      }
      await api.composeDeploy(req)
      return []
    },
    onSuccess: (list, input) => {
      if (list.length) {
        setConfirm(null)
        setRisks({ list, input })
        return
      }
      setRisks(null)
      done(input.fresh ? 'Fresh redeploy submitted. Services are being placed again.' : 'Redeploy submitted. Services update on the nodes they run on.')
    },
  })
  // Rebuild every image in place: read the Compose file from disk, build
  // without the cache and recreate those containers on the nodes they run on.
  // Every deployed service is pinned to its current node, so nothing moves.
  const rebuild = useMutation({
    mutationFn: async () => {
      const saved = loadDeploy(name)
      const source = await loadComposeSource(name)
      if (!saved || !source) throw new Error('No saved Compose file for this project. Use Edit configuration to upload it.')
      const parsed = summarizeCompose(source.text)
      if (!parsed.ok) throw new Error(parsed.error)
      if (parsed.summary.builds === 0) throw new Error('No service in the Compose file has a build: section, so there is nothing to rebuild.')
      const here = (workloadsQ.data ?? []).filter((w) => w.Project === name)
      const config = { ...defaultConfig(parsed.summary), ...saved.config, project: name, scope: 'all' as const, noCache: true, fresh: false, forced: [], placements: Object.fromEntries(here.map((w) => [w.Service, w.NodeID])) }
      // Services with a prebuilt image have nothing to rebuild: leave them running as they are.
      const prebuilt = parsed.summary.details.filter((d) => !d.build).map((d) => d.name)
      await api.composeDeploy({ ...buildRequest(source.text, config, true), keep_running: prebuilt })
      return parsed.summary.builds
    },
    onSuccess: (n) => done(`Rebuilding ${n} ${n === 1 ? 'service' : 'services'} without the cache. They restart where they run as each build finishes.`),
  })
  // Start or restart every matching service; one failure does not stop the rest.
  const bulk = useMutation({
    mutationFn: async (action: 'start' | 'restart') => {
      const mine = (workloadsQ.data ?? []).filter((w) => w.Project === name)
      const targets = mine.filter((w) => (action === 'start' ? w.Status === 'stopped' || w.Status === 'failed' : w.DesiredState === 'running' && w.Status !== 'stopped'))
      const results = await Promise.allSettled(targets.map((w) => api.workloadAction(w.Project, w.Service, w.Revision, action)))
      const errors = results.flatMap((r, i) => (r.status === 'rejected' ? [`${targets[i].Service}: ${(r.reason as Error).message}`] : []))
      if (errors.length) throw new Error(errors.join('\n'))
      return targets.length
    },
    onSuccess: (n, action) => done(`${action === 'start' ? 'Starting' : 'Restarting'} ${n} ${n === 1 ? 'service' : 'services'}.`),
  })
  const stopAll = useMutation({
    mutationFn: async () => {
      const running = (workloadsQ.data ?? []).filter((w) => w.Project === name && w.DesiredState === 'running')
      const results = await Promise.allSettled(running.map((w) => api.stopWorkload(w.Project, w.Service, w.Revision)))
      const failed = results.filter((r) => r.status === 'rejected').length
      if (failed > 0) throw new Error(`${failed} of ${running.length} services could not be stopped.`)
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['workloads'] }),
    onSuccess: () => setConfirm(null),
  })
  const remove = useMutation({
    mutationFn: () => api.removeProject(name),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['workloads'] })
      navigate('/projects')
    },
  })

  const project = useMemo(() => deriveProject(name, workloadsQ.data ?? [], revision), [name, workloadsQ.data, revision])
  const graph = useMemo(() => (project ? graphNodesOf(project) : []), [project])
  const events = useMemo(() => projectEvents(name, eventsQ.data ?? []), [name, eventsQ.data])

  const crumbs = (
    <nav aria-label="Breadcrumb" className="text-[13px] text-muted-foreground">
      <Link to="/projects" className="underline">
        Projects
      </Link>{' '}
      / <span className="font-mono text-foreground">{name}</span>
    </nav>
  )

  if (!project) {
    return (
      <main className="mx-auto flex max-w-[1180px] flex-col gap-4 p-6">
        {crumbs}
        <Card className="p-6">
          <h1 className="m-0 text-lg font-semibold">{workloadsQ.isPending ? 'Loading…' : 'Project not found'}</h1>
          {!workloadsQ.isPending && (
            <p className="m-0 mt-1 text-muted-foreground">
              Nothing is deployed under <span className="font-mono">{name}</span>. <Link to="/projects" className="underline">Back to projects</Link>.
            </p>
          )}
        </Card>
      </main>
    )
  }

  const sameRevision = project.revisions.length === 1
  const saved = loadDeploy(project.name)
  const stoppedCount = project.services.filter((s) => s.status === 'stopped' || s.status === 'failed').length
  return (
    <main className="mx-auto flex max-w-[1180px] flex-col gap-6 p-6">
      <div className="flex flex-col gap-3">
        {crumbs}
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div className="flex flex-col gap-2">
            <h1 className="m-0 font-mono text-2xl font-semibold tracking-tight">{project.name}</h1>
            <div className="flex flex-wrap items-center gap-1.5">
              {project.statusCounts.map((s) => (
                <Badge key={s.status} tone={statusTone(s.status)}>
                  {s.count} {s.status.replace(/_/g, ' ')}
                </Badge>
              ))}
              {project.allStopped && <Badge tone="off">stopped</Badge>}
              {!sameRevision && (
                <label className="ml-2 flex items-center gap-1.5 text-[13px] text-muted-foreground">
                  Revision
                  <select
                    value={project.revision}
                    onChange={(e) => setRevision(e.target.value)}
                    className="min-h-11 rounded-md border border-zinc-300 bg-card px-2 font-mono text-foreground"
                  >
                    {project.revisions.map((r) => (
                      <option key={r} value={r}>
                        {r}
                      </option>
                    ))}
                  </select>
                </label>
              )}
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
          <Button
            disabled={!saved || redeploy.isPending}
            title={saved ? 'Deploy the saved Compose file again, keeping services where they run' : 'No saved Compose file in this browser'}
            onClick={() => { redeploy.reset(); setConfirm('redeploy') }}
          >
            {redeploy.isPending ? 'Redeploying…' : 'Redeploy'}
          </Button>
          <Button variant="outline" className="text-bad-fg" onClick={() => { remove.reset(); setConfirm('remove') }}>
            Remove project
          </Button>
          <DropdownMenu modal={false}>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" className="gap-1.5">
                Actions
                <ChevronDown className="size-4" aria-hidden="true" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-80">
              <DropdownMenuLabel>Run</DropdownMenuLabel>
              {stoppedCount > 0 ? (
                <DropdownMenuItem onSelect={() => { bulk.reset(); setConfirm('start') }}>
                  <span className="font-medium">Start {stoppedCount === project.services.length ? 'all' : stoppedCount} stopped</span>
                  <span className="text-[13px] text-muted-foreground">Each starts again on the node it last ran on</span>
                </DropdownMenuItem>
              ) : (
                <DropdownMenuItem onSelect={() => { bulk.reset(); setConfirm('restart') }}>
                  <span className="font-medium">Restart all</span>
                  <span className="text-[13px] text-muted-foreground">Recreates every container where it runs</span>
                </DropdownMenuItem>
              )}
              <DropdownMenuItem disabled={project.allStopped} onSelect={() => { stopAll.reset(); setConfirm('stop') }}>
                <span className="font-medium">Stop all</span>
                <span className="text-[13px] text-muted-foreground">Containers stop; Start brings them back</span>
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuLabel>Services</DropdownMenuLabel>
              <DropdownMenuItem disabled={!saved} onSelect={() => setAdding(true)}>
                <span className="font-medium">Add service…</span>
                <span className="text-[13px] text-muted-foreground">{saved ? 'From the saved Compose file; the others are not touched' : 'Needs the saved Compose file'}</span>
              </DropdownMenuItem>
              <DropdownMenuItem danger onSelect={() => setRemovingServices(true)}>
                <span className="font-medium">Remove service…</span>
                <span className="text-[13px] text-muted-foreground">Also removes the services that depend on it</span>
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuLabel>Deploy</DropdownMenuLabel>
              <DropdownMenuItem asChild>
                <Link to={deployLink(project.name, 'edit')}>
                  <span className="font-medium">Edit configuration</span>
                  <span className="text-[13px] text-muted-foreground">Change the Compose file or settings, then plan</span>
                </Link>
              </DropdownMenuItem>
              <DropdownMenuItem disabled={!saved || rebuild.isPending} onSelect={() => { rebuild.reset(); setConfirm('rebuild') }}>
                <span className="font-medium">{rebuild.isPending ? 'Rebuilding…' : 'Rebuild images…'}</span>
                <span className="text-[13px] text-muted-foreground">{saved ? 'Rebuild every build: service without the cache, where it runs' : 'Needs the saved Compose file'}</span>
              </DropdownMenuItem>
              <DropdownMenuItem disabled={!saved || redeploy.isPending} onSelect={() => { redeploy.reset(); setConfirm('fresh') }}>
                <span className="font-medium">Redeploy fresh</span>
                <span className="text-[13px] text-muted-foreground">Place every service again. Moved volumes start empty.</span>
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          </div>
        </div>
      </div>

      <ConfirmDialog
        open={confirm === 'stop'}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={`Stop all services in ${project.name}?`}
        confirmLabel="Stop all"
        pending={stopAll.isPending}
        error={stopAll.error?.message ?? null}
        onConfirm={() => stopAll.mutate()}
      >
        Containers are stopped and removed. The services stay listed as stopped; Start brings them back on the same nodes.
      </ConfirmDialog>
      <ConfirmDialog open={confirm === 'redeploy' || confirm === 'fresh'} onOpenChange={(o) => !o && setConfirm(null)} danger={confirm === 'fresh'} title={confirm === 'fresh' ? `Redeploy ${project.name} fresh?` : `Redeploy ${project.name}?`} confirmLabel={confirm === 'fresh' ? 'Redeploy fresh' : 'Redeploy'} pending={redeploy.isPending} error={redeploy.error?.message ?? null} onConfirm={() => redeploy.mutate({ fresh: confirm === 'fresh' })}>
        {confirm === 'fresh'
          ? 'Every service is placed again from scratch and may move to another node. A service with a volume that moves starts with an empty volume.'
          : `Deploys ${saved?.file.name ?? 'the saved Compose file'} again with its saved settings. Services stay on the nodes they run on; images build again as in any deploy.`}
      </ConfirmDialog>
      <AddServiceDialog
        project={project.name}
        saved={saved}
        workloads={(workloadsQ.data ?? []).filter((w) => w.Project === project.name)}
        open={adding}
        onOpenChange={setAdding}
        onAdded={(message) => { setAdding(false); setNotice(message) }}
      />
      <RemoveServiceDialog
        project={project.name}
        workloads={(workloadsQ.data ?? []).filter((w) => w.Project === project.name)}
        open={removingServices}
        onOpenChange={setRemovingServices}
        onRemoved={(message) => { setRemovingServices(false); setNotice(message) }}
      />
      <ConfirmDialog open={confirm === 'rebuild'} onOpenChange={(o) => !o && setConfirm(null)} danger={false} title={`Rebuild ${project.name}?`} confirmLabel="Rebuild" pending={rebuild.isPending} error={rebuild.error?.message ?? null} onConfirm={() => rebuild.mutate()}>
        Reads {saved?.file.name ?? 'the Compose file'} from the project folder again, rebuilds every service that has a build: section without the build cache, and recreates those containers on the nodes they already run on. Services with a prebuilt image are not touched. Each service is briefly down while its container is recreated.
      </ConfirmDialog>
      <VolumeRiskDialog
        risks={risks?.list ?? []}
        open={!!risks}
        onOpenChange={(o) => !o && setRisks(null)}
        pending={redeploy.isPending}
        error={risks ? (redeploy.error?.message ?? null) : null}
        onReplan={() => redeploy.mutate({ ...risks!.input, pins: keepVolumePlacements(risks!.list, risks!.input.pins ?? {}) })}
        onDeployAnyway={() => redeploy.mutate({ ...risks!.input, force: true })}
        replanLabel="Keep volumes and redeploy"
      />
      <ConfirmDialog open={confirm === 'start' || confirm === 'restart'} onOpenChange={(o) => !o && setConfirm(null)} danger={false} title={confirm === 'start' ? `Start ${stoppedCount} stopped ${stoppedCount === 1 ? 'service' : 'services'}?` : `Restart all services in ${project.name}?`} confirmLabel={confirm === 'start' ? 'Start' : 'Restart'} pending={bulk.isPending} error={bulk.error?.message ?? null} onConfirm={() => bulk.mutate(confirm === 'start' ? 'start' : 'restart')}>
        {confirm === 'start'
          ? 'Each starts again on the node it last ran on, with its data and image still there. No rebuild. A service whose port or capacity was taken meanwhile is refused; redeploy that one.'
          : 'Each container is recreated on the node it runs on. No rebuild and no rescheduling.'}
      </ConfirmDialog>
      <ConfirmDialog
        open={confirm === 'remove'}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={`Remove ${project.name}?`}
        confirmLabel="Remove project"
        pending={remove.isPending}
        error={remove.error?.message ?? null}
        onConfirm={() => remove.mutate()}
      >
        This deletes all {project.services.length} services of this project from the control plane. Running containers are removed by their nodes shortly after. It cannot be undone.
      </ConfirmDialog>

      {!saved && (
        <p role="status" className="m-0 rounded-lg border border-border bg-card px-3.5 py-2.5 text-[13px] text-muted-foreground">
          This browser has no saved Compose file for this project, so Redeploy is off. Use Edit configuration to upload it; after that deploy, Redeploy works here.
        </p>
      )}
      {notice && (
        <p role="status" className="m-0 flex items-center justify-between gap-3 rounded-lg border border-ok/30 bg-ok-soft px-3.5 py-2.5 text-[13px] text-ok-fg">
          {notice}
          <button type="button" className="underline" onClick={() => setNotice(null)}>Dismiss</button>
        </p>
      )}

      <section aria-label="Summary" className="grid grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-4">
        <Tile label="Services" value={String(project.services.length)} note={sameRevision ? `revision ${project.revision}` : `revision ${project.revision} of ${project.revisions.length}`} />
        <Tile label="Reserved" value={`${project.cpuCores.toFixed(1)} cores`} note={requestLabel({ cpuCores: project.cpuCores, memGiB: project.memGiB }).split(' · ')[1]} />
        <Tile label="RAM in use" value={formatMem(project.memLiveGiB)} note={project.memLiveGiB === null ? 'no recent report' : 'live, across all its containers'} />
        <Tile label="Nodes in use" value={String(project.nodes.length)} note={project.nodes.join(', ') || 'none placed'} />
      </section>

      <section aria-label="Services" className="flex flex-col gap-3">
        <h2 className="m-0 text-base font-semibold">Services</h2>
        <Card className="overflow-x-auto">
          <table className="w-full min-w-[900px] border-collapse">
            <thead>
              <tr className="text-left text-xs text-muted-foreground">
                <th className="px-4 py-3 font-medium">Service</th>
                <th className="px-4 py-3 font-medium">Status</th>
                <th className="px-4 py-3 font-medium">Node</th>
                <th className="px-4 py-3 font-medium">Reserved</th>
                <th className="px-4 py-3 font-medium">RAM used</th>
                <th className="px-4 py-3 font-medium">Waits for</th>
              </tr>
            </thead>
            <tbody>
              {project.services.map((s) => (
                <tr key={s.name} className="border-t border-muted align-top">
                  <td className="px-4 py-3.5 font-mono font-medium">
                    <Link to={`/projects/${encodeURIComponent(project.name)}/services/${encodeURIComponent(s.name)}`} className="underline">
                      {s.name}
                    </Link>
                  </td>
                  <td className="px-4 py-3.5">
                    <Badge tone={statusTone(s.status)}>{s.status.replace(/_/g, ' ')}</Badge>
                    {s.status === 'failed' && s.reason && <div className="mt-1 max-w-[280px] text-xs text-bad-fg">{s.reason}</div>}
                  </td>
                  <td className="px-4 py-3.5 font-mono">{s.node || <span className="text-muted-foreground">unplaced</span>}</td>
                  <td className="whitespace-nowrap px-4 py-3.5 font-mono text-xs">{requestLabel(s)}</td>
                  <td className="whitespace-nowrap px-4 py-3.5 font-mono text-xs">{formatMem(s.memLiveGiB)}</td>
                  <td className="min-w-[260px] px-4 py-3.5 text-[13px]">
                    {s.waitsFor.length ? s.waitsFor.map((d) => `${d.name} (${d.healthy ? 'healthy' : 'started'})`).join(', ') : <span className="text-muted-foreground">nothing</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      </section>

      <section aria-label="Dependencies" className="flex flex-col gap-3">
        <div>
          <h2 className="m-0 text-base font-semibold">Dependencies</h2>
          <p className="m-0 mt-0.5 text-[13px] text-muted-foreground">{blockedSummary(project)}</p>
        </div>
        <DependencyGraph nodes={graph} />
      </section>

      <section aria-label="Project events" className="flex flex-col gap-3">
        <div className="flex items-end justify-between gap-4">
          <h2 className="m-0 text-base font-semibold">Events</h2>
          <Link to="/events" className="text-[13px] underline">
            All events
          </Link>
        </div>
        <Card>
          <ul className="m-0 list-none p-0">
            {events.map(({ event: e, count }) => (
              <li key={e.ID} className="flex flex-col gap-1 border-b border-muted px-4 py-3 last:border-b-0">
                <div className="flex flex-wrap items-center gap-2 text-xs">
                  <Badge tone={eventTone(e)} className="rounded">
                    {e.Type}
                  </Badge>
                  <span className="font-mono text-muted-foreground">{sinceLabel(e.CreatedAt)}</span>
                  <span className="font-mono text-muted-foreground">{e.WorkloadKey}</span>
                  {count > 1 && <span className="font-mono text-muted-foreground">×{count}</span>}
                </div>
                <span>{e.Message}</span>
              </li>
            ))}
            {events.length === 0 && <li className="px-4 py-6 text-center text-muted-foreground">{eventsQ.isPending ? 'Loading…' : 'No events for this project yet.'}</li>}
          </ul>
        </Card>
      </section>
    </main>
  )
}
