import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { DeployBar } from '@/components/deploy/deploy-bar'
import { DependencyGraph, type GraphNodeInfo } from '@/components/deploy/dependency-graph'
import { PlanTable } from '@/components/deploy/plan-table'
import type { ComposeFile } from '@/components/deploy/upload-step'
import { api } from '@/lib/api'
import type { DeployConfig } from '@/lib/deploy-config'
import { saveDeploy } from '@/lib/saved-deploys'
import { buildRequest, derivePlan, liveState, nodesUsed, pinsFor, type LiveState } from '@/lib/plan'

interface Props {
  compose: ComposeFile
  config: DeployConfig
  onChange: (patch: Partial<DeployConfig>) => void
  onBack: () => void
}

const LINE: Record<LiveState, string> = { ready: 'ready', progress: 'starting', failed: 'failed', waiting: 'waiting', stopped: 'stopped', planned: '' }

function Message({ title, children, actions }: { title: string; children: React.ReactNode; actions: React.ReactNode }) {
  return (
    <Card className="flex max-w-[640px] flex-col gap-4 p-6">
      <div>
        <h2 className="m-0 text-lg font-semibold">{title}</h2>
        <div className="mt-1 text-muted-foreground">{children}</div>
      </div>
      <div className="flex flex-wrap gap-2">{actions}</div>
    </Card>
  )
}

