import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { OsMonitor } from '@/components/os-monitor'
import { Badge } from '@/components/ui/badge'
import { Card } from '@/components/ui/card'
import { api } from '@/lib/api'
import { OS_LABEL, STATE_LABEL, ageLabel, deriveNodes, stateTone, thermalLabel, type NodeView } from '@/lib/cluster'
import { cn } from '@/lib/utils'

function pct(used: number, cap: number) {
  return cap > 0 ? Math.min(100, Math.round((used / cap) * 100)) : 0
}

export function MiniBar({ used, cap, unit }: { used: number; cap: number; unit: string }) {
  const p = pct(used, cap)
  return (
    <div className="flex min-w-[120px] flex-col gap-1">
      <span className="font-mono text-xs">
        {used.toFixed(1)} / {cap.toFixed(1)} {unit}
      </span>
      <div className="h-1.5 overflow-hidden rounded-full bg-off-soft">
        <div className={cn('h-full rounded-full', p >= 85 ? 'bg-bad' : p >= 70 ? 'bg-warn' : 'bg-ok')} style={{ width: `${p}%` }} />
      </div>
    </div>
  )
}

function running(n: NodeView) {
  return n.workloads.filter((w) => w.DesiredState === 'running' && w.Status !== 'stopped').length
}

export function NodesPage() {
  const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: api.nodes, refetchInterval: 3000 })
  const workloadsQ = useQuery({ queryKey: ['workloads'], queryFn: api.workloads, refetchInterval: 3000 })
  const nodes = useMemo(
    () => deriveNodes(nodesQ.data ?? [], workloadsQ.data ?? []).sort((a, b) => Number(b.isHost) - Number(a.isHost) || a.id.localeCompare(b.id)),
    [nodesQ.data, workloadsQ.data],
  )
  const schedulable = nodes.filter((n) => n.state === 'available' || n.state === 'throttling').length

  return (
    <main className="mx-auto flex max-w-[1180px] flex-col gap-6 p-6">
      <div>
        <h1 className="m-0 text-2xl font-semibold tracking-tight">Nodes</h1>
        <p className="m-0 mt-1 text-muted-foreground">
          {nodes.length} joined · {schedulable} taking new workloads
        </p>
      </div>
      <Card className="overflow-x-auto">
        <table className="w-full min-w-[1000px] border-collapse">
          <thead>
            <tr className="text-left text-xs text-muted-foreground">
              {['Node', 'State', 'CPU reserved', 'Memory reserved', 'RAM used', 'GPU', 'Thermal', 'Workloads', 'Heartbeat'].map((h) => (
                <th key={h} className="px-4 py-3 font-medium">{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {nodes.map((n) => (
              <tr key={n.id} className="border-t border-muted align-middle">
                <td className="px-4 py-3">
                  <Link to={`/nodes/${encodeURIComponent(n.id)}`} className="flex items-center gap-3 no-underline">
                    <OsMonitor os={n.os} size={36} dim={n.state === 'offline'} />
                    <span className="flex flex-col leading-tight">
                      <span className="whitespace-nowrap font-mono font-medium text-foreground underline">{n.name}</span>
                      <span className="whitespace-nowrap text-xs text-muted-foreground">
                        {n.name !== n.id && `${n.id} · `}{n.address} · {OS_LABEL[n.os]} · {n.arch}{n.isHost ? ' · host' : ''}
                      </span>
                    </span>
                  </Link>
                </td>
                <td className="px-4 py-3"><Badge tone={stateTone(n.state)}>{STATE_LABEL[n.state]}</Badge></td>
                <td className="px-4 py-3"><MiniBar used={n.cpuUsed} cap={n.cpuCap} unit="cores" /></td>
                <td className="px-4 py-3"><MiniBar used={n.memUsed} cap={n.memCap} unit="GiB" /></td>
                <td className="px-4 py-3">
                  {n.memLive === null ? <span className="text-muted-foreground">—</span> : <MiniBar used={n.memLive} cap={n.detected.mem} unit="GiB" />}
                </td>
                <td className="px-4 py-3 text-[13px]">{n.gpus.length ? n.gpus.map((g) => g.Model).join(', ') : <span className="text-muted-foreground">none</span>}</td>
                <td className="px-4 py-3 text-[13px]">{thermalLabel(n)}</td>
                <td className="px-4 py-3 font-mono">{running(n)}</td>
                <td className="px-4 py-3 font-mono text-[13px]">{n.heartbeatAgeSec === null ? 'never' : `${ageLabel(n.heartbeatAgeSec)} ago`}</td>
              </tr>
            ))}
            {nodes.length === 0 && (
              <tr className="border-t border-muted">
                <td colSpan={9} className="px-4 py-8 text-center text-muted-foreground">{nodesQ.isPending ? 'Loading…' : 'No nodes have joined yet.'}</td>
              </tr>
            )}
          </tbody>
        </table>
      </Card>
    </main>
  )
}
