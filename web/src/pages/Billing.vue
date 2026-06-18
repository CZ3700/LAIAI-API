<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'
import type { SummaryDay } from '@/components/DailyBarChart.vue'

interface LedgerEntry {
  id: number
  kind: string
  delta: string
  balance_after: string
  reason: string
  created_at: string
}
interface UsageRow {
  id: number
  model: string
  status: string
  prompt_tokens: number
  completion_tokens: number
  cost_usd: string
  created_at: string
}
interface Order {
  id: number
  order_no: string
  amount_usd: string
  status: number
  created_at: number
}

const { t, n, d } = useI18n()
const activeTab = ref<'monthly' | 'ledger' | 'usage'>('monthly')
const balance = ref('0')
const held = ref('0')
const summary = ref<SummaryDay[]>([])
const ledger = ref<LedgerEntry[]>([])
const usage = ref<UsageRow[]>([])
const orders = ref<Order[]>([])
const monthRechargeUSD = ref(0)
const ledgerTotal = ref(0)
const usageTotal = ref(0)
const loading = ref(true)

function num(v: string | number): number {
  const out = Number(v)
  return Number.isFinite(out) ? out : 0
}
function fmtMoney(v: number): string {
  return `$${v.toFixed(v >= 1 ? 2 : 4)}`
}
const monthSpend = computed(() => summary.value.reduce((sum, s) => sum + num(s.cost_usd), 0))
const monthTokens = computed(() => summary.value.reduce((sum, s) => sum + s.prompt_tokens + s.completion_tokens, 0))
const monthCalls = computed(() => summary.value.reduce((sum, s) => sum + s.calls, 0))
// 本月充值用服务端 UTC 自然月聚合（/api/payment/recharge-stats month_usd），与仪表盘同源；
// 不再对 /api/payment/orders（后端 Limit 50、且无时间窗）本地求和——那既会截断漏算、又会把上月计入本月。
const paidThisMonth = computed(() => monthRechargeUSD.value)
const pendingOrders = computed(() => orders.value.filter((o) => o.status === 0).length)
const monthLabel = computed(() => new Date().toISOString().slice(0, 7))

