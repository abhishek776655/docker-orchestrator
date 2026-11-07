import { useId, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { FolderPicker } from '@/components/deploy/folder-picker'
import { ServiceList, type ServiceItem } from '@/components/deploy/service-list'
import { FolderOpen } from 'lucide-react'
import type { ComposeFile } from '@/components/deploy/upload-step'
import { api } from '@/lib/api'
import { LABEL_ROLE } from '@/lib/cluster'
import { dependencyClosure, servicesInScope, validateConfig, type DeployConfig } from '@/lib/deploy-config'
import { cn } from '@/lib/utils'

interface Props {
  compose: ComposeFile
  config: DeployConfig
  onChange: (patch: Partial<DeployConfig>) => void
  onBack: () => void
  onNext: () => void
}

const inputClass = (invalid: boolean) =>
  cn('min-h-11 rounded-md border bg-card px-3 font-mono text-sm', invalid ? 'border-bad' : 'border-zinc-300')

function PresetButton({ children, onClick }: { children: string; onClick: () => void }) {
  return (
    <Button type="button" variant="outline" className="text-[13px]" onClick={onClick}>
      {children}
    </Button>
  )
}

// A checkbox or radio as a card: a title, what it does, and when to pick it.
function OptionCard({
  type,
  name,
  checked,
  disabled,
  onChange,
  title,
  children,
  when,
}: {
  type: 'checkbox' | 'radio'
  name?: string
  checked: boolean
  disabled?: boolean
  onChange: (checked: boolean) => void
  title: React.ReactNode
  children: React.ReactNode
  when?: React.ReactNode
}) {
  return (
    <label
      className={cn(
        'flex cursor-pointer gap-3 rounded-lg border px-4 py-3 transition-colors',
        checked ? 'border-primary bg-muted/60' : 'border-border hover:bg-muted/40',
        disabled && 'cursor-not-allowed opacity-55',
      )}
    >
      <input type={type} name={name} checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} className="mt-1 size-4 flex-none accent-primary" />
      <span className="flex min-w-0 flex-col gap-1">
        <span className="font-medium">{title}</span>
        <span className="text-[13px] text-muted-foreground">{children}</span>
        {when && <span className="text-[13px] text-zinc-700"><span className="font-medium">Use it when </span>{when}</span>}
      </span>
    </label>
  )
}

function SectionHead({ title, children }: { title: string; children?: React.ReactNode }) {
  return (
    <div>
      <h3 className="m-0 text-base font-semibold">{title}</h3>
      {children && <p className="m-0 mt-0.5 text-[13px] text-muted-foreground">{children}</p>}
    </div>
  )
}

function FieldError({ id, message }: { id: string; message?: string }) {
  if (!message) return null
  return (
    <p id={id} role="alert" className="m-0 text-[13px] text-bad-fg">
      {message}
    </p>
  )
}

