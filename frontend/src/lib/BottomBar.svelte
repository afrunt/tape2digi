<script lang="ts">
  import { SetFontScale, SetLanguage, SetTheme } from '../../wailsjs/go/main/App'
  import { applyLanguage, lang, normalizeLanguage, t } from './i18n.svelte'
  import { applyTheme, normalizeTheme, theme } from './theme.svelte'
  import { applyFontScale, FONT_MAX, FONT_MIN, FONT_STEP, fontScale } from './fontscale.svelte'
  import { errText, history, hooks, ui } from './store.svelte'

  let error = $state('')

  async function changeLanguage(e: Event) {
    const el = e.currentTarget as HTMLSelectElement
    const next = normalizeLanguage(el.value)
    try {
      await SetLanguage(next)
      applyLanguage(next)
      error = ''
    } catch (err) {
      error = errText(err)
      el.value = lang.current
    }
  }

  async function changeTheme(e: Event) {
    const el = e.currentTarget as HTMLSelectElement
    const next = normalizeTheme(el.value)
    try {
      await SetTheme(next)
      applyTheme(next)
      error = ''
    } catch (err) {
      error = errText(err)
      el.value = theme.current
    }
  }

  async function stepFont(delta: number) {
    const next = Math.min(FONT_MAX, Math.max(FONT_MIN, fontScale.percent + delta))
    if (next === fontScale.percent) return
    try {
      await SetFontScale(next)
      applyFontScale(next)
      error = ''
    } catch (err) {
      error = errText(err)
    }
  }
</script>

<footer class="island bottom">
  {#if ui.project}
    <div class="item" role="group" aria-label={t('edit.undo') + ' / ' + t('edit.redo')}>
      <button class="icon" aria-label={t('edit.undo')} disabled={history.blocked || !history.canUndo} title={history.canUndo ? t('rec.undoTitle', { side: history.undoSide }) : t('edit.undo')} onclick={() => hooks.undoMain?.()}>
        <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M6 3 2 7l4 4M2 7h7a4 4 0 0 1 0 8H6" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
      </button>
      <button class="icon" aria-label={t('edit.redo')} disabled={history.blocked || !history.canRedo} title={history.canRedo ? t('rec.redoTitle', { side: history.redoSide }) : t('edit.redo')} onclick={() => hooks.redoMain?.()}>
        <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="m10 3 4 4-4 4M14 7H7a4 4 0 0 0 0 8h3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
      </button>
      {#if ui.project.dirty}<span class="dirty" role="status" aria-label={t('rec.dirty')} title={t('rec.dirtyTitle')}>●</span>{/if}
    </div>
  {/if}
  <div class="grow">{#if error}<span class="error small" role="alert">{error}</span>{/if}</div>
  <div class="item" role="group" aria-label={t('fontSize')}>
    {t('fontSize')}
    <button class="step" aria-label={t('fontSize.decrease')} disabled={fontScale.percent <= FONT_MIN} onclick={() => stepFont(-FONT_STEP)}>−</button>
    <span class="value">{fontScale.percent}%</span>
    <button class="step" aria-label={t('fontSize.increase')} disabled={fontScale.percent >= FONT_MAX} onclick={() => stepFont(FONT_STEP)}>+</button>
  </div>
  <label class="item">
    {t('theme')}
    <select value={theme.current} onchange={changeTheme}>
      <option value="dark">{t('theme.dark')}</option>
      <option value="light">{t('theme.light')}</option>
    </select>
  </label>
  <label class="item">
    {t('language')}
    <select value={lang.current} onchange={changeLanguage}>
      <option value="en">English</option>
      <option value="uk">Українська</option>
    </select>
  </label>
</footer>

<style>
  .bottom {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 6px 20px;
    flex-shrink: 0;
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .item {
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .step {
    min-width: 28px;
    padding: 2px 8px;
  }
  .value {
    min-width: 4ch;
    text-align: center;
  }
  .icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 0;
    width: var(--control-height);
    padding: 0;
  }
  .dirty {
    color: var(--warning);
  }
  .small {
    font-size: calc(12px * var(--font-scale, 1));
  }
</style>
