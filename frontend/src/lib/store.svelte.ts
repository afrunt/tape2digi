import { t } from './i18n.svelte'
import { ClipboardGetText, EventsOn } from '../../wailsjs/runtime/runtime'
import { main, project, recorder } from '../../wailsjs/go/models'
import { SideClips } from '../../wailsjs/go/main/App'

export interface ChannelLevel {
  peakDb: number
  rmsDb: number
  clip: boolean
}
export type Levels = ChannelLevel[]

export interface JobProgress {
  kind: 'import' | 'saveAs' | 'amplify' | 'denoise' | 'loudness' | 'normalize' | 'repairScan' | 'repair' | 'export'
  side: string
  done: number
  total: number
}
export interface QuitRequest {
  dirty: boolean
  recording: boolean
  job: boolean
}

// Latest meter values live outside reactive state: the meter reads them in
// requestAnimationFrame so level events never trigger DOM re-renders.
export const meter: { levels: Levels } = { levels: [] }

export interface AskButton {
  id: string
  label: string
  kind?: 'primary' | 'danger'
}
export interface AskRequest {
  title: string
  message: string
  buttons: AskButton[]
  // Chosen on Escape and when another question is already open; also focused first.
  cancel: string
}

export const ui = $state({
  ask: null as AskRequest | null,
  project: null as project.Info | null,
  status: { state: 'idle', monitor: false, elapsedMs: 0, sizeBytes: 0, file: '', overruns: 0, channels: 2, side: '', importSide: '' } as recorder.Status,
  error: '',
  // Import or Save As progress; null when no such job runs.
  job: null as JobProgress | null,
  saveAsOpen: false,
  exportOpen: false,
  lookupOpen: false,
  // Mirrors settings.onlineLookup.
  onlineLookup: false,
  // Side shown in the editor modal, or null.
  edit: null as string | null,
  play: null as { side: 'A' | 'B'; startMs: number; endMs: number; num: string; title: string } | null,
  quit: null as QuitRequest | null,
  // Bumped when the cover changes so the <img> reloads.
  coverV: 0,
  // Clipped places per side from the background scan; absent until known.
  clips: {} as Record<string, main.ClipSummary>,
})

function refreshClipState() {
  for (const s of SIDES) {
    SideClips(s)
      .then((c) => {
        if (c.known) ui.clips[s] = c
        else delete ui.clips[s]
      })
      .catch(() => delete ui.clips[s])
  }
}

export const SIDES = ['A', 'B'] as const

export function sideExistsMessage(e: unknown): string | null {
  const m = errText(e)
  return m.startsWith('side_exists:') ? m.slice('side_exists:'.length) : null
}

// Undo/redo availability, published by the recorder screen for the bottom bar.
export const history = $state({ canUndo: false, canRedo: false, undoSide: '', redoSide: '', blocked: true })

let wired = false

export function wireEvents() {
  if (wired) return
  wired = true
  EventsOn('level', (l: Levels) => {
    meter.levels = l
  })
  EventsOn('state', (s: recorder.Status) => {
    ui.status = s
  })
  EventsOn('error', (m: string) => {
    ui.error = m
  })
  EventsOn('project', (p: project.Info) => {
    ui.project = p
    ui.coverV++
    if (p) refreshClipState()
    else ui.clips = {}
  })
  EventsOn('clips', (c: main.ClipSummary) => {
    ui.clips[c.side] = c
  })
  EventsOn('settings', (s: { onlineLookup: boolean }) => {
    ui.onlineLookup = !!s.onlineLookup
  })
  EventsOn('import', (j: JobProgress) => {
    ui.job = j
  })
  EventsOn('menu:new', () => hooks.newProject?.())
  EventsOn('menu:open', () => hooks.openProject?.())
  EventsOn('menu:undo', () => stepEdit(true))
  EventsOn('menu:redo', () => stepEdit(false))
  EventsOn('menu:cut', () => document.execCommand('cut'))
  EventsOn('menu:copy', () => document.execCommand('copy'))
  EventsOn('menu:selectAll', () => document.execCommand('selectAll'))
  EventsOn('menu:paste', () => {
    if (!textField()) return
    ClipboardGetText()
      .then((s) => document.execCommand('insertText', false, s))
      .catch(() => {})
  })
  EventsOn('menu:saveAs', () => {
    if (ui.project) ui.saveAsOpen = true
  })
  EventsOn('menu:lookup', () => hooks.lookUp?.())
  EventsOn('menu:export', () => hooks.exportTracks?.())
  EventsOn('quit-request', (q: QuitRequest) => {
    ui.quit = q
  })
}

