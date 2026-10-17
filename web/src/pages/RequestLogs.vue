<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'
import { fmtUsd } from '@/lib/money'
import { useRoute, useRouter } from 'vue-router'
import { useAuth } from '@/stores/auth'
import DetailDrawer from '@/components/DetailDrawer.vue'
import SeedanceRequestLogs from '@/components/seedance/SeedanceRequestLogs.vue'
import { useSeedanceI18n } from '@/components/seedance/messages'

interface UsageRow {
  id: number
  request_id: string
  api_key_id?: number
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
  client_ip?: string
  user_agent?: string
  referer?: string
  created_at: string
  // 排障闭环（0045）：真实详情字段。空/0 = 历史行或该模态无此值。
  finish_reason?: string
  protocol?: string
  cached_tokens?: number
  reasoning_tokens?: number
  upstream_request_id?: string
  upstream_error_code?: string
}
interface ModelStat { model: string }
interface KeyRow { id: number; name: string; prefix: string }

const { t, n, d } = useI18n()
useSeedanceI18n()
const route = useRoute()
const router = useRouter()
const auth = useAuth()
const seedanceTab = computed(() => route.query.tab === 'seedance')
function switchTab(value: boolean) { closeDetail(); void router.replace({ query: value ? { tab: 'seedance' } : {} }) }
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
const modality = ref<'all' | 'chat' | 'image' | 'video'>('all') // 模态筛选（并入原图像日志页；值须与后端 modality 列精确匹配）
const customStart = ref('')
const customEnd = ref('')
const models = ref<ModelStat[]>([])
const keys = ref<KeyRow[]>([])
const selected = ref<UsageRow | null>(null)
let listVersion = 0
let metaVersion = 0
function closeDetail() { selected.value = null }

function statusText(r: UsageRow): string {
  if (r.status === 'success') return `${r.status_code || 200} OK`
  return r.status_code ? String(r.status_code) : r.status
}
function fmtMs(v: number): string {
  return v >= 1000 ? `${(v / 1000).toFixed(3)} ${t('reqlog.unitSec')}` : `${v}ms`
}
// 按 id 还原密钥名（已吊销/已清除的 key 不在 /api/keys 列表里，退化显示 #id）
function keyLabel(id?: number): string {
  if (!id) return '—'
  const k = keys.value.find((x) => x.id === id)
  return k ? k.name || k.prefix : `#${id}`
}
// 时间窗按 UTC 自然日界计算（与后端 created_at 的 UTC 存储/聚合口径一致），下推到后端 since/until 过滤，
// 由后端分页 + 计 total——不再对当前页本地按浏览器时区筛（旧实现会漏算窗口内分页外的行、且把边界日按本地月误分桶，
// 还让 footer 的 N/total 用本地子集分子配全局分母变得不自洽）。区间左闭右开 [since, until)。
function utcDate(ms: number): string {
  return new Date(ms).toISOString().slice(0, 10)
}
function windowParams(): { since?: string; until?: string } {
  const now = new Date()
  const y = now.getUTCFullYear()
  const m = now.getUTCMonth() // 0-based
  const thisMonth1 = `${y}-${String(m + 1).padStart(2, '0')}-01`
  if (range.value === '7d') return { since: utcDate(Date.now() - 6 * 86400_000) }
  if (range.value === '30d') return { since: utcDate(Date.now() - 29 * 86400_000) }
  if (range.value === 'month') return { since: thisMonth1 }
  if (range.value === 'prev') {
    const py = m === 0 ? y - 1 : y
    const pm = m === 0 ? 12 : m // 上月（1-based）
    return { since: `${py}-${String(pm).padStart(2, '0')}-01`, until: thisMonth1 }
  }
  if (range.value === 'custom') {
    const out: { since?: string; until?: string } = {}
    if (customStart.value) out.since = customStart.value
    if (customEnd.value) out.until = utcDate(new Date(customEnd.value).getTime() + 86400_000) // 含结束日 → 右开到次日
    return out
  }
  return {}
}

// 全部过滤（含 request_id 精确查）均服务端下推——修复：此前 request_id 只在当前页本地子串筛，
// 从响应头复制 X-Request-Id 粘进来、目标行不在当前页就查不到（排障闭环断裂，顶级审计修排名 5）。
const visibleRows = computed(() => rows.value)
// 入站方言标签（0045 真实落库；空=历史行，显示 —）
function protocolLabel(p?: string): string {
  if (p === 'anthropic') return 'Anthropic'
  if (p === 'responses') return 'Responses'
  if (p === 'openai') return 'OpenAI'
  return '—'
}

