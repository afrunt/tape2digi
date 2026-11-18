export type Theme = 'dark' | 'light'

export const theme = $state<{ current: Theme }>({ current: 'dark' })

export function normalizeTheme(v: string | undefined | null): Theme {
  return v === 'light' ? 'light' : 'dark'
}

export function applyTheme(v: Theme) {
  theme.current = v
  document.documentElement.dataset.theme = v
  try {
    localStorage.setItem('theme', v)
  } catch {
    // The mirror only avoids a flash on start; settings.json stays authoritative.
  }
}
