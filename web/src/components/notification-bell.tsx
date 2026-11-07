import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Bell } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { api } from '@/lib/api'
import { isAlert, newestId, unseenCount } from '@/lib/attention'
import { eventTone, sinceLabel } from '@/lib/cluster'

const KEY = 'haco.events.seenId'
let memorySeen: number | null = null // when storage is unavailable

function readSeen(): number | null {
  try {
    const v = localStorage.getItem(KEY)
    return v === null ? memorySeen : Number(v)
  } catch {
    return memorySeen
  }
}

function writeSeen(id: number) {
  memorySeen = id
  try {
    localStorage.setItem(KEY, String(id))
  } catch {
    // keep it in memory for this session
  }
}

// New failures and warnings since the operator last opened the list.
export function NotificationBell() {
  const eventsQ = useQuery({ queryKey: ['events', 200], queryFn: () => api.events(200), refetchInterval: 5000 })
  const events = eventsQ.data ?? []
  const [seen, setSeen] = useState<number | null>(readSeen)
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  // First visit: start from now, so old history does not flood the badge.
  useEffect(() => {
    if (seen === null && eventsQ.data) {
      const id = newestId(eventsQ.data)
      writeSeen(id)
      setSeen(id)
    }
  }, [seen, eventsQ.data])

  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => !ref.current?.contains(e.target as Node) && setOpen(false)
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    document.addEventListener('mousedown', onDoc)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDoc)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  const count = seen === null ? 0 : unseenCount(events, seen)
  const recent = events.filter(isAlert).slice(0, 8)

  function toggle() {
    if (!open && events.length) {
      const id = newestId(events)
      writeSeen(id)
      setSeen(id)
    }
    setOpen((o) => !o)
  }

  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        onClick={toggle}
        aria-expanded={open}
        aria-label={count ? `Notifications, ${count} new` : 'Notifications'}
        className="relative flex size-11 items-center justify-center rounded-lg border border-border bg-card text-zinc-700 hover:bg-muted"
      >
        <Bell className="size-[18px]" aria-hidden="true" />
        {count > 0 && (
          <span className="absolute -right-1.5 -top-1.5 flex h-[18px] min-w-[18px] items-center justify-center rounded-full bg-bad px-1.5 text-[11px] font-semibold text-white">
            {count > 9 ? '9+' : count}
          </span>
        )}
      </button>
      {open && (
        <div className="absolute right-0 top-full z-30 mt-2 w-[min(380px,calc(100vw-32px))] overflow-hidden rounded-lg border border-border bg-card shadow-lg">
          <div className="border-b border-border px-4 py-2.5 text-sm font-semibold">Recent problems</div>
          <ul className="m-0 max-h-[360px] list-none overflow-y-auto p-0">
            {recent.map((e) => (
              <li key={e.ID} className="flex flex-col gap-1 border-b border-muted px-4 py-2.5 last:border-b-0">
                <div className="flex flex-wrap items-center gap-2 text-xs">
                  <Badge tone={eventTone(e)} className="rounded">{e.Type}</Badge>
                  <span className="font-mono text-muted-foreground">{sinceLabel(e.CreatedAt)}</span>
                  <span className="truncate font-mono text-muted-foreground">{e.WorkloadKey || e.NodeID}</span>
                </div>
                <span className="text-[13px] [overflow-wrap:anywhere]">{e.Message}</span>
              </li>
            ))}
            {recent.length === 0 && <li className="px-4 py-6 text-center text-[13px] text-muted-foreground">No failures or warnings recently.</li>}
          </ul>
          <Link to="/events" onClick={() => setOpen(false)} className="block border-t border-border px-4 py-2.5 text-center text-[13px] font-medium underline">
            View all events
          </Link>
        </div>
      )}
    </div>
  )
}