async function loadMeta() {
  const version = ++metaVersion; const org = auth.orgId
  const [bm, k] = await Promise.allSettled([
    api<{ items: ModelStat[] }>('GET', '/api/usage/by-model?days=30'),
    api<{ keys: KeyRow[] }>('GET', '/api/keys'),
  ])
  if (version !== metaVersion || org !== auth.orgId) return
  if (bm.status === 'fulfilled') models.value = bm.value.items
  if (k.status === 'fulfilled') keys.value = k.value.keys
}

async function load() {
  const version = ++listVersion; const org = auth.orgId
  loading.value = true
  error.value = ''
  try {
    const q = new URLSearchParams({ page: String(page.value), page_size: String(pageSize) })
    if (model.value) q.set('model', model.value)
    if (apiKeyId.value) q.set('api_key_id', apiKeyId.value)
    if (modality.value !== 'all') q.set('modality', modality.value) // 服务端模态过滤（0040）
    if (requestId.value.trim()) q.set('request_id', requestId.value.trim()) // 服务端精确查（uk_inv_request，跨页可命中）
    if (result.value !== 'all') q.set('status', result.value) // 'success' 精确；'failed' 后端识别为非 success
    const w = windowParams()
    if (w.since) q.set('since', w.since)
    if (w.until) q.set('until', w.until)
    const r = await api<{ total: number; items: UsageRow[] }>('GET', `/api/usage?${q}`)
    if (version !== listVersion || org !== auth.orgId) return
    rows.value = r.items
    total.value = r.total
  } catch (e) {
    if (version === listVersion && org === auth.orgId) error.value = errorMessage(e, t('reqlog.loadFailed'))
  } finally {
    if (version === listVersion && org === auth.orgId) loading.value = false
  }
}

