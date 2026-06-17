<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'

interface UsageRow {
  id: number
  request_id: string
  model: string
  status: string
  status_code: number
  is_stream: boolean
  prompt_tokens: number
  completion_tokens: number
  cost_usd: string
  usage_source?: string
  frt_ms: number
  duration_ms: number
  error_brief?: string
  created_at: string
}

const { t, n, d } = useI18n()

const rows = ref<UsageRow[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const loading = ref(true)
const error = ref('')
const range = ref<'7d' | '30d' | 'month' | 'lastMonth' | 'custom'>('7d')
const ranges = ['7d', '30d', 'month', 'lastMonth', 'custom'] as const
const status = ref('')
const model = ref('')
const requestId = ref('')
const customSince = ref('')
const customUntil = ref('')
const selectedId = ref<number | null>(null)

const filteredRows = computed(() => {
  const q = requestId.value.trim().toLowerCase()
  return rows.value.filter((r) => {
    if (q && !r.request_id.toLowerCase().includes(q)) return false
    return isWithinRange(r.created_at)
  })
})
const selected = computed(() => filteredRows.value.find((r) => r.id === selectedId.value) ?? filteredRows.value[0] ?? null)
const totals = computed(() => {
  const calls = filteredRows.value.length
  const succeeded = filteredRows.value.filter((r) => r.status === 'success').length // 与后端聚合口径一致（仅 status='success'，不靠 status_code）
  const tokens = filteredRows.value.reduce((sum, r) => sum + r.prompt_tokens + r.completion_tokens, 0)
  const cost = filteredRows.value.reduce((sum, r) => sum + Number(r.cost_usd || 0), 0)
  const latency = filteredRows.value.reduce((sum, r) => sum + r.duration_ms, 0)
  return {
    calls,
    tokens,
    cost,
    successRate: calls ? succeeded / calls : 1,
    avgLatency: calls ? Math.round(latency / calls) : 0,
  }
})

function startOfLocalDay(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate())
}

function isWithinRange(value: string): boolean {
  const created = new Date(value)
  if (Number.isNaN(created.getTime())) return true
  const now = new Date()
  let from: Date | null = null
  let to: Date | null = null
  if (range.value === '7d') from = new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000)
  if (range.value === '30d') from = new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000)
  if (range.value === 'month') from = new Date(now.getFullYear(), now.getMonth(), 1)
  if (range.value === 'lastMonth') {
    from = new Date(now.getFullYear(), now.getMonth() - 1, 1)
    to = new Date(now.getFullYear(), now.getMonth(), 1)
  }
  if (range.value === 'custom') {
    from = customSince.value ? startOfLocalDay(new Date(customSince.value)) : null
    to = customUntil.value ? new Date(startOfLocalDay(new Date(customUntil.value)).getTime() + 24 * 60 * 60 * 1000) : null
  }
  if (from && created < from) return false
  if (to && created >= to) return false
  return true
}

let reqSeq = 0
async function load() {
  const seq = ++reqSeq
  loading.value = true
  try {
    const q = new URLSearchParams({ page: String(page.value), page_size: String(pageSize) })
    if (model.value.trim()) q.set('model', model.value.trim())
    if (status.value) q.set('status', status.value)
    const r = await api<{ total: number; items: UsageRow[] }>('GET', `/api/usage?${q}`)
    if (seq !== reqSeq) return
    rows.value = r.items ?? []
    total.value = r.total ?? 0
    selectedId.value = rows.value[0]?.id ?? null
    error.value = ''
  } catch (e) {
    if (seq !== reqSeq) return
    error.value = errorMessage(e, t('common.error'))
  } finally {
    if (seq === reqSeq) loading.value = false
  }
}

