<script setup lang="ts">
import { fmtUsd, addMoney } from '@/lib/money'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'
import DailyBarChart, { type ChartMetric, type SummaryDay } from '@/components/DailyBarChart.vue'
import PerformancePanel from '@/components/PerformancePanel.vue' // 原 Latency 页并入本页「性能」tab

const tab = ref<'overview' | 'performance'>('overview')

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
interface KeyStat {
  api_key_id: number
  name: string
  calls: number
  prompt_tokens: number
  completion_tokens: number
  cost_usd: string
}

const { t, n } = useI18n()
const range = ref<7 | 30 | 31 | 60>(30)
const rangeLabel = ref('30d')
const granularity = ref<'hour' | 'day'>('day')
const metric = ref<ChartMetric>('tokens')
const modelFilter = ref('')
const modelSearch = ref('')
const keyFilter = ref('') // api_key_id 服务端过滤（summary/by-model 下推）
const loading = ref(true)
const error = ref('')
const summary = ref<SummaryDay[]>([])
const byModel = ref<ModelStat[]>([])
const byKey = ref<KeyStat[]>([])
const keys = ref<KeyRow[]>([])
const sortKey = ref<'model' | 'calls' | 'input' | 'output' | 'tokens' | 'cost'>('tokens')
const sortDir = ref<'asc' | 'desc'>('desc')

const rangeOptions = [
  { label: '7d', days: 7 as const },
  { label: '30d', days: 30 as const },
  { label: '31d', days: 31 as const },
  { label: '60d', days: 60 as const },
]
const metricOptions = computed<{ key: ChartMetric; label: string }[]>(() => [
  { key: 'tokens', label: 'Tokens' },
  { key: 'cost', label: t('usagex.colCost') },
  { key: 'calls', label: t('usagex.metricCalls') },
])

function num(v: string | number): number {
  const out = Number(v)
  return Number.isFinite(out) ? out : 0
}
function fmtTokens(v: number): string {
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(2)}M`
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}K`
  return n(v)
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
  let cost = '0'
  for (const m of filteredModels.value) {
    calls += m.calls
    input += m.prompt_tokens
    output += m.completion_tokens
    cost = addMoney(cost, m.cost_usd)
  }
  return { calls, input, output, tokens: input + output, cost }
})
const maxModelTokens = computed(() => Math.max(1, ...filteredModels.value.map((m) => m.prompt_tokens + m.completion_tokens)))
// 已吊销/清除的 key 不在 /api/keys 列表里，退化显示 #id（与 RequestLogs 同规）
function keyName(row: KeyStat): string {
  if (row.name) return row.name
  const k = keys.value.find((x) => x.id === row.api_key_id)
  return k ? k.name || k.prefix : `#${row.api_key_id}`
}

async function load() {
  loading.value = true
  error.value = ''
  const sumQ = new URLSearchParams({ days: String(range.value) })
  if (granularity.value === 'hour') sumQ.set('granularity', 'hour') // 近 24h 小时分桶（UTC）
  const bmQ = new URLSearchParams({ days: String(range.value) })
  if (keyFilter.value) {
    sumQ.set('api_key_id', keyFilter.value)
    bmQ.set('api_key_id', keyFilter.value)
  }
  const [sum, models, byKeyResult, keyRowsResult] = await Promise.allSettled([
    api<{ items: SummaryDay[] }>('GET', `/api/usage/summary?${sumQ}`),
    api<{ items: ModelStat[] }>('GET', `/api/usage/by-model?${bmQ}`),
    api<{ items: KeyStat[] }>('GET', `/api/usage/by-key?days=${range.value}`),
    api<{ keys: KeyRow[] }>('GET', '/api/keys'),
  ])
  // hour 桶键是 'YYYY-MM-DD HH:00'，不能截日期；day 桶保持既有 slice(0,10) 口径
  if (sum.status === 'fulfilled') {
    summary.value = granularity.value === 'hour'
      ? (sum.value.items ?? [])
      : (sum.value.items ?? []).map((s) => ({ ...s, day: s.day.slice(0, 10) }))
  }
  if (models.status === 'fulfilled') byModel.value = models.value.items ?? []
  if (byKeyResult.status === 'fulfilled') byKey.value = byKeyResult.value.items ?? []
  if (keyRowsResult.status === 'fulfilled') keys.value = keyRowsResult.value.keys ?? []
  const failed = [sum, models].find((r) => r.status === 'rejected')
  if (failed?.status === 'rejected') error.value = errorMessage(failed.reason, t('usagex.loadFailed'))
  loading.value = false
}

