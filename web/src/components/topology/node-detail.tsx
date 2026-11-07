import { Badge } from '@/components/ui/badge'
import type { Discovered } from '@/lib/api'
import {
  OS_LABEL,
  STATE_LABEL,
  ageLabel,
  gpuLabel,
  offlineHint,
  stateTone,
  thermalLabel,
  type NodeView,
} from '@/lib/cluster'
import { cn } from '@/lib/utils'

function barColor(pct: number) {
  if (pct >= 85) return 'bg-bad'
  if (pct >= 70) return 'bg-warn'
  return 'bg-ok'
}

function Bar({ label, used, cap, unit }: { label: string; used: number; cap: number; unit: string }) {
  const pct = cap > 0 ? Math.min(100, Math.round((used / cap) * 100)) : 0
  return (
    <div>
      <div className="mb-1 flex justify-between text-xs">
        <span className="text-muted-foreground">{label}</span>
        <span className="font-mono">
          {used.toFixed(1)} / {cap.toFixed(1)} {unit}
        </span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-off-soft">
        <div className={cn('h-full rounded-full', barColor(pct))} style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-3 border-t border-muted py-1.5 text-xs">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="m-0 break-words text-right">{value}</dd>
    </div>
  )
}

export function NodeDetail({ node, host }: { node: NodeView; host: boolean }) {
  const failed = node.workloads.filter((w) => w.Status === 'failed').length
  const waiting = node.workloads.filter((w) => w.Status === 'waiting_dependencies').length
  const workloadSummary = [
    `${node.workloads.length}`,
    failed ? `${failed} failed` : '',
    waiting ? `${waiting} waiting` : '',
  ]
    .filter(Boolean)
    .join(' · ')
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-1">
        <span className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
          {host ? 'Host · control plane' : 'Agent node'}
        </span>
        <span className="font-mono text-[15px] font-medium">{node.name}</span>
        <span className="text-xs text-muted-foreground">
          {node.address} · {OS_LABEL[node.os]} · {node.arch}
        </span>
        <div className="mt-0.5">
          <Badge tone={stateTone(node.state)}>{STATE_LABEL[node.state]}</Badge>
        </div>
      </div>
      {node.state === 'offline' && (
        <p className="m-0 rounded-md bg-bad-soft p-2 text-xs text-bad-fg">{offlineHint(node)}</p>
      )}
      <Bar label="CPU reserved" used={node.cpuUsed} cap={node.cpuCap} unit="cores" />
      <Bar label="Memory reserved" used={node.memUsed} cap={node.memCap} unit="GiB" />
      <dl className="m-0 flex flex-col">
        <Row label="Heartbeat" value={node.heartbeatAgeSec === null ? 'never' : `${ageLabel(node.heartbeatAgeSec)} ago`} />
        <Row label="Thermal" value={thermalLabel(node)} />
        <Row label="GPU" value={node.gpus.length ? node.gpus.map(gpuLabel).join(', ') : 'None'} />
        <Row label="Workloads" value={workloadSummary} />
      </dl>
      <span className="text-xs font-medium text-ok">{host ? 'Click to start a deploy' : 'Click to open node page'}</span>
    </div>
  )
}

export function DiscoveredDetail({ device }: { device: Discovered }) {
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-1">
        <span className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">Discovered on the network</span>
        <span className="font-mono text-[15px] font-medium">{device.hostname || device.address}</span>
        <span className="text-xs text-muted-foreground">
          {device.address}
          {device.mac && ` · ${device.mac}`}
        </span>
        <div className="mt-0.5">
          <Badge tone="off">not joined</Badge>
        </div>
      </div>
      <dl className="m-0 flex flex-col">
        <Row label="Seen" value="Answered a probe on this network" />
        <Row label="Agent" value="Not installed" />
        <Row label="Connection" value="None to the control plane" />
      </dl>
      <span className="text-xs font-medium text-ok">Click to issue a join token</span>
    </div>
  )
}

export function ControlPlaneOnlyDetail() {
  return (
    <div className="flex flex-col gap-2">
      <span className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">Host · control plane</span>
      <span className="font-mono text-[15px] font-medium">Control plane</span>
      <p className="m-0 text-xs text-muted-foreground">
        No agent is running on this machine, so it holds no workloads. Workloads run on the nodes around it.
      </p>
      <span className="text-xs font-medium text-ok">Click to start a deploy</span>
    </div>
  )
}
