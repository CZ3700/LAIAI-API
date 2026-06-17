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

const { n, d } = useI18n()
const activeTab = ref<'monthly' | 'ledger' | 'usage'>('monthly')
const balance = ref('0')
const held = ref('0')
const summary = ref<SummaryDay[]>([])
const ledger = ref<LedgerEntry[]>([])
const usage = ref<UsageRow[]>([])
const orders = ref<Order[]>([])
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
const paidThisMonth = computed(() => orders.value.filter((o) => o.status === 1).reduce((sum, o) => sum + num(o.amount_usd), 0))
const pendingOrders = computed(() => orders.value.filter((o) => o.status === 0).length)
const monthLabel = computed(() => new Date().toISOString().slice(0, 7))

onMounted(async () => {
  loading.value = true
  const [bal, sum, lg, use, ord] = await Promise.allSettled([
    api<{ balance: string; held: string }>('GET', '/api/balance'),
    api<{ items: SummaryDay[] }>('GET', '/api/usage/summary?days=31'),
    api<{ items: LedgerEntry[] }>('GET', '/api/ledger'),
    api<{ items: UsageRow[] }>('GET', '/api/usage?page=1&page_size=20'),
    api<{ orders: Order[] }>('GET', '/api/payment/orders'),
  ])
  if (bal.status === 'fulfilled') {
    balance.value = bal.value.balance
    held.value = bal.value.held
  }
  if (sum.status === 'fulfilled') summary.value = sum.value.items.map((s) => ({ ...s, day: s.day.slice(0, 10) }))
  if (lg.status === 'fulfilled') ledger.value = lg.value.items
  if (use.status === 'fulfilled') usage.value = use.value.items
  if (ord.status === 'fulfilled') orders.value = ord.value.orders
  loading.value = false
})
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <h1 class="app-page-title">账单与余额</h1>
        <p class="app-page-subtitle">查看当前余额、冻结金额、账本流水和最近调用消费明细。</p>
      </div>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div class="metric-card">
        <p class="text-xs text-muted">账户余额</p>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ loading ? '…' : fmtMoney(num(balance)) }}</p>
        <p class="mt-2 text-xs text-muted">冻结 {{ fmtMoney(num(held)) }}</p>
      </div>
      <div class="metric-card">
        <p class="text-xs text-muted">本月充值</p>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ fmtMoney(paidThisMonth) }}</p>
      </div>
      <div class="metric-card">
        <p class="text-xs text-muted">本月消耗</p>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ fmtMoney(monthSpend) }}</p>
      </div>
      <router-link to="/app/recharges" class="metric-card block">
        <p class="text-xs text-muted">待支付充值单</p>
        <p class="mt-3 font-mono text-2xl font-semibold">{{ pendingOrders }} 单</p>
        <p class="mt-2 text-xs text-muted">查看充值记录 →</p>
      </router-link>
    </div>

    <div class="border-b border-line">
      <div class="tabs-row">
        <button type="button" :class="{ active: activeTab === 'monthly' }" @click="activeTab = 'monthly'">月度账单</button>
        <button type="button" :class="{ active: activeTab === 'ledger' }" @click="activeTab = 'ledger'">费用流水</button>
        <button type="button" :class="{ active: activeTab === 'usage' }" @click="activeTab = 'usage'">消费明细</button>
      </div>
    </div>

    <div v-if="activeTab === 'monthly'" class="space-y-3">
      <p class="text-sm text-muted">当前账期汇总。现有接口按日返回用量，月度历史账单待后端开放后补齐。</p>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3">账期</th>
              <th class="px-4 py-3 text-right">总消耗</th>
              <th class="px-4 py-3 text-right">赠送消耗</th>
              <th class="px-4 py-3 text-right">付费消耗</th>
              <th class="px-4 py-3 text-right">请求数</th>
              <th class="px-4 py-3 text-right">Token 数</th>
              <th class="px-4 py-3">状态</th>
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
              <td class="px-4 py-2.5"><span class="badge-ok">已统计</span></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-else-if="activeTab === 'ledger'" class="space-y-3">
      <p class="text-sm text-muted">共 {{ ledger.length }} 条记录，每页 20 条</p>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3">时间</th>
              <th class="px-4 py-3">类型</th>
              <th class="px-4 py-3 text-right">Credits 变动</th>
              <th class="px-4 py-3 text-right">赠送变动</th>
              <th class="px-4 py-3 text-right">变动后余额</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="ledger.length === 0"><td colspan="5" class="px-4 py-8 text-center text-muted">暂无数据</td></tr>
            <tr v-for="e in ledger" :key="e.id">
              <td class="px-4 py-2.5 text-muted">{{ d(new Date(e.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
              <td class="px-4 py-2.5"><span class="badge-muted">{{ e.kind }}</span></td>
              <td class="px-4 py-2.5 text-right font-mono" :class="num(e.delta) >= 0 ? 'text-ok' : 'text-danger'">{{ e.delta }}</td>
              <td class="px-4 py-2.5 text-right font-mono text-muted">—</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ e.balance_after }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-else class="space-y-3">
      <p class="text-sm text-muted">最近 31 天记录，共 {{ usage.length }} 条，每页 20 条</p>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3">时间</th>
              <th class="px-4 py-3">模型</th>
              <th class="px-4 py-3 text-right">Token 数</th>
              <th class="px-4 py-3 text-right">消耗</th>
              <th class="px-4 py-3 text-right">赠送消耗</th>
              <th class="px-4 py-3 text-right">付费消耗</th>
              <th class="px-4 py-3">状态</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="usage.length === 0"><td colspan="7" class="px-4 py-8 text-center text-muted">暂无数据</td></tr>
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
