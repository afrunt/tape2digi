import { en, type MessageKey } from './locales/en'
import { uk } from './locales/uk'

export type Language = 'en' | 'uk'

const catalogs: Record<Language, Record<string, string>> = { en, uk }

export const lang = $state<{ current: Language }>({ current: 'en' })

export function normalizeLanguage(v: string | undefined | null): Language {
  return v === 'uk' ? 'uk' : 'en'
}

export function applyLanguage(v: Language) {
  lang.current = v
  document.documentElement.lang = v
}

export function t(key: MessageKey, params?: Record<string, string | number>): string {
  const msg = catalogs[lang.current][key] ?? en[key] ?? key
  if (!params) return msg
  return msg.replace(/\{(\w+)\}/g, (m, name) => (name in params ? String(params[name]) : m))
}

export function dateLocale(): string {
  return lang.current === 'uk' ? 'uk-UA' : 'en-US'
}