export function PlanStep({ compose, config, onChange, onBack }: Props) {
  const [deployed, setDeployed] = useState(false)

  const baseReq = useMemo(() => buildRequest(compose.text, config, false), [compose.text, config])
  const baseQ = useQuery({
    queryKey: ['plan', baseReq],
    queryFn: () => api.composePlan(baseReq),
    retry: false,
    staleTime: Infinity, // re-plan only when the choices change, or on request
    refetchOnWindowFocus: false,
    placeholderData: keepPreviousData, // keep the table on screen while "Plan fresh" re-plans
  })
  const base = baseQ.data

  // Placement changes that still apply to a service in this plan.
  const placements = useMemo(() => {
    const inPlan = new Set((base?.plan.workloads ?? []).map((w) => w.Service))
    return Object.fromEntries(Object.entries(config.placements).filter(([service]) => inPlan.has(service)))
  }, [base, config.placements])
  const hasPlacements = Object.keys(placements).length > 0
  const effReq = useMemo(() => {
    const changes = { ...config, placements }
    return buildRequest(compose.text, base ? { ...changes, placements: pinsFor(base, changes) } : changes, true)
  }, [compose.text, config, placements, base])
  const effQ = useQuery({
    queryKey: ['plan-eff', effReq],
    queryFn: () => api.composePlan(effReq),
    enabled: hasPlacements && !!base && (base.plan.rejections ?? []).length === 0,
    retry: false,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    placeholderData: keepPreviousData,
  })

  const workloadsQ = useQuery({ queryKey: ['workloads'], queryFn: api.workloads, enabled: deployed, refetchInterval: deployed ? 2000 : false })
  const deploy = useMutation({
    mutationFn: (fresh: boolean) => {
      const req = hasPlacements ? effReq : baseReq
      return api.composeDeploy(fresh ? { ...req, fresh: true } : req)
    },
    onSuccess: () => {
      setDeployed(true)
      // Remember what was deployed so the project page can redeploy or edit it.
      saveDeploy(config.project, {
        file: { name: compose.name, size: compose.size, text: compose.text },
        config: { ...config, placements },
        at: new Date().toISOString(),
      })
    },
  })

  const effective = hasPlacements ? (effQ.data ?? null) : null
  const services = useMemo(() => (base ? derivePlan(base, effective, config) : []), [base, effective, config])
  const states = useMemo(() => {
    const m = new Map<string, LiveState>()
    if (!deployed) return m
    for (const w of workloadsQ.data ?? []) {
      if (w.Project === config.project && w.DesiredState === 'running') m.set(w.Service, liveState(w.Status))
    }
    return m
  }, [deployed, workloadsQ.data, config.project])

  if (baseQ.isPending) {
    return (
      <Message title="Planning…" actions={<Button variant="outline" onClick={onBack}>Back</Button>}>
        Checking the file and asking the scheduler where each service fits.
      </Message>
    )
  }
  if (baseQ.isError || !base) {
    return (
      <Message
        title="Could not plan this deploy"
        actions={
          <>
            <Button variant="outline" onClick={onBack}>Back</Button>
            <Button onClick={() => void baseQ.refetch()}>Try again</Button>
          </>
        }
      >
        <p role="alert" className="m-0 text-bad-fg">{baseQ.error?.message}</p>
        {/no such file|not found/.test(baseQ.error?.message ?? '') && (
          <p className="m-0 mt-2 text-[13px]">
            Relative paths in the file (env files, build folders) resolve against the project folder
            {config.workingDir.trim() ? <> you gave: <span className="font-mono">{config.workingDir.trim()}</span></> : ''}. Check that
            it is the folder holding the compose file, then go back and fix it.
          </p>
        )}
      </Message>
    )
  }
  const rejections = base.plan.rejections ?? []
  if (rejections.length > 0) {
    return (
      <Message title="This plan cannot be deployed" actions={<Button variant="outline" onClick={onBack}>Back to configuration</Button>}>
        <ul className="m-0 list-disc pl-5 text-bad-fg">
          {rejections.map((r) => (
            <li key={r}>{r}</li>
          ))}
        </ul>
      </Message>
    )
  }

  const effRejections = effQ.data?.plan.rejections ?? []
  const stages = services.reduce((m, s) => Math.max(m, s.stage + 1), 0)
  const warnings = [...(base.plan.warnings ?? []), ...services.flatMap((s) => s.warnings.map((w) => `${s.name}: ${w}`))]
  const net = base.plan.networking
  const networkBlock = net.supported ? null : `Networking is not supported: ${(net.reasons ?? []).join(' ') || net.mode}`
  const blocked =
    networkBlock ??
    (effRejections.length > 0 ? `That placement cannot work: ${effRejections.join('; ')}` : null) ??
    (hasPlacements && (effQ.isFetching || !effQ.data) ? 'Updating placements…' : null)
  const changed = services.filter((s) => s.changed).length

  const graphNodes: GraphNodeInfo[] = services.map((s) => {
    const state = states.get(s.name) ?? 'planned'
    return { name: s.name, stage: s.stage, deps: s.deps, state, line: deployed && state !== 'planned' ? LINE[state] : s.node || 'unplaced' }
  })

  function use(service: string, node: string) {
    const pick = services.find((s) => s.name === service)?.pickNode
    const next = { ...config.placements }
    if (node === pick) delete next[service]
    else next[service] = node
    onChange({ placements: next })
  }
  function reset(service: string) {
    const next = { ...config.placements }
    delete next[service]
    onChange({ placements: next })
  }

  return (
    <>
      <section aria-label="Plan summary" className="flex flex-wrap items-center gap-x-6 gap-y-2.5 rounded-[10px] border border-border bg-card px-5 py-3.5">
        {[
          [services.length, 'services'],
          [stages, 'stages'],
          [nodesUsed(services), 'nodes'],
          [services.filter((s) => s.build).length, 'builds'],
          [services.filter((s) => s.gpu).length, 'need a GPU'],
        ].map(([n, label]) => (
          <span key={label} className="flex items-baseline gap-1.5">
            <span className="text-2xl font-semibold">{n}</span>
            <span className="text-muted-foreground">{label}</span>
          </span>
        ))}
        <span className="ml-auto flex flex-wrap gap-2">
          <span className="rounded-full bg-ok-soft px-2.5 py-0.5 text-xs font-medium text-ok-fg">✓ No cycles</span>
          <span className="rounded-full bg-warn-soft px-2.5 py-0.5 text-xs font-medium text-warn-fg">{warnings.length} warnings</span>
          {changed > 0 && <span className="rounded-full bg-info-soft px-2.5 py-0.5 text-xs font-medium text-info-fg">{changed} {changed === 1 ? 'placement' : 'placements'} changed</span>}
        </span>
      </section>

      {warnings.length > 0 && (
        <ul aria-label="Warnings" className="m-0 flex list-none flex-col gap-2 p-0">
          {warnings.map((w) => (
            <li key={w} className="rounded-lg border border-warn/30 bg-warn-soft px-3.5 py-2.5 text-[13px] text-warn-fg">
              {w}
            </li>
          ))}
        </ul>
      )}

      <section aria-label="Plan" className="flex flex-col gap-3">
        <div className="flex flex-wrap items-end justify-between gap-x-4 gap-y-2">
          <div>
            <h2 className="m-0 text-base font-semibold">Plan</h2>
            <p className="m-0 mt-0.5 text-[13px] text-muted-foreground">Where each service lands and why. Expand a row to compare nodes, or change where it runs.</p>
          </div>
          <span className="flex flex-wrap gap-2">
            <Button
              type="button"
              variant="outline"
              className="text-[13px]"
              aria-pressed={config.fresh}
              title="Ignore where earlier deploys placed services and let the scheduler choose again"
              disabled={deployed || baseQ.isFetching}
              onClick={() => onChange({ fresh: !config.fresh })}
            >
              {config.fresh ? '✓ Fresh plan · use stored placements' : 'Plan fresh'}
            </Button>
            <Button type="button" variant="outline" className="text-[13px]" disabled={deployed || baseQ.isFetching} onClick={() => void baseQ.refetch()}>
              {baseQ.isFetching ? 'Planning…' : 'Plan again'}
            </Button>
          </span>
        </div>
        <PlanTable services={services} states={states} locked={deployed} onUse={use} onReset={reset} />
      </section>

      <section aria-label="Dependency graph" className="flex flex-col gap-3">
        <div>
          <h2 className="m-0 text-base font-semibold">Dependencies</h2>
          <p className="m-0 mt-0.5 text-[13px] text-muted-foreground">
            Start order. An arrow points at the service that waits. A solid line waits for healthy, a dashed one for started, a red one is blocked by a failure.
          </p>
        </div>
        <DependencyGraph nodes={graphNodes} />
      </section>

      <DeployBar
        services={services}
        states={states}
        nodes={nodesUsed(services)}
        stages={stages}
        deployed={deployed}
        deploying={deploy.isPending}
        error={deploy.error?.message ?? null}
        blocked={blocked}
        onBack={onBack}
        fresh={config.fresh}
        onDeploy={(fresh) => deploy.mutate(fresh)}
      />
    </>
  )
}
