<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'
import DailyBarChart, { type ChartMetric, type SummaryDay } from '@/components/DailyBarChart.vue'

interface ModelStat {
  model: string
  calls: number
  prompt_tokens: number
  completion_tokens: number
  cost_usd: string
}
interface KeyRow { id: number; name: string; prefix: string }

const { t, n } = useI18n()
const days = ref(7)
const metric = ref<ChartMetric>('tokens')
const grain = ref<'day' | 'hour'>('day')
const tag = ref('')
const userId = ref('')
const agent = ref('')
const profileNotice = ref('')
const summary = ref<SummaryDay[]>([])
const byModel = ref<ModelStat[]>([])
const keys = ref<KeyRow[]>([])

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

const totals = computed(() => {
  let calls = 0
  let tokens = 0
  let cost = 0
  for (const s of summary.value) {
    calls += s.calls
    tokens += s.prompt_tokens + s.completion_tokens
    cost += num(s.cost_usd)
  }
  return { calls, tokens, cost, avg: calls ? tokens / calls : 0 }
})

async function load() {
  const [sum, bm, keyRows] = await Promise.allSettled([
    api<{ items: SummaryDay[] }>('GET', `/api/usage/summary?days=${days.value}`),
    api<{ items: ModelStat[] }>('GET', `/api/usage/by-model?days=${days.value}`),
    api<{ keys: KeyRow[] }>('GET', '/api/keys'),
  ])
  if (sum.status === 'fulfilled') summary.value = sum.value.items.map((s) => ({ ...s, day: s.day.slice(0, 10) }))
  if (bm.status === 'fulfilled') byModel.value = bm.value.items
  if (keyRows.status === 'fulfilled') keys.value = keyRows.value.keys
}

onMounted(load)
</script>

