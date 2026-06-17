<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'
import DailyBarChart, { type ChartMetric, type SummaryDay } from '@/components/DailyBarChart.vue'

interface UsageRow {
  id: number
  request_id: string
  model: string
  status: string
  prompt_tokens: number
  completion_tokens: number
  cost_usd: string
  frt_ms: number
  duration_ms: number
  created_at: string
}
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
  status: number
}

const { n } = useI18n()

const balance = ref('0')
const held = ref('0')
const summary = ref<SummaryDay[]>([])
const byModel = ref<ModelStat[]>([])
const recent = ref<UsageRow[]>([])
const keys = ref<KeyRow[]>([])
const loading = ref(true)
const chartMetric = ref<ChartMetric>('tokens')
const chartMetrics: ChartMetric[] = ['tokens', 'cost', 'calls']

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
function dayKey(offset: number): string {
  const dt = new Date()
  dt.setDate(dt.getDate() + offset)
  return dt.toISOString().slice(0, 10)
}
function sumDays(startOffset: number, count: number) {
  const keys = new Set<string>()
  for (let i = 0; i < count; i++) keys.add(dayKey(startOffset + i))
  let calls = 0
  let succeeded = 0
  let tokens = 0
  let cost = 0
  for (const s of summary.value) {
    if (!keys.has(s.day)) continue
    calls += s.calls
    succeeded += s.succeeded
    tokens += s.prompt_tokens + s.completion_tokens
    cost += num(s.cost_usd)
  }
  return { calls, succeeded, tokens, cost }
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
  return {
    calls,
    succeeded,
    input,
    output,
    tokens: input + output,
    cost,
    successRate: calls ? succeeded / calls : 1,
  }
})
const today = computed(() => sumDays(0, 1))
const yesterday = computed(() => sumDays(-1, 1))
const last7 = computed(() => sumDays(-6, 7))
const prev7 = computed(() => sumDays(-13, 7))
const activeKeys = computed(() => keys.value.filter((k) => k.status === 1).length)
const fastest = computed(() => [...recent.value]
  .filter((r) => r.duration_ms > 0)
  .sort((a, b) => a.duration_ms - b.duration_ms)
  .slice(0, 5))
const maxModelTokens = computed(() => Math.max(1, ...byModel.value.map((m) => m.prompt_tokens + m.completion_tokens)))

onMounted(async () => {
  loading.value = true
  const [bal, sum, bm, use, keyRows] = await Promise.allSettled([
    api<{ balance: string; held: string }>('GET', '/api/balance'),
    api<{ items: SummaryDay[] }>('GET', '/api/usage/summary?days=30'),
    api<{ items: ModelStat[] }>('GET', '/api/usage/by-model?days=30'),
    api<{ items: UsageRow[] }>('GET', '/api/usage?page=1&page_size=20'),
    api<{ keys: KeyRow[] }>('GET', '/api/keys'),
  ])
  if (bal.status === 'fulfilled') {
    balance.value = bal.value.balance
    held.value = bal.value.held
  }
  if (sum.status === 'fulfilled') summary.value = sum.value.items.map((s) => ({ ...s, day: s.day.slice(0, 10) }))
  if (bm.status === 'fulfilled') byModel.value = bm.value.items
  if (use.status === 'fulfilled') recent.value = use.value.items
  if (keyRows.status === 'fulfilled') keys.value = keyRows.value.keys
  loading.value = false
})
</script>

