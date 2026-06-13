<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminApi, adminToken, ApiError, errorMessage, uuid } from '@/lib/api'

interface Provider {
  name: string
  display_name: string
  default_base_url: string
  adapter: string
}
interface Channel {
  id: number
  name: string
  provider: string
  base_url: string
  models: string
  model_count: number
  weight: number
  priority: number
  strict_response: boolean
  model_mapping: string
  calls: number
  status: number
}
interface Overview {
  window_days: number
  channels: { total: number; active: number; disabled: number; auto_disabled: number }
  calls_today: number
  calls_window: number
  success_rate: number
  revenue_window: string
  top_models: { model: string; calls: number; cost: string }[]
  channel_health: { id: number; name: string; status: number; calls: number }[]
}
interface TestResult {
  ok: boolean
  status: number
  latency_ms: number
  model: string
  message: string
}
interface OrgRow {
  id: number
  name: string
  owner_email: string
  members: number
  balance: string
  held: string
  status: number
}
interface Price {
  Model: string
  InputUSDPer1M: string
  OutputUSDPer1M: string
  ImageUSD: string
}

const { t } = useI18n()

// —— token 门 ——
const tokenInput = ref('')
const unlocked = ref(adminToken.get() !== '')
const error = ref('')

function unlock() {
  adminToken.set(tokenInput.value.trim())
  tokenInput.value = ''
  unlocked.value = true
  error.value = ''
  void loadAll()
}
function lock() {
  adminToken.clear()
  unlocked.value = false
}

function showError(e: unknown) {
  error.value = errorMessage(e, t('common.error'))
  if (e instanceof ApiError && e.status === 401) unlocked.value = false
}

// —— tabs ——
const tab = ref<'overview' | 'channels' | 'prices' | 'credits' | 'orgs'>('overview')

// —— 平台概览 ——
const overview = ref<Overview | null>(null)
async function loadOverview() {
  overview.value = await adminApi<Overview>('GET', '/api/admin/overview')
}
const maxModelCalls = computed(() =>
  Math.max(1, ...(overview.value?.top_models ?? []).map((m) => m.calls)),
)
function chStatusBadge(s: number): string {
  return s === 1 ? 'badge-ok' : s === 2 ? 'badge-danger' : 'badge-muted'
}

// —— 渠道 ——
const providers = ref<Provider[]>([])
const channels = ref<Channel[]>([])
const ch = ref({ name: '', provider: 'openai', base_url: '', api_key: '', models: '', weight: 1, priority: 0 })
const chBusy = ref(false)
const providerDefault = computed(
  () => providers.value.find((p) => p.name === ch.value.provider)?.default_base_url ?? '',
)

async function loadChannels() {
  const [chs, provs] = await Promise.all([
    adminApi<{ channels: Channel[] }>('GET', '/api/admin/channels'),
    adminApi<{ providers: Provider[] }>('GET', '/api/admin/providers'),
  ])
  channels.value = chs.channels
  providers.value = provs.providers
}
async function addChannel() {
  chBusy.value = true
  try {
    await adminApi('POST', '/api/admin/channels', {
      ...ch.value,
      models: ch.value.models.split(',').map((m) => m.trim()).filter(Boolean),
    })
    ch.value = { name: '', provider: ch.value.provider, base_url: '', api_key: '', models: '', weight: 1, priority: 0 }
    await loadChannels()
    error.value = ''
  } catch (e) {
    showError(e)
  } finally {
    chBusy.value = false
  }
}
async function disableChannel(id: number) {
  if (!confirm(t('admin.chDisableConfirm'))) return
  try {
    await adminApi('DELETE', `/api/admin/channels/${id}`)
    await loadChannels()
  } catch (e) {
    showError(e)
  }
}
function statusLabel(s: number): string {
  if (s === 1) return t('admin.chStatusActive')
  if (s === 2) return t('admin.chStatusAuto')
  return t('admin.chStatusDisabled')
}

// —— 渠道一键测试 ——
const testingId = ref<number | null>(null)
const testResult = ref<{ name: string; r: TestResult } | null>(null)

async function testChannel(c: Channel) {
  testingId.value = c.id
  testResult.value = null
  try {
    const r = await adminApi<TestResult>('POST', `/api/admin/channels/${c.id}/test`)
    testResult.value = { name: c.name, r }
    error.value = ''
  } catch (e) {
    showError(e)
  } finally {
    testingId.value = null
  }
}

