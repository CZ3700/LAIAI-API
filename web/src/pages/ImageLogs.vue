<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'
import { fmtUsd } from '@/lib/money'

// 图像调用日志：/api/usage?modality=image 服务端过滤（0040 模态列）。
// 与 RequestLogs 同一数据源与时间窗口径（UTC 自然日、左闭右开），仅按模态收窄 + 精简列。

interface UsageRow {
  id: number
  request_id: string
  model: string
  status: string
  status_code: number
  cost_usd: string
  duration_ms: number
  error_brief?: string
  created_at: string
}
interface ModelPrice {
  model: string
  image_usd: string
}
interface KeyRow { id: number; name: string; prefix: string }

const { t, d } = useI18n()
const rows = ref<UsageRow[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const loading = ref(true)
const error = ref('')
const range = ref<'7d' | '30d' | 'month' | 'prev' | 'custom'>('7d')
const model = ref('')
const apiKeyId = ref('')
const requestId = ref('')
const result = ref<'all' | 'success' | 'failed'>('all')
const customStart = ref('')
const customEnd = ref('')
const models = ref<ModelPrice[]>([])
const keys = ref<KeyRow[]>([])

function statusText(r: UsageRow): string {
  if (r.status === 'success') return `${r.status_code || 200} OK`
  return r.status_code ? String(r.status_code) : r.status
}
// 时间窗按 UTC 自然日界计算（与 RequestLogs 同口径，见其注释），下推后端 since/until。
function utcDate(ms: number): string {
  return new Date(ms).toISOString().slice(0, 10)
}
function windowParams(): { since?: string; until?: string } {
  const now = new Date()
  const y = now.getUTCFullYear()
  const m = now.getUTCMonth()
  const thisMonth1 = `${y}-${String(m + 1).padStart(2, '0')}-01`
  if (range.value === '7d') return { since: utcDate(Date.now() - 6 * 86400_000) }
  if (range.value === '30d') return { since: utcDate(Date.now() - 29 * 86400_000) }
  if (range.value === 'month') return { since: thisMonth1 }
  if (range.value === 'prev') {
    const py = m === 0 ? y - 1 : y
    const pm = m === 0 ? 12 : m
    return { since: `${py}-${String(pm).padStart(2, '0')}-01`, until: thisMonth1 }
  }
  if (range.value === 'custom') {
    const out: { since?: string; until?: string } = {}
    if (customStart.value) out.since = customStart.value
    if (customEnd.value) out.until = utcDate(new Date(customEnd.value).getTime() + 86400_000)
    return out
  }
  return {}
}

async function loadMeta() {
  const [pricing, keyRows] = await Promise.allSettled([
    api<{ models: ModelPrice[] }>('GET', '/api/pricing', undefined, { auth: false }),
    api<{ keys: KeyRow[] }>('GET', '/api/keys'),
  ])
  if (pricing.status === 'fulfilled') models.value = pricing.value.models.filter((m) => Number(m.image_usd) > 0)
  if (keyRows.status === 'fulfilled') keys.value = keyRows.value.keys
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const q = new URLSearchParams({ modality: 'image', page: String(page.value), page_size: String(pageSize) })
    if (model.value) q.set('model', model.value)
    if (apiKeyId.value) q.set('api_key_id', apiKeyId.value)
    if (requestId.value.trim()) q.set('request_id', requestId.value.trim()) // 精确锚点（uk_inv_request）
    if (result.value !== 'all') q.set('status', result.value)
    const w = windowParams()
    if (w.since) q.set('since', w.since)
    if (w.until) q.set('until', w.until)
    const r = await api<{ total: number; items: UsageRow[] }>('GET', `/api/usage?${q}`)
    rows.value = r.items
    total.value = r.total
  } catch (e) {
    error.value = errorMessage(e, t('imglog.loadFailed'))
  } finally {
    loading.value = false
  }
}

