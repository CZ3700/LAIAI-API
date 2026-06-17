<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import { type SummaryDay } from '@/components/DailyBarChart.vue'
import StackedBarChart, { type ModelDailyRow } from '@/components/StackedBarChart.vue'
import Sparkline from '@/components/Sparkline.vue'

const auth = useAuth()
const userName = computed(() => (auth.email || '').split('@')[0] || 'there')

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
const error = ref('')
const summary = ref<SummaryDay[]>([])
const byModel = ref<ModelStat[]>([])
const byModelDaily = ref<ModelDailyRow[]>([])
const recent = ref<UsageRow[]>([])
const perf = ref({ rpm: 0, tpm: 0 })
const summaryDays = 14
let perfTimer: ReturnType<typeof setInterval> | undefined

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
// sparkline 序列：补齐 14 天为零
const series = computed(() => {
  const byDay = new Map(summary.value.map((s) => [s.day, s]))
  const calls: number[] = []
  const cost: number[] = []
  const tokens: number[] = []
  const today = new Date()
  for (let i = summaryDays - 1; i >= 0; i--) {
    const dt = new Date(today)
    dt.setDate(today.getDate() - i)
    const s = byDay.get(dt.toISOString().slice(0, 10))
    calls.push(s?.calls ?? 0)
    cost.push(s ? Number(s.cost_usd) : 0)
    tokens.push(s ? s.prompt_tokens + s.completion_tokens : 0)
  }
  return { calls, cost, tokens }
})
const maxModelCalls = computed(() => Math.max(1, ...byModel.value.map((m) => m.calls)))
const maxModelCost = computed(() => Math.max(0.0001, ...byModel.value.map((m) => Number(m.cost_usd))))

function fmtTokens(x: number): string {
  if (x >= 1_000_000) return (x / 1_000_000).toFixed(2) + 'M'
  if (x >= 1_000) return (x / 1_000).toFixed(1) + 'K'
  return String(x)
}

// 模型分析多视图
const modelView = ref<'cost' | 'calls' | 'rank'>('cost')
const modelViews = [
  { key: 'cost', label: () => t('dashboard.vCost') },
  { key: 'calls', label: () => t('dashboard.vCalls') },
  { key: 'rank', label: () => t('dashboard.vRank') },
] as const
// 一组稳定的分类色（与品牌呼应）
const palette = [
  '129 140 248', '192 132 252', '56 189 248', '52 211 153', '251 191 36',
  '248 113 113', '244 114 182', '167 139 250', '34 211 238', '163 230 53',
]

async function loadPerf() {
  try {
    perf.value = await api<{ rpm: number; tpm: number }>('GET', '/api/usage/perf')
  } catch {
    // 静默
  }
}

onMounted(async () => {
  const [bal, sum, bm, bmd, use] = await Promise.allSettled([
    api<{ balance: string; held: string }>('GET', '/api/balance'),
    api<{ items: SummaryDay[] }>('GET', `/api/usage/summary?days=${summaryDays}`),
    api<{ items: ModelStat[] }>('GET', `/api/usage/by-model?days=${summaryDays}`),
    api<{ items: ModelDailyRow[] }>('GET', `/api/usage/by-model-daily?days=${summaryDays}`),
    api<{ items: UsageRow[] }>('GET', '/api/usage?page=1&page_size=8'),
  ])
  if (bal.status === 'fulfilled') {
    balance.value = bal.value.balance
    held.value = bal.value.held
  }
  if (sum.status === 'fulfilled') summary.value = sum.value.items.map((s) => ({ ...s, day: s.day.slice(0, 10) }))
  if (bm.status === 'fulfilled') byModel.value = bm.value.items
  if (bmd.status === 'fulfilled') byModelDaily.value = bmd.value.items.map((r) => ({ ...r, day: r.day.slice(0, 10) }))
  if (use.status === 'fulfilled') recent.value = use.value.items
  // 区分「加载失败」与「真没数据」（FE-5）：任一首屏接口失败给出明确横幅，而非静默显示全 0。
  const failed = [bal, sum, bm, bmd, use].filter((r) => r.status === 'rejected').length
  error.value = failed > 0 ? t('billing.partialError', { count: failed }) : ''
  void loadPerf()
  perfTimer = setInterval(loadPerf, 15000) // 15s 刷新实时指标
})
onBeforeUnmount(() => clearInterval(perfTimer))
</script>

