<script lang="ts">
  import { t } from './i18n.svelte'
  import { CancelJob, ChooseFolder, ConfirmQuit, SaveProjectAs } from '../../wailsjs/go/main/App'
  import { answerAsk, errText, formatSize, ui } from './store.svelte'

  let parent = $state('')
  let name = $state('')
  let busy = $state(false)
  let error = $state('')

  $effect(() => {
    if (ui.saveAsOpen) {
      name = ui.project?.name ?? ''
      error = ''
    }
  })

  async function pickFolder() {
    const dir = await ChooseFolder(t('dlg.copyFolderTitle'), 'projectParent')
    if (dir) parent = dir
  }

  async function saveAs() {
    busy = true
    error = ''
    try {
      ui.project = await SaveProjectAs(parent, name)
      ui.coverV++
      ui.saveAsOpen = false
    } catch (e) {
      error = errText(e)
    } finally {
      busy = false
      ui.job = null
    }
  }

  async function quit(action: 'save' | 'discard') {
    try {
      await ConfirmQuit(action)
    } catch (e) {
      ui.quit = null
      ui.error = errText(e)
    }
  }

  const pct = $derived(ui.job && ui.job.total > 0 ? Math.min(100, Math.round((ui.job.done / ui.job.total) * 100)) : null)
</script>

<svelte:window
  onkeydown={(e) => {
    if (ui.ask && e.key === 'Escape') answerAsk(ui.ask.cancel)
  }}
/>

{#if ui.ask}
  <div class="backdrop" role="presentation">
    <div class="island dialog" role="alertdialog" aria-modal="true" aria-labelledby="ask-title" aria-describedby="ask-msg">
      <h2 id="ask-title">{ui.ask.title}</h2>
      <p id="ask-msg" class="ask-msg">{ui.ask.message}</p>
      <div class="row actions">
        {#each ui.ask.buttons as b (b.id)}
          <!-- svelte-ignore a11y_autofocus -->
          <button class={b.kind ?? ''} autofocus={b.id === ui.ask.cancel} onclick={() => answerAsk(b.id)}>{b.label}</button>
        {/each}
      </div>
    </div>
  </div>
{/if}

{#if ui.saveAsOpen}
  <div class="backdrop" role="presentation">
    <div class="island dialog" role="dialog" aria-modal="true" aria-labelledby="sa-title">
      <h2 id="sa-title">{t('saveAs')}</h2>
      <p class="muted">{t('dlg.saveAsNote')}</p>
      <label for="sa-name">{t('projectName')}</label>
      <input id="sa-name" type="text" bind:value={name} maxlength="200" disabled={busy} onkeydown={(e) => e.key === 'Enter' && !e.isComposing && !busy && parent && name.trim() && saveAs()} />
      <label for="sa-parent">{t('folder')}</label>
      <div class="row">
        <input id="sa-parent" class="grow" type="text" value={parent} readonly placeholder={t('chooseFolderPlaceholder')} />
        <button onclick={pickFolder} disabled={busy}>{t('choose')}</button>
      </div>
      {#if error}<p class="error small" role="alert">{error}</p>{/if}
      {#if busy && ui.job?.kind === 'saveAs'}
        <progress max="100" value={pct ?? undefined}></progress>
        <p class="muted small">{t('dlg.copying')} {formatSize(ui.job.done)}{pct !== null ? ` (${pct}%)` : ''}</p>
      {/if}
      <div class="row actions">
        {#if busy}
          <button class="danger" onclick={() => CancelJob()}>{t('dlg.cancelCopy')}</button>
        {:else}
          <button onclick={() => (ui.saveAsOpen = false)}>{t('cancel')}</button>
          <button class="primary" onclick={saveAs} disabled={!parent || !name.trim()}>{t('save')}</button>
        {/if}
      </div>
    </div>
  </div>
{/if}

{#if ui.quit}
  <div class="backdrop" role="presentation">
    <div class="island dialog" role="alertdialog" aria-modal="true" aria-labelledby="q-title">
      <h2 id="q-title">{t('dlg.quitTitle')}</h2>
      {#if ui.quit.recording}
        <p>{t('dlg.quitRecording')}</p>
      {/if}
      {#if ui.quit.job}
        <p>{t('dlg.quitJob')}</p>
      {/if}
      {#if ui.quit.dirty}
        <p>{t('unsavedHint')}</p>
      {/if}
      <div class="row actions">
        <button onclick={() => (ui.quit = null)}>{t('dlg.stay')}</button>
        {#if ui.quit.dirty}
          <button class="danger" onclick={() => quit('discard')}>{t('dontSave')}</button>
          <button class="primary" onclick={() => quit('save')}>{t('dlg.saveAndQuit')}</button>
        {:else}
          <button class="primary" onclick={() => quit('discard')}>{t('dlg.quit')}</button>
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
  .dialog input[type='text'] {
    width: 100%;
  }
  .ask-msg {
    white-space: pre-line;
  }
  .actions {
    justify-content: flex-end;
    margin-top: 12px;
  }
  .grow {
    flex: 1;
  }
  .small {
    font-size: calc(12px * var(--font-scale, 1));
  }
  progress {
    width: 100%;
  }
</style>
