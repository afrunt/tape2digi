<script lang="ts">
  import { fontScale } from './fontscale.svelte'
  import { onDestroy, untrack } from 'svelte'
  import { t } from './i18n.svelte'
  import { PauseSidePlayback, PlaySide, ResumeSidePlayback, SeekSide, SidePlaybackState, SideWaveformRange, StopSidePlayback } from '../../wailsjs/go/main/App'
  import type { main } from '../../wailsjs/go/models'
  import PlayerMeters from './PlayerMeters.svelte'
  import { errText, formatDuration, ui } from './store.svelte'

  const LANE_H = 90
  const RULER_H = 18
  const MIN_VIEW_MS = 1000
  const TICKS = [100, 200, 500, 1000, 2000, 5000, 10000, 15000, 30000, 60000, 120000, 300000, 600000, 1800000, 3600000]

  let wave = $state<main.Waveform | null>(null)
  let loading = $state(false)
  let error = $state('')
  let width = $state(0)
  let canvas = $state<HTMLCanvasElement>()
  // Positions are absolute in the side's file; the view is relative to the track start.
  let posMs = $state(0)
  let playing = $state(false)
  let active = $state(false)
  let viewStart = $state(0)
  let viewMs = $state(0)
  let loadSeq = 0
  let reloadTimer: ReturnType<typeof setTimeout> | undefined
  let timer: ReturnType<typeof setInterval> | undefined
  let polling = false
  // Written by the poll, read by the meters every frame.
  const levels: { v: { peakDb: number; clip: boolean }[] } = { v: [] }
  let scrubbing = false

  const p = $derived(ui.play)
  const startMs = $derived(p?.startMs ?? 0)
  const endMs = $derived(p?.endMs ?? 0)
  const duration = $derived(Math.max(0, endMs - startMs))
  const lanes = $derived(wave?.channels ?? 0)
  const winMs = $derived(viewMs > 0 && viewMs < duration ? viewMs : duration)
  const winStart = $derived(viewMs > 0 && viewMs < duration ? viewStart : 0)
  const zoomed = $derived(winMs < duration)
  const rel = $derived(Math.min(Math.max(posMs - startMs, 0), duration))
  const zoomText = $derived(winMs > 0 ? `${(duration / winMs).toFixed(duration / winMs < 10 ? 1 : 0)}x` : '1x')
  const heading = $derived(p ? `${p.num} ${p.title}`.trim() : '')

  async function load() {
    if (!p) return
    const seq = ++loadSeq
    loading = true
    error = ''
    try {
      const buckets = Math.max(256, Math.round(width * (window.devicePixelRatio || 1)))
      const from = startMs + winStart
      const w = await SideWaveformRange(p.side, Math.round(from), Math.round(from + winMs), buckets)
      if (seq === loadSeq) wave = w
    } catch (e) {
      if (seq === loadSeq) error = errText(e)
    } finally {
      if (seq === loadSeq) loading = false
    }
  }

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

  function zoomBy(factor: number, anchor: number, frac: number) {
    if (!wave || duration <= 0) return
    const span = Math.min(duration, Math.max(Math.min(MIN_VIEW_MS, duration), winMs / factor))
    setView(anchor - frac * span, span)
  }

  function zoomButtons(factor: number) {
    const inView = rel >= winStart && rel <= winStart + winMs
    zoomBy(factor, inView ? rel : winStart + winMs / 2, inView ? (rel - winStart) / winMs : 0.5)
  }

  function onWheel(e: WheelEvent) {
    if (!wave) return
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

  let thumbDrag: { x0: number; start0: number } | null = null
  let track = $state<HTMLDivElement>()
  function thumbDown(e: PointerEvent) {
    thumbDrag = { x0: e.clientX, start0: winStart }
    ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  }
  function thumbMove(e: PointerEvent) {
    if (!thumbDrag || !track) return
    setView(thumbDrag.start0 + ((e.clientX - thumbDrag.x0) / track.getBoundingClientRect().width) * duration, winMs)
  }
  function trackDown(e: PointerEvent) {
    if (e.target !== track || !track) return
    const r = track.getBoundingClientRect()
    setView(((e.clientX - r.left) / r.width) * duration - winMs / 2, winMs)
  }

  // Each opened track starts from a clean state.
  $effect(() => {
    if (p) {
      untrack(() => {
        wave = null
        viewStart = viewMs = 0
        posMs = startMs
        playing = active = false
        error = ''
        void load()
        timer = setInterval(poll, 50)
      })
    }
    return () => {
      clearInterval(timer)
      timer = undefined
    }
  })

  onDestroy(() => {
    clearInterval(timer)
    clearTimeout(reloadTimer)
    void StopSidePlayback()
  })

  async function poll() {
    if (polling || !p) return
    polling = true
    try {
      const s = await SidePlaybackState()
      active = s.active && s.side === p.side
      levels.v = active && s.playing ? (s.levels ?? []) : []
      playing = active && s.playing
      if (!active) return
      if (s.positionMs >= endMs) {
        // The backend plays to the end of the file, so the track end is enforced here.
        await StopSidePlayback()
        active = playing = false
        posMs = startMs
        return
      }
      if (!scrubbing) posMs = s.positionMs
      if (playing && zoomed && !scrubbing && (rel < winStart || rel > winStart + winMs)) setView(rel, winMs)
    } catch {
      // The poll is best effort; the next tick retries.
    } finally {
      polling = false
    }
  }

  function css(name: string) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  }

  function tickLabel(ms: number, step: number) {
    return step >= 1000 ? formatDuration(ms) : `${formatDuration(ms)}.${Math.floor((ms % 1000) / 100)}`
  }

  function draw() {
    if (!canvas || !wave || width <= 0 || winMs <= 0) return
    const dpr = window.devicePixelRatio || 1
    const h = RULER_H + LANE_H * lanes
    canvas.width = Math.round(width * dpr)
    canvas.height = Math.round(h * dpr)
    const g = canvas.getContext('2d')
    if (!g) return
    g.setTransform(dpr, 0, 0, dpr, 0, 0)
    g.clearRect(0, 0, width, h)
    const xOf = (ms: number) => ((ms - winStart) / winMs) * width
    for (let ch = 0; ch < lanes; ch++) {
      const top = RULER_H + ch * LANE_H
      const mid = top + LANE_H / 2
      g.fillStyle = css('--meter-track')
      g.fillRect(0, top + 1, width, LANE_H - 2)
      g.strokeStyle = css('--control-border')
      g.beginPath()
      g.moveTo(0, mid)
      g.lineTo(width, mid)
      g.stroke()
      g.fillStyle = css('--accent')
      const mins = wave.min[ch]
      const maxs = wave.max[ch]
      const n = mins.length
      for (let x = 0; x < width; x++) {
        const i = Math.min(n - 1, Math.floor((x / width) * n))
        const y1 = mid - maxs[i] * (LANE_H / 2 - 2)
        const y2 = mid - mins[i] * (LANE_H / 2 - 2)
        g.fillRect(x, y1, 1, Math.max(1, y2 - y1))
      }
    }
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
    const px = Math.round(xOf(rel)) + 0.5
    g.strokeStyle = css('--text')
    g.lineWidth = 1.5
    g.beginPath()
    g.moveTo(px, 0)
    g.lineTo(px, h)
    g.stroke()
    g.lineWidth = 1
  }

  $effect(() => {
    void [wave, width, rel, lanes, winStart, winMs, fontScale.percent, document.documentElement.dataset.theme]
    draw()
  })

  async function guard(fn: () => Promise<unknown>) {
    error = ''
    try {
      await fn()
    } catch (e) {
      error = errText(e)
    }
  }

  function seekTo(e: PointerEvent) {
    const r = canvas!.getBoundingClientRect()
    const x = Math.min(Math.max(0, e.clientX - r.left), r.width)
    const ms = Math.min(startMs + Math.round(winStart + (x / r.width) * winMs), endMs - 1)
    posMs = ms
    if (active) void guard(() => SeekSide(ms))
  }

  function onDown(e: PointerEvent) {
    if (e.button !== 0 || !wave) return
    scrubbing = true
    canvas!.setPointerCapture(e.pointerId)
    seekTo(e)
  }
  const onMove = (e: PointerEvent) => scrubbing && seekTo(e)
  const onUp = () => (scrubbing = false)

  async function togglePlay() {
    if (!p) return
    await guard(async () => {
      if (active && playing) await PauseSidePlayback()
      else if (active) await ResumeSidePlayback()
      else await PlaySide(p.side, posMs >= endMs - 1 || posMs < startMs ? startMs : posMs)
      await poll()
    })
  }

  async function stop() {
    await guard(async () => {
      await StopSidePlayback()
      active = playing = false
      posMs = startMs
    })
  }

  async function close() {
    await StopSidePlayback().catch(() => {})
    ui.play = null
  }

  const laneLabel = (i: number) => (lanes === 1 ? t('edit.channelMono') : i === 0 ? t('edit.channelL') : t('edit.channelR'))
