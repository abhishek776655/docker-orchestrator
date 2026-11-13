import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, Search } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { api, type ApiWorkload } from '@/lib/api'
import { addableServices, addConfig, previewAdd, whyPlaced, withOverrides, type Override } from '@/lib/add-service'
import { summarizeCompose } from '@/lib/compose-summary'
import { defaultConfig } from '@/lib/deploy-config'
import { loadComposeSource } from '@/lib/compose-source'
import { buildRequest, derivePlan } from '@/lib/plan'
import type { Candidate, PlanService } from '@/lib/plan'
import type { SavedDeploy } from '@/lib/saved-deploys'
import { cn } from '@/lib/utils'

interface Props {
  project: string
  saved: SavedDeploy | null
  workloads: ApiWorkload[] // this project's
  open: boolean
  onOpenChange: (open: boolean) => void
  onAdded: (message: string) => void
}

/**
 * Adds services from the project's saved Compose file without touching the
 * ones that already run. Shows what would start, where, and what is left
 * alone before anything is sent.
 */
export function AddServiceDialog({ project, saved, workloads, open, onOpenChange, onAdded }: Props) {
  const queryClient = useQueryClient()
  const [picked, setPicked] = useState<string[]>([])
  const [query, setQuery] = useState('')
  // Nodes the user chose instead of the scheduler's pick, by service.
  const [overrides, setOverrides] = useState<Record<string, Override>>({})

  // The Compose file as it is on disk now, so edits made after the last
  // deploy are picked up; the saved copy only stands in when it can't be read.
  const source = useQuery({
    queryKey: ['compose-source', project],
    queryFn: () => loadComposeSource(project),
    enabled: open && !!saved,
    gcTime: 0,
    staleTime: 0,
    retry: false,
  })
  const text = source.data?.text ?? null
  const parsed = useMemo(() => (text ? summarizeCompose(text) : null), [text])
  const summary = parsed?.ok ? parsed.summary : null
  const deployed = useMemo(() => [...new Set(workloads.map((w) => w.Service))], [workloads])
  const addable = useMemo(() => (summary ? addableServices(summary.details, deployed) : []), [summary, deployed])
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase()
    return q ? addable.filter((d) => d.name.toLowerCase().includes(q)) : addable
  }, [addable, query])
  const preview = useMemo(() => (summary && picked.length ? previewAdd(summary.details, picked, deployed) : null), [summary, picked, deployed])

  const config = useMemo(() => {
    if (!saved || !text || !summary || !preview) return null
    const runningNodes = Object.fromEntries(workloads.map((w) => [w.Service, w.NodeID]))
    return addConfig({ ...defaultConfig(summary), ...saved.config, project }, picked, preview, runningNodes)
  }, [saved, text, summary, preview, workloads, picked, project])
  // The scheduler's own plan, always asked for without the user's choices so
  // every node can be compared; and the plan with their choices applied.
  const baseRequest = useMemo(() => (text && config ? buildRequest(text, config, true) : null), [text, config])
  const names = useMemo(() => (preview ? [...preview.deploy, ...preview.alsoNeeded] : []), [preview])
  const effConfig = useMemo(() => (config ? withOverrides(config, overrides, names) : null), [config, overrides, names])
  const chosen = effConfig ? names.filter((n) => overrides[n]) : []
  const request = useMemo(() => (text && effConfig ? buildRequest(text, effConfig, true) : null), [text, effConfig])

  const plan = useQuery({
    queryKey: ['add-service-plan', project, picked.join(',')],
    queryFn: () => api.composePlan(baseRequest!),
    enabled: open && !!baseRequest,
    retry: false,
  })
  const effPlan = useQuery({
    queryKey: ['add-service-plan-chosen', project, picked.join(','), JSON.stringify(chosen.map((n) => [n, overrides[n]]))],
    queryFn: () => api.composePlan(request!),
    enabled: open && !!request && chosen.length > 0,
    retry: false,
  })
  const shownPlan = chosen.length > 0 ? effPlan.data : plan.data
  const rows = useMemo(() => (plan.data && effConfig ? derivePlan(plan.data, chosen.length > 0 ? (effPlan.data ?? null) : null, effConfig, workloads) : []), [plan.data, effPlan.data, effConfig, chosen.length, workloads])
  const newRows = rows.filter((r) => preview && (preview.deploy.includes(r.name) || preview.alsoNeeded.includes(r.name)))
  const unplaced = newRows.filter((r) => !r.node)
  const rejections = shownPlan?.plan.rejections ?? []
  const planned = chosen.length > 0 ? effPlan.isSuccess : plan.isSuccess
  const useNode = (service: string, node: string, forced: boolean) => {
    add.reset()
    setOverrides((cur) => ({ ...cur, [service]: { node, forced } }))
  }
  const resetNode = (service: string) => {
    add.reset()
    setOverrides(({ [service]: _drop, ...rest }) => rest)
  }

  const add = useMutation({
    // Running dependencies are only referenced, never submitted again, so
    // adding a service cannot restart or rebuild them.
    mutationFn: () => api.composeDeploy({ ...request!, keep_running: preview?.reused ?? [] }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['workloads'] })
      onAdded(`Adding ${picked.join(', ')}.`)
      setPicked([])
      setQuery('')
      setOverrides({})
    },
  })
  const toggle = (name: string) => {
    add.reset()
    setPicked((cur) => (cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name]))
  }
  const canAdd = !!preview && planned && unplaced.length === 0 && rejections.length === 0 && !add.isPending

  return (
    <Dialog open={open} onOpenChange={(o) => !add.isPending && onOpenChange(o)}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-2xl gap-0 p-0">
        <div className="flex-none px-6 pb-3 pt-6 pr-14">
          <DialogTitle className="m-0 text-lg font-semibold">Add a service to {project}</DialogTitle>
          <DialogDescription className="m-0 mt-1 text-sm text-muted-foreground">
            Starts only what you pick, from {source.data?.name ?? saved?.file.name ?? 'the Compose file'}. Services that already run are not redeployed or moved.
          </DialogDescription>
        </div>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-6 pb-4 pt-1">

        {!saved && <p className="m-0 text-[13px] text-muted-foreground">This browser has no saved Compose file for this project. Use Edit configuration to upload it first.</p>}
        {saved && source.isPending && <p className="m-0 text-[13px] text-muted-foreground">Reading the Compose file…</p>}
        {source.data && (
          <p className={cn('m-0 flex flex-wrap items-center gap-2 text-[13px]', source.data.from === 'saved' ? 'text-warn-fg' : 'text-muted-foreground')}>
            <span>{source.data.note}</span>
            {source.data.changed && <Badge tone="info">new</Badge>}
            <button type="button" onClick={() => void source.refetch()} className="underline">
              Read again
            </button>
          </p>
        )}
        {parsed && !parsed.ok && <p role="alert" className="m-0 text-[13px] text-bad-fg">{parsed.error}</p>}
        {summary && addable.length === 0 && <p className="m-0 text-[13px] text-muted-foreground">Every service in the Compose file is already part of this project. To add one that is not in the file, add it to the file in Edit configuration first.</p>}

        {addable.length > 0 && (
          <div className="flex flex-col gap-2">
            <div className="flex items-center gap-2">
              <div className="relative flex-1">
                <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
                <input
                  type="search"
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder={`Search ${addable.length} ${addable.length === 1 ? 'service' : 'services'}`}
                  aria-label="Search services to add"
                  className="min-h-11 w-full rounded-md border border-border bg-background pl-9 pr-3 text-[13px] outline-none focus-visible:ring-2 focus-visible:ring-ok"
                />
              </div>
              {picked.length > 0 && (
                <button type="button" onClick={() => { add.reset(); setPicked([]) }} className="min-h-11 whitespace-nowrap px-2 text-[13px] text-muted-foreground underline">
                  Clear {picked.length} selected
                </button>
              )}
            </div>
            <fieldset className="m-0 flex max-h-48 flex-col gap-1 overflow-y-auto border-0 p-0">
              <legend className="sr-only">Services not running yet</legend>
              {shown.length === 0 && <p className="m-0 px-1 py-3 text-center text-[13px] text-muted-foreground">No service matches “{query.trim()}”.</p>}
              {shown.map((d) => (
                <label key={d.name} title={d.dependsOn.length ? `Needs ${d.dependsOn.join(', ')}` : undefined} className={cn('flex min-h-11 min-w-0 cursor-pointer items-center gap-2.5 rounded-md border px-3 text-[13px]', picked.includes(d.name) ? 'border-ok bg-ok-soft/40' : 'border-border')}>
                  <input type="checkbox" checked={picked.includes(d.name)} onChange={() => toggle(d.name)} className="size-4" />
                  <span className="min-w-0 truncate font-mono font-medium">{d.name}</span>
                  {d.build && <Badge tone="warn">build</Badge>}
                  {d.stateful && <Badge tone="info">volume</Badge>}
                </label>
              ))}
            </fieldset>
          </div>
        )}

        {preview && (
          <div className="flex flex-col gap-3 text-[13px]">
            {(plan.isPending || (chosen.length > 0 && effPlan.isPending)) && <span className="text-muted-foreground">Planning placement…</span>}
            {plan.isError && <span role="alert" className="whitespace-pre-line text-bad-fg">{(plan.error as Error).message}</span>}
            {chosen.length > 0 && effPlan.isError && <span role="alert" className="whitespace-pre-line text-bad-fg">{(effPlan.error as Error).message}</span>}
            {rejections.map((r) => (
              <span key={r} className="text-bad-fg">{r}</span>
            ))}
            {newRows.map((r) => (
              <PlacementCard key={r.name} row={r} needed={preview.alsoNeeded.includes(r.name)} onUse={useNode} onReset={resetNode} />
            ))}
            <ServiceGroup
              title="Already running, and it needs them"
              note="They stay where they are and are not restarted."
              names={preview.reused}
            />
            <ServiceGroup title="Not affected" note="Everything else in this project keeps running unchanged." names={preview.untouched} />
          </div>
        )}

        {add.isError && (
          <p role="alert" className="m-0 whitespace-pre-line rounded-md border border-bad/30 bg-bad-soft p-3 text-[13px] text-bad-fg">
            {(add.error as Error).message}
          </p>
        )}
        </div>
        <div className="flex flex-none flex-wrap justify-end gap-2 border-t border-border px-6 py-4">
          <Button type="button" variant="outline" disabled={add.isPending} onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="button" disabled={!canAdd} onClick={() => add.mutate()}>
            {add.isPending ? 'Adding…' : picked.length > 1 ? `Add ${picked.length} services` : 'Add service'}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}

