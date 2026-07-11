<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAuth } from '@/stores/auth'
import ThemeLocale from '@/components/ThemeLocale.vue'
import BrandMark from '@/components/BrandMark.vue'
import OrgSwitcher from '@/components/OrgSwitcher.vue'

const { t } = useI18n()
const auth = useAuth()
const route = useRoute()
const collapsed = ref(false)

const ACTIVE = '!text-ink bg-elev shadow-sm nav-active'

const icons = {
  dashboard: 'M3 13.125C3 12.504 3.504 12 4.125 12h2.25c.621 0 1.125.504 1.125 1.125v6.75C7.5 20.496 6.996 21 6.375 21h-2.25A1.125 1.125 0 0 1 3 19.875v-6.75ZM9.75 8.625c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125v11.25c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V8.625ZM16.5 4.125c0-.621.504-1.125 1.125-1.125h2.25C20.496 3 21 3.504 21 4.125v15.75c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V4.125Z',
  model: 'm21 7.5-9-5.25L3 7.5m18 0-9 5.25m9-5.25v9l-9 5.25M3 7.5l9 5.25M3 7.5v9l9 5.25m0-9v9',
  docs: 'M19.5 14.25v-2.625A3.375 3.375 0 0 0 16.125 8.25h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5A3.375 3.375 0 0 0 10.125 2.25H8.25m2.25 0H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z',
  key: 'M15.75 5.25a3 3 0 0 1 3 3m3 0a6 6 0 0 1-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1 1 21.75 8.25Z',
  wallet: 'M21 12a2.25 2.25 0 0 0-2.25-2.25H15a3 3 0 1 1-6 0H5.25A2.25 2.25 0 0 0 3 12m18 0v6a2.25 2.25 0 0 1-2.25 2.25H5.25A2.25 2.25 0 0 1 3 18v-6m18 0V9M3 12V9m18 0a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 9m18 0V6a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 6v3',
  invoice: 'M19.5 14.25v-2.625A3.375 3.375 0 0 0 16.125 8.25h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5A3.375 3.375 0 0 0 10.125 2.25H6.75A2.25 2.25 0 0 0 4.5 4.5v15A2.25 2.25 0 0 0 6.75 21h10.5a2.25 2.25 0 0 0 2.25-2.25v-4.5ZM8.25 15h7.5M8.25 18h4.5M8.25 12h3',
  log: 'M19.5 14.25v-2.625A3.375 3.375 0 0 0 16.125 8.25h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5A3.375 3.375 0 0 0 10.125 2.25H6.75A2.25 2.25 0 0 0 4.5 4.5v15A2.25 2.25 0 0 0 6.75 21h10.5a2.25 2.25 0 0 0 2.25-2.25v-4.5ZM9 15.75h6M9 12.75h3',
  image: 'm2.25 15.75 5.159-5.159a2.25 2.25 0 0 1 3.182 0l5.159 5.159m-1.5-1.5 1.409-1.409a2.25 2.25 0 0 1 3.182 0l2.909 2.909M3.75 19.5h16.5a1.5 1.5 0 0 0 1.5-1.5V6a1.5 1.5 0 0 0-1.5-1.5H3.75A1.5 1.5 0 0 0 2.25 6v12a1.5 1.5 0 0 0 1.5 1.5Zm10.5-11.25h.008v.008h-.008V8.25Z',
  node: 'M6.75 12a2.25 2.25 0 1 0 0-4.5 2.25 2.25 0 0 0 0 4.5Zm0 0v3.75A2.25 2.25 0 0 0 9 18h6m-8.25-6h8.25A2.25 2.25 0 0 0 17.25 9.75V6M17.25 6a2.25 2.25 0 1 0 0-4.5 2.25 2.25 0 0 0 0 4.5Zm0 12a2.25 2.25 0 1 0 0-4.5 2.25 2.25 0 0 0 0 4.5Z',
  clock: 'M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z',
  team: 'M15 19.128a9.38 9.38 0 0 0 2.625.372 9.337 9.337 0 0 0 4.121-.952 4.125 4.125 0 0 0-7.533-2.493M15 19.128v-.003c0-1.113-.285-2.16-.786-3.07M15 19.128v.106A12.318 12.318 0 0 1 8.624 21c-2.331 0-4.512-.645-6.374-1.766l-.001-.109a6.375 6.375 0 0 1 11.964-3.07M12 6.375a3.375 3.375 0 1 1-6.75 0 3.375 3.375 0 0 1 6.75 0Zm8.25 2.25a2.625 2.625 0 1 1-5.25 0 2.625 2.625 0 0 1 5.25 0Z',
  admin: 'M3.75 21h16.5M4.5 3h15M5.25 3v18m13.5-18v18M9 6.75h1.5m-1.5 3h1.5m-1.5 3h1.5m3-6H15m-1.5 3H15m-1.5 3H15M9 21v-3.375c0-.621.504-1.125 1.125-1.125h3.75c.621 0 1.125.504 1.125 1.125V21',
  user: 'M15.75 6a3.75 3.75 0 1 1-7.5 0 3.75 3.75 0 0 1 7.5 0ZM4.501 20.118a7.5 7.5 0 0 1 14.998 0A17.933 17.933 0 0 1 12 21.75c-2.676 0-5.216-.584-7.499-1.632Z',
  card: 'M2.25 8.25h19.5M2.25 9h19.5m-16.5 5.25h6m-6 2.25h3m-3.75 3h15a2.25 2.25 0 0 0 2.25-2.25V6.75A2.25 2.25 0 0 0 19.5 4.5h-15a2.25 2.25 0 0 0-2.25 2.25v10.5A2.25 2.25 0 0 0 4.5 19.5Z',
  chat: 'M7.5 8.25h9m-9 3H12m-9.75 1.51c0 1.6 1.123 2.994 2.707 3.227 1.129.166 2.27.293 3.423.379.35.026.67.21.865.501L12 21l2.755-4.133a1.14 1.14 0 0 1 .865-.501 48.172 48.172 0 0 0 3.423-.379c1.584-.233 2.707-1.626 2.707-3.228V6.741c0-1.602-1.123-2.995-2.707-3.228A48.394 48.394 0 0 0 12 3c-2.392 0-4.744.175-7.043.513C3.373 3.746 2.25 5.14 2.25 6.741v6.018Z',
  diag: 'm21 21-5.197-5.197m0 0A7.5 7.5 0 1 0 5.196 5.196a7.5 7.5 0 0 0 10.607 10.607Z',
}

