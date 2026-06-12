<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'

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
const copyError = ref(false)
const revoking = ref<number | null>(null)

// —— 弹窗焦点管理 + Escape（无障碍：role=dialog 焦点陷阱 + 关闭即恢复焦点）——
const createDialog = ref<HTMLElement | null>(null)
const createdDialog = ref<HTMLElement | null>(null)
let lastFocused: HTMLElement | null = null
let copyResetTimer: ReturnType<typeof setTimeout> | undefined

function focusDialog(el: HTMLElement | null) {
  const focusable = el?.querySelector<HTMLElement>(
    'input, button, [tabindex]:not([tabindex="-1"])',
  )
  focusable?.focus()
}

watch([showCreate, createdKey], async ([create, created]) => {
  const open = create || created !== ''
  if (open) {
    lastFocused = document.activeElement as HTMLElement
    await nextTick()
    focusDialog(create ? createDialog.value : createdDialog.value)
  } else {
    lastFocused?.focus()
    lastFocused = null
  }
})

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    if (createdKey.value) createdKey.value = ''
    else if (showCreate.value) showCreate.value = false
    return
  }
  // 焦点陷阱：Tab 在弹窗内循环
  if (e.key !== 'Tab') return
  const dialog = createdKey.value ? createdDialog.value : showCreate.value ? createDialog.value : null
  if (!dialog) return
  const nodes = dialog.querySelectorAll<HTMLElement>(
    'a[href], button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])',
  )
  if (nodes.length === 0) return
  const first = nodes[0]
  const last = nodes[nodes.length - 1]
  if (e.shiftKey && document.activeElement === first) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && document.activeElement === last) {
    e.preventDefault()
    first.focus()
  }
}
onMounted(() => document.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
  clearTimeout(copyResetTimer)
})

async function load() {
  loading.value = true
  try {
    const r = await api<{ keys: KeyRow[] }>('GET', '/api/keys')
    keys.value = r.keys
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
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
    error.value = errorMessage(e, t('common.error'))
  } finally {
    creating.value = false
  }
}

async function copyKey() {
  copyError.value = false
  try {
    await navigator.clipboard.writeText(createdKey.value)
    copied.value = true
    clearTimeout(copyResetTimer)
    copyResetTimer = setTimeout(() => (copied.value = false), 1500)
  } catch {
    // clipboard 在非安全上下文 / 权限拒绝时会 reject —— 提示用户手动复制
    copyError.value = true
  }
}

async function revoke(id: number) {
  if (!confirm(t('keys.revokeConfirm'))) return
  revoking.value = id
  try {
    await api('DELETE', `/api/keys/${id}`)
    await load()
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    revoking.value = null
  }
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
              <button
                v-if="k.status === 1"
                class="btn-danger !py-1 text-xs"
                :disabled="revoking === k.id"
                @click="revoke(k.id)"
              >
                {{ t('keys.revoke') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 创建弹窗 -->
    <div v-if="showCreate" class="fixed inset-0 z-50 grid place-items-center bg-black/60 p-4" @click.self="showCreate = false">
      <form
        ref="createDialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="create-key-title"
        class="card w-full max-w-sm p-6 space-y-4"
        @submit.prevent="create"
      >
        <h2 id="create-key-title" class="font-semibold">{{ t('keys.create') }}</h2>
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
      <div
        ref="createdDialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="created-key-title"
        class="card w-full max-w-lg p-6 space-y-4"
      >
        <h2 id="created-key-title" class="font-semibold">{{ t('keys.createdTitle') }}</h2>
        <p class="text-sm text-danger">{{ t('keys.createdWarning') }}</p>
        <div class="flex gap-2">
          <code class="input font-mono text-xs break-all select-all">{{ createdKey }}</code>
          <button class="btn-ghost shrink-0" @click="copyKey">
            {{ copied ? t('common.copied') : t('common.copy') }}
          </button>
        </div>
        <p v-if="copyError" class="text-xs text-danger" role="alert">{{ t('common.copyFailed') }}</p>
        <div class="flex justify-end">
          <button class="btn-primary" @click="createdKey = ''">{{ t('common.close') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>
