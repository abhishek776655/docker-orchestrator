import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, ArrowRight } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { api, type ApiNode } from '@/lib/api'
import { cn } from '@/lib/utils'

interface Props {
  project: string
  service: string
  revision: string
  currentNode: string
  stateful: boolean // keeps data in a node-local volume
  dependents: string[] // restarted so they pick up the new address
  nodes: ApiNode[]
  open: boolean
  onOpenChange: (open: boolean) => void
  onMoved: (message: string) => void
}

/**
 * Moves one service to another node. Its container is removed from the old
 * node and started on the new one; the services that depend on it restart so
 * their address for it points at the new node.
 */
export function MoveDialog({ project, service, revision, currentNode, stateful, dependents, nodes, open, onOpenChange, onMoved }: Props) {
  const queryClient = useQueryClient()
  const [target, setTarget] = useState('')
  const [emptyVolume, setEmptyVolume] = useState(false)
  const [force, setForce] = useState(false)
  const others = nodes.filter((n) => n.ID !== currentNode)
  // The name people gave a node, else its id.
  const label = (id: string) => nodes.find((n) => n.ID === id)?.DisplayName || id

  const move = useMutation({
    mutationFn: () => api.moveWorkload(project, service, revision, { node: target, force, acceptEmptyVolume: emptyVolume }),
    onSuccess: async (r) => {
      await queryClient.invalidateQueries({ queryKey: ['workloads'] })
      setTarget('')
      setEmptyVolume(false)
      setForce(false)
      onMoved(
        `Moving ${service} from ${r.from} to ${r.to}.${r.restarted.length ? ` Restarting ${r.restarted.join(', ')} so they use its new address.` : ''}`,
      )
    },
  })
  const message = move.error?.message ?? ''
  const rejected = move.isError && message.includes('no eligible node')
  const choose = (id: string) => {
    move.reset()
    setForce(false)
    setTarget(id)
  }
  const ready = !!target && (!stateful || emptyVolume) && !move.isPending

  return (
    <Dialog open={open} onOpenChange={(o) => !move.isPending && onOpenChange(o)}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-xl gap-0 p-0">
        <div className="flex-none px-6 pb-3 pt-6 pr-14">
          <DialogTitle className="m-0 text-lg font-semibold">Move {service} to another node</DialogTitle>
          <DialogDescription className="m-0 mt-1 text-sm text-muted-foreground">
            Stops it on <span className="font-mono">{label(currentNode)}</span> and starts it on the node you pick.
          </DialogDescription>
        </div>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-6 pb-4 pt-1">
          <fieldset className="m-0 flex flex-col gap-1.5 border-0 p-0">
            <legend className="mb-1 text-xs text-muted-foreground">Move to</legend>
            {others.length === 0 && <p className="m-0 text-[13px] text-muted-foreground">There is no other node in this cluster.</p>}
            {others.map((n) => (
              <label key={n.ID} className={cn('flex min-h-11 cursor-pointer items-center gap-2.5 rounded-md border px-3 text-[13px]', target === n.ID ? 'border-ok bg-ok-soft/40' : 'border-border')}>
                <input type="radio" name="move-target" checked={target === n.ID} onChange={() => choose(n.ID)} className="size-4" />
                <span className="font-mono font-medium">{n.DisplayName || n.ID}</span>
                <span className="font-mono text-xs text-muted-foreground">{n.Address}</span>
                <Badge tone={n.Availability === 'available' ? 'ok' : 'bad'} className="ml-auto">{n.Availability}</Badge>
              </label>
            ))}
          </fieldset>

          {target && (
            <div className="flex flex-col gap-2 rounded-lg border border-border bg-card p-3.5 text-[13px]">
              <span className="flex items-center gap-2 font-mono">
                {label(currentNode)} <ArrowRight className="size-4 text-muted-foreground" aria-hidden="true" /> {label(target)}
              </span>
              <span className="text-muted-foreground">The old container is removed once the node sees it is no longer assigned there. For a few seconds both can exist.</span>
              {dependents.length > 0 ? (
                <span>
                  <strong className="font-medium">{dependents.join(', ')}</strong> restart{dependents.length === 1 ? 's' : ''} so {dependents.length === 1 ? 'it uses' : 'they use'} the new address. They are briefly unavailable.
                </span>
              ) : (
                <span className="text-muted-foreground">Nothing in this project depends on it, so nothing else restarts.</span>
              )}
              <span className="text-muted-foreground">A service that reaches it without listing it in depends_on keeps the old address until it is restarted.</span>
            </div>
          )}

          {stateful && target && (
            <div className="flex flex-col gap-2 rounded-lg border border-warn/40 bg-warn-soft p-3 text-[13px] text-warn-fg">
              <span className="flex items-start gap-2">
                <AlertTriangle className="mt-0.5 size-4 flex-none" aria-hidden="true" />
                <span>
                  It keeps data in a volume on <span className="font-mono">{label(currentNode)}</span>. The data is not copied: on <span className="font-mono">{label(target)}</span> it starts with an empty volume. The old data stays on {label(currentNode)}, unused.
                </span>
              </span>
              <label className="flex items-start gap-2.5">
                <input type="checkbox" className="mt-0.5 size-4" checked={emptyVolume} onChange={(e) => setEmptyVolume(e.target.checked)} />
                <span>Start it with an empty volume on {label(target)}.</span>
              </label>
            </div>
          )}

          {move.isError && (
            <div role="alert" className="flex flex-col gap-2 rounded-md border border-bad/30 bg-bad-soft p-3 text-[13px] text-bad-fg">
              <span className="whitespace-pre-line">{message}</span>
              {rejected && (
                <label className="flex items-start gap-2.5 text-foreground">
                  <input type="checkbox" className="mt-0.5 size-4" checked={force} onChange={(e) => setForce(e.target.checked)} />
                  <span>Move it there anyway. It may fail to start, for example if the port really is taken.</span>
                </label>
              )}
            </div>
          )}
        </div>

        <div className="flex flex-none flex-wrap justify-end gap-2 border-t border-border px-6 py-4">
          <Button type="button" variant="outline" disabled={move.isPending} onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="button" disabled={!ready || (rejected && !force)} onClick={() => move.mutate()}>
            {move.isPending ? 'Moving…' : rejected && force ? 'Move anyway' : 'Move service'}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
