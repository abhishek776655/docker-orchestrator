import type { ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  children: ReactNode
  confirmLabel: string
  pending: boolean
  error: string | null
  onConfirm: () => void
  danger?: boolean // red confirm button for destructive actions (default)
  confirmDisabled?: boolean
}

/** A yes/no question before something that cannot be undone. */
export function ConfirmDialog({ open, onOpenChange, title, children, confirmLabel, pending, error, onConfirm, danger = true, confirmDisabled = false }: Props) {
  return (
    <Dialog open={open} onOpenChange={(next) => !pending && onOpenChange(next)}>
      <DialogContent>
        <div>
          <DialogTitle className="m-0 text-lg font-semibold">{title}</DialogTitle>
          <DialogDescription asChild>
            <div className="mt-1 text-sm text-muted-foreground">{children}</div>
          </DialogDescription>
        </div>
        {error && (
          <p role="alert" className="m-0 whitespace-pre-line rounded-md border border-bad/30 bg-bad-soft p-3 text-[13px] text-bad-fg">
            {error}
          </p>
        )}
        <div className="flex flex-wrap justify-end gap-2">
          <Button type="button" variant="outline" disabled={pending} onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="button" className={danger ? 'bg-bad text-white hover:bg-bad/90' : undefined} disabled={pending || confirmDisabled} onClick={onConfirm}>
            {pending ? 'Working…' : confirmLabel}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
