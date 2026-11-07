import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { ErrorBoundary } from '@/components/error-boundary'
import { useQuery } from '@tanstack/react-query'
import { Activity, Layers, Network, Server, Upload, type LucideIcon } from 'lucide-react'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'

const PLATFORM: { to: string; label: string; icon: LucideIcon; end?: boolean }[] = [
  { to: '/', label: 'Cluster', icon: Network, end: true },
  { to: '/nodes', label: 'Nodes', icon: Server },
  { to: '/projects', label: 'Projects', icon: Layers },
  { to: '/deploy', label: 'Deploy', icon: Upload },
]
const OPERATE: typeof PLATFORM = [{ to: '/events', label: 'Events', icon: Activity }]

function NavItem({ to, label, icon: Icon, end }: (typeof PLATFORM)[number]) {
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) =>
        cn(
          'flex min-h-11 items-center gap-2.5 rounded-md px-2.5 text-sm text-zinc-700 hover:bg-muted',
          isActive && 'bg-muted font-medium text-foreground',
        )
      }
    >
      <Icon className="size-4" aria-hidden="true" />
      {label}
    </NavLink>
  )
}

function NavGroup({ title, items }: { title: string; items: typeof PLATFORM }) {
  return (
    <nav aria-label={title} className="flex flex-col gap-0.5">
      <div className="px-2 py-1 text-xs font-medium text-muted-foreground">{title}</div>
      {items.map((item) => (
        <NavItem key={item.to} {...item} />
      ))}
    </nav>
  )
}

function ConnectionCard() {
  const nodes = useQuery({ queryKey: ['nodes'], queryFn: api.nodes, refetchInterval: 3000 })
  const up = !nodes.isError
  return (
    <div className="flex items-center gap-2.5 rounded-lg border border-border px-3 py-2.5">
      <span aria-hidden="true" className={cn('size-2 shrink-0 rounded-full', up ? 'bg-ok' : 'bg-bad')} />
      <div className="flex flex-col leading-tight">
        <span className="text-[13px] font-medium">Control plane</span>
        <span className="font-mono text-xs text-muted-foreground">{up ? 'connected' : 'unreachable'}</span>
      </div>
    </div>
  )
}

const TITLES: Record<string, string> = {
  '/': 'Cluster',
  '/nodes': 'Nodes',
  '/projects': 'Projects',
  '/deploy': 'Deploy',
  '/events': 'Events',
}

export function AppShell() {
  const { pathname } = useLocation()
  const title = TITLES[pathname] ?? (pathname.startsWith('/nodes/') ? 'Nodes' : pathname.startsWith('/projects/') ? 'Projects' : 'haco')
  return (
    <div className="flex min-h-screen flex-wrap items-stretch">
      <aside className="flex min-w-0 flex-[0_0_256px] flex-col md:sticky md:top-0 md:h-screen md:self-start md:overflow-y-auto gap-5 border-r border-border bg-card p-3 max-md:flex-[1_1_100%] max-md:border-b max-md:border-r-0">
        <NavLink to="/" className="flex items-center gap-2.5 px-2 py-1.5">
          <span
            aria-hidden="true"
            className="flex size-8 items-center justify-center rounded-lg bg-primary font-mono text-sm font-medium text-primary-foreground"
          >
            h/
          </span>
          <span className="flex flex-col leading-tight">
            <span className="text-sm font-semibold">haco</span>
            <span className="text-xs text-muted-foreground">Orchestrator console</span>
          </span>
        </NavLink>
        <NavGroup title="Platform" items={PLATFORM} />
        <NavGroup title="Operate" items={OPERATE} />
        <div className="mt-auto flex flex-col gap-2">
          <ConnectionCard />
        </div>
      </aside>
      <div className="min-w-0 flex-[1_1_640px]">
        <header className="flex min-h-14 items-center border-b border-border bg-card px-6 text-sm text-muted-foreground">
          Platform <span aria-hidden="true">&nbsp;/&nbsp;</span>
          <span className="font-medium text-foreground">{title}</span>
        </header>
        <ErrorBoundary resetKey={pathname}>
          <Outlet />
        </ErrorBoundary>
      </div>
    </div>
  )
}
