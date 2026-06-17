<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'
import type { SummaryDay } from '@/components/DailyBarChart.vue'

interface Order {
  id: number
  order_no: string
  provider: string
  amount_usd: string
  pay_amount: string
  pay_currency: string
  status: number
  created_at: number
}

interface LedgerEntry {
  id: number
  kind: string
  delta: string
  balance_after: string
  ref_type: string
  ref_id: string
  reason: string
  created_at: string
}

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
const orders = ref<Order[]>([])
const ledger = ref<LedgerEntry[]>([])
const usage = ref<UsageRow[]>([])
const loading = ref(true)
const error = ref('')
const tab = ref<'monthly' | 'ledger' | 'orders' | 'consumption'>('monthly')
const tabs = ['monthly', 'ledger', 'orders', 'consumption'] as const

const monthRows = computed(() => {
  const map = new Map<string, { period: string; calls: number; tokens: number; cost: number }>()
  for (const row of summary.value) {
    const period = row.day.slice(0, 7)
    const current = map.get(period) ?? { period, calls: 0, tokens: 0, cost: 0 }
    current.calls += row.calls
    current.tokens += row.prompt_tokens + row.completion_tokens
    current.cost += Number(row.cost_usd || 0)
    map.set(period, current)
  }
  return [...map.values()].sort((a, b) => b.period.localeCompare(a.period))
})

const totals = computed(() => ({
  cost: monthRows.value.reduce((sum, row) => sum + row.cost, 0),
  ledger: ledger.value.length,
}))

function statusLabel(status: number): string {
  if (status === 1) return 'paid'
  if (status === 2) return 'failed'
  if (status === 4) return 'expired'
  return 'pending'
}

