<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'

interface UsageRow {
  id: number
  model: string
  status: string
  frt_ms: number
  duration_ms: number
  created_at: string
}
interface ModelStat { model: string }

const { n } = useI18n()
const model = ref('')
const window = ref<'24h' | '7d' | '30d'>('24h')
const search = ref('')
const rows = ref<UsageRow[]>([])
const models = ref<ModelStat[]>([])

function pct(values: number[], p: number): number {
  const list = values.filter((v) => v > 0).sort((a, b) => a - b)
  if (!list.length) return 0
  const idx = Math.min(list.length - 1, Math.ceil((p / 100) * list.length) - 1)
  return list[idx]
}
function fmtMs(v: number): string {
  return v ? `${Math.round(v)}ms` : '—'
}
function withinWindow(row: UsageRow): boolean {
  const t = new Date(row.created_at).getTime()
  if (!Number.isFinite(t)) return true
  const ms = window.value === '24h' ? 86400_000 : window.value === '7d' ? 7 * 86400_000 : 30 * 86400_000
  return t >= Date.now() - ms
}

const visible = computed(() => rows.value
  .filter(withinWindow)
  .filter((r) => !model.value || r.model === model.value)
  .filter((r) => !search.value || r.model.toLowerCase().includes(search.value.toLowerCase())))
const frts = computed(() => visible.value.map((r) => r.frt_ms || r.duration_ms))
const successRate = computed(() => visible.value.length ? visible.value.filter((r) => r.status === 'success').length / visible.value.length : 0)
const modelRows = computed(() => {
  const map = new Map<string, UsageRow[]>()
  for (const r of visible.value) {
    const list = map.get(r.model) ?? []
    list.push(r)
    map.set(r.model, list)
  }
  return [...map.entries()].map(([name, list]) => {
    const values = list.map((r) => r.frt_ms || r.duration_ms)
    return {
      model: name,
      p50: pct(values, 50),
      p95: pct(values, 95),
      p99: pct(values, 99),
      success: list.length ? list.filter((r) => r.status === 'success').length / list.length : 0,
      calls: list.length,
    }
  }).sort((a, b) => b.calls - a.calls)
})
const buckets = computed(() => {
  const defs = [
    { label: '<1s', max: 1000 },
    { label: '1-3s', max: 3000 },
    { label: '3-10s', max: 10000 },
    { label: '>10s', max: Infinity },
  ]
  return defs.map((b, i) => ({ ...b, count: visible.value.filter((r) => {
    const v = r.duration_ms || r.frt_ms
    const min = i === 0 ? 0 : defs[i - 1].max
    return v > min && v <= b.max
  }).length }))
})
const maxBucket = computed(() => Math.max(1, ...buckets.value.map((b) => b.count)))

async function load() {
  const [use, bm] = await Promise.allSettled([
    api<{ items: UsageRow[] }>('GET', '/api/usage?page=1&page_size=100'),
    api<{ items: ModelStat[] }>('GET', '/api/usage/by-model?days=30'),
  ])
  if (use.status === 'fulfilled') rows.value = use.value.items
  if (bm.status === 'fulfilled') models.value = bm.value.items
}
function exportCsv() {
  const body = ['model,p50,p95,p99,success,calls']
    .concat(modelRows.value.map((r) => `${r.model},${Math.round(r.p50)},${Math.round(r.p95)},${Math.round(r.p99)},${(r.success * 100).toFixed(1)}%,${r.calls}`))
    .join('\n')
  const blob = new Blob([body], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'latency.csv'
  a.click()
  URL.revokeObjectURL(url)
}

onMounted(load)
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <h1 class="app-page-title">响应与成功率</h1>
      </div>
    </div>

    <div class="toolbar-panel items-end">
      <div class="filter-group min-w-[180px]">
        <label>图表筛选</label>
        <select v-model="model" class="input">
          <option value="">全部模型</option>
          <option v-for="m in models" :key="m.model" :value="m.model">{{ m.model }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[160px]">
        <label>时间窗口</label>
        <select v-model="window" class="input">
          <option value="24h">最近 24 小时</option>
          <option value="7d">最近 7 天</option>
          <option value="30d">最近 30 天</option>
        </select>
      </div>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
      <div class="metric-card"><p class="text-xs text-muted">P50 首 Token 延迟</p><p class="mt-2 font-mono text-2xl font-semibold">{{ fmtMs(pct(frts, 50)) }}</p></div>
      <div class="metric-card"><p class="text-xs text-muted">P95 首 Token 延迟</p><p class="mt-2 font-mono text-2xl font-semibold">{{ fmtMs(pct(frts, 95)) }}</p></div>
      <div class="metric-card"><p class="text-xs text-muted">P99 首 Token 延迟</p><p class="mt-2 font-mono text-2xl font-semibold">{{ fmtMs(pct(frts, 99)) }}</p></div>
      <div class="metric-card"><p class="text-xs text-muted">成功率</p><p class="mt-2 font-mono text-2xl font-semibold">{{ visible.length ? (successRate * 100).toFixed(1) + '%' : '—' }}</p></div>
      <div class="metric-card"><p class="text-xs text-muted">请求数</p><p class="mt-2 font-mono text-2xl font-semibold">{{ n(visible.length) }}<span class="text-sm text-muted"> reqs</span></p></div>
    </div>

    <div class="grid gap-4 xl:grid-cols-3">
      <div class="app-panel p-5">
        <h2 class="font-semibold">总延迟分布</h2>
        <div class="mt-6 flex h-48 items-end gap-3">
          <div v-for="b in buckets" :key="b.label" class="flex flex-1 flex-col items-center gap-2">
            <div class="w-full rounded-t bg-accent/80" :style="{ height: `${(b.count / maxBucket) * 100}%`, minHeight: b.count ? '8px' : '0' }"></div>
            <span class="text-xs text-muted">{{ b.label }}</span>
          </div>
        </div>
      </div>
      <div class="app-panel p-5">
        <h2 class="font-semibold">成功率趋势</h2>
        <div class="mt-16 text-center font-mono text-3xl font-semibold">{{ visible.length ? (successRate * 100).toFixed(1) + '%' : '—' }}</div>
      </div>
      <div class="app-panel p-5">
        <h2 class="font-semibold">流式首 Token 延迟</h2>
        <div class="mt-16 text-center font-mono text-3xl font-semibold">{{ fmtMs(pct(frts, 50)) }}</div>
      </div>
    </div>

    <div class="space-y-3">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <h2 class="font-semibold">按模型排行</h2>
        <div class="flex gap-2">
          <input v-model="search" class="input max-w-xs" placeholder="搜索模型" />
          <button class="btn-ghost shrink-0 whitespace-nowrap !py-2" type="button" @click="exportCsv">导出 CSV</button>
        </div>
      </div>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3">模型</th>
              <th class="px-4 py-3 text-right">P50</th>
              <th class="px-4 py-3 text-right">P95</th>
              <th class="px-4 py-3 text-right">P99</th>
              <th class="px-4 py-3 text-right">成功率</th>
              <th class="px-4 py-3 text-right">请求数</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="modelRows.length === 0"><td colspan="6" class="px-4 py-8 text-center text-muted">暂无模型数据</td></tr>
            <tr v-for="r in modelRows" :key="r.model">
              <td class="px-4 py-2.5 font-mono text-xs">{{ r.model }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtMs(r.p50) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtMs(r.p95) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ fmtMs(r.p99) }}</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ (r.success * 100).toFixed(1) }}%</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ n(r.calls) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
