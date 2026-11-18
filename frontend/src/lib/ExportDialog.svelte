<script lang="ts">
  import { t } from './i18n.svelte'
  import { CancelJob, ChooseFolder, ExportConflicts, ExportPlan, ExportPrefs, ExportTracks, RevealFolder } from '../../wailsjs/go/main/App'
  import { exporter as exp } from '../../wailsjs/go/models'
  import { ask, errText, formatDuration, ui } from './store.svelte'

  const FORMATS = ['wav', 'mp3', 'flac-cue'] as const
  const BITRATES = [320, 256, 192, 128]

  let plan = $state<exp.Plan | null>(null)
  let format = $state<string>('mp3')
  let bitrate = $state(320)
  let rate = $state(0)
  let coverMax = $state(1)
  let embed = $state(true)
  let original = $state(false)
  let busy = $state(false)
  let error = $state('')
  let result = $state<{ n: number; dir: string } | null>(null)

  $effect(() => {
    if (!ui.exportOpen) return
    plan = null
    error = ''
    result = null
    Promise.all([ExportPlan(), ExportPrefs()])
      .then(([p, prefs]) => {
        plan = p
        format = prefs.format
        bitrate = prefs.bitrate
        rate = prefs.wavRate
        coverMax = prefs.coverMaxMB
        embed = prefs.embedCover
        original = !!prefs.exportOriginal
        const pick = p.formats.find((f) => f.id === format)
        if (!pick?.available) format = p.formats.find((f) => f.available)?.id ?? format
      })
      .catch((e) => (error = errText(e)))
  })

  const info = $derived(plan?.formats.find((f) => f.id === format))
  const bitrates = $derived(info?.bitrates?.length ? info.bitrates : BITRATES)
  $effect(() => {
    if (format === 'mp3' && !bitrates.includes(bitrate)) bitrate = bitrates[bitrates.length - 1]
  })
  const rates = $derived(info?.rates ?? [])
  $effect(() => {
    if (format === 'wav' && rate !== 0 && !rates.includes(rate)) rate = 0
  })
  const pct = $derived(ui.job?.kind === 'export' && ui.job.total > 0 ? Math.min(100, Math.round((ui.job.done / ui.job.total) * 100)) : null)
  const coverField = $derived(format !== 'flac-cue' && embed && !!plan?.hasCover)
  const coverMaxOk = $derived(typeof coverMax === 'number' && coverMax >= 0.1 && coverMax <= 50)
  const canExport = $derived(!!plan && plan.tracks > 0 && !!info?.available && !busy && (!coverField || coverMaxOk))

  function close() {
    if (!busy) ui.exportOpen = false
  }

  async function run() {
    error = ''
    result = null
    const dir = await ChooseFolder(t('export.folderTitle'))
    if (!dir) return
    busy = true
    try {
      const clash = await ExportConflicts(format, dir, original)
      if (clash.length > 0) {
        const shown = clash.slice(0, 10).join('\n') + (clash.length > 10 ? '\n…' : '')
        const a = await ask({
          title: t('export.replaceTitle'),
          message: t('export.replaceText', { files: shown }),
          buttons: [
            { id: 'cancel', label: t('cancel') },
            { id: 'yes', label: t('export.replaceYes'), kind: 'danger' },
          ],
          cancel: 'cancel',
        })
        if (a !== 'yes') return
      }
      const r = await ExportTracks(new exp.Options({ format, bitrate, sampleRate: format === 'wav' ? rate : 0, coverMaxMB: coverMax, embedCover: embed, exportOriginal: original, dir }))
      result = { n: r.files.length, dir: r.dir }
    } catch (e) {
      const m = errText(e)
      if (!m.includes('context canceled')) error = m
    } finally {
      busy = false
      ui.job = null
    }
  }
</script>

<svelte:window onkeydown={(e) => ui.exportOpen && !ui.ask && e.key === 'Escape' && close()} />

