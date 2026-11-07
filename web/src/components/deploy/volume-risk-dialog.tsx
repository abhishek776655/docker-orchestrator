import { useState } from 'react'
import { AlertTriangle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import type { VolumeRisk } from '@/lib/volume-risk'

interface Props {
  risks: VolumeRisk[]
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  error: string | null
  onReplan: () => void // keep volumes: put moved services back on their data's node
  onDeployAnyway: () => void
  replanLabel?: string
}

const WHY: Record<VolumeRisk['reason'], (r: VolumeRisk) => string> = {
  'node-down': (r) => `${r.from || 'Its data node'} is unavailable, so it starts on ${r.to} with an empty volume.`,
  moved: (r) => `Its data is on ${r.from}; this plan puts it on ${r.to} with an empty volume.`,
}

/** Asks before a deploy would start services on empty volumes. */
export function VolumeRiskDialog({ risks, open, onOpenChange, pending, error, onReplan, onDeployAnyway, replanLabel = 'Replan keeping volumes' }: Props) {
  const [accepted, setAccepted] = useState(false)
  const canKeep = risks.some((r) => r.reason === 'moved')
  const stuck = risks.filter((r) => r.reason === 'node-down')

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!pending) { onOpenChange(o); setAccepted(false) } }}>
      <DialogContent className="max-w-xl">
        <div className="flex gap-3">
          <AlertTriangle className="mt-0.5 size-5 flex-none text-warn-fg" aria-hidden="true" />
          <div>
            <DialogTitle className="m-0 text-lg font-semibold">
              {risks.length} {risks.length === 1 ? 'service starts' : 'services start'} with an empty volume
            </DialogTitle>
            <DialogDescription className="m-0 mt-1 text-sm text-muted-foreground">
              Their current data is not copied. It stays on the old node, unused.
            </DialogDescription>
          </div>
        </div>

        <ul className="m-0 flex max-h-[280px] list-none flex-col overflow-y-auto rounded-lg border border-border p-0">
          {risks.map((r) => (
            <li key={r.service} className="flex flex-col gap-0.5 border-b border-muted px-3.5 py-2.5 last:border-b-0">
              <span className="font-mono text-sm font-medium">{r.service}</span>
              <span className="text-[13px] text-muted-foreground">{WHY[r.reason](r)}</span>
            </li>
          ))}
        </ul>

        {stuck.length > 0 && (
          <p className="m-0 text-[13px] text-muted-foreground">
            {stuck.map((r) => r.service).join(', ')} cannot keep {stuck.length === 1 ? 'its' : 'their'} data until that node is back. Start its agent with{' '}
            <span className="font-mono">haco agent run</span>, then plan again.
          </p>
        )}

        <label className="flex items-start gap-2.5 text-sm">
          <input type="checkbox" className="mt-0.5 size-4" checked={accepted} onChange={(e) => setAccepted(e.target.checked)} />
          <span>I accept that {risks.length === 1 ? 'this service starts' : 'these services start'} without {risks.length === 1 ? 'its' : 'their'} current data.</span>
        </label>

        {error && <p role="alert" className="m-0 whitespace-pre-line rounded-md border border-bad/30 bg-bad-soft p-3 text-[13px] text-bad-fg">{error}</p>}

        <div className="flex flex-wrap justify-end gap-2">
          <Button type="button" variant="outline" disabled={pending} onClick={() => onOpenChange(false)}>Cancel</Button>
          {canKeep && (
            <Button type="button" variant="outline" disabled={pending} onClick={onReplan}>
              {replanLabel}
            </Button>
          )}
          <Button type="button" className="bg-bad text-white hover:bg-bad/90" disabled={!accepted || pending} onClick={onDeployAnyway}>
            {pending ? 'Working…' : 'Deploy anyway'}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
