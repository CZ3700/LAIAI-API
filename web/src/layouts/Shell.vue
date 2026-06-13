<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useAuth } from '@/stores/auth'
import ThemeLocale from '@/components/ThemeLocale.vue'
import OrgSwitcher from '@/components/OrgSwitcher.vue'

const { t } = useI18n()
const auth = useAuth()

// '/' 是所有路径的前缀：仪表盘项只在精确匹配时激活，其余项保留前缀匹配
const ACTIVE = '!text-ink bg-elev shadow-sm nav-active'

// 内联 SVG path（24×24 stroke 风格）：不引图标库，几个图标不值一个依赖。
// 分组导航（控制台 / 个人中心 / 管理）——对标成熟后台的信息架构。
const DASH = 'M2.25 12 11.204 3.045c.44-.439 1.152-.439 1.591 0L21.75 12M4.5 9.75v10.125c0 .621.504 1.125 1.125 1.125H9.75v-4.875c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125V21h4.125c.621 0 1.125-.504 1.125-1.125V9.75M8.25 21h8.25'
const sections = [
  {
    title: () => t('nav.grpConsole'),
    items: [
      { to: '/app', label: () => t('nav.dashboard'), icon: DASH },
      { to: '/app/playground', label: () => t('nav.playground'), icon: 'M8.25 7.5V6.108c0-1.135.845-2.098 1.976-2.192.373-.03.748-.057 1.123-.08M15.75 18H18a2.25 2.25 0 0 0 2.25-2.25V6.108c0-1.135-.845-2.098-1.976-2.192a48.424 48.424 0 0 0-1.123-.08M15.75 18.75v-1.875a3.375 3.375 0 0 0-3.375-3.375h-1.5a1.125 1.125 0 0 1-1.125-1.125v-1.5A3.375 3.375 0 0 0 6.375 7.5H5.25m11.9-3.664A2.251 2.251 0 0 0 15 2.25h-1.5a2.251 2.251 0 0 0-2.15 1.586m5.8 0c.065.21.1.433.1.664v.75h-6V4.5c0-.231.035-.454.1-.664M6.75 7.5H4.875c-.621 0-1.125.504-1.125 1.125v12c0 .621.504 1.125 1.125 1.125h13.5c.621 0 1.125-.504 1.125-1.125V16.5a9 9 0 0 0-9-9Z' },
      { to: '/app/models', label: () => t('nav.models'), icon: 'm21 7.5-9-5.25L3 7.5m18 0-9 5.25m9-5.25v9l-9 5.25M3 7.5l9 5.25M3 7.5v9l9 5.25m0-9v9' },
      { to: '/app/keys', label: () => t('nav.keys'), icon: 'M15.75 5.25a3 3 0 0 1 3 3m3 0a6 6 0 0 1-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1 1 21.75 8.25Z' },
      { to: '/app/usage', label: () => t('nav.usage'), icon: 'M3 13.125C3 12.504 3.504 12 4.125 12h2.25c.621 0 1.125.504 1.125 1.125v6.75C7.5 20.496 6.996 21 6.375 21h-2.25A1.125 1.125 0 0 1 3 19.875v-6.75ZM9.75 8.625c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125v11.25c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V8.625ZM16.5 4.125c0-.621.504-1.125 1.125-1.125h2.25C20.496 3 21 3.504 21 4.125v15.75c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V4.125Z' },
    ],
  },
  {
    title: () => t('nav.grpPersonal'),
    items: [
      { to: '/app/wallet', label: () => t('nav.wallet'), icon: 'M21 12a2.25 2.25 0 0 0-2.25-2.25H15a3 3 0 1 1-6 0H5.25A2.25 2.25 0 0 0 3 12m18 0v6a2.25 2.25 0 0 1-2.25 2.25H5.25A2.25 2.25 0 0 1 3 18v-6m18 0V9M3 12V9m18 0a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 9m18 0V6a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 6v3' },
      { to: '/app/team', label: () => t('team.title'), icon: 'M15 19.128a9.38 9.38 0 0 0 2.625.372 9.337 9.337 0 0 0 4.121-.952 4.125 4.125 0 0 0-7.533-2.493M15 19.128v-.003c0-1.113-.285-2.16-.786-3.07M15 19.128v.106A12.318 12.318 0 0 1 8.624 21c-2.331 0-4.512-.645-6.374-1.766l-.001-.109a6.375 6.375 0 0 1 11.964-3.07M12 6.375a3.375 3.375 0 1 1-6.75 0 3.375 3.375 0 0 1 6.75 0Zm8.25 2.25a2.625 2.625 0 1 1-5.25 0 2.625 2.625 0 0 1 5.25 0Z' },
      { to: '/app/settings', label: () => t('settings.title'), icon: 'M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.325.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 0 1 1.37.49l1.296 2.247a1.125 1.125 0 0 1-.26 1.431l-1.003.827c-.293.241-.438.613-.43.992a7.723 7.723 0 0 1 0 .255c-.008.378.137.75.43.991l1.004.827c.424.35.534.955.26 1.43l-1.298 2.247a1.125 1.125 0 0 1-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.47 6.47 0 0 1-.22.128c-.331.183-.581.495-.644.869l-.213 1.281c-.09.543-.56.94-1.11.94h-2.594c-.55 0-1.019-.398-1.11-.94l-.213-1.281c-.062-.374-.312-.686-.644-.87a6.52 6.52 0 0 1-.22-.127c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 0 1-1.369-.49l-1.297-2.247a1.125 1.125 0 0 1 .26-1.431l1.004-.827c.292-.24.437-.613.43-.991a6.932 6.932 0 0 1 0-.255c.007-.38-.138-.751-.43-.992l-1.004-.827a1.125 1.125 0 0 1-.26-1.43l1.297-2.247a1.125 1.125 0 0 1 1.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.086.22-.128.332-.183.582-.495.644-.869l.214-1.28Z M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z' },
    ],
  },
  {
    title: () => t('nav.grpAdmin'),
    items: [
      { to: '/app/admin', label: () => t('admin.title'), icon: 'M3.75 21h16.5M4.5 3h15M5.25 3v18m13.5-18v18M9 6.75h1.5m-1.5 3h1.5m-1.5 3h1.5m3-6H15m-1.5 3H15m-1.5 3H15M9 21v-3.375c0-.621.504-1.125 1.125-1.125h3.75c.621 0 1.125.504 1.125 1.125V21' },
    ],
  },
]
</script>

