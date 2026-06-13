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
const inviterCode = String(route.query.ref ?? '')

const email = ref('')
const password = ref('')
const code = ref('')
const busy = ref(false)
const error = ref('')

// 是否要求邮箱验证（运营开关）
const needVerify = ref(false)
const sending = ref(false)
const sentMsg = ref('')
onMounted(async () => {
  try {
    const cfg = await api<{ email_verification_enabled: boolean }>('GET', '/api/config', undefined, { auth: false })
    needVerify.value = cfg.email_verification_enabled
  } catch {
    // 拉不到 config 当作不需验证
  }
})

async function sendCode() {
  if (!email.value) {
    error.value = t('auth.emailFirst')
    return
  }
  sending.value = true
  sentMsg.value = ''
  error.value = ''
  try {
    await api('POST', '/api/auth/send-verification', { email: email.value }, { auth: false })
    sentMsg.value = t('auth.codeSent')
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    sending.value = false
  }
}

async function submit() {
  busy.value = true
  error.value = ''
  try {
    await auth.register(email.value, password.value, needVerify.value ? code.value : undefined, inviterCode || undefined)
    await router.replace('/app')
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
      <form class="card modal-panel w-full p-8 space-y-5" @submit.prevent="submit">
        <h1 class="text-xl font-semibold">{{ t('auth.signupTitle') }}</h1>
      <div>
        <label class="label" for="email">{{ t('auth.email') }}</label>
        <input id="email" v-model="email" class="input" type="email" required autocomplete="email" />
      </div>
      <div>
        <label class="label" for="password">{{ t('auth.password') }}</label>
        <input
          id="password" v-model="password" class="input" type="password"
          required minlength="8" autocomplete="new-password" :placeholder="t('auth.passwordHint')"
        />
      </div>
      <div v-if="needVerify">
        <label class="label" for="code">{{ t('auth.verifyCode') }}</label>
        <div class="flex gap-2">
          <input id="code" v-model="code" class="input font-mono" inputmode="numeric" maxlength="6" required />
          <button type="button" class="btn-ghost shrink-0" :disabled="sending" @click="sendCode">
            {{ sending ? t('auth.sending') : t('auth.sendCode') }}
          </button>
        </div>
        <p v-if="sentMsg" class="mt-1 text-xs text-ok">{{ sentMsg }}</p>
      </div>
      <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
      <button class="btn-primary w-full" :disabled="busy">{{ t('auth.signup') }}</button>
        <p class="text-sm text-muted text-center">
          {{ t('auth.haveAccount') }}
          <router-link class="text-accent hover:underline" to="/login">{{ t('auth.signin') }}</router-link>
        </p>
      </form>
    </div>
  </div>
</template>