const barColor = (v: number) => (v >= 0.7 ? 'bg-ok' : v >= 0.4 ? 'bg-warn' : 'bg-bad')

/** Where one service lands and why: the chosen node, its fit, and every node passed over. */
function PlacementCard({ row, needed, onUse, onReset }: { row: PlanService; needed: boolean; onUse: (service: string, node: string, forced: boolean) => void; onReset: (service: string) => void }) {
  const chosen = row.candidates.find((c) => c.outcome === 'selected')
  const rejected = row.candidates.filter((c) => c.outcome === 'rejected')
  return (
    <section className="flex flex-col gap-3 rounded-lg border border-border bg-card p-3.5">
      <header className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-sm font-semibold">{row.name}</span>
        <span aria-hidden="true" className="text-muted-foreground">→</span>
        {row.node ? <Badge tone="ok" className="font-mono">{row.node}</Badge> : <Badge tone="bad">no node can take it</Badge>}
        {needed && <span className="text-xs text-muted-foreground">needed by what you picked</span>}
        {row.forced && <Badge tone="warn">forced</Badge>}
        {row.changed && !row.forced && <Badge tone="info">your choice</Badge>}
        <span className="ml-auto flex items-center gap-2">
          {chosen?.score !== undefined && <span className="font-mono text-xs text-muted-foreground">score {chosen.score.toFixed(2)}</span>}
          {row.candidates.length > 1 && <NodePicker row={row} onUse={onUse} />}
        </span>
      </header>
      {row.changed && (
        <button type="button" onClick={() => onReset(row.name)} className="-mt-1 self-start text-xs text-muted-foreground underline">
          Reset to the scheduler's choice ({row.pickNode})
        </button>
      )}

      {chosen && (
        <div className="flex flex-col gap-2 rounded-md bg-ok-soft/50 px-3 py-2.5">
          <span className="text-xs font-semibold uppercase tracking-wide text-ok-fg">Why here</span>
          <span>{whyPlaced(row.candidates)}</span>
          {chosen.factors && (
            <div className="grid gap-x-4 gap-y-1.5 sm:grid-cols-2">
              {chosen.factors.map((f) => (
                <div key={f.label} className="flex items-center gap-2 text-xs">
                  <span className="w-[5.5rem] flex-none text-muted-foreground">{f.label}</span>
                  <div className="h-1.5 flex-1 overflow-hidden rounded bg-border">
                    <div className={cn('h-1.5', barColor(f.value))} style={{ width: `${Math.round(f.value * 100)}%` }} />
                  </div>
                  <span className="w-8 text-right font-mono">{f.value.toFixed(2)}</span>
                </div>
              ))}
            </div>
          )}
          {chosen.warning && !chosen.detail.includes(chosen.warning) && <span className="text-warn-fg">{chosen.warning}</span>}
        </div>
      )}

      {rejected.length > 0 && (
        <div className="flex flex-col gap-1.5">
          <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Other nodes</span>
          <ul className="m-0 flex list-none flex-col gap-1.5 p-0">
            {rejected.map((c) => (
              <RejectedRow key={c.node} c={c} />
            ))}
          </ul>
        </div>
      )}
    </section>
  )
}

