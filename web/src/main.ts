import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { i18n } from './i18n'
import './style.css'

// 暗色优先（设计 §5.6）：默认 dark，用户切换持久化
const theme = localStorage.getItem('laiai.theme') ?? 'dark'
document.documentElement.classList.toggle('dark', theme === 'dark')

createApp(App).use(createPinia()).use(router).use(i18n).mount('#app')
