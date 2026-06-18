<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'
import DailyBarChart, { type ChartMetric, type SummaryDay } from '@/components/DailyBarChart.vue'
import DonutChart from '@/components/DonutChart.vue'

interface UsageRow { id: number; model: string; status: string; duration_ms: number }
interface ModelStat {
  model: string
  calls: number
  succeeded: number
  prompt_tokens: number
  completion_tokens: number
  cost_usd: string
}
interface KeyStat {
  api_key_id: number
  name: string
  calls: number
  prompt_tokens: number
  completion_tokens: number
  cost_usd: string
}
interface KeyRow { id: number; status: number }

const { n, t } = useI18n()

const balance = ref('0')
const held = ref('0')
const summary = ref<SummaryDay[]>([])
const byModel = ref<ModelStat[]>([])
const byKey = ref<KeyStat[]>([])
const recent = ref<UsageRow[]>([])
const keys = ref<KeyRow[]>([])
const monthRechargeUSD = ref(0)
const last7RechargeUSD = ref(0)
const fx = ref(7.3)
const loading = ref(true)

type RankMetric = 'tokens' | 'credits' | 'calls'
const chartMetric = ref<ChartMetric>('tokens')
const chartMetrics: ChartMetric[] = ['tokens', 'cost', 'calls']
const modelMetric = ref<RankMetric>('tokens')
const keyMetric = ref<RankMetric>('tokens')
const RANK_METRICS: RankMetric[] = ['tokens', 'credits', 'calls']
const DONUT_COLORS = ['rgb(var(--chart-1))', 'rgb(var(--chart-2))', 'rgb(var(--chart-3))', 'rgb(var(--chart-4))', 'rgb(var(--chart-5))', 'rgb(var(--chart-6))', 'rgb(var(--c-muted))']

function num(v: string | number): number {
  const out = Number(v)
  return Number.isFinite(out) ? out : 0
}
function fmtTokens(v: number): string {
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(2)}M`
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}K`
  return n(v)
}
// 资金统一以 credits 展示（1 credit = 0.000001 元；credits = USD × 汇率 × 100 万），并附 ≈¥ 估算。
function credits(usd: string | number): number {
  return Math.round(num(usd) * fx.value * 1_000_000)
}
function fmtCredits(usd: string | number): string {
  return n(credits(usd))
}
function cnyText(usd: string | number): string {
  return `≈¥${(num(usd) * fx.value).toFixed(2)}`
}
function rankLabel(m: RankMetric): string {
  return m === 'tokens' ? 'Tokens' : m === 'credits' ? 'Credits' : t('dash.rankCalls')
}

function dayKey(offset: number): string {
  const dt = new Date()
  dt.setDate(dt.getDate() + offset)
  return dt.toISOString().slice(0, 10)
}
function sumDays(startOffset: number, count: number) {
  const days = new Set<string>()
  for (let i = 0; i < count; i++) days.add(dayKey(startOffset + i))
  let calls = 0
  let tokens = 0
  let cost = 0
  for (const s of summary.value) {
    if (!days.has(s.day)) continue
    calls += s.calls
    tokens += s.prompt_tokens + s.completion_tokens
    cost += num(s.cost_usd)
  }
  return { calls, tokens, cost }
}

const totals = computed(() => {
  let calls = 0
  let succeeded = 0
  let input = 0
  let output = 0
  let cost = 0
  for (const s of summary.value) {
    calls += s.calls
    succeeded += s.succeeded
    input += s.prompt_tokens
    output += s.completion_tokens
    cost += num(s.cost_usd)
  }
  return { calls, succeeded, input, output, tokens: input + output, cost, successRate: calls ? succeeded / calls : 1 }
})
const today = computed(() => sumDays(0, 1))
const yesterday = computed(() => sumDays(-1, 1))
const last7 = computed(() => sumDays(-6, 7))
const prev7 = computed(() => sumDays(-13, 7))
const activeKeys = computed(() => keys.value.filter((k) => k.status === 1).length)

// 本月/近7日充值额来自服务端聚合（/api/payment/recharge-stats），避免对分页明细本地求和漏算；
// 近7日窗口与后端 sumDays(-6,7) 同为 7 个 UTC 自然日，故 net7d 收入与支出同窗、口径一致。
const monthRecharge = computed(() => monthRechargeUSD.value)
const net7d = computed(() => last7RechargeUSD.value - last7.value.cost)

