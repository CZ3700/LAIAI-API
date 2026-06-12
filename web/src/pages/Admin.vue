<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminApi, adminToken, ApiError, errorMessage } from '@/lib/api'

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
  weight: number
  priority: number
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
const tab = ref<'channels' | 'prices' | 'credits'>('channels')

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
        idempotency_key: crypto.randomUUID(),
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
    await Promise.all([loadChannels(), loadPrices()])
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
    <h1 class="text-xl font-semibold">{{ t('admin.title') }}</h1>

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
            v-for="tb in ['channels', 'prices', 'credits'] as const"
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

        <div class="card overflow-x-auto">
          <table class="w-full text-sm whitespace-nowrap">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">ID</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.chName') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.chProvider') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.chModels') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.chWeight') }}/{{ t('admin.chPriority') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('keys.status') }}</th>
                <th class="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="channels.length === 0">
                <td colspan="7" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <tr v-for="c in channels" :key="c.id" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 text-muted">{{ c.id }}</td>
                <td class="px-4 py-2.5">{{ c.name }}</td>
                <td class="px-4 py-2.5 font-mono text-xs">{{ c.provider }}</td>
                <td class="px-4 py-2.5 font-mono text-xs max-w-64 truncate" :title="c.models">{{ c.models }}</td>
                <td class="px-4 py-2.5">{{ c.weight }} / {{ c.priority }}</td>
                <td class="px-4 py-2.5">
                  <span :class="c.status === 1 ? 'text-ok' : 'text-danger'">{{ statusLabel(c.status) }}</span>
                </td>
                <td class="px-4 py-2.5 text-right">
                  <button v-if="c.status === 1" class="btn-danger !py-1 text-xs" @click="disableChannel(c.id)">
                    {{ t('admin.chDisable') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
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
          <table class="w-full text-sm">
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
    </template>
  </div>
</template>
