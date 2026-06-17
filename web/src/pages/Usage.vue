<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'
import DailyBarChart, { type ChartMetric, type SummaryDay } from '@/components/DailyBarChart.vue'

interface ModelStat {
  model: string
  calls: number
  succeeded: number
  prompt_tokens: number
  completion_tokens: number
  cost_usd: string
}
interface KeyRow {
  id: number
  name: string
  prefix: string
  status: number
}

const { n } = useI18n()
const range = ref<7 | 30 | 31 | 60>(30)
const rangeLabel = ref('30 天')
const granularity = ref<'hour' | 'day' | 'month'>('day')
const metric = ref<ChartMetric>('tokens')
const modelFilter = ref('')
const modelSearch = ref('')
const loading = ref(true)
const error = ref('')
const summary = ref<SummaryDay[]>([])
const byModel = ref<ModelStat[]>([])
const keys = ref<KeyRow[]>([])
const sortKey = ref<'model' | 'calls' | 'input' | 'output' | 'tokens' | 'cost'>('tokens')
const sortDir = ref<'asc' | 'desc'>('desc')

const rangeOptions = [
  { label: '7 天', days: 7 as const },
  { label: '30 天', days: 30 as const },
  { label: '31 天', days: 31 as const },
  { label: '60 天', days: 60 as const },
]
const metricOptions: { key: ChartMetric; label: string }[] = [
  { key: 'tokens', label: 'Tokens' },
  { key: 'cost', label: 'Credits' },
  { key: 'calls', label: '请求量' },
]

function num(v: string | number): number {
  const out = Number(v)
  return Number.isFinite(out) ? out : 0
}
function fmtTokens(v: number): string {
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(2)}M`
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}K`
  return n(v)
}
function fmtMoney(v: number): string {
  return `$${v.toFixed(v >= 1 ? 2 : 4)}`
}
function setRange(label: string, days: 7 | 30 | 31 | 60) {
  rangeLabel.value = label
  range.value = days
}
function setSort(key: typeof sortKey.value) {
  if (sortKey.value === key) sortDir.value = sortDir.value === 'desc' ? 'asc' : 'desc'
  else {
    sortKey.value = key
    sortDir.value = 'desc'
  }
}

const filteredModels = computed(() => byModel.value
  .filter((m) => !modelFilter.value || m.model === modelFilter.value)
  .filter((m) => !modelSearch.value || m.model.toLowerCase().includes(modelSearch.value.toLowerCase()))
  .sort((a, b) => {
    const av = sortKey.value === 'model' ? a.model : sortKey.value === 'calls' ? a.calls : sortKey.value === 'input' ? a.prompt_tokens : sortKey.value === 'output' ? a.completion_tokens : sortKey.value === 'cost' ? num(a.cost_usd) : a.prompt_tokens + a.completion_tokens
    const bv = sortKey.value === 'model' ? b.model : sortKey.value === 'calls' ? b.calls : sortKey.value === 'input' ? b.prompt_tokens : sortKey.value === 'output' ? b.completion_tokens : sortKey.value === 'cost' ? num(b.cost_usd) : b.prompt_tokens + b.completion_tokens
    const result = typeof av === 'string' ? av.localeCompare(String(bv)) : av - Number(bv)
    return sortDir.value === 'desc' ? -result : result
  }))
const totals = computed(() => {
  let calls = 0
  let input = 0
  let output = 0
  let cost = 0
  for (const m of filteredModels.value) {
    calls += m.calls
    input += m.prompt_tokens
    output += m.completion_tokens
    cost += num(m.cost_usd)
  }
  return { calls, input, output, tokens: input + output, cost }
})
const maxModelTokens = computed(() => Math.max(1, ...filteredModels.value.map((m) => m.prompt_tokens + m.completion_tokens)))
const keyRows = computed(() => {
  return [{ name: '全部密钥', calls: totals.value.calls, input: totals.value.input, output: totals.value.output, cost: totals.value.cost }]
})

