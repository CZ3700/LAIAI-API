import { createI18n } from 'vue-i18n'
import en from './en'
import zhCN from './zh-CN'

const stored = localStorage.getItem('laiai.locale')
const browser = navigator.language.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en'

export const i18n = createI18n({
  legacy: false,
  locale: stored ?? browser,
  fallbackLocale: 'en',
  messages: { en, 'zh-CN': zhCN },
})

export function setLocale(locale: 'en' | 'zh-CN') {
  i18n.global.locale.value = locale
  localStorage.setItem('laiai.locale', locale)
  document.documentElement.lang = locale
}
