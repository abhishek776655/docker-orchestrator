import { ChevronLeft, ChevronRight } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { Page } from '@/lib/paginate'

const SIZES = [25, 50, 100]

interface Props {
  page: Page<unknown>
  size: number
  noun: string
  onPage: (page: number) => void
  onSize: (size: number) => void
}

export function Pager({ page, size, noun, onPage, onSize }: Props) {
  return (
    <nav aria-label={`${noun} pages`} className="flex flex-wrap items-center justify-between gap-3 border-t border-border px-4 py-3 text-[13px]">
      <span className="text-muted-foreground">
        {page.total ? `${page.from}–${page.to} of ${page.total} ${noun}` : `No ${noun}`}
      </span>
      <span className="flex flex-wrap items-center gap-2">
        <label className="flex items-center gap-2 text-muted-foreground">
          Per page
          <select value={size} onChange={(e) => onSize(Number(e.target.value))} className="min-h-11 rounded-md border border-zinc-300 bg-card px-2 text-foreground">
            {SIZES.map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </label>
        <Button variant="outline" className="min-h-11 px-3" disabled={page.page <= 1} onClick={() => onPage(page.page - 1)} aria-label="Previous page">
          <ChevronLeft className="size-4" aria-hidden="true" />
        </Button>
        <span className="min-w-[96px] text-center">Page {page.page} of {page.pages}</span>
        <Button variant="outline" className="min-h-11 px-3" disabled={page.page >= page.pages} onClick={() => onPage(page.page + 1)} aria-label="Next page">
          <ChevronRight className="size-4" aria-hidden="true" />
        </Button>
      </span>
    </nav>
  )
}