async function load() {
  loading.value = true
  error.value = ''
  const [sum, models, keyRowsResult] = await Promise.allSettled([
    api<{ items: SummaryDay[] }>('GET', `/api/usage/summary?days=${range.value}`),
    api<{ items: ModelStat[] }>('GET', `/api/usage/by-model?days=${range.value}`),
    api<{ keys: KeyRow[] }>('GET', '/api/keys'),
  ])
  if (sum.status === 'fulfilled') summary.value = sum.value.items.map((s) => ({ ...s, day: s.day.slice(0, 10) }))
  if (models.status === 'fulfilled') byModel.value = models.value.items
  if (keyRowsResult.status === 'fulfilled') keys.value = keyRowsResult.value.keys
  const failed = [sum, models].find((r) => r.status === 'rejected')
  if (failed?.status === 'rejected') error.value = errorMessage(failed.reason, '加载失败')
  loading.value = false
}

watch(range, () => void load())
onMounted(load)
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <h1 class="app-page-title">用量分析</h1>
      </div>
    </div>

    <div class="toolbar-panel items-end">
      <div class="filter-group">
        <label>时间范围</label>
        <div class="segmented-control">
          <button v-for="r in rangeOptions" :key="r.label" type="button" :class="{ active: rangeLabel === r.label }" @click="setRange(r.label, r.days)">{{ r.label }}</button>
        </div>
      </div>
      <div class="filter-group">
        <label>粒度</label>
        <div class="segmented-control">
          <button type="button" disabled :class="{ active: granularity === 'hour' }" @click="granularity = 'hour'">按小时</button>
          <button type="button" :class="{ active: granularity === 'day' }" @click="granularity = 'day'">按天</button>
          <button type="button" disabled :class="{ active: granularity === 'month' }" @click="granularity = 'month'">按月</button>
        </div>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>模型</label>
        <select v-model="modelFilter" class="input">
          <option value="">全部模型 ({{ byModel.length }})</option>
          <option v-for="m in byModel" :key="m.model" :value="m.model">{{ m.model }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>密钥（待接口）</label>
        <select class="input" disabled>
          <option value="">全部密钥</option>
          <option v-for="k in keys" :key="k.id" :value="String(k.id)">{{ k.name || k.prefix }}</option>
        </select>
      </div>
    </div>

    <p class="text-xs text-muted">时间范围会重新请求后端聚合接口；当前后端暂不返回按小时、按月或按密钥聚合，相关控件保留为后续接入位。</p>

    <p v-if="error" class="inline-alert" role="alert">{{ error }}</p>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
      <div class="metric-card">
        <p class="text-xs text-muted">Tokens 总量</p>
        <p class="mt-2 font-mono text-2xl font-semibold">{{ loading ? '—' : fmtTokens(totals.tokens) }}</p>
      </div>
      <div class="metric-card">
        <p class="text-xs text-muted">输入 / 输出</p>
        <p class="mt-2 flex flex-wrap items-baseline gap-x-1.5 font-mono text-xl font-semibold">
          <span>{{ fmtTokens(totals.input) }}</span>
          <span class="text-sm text-muted">/</span>
          <span>{{ fmtTokens(totals.output) }}</span>
        </p>
      </div>
      <div class="metric-card">
        <p class="text-xs text-muted">请求总量</p>
        <p class="mt-2 font-mono text-2xl font-semibold">{{ n(totals.calls) }}</p>
      </div>
      <div class="metric-card">
        <p class="text-xs text-muted">预估费用</p>
        <p class="mt-2 font-mono text-2xl font-semibold">{{ fmtMoney(totals.cost) }}</p>
      </div>
      <div class="metric-card">
        <p class="text-xs text-muted">输入/输出占比</p>
        <p class="mt-2 font-mono text-lg font-semibold">
          {{ totals.tokens ? ((totals.input / totals.tokens) * 100).toFixed(1) : '0.0' }}% / {{ totals.tokens ? ((totals.output / totals.tokens) * 100).toFixed(1) : '0.0' }}%
        </p>
      </div>
    </div>

    <div class="grid gap-4 xl:grid-cols-[1fr_0.72fr]">
      <div class="app-panel p-5">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <h2 class="font-semibold">{{ granularity === 'hour' ? '按小时' : granularity === 'month' ? '按月' : '按天' }} {{ metric === 'tokens' ? 'Tokens' : metric === 'cost' ? 'Credits' : '请求量' }} 趋势</h2>
          <div class="segmented-control">
            <button v-for="m in metricOptions" :key="m.key" type="button" :class="{ active: metric === m.key }" @click="metric = m.key">{{ m.label }}</button>
          </div>
        </div>
        <DailyBarChart :items="summary" :days="range" :metric="metric" />
      </div>

      <div class="app-panel p-5">
        <h2 class="font-semibold">模型用量占比（按 Tokens）</h2>
        <div class="mt-4 space-y-3">
          <div v-for="m in filteredModels.slice(0, 8)" :key="m.model" class="flex items-center gap-3">
            <span class="w-36 truncate font-mono text-xs" :title="m.model">{{ m.model }}</span>
            <div class="h-2 flex-1 overflow-hidden rounded-full bg-line">
              <div class="h-full rounded-full bg-accent" :style="{ width: `${((m.prompt_tokens + m.completion_tokens) / maxModelTokens) * 100}%` }"></div>
            </div>
            <span class="w-16 text-right font-mono text-xs text-muted">{{ fmtTokens(m.prompt_tokens + m.completion_tokens) }}</span>
          </div>
          <p v-if="filteredModels.length === 0" class="empty-state">暂无数据</p>
        </div>
      </div>
    </div>

    <div class="app-panel p-5">
      <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
        <h2 class="font-semibold">按模型</h2>
        <input v-model="modelSearch" class="input max-w-xs" placeholder="搜索模型名称..." />
      </div>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3"><button class="table-sort" @click="setSort('model')">模型 ↕</button></th>
              <th class="px-4 py-3 text-right"><button class="table-sort" @click="setSort('calls')">请求数 ↕</button></th>
              <th class="px-4 py-3 text-right"><button class="table-sort" @click="setSort('input')">输入 Tokens ↕</button></th>
              <th class="px-4 py-3 text-right"><button class="table-sort" @click="setSort('output')">输出 Tokens ↕</button></th>
              <th class="px-4 py-3 text-right">推理 Tokens</th>
              <th class="px-4 py-3 text-right">缓存命中 Tokens</th>
              <th class="px-4 py-3 text-right"><button class="table-sort" @click="setSort('tokens')">Tokens 合计 {{ sortKey === 'tokens' ? (sortDir === 'desc' ? '↓' : '↑') : '↕' }}</button></th>
              <th class="px-4 py-3 text-right"><button class="table-sort" @click="setSort('cost')">Credits ↕</button></th>
              <th class="px-4 py-3 text-right">费用</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="filteredModels.length === 0">
              <td colspan="9" class="px-4 py-8 text-center text-muted">暂无数据</td>
            </tr>
            <tr v-for="m in filteredModels" :key="m.model">
              <td class="px-4 py-2.5 font-mono text-xs">{{ m.model }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(m.calls) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(m.prompt_tokens) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(m.completion_tokens) }}</td>
              <td class="px-4 py-2.5 text-right font-mono text-muted">—</td>
              <td class="px-4 py-2.5 text-right font-mono text-muted">—</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(m.prompt_tokens + m.completion_tokens) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(Math.round(num(m.cost_usd) * 1_000_000)) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtMoney(num(m.cost_usd)) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="app-panel p-5">
      <h2 class="mb-4 font-semibold">按密钥</h2>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3">密钥 ↕</th>
              <th class="px-4 py-3 text-right">请求数 ↕</th>
              <th class="px-4 py-3 text-right">输入 Tokens ↕</th>
              <th class="px-4 py-3 text-right">输出 Tokens ↕</th>
              <th class="px-4 py-3 text-right">推理 Tokens ↕</th>
              <th class="px-4 py-3 text-right">缓存命中 Tokens ↕</th>
              <th class="px-4 py-3 text-right">Tokens 合计 ↓</th>
              <th class="px-4 py-3 text-right">Credits ↕</th>
              <th class="px-4 py-3 text-right">费用</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in keyRows" :key="row.name">
              <td class="px-4 py-2.5 font-mono text-xs">{{ row.name }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(row.calls) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(row.input) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(row.output) }}</td>
              <td class="px-4 py-2.5 text-right font-mono text-muted">—</td>
              <td class="px-4 py-2.5 text-right font-mono text-muted">—</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(row.input + row.output) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(Math.round(row.cost * 1_000_000)) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtMoney(row.cost) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="mt-3 text-xs text-muted">用户侧用量接口暂不暴露每条请求的 API key 归属；密钥表保留参考站结构，待后端开放后可直接填充。</p>
    </div>
  </div>
</template>
