<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter, useRoute } from 'vue-router'
import { useAuth } from '@/stores/auth'
import { ApiError } from '@/lib/api'

const { t } = useI18n()
const auth = useAuth()
const router = useRouter()
const route = useRoute()

const email = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')

async function submit() {
  busy.value = true
  error.value = ''
  try {
    await auth.login(email.value, password.value)
    await router.replace(String(route.query.to ?? '/'))
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : t('common.error')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="min-h-screen grid place-items-center p-6">
    <form class="card w-full max-w-sm p-8 space-y-5" @submit.prevent="submit">
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
      <p class="text-sm text-muted text-center">
        {{ t('auth.noAccount') }}
        <router-link class="text-accent hover:underline" to="/register">{{ t('auth.signup') }}</router-link>
      </p>
    </form>
  </div>
</template>