type NavItem = { to: string; label: string; icon: keyof typeof icons; admin?: boolean }
const sections: { title: string; items: NavItem[] }[] = [
  { title: 'shell.groupWorkspace', items: [
    { to: '/app', label: 'shell.navOverview', icon: 'dashboard' },
    { to: '/app/models', label: 'shell.navModels', icon: 'model' },
    { to: '/app/playground', label: 'nav.playground', icon: 'chat' },
    { to: '/app/diagnostics', label: 'shell.navDiagnostics', icon: 'diag' },
  ] },
  { title: 'shell.groupAccess', items: [
    { to: '/app/keys', label: 'shell.navKeys', icon: 'key' },
    { to: '/app/team', label: 'shell.navTeam', icon: 'team' },
  ] },
  { title: 'shell.groupUsage', items: [
    { to: '/app/usage', label: 'shell.navUsage', icon: 'dashboard' },
    { to: '/app/billing', label: 'shell.navBilling', icon: 'wallet' },
    { to: '/app/recharges', label: 'shell.navRecharges', icon: 'card' },
  ] },
  { title: 'shell.groupObservability', items: [
    { to: '/app/request-logs', label: 'shell.navRequestLogs', icon: 'log' },
    { to: '/app/image-logs', label: 'shell.navImageLogs', icon: 'image' },
    { to: '/app/token-analysis', label: 'shell.navTokenAnalysis', icon: 'node' },
    { to: '/app/latency', label: 'shell.navLatency', icon: 'clock' },
  ] },
  { title: 'shell.groupSystem', items: [
    { to: '/app/settings', label: 'shell.navSettings', icon: 'user' },
    { to: '/app/admin', label: 'shell.navAdmin', icon: 'admin', admin: true },
  ] },
]

const visibleSections = computed(() => sections
  .map((s) => ({ ...s, items: s.items.filter((item) => !item.admin || auth.isAdmin) }))
  .filter((s) => s.items.length > 0))

const currentTitle = computed(() => {
  const found = sections.flatMap((s) => s.items).find((item) => {
    if (item.to === '/app') return route.path === '/app'
    return route.path.startsWith(item.to)
  })
  return found ? t(found.label) : t('nav.console')
})

const accountLabel = computed(() => (auth.email || 'Laiai').split('@')[0])

onMounted(() => { void auth.fetchMe() })
</script>