watch([model, apiKeyId, requestId, result, modality, range, customStart, customEnd], () => {
  page.value = 1
  void load()
})
watch(page, () => void load())
watch(() => auth.orgId, () => {
  closeDetail(); rows.value = []; models.value = []; keys.value = []; total.value = 0
  void loadMeta(); void load()
})
watch(seedanceTab, closeDetail)
onBeforeUnmount(() => { listVersion++; metaVersion++; closeDetail() })
onMounted(() => {
  void loadMeta()
  void load()
})
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <h1 class="app-page-title">{{ t('reqlog.title') }}</h1>
      </div>
    </div>

    <div class="segmented-control mb-4" role="tablist"><button type="button" role="tab" :aria-selected="!seedanceTab" :class="{ active: !seedanceTab }" @click="switchTab(false)">{{ t('seedance.general') }}</button><button type="button" role="tab" :aria-selected="seedanceTab" :class="{ active: seedanceTab }" @click="switchTab(true)">{{ t('seedance.tab') }}</button></div>
    <SeedanceRequestLogs v-if="seedanceTab" />
    <template v-else>
    <div class="toolbar-panel items-end">
      <div class="filter-group">
        <label>{{ t('reqlog.filterTimeRange') }}</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: range === '7d' }" @click="range = '7d'">{{ t('reqlog.range7d') }}</button>
          <button type="button" :class="{ active: range === '30d' }" @click="range = '30d'">{{ t('reqlog.range30d') }}</button>
          <button type="button" :class="{ active: range === 'month' }" @click="range = 'month'">{{ t('reqlog.rangeMonth') }}</button>
          <button type="button" :class="{ active: range === 'prev' }" @click="range = 'prev'">{{ t('reqlog.rangePrev') }}</button>
          <button type="button" :class="{ active: range === 'custom' }" @click="range = 'custom'">{{ t('reqlog.rangeCustom') }}</button>
        </div>
      </div>
      <div v-if="range === 'custom'" class="filter-group min-w-[260px]">
        <label>{{ t('reqlog.customDate') }}</label>
        <div class="grid grid-cols-2 gap-2">
          <input v-model="customStart" class="input" type="date" :placeholder="t('reqlog.startDate')" />
          <input v-model="customEnd" class="input" type="date" :placeholder="t('reqlog.endDate')" />
        </div>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>{{ t('reqlog.filterModel') }}</label>
        <select v-model="model" class="input">
          <option value="">{{ t('reqlog.allModels') }}</option>
          <option v-for="m in models" :key="m.model" :value="m.model">{{ m.model }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>{{ t('reqlog.filterApiKey') }}</label>
        <select v-model="apiKeyId" class="input">
          <option value="">{{ t('reqlog.allKeys') }}</option>
          <option v-for="k in keys" :key="k.id" :value="String(k.id)">{{ k.name || k.prefix }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[160px]">
        <label>{{ t('reqlog.filterModality') }}</label>
        <select v-model="modality" class="input">
          <option value="all">{{ t('reqlog.modAll') }}</option>
          <option value="chat">{{ t('reqlog.modChat') }}</option>
          <option value="image">{{ t('reqlog.modImage') }}</option>
          <option value="video">{{ t('reqlog.modVideo') }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>{{ t('reqlog.filterRequestId') }}</label>
        <input v-model="requestId" class="input" :placeholder="t('reqlog.requestIdPlaceholder')" />
      </div>
      <div class="filter-group">
        <label>{{ t('reqlog.filterResult') }}</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: result === 'all' }" @click="result = 'all'">{{ t('reqlog.resultAll') }}</button>
          <button type="button" :class="{ active: result === 'success' }" @click="result = 'success'">{{ t('reqlog.resultSuccess') }}</button>
          <button type="button" :class="{ active: result === 'failed' }" @click="result = 'failed'">{{ t('reqlog.resultFailed') }}</button>
        </div>
      </div>
      <button class="btn-primary !min-h-0 !px-4 !py-2" type="button" @click="load">{{ t('reqlog.btnQuery') }}</button>
    </div>

    <p class="text-xs text-muted">{{ t('reqlog.localFilterNote') }}</p>

    <p v-if="error" class="inline-alert" role="alert">{{ error }}</p>
    <p v-if="loading" class="text-sm text-muted">{{ t('reqlog.loading') }}</p>

    <div v-else class="data-table-wrap">
      <table class="table w-full text-sm whitespace-nowrap">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3">{{ t('reqlog.colRequestId') }}</th>
            <th class="px-4 py-3">{{ t('reqlog.colTime') }}</th>
            <th class="px-4 py-3">{{ t('reqlog.colModel') }}</th>
            <th class="px-4 py-3">{{ t('reqlog.colProtocol') }}</th>
            <th class="px-4 py-3 text-right">{{ t('reqlog.colTokens') }}</th>
            <th class="px-4 py-3 text-right">{{ t('reqlog.colCost') }}</th>
            <th class="px-4 py-3 text-right">{{ t('reqlog.colLatency') }}</th>
            <th class="px-4 py-3">{{ t('reqlog.colStatus') }}</th>
            <th class="sticky right-0 z-10 bg-panel px-4 py-3 text-right">{{ t('reqlog.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="visibleRows.length === 0">
            <td colspan="9" class="px-4 py-8 text-center text-muted">{{ t('reqlog.emptyData') }}</td>
          </tr>
          <tr v-for="r in visibleRows" :key="r.id">
            <td class="max-w-[17rem] truncate px-4 py-2.5 font-mono text-xs" :title="r.request_id">{{ r.request_id }}</td>
            <td class="px-4 py-2.5 text-muted">{{ d(new Date(r.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
            <td class="px-4 py-2.5 font-mono text-xs">{{ r.model }}</td>
            <td class="px-4 py-2.5"><span class="badge-muted">{{ protocolLabel(r.protocol) }}</span></td>
            <td class="px-4 py-2.5 text-right font-mono">{{ n(r.prompt_tokens + r.completion_tokens) }}</td>
            <td class="px-4 py-2.5 text-right font-mono">{{ fmtUsd(r.cost_usd) }}</td>
            <td class="px-4 py-2.5 text-right font-mono">{{ r.duration_ms }}ms</td>
            <td class="px-4 py-2.5"><span :class="r.status === 'success' ? 'badge-ok' : 'badge-danger'">{{ statusText(r) }}</span></td>
            <td class="sticky right-0 bg-panel px-4 py-2.5 text-right"><button class="table-link" type="button" @click="selected = r">{{ t('reqlog.btnDetail') }}</button></td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="flex items-center justify-between text-sm text-muted">
      <span>1-{{ visibleRows.length }} / {{ total }}</span>
      <div class="flex items-center gap-2">
        <button class="btn-ghost !py-1" :disabled="page <= 1" @click="page--">‹</button>
        <span class="badge-muted">{{ page }}</span>
        <button class="btn-ghost !py-1" :disabled="page * pageSize >= total" @click="page++">›</button>
      </div>
    </div>

    <DetailDrawer :open="!!selected" :title="t('reqlog.drawerTitle', { id: selected?.id ?? '' })" :close-label="t('seedance.close')" size="lg" @update:open="open => { if (!open) closeDetail() }">
        <div v-if="selected" class="space-y-4">
          <div class="grid grid-cols-3 divide-x divide-line rounded border border-line bg-panel">
            <div class="p-4 text-center">
              <p class="text-xs text-muted">{{ t('reqlog.statTokensTotal') }}</p>
              <p class="mt-1 font-mono text-2xl font-semibold">{{ n(selected.prompt_tokens + selected.completion_tokens) }}</p>
            </div>
            <div class="p-4 text-center">
              <p class="text-xs text-muted">{{ t('reqlog.statCostUSD') }}</p>
              <p class="mt-1 font-mono text-2xl font-semibold">{{ fmtUsd(selected.cost_usd) }}</p>
            </div>
            <div class="p-4 text-center">
              <p class="text-xs text-muted">{{ t('reqlog.statLatency') }}</p>
              <p class="mt-1 font-mono text-2xl font-semibold">{{ selected.duration_ms }}ms</p>
            </div>
          </div>

          <section class="drawer-section">
            <h3>{{ t('reqlog.sectionIdentity') }}</h3>
            <dl class="detail-grid">
              <div><dt>ID</dt><dd>{{ selected.id }}</dd></div>
              <div><dt>{{ t('reqlog.dtTime') }}</dt><dd>{{ d(new Date(selected.created_at), { dateStyle: 'medium', timeStyle: 'medium' }) }}</dd></div>
              <div class="sm:col-span-2"><dt>{{ t('reqlog.dtRequestId') }}</dt><dd class="select-all break-all">{{ selected.request_id }}</dd></div>
              <div><dt>{{ t('reqlog.dtModel') }}</dt><dd>{{ selected.model }}</dd></div>
              <div><dt>{{ t('reqlog.dtSourceProtocol') }}</dt><dd><span class="badge-muted">{{ protocolLabel(selected.protocol) }}</span></dd></div>
              <div><dt>{{ t('reqlog.dtRequestModelRaw') }}</dt><dd>{{ selected.model }}</dd></div>
              <div><dt>{{ t('reqlog.dtApiKey') }}</dt><dd>{{ keyLabel(selected.api_key_id) }}</dd></div>
            </dl>
          </section>

          <section class="drawer-section">
            <h3>{{ t('reqlog.sectionUsage') }}</h3>
            <dl class="detail-grid">
              <div><dt>{{ t('reqlog.dtInputTokens') }}</dt><dd>{{ n(selected.prompt_tokens) }}</dd></div>
              <div><dt>{{ t('reqlog.dtOutputTokens') }}</dt><dd>{{ n(selected.completion_tokens) }}</dd></div>
              <div><dt>{{ t('reqlog.dtTokensTotal') }}</dt><dd>{{ n(selected.prompt_tokens + selected.completion_tokens) }}</dd></div>
              <div><dt>{{ t('reqlog.dtReasoningTokens') }}</dt><dd>{{ selected.reasoning_tokens ? n(selected.reasoning_tokens) : '—' }}</dd></div>
              <div><dt>{{ t('reqlog.dtCacheReadTokens') }}</dt><dd>{{ selected.cached_tokens ? n(selected.cached_tokens) : '—' }}</dd></div>
              <div><dt>{{ t('reqlog.dtCostUSD') }}</dt><dd>{{ fmtUsd(selected.cost_usd) }}</dd></div>
            </dl>
          </section>

          <section class="drawer-section">
            <h3>{{ t('reqlog.sectionResult') }}</h3>
            <dl class="detail-grid">
              <div><dt>{{ t('reqlog.dtStatus') }}</dt><dd><span :class="selected.status === 'success' ? 'badge-ok' : 'badge-danger'">{{ statusText(selected) }}</span></dd></div>
              <div><dt>{{ t('reqlog.dtFinishReason') }}</dt><dd>{{ selected.finish_reason || '—' }}</dd></div>
              <div><dt>{{ t('reqlog.dtStream') }}</dt><dd>{{ selected.is_stream ? t('reqlog.yes') : t('reqlog.no') }}</dd></div>
              <div><dt>{{ t('reqlog.dtLatency') }}</dt><dd>{{ fmtMs(selected.duration_ms) }}</dd></div>
              <div><dt>{{ t('reqlog.dtFirstToken') }}</dt><dd>{{ fmtMs(selected.frt_ms) }}</dd></div>
              <div><dt>{{ t('reqlog.dtErrorBrief') }}</dt><dd>{{ selected.error_brief || '—' }}</dd></div>
              <div><dt>{{ t('reqlog.dtUpstreamErrorCode') }}</dt><dd class="font-mono">{{ selected.upstream_error_code || '—' }}</dd></div>
              <div class="sm:col-span-2"><dt>{{ t('reqlog.dtUpstreamRequestId') }}</dt><dd class="select-all break-all font-mono">{{ selected.upstream_request_id || '—' }}</dd></div>
            </dl>
          </section>

          <section class="drawer-section">
            <h3>CLIENT</h3>
            <dl class="detail-grid">
              <div><dt>IP</dt><dd class="select-all break-all">{{ selected.client_ip || '—' }}</dd></div>
              <div class="sm:col-span-2"><dt>User-Agent</dt><dd class="break-all">{{ selected.user_agent || '—' }}</dd></div>
              <div class="sm:col-span-2"><dt>Referer</dt><dd class="break-all">{{ selected.referer || '—' }}</dd></div>
              <div><dt>{{ t('reqlog.dtUsageSource') }}</dt><dd>{{ selected.usage_source || '—' }}</dd></div>
            </dl>
          </section>
        </div>
    </DetailDrawer>
    </template>
  </div>
</template>
