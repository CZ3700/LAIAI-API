import { createI18n } from 'vue-i18n'
import en from './en'
import zhCN from './zh-CN'
import zhTW from './zh-TW'
import ja from './ja'
import fr from './fr'
import ru from './ru'
import vi from './vi'

// 语种对标 NewAPI（en/zh-CN/zh-TW/ja/fr/ru/vi）；label 用各语种自称（语言选择器惯例）
export const SUPPORTED_LOCALES = [
  { code: 'en', label: 'English' },
  { code: 'zh-CN', label: '简体中文' },
  { code: 'zh-TW', label: '繁體中文' },
  { code: 'ja', label: '日本語' },
  { code: 'fr', label: 'Français' },
  { code: 'ru', label: 'Русский' },
  { code: 'vi', label: 'Tiếng Việt' },
] as const

export type LocaleCode = (typeof SUPPORTED_LOCALES)[number]['code']

function detectLocale(): LocaleCode {
  const stored = localStorage.getItem('laiai.locale')
  if (stored && SUPPORTED_LOCALES.some((l) => l.code === stored)) return stored as LocaleCode
  const lang = navigator.language.toLowerCase()
  if (lang.startsWith('zh')) {
    // 繁体圈：台湾/香港/澳门 → zh-TW，其余中文 → zh-CN
    return /-(tw|hk|mo)|hant/.test(lang) ? 'zh-TW' : 'zh-CN'
  }
  for (const { code } of SUPPORTED_LOCALES) {
    if (code !== 'en' && lang.startsWith(code.toLowerCase())) return code
  }
  return 'en'
}

export const i18n = createI18n({
  legacy: false,
  locale: detectLocale(),
  fallbackLocale: 'en',
  messages: { en, 'zh-CN': zhCN, 'zh-TW': zhTW, ja, fr, ru, vi },
})

document.documentElement.lang = i18n.global.locale.value

export function setLocale(locale: LocaleCode) {
  i18n.global.locale.value = locale
  localStorage.setItem('laiai.locale', locale)
  document.documentElement.lang = locale
}
