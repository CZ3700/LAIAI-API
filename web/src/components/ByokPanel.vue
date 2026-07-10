<script setup lang="ts">
// BYOK 自带上游 Key（0044）：org 自助管理自有上游渠道。命中自有渠道时平台只收路由费。
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'

interface ByokChannel {
  id: number
  name: string
  provider: string
  base_url: string
  models: string
  status: number
}
// 白名单与后端 relay.byokProviders 对齐（azure 需手填 base_url）
const PROVIDERS = ['openai', 'anthropic', 'gemini', 'azure', 'groq', 'mistral', 'xai', 'deepseek', 'moonshot', 'zhipu', 'dashscope-intl', 'minimax', 'siliconflow', 'volcengine']

const { t } = useI18n()
const channels = ref<ByokChannel[]>([])
const max = ref(10)
const loading = ref(true)
const error = ref('')
const showCreate = ref(false)
const busy = ref(false)

const form = ref({ provider: 'openai', api_key: '', base_url: '', models: '', name: '' })

async function load() {
  loading.value = true
  try {
    const r = await api<{ channels: ByokChannel[]; max: number }>('GET', '/api/org/byok-channels')
    channels.value = r.channels
    max.value = r.max
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    loading.value = false
  }
}

async function create() {
  busy.value = true
  error.value = ''
  try {
    await api('POST', '/api/org/byok-channels', {
      provider: form.value.provider,
      api_key: form.value.api_key.trim(),
      base_url: form.value.base_url.trim() || undefined,
      models: form.value.models.split(',').map((m) => m.trim()).filter(Boolean),
      name: form.value.name.trim() || undefined,
    })
    showCreate.value = false
    form.value = { provider: 'openai', api_key: '', base_url: '', models: '', name: '' }
    await load()
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    busy.value = false
  }
}

async function toggle(ch: ByokChannel) {
  try {
    await api('PATCH', `/api/org/byok-channels/${ch.id}`, { status: ch.status === 1 ? 0 : 1 })
    await load()
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  }
}

async function remove(ch: ByokChannel) {
  if (!confirm(t('byok.confirmDelete', { name: ch.name }))) return
  try {
    await api('DELETE', `/api/org/byok-channels/${ch.id}`)
    await load()
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  }
}

onMounted(load)
</script>

<template>
  <section class="mt-8">
    <div class="mb-3 flex flex-wrap items-end justify-between gap-3">
      <div>
        <h2 class="font-semibold">{{ t('byok.title') }}</h2>
        <p class="mt-1 text-sm text-muted">{{ t('byok.desc') }}</p>
      </div>
      <button class="btn-primary shrink-0" :disabled="channels.length >= max" @click="showCreate = true">
        {{ t('byok.add') }}
      </button>
    </div>

    <p v-if="error" class="mb-3 text-sm text-danger" role="alert">{{ error }}</p>
    <p v-if="loading" class="text-sm text-muted">{{ t('common.loading') }}</p>

    <div v-else-if="channels.length === 0" class="app-panel p-6 text-center text-sm text-muted">
      {{ t('byok.empty') }}
    </div>

    <div v-else class="data-table-wrap">
      <table class="table w-full text-sm whitespace-nowrap">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3 font-medium">{{ t('byok.colName') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('byok.colProvider') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('byok.colModels') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('byok.colStatus') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('byok.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="ch in channels" :key="ch.id" class="border-b border-line last:border-0">
            <td class="px-4 py-2.5">{{ ch.name }}</td>
            <td class="px-4 py-2.5 text-muted">{{ ch.provider }}</td>
            <td class="px-4 py-2.5 font-mono text-xs">{{ ch.models }}</td>
            <td class="px-4 py-2.5">
              <span class="rounded px-1.5 py-0.5 text-xs" :class="ch.status === 1 ? 'bg-ok/15 text-ok' : 'bg-elev text-muted'">
                {{ ch.status === 1 ? t('byok.active') : t('byok.disabled') }}
              </span>
            </td>
            <td class="px-4 py-2.5 text-right">
              <button class="btn-ghost !min-h-0 !px-2 !py-1 text-xs" @click="toggle(ch)">
                {{ ch.status === 1 ? t('byok.disable') : t('byok.enable') }}
              </button>
              <button class="btn-ghost !min-h-0 !px-2 !py-1 text-xs text-danger" @click="remove(ch)">{{ t('common.delete') }}</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 创建弹窗 -->
    <div v-if="showCreate" class="modal-backdrop" @click.self="showCreate = false">
      <form class="app-panel modal-panel w-full max-w-lg p-6 space-y-4" @submit.prevent="create">
        <h3 class="font-semibold">{{ t('byok.add') }}</h3>
        <p class="text-xs text-muted">{{ t('byok.feeHint') }}</p>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="label" for="byok-provider">{{ t('byok.colProvider') }}</label>
            <select id="byok-provider" v-model="form.provider" class="input">
              <option v-for="p in PROVIDERS" :key="p" :value="p">{{ p }}</option>
            </select>
          </div>
          <div>
            <label class="label" for="byok-name">{{ t('byok.colName') }}</label>
            <input id="byok-name" v-model="form.name" class="input" :placeholder="`byok-${form.provider}`" />
          </div>
        </div>
        <div>
          <label class="label" for="byok-key">{{ t('byok.apiKey') }}</label>
          <input id="byok-key" v-model="form.api_key" class="input font-mono text-xs" type="password" autocomplete="off" placeholder="sk-..." required />
        </div>
        <div>
          <label class="label" for="byok-base">{{ t('byok.baseUrl') }}</label>
          <input id="byok-base" v-model="form.base_url" class="input font-mono text-xs" placeholder="https://… (azure 必填，其余留空用默认)" />
        </div>
        <div>
          <label class="label" for="byok-models">{{ t('byok.colModels') }}</label>
          <input id="byok-models" v-model="form.models" class="input font-mono text-xs" placeholder="gpt-5.2, claude-4" required />
        </div>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn-ghost" @click="showCreate = false">{{ t('common.cancel') }}</button>
          <button class="btn-primary" :disabled="busy">{{ t('common.confirm') }}</button>
        </div>
      </form>
    </div>
  </section>
</template>
