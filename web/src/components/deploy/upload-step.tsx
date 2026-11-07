import { useRef, useState, type DragEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { FileText, Upload } from 'lucide-react'
import { FolderPicker } from '@/components/deploy/folder-picker'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { api } from '@/lib/api'
import { LABEL_ROLE } from '@/lib/cluster'
import { describeSummary, formatBytes, summarizeCompose, type ComposeSummary } from '@/lib/compose-summary'
import { cn } from '@/lib/utils'

export interface ComposeFile {
  name: string
  size: number
  text: string
  summary: ComposeSummary
  /** Folder on the control-plane machine the file was picked from; unknown for uploads. */
  dir?: string
}

const MAX_BYTES = 1024 * 1024
const SHOWN_SERVICES = 12

interface Props {
  compose: ComposeFile | null
  onLoad: (file: ComposeFile) => void
  onClear: () => void
  onContinue: () => void
}

export function UploadStep({ compose, onLoad, onClear, onContinue }: Props) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [error, setError] = useState<string | null>(null)
  const [dragging, setDragging] = useState(false)
  const [pasting, setPasting] = useState(false)
  const [pasted, setPasted] = useState('')
  const [picking, setPicking] = useState(false)
  const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: api.nodes })
  const host = nodesQ.data?.find((n) => n.Labels?.[LABEL_ROLE] === 'host')
  const hostName = host ? host.DisplayName || host.ID : 'the control-plane machine'

  function accept(name: string, text: string, dir?: string) {
    const result = summarizeCompose(text)
    if (!result.ok) {
      setError(result.error)
      return
    }
    setError(null)
    setPasting(false)
    setPasted('')
    onLoad({ name, size: new Blob([text]).size, text, summary: result.summary, dir })
  }

  async function pickFromMachine(dir: string) {
    try {
      const file = await api.readCompose(dir)
      accept(file.name, file.yaml, file.dir)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  async function readFile(file: File | undefined) {
    if (!file) return
    if (file.size > MAX_BYTES) {
      setError(`${file.name} is ${formatBytes(file.size)}. A Compose file larger than ${formatBytes(MAX_BYTES)} is probably not the right file.`)
      return
    }
    accept(file.name, await file.text())
  }

  function onDrop(e: DragEvent) {
    e.preventDefault()
    setDragging(false)
    void readFile(e.dataTransfer.files[0])
  }

  return (
    <Card className="flex max-w-[640px] flex-col gap-4 p-6">
      <FolderPicker open={picking} onOpenChange={setPicking} start="" machine={hostName} mode="compose" onPick={(dir) => void pickFromMachine(dir)} />
      <div>
        <h2 className="m-0 text-lg font-semibold">Upload your Compose file</h2>
        <p className="m-0 mt-1 text-muted-foreground">We read the services, dependencies and build settings. Nothing starts until you deploy.</p>
      </div>

      <input
        ref={inputRef}
        type="file"
        accept=".yml,.yaml,text/yaml,application/x-yaml"
        className="sr-only"
        tabIndex={-1}
        aria-label="Compose file"
        onChange={(e) => {
          void readFile(e.target.files?.[0])
          e.target.value = ''
        }}
      />

      {!compose && !pasting && (
        <div
          onDragOver={(e) => {
            e.preventDefault()
            setDragging(true)
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={onDrop}
          className={cn(
            'flex flex-col items-center gap-2.5 rounded-[10px] border-2 border-dashed px-4 py-7 text-center',
            dragging ? 'border-primary bg-muted' : 'border-zinc-300 bg-background',
          )}
        >
          <Upload className="size-7 text-muted-foreground" aria-hidden="true" />
          <span className="font-medium">Drop compose.yml here</span>
          <Button type="button" onClick={() => inputRef.current?.click()}>
            Choose file
          </Button>
          <span className="text-xs text-muted-foreground">
            or{' '}
            <button type="button" className="min-h-6 text-ok underline" onClick={() => setPasting(true)}>
              paste YAML
            </button>
            {' '}or{' '}
            <button type="button" className="min-h-6 text-ok underline" onClick={() => setPicking(true)}>
              choose it on {hostName}
            </button>
            {' '}(also fills in the project folder)
          </span>
        </div>
      )}

      {!compose && pasting && (
        <div className="flex flex-col gap-3">
          <label htmlFor="compose-paste" className="font-medium">
            Compose YAML
          </label>
          <textarea
            id="compose-paste"
            value={pasted}
            onChange={(e) => setPasted(e.target.value)}
            spellCheck={false}
            rows={12}
            placeholder={'services:\n  web:\n    image: nginx'}
            className="w-full resize-y rounded-md border border-zinc-300 bg-card p-3 font-mono text-xs"
          />
          <div className="flex flex-wrap justify-end gap-2">
            <Button type="button" variant="outline" onClick={() => (setPasting(false), setError(null))}>
              Cancel
            </Button>
            <Button type="button" onClick={() => accept('compose.yml', pasted)} disabled={pasted.trim() === ''}>
              Use this YAML
            </Button>
          </div>
        </div>
      )}

      {error && (
        <p role="alert" className="m-0 rounded-md border border-bad/30 bg-bad-soft p-3 text-sm text-bad-fg">
          {error}
        </p>
      )}

      {compose && (
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-x-4 gap-y-3 rounded-[10px] border border-border p-3.5">
            <span aria-hidden="true" className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-ok-soft text-ok-fg">
              <FileText className="size-5" />
            </span>
            <span className="flex min-w-[200px] flex-1 flex-col leading-snug">
              <span className="font-mono font-medium">{compose.name}</span>
              <span className="text-[13px] text-muted-foreground">
                {formatBytes(compose.size)} · {describeSummary(compose.summary)}
              </span>
            </span>
            <Button type="button" variant="outline" onClick={onClear}>
              Replace
            </Button>
          </div>

          <ul aria-label="Services in this file" className="m-0 flex list-none flex-wrap gap-1.5 p-0">
            {compose.summary.services.slice(0, SHOWN_SERVICES).map((s) => (
              <li key={s} className="rounded bg-muted px-2 py-0.5 font-mono text-xs text-zinc-700">
                {s}
              </li>
            ))}
            {compose.summary.services.length > SHOWN_SERVICES && (
              <li className="px-1 py-0.5 text-xs text-muted-foreground">+{compose.summary.services.length - SHOWN_SERVICES} more</li>
            )}
          </ul>

          {compose.summary.builds > 0 && (
            <p className="m-0 rounded-md bg-info-soft p-3 text-[13px] text-info-fg">
              {compose.summary.builds} {compose.summary.builds === 1 ? 'service builds' : 'services build'} from source. The folder
              holding those build contexts is set in the next step.
            </p>
          )}
        </div>
      )}

      <div className="flex justify-end">
        <Button type="button" onClick={onContinue} disabled={!compose}>
          Continue
        </Button>
      </div>
    </Card>
  )
}