// 模型映射 ⇄ 行文本（每行 对外名=上游名）；textarea 比 JSON 输入友好
function mappingToLines(json: string): string {
  if (!json) return ''
  try {
    return Object.entries(JSON.parse(json) as Record<string, string>)
      .map(([k, v]) => `${k}=${v}`)
      .join('\n')
  } catch {
    return ''
  }
}
function linesToMapping(lines: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of lines.split('\n')) {
    const i = line.indexOf('=')
    if (i <= 0) continue
    const k = line.slice(0, i).trim()
    const v = line.slice(i + 1).trim()
    if (k && v) out[k] = v
  }
  return out
}

// —— 编辑渠道 ——
const editing = ref<Channel | null>(null)
const editForm = ref({ base_url: '', models: '', api_key: '', weight: 1, priority: 0, strict_response: true, mapping: '' })
const editBusy = ref(false)

function openEdit(ch: Channel) {
  editing.value = ch
  editForm.value = {
    base_url: ch.base_url,
    models: ch.models,
    api_key: '', // 留空=保留原凭证
    weight: ch.weight,
    priority: ch.priority,
    strict_response: ch.strict_response,
    mapping: mappingToLines(ch.model_mapping),
  }
}
async function saveEdit() {
  if (!editing.value) return
  editBusy.value = true
  try {
    const body: Record<string, unknown> = {
      base_url: editForm.value.base_url,
      models: editForm.value.models.split(',').map((m) => m.trim()).filter(Boolean),
      weight: editForm.value.weight,
      priority: editForm.value.priority,
      strict_response: editForm.value.strict_response,
      model_mapping: linesToMapping(editForm.value.mapping), // 空对象 = 清空映射
    }
    if (editForm.value.api_key) body.api_key = editForm.value.api_key
    await adminApi('PUT', `/api/admin/channels/${editing.value.id}`, body)
    editing.value = null
    await loadChannels()
    error.value = ''
  } catch (e) {
    showError(e)
  } finally {
    editBusy.value = false
  }
}

// —— 组织 ——
const orgs = ref<OrgRow[]>([])
async function loadOrgs() {
  orgs.value = (await adminApi<{ orgs: OrgRow[] }>('GET', '/api/admin/orgs')).orgs
}
// 从组织行一键带入授信表单
function grantTo(o: OrgRow) {
  cr.value.org_id = String(o.id)
  tab.value = 'credits'
}

// —— 定价 ——
const prices = ref<Price[]>([])
const pr = ref({ model: '', input_usd_per_1m: '0', output_usd_per_1m: '0', image_usd: '' })
const prBusy = ref(false)

async function loadPrices() {
  const r = await adminApi<{ prices: Price[] }>('GET', '/api/admin/model-prices')
  prices.value = r.prices
}
async function upsertPrice() {
  prBusy.value = true
  try {
    const body: Record<string, string> = {
      model: pr.value.model,
      input_usd_per_1m: pr.value.input_usd_per_1m,
      output_usd_per_1m: pr.value.output_usd_per_1m,
    }
    if (pr.value.image_usd) body.image_usd = pr.value.image_usd
    await adminApi('POST', '/api/admin/model-prices', body)
    pr.value = { model: '', input_usd_per_1m: '0', output_usd_per_1m: '0', image_usd: '' }
    await loadPrices()
    error.value = ''
  } catch (e) {
    showError(e)
  } finally {
    prBusy.value = false
  }
}

// —— 授信 ——
const cr = ref({ org_id: '', amount_usd: '', reason: '' })
const crBusy = ref(false)
const crResult = ref<{ balance_after: string; already_applied: boolean } | null>(null)

async function grant() {
  crBusy.value = true
  crResult.value = null
  try {
    // 幂等键由前端生成并随表单存活：重复点击/网络重试不会重复入账
    const r = await adminApi<{ balance_after: string; already_applied: boolean }>(
      'POST',
      '/api/admin/credits',
      {
        org_id: Number(cr.value.org_id),
        amount_usd: cr.value.amount_usd,
        reason: cr.value.reason,
        idempotency_key: uuid(),
      },
    )
    crResult.value = r
    error.value = ''
  } catch (e) {
    showError(e)
  } finally {
    crBusy.value = false
  }
}

