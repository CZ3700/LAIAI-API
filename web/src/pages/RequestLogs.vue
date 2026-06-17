<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
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
interface ModelStat { model: string }
interface KeyRow { id: number; name: string; prefix: string }

const { n, d } = useI18n()
const rows = ref<UsageRow[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const loading = ref(true)
const error = ref('')
const range = ref<'7d' | '30d' | 'month' | 'prev' | 'custom'>('7d')
const model = ref('')
const requestId = ref('')
const result = ref<'all' | 'success' | 'failed'>('all')
const customStart = ref('')
const customEnd = ref('')
const models = ref<ModelStat[]>([])
const keys = ref<KeyRow[]>([])
const selected = ref<UsageRow | null>(null)

function num(v: string | number): number {
  const out = Number(v)
  return Number.isFinite(out) ? out : 0
}
function fmtMoney(v: number): string {
  return `$${v.toFixed(v >= 1 ? 2 : 4)}`
}
function fmtMs(v: number): string {
  return v >= 1000 ? `${(v / 1000).toFixed(3)} 秒` : `${v}ms`
}
function rangeMatches(row: UsageRow): boolean {
  if (range.value === '30d') return true
  const created = new Date(row.created_at).getTime()
  if (!Number.isFinite(created)) return true
  const now = Date.now()
  if (range.value === '7d') return created >= now - 7 * 86400_000
  if (range.value === 'month') {
    const d0 = new Date()
    return new Date(created).getMonth() === d0.getMonth() && new Date(created).getFullYear() === d0.getFullYear()
  }
  if (range.value === 'prev') {
    const d0 = new Date()
    const prevMonth = d0.getMonth() === 0 ? 11 : d0.getMonth() - 1
    const prevYear = d0.getMonth() === 0 ? d0.getFullYear() - 1 : d0.getFullYear()
    const dt = new Date(created)
    return dt.getMonth() === prevMonth && dt.getFullYear() === prevYear
  }
  if (range.value === 'custom') {
    const start = customStart.value ? new Date(customStart.value).getTime() : -Infinity
    const end = customEnd.value ? new Date(customEnd.value).getTime() + 86400_000 : Infinity
    return created >= start && created <= end
  }
  return true
}

const visibleRows = computed(() => rows.value
  .filter((r) => rangeMatches(r))
  .filter((r) => !requestId.value || r.request_id.toLowerCase().includes(requestId.value.toLowerCase()))
  .filter((r) => result.value === 'all' || (result.value === 'success' ? r.status === 'success' : r.status !== 'success')))

async function loadMeta() {
  const [bm, k] = await Promise.allSettled([
    api<{ items: ModelStat[] }>('GET', '/api/usage/by-model?days=30'),
    api<{ keys: KeyRow[] }>('GET', '/api/keys'),
  ])
  if (bm.status === 'fulfilled') models.value = bm.value.items
  if (k.status === 'fulfilled') keys.value = k.value.keys
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const q = new URLSearchParams({ page: String(page.value), page_size: String(pageSize) })
    if (model.value) q.set('model', model.value)
    if (result.value === 'success') q.set('status', 'success')
    const r = await api<{ total: number; items: UsageRow[] }>('GET', `/api/usage?${q}`)
    rows.value = r.items
    total.value = r.total
  } catch (e) {
    error.value = errorMessage(e, '加载失败')
  } finally {
    loading.value = false
  }
}