// Set by the recorder screen: closes the open project (with its unsaved/recording
// guards) so the start screen offers a new one.
export const hooks: {
  newProject: (() => void) | null
  openProject: (() => void) | null
  undo: (() => void) | null
  redo: (() => void) | null
  // Used when the editor is closed.
  undoMain: (() => void) | null
  redoMain: (() => void) | null
  exportTracks: (() => void) | null
  lookUp: (() => void) | null
} = { newProject: null, openProject: null, undo: null, redo: null, undoMain: null, redoMain: null, exportTracks: null, lookUp: null }

const typedInputs = new Set(['text', 'search', 'number', 'email', 'url', 'tel', 'password'])

function textField(): boolean {
  const e = document.activeElement
  return e instanceof HTMLTextAreaElement || (e instanceof HTMLInputElement && typedInputs.has(e.type))
}

// A text field keeps its own undo history only after its value changed since it
// got focus; otherwise (and when the native undo does nothing) the project steps.
const focusValue = new WeakMap<Element, string>()
if (typeof document !== 'undefined') {
  document.addEventListener('focusin', (ev) => {
    const t = ev.target
    if (t instanceof HTMLInputElement || t instanceof HTMLTextAreaElement) focusValue.set(t, t.value)
  })
}

function fieldEdited(): boolean {
  const e = document.activeElement
  return textField() && (e as HTMLInputElement).value !== focusValue.get(e as Element)
}

function stepEdit(undo: boolean) {
  if (fieldEdited() && document.execCommand(undo ? 'undo' : 'redo')) return
  const h = ui.edit ? (undo ? hooks.undo : hooks.redo) : undo ? hooks.undoMain : hooks.redoMain
  h?.()
}

let askResolve: ((id: string) => void) | null = null

// window.confirm shows nothing in the macOS webview, so confirmations use an
// in-app modal. A second question while one is open resolves to its cancel id.
export function ask(req: AskRequest): Promise<string> {
  if (ui.ask) return Promise.resolve(req.cancel)
  return new Promise((resolve) => {
    askResolve = resolve
    ui.ask = req
  })
}

export function answerAsk(id: string) {
  const r = askResolve
  askResolve = null
  ui.ask = null
  r?.(id)
}

export function errText(e: unknown): string {
  if (typeof e === 'string') return e
  if (e instanceof Error) return e.message
  return String(e)
}

// formatClock is HH:mm:SS with hours always shown.
export function formatClock(ms: number): string {
  const total = Math.floor(ms / 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(Math.floor(total / 3600))}:${pad(Math.floor((total % 3600) / 60))}:${pad(total % 60)}`
}

export function formatDuration(ms: number): string {
  const total = Math.floor(ms / 1000)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const pad = (n: number) => String(n).padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`
}

export function formatSize(bytes: number): string {
  if (bytes >= 1 << 30) return (bytes / (1 << 30)).toFixed(2) + ' ' + t('unit.gb')
  if (bytes >= 1 << 20) return (bytes / (1 << 20)).toFixed(1) + ' ' + t('unit.mb')
  return Math.max(0, Math.round(bytes / 1024)) + ' ' + t('unit.kb')
}
