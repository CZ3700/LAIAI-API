<script setup lang="ts">
// Key 检测工具站（§4.8）：提交自己的厂商 Key → 检测有效性/延迟/可用模型。
// Key 只提交给本平台后端做一次同步探测，后端全程只在内存用、绝不落库/记日志（红线 7）；
// 前端 input type=password、不持久化。
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'

// 与后端 relay.byokProviders 白名单对齐
const PROVIDERS = ['openai', 'anthropic', 'gemini', 'azure', 'groq', 'mistral', 'xai', 'deepseek', 'moonshot', 'zhipu', 'dashscope-intl', 'minimax', 'siliconflow', 'volcengine']

interface DiagResult {
  ok: boolean
  status: string
  base_url: string
  latency_ms: number
  model_count: number
  models_sample: string[]
  gateway: string
  message: string
}

const { t } = useI18n()
const provider = ref('openai')
const apiKey = ref('')
const baseUrl = ref('')
const busy = ref(false)
const error = ref('')
const result = ref<DiagResult | null>(null)

const STATUS_CLASS: Record<string, string> = {
  ok: 'bg-ok/15 text-ok',
  invalid_key: 'bg-danger/15 text-danger',
  forbidden: 'bg-danger/15 text-danger',
  unreachable: 'bg-amber-500/15 text-amber-500',
  error: 'bg-elev text-muted',
}

async function run() {
  if (!apiKey.value.trim()) return
  busy.value = true
  error.value = ''
  result.value = null
  try {
    result.value = await api<DiagResult>('POST', '/api/diagnostics/check', {
      provider: provider.value,
      api_key: apiKey.value.trim(),
      base_url: baseUrl.value.trim() || undefined,
    })
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('surface.diagEyebrow') }}</p>
        <h1 class="app-page-title">{{ t('diag.title') }}</h1>
        <p class="app-page-subtitle">{{ t('diag.subtitle') }}</p>
      </div>
    </div>

    <div class="grid gap-5 lg:grid-cols-[24rem_1fr]">
      <!-- 表单 -->
      <form class="app-panel h-fit p-5 space-y-4" @submit.prevent="run">
        <div>
          <label class="label" for="diag-provider">{{ t('diag.provider') }}</label>
          <select id="diag-provider" v-model="provider" class="input">
            <option v-for="p in PROVIDERS" :key="p" :value="p">{{ p }}</option>
          </select>
        </div>
        <div>
          <label class="label" for="diag-key">{{ t('diag.apiKey') }}</label>
          <input id="diag-key" v-model="apiKey" class="input font-mono text-xs" type="password" autocomplete="off" placeholder="sk-..." required />
        </div>
        <div>
          <label class="label" for="diag-base">{{ t('diag.baseUrl') }}</label>
          <input id="diag-base" v-model="baseUrl" class="input font-mono text-xs" placeholder="https://… (optional)" />
        </div>
        <p class="text-xs text-muted">{{ t('diag.privacyNote') }}</p>
        <button class="btn-primary w-full" :disabled="busy || !apiKey.trim()">
          {{ busy ? t('diag.checking') : t('diag.check') }}
        </button>
      </form>

      <!-- 结果 -->
      <div class="app-panel min-h-[20rem] p-5">
        <p v-if="error" class="inline-alert" role="alert">{{ error }}</p>

        <div v-if="busy" class="grid h-full place-items-center text-muted">
          <span class="inline-flex gap-1 py-1">
            <span class="h-2 w-2 animate-bounce rounded-full bg-muted" style="animation-delay: 0ms"></span>
            <span class="h-2 w-2 animate-bounce rounded-full bg-muted" style="animation-delay: 150ms"></span>
            <span class="h-2 w-2 animate-bounce rounded-full bg-muted" style="animation-delay: 300ms"></span>
          </span>
        </div>

        <div v-else-if="result" class="space-y-5">
          <div class="flex flex-wrap items-center gap-3">
            <span class="rounded px-2 py-1 text-sm font-medium" :class="STATUS_CLASS[result.status] || 'bg-elev text-muted'">
              {{ t(`diag.status_${result.status}`) }}
            </span>
            <span v-if="result.ok" class="text-sm text-muted">{{ result.message }}</span>
          </div>
          <p v-if="!result.ok" class="text-sm text-muted">{{ result.message }}</p>

          <div v-if="result.ok" class="grid grid-cols-2 gap-px overflow-hidden rounded-lg bg-line text-sm sm:grid-cols-4">
            <div class="bg-panel p-4">
              <p class="text-xs text-muted">{{ t('diag.latency') }}</p>
              <p class="mt-1 font-mono text-xl font-semibold">{{ result.latency_ms }}ms</p>
            </div>
            <div class="bg-panel p-4">
              <p class="text-xs text-muted">{{ t('diag.models') }}</p>
              <p class="mt-1 font-mono text-xl font-semibold">{{ result.model_count }}</p>
            </div>
            <div class="bg-panel p-4">
              <p class="text-xs text-muted">{{ t('diag.gateway') }}</p>
              <p class="mt-1 truncate font-mono text-sm" :title="result.gateway">{{ result.gateway || '—' }}</p>
            </div>
            <div class="bg-panel p-4">
              <p class="text-xs text-muted">{{ t('diag.endpoint') }}</p>
              <p class="mt-1 truncate font-mono text-xs" :title="result.base_url">{{ result.base_url }}</p>
            </div>
          </div>

          <div v-if="result.models_sample && result.models_sample.length">
            <p class="mb-2 text-xs text-muted">{{ t('diag.modelsSample') }}</p>
            <div class="flex flex-wrap gap-1.5">
              <span v-for="m in result.models_sample" :key="m" class="badge-muted font-mono text-xs">{{ m }}</span>
            </div>
          </div>
        </div>

        <div v-else class="grid h-full place-items-center text-center text-muted">
          <p class="max-w-sm text-sm">{{ t('diag.emptyHint') }}</p>
        </div>
      </div>
    </div>
  </div>
</template>