function RejectedRow({ c }: { c: Candidate }) {
  return (
    <li className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 rounded-md border border-border px-2.5 py-1.5">
      <Badge tone="bad" className="text-[11px]">rejected</Badge>
      <span className="font-mono text-xs font-medium">{c.node}</span>
      {c.score !== undefined && <span className="font-mono text-xs text-muted-foreground">{c.score.toFixed(2)}</span>}
      <span className="basis-full text-xs text-muted-foreground sm:basis-auto">{c.detail}</span>
    </li>
  )
}

/** A titled set of service names; a long set folds away behind its count. */
function ServiceGroup({ title, note, names }: { title: string; note: string; names: string[] }) {
  if (names.length === 0) return null
  const chips = (
    <ul className="m-0 mt-2 flex list-none flex-wrap gap-1.5 p-0">
      {names.map((n) => (
        <li key={n} className="rounded-md border border-border bg-muted/50 px-2 py-0.5 font-mono text-xs">
          {n}
        </li>
      ))}
    </ul>
  )
  return (
    <details className="group rounded-lg border border-border bg-card px-3.5 py-2.5" open={names.length <= 4}>
      <summary className="flex cursor-pointer list-none items-baseline gap-2 text-[13px] [&::-webkit-details-marker]:hidden">
        <span className="font-medium">{title}</span>
        <Badge tone="off">{names.length}</Badge>
        <span className="ml-auto text-xs text-muted-foreground group-open:hidden">show</span>
        <span className="ml-auto hidden text-xs text-muted-foreground group-open:inline">hide</span>
      </summary>
      <p className="m-0 mt-1 text-xs text-muted-foreground">{note}</p>
      {chips}
    </details>
  )
}

