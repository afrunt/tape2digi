<script lang="ts">
  import { SetTrackDetails } from '../../wailsjs/go/main/App'
  import { t } from './i18n.svelte'
  import { errText, formatClock, ui } from './store.svelte'

  let { side }: { side: 'A' | 'B' } = $props()

  type Track = { startMs: number; endMs: number; title: string; artist: string; albumArtist: string }

  function tracksOf(key: string): Track[] {
    const list = [...(ui.project?.sides?.[key]?.markers ?? [])].sort((a, b) => a.positionMs - b.positionMs)
    const out: Track[] = []
    for (let i = 0; i + 1 < list.length; i++) {
      const s = list[i]
      const e = list[i + 1]
      if (s.kind === 'start' && e.kind === 'end') {
        out.push({ startMs: s.positionMs, endMs: e.positionMs, title: s.title ?? '', artist: s.artist ?? '', albumArtist: s.albumArtist ?? '' })
      }
    }
    return out
  }

  const tracks = $derived(tracksOf(side))
  // Side B continues the numbering after the last track of side A.
  const offset = $derived(side === 'B' ? tracksOf('A').length : 0)
  const total = $derived(tracks.reduce((n, x) => n + (x.endMs - x.startMs), 0))
  // An empty artist follows the project artist, which a compilation keeps.
  const projectArtist = $derived(ui.project?.artist ?? '')
  const readOnly = $derived(ui.status.state === 'recording' || ui.status.state === 'paused' || ui.status.importSide !== '' || !!ui.job)
  let error = $state('')

  const num = (i: number) => String(offset + i + 1).padStart(2, '0')

  async function commit(tr: Track, field: 'title' | 'artist' | 'albumArtist', raw: string) {
    let v = raw.trim()
    if (field !== 'title' && v === projectArtist) v = ''
    if (v === tr[field]) return
    const next = { title: tr.title, artist: tr.artist, albumArtist: tr.albumArtist, [field]: v }
    try {
      ui.project = await SetTrackDetails(side, tr.startMs, next.title, next.artist, next.albumArtist)
      error = ''
    } catch (e) {
      error = errText(e)
    }
  }

  function openPlayer(tr: Track, i: number) {
    ui.play = { side, startMs: tr.startMs, endMs: tr.endMs, num: num(i), title: tr.title }
  }

  function onKey(e: KeyboardEvent, shown: string) {
    const el = e.currentTarget as HTMLInputElement
    if (e.key === 'Enter') el.blur()
    else if (e.key === 'Escape') {
      el.value = shown
      el.blur()
      e.stopPropagation()
    }
  }
</script>

{#if tracks.length > 0}
  <div class="tracks">
    <table>
      <thead>
        <tr>
          <th class="num">{t('tracks.num')}</th>
          <th>{t('tracks.title')}</th>
          <th>{t('tracks.artist')}</th>
          {#if !ui.project?.compilation}<th>{t('tracks.albumArtist')}</th>{/if}
          <th class="dur">{t('tracks.duration')}</th>
          <th class="act"></th>
        </tr>
      </thead>
      <tbody>
        {#each tracks as tr, i (tr.startMs)}
          {@const artist = tr.artist || projectArtist}
          {@const albumArtist = tr.albumArtist || projectArtist}
          <tr>
            <td class="num">{num(i)}</td>
            <td>
              <input type="text" maxlength="200" value={tr.title} disabled={readOnly} aria-label={t('tracks.title')} onchange={(e) => commit(tr, 'title', e.currentTarget.value)} onkeydown={(e) => onKey(e, tr.title)} />
            </td>
            <td>
              <input type="text" maxlength="200" class:dflt={!tr.artist} value={artist} disabled={readOnly} aria-label={t('tracks.artist')} onchange={(e) => commit(tr, 'artist', e.currentTarget.value)} onkeydown={(e) => onKey(e, artist)} />
            </td>
            {#if !ui.project?.compilation}
              <td>
                <input type="text" maxlength="200" class:dflt={!tr.albumArtist} value={albumArtist} disabled={readOnly} aria-label={t('tracks.albumArtist')} onchange={(e) => commit(tr, 'albumArtist', e.currentTarget.value)} onkeydown={(e) => onKey(e, albumArtist)} />
              </td>
            {/if}
            <td class="dur">{formatClock(tr.endMs - tr.startMs)}</td>
            <td class="act">
              <button class="icon" disabled={readOnly} title={t('tracks.play')} aria-label={t('tracks.play')} onclick={() => openPlayer(tr, i)}>▶</button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
    <div class="row total">
      <p class="muted small">{t('tracks.total', { count: tracks.length, time: formatClock(total) })}</p>
    </div>
    {#if error}<p class="error small" role="alert">{error}</p>{/if}
  </div>
{/if}

<style>
  .tracks {
    margin-top: 12px;
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th {
    text-align: left;
    font-weight: 600;
    color: var(--text-muted);
    padding: 0 4px 6px;
    white-space: nowrap;
  }
  td {
    padding: 2px 4px;
  }
  td input {
    width: 100%;
    box-sizing: border-box;
  }
  .num,
  .dur {
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
    color: var(--text-muted);
  }
  .num {
    width: 2.5em;
  }
  .act {
    width: 2.5em;
    text-align: right;
  }
  .icon {
    padding: 2px 8px;
  }
  .dflt {
    color: var(--text-muted);
  }
  .total {
    margin: 8px 0 0;
  }
  .total p {
    margin: 0;
  }
</style>
