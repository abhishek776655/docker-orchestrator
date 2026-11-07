import { useId, useState } from 'react'
import { cn } from '@/lib/utils'

export interface ServiceItem {
  name: string
  note?: string
  disabled?: boolean
}

interface Props {
  label: string
  items: ServiceItem[]
  selected: string[]
  mode: 'multi' | 'single'
  onChange: (selected: string[]) => void
}

// A searchable list of services to tick (multi) or choose between (single).
// It scrolls inside a fixed height, so it stays usable with hundreds of
// services.
export function ServiceList({ label, items, selected, mode, onChange }: Props) {
  const [query, setQuery] = useState('')
  const group = useId()
  const q = query.trim().toLowerCase()
  const shown = q ? items.filter((i) => i.name.toLowerCase().includes(q)) : items

  function toggle(name: string) {
    if (mode === 'single') onChange([name])
    else onChange(selected.includes(name) ? selected.filter((s) => s !== name) : [...selected, name])
  }

  return (
    <div className="flex flex-col gap-2.5">
      <input
        type="search"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder="Search services"
        aria-label={`Search ${label}`}
        className="min-h-11 rounded-md border border-zinc-300 bg-card px-3 text-sm"
      />
      <div role={mode === 'single' ? 'radiogroup' : 'group'} aria-label={label} className="grid max-h-64 grid-cols-[repeat(auto-fill,minmax(220px,1fr))] overflow-y-auto rounded-lg border border-border">
        {shown.map((item) => {
          const checked = selected.includes(item.name)
          return (
            <label
              key={item.name}
              className={cn('flex min-h-12 cursor-pointer items-center gap-2.5 border-b border-muted px-3', item.disabled && 'cursor-not-allowed opacity-55')}
            >
              <input
                type={mode === 'single' ? 'radio' : 'checkbox'}
                name={group}
                checked={checked}
                disabled={item.disabled}
                onChange={() => toggle(item.name)}
              />
              <span className="flex min-w-0 flex-col leading-snug">
                <span className="truncate font-mono text-[13px]">{item.name}</span>
                {item.note && <span className="text-[11px] text-muted-foreground">{item.note}</span>}
              </span>
            </label>
          )
        })}
        {shown.length === 0 && <div className="col-span-full p-4 text-center text-[13px] text-muted-foreground">No services match your search.</div>}
      </div>
    </div>
  )
}
