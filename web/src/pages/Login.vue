<script setup lang="ts">
import { onMounted, ref } from 'vue'
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
onMounted(async () => {
  try {
    const r = await api<{ providers: string[] }>('GET', '/api/auth/oauth', undefined, { auth: false })
    oauthProviders.value = r.providers ?? []
  } catch {
    // 无 OAuth 不影响密码登录
  }
})
function oauthLogin(provider: string) {
  window.location.href = `/api/auth/oauth/${provider}/start`
}
function providerLabel(p: string): string {
  return p === 'github' ? 'GitHub' : p === 'google' ? 'Google' : p
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

      <template v-if="oauthProviders.length">
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
      </template>

        <p class="text-sm text-muted text-center">
          {{ t('auth.noAccount') }}
          <router-link class="text-accent hover:underline" to="/register">{{ t('auth.signup') }}</router-link>
        </p>
      </form>
    </div>
  </div>
</template>
