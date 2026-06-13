<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'
import DailyBarChart, { type SummaryDay } from '@/components/DailyBarChart.vue'

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
  usage_source?: string
  frt_ms: number
  duration_ms: number
  error_brief?: string
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
const expanded = ref<number | null>(null)
function toggle(id: number) {
  expanded.value = expanded.value === id ? null : id
}

// —— 近 14 天日聚合（仪表盘）——
const summary = ref<SummaryDay[]>([])
const summaryDays = 14
const totals = computed(() => {
  let calls = 0
  let tokens = 0
  let cost = 0
  for (const s of summary.value) {
    calls += s.calls
    tokens += s.prompt_tokens + s.completion_tokens
    cost += Number(s.cost_usd)
  }
  return { calls, tokens, cost }
})
async function loadSummary() {
  try {
    const r = await api<{ items: SummaryDay[] }>('GET', `/api/usage/summary?days=${summaryDays}`)
    // day 统一为 YYYY-MM-DD（不同驱动对 DATE() 的序列化可能带时间后缀）
    summary.value = r.items.map((s) => ({ ...s, day: s.day.slice(0, 10) }))
  } catch {
    // 仪表盘失败不阻塞明细表（明细自身有错误展示）
  }
}

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
onMounted(() => {
  void load()
  void loadSummary()
})
// 防抖定时器在卸载时清理：避免组件销毁后 setTimeout 仍触发 load（陈旧请求 + 内存泄漏）
onBeforeUnmount(() => clearTimeout(filterTimer))
</script>

<template>
  <div class="space-y-6">
    <div class="flex items-center justify-between gap-4">
      <h1 class="text-2xl font-semibold tracking-tight">{{ t('usage.title') }}</h1>
      <input v-model="model" class="input max-w-xs" :placeholder="t('usage.filterModel')" />
    </div>

    <!-- 近 14 天概览 -->
    <div class="grid grid-cols-3 gap-4">
      <div class="card p-4">
        <p class="text-xs text-muted">{{ t('usage.sumCalls', { days: summaryDays }) }}</p>
        <p class="mt-1 text-2xl font-semibold font-mono">{{ n(totals.calls) }}</p>
      </div>
      <div class="card p-4">
        <p class="text-xs text-muted">{{ t('usage.sumTokens', { days: summaryDays }) }}</p>
        <p class="mt-1 text-2xl font-semibold font-mono">{{ n(totals.tokens) }}</p>
      </div>
      <div class="card p-4">
        <p class="text-xs text-muted">{{ t('usage.sumCost', { days: summaryDays }) }}</p>
        <p class="mt-1 text-2xl font-semibold font-mono">${{ totals.cost.toFixed(4) }}</p>
      </div>
    </div>

    <!-- 日调用量柱状图 -->
    <DailyBarChart :items="summary" :days="summaryDays" />

    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <p v-if="loading" class="text-sm text-muted">{{ t('common.loading') }}</p>

    <div v-else class="card overflow-x-auto">
      <table class="table w-full text-sm whitespace-nowrap">
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
          <template v-for="r in rows" :key="r.id">
            <tr class="cursor-pointer border-b border-line last:border-0" @click="toggle(r.id)">
              <td class="px-4 py-2.5 text-muted">{{ d(new Date(r.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
              <td class="px-4 py-2.5 font-mono">{{ r.model }}</td>
              <td class="px-4 py-2.5">
                <span :class="r.status === 'success' ? 'badge-ok' : 'badge-danger'">{{ r.status }}</span>
              </td>
              <td class="px-4 py-2.5 text-right font-mono">
                {{ n(r.prompt_tokens) }} / {{ n(r.completion_tokens) }}
              </td>
              <td class="px-4 py-2.5 text-right font-mono">{{ r.cost_usd }}</td>
              <td class="px-4 py-2.5 font-mono text-muted select-all">{{ r.request_id }}</td>
            </tr>
            <!-- 调用详情展开 -->
            <tr v-if="expanded === r.id" class="!border-0 bg-elev/40">
              <td colspan="6" class="px-4 pb-4 pt-1">
                <dl class="grid grid-cols-2 gap-x-8 gap-y-1.5 text-xs sm:grid-cols-4">
                  <div><dt class="text-muted">{{ t('usage.dStatusCode') }}</dt><dd class="font-mono">{{ r.status_code }}</dd></div>
                  <div><dt class="text-muted">{{ t('usage.dStream') }}</dt><dd class="font-mono">{{ r.is_stream ? t('common.yes') : t('common.no') }}</dd></div>
                  <div><dt class="text-muted">{{ t('usage.dFrt') }}</dt><dd class="font-mono">{{ r.frt_ms }} ms</dd></div>
                  <div><dt class="text-muted">{{ t('usage.dDuration') }}</dt><dd class="font-mono">{{ r.duration_ms }} ms</dd></div>
                  <div v-if="r.usage_source"><dt class="text-muted">{{ t('usage.dUsageSource') }}</dt><dd class="font-mono">{{ r.usage_source }}</dd></div>
                  <div v-if="r.error_brief" class="col-span-2 sm:col-span-4">
                    <dt class="text-muted">{{ t('usage.dError') }}</dt>
                    <dd class="font-mono text-danger break-all">{{ r.error_brief }}</dd>
                  </div>
                </dl>
              </td>
            </tr>
          </template>
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
