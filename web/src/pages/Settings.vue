<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'

interface Me {
  user_id: number
  email: string
  display_name: string
  org_id: number
}

const { t } = useI18n()
const me = ref<Me | null>(null)

// 显示名
const displayName = ref('')
const profileBusy = ref(false)
const profileMsg = ref('')
const profileErr = ref('')

// 密码
const oldPw = ref('')
const newPw = ref('')
const newPw2 = ref('')
const pwBusy = ref(false)
const pwMsg = ref('')
const pwErr = ref('')

onMounted(async () => {
  try {
    const r = await api<Me>('GET', '/api/me')
    me.value = r
    displayName.value = r.display_name
  } catch (e) {
    profileErr.value = errorMessage(e, t('common.error'))
  }
})

async function saveProfile() {
  profileBusy.value = true
  profileMsg.value = ''
  profileErr.value = ''
  try {
    await api('PATCH', '/api/me', { display_name: displayName.value })
    profileMsg.value = t('settings.saved')
  } catch (e) {
    profileErr.value = errorMessage(e, t('common.error'))
  } finally {
    profileBusy.value = false
  }
}

async function changePassword() {
  pwMsg.value = ''
  pwErr.value = ''
  if (newPw.value !== newPw2.value) {
    pwErr.value = t('settings.pwMismatch')
    return
  }
  pwBusy.value = true
  try {
    await api('POST', '/api/me/password', { old_password: oldPw.value, new_password: newPw.value })
    pwMsg.value = t('settings.pwChanged')
    oldPw.value = newPw.value = newPw2.value = ''
  } catch (e) {
    pwErr.value = errorMessage(e, t('common.error'))
  } finally {
    pwBusy.value = false
  }
}
</script>

<template>
  <div class="max-w-2xl space-y-6">
    <h1 class="text-2xl font-semibold tracking-tight">{{ t('settings.title') }}</h1>

    <!-- 账户信息 -->
    <div class="card p-6">
      <h2 class="mb-4 font-semibold">{{ t('settings.account') }}</h2>
      <dl class="grid grid-cols-2 gap-y-3 text-sm">
        <dt class="text-muted">{{ t('auth.email') }}</dt>
        <dd class="font-mono">{{ me?.email }}</dd>
        <dt class="text-muted">{{ t('settings.userId') }}</dt>
        <dd class="font-mono">{{ me?.user_id }}</dd>
        <dt class="text-muted">{{ t('settings.orgId') }}</dt>
        <dd class="font-mono">{{ me?.org_id }}</dd>
      </dl>
    </div>

    <!-- 显示名 -->
    <form class="card p-6 space-y-4" @submit.prevent="saveProfile">
      <h2 class="font-semibold">{{ t('settings.profile') }}</h2>
      <div>
        <label class="label" for="dn">{{ t('settings.displayName') }}</label>
        <input id="dn" v-model="displayName" class="input max-w-sm" :placeholder="t('settings.displayNamePlaceholder')" />
      </div>
      <p v-if="profileMsg" class="text-sm text-ok" role="status">{{ profileMsg }}</p>
      <p v-if="profileErr" class="text-sm text-danger" role="alert">{{ profileErr }}</p>
      <button class="btn-primary" :disabled="profileBusy">{{ t('settings.save') }}</button>
    </form>

    <!-- 改密码 -->
    <form class="card p-6 space-y-4" @submit.prevent="changePassword">
      <h2 class="font-semibold">{{ t('settings.password') }}</h2>
      <div>
        <label class="label" for="op">{{ t('settings.currentPassword') }}</label>
        <input id="op" v-model="oldPw" class="input max-w-sm" type="password" required autocomplete="current-password" />
      </div>
      <div>
        <label class="label" for="np">{{ t('settings.newPassword') }}</label>
        <input id="np" v-model="newPw" class="input max-w-sm" type="password" required minlength="8" autocomplete="new-password" />
      </div>
      <div>
        <label class="label" for="np2">{{ t('settings.confirmPassword') }}</label>
        <input id="np2" v-model="newPw2" class="input max-w-sm" type="password" required minlength="8" autocomplete="new-password" />
      </div>
      <p v-if="pwMsg" class="text-sm text-ok" role="status">{{ pwMsg }}</p>
      <p v-if="pwErr" class="text-sm text-danger" role="alert">{{ pwErr }}</p>
      <button class="btn-primary" :disabled="pwBusy">{{ t('settings.changePassword') }}</button>
    </form>
  </div>
</template>
