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
      ],
    },
    { path: '/login', component: () => import('@/pages/Login.vue') },
    { path: '/register', component: () => import('@/pages/Register.vue') },
    // 控制台（需登录）
    {
      path: '/app',
      component: () => import('@/layouts/Shell.vue'),
      meta: { auth: true },
      children: [
        { path: '', component: () => import('@/pages/Dashboard.vue') },
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
