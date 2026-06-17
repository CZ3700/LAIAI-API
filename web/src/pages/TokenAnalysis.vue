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

const { n } = useI18n()
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
        <h1 class="app-page-title">Tokens 分析</h1>
      </div>
      <button class="btn-primary" type="button" @click="profileNotice = '用户画像接口尚未接入；当前仅展示可由用量接口计算的聚合指标。'">用户画像</button>
    </div>

    <p v-if="profileNotice" class="inline-success">{{ profileNotice }}</p>

    <div class="toolbar-panel items-end">
      <div class="filter-group min-w-[160px]">
        <label>筛选密钥标签</label>
        <input v-model="tag" class="input" placeholder="输入标签筛选..." />
      </div>
      <div class="filter-group min-w-[160px]">
        <label>筛选密钥</label>
        <select class="input">
          <option>全部密钥</option>
          <option v-for="k in keys" :key="k.id">{{ k.name || k.prefix }}</option>
        </select>
      </div>
      <div class="filter-group">
        <label>时间范围</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: days === 1 }" @click="days = 1; load()">今天</button>
          <button type="button" :class="{ active: days === 7 }" @click="days = 7; load()">近 7 天</button>
          <button type="button" :class="{ active: days === 31 }" @click="days = 31; load()">本月</button>
          <button type="button" disabled>自定义</button>
        </div>
      </div>
      <div class="filter-group min-w-[160px]">
        <label>筛选x-user-id</label>
        <input v-model="userId" class="input" placeholder="搜索用户ID..." />
      </div>
      <div class="filter-group min-w-[160px]">
        <label>筛选x-agent-name</label>
        <input v-model="agent" class="input" placeholder="搜索Agent..." />
      </div>
      <button class="btn-primary !min-h-0 !px-4 !py-2" type="button" @click="load">查 询</button>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div class="metric-card"><p class="text-xs text-muted">Tokens 总量</p><p class="mt-2 font-mono text-2xl font-semibold">{{ fmtTokens(totals.tokens) }}</p><p class="text-xs text-muted">所选范围内总输出</p></div>
      <div class="metric-card"><p class="text-xs text-muted">预估费用</p><p class="mt-2 font-mono text-2xl font-semibold">{{ fmtMoney(totals.cost) }}</p><p class="text-xs text-muted">按系统计费标准折算</p></div>
      <div class="metric-card"><p class="text-xs text-muted">请求总数</p><p class="mt-2 font-mono text-2xl font-semibold">{{ n(totals.calls) }}</p><p class="text-xs text-muted">所选范围内总请求</p></div>
      <div class="metric-card"><p class="text-xs text-muted">平均 Tokens/请求</p><p class="mt-2 font-mono text-2xl font-semibold">{{ n(Math.round(totals.avg)) }}</p><p class="text-xs text-muted">单次请求均值</p></div>
    </div>

    <div class="app-panel p-5">
      <div class="mb-4 flex items-center justify-between gap-3">
        <h2 class="font-semibold">用量趋势</h2>
        <div class="flex gap-2">
          <div class="segmented-control"><button :class="{ active: grain === 'day' }" @click="grain = 'day'">按天</button><button :class="{ active: grain === 'hour' }" @click="grain = 'hour'">按小时</button></div>
          <div class="segmented-control"><button :class="{ active: metric === 'tokens' }" @click="metric = 'tokens'">Tokens</button><button :class="{ active: metric === 'cost' }" @click="metric = 'cost'">Credits</button><button :class="{ active: metric === 'calls' }" @click="metric = 'calls'">请求量</button></div>
        </div>
      </div>
      <DailyBarChart :items="summary" :days="days" :metric="metric" />
    </div>

    <div class="grid gap-4 lg:grid-cols-2">
      <div class="app-panel p-5">
        <h2 class="font-semibold">标签用量排行</h2>
        <p class="mt-2 text-xs text-muted">排名范围：当前所选时间与筛选</p>
        <div class="mt-4 flex items-center justify-between text-sm">
          <span>未分类密钥</span>
          <span class="font-mono">{{ fmtTokens(totals.tokens) }}</span>
        </div>
      </div>
      <div class="app-panel p-5">
        <h2 class="font-semibold">密钥用量排行</h2>
        <p class="mt-2 text-xs text-muted">排名范围：当前所选时间与筛选</p>
        <div class="mt-4 space-y-2">
          <div v-for="k in keys" :key="k.id" class="flex items-center justify-between text-sm">
            <span>{{ k.name || k.prefix }}</span>
            <span class="font-mono text-muted">—</span>
          </div>
          <p v-if="keys.length === 0" class="text-sm text-muted">暂无密钥数据</p>
        </div>
      </div>
    </div>
  </div>
</template>