<template>
  <div class="min-h-screen flex">
    <aside class="w-60 shrink-0 border-r border-line/60 flex flex-col backdrop-blur-xl"
      style="background: rgb(var(--c-glass) / 0.5)">
      <!-- 品牌：点击回公开首页 -->
      <router-link
        to="/"
        class="group flex items-center gap-2.5 px-5 pt-5 pb-4 transition-opacity hover:opacity-80"
        :title="t('nav.home')"
      >
        <div
          class="grid h-8 w-8 shrink-0 place-items-center rounded-lg text-sm font-bold text-white transition-transform group-hover:scale-105"
          style="background: linear-gradient(135deg, rgb(var(--c-accent)), rgb(var(--c-accent-2)))"
          aria-hidden="true"
        >
          L
        </div>
        <span class="text-lg font-semibold tracking-tight">LAIAI</span>
        <svg class="ml-auto h-4 w-4 text-muted opacity-0 transition-opacity group-hover:opacity-100" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M9 6l6 6-6 6" />
        </svg>
      </router-link>
      <div class="px-3 pb-3">
        <OrgSwitcher />
      </div>
      <nav class="flex-1 overflow-y-auto px-3 pb-2 space-y-4">
        <div v-for="(sec, si) in sections" :key="si">
          <p class="px-3 pb-1 pt-1 text-[10px] font-medium uppercase tracking-wider text-muted/70">{{ sec.title() }}</p>
          <div class="space-y-0.5">
            <router-link
              v-for="item in sec.items"
              :key="item.to"
              :to="item.to"
              class="group relative flex items-center gap-3 rounded-lg px-3 py-2 text-sm text-muted
                transition-colors hover:text-ink hover:bg-elev"
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
      <div class="p-3 border-t border-line space-y-2.5">
        <router-link
          to="/app/settings"
          class="flex items-center gap-2 rounded-lg px-2 py-1.5 text-xs text-muted transition-colors hover:bg-elev hover:text-ink"
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
    <main class="flex-1 min-w-0 p-8">
      <router-view v-slot="{ Component }">
        <transition name="page" mode="out-in">
          <component :is="Component" :key="$route.path" />
        </transition>
      </router-view>
    </main>
  </div>
</template>

<style scoped>
/* 激活项左侧 accent 指示条 */
.nav-active::before {
  content: '';
  position: absolute;
  left: -12px;
  top: 50%;
  transform: translateY(-50%);
  height: 18px;
  width: 3px;
  border-radius: 3px;
  background: linear-gradient(180deg, rgb(var(--c-accent)), rgb(var(--c-accent-2)));
}
</style>
