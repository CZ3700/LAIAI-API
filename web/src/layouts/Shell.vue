<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuth } from '@/stores/auth'
import ThemeLocale from '@/components/ThemeLocale.vue'
import OrgSwitcher from '@/components/OrgSwitcher.vue'
import BrandMark from '@/components/BrandMark.vue'

const { t } = useI18n()
const auth = useAuth()

// '/' 是所有路径的前缀：仪表盘项只在精确匹配时激活，其余项保留前缀匹配
const ACTIVE = '!text-ink bg-elev shadow-sm nav-active'

// 内联 SVG path（24×24 stroke 风格）：不引图标库，几个图标不值一个依赖。
// 分组导航（控制台 / 个人中心 / 管理）——对标成熟后台的信息架构。
const DASH = 'M2.25 12 11.204 3.045c.44-.439 1.152-.439 1.591 0L21.75 12M4.5 9.75v10.125c0 .621.504 1.125 1.125 1.125H9.75v-4.875c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125V21h4.125c.621 0 1.125-.504 1.125-1.125V9.75M8.25 21h8.25'
const DOC = 'M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5A3.375 3.375 0 0 0 10.125 2.25H8.25m2.25 0H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z'
const CHART = 'M3 13.125C3 12.504 3.504 12 4.125 12h2.25c.621 0 1.125.504 1.125 1.125v6.75C7.5 20.496 6.996 21 6.375 21h-2.25A1.125 1.125 0 0 1 3 19.875v-6.75ZM9.75 8.625c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125v11.25c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V8.625ZM16.5 4.125c0-.621.504-1.125 1.125-1.125h2.25C20.496 3 21 3.504 21 4.125v15.75c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V4.125Z'
const KEY = 'M15.75 5.25a3 3 0 0 1 3 3m3 0a6 6 0 0 1-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1 1 21.75 8.25Z'
const WALLET = 'M21 12a2.25 2.25 0 0 0-2.25-2.25H15a3 3 0 1 1-6 0H5.25A2.25 2.25 0 0 0 3 12m18 0v6a2.25 2.25 0 0 1-2.25 2.25H5.25A2.25 2.25 0 0 1 3 18v-6m18 0V9M3 12V9m18 0a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 9m18 0V6a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 6v3'
const USERS = 'M15 19.128a9.38 9.38 0 0 0 2.625.372 9.337 9.337 0 0 0 4.121-.952 4.125 4.125 0 0 0-7.533-2.493M15 19.128v-.003c0-1.113-.285-2.16-.786-3.07M15 19.128v.106A12.318 12.318 0 0 1 8.624 21c-2.331 0-4.512-.645-6.374-1.766l-.001-.109a6.375 6.375 0 0 1 11.964-3.07M12 6.375a3.375 3.375 0 1 1-6.75 0 3.375 3.375 0 0 1 6.75 0Zm8.25 2.25a2.625 2.625 0 1 1-5.25 0 2.625 2.625 0 0 1 5.25 0Z'
const TRUST = 'M9 12.75 11.25 15 15 9.75m-3-7.036A11.959 11.959 0 0 1 3.598 6 11.99 11.99 0 0 0 3 9.75c0 5.592 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.31-.21-2.57-.598-3.75h-.152A11.959 11.959 0 0 1 12 2.714Z'
const SETTINGS = 'M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.325.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 0 1 1.37.49l1.296 2.247a1.125 1.125 0 0 1-.26 1.431l-1.003.827c-.293.241-.438.613-.43.992a7.723 7.723 0 0 1 0 .255c-.008.378.137.75.43.991l1.004.827c.424.35.534.955.26 1.43l-1.298 2.247a1.125 1.125 0 0 1-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.47 6.47 0 0 1-.22.128c-.331.183-.581.495-.644.869l-.213 1.281c-.09.543-.56.94-1.11.94h-2.594c-.55 0-1.019-.398-1.11-.94l-.213-1.281c-.062-.374-.312-.686-.644-.87a6.52 6.52 0 0 1-.22-.127c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 0 1-1.369-.49l-1.297-2.247a1.125 1.125 0 0 1 .26-1.431l1.004-.827c.292-.24.437-.613.43-.991a6.932 6.932 0 0 1 0-.255c.007-.38-.138-.751-.43-.992l-1.004-.827a1.125 1.125 0 0 1-.26-1.43l1.297-2.247a1.125 1.125 0 0 1 1.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.086.22-.128.332-.183.582-.495.644-.869l.214-1.28Z M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z'
const sections = [
  {
    title: () => t('nav.grpWorkspace'),
    items: [
      { to: '/app', label: () => t('nav.dashboard'), icon: DASH },
      { to: '/app/onboarding', label: () => t('nav.onboarding'), icon: DOC },
      { to: '/app/devex', label: () => t('nav.devex'), icon: DOC },
      { to: '/app/playground', label: () => t('nav.playground'), icon: DOC },
      { to: '/app/models', label: () => t('nav.models'), icon: 'm21 7.5-9-5.25L3 7.5m18 0-9 5.25m9-5.25v9l-9 5.25M3 7.5l9 5.25M3 7.5v9l9 5.25m0-9v9' },
    ],
  },
  {
    title: () => t('nav.grpAccess'),
    items: [
      { to: '/app/keys', label: () => t('nav.keys'), icon: KEY },
      { to: '/app/team', label: () => t('team.title'), icon: USERS },
      { to: '/app/settings', label: () => t('settings.title'), icon: SETTINGS },
    ],
  },
  {
    title: () => t('nav.grpUsageCost'),
    items: [
      { to: '/app/usage', label: () => t('nav.usage'), icon: CHART },
      { to: '/app/logs', label: () => t('nav.requestLogs'), icon: DOC },
      { to: '/app/latency', label: () => t('nav.latency'), icon: CHART },
      { to: '/app/billing', label: () => t('nav.billing'), icon: WALLET },
      { to: '/app/wallet', label: () => t('nav.wallet'), icon: WALLET },
    ],
  },
  {
    title: () => t('nav.grpTrust'),
    items: [
      { to: '/status', label: () => t('nav.status'), icon: TRUST },
      { to: '/trust', label: () => t('nav.trust'), icon: TRUST },
    ],
  },
  {
    title: () => t('nav.grpAdmin'),
    admin: true,
    items: [
      { to: '/app/admin', label: () => t('admin.title'), icon: 'M3.75 21h16.5M4.5 3h15M5.25 3v18m13.5-18v18M9 6.75h1.5m-1.5 3h1.5m-1.5 3h1.5m3-6H15m-1.5 3H15m-1.5 3H15M9 21v-3.375c0-.621.504-1.125 1.125-1.125h3.75c.621 0 1.125.504 1.125 1.125V21' },
    ],
  },
]