<template>
  <div class="app-page">
    <div class="flex flex-wrap items-end justify-between gap-4">
      <div>
        <h1 class="app-page-title">{{ t('tokana.title') }}</h1>
      </div>
      <button class="btn-primary" type="button" @click="profileNotice = t('tokana.profileNotice')">{{ t('tokana.btnProfile') }}</button>
    </div>

    <p v-if="profileNotice" class="inline-success">{{ profileNotice }}</p>

    <div class="toolbar-panel items-end">
      <div class="filter-group min-w-[160px]">
        <label>{{ t('tokana.filterTag') }}</label>
        <input v-model="tag" class="input" :placeholder="t('tokana.filterTagPlaceholder')" />
      </div>
      <div class="filter-group min-w-[160px]">
        <label>{{ t('tokana.filterKey') }}</label>
        <select class="input">
          <option>{{ t('tokana.allKeys') }}</option>
          <option v-for="k in keys" :key="k.id">{{ k.name || k.prefix }}</option>
        </select>
      </div>
      <div class="filter-group">
        <label>{{ t('tokana.timeRange') }}</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: days === 1 }" @click="days = 1; load()">{{ t('tokana.rangeToday') }}</button>
          <button type="button" :class="{ active: days === 7 }" @click="days = 7; load()">{{ t('tokana.range7d') }}</button>
          <button type="button" :class="{ active: days === 31 }" @click="days = 31; load()">{{ t('tokana.rangeMonth') }}</button>
          <button type="button" disabled>{{ t('tokana.rangeCustom') }}</button>
        </div>
      </div>
      <div class="filter-group min-w-[160px]">
        <label>{{ t('tokana.filterUserId') }}</label>
        <input v-model="userId" class="input" :placeholder="t('tokana.filterUserIdPlaceholder')" />
      </div>
      <div class="filter-group min-w-[160px]">
        <label>{{ t('tokana.filterAgent') }}</label>
        <input v-model="agent" class="input" :placeholder="t('tokana.filterAgentPlaceholder')" />
      </div>
      <button class="btn-primary !min-h-0 !px-4 !py-2" type="button" @click="load">{{ t('tokana.btnQuery') }}</button>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div class="metric-card"><div class="flex items-center gap-3"><span class="metric-icon mi-indigo"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m3.75 13.5 10.5-11.25L12 10.5h8.25L9.75 21.75 12 13.5H3.75Z" /></svg></span><p class="text-xs text-muted">{{ t('tokana.metricTokensTotal') }}</p></div><p class="mt-2 font-mono text-2xl font-semibold">{{ fmtTokens(totals.tokens) }}</p><p class="text-xs text-muted">{{ t('tokana.metricTokensHint') }}</p></div>
      <div class="metric-card"><div class="flex items-center gap-3"><span class="metric-icon mi-emerald"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v12m-3-2.818.879.659c1.171.879 3.07.879 4.242 0 1.172-.879 1.172-2.303 0-3.182C13.536 12.219 12.768 12 12 12c-.725 0-1.45-.22-2.003-.659-1.106-.879-1.106-2.303 0-3.182s2.9-.879 4.006 0l.415.33M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span><p class="text-xs text-muted">{{ t('tokana.metricCost') }}</p></div><p class="mt-2 font-mono text-2xl font-semibold">{{ fmtMoney(totals.cost) }}</p><p class="text-xs text-muted">{{ t('tokana.metricCostHint') }}</p></div>
      <div class="metric-card"><div class="flex items-center gap-3"><span class="metric-icon mi-amber"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M7.5 21 3 16.5m0 0L7.5 12M3 16.5h13.5m0-13.5L21 7.5m0 0L16.5 12M21 7.5H7.5" /></svg></span><p class="text-xs text-muted">{{ t('tokana.metricCalls') }}</p></div><p class="mt-2 font-mono text-2xl font-semibold">{{ n(totals.calls) }}</p><p class="text-xs text-muted">{{ t('tokana.metricCallsHint') }}</p></div>
      <div class="metric-card"><div class="flex items-center gap-3"><span class="metric-icon mi-rose"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.25 13.5h8.25l-3.696 3.696a8.25 8.25 0 1 1 0-11.668L10.5 9.75H2.25v3.75Z" /></svg></span><p class="text-xs text-muted">{{ t('tokana.metricAvg') }}</p></div><p class="mt-2 font-mono text-2xl font-semibold">{{ n(Math.round(totals.avg)) }}</p><p class="text-xs text-muted">{{ t('tokana.metricAvgHint') }}</p></div>
    </div>

    <div class="app-panel p-5">
      <div class="mb-4 flex items-center justify-between gap-3">
        <h2 class="font-semibold">{{ t('tokana.usageTrend') }}</h2>
        <div class="flex gap-2">
          <div class="segmented-control"><button :class="{ active: grain === 'day' }" @click="grain = 'day'">{{ t('tokana.grainDay') }}</button><button :class="{ active: grain === 'hour' }" @click="grain = 'hour'">{{ t('tokana.grainHour') }}</button></div>
          <div class="segmented-control"><button :class="{ active: metric === 'tokens' }" @click="metric = 'tokens'">Tokens</button><button :class="{ active: metric === 'cost' }" @click="metric = 'cost'">Credits</button><button :class="{ active: metric === 'calls' }" @click="metric = 'calls'">{{ t('tokana.metricCallsLabel') }}</button></div>
        </div>
      </div>
      <DailyBarChart :items="summary" :days="days" :metric="metric" />
    </div>

    <div class="grid gap-4 lg:grid-cols-2">
      <div class="app-panel p-5">
        <h2 class="font-semibold">{{ t('tokana.tagRanking') }}</h2>
        <p class="mt-2 text-xs text-muted">{{ t('tokana.rankingScope') }}</p>
        <div class="mt-4 flex items-center justify-between text-sm">
          <span>{{ t('tokana.uncategorizedKeys') }}</span>
          <span class="font-mono">{{ fmtTokens(totals.tokens) }}</span>
        </div>
      </div>
      <div class="app-panel p-5">
        <h2 class="font-semibold">{{ t('tokana.keyRanking') }}</h2>
        <p class="mt-2 text-xs text-muted">{{ t('tokana.rankingScope') }}</p>
        <div class="mt-4 space-y-2">
          <div v-for="k in keys" :key="k.id" class="flex items-center justify-between text-sm">
            <span>{{ k.name || k.prefix }}</span>
            <span class="font-mono text-muted">—</span>
          </div>
          <p v-if="keys.length === 0" class="text-sm text-muted">{{ t('tokana.emptyKeys') }}</p>
        </div>
      </div>
    </div>
  </div>
</template>
