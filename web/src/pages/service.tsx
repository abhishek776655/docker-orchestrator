import { useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { api, type ApiNode } from '@/lib/api'
import { sinceLabel, statusTone } from '@/lib/cluster'
import { defaultConfig } from '@/lib/deploy-config'
import { candidateTags, derivePlan, requestLabel, buildRequest, tiedWithPick, type Candidate } from '@/lib/plan'
import { loadDeploy } from '@/lib/saved-deploys'
import { summarizeCompose } from '@/lib/compose-summary'
import { deriveService, lifecycle, type LifecycleStep } from '@/lib/service'
import { cn } from '@/lib/utils'

const MARK: Record<LifecycleStep['state'], { mark: string; cls: string }> = {
  done: { mark: '✓', cls: 'bg-ok-soft text-ok-fg' },
  now: { mark: '●', cls: 'bg-primary text-primary-foreground' },
  todo: { mark: '', cls: 'bg-muted text-muted-foreground' },
  bad: { mark: '!', cls: 'bg-bad-soft text-bad-fg' },
}

const mono = 'font-mono'
const barColor = (v: number) => (v >= 0.7 ? 'bg-ok' : v >= 0.4 ? 'bg-warn' : 'bg-bad')

function Section({ title, aside, children }: { title: string; aside?: React.ReactNode; children: React.ReactNode }) {
  return (
    <Card className="flex flex-col gap-3.5 p-5">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="m-0 text-base font-semibold">{title}</h2>
        {aside}
      </div>
      {children}
    </Card>
  )
}

function CandidateCard({ c, runsOn }: { c: Candidate; runsOn: string }) {
  return (
    <div className={cn('flex flex-col gap-2.5 rounded-lg border px-4 py-3.5', c.isPick ? 'border-ok' : 'border-border')}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="flex flex-wrap items-center gap-2">
          <span className={`${mono} font-medium`}>{c.node}</span>
          {candidateTags(c, runsOn).map((t) => (
            <Badge key={t.label} tone={t.tone}>{t.label}</Badge>
          ))}
        </span>
        {c.score !== undefined && <span className={`${mono} font-medium`}>total {c.score.toFixed(2)}</span>}
      </div>
      {c.rejectedBy === 'scheduler' && <p className="m-0 text-[13px] text-bad-fg">{c.detail}</p>}
      {c.rejectedBy === 'volume' && <p className="m-0 text-[13px] text-warn-fg">The scheduler accepts this node, but {c.detail}.</p>}
      {(c.factors ?? []).map((f) => (
        <div key={f.label} className="flex items-center gap-3 text-[13px]">
          <span className="w-24 flex-none text-muted-foreground">{f.label}</span>
          <div className="h-2 flex-1 overflow-hidden rounded bg-border">
            <div className={cn('h-2', barColor(f.value))} style={{ width: `${Math.round(f.value * 100)}%` }} />
          </div>
          <span className={`${mono} w-9 text-right`}>{f.value.toFixed(2)}</span>
        </div>
      ))}
    </div>
  )
}

function nodesAddress(nodes: ApiNode[], id: string): string {
  return nodes.find((n) => n.ID === id)?.Address ?? ''
}

export function ServicePage() {
  const { name = '', service = '' } = useParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [revision, setRevision] = useState<string>()
  const [confirm, setConfirm] = useState<'stop' | 'remove' | 'start' | 'restart' | 'volume' | null>(null)
  const [follow, setFollow] = useState(false)
  const [volumeNode, setVolumeNode] = useState('')
  const [volumeAck, setVolumeAck] = useState(false)
  const [volumeSent, setVolumeSent] = useState<string | null>(null)

  const workloadsQ = useQuery({ queryKey: ['workloads'], queryFn: api.workloads, refetchInterval: 3000 })
  const instancesQ = useQuery({ queryKey: ['instances', name], queryFn: () => api.instances(name), refetchInterval: 3000 })
  const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: api.nodes, refetchInterval: 5000 })
  const eventsQ = useQuery({ queryKey: ['events', 200], queryFn: () => api.events(200), refetchInterval: 5000 })

  const detail = useMemo(() => deriveService(name, service, workloadsQ.data ?? [], instancesQ.data ?? [], revision), [name, service, workloadsQ.data, instancesQ.data, revision])
  const key = detail ? `${name}/${service}/${detail.revision}` : ''
  const steps = useMemo(() => (detail ? lifecycle(key, detail.status, detail.reason, eventsQ.data ?? []) : []), [detail, key, eventsQ.data])

  const logsQ = useQuery({
    queryKey: ['logs', key],
    queryFn: () => api.logs(name, service, detail!.revision, 200),
    enabled: !!detail && detail.status !== 'pending',
    refetchInterval: follow ? 3000 : false,
    retry: false,
  })

  const stop = useMutation({
    mutationFn: () => api.stopWorkload(name, service, detail!.revision),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['workloads'] })
      setConfirm(null)
    },
  })
  const runAction = useMutation({
    mutationFn: (action: 'start' | 'restart') => api.workloadAction(name, service, detail!.revision, action),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['workloads'] })
      setConfirm(null)
    },
  })
  const removeVolume = useMutation({
    mutationFn: (node: string) => api.removeVolumes(node, [`${name}/${service}/${detail!.revision}`]),
    onSuccess: (_, node) => {
      setVolumeSent(`Asked ${node} to delete the volume data. The result shows in Events.`)
      setConfirm(null)
    },
  })
  const remove = useMutation({
    mutationFn: () => api.removeWorkload(name, service, detail!.revision),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['workloads'] })
      navigate(`/projects/${encodeURIComponent(name)}`)
    },
  })

  // Scores for every node right now, ignoring where it ran before. Needs the
  // Compose file, which only a deploy made from this browser has saved.
  const saved = loadDeploy(name)
  const compare = useMutation({
    mutationFn: async () => {
      const parsed = summarizeCompose(saved!.file.text)
      if (!parsed.ok) throw new Error(parsed.error)
      const config = { ...defaultConfig(parsed.summary), ...saved!.config, scope: 'one' as const, onlyServices: [service], placements: {}, fresh: true }
      const response = await api.composePlan(buildRequest(saved!.file.text, config, false))
      const row = derivePlan(response, null, config, workloadsQ.data ?? []).find((s) => s.name === service)
      if (!row) throw new Error(`${service} is not in the saved Compose file.`)
      return row
    },
  })

  const crumbs = (
    <nav aria-label="Breadcrumb" className="text-[13px] text-muted-foreground">
      <Link to="/projects" className="underline">Projects</Link> /{' '}
      <Link to={`/projects/${encodeURIComponent(name)}`} className={`${mono} underline`}>{name}</Link> / <span className={`${mono} text-foreground`}>{service}</span>
    </nav>
  )

  if (!detail) {
    return (
      <main className="mx-auto flex max-w-[1280px] flex-col gap-4 p-6">
        {crumbs}
        <Card className="p-6">
          <h1 className="m-0 text-lg font-semibold">{workloadsQ.isPending ? 'Loading…' : 'Service not found'}</h1>
          {!workloadsQ.isPending && (
            <p className="m-0 mt-1 text-muted-foreground">
              <span className={mono}>{name}/{service}</span> is not deployed. <Link to={`/projects/${encodeURIComponent(name)}`} className="underline">Back to {name}</Link>.
            </p>
          )}
        </Card>
      </main>
    )
  }

  const stopped = detail.desired === 'stopped'
  const stoppedHere = detail.status === 'stopped' || detail.status === 'failed'
  // The service's home page from outside: its node's address and first published port.
  const address = detail.instances[0]?.Address ?? nodesAddress(nodesQ.data ?? [], detail.node)
  const homeUrl = address && detail.ports[0] ? `http://${address}:${detail.ports[0].host}/` : ''
  const row = compare.data
  return (
    <main className="mx-auto flex max-w-[1280px] flex-col gap-6 p-6">
      <div className="flex flex-col gap-3">
        {crumbs}
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex flex-wrap items-center gap-3">
            <h1 className={`${mono} m-0 text-[28px] font-medium`}>{detail.name}</h1>
            <Badge tone={statusTone(detail.status)}>{detail.status.replace(/_/g, ' ')}</Badge>
            <span className={`${mono} text-[13px] text-muted-foreground`}>desired: {detail.desired}</span>
          </div>
          <div className="flex flex-wrap items-end gap-2">
            {detail.revisions.length > 1 && (
              <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                Revision
                <select value={detail.revision} onChange={(e) => setRevision(e.target.value)} className={`${mono} min-h-11 rounded-md border border-zinc-300 bg-card px-3 text-sm text-foreground`}>
                  {detail.revisions.map((r) => (
                    <option key={r.revision} value={r.revision}>
                      {r.revision}{r.stopped ? ' · stopped' : ''}
                    </option>
                  ))}
                </select>
              </label>
            )}
            {detail.status === 'stopped' || detail.status === 'failed' ? (
              <Button onClick={() => { runAction.reset(); setConfirm('start') }}>Start</Button>
            ) : (
              <Button variant="outline" onClick={() => { runAction.reset(); setConfirm('restart') }}>Restart</Button>
            )}
            <Button variant="outline" disabled={stopped} onClick={() => { stop.reset(); setConfirm('stop') }}>Stop</Button>
            <Button variant="outline" className="text-bad-fg" title="Delete this revision's record and container. Other revisions are not touched." onClick={() => { remove.reset(); setConfirm('remove') }}>Remove revision</Button>
          </div>
        </div>
        <p className="m-0 text-[13px] text-muted-foreground">
          A revision is one deployed version of this service, <span className={mono}>{detail.revision}</span> here. Stop keeps its record as stopped. Remove revision deletes the record and its container; other revisions stay.
        </p>
        {detail.status === 'failed' && detail.reason && (
          <p role="alert" className="m-0 rounded-md border border-bad/30 bg-bad-soft p-3 text-[13px] text-bad-fg">{detail.reason}</p>
        )}
      </div>

      <ConfirmDialog open={confirm === 'stop'} onOpenChange={(o) => !o && setConfirm(null)} title={`Stop ${service}?`} confirmLabel="Stop" pending={stop.isPending} error={stop.error?.message ?? null} onConfirm={() => stop.mutate()}>
        The container is stopped and removed. The revision stays listed as stopped until you Start it again. Services waiting for it will keep waiting.
      </ConfirmDialog>
      <ConfirmDialog open={confirm === 'start' || confirm === 'restart'} onOpenChange={(o) => !o && setConfirm(null)} danger={false} title={confirm === 'start' ? `Start ${service}?` : `Restart ${service}?`} confirmLabel={confirm === 'start' ? 'Start' : 'Restart'} pending={runAction.isPending} error={runAction.error?.message ?? null} onConfirm={() => runAction.mutate(confirm === 'start' ? 'start' : 'restart')}>
        {confirm === 'start'
          ? `Runs again on ${detail.node || 'its last node'}, with its data and image still there. No rebuild.`
          : `Its container on ${detail.node} is recreated. No rebuild, no move.`}
      </ConfirmDialog>
      <ConfirmDialog
        open={confirm === 'volume'}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={`Delete ${service}'s volume data?`}
        confirmLabel="Delete data"
        pending={removeVolume.isPending}
        error={removeVolume.error?.message ?? null}
        confirmDisabled={!volumeNode || !volumeAck || (volumeNode === detail.node && !stoppedHere)}
        onConfirm={() => removeVolume.mutate(volumeNode)}
      >
        <div className="flex flex-col gap-3 text-foreground">
          <label className="flex flex-col gap-1.5 text-[13px]">
            <span className="text-muted-foreground">On node</span>
            <select value={volumeNode} onChange={(e) => setVolumeNode(e.target.value)} className="min-h-11 rounded-md border border-zinc-300 bg-card px-2 font-mono text-sm">
              {(nodesQ.data ?? []).map((n) => (
                <option key={n.ID} value={n.ID}>
                  {n.DisplayName || n.ID}{n.ID === detail.dataNode ? ' · holds its data' : ''}{n.ID === detail.node ? ' · runs it' : ''}
                </option>
              ))}
            </select>
          </label>
          {volumeNode === detail.node && !stoppedHere && (
            <p className="m-0 rounded-md bg-warn-soft px-3 py-2 text-[13px] text-warn-fg">It is running on this node. Stop it first.</p>
          )}
          <p className="m-0 text-[13px] text-muted-foreground">
            Every volume of this revision on that node is deleted from disk. It cannot be undone. Next time it starts there, it starts empty.
          </p>
          <label className="flex items-start gap-2.5 text-sm">
            <input type="checkbox" className="mt-0.5 size-4" checked={volumeAck} onChange={(e) => setVolumeAck(e.target.checked)} />
            <span>I understand this data is gone for good.</span>
          </label>
        </div>
      </ConfirmDialog>
      <ConfirmDialog open={confirm === 'remove'} onOpenChange={(o) => !o && setConfirm(null)} title={`Remove ${service} ${detail.revision}?`} confirmLabel="Remove revision" pending={remove.isPending} error={remove.error?.message ?? null} onConfirm={() => remove.mutate()}>
        This deletes the record of this revision from the control plane. Its node removes the container shortly after. It cannot be undone.
      </ConfirmDialog>

      <section aria-label="Instances" className="flex flex-col gap-3">
        <h2 className="m-0 text-lg font-semibold">Instances</h2>
        <Card className="overflow-x-auto">
          <table className="w-full min-w-[640px] border-collapse">
            <thead>
              <tr className="text-left text-xs text-muted-foreground">
                {['Node', 'Address', 'Internal name', 'Published', 'Status'].map((h) => (
                  <th key={h} className="px-4 py-3 font-medium">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {detail.instances.map((i) => (
                <tr key={`${i.NodeID}:${i.ContainerPort}`} className="border-t border-muted">
                  <td className={`${mono} px-4 py-3.5 font-medium`}>{i.NodeID}</td>
                  <td className={`${mono} px-4 py-3.5 text-[13px]`}>{i.Address}</td>
                  <td className={`${mono} px-4 py-3.5 text-[13px]`}>{i.ContainerPort ? `${detail.name}:${i.ContainerPort}` : 'no port'}</td>
                  <td className={`${mono} px-4 py-3.5 text-[13px]`}>
                    {detail.ports.length ? detail.ports.map((p) => `${p.host} → ${p.container}`).join(', ') : 'none'}
                  </td>
                  <td className="px-4 py-3.5"><Badge tone={statusTone(i.Status)}>{i.Status}</Badge></td>
                </tr>
              ))}
              {detail.instances.length === 0 && (
                <tr className="border-t border-muted">
                  <td colSpan={5} className="px-4 py-6 text-center text-muted-foreground">
                    {detail.node ? <>Placed on <span className={mono}>{detail.node}</span>, not ready yet.</> : 'Not placed on a node yet.'}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </Card>
        {detail.internalName && (
          <p className="m-0 text-[13px] text-muted-foreground">
            Other services in this project reach it at <span className={mono}>{detail.internalName}</span> once an instance is ready.
          </p>
        )}
      </section>

      <div className="flex flex-wrap items-start gap-6">
        <div className="flex min-w-0 flex-[1_1_400px] flex-col gap-4">
          <Section title="Lifecycle">
            <ol className="m-0 flex list-none flex-col p-0">
              {steps.map((s) => (
                <li key={s.label} className="flex gap-3.5 border-t border-muted py-2.5 first:border-t-0">
                  <span aria-hidden="true" className={cn('flex size-[22px] flex-none items-center justify-center rounded-full text-xs', MARK[s.state].cls)}>{MARK[s.state].mark}</span>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap justify-between gap-x-3 gap-y-1">
                      <span className={cn(mono, s.state === 'now' && 'font-medium')}>{s.label.replace(/_/g, ' ')}</span>
                      <span className={`${mono} text-xs text-muted-foreground`}>{s.time ? sinceLabel(s.time) : ''}</span>
                    </div>
                    <div className="text-[13px] text-muted-foreground">{s.note}</div>
                  </div>
                </li>
              ))}
            </ol>
          </Section>

          <Section title="Health and dependencies">
            <dl className="m-0 grid grid-cols-[max-content_1fr] gap-x-5 gap-y-2 text-[13px]">
              <dt className="text-muted-foreground">Healthcheck</dt>
              <dd className={`${mono} m-0 [overflow-wrap:anywhere]`}>{detail.health?.command ?? 'none'}</dd>
              {homeUrl && (
                <>
                  <dt className="text-muted-foreground">Try it</dt>
                  <dd className={`${mono} m-0 [overflow-wrap:anywhere]`}>curl {homeUrl}</dd>
                </>
              )}
              {detail.health && (<><dt className="text-muted-foreground">Timing</dt><dd className={`${mono} m-0`}>{detail.health.timing}</dd></>)}
              <dt className="text-muted-foreground">Depends on</dt>
              <dd className="m-0">
                {detail.dependsOn.length
                  ? detail.dependsOn.map((d, i) => (
                      <span key={d.name}>
                        {i > 0 && ', '}
                        <Link to={`/projects/${encodeURIComponent(name)}/services/${encodeURIComponent(d.name)}`} className={`${mono} underline`}>{d.name}</Link>{' '}
                        ({d.healthy ? 'healthy' : 'started'})
                      </span>
                    ))
                  : 'nothing'}
              </dd>
              <dt className="text-muted-foreground">Needed by</dt>
              <dd className="m-0">
                {detail.neededBy.length
                  ? detail.neededBy.map((d, i) => (
                      <span key={d.name}>
                        {i > 0 && ', '}
                        <Link to={`/projects/${encodeURIComponent(name)}/services/${encodeURIComponent(d.name)}`} className={`${mono} underline`}>{d.name}</Link>{' '}
                        (once {d.healthy ? 'healthy' : 'started'}; {d.status.replace(/_/g, ' ')} now)
                      </span>
                    ))
                  : 'nothing'}
              </dd>
            </dl>
          </Section>
        </div>

        <div className="flex min-w-0 flex-[999_1_560px] flex-col gap-4">
          <Section title="Placement" aside={<span className="text-[13px] text-muted-foreground">Where it runs and why</span>}>
            <dl className="m-0 grid grid-cols-[max-content_1fr] gap-x-5 gap-y-2 text-[13px]">
              <dt className="text-muted-foreground">Runs on</dt>
              <dd className={`${mono} m-0 font-medium`}>{detail.node || 'not placed yet'}</dd>
              <dt className="text-muted-foreground">Reserves</dt>
              <dd className={`${mono} m-0`}>{requestLabel(detail)}</dd>
              <dt className="text-muted-foreground">Image</dt>
              <dd className={`${mono} m-0 [overflow-wrap:anywhere]`}>{detail.image}</dd>
            </dl>
            <div>
              <div className="mb-1.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">What the scheduler had to respect</div>
              {detail.placement.length ? (
                <ul className="m-0 flex list-none flex-wrap gap-1.5 p-0">
                  {detail.placement.map((f) => (
                    <li key={f.label + f.value} className={`${mono} rounded bg-muted px-2 py-1 text-xs text-zinc-700`}>
                      <span className="font-sans text-muted-foreground">{f.label}: </span>{f.value}
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="m-0 text-[13px] text-muted-foreground">Nothing. Any node with room could run it.</p>
              )}
            </div>

            {detail.persistent && (
              <div className="flex flex-wrap items-center justify-between gap-2 border-t border-muted pt-3.5">
                <div>
                  <div className="text-sm font-medium">Volume data</div>
                  <p className="m-0 text-[13px] text-muted-foreground">
                    Lives on <span className={mono}>{detail.dataNode || 'an unknown node'}</span>. Old copies can remain on nodes it ran on before.
                  </p>
                  {volumeSent && <p role="status" className="m-0 mt-1 text-[13px] text-ok-fg">{volumeSent}</p>}
                </div>
                <Button
                  variant="outline"
                  className="text-bad-fg"
                  onClick={() => {
                    removeVolume.reset()
                    setVolumeNode(detail.dataNode || detail.node)
                    setVolumeAck(false)
                    setConfirm('volume')
                  }}
                >
                  Delete volume data…
                </Button>
              </div>
            )}

            <div className="flex flex-col gap-3 border-t border-muted pt-3.5">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <div className="text-sm font-medium">Compare nodes now</div>
                  <p className="m-0 text-[13px] text-muted-foreground">
                    Scores every node as the cluster looks today, ignoring where this service ran before. Nothing is changed.
                  </p>
                </div>
                <Button variant="outline" disabled={!saved || compare.isPending} onClick={() => compare.mutate()}>
                  {compare.isPending ? 'Scoring…' : row ? 'Compare again' : 'Compare nodes'}
                </Button>
              </div>
              {!saved && (
                <p className="m-0 text-[13px] text-muted-foreground">
                  This needs the Compose file, and this browser has none saved for <span className={mono}>{name}</span>. Deploy it from the console once and it will be here.
                </p>
              )}
              {compare.error && <p role="alert" className="m-0 rounded-md border border-bad/30 bg-bad-soft p-3 text-[13px] text-bad-fg">{compare.error.message}</p>}
              {row && (
                <>
                  <p className="m-0 text-[13px]">
                    Today the scheduler would choose <span className={`${mono} font-medium`}>{row.pickNode || 'no node'}</span>
                    {row.pickNode && detail.node && row.pickNode !== detail.node ? <>, not <span className={mono}>{detail.node}</span>, where it runs now.</> : row.pickNode ? ', where it runs now.' : '.'}
                  </p>
                  {detail.placement.some((f) => f.label === 'Volume data lives on') && (
                    <p className="m-0 rounded-md bg-warn-soft px-3 py-2 text-[13px] text-warn-fg">
                      Its volume data lives on one node. Running it on another node would start with an empty volume.
                    </p>
                  )}
                  {tiedWithPick(row.candidates).length > 0 && (
                    <p className="m-0 text-[13px] text-muted-foreground">
                      That is a tie: <span className={mono}>{tiedWithPick(row.candidates).join(', ')}</span> scored the same. The scheduler breaks ties by node name, so the pick
                      is not a real preference.
                    </p>
                  )}
                  {row.candidates.length === 0 && <p className="m-0 text-[13px] text-muted-foreground">No scoring details came back for this service.</p>}
                  {row.candidates.map((c) => <CandidateCard key={c.node} c={c} runsOn={detail.node} />)}
                </>
              )}
            </div>
          </Section>

          <section aria-label="Logs" className="flex flex-col gap-3 rounded-[10px] bg-zinc-900 p-5">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h2 className="m-0 text-base font-semibold text-zinc-100">Logs</h2>
              <div className="flex flex-wrap items-center gap-3">
                <label className="flex min-h-11 items-center gap-2 text-xs text-zinc-300">
                  <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
                  Refresh every 3s
                </label>
                <Button variant="outline" className="border-zinc-600 bg-transparent text-zinc-100 hover:bg-zinc-800" disabled={logsQ.isFetching} onClick={() => void logsQ.refetch()}>
                  {logsQ.isFetching ? 'Loading…' : 'Refresh'}
                </Button>
              </div>
            </div>
            <pre className="m-0 min-h-[360px] max-h-[640px] overflow-auto font-mono text-[12.5px] leading-[1.65] text-zinc-200">
              {logsQ.isError ? logsQ.error.message : logsQ.data?.trim() || (logsQ.isPending && detail.status !== 'pending' ? 'Loading…' : 'No logs yet.')}
            </pre>
          </section>
        </div>
      </div>
    </main>
  )
}
