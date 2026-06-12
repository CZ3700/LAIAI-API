<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useAuth } from '@/stores/auth'
import { setLocale, i18n } from '@/i18n'

const { t } = useI18n()
const auth = useAuth()

function toggleTheme() {
  const dark = !document.documentElement.classList.contains('dark')
  document.documentElement.classList.toggle('dark', dark)
  localStorage.setItem('laiai.theme', dark ? 'dark' : 'light')
}
function toggleLocale() {
  setLocale(i18n.global.locale.value === 'en' ? 'zh-CN' : 'en')
}
</script>

<template>
  <div class="min-h-screen flex">
    <aside class="w-56 shrink-0 border-r border-line flex flex-col">
      <div class="px-5 py-4 text-lg font-semibold tracking-tight">LAIAI</div>
      <nav class="flex-1 px-3 space-y-1">
        <router-link
          v-for="item in [
            { to: '/keys', label: t('nav.keys') },
            { to: '/usage', label: t('nav.usage') },
          ]"
          :key="item.to"
          :to="item.to"
          class="block rounded-md px-3 py-2 text-sm text-muted hover:text-ink hover:bg-panel"
          active-class="!text-ink bg-panel"
        >
          {{ item.label }}
        </router-link>
      </nav>
      <div class="p-3 border-t border-line space-y-2">
        <div class="px-2 text-xs text-muted truncate" :title="auth.email">{{ auth.email }}</div>
        <div class="flex gap-1">
          <button class="btn-ghost flex-1 !px-2 text-xs" :aria-label="t('nav.theme')" @click="toggleTheme">◐</button>
          <button class="btn-ghost flex-1 !px-2 text-xs" aria-label="Language" @click="toggleLocale">EN/中</button>
          <button class="btn-ghost flex-1 !px-2 text-xs" @click="auth.logout()">{{ t('nav.logout') }}</button>
        </div>
      </div>
    </aside>
    <main class="flex-1 min-w-0 p-8">
      <router-view />
    </main>
  </div>
</template>