// 费用构成环图：按模型 credits 占比，top6 + 其他。
const donutItems = computed(() => {
  const sorted = [...byModel.value].sort((a, b) => num(b.cost_usd) - num(a.cost_usd))
  const top = sorted.slice(0, 6)
  const restCost = sorted.slice(6).reduce((s, m) => s + num(m.cost_usd), 0)
  const items = top.map((m, i) => ({ label: m.model, value: credits(m.cost_usd), color: DONUT_COLORS[i] }))
  if (restCost > 0) items.push({ label: t('dash.donutOther'), value: credits(restCost), color: DONUT_COLORS[6] })
  return items
})

function modelVal(m: ModelStat): number {
  return modelMetric.value === 'tokens' ? m.prompt_tokens + m.completion_tokens : modelMetric.value === 'credits' ? credits(m.cost_usd) : m.calls
}
function keyVal(k: KeyStat): number {
  return keyMetric.value === 'tokens' ? k.prompt_tokens + k.completion_tokens : keyMetric.value === 'credits' ? credits(k.cost_usd) : k.calls
}
const modelRanked = computed(() => [...byModel.value].sort((a, b) => modelVal(b) - modelVal(a)).slice(0, 6))
const keyRanked = computed(() => [...byKey.value].sort((a, b) => keyVal(b) - keyVal(a)).slice(0, 6))
const maxModel = computed(() => Math.max(1, ...modelRanked.value.map(modelVal)))
const maxKey = computed(() => Math.max(1, ...keyRanked.value.map(keyVal)))

// 平均响应最快 Top5（按模型对近期样本求平均时长，升序）。
const fastestModels = computed(() => {
  const map = new Map<string, { sum: number; n: number }>()
  for (const r of recent.value) {
    if (r.duration_ms > 0) {
      const e = map.get(r.model) ?? { sum: 0, n: 0 }
      e.sum += r.duration_ms
      e.n++
      map.set(r.model, e)
    }
  }
  return [...map.entries()].map(([model, e]) => ({ model, avg: Math.round(e.sum / e.n) })).sort((a, b) => a.avg - b.avg).slice(0, 5)
})

// 完整周期间的同比变化（前 7 日 > 0 才有意义；今日 vs 昨日为残日不算百分比，避免误导性 -100%）。
function pctText(cur: number, prev: number): string {
  if (prev <= 0) return ''
  const d = ((cur - prev) / prev) * 100
  return `${d >= 0 ? '+' : ''}${d.toFixed(1)}%`
}
function pctClass(cur: number, prev: number): string {
  if (prev <= 0) return 'text-muted'
  return cur >= prev ? 'text-ok' : 'text-danger'
}

onMounted(async () => {
  loading.value = true
  const [bal, sum, bm, bk, use, keyRows, ords, cfg] = await Promise.allSettled([
    api<{ balance: string; held: string }>('GET', '/api/balance'),
    api<{ items: SummaryDay[] }>('GET', '/api/usage/summary?days=30'),
    api<{ items: ModelStat[] }>('GET', '/api/usage/by-model?days=30'),
    api<{ items: KeyStat[] }>('GET', '/api/usage/by-key?days=30'),
    api<{ items: UsageRow[] }>('GET', '/api/usage?page=1&page_size=100'),
    api<{ keys: KeyRow[] }>('GET', '/api/keys'),
    api<{ month_usd: string; last7_usd: string }>('GET', '/api/payment/recharge-stats'),
    api<{ cny_per_usd: string }>('GET', '/api/config', undefined, { auth: false }),
  ])
  if (bal.status === 'fulfilled') {
    balance.value = bal.value.balance
    held.value = bal.value.held
  }
  if (sum.status === 'fulfilled') summary.value = sum.value.items.map((s) => ({ ...s, day: s.day.slice(0, 10) }))
  if (bm.status === 'fulfilled') byModel.value = bm.value.items
  if (bk.status === 'fulfilled') byKey.value = bk.value.items
  if (use.status === 'fulfilled') recent.value = use.value.items
  if (keyRows.status === 'fulfilled') keys.value = keyRows.value.keys
  if (ords.status === 'fulfilled') {
    monthRechargeUSD.value = num(ords.value.month_usd)
    last7RechargeUSD.value = num(ords.value.last7_usd)
  }
  if (cfg.status === 'fulfilled') {
    const rate = Number(cfg.value.cny_per_usd)
    if (Number.isFinite(rate) && rate > 0) fx.value = rate
  }
  loading.value = false
})
</script>