onMounted(async () => {
  loading.value = true
  // 本月消耗按「本月至今」口径：summary 取当前 UTC 月已过天数（与 monthLabel 的 YYYY-MM 自然月一致），
  // 不再用滚动 31 天冒充自然月。本月充值用服务端 UTC 自然月聚合 month_usd。
  const mtdDays = new Date().getUTCDate()
  const [bal, sum, lg, use, ord, rs] = await Promise.allSettled([
    api<{ balance: string; held: string }>('GET', '/api/balance'),
    api<{ items: SummaryDay[] }>('GET', `/api/usage/summary?days=${mtdDays}`),
    api<{ total: number; items: LedgerEntry[] }>('GET', '/api/ledger'),
    api<{ total: number; items: UsageRow[] }>('GET', '/api/usage?page=1&page_size=20'),
    api<{ orders: Order[] }>('GET', '/api/payment/orders'),
    api<{ month_usd: string }>('GET', '/api/payment/recharge-stats'),
  ])
  if (bal.status === 'fulfilled') {
    balance.value = bal.value.balance
    held.value = bal.value.held
  }
  if (sum.status === 'fulfilled') summary.value = sum.value.items.map((s) => ({ ...s, day: s.day.slice(0, 10) }))
  if (lg.status === 'fulfilled') {
    ledger.value = lg.value.items
    ledgerTotal.value = lg.value.total
  }
  if (use.status === 'fulfilled') {
    usage.value = use.value.items
    usageTotal.value = use.value.total
  }
  if (ord.status === 'fulfilled') orders.value = ord.value.orders
  if (rs.status === 'fulfilled') monthRechargeUSD.value = num(rs.value.month_usd)
  loading.value = false
})
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <h1 class="app-page-title">{{ t('billx.title') }}</h1>
        <p class="app-page-subtitle">{{ t('billx.subtitle') }}</p>
      </div>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-emerald"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12a2.25 2.25 0 0 0-2.25-2.25H15a3 3 0 1 1-6 0H5.25A2.25 2.25 0 0 0 3 12m18 0v6a2.25 2.25 0 0 1-2.25 2.25H5.25A2.25 2.25 0 0 1 3 18v-6m18 0V9M3 12V9m18 0a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 9m18 0V6a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 6v3" /></svg></span>
          <p class="text-xs text-muted">{{ t('billx.cardBalance') }}</p>
        </div>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ loading ? '…' : fmtMoney(num(balance)) }}</p>
        <p class="mt-2 text-xs text-muted">{{ t('billx.cardHeld', { v: fmtMoney(num(held)) }) }}</p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-sky"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.25 8.25h19.5M2.25 9h19.5m-16.5 5.25h6m-6 2.25h3m-3.75 3h15a2.25 2.25 0 0 0 2.25-2.25V6.75A2.25 2.25 0 0 0 19.5 4.5h-15a2.25 2.25 0 0 0-2.25 2.25v10.5A2.25 2.25 0 0 0 4.5 19.5Z" /></svg></span>
          <p class="text-xs text-muted">{{ t('billx.cardTopUp') }}</p>
        </div>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ fmtMoney(paidThisMonth) }}</p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-amber"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v12m-3-2.818.879.659c1.171.879 3.07.879 4.242 0 1.172-.879 1.172-2.303 0-3.182C13.536 12.219 12.768 12 12 12c-.725 0-1.45-.22-2.003-.659-1.106-.879-1.106-2.303 0-3.182s2.9-.879 4.006 0l.415.33M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span>
          <p class="text-xs text-muted">{{ t('billx.cardSpend') }}</p>
        </div>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ fmtMoney(monthSpend) }}</p>
      </div>
      <router-link to="/app/recharges" class="metric-card block">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-rose"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span>
          <p class="text-xs text-muted">{{ t('billx.cardPending') }}</p>
        </div>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ t('billx.unitOrders', { n: pendingOrders }) }}</p>
        <p class="mt-2 text-xs text-muted">{{ t('billx.viewRecharges') }} →</p>
      </router-link>
    </div>

    <div class="border-b border-line">
      <div class="tabs-row">
        <button type="button" :class="{ active: activeTab === 'monthly' }" @click="activeTab = 'monthly'">{{ t('billx.tabMonthly') }}</button>
        <button type="button" :class="{ active: activeTab === 'ledger' }" @click="activeTab = 'ledger'">{{ t('billx.tabLedger') }}</button>
        <button type="button" :class="{ active: activeTab === 'usage' }" @click="activeTab = 'usage'">{{ t('billx.tabUsage') }}</button>
      </div>
    </div>

    <div v-if="activeTab === 'monthly'" class="space-y-3">
      <p class="text-sm text-muted">{{ t('billx.monthlyHint') }}</p>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3">{{ t('billx.colPeriod') }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colTotalSpend') }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colGiftSpend') }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colPaidSpend') }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colRequests') }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colTokens') }}</th>
              <th class="px-4 py-3">{{ t('billx.colStatus') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td class="px-4 py-2.5 font-mono">{{ monthLabel }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtMoney(monthSpend) }}</td>
              <td class="px-4 py-2.5 text-right font-mono text-muted">—</td>
              <td class="px-4 py-2.5 text-right font-mono text-muted">—</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(monthCalls) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(monthTokens) }}</td>
              <td class="px-4 py-2.5"><span class="badge-ok">{{ t('billx.statusCounted') }}</span></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-else-if="activeTab === 'ledger'" class="space-y-3">
      <p class="text-sm text-muted">{{ t('billx.ledgerCount', { n: ledgerTotal }) }}</p>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3">{{ t('billx.colTime') }}</th>
              <th class="px-4 py-3">{{ t('billx.colKind') }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colUSDDelta') }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colGiftDelta') }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colBalanceAfter') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="ledger.length === 0"><td colspan="5" class="px-4 py-8 text-center text-muted">{{ t('billx.emptyData') }}</td></tr>
            <tr v-for="e in ledger" :key="e.id">
              <td class="px-4 py-2.5 text-muted">{{ d(new Date(e.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
              <td class="px-4 py-2.5"><span class="badge-muted">{{ e.kind }}</span></td>
              <td class="px-4 py-2.5 text-right font-mono" :class="num(e.delta) >= 0 ? 'text-ok' : 'text-danger'">{{ num(e.delta) >= 0 ? '+' : '-' }}{{ fmtMoney(Math.abs(num(e.delta))) }}</td>
              <td class="px-4 py-2.5 text-right font-mono text-muted">—</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtMoney(num(e.balance_after)) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-else class="space-y-3">
      <p class="text-sm text-muted">{{ t('billx.usageCount', { n: usageTotal }) }}</p>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3">{{ t('billx.colTime') }}</th>
              <th class="px-4 py-3">{{ t('billx.colModel') }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colTokens') }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colSpend') }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colGiftSpend') }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colPaidSpend') }}</th>
              <th class="px-4 py-3">{{ t('billx.colStatus') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="usage.length === 0"><td colspan="7" class="px-4 py-8 text-center text-muted">{{ t('billx.emptyData') }}</td></tr>
            <tr v-for="r in usage" :key="r.id">
              <td class="px-4 py-2.5 text-muted">{{ d(new Date(r.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
              <td class="px-4 py-2.5 font-mono text-xs">{{ r.model }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(r.prompt_tokens + r.completion_tokens) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtMoney(num(r.cost_usd)) }}</td>
              <td class="px-4 py-2.5 text-right font-mono text-muted">—</td>
              <td class="px-4 py-2.5 text-right font-mono text-muted">—</td>
              <td class="px-4 py-2.5"><span :class="r.status === 'success' ? 'badge-ok' : 'badge-danger'">{{ r.status }}</span></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
