import { useEffect, useRef, useState } from 'react'
import { ChevronDown } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

interface Option {
  label: string
  description?: string
  onSelect: () => void
}

interface Props {
  label: string
  onClick: () => void
  options: Option[] // shown when the arrow is clicked
  disabled?: boolean
  title?: string
}

/** A main action with an arrow that reveals related actions. */
export function SplitButton({ label, onClick, options, disabled, title }: Props) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

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

  return (
    <div ref={ref} className="relative inline-flex">
      <Button type="button" className="rounded-r-none" disabled={disabled} title={title} onClick={onClick}>
        {label}
      </Button>
      <Button
        type="button"
        className="rounded-l-none border-l border-primary-foreground/25 px-2.5"
        disabled={disabled}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`More ${label.toLowerCase()} options`}
        onClick={() => setOpen((o) => !o)}
      >
        <ChevronDown className={cn('size-4 transition-transform', open && 'rotate-180')} aria-hidden="true" />
      </Button>
      {open && (
        <div role="menu" className="absolute right-0 top-full z-20 mt-1 w-72 overflow-hidden rounded-lg border border-border bg-card shadow-lg">
          {options.map((o) => (
            <button
              key={o.label}
              type="button"
              role="menuitem"
              onClick={() => {
                setOpen(false)
                o.onSelect()
              }}
              className="flex w-full flex-col gap-0.5 px-3.5 py-2.5 text-left hover:bg-muted"
            >
              <span className="text-sm font-medium">{o.label}</span>
              {o.description && <span className="text-[13px] text-muted-foreground">{o.description}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
