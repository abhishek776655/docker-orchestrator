import { cn } from '@/lib/utils'
import type { OsKind } from '@/lib/cluster'

export type MonitorTone = 'default' | 'inverse' | 'muted'

const PALETTE: Record<MonitorTone, { frame: string; screen: string; apple: string; windows: string; body: string; belly: string; accent: string; plus: string }> = {
  default: { frame: '#18181b', screen: '#27272a', apple: '#fafafa', windows: '#4da3ff', body: '#71717a', belly: '#fafafa', accent: '#f5a623', plus: '#fafafa' },
  inverse: { frame: '#fafafa', screen: '#27272a', apple: '#fafafa', windows: '#4da3ff', body: '#71717a', belly: '#fafafa', accent: '#f5a623', plus: '#fafafa' },
  muted: { frame: '#a1a1aa', screen: '#e4e4e7', apple: '#71717a', windows: '#71717a', body: '#71717a', belly: '#fafafa', accent: '#a1a1aa', plus: '#63636b' },
}

const APPLE =
  'M12.152 6.896c-.948 0-2.415-1.078-3.96-1.04-2.04.027-3.91 1.183-4.961 3.014-2.117 3.675-.546 9.103 1.519 12.09 1.013 1.454 2.208 3.09 3.792 3.039 1.52-.065 2.09-.987 3.935-.987 1.831 0 2.35.987 3.96.948 1.637-.026 2.676-1.48 3.676-2.948 1.156-1.688 1.636-3.325 1.662-3.415-.039-.013-3.182-1.221-3.22-4.857-.026-3.04 2.48-4.494 2.597-4.559-1.429-2.09-3.623-2.324-4.39-2.376-2-.156-3.675 1.09-4.61 1.09zM15.53 3.83c.843-1.012 1.4-2.427 1.245-3.83-1.207.052-2.662.805-3.532 1.818-.78.896-1.454 2.338-1.273 3.714 1.338.104 2.715-.688 3.559-1.701'

const LABEL: Record<OsKind | 'add', string> = {
  darwin: 'macOS',
  linux: 'Linux',
  windows: 'Windows',
  unknown: 'Unknown OS',
  add: 'New machine',
}

interface Props {
  os: OsKind | 'add'
  tone?: MonitorTone
  size?: number
  dim?: boolean
  className?: string
}

// A desktop monitor with the OS mark on its screen. The marks are simplified
// stand-ins; swap in official brand assets before shipping.
export function OsMonitor({ os, tone = 'default', size = 56, dim = false, className }: Props) {
  const c = PALETTE[tone]
  return (
    <svg
      role="img"
      aria-label={LABEL[os]}
      width={size}
      height={(size * 52) / 56}
      viewBox="0 0 56 52"
      fill="none"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={cn('shrink-0', className)}
    >
      <rect x="2" y="2" width="52" height="34" rx="4" fill={c.screen} stroke={c.frame} strokeWidth="2.5" strokeDasharray={os === 'add' ? '5 4' : undefined} />
      <path d="M20 47h16M28 36v11" stroke={c.frame} strokeWidth="2.5" />
      <g opacity={dim ? 0.55 : 1} stroke="none">
        {os === 'darwin' && (
          <g transform="translate(18.4 9.4) scale(0.8)" fill={c.apple}>
            <path d={APPLE} />
          </g>
        )}
        {os === 'windows' && (
          <g fill={c.windows}>
            <rect x="19" y="10" width="8" height="8" rx="0.5" />
            <rect x="29" y="10" width="8" height="8" rx="0.5" />
            <rect x="19" y="20" width="8" height="8" rx="0.5" />
            <rect x="29" y="20" width="8" height="8" rx="0.5" />
          </g>
        )}
        {os === 'linux' && (
          <g transform="translate(16 5.5)">
            <ellipse cx="12" cy="13" rx="7" ry="9.5" fill={c.body} />
            <ellipse cx="12" cy="15" rx="4.2" ry="6.5" fill={c.belly} />
            <circle cx="10" cy="8" r="1.1" fill={c.belly} />
            <circle cx="14" cy="8" r="1.1" fill={c.belly} />
            <path d="M10 10.5 L12 12.5 L14 10.5 Q12 9.5 10 10.5Z" fill={c.accent} />
            <ellipse cx="8.5" cy="22.5" rx="3" ry="1.5" fill={c.accent} />
            <ellipse cx="15.5" cy="22.5" rx="3" ry="1.5" fill={c.accent} />
          </g>
        )}
        {os === 'add' && <path d="M28 12v14M21 19h14" stroke={c.plus} strokeWidth="3" />}
      </g>
    </svg>
  )
}
