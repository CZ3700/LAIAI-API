import { createRouter, createWebHistory } from 'vue-router'
import { useAuth } from '@/stores/auth'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    // 公开门面（无需登录）
    {
      path: '/',
      component: () => import('@/layouts/PublicLayout.vue'),
      children: [
        { path: '', component: () => import('@/pages/Landing.vue') },
        { path: 'models', component: () => import('@/pages/Models.vue') },
        { path: 'rankings', component: () => import('@/pages/Rankings.vue') },
        { path: 'docs', component: () => import('@/pages/Docs.vue') },
        { path: 'about', component: () => import('@/pages/About.vue') },
        { path: 'status', component: () => import('@/pages/Status.vue') },
        { path: 'terms', component: () => import('@/pages/Legal.vue') },
        { path: 'privacy', component: () => import('@/pages/Legal.vue') },
      ],
    },
    { path: '/login', component: () => import('@/pages/Login.vue') },
    { path: '/register', component: () => import('@/pages/Register.vue') },
    { path: '/oauth', component: () => import('@/pages/OAuthCallback.vue') },
    // 控制台（需登录）
    {
      path: '/app',
      component: () => import('@/layouts/Shell.vue'),
      meta: { auth: true },
      children: [
        { path: '', component: () => import('@/pages/Dashboard.vue') },
        { path: 'models', component: () => import('@/pages/ConsoleModels.vue') },
        { path: 'playground', component: () => import('@/pages/Playground.vue') },
        { path: 'wallet', component: () => import('@/pages/Wallet.vue') },
        { path: 'keys', component: () => import('@/pages/Keys.vue') },
        { path: 'usage', component: () => import('@/pages/Usage.vue') },
        { path: 'team', component: () => import('@/pages/Team.vue') },
        { path: 'settings', component: () => import('@/pages/Settings.vue') },
        { path: 'admin', component: () => import('@/pages/Admin.vue') },
      ],
    },
  ],
})

router.beforeEach((to) => {
  const auth = useAuth()
  if (to.meta.auth && !auth.isAuthed) return { path: '/login', query: { to: to.fullPath } }
  if ((to.path === '/login' || to.path === '/register') && auth.isAuthed) return { path: '/app' }
})

// 部署自愈：Vite 按内容哈希切分懒加载 chunk，发版后旧标签页/缓存里的 index.html 仍引用
// 已被替换的旧 chunk 哈希，导致某个未访问过的懒路由（如 /app/playground）import 失败「打不开」，
// 而已加载到内存的页面照常工作。这里抓到动态 import 失败 → 当次会话只硬刷新一次（防循环）→
// 新 index.html 带新哈希自动恢复。成功导航后清标记，使未来每次发版都能再次自愈。
const chunkReloadKey = 'laiai.chunkReloaded'
function isDynamicImportError(err: unknown): boolean {
  const m = String((err as Error)?.message || err)
  return /dynamically imported module|Importing a module script failed|Failed to fetch dynamically|error loading dynamically imported/i.test(m)
}
function reloadOnce() {
  if (sessionStorage.getItem(chunkReloadKey)) return // 已重载过一次仍失败 → 不再循环，让错误暴露
  sessionStorage.setItem(chunkReloadKey, '1')
  window.location.reload()
}
router.onError((err) => {
  if (isDynamicImportError(err)) reloadOnce()
})
router.afterEach(() => {
  sessionStorage.removeItem(chunkReloadKey) // 成功导航=chunk 正常，清一次性标记
})
// 兜底：Vite 预加载助手失败时也走同一自愈路径
window.addEventListener('vite:preloadError', reloadOnce)
