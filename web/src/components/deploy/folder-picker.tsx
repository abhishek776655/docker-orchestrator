import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ArrowUp, FileCheck2, Folder, Home } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { ApiError, api } from '@/lib/api'
import { cn } from '@/lib/utils'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  start: string // folder to open at; "" opens the home folder
  machine: string
  onPick: (path: string) => void
}

// Browses folders on the control-plane machine. The browser's own file picker
// cannot be used: it never reveals absolute paths, and the folder has to be
// on the control-plane machine, not wherever the browser runs.
export function FolderPicker({ open, onOpenChange, start, machine, onPick }: Props) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        {open && <Browser start={start} machine={machine} onPick={(p) => { onPick(p); onOpenChange(false) }} onCancel={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function Browser({ start, machine, onPick, onCancel }: { start: string; machine: string; onPick: (p: string) => void; onCancel: () => void }) {
  const [path, setPath] = useState(start)
  const [typed, setTyped] = useState(start)
  const q = useQuery({ queryKey: ['dirs', path], queryFn: () => api.listDirs(path), retry: false, staleTime: 10_000 })
  const listing = q.data
  const go = (p: string) => {
    setPath(p)
    setTyped(p)
  }
  const join = (name: string) => `${listing!.path === '/' ? '' : listing!.path}/${name}`
  // An older control plane has no such route and answers with a plain 404.
  const oldControlPlane = q.error instanceof ApiError && q.error.status === 404 && /not found$/i.test(q.error.message)

  // Breadcrumb segments of the current path.
  const parts = (listing?.path ?? '').split('/').filter(Boolean)

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div>
        <DialogTitle className="m-0 text-lg font-semibold">Choose the project folder</DialogTitle>
        <DialogDescription className="m-0 mt-1 text-sm text-muted-foreground">
          Folders on {machine}, where builds and bind mounts are read. Pick the folder that holds the Compose file.
        </DialogDescription>
      </div>

      <form
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          go(typed.trim())
        }}
      >
        <input
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          placeholder="/Users/you/projects"
          spellCheck={false}
          aria-label="Folder path"
          className="min-h-11 min-w-0 flex-1 rounded-md border border-zinc-300 bg-card px-3 font-mono text-sm"
        />
        <Button type="submit" variant="outline">Go</Button>
      </form>

      <div className="flex flex-wrap items-center gap-1 text-[13px]">
        <Button type="button" variant="outline" className="min-h-9 px-2.5" disabled={!listing?.parent} onClick={() => listing && go(listing.parent)} aria-label="Up one folder">
          <ArrowUp className="size-4" aria-hidden="true" />
        </Button>
        <Button type="button" variant="outline" className="min-h-9 px-2.5" disabled={!listing} onClick={() => listing && go(listing.home)} aria-label="Home folder">
          <Home className="size-4" aria-hidden="true" />
        </Button>
        <nav aria-label="Current folder" className="ml-1 flex min-w-0 flex-wrap items-center font-mono">
          <button type="button" className="rounded px-1 py-0.5 hover:bg-muted" onClick={() => go('/')}>/</button>
          {parts.map((p, i) => (
            <span key={i} className="flex items-center">
              <button type="button" className="rounded px-1 py-0.5 hover:bg-muted" onClick={() => go('/' + parts.slice(0, i + 1).join('/'))}>{p}</button>
              {i < parts.length - 1 && <span className="text-muted-foreground">/</span>}
            </span>
          ))}
        </nav>
      </div>

      <div className="h-[320px] overflow-y-auto rounded-lg border border-border">
        {q.isPending && <p className="m-0 p-4 text-[13px] text-muted-foreground">Loading…</p>}
        {q.isError && (
          <p role="alert" className="m-0 p-4 text-[13px] text-bad-fg">
            {oldControlPlane
              ? 'This control plane cannot list folders yet. Run ./install.sh, then hacoctl stop-all and hacoctl start --web, or type the path instead.'
              : q.error.message}
          </p>
        )}
        {listing && listing.dirs.length === 0 && <p className="m-0 p-4 text-[13px] text-muted-foreground">No folders inside.</p>}
        {listing && (
          <ul className="m-0 list-none p-0">
            {listing.dirs.map((d) => (
              <li key={d}>
                <button
                  type="button"
                  onClick={() => go(join(d))}
                  className="flex min-h-11 w-full items-center gap-2.5 border-b border-muted px-3 text-left text-sm hover:bg-muted"
                >
                  <Folder className="size-4 flex-none text-muted-foreground" aria-hidden="true" />
                  <span className="truncate font-mono">{d}</span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className={cn('flex items-center gap-2 rounded-md px-3 py-2 text-[13px]', listing?.has_compose ? 'bg-ok-soft text-ok-fg' : 'bg-muted text-muted-foreground')}>
        {listing?.has_compose ? (
          <>
            <FileCheck2 className="size-4 flex-none" aria-hidden="true" />
            <span>
              This folder has <span className="font-mono">{listing.compose_file}</span>.
            </span>
          </>
        ) : (
          <span>No Compose file directly in this folder. That is fine if your file lives elsewhere, but relative paths resolve from here.</span>
        )}
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="min-w-0 truncate font-mono text-[13px] text-muted-foreground">{listing?.path}</span>
        <span className="flex gap-2">
          <Button type="button" variant="outline" onClick={onCancel}>Cancel</Button>
          <Button type="button" disabled={!listing} onClick={() => listing && onPick(listing.path)}>Use this folder</Button>
        </span>
      </div>
    </div>
  )
}