async function loadAll() {
  try {
    await Promise.all([loadOverview(), loadChannels(), loadPrices(), loadOrgs()])
  } catch (e) {
    showError(e)
  }
}
onMounted(() => {
  if (unlocked.value) void loadAll()
})
</script>

<template>
  <div class="max-w-4xl space-y-6">
    <h1 class="text-2xl font-semibold tracking-tight">{{ t('admin.title') }}</h1>

    <!-- token 门 -->
    <form v-if="!unlocked" class="card max-w-md p-6 space-y-4" @submit.prevent="unlock">
      <div>
        <label class="label" for="admintoken">{{ t('admin.token') }}</label>
        <input id="admintoken" v-model="tokenInput" class="input" type="password" required autocomplete="off" />
        <p class="mt-1.5 text-xs text-muted">{{ t('admin.tokenHint') }}</p>
      </div>
      <button class="btn-primary">{{ t('admin.unlock') }}</button>
    </form>

    <template v-else>
      <div class="flex items-center justify-between">
        <div class="flex gap-1" role="tablist">
          <button
            v-for="tb in ['overview', 'channels', 'prices', 'credits', 'orgs'] as const"
            :key="tb"
            type="button"
            role="tab"
            :aria-selected="tab === tb"
            class="btn-ghost !py-1.5"
            :class="tab === tb ? '!bg-panel !text-ink' : '!border-transparent text-muted'"
            @click="tab = tb"
          >
            {{ t(`admin.${tb}`) }}
          </button>
        </div>
        <button class="btn-ghost !py-1.5 text-xs" @click="lock">{{ t('admin.clear') }}</button>
      </div>

      <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>

      <!-- 概览 -->
      <template v-if="tab === 'overview' && overview">
        <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <div class="card p-4">
            <p class="text-xs text-muted">{{ t('admin.ovChannels') }}</p>
            <p class="mt-1 text-2xl font-semibold font-mono">
              {{ overview.channels.active }}<span class="text-base text-muted">/{{ overview.channels.total }}</span>
            </p>
            <p class="mt-1 text-xs text-muted">
              <span v-if="overview.channels.auto_disabled" class="text-danger">
                {{ overview.channels.auto_disabled }} {{ t('admin.chStatusAuto') }}
              </span>
              <span v-else>{{ t('admin.ovAllHealthy') }}</span>
            </p>
          </div>
          <div class="card p-4">
            <p class="text-xs text-muted">{{ t('dashboard.todayCalls') }}</p>
            <p class="mt-1 text-2xl font-semibold font-mono">{{ overview.calls_today }}</p>
            <p class="mt-1 text-xs text-muted">{{ t('admin.ovWindowCalls', { n: overview.calls_window, days: overview.window_days }) }}</p>
          </div>
          <div class="card p-4">
            <p class="text-xs text-muted">{{ t('admin.ovSuccessRate') }}</p>
            <p class="mt-1 text-2xl font-semibold font-mono">{{ (overview.success_rate * 100).toFixed(1) }}%</p>
            <p class="mt-1 text-xs text-muted">{{ t('admin.ovLastDays', { days: overview.window_days }) }}</p>
          </div>
          <div class="card p-4">
            <p class="text-xs text-muted">{{ t('admin.ovRevenue') }}</p>
            <p class="mt-1 text-2xl font-semibold font-mono">${{ Number(overview.revenue_window).toFixed(4) }}</p>
            <p class="mt-1 text-xs text-muted">{{ t('admin.ovLastDays', { days: overview.window_days }) }}</p>
          </div>
        </div>

        <div class="grid gap-4 lg:grid-cols-2">
          <!-- 模型 top -->
          <div class="card p-5">
            <h2 class="mb-4 font-semibold">{{ t('admin.ovTopModels') }}</h2>
            <div v-if="overview.top_models.length === 0" class="text-sm text-muted">{{ t('common.empty') }}</div>
            <div v-else class="space-y-3">
              <div v-for="m in overview.top_models" :key="m.model">
                <div class="flex items-baseline justify-between text-sm">
                  <span class="font-mono truncate pr-2">{{ m.model }}</span>
                  <span class="shrink-0 text-muted">{{ m.calls }} · ${{ Number(m.cost).toFixed(4) }}</span>
                </div>
                <div class="mt-1 h-1.5 rounded-full bg-line">
                  <div
                    class="h-full rounded-full"
                    style="background: linear-gradient(90deg, rgb(var(--c-accent)), rgb(var(--c-accent-2)))"
                    :style="{ width: `${(m.calls / maxModelCalls) * 100}%` }"
                  ></div>
                </div>
              </div>
            </div>
          </div>

          <!-- 渠道健康 -->
          <div class="card p-5">
            <h2 class="mb-4 font-semibold">{{ t('admin.ovChannelHealth') }}</h2>
            <div v-if="overview.channel_health.length === 0" class="text-sm text-muted">{{ t('common.empty') }}</div>
            <div v-else class="space-y-2">
              <div
                v-for="h in overview.channel_health"
                :key="h.id"
                class="flex items-center justify-between text-sm"
              >
                <span class="flex items-center gap-2 truncate pr-2">
                  <span :class="chStatusBadge(h.status)">{{ statusLabel(h.status) }}</span>
                  {{ h.name }}
                </span>
                <span class="shrink-0 font-mono text-muted">{{ h.calls }} {{ t('admin.ovCalls') }}</span>
              </div>
            </div>
          </div>
        </div>
      </template>

      <!-- 渠道 -->
      <template v-if="tab === 'channels'">
        <form class="card p-5 grid grid-cols-2 gap-4" @submit.prevent="addChannel">
          <div>
            <label class="label" for="ch-name">{{ t('admin.chName') }}</label>
            <input id="ch-name" v-model="ch.name" class="input" required />
          </div>
          <div>
            <label class="label" for="ch-provider">{{ t('admin.chProvider') }}</label>
            <select id="ch-provider" v-model="ch.provider" class="input">
              <option v-for="p in providers" :key="p.name" :value="p.name">
                {{ p.display_name }} ({{ p.name }})
              </option>
            </select>
          </div>
          <div class="col-span-2">
            <label class="label" for="ch-baseurl">{{ t('admin.chBaseUrl') }}</label>
            <input id="ch-baseurl" v-model="ch.base_url" class="input font-mono text-xs" :placeholder="providerDefault || 'https://…'" />
            <p class="mt-1 text-xs text-muted">{{ t('admin.chBaseUrlHint') }}</p>
          </div>
          <div class="col-span-2">
            <label class="label" for="ch-key">{{ t('admin.chKey') }}</label>
            <input id="ch-key" v-model="ch.api_key" class="input font-mono text-xs" type="password" required autocomplete="off" />
          </div>
          <div class="col-span-2">
            <label class="label" for="ch-models">{{ t('admin.chModels') }}</label>
            <input id="ch-models" v-model="ch.models" class="input font-mono text-xs" placeholder="gpt-4o-mini, gpt-4o" required />
          </div>
          <div>
            <label class="label" for="ch-weight">{{ t('admin.chWeight') }}</label>
            <input id="ch-weight" v-model.number="ch.weight" class="input" type="number" min="0" />
          </div>
          <div>
            <label class="label" for="ch-priority">{{ t('admin.chPriority') }}</label>
            <input id="ch-priority" v-model.number="ch.priority" class="input" type="number" />
          </div>
          <div class="col-span-2">
            <button class="btn-primary" :disabled="chBusy">{{ t('admin.chAdd') }}</button>
          </div>
        </form>

        <!-- 测试结果条 -->
        <div
          v-if="testResult"
          class="card p-4 text-sm flex items-start gap-3"
          role="status"
          aria-live="polite"
        >
          <span :class="testResult.r.ok ? 'text-ok' : 'text-danger'" class="font-medium shrink-0">
            {{ testResult.name }} · {{ testResult.r.ok ? t('admin.chTestOk', { ms: testResult.r.latency_ms }) : t('admin.chTestFail') }}
          </span>
          <span v-if="!testResult.r.ok" class="text-muted break-all">{{ testResult.r.message }}</span>
          <button class="btn-ghost !py-0.5 ml-auto text-xs shrink-0" @click="testResult = null">✕</button>
        </div>

        <div class="card overflow-x-auto">
          <table class="table w-full text-sm whitespace-nowrap">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">ID</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.chName') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.chProvider') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.chBaseUrl') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.chModels') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.chWeight') }}/{{ t('admin.chPriority') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.ovCalls') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('keys.status') }}</th>
                <th class="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="channels.length === 0">
                <td colspan="9" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <tr v-for="c in channels" :key="c.id" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 text-muted">{{ c.id }}</td>
                <td class="px-4 py-2.5">
                  {{ c.name }}
                  <span v-if="c.model_mapping" class="ml-1 badge-muted" :title="t('admin.chMapping')">map</span>
                  <span v-if="!c.strict_response" class="ml-1 badge-danger" :title="t('admin.chStrict')">lax</span>
                </td>
                <td class="px-4 py-2.5 font-mono text-xs">{{ c.provider }}</td>
                <td class="px-4 py-2.5 font-mono text-xs max-w-48 truncate text-muted" :title="c.base_url">{{ c.base_url }}</td>
                <td class="px-4 py-2.5 font-mono text-xs max-w-48 truncate" :title="c.models">
                  {{ c.models }}
                  <span class="text-muted">({{ c.model_count }})</span>
                </td>
                <td class="px-4 py-2.5 text-right font-mono">{{ c.weight }} / {{ c.priority }}</td>
                <td class="px-4 py-2.5 text-right font-mono" :class="c.calls ? '' : 'text-muted'">{{ c.calls }}</td>
                <td class="px-4 py-2.5">
                  <span :class="c.status === 1 ? 'badge-ok' : c.status === 2 ? 'badge-danger' : 'badge-muted'">
                    {{ statusLabel(c.status) }}
                  </span>
                </td>
                <td class="px-4 py-2.5 text-right space-x-2">
                  <button class="btn-ghost !py-1 text-xs" :disabled="testingId === c.id" @click="testChannel(c)">
                    {{ testingId === c.id ? t('admin.chTesting') : t('admin.chTest') }}
                  </button>
                  <button class="btn-ghost !py-1 text-xs" @click="openEdit(c)">{{ t('admin.chEdit') }}</button>
                  <button v-if="c.status === 1" class="btn-danger !py-1 text-xs" @click="disableChannel(c.id)">
                    {{ t('admin.chDisable') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- 编辑渠道弹窗 -->
        <div v-if="editing" class="modal-backdrop" @click.self="editing = null">
          <form class="card modal-panel w-full max-w-md p-6 space-y-4" @submit.prevent="saveEdit">
            <h2 class="font-semibold">{{ t('admin.chEdit') }} · {{ editing.name }}</h2>
            <div>
              <label class="label" for="edit-baseurl">{{ t('admin.chBaseUrl') }}</label>
              <input id="edit-baseurl" v-model="editForm.base_url" class="input font-mono text-xs" />
            </div>
            <div>
              <label class="label" for="edit-models">{{ t('admin.chModels') }}</label>
              <input id="edit-models" v-model="editForm.models" class="input font-mono text-xs" />
            </div>
            <div>
              <label class="label" for="edit-key">{{ t('admin.chKey') }}</label>
              <input id="edit-key" v-model="editForm.api_key" class="input font-mono text-xs" type="password"
                autocomplete="off" :placeholder="t('admin.chKeyKeep')" />
            </div>
            <div class="grid grid-cols-2 gap-3">
              <div>
                <label class="label" for="edit-weight">{{ t('admin.chWeight') }}</label>
                <input id="edit-weight" v-model.number="editForm.weight" class="input" type="number" min="0" />
              </div>
              <div>
                <label class="label" for="edit-priority">{{ t('admin.chPriority') }}</label>
                <input id="edit-priority" v-model.number="editForm.priority" class="input" type="number" />
              </div>
            </div>
            <div>
              <label class="label" for="edit-mapping">{{ t('admin.chMapping') }}</label>
              <textarea
                id="edit-mapping"
                v-model="editForm.mapping"
                class="input font-mono text-xs"
                rows="3"
                :placeholder="'gpt-5.2=gpt-5.2-internal'"
              ></textarea>
              <p class="mt-1 text-xs text-muted">{{ t('admin.chMappingHint') }}</p>
            </div>
            <label class="flex items-center gap-2 text-sm">
              <input v-model="editForm.strict_response" type="checkbox" class="accent-accent" />
              {{ t('admin.chStrict') }}
            </label>
            <p class="text-xs text-muted">{{ t('admin.chStrictHint') }}</p>
            <div class="flex justify-end gap-2">
              <button type="button" class="btn-ghost" @click="editing = null">{{ t('common.cancel') }}</button>
              <button class="btn-primary" :disabled="editBusy">{{ t('common.confirm') }}</button>
            </div>
          </form>
        </div>
      </template>

      <!-- 定价 -->
      <template v-if="tab === 'prices'">
        <form class="card p-5 grid grid-cols-4 gap-4 items-end" @submit.prevent="upsertPrice">
          <div>
            <label class="label" for="pr-model">{{ t('admin.prModel') }}</label>
            <input id="pr-model" v-model="pr.model" class="input font-mono text-xs" required />
          </div>
          <div>
            <label class="label" for="pr-input">{{ t('admin.prInput') }}</label>
            <input id="pr-input" v-model="pr.input_usd_per_1m" class="input" required />
          </div>
          <div>
            <label class="label" for="pr-output">{{ t('admin.prOutput') }}</label>
            <input id="pr-output" v-model="pr.output_usd_per_1m" class="input" required />
          </div>
          <div>
            <label class="label" for="pr-image">{{ t('admin.prImage') }}</label>
            <input id="pr-image" v-model="pr.image_usd" class="input" placeholder="0" />
          </div>
          <div class="col-span-4">
            <button class="btn-primary" :disabled="prBusy">{{ t('admin.prUpsert') }}</button>
          </div>
        </form>

        <div class="card overflow-hidden">
          <table class="table w-full text-sm">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">{{ t('admin.prModel') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.prInput') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.prOutput') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.prImage') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="prices.length === 0">
                <td colspan="4" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <tr v-for="p in prices" :key="p.Model" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 font-mono text-xs">{{ p.Model }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ p.InputUSDPer1M }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ p.OutputUSDPer1M }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ p.ImageUSD }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>

      <!-- 授信 -->
      <template v-if="tab === 'credits'">
        <form class="card max-w-md p-5 space-y-4" @submit.prevent="grant">
          <div>
            <label class="label" for="cr-org">{{ t('admin.crOrg') }}</label>
            <input id="cr-org" v-model="cr.org_id" class="input" type="number" min="1" required />
          </div>
          <div>
            <label class="label" for="cr-amount">{{ t('admin.crAmount') }}</label>
            <input id="cr-amount" v-model="cr.amount_usd" class="input" required placeholder="10.00" />
          </div>
          <div>
            <label class="label" for="cr-reason">{{ t('admin.crReason') }}</label>
            <input id="cr-reason" v-model="cr.reason" class="input" required />
          </div>
          <button class="btn-primary" :disabled="crBusy">{{ t('admin.crGrant') }}</button>
          <div v-if="crResult" class="text-sm space-y-1" role="status" aria-live="polite">
            <p class="text-ok">{{ t('admin.crBalance', { balance: crResult.balance_after }) }}</p>
            <p v-if="crResult.already_applied" class="text-muted">{{ t('admin.crReplayed') }}</p>
          </div>
        </form>
      </template>

      <!-- 组织 -->
      <template v-if="tab === 'orgs'">
        <div class="card overflow-x-auto">
          <table class="table w-full text-sm whitespace-nowrap">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">ID</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.orgName') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.orgOwner') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.orgMembers') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.orgBalance') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.orgHeld') }}</th>
                <th class="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="orgs.length === 0">
                <td colspan="7" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <tr v-for="o in orgs" :key="o.id" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 text-muted">{{ o.id }}</td>
                <td class="px-4 py-2.5">{{ o.name }}</td>
                <td class="px-4 py-2.5 font-mono text-xs">{{ o.owner_email }}</td>
                <td class="px-4 py-2.5 text-right">{{ o.members }}</td>
                <td class="px-4 py-2.5 text-right font-mono">${{ o.balance }}</td>
                <td class="px-4 py-2.5 text-right font-mono text-muted">${{ o.held }}</td>
                <td class="px-4 py-2.5 text-right">
                  <button class="btn-ghost !py-1 text-xs" @click="grantTo(o)">{{ t('admin.orgGrant') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </template>
  </div>
</template>
