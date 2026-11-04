import { useEffect, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Check, Copy } from 'lucide-react'
import { api, encodeJoinBundle } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'

export function AddNodeDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const { mutate, reset, data, error, isPending } = useMutation({ mutationFn: api.issueJoinToken })
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (open) mutate()
    else {
      reset()
      setCopied(false)
    }
  }, [open, mutate, reset])

  const command = data ? `hacoctl agent join '${encodeJoinBundle(data)}'` : ''

  async function copy() {
    try {
      await navigator.clipboard.writeText(command)
      setCopied(true)
    } catch {
      // clipboard blocked: the command is still selectable in the box
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <div className="flex flex-col gap-1.5 pr-10">
          <DialogTitle className="m-0 text-lg font-semibold">Add a node</DialogTitle>
          <DialogDescription className="m-0 text-sm text-muted-foreground">
            Run this on the machine you want to join. The token works once and expires.
          </DialogDescription>
        </div>
        {isPending && <p className="m-0 text-sm text-muted-foreground">Issuing a join token…</p>}
        {error && (
          <p role="alert" className="m-0 rounded-md bg-bad-soft p-3 text-sm text-bad-fg">
            Could not issue a token: {error.message}
          </p>
        )}
        {data && (
          <>
            <pre className="m-0 max-h-40 overflow-auto whitespace-pre-wrap break-all rounded-md bg-primary p-3 font-mono text-xs text-primary-foreground">
              {command}
            </pre>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="text-xs text-muted-foreground">Expires {new Date(data.expires_at).toLocaleTimeString()}</span>
              <Button type="button" variant="outline" onClick={copy}>
                {copied ? <Check className="size-4" aria-hidden="true" /> : <Copy className="size-4" aria-hidden="true" />}
                {copied ? 'Copied' : 'Copy command'}
              </Button>
            </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
