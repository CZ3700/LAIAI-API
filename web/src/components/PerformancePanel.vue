<script setup lang="ts">
// 性能面板（原 Latency 页并入 Usage 的「性能」tab）：延迟分位 / 成功率 / 时长分布 / 按模型 p50-p99。
// 数据全由后端 /api/usage/perf-stats 在窗口全量上聚合（切窗口重拉），与用量的 token 聚合是不同粒度。
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'

interface ModelPerf {
  model: string
  calls: number
  succeeded: number
  success_rate: number
  p50: number
  p95: number
  p99: number
}
interface Bucket { label: string; count: number }
interface PerfStats {
  window_mins: number
  calls: number
  succeeded: number
  success_rate: number
  frt: { p50: number; p95: number; p99: number }
  dur: { p50: number; p95: number; p99: number }
  buckets: Bucket[]
  models: ModelPerf[]
  sampled: boolean
  sample_size: number
}
interface ModelStat { model: string }

const { t, n } = useI18n()
const model = ref('')
const win = ref<'24h' | '7d' | '30d'>('24h')
const search = ref('')
const stats = ref<PerfStats | null>(null)
const models = ref<ModelStat[]>([])
const loading = ref(true)

const WINDOW_MINS: Record<'24h' | '7d' | '30d', number> = { '24h': 1440, '7d': 10080, '30d': 43200 }

function fmtMs(v: number): string {
  return v ? `${Math.round(v)}ms` : '—'
}

const frt = computed(() => stats.value?.frt ?? { p50: 0, p95: 0, p99: 0 })
const successRate = computed(() => stats.value?.success_rate ?? 0)
const calls = computed(() => stats.value?.calls ?? 0)
const buckets = computed(() => stats.value?.buckets ?? [])
const maxBucket = computed(() => Math.max(1, ...buckets.value.map((b) => b.count)))
const modelRows = computed(() => (stats.value?.models ?? [])
  .filter((m) => !model.value || m.model === model.value)
  .filter((m) => !search.value || m.model.toLowerCase().includes(search.value.toLowerCase())))

async function load() {
  loading.value = true
  const [st, bm] = await Promise.allSettled([
    api<PerfStats>('GET', `/api/usage/perf-stats?mins=${WINDOW_MINS[win.value]}`),
    api<{ items: ModelStat[] }>('GET', '/api/usage/by-model?days=30'),
  ])
  if (st.status === 'fulfilled') stats.value = st.value
  if (bm.status === 'fulfilled') models.value = bm.value.items
  loading.value = false
}
function exportCsv() {
  const body = ['model,p50,p95,p99,success,calls']
    .concat(modelRows.value.map((r) => `${r.model},${Math.round(r.p50)},${Math.round(r.p95)},${Math.round(r.p99)},${(r.success_rate * 100).toFixed(1)}%,${r.calls}`))
    .join('\n')
  const blob = new Blob([body], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'latency.csv'
  a.click()
  URL.revokeObjectURL(url)
}

watch(win, () => void load())
onMounted(load)
</script>

<template>
  <div class="space-y-5">
    <div class="toolbar-panel items-end">
      <div class="filter-group min-w-[180px]">
        <label>{{ t('lat.filterChart') }}</label>
        <select v-model="model" class="input">
          <option value="">{{ t('lat.allModels') }}</option>
          <option v-for="m in models" :key="m.model" :value="m.model">{{ m.model }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[160px]">
        <label>{{ t('lat.timeWindow') }}</label>
        <select v-model="win" class="input">
          <option value="24h">{{ t('lat.window24h') }}</option>
          <option value="7d">{{ t('lat.window7d') }}</option>
          <option value="30d">{{ t('lat.window30d') }}</option>
        </select>
      </div>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
      <div class="metric-card"><div class="flex items-center gap-3"><span class="metric-icon mi-indigo"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span><p class="text-xs text-muted">{{ t('lat.metricP50Frt') }}</p></div><p class="mt-2 font-mono text-2xl font-semibold">{{ fmtMs(frt.p50) }}</p></div>
      <div class="metric-card"><div class="flex items-center gap-3"><span class="metric-icon mi-emerald"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span><p class="text-xs text-muted">{{ t('lat.metricP95Frt') }}</p></div><p class="mt-2 font-mono text-2xl font-semibold">{{ fmtMs(frt.p95) }}</p></div>
      <div class="metric-card"><div class="flex items-center gap-3"><span class="metric-icon mi-sky"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span><p class="text-xs text-muted">{{ t('lat.metricP99Frt') }}</p></div><p class="mt-2 font-mono text-2xl font-semibold">{{ fmtMs(frt.p99) }}</p></div>
      <div class="metric-card"><div class="flex items-center gap-3"><span class="metric-icon mi-green"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 12.75 11.25 15 15 9.75M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span><p class="text-xs text-muted">{{ t('lat.metricSuccessRate') }}</p></div><p class="mt-2 font-mono text-2xl font-semibold">{{ calls ? (successRate * 100).toFixed(1) + '%' : '—' }}</p></div>
      <div class="metric-card"><div class="flex items-center gap-3"><span class="metric-icon mi-amber"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M7.5 21 3 16.5m0 0L7.5 12M3 16.5h13.5m0-13.5L21 7.5m0 0L16.5 12M21 7.5H7.5" /></svg></span><p class="text-xs text-muted">{{ t('lat.metricRequests') }}</p></div><p class="mt-2 font-mono text-2xl font-semibold">{{ n(calls) }}<span class="text-sm text-muted"> reqs</span></p></div>
    </div>

    <p v-if="stats?.sampled" class="text-xs text-muted">{{ t('lat.sampledNote', { n: n(stats.sample_size) }) }}</p>

    <div class="app-panel p-5">
      <h2 class="font-semibold">{{ t('lat.chartTotalDist') }}</h2>
      <div class="mt-6 flex h-48 items-end gap-3">
        <div v-for="b in buckets" :key="b.label" class="flex flex-1 flex-col items-center gap-2">
          <div class="w-full rounded-t bg-accent/80" :style="{ height: `${(b.count / maxBucket) * 100}%`, minHeight: b.count ? '8px' : '0' }"></div>
          <span class="text-xs text-muted">{{ b.label }}</span>
        </div>
      </div>
    </div>

    <div class="space-y-3">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <h2 class="font-semibold">{{ t('lat.byModelTitle') }}</h2>
        <div class="flex gap-2">
          <input v-model="search" class="input max-w-xs" :placeholder="t('lat.searchModel')" />
          <button class="btn-ghost shrink-0 whitespace-nowrap !py-2" type="button" @click="exportCsv">{{ t('lat.exportCsv') }}</button>
        </div>
      </div>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3">{{ t('lat.colModel') }}</th>
              <th class="px-4 py-3 text-right">P50</th>
              <th class="px-4 py-3 text-right">P95</th>
              <th class="px-4 py-3 text-right">P99</th>
              <th class="px-4 py-3 text-right">{{ t('lat.colSuccessRate') }}</th>
              <th class="px-4 py-3 text-right">{{ t('lat.colRequests') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="modelRows.length === 0"><td colspan="6" class="px-4 py-8 text-center text-muted">{{ t('lat.emptyData') }}</td></tr>
            <tr v-for="r in modelRows" :key="r.model">
              <td class="px-4 py-2.5 font-mono text-xs">{{ r.model }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtMs(r.p50) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtMs(r.p95) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtMs(r.p99) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ (r.success_rate * 100).toFixed(1) }}%</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(r.calls) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
