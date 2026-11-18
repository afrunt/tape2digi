export const FONT_MIN = 100
export const FONT_MAX = 150
export const FONT_STEP = 10

export const fontScale = $state<{ percent: number }>({ percent: FONT_MIN })

export function normalizeFontScale(v: number | undefined | null): number {
  const n = Math.round(Number(v) || FONT_MIN)
  return Math.min(FONT_MAX, Math.max(FONT_MIN, n))
}

export function applyFontScale(v: number) {
  const p = normalizeFontScale(v)
  fontScale.percent = p
  document.documentElement.style.setProperty('--font-scale', String(p / 100))
  try {
    localStorage.setItem('fontScale', String(p))
  } catch {
    // The mirror only avoids a flash on start; settings.json stays authoritative.
  }
}
