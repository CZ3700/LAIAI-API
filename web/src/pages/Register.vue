<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useAuth } from '@/stores/auth'
import { errorMessage } from '@/lib/api'
import ThemeLocale from '@/components/ThemeLocale.vue'

const { t } = useI18n()
const auth = useAuth()
const router = useRouter()

const email = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')

async function submit() {
  busy.value = true
  error.value = ''
  try {
    await auth.register(email.value, password.value)
    await router.replace('/')
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="min-h-screen grid place-items-center p-6">
    <div class="fixed top-4 right-4"><ThemeLocale /></div>
    <form class="card w-full max-w-sm p-8 space-y-5" @submit.prevent="submit">
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
      <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
      <button class="btn-primary w-full" :disabled="busy">{{ t('auth.signup') }}</button>
      <p class="text-sm text-muted text-center">
        {{ t('auth.haveAccount') }}
        <router-link class="text-accent hover:underline" to="/login">{{ t('auth.signin') }}</router-link>
      </p>
    </form>
  </div>
</template>
