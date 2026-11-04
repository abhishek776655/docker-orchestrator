import { useLayoutEffect, useRef, useState, type CSSProperties } from 'react'
import { Link } from 'react-router-dom'
import { Cpu, Thermometer } from 'lucide-react'
import { HoverCard, HoverCardContent, HoverCardTrigger } from '@/components/ui/hover-card'
import { Badge } from '@/components/ui/badge'
import { OsMonitor } from '@/components/os-monitor'
import { ControlPlaneOnlyDetail, NodeDetail } from '@/components/topology/node-detail'
import { STATE_LABEL, ageLabel, stateTone, type NodeState, type NodeView } from '@/lib/cluster'
import { cn } from '@/lib/utils'

const AGENT = { w: 176, h: 140 }
const HOST = { w: 216, h: 176 }
const HEIGHT = 640
// Below this width the ring cannot keep a readable gap between the host and
// the side cards, so nodes stack as a list instead.
const COMPACT_BELOW = 780
const MIN_RING_RX = HOST.w / 2 + AGENT.w / 2 + 100

// Where along the host-to-node line the label sits: halfway between the
// host card's edge and the node card's edge, so it never hides behind a card.
function labelFraction(dx: number, dy: number) {
  const exit = (halfW: number, halfH: number) => Math.min(halfW / Math.abs(dx || 1e-9), halfH / Math.abs(dy || 1e-9))
  const fromHost = exit(HOST.w / 2, HOST.h / 2)
  const toNode = 1 - exit(AGENT.w / 2, AGENT.h / 2)
  return (fromHost + toNode) / 2
}

const EDGE: Record<NodeState, { stroke: string; dash?: string }> = {
  available: { stroke: '#0b6b5d' },
  throttling: { stroke: '#b36b00' },
  cordoned: { stroke: '#8a8a93', dash: '6 6' },
  offline: { stroke: '#b3261e', dash: '6 6' },
}

interface Props {
  nodes: NodeView[]
  onAddNode: () => void
}

type Slot = { kind: 'node'; node: NodeView } | { kind: 'add' }

function NodeCard({ node, host = false, style, className }: { node: NodeView; host?: boolean; style?: CSSProperties; className?: string }) {
  const showThermal = node.thermal.Known
  return (
    <HoverCard openDelay={60} closeDelay={80}>
      <HoverCardTrigger asChild>
        <Link
          to={host ? '/deploy' : `/nodes/${node.id}`}
          style={style}
          className={cn(
            'flex flex-col items-center justify-center gap-1.5 rounded-xl border p-2.5 text-center no-underline outline-none transition-shadow',
            'focus-visible:ring-2 focus-visible:ring-primary hover:ring-2 hover:ring-primary',
            host ? 'border-primary bg-primary text-primary-foreground' : 'border-border bg-card text-foreground shadow-sm',
            className,
          )}
        >
          <OsMonitor os={node.os} tone={host ? 'inverse' : 'default'} size={host ? 64 : 52} dim={node.state === 'cordoned'} />
          {host && <span className="text-[11px] font-medium uppercase tracking-wider text-zinc-400">Host · control plane</span>}
          <span className="max-w-full truncate font-mono text-[13px] font-medium">{node.id}</span>
          <span className="flex flex-wrap items-center justify-center gap-1.5">
            <Badge tone={stateTone(node.state)}>● {STATE_LABEL[node.state]}</Badge>
            {showThermal && (
              <Thermometer
                className={cn('size-3.5', node.thermal.Throttling ? 'text-warn' : host ? 'text-zinc-300' : 'text-ok')}
                aria-label={node.thermal.Throttling ? 'Throttling' : 'Cool'}
              />
            )}
            {node.gpus.length > 0 && <Cpu className={cn('size-3.5', host ? 'text-zinc-300' : 'text-info')} aria-label="Has GPU" />}
          </span>
        </Link>
      </HoverCardTrigger>
      <HoverCardContent>
        <NodeDetail node={node} host={host} />
      </HoverCardContent>
    </HoverCard>
  )
}

function ControlPlaneCard({ style, className }: { style?: CSSProperties; className?: string }) {
  return (
    <HoverCard openDelay={60} closeDelay={80}>
      <HoverCardTrigger asChild>
        <Link
          to="/deploy"
          style={style}
          className={cn(
            'flex flex-col items-center justify-center gap-1.5 rounded-xl border border-primary bg-primary p-2.5 text-center text-primary-foreground no-underline outline-none hover:ring-2 hover:ring-primary focus-visible:ring-2 focus-visible:ring-primary',
            className,
          )}
        >
          <OsMonitor os="unknown" tone="inverse" size={64} />
          <span className="text-[11px] font-medium uppercase tracking-wider text-zinc-400">Host</span>
          <span className="font-mono text-[13px] font-medium">Control plane</span>
        </Link>
      </HoverCardTrigger>
      <HoverCardContent>
        <ControlPlaneOnlyDetail />
      </HoverCardContent>
    </HoverCard>
  )
}

