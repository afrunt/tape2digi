<script lang="ts">
  import { t, dateLocale } from '../lib/i18n.svelte'
  import { onDestroy, onMount } from 'svelte'
  import {
    CancelJob,
    ChooseAudioFile,
    ChooseImage,
    ChooseFolder,
    ClearCover,
    CloseProject,
    DeleteSide,
    DiscardChanges,
    EditHistoryLast,
    DiscardPart,
    Devices,
    GetSettings,
    ImportSide,
    OpenProject,
    OpenInput,
    PlaybackDevices,
    PauseRecording,
    RecoverPart,
    RedoLast,
    RenameProject,
    SetProjectDetails,
    ResumeRecording,
    RevealSide,
    SaveProject,
    SetCover,
    SetOnlineLookup,
    SetTracksReady,
    SetMonitor,
    SetMonitorDevice,
    SetAutoStop,
    StartRecording,
    UndoLast,
    Status,
    StopRecording,
  } from '../../wailsjs/go/main/App'
  import { audio } from '../../wailsjs/go/models'
  import LevelMeter from '../lib/LevelMeter.svelte'
  import TrackTable from '../lib/TrackTable.svelte'
  import { ask, errText, history, hooks, formatDuration, formatSize, SIDES, sideExistsMessage, ui } from '../lib/store.svelte'

  const RATES = [44100, 48000, 96000]

  let devices: audio.Device[] = $state([])
  let deviceName = $state('')
  let outputs: audio.Device[] = $state([])
  // Empty means the system default output.
  let outputName = $state('')
  const defaultOutput = $derived(outputs.find((d) => d.isDefault)?.name)
  let sampleRate = $state(44100)
  let channels = $state(2)
  let bitDepth = $state(16)
  let inputFailed = $state(false)
  let ready = $state(false)

  let nameDraft = $state(ui.project?.name ?? '')
  let nameFocused = $state(false)
  $effect(() => {
    const stored = ui.project?.name ?? ''
    if (!nameFocused) nameDraft = stored
  })
  // Calls that return project info run strictly in order, so a slow earlier
  // response never overwrites a newer one and Save/close see the latest edit.
  let queue: Promise<unknown> = Promise.resolve()
  function serial<T>(fn: () => Promise<T>): Promise<T> {
    const run = queue.then(fn, fn)
    queue = run.catch(() => {})
    return run
  }
  let nameError = $state('')

  let artistDraft = $state(ui.project?.artist ?? '')
  let yearDraft = $state(ui.project?.year ?? '')
  let artistFocused = $state(false)
  let yearFocused = $state(false)
  let albumDraft = $state(ui.project?.album ?? '')
  let albumFocused = $state(false)
  let genreDraft = $state(ui.project?.genre ?? '')
  let genreFocused = $state(false)
  let detailsError = $state('')
  $effect(() => {
    const p = ui.project
    if (!artistFocused) artistDraft = p?.artist ?? ''
    if (!yearFocused) yearDraft = p?.year ?? ''
    if (!albumFocused) albumDraft = p?.album ?? ''
    if (!genreFocused) genreDraft = p?.genre ?? ''
  })
  const yearOk = (y: string) => y === '' || (/^\d{4}$/.test(y) && +y >= 1900 && +y <= 2100)
  const albumOk = (a: string) => [...a].length <= 200 && !/\p{Cc}/u.test(a)
  const genreOk = (g: string) => [...g].length <= 100 && !/\p{Cc}/u.test(g)
  // An invalid field is not sent: the last accepted value goes instead, so a
  // partial year never blocks the artist or the checkbox.
  async function stageDetails(compilation = ui.project?.compilation ?? false) {
    if (!ui.project) return
    const year = yearOk(yearDraft.trim()) ? yearDraft.trim() : ui.project.year
    const artist = artistDraft
    const album = albumOk(albumDraft.trim()) ? albumDraft : ui.project.album
    const genre = genreOk(genreDraft.trim()) ? genreDraft : ui.project.genre
    try {
      const info = await serial(() => SetProjectDetails(artist, compilation, year, album, genre))
      detailsError = ''
      ui.project = info
    } catch (e) {
      detailsError = errText(e)
    }
  }
  function onYearInput() {
    yearDraft = yearDraft.replace(/\D/g, '').slice(0, 4)
    stageDetails()
  }
  async function leaveField(field: 'artist' | 'year' | 'album' | 'genre') {
    if (field === 'artist') artistFocused = false
    if (field === 'year') yearFocused = false
    if (field === 'album') albumFocused = false
    if (field === 'genre') genreFocused = false
    await stageDetails()
    detailsError = ''
    if (field === 'artist' && !artistFocused) artistDraft = ui.project?.artist ?? ''
    if (field === 'year' && !yearFocused) yearDraft = ui.project?.year ?? ''
    if (field === 'album' && !albumFocused) albumDraft = ui.project?.album ?? ''
    if (field === 'genre' && !genreFocused) genreDraft = ui.project?.genre ?? ''
  }
  const yearInvalid = $derived(!yearOk(yearDraft.trim()))
  const albumInvalid = $derived(!albumOk(albumDraft.trim()))
  const genreInvalid = $derived(!genreOk(genreDraft.trim()))
  const GENRES = ['Rock', 'Pop', 'Jazz', 'Blues', 'Classical', 'Folk', 'Country', 'Electronic', 'Hip-Hop', 'Metal', 'Punk', 'Reggae', 'Soul', 'Funk', 'Disco', 'Soundtrack', 'Spoken word']

  const recording = $derived(ui.status.state === 'recording')
  const paused = $derived(ui.status.state === 'paused')
  const locked = $derived(recording || paused)
  const importing = $derived(ui.status.importSide !== '' || ui.job?.kind === 'import')
  const busy = $derived(locked || importing)
  const metering = $derived(ui.status.state !== 'idle')
  const meterChannels = $derived(metering ? ui.status.channels : channels)

  let poll: ReturnType<typeof setInterval> | undefined

  const onNew = () => void close(true)
  const onOpen = () => void openOther()
  hooks.newProject = onNew
  hooks.openProject = onOpen
  hooks.exportTracks = () => {
    if (!stepBlocked && hasTracks && !dialogOpen()) ui.exportOpen = true
  }
  hooks.lookUp = () => {
    if (!stepBlocked && hasTracks) void lookUpTracks()
  }
  const onUndo = () => void stepLast(true, true)
  const onRedo = () => void stepLast(false, true)
  hooks.undoMain = onUndo
  hooks.redoMain = onRedo
  onDestroy(() => {
    if (hooks.newProject === onNew) hooks.newProject = null
    if (hooks.openProject === onOpen) hooks.openProject = null
    if (hooks.undoMain === onUndo) hooks.undoMain = null
    if (hooks.redoMain === onRedo) hooks.redoMain = null
    hooks.exportTracks = null
    hooks.lookUp = null
  })

  onMount(async () => {
    const s = await GetSettings()
    sampleRate = s.sampleRate
    channels = s.channels
    bitDepth = s.bitDepth
    deviceName = s.deviceName
    outputName = s.monitorDeviceName
    autoStop = s.autoStop
    autoStopSec = s.autoStopSec
    autoStopText = formatInterval(s.autoStopSec)
    await refreshDevices()
    ready = true
    await openInput()
    poll = setInterval(async () => {
      if (locked) ui.status = await Status()
    }, 250)
  })

  onDestroy(() => clearInterval(poll))

  async function refreshDevices() {
    try {
      devices = (await Devices()) ?? []
    } catch (e) {
      devices = []
      ui.error = errText(e)
    }
    if (!devices.some((d) => d.name === deviceName)) {
      deviceName = (devices.find((d) => d.isDefault) ?? devices[0])?.name ?? ''
    }
    await refreshOutputs()
  }

  async function refreshOutputs() {
    try {
      outputs = (await PlaybackDevices()) ?? []
    } catch {
      outputs = []
    }
    // A vanished output shows the default entry; the backend keeps playing or falling back on its own.
    if (outputName && !outputs.some((d) => d.name === outputName)) {
      const wasOn = ui.status.monitor
      outputName = ''
      if (wasOn) await changeOutput()
    }
  }

  async function changeOutput() {
    try {
      await SetMonitorDevice(outputName)
    } catch (e) {
      ui.error = errText(e)
    }
    ui.status = await Status()
  }

  async function openInput() {
    if (!deviceName) return
    try {
      const d = await OpenInput(deviceName, sampleRate, channels, bitDepth)
      deviceName = d.name
      inputFailed = false
    } catch (e) {
      inputFailed = true
      ui.error = errText(e)
    }
  }

  async function refreshClicked() {
    await refreshDevices()
    await openInput()
  }

  let autoStop = $state(true)
  let autoStopSec = $state(60)
  let autoStopText = $state('1:00')
  let autoStopError = $state('')

  function formatInterval(sec: number): string {
    return `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`
  }

  async function saveAutoStop() {
    const m = /^\s*(\d{1,2})(?::([0-5]\d))?\s*$/.exec(autoStopText)
    const sec = m ? Number(m[1]) * 60 + Number(m[2] ?? 0) : NaN
    if (!(sec >= 10 && sec <= 600)) {
      autoStopError = t('rec.autoStopRange')
      return
    }
    autoStopError = ''
    autoStopSec = sec
    autoStopText = formatInterval(sec)
    try {
      await SetAutoStop(autoStop, sec)
    } catch (e) {
      ui.error = errText(e)
    }
  }

  async function toggleMonitor(on: boolean) {
    try {
      await SetMonitor(on)
    } catch (e) {
      ui.error = errText(e)
    }
    ui.status = await Status()
  }

  async function run(fn: () => Promise<unknown>) {
    try {
      await fn()
    } catch (e) {
      ui.error = errText(e)
    }
    ui.status = await Status()
  }

  async function deleteSide(side: string) {
    if (!(await yesNo(t('rec.deleteTitle', { side }), t('rec.deleteText', { side }), t('rec.deleteYes')))) return
    try {
      ui.project = await serial(() => DeleteSide(side))
    } catch (e) {
      ui.error = errText(e)
    }
  }

  async function yesNo(title: string, message: string, yes: string, danger = true) {
    return (await ask({ title, message, buttons: [{ id: 'no', label: t('cancel') }, { id: 'yes', label: yes, kind: danger ? 'danger' : 'primary' }], cancel: 'no' })) === 'yes'
  }

  function overwriteText(side: string) {
    const info = ui.project?.sides?.[side]
    if (info?.missing) {
      return t('rec.overwriteStale', { side })
    }
    if (!info) {
      return t('rec.overwriteUnlisted', { side })
    }
    return t('rec.overwriteExisting', { side })
  }

  async function record(side: string) {
    try {
      await StartRecording(side, false)
    } catch (e) {
      if (sideExistsMessage(e) === null) {
        ui.error = errText(e)
      } else if (await yesNo(t('rec.overwriteTitle'), overwriteText(side), t('rec.overwriteYes'))) {
        await run(() => StartRecording(side, true))
        return
      }
    }
    ui.status = await Status()
  }

  const pauseOrResume = () => run(() => (recording ? PauseRecording() : ResumeRecording()))

  async function importFile(side: string) {
    const path = await ChooseAudioFile()
    if (!path) return
    let overwrite = false
    for (;;) {
      try {
        ui.project = await ImportSide(side, path, overwrite)
        break
      } catch (e) {
        if (!overwrite && sideExistsMessage(e) !== null) {
          if (!(await yesNo(t('rec.replaceTitle'), t('rec.replaceImport', { side }), t('rec.replaceYes')))) break
          overwrite = true
          continue
        }
        const msg = errText(e)
        if (!msg.includes('context canceled')) ui.error = msg
        break
      }
    }
    ui.job = null
    ui.status = await Status()
  }

  async function recover(side: string) {
    try {
      ui.project = await RecoverPart(side, false)
    } catch (e) {
      if (sideExistsMessage(e) !== null && (await yesNo(t('rec.replaceTitle'), t('rec.replaceRecover', { side }), t('rec.replaceYes')))) {
        try {
          ui.project = await RecoverPart(side, true)
        } catch (e2) {
          ui.error = errText(e2)
        }
      } else if (sideExistsMessage(e) === null) {
        ui.error = errText(e)
      }
    }
  }

  async function discardPart(side: string) {
    if (!(await yesNo(t('rec.deleteLeftoverTitle'), t('rec.deleteLeftoverText', { side }), t('delete')))) return
    try {
      ui.project = await DiscardPart(side)
    } catch (e) {
      ui.error = errText(e)
    }
  }

  let last = $state({ canUndo: false, canRedo: false, undoSide: '', redoSide: '' })
  const stepBlocked = $derived(busy || !!ui.job || !!ui.edit)
  const hasTracks = $derived(
    SIDES.some((k) => {
      const list = [...(ui.project?.sides?.[k]?.markers ?? [])].sort((a, b) => a.positionMs - b.positionMs)
      return list.some((m, i) => m.kind === 'start' && list[i + 1]?.kind === 'end')
    }),
  )
  $effect(() => {
    void ui.project
    void SetTracksReady(hasTracks).catch(() => {})
  })
  $effect(() => {
    void ui.project
    EditHistoryLast()
      .then((h) => (last = h))
      .catch(() => (last = { canUndo: false, canRedo: false, undoSide: '', redoSide: '' }))
  })

  $effect(() => {
    Object.assign(history, last, { blocked: stepBlocked })
  })
  onDestroy(() => Object.assign(history, { canUndo: false, canRedo: false, blocked: true }))

  // The menu, shortcuts and bottom bar skip the cached `last`: the backend ignores a step with nothing to do.
  async function stepLast(undo: boolean, direct = false) {
    if (stepBlocked || (!direct && !(undo ? last.canUndo : last.canRedo))) return
    try {
      ui.project = await serial(() => (undo ? UndoLast() : RedoLast()))
    } catch (e) {
      ui.error = errText(e)
    }
  }

  async function save() {
    try {
      ui.project = await serial(() => SaveProject())
    } catch (e) {
      ui.error = errText(e)
    }
  }

  async function stop() {
    try {
      ui.project = await StopRecording()
    } catch (e) {
      ui.error = errText(e)
    }
    ui.status = await Status()
  }

  async function stageName() {
    if (!ui.project) return
    const draft = nameDraft
    try {
      const info = await serial(() => RenameProject(draft))
      if (draft === nameDraft) nameError = ''
      ui.project = info
    } catch (e) {
      if (draft === nameDraft) nameError = errText(e)
    }
  }

  async function leaveName() {
    nameFocused = false
    await stageName()
    if (nameError) {
      nameError = ''
      nameDraft = ui.project?.name ?? ''
    } else if (ui.project) {
      nameDraft = ui.project.name
    }
  }

  async function chooseCover() {
    const path = await ChooseImage()
    if (!path) return
    try {
      ui.project = await serial(() => SetCover(path))
      ui.coverV++
    } catch (e) {
      ui.error = errText(e)
    }
  }

  async function lookUpTracks() {
    if (dialogOpen()) return
    if (!ui.onlineLookup) {
      if (!(await yesNo(t('lookup.consentTitle'), t('lookup.consentText'), t('lookup.consentYes'), false))) return
      try {
        await SetOnlineLookup(true)
        ui.onlineLookup = true
      } catch (e) {
        ui.error = errText(e)
        return
      }
    }
    ui.lookupOpen = true
  }

  async function removeCover() {
    try {
      ui.project = await serial(() => ClearCover())
      ui.coverV++
    } catch (e) {
      ui.error = errText(e)
    }
  }

  // Runs the checks before leaving the current project; false means stay.
  async function leave(): Promise<boolean> {
    if (locked) {
      if (!(await yesNo(t('rec.closeTitle'), t('rec.closeText'), t('rec.closeYes'), false))) return false
      try {
        ui.project = await StopRecording()
      } catch (e) {
        ui.error = errText(e)
        return false
      }
    }
    if (importing || ui.job) {
      ui.error = t('rec.waitImport')
      return false
    }
    await queue
    if (ui.project?.dirty) {
      const choice = await ask({
        title: t('rec.unsavedTitle'),
        message: t('rec.unsavedText'),
        buttons: [{ id: 'cancel', label: t('cancel') }, { id: 'discard', label: t('dontSave'), kind: 'danger' }, { id: 'save', label: t('save'), kind: 'primary' }],
        cancel: 'cancel',
      })
      if (choice === 'cancel') return false
      try {
        if (choice === 'save') await SaveProject()
        else await DiscardChanges()
      } catch (e) {
        ui.error = errText(e)
        return false
      }
    }
    return true
  }

  // One flow at a time: a second menu shortcut or click while a picker or
  // question is open is ignored.
  let flow = false
  const dialogOpen = () => !!(ui.ask || ui.saveAsOpen || ui.exportOpen || ui.lookupOpen || ui.quit || ui.edit)

  async function close(create = false) {
    if (flow || dialogOpen()) return
    flow = true
    try {
      if (!(await leave())) return
      await CloseProject()
      ui.startCreating = create
      ui.project = null
      ui.error = ''
    } catch (e) {
      ui.error = errText(e)
    } finally {
      flow = false
    }
  }

  async function openOther() {
    if (flow || dialogOpen()) return
    flow = true
    try {
      const dir = await ChooseFolder(t('start.chooseOpenTitle'))
      if (!dir || !(await leave())) return
      try {
        ui.project = await OpenProject(dir)
        ui.error = ''
        ui.coverV++
        nameFocused = artistFocused = yearFocused = albumFocused = genreFocused = false
      } catch (e) {
        ui.error = errText(e)
      }
      // The backend closed the input while switching (or failing to); the screen
      // is not remounted, so reopen it and refresh the status.
      await openInput()
      ui.status = await Status()
    } catch (e) {
      ui.error = errText(e)
    } finally {
      flow = false
    }
  }

  const coverSrc = $derived(ui.project?.cover && !ui.project.coverMissing ? `/cover?v=${ui.coverV}` : '')
  const when = (t: unknown) => new Date(String(t)).toLocaleString(dateLocale())
  const jobPct = $derived(ui.job && ui.job.total > 0 ? Math.min(100, Math.round((ui.job.done / ui.job.total) * 100)) : null)
