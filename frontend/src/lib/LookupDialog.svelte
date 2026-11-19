<script lang="ts">
  import { t } from './i18n.svelte'
  import { CancelJob, LookupApply, LookupRelease, LookupSearch } from '../../wailsjs/go/main/App'
  import { lookup, main } from '../../wailsjs/go/models'
  import { SIDES, errText, formatDuration, ui } from './store.svelte'

  let artist = $state('')
  let album = $state('')
  let results = $state<lookup.Release[] | null>(null)
  let preview = $state<main.LookupPreview | null>(null)
  let mediaA = $state(0)
  let mediaB = $state(1)
  let useCover = $state(false)
  let busy = $state(false)
  let error = $state('')
  let applied = $state<main.LookupResult | null>(null)

  $effect(() => {
    if (!ui.lookupOpen) return
    const p = ui.project
    artist = p?.compilation ? '' : (p?.artist ?? '')
    album = p?.album ?? ''
    results = null
    preview = null
    applied = null
    error = ''
    useCover = !p?.cover
  })

  async function request<T>(fn: () => Promise<T>): Promise<T | null> {
    error = ''
    busy = true
    try {
      return await fn()
    } catch (e) {
      const m = errText(e)
      if (!m.includes('context canceled')) error = m
      return null
    } finally {
      busy = false
    }
  }

  async function search() {
    preview = null
    applied = null
    const r = await request(() => LookupSearch(artist, album))
    if (r) results = r
  }

  async function pick(r: lookup.Release, media: number[] | null = null) {
    applied = null
    const p = await request(() => LookupRelease(r.mbid, media as number[]))
    if (!p) return
    preview = p
    mediaA = p.media?.[0] ?? 0
    mediaB = p.media?.[1] ?? -1
  }

  function remap() {
    if (!preview) return
    const media = mediaB >= 0 && mediaB !== mediaA ? [mediaA, mediaB] : [mediaA]
    pick(preview.release, media)
  }

  async function apply() {
    if (!preview) return
    const res = await request(() => LookupApply(preview!.release.mbid, preview!.media ?? [], useCover))
    if (!res) return
    applied = res
    ui.project = res.info
    ui.coverV++
  }

  function close() {
    if (!busy) ui.lookupOpen = false
  }

  const sideTracks = (side: string) => ((side === 'A' ? preview?.mapping.a : preview?.mapping.b) ?? []) as lookup.Track[]
  const year = (d: string) => (d ?? '').slice(0, 4)
  const mediumName = (m: lookup.Medium, i: number) => `${i + 1}. ${m.format || t('lookup.medium')}${m.title ? ' — ' + m.title : ''} (${m.tracks?.length ?? 0})`
  const recordedSides = $derived(SIDES.filter((s) => !!ui.project?.sides?.[s]))
</script>

<svelte:window onkeydown={(e) => ui.lookupOpen && !ui.ask && e.key === 'Escape' && close()} />