{#if ui.exportOpen}
  <div class="backdrop" role="presentation">
    <div class="island dialog" role="dialog" aria-modal="true" aria-labelledby="ex-title">
      <h2 id="ex-title">{t('export.title')}</h2>
      {#if plan}
        {#if plan.tracks === 0}
          <p class="muted">{t('export.noTracks')}</p>
        {:else}
          <p class="muted">{t('export.summary', { n: plan.tracks, duration: formatDuration(plan.durationMs) })}</p>
        {/if}
        <fieldset disabled={busy}>
          <legend>{t('export.format')}</legend>
          {#each plan.formats as f (f.id)}
            <label class="opt">
              <input type="radio" name="ex-format" value={f.id} bind:group={format} disabled={!f.available} />
              {t(f.id === 'wav' ? 'export.wav' : f.id === 'mp3' ? 'export.mp3' : 'export.flac')}
            </label>
            {#if !f.available && f.reason}<p class="muted small indent">{f.reason}</p>{/if}
          {/each}
        </fieldset>
        {#if format === 'mp3' && info?.available}
          <div class="row">
            <label for="ex-br">{t('export.bitrate')}</label>
            <select id="ex-br" bind:value={bitrate} disabled={busy}>
              {#each bitrates as b (b)}<option value={b}>{t('export.kbps', { n: b })}</option>{/each}
            </select>
          </div>
        {/if}
        {#if format === 'wav' && info?.available}
          <div class="row">
            <label for="ex-rate">{t('export.rate')}</label>
            <select id="ex-rate" bind:value={rate} disabled={busy}>
              <option value={0}>{info.rate ? t('export.rateSameN', { n: info.rate }) : t('export.rateSame')}</option>
              {#each rates as r (r)}<option value={r}>{t('export.hz', { n: r })}</option>{/each}
            </select>
          </div>
        {/if}
        <label class="opt">
          <input type="checkbox" bind:checked={embed} disabled={busy || !plan.hasCover} />
          {t('export.embed')}
        </label>
        <label class="opt">
          <input type="checkbox" bind:checked={original} disabled={busy} />
          {t('export.original')}
        </label>
        {#if original}
          <p class="muted small">
            {plan.originals?.length ? t('export.originalFiles', { files: plan.originals.join(', ') }) : t('export.originalNone')}
          </p>
        {/if}
        {#if coverField}
          <div class="row">
            <label for="ex-cover">{t('export.coverMax')}</label>
            <input id="ex-cover" type="number" min="0.1" max="50" step="0.1" bind:value={coverMax} disabled={busy} aria-invalid={!coverMaxOk} />
          </div>
          {#if !coverMaxOk}<p class="error small" role="alert">{t('export.coverMaxInvalid')}</p>{/if}
          <p class="muted small">{t('export.coverMaxNote')}</p>
        {/if}
        <p class="muted small">
          {format === 'wav' ? t('export.noteWav') : format === 'mp3' ? t('export.noteMp3') : t('export.noteFlac')}
          {plan.hasCover ? t('export.noteCover') : t('export.noCover')}
        </p>
      {/if}
      {#if error}<p class="error small" role="alert">{error}</p>{/if}
      {#if busy && ui.job?.kind === 'export'}
        <progress max="100" value={pct ?? undefined}></progress>
        <p class="muted small">{t('export.running')}{pct !== null ? ` ${pct}%` : ''}</p>
      {/if}
      {#if result}
        <p class="small">{t('export.done', { n: result.n, dir: result.dir })}</p>
      {/if}
      <div class="row actions">
        {#if busy}
          <button class="danger" onclick={() => CancelJob()}>{t('export.cancelRun')}</button>
        {:else}
          {#if result}<button onclick={() => RevealFolder(result!.dir).catch((e) => (error = errText(e)))}>{t('export.reveal')}</button>{/if}
          <button onclick={close}>{t('close')}</button>
          <button class="primary" onclick={run} disabled={!canExport}>{t('export.go')}</button>
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
    width: min(520px, 90vw);
    padding: 20px 24px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    border: 1px solid var(--control-border);
  }
  fieldset {
    border: 1px solid var(--control-border);
    border-radius: 6px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 8px 12px;
  }
  .opt {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .indent {
    margin: 0 0 0 24px;
  }
  .actions {
    justify-content: flex-end;
    margin-top: 12px;
  }
  .small {
    font-size: calc(12px * var(--font-scale, 1));
  }
  progress {
    width: 100%;
  }
</style>
