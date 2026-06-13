import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { i18n } from './i18n'
import { reveal } from './directives/reveal'
import './style.css'

// 双色系（设计 §5.6）：用户选择 > 系统偏好 > 暗色优先默认
const stored = localStorage.getItem('laiai.theme')
const prefersLight = window.matchMedia('(prefers-color-scheme: light)').matches
const dark = stored ? stored === 'dark' : !prefersLight
document.documentElement.classList.toggle('dark', dark)

createApp(App).use(createPinia()).use(router).use(i18n).directive('reveal', reveal).mount('#app')
