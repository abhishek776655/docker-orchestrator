import { useEffect, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { ConfigureStep } from '@/components/deploy/configure-step'
import { PlanStep } from '@/components/deploy/plan-step'
import { Stepper } from '@/components/deploy/stepper'
import { UploadStep, type ComposeFile } from '@/components/deploy/upload-step'
import { summarizeCompose } from '@/lib/compose-summary'
import { defaultConfig, type DeployConfig } from '@/lib/deploy-config'
import { loadDeploy } from '@/lib/saved-deploys'

const STORAGE_KEY = 'haco.deploy.v2'

interface Saved {
  file: { name: string; size: number; text: string }
  config: DeployConfig
}

// The uploaded file and the choices made on it survive a page reload for the
// session, so a refresh in the middle of a deploy loses nothing. The summary is
// recomputed from the saved YAML rather than trusted, so an older saved shape
// can never break the page. Storage can be unavailable (private windows), in
// which case the page just starts empty.
function loadSaved(): { compose: ComposeFile; config: DeployConfig } | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const saved = JSON.parse(raw) as Saved
    const result = summarizeCompose(saved.file.text)
    if (!result.ok) return null
    return { compose: { ...saved.file, summary: result.summary }, config: { ...defaultConfig(result.summary), ...saved.config } }
  } catch {
    return null
  }
}

function save(compose: ComposeFile | null, config: DeployConfig | null) {
  try {
    if (compose && config) {
      const saved: Saved = { file: { name: compose.name, size: compose.size, text: compose.text }, config }
      sessionStorage.setItem(STORAGE_KEY, JSON.stringify(saved))
    } else {
      sessionStorage.removeItem(STORAGE_KEY)
    }
  } catch {
    // ignore: persistence is a convenience
  }
}

// A link from a project page (?project=name&step=2|3&fresh=1) opens that
// project's last deploy in the wizard, at the step the action needs.
function fromProject(params: URLSearchParams): { compose: ComposeFile; config: DeployConfig; step: number } | null {
  const project = params.get('project')
  if (!project) return null
  // The draft in this tab counts only when it is this project's own.
  const draft = loadSaved()
  const saved = loadDeploy(project) ?? (draft && draft.config.project === project ? { file: draft.compose, config: draft.config } : null)
  if (!saved) return null
  const result = summarizeCompose(saved.file.text)
  if (!result.ok) return null
  const fresh = params.get('fresh') === '1'
  const config: DeployConfig = { ...defaultConfig(result.summary), ...saved.config, ...(fresh ? { fresh: true, placements: {} } : {}) }
  return { compose: { ...saved.file, summary: result.summary }, config, step: params.get('step') === '2' ? 2 : 3 }
}

export function DeployPage() {
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const [linked] = useState(() => fromProject(params))
  const [wantedProject] = useState(() => params.get('project'))
  const [step, setStep] = useState(linked?.step ?? 1)
  // Opened for a project: never fall back to the draft of a different one.
  const [initial] = useState(() => linked ?? (wantedProject ? null : loadSaved()))
  const [compose, setCompose] = useState<ComposeFile | null>(initial?.compose ?? null)
  const [config, setConfig] = useState<DeployConfig | null>(initial?.config ?? null)

  // The link has done its job; drop it so a refresh does not reapply it.
  useEffect(() => {
    if (params.has('project')) navigate('/deploy', { replace: true })
  }, [navigate, params])

  useEffect(() => save(compose, config), [compose, config])

  const locked = !compose || !config
  return (
    <main className="mx-auto flex max-w-[1180px] flex-col gap-6 p-6">
      <div>
        <h1 className="m-0 text-2xl font-semibold tracking-tight">Deploy</h1>
        <p className="m-0 mt-1 text-muted-foreground">Upload a Compose file, review the plan, then deploy.</p>
      </div>

      {wantedProject && !linked && (
        <p role="status" className="m-0 rounded-lg border border-warn/30 bg-warn-soft px-3.5 py-2.5 text-[13px] text-warn-fg">
          This browser has no saved Compose file for <span className="font-mono">{wantedProject}</span>, because it was deployed from somewhere else. Upload the file again to continue.
        </p>
      )}

      <Stepper
        current={step}
        onSelect={setStep}
        steps={[
          { title: 'Compose file', sub: compose ? compose.name : 'Upload your file' },
          { title: 'Configuration', sub: config?.project || 'Project and build options', locked },
          { title: 'Plan and deploy', sub: 'Placement and dependencies', locked },
        ]}
      />

      {step === 1 && (
        <UploadStep
          compose={compose}
          onLoad={(file) => {
            setCompose(file)
            // A different file means different choices; a file picked on the
            // control-plane machine also says which folder it came from.
            setConfig({ ...defaultConfig(file.summary), ...(file.dir ? { workingDir: file.dir } : {}), ...(wantedProject ? { project: wantedProject } : {}) })
          }}
          onClear={() => {
            setCompose(null)
            setConfig(null)
          }}
          onContinue={() => setStep(2)}
        />
      )}

      {step === 2 && compose && config && (
        <ConfigureStep
          compose={compose}
          config={config}
          onChange={(patch) => setConfig({ ...config, ...patch })}
          onBack={() => setStep(1)}
          onNext={() => setStep(3)}
        />
      )}

      {step === 3 && compose && config && (
        <PlanStep compose={compose} config={config} onChange={(patch) => setConfig({ ...config, ...patch })} onBack={() => setStep(2)} />
      )}
    </main>
  )
}
