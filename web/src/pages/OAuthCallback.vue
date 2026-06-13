<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuth } from '@/stores/auth'

// OAuth 回调落地页：后端验证后重定向到此，URL 带 session token。
// 立即存入会话并跳转控制台，replace 清掉 URL 里的 token（不留浏览器历史）。
const router = useRouter()
const route = useRoute()
const auth = useAuth()

onMounted(async () => {
  const token = String(route.query.token ?? '')
  const exp = Number(route.query.exp ?? 0)
  const org = Number(route.query.org ?? 0)
  const email = String(route.query.email ?? '')
  if (token && exp && org) {
    auth.setSession({ token, expiresAt: exp, orgId: org, email })
    await router.replace('/app')
  } else {
    await router.replace('/login')
  }
})
</script>

<template>
  <div class="grid min-h-screen place-items-center text-muted">
    <div class="flex items-center gap-2 text-sm">
      <span class="h-2 w-2 animate-bounce rounded-full bg-accent" style="animation-delay: 0ms"></span>
      <span class="h-2 w-2 animate-bounce rounded-full bg-accent" style="animation-delay: 150ms"></span>
      <span class="h-2 w-2 animate-bounce rounded-full bg-accent" style="animation-delay: 300ms"></span>
    </div>
  </div>
</template>
