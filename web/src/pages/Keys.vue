<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, ApiError } from '@/lib/api'

interface KeyRow {
  id: number
  prefix: string
  name: string
  status: number
  expires_at: string | null
}

const { t } = useI18n()
const keys = ref<KeyRow[]>([])
const loading = ref(true)
const error = ref('')

const showCreate = ref(false)
const newName = ref('')
const creating = ref(false)
// 明文只显示一次（红线 8 的 UI 面）：弹窗关闭即不可再取
const createdKey = ref('')
const copied = ref(false)

async function load() {
  loading.value = true
  try {
    const r = await api<{ keys: KeyRow[] }>('GET', '/api/keys')
    keys.value = r.keys
    error.value = ''
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : t('common.error')
  } finally {
    loading.value = false
  }
}

async function create() {
  creating.value = true
  try {
    const r = await api<{ key: string }>('POST', '/api/keys', { name: newName.value })
    createdKey.value = r.key
    showCreate.value = false
    newName.value = ''
    await load()
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : t('common.error')
  } finally {
    creating.value = false
  }
}

async function copyKey() {
  await navigator.clipboard.writeText(createdKey.value)
  copied.value = true
  setTimeout(() => (copied.value = false), 1500)
}

async function revoke(id: number) {
  if (!confirm(t('keys.revokeConfirm'))) return
  await api('DELETE', `/api/keys/${id}`)
  await load()
}

onMounted(load)
</script>

<template>
  <div class="max-w-3xl space-y-6">
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-semibold">{{ t('keys.title') }}</h1>
      <button class="btn-primary" @click="showCreate = true">{{ t('keys.create') }}</button>
    </div>

    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <p v-if="loading" class="text-sm text-muted">{{ t('common.loading') }}</p>

    <div v-else class="card overflow-hidden">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3 font-medium">{{ t('keys.name') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('keys.prefix') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('keys.status') }}</th>
            <th class="px-4 py-3"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="keys.length === 0">
            <td colspan="4" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
          </tr>
          <tr v-for="k in keys" :key="k.id" class="border-b border-line last:border-0">
            <td class="px-4 py-3">{{ k.name || '—' }}</td>
            <td class="px-4 py-3 font-mono text-muted">{{ k.prefix }}…</td>
            <td class="px-4 py-3">
              <span :class="k.status === 1 ? 'text-ok' : 'text-muted'">
                {{ k.status === 1 ? t('keys.active') : t('keys.revoked') }}
              </span>
            </td>
            <td class="px-4 py-3 text-right">
              <button v-if="k.status === 1" class="btn-danger !py-1 text-xs" @click="revoke(k.id)">
                {{ t('keys.revoke') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 创建弹窗 -->
    <div v-if="showCreate" class="fixed inset-0 z-50 grid place-items-center bg-black/60 p-4" @click.self="showCreate = false">
      <form class="card w-full max-w-sm p-6 space-y-4" @submit.prevent="create">
        <h2 class="font-semibold">{{ t('keys.create') }}</h2>
        <div>
          <label class="label" for="keyname">{{ t('keys.name') }}</label>
          <input id="keyname" v-model="newName" class="input" :placeholder="t('keys.namePlaceholder')" />
        </div>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn-ghost" @click="showCreate = false">{{ t('common.cancel') }}</button>
          <button class="btn-primary" :disabled="creating">{{ t('common.confirm') }}</button>
        </div>
      </form>
    </div>

    <!-- 明文一次性展示 -->
    <div v-if="createdKey" class="fixed inset-0 z-50 grid place-items-center bg-black/60 p-4">
      <div class="card w-full max-w-lg p-6 space-y-4">
        <h2 class="font-semibold">{{ t('keys.createdTitle') }}</h2>
        <p class="text-sm text-danger">{{ t('keys.createdWarning') }}</p>
        <div class="flex gap-2">
          <code class="input font-mono text-xs break-all select-all">{{ createdKey }}</code>
          <button class="btn-ghost shrink-0" @click="copyKey">
            {{ copied ? t('common.copied') : t('common.copy') }}
          </button>
        </div>
        <div class="flex justify-end">
          <button class="btn-primary" @click="createdKey = ''">{{ t('common.close') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>
