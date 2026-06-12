<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'

interface UsageRow {
  id: number
  request_id: string
  model: string
  status: string
  status_code: number
  is_stream: boolean
  prompt_tokens: number
  completion_tokens: number
  cost_usd: string
  frt_ms: number
  duration_ms: number
  created_at: string
}

const { t, n, d } = useI18n()
const rows = ref<UsageRow[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const model = ref('')
const loading = ref(true)
const error = ref('')

// 请求序号守卫：分页/过滤快速切换时，慢的旧响应不得覆盖新响应
let reqSeq = 0
async function load() {
  const seq = ++reqSeq
  loading.value = true
  try {
    const q = new URLSearchParams({ page: String(page.value), page_size: String(pageSize) })
    if (model.value) q.set('model', model.value)
    const r = await api<{ total: number; items: UsageRow[] }>('GET', `/api/usage?${q}`)
    if (seq !== reqSeq) return // 已有更新的请求在途，丢弃本次结果
    rows.value = r.items
    total.value = r.total
    error.value = ''
  } catch (e) {
    if (seq !== reqSeq) return
    error.value = errorMessage(e, t('common.error'))
  } finally {
    if (seq === reqSeq) loading.value = false
  }
}

let filterTimer: ReturnType<typeof setTimeout> | undefined
watch(model, () => {
  clearTimeout(filterTimer)
  filterTimer = setTimeout(() => {
    page.value = 1
    void load()
  }, 300)
})
watch(page, () => void load())
onMounted(load)
// 防抖定时器在卸载时清理：避免组件销毁后 setTimeout 仍触发 load（陈旧请求 + 内存泄漏）
onBeforeUnmount(() => clearTimeout(filterTimer))
</script>

<template>
  <div class="space-y-6">
    <div class="flex items-center justify-between gap-4">
      <h1 class="text-xl font-semibold">{{ t('usage.title') }}</h1>
      <input v-model="model" class="input max-w-xs" :placeholder="t('usage.filterModel')" />
    </div>

    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <p v-if="loading" class="text-sm text-muted">{{ t('common.loading') }}</p>

    <div v-else class="card overflow-x-auto">
      <table class="w-full text-sm whitespace-nowrap">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3 font-medium">{{ t('usage.time') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('usage.model') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('usage.status') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('usage.tokens') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('usage.cost') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('usage.requestId') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="rows.length === 0">
            <td colspan="6" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
          </tr>
          <tr v-for="r in rows" :key="r.id" class="border-b border-line last:border-0">
            <td class="px-4 py-2.5 text-muted">{{ d(new Date(r.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
            <td class="px-4 py-2.5 font-mono">{{ r.model }}</td>
            <td class="px-4 py-2.5">
              <span :class="r.status === 'success' ? 'text-ok' : 'text-danger'">{{ r.status }}</span>
            </td>
            <td class="px-4 py-2.5 text-right font-mono">
              {{ n(r.prompt_tokens) }} / {{ n(r.completion_tokens) }}
            </td>
            <td class="px-4 py-2.5 text-right font-mono">{{ r.cost_usd }}</td>
            <td class="px-4 py-2.5 font-mono text-muted select-all">{{ r.request_id }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="flex items-center justify-between text-sm text-muted">
      <span>{{ t('usage.total', { total }) }}</span>
      <div class="flex items-center gap-2">
        <button class="btn-ghost !py-1" :disabled="page <= 1" @click="page--">{{ t('common.prev') }}</button>
        <span>{{ t('common.page', { page }) }}</span>
        <button class="btn-ghost !py-1" :disabled="page * pageSize >= total" @click="page++">{{ t('common.next') }}</button>
      </div>
    </div>
  </div>
</template>
