<script setup lang="ts">
import { fmtUsd, addMoney, displayCurrencyCode } from '@/lib/money'
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import DetailDrawer from '@/components/DetailDrawer.vue'
import { useAuth } from '@/stores/auth'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'
import type { SummaryDay } from '@/components/DailyBarChart.vue'

interface LedgerEntry {
  id: number
  kind: string
  delta: string
  balance_after: string
  reason: string
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
const auth = useAuth()
const detailKind = ref<'monthly' | 'ledger' | null>(null), selectedLedger = ref<LedgerEntry | null>(null)
let loadEpoch = 0
function closeDetail() { detailKind.value = null; selectedLedger.value = null }
const activeTab = ref<'monthly' | 'ledger'>('monthly') // usage 明细并入请求日志页，此处不再重复
const balance = ref('0')
const held = ref('0')
const summary = ref<SummaryDay[]>([])
const ledger = ref<LedgerEntry[]>([])
const orders = ref<Order[]>([])
const monthRechargeUSD = ref('0')
const ledgerTotal = ref(0)
const loading = ref(true)
const balanceLoading = ref(true), balanceError = ref('')

function num(v: string | number): number {
  const out = Number(v)
  return Number.isFinite(out) ? out : 0
}
const monthSpend = computed(() => addMoney(...summary.value.map(s => s.cost_usd)))
const monthTokens = computed(() => summary.value.reduce((sum, s) => sum + s.prompt_tokens + s.completion_tokens, 0))
const monthCalls = computed(() => summary.value.reduce((sum, s) => sum + s.calls, 0))
// 本月充值用服务端 UTC 自然月聚合（/api/payment/recharge-stats month_usd），与仪表盘同源；
// 不再对 /api/payment/orders（后端 Limit 50、且无时间窗）本地求和——那既会截断漏算、又会把上月计入本月。
const paidThisMonth = computed(() => monthRechargeUSD.value)
const pendingOrders = computed(() => orders.value.filter((o) => o.status === 0).length)
const monthLabel = computed(() => new Date().toISOString().slice(0, 7))

async function load() {
  const epoch = ++loadEpoch, org = auth.orgId
  loading.value = true
  balanceLoading.value = true; balanceError.value = ''
  // 本月消耗按「本月至今」口径：summary 取当前 UTC 月已过天数（与 monthLabel 的 YYYY-MM 自然月一致），
  // 不再用滚动 31 天冒充自然月。本月充值用服务端 UTC 自然月聚合 month_usd。
  const mtdDays = new Date().getUTCDate()
  const [, sum, lg, ord, rs] = await Promise.allSettled([
    api<{ balance: string; held: string }>('GET', '/api/balance')
      .then(value => { if (epoch === loadEpoch && org === auth.orgId) { balance.value = value.balance; held.value = value.held } })
      .catch(e => { if (epoch === loadEpoch && org === auth.orgId) balanceError.value = errorMessage(e, t('usagex.loadFailed')) })
      .finally(() => { if (epoch === loadEpoch && org === auth.orgId) balanceLoading.value = false }),
    api<{ items: SummaryDay[] }>('GET', `/api/usage/summary?days=${mtdDays}`),
    api<{ total: number; items: LedgerEntry[] }>('GET', '/api/ledger'),
    api<{ orders: Order[] }>('GET', '/api/payment/orders'),
    api<{ month_usd: string }>('GET', '/api/payment/recharge-stats'),
  ])
  if (epoch !== loadEpoch || org !== auth.orgId) return
  if (sum.status === 'fulfilled') summary.value = (sum.value.items ?? []).map((s) => ({ ...s, day: s.day.slice(0, 10) }))
  if (lg.status === 'fulfilled') {
    ledger.value = lg.value.items ?? []
    ledgerTotal.value = lg.value.total
  }
  if (ord.status === 'fulfilled') orders.value = ord.value.orders ?? []
  if (rs.status === 'fulfilled') monthRechargeUSD.value = rs.value.month_usd
  loading.value = false
}
watch(() => auth.orgId, () => { closeDetail(); summary.value = []; ledger.value = []; orders.value = []; balance.value = held.value = '0'; monthRechargeUSD.value = '0'; ledgerTotal.value = 0; void load() })
watch(activeTab, closeDetail)
onBeforeUnmount(() => { loadEpoch++; closeDetail() })
onMounted(load)
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
        <p class="mt-3 font-mono text-2xl font-semibold">{{ balanceLoading ? '…' : balanceError ? '—' : fmtUsd(balance) }}</p>
        <p v-if="!balanceLoading && !balanceError" class="mt-2 text-xs text-muted">{{ t('billx.cardHeld', { v: fmtUsd(held) }) }}</p>
        <p v-if="balanceError" role="alert" class="mt-2 text-xs text-danger">{{ balanceError }} <button type="button" class="table-link" @click="load">{{ t('common.retry') }}</button></p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-sky"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.25 8.25h19.5M2.25 9h19.5m-16.5 5.25h6m-6 2.25h3m-3.75 3h15a2.25 2.25 0 0 0 2.25-2.25V6.75A2.25 2.25 0 0 0 19.5 4.5h-15a2.25 2.25 0 0 0-2.25 2.25v10.5A2.25 2.25 0 0 0 4.5 19.5Z" /></svg></span>
          <p class="text-xs text-muted">{{ t('billx.cardTopUp') }}</p>
        </div>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ fmtUsd(paidThisMonth) }}</p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-amber"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v12m-3-2.818.879.659c1.171.879 3.07.879 4.242 0 1.172-.879 1.172-2.303 0-3.182C13.536 12.219 12.768 12 12 12c-.725 0-1.45-.22-2.003-.659-1.106-.879-1.106-2.303 0-3.182s2.9-.879 4.006 0l.415.33M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span>
          <p class="text-xs text-muted">{{ t('billx.cardSpend') }}</p>
        </div>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ fmtUsd(monthSpend) }}</p>
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
              <th class="px-4 py-3 text-right">{{ t('billx.colRequests') }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colTokens') }}</th>
              <th class="px-4 py-3">{{ t('billx.colStatus') }}</th><th class="px-4 py-3">{{ t('reqlog.btnDetail') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td class="px-4 py-2.5 font-mono">{{ monthLabel }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtUsd(monthSpend) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(monthCalls) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(monthTokens) }}</td>
              <td class="px-4 py-2.5"><span class="badge-ok">{{ t('billx.statusCounted') }}</span></td><td class="px-4 py-2.5"><button type="button" class="table-link" :disabled="loading" @click="detailKind = 'monthly'">{{ t('reqlog.btnDetail') }}</button></td>
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
              <th class="px-4 py-3 text-right">{{ t('billx.colUSDDelta', { currency: displayCurrencyCode() }) }}</th>
              <th class="px-4 py-3 text-right">{{ t('billx.colBalanceAfter') }}</th><th class="px-4 py-3">{{ t('reqlog.btnDetail') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="ledger.length === 0"><td colspan="5" class="px-4 py-8 text-center text-muted">{{ t('billx.emptyData') }}</td></tr>
            <tr v-for="e in ledger" :key="e.id">
              <td class="px-4 py-2.5 text-muted">{{ d(new Date(e.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
              <td class="px-4 py-2.5"><span class="badge-muted">{{ e.kind }}</span></td>
              <td class="px-4 py-2.5 text-right font-mono" :class="num(e.delta) >= 0 ? 'text-ok' : 'text-danger'">{{ fmtUsd(e.delta, { signed: true }) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtUsd(e.balance_after) }}</td><td class="px-4 py-2.5"><button type="button" class="table-link" @click="selectedLedger = e; detailKind = 'ledger'">{{ t('reqlog.btnDetail') }}</button></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <DetailDrawer :open="!!detailKind" :title="t(detailKind === 'monthly' ? 'billx.tabMonthly' : 'billx.tabLedger')" @update:open="open => { if (!open) closeDetail() }">
      <template v-if="detailKind === 'monthly'"><p class="mb-5 text-sm text-muted">{{ t('billx.monthlyHint') }}</p><dl class="detail-grid"><div><dt>{{ t('billx.colPeriod') }}</dt><dd>{{ monthLabel }}</dd></div><div><dt>{{ t('billx.colTotalSpend') }}</dt><dd>{{ fmtUsd(monthSpend) }}</dd></div><div><dt>{{ t('billx.colRequests') }}</dt><dd>{{ n(monthCalls) }}</dd></div><div><dt>{{ t('billx.colTokens') }}</dt><dd>{{ n(monthTokens) }}</dd></div></dl></template>
      <dl v-else-if="selectedLedger" class="detail-grid"><div><dt>ID</dt><dd>{{ selectedLedger.id }}</dd></div><div><dt>{{ t('billx.colTime') }}</dt><dd>{{ d(new Date(selectedLedger.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</dd></div><div><dt>{{ t('billx.colKind') }}</dt><dd>{{ selectedLedger.kind }}</dd></div><div><dt>{{ t('billx.colUSDDelta', { currency: displayCurrencyCode() }) }}</dt><dd>{{ fmtUsd(selectedLedger.delta, { signed: true }) }}</dd></div><div><dt>{{ t('billx.colBalanceAfter') }}</dt><dd>{{ fmtUsd(selectedLedger.balance_after) }}</dd></div><div class="col-span-2"><dt>{{ t('admin.crReason') }}</dt><dd>{{ selectedLedger.reason || '—' }}</dd></div></dl>
    </DetailDrawer>
  </div>
</template>