</script>

<header class="island bar">
  <div class="grow">
    <strong>{ui.project?.name}</strong>
    <span class="muted path">{ui.project?.dir}</span>
  </div>
  <button onclick={() => close()}>{t('home')}</button>
  <button onclick={() => close(true)}>{t('newProject')}</button>
  <button onclick={openOther}>{t('openProject')}</button>
  <button onclick={save} disabled={!ui.project?.dirty || !!nameError || !!detailsError || yearInvalid || albumInvalid || genreInvalid}>{t('save')}</button>
  <button onclick={() => (ui.saveAsOpen = true)} disabled={busy}>{t('saveAs')}</button>
  <button onclick={lookUpTracks} disabled={busy || !!ui.job || !hasTracks} title={t('lookup.buttonTitle')}>{t('lookup.button')}</button>
  <button onclick={() => (ui.exportOpen = true)} disabled={stepBlocked || !hasTracks} title={t('export.buttonTitle')}>{t('export.button')}</button>
</header>

{#each ui.project?.leftovers ?? [] as l (l.side)}
  <div class="island banner" role="alert">
    <div class="grow">
      {t('rec.leftoverFound', { side: l.side })} ({formatDuration(l.durationMs)}, {formatSize(l.bytes)}) {t('rec.leftoverCause')}
    </div>
    <button disabled={busy || l.bytes === 0} onclick={() => recover(l.side)}>{t('rec.recover')}</button>
    <button disabled={busy} onclick={() => discardPart(l.side)}>{t('delete')}</button>
  </div>
{/each}

{#if ui.job?.kind === 'import'}
  <div class="island banner">
    <div class="grow">
      {t('rec.importing', { side: ui.job.side })} {jobPct !== null ? `${jobPct}%` : formatSize(ui.job.done)}
      <progress max="100" value={jobPct ?? undefined}></progress>
    </div>
    <button class="danger" onclick={() => CancelJob()}>{t('cancel')}</button>
  </div>
{/if}

{#if ui.error}
  <div class="island banner" role="alert">
    <div class="grow">
      <span class="error">{ui.error}</span>
      {#if inputFailed}
        <div class="muted">{t('rec.inputHint')}</div>
      {/if}
    </div>
    <button onclick={() => (ui.error = '')}>{t('close')}</button>
  </div>
{/if}

<div class="columns">
  <div class="col">
    <section class="island">
      <h2>{t('rec.project')}</h2>
      <div class="field">
      <label for="pname">{t('projectName')}</label>
      <input
        id="pname"
        type="text"
        bind:value={nameDraft}
        maxlength="200"
        oninput={stageName}
        onfocus={() => (nameFocused = true)}
        onblur={leaveName}
        onkeydown={(e) => e.key === 'Enter' && e.currentTarget.blur()}
      />
      {#if nameError}<p class="error small" role="alert">{nameError}</p>{/if}
      </div>

      <div class="field">
      <label for="partist">{t('rec.artist')}</label>
      <div class="artist-row">
        <input
          id="partist"
          type="text"
          bind:value={artistDraft}
          maxlength="200"
          disabled={ui.project?.compilation}
          oninput={() => stageDetails()}
          onfocus={() => (artistFocused = true)}
          onblur={() => leaveField('artist')}
        />
        <label class="check" for="pcompilation">
          <input
            id="pcompilation"
            type="checkbox"
            checked={ui.project?.compilation ?? false}
            onchange={(e) => stageDetails(e.currentTarget.checked)}
          />
          {t('rec.compilation')}
        </label>
      </div>
      </div>

      <div class="field">
      <label for="palbum">{t('rec.album')}</label>
      <input
        id="palbum"
        type="text"
        bind:value={albumDraft}
        maxlength="200"
        oninput={() => stageDetails()}
        onfocus={() => (albumFocused = true)}
        onblur={() => leaveField('album')}
      />
      {#if albumInvalid}<p class="error small" role="alert">{t('rec.albumInvalid')}</p>{/if}
      </div>

      <div class="field">
      <label for="pgenre">{t('rec.genre')}</label>
      <input
        id="pgenre"
        type="text"
        list="genre-list"
        bind:value={genreDraft}
        maxlength="100"
        oninput={() => stageDetails()}
        onfocus={() => (genreFocused = true)}
        onblur={() => leaveField('genre')}
      />
      <datalist id="genre-list">{#each GENRES as g}<option value={g}></option>{/each}</datalist>
      {#if genreInvalid}<p class="error small" role="alert">{t('rec.genreInvalid')}</p>{/if}
      </div>

      <div class="field">
      <label for="pyear">{t('rec.year')}</label>
      <input
        id="pyear"
        type="text"
        inputmode="numeric"
        bind:value={yearDraft}
        maxlength="4"
        placeholder="1900–2100"
        oninput={onYearInput}
        onfocus={() => (yearFocused = true)}
        onblur={() => leaveField('year')}
      />
      {#if yearInvalid}<p class="error small" role="alert">{t('rec.yearInvalid')}</p>{:else if detailsError && !albumInvalid && !genreInvalid}<p class="error small" role="alert">{detailsError}</p>{/if}
      </div>

      <div class="cover">
        {#if coverSrc}
          <img src={coverSrc} alt={t('rec.coverAlt')} />
        {:else}
          <div class="placeholder muted">{ui.project?.coverMissing ? t('rec.coverMissing') : t('rec.noCover')}</div>
        {/if}
      </div>
      <div class="row">
        <button onclick={chooseCover}>{t('rec.chooseImage')}</button>
        <button onclick={removeCover} disabled={!ui.project?.cover}>{t('remove')}</button>
      </div>
    </section>

    <section class="island">
      <h2>{t('rec.source')}</h2>
      {#if ready && devices.length === 0}
        <p class="muted">{t('rec.noDevices')}</p>
        <button onclick={refreshClicked}>{t('refresh')}</button>
      {:else}
        <div class="row source">
          <select aria-label={t('rec.inputDevice')} bind:value={deviceName} onchange={openInput} disabled={locked} class="grow">
            {#each devices as d (d.id)}
              <option value={d.name}>{d.name}{d.isDefault ? ' (' + t('rec.default') + ')' : ''}</option>
            {/each}
          </select>
          <button onclick={refreshClicked} disabled={locked} title={t('rec.refreshTitle')}>{t('refresh')}</button>
          <LevelMeter channels={meterChannels} />
        </div>
        <div class="row monitor">
          <label class="check">
            <input type="checkbox" checked={ui.status.monitor} disabled={!metering} onchange={(e) => toggleMonitor(e.currentTarget.checked)} />
            {t('rec.monitor')}
          </label>
          <select aria-label={t('rec.monitorOutput')} bind:value={outputName} onchange={changeOutput} disabled={!metering}>
            <option value="">{t('rec.systemDefault')}{defaultOutput ? ` (${defaultOutput})` : ''}</option>
            {#each outputs as d (d.id)}
              <option value={d.name}>{d.name}</option>
            {/each}
          </select>
          {#if ui.status.monitor}
            <span class="muted small">{t('rec.monitorWarn')}</span>
          {/if}
        </div>
        <div class="row formats">
          <label>{t('rec.rate')}
            <select bind:value={sampleRate} onchange={openInput} disabled={locked}>
              {#each RATES as r}<option value={r}>{r} {t('unit.hz')}</option>{/each}
            </select>
          </label>
          <label>{t('rec.channels')}
            <select bind:value={channels} onchange={openInput} disabled={locked}>
              <option value={1}>{t('rec.mono')}</option>
              <option value={2}>{t('rec.stereo')}</option>
            </select>
          </label>
          <label>{t('rec.bitDepth')}
            <select bind:value={bitDepth} onchange={openInput} disabled={locked}>
              <option value={16}>16 {t('unit.bit')}</option>
              <option value={24}>24 {t('unit.bit')}</option>
            </select>
          </label>
        </div>
        <div class="row autostop">
          <label class="check">
            <input type="checkbox" bind:checked={autoStop} onchange={saveAutoStop} />
            {t('rec.autoStop')}
          </label>
          <input
            class="interval"
            aria-label={t('rec.autoStopInterval')}
            title={t('rec.autoStopTitle')}
            bind:value={autoStopText}
            onchange={saveAutoStop}
            disabled={!autoStop}
          />
          <span class="muted small">{t('rec.autoStopUnit')}</span>
          {#if autoStopError}<span class="error small">{autoStopError}</span>{/if}
        </div>
      {/if}
    </section>
  </div>

  <div class="col">
    {#each SIDES as side (side)}
      {@const info = ui.project?.sides?.[side]}
      {@const active = locked && ui.status.side === side}
      <section class="island">
        <h2>{t('rec.sideTitle', { side })}</h2>
        <p class="muted small">
          {#if info}
            <span class:error={info.missing}>
              {info.missing ? t('side.fileMissing') : ''}{info.staged ? t('side.notSaved') : ''}{info.origin === 'imported' ? t('side.imported') : t('side.recorded')}{info.sourceName ? t('side.from', { name: info.sourceName }) : ''} ·
              {formatDuration(info.durationMs)} · {info.sampleRate} {t('unit.hz')} · {info.channels === 1 ? t('side.mono') : t('side.stereo')} · {info.bitDepth} {t('unit.bit')} · {when(info.recordedAt)}
            </span>
            {#if !info.missing && ui.clips[side]?.count}
              <span class="clips" title={t('side.clipsHint')}>· {t('side.clips', { n: ui.clips[side].count })}</span>
            {/if}
          {:else}
            {t('side.empty')}
          {/if}
        </p>
        <div class="row">
          {#if active}
            <button class="primary" onclick={pauseOrResume}>{recording ? t('rec.pause') : t('rec.resume')}</button>
            <button class="danger" onclick={stop}>{t('rec.stop')}</button>
            <span class="timer">{formatDuration(ui.status.elapsedMs)}</span>
            <span class="muted">{formatSize(ui.status.sizeBytes)}</span>
            {#if paused}<span class="muted">{t('rec.paused')}</span>{/if}
          {:else}
            <button class="primary" onclick={() => record(side)} disabled={!metering || busy}>● {t('rec.recordSide', { side })}</button>
            <button onclick={() => importFile(side)} disabled={busy}>{t('rec.import')}</button>
            <button onclick={() => (ui.edit = side)} disabled={!info || info.missing || ![16, 24].includes(info.bitDepth) || !!ui.job || busy}>{t('rec.edit')}</button>
            <button onclick={() => run(() => RevealSide(side))} disabled={!info || info.missing}>{t('rec.reveal')}</button>
            <button class="danger" onclick={() => deleteSide(side)} disabled={!info || busy || !!ui.job}>{t('rec.delete')}</button>
          {/if}
        </div>
        {#if !active && ui.status.autoStoppedSide === side}
          <p class="muted small">{t('rec.autoStopped', { time: formatInterval(autoStopSec) })}</p>
        {/if}
        <TrackTable {side} />
      </section>
    {/each}
    {#if ui.status.overruns > 0}
      <p class="error small">{t('rec.overruns', { count: ui.status.overruns })}</p>
    {/if}
  </div>
</div>

<style>
  .bar,
  .banner {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 20px;
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .path {
    margin-left: 8px;
    font-size: calc(12px * var(--font-scale, 1));
  }
  .columns {
    flex: 1;
    display: flex;
    gap: var(--island-gap);
    min-height: 0;
  }
  .col {
    flex: 1;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: var(--island-gap);
    min-width: 0;
  }
  .col > :global(.island) {
    flex-shrink: 0;
  }
  .source {
    align-items: flex-start;
  }
  .source :global(select) {
    max-width: 100%;
  }
  .monitor,
  .formats,
  .autostop {
    margin-top: 12px;
    flex-wrap: wrap;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text);
  }
  .interval {
    width: 5em;
  }
  .formats label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .small {
    font-size: calc(12px * var(--font-scale, 1));
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .field + .field {
    margin-top: 12px;
  }
  .field > label {
    display: block;
  }
  .clips {
    color: var(--warning);
  }

  .field > .error {
    margin: 4px 0 0;
  }
  .artist-row {
    display: flex;
    gap: 12px;
    align-items: center;
  }
  .artist-row input[type='text'] {
    flex: 1;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
  }
  .timer {
    font: calc(20px * var(--font-scale, 1)) var(--font-mono);
    margin: 0 8px;
  }
  .cover {
    margin: 12px 0;
    aspect-ratio: 3 / 2;
    max-height: 220px;
    width: 100%;
    background: var(--placeholder-bg);
    border: 1px solid var(--control-border);
    border-radius: var(--radius-control);
    overflow: hidden;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .cover img {
    width: 100%;
    height: 100%;
    object-fit: contain;
  }
  progress {
    width: 100%;
    margin-top: 6px;
  }
  #pname {
    width: 100%;
  }
</style>