function fmtMs(ms: number): string {
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`
  return `${ms}ms`
}

function statusClass(row: UsageRow): string {
  return row.status === 'success' ? 'badge-ok' : 'badge-danger'
}

let filterTimer: ReturnType<typeof setTimeout> | undefined
watch([model, status], () => {
  clearTimeout(filterTimer)
  filterTimer = setTimeout(() => {
    page.value = 1
    void load()
  }, 250)
})
watch(page, () => void load())
onMounted(load)
onBeforeUnmount(() => clearTimeout(filterTimer))
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('surface.logsEyebrow') }}</p>
        <h1 class="app-page-title">{{ t('logs.title') }}</h1>
        <p class="app-page-subtitle">{{ t('logs.subtitle') }}</p>
      </div>
    </div>

    <div class="toolbar-panel">
      <div class="flex min-w-0 flex-wrap items-center gap-2">
        <div class="seg-control">
          <button v-for="r in ranges" :key="r" type="button"
            :class="range === r ? 'is-active' : ''" @click="range = r">
            {{ t(`logs.range_${r}`) }}
          </button>
        </div>
        <input v-model="model" class="input !w-44" :placeholder="t('logs.filterModel')" />
        <input v-model="requestId" class="input !w-56 font-mono text-xs" :placeholder="t('logs.filterRequest')" />
        <template v-if="range === 'custom'">
          <input v-model="customSince" class="input !w-40 text-xs" type="date" :aria-label="t('logs.customSince')" />
          <input v-model="customUntil" class="input !w-40 text-xs" type="date" :aria-label="t('logs.customUntil')" />
        </template>
        <select v-model="status" class="input !w-36">
          <option value="">{{ t('logs.allResults') }}</option>
          <option value="success">{{ t('logs.success') }}</option>
          <option value="upstream_error">{{ t('logs.failed') }}</option>
        </select>
      </div>
      <button class="btn-primary !py-1.5 text-xs" @click="load">{{ t('logs.query') }}</button>
    </div>
    <div class="inline-info text-sm">
      {{ t('logs.localFilterHint', { shown: filteredRows.length, loaded: rows.length }) }}
    </div>

    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div class="metric-card">
        <p class="text-xs text-muted">{{ t('logs.reqTotal') }}</p>
        <p class="mt-1 font-mono text-2xl font-semibold">{{ n(totals.calls) }}</p>
      </div>
      <div class="metric-card">
        <p class="text-xs text-muted">{{ t('logs.tokensTotal') }}</p>
        <p class="mt-1 font-mono text-2xl font-semibold">{{ n(totals.tokens) }}</p>
      </div>
      <div class="metric-card">
        <p class="text-xs text-muted">{{ t('logs.creditsTotal') }}</p>
        <p class="mt-1 font-mono text-2xl font-semibold">${{ totals.cost.toFixed(4) }}</p>
      </div>
      <div class="metric-card">
        <p class="text-xs text-muted">{{ t('logs.quality') }}</p>
        <p class="mt-1 font-mono text-2xl font-semibold">{{ (totals.successRate * 100).toFixed(1) }}%</p>
        <p class="mt-1 text-xs text-muted">{{ t('logs.avgLatency', { ms: fmtMs(totals.avgLatency) }) }}</p>
      </div>
    </div>

    <div class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_26rem]">
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3 font-medium">{{ t('logs.requestId') }}</th>
              <th class="px-4 py-3 font-medium">{{ t('usage.time') }}</th>
              <th class="px-4 py-3 font-medium">{{ t('usage.model') }}</th>
              <th class="px-4 py-3 font-medium text-right">{{ t('usage.tokens') }}</th>
              <th class="px-4 py-3 font-medium text-right">{{ t('logs.credits') }}</th>
              <th class="px-4 py-3 font-medium text-right">{{ t('logs.latency') }}</th>
              <th class="px-4 py-3 font-medium">{{ t('usage.status') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="loading">
              <td colspan="7" class="px-4 py-8 text-center text-muted">{{ t('common.loading') }}</td>
            </tr>
            <tr v-else-if="filteredRows.length === 0">
              <td colspan="7" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
            </tr>
            <template v-else>
              <tr v-for="r in filteredRows" :key="r.id" class="cursor-pointer border-b border-line last:border-0"
                :class="selected?.id === r.id ? 'bg-accent/10' : ''" @click="selectedId = r.id">
                <td class="max-w-64 truncate px-4 py-2.5 font-mono text-xs text-muted" :title="r.request_id">{{ r.request_id }}</td>
                <td class="px-4 py-2.5 text-muted">{{ d(new Date(r.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
                <td class="px-4 py-2.5 font-mono text-xs">{{ r.model }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ n(r.prompt_tokens + r.completion_tokens) }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ Number(r.cost_usd).toFixed(4) }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ fmtMs(r.duration_ms) }}</td>
                <td class="px-4 py-2.5">
                  <span :class="statusClass(r)">{{ r.status_code }} {{ r.status }}</span>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>

      <aside class="app-panel h-fit p-5 xl:sticky xl:top-6">
        <div v-if="selected" class="space-y-5">
          <div>
            <p class="text-xs font-medium text-accent">{{ t('logs.detailTitle') }} #{{ selected.id }}</p>
            <p class="mt-2 break-all font-mono text-sm">{{ selected.request_id }}</p>
          </div>
          <div class="grid grid-cols-3 gap-2">
            <div class="soft-panel p-3">
              <p class="text-[11px] text-muted">{{ t('logs.tokensTotal') }}</p>
              <p class="mt-1 font-mono font-semibold">{{ n(selected.prompt_tokens + selected.completion_tokens) }}</p>
            </div>
            <div class="soft-panel p-3">
              <p class="text-[11px] text-muted">{{ t('logs.credits') }}</p>
              <p class="mt-1 font-mono font-semibold">{{ Number(selected.cost_usd).toFixed(4) }}</p>
            </div>
            <div class="soft-panel p-3">
              <p class="text-[11px] text-muted">{{ t('logs.latency') }}</p>
              <p class="mt-1 font-mono font-semibold">{{ fmtMs(selected.duration_ms) }}</p>
            </div>
          </div>

          <section class="detail-group">
            <h2>{{ t('logs.identity') }}</h2>
            <dl>
              <div><dt>{{ t('usage.model') }}</dt><dd>{{ selected.model }}</dd></div>
              <div><dt>{{ t('logs.protocol') }}</dt><dd>OpenAI</dd></div>
              <div><dt>{{ t('usage.time') }}</dt><dd>{{ d(new Date(selected.created_at), { dateStyle: 'medium', timeStyle: 'medium' }) }}</dd></div>
            </dl>
          </section>

          <section class="detail-group">
            <h2>{{ t('logs.usageCost') }}</h2>
            <dl>
              <div><dt>{{ t('logs.inputTokens') }}</dt><dd>{{ n(selected.prompt_tokens) }}</dd></div>
              <div><dt>{{ t('logs.outputTokens') }}</dt><dd>{{ n(selected.completion_tokens) }}</dd></div>
              <div><dt>{{ t('usage.dUsageSource') }}</dt><dd>{{ selected.usage_source || '—' }}</dd></div>
            </dl>
          </section>

          <section class="detail-group">
            <h2>{{ t('logs.result') }}</h2>
            <dl>
              <div><dt>{{ t('usage.dStatusCode') }}</dt><dd>{{ selected.status_code }}</dd></div>
              <div><dt>{{ t('usage.dStream') }}</dt><dd>{{ selected.is_stream ? t('common.yes') : t('common.no') }}</dd></div>
              <div><dt>{{ t('usage.dFrt') }}</dt><dd>{{ fmtMs(selected.frt_ms) }}</dd></div>
            </dl>
            <p v-if="selected.error_brief" class="mt-3 break-all rounded-md bg-danger/10 p-3 font-mono text-xs text-danger">
              {{ selected.error_brief }}
            </p>
          </section>

          <section class="detail-group">
            <h2>{{ t('logs.supportPacket') }}</h2>
            <p class="text-xs leading-5 text-muted">{{ t('logs.privacyHint') }}</p>
            <dl class="mt-3">
              <div><dt>{{ t('usage.requestId') }}</dt><dd class="select-all break-all">{{ selected.request_id }}</dd></div>
              <div><dt>{{ t('logs.protocol') }}</dt><dd>OpenAI</dd></div>
              <div><dt>{{ t('usage.dDuration') }}</dt><dd>{{ fmtMs(selected.duration_ms) }}</dd></div>
            </dl>
          </section>
        </div>
        <p v-else class="text-sm text-muted">{{ t('common.empty') }}</p>
      </aside>
    </div>

    <div class="flex items-center justify-between text-sm text-muted">
      <span>{{ t('usage.total', { total }) }}</span>
      <div class="flex items-center gap-2">
        <button class="btn-ghost !py-1" :disabled="page <= 1" @click="page--">{{ t('common.prev') }}</button>
        <span>{{ t('common.page', { page }) }}</span>
        <button class="btn-ghost !py-1" :disabled="page * pageSize >= total" @click="page++">{{ t('common.next') }}</button>
      </div>
    </div>
  </div>
</template>
