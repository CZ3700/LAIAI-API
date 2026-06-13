import { createRouter, createWebHistory } from 'vue-router'
import { useAuth } from '@/stores/auth'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: () => import('@/pages/Login.vue') },
    { path: '/register', component: () => import('@/pages/Register.vue') },
    {
      path: '/',
      component: () => import('@/layouts/Shell.vue'),
      meta: { auth: true },
      children: [
        { path: '', component: () => import('@/pages/Dashboard.vue') },
        { path: 'playground', component: () => import('@/pages/Playground.vue') },
        { path: 'models', component: () => import('@/pages/Models.vue') },
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
  if ((to.path === '/login' || to.path === '/register') && auth.isAuthed) return { path: '/' }
})
