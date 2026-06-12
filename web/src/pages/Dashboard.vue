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
  created_at: string
}

const { t, n, d } = useI18n()

const balance = ref('0')
const held = ref('0')
const summary = ref<SummaryDay[]>([])
const summaryDays = 14
const recent = ref<UsageRow[]>([])

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
const todayCalls = computed(() => {
  const key = new Date().toISOString().slice(0, 10)
  return summary.value.find((s) => s.day === key)?.calls ?? 0
})

onMounted(async () => {
  // 三路并行；任一失败不阻塞其余卡片（各自兜底为零值）
  const [bal, sum, use] = await Promise.allSettled([
    api<{ balance: string; held: string }>('GET', '/api/balance'),
    api<{ items: SummaryDay[] }>('GET', `/api/usage/summary?days=${summaryDays}`),
    api<{ items: UsageRow[] }>('GET', '/api/usage?page=1&page_size=5'),
  ])
  if (bal.status === 'fulfilled') {
    balance.value = bal.value.balance
    held.value = bal.value.held
  }
  if (sum.status === 'fulfilled') {
    summary.value = sum.value.items.map((s) => ({ ...s, day: s.day.slice(0, 10) }))
  }
  if (use.status === 'fulfilled') recent.value = use.value.items
})
</script>

<template>
  <div class="space-y-6">
    <h1 class="text-2xl font-semibold tracking-tight">{{ t('nav.dashboard') }}</h1>

    <!-- 余额主卡 + 统计卡 -->
    <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <div
        class="card relative overflow-hidden p-5 text-white"
        style="background: linear-gradient(135deg, rgb(var(--c-accent)), rgb(var(--c-accent-2)))"
      >
        <p class="text-xs opacity-80">{{ t('dashboard.balance') }}</p>
        <p class="mt-1 text-3xl font-semibold font-mono">${{ balance }}</p>
        <p v-if="held !== '0'" class="mt-1 text-xs opacity-80">
          {{ t('dashboard.held') }}: ${{ held }}
        </p>
      </div>
      <div class="card p-5">
        <p class="text-xs text-muted">{{ t('dashboard.todayCalls') }}</p>
        <p class="mt-1 text-3xl font-semibold font-mono">{{ n(todayCalls) }}</p>
      </div>
      <div class="card p-5">
        <p class="text-xs text-muted">{{ t('usage.sumCalls', { days: summaryDays }) }}</p>
        <p class="mt-1 text-3xl font-semibold font-mono">{{ n(totals.calls) }}</p>
      </div>
      <div class="card p-5">
        <p class="text-xs text-muted">{{ t('usage.sumCost', { days: summaryDays }) }}</p>
        <p class="mt-1 text-3xl font-semibold font-mono">${{ totals.cost.toFixed(4) }}</p>
      </div>
    </div>

    <DailyBarChart :items="summary" :days="summaryDays" />

    <!-- 最近调用 -->
    <div class="space-y-3">
      <div class="flex items-center justify-between">
        <h2 class="font-semibold">{{ t('dashboard.recent') }}</h2>
        <router-link class="text-sm text-accent hover:underline" to="/usage">
          {{ t('usage.title') }} →
        </router-link>
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
              <td class="px-4 py-2.5 text-muted">
                {{ d(new Date(r.created_at), { dateStyle: 'short', timeStyle: 'short' }) }}
              </td>
              <td class="px-4 py-2.5 font-mono">{{ r.model }}</td>
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
</template>
