<script lang="ts">
  import { onMount } from 'svelte'
  import { t } from './i18n.svelte'

  type Level = { peakDb: number; clip: boolean }
  // `levels.v` is written by the poll outside reactive state and read every frame.
  let { channels, topPad, barHeight, levels }: { channels: number; topPad: number; barHeight: number; levels: { v: Level[] } } = $props()

  const FLOOR_DB = -60
  const HOLD_MS = 1000
  const DECAY_PER_S = 0.6
  const SCALE = [0, -3, -6, -10, -20, -40, -60]

  const names = $derived(channels === 1 ? ['M'] : ['L', 'R'])
  let covers: HTMLElement[] = $state([])
  let holds: HTMLElement[] = $state([])
  let lamps: HTMLElement[] = $state([])
  let clipped = [false, false]
  const shown = [0, 0]
  const peakHold = [0, 0]
  const peakAt = [0, 0]

  const pos = (db: number) => Math.min(1, Math.max(0, (db - FLOOR_DB) / -FLOOR_DB))

  onMount(() => {
    let raf = 0
    let last = performance.now()
    const tick = (now: number) => {
      const dt = (now - last) / 1000
      last = now
      for (let i = 0; i < channels; i++) {
        const l = levels.v[i]
        const target = l ? pos(l.peakDb) : 0
        shown[i] = target >= shown[i] ? target : Math.max(target, shown[i] - DECAY_PER_S * dt)
        if (shown[i] >= peakHold[i] || now - peakAt[i] > HOLD_MS) {
          peakHold[i] = shown[i]
          peakAt[i] = now
        }
        if (l?.clip && !clipped[i]) {
          clipped[i] = true
          lamps[i]?.classList.add('on')
        }
        covers[i] && (covers[i].style.transform = `scaleY(${1 - shown[i]})`)
        holds[i] && (holds[i].style.transform = `translateY(${-peakHold[i] * barHeight}px)`)
      }
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  })

  function resetClip() {
    clipped = [false, false]
    lamps.forEach((c) => c?.classList.remove('on'))
  }
</script>

{#snippet bar(i: number)}
  <div class="col">
    <button class="clip" bind:this={lamps[i]} style:height="{topPad - 6}px" title={t('meter.clipTitle')} aria-label={t('meter.clipReset', { name: names[i] })} onclick={resetClip}></button>
    <div class="bar" style:height="{barHeight}px">
      <div class="cover" bind:this={covers[i]}></div>
      <div class="hold" bind:this={holds[i]}></div>
    </div>
    <span class="name">{names[i]}</span>
  </div>
{/snippet}

<div class="meters" role="img" aria-label={t('meter.level')}>
  {@render bar(0)}
  <div class="scale" style:margin-top="{topPad}px" style:height="{barHeight}px">
    {#each SCALE as db}
      <span style:bottom="{pos(db) * 100}%">{db}</span>
    {/each}
  </div>
  {#if channels > 1}{@render bar(1)}{/if}
</div>

<style>
  .meters {
    display: flex;
    gap: 4px;
    flex-shrink: 0;
    margin-right: 8px;
  }
  .col {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0;
    width: 16px;
  }
  .bar {
    position: relative;
    width: 14px;
    overflow: hidden;
    border-radius: 3px;
    background: linear-gradient(to top, var(--meter-ok) 0%, var(--meter-ok) 65%, var(--meter-warn) 85%, var(--danger) 100%);
  }
  .cover {
    position: absolute;
    inset: 0;
    background: var(--meter-track);
    transform-origin: top center;
    will-change: transform;
  }
  .hold {
    position: absolute;
    left: 0;
    bottom: -2px;
    width: 100%;
    height: 2px;
    background: var(--text);
    will-change: transform;
  }
  .clip {
    min-width: 0;
    width: 14px;
    margin-bottom: 6px;
    padding: 0;
    border: none;
    border-radius: 3px;
    background: var(--clip-off);
  }
  .clip:global(.on) {
    background: var(--danger);
  }
  .name {
    margin-top: 2px;
    font-size: calc(11px * var(--font-scale, 1));
    color: var(--text-muted);
  }
  .scale {
    position: relative;
    width: 26px;
  }
  .scale span {
    position: absolute;
    left: 0;
    right: 0;
    transform: translateY(50%);
    text-align: center;
    font-family: var(--font-mono);
    font-size: calc(10px * var(--font-scale, 1));
    line-height: 1;
    color: var(--text-muted);
  }
</style>
