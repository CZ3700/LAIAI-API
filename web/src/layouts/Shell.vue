<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useAuth } from '@/stores/auth'
import ThemeLocale from '@/components/ThemeLocale.vue'

const { t } = useI18n()
const auth = useAuth()
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
            { to: '/admin', label: t('admin.title') },
          ]"
          :key="item.to"
          :to="item.to"
          class="block rounded-md px-3 py-2 text-sm text-muted hover:text-ink hover:bg-panel"
          active-class="!text-ink bg-panel"
        >
          {{ item.label }}
        </router-link>
      </nav>
      <div class="p-3 border-t border-line space-y-2.5">
        <div class="px-2 text-xs text-muted truncate" :title="auth.email">{{ auth.email }}</div>
        <ThemeLocale />
        <button class="btn-ghost w-full !py-1.5 text-xs" @click="auth.logout()">{{ t('nav.logout') }}</button>
      </div>
    </aside>
    <main class="flex-1 min-w-0 p-8">
      <router-view />
    </main>
  </div>
</template>