watch([range, granularity, keyFilter], () => void load())
onMounted(load)
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <h1 class="app-page-title">{{ t('usagex.title') }}</h1>
      </div>
    </div>

    <div class="border-b border-line">
      <div class="tabs-row">
        <button type="button" :class="{ active: tab === 'overview' }" @click="tab = 'overview'">{{ t('usagex.tabOverview') }}</button>
        <button type="button" :class="{ active: tab === 'performance' }" @click="tab = 'performance'">{{ t('usagex.tabPerformance') }}</button>
      </div>
    </div>

    <PerformancePanel v-if="tab === 'performance'" />
    <template v-else>
    <div class="toolbar-panel items-end">
      <div class="filter-group">
        <label>{{ t('usagex.filterRange') }}</label>
        <div class="segmented-control">
          <button v-for="r in rangeOptions" :key="r.label" type="button" :class="{ active: rangeLabel === r.label }" @click="setRange(r.label, r.days)">{{ r.label }}</button>
        </div>
      </div>
      <div class="filter-group">
        <label>{{ t('usagex.filterGranularity') }}</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: granularity === 'hour' }" @click="granularity = 'hour'">{{ t('usagex.granHour') }}</button>
          <button type="button" :class="{ active: granularity === 'day' }" @click="granularity = 'day'">{{ t('usagex.granDay') }}</button>
        </div>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>{{ t('usagex.filterModel') }}</label>
        <select v-model="modelFilter" class="input">
          <option value="">{{ t('usagex.allModels', { n: byModel.length }) }}</option>
          <option v-for="m in byModel" :key="m.model" :value="m.model">{{ m.model }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>{{ t('usagex.filterKey') }}</label>
        <select v-model="keyFilter" class="input">
          <option value="">{{ t('usagex.allKeys') }}</option>
          <option v-for="k in keys" :key="k.id" :value="String(k.id)">{{ k.name || k.prefix }}</option>
        </select>
      </div>
    </div>

    <p class="text-xs text-muted">{{ t('usagex.toolbarHint') }}</p>

    <p v-if="error" class="inline-alert" role="alert">{{ error }}</p>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-indigo"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m3.75 13.5 10.5-11.25L12 10.5h8.25L9.75 21.75 12 13.5H3.75Z" /></svg></span>
          <p class="text-xs text-muted">{{ t('usagex.metricTotalTokens') }}</p>
        </div>
        <p class="mt-2 font-mono text-2xl font-semibold">{{ loading ? '—' : fmtTokens(totals.tokens) }}</p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-emerald"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M7.5 21 3 16.5m0 0L7.5 12M3 16.5h13.5m0-13.5L21 7.5m0 0L16.5 12M21 7.5H7.5" /></svg></span>
          <p class="text-xs text-muted">{{ t('usagex.metricInputOutput') }}</p>
        </div>
        <p class="mt-2 flex flex-wrap items-baseline gap-x-1.5 font-mono text-xl font-semibold">
          <span>{{ fmtTokens(totals.input) }}</span>
          <span class="text-sm text-muted">/</span>
          <span>{{ fmtTokens(totals.output) }}</span>
        </p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-amber"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.25 18 9 11.25l4.306 4.306a11.95 11.95 0 0 1 5.814-5.518l2.74-1.22m0 0-5.94-2.281m5.94 2.28-2.28 5.941" /></svg></span>
          <p class="text-xs text-muted">{{ t('usagex.metricTotalCalls') }}</p>
        </div>
        <p class="mt-2 font-mono text-2xl font-semibold">{{ n(totals.calls) }}</p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-rose"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v12m-3-2.818.879.659c1.171.879 3.07.879 4.242 0 1.172-.879 1.172-2.303 0-3.182C13.536 12.219 12.768 12 12 12c-.725 0-1.45-.22-2.003-.659-1.106-.879-1.106-2.303 0-3.182s2.9-.879 4.006 0l.415.33M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span>
          <p class="text-xs text-muted">{{ t('usagex.metricEstCost') }}</p>
        </div>
        <p class="mt-2 font-mono text-2xl font-semibold">{{ fmtUsd(totals.cost) }}</p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-sky"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.25 13.5h8.25l-3.696 3.696a8.25 8.25 0 1 1 0-11.668L10.5 9.75H2.25v3.75Z" /></svg></span>
          <p class="text-xs text-muted">{{ t('usagex.metricIoRatio') }}</p>
        </div>
        <p class="mt-2 font-mono text-lg font-semibold">
          {{ totals.tokens ? ((totals.input / totals.tokens) * 100).toFixed(1) : '0.0' }}% / {{ totals.tokens ? ((totals.output / totals.tokens) * 100).toFixed(1) : '0.0' }}%
        </p>
      </div>
    </div>

    <div class="grid gap-4 xl:grid-cols-[1fr_0.72fr]">
      <div class="app-panel p-5">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <h2 class="font-semibold">{{ t('usagex.trendTitle', { period: granularity === 'hour' ? t('usagex.granHour') : t('usagex.granDay'), metric: metric === 'tokens' ? 'Tokens' : metric === 'cost' ? t('usagex.colCost') : t('usagex.metricCalls') }) }}</h2>
          <div class="segmented-control">
            <button v-for="m in metricOptions" :key="m.key" type="button" :class="{ active: metric === m.key }" @click="metric = m.key">{{ m.label }}</button>
          </div>
        </div>
        <DailyBarChart :items="summary" :days="range" :metric="metric" :granularity="granularity" />
      </div>

      <div class="app-panel p-5">
        <h2 class="font-semibold">{{ t('usagex.modelShareTitle') }}</h2>
        <div class="mt-4 space-y-3">
          <div v-for="m in filteredModels.slice(0, 8)" :key="m.model" class="flex items-center gap-3">
            <span class="w-36 truncate font-mono text-xs" :title="m.model">{{ m.model }}</span>
            <div class="h-2 flex-1 overflow-hidden rounded-full bg-line">
              <div class="h-full rounded-full bg-accent" :style="{ width: `${((m.prompt_tokens + m.completion_tokens) / maxModelTokens) * 100}%` }"></div>
            </div>
            <span class="w-16 text-right font-mono text-xs text-muted">{{ fmtTokens(m.prompt_tokens + m.completion_tokens) }}</span>
          </div>
          <p v-if="filteredModels.length === 0" class="empty-state">{{ t('usagex.emptyData') }}</p>
        </div>
      </div>
    </div>

    <div class="app-panel p-5">
      <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
        <h2 class="font-semibold">{{ t('usagex.byModelTitle') }}</h2>
        <input v-model="modelSearch" class="input max-w-xs" :placeholder="t('usagex.searchModelPlaceholder')" />
      </div>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3"><button class="table-sort" @click="setSort('model')">{{ t('usagex.colModel') }} ↕</button></th>
              <th class="px-4 py-3 text-right"><button class="table-sort" @click="setSort('calls')">{{ t('usagex.colCalls') }} ↕</button></th>
              <th class="px-4 py-3 text-right"><button class="table-sort" @click="setSort('input')">{{ t('usagex.colInputTokens') }} ↕</button></th>
              <th class="px-4 py-3 text-right"><button class="table-sort" @click="setSort('output')">{{ t('usagex.colOutputTokens') }} ↕</button></th>
              <th class="px-4 py-3 text-right"><button class="table-sort" @click="setSort('tokens')">{{ t('usagex.colTotalTokens') }} {{ sortKey === 'tokens' ? (sortDir === 'desc' ? '↓' : '↑') : '↕' }}</button></th>
              <th class="px-4 py-3 text-right"><button class="table-sort" @click="setSort('cost')">{{ t('usagex.colCost') }} {{ sortKey === 'cost' ? (sortDir === 'desc' ? '↓' : '↑') : '↕' }}</button></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="filteredModels.length === 0">
              <td colspan="6" class="px-4 py-8 text-center text-muted">{{ t('usagex.emptyData') }}</td>
            </tr>
            <tr v-for="m in filteredModels" :key="m.model">
              <td class="px-4 py-2.5 font-mono text-xs">{{ m.model }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(m.calls) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(m.prompt_tokens) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(m.completion_tokens) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(m.prompt_tokens + m.completion_tokens) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtUsd(m.cost_usd) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="app-panel p-5">
      <h2 class="mb-4 font-semibold">{{ t('usagex.byKeyTitle') }}</h2>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3">{{ t('usagex.colKey') }} ↕</th>
              <th class="px-4 py-3 text-right">{{ t('usagex.colCalls') }} ↕</th>
              <th class="px-4 py-3 text-right">{{ t('usagex.colInputTokens') }} ↕</th>
              <th class="px-4 py-3 text-right">{{ t('usagex.colOutputTokens') }} ↕</th>
              <th class="px-4 py-3 text-right">{{ t('usagex.colTotalTokens') }} ↓</th>
              <th class="px-4 py-3 text-right">{{ t('usagex.colCost') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="byKey.length === 0">
              <td colspan="6" class="px-4 py-8 text-center text-muted">{{ t('usagex.emptyData') }}</td>
            </tr>
            <tr v-for="row in byKey" :key="row.api_key_id">
              <td class="px-4 py-2.5 font-mono text-xs">{{ keyName(row) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(row.calls) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(row.prompt_tokens) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(row.completion_tokens) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(row.prompt_tokens + row.completion_tokens) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtUsd(row.cost_usd) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="mt-3 text-xs text-muted">{{ t('usagex.byKeyHint') }}</p>
    </div>
    </template>
  </div>
</template>
