<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'
import { useAuth } from '@/stores/auth'

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
  frt_ms: number
  duration_ms: number
  created_at: string
}

const { t, n, d } = useI18n()
const auth = useAuth()

const rows = ref<UsageRow[]>([])
const exporting = ref(false)
const loading = ref(true)
const error = ref('')
const windowKey = ref<'last20' | '7d' | '30d'>('last20')
const windows = ['last20', '7d', '30d'] as const

function percentile(values: number[], p: number): number {
  if (values.length === 0) return 0
  const sorted = [...values].sort((a, b) => a - b)
  const idx = Math.min(sorted.length - 1, Math.ceil((p / 100) * sorted.length) - 1)
  return sorted[idx] ?? 0
}

function fmtMs(ms: number): string {
  if (!ms) return '—'
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`
  return `${Math.round(ms)}ms`
}

function isSuccess(row: UsageRow): boolean {
  return row.status === 'success'
}

const visibleRows = computed(() => {
  if (windowKey.value === 'last20') return rows.value
  const days = windowKey.value === '7d' ? 7 : 30
  const since = Date.now() - days * 24 * 60 * 60 * 1000
  return rows.value.filter((row) => new Date(row.created_at).getTime() >= since)
})

const stats = computed(() => {
  const data = visibleRows.value
  const durations = data.map((row) => row.duration_ms).filter((v) => v > 0)
  const frt = data.map((row) => row.frt_ms).filter((v) => v > 0)
  const success = data.filter(isSuccess).length
  return {
    count: data.length,
    successRate: data.length ? success / data.length : 1,
    avg: durations.length ? durations.reduce((sum, v) => sum + v, 0) / durations.length : 0,
    p50: percentile(durations, 50),
    p95: percentile(durations, 95),
    p99: percentile(durations, 99),
    frtAvg: frt.length ? frt.reduce((sum, v) => sum + v, 0) / frt.length : 0,
  }
})

const bands = computed(() => {
  const fast = visibleRows.value.filter((row) => row.duration_ms > 0 && row.duration_ms < 3000).length
  const watch = visibleRows.value.filter((row) => row.duration_ms >= 3000 && row.duration_ms < 12000).length
  const slow = visibleRows.value.filter((row) => row.duration_ms >= 12000).length
  const total = Math.max(1, fast + watch + slow)
  return [
    { key: 'fast', value: fast, pct: (fast / total) * 100, cls: 'bg-ok' },
    { key: 'watch', value: watch, pct: (watch / total) * 100, cls: 'bg-accent' },
    { key: 'slow', value: slow, pct: (slow / total) * 100, cls: 'bg-danger' },
  ]
})

const byModel = computed(() => {
  const map = new Map<string, { model: string; calls: number; duration: number; ok: number }>()
  for (const row of visibleRows.value) {
    const current = map.get(row.model) ?? { model: row.model, calls: 0, duration: 0, ok: 0 }
    current.calls += 1
    current.duration += row.duration_ms
    if (isSuccess(row)) current.ok += 1
    map.set(row.model, current)
  }
  return [...map.values()]
    .map((row) => ({ ...row, avg: row.calls ? row.duration / row.calls : 0, success: row.calls ? row.ok / row.calls : 1 }))
    .sort((a, b) => b.avg - a.avg)
})

async function load() {
  loading.value = true
  try {
    const r = await api<{ items: UsageRow[] }>('GET', '/api/usage?page=1&page_size=100')
    rows.value = r.items ?? []
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    loading.value = false
  }
}

// 调用明细 CSV 导出：带 Bearer 头 fetch 成 blob 触发下载（GET 直链无法带 auth 头）。
async function exportCsv() {
  if (exporting.value) return
  exporting.value = true
  try {
    const days = windowKey.value === '30d' ? 30 : 7 // 30d → 30 天；last20/7d → 7 天（last20 看的是已加载页，不是时间窗，取 7 天兜底，文件名随 days 自洽）
    const headers: Record<string, string> = {}
    if (auth.token) headers['Authorization'] = `Bearer ${auth.token}`
    const resp = await fetch(`/api/usage/export?days=${days}`, { headers })
    if (!resp.ok) throw new Error('export failed')
    const blob = await resp.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `usage_${days}d.csv`
    a.click()
    URL.revokeObjectURL(url)
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    exporting.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="app-page latency-page">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('surface.latencyEyebrow') }}</p>
        <h1 class="app-page-title">{{ t('latency.title') }}</h1>
        <p class="app-page-subtitle">{{ t('latency.subtitle') }}</p>
      </div>
      <button class="btn-ghost shrink-0" type="button" :disabled="exporting" @click="exportCsv">
        {{ exporting ? t('common.loading') : t('latency.exportCsv') }}
      </button>
    </div>

    <div class="toolbar-panel">
      <div class="flex flex-wrap items-center gap-2">
        <span class="text-xs font-medium uppercase text-muted/70">{{ t('latency.window') }}</span>
        <div class="seg-control">
          <button v-for="w in windows" :key="w" type="button" :class="windowKey === w ? 'is-active' : ''" @click="windowKey = w">
            {{ w === 'last20' ? t('latency.last20') : t(`logs.range_${w}`) }}
          </button>
        </div>
      </div>
      <span class="text-xs text-muted">{{ t('latency.localHint') }}</span>
    </div>

    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <p v-if="loading" class="text-sm text-muted">{{ t('common.loading') }}</p>

    <template v-else>
      <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <div class="metric-card">
          <p class="text-xs text-muted">{{ t('latency.successRate') }}</p>
          <p class="mt-1 font-mono text-2xl font-semibold">{{ (stats.successRate * 100).toFixed(1) }}%</p>
          <p class="mt-1 text-xs text-muted">{{ n(stats.count) }} {{ t('latency.requests') }}</p>
        </div>
        <div class="metric-card">
          <p class="text-xs text-muted">{{ t('latency.avgLatency') }}</p>
          <p class="mt-1 font-mono text-2xl font-semibold">{{ fmtMs(stats.avg) }}</p>
        </div>
        <div class="metric-card">
          <p class="text-xs text-muted">{{ t('latency.p95') }}</p>
          <p class="mt-1 font-mono text-2xl font-semibold">{{ fmtMs(stats.p95) }}</p>
          <p class="mt-1 text-xs text-muted">{{ t('latency.p50') }} {{ fmtMs(stats.p50) }} · {{ t('latency.p99') }} {{ fmtMs(stats.p99) }}</p>
        </div>
        <div class="metric-card">
          <p class="text-xs text-muted">{{ t('latency.firstToken') }}</p>
          <p class="mt-1 font-mono text-2xl font-semibold">{{ fmtMs(stats.frtAvg) }}</p>
        </div>
      </div>

      <section class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_24rem]">
        <div class="app-panel p-5">
          <h2 class="font-semibold">{{ t('latency.latencyBands') }}</h2>
          <div class="mt-5 overflow-hidden rounded-lg border border-line bg-elev/45">
            <div class="flex h-7">
              <div v-for="band in bands" :key="band.key" :class="band.cls" :style="{ width: `${band.pct}%` }"></div>
            </div>
          </div>
          <div class="mt-4 grid gap-3 sm:grid-cols-3">
            <div v-for="band in bands" :key="band.key" class="soft-panel p-3">
              <p class="text-xs text-muted">{{ t(`latency.${band.key}`) }}</p>
              <p class="mt-1 font-mono text-lg">{{ n(band.value) }}</p>
            </div>
          </div>
        </div>

        <aside class="app-panel p-5">
          <h2 class="font-semibold">{{ t('latency.statusMix') }}</h2>
          <p class="mt-2 text-sm leading-6 text-muted">{{ t('latency.localHint') }}</p>
          <router-link to="/app/logs" class="btn-primary mt-5 w-full !py-2 text-sm">{{ t('nav.requestLogs') }}</router-link>
        </aside>
      </section>

      <section class="space-y-3">
        <div>
          <h2 class="font-semibold">{{ t('latency.modelRank') }}</h2>
          <p class="mt-1 text-xs text-muted">{{ t('latency.modelRankHint') }}</p>
        </div>
        <div class="data-table-wrap">
          <table class="table w-full text-sm">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">{{ t('usage.model') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('latency.requests') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('latency.avgLatency') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('latency.successRate') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="byModel.length === 0"><td colspan="4" class="px-4 py-8 text-center text-muted">{{ t('latency.empty') }}</td></tr>
              <tr v-for="row in byModel" :key="row.model" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 font-mono text-xs">{{ row.model }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ n(row.calls) }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ fmtMs(row.avg) }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ (row.success * 100).toFixed(1) }}%</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="space-y-3">
        <h2 class="font-semibold">{{ t('logs.supportPacket') }}</h2>
        <div class="data-table-wrap">
          <table class="table w-full text-sm whitespace-nowrap">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">{{ t('usage.time') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('usage.model') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('latency.avgLatency') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('usage.status') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('usage.requestId') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="visibleRows.length === 0">
                <td colspan="5" class="px-4 py-8 text-center text-muted">{{ t('latency.empty') }}</td>
              </tr>
              <tr v-for="row in visibleRows" :key="row.id" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 text-muted">{{ d(new Date(row.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
                <td class="px-4 py-2.5 font-mono text-xs">{{ row.model }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ fmtMs(row.duration_ms) }}</td>
                <td class="px-4 py-2.5"><span :class="isSuccess(row) ? 'badge-ok' : 'badge-danger'">{{ row.status_code }} {{ row.status }}</span></td>
                <td class="max-w-72 truncate px-4 py-2.5 font-mono text-xs text-muted" :title="row.request_id">{{ row.request_id }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>
  </div>
</template>
