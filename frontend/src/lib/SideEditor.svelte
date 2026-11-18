<script lang="ts">
  import { fontScale } from './fontscale.svelte'
  import { onDestroy, untrack } from 'svelte'
  import { t } from './i18n.svelte'
  import {
    AddMarker,
    AmplifySide,
    EditHistory,
    MarkerOptions,
    MarkerRange,
    MoveMarker,
    PauseSidePlayback,
    PlaySide,
    RedoSide,
    RemoveMarker,
    ResumeSidePlayback,
    SeekSide,
    SideNoisePlan,
    SideSplitPlan,
    DenoiseSide,
    ResetMarkers,
    SplitSide,
    SideWaveform,
    SideWaveformRange,
    SidePlaybackState,
    StopSidePlayback,
    UndoSide,
    GetEditorWindow,
    SaveEditorWindow,
  } from '../../wailsjs/go/main/App'
  import type { main } from '../../wailsjs/go/models'
  import { errText, formatDuration, hooks, ui } from './store.svelte'

  const LANE_MIN = 110
  const WIN_MIN_W = 640
  const WIN_MIN_H = 420
  const TITLE_KEEP = 120
  const HIT_PX = 6
  const DRAG_PX = 3
  const RULER_H = 18
  const MIN_VIEW_MS = 1000

  let wave = $state<main.Waveform | null>(null)
  let loading = $state(false)
  let amplifying = $state(false)
  let workKind = $state<'amplify' | 'denoise'>('amplify')
  let error = $state('')
  let width = $state(0)
  let canvas = $state<HTMLCanvasElement>()
  let posMs = $state(0)
  let playing = $state(false)
  let active = $state(false)
  let menu = $state<{ x: number; y: number; ms: number; start: boolean; end: boolean; hit: number | null; both: boolean; locked: boolean } | null>(null)
  // A pending press on a marker; it becomes a drag after DRAG_PX of movement.
  let drag = $state<{ from: number; cur: number; x0: number; lo: number | null; hi: number | null; moved: boolean } | null>(null)
  let hover = $state(false)
  let waveH = $state(0)
  // null while the split analysis runs, 0 when no split was found, otherwise the number of tracks.
  let splitTracks = $state<number | null>(null)
  let splitSeq = 0
  // null while the noise analysis runs, undefined when no pattern was found, otherwise the noise level in dBFS.
  let noiseDb = $state<number | null | undefined>(null)
  let noiseSeq = 0
  let noiseSide = ''
  let splitSide = ''
  // null keeps the default centred layout until the user moves or resizes the window.
  let geo = $state<{ x: number; y: number; w: number; h: number } | null>(null)
  let editorEl = $state<HTMLElement | null>(null)
  let winDrag: { dir: string; px: number; py: number; g: { x: number; y: number; w: number; h: number } } | null = null
  // Visible window: viewMs 0 means the whole side. waveStart/waveMs describe what `wave` holds.
  let viewStart = $state(0)
  let viewMs = $state(0)
  let waveStart = 0
  let waveMs = 0
  let loadSeq = 0
  let reloadTimer: ReturnType<typeof setTimeout> | undefined
  let canUndo = $state(false)
  let canRedo = $state(false)
  let suppressClick = false
  let timer: ReturnType<typeof setInterval> | undefined
  let polling = false

  const side = $derived(ui.edit)
  const saved = $derived(side ? (ui.project?.sides?.[side]?.markers ?? []) : [])
  const markers = $derived(drag?.moved ? saved.map((m) => (m.positionMs === drag!.from ? { ...m, positionMs: drag!.cur } : m)) : saved)
  const duration = $derived(wave?.durationMs ?? 0)
  const lanes = $derived(wave?.channels ?? 0)
  const winMs = $derived(viewMs > 0 && viewMs < duration ? viewMs : duration)
  const winStart = $derived(viewMs > 0 && viewMs < duration ? viewStart : 0)
  const zoomed = $derived(winMs < duration)
  // A moved or resized window gives the waveform whatever height is left; the default layout keeps the minimum.
  const laneH = $derived(geo && waveH > 0 ? Math.max(LANE_MIN, Math.floor((waveH - RULER_H - (zoomed ? 16 : 0)) / lanes)) : LANE_MIN)
  const zoomText = $derived(winMs > 0 ? `${(duration / winMs).toFixed(duration / winMs < 10 ? 1 : 0)}x` : '1x')
  const pct = $derived(ui.job?.kind === workKind && ui.job.total > 0 ? Math.min(100, Math.round((ui.job.done / ui.job.total) * 100)) : null)

  async function load() {
    if (!side) return
    const seq = ++loadSeq
    loading = true
    error = ''
    try {
      const buckets = Math.max(256, Math.round(width * (window.devicePixelRatio || 1)))
      const from = winStart
      const span = winMs
      const w = span > 0 && zoomed ? await SideWaveformRange(side, Math.round(from), Math.round(from + span), buckets) : await SideWaveform(side, buckets)
      if (seq !== loadSeq) return
      wave = w
      waveStart = zoomed ? from : 0
      waveMs = zoomed ? span : w.durationMs
      if (viewMs > w.durationMs) viewMs = 0
    } catch (e) {
      if (seq === loadSeq) error = errText(e)
    } finally {
      if (seq === loadSeq) loading = false
    }
  }

  // Zoom and scroll reload the window data after a short pause.
  function scheduleLoad() {
    clearTimeout(reloadTimer)
    reloadTimer = setTimeout(() => void load(), 120)
  }

  function setView(start: number, span: number) {
    start = Math.round(start)
    span = Math.round(span)
    if (span >= duration) {
      viewStart = 0
      viewMs = 0
    } else {
      viewMs = span
      viewStart = Math.min(Math.max(0, start), duration - span)
    }
    scheduleLoad()
  }

  function zoomBy(factor: number, anchorMs: number, frac: number) {
    if (!wave || duration <= 0) return
    const minSpan = Math.min(MIN_VIEW_MS, duration)
    const span = Math.min(duration, Math.max(minSpan, winMs / factor))
    setView(anchorMs - frac * span, span)
  }

  function zoomButtons(factor: number) {
    const inView = posMs >= winStart && posMs <= winStart + winMs
    zoomBy(factor, inView ? posMs : winStart + winMs / 2, inView ? (posMs - winStart) / winMs : 0.5)
  }

  function onWheel(e: WheelEvent) {
    if (!wave || amplifying || drag) return
    const r = canvas!.getBoundingClientRect()
    if (e.ctrlKey || e.metaKey) {
      e.preventDefault()
      const frac = Math.min(Math.max(0, (e.clientX - r.left) / r.width), 1)
      zoomBy(Math.exp(-e.deltaY * 0.01), winStart + frac * winMs, frac)
    } else if (zoomed && (e.deltaX !== 0 || e.shiftKey)) {
      e.preventDefault()
      const d = e.deltaX !== 0 ? e.deltaX : e.deltaY
      setView(winStart + (d / r.width) * winMs, winMs)
    }
  }

  $effect(() => {
    const c = canvas
    if (!c) return
    c.addEventListener('wheel', onWheel, { passive: false })
    return () => c.removeEventListener('wheel', onWheel)
  })

  $effect(() => {
    void width
    untrack(() => {
      if (wave) scheduleLoad()
    })
  })

  // Scrollbar thumb drag.
  let thumbDrag: { x0: number; start0: number } | null = null
  let track = $state<HTMLDivElement>()
  function thumbDown(e: PointerEvent) {
    thumbDrag = { x0: e.clientX, start0: winStart }
    ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  }
  function thumbMove(e: PointerEvent) {
    if (!thumbDrag || !track) return
    const w = track.getBoundingClientRect().width
    setView(thumbDrag.start0 + ((e.clientX - thumbDrag.x0) / w) * duration, winMs)
  }
  function trackDown(e: PointerEvent) {
    if (e.target !== track || !track) return
    const r = track.getBoundingClientRect()
    setView(((e.clientX - r.left) / r.width) * duration - winMs / 2, winMs)
  }

  $effect(() => {
    if (side) {
      untrack(() => {
        wave = null
        viewStart = viewMs = 0
        posMs = 0
        playing = active = false
        menu = null
        void load()
        timer = setInterval(poll, 66)
      })
    }
    return () => {
      clearInterval(timer)
      timer = undefined
    }
  })

  // Every change replaces ui.project, so the history buttons follow it.
  $effect(() => {
    void ui.project
    const s = side
    if (!s) return
    EditHistory(s)
      .then((h) => {
        canUndo = h.canUndo
        canRedo = h.canRedo
      })
      .catch(() => {})
  })

  // Every project change re-asks; the backend caches the analysis per audio file version.
  $effect(() => {
    void ui.project
    const s = side
    if (!s) {
      splitSide = ''
      return
    }
    if (s !== splitSide) {
      splitSide = s
      splitTracks = null
    }
    const seq = ++splitSeq
    SideSplitPlan(s)
      .then((p) => {
        if (seq === splitSeq) splitTracks = p.available ? p.tracks : 0
      })
      .catch(() => {
        if (seq === splitSeq) splitTracks = 0
      })
  })

  $effect(() => {
    void ui.project
    const s = side
    if (!s) return
    if (s !== noiseSide) {
      noiseSide = s
      noiseDb = null
    }
    const seq = ++noiseSeq
    SideNoisePlan(s)
      .then((p) => {
        if (seq === noiseSeq) noiseDb = p.available ? p.noiseDb : undefined
      })
      .catch(() => {
        if (seq === noiseSeq) noiseDb = undefined
      })
  })

  const busy = () => amplifying || drag !== null

  async function stepHistory(undo: boolean) {
    if (!side || busy() || (undo ? !canUndo : !canRedo)) return
    menu = null
    await guard(async () => {
      ui.project = undo ? await UndoSide(side!) : await RedoSide(side!)
      posMs = 0
      await load()
    })
  }

  $effect(() => {
    hooks.undo = () => stepHistory(true)
    hooks.redo = () => stepHistory(false)
    return () => {
      hooks.undo = hooks.redo = null
    }
  })

  onDestroy(() => {
    clearInterval(timer)
    clearTimeout(reloadTimer)
    void StopSidePlayback()
  })

  async function poll() {
    if (polling || amplifying) return
    polling = true
    try {
      const s = await SidePlaybackState()
      active = s.active
      playing = s.playing
      if (s.active) posMs = s.positionMs
      if (s.active && s.playing && zoomed && !drag && (posMs < winStart || posMs > winStart + winMs)) setView(posMs, winMs)
    } catch {
      // The poll is best effort; the next tick retries.
    } finally {
      polling = false
    }
  }

  function css(name: string) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  }

  const TICKS = [100, 200, 500, 1000, 2000, 5000, 10000, 15000, 30000, 60000, 120000, 300000, 600000, 1800000, 3600000]

  function tickLabel(ms: number, step: number) {
    if (step >= 1000) return formatDuration(ms)
    return `${formatDuration(ms)}.${Math.floor((ms % 1000) / 100)}`
  }

  function drawRuler(g: CanvasRenderingContext2D, xOf: (ms: number) => number) {
    if (winMs <= 0) return
    const step = TICKS.find((s) => (s / winMs) * width >= 80) ?? TICKS[TICKS.length - 1]
    g.fillStyle = css('--text-muted')
    g.strokeStyle = css('--control-border')
    g.font = `${Math.round(10 * fontScale.percent) / 100}px ` + css('--font')
    for (let ms = Math.ceil(winStart / step) * step; ms <= winStart + winMs; ms += step) {
      const x = Math.round(xOf(ms)) + 0.5
      g.beginPath()
      g.moveTo(x, RULER_H - 5)
      g.lineTo(x, RULER_H)
      g.stroke()
      g.fillText(tickLabel(ms, step), x + 3, RULER_H - 6)
    }
  }

  function draw() {
    if (!canvas || !wave || width <= 0) return
    const dpr = window.devicePixelRatio || 1
    const h = RULER_H + laneH * lanes
    canvas.width = Math.round(width * dpr)
    canvas.height = Math.round(h * dpr)
    const g = canvas.getContext('2d')
    if (!g) return
    g.setTransform(dpr, 0, 0, dpr, 0, 0)
    g.clearRect(0, 0, width, h)
    const xOf = (ms: number) => (winMs > 0 ? ((ms - winStart) / winMs) * width : 0)
    const accent = css('--accent')

    for (let ch = 0; ch < lanes; ch++) {
      const top = RULER_H + ch * laneH
      const mid = top + laneH / 2
      g.fillStyle = css('--meter-track')
      g.fillRect(0, top + 1, width, laneH - 2)
      g.strokeStyle = css('--control-border')
      g.beginPath()
      g.moveTo(0, mid)
      g.lineTo(width, mid)
      g.stroke()
      g.fillStyle = accent
      const mins = wave.min[ch]
      const maxs = wave.max[ch]
      const n = mins.length
      for (let x = 0; x < width; x++) {
        const ms = winStart + (x / width) * winMs
        if (waveMs <= 0 || ms < waveStart || ms >= waveStart + waveMs) continue
        const i = Math.min(n - 1, Math.floor(((ms - waveStart) / waveMs) * n))
        const y1 = mid - maxs[i] * (laneH / 2 - 2)
        const y2 = mid - mins[i] * (laneH / 2 - 2)
        g.fillRect(x, y1, 1, Math.max(1, y2 - y1))
      }
    }

    // Tracks: shade start..end, then draw the marker lines on top.
    g.globalAlpha = 0.14
    g.fillStyle = css('--meter-ok')
    for (let i = 0; i < markers.length; i++) {
      if (markers[i].kind !== 'start') continue
      const x1 = xOf(markers[i].positionMs)
      const x2 = markers[i + 1] ? xOf(markers[i + 1].positionMs) : width
      g.fillRect(x1, RULER_H, x2 - x1, h - RULER_H)
    }
    g.globalAlpha = 1
    g.font = `${Math.round(11 * fontScale.percent) / 100}px ` + css('--font')
    for (const m of markers) {
      const x = Math.round(xOf(m.positionMs)) + 0.5
      g.strokeStyle = g.fillStyle = m.kind === 'start' ? css('--meter-ok') : css('--danger')
      g.beginPath()
      g.moveTo(x, RULER_H)
      g.lineTo(x, h)
      g.stroke()
      g.fillText(m.kind === 'start' ? 'S' : 'E', Math.min(x + 3, width - 10), RULER_H + 12)
    }

    drawRuler(g, xOf)
    const px = Math.round(xOf(posMs)) + 0.5
    g.strokeStyle = css('--text')
    g.lineWidth = 1.5
    g.beginPath()
    g.moveTo(px, 0)
    g.lineTo(px, h)
    g.stroke()
    g.lineWidth = 1
  }

  $effect(() => {
    // Redraw on any input of the picture, including the theme attribute.
    void [wave, width, laneH, posMs, markers, lanes, winStart, winMs, fontScale.percent, document.documentElement.dataset.theme]
    draw()
  })

  // The nearest marker within HIT_PX of x; ties go to the earlier one.
  function hitAt(x: number): number | null {
    if (!canvas || winMs <= 0) return null
    const w = canvas.getBoundingClientRect().width
    let best: number | null = null
    let bestD = HIT_PX + 1
    for (const m of saved) {
      const d = Math.abs(((m.positionMs - winStart) / winMs) * w - x)
      if (d <= HIT_PX && d < bestD) {
        best = m.positionMs
        bestD = d
      }
    }
    return best
  }

  function msAt(e: MouseEvent) {
    const r = canvas!.getBoundingClientRect()
    const x = Math.min(Math.max(0, e.clientX - r.left), r.width)
    return { x, ms: Math.round(winStart + (x / r.width) * winMs) }
  }

  async function guard(fn: () => Promise<unknown>) {
    error = ''
    try {
      await fn()
    } catch (e) {
      error = errText(e)
    }
  }

  async function onDown(e: PointerEvent) {
    if (e.button !== 0 || !wave || !side || amplifying) return
    suppressClick = false
    const { x } = msAt(e)
    const hit = hitAt(x)
    if (hit === null) return
    canvas!.setPointerCapture(e.pointerId)
    drag = { from: hit, cur: hit, x0: x, lo: null, hi: null, moved: false }
    // Work on the reactive proxy: the raw object would never equal `drag` nor notify readers.
    const d = drag
    try {
      const r = await MarkerRange(side, hit)
      if (drag === d) {
        d.lo = r.min
        d.hi = r.max
      }
    } catch (err) {
      if (drag === d) drag = null
      error = errText(err)
    }
  }

  function onMove(e: PointerEvent) {
    const { x, ms } = msAt(e)
    const d = drag
    if (!d) {
      hover = !amplifying && hitAt(x) !== null
      return
    }
    if (!d.moved && Math.abs(x - d.x0) < DRAG_PX) return
    if (d.lo === null || d.hi === null) return
    d.moved = true
    d.cur = Math.min(Math.max(ms, d.lo), d.hi)
  }

  async function onUp() {
    const d = drag
    if (!d) return
    if (!d.moved) {
      drag = null
      return
    }
    suppressClick = true
    try {
      if (d.cur !== d.from && side) ui.project = await MoveMarker(side, d.from, d.cur)
    } catch (e) {
      error = errText(e)
    } finally {
      drag = null
    }
  }

  async function onLeftClick(e: MouseEvent) {
    if (suppressClick) {
      suppressClick = false
      return
    }
    if (!wave || amplifying) return
    menu = null
    const { ms } = msAt(e)
    posMs = ms
    if (active) await guard(() => SeekSide(ms))
  }

  async function onContext(e: MouseEvent) {
    e.preventDefault()
    if (!wave || !side || amplifying) return
    const { x, ms } = msAt(e)
    const hit = hitAt(x)
    const i = saved.findIndex((m) => m.positionMs === hit)
    const both = i >= 0 && saved[i].kind === 'start' && saved[i + 1]?.kind === 'end'
    const locked = i >= 0 && saved[i].kind === 'end' && i !== saved.length - 1
    let start = false
    let end = false
    try {
      const o = await MarkerOptions(side, ms)
      start = o.start
      end = o.end
    } catch (err) {
      error = errText(err)
    }
    menu = { x: e.clientX, y: e.clientY, ms: hit ?? ms, start, end, hit, both, locked }
  }

  async function addMarker(kind: 'start' | 'end') {
    const m = menu
    menu = null
    if (!m || !side) return
    await guard(async () => {
      ui.project = await AddMarker(side, kind, m.ms)
    })
  }

  async function removeMarker() {
    const m = menu
    menu = null
    if (!m || !side || m.hit === null) return
    await guard(async () => {
      ui.project = await RemoveMarker(side, m.hit!)
    })
  }

  async function togglePlay() {
    if (!side) return
    await guard(async () => {
      if (active && playing) await PauseSidePlayback()
      else if (active) await ResumeSidePlayback()
      else await PlaySide(side, posMs >= duration ? 0 : posMs)
      await poll()
    })
  }

  async function split() {
    if (!side || busy() || !splitTracks) return
    menu = null
    await guard(async () => {
      ui.project = await SplitSide(side!)
    })
  }

  async function resetMarkers() {
    if (!side || busy() || !saved.length) return
    menu = null
    await guard(async () => {
      ui.project = await ResetMarkers(side!)
    })
  }

  async function fixNoise() {
    if (!side || busy() || noiseDb == null) return
    menu = null
    await runJob('denoise', () => DenoiseSide(side!))
  }

  async function amplify() {
    if (!side) return
    await runJob('amplify', () => AmplifySide(side!))
  }

  async function runJob(kind: 'amplify' | 'denoise', fn: () => Promise<typeof ui.project>) {
    workKind = kind
    amplifying = true
    error = ''
    ui.job = null
    try {
      ui.project = await fn()
      await load()
    } catch (e) {
      error = errText(e)
    } finally {
      amplifying = false
      ui.job = null
    }
  }

  async function close() {
    if (amplifying) return
    await StopSidePlayback().catch(() => {})
    ui.edit = null
  }

  const splitHint = $derived(splitTracks === null ? t('edit.splitBusy') : splitTracks > 0 ? t('edit.splitHint', { n: splitTracks }) : t('edit.splitNone'))
  const noiseHint = $derived(noiseDb === null ? t('edit.fixNoiseBusy') : noiseDb === undefined ? t('edit.fixNoiseNone') : t('edit.fixNoiseHint', { db: noiseDb.toFixed(0) }))
  const gainText = $derived(wave?.gainAvailable ? t('edit.amplifyHint', { db: wave.gainDb.toFixed(1) }) : t('edit.amplifyMax'))
  function clampGeo(g: { x: number; y: number; w: number; h: number }) {
    const w = Math.min(Math.max(g.w, Math.min(WIN_MIN_W, window.innerWidth)), window.innerWidth)
    const h = Math.min(Math.max(g.h, Math.min(WIN_MIN_H, window.innerHeight)), window.innerHeight)
    return {
      w,
      h,
      x: Math.min(Math.max(g.x, TITLE_KEEP - w), window.innerWidth - TITLE_KEEP),
      y: Math.min(Math.max(g.y, 0), window.innerHeight - 40),
    }
  }

  function winDown(e: PointerEvent, dir: string) {
    if (e.button !== 0 || !editorEl) return
    if (dir === 'move' && (e.target as HTMLElement).closest('button')) return
    const r = editorEl.getBoundingClientRect()
    const g = geo ?? { x: r.left, y: r.top, w: r.width, h: r.height }
    geo = g
    winDrag = { dir, px: e.clientX, py: e.clientY, g: { ...g } }
    ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
    e.preventDefault()
  }

  function winMove(e: PointerEvent) {
    if (!winDrag) return
    const { dir, g } = winDrag
    const dx = e.clientX - winDrag.px
    const dy = e.clientY - winDrag.py
    if (dir === 'move') {
      geo = clampGeo({ ...g, x: g.x + dx, y: g.y + dy })
      return
    }
    let { x, y, w, h } = g
    if (dir.includes('e')) w = g.w + dx
    if (dir.includes('s')) h = g.h + dy
    if (dir.includes('w')) {
      w = Math.max(g.w - dx, WIN_MIN_W)
      x = g.x + g.w - w
    }
    if (dir.includes('n')) {
      h = Math.max(g.h - dy, WIN_MIN_H)
      y = g.y + g.h - h
    }
    const c = clampGeo({ x, y, w, h })
    geo = c
  }

  function winUp() {
    const moved = winDrag !== null
    winDrag = null
    if (moved && geo) void SaveEditorWindow(Math.round(geo.x), Math.round(geo.y), Math.round(geo.w), Math.round(geo.h)).catch(() => {})
  }

  // The window reopens where the user left it, also after a restart.
  void GetEditorWindow()
    .then((r) => {
      if (r && !geo) geo = clampGeo({ x: r.x, y: r.y, w: r.w, h: r.h })
    })
    .catch(() => {})

  const laneLabel = (i: number) => (lanes === 1 ? t('edit.channelMono') : i === 0 ? t('edit.channelL') : t('edit.channelR'))
