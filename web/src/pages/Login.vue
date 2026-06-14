<script setup lang="ts">
import { onMounted, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter, useRoute } from 'vue-router'
import { useAuth } from '@/stores/auth'
import { api, errorMessage } from '@/lib/api'
import ThemeLocale from '@/components/ThemeLocale.vue'

const { t } = useI18n()
const auth = useAuth()
const router = useRouter()
const route = useRoute()

const oauthProviders = ref<string[]>([])
const telegramBot = ref('')
const telegramHost = ref<HTMLElement | null>(null)
onMounted(async () => {
  try {
    const r = await api<{ providers: string[]; telegram_bot?: string }>('GET', '/api/auth/oauth', undefined, { auth: false })
    oauthProviders.value = r.providers ?? []
    telegramBot.value = r.telegram_bot ?? ''
    if (telegramBot.value) {
      await nextTick() // 等 v-if 的容器 div 渲染出来，否则 telegramHost 仍为 null，组件挂不上
      mountTelegramWidget()
    }
  } catch {
    // 无 OAuth 不影响密码登录
  }
})
function oauthLogin(provider: string) {
  window.location.href = `/api/auth/oauth/${provider}/start`
}
function providerLabel(p: string): string {
  const m: Record<string, string> = { github: 'GitHub', google: 'Google', discord: 'Discord', oidc: 'SSO' }
  return m[p] ?? p
}

// —— Telegram 登录组件（widget，非跳转流）——
// Telegram JS 组件授权后回调 window.onTelegramAuth(user)，把签名数据 POST 给后端验 HMAC。
async function onTelegramAuth(u: Record<string, unknown>) {
  try {
    const r = await api<{ token: string; expires_at: number; org_id: number }>(
      'POST', '/api/auth/telegram', u, { auth: false },
    )
    auth.setSession({ token: r.token, expiresAt: r.expires_at, orgId: r.org_id, email: '' })
    await router.replace((route.query.to as string) || '/app')
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  }
}
function mountTelegramWidget() {
  ;(window as unknown as { onTelegramAuth: typeof onTelegramAuth }).onTelegramAuth = onTelegramAuth
  const el = telegramHost.value
  if (!el) return
  el.innerHTML = ''
  const s = document.createElement('script')
  s.async = true
  s.src = 'https://telegram.org/js/telegram-widget.js?22'
  s.setAttribute('data-telegram-login', telegramBot.value)
  s.setAttribute('data-size', 'large')
  s.setAttribute('data-onauth', 'onTelegramAuth(user)')
  s.setAttribute('data-request-access', 'write')
  el.appendChild(s)
}

const email = ref('')
const password = ref('')
const code = ref('')
const need2fa = ref(false)
const busy = ref(false)
const error = ref('')

async function submit() {
  busy.value = true
  error.value = ''
  try {
    const r = await auth.login(email.value, password.value)
    if (r === '2fa') {
      need2fa.value = true
      return
    }
    await router.replace(String(route.query.to ?? '/app'))
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    busy.value = false
  }
}

async function submit2fa() {
  busy.value = true
  error.value = ''
  try {
    await auth.verify2fa(code.value)
    await router.replace(String(route.query.to ?? '/app'))
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="aurora min-h-screen grid place-items-center p-6">
    <div class="fixed top-4 right-4"><ThemeLocale /></div>
    <div class="w-full max-w-sm space-y-6">
      <div class="flex items-center justify-center gap-2.5">
        <div
          class="grid h-10 w-10 place-items-center rounded-xl text-base font-bold text-white"
          style="background: linear-gradient(135deg, rgb(var(--c-accent)), rgb(var(--c-accent-2)))"
          aria-hidden="true"
        >
          L
        </div>
        <span class="text-2xl font-semibold tracking-tight">LAIAI</span>
      </div>
      <!-- 2FA 二阶段 -->
      <form v-if="need2fa" class="card modal-panel w-full p-8 space-y-5" @submit.prevent="submit2fa">
        <h1 class="text-xl font-semibold">{{ t('auth.twofaTitle') }}</h1>
        <p class="text-sm text-muted">{{ t('auth.twofaHint') }}</p>
        <div>
          <label class="label" for="totp">{{ t('auth.verifyCode') }}</label>
          <input id="totp" v-model="code" class="input font-mono text-center text-lg tracking-widest" maxlength="20" autocomplete="one-time-code" autofocus required />
          <p class="mt-1 text-xs text-muted">{{ t('auth.twofaBackupHint') }}</p>
        </div>
        <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
        <button class="btn-primary w-full" :disabled="busy">{{ t('auth.signin') }}</button>
      </form>

      <!-- 密码登录 -->
      <form v-else class="card modal-panel w-full p-8 space-y-5" @submit.prevent="submit">
        <h1 class="text-xl font-semibold">{{ t('auth.signinTitle') }}</h1>
      <div>
        <label class="label" for="email">{{ t('auth.email') }}</label>
        <input id="email" v-model="email" class="input" type="email" required autocomplete="email" />
      </div>
      <div>
        <label class="label" for="password">{{ t('auth.password') }}</label>
        <input id="password" v-model="password" class="input" type="password" required autocomplete="current-password" />
      </div>
      <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
      <button class="btn-primary w-full" :disabled="busy">{{ t('auth.signin') }}</button>

      <template v-if="oauthProviders.length || telegramBot">
        <div class="flex items-center gap-3 text-xs text-muted">
          <span class="h-px flex-1 bg-line"></span>{{ t('auth.or') }}<span class="h-px flex-1 bg-line"></span>
        </div>
        <button
          v-for="p in oauthProviders"
          :key="p"
          type="button"
          class="btn-ghost w-full"
          @click="oauthLogin(p)"
        >
          {{ t('auth.continueWith', { provider: providerLabel(p) }) }}
        </button>
        <!-- Telegram 登录组件（脚本动态注入；无 email，用合成身份） -->
        <div v-if="telegramBot" ref="telegramHost" class="flex justify-center"></div>
      </template>

        <p class="text-sm text-muted text-center">
          {{ t('auth.noAccount') }}
          <router-link class="text-accent hover:underline" to="/register">{{ t('auth.signup') }}</router-link>
        </p>
      </form>
    </div>
  </div>
</template>