watch([model, apiKeyId, result, range, customStart, customEnd], () => {
  page.value = 1
  void load()
})
watch(page, () => void load())
onMounted(() => {
  void loadMeta()
  void load()
})
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <h1 class="app-page-title">{{ t('imglog.title') }}</h1>
      </div>
    </div>

    <div class="toolbar-panel items-end">
      <div class="filter-group">
        <label>{{ t('imglog.filterTimeRange') }}</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: range === '7d' }" @click="range = '7d'">{{ t('imglog.range7d') }}</button>
          <button type="button" :class="{ active: range === '30d' }" @click="range = '30d'">{{ t('imglog.range30d') }}</button>
          <button type="button" :class="{ active: range === 'month' }" @click="range = 'month'">{{ t('imglog.rangeMonth') }}</button>
          <button type="button" :class="{ active: range === 'prev' }" @click="range = 'prev'">{{ t('imglog.rangePrev') }}</button>
          <button type="button" :class="{ active: range === 'custom' }" @click="range = 'custom'">{{ t('imglog.rangeCustom') }}</button>
        </div>
      </div>
      <div v-if="range === 'custom'" class="filter-group min-w-[260px]">
        <label>{{ t('imglog.filterCustomDate') }}</label>
        <div class="grid grid-cols-2 gap-2">
          <input v-model="customStart" class="input" type="date" :placeholder="t('imglog.startDate')" />
          <input v-model="customEnd" class="input" type="date" :placeholder="t('imglog.endDate')" />
        </div>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>{{ t('imglog.filterModel') }}</label>
        <select v-model="model" class="input">
          <option value="">{{ t('imglog.allModels') }}</option>
          <option v-for="m in models" :key="m.model" :value="m.model">{{ m.model }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>{{ t('imglog.filterApiKey') }}</label>
        <select v-model="apiKeyId" class="input">
          <option value="">{{ t('imglog.allKeys') }}</option>
          <option v-for="k in keys" :key="k.id" :value="String(k.id)">{{ k.name || k.prefix }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>{{ t('imglog.filterRequestId') }}</label>
        <input v-model="requestId" class="input" :placeholder="t('imglog.requestIdPlaceholder')" @keyup.enter="page = 1; load()" />
      </div>
      <div class="filter-group">
        <label>{{ t('imglog.filterResult') }}</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: result === 'all' }" @click="result = 'all'">{{ t('imglog.resultAll') }}</button>
          <button type="button" :class="{ active: result === 'success' }" @click="result = 'success'">{{ t('imglog.resultSuccess') }}</button>
          <button type="button" :class="{ active: result === 'failed' }" @click="result = 'failed'">{{ t('imglog.resultFailed') }}</button>
        </div>
      </div>
      <button class="btn-primary !min-h-0 !px-4 !py-2" type="button" @click="page = 1; load()">{{ t('imglog.btnQuery') }}</button>
    </div>

    <p class="text-xs text-muted">{{ t('imglog.note') }}</p>

    <p v-if="error" class="inline-alert" role="alert">{{ error }}</p>
    <p v-if="loading" class="text-sm text-muted">{{ t('imglog.loading') }}</p>

    <div v-else class="data-table-wrap">
      <table class="table w-full text-sm whitespace-nowrap">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3">{{ t('imglog.colRequestId') }}</th>
            <th class="px-4 py-3">{{ t('imglog.colTime') }}</th>
            <th class="px-4 py-3">{{ t('imglog.colModel') }}</th>
            <th class="px-4 py-3 text-right">{{ t('imglog.colCost') }}</th>
            <th class="px-4 py-3 text-right">{{ t('imglog.colDuration') }}</th>
            <th class="px-4 py-3">{{ t('imglog.colStatus') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="rows.length === 0">
            <td colspan="6" class="px-4 py-8 text-center text-muted">{{ t('imglog.emptyData') }}</td>
          </tr>
          <tr v-for="r in rows" :key="r.id">
            <td class="max-w-[17rem] truncate px-4 py-2.5 font-mono text-xs" :title="r.request_id">{{ r.request_id }}</td>
            <td class="px-4 py-2.5 text-muted">{{ d(new Date(r.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
            <td class="px-4 py-2.5 font-mono text-xs">{{ r.model }}</td>
            <td class="px-4 py-2.5 text-right font-mono">{{ fmtUsd(r.cost_usd) }}</td>
            <td class="px-4 py-2.5 text-right font-mono">{{ r.duration_ms }}ms</td>
            <td class="px-4 py-2.5"><span :class="r.status === 'success' ? 'badge-ok' : 'badge-danger'" :title="r.error_brief || ''">{{ statusText(r) }}</span></td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="flex items-center justify-between text-sm text-muted">
      <span>1-{{ rows.length }} / {{ total }}</span>
      <div class="flex items-center gap-2">
        <button class="btn-ghost !py-1" :disabled="page <= 1" @click="page--">‹</button>
        <span class="badge-muted">{{ page }}</span>
        <button class="btn-ghost !py-1" :disabled="page * pageSize >= total" @click="page++">›</button>
      </div>
    </div>
  </div>
</template>