function AddCard({ onClick, style, className }: { onClick: () => void; style?: CSSProperties; className?: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      style={style}
      className={cn(
        'flex flex-col items-center justify-center gap-1.5 rounded-xl border border-dashed border-off bg-card p-2.5 text-center outline-none hover:ring-2 hover:ring-primary focus-visible:ring-2 focus-visible:ring-primary',
        className,
      )}
    >
      <OsMonitor os="add" tone="muted" size={52} />
      <span className="text-[13px] font-semibold">+ Add node</span>
      <span className="text-xs text-muted-foreground">Issue a join token</span>
    </button>
  )
}

export function Topology({ nodes, onAddNode }: Props) {
  const ref = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(0)

  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const observer = new ResizeObserver((entries) => setWidth(entries[0].contentRect.width))
    observer.observe(el)
    setWidth(el.clientWidth)
    return () => observer.disconnect()
  }, [])

  const host = nodes.find((n) => n.isHost) ?? null
  const agents = nodes.filter((n) => !n.isHost)
  const slots: Slot[] = [...agents.map((node): Slot => ({ kind: 'node', node })), { kind: 'add' }]
  const compact = width > 0 && width < COMPACT_BELOW

  if (compact) {
    return (
      <div ref={ref} className="dot-grid flex flex-col gap-3 p-4">
        {host ? <NodeCard node={host} host className="w-full" /> : <ControlPlaneCard className="w-full" />}
        {slots.map((slot) =>
          slot.kind === 'node' ? <NodeCard key={slot.node.id} node={slot.node} className="w-full" /> : <AddCard key="add" onClick={onAddNode} className="w-full" />,
        )}
      </div>
    )
  }

  const cx = width / 2
  const cy = HEIGHT / 2
  const rx = Math.max(width / 2 - AGENT.w / 2 - 24, MIN_RING_RX)
  const ry = HEIGHT / 2 - AGENT.h / 2 - 24
  const placed = slots.map((slot, i) => {
    const angle = -Math.PI / 2 + (2 * Math.PI * i) / slots.length
    return { slot, x: cx + rx * Math.cos(angle), y: cy + ry * Math.sin(angle) }
  })

  return (
    <div ref={ref} className="dot-grid relative overflow-hidden" style={{ height: HEIGHT }}>
      {width > 0 && (
        <>
          <svg className="absolute inset-0 size-full" aria-hidden="true">
            {placed.map(({ slot, x, y }) => {
              const style = slot.kind === 'node' ? EDGE[slot.node.state] : { stroke: '#8a8a93', dash: '6 6' }
              return (
                <line
                  key={slot.kind === 'node' ? slot.node.id : 'add'}
                  x1={cx}
                  y1={cy}
                  x2={x}
                  y2={y}
                  stroke={style.stroke}
                  strokeWidth={2}
                  strokeLinecap="round"
                  strokeDasharray={style.dash}
                />
              )
            })}
          </svg>
          {placed.map(({ slot, x, y }) => {
            const t = labelFraction(x - cx, y - cy)
            const label = slot.kind === 'node' ? `hb ${ageLabel(slot.node.heartbeatAgeSec)}` : 'join token'
            return (
              <span
                key={slot.kind === 'node' ? `l-${slot.node.id}` : 'l-add'}
                className="pointer-events-none absolute -translate-x-1/2 -translate-y-1/2 rounded bg-background px-1.5 text-[13px] leading-4 text-muted-foreground"
                style={{ left: cx + (x - cx) * t, top: cy + (y - cy) * t }}
              >
                {label}
              </span>
            )
          })}
          {host ? (
            <NodeCard node={host} host style={{ position: 'absolute', left: cx - HOST.w / 2, top: cy - HOST.h / 2, width: HOST.w, height: HOST.h }} />
          ) : (
            <ControlPlaneCard style={{ position: 'absolute', left: cx - HOST.w / 2, top: cy - HOST.h / 2, width: HOST.w, height: HOST.h }} />
          )}
          {placed.map(({ slot, x, y }) => {
            const style: CSSProperties = { position: 'absolute', left: x - AGENT.w / 2, top: y - AGENT.h / 2, width: AGENT.w, height: AGENT.h }
            return slot.kind === 'node' ? <NodeCard key={slot.node.id} node={slot.node} style={style} /> : <AddCard key="add" onClick={onAddNode} style={style} />
          })}
        </>
      )}
    </div>
  )
}
