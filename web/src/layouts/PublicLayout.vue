<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuth } from '@/stores/auth'
import { api } from '@/lib/api'
import ThemeLocale from '@/components/ThemeLocale.vue'

const { t } = useI18n()
const auth = useAuth()

const announcement = ref('')
const siteName = ref('LAIAI')
const supportEmail = ref('')
const footerText = ref('')
const hasTerms = ref(false)
const hasPrivacy = ref(false)
onMounted(async () => {
  try {
    const cfg = await api<{
      announcement: string; site_name: string; support_email: string
      footer_text: string; terms_of_service: string; privacy_policy: string
    }>('GET', '/api/config', undefined, { auth: false })
    announcement.value = cfg.announcement
    siteName.value = cfg.site_name || 'LAIAI'
    supportEmail.value = cfg.support_email
    footerText.value = cfg.footer_text
    hasTerms.value = !!cfg.terms_of_service
    hasPrivacy.value = !!cfg.privacy_policy
  } catch {
    // 公告失败不影响门面
  }
})

const links = [
  { to: '/models', label: () => t('nav.models') },
  { to: '/rankings', label: () => t('nav.rankings') },
  { to: '/docs', label: () => t('nav.docs') },
  { to: '/about', label: () => t('nav.about') },
]
</script>

<template>
  <div class="min-h-screen flex flex-col">
    <header class="sticky top-0 z-40 border-b border-line bg-bg/80 backdrop-blur">
      <div class="mx-auto flex max-w-6xl items-center gap-6 px-6 py-3.5">
        <router-link to="/" class="flex items-center gap-2.5">
          <div
            class="grid h-8 w-8 place-items-center rounded-lg text-sm font-bold text-white"
            style="background: linear-gradient(135deg, rgb(var(--c-accent)), rgb(var(--c-accent-2)))"
            aria-hidden="true"
          >
            L
          </div>
          <span class="text-lg font-semibold tracking-tight">LAIAI</span>
        </router-link>
        <nav class="hidden gap-1 sm:flex">
          <router-link
            v-for="l in links"
            :key="l.to"
            :to="l.to"
            class="rounded-lg px-3 py-1.5 text-sm text-muted transition-colors hover:bg-elev hover:text-ink"
            active-class="!text-ink"
          >
            {{ l.label() }}
          </router-link>
        </nav>
        <div class="ml-auto flex items-center gap-2">
          <ThemeLocale />
          <router-link v-if="auth.isAuthed" to="/app" class="btn-primary !py-1.5 text-sm">
            {{ t('nav.console') }}
          </router-link>
          <template v-else>
            <router-link to="/login" class="btn-ghost !py-1.5 text-sm">{{ t('auth.signin') }}</router-link>
            <router-link to="/register" class="btn-primary !py-1.5 text-sm">{{ t('auth.signup') }}</router-link>
          </template>
        </div>
      </div>
    </header>

    <!-- 运营公告条 -->
    <div v-if="announcement" class="border-b border-line bg-accent/10 px-6 py-2 text-center text-sm text-accent">
      {{ announcement }}
    </div>

    <main class="flex-1">
      <router-view v-slot="{ Component }">
        <transition name="page" mode="out-in">
          <component :is="Component" :key="$route.path" />
        </transition>
      </router-view>
    </main>

    <footer class="border-t border-line">
      <div class="mx-auto max-w-6xl space-y-3 px-6 py-8">
        <div class="flex flex-wrap items-center justify-between gap-4 text-sm text-muted">
          <span>© {{ siteName }}</span>
          <div class="flex flex-wrap gap-x-4 gap-y-2">
            <router-link class="hover:text-ink" to="/models">{{ t('nav.models') }}</router-link>
            <router-link class="hover:text-ink" to="/rankings">{{ t('nav.rankings') }}</router-link>
            <router-link class="hover:text-ink" to="/docs">{{ t('nav.docs') }}</router-link>
            <router-link class="hover:text-ink" to="/about">{{ t('nav.about') }}</router-link>
            <router-link class="hover:text-ink" to="/status">{{ t('status.short') }}</router-link>
            <router-link v-if="hasTerms" class="hover:text-ink" to="/terms">{{ t('legal.terms') }}</router-link>
            <router-link v-if="hasPrivacy" class="hover:text-ink" to="/privacy">{{ t('legal.privacy') }}</router-link>
            <a v-if="supportEmail" class="hover:text-ink" :href="`mailto:${supportEmail}`">{{ t('legal.contact') }}</a>
          </div>
        </div>
        <p v-if="footerText" class="text-xs text-muted">{{ footerText }}</p>
      </div>
    </footer>
  </div>
</template>