</script>

<svelte:window
  onkeydown={(e) => {
    if (ui.play && e.key === 'Escape' && !ui.ask) void close()
  }}
/>

{#if ui.play}
  <div class="backdrop" role="presentation">
    <div class="island player" role="dialog" aria-modal="true" aria-labelledby="tp-title">
      <h2 id="tp-title">{t('player.title', { track: heading })}</h2>

      <div class="deck">
      <PlayerMeters channels={lanes || 2} topPad={RULER_H} barHeight={LANE_H * (lanes || 2)} {levels} />
      <div class="wave" bind:clientWidth={width}>
        {#if loading && !wave}
          <p class="muted pad">{t('edit.analysing')}</p>
        {/if}
        <div class="lanes" style:display={wave ? 'block' : 'none'}>
          <canvas
            bind:this={canvas}
            style:width="100%"
            style:height="{RULER_H + LANE_H * lanes}px"
            onpointerdown={onDown}
            onpointermove={onMove}
            onpointerup={onUp}
            onpointercancel={onUp}
          ></canvas>
          <div class="labels">
            {#each Array(lanes) as _, i}<span style:top="{RULER_H + i * LANE_H + 4}px">{laneLabel(i)}</span>{/each}
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
              aria-controls="tp-title"
              aria-orientation="horizontal"
              aria-valuenow={Math.round(winStart)}
              tabindex="-1"
            ></div>
          </div>
        {/if}
      </div>
      </div>

      <div class="row">
        <button class="primary" onclick={togglePlay} disabled={!wave}>
          {active && playing ? '⏸ ' + t('edit.pause') : '▶ ' + t('edit.play')}
        </button>
        <button onclick={stop} disabled={!wave || (!active && posMs <= startMs)}>■ {t('player.stop')}</button>
        <span class="timer">{formatDuration(rel)} / {formatDuration(duration)}</span>
        <span class="grow"></span>
        <button onclick={() => zoomButtons(1 / 2)} disabled={!wave || !zoomed} title={t('edit.zoomOut')}>−</button>
        <span class="timer">{zoomText}</span>
        <button onclick={() => zoomButtons(2)} disabled={!wave || winMs <= Math.min(MIN_VIEW_MS, duration)} title={t('edit.zoomIn')}>+</button>
        <button onclick={() => setView(0, duration)} disabled={!wave || !zoomed}>{t('edit.zoomFit')}</button>
      </div>

      {#if error}<p class="error small" role="alert">{error}</p>{/if}
      <p class="muted small">{t('player.hint')} {t('edit.zoomHint')}</p>

      <div class="row actions">
        <button onclick={close}>{t('close')}</button>
      </div>
    </div>
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
  .player {
    width: min(900px, 94vw);
    max-height: 94vh;
    overflow: auto;
    padding: 20px 24px;
    display: flex;
    flex-direction: column;
    gap: 10px;
    border: 1px solid var(--control-border);
  }
  .player > :global(*) {
    flex-shrink: 0;
  }
  .deck {
    display: flex;
    align-items: flex-start;
  }
  .wave {
    flex: 1;
    min-width: 0;
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
</style>