</script>

<svelte:window
  onkeydown={(e) => {
    if (!ui.edit || e.key !== 'Escape' || ui.ask) return
    if (drag) {
      suppressClick = drag.moved
      drag = null
    } else if (menu) menu = null
    else void close()
  }}
  onclick={() => (menu = null)}
  onresize={() => {
    if (geo) geo = clampGeo(geo)
  }}
/>

{#if ui.edit}
  <div class="backdrop" role="presentation">
    <div
      class="island editor"
      class:placed={geo}
      role="dialog"
      aria-modal="true"
      aria-labelledby="ed-title"
      bind:this={editorEl}
      style:left={geo ? geo.x + 'px' : undefined}
      style:top={geo ? geo.y + 'px' : undefined}
      style:width={geo ? geo.w + 'px' : undefined}
      style:height={geo ? geo.h + 'px' : undefined}
    >
      <h2 id="ed-title" class="grab" onpointerdown={(e) => winDown(e, 'move')} onpointermove={winMove} onpointerup={winUp}>
        {t('edit.title', { side: ui.edit })}
      </h2>
      {#each ['n', 's', 'e', 'w', 'ne', 'nw', 'se', 'sw'] as d}
        <div class="grip {d}" onpointerdown={(e) => winDown(e, d)} onpointermove={winMove} onpointerup={winUp} role="presentation"></div>
      {/each}

      <div class="wave" class:fill={geo} bind:clientWidth={width} bind:clientHeight={waveH}>
        {#if loading && !wave}
          <p class="muted pad">{t('edit.analysing')}</p>
        {/if}
        <div class="lanes" style:display={wave ? 'block' : 'none'}>
          <canvas
            bind:this={canvas}
            style:width="100%"
            style:height="{RULER_H + laneH * lanes}px"
            style:cursor={drag?.moved || hover ? 'ew-resize' : 'pointer'}
            onpointerdown={onDown}
            onpointermove={onMove}
            onpointerup={onUp}
            onpointercancel={() => (drag = null)}
            onclick={onLeftClick}
            oncontextmenu={onContext}
          ></canvas>
          <div class="labels">
            {#each Array(lanes) as _, i}<span style:top="{RULER_H + i * laneH + 4}px">{laneLabel(i)}</span>{/each}
          </div>
        </div>
        {#if zoomed}
          <div class="track" bind:this={track} onpointerdown={trackDown} role="presentation">
            <div
              class="thumb"
              style:left="{(winStart / duration) * 100}%"
              style:width="{(winMs / duration) * 100}%"
              onpointerdown={thumbDown}
              onpointermove={thumbMove}
              onpointerup={() => (thumbDrag = null)}
              role="scrollbar"
              aria-controls="ed-title"
              aria-orientation="horizontal"
              aria-valuenow={Math.round(winStart)}
              tabindex="-1"
            ></div>
          </div>
        {/if}
      </div>

      <div class="row">
        <button class="primary" onclick={togglePlay} disabled={!wave || amplifying}>
          {active && playing ? '⏸ ' + t('edit.pause') : '▶ ' + t('edit.play')}
        </button>
        <span class="timer">{formatDuration(posMs)} / {formatDuration(duration)}</span>
        <button onclick={() => zoomButtons(1 / 2)} disabled={!wave || !zoomed} title={t('edit.zoomOut')}>−</button>
        <span class="timer">{zoomText}</span>
        <button onclick={() => zoomButtons(2)} disabled={!wave || winMs <= Math.min(MIN_VIEW_MS, duration)} title={t('edit.zoomIn')}>+</button>
        <button onclick={() => setView(0, duration)} disabled={!wave || !zoomed}>{t('edit.zoomFit')}</button>
        <span class="grow"></span>
        <button onclick={() => stepHistory(true)} disabled={!canUndo || amplifying} title={t('edit.undo')}>↶ {t('edit.undo')}</button>
        <button onclick={() => stepHistory(false)} disabled={!canRedo || amplifying} title={t('edit.redo')}>↷ {t('edit.redo')}</button>
        <button onclick={split} disabled={!splitTracks || amplifying} title={splitHint}>{t('edit.split')}</button>
        <button onclick={resetMarkers} disabled={!saved.length || amplifying} title={t('edit.resetHint')}>{t('edit.reset')}</button>
        <button onclick={fixNoise} disabled={noiseDb == null || amplifying} title={noiseHint}>{t('edit.fixNoise')}</button>
        <button onclick={amplify} disabled={!wave?.gainAvailable || amplifying} title={gainText}>
          {t('edit.amplify')}{wave?.gainAvailable ? ` (+${wave.gainDb.toFixed(1)} ${t('unit.db')})` : ''}
        </button>
      </div>

      {#if amplifying}
        <progress max="100" value={pct ?? undefined}></progress>
        <p class="muted small">{workKind === 'denoise' ? t('edit.fixNoising') : t('edit.amplifying')}{pct !== null ? ` ${pct}%` : ''}</p>
      {/if}
      {#if error}<p class="error small" role="alert">{error}</p>{/if}
      <p class="muted small">{t('edit.hint')} {t('edit.zoomHint')}</p>

      <div class="row actions">
        <button onclick={close} disabled={amplifying}>{t('close')}</button>
      </div>
    </div>

    {#if menu}
      <div class="menu island" role="menu" style:left="{menu.x}px" style:top="{menu.y}px">
        <button role="menuitem" disabled={!menu.start} onclick={() => addMarker('start')}>{t('edit.markerStart')}</button>
        <button role="menuitem" disabled={!menu.end} onclick={() => addMarker('end')}>{t('edit.markerEnd')}</button>
        {#if menu.hit !== null}
          <button role="menuitem" disabled={menu.locked} title={menu.locked ? t('edit.markerEndNotLast') : undefined} onclick={removeMarker}>{menu.both ? t('edit.markerRemoveBoth') : t('edit.markerRemove')}</button>
        {/if}
      </div>
    {/if}
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: var(--backdrop);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 20;
  }
  .editor {
    position: relative;
    width: min(1000px, 94vw);
    padding: 20px 24px;
    display: flex;
    flex-direction: column;
    gap: 10px;
    border: 1px solid var(--control-border);
  }
  .editor.placed {
    position: absolute;
    max-width: none;
    overflow: auto;
  }
  .grab {
    cursor: move;
    user-select: none;
  }
  .grip {
    position: absolute;
    z-index: 2;
  }
  .grip.n,
  .grip.s {
    left: 8px;
    right: 8px;
    height: 6px;
    cursor: ns-resize;
  }
  .grip.e,
  .grip.w {
    top: 8px;
    bottom: 8px;
    width: 6px;
    cursor: ew-resize;
  }
  .grip.n { top: 0; }
  .grip.s { bottom: 0; }
  .grip.e { right: 0; }
  .grip.w { left: 0; }
  .grip.ne,
  .grip.nw,
  .grip.se,
  .grip.sw {
    width: 12px;
    height: 12px;
  }
  .grip.ne { top: 0; right: 0; cursor: nesw-resize; }
  .grip.nw { top: 0; left: 0; cursor: nwse-resize; }
  .grip.se { bottom: 0; right: 0; cursor: nwse-resize; }
  .grip.sw { bottom: 0; left: 0; cursor: nesw-resize; }
  .editor > :global(*) {
    flex-shrink: 0;
  }
  .wave {
    min-height: 40px;
  }
  .editor > .wave.fill {
    flex: 1 1 0;
    min-height: 0;
    overflow-y: auto;
  }
  .lanes {
    position: relative;
    line-height: 0;
  }
  canvas {
    display: block;
    cursor: pointer;
    border-radius: var(--radius-control);
  }
  .track {
    position: relative;
    height: 12px;
    margin-top: 4px;
    background: var(--meter-track);
    border-radius: var(--radius-control);
  }
  .thumb {
    position: absolute;
    top: 1px;
    bottom: 1px;
    min-width: 12px;
    background: var(--control-border);
    border-radius: var(--radius-control);
    cursor: grab;
  }
  .labels span {
    position: absolute;
    left: 6px;
    font-size: calc(11px * var(--font-scale, 1));
    line-height: 1;
    color: var(--text-muted);
    pointer-events: none;
  }
  .pad {
    padding: 12px 0;
  }
  .grow {
    flex: 1;
  }
  .small {
    font-size: calc(12px * var(--font-scale, 1));
  }
  .actions {
    justify-content: flex-end;
  }
  .timer {
    font-family: var(--font-mono);
  }
  progress {
    width: 100%;
  }
  .menu {
    position: fixed;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 6px;
    border: 1px solid var(--control-border);
    z-index: 21;
  }
  .menu button {
    text-align: left;
    border-color: transparent;
  }
</style>