{#if ui.lookupOpen}
  <div class="backdrop" role="presentation">
    <div class="island dialog" role="dialog" aria-modal="true" aria-labelledby="lk-title">
      <h2 id="lk-title">{t('lookup.title')}</h2>
      <form
        class="row search"
        onsubmit={(e) => {
          e.preventDefault()
          search()
        }}
      >
        <label for="lk-artist">{t('rec.artist')}</label>
        <input id="lk-artist" type="text" bind:value={artist} maxlength="200" disabled={busy} />
        <label for="lk-album">{t('rec.album')}</label>
        <input id="lk-album" type="text" bind:value={album} maxlength="200" disabled={busy} />
        <button class="primary" type="submit" disabled={busy || (!artist.trim() && !album.trim())}>{t('lookup.search')}</button>
      </form>
      <p class="muted small">{t('lookup.privacy')}</p>

      {#if results && !preview}
        <div class="list" role="listbox" aria-label={t('lookup.results')}>
          {#each results as r (r.mbid)}
            <button class="result" role="option" aria-selected="false" disabled={busy} onclick={() => pick(r)}>
              <span class="r-title">{r.title}</span>
              <span class="muted">{r.artist}</span>
              <span class="muted small">{[year(r.date), r.country, (r.formats ?? []).join(' + '), t('lookup.tracksN', { n: r.trackCount })].filter(Boolean).join(' · ')}</span>
            </button>
          {/each}
        </div>
      {/if}

      {#if preview}
        <p>
          <strong>{preview.release.title}</strong> — {preview.release.artist}
          <span class="muted small">{[year(preview.release.date), preview.release.country].filter(Boolean).join(' · ')}</span>
        </p>
        {#if (preview.release.media?.length ?? 0) > 2}
          <div class="row">
            <label for="lk-ma">{t('lookup.mediumFor', { side: 'A' })}</label>
            <select id="lk-ma" bind:value={mediaA} onchange={remap} disabled={busy || !!applied}>
              {#each preview.release.media as m, i (i)}<option value={i}>{mediumName(m, i)}</option>{/each}
            </select>
            <label for="lk-mb">{t('lookup.mediumFor', { side: 'B' })}</label>
            <select id="lk-mb" bind:value={mediaB} onchange={remap} disabled={busy || !!applied}>
              <option value={-1}>{t('lookup.none')}</option>
              {#each preview.release.media as m, i (i)}<option value={i}>{mediumName(m, i)}</option>{/each}
            </select>
          </div>
        {/if}
        <div class="sides">
          {#each SIDES as s (s)}
            <div class="side">
              <h3>{t('lookup.side', { side: s })}</h3>
              {#if !ui.project?.sides?.[s]}
                <p class="muted small">{t('lookup.notRecorded')}</p>
              {/if}
              {#if sideTracks(s).length === 0}
                <p class="muted small">{t('lookup.noTracks')}</p>
              {:else}
                <ol>
                  {#each sideTracks(s) as tr, i (i)}
                    <li class:unmatched={!!ui.project?.sides?.[s] && (preview.recorded?.[s] ?? 0) > 0 && i >= (preview.recorded?.[s] ?? 0)}>
                      <span class="li-row">
                        <span>{tr.title}{tr.artist && tr.artist !== preview.release.artist ? ` — ${tr.artist}` : ''}</span>
                        <span class="muted small">{tr.lengthMs ? formatDuration(tr.lengthMs) : '—'}</span>
                      </span>
                    </li>
                  {/each}
                </ol>
                {#if ui.project?.sides?.[s]}
                  <p class="muted small">{t('lookup.recordedN', { n: preview.recorded?.[s] ?? 0 })}</p>
                {/if}
              {/if}
            </div>
          {/each}
        </div>
        <label class="opt">
          <input type="checkbox" bind:checked={useCover} disabled={busy || !!applied} />
          {t('lookup.useCover')}
        </label>
        <p class="muted small">{t('lookup.applyNote')}</p>
      {/if}

      {#if error}<p class="error small" role="alert">{error}</p>{/if}
      {#if busy}<p class="muted small">{t('lookup.busyText')}</p>{/if}
      {#if applied}
        <p class="small">{t('lookup.applied')}</p>
        {#each SIDES as s (s)}
          {#if (applied.unmatched?.[s] ?? 0) > 0 && (preview?.recorded?.[s] ?? 0) > 0}
            <p class="muted small">{t('lookup.unmatched', { side: s, n: applied.unmatched[s] })}</p>
          {/if}
        {/each}
        {#if applied.coverError}<p class="muted small">{t('lookup.coverSkipped', { reason: applied.coverError })}</p>{/if}
      {/if}

      <div class="row actions">
        {#if busy}
          <button class="danger" onclick={() => CancelJob()}>{t('cancel')}</button>
        {:else}
          {#if preview && !applied}<button onclick={() => (preview = null)}>{t('lookup.back')}</button>{/if}
          <button onclick={close}>{t('close')}</button>
          {#if preview && !applied}<button class="primary" onclick={apply}>{t('lookup.apply')}</button>{/if}
        {/if}
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
    z-index: 10;
  }
  .dialog {
    width: min(720px, 92vw);
    max-height: 90vh;
    overflow: auto;
    padding: 20px 24px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    border: 1px solid var(--control-border);
  }
  .search input {
    flex: 1;
    min-width: 0;
  }
  .list {
    display: flex;
    flex-direction: column;
    gap: 4px;
    max-height: 320px;
    overflow: auto;
  }
  .result {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    text-align: left;
    gap: 2px;
    height: auto;
    padding: 6px 10px;
  }
  .r-title {
    font-weight: 600;
  }
  .sides {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .side h3 {
    margin: 0 0 4px;
    font-size: calc(13px * var(--font-scale, 1));
  }
  ol {
    margin: 0;
    padding-left: 22px;
  }
  .li-row {
    display: flex;
    justify-content: space-between;
    gap: 8px;
  }
  li.unmatched {
    opacity: 0.55;
  }
  .opt {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .actions {
    justify-content: flex-end;
    margin-top: 12px;
  }
  .small {
    font-size: calc(12px * var(--font-scale, 1));
  }
</style>
