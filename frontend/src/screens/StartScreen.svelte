<script lang="ts">
  import { t } from '../lib/i18n.svelte'
  import { onDestroy, onMount } from 'svelte'
  import { ChooseFolder, CreateProject, OpenProject, RecentProjects, RemoveRecent } from '../../wailsjs/go/main/App'
  import { project } from '../../wailsjs/go/models'
  import { errText, hooks, ui } from '../lib/store.svelte'

  let recents: project.RecentEntry[] = $state([])
  let creating = $state(false)
  let name = $state('')
  let parent = $state('')
  let error = $state('')
  let busy = $state(false)

  async function loadRecents() {
    recents = (await RecentProjects()) ?? []
  }

  onMount(loadRecents)

  async function pickParent() {
    const dir = await ChooseFolder(t('start.chooseNewTitle'))
    if (dir) parent = dir
  }

  async function create() {
    error = ''
    if (!name.trim()) return (error = t('start.enterName'))
    if (!parent) return (error = t('start.chooseFolder'))
    busy = true
    try {
      ui.project = await CreateProject(parent, name)
    } catch (e) {
      error = errText(e)
    } finally {
      busy = false
    }
  }

  async function open(dir: string) {
    error = ''
    busy = true
    try {
      ui.project = await OpenProject(dir)
    } catch (e) {
      error = errText(e)
    } finally {
      busy = false
    }
  }

  async function openFolder() {
    const dir = await ChooseFolder(t('start.chooseOpenTitle'))
    if (dir) await open(dir)
  }

  const onOpen = () => {
    if (!busy && !ui.ask) void openFolder()
  }
  hooks.openProject = onOpen
  onDestroy(() => {
    if (hooks.openProject === onOpen) hooks.openProject = null
  })

  async function remove(path: string) {
    await RemoveRecent(path)
    await loadRecents()
  }
</script>

<main class="island start">
  <h1>tape2digi</h1>
  <p class="muted">{t('start.tagline')}</p>

  {#if error}<p class="error" role="alert">{error}</p>{/if}

  {#if !creating}
    <div class="row actions">
      <button class="primary" onclick={() => (creating = true)} disabled={busy}>{t('start.newEllipsis')}</button>
      <button onclick={openFolder} disabled={busy}>{t('start.open')}</button>
    </div>
  {:else}
    <form
      class="new"
      onsubmit={(e) => {
        e.preventDefault()
        create()
      }}
    >
      <h2>{t('start.newTitle')}</h2>
      <label for="pname">{t('projectName')}</label>
      <input id="pname" type="text" bind:value={name} placeholder={t('start.namePlaceholder')} maxlength="200" />
      <label for="pparent">{t('folder')}</label>
      <div class="row">
        <input id="pparent" type="text" bind:value={parent} placeholder={t('chooseFolderPlaceholder')} readonly />
        <button type="button" onclick={pickParent}>{t('choose')}</button>
      </div>
      <p class="muted small">{t('start.createNote')}</p>
      <div class="row">
        <button class="primary" type="submit" disabled={busy}>{t('create')}</button>
        <button type="button" onclick={() => ((creating = false), (error = ''))}>{t('cancel')}</button>
      </div>
    </form>
  {/if}

  <h2 class="recent-title">{t('start.recent')}</h2>
  {#if recents.length === 0}
    <p class="muted">{t('start.noRecent')}</p>
  {:else}
    <ul class="recent">
      {#each recents as r (r.path)}
        <li class:unavailable={!r.available}>
          <button class="item" disabled={!r.available || busy} onclick={() => open(r.path)}>
            <span class="rname">{r.name}</span>
            <span class="rpath muted">{r.path}</span>
          </button>
          {#if !r.available}
            <span class="error small">{t('start.unavailable')}</span>
            <button onclick={() => remove(r.path)}>{t('remove')}</button>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</main>

<style>
  .start {
    flex: 1;
    overflow: auto;
    max-width: 760px;
    width: 100%;
    margin: 0 auto;
    padding: 28px 32px;
  }
  .actions {
    margin: 16px 0 8px;
  }
  .new {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 16px 0 8px;
  }
  .new input {
    width: 100%;
  }
  .new .row input {
    flex: 1;
  }
  .small {
    font-size: calc(12px * var(--font-scale, 1));
  }
  .recent-title {
    margin-top: 28px;
  }
  .recent {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .recent li {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .item {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    height: auto;
    padding: 8px 12px;
    text-align: left;
    overflow: hidden;
  }
  .rname {
    font-weight: 600;
  }
  .rpath {
    font-size: calc(12px * var(--font-scale, 1));
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
