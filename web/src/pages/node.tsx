import { useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { OsMonitor } from '@/components/os-monitor'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { ApiError, api } from '@/lib/api'
import { OS_LABEL, STATE_LABEL, ageLabel, deriveNodes, offlineHint, stateTone, statusTone } from '@/lib/cluster'
import { formatMem, liveMemGiB } from '@/lib/memory'
import { requestLabel } from '@/lib/plan'
import { cn } from '@/lib/utils'

const GIB = 1024 ** 3
const mono = 'font-mono'

function Bar({ label, value, pct, tone }: { label: string; value: string; pct: number; tone?: string }) {
  const p = Math.max(0, Math.min(100, Math.round(pct)))
  return (
    <div>
      <div className="mb-1 flex justify-between gap-3 text-[13px]">
        <span>{label}</span>
        <span className={mono}>{value}</span>
      </div>
      <div className="h-2.5 overflow-hidden rounded-full bg-border">
        <div className={cn('h-full', tone ?? (p >= 85 ? 'bg-bad' : p >= 70 ? 'bg-warn' : 'bg-ok'))} style={{ width: `${p}%` }} />
      </div>
    </div>
  )
}

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

type Dialog = 'cordon' | 'revoke' | 'remove' | 'rename' | 'images' | null

export function NodePage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [dialog, setDialog] = useState<Dialog>(null)
  const [force, setForce] = useState(false)
  const [newName, setNewName] = useState('')
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [imagesSent, setImagesSent] = useState<string | null>(null)

  const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: api.nodes, refetchInterval: 3000 })
  const workloadsQ = useQuery({ queryKey: ['workloads'], queryFn: api.workloads, refetchInterval: 3000 })
  const node = useMemo(() => deriveNodes(nodesQ.data ?? [], workloadsQ.data ?? []).find((n) => n.id === id), [nodesQ.data, workloadsQ.data, id])
  const raw = nodesQ.data?.find((n) => n.ID === id)

  const refresh = () => queryClient.invalidateQueries({ queryKey: ['nodes'] })
  const close = () => setDialog(null)
  const cordon = useMutation({ mutationFn: () => api.nodeAction(id, node?.state === 'cordoned' ? 'uncordon' : 'cordon'), onSuccess: async () => { await refresh(); close() } })
  const revoke = useMutation({ mutationFn: () => api.nodeAction(id, 'revoke'), onSuccess: async () => { await refresh(); close() } })
  const remove = useMutation({
    mutationFn: () => api.removeNode(id, force),
    onSuccess: async () => {
      await refresh()
      navigate('/nodes')
    },
  })
  const rename = useMutation({ mutationFn: () => api.setNodeName(id, newName.trim()), onSuccess: async () => { await refresh(); close() } })
  const removeImages = useMutation({
    mutationFn: (images: string[]) => api.removeNodeImages(id, images),
    onSuccess: (_, images) => {
      setImagesSent(`Asked ${node?.name ?? id} to delete ${images.length} ${images.length === 1 ? 'image' : 'images'}. They leave this list after its next heartbeat; any Docker refuses (in use by a container) are listed in Events.`)
      setPicked(new Set())
      close()
    },
  })

  const crumbs = (
    <nav aria-label="Breadcrumb" className="text-[13px] text-muted-foreground">
      <Link to="/nodes" className="underline">Nodes</Link> / <span className={`${mono} text-foreground`}>{node?.name ?? id}</span>
    </nav>
  )

  if (!node || !raw) {
    return (
      <main className="mx-auto flex max-w-[1280px] flex-col gap-4 p-6">
        {crumbs}
        <Card className="p-6">
          <h1 className="m-0 text-lg font-semibold">{nodesQ.isPending ? 'Loading…' : 'Node not found'}</h1>
          {!nodesQ.isPending && <p className="m-0 mt-1 text-muted-foreground">No node is joined as <span className={mono}>{id}</span>. <Link to="/nodes" className="underline">All nodes</Link>.</p>}
        </Card>
      </main>
    )
  }

  const cordoned = node.state === 'cordoned'
  const active = node.workloads.filter((w) => w.Status !== 'stopped' && w.Status !== 'failed')
  const labels = Object.entries(node.labels)
  const caps = Object.entries(raw.Capabilities ?? {}).filter(([, on]) => on).map(([k]) => k)
  const removeConflict = remove.error instanceof ApiError && remove.error.status === 409

  return (
    <main className="mx-auto flex max-w-[1280px] flex-col gap-6 p-6">
      <div className="flex flex-col gap-3">
        {crumbs}
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex items-center gap-4">
            <OsMonitor os={node.os} size={56} dim={node.state === 'offline'} />
            <div>
              <div className="flex flex-wrap items-center gap-3">
                <h1 className={`${mono} m-0 text-[28px] font-medium`}>{node.name}</h1>
                <Badge tone={stateTone(node.state)}>{STATE_LABEL[node.state]}</Badge>
                {node.isHost && <Badge tone="info">control-plane host</Badge>}
              </div>
              <p className={`${mono} m-0 mt-1.5 text-[13px] text-muted-foreground`}>
                {node.name !== node.id && <>ID {node.id} · </>}{node.address} · {OS_LABEL[node.os]} · {node.arch} · last heartbeat {node.heartbeatAgeSec === null ? 'never' : `${ageLabel(node.heartbeatAgeSec)} ago`}
              </p>
            </div>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={() => { cordon.reset(); setDialog('cordon') }}>{cordoned ? 'Uncordon' : 'Cordon'}</Button>
            <Button variant="outline" onClick={() => { rename.reset(); setNewName(node.name === node.id ? '' : node.name); setDialog('rename') }}>Rename</Button>
            {!node.isHost && <Button variant="outline" onClick={() => { revoke.reset(); setDialog('revoke') }}>Revoke credentials</Button>}
            {!node.isHost && <Button variant="outline" className="text-bad-fg" onClick={() => { remove.reset(); setForce(false); setDialog('remove') }}>Remove node</Button>}
          </div>
        </div>
        <p className="m-0 text-[13px] text-muted-foreground">
          {cordoned ? 'Cordoned: the scheduler places nothing new here. Running workloads are not touched.' : 'Cordon stops new placements on this node. Running workloads are not touched.'}
        </p>
      </div>

      {node.state === 'throttling' && (
        <div role="status" className="rounded-lg border border-warn/40 bg-warn-soft px-4 py-3.5 text-warn-fg">
          <strong className="font-semibold">Running hot at {Math.round(node.thermal.TemperatureC)} °C.</strong> The agent reports throttling, so the scheduler
          lowers this node's thermal score for new workloads. It is a soft signal: a workload can still land here when no cooler node fits.
        </div>
      )}
      {node.state === 'offline' && (
        <div role="status" className="rounded-lg border border-bad/30 bg-bad-soft px-4 py-3.5 text-bad-fg">
          <strong className="font-semibold">No heartbeat.</strong> {offlineHint(node)} Start it on that machine with <span className={mono}>haco agent run</span>.
        </div>
      )}

      <ConfirmDialog open={dialog === 'cordon'} onOpenChange={(o) => !o && close()} danger={false} title={cordoned ? `Uncordon ${node.id}?` : `Cordon ${node.id}?`} confirmLabel={cordoned ? 'Uncordon' : 'Cordon'} pending={cordon.isPending} error={cordon.error?.message ?? null} onConfirm={() => cordon.mutate()}>
        {cordoned ? 'The scheduler can place new workloads here again.' : `The scheduler stops placing new workloads here. The ${active.length} running here keep running.`}
      </ConfirmDialog>
      <ConfirmDialog open={dialog === 'revoke'} onOpenChange={(o) => !o && close()} title={`Revoke ${node.id}'s credentials?`} confirmLabel="Revoke" pending={revoke.isPending} error={revoke.error?.message ?? null} onConfirm={() => revoke.mutate()}>
        Its agent can no longer talk to the control plane, so it stops getting work and its heartbeats are refused. To bring it back, join it again with a new token from Add node.
      </ConfirmDialog>
      <ConfirmDialog open={dialog === 'remove'} onOpenChange={(o) => !o && close()} title={`Remove ${node.id}?`} confirmLabel={force ? 'Remove anyway' : 'Remove node'} pending={remove.isPending} error={remove.error?.message ?? null} onConfirm={() => remove.mutate()}>
        <p className="m-0">This forgets the node. {active.length ? `${active.length} workloads are still assigned here.` : 'Nothing is assigned here.'} It cannot be undone; the machine can join again later.</p>
        {(removeConflict || active.length > 0) && (
          <label className="mt-3 flex items-start gap-2 text-foreground">
            <input type="checkbox" className="mt-1" checked={force} onChange={(e) => setForce(e.target.checked)} />
            <span>Remove it even though workloads are assigned. Their containers on that machine are left running and unmanaged.</span>
          </label>
        )}
      </ConfirmDialog>
      <ConfirmDialog open={dialog === 'rename'} onOpenChange={(o) => !o && close()} danger={false} title={`Rename ${node.name}`} confirmLabel="Save name" pending={rename.isPending} error={rename.error?.message ?? null} onConfirm={() => rename.mutate()}>
        <label className="flex flex-col gap-1.5 text-foreground">
          <span className="text-[13px] text-muted-foreground">
            Only the name shown changes. The node ID <span className={mono}>{node.id}</span> belongs to the machine and stays, so pins and data are untouched. Leave empty to show the ID.
          </span>
          <input value={newName} onChange={(e) => setNewName(e.target.value)} maxLength={64} placeholder={node.id} className="min-h-11 rounded-md border border-zinc-300 bg-card px-3" autoFocus />
        </label>
      </ConfirmDialog>
      <ConfirmDialog open={dialog === 'images'} onOpenChange={(o) => !o && close()} title={`Delete ${picked.size} ${picked.size === 1 ? 'image' : 'images'} from ${node.name}?`} confirmLabel="Delete images" pending={removeImages.isPending} error={removeImages.error?.message ?? null} onConfirm={() => removeImages.mutate([...picked])}>
        Frees disk space on that machine. A later deploy pulls or builds them again, which is slower. Images a container is using are refused and kept.
      </ConfirmDialog>

      <div className="flex flex-wrap items-start gap-6">
        <div className="flex min-w-0 flex-[1_1_400px] flex-col gap-4">
          <Section title="Capacity">
            <p className="m-0 text-[13px] text-muted-foreground">Reserved by workloads placed here, out of what the scheduler may use.</p>
            {(node.limits.cpu > 0 || node.limits.mem > 0) && (
              <p className="m-0 rounded-md bg-info-soft px-3 py-2 text-[13px] text-info-fg">
                Capped with <span className={mono}>haco node limit</span>: {node.limits.cpu > 0 ? `${node.limits.cpu} of ${node.detected.cpu.toFixed(0)} cores` : 'all cores'}, {node.limits.mem > 0 ? `${node.limits.mem.toFixed(1)} of ${node.detected.mem.toFixed(1)} GiB` : 'all memory'}.
              </p>
            )}
            <Bar label="CPU reserved" value={`${node.cpuUsed.toFixed(1)} / ${node.cpuCap.toFixed(1)} cores`} pct={node.cpuCap ? (node.cpuUsed / node.cpuCap) * 100 : 0} />
            <Bar label="Memory reserved" value={`${node.memUsed.toFixed(1)} / ${node.memCap.toFixed(1)} GiB`} pct={node.memCap ? (node.memUsed / node.memCap) * 100 : 0} />
            {node.memLive === null ? (
              <p className="m-0 text-[13px] text-muted-foreground">RAM in use: no recent report from this node's containers.</p>
            ) : (
              <Bar label="RAM in use by containers" value={`${formatMem(node.memLive)} / ${node.detected.mem.toFixed(1)} GiB`} pct={node.detected.mem ? (node.memLive / node.detected.mem) * 100 : 0} tone="bg-info" />
            )}
          </Section>

          <Section title="GPU and thermal">
            {node.gpus.length === 0 && <p className="m-0 text-[13px] text-muted-foreground">No GPU reported.</p>}
            {node.gpus.map((g, i) => (
              <Bar key={i} label={g.Model || g.Vendor} value={`${(g.VRAMFreeBytes / GIB).toFixed(1)} / ${(g.VRAMTotalBytes / GIB).toFixed(1)} GB VRAM free`} pct={g.VRAMTotalBytes ? ((g.VRAMTotalBytes - g.VRAMFreeBytes) / g.VRAMTotalBytes) * 100 : 0} tone="bg-info" />
            ))}
            {node.thermal.Known ? (
              <Bar label="Temperature" value={`${Math.round(node.thermal.TemperatureC)} °C · ${node.thermal.Throttling ? 'throttling' : 'cool'}`} pct={node.thermal.TemperatureC} tone={node.thermal.Throttling ? 'bg-warn' : 'bg-ok'} />
            ) : (
              <p className="m-0 text-[13px] text-muted-foreground">No temperature sensor reported. The scheduler treats the node as cool.</p>
            )}
          </Section>

          <Section title="Identity">
            <dl className="m-0 grid grid-cols-[max-content_1fr] gap-x-5 gap-y-2 text-[13px]">
              <dt className="text-muted-foreground">Address</dt><dd className={`${mono} m-0`}>{node.address}</dd>
              <dt className="text-muted-foreground">Architecture</dt><dd className={`${mono} m-0`}>{node.arch}</dd>
              <dt className="text-muted-foreground">Operating system</dt><dd className="m-0">{OS_LABEL[node.os]}</dd>
              <dt className="text-muted-foreground">Heartbeat</dt><dd className="m-0">{node.heartbeatAgeSec === null ? 'never' : `${ageLabel(node.heartbeatAgeSec)} ago`}</dd>
            </dl>
            <div>
              <div className="mb-1.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">Labels</div>
              <div className="flex flex-wrap gap-1.5">
                {labels.length ? labels.map(([k, v]) => <span key={k} className={`${mono} rounded bg-muted px-2 py-0.5 text-xs text-zinc-700`}>{k}={v}</span>) : <span className="text-[13px] text-muted-foreground">none</span>}
              </div>
            </div>
            <div>
              <div className="mb-1.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">Capabilities</div>
              <div className="flex flex-wrap gap-1.5">
                {caps.length ? caps.map((c) => <span key={c} className={`${mono} rounded bg-ok-soft px-2 py-0.5 text-xs text-ok-fg`}>{c}</span>) : <span className="text-[13px] text-muted-foreground">none</span>}
              </div>
            </div>
          </Section>
        </div>

        <div className="flex min-w-0 flex-[999_1_560px] flex-col gap-4">
          <section aria-label="Workloads on this node" className="flex flex-col gap-3">
            <h2 className="m-0 text-lg font-semibold">Workloads on this node</h2>
            <Card className="overflow-x-auto">
              <table className="w-full min-w-[600px] border-collapse">
                <thead>
                  <tr className="text-left text-xs text-muted-foreground">
                    {['Workload', 'Status', 'Reserved', 'RAM used', 'Host ports'].map((h) => <th key={h} className="px-4 py-3 font-medium">{h}</th>)}
                  </tr>
                </thead>
                <tbody>
                  {node.workloads.map((w) => (
                    <tr key={`${w.Project}/${w.Service}/${w.Revision}`} className="border-t border-muted">
                      <td className="px-4 py-3">
                        <Link to={`/projects/${encodeURIComponent(w.Project)}/services/${encodeURIComponent(w.Service)}`} className={`${mono} font-medium underline`}>
                          {w.Project}/{w.Service}/{w.Revision}
                        </Link>
                      </td>
                      <td className="px-4 py-3"><Badge tone={statusTone(w.Status)}>{w.Status.replace(/_/g, ' ')}</Badge></td>
                      <td className={`${mono} whitespace-nowrap px-4 py-3 text-xs`}>{requestLabel({ cpuCores: w.Resources.CPUMillis / 1000, memGiB: w.Resources.MemoryBytes / GIB })}</td>
                      <td className={`${mono} whitespace-nowrap px-4 py-3 text-xs`}>{formatMem(liveMemGiB([w]))}</td>
                      <td className={`${mono} px-4 py-3 text-[13px]`}>
                        {(w.PortBindings ?? []).length ? w.PortBindings!.map((p) => (p.HostPort === p.ContainerPort ? `${p.HostPort}` : `${p.HostPort} → ${p.ContainerPort}`)).join(', ') : 'none'}
                      </td>
                    </tr>
                  ))}
                  {node.workloads.length === 0 && <tr className="border-t border-muted"><td colSpan={5} className="px-4 py-6 text-center text-muted-foreground">Nothing is placed on this node.</td></tr>}
                </tbody>
              </table>
            </Card>
          </section>

          <Section
            title="Cached images"
            aside={
              <Button variant="outline" className="text-bad-fg" disabled={picked.size === 0} onClick={() => { removeImages.reset(); setDialog('images') }}>
                Delete {picked.size || ''} selected
              </Button>
            }
          >
            <p className="m-0 text-[13px] text-muted-foreground">A scoring bonus only. Any node can still pull.</p>
            {imagesSent && <p role="status" className="m-0 rounded-md bg-ok-soft px-3 py-2 text-[13px] text-ok-fg">{imagesSent}</p>}
            {node.cachedImages.length > 0 && (
              <label className="flex min-h-11 items-center gap-2.5 border-b border-muted text-[13px] font-medium">
                <input
                  type="checkbox"
                  className="size-4"
                  checked={picked.size === node.cachedImages.length}
                  ref={(el) => { if (el) el.indeterminate = picked.size > 0 && picked.size < node.cachedImages.length }}
                  onChange={(e) => setPicked(e.target.checked ? new Set(node.cachedImages) : new Set())}
                />
                Select all {node.cachedImages.length}
              </label>
            )}
            <ul className="m-0 flex max-h-[360px] list-none flex-col overflow-y-auto p-0">
              {node.cachedImages.map((im) => (
                <li key={im} className="border-b border-muted last:border-b-0">
                  <label className="flex min-h-11 items-center gap-2.5 py-1.5 text-[13px]">
                    <input
                      type="checkbox"
                      className="size-4 flex-none"
                      checked={picked.has(im)}
                      onChange={(e) => setPicked((cur) => { const next = new Set(cur); if (e.target.checked) next.add(im); else next.delete(im); return next })}
                    />
                    <span className={`${mono} [overflow-wrap:anywhere]`}>{im}</span>
                  </label>
                </li>
              ))}
              {node.cachedImages.length === 0 && <li className="text-[13px] text-muted-foreground">No images reported.</li>}
            </ul>
          </Section>
        </div>
      </div>
    </main>
  )
}
