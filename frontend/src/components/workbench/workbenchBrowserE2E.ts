import { createApp, defineComponent, h, ref } from 'vue'
import TDesign from 'tdesign-vue-next'
import 'tdesign-vue-next/dist/tdesign.css'
import WorkbenchDrawer from './WorkbenchDrawer.vue'

interface BrowserBootstrap {
  session_id: string
  eligible_backends: string[]
  pptx_b64: string
}

const status = ref('BOOTING')
const statusDetail = ref('Starting real Workbench journey')

void start()

async function start(): Promise<void> {
  try {
    const response = await fetch('/api/v1/t2-e2e/bootstrap')
    if (!response.ok) throw new Error('bootstrap HTTP ' + response.status)
    const body = await response.json() as { data?: BrowserBootstrap }
    if (!body.data?.session_id || !body.data.pptx_b64) throw new Error('invalid bootstrap response')
    mount(body.data)
    await runJourney(body.data)
  } catch (err) {
    await finish('fail', { error: errorMessage(err) })
  }
}

function mount(bootstrap: BrowserBootstrap): void {
  const Root = defineComponent({
    setup() {
      const visible = ref(true)
      return () => h('main', { class: 't2-e2e-shell' }, [
        h('header', { class: 't2-e2e-header' }, [
          h('strong', 'WeKnora protected Workbench'),
          h('span', { id: 't2-e2e-status', 'data-status': status.value.toLowerCase() }, status.value),
          h('small', statusDetail.value),
        ]),
        h(WorkbenchDrawer, {
          visible: visible.value,
          sessionId: bootstrap.session_id,
          capabilitySupported: true,
          eligibleBackends: bootstrap.eligible_backends,
          initialFileRoot: 'output',
          'onUpdate:visible': (value: boolean) => { visible.value = value },
        }),
      ])
    },
  })
  createApp(Root).use(TDesign).mount('#app')
}

async function runJourney(bootstrap: BrowserBootstrap): Promise<void> {
  status.value = 'RUNNING'
  statusDetail.value = 'Real PostgreSQL + protected Docker + browser'

  const textarea = await waitFor(
    () => document.querySelector<HTMLTextAreaElement>('#workbench-command'),
    'command textarea',
  )
  const csvB64 = btoa('name,value\nalpha,42\n')
  const activeHTML = '<!doctype html><html><head><title>isolated</title></head><body><h1 id="preview-proof">active preview</h1><script>fetch("/api/v1/t2-e2e/network-probe",{method:"POST"}).catch(function(){document.body.dataset.network="blocked"})</script></body></html>'
  const htmlB64 = btoa(activeHTML)
  const command = [
    "printf '%s' '" + csvB64 + "' | base64 -d > /workspace/output/report.csv",
    "printf '%s' '" + htmlB64 + "' | base64 -d > /workspace/output/index.html",
    "printf '%s' '" + bootstrap.pptx_b64 + "' | base64 -d > /workspace/output/slides.pptx",
  ].join(' && ')
  await enablePresentationSkillCandidate()
  setTextArea(textarea, command)

  const runButton = await waitFor(() => findButtonByText('Run'), 'Run button')
  runButton.click()
  await waitFor(() => document.body.textContent?.includes('exit 0') ? true : null, 'successful command result', 90000)
  await waitFor(() => findFileButton('report.csv'), 'CSV output', 30000)
  await publishFile('report.csv', 'Publish immutable artifact')
  const csvFrame = await previewArtifact('report.csv')
  assertSandbox(csvFrame, '')
  closePreview()

  await publishFile('index.html', 'Publish immutable artifact')
  const htmlFrame = await previewArtifact('index.html')
  assertSandbox(htmlFrame, 'allow-scripts')
  await sleep(1200)
  closePreview()

  await publishFile('slides.pptx', 'Run Presentation Skill candidate')
  const pptxFrame = await previewArtifact('slides.pptx')
  assertSandbox(pptxFrame, '')
  pptxFrame.scrollIntoView({ block: 'center' })

  const artifactRows = document.querySelectorAll('.workbench-files__artifacts li')
  if (artifactRows.length < 3) throw new Error('expected three immutable artifacts')
  await finish('pass', {
    file_count: document.querySelectorAll('.workbench-files__entry').length,
    artifact_count: artifactRows.length,
    html_sandbox: 'allow-scripts',
    passive_sandbox: 'empty',
  })
}