<template>
  <div class="app-shell min-h-screen bg-bg text-ink lg:flex">
    <header class="sticky top-0 z-40 flex items-center gap-3 border-b border-line bg-panel/95 px-4 py-3 lg:hidden">
      <router-link to="/" class="flex items-center gap-2">
        <BrandMark size="sm" />
      </router-link>
      <span class="min-w-0 flex-1 truncate text-sm font-semibold">{{ currentTitle }}</span>
      <div class="flex shrink-0 items-center gap-2">
        <ThemeLocale />
      </div>
    </header>

    <aside
      class="console-sidebar hidden h-screen shrink-0 border-r border-line bg-panel lg:sticky lg:top-0 lg:flex lg:flex-col"
      :class="collapsed ? 'w-[4.75rem]' : 'w-[13.75rem]'"
    >
      <router-link to="/" class="flex items-center justify-center gap-2.5 px-4 py-4 transition-opacity hover:opacity-85">
        <BrandMark :size="collapsed ? 'sm' : 'md'" :show-word="!collapsed" />
      </router-link>

      <nav class="flex-1 overflow-y-auto px-2 pb-4">
        <div v-for="sec in visibleSections" :key="sec.title" class="mb-4">
          <p v-if="!collapsed" class="px-3 pb-1.5 pt-1 text-xs text-muted">{{ t(sec.title) }}</p>
          <div class="space-y-1">
            <router-link
              v-for="item in sec.items"
              :key="item.to"
              :to="item.to"
              class="app-nav-item group relative flex items-center gap-3 rounded px-3 py-2 text-sm text-muted transition-colors hover:bg-elev/80 hover:text-ink"
              :active-class="item.to === '/app' ? '' : ACTIVE"
              :exact-active-class="ACTIVE"
              :title="t(item.label)"
            >
              <svg class="h-[18px] w-[18px] shrink-0 opacity-70 transition-opacity group-hover:opacity-100" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path :d="icons[item.icon]" />
              </svg>
              <span v-if="!collapsed" class="truncate">{{ t(item.label) }}</span>
            </router-link>
          </div>
        </div>
      </nav>

      <!-- 组织/团队切换器：创建团队 + 在个人/团队 org 间切换（Team 页管理成员的前置入口）。
           收起态隐藏（其内含 select/弹窗，窄栏放不下）。 -->
      <div v-if="!collapsed" class="border-t border-line p-3">
        <OrgSwitcher />
      </div>
    </aside>

    <main class="app-main min-w-0 flex-1">
      <div class="sticky top-0 z-30 hidden h-14 items-center gap-3 border-b border-line bg-bg/92 px-6 backdrop-blur lg:flex">
        <button class="btn-ghost !h-8 !min-h-0 !w-8 !px-0 !py-0" type="button" :aria-label="collapsed ? t('shell.ariaExpandSidebar') : t('shell.ariaCollapseSidebar')" @click="collapsed = !collapsed">
          <svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true">
            <path d="M4 7h16M4 12h16M4 17h16" />
          </svg>
        </button>
        <h1 class="text-base font-semibold">{{ currentTitle }}</h1>
        <div class="ml-auto flex items-center gap-2">
          <router-link to="/app/recharges" class="btn-primary !min-h-0 !px-3 !py-1.5 text-xs">{{ t('shell.btnRecharge') }}</router-link>
          <router-link to="/app/billing" class="btn-ghost !min-h-0 !px-3 !py-1.5 text-xs">{{ t('shell.btnBalance') }}</router-link>
          <ThemeLocale />
          <router-link to="/app/settings" class="btn-ghost !min-h-0 !px-3 !py-1.5 text-xs">
            {{ accountLabel }}
          </router-link>
        </div>
      </div>

      <div class="mx-auto w-full max-w-[1480px] px-4 py-5 sm:px-6 lg:px-8 lg:py-7">
        <nav class="mb-5 flex min-w-0 max-w-full gap-2 overflow-x-auto pb-1 lg:hidden" :aria-label="t('shell.ariaMobileNav')">
          <template v-for="sec in visibleSections" :key="sec.title">
            <router-link
              v-for="item in sec.items"
              :key="item.to"
              :to="item.to"
              class="shrink-0 rounded border border-line bg-panel px-3 py-2 text-sm text-muted"
              :active-class="item.to === '/app' ? '' : ACTIVE"
              :exact-active-class="ACTIVE"
            >
              {{ t(item.label) }}
            </router-link>
          </template>
        </nav>
        <router-view v-slot="{ Component }">
          <transition name="page" mode="out-in">
            <!-- Playground keep-alive：切页不销毁组件——生图结果/视频结果/模型选择/各模态输入
                 全是组件内存态，销毁重建=全丢（用户实测反馈）。聊天已有 localStorage 兜底，
                 保活后其余状态同样跨导航存活；视频轮询在后台继续，切回即见结果。 -->
            <keep-alive include="Playground">
              <component :is="Component" :key="$route.path" />
            </keep-alive>
          </transition>
        </router-view>
      </div>
    </main>
  </div>
</template>

<style scoped>
.console-sidebar {
  transition: width 0.18s ease;
}
.nav-active::before {
  content: '';
  position: absolute;
  left: -9px;
  top: 50%;
  transform: translateY(-50%);
  height: 20px;
  width: 2px;
  border-radius: 2px;
  background: rgb(var(--c-accent));
}
</style>