/** Pick another node for a service; one the scheduler rejected asks to be used anyway. */
function NodePicker({ row, onUse }: { row: PlanService; onUse: (service: string, node: string, forced: boolean) => void }) {
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant="outline" className="min-h-9 gap-1 px-2.5 text-xs">
          Change node
          <ChevronDown className="size-3.5" aria-hidden="true" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <DropdownMenuLabel>Place {row.name} on</DropdownMenuLabel>
        {row.candidates.map((c) => {
          const refused = c.rejectedBy === 'scheduler' || c.rejectedBy === 'volume'
          return (
            <DropdownMenuItem key={c.node} disabled={c.inUse} onSelect={() => onUse(row.name, c.node, refused)}>
              <span className="flex items-center gap-2">
                <span className="font-mono text-[13px] font-medium">{c.node}</span>
                {c.inUse && <Badge tone="ok">selected</Badge>}
                {c.isPick && !c.inUse && <Badge tone="info">scheduler's pick</Badge>}
                {c.score !== undefined && <span className="ml-auto font-mono text-xs text-muted-foreground">{c.score.toFixed(2)}</span>}
              </span>
              {refused && <span className="text-xs text-bad-fg">Rejected: {c.detail}. Use anyway; it may fail or start empty.</span>}
              {!refused && !c.inUse && <span className="text-xs text-muted-foreground">{c.detail}</span>}
            </DropdownMenuItem>
          )
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