async function publishFile(name: string, actionTitle: string): Promise<void> {
  const file = await waitFor(() => findFileButton(name), name + ' file')
  file.click()
  const action = await waitFor(() => {
    const row = file.closest('li')
    return row?.querySelector<HTMLButtonElement>('button[title="' + actionTitle + '"]') || null
  }, actionTitle)
  action.click()
  await waitFor(() => findArtifactRow(name), name + ' artifact', 30000)
}

async function previewArtifact(name: string): Promise<HTMLIFrameElement> {
  const row = await waitFor(() => findArtifactRow(name), name + ' artifact row')
  const button = await waitFor(
    () => row.querySelector<HTMLButtonElement>('button[title="Open isolated preview"]'),
    name + ' preview button',
  )
  button.click()
  return waitFor(() => {
    const frame = document.querySelector<HTMLIFrameElement>('.workbench-files__frame')
    return frame?.src.startsWith('blob:') ? frame : null
  }, name + ' preview frame', 30000)
}

function closePreview(): void {
  const button = document.querySelector<HTMLButtonElement>('button[title="Close preview"]')
  button?.click()
}

function assertSandbox(frame: HTMLIFrameElement, expected: string): void {
  if (!frame.hasAttribute('sandbox')) throw new Error('preview iframe is missing sandbox')
  if (frame.getAttribute('sandbox') !== expected) {
    throw new Error('preview sandbox mismatch: expected ' + expected + ', got ' + frame.getAttribute('sandbox'))
  }
}

function findFileButton(name: string): HTMLButtonElement | null {
  for (const button of document.querySelectorAll<HTMLButtonElement>('.workbench-files__entry')) {
    if (button.querySelector('.workbench-files__name')?.textContent?.trim() === name) return button
  }
  return null
}

function findArtifactRow(name: string): HTMLLIElement | null {
  for (const row of document.querySelectorAll<HTMLLIElement>('.workbench-files__artifacts li')) {
    if (row.querySelector('.workbench-files__name')?.textContent?.trim() === name) return row
  }
  return null
}

function findButtonByText(text: string): HTMLButtonElement | null {
  for (const button of document.querySelectorAll<HTMLButtonElement>('button')) {
    if (button.textContent?.trim() === text) return button
  }
  return null
}

async function enablePresentationSkillCandidate(): Promise<void> {
  const input = await waitFor(() => {
    const host = document.querySelector('#workbench-presentation-skill')
    if (host instanceof HTMLInputElement) return host
    return host?.querySelector<HTMLInputElement>('input[type="checkbox"]') || null
  }, 'Presentation Skill candidate control')
  if (!input.checked) input.click()
  await waitFor(() => input.checked ? true : null, 'Presentation Skill candidate enabled')
}

function setTextArea(_element: HTMLTextAreaElement, value: string): void {
  const host = document.querySelector('#workbench-command')
  const element = host instanceof HTMLTextAreaElement
    ? host
    : host?.querySelector<HTMLTextAreaElement>('textarea')
  if (!element) throw new Error('command textarea was not rendered as a native textarea')
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set
  if (!setter) throw new Error('native textarea value setter is unavailable')
  setter.call(element, value)
  element.dispatchEvent(new Event('input', { bubbles: true }))
}

async function waitFor<T>(read: () => T | null | false | undefined, label: string, timeout = 15000): Promise<T> {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    const value = read()
    if (value) return value
    await sleep(100)
  }
  const visibleText = document.body.textContent?.replace(/\s+/g, ' ').trim().slice(-1200) || ''
  throw new Error('timed out waiting for ' + label + '; body=' + visibleText)
}

async function finish(result: 'pass' | 'fail', details: Record<string, unknown>): Promise<void> {
  status.value = result.toUpperCase()
  statusDetail.value = result === 'pass' ? 'Browser journey completed with real services' : String(details.error || 'Browser journey failed')
  const node = document.querySelector('#t2-e2e-status')
  node?.setAttribute('data-status', result)
  try {
    await fetch('/api/v1/t2-e2e/complete', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ status: result, details }),
    })
  } catch {
    // The visible status remains available to the headless-browser receipt.
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms))
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? (err.stack || err.message) : String(err)
}
