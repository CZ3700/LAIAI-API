<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'
import DailyBarChart, { type SummaryDay } from '@/components/DailyBarChart.vue'

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

const { t, n, d } = useI18n()

const balance = ref('0')
const held = ref('0')
const summary = ref<SummaryDay[]>([])
const byModel = ref<ModelStat[]>([])
const recent = ref<UsageRow[]>([])
const summaryDays = 14

const totals = computed(() => {
  let calls = 0
  let succeeded = 0
  let inTok = 0
  let outTok = 0
  let cost = 0
  for (const s of summary.value) {
    calls += s.calls
    succeeded += s.succeeded
    inTok += s.prompt_tokens
    outTok += s.completion_tokens
    cost += Number(s.cost_usd)
  }
  return { calls, succeeded, inTok, outTok, cost, rate: calls ? succeeded / calls : 1 }
})
const todayCalls = computed(() => {
  const key = new Date().toISOString().slice(0, 10)
  return summary.value.find((s) => s.day === key)?.calls ?? 0
})
const maxModelCalls = computed(() => Math.max(1, ...byModel.value.map((m) => m.calls)))

function fmtTokens(x: number): string {
  if (x >= 1_000_000) return (x / 1_000_000).toFixed(2) + 'M'
  if (x >= 1_000) return (x / 1_000).toFixed(1) + 'K'
  return String(x)
}

onMounted(async () => {
  const [bal, sum, bm, use] = await Promise.allSettled([
    api<{ balance: string; held: string }>('GET', '/api/balance'),
    api<{ items: SummaryDay[] }>('GET', `/api/usage/summary?days=${summaryDays}`),
    api<{ items: ModelStat[] }>('GET', `/api/usage/by-model?days=${summaryDays}`),
    api<{ items: UsageRow[] }>('GET', '/api/usage?page=1&page_size=8'),
  ])
  if (bal.status === 'fulfilled') {
    balance.value = bal.value.balance
    held.value = bal.value.held
  }
  if (sum.status === 'fulfilled') summary.value = sum.value.items.map((s) => ({ ...s, day: s.day.slice(0, 10) }))
  if (bm.status === 'fulfilled') byModel.value = bm.value.items
  if (use.status === 'fulfilled') recent.value = use.value.items
})
</script>

<template>
  <div class="space-y-6">
    <h1 class="text-2xl font-semibold tracking-tight">{{ t('nav.dashboard') }}</h1>

    <!-- 主统计：余额 + 调用 + token + 费用 -->
    <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <div
        class="enter card relative overflow-hidden p-5 text-white"
        style="background: linear-gradient(135deg, rgb(var(--c-accent)), rgb(var(--c-accent-2))); animation-delay: 0ms"
      >
        <p class="text-xs opacity-80">{{ t('dashboard.balance') }}</p>
        <p class="mt-1 text-3xl font-semibold font-mono">${{ balance }}</p>
        <p v-if="held !== '0'" class="mt-1 text-xs opacity-80">{{ t('dashboard.held') }}: ${{ held }}</p>
        <router-link to="/app/wallet" class="mt-2 inline-block text-xs underline opacity-90 hover:opacity-100">
          {{ t('wallet.topup') }} →
        </router-link>
      </div>
      <div class="enter card p-5" style="animation-delay: 70ms">
        <p class="text-xs text-muted">{{ t('dashboard.todayCalls') }}</p>
        <p class="mt-1 text-3xl font-semibold font-mono">{{ n(todayCalls) }}</p>
        <p class="mt-1 text-xs text-muted">{{ t('admin.ovWindowCalls', { n: totals.calls, days: summaryDays }) }}</p>
      </div>
      <div class="enter card p-5" style="animation-delay: 140ms">
        <p class="text-xs text-muted">{{ t('dashboard.tokens') }}</p>
        <p class="mt-1 text-3xl font-semibold font-mono">{{ fmtTokens(totals.inTok + totals.outTok) }}</p>
        <p class="mt-1 text-xs text-muted">{{ fmtTokens(totals.inTok) }} → / {{ fmtTokens(totals.outTok) }} ←</p>
      </div>
      <div class="enter card p-5" style="animation-delay: 210ms">
        <p class="text-xs text-muted">{{ t('usage.sumCost', { days: summaryDays }) }}</p>
        <p class="mt-1 text-3xl font-semibold font-mono">${{ totals.cost.toFixed(4) }}</p>
        <p class="mt-1 text-xs" :class="totals.rate >= 0.95 ? 'text-ok' : 'text-danger'">
          {{ t('dashboard.successRate', { rate: (totals.rate * 100).toFixed(1) }) }}
        </p>
      </div>
    </div>

    <DailyBarChart :items="summary" :days="summaryDays" />

    <div class="grid gap-6 lg:grid-cols-2">
      <!-- 模型分布 -->
      <div class="space-y-3">
        <h2 class="font-semibold">{{ t('dashboard.byModel') }}</h2>
        <div class="card overflow-hidden">
          <table class="table w-full text-sm">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">{{ t('usage.model') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.ovCalls') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('dashboard.tokens') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('usage.cost') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="byModel.length === 0">
                <td colspan="4" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <tr v-for="m in byModel" :key="m.model">
                <td class="px-4 py-2.5">
                  <div class="font-mono text-xs">{{ m.model }}</div>
                  <div class="mt-1 h-1 rounded-full bg-line">
                    <div
                      class="h-full rounded-full"
                      style="background: linear-gradient(90deg, rgb(var(--c-accent)), rgb(var(--c-accent-2)))"
                      :style="{ width: `${(m.calls / maxModelCalls) * 100}%` }"
                    ></div>
                  </div>
                </td>
                <td class="px-4 py-2.5 text-right font-mono align-top">{{ n(m.calls) }}</td>
                <td class="px-4 py-2.5 text-right font-mono align-top text-muted">
                  {{ fmtTokens(m.prompt_tokens + m.completion_tokens) }}
                </td>
                <td class="px-4 py-2.5 text-right font-mono align-top">${{ Number(m.cost_usd).toFixed(4) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 最近调用 -->
      <div class="space-y-3">
        <div class="flex items-center justify-between">
          <h2 class="font-semibold">{{ t('dashboard.recent') }}</h2>
          <router-link class="text-sm text-accent hover:underline" to="/app/usage">{{ t('usage.title') }} →</router-link>
        </div>
        <div class="card overflow-x-auto">
          <table class="table w-full text-sm whitespace-nowrap">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">{{ t('usage.time') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('usage.model') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('usage.status') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('usage.cost') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="recent.length === 0">
                <td colspan="4" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <tr v-for="r in recent" :key="r.id">
                <td class="px-4 py-2.5 text-muted">{{ d(new Date(r.created_at), { dateStyle: 'short', timeStyle: 'short' }) }}</td>
                <td class="px-4 py-2.5 font-mono text-xs">{{ r.model }}</td>
                <td class="px-4 py-2.5">
                  <span :class="r.status === 'success' ? 'badge-ok' : 'badge-danger'">{{ r.status }}</span>
                </td>
                <td class="px-4 py-2.5 text-right font-mono">{{ r.cost_usd }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>