export function ConfigureStep({ compose, config, onChange, onBack, onNext }: Props) {
  const { summary } = compose
  const [showErrors, setShowErrors] = useState(false)
  const [picking, setPicking] = useState(false)
  const ids = { project: useId(), dir: useId(), projectErr: useId(), dirErr: useId() }

  // The host is the machine the control plane runs on. Whether it has a GPU
  // decides which services can be kept there.
  const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: api.nodes, refetchInterval: 3000 })
  const host = nodesQ.data?.find((n) => n.Labels?.[LABEL_ROLE] === 'host')
  const hostName = host ? host.ID : 'the control-plane machine'
  const hostHasGpu = (host?.GPUs?.length ?? 0) > 0

  const errors = useMemo(() => validateConfig(summary, config), [summary, config])
  const hasErrors = Object.keys(errors).length > 0
  const inScope = servicesInScope(summary, config)

  const hostItems: ServiceItem[] = summary.details.map((d) => {
    const blocked = d.gpu && !hostHasGpu
    return {
      name: d.name,
      disabled: blocked,
      note: blocked ? 'needs a GPU, the host has none' : d.stateful ? 'has a named volume' : undefined,
    }
  })
  const eligible = summary.details.filter((d) => !(d.gpu && !hostHasGpu))
  const preset = (names: string[]) => onChange({ hostServices: names })
  const keptStateful = summary.details.filter((d) => d.stateful && config.hostServices.includes(d.name)).length

  const scopeItems: ServiceItem[] = summary.details.map((d) => ({ name: d.name }))
  const closure = config.scope === 'one' && config.onlyService ? dependencyClosure(summary.details, config.onlyService) : []

  function run() {
    if (hasErrors) {
      setShowErrors(true)
      return
    }
    onNext()
  }

  return (
    <div className="flex flex-wrap items-start gap-6">
      <Card className="flex min-w-0 flex-[999_1_460px] flex-col gap-5 p-6">
        <div>
          <h2 className="m-0 text-lg font-semibold">Configuration</h2>
          <p className="m-0 mt-1 text-muted-foreground">Choose how this deploy is built and placed.</p>
        </div>

        <div className="flex flex-col gap-1.5">
          <label htmlFor={ids.project} className="font-medium">
            Project name
          </label>
          <input
            id={ids.project}
            type="text"
            value={config.project}
            onChange={(e) => onChange({ project: e.target.value })}
            aria-invalid={showErrors && !!errors.project}
            aria-describedby={ids.projectErr}
            className={inputClass(showErrors && !!errors.project)}
          />
          <FieldError id={ids.projectErr} message={showErrors ? errors.project : undefined} />
        </div>

        <div className="flex flex-col gap-1.5">
          <label htmlFor={ids.dir} className="font-medium">
            Project folder on {hostName}
            {summary.builds === 0 && <span className="font-normal text-muted-foreground"> (optional)</span>}
          </label>
          <div className="flex gap-2">
            <input
              id={ids.dir}
              type="text"
              value={config.workingDir}
              onChange={(e) => onChange({ workingDir: e.target.value })}
              placeholder="/Users/you/projects/storefront"
              spellCheck={false}
              aria-invalid={showErrors && !!errors.workingDir}
              aria-describedby={ids.dirErr}
              className={cn(inputClass(showErrors && !!errors.workingDir), 'min-w-0 flex-1')}
            />
            <Button type="button" variant="outline" onClick={() => setPicking(true)}>
              <FolderOpen className="size-4" aria-hidden="true" />
              Browse…
            </Button>
          </div>
          <FolderPicker open={picking} onOpenChange={setPicking} start={config.workingDir.trim()} machine={hostName} onPick={(workingDir) => onChange({ workingDir })} />
          <p className="m-0 text-[13px] text-muted-foreground">Folder holding the Compose file. Relative paths resolve from here.</p>
          <FieldError id={ids.dirErr} message={showErrors ? errors.workingDir : undefined} />
        </div>

        <fieldset className="m-0 flex flex-col gap-2.5 border-0 p-0">
          <legend className="mb-1 p-0">
            <SectionHead title="Keep on the host machine">Checked services run on {hostName}. The scheduler places the rest.</SectionHead>
          </legend>
          <div className="flex flex-wrap items-center gap-2">
            <PresetButton onClick={() => preset(eligible.filter((d) => !d.stateful).map((d) => d.name))}>Stateless services</PresetButton>
            <PresetButton onClick={() => preset(eligible.filter((d) => d.stateful).map((d) => d.name))}>Stateful services</PresetButton>
            <PresetButton onClick={() => preset(eligible.map((d) => d.name))}>Everything that fits</PresetButton>
            <PresetButton onClick={() => preset([])}>Clear</PresetButton>
            <span className="ml-auto text-[13px] text-muted-foreground">{config.hostServices.length} selected</span>
          </div>
          <ServiceList label="services to keep on the host" items={hostItems} selected={config.hostServices} mode="multi" onChange={(hostServices) => onChange({ hostServices })} />
          {keptStateful > 0 && (
            <p role="status" className="m-0 rounded-md border border-warn/30 bg-warn-soft p-3 text-[13px] text-warn-fg">
              {keptStateful} selected {keptStateful === 1 ? 'service keeps' : 'services keep'} data in a volume. On the host{' '}
              {keptStateful === 1 ? 'it starts' : 'they start'} with the volume already there, or an empty one.
            </p>
          )}
        </fieldset>

        <fieldset className="m-0 flex flex-col gap-2.5 border-0 p-0">
          <legend className="mb-1.5 p-0">
            <SectionHead title="What to deploy" />
          </legend>
          <OptionCard type="radio" name="scope" checked={config.scope === 'all'} onChange={() => onChange({ scope: 'all' })} title={`All ${summary.services.length} services`}>
            Every service in the file.
          </OptionCard>
          <OptionCard
            type="radio"
            name="scope"
            checked={config.scope === 'one'}
            onChange={() => onChange({ scope: 'one' })}
            title="One service, plus everything it depends on"
          >
            The chosen service and what it depends on. Others are left alone.
          </OptionCard>
          {config.scope === 'one' && (
            <div className="flex flex-col gap-2.5 pl-7">
              <ServiceList label="service to deploy" items={scopeItems} selected={config.onlyService ? [config.onlyService] : []} mode="single" onChange={([onlyService]) => onChange({ onlyService })} />
              {closure.length > 0 ? (
                <p className="m-0 text-[13px] text-muted-foreground">
                  {config.onlyService} and {closure.length - 1} {closure.length - 1 === 1 ? 'dependency' : 'dependencies'} will deploy ({closure.length} of{' '}
                  {summary.services.length}).
                </p>
              ) : (
                <FieldError id="scope-err" message={showErrors ? errors.onlyService : undefined} />
              )}
            </div>
          )}
        </fieldset>

        <fieldset className="m-0 flex flex-col gap-2.5 border-0 p-0">
          <legend className="mb-1.5 p-0">
            <SectionHead title="Options" />
          </legend>
          <OptionCard
            type="checkbox"
            checked={config.noCache}
            disabled={summary.builds === 0}
            onChange={(noCache) => onChange({ noCache })}
            title={<>Rebuild images without cache{summary.builds === 0 && <span className="font-normal text-muted-foreground"> (nothing builds from source)</span>}</>}
          >
            Build images from scratch. Slower.
          </OptionCard>
          <OptionCard
            type="checkbox"
            checked={config.fresh}
            onChange={(fresh) => onChange({ fresh })}
            title="Ignore where services were placed before"
          >
            Let the scheduler pick nodes again. Moved volumes start empty.
          </OptionCard>
        </fieldset>

        <div className="flex flex-wrap justify-between gap-2 border-t border-muted pt-4">
          <Button type="button" variant="outline" onClick={onBack}>
            Back
          </Button>
          <Button type="button" onClick={run}>
            Run plan
          </Button>
        </div>
        {showErrors && hasErrors && (
          <p role="alert" className="m-0 text-right text-[13px] text-bad-fg">
            Fix the highlighted fields to continue.
          </p>
        )}
      </Card>

      <aside aria-label="Detected in the compose file" className="flex min-w-0 flex-[1_1_280px] flex-col gap-1 rounded-[10px] border border-border bg-card p-5">
        <h3 className="m-0 mb-2 text-sm font-semibold">Detected in {compose.name}</h3>
        <Row label="Services" value={summary.services.length} />
        <Row label="Built from source" value={summary.builds} />
        <Row label="Need a GPU" value={summary.gpu} />
        <Row label="Stateful (named volumes)" value={summary.details.filter((d) => d.stateful).length} />
        <Row label="Published ports" value={summary.ports} />
        <Row label="Dependencies" value={summary.dependencies} />
        <Row label="Kept on host" value={config.hostServices.length} />
        <Row label="Will deploy" value={`${inScope.length} of ${summary.services.length}`} />
      </aside>
    </div>
  )
}

function Row({ label, value }: { label: string; value: number | string }) {
  return (
    <div className="flex justify-between border-t border-muted py-2 text-[13px]">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono">{value}</span>
    </div>
  )
}