<template>
  <div class="app-page">
    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <section class="grid gap-4 lg:grid-cols-[1fr_24rem]">
      <div class="console-hero p-5 sm:p-6">
        <p class="text-sm text-muted">{{ t('dashboard.greeting') }}, {{ userName }}</p>
        <div class="mt-3 flex flex-wrap items-end justify-between gap-4">
          <div>
            <h1 class="app-page-title">{{ t('nav.dashboard') }}</h1>
            <p class="mt-2 max-w-2xl text-sm leading-6 text-muted">{{ t('surface.dashboardSubtitle') }}</p>
          </div>
          <router-link to="/app/playground" class="btn-primary">{{ t('nav.playground') }}</router-link>
        </div>
      </div>
      <div class="app-panel-strong p-5">
        <div class="flex items-start justify-between gap-3">
          <div>
            <p class="text-xs text-muted">{{ t('dashboard.balance') }}</p>
            <p class="mt-1 font-mono text-3xl font-semibold">${{ balance }}</p>
          </div>
          <router-link to="/app/wallet" class="btn-ghost !min-h-0 !px-3 !py-1.5 text-xs">{{ t('wallet.topup') }}</router-link>
        </div>
        <div class="mt-4 grid grid-cols-2 gap-3 text-xs">
          <div class="soft-panel p-3">
            <p class="text-muted">{{ t('wallet.held') }}</p>
            <p class="mt-1 font-mono text-ink">${{ held }}</p>
          </div>
          <div class="soft-panel p-3">
            <p class="text-muted">{{ t('dashboard.histSpend') }}</p>
            <p class="mt-1 font-mono text-ink">${{ totals.cost.toFixed(2) }}</p>
          </div>
        </div>
      </div>
    </section>

    <div class="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
      <router-link to="/app/onboarding" class="action-strip">
        <span>{{ t('dashboard.actionSetup') }}</span>
        <strong>{{ t('dashboard.actionReady') }}</strong>
        <small>{{ t('dashboard.actionSetupHint') }}</small>
      </router-link>
      <router-link to="/app/logs" class="action-strip">
        <span>{{ t('dashboard.actionLogs') }}</span>
        <strong>{{ totals.calls }}</strong>
        <small>{{ t('dashboard.actionLogsHint') }}</small>
      </router-link>
      <router-link to="/app/billing" class="action-strip">
        <span>{{ t('dashboard.actionBilling') }}</span>
        <strong>${{ totals.cost.toFixed(2) }}</strong>
        <small>{{ t('dashboard.actionBillingHint') }}</small>
      </router-link>
      <router-link to="/app/latency" class="action-strip">
        <span>{{ t('dashboard.actionLatency') }}</span>
        <strong>{{ (totals.rate * 100).toFixed(1) }}%</strong>
        <small>{{ t('dashboard.actionLatencyHint') }}</small>
      </router-link>
    </div>

    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div class="enter metric-card" style="animation-delay: 0ms">
        <div class="flex items-start justify-between">
          <div>
            <p class="text-xs text-muted">{{ t('dashboard.todayCalls') }}</p>
            <p class="mt-1 text-2xl font-semibold font-mono">{{ n(todayCalls) }}</p>
          </div>
          <Sparkline :data="series.calls" />
        </div>
        <p class="mt-2 text-xs text-muted">{{ t('admin.ovWindowCalls', { n: totals.calls, days: summaryDays }) }}</p>
      </div>

      <div class="enter metric-card" style="animation-delay: 70ms">
        <div class="flex items-start justify-between">
          <div>
            <p class="text-xs text-muted">{{ t('usage.sumCost', { days: summaryDays }) }}</p>
            <p class="mt-1 text-2xl font-semibold font-mono">${{ totals.cost.toFixed(2) }}</p>
          </div>
          <Sparkline :data="series.cost" />
        </div>
        <p class="mt-2 text-xs" :class="totals.rate >= 0.95 ? 'text-ok' : 'text-danger'">
          {{ t('dashboard.successRate', { rate: (totals.rate * 100).toFixed(1) }) }}
        </p>
      </div>

      <div class="enter metric-card" style="animation-delay: 140ms">
        <div class="flex items-start justify-between">
          <div>
            <p class="text-xs text-muted">{{ t('dashboard.tokens') }}</p>
            <p class="mt-1 text-2xl font-semibold font-mono">{{ fmtTokens(totals.inTok + totals.outTok) }}</p>
          </div>
          <Sparkline :data="series.tokens" />
        </div>
        <p class="mt-2 text-xs text-muted">{{ fmtTokens(totals.inTok) }} in / {{ fmtTokens(totals.outTok) }} out</p>
      </div>

      <div class="enter metric-card" style="animation-delay: 210ms">
        <div class="flex items-start justify-between">
          <div>
            <p class="text-xs text-muted">{{ t('surface.dashboardThroughput') }}</p>
            <p class="mt-1 text-2xl font-semibold font-mono">{{ perf.rpm }} RPM</p>
          </div>
          <span class="badge-muted">15s</span>
        </div>
        <div class="mt-2 flex gap-4 text-xs text-muted">
          <span>TPM <span class="font-mono text-ink">{{ fmtTokens(perf.tpm) }}</span></span>
        </div>
      </div>
    </div>

    <!-- 模型数据分析：多视图 -->
    <div class="app-panel p-5">
      <div class="mb-4 flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 class="font-semibold">{{ t('dashboard.modelAnalysis') }}</h2>
          <p class="mt-1 text-xs text-muted">{{ t('surface.dashboardModelHint') }}</p>
        </div>
        <div class="flex gap-1 rounded-lg border border-line bg-elev/45 p-1">
          <button
            v-for="v in modelViews"
            :key="v.key"
            type="button"
            class="rounded-md px-3 py-1 text-xs transition-colors"
            :class="modelView === v.key ? 'bg-panel text-ink shadow-sm' : 'text-muted hover:text-ink'"
            @click="modelView = v.key"
          >
            {{ v.label() }}
          </button>
        </div>
      </div>

      <!-- 趋势视图：14 天柱状 -->
      <StackedBarChart v-if="modelView === 'calls'" :rows="byModelDaily" :days="summaryDays" metric="calls" />

      <!-- 消耗分布 / 调用排行：水平条 -->
      <div v-else-if="byModel.length" class="space-y-2.5">
        <div v-for="(m, i) in byModel" :key="m.model" class="flex items-center gap-3">
          <span class="h-2.5 w-2.5 shrink-0 rounded-full" :style="{ background: `rgb(${palette[i % palette.length]})` }"></span>
          <span class="w-40 shrink-0 truncate font-mono text-xs" :title="m.model">{{ m.model }}</span>
          <div class="h-2 flex-1 overflow-hidden rounded-full bg-line">
            <div
              class="h-full rounded-full"
              :style="{
                width: modelView === 'cost'
                  ? `${(Number(m.cost_usd) / maxModelCost) * 100}%`
                  : `${(m.calls / maxModelCalls) * 100}%`,
                background: `rgb(${palette[i % palette.length]})`,
              }"
            ></div>
          </div>
          <span class="w-20 shrink-0 text-right font-mono text-xs text-muted">
            {{ modelView === 'cost' ? '$' + Number(m.cost_usd).toFixed(4) : n(m.calls) }}
          </span>
        </div>
      </div>
      <div v-else class="py-10 text-center text-sm text-muted">{{ t('common.empty') }}</div>
    </div>

    <!-- 模型明细 + 最近调用 -->
    <div class="grid gap-6 lg:grid-cols-2">
      <div class="space-y-3">
        <h2 class="font-semibold">{{ t('dashboard.byModel') }}</h2>
      <div class="data-table-wrap">
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
                <td class="px-4 py-2.5 font-mono text-xs">{{ m.model }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ n(m.calls) }}</td>
                <td class="px-4 py-2.5 text-right font-mono text-muted">{{ fmtTokens(m.prompt_tokens + m.completion_tokens) }}</td>
                <td class="px-4 py-2.5 text-right font-mono">${{ Number(m.cost_usd).toFixed(4) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="space-y-3">
        <div class="flex items-center justify-between">
          <h2 class="font-semibold">{{ t('dashboard.recent') }}</h2>
          <router-link class="text-sm text-accent hover:underline" to="/app/usage">{{ t('usage.title') }} →</router-link>
        </div>
        <div class="data-table-wrap">
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