async function load() {
  loading.value = true
  try {
    const results = await Promise.allSettled([
      api<{ balance: string; held: string }>('GET', '/api/balance'),
      api<{ items: SummaryDay[] }>('GET', '/api/usage/summary?days=90'),
      api<{ orders: Order[] }>('GET', '/api/payment/orders'),
      api<{ items: LedgerEntry[] }>('GET', '/api/ledger'),
      api<{ items: UsageRow[] }>('GET', '/api/usage?page=1&page_size=12'),
    ])
    const [bal, sum, ord, led, use] = results
    if (bal.status === 'fulfilled') {
      balance.value = bal.value.balance
      held.value = bal.value.held
    }
    if (sum.status === 'fulfilled') summary.value = sum.value.items.map((s) => ({ ...s, day: s.day.slice(0, 10) }))
    if (ord.status === 'fulfilled') orders.value = ord.value.orders
    if (led.status === 'fulfilled') ledger.value = led.value.items
    if (use.status === 'fulfilled') usage.value = use.value.items
    const failedCount = results.filter((result) => result.status === 'rejected').length
    error.value = failedCount > 0 ? t('billing.partialError', { count: failedCount }) : ''
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="app-page billing-page">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('surface.billingEyebrow') }}</p>
        <h1 class="app-page-title">{{ t('billing.title') }}</h1>
        <p class="app-page-subtitle">{{ t('billing.subtitle') }}</p>
      </div>
      <router-link to="/app/wallet" class="btn-primary shrink-0">{{ t('billing.topupCta') }}</router-link>
    </div>

    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div class="metric-card">
        <p class="text-xs text-muted">{{ t('billing.available') }}</p>
        <p class="mt-1 font-mono text-2xl font-semibold">${{ balance }}</p>
      </div>
      <div class="metric-card">
        <p class="text-xs text-muted">{{ t('billing.held') }}</p>
        <p class="mt-1 font-mono text-2xl font-semibold">${{ held }}</p>
      </div>
      <div class="metric-card">
        <p class="text-xs text-muted">{{ t('billing.monthSpend') }}</p>
        <p class="mt-1 font-mono text-2xl font-semibold">${{ totals.cost.toFixed(4) }}</p>
      </div>
      <div class="metric-card">
        <p class="text-xs text-muted">{{ t('billing.ledgerRows') }}</p>
        <p class="mt-1 font-mono text-2xl font-semibold">{{ n(totals.ledger) }}</p>
      </div>
    </div>

    <div class="toolbar-panel">
      <div class="seg-control">
        <button v-for="item in tabs" :key="item" type="button" :class="tab === item ? 'is-active' : ''" @click="tab = item">
          {{ t(`billing.${item}`) }}
        </button>
      </div>
      <span class="text-xs text-muted">{{ t(`billing.${tab}Hint`) }}</span>
    </div>

    <p v-if="loading" class="text-sm text-muted">{{ t('common.loading') }}</p>

    <div v-else-if="tab === 'monthly'" class="data-table-wrap">
      <table class="table w-full text-sm">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3 font-medium">{{ t('billing.period') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('billing.calls') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('billing.tokens') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('billing.cost') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="monthRows.length === 0"><td colspan="4" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td></tr>
          <tr v-for="row in monthRows" :key="row.period" class="border-b border-line last:border-0">
            <td class="px-4 py-2.5 font-mono">{{ row.period }}</td>
            <td class="px-4 py-2.5 text-right font-mono">{{ n(row.calls) }}</td>
            <td class="px-4 py-2.5 text-right font-mono">{{ n(row.tokens) }}</td>
            <td class="px-4 py-2.5 text-right font-mono">${{ row.cost.toFixed(4) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else-if="tab === 'ledger'" class="data-table-wrap">
      <table class="table w-full text-sm whitespace-nowrap">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3 font-medium">{{ t('usage.time') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('billing.kind') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('wallet.txDetail') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('billing.delta') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('billing.balanceAfter') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="ledger.length === 0"><td colspan="5" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td></tr>
          <tr v-for="entry in ledger" :key="entry.id" class="border-b border-line last:border-0">
            <td class="px-4 py-2.5 text-muted">{{ d(new Date(entry.created_at), { dateStyle: 'short', timeStyle: 'short' }) }}</td>
            <td class="px-4 py-2.5"><span class="badge-muted">{{ entry.kind }}</span></td>
            <td class="px-4 py-2.5 text-muted">{{ entry.reason || entry.ref_type || '—' }}</td>
            <td class="px-4 py-2.5 text-right font-mono" :class="Number(entry.delta) >= 0 ? 'text-ok' : 'text-danger'">{{ Number(entry.delta) >= 0 ? '+' : '' }}{{ entry.delta }}</td>
            <td class="px-4 py-2.5 text-right font-mono text-muted">${{ entry.balance_after }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else-if="tab === 'orders'" class="data-table-wrap">
      <table class="table w-full text-sm">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3 font-medium">{{ t('billing.order') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('billing.provider') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('billing.amount') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('billing.status') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('usage.time') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="orders.length === 0"><td colspan="5" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td></tr>
          <tr v-for="order in orders" :key="order.id" class="border-b border-line last:border-0">
            <td class="px-4 py-2.5 font-mono text-xs">{{ order.order_no }}</td>
            <td class="px-4 py-2.5">{{ order.provider }}</td>
            <td class="px-4 py-2.5 text-right font-mono">${{ order.amount_usd }}</td>
            <td class="px-4 py-2.5"><span class="badge-muted">{{ statusLabel(order.status) }}</span></td>
            <td class="px-4 py-2.5 text-muted">{{ d(new Date(order.created_at * 1000), { dateStyle: 'short', timeStyle: 'short' }) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else class="data-table-wrap">
      <table class="table w-full text-sm whitespace-nowrap">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3 font-medium">{{ t('usage.time') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('billing.model') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('billing.tokens') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('billing.cost') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('billing.requestId') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="usage.length === 0"><td colspan="5" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td></tr>
          <tr v-for="row in usage" :key="row.id" class="border-b border-line last:border-0">
            <td class="px-4 py-2.5 text-muted">{{ d(new Date(row.created_at), { dateStyle: 'short', timeStyle: 'short' }) }}</td>
            <td class="px-4 py-2.5 font-mono text-xs">{{ row.model }}</td>
            <td class="px-4 py-2.5 text-right font-mono">{{ n(row.prompt_tokens + row.completion_tokens) }}</td>
            <td class="px-4 py-2.5 text-right font-mono">${{ Number(row.cost_usd).toFixed(4) }}</td>
            <td class="max-w-72 truncate px-4 py-2.5 font-mono text-xs text-muted" :title="row.request_id">{{ row.request_id }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