<template>
  <div class="app-page">
    <div class="flex flex-wrap items-end justify-between gap-4">
      <div>
        <p class="app-eyebrow">{{ t('dash.eyebrow') }}</p>
        <h1 class="app-page-title">{{ t('dash.title') }}</h1>
      </div>
      <router-link to="/app/keys" class="btn-primary">{{ t('dash.btnCreateKey') }}</router-link>
    </div>

    <!-- 指标卡 -->
    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-indigo"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m3.75 13.5 10.5-11.25L12 10.5h8.25L9.75 21.75 12 13.5H3.75Z" /></svg></span>
          <p class="text-xs font-medium text-muted">{{ t('dash.cardTokensTitle') }}</p>
        </div>
        <p class="mt-3 font-mono text-3xl font-semibold">{{ loading ? '—' : fmtTokens(totals.tokens) }}</p>
        <p class="mt-2 text-xs text-muted">{{ t('dash.cardTokensSub', { input: fmtTokens(totals.input), output: fmtTokens(totals.output) }) }}</p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-emerald"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v12m-3-2.818.879.659c1.171.879 3.07.879 4.242 0 1.172-.879 1.172-2.303 0-3.182C13.536 12.219 12.768 12 12 12c-.725 0-1.45-.22-2.003-.659-1.106-.879-1.106-2.303 0-3.182s2.9-.879 4.006 0l.415.33M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span>
          <p class="text-xs font-medium text-muted">{{ t('dash.cardCostTitle') }}</p>
        </div>
        <p class="mt-3 font-mono text-3xl font-semibold">{{ loading ? '—' : fmtCredits(totals.cost) }}</p>
        <p class="mt-2 text-xs text-muted">{{ cnyText(totals.cost) }}</p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-amber"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M7.5 21 3 16.5m0 0L7.5 12M3 16.5h13.5m0-13.5L21 7.5m0 0L16.5 12M21 7.5H7.5" /></svg></span>
          <p class="text-xs font-medium text-muted">{{ t('dash.cardRequestsTitle') }}</p>
        </div>
        <p class="mt-3 font-mono text-3xl font-semibold">{{ loading ? '—' : n(totals.calls) }}</p>
        <p class="mt-2 text-xs"><span class="text-ok">{{ n(totals.succeeded) }}</span> <span class="text-muted">{{ t('dash.cardRequestsSucceeded') }}</span></p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-rose"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M15.75 5.25a3 3 0 0 1 3 3m3 0a6 6 0 0 1-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1 1 21.75 8.25Z" /></svg></span>
          <p class="text-xs font-medium text-muted">{{ t('dash.cardKeysTitle') }}</p>
        </div>
        <p class="mt-3 font-mono text-3xl font-semibold">{{ activeKeys }}<span class="text-base text-muted">/{{ keys.length }}</span></p>
        <p class="mt-2 text-xs text-muted">{{ activeKeys === keys.length ? t('dash.cardKeysAllActive') : t('dash.cardKeysHasInactive') }}</p>
      </div>

      <router-link to="/app/billing" class="metric-card block">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-sky"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12a2.25 2.25 0 0 0-2.25-2.25H15a3 3 0 1 1-6 0H5.25A2.25 2.25 0 0 0 3 12m18 0v6a2.25 2.25 0 0 1-2.25 2.25H5.25A2.25 2.25 0 0 1 3 18v-6m18 0V9M3 12V9m18 0a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 9m18 0V6a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 6v3" /></svg></span>
          <p class="text-xs font-medium text-muted">{{ t('dash.cardBalanceTitle') }}</p>
        </div>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ fmtCredits(balance) }}</p>
        <p class="mt-2 text-xs text-muted">{{ t('dash.cardBalanceSub', { cny: cnyText(balance), held: fmtCredits(held) }) }}</p>
      </router-link>
      <router-link to="/app/recharges" class="metric-card block">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-violet"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.25 8.25h19.5M2.25 9h19.5m-16.5 5.25h6m-6 2.25h3m-3.75 3h15a2.25 2.25 0 0 0 2.25-2.25V6.75A2.25 2.25 0 0 0 19.5 4.5h-15a2.25 2.25 0 0 0-2.25 2.25v10.5A2.25 2.25 0 0 0 4.5 19.5Z" /></svg></span>
          <p class="text-xs font-medium text-muted">{{ t('dash.cardMonthRechargeTitle') }}</p>
        </div>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ fmtCredits(monthRecharge) }}</p>
        <p class="mt-2 text-xs text-muted">{{ t('dash.cardMonthRechargeSub', { cny: cnyText(monthRecharge) }) }}</p>
      </router-link>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-cyan"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.25 18 9 11.25l4.306 4.306a11.95 11.95 0 0 1 5.814-5.518l2.74-1.22m0 0-5.94-2.281m5.94 2.28-2.28 5.941" /></svg></span>
          <p class="text-xs font-medium text-muted">{{ t('dash.cardNet7dTitle') }}</p>
        </div>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ fmtCredits(net7d) }}</p>
        <p class="mt-2 text-xs text-muted">{{ t('dash.cardNet7dSub') }}</p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-green"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 12.75 11.25 15 15 9.75M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span>
          <p class="text-xs font-medium text-muted">{{ t('dash.cardSuccessRateTitle') }}</p>
        </div>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ (totals.successRate * 100).toFixed(1) }}%</p>
        <p class="mt-2 text-xs text-muted">{{ t('dash.cardSuccessRateSub', { n: n(totals.calls) }) }}</p>
      </div>
    </div>

    <!-- 今日 vs 昨日 / 近 7 日 vs 前 7 日 -->
    <div class="grid gap-4 lg:grid-cols-2">
      <div class="app-panel p-5">
        <h2 class="font-semibold">{{ t('dash.todayVsYesterday') }}</h2>
        <div class="mt-5 grid grid-cols-3 gap-4 text-center">
          <div>
            <p class="text-xs text-muted">Tokens</p>
            <p class="mt-2 font-mono text-xl font-semibold">{{ fmtTokens(today.tokens) }}</p>
            <p class="text-xs text-muted">{{ t('dash.yesterdayValue', { v: fmtTokens(yesterday.tokens) }) }}</p>
          </div>
          <div>
            <p class="text-xs text-muted">{{ t('dash.colRequests') }}</p>
            <p class="mt-2 font-mono text-xl font-semibold">{{ n(today.calls) }}</p>
            <p class="text-xs text-muted">{{ t('dash.yesterdayValue', { v: n(yesterday.calls) }) }}</p>
          </div>
          <div>
            <p class="text-xs text-muted">{{ t('dash.colCost') }}</p>
            <p class="mt-2 font-mono text-xl font-semibold">{{ fmtCredits(today.cost) }}</p>
            <p class="text-xs text-muted">{{ t('dash.yesterdayValue', { v: fmtCredits(yesterday.cost) }) }}</p>
          </div>
        </div>
        <p class="mt-3 text-xs text-muted">{{ t('dash.partialDayNote') }}</p>
      </div>
      <div class="app-panel p-5">
        <div class="flex items-center justify-between">
          <h2 class="font-semibold">{{ t('dash.last7VsPrev7') }}</h2>
          <router-link class="text-sm text-accent hover:underline" to="/app/usage">{{ t('dash.linkUsageAnalysis') }}</router-link>
        </div>
        <div class="mt-5 grid grid-cols-3 gap-4 text-center">
          <div>
            <p class="text-xs text-muted">Tokens</p>
            <p class="mt-2 font-mono text-xl font-semibold">{{ fmtTokens(last7.tokens) }}</p>
            <p class="text-xs" :class="pctClass(last7.tokens, prev7.tokens)">{{ pctText(last7.tokens, prev7.tokens) || t('dash.prev7Value', { v: fmtTokens(prev7.tokens) }) }}</p>
          </div>
          <div>
            <p class="text-xs text-muted">{{ t('dash.colRequests') }}</p>
            <p class="mt-2 font-mono text-xl font-semibold">{{ n(last7.calls) }}</p>
            <p class="text-xs" :class="pctClass(last7.calls, prev7.calls)">{{ pctText(last7.calls, prev7.calls) || t('dash.prev7Value', { v: n(prev7.calls) }) }}</p>
          </div>
          <div>
            <p class="text-xs text-muted">{{ t('dash.colCost') }}</p>
            <p class="mt-2 font-mono text-xl font-semibold">{{ fmtCredits(last7.cost) }}</p>
            <p class="text-xs" :class="pctClass(last7.cost, prev7.cost)">{{ pctText(last7.cost, prev7.cost) || t('dash.prev7Value', { v: fmtCredits(prev7.cost) }) }}</p>
          </div>
        </div>
      </div>
    </div>

    <!-- 用量趋势 + 费用构成 -->
    <div class="grid gap-4 xl:grid-cols-[1fr_0.72fr]">
      <div class="app-panel p-5">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <h2 class="font-semibold">{{ t('dash.usageTrendTitle') }}</h2>
          <div class="segmented-control">
            <button v-for="m in chartMetrics" :key="m" type="button" :class="{ active: chartMetric === m }" @click="chartMetric = m">
              {{ m === 'tokens' ? 'Tokens' : m === 'cost' ? 'Credits' : t('dash.rankCalls') }}
            </button>
          </div>
        </div>
        <DailyBarChart :items="summary" :days="30" :metric="chartMetric" :credits-fx="fx" />
      </div>

      <div class="app-panel p-5">
        <h2 class="font-semibold">{{ t('dash.costBreakdownTitle') }}</h2>
        <div class="mt-6">
          <DonutChart :items="donutItems" :empty-text="t('dash.emptyData')" />
        </div>
      </div>
    </div>

    <!-- 模型排行 + 密钥排行 -->
    <div class="grid gap-4 lg:grid-cols-2">
      <div class="app-panel p-5">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <h2 class="font-semibold">{{ t('dash.modelRankTitle') }}</h2>
          <div class="segmented-control">
            <button v-for="m in RANK_METRICS" :key="m" type="button" :class="{ active: modelMetric === m }" @click="modelMetric = m">{{ rankLabel(m) }}</button>
          </div>
        </div>
        <div class="space-y-3">
          <div v-for="m in modelRanked" :key="m.model" class="flex items-center gap-3">
            <span class="w-40 truncate font-mono text-xs" :title="m.model">{{ m.model }}</span>
            <div class="h-2 flex-1 overflow-hidden rounded-full bg-line">
              <div class="h-full rounded-full bg-accent" :style="{ width: `${(modelVal(m) / maxModel) * 100}%` }"></div>
            </div>
            <span class="w-20 text-right font-mono text-xs text-muted">{{ n(modelVal(m)) }}</span>
          </div>
          <p v-if="modelRanked.length === 0" class="empty-state">{{ t('dash.emptyData') }}</p>
        </div>
      </div>

      <div class="app-panel p-5">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <h2 class="font-semibold">{{ t('dash.keyRankTitle') }}</h2>
          <div class="segmented-control">
            <button v-for="m in RANK_METRICS" :key="m" type="button" :class="{ active: keyMetric === m }" @click="keyMetric = m">{{ rankLabel(m) }}</button>
          </div>
        </div>
        <div class="space-y-3">
          <div v-for="k in keyRanked" :key="k.api_key_id" class="flex items-center gap-3">
            <span class="w-40 truncate font-mono text-xs" :title="k.name || `#${k.api_key_id}`">{{ k.name || `#${k.api_key_id}` }}</span>
            <div class="h-2 flex-1 overflow-hidden rounded-full bg-line">
              <div class="h-full rounded-full bg-accent2" :style="{ width: `${(keyVal(k) / maxKey) * 100}%` }"></div>
            </div>
            <span class="w-20 text-right font-mono text-xs text-muted">{{ n(keyVal(k)) }}</span>
          </div>
          <p v-if="keyRanked.length === 0" class="empty-state">{{ t('dash.emptyData') }}</p>
        </div>
      </div>
    </div>

    <!-- 平均响应最快 Top 5 -->
    <div class="app-panel p-5">
      <h2 class="font-semibold">{{ t('dash.fastestTitle') }}</h2>
      <div class="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
        <div v-for="r in fastestModels" :key="r.model" class="soft-panel p-3">
          <p class="truncate font-mono text-xs" :title="r.model">{{ r.model }}</p>
          <p class="mt-1 font-mono text-lg font-semibold text-ok">{{ r.avg }}ms</p>
        </div>
        <p v-if="fastestModels.length === 0" class="empty-state">{{ t('dash.emptyData') }}</p>
      </div>
    </div>
  </div>
</template>