<template>
  <div class="app-page">
    <div class="flex flex-wrap items-end justify-between gap-4">
      <div>
        <p class="app-eyebrow">平台概览</p>
        <h1 class="app-page-title">欢迎回来</h1>
      </div>
      <router-link to="/app/keys" class="btn-primary">创建 API 密钥</router-link>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div class="metric-card">
        <p class="text-xs font-medium text-muted">近 30 天 Tokens</p>
        <p class="mt-3 font-mono text-3xl font-semibold">{{ loading ? '—' : fmtTokens(totals.tokens) }}</p>
        <p class="mt-2 text-xs text-muted">输入 {{ fmtTokens(totals.input) }} · 输出 {{ fmtTokens(totals.output) }}</p>
      </div>
      <div class="metric-card">
        <p class="text-xs font-medium text-muted">近 30 天费用</p>
        <p class="mt-3 font-mono text-3xl font-semibold">{{ loading ? '—' : fmtMoney(totals.cost) }}</p>
        <p class="mt-2 text-xs text-muted">按项目当前 USD 计费单位展示</p>
      </div>
      <div class="metric-card">
        <p class="text-xs font-medium text-muted">近 30 天请求</p>
        <p class="mt-3 font-mono text-3xl font-semibold">{{ loading ? '—' : n(totals.calls) }}</p>
        <p class="mt-2 text-xs"><span class="text-ok">{{ n(totals.succeeded) }}</span> <span class="text-muted">成功</span></p>
      </div>
      <div class="metric-card">
        <p class="text-xs font-medium text-muted">API 密钥</p>
        <p class="mt-3 font-mono text-3xl font-semibold">{{ activeKeys }}<span class="text-base text-muted">/{{ keys.length }}</span></p>
        <p class="mt-2 text-xs text-muted">{{ activeKeys === keys.length ? '所有密钥状态正常' : '存在非活跃密钥' }}</p>
      </div>
      <router-link to="/app/billing" class="metric-card block">
        <p class="text-xs font-medium text-muted">预付余额</p>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ fmtMoney(num(balance)) }}</p>
        <p class="mt-2 text-xs text-muted">冻结 {{ fmtMoney(num(held)) }}</p>
      </router-link>
      <router-link to="/app/recharges" class="metric-card block">
        <p class="text-xs font-medium text-muted">本月累计充值</p>
        <p class="mt-3 font-mono text-2xl font-semibold">—</p>
        <p class="mt-2 text-xs text-muted">查看充值记录 →</p>
      </router-link>
      <div class="metric-card">
        <p class="text-xs font-medium text-muted">可用余额</p>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ fmtMoney(num(balance) - num(held)) }}</p>
        <p class="mt-2 text-xs text-muted">余额减冻结金额</p>
      </div>
      <div class="metric-card">
        <p class="text-xs font-medium text-muted">成功率 · 30d</p>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ (totals.successRate * 100).toFixed(1) }}%</p>
        <p class="mt-2 text-xs text-muted">{{ n(totals.calls) }} 次样本</p>
      </div>
    </div>

    <div class="grid gap-4 lg:grid-cols-2">
      <div class="app-panel p-5">
        <h2 class="font-semibold">今日 vs 昨日</h2>
        <div class="mt-5 grid grid-cols-3 gap-4 text-center">
          <div>
            <p class="text-xs text-muted">Tokens</p>
            <p class="mt-2 font-mono text-xl font-semibold">{{ fmtTokens(today.tokens) }}</p>
            <p class="text-xs text-muted">昨日 {{ fmtTokens(yesterday.tokens) }}</p>
          </div>
          <div>
            <p class="text-xs text-muted">请求数</p>
            <p class="mt-2 font-mono text-xl font-semibold">{{ n(today.calls) }}</p>
            <p class="text-xs text-muted">昨日 {{ n(yesterday.calls) }}</p>
          </div>
          <div>
            <p class="text-xs text-muted">费用</p>
            <p class="mt-2 font-mono text-xl font-semibold">{{ fmtMoney(today.cost) }}</p>
            <p class="text-xs text-muted">昨日 {{ fmtMoney(yesterday.cost) }}</p>
          </div>
        </div>
      </div>
      <div class="app-panel p-5">
        <div class="flex items-center justify-between">
          <h2 class="font-semibold">近 7 日 vs 前 7 日</h2>
          <router-link class="text-sm text-accent hover:underline" to="/app/usage">用量分析 →</router-link>
        </div>
        <div class="mt-5 grid grid-cols-3 gap-4 text-center">
          <div>
            <p class="text-xs text-muted">Tokens</p>
            <p class="mt-2 font-mono text-xl font-semibold">{{ fmtTokens(last7.tokens) }}</p>
            <p class="text-xs text-muted">前 7 日 {{ fmtTokens(prev7.tokens) }}</p>
          </div>
          <div>
            <p class="text-xs text-muted">请求数</p>
            <p class="mt-2 font-mono text-xl font-semibold">{{ n(last7.calls) }}</p>
            <p class="text-xs text-muted">前 7 日 {{ n(prev7.calls) }}</p>
          </div>
          <div>
            <p class="text-xs text-muted">费用</p>
            <p class="mt-2 font-mono text-xl font-semibold">{{ fmtMoney(last7.cost) }}</p>
            <p class="text-xs text-muted">前 7 日 {{ fmtMoney(prev7.cost) }}</p>
          </div>
        </div>
      </div>
    </div>

    <div class="grid gap-4 xl:grid-cols-[1fr_0.72fr]">
      <div class="app-panel p-5">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <h2 class="font-semibold">30 天用量趋势</h2>
          <div class="segmented-control">
            <button v-for="m in chartMetrics" :key="m" type="button" :class="{ active: chartMetric === m }" @click="chartMetric = m">
              {{ m === 'tokens' ? 'Tokens' : m === 'cost' ? '费用' : '请求数' }}
            </button>
          </div>
        </div>
        <DailyBarChart :items="summary" :days="30" :metric="chartMetric" />
      </div>

      <div class="app-panel p-5">
        <h2 class="font-semibold">费用构成</h2>
        <div class="mt-5 grid grid-cols-2 gap-3 text-sm">
          <div class="soft-panel p-3">
            <p class="text-muted">输入 Tokens</p>
            <p class="mt-1 font-mono text-lg font-semibold">{{ fmtTokens(totals.input) }}</p>
          </div>
          <div class="soft-panel p-3">
            <p class="text-muted">输出 Tokens</p>
            <p class="mt-1 font-mono text-lg font-semibold">{{ fmtTokens(totals.output) }}</p>
          </div>
        </div>
        <p class="mt-4 text-xs text-muted">当前用户接口不暴露赠送/付费消耗拆分，前端保持只读展示。</p>
      </div>
    </div>

    <div class="grid gap-4 lg:grid-cols-3">
      <div class="app-panel p-5 lg:col-span-2">
        <div class="mb-4 flex items-center justify-between">
          <h2 class="font-semibold">30 天模型排行榜</h2>
          <router-link class="text-sm text-accent hover:underline" to="/app/usage">更多 →</router-link>
        </div>
        <div class="space-y-3">
          <div v-for="m in byModel.slice(0, 6)" :key="m.model" class="flex items-center gap-3">
            <span class="w-48 truncate font-mono text-xs" :title="m.model">{{ m.model }}</span>
            <div class="h-2 flex-1 overflow-hidden rounded-full bg-line">
              <div class="h-full rounded-full bg-accent" :style="{ width: `${((m.prompt_tokens + m.completion_tokens) / maxModelTokens) * 100}%` }"></div>
            </div>
            <span class="w-20 text-right font-mono text-xs text-muted">{{ fmtTokens(m.prompt_tokens + m.completion_tokens) }}</span>
          </div>
          <p v-if="byModel.length === 0" class="empty-state">暂无数据</p>
        </div>
      </div>

      <div class="app-panel p-5">
        <h2 class="font-semibold">最近最快请求 Top 5</h2>
        <div class="mt-4 space-y-3">
          <div v-for="r in fastest" :key="r.id" class="flex items-center justify-between gap-3 text-sm">
            <span class="truncate font-mono text-xs" :title="r.model">{{ r.model }}</span>
            <span class="font-mono text-muted">{{ r.duration_ms }}ms</span>
          </div>
          <p v-if="fastest.length === 0" class="empty-state">暂无数据</p>
        </div>
      </div>
    </div>
  </div>
</template>
