<script lang="ts">
  import { onMount } from 'svelte'
  import { CurrentProject, GetSettings } from '../wailsjs/go/main/App'
  import { applyLanguage, normalizeLanguage } from './lib/i18n.svelte'
  import { ui, wireEvents } from './lib/store.svelte'
  import StartScreen from './screens/StartScreen.svelte'
  import RecorderScreen from './screens/RecorderScreen.svelte'
  import Dialogs from './lib/Dialogs.svelte'
  import ExportDialog from './lib/ExportDialog.svelte'
  import SideEditor from './lib/SideEditor.svelte'
  import TrackPlayer from './lib/TrackPlayer.svelte'
  import BottomBar from './lib/BottomBar.svelte'
  import { applyTheme, normalizeTheme } from './lib/theme.svelte'
  import { applyFontScale, normalizeFontScale } from './lib/fontscale.svelte'

  onMount(async () => {
    wireEvents()
    GetSettings()
      .then((s) => {
        applyLanguage(normalizeLanguage(s.language))
        applyTheme(normalizeTheme(s.theme))
        applyFontScale(normalizeFontScale(s.fontScale))
      })
      .catch(() => {})
    // Never auto-open: a project is only active if one was opened this session.
    const p = await CurrentProject()
    ui.project = p.dir ? p : null
  })
</script>

{#if ui.project}
  <RecorderScreen />
{:else}
  <StartScreen />
{/if}

<BottomBar />
<ExportDialog />
<Dialogs />
<SideEditor />
<TrackPlayer />