watch([model, result], () => {
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
        <h1 class="app-page-title">请求日志</h1>
      </div>
    </div>

    <div class="toolbar-panel items-end">
      <div class="filter-group">
        <label>时间范围</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: range === '7d' }" @click="range = '7d'">7 天</button>
          <button type="button" :class="{ active: range === '30d' }" @click="range = '30d'">30 天</button>
          <button type="button" :class="{ active: range === 'month' }" @click="range = 'month'">本月</button>
          <button type="button" :class="{ active: range === 'prev' }" @click="range = 'prev'">上月</button>
          <button type="button" :class="{ active: range === 'custom' }" @click="range = 'custom'">自定义</button>
        </div>
      </div>
      <div v-if="range === 'custom'" class="filter-group min-w-[260px]">
        <label>自定义日期</label>
        <div class="grid grid-cols-2 gap-2">
          <input v-model="customStart" class="input" type="date" placeholder="开始日期" />
          <input v-model="customEnd" class="input" type="date" placeholder="结束日期" />
        </div>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>模型</label>
        <select v-model="model" class="input">
          <option value="">全部模型</option>
          <option v-for="m in models" :key="m.model" :value="m.model">{{ m.model }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>API 密钥（待接口）</label>
        <select class="input" disabled>
          <option value="">全部密钥</option>
          <option v-for="k in keys" :key="k.id" :value="String(k.id)">{{ k.name || k.prefix }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>请求 ID</label>
        <input v-model="requestId" class="input" placeholder="输入请求 ID" />
      </div>
      <div class="filter-group">
        <label>结果</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: result === 'all' }" @click="result = 'all'">全部</button>
          <button type="button" :class="{ active: result === 'success' }" @click="result = 'success'">成功</button>
          <button type="button" :class="{ active: result === 'failed' }" @click="result = 'failed'">失败</button>
        </div>
      </div>
      <button class="btn-primary !min-h-0 !px-4 !py-2" type="button" @click="load">查 询</button>
    </div>

    <p class="text-xs text-muted">模型和成功状态会请求后端；时间范围、请求 ID 是当前页本地筛选；API 密钥维度待后端返回 request-key 归属后启用。</p>

    <p v-if="error" class="inline-alert" role="alert">{{ error }}</p>
    <p v-if="loading" class="text-sm text-muted">加载中…</p>

    <div v-else class="data-table-wrap">
      <table class="table w-full text-sm whitespace-nowrap">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3">请求 ID</th>
            <th class="px-4 py-3">时间</th>
            <th class="px-4 py-3">模型</th>
            <th class="px-4 py-3">协议</th>
            <th class="px-4 py-3 text-right">Token 数</th>
            <th class="px-4 py-3 text-right">消耗</th>
            <th class="px-4 py-3 text-right">延迟</th>
            <th class="px-4 py-3">状态</th>
            <th class="sticky right-0 z-10 bg-panel px-4 py-3 text-right">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="visibleRows.length === 0">
            <td colspan="9" class="px-4 py-8 text-center text-muted">暂无数据</td>
          </tr>
          <tr v-for="r in visibleRows" :key="r.id">
            <td class="max-w-[17rem] truncate px-4 py-2.5 font-mono text-xs" :title="r.request_id">{{ r.request_id }}</td>
            <td class="px-4 py-2.5 text-muted">{{ d(new Date(r.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
            <td class="px-4 py-2.5 font-mono text-xs">{{ r.model }}</td>
            <td class="px-4 py-2.5"><span class="badge-muted">OpenAI</span></td>
            <td class="px-4 py-2.5 text-right font-mono">{{ n(r.prompt_tokens + r.completion_tokens) }}</td>
            <td class="px-4 py-2.5 text-right font-mono">{{ fmtMoney(num(r.cost_usd)) }}</td>
            <td class="px-4 py-2.5 text-right font-mono">{{ r.duration_ms }}ms</td>
            <td class="px-4 py-2.5"><span :class="r.status === 'success' ? 'badge-ok' : 'badge-danger'">{{ r.status_code || r.status }}</span></td>
            <td class="sticky right-0 bg-panel px-4 py-2.5 text-right"><button class="table-link" type="button" @click="selected = r">详情</button></td>
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

    <div v-if="selected" class="drawer-backdrop" @click.self="selected = null">
      <aside class="detail-drawer" role="dialog" aria-modal="true">
        <div class="drawer-head">
          <button class="btn-ghost !h-8 !min-h-0 !w-8 !px-0 !py-0" type="button" @click="selected = null">×</button>
          <h2 class="font-semibold">请求详情 #{{ selected.id }}</h2>
        </div>
        <div class="drawer-body">
          <div class="grid grid-cols-3 divide-x divide-line rounded border border-line bg-panel">
            <div class="p-4 text-center">
              <p class="text-xs text-muted">Tokens 合计</p>
              <p class="mt-1 font-mono text-2xl font-semibold">{{ n(selected.prompt_tokens + selected.completion_tokens) }}</p>
            </div>
            <div class="p-4 text-center">
              <p class="text-xs text-muted">消耗 USD</p>
              <p class="mt-1 font-mono text-2xl font-semibold">{{ fmtMoney(num(selected.cost_usd)) }}</p>
            </div>
            <div class="p-4 text-center">
              <p class="text-xs text-muted">延迟</p>
              <p class="mt-1 font-mono text-2xl font-semibold">{{ selected.duration_ms }}ms</p>
            </div>
          </div>

          <section class="drawer-section">
            <h3>标识</h3>
            <dl class="detail-grid">
              <div><dt>ID</dt><dd>{{ selected.id }}</dd></div>
              <div><dt>时间</dt><dd>{{ d(new Date(selected.created_at), { dateStyle: 'medium', timeStyle: 'medium' }) }}</dd></div>
              <div class="sm:col-span-2"><dt>请求 ID</dt><dd class="select-all break-all">{{ selected.request_id }}</dd></div>
              <div><dt>模型</dt><dd>{{ selected.model }}</dd></div>
              <div><dt>来源协议</dt><dd><span class="badge-muted">OpenAI</span></dd></div>
              <div><dt>请求模型（原始）</dt><dd>{{ selected.model }}</dd></div>
              <div><dt>API 密钥</dt><dd>接口暂未返回</dd></div>
            </dl>
          </section>

          <section class="drawer-section">
            <h3>用量与消耗</h3>
            <dl class="detail-grid">
              <div><dt>输入 Tokens</dt><dd>{{ n(selected.prompt_tokens) }}</dd></div>
              <div><dt>输出 Tokens</dt><dd>{{ n(selected.completion_tokens) }}</dd></div>
              <div><dt>Tokens 合计</dt><dd>{{ n(selected.prompt_tokens + selected.completion_tokens) }}</dd></div>
              <div><dt>推理 Tokens</dt><dd>—</dd></div>
              <div><dt>缓存输入 Tokens</dt><dd>—</dd></div>
              <div><dt>缓存读取 Tokens</dt><dd>—</dd></div>
              <div><dt>赠送消耗</dt><dd>—</dd></div>
              <div><dt>付费消耗</dt><dd>—</dd></div>
              <div><dt>消耗 USD</dt><dd>{{ fmtMoney(num(selected.cost_usd)) }}</dd></div>
            </dl>
          </section>

          <section class="drawer-section">
            <h3>结果</h3>
            <dl class="detail-grid">
              <div><dt>状态</dt><dd><span :class="selected.status === 'success' ? 'badge-ok' : 'badge-danger'">{{ selected.status_code || selected.status }}</span></dd></div>
              <div><dt>完成原因</dt><dd>{{ selected.status === 'success' ? 'stop' : '—' }}</dd></div>
              <div><dt>流式</dt><dd>{{ selected.is_stream ? '是' : '否' }}</dd></div>
              <div><dt>延迟</dt><dd>{{ fmtMs(selected.duration_ms) }}</dd></div>
              <div><dt>首 Token</dt><dd>{{ fmtMs(selected.frt_ms) }}</dd></div>
              <div><dt>错误摘要</dt><dd>{{ selected.error_brief || '—' }}</dd></div>
            </dl>
          </section>

          <section class="drawer-section">
            <h3>CLIENT</h3>
            <dl class="detail-grid">
              <div><dt>IP</dt><dd>—</dd></div>
              <div><dt>User-Agent</dt><dd>—</dd></div>
              <div><dt>Referer</dt><dd>—</dd></div>
              <div><dt>用量来源</dt><dd>{{ selected.usage_source || '—' }}</dd></div>
            </dl>
          </section>
        </div>
      </aside>
    </div>
  </div>
</template>
