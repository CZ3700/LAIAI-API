import { createI18n } from 'vue-i18n'
import en from './en'
import zhCN from './zh-CN'
import zhTW from './zh-TW'
import ja from './ja'
import fr from './fr'
import ru from './ru'
import vi from './vi'
import ms from './ms'
import th from './th'

// 语种对标 NewAPI（en/zh-CN/zh-TW/ja/fr/ru/vi/ms/th）；label 用各语种自称（语言选择器惯例）
export const SUPPORTED_LOCALES = [
  { code: 'en', label: 'English' },
  { code: 'zh-CN', label: '简体中文' },
  { code: 'zh-TW', label: '繁體中文' },
  { code: 'ja', label: '日本語' },
  { code: 'fr', label: 'Français' },
  { code: 'ru', label: 'Русский' },
  { code: 'vi', label: 'Tiếng Việt' },
  { code: 'ms', label: 'Bahasa Melayu' },
  { code: 'th', label: 'ไทย' },
] as const

export type LocaleCode = (typeof SUPPORTED_LOCALES)[number]['code']

function detectLocale(): LocaleCode {
  // 默认英文——不按浏览器语言自动切换；仅当用户曾手动切换（持久化在 laiai.locale）才沿用其选择。
  const stored = localStorage.getItem('laiai.locale')
  if (stored && SUPPORTED_LOCALES.some((l) => l.code === stored)) return stored as LocaleCode
  return 'en'
}

export const i18n = createI18n({
  legacy: false,
  locale: detectLocale(),
  fallbackLocale: 'en',
  messages: { en, 'zh-CN': zhCN, 'zh-TW': zhTW, ja, fr, ru, vi, ms, th },
})

document.documentElement.lang = i18n.global.locale.value

export function setLocale(locale: LocaleCode) {
  i18n.global.locale.value = locale
  localStorage.setItem('laiai.locale', locale)
  document.documentElement.lang = locale
}
