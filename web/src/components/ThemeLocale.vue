<script setup lang="ts">
// 主题 + 语种切换控件（Shell 侧栏与登录/注册页共用）
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { SUPPORTED_LOCALES, setLocale, type LocaleCode } from '@/i18n'

const { t, locale } = useI18n()
const isDark = ref(document.documentElement.classList.contains('dark'))

function toggleTheme() {
  isDark.value = !isDark.value
  document.documentElement.classList.toggle('dark', isDark.value)
  localStorage.setItem('laiai.theme', isDark.value ? 'dark' : 'light')
}

function onLocale(event: Event) {
  setLocale((event.target as HTMLSelectElement).value as LocaleCode)
}
</script>

<template>
  <div class="flex items-center gap-1.5">
    <button class="btn-ghost !px-2.5 !py-1.5" :aria-label="t('nav.theme')" :title="t('nav.theme')" @click="toggleTheme">
      <!-- 日/月 inline SVG（不引图标库，tree-shaking 友好；接入 lucide 时替换） -->
      <svg v-if="isDark" class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
        <circle cx="12" cy="12" r="4" />
        <path d="M12 2v2m0 16v2M4.9 4.9l1.4 1.4m11.4 11.4 1.4 1.4M2 12h2m16 0h2M4.9 19.1l1.4-1.4m11.4-11.4 1.4-1.4" />
      </svg>
      <svg v-else class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
        <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z" />
      </svg>
    </button>
    <select
      class="input !w-auto !py-1.5 text-xs cursor-pointer"
      :value="locale"
      :aria-label="t('nav.language')"
      @change="onLocale"
    >
      <option v-for="l in SUPPORTED_LOCALES" :key="l.code" :value="l.code">{{ l.label }}</option>
    </select>
  </div>
</template>