// 管理分组仅对 admin/superadmin 可见——非管理员看不到也点不到入口（后端 RBAC 仍是真正的强制层）。
const visibleSections = computed(() => sections.filter((s) => !s.admin || auth.isAdmin))

// 进入控制台外壳时拉取自身平台角色，决定管理入口可见性。
onMounted(() => { void auth.fetchMe() })
</script>

<template>
  <div class="app-shell min-h-screen bg-bg text-ink lg:flex">
    <header class="app-mobile-header sticky top-0 z-40 flex items-center gap-3 border-b border-line bg-panel/90 px-4 py-3 backdrop-blur lg:hidden">
      <router-link
        to="/"
        class="group flex items-center gap-2.5 transition-opacity hover:opacity-80"
        :title="t('nav.home')"
      >
        <BrandMark size="sm" />
      </router-link>
      <div class="ml-auto flex items-center gap-2">
        <ThemeLocale />
        <button class="btn-ghost !min-h-0 !px-3 !py-1.5 text-xs" @click="auth.logout()">{{ t('nav.logout') }}</button>
      </div>
    </header>

    <aside class="app-sidebar hidden h-screen w-[18rem] shrink-0 border-r border-line bg-panel/82 lg:sticky lg:top-0 lg:flex lg:flex-col">
      <router-link
        to="/"
        class="group flex items-center gap-2.5 px-5 py-5 transition-opacity hover:opacity-85"
        :title="t('nav.home')"
      >
        <BrandMark size="md" :subtitle="t('surface.brandSubtitle')" />
      </router-link>

      <div class="px-3 pb-3">
        <OrgSwitcher />
      </div>

      <nav class="flex-1 overflow-y-auto px-3 pb-4 space-y-5">
        <div v-for="(sec, si) in visibleSections" :key="si">
          <p class="app-nav-label px-3 pb-1.5 pt-1 text-[10px] font-semibold uppercase text-muted/70">{{ sec.title() }}</p>
          <div class="space-y-1">
            <router-link
              v-for="item in sec.items"
              :key="item.to"
              :to="item.to"
              class="app-nav-item group relative flex items-center gap-3 rounded-lg px-3 py-2 text-sm text-muted transition-colors hover:bg-elev/80 hover:text-ink"
              :active-class="item.to === '/app' ? '' : ACTIVE"
              :exact-active-class="ACTIVE"
            >
              <svg
                class="h-[18px] w-[18px] shrink-0 opacity-70 transition-opacity group-hover:opacity-100"
                viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6"
                stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"
              >
                <path :d="item.icon" />
              </svg>
              {{ item.label() }}
            </router-link>
          </div>
        </div>
      </nav>

      <div class="app-sidebar-footer border-t border-line p-3 space-y-2.5">
        <router-link
          to="/app/settings"
          class="flex items-center gap-2 rounded-lg px-2 py-2 text-xs text-muted transition-colors hover:bg-elev hover:text-ink"
          active-class="!text-ink bg-elev"
          :title="auth.email"
        >
          <svg class="h-4 w-4 shrink-0 opacity-70" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M15.75 6a3.75 3.75 0 1 1-7.5 0 3.75 3.75 0 0 1 7.5 0ZM4.501 20.118a7.5 7.5 0 0 1 14.998 0A17.933 17.933 0 0 1 12 21.75c-2.676 0-5.216-.584-7.499-1.632Z" />
          </svg>
          <span class="truncate">{{ auth.email }}</span>
        </router-link>
        <ThemeLocale />
        <button class="btn-ghost w-full !py-1.5 text-xs" @click="auth.logout()">{{ t('nav.logout') }}</button>
      </div>
    </aside>

    <main class="app-main min-w-0 flex-1">
      <div class="mx-auto w-full max-w-[1480px] px-4 py-5 sm:px-6 lg:px-8 lg:py-7">
        <div class="app-topbar mb-6 hidden items-center justify-between gap-4 lg:flex">
          <div>
            <p class="text-xs font-medium uppercase text-muted/70">{{ t('nav.console') }}</p>
            <p class="mt-1 text-sm text-muted">{{ t('surface.consoleSubtitle') }}</p>
          </div>
          <div class="flex items-center gap-2">
            <router-link to="/app/playground" class="btn-ghost !py-1.5 text-xs">{{ t('nav.playground') }}</router-link>
            <router-link to="/app/keys" class="btn-primary !py-1.5 text-xs">{{ t('keys.create') }}</router-link>
          </div>
        </div>
        <nav class="app-mobile-tabs mb-5 flex min-w-0 max-w-full gap-2 overflow-x-auto pb-1 lg:hidden" aria-label="Mobile navigation">
          <template v-for="sec in visibleSections" :key="sec.title()">
            <router-link
              v-for="item in sec.items"
              :key="item.to"
              :to="item.to"
              class="shrink-0 rounded-lg border border-line bg-panel px-3 py-2 text-sm text-muted"
              :active-class="item.to === '/app' ? '' : ACTIVE"
              :exact-active-class="ACTIVE"
            >
              {{ item.label() }}
            </router-link>
          </template>
        </nav>
      <router-view v-slot="{ Component }">
        <transition name="page" mode="out-in">
          <component :is="Component" :key="$route.path" />
        </transition>
      </router-view>
      </div>
    </main>
  </div>
</template>

<style scoped>
/* 激活项左侧 accent 指示条 */
.nav-active::before {
  content: '';
  position: absolute;
  left: -13px;
  top: 50%;
  transform: translateY(-50%);
  height: 20px;
  width: 2px;
  border-radius: 2px;
  background: rgb(var(--c-accent));
}
</style>
