<script lang="ts">
  import { t } from './i18n.svelte'
  import { onMount } from 'svelte'
  import { meter } from '../lib/store.svelte'

  let { channels }: { channels: number } = $props()

  const FLOOR_DB = -60
  const HOLD_MS = 1200
  const DECAY_PER_S = 0.5 // fraction of full scale per second

  const names = $derived(channels === 1 ? ['M'] : ['L', 'R'])
  let covers: HTMLElement[] = $state([])
  let holds: HTMLElement[] = $state([])
  let clipLamps: HTMLElement[] = $state([])
  let clipped: boolean[] = [false, false]

  const shown = [0, 0]
  const peakHold = [0, 0]
  const peakAt = [0, 0]

  function pos(db: number): number {
    return Math.min(1, Math.max(0, (db - FLOOR_DB) / -FLOOR_DB))
  }

  onMount(() => {
    let raf = 0
    let last = performance.now()
    const tick = (now: number) => {
      const dt = (now - last) / 1000
      last = now
      for (let i = 0; i < channels; i++) {
        const l = meter.levels[i]
        const target = l ? pos(l.peakDb) : 0
        shown[i] = target >= shown[i] ? target : Math.max(target, shown[i] - DECAY_PER_S * dt)
        if (shown[i] >= peakHold[i] || now - peakAt[i] > HOLD_MS) {
          peakHold[i] = shown[i]
          peakAt[i] = now
        }
        if (l?.clip && !clipped[i]) {
          clipped[i] = true
          clipLamps[i]?.classList.add('on')
        }
        covers[i] && (covers[i].style.transform = `scaleX(${1 - shown[i]})`)
        holds[i] && (holds[i].style.transform = `translateX(${peakHold[i] * 100}cqw)`)
      }
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  })

  function resetClip() {
    clipped = [false, false]
    clipLamps.forEach((c) => c?.classList.remove('on'))
  }
</script>

<div class="meter" role="img" aria-label={t('meter.level')}>
  {#each names as name, i}
    <div class="channel">
      <span class="label">{name}</span>
      <div class="track">
        <div class="cover" bind:this={covers[i]}></div>
        <div class="hold" bind:this={holds[i]}></div>
      </div>
      <button class="clip" bind:this={clipLamps[i]} title={t('meter.clipTitle')} aria-label={t('meter.clipReset', { name })} onclick={resetClip}></button>
    </div>
  {/each}
</div>

<style>
  .meter {
    display: flex;
    flex-direction: column;
    gap: 4px;
    width: 260px;
  }
  .channel {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .label {
    width: 12px;
    color: var(--text-muted);
    font-size: calc(11px * var(--font-scale, 1));
  }
  .track {
    position: relative;
    flex: 1;
    height: 10px;
    overflow: hidden;
    container-type: inline-size;
    border-radius: 3px;
    background: linear-gradient(to right, var(--meter-ok) 0%, var(--meter-ok) 65%, var(--meter-warn) 85%, var(--danger) 100%);
  }
  .cover {
    position: absolute;
    inset: 0;
    background: var(--meter-track);
    transform-origin: right center;
    will-change: transform;
  }
  .hold {
    position: absolute;
    top: 0;
    left: -2px;
    width: 2px;
    height: 100%;
    background: var(--text);
    will-change: transform;
  }
  .clip {
    min-width: 0;
    width: 10px;
    height: 10px;
    padding: 0;
    border-radius: 50%;
    background: var(--clip-off);
    border: none;
  }
  .clip:global(.on) {
    background: var(--danger);
  }
</style>
