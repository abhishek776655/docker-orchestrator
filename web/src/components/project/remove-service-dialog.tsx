import { useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Search } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { api, type ApiWorkload } from '@/lib/api'
import { removalPlan } from '@/lib/remove-service'
import { cn } from '@/lib/utils'

interface Props {
  project: string
  workloads: ApiWorkload[] // this project's
  open: boolean
  onOpenChange: (open: boolean) => void
  onRemoved: (message: string) => void
}

/**
 * Removes services from a project. A service that depends on a removed one
 * would wait for it forever, so those go too; the dialog says which before
 * anything is deleted.
 */
export function RemoveServiceDialog({ project, workloads, open, onOpenChange, onRemoved }: Props) {
  const queryClient = useQueryClient()
  const [picked, setPicked] = useState<string[]>([])
  const [query, setQuery] = useState('')
  const services = useMemo(() => [...new Set(workloads.map((w) => w.Service))], [workloads])
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase()
    return q ? services.filter((n) => n.toLowerCase().includes(q)) : services
  }, [services, query])
  const plan = useMemo(() => removalPlan(workloads, picked), [workloads, picked])
  const total = plan.remove.length + plan.alsoRemoved.length

  const remove = useMutation({
    mutationFn: async () => {
      const doomed = new Set([...plan.remove, ...plan.alsoRemoved])
      const targets = workloads.filter((w) => doomed.has(w.Service))
      const results = await Promise.allSettled(targets.map((w) => api.removeWorkload(w.Project, w.Service, w.Revision)))
      const errors = results.flatMap((r, i) => (r.status === 'rejected' ? [`${targets[i].Service}: ${(r.reason as Error).message}`] : []))
      if (errors.length) throw new Error(errors.join('\n'))
      return doomed.size
    },
    onSuccess: async (n) => {
      await queryClient.invalidateQueries({ queryKey: ['workloads'] })
      setPicked([])
      setQuery('')
      onRemoved(`Removed ${n} ${n === 1 ? 'service' : 'services'} from ${project}.`)
    },
  })
  const toggle = (name: string) => {
    remove.reset()
    setPicked((cur) => (cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name]))
  }
  const dependsOnPicked = (name: string) => plan.alsoRemoved.includes(name)

  return (
    <Dialog open={open} onOpenChange={(o) => !remove.isPending && onOpenChange(o)}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-xl gap-0 p-0">
        <div className="flex-none px-6 pb-3 pt-6 pr-14">
          <DialogTitle className="m-0 text-lg font-semibold">Remove a service from {project}</DialogTitle>
          <DialogDescription className="m-0 mt-1 text-sm text-muted-foreground">
            Deletes the services you pick. Their containers are removed by their nodes shortly after. The other services keep running.
          </DialogDescription>
        </div>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-6 pb-4 pt-1">

        <div className="flex flex-col gap-2">
          <div className="flex items-center gap-2">
            <div className="relative flex-1">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
              <input
                type="search"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={`Search ${services.length} ${services.length === 1 ? 'service' : 'services'}`}
                aria-label="Search services to remove"
                className="min-h-11 w-full rounded-md border border-border bg-background pl-9 pr-3 text-[13px] outline-none focus-visible:ring-2 focus-visible:ring-ok"
              />
            </div>
            {picked.length > 0 && (
              <button type="button" onClick={() => { remove.reset(); setPicked([]) }} className="min-h-11 whitespace-nowrap px-2 text-[13px] text-muted-foreground underline">
                Clear {picked.length} selected
              </button>
            )}
          </div>
          <fieldset className="m-0 flex max-h-48 flex-col gap-1 overflow-y-auto border-0 p-0">
            <legend className="sr-only">Services in this project</legend>
            {shown.length === 0 && <p className="m-0 px-1 py-3 text-center text-[13px] text-muted-foreground">No service matches “{query.trim()}”.</p>}
            {shown.map((name) => {
              const forced = dependsOnPicked(name)
              return (
                <label key={name} className={cn('flex min-h-11 cursor-pointer items-center gap-2.5 rounded-md border px-3 text-[13px]', picked.includes(name) || forced ? 'border-bad/50 bg-bad-soft/40' : 'border-border')}>
                  <input type="checkbox" checked={picked.includes(name) || forced} disabled={forced} onChange={() => toggle(name)} className="size-4" />
                  <span className="font-mono font-medium">{name}</span>
                  {forced && <Badge tone="bad" className="ml-auto">removed too</Badge>}
                </label>
              )
            })}
          </fieldset>
        </div>

        {plan.alsoRemoved.length > 0 && (
          <div role="alert" className="flex gap-2.5 rounded-lg border border-warn/40 bg-warn-soft p-3 text-[13px] text-warn-fg">
            <AlertTriangle className="mt-0.5 size-4 flex-none" aria-hidden="true" />
            <span>
              <strong className="font-semibold">{plan.alsoRemoved.join(', ')}</strong> {plan.alsoRemoved.length === 1 ? 'depends' : 'depend'} on what you picked, so{' '}
              {plan.alsoRemoved.length === 1 ? 'it is' : 'they are'} removed too. Without it {plan.alsoRemoved.length === 1 ? 'it' : 'they'} would wait forever.
            </span>
          </div>
        )}
        {total > 0 && <p className="m-0 text-[13px] text-muted-foreground">This cannot be undone. Data in named volumes stays on its node.</p>}

        {remove.isError && (
          <p role="alert" className="m-0 whitespace-pre-line rounded-md border border-bad/30 bg-bad-soft p-3 text-[13px] text-bad-fg">
            {(remove.error as Error).message}
          </p>
        )}
        </div>
        <div className="flex flex-none flex-wrap justify-end gap-2 border-t border-border px-6 py-4">
          <Button type="button" variant="outline" disabled={remove.isPending} onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="button" className="bg-bad text-white hover:bg-bad/90" disabled={total === 0 || remove.isPending} onClick={() => remove.mutate()}>
            {remove.isPending ? 'Removing…' : total > 1 ? `Remove ${total} services` : 'Remove service'}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
