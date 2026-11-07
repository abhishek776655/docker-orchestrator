import { useMemo, useState } from 'react'
import { edgeOnPath, layoutGraph, NODE_H, NODE_W, traceFrom, type GraphItem } from '@/lib/graph-layout'
import type { LiveState } from '@/lib/plan'
import { cn } from '@/lib/utils'

export interface GraphNodeInfo extends GraphItem {
  line: string // second line on the card: the node it runs on, or its live state
  state: LiveState
}

const PILL: Record<LiveState, string> = {
  ready: 'bg-ok-soft text-ok-fg',
  progress: 'bg-warn-soft text-warn-fg',
  failed: 'bg-bad-soft text-bad-fg',
  waiting: 'bg-off-soft text-off-fg',
  stopped: 'bg-off-soft text-off-fg',
  planned: 'text-muted-foreground',
}

// The start-order graph. Hover or focus a service to light the path of
// everything it waits for and everything that waits for it; click to pin.
export function DependencyGraph({ nodes }: { nodes: GraphNodeInfo[] }) {
  const [hover, setHover] = useState<string | null>(null)
  const [pinned, setPinned] = useState<string | null>(null)
  const target = hover ?? pinned
  const layout = useMemo(() => layoutGraph(nodes), [nodes])
  const trace = useMemo(() => (target ? traceFrom(nodes, target) : null), [nodes, target])
  const stateOf = new Map(nodes.map((n) => [n.name, n.state]))

  const onPath = (n: string) => !!trace && !!target && (n === target || trace.up.has(n) || trace.down.has(n))
  const edges = layout.edges
    .map((e) => ({ e, hot: !!trace && !!target && edgeOnPath(e, target, trace) }))
    .sort((a, b) => Number(a.hot) - Number(b.hot)) // highlighted edges paint last

  const caption = trace && target
    ? `${target} waits for ${trace.up.size ? `${trace.up.size} (${[...trace.up].join(', ')})` : 'nothing'}. ${
        trace.down.size ? `${trace.down.size} wait for it (${[...trace.down].join(', ')}).` : 'Nothing waits for it.'
      }`
    : 'Hover or tab to a service to trace what it waits for and what waits for it. Click to pin it.'

  return (
    <div className="overflow-hidden rounded-[10px] border border-border bg-card">
      <div role="status" aria-live="polite" className="flex min-h-11 items-center border-b border-border px-4 py-2.5 text-[13px] text-zinc-700">
        {caption}
      </div>
      <div className="dot-grid max-h-[640px] overflow-auto">
        <div className="relative" style={{ width: layout.width, height: layout.height }}>
          <svg className="absolute inset-0" width={layout.width} height={layout.height} aria-hidden="true">
            {edges.map(({ e, hot }) => {
              const failedParent = stateOf.get(e.from) === 'failed'
              const color = failedParent ? '#B3261E' : hot ? '#18181B' : '#71717A'
              const opacity = !target || hot ? 1 : 0.12
              return (
                <g key={`${e.from}>${e.to}`} opacity={opacity} style={{ transition: 'opacity 0.12s' }}>
                  <polyline
                    points={e.points.map((p) => p.join(',')).join(' ')}
                    fill="none"
                    stroke={color}
                    strokeWidth={hot ? 3 : 2}
                    strokeDasharray={e.healthy ? undefined : '6 6'}
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  />
                  <polygon points={e.head.map((p) => p.join(',')).join(' ')} fill={color} />
                </g>
              )
            })}
          </svg>
          {layout.labels.map((l) => (
            <div key={l.text} className="absolute top-2.5 text-xs font-medium uppercase tracking-wider text-muted-foreground" style={{ left: l.left }}>
              {l.text}
            </div>
          ))}
          {nodes.map((n) => {
            const pos = layout.nodes[n.name]
            const failed = n.state === 'failed'
            return (
              <button
                key={n.name}
                type="button"
                title={`${n.name}: ${n.line}`}
                aria-pressed={pinned === n.name}
                onMouseEnter={() => setHover(n.name)}
                onMouseLeave={() => setHover(null)}
                onFocus={() => setHover(n.name)}
                onBlur={() => setHover(null)}
                onClick={() => setPinned(pinned === n.name ? null : n.name)}
                className={cn(
                  'absolute flex items-center rounded-[10px] border bg-card px-3 py-2 text-left transition-opacity',
                  failed ? 'border-bad' : onPath(n.name) ? 'border-primary' : 'border-border',
                  n.name === target && 'ring-[3px] ring-primary/20',
                )}
                style={{ left: pos.left, top: pos.top, width: NODE_W, height: NODE_H, opacity: !target || onPath(n.name) ? 1 : 0.35 }}
              >
                <span className="min-w-0 leading-[22px]">
                  <span className="block truncate font-mono text-[13px] font-medium">{n.name}</span>
                  <span className={cn('inline-block rounded-full text-[11px] font-medium', n.state === 'planned' ? 'px-0' : 'px-2 py-0.5', PILL[n.state])}>{n.line}</span>
                </span>
              </button>
            )
          })}
        </div>
      </div>
    </div>
  )
}
