<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'

interface Method { provider: string; currency: string; label: string }
interface Order {
  id: number
  order_no: string
  provider: string
  amount_usd: string
  pay_amount: string
  pay_currency: string
  status: number
  created_at: number
}

interface LedgerEntry {
  id: number
  kind: string
  delta: string
  balance_after: string
  ref_type: string
  ref_id: string
  reason: string
  created_at: string
}

const { t, d } = useI18n()
const balance = ref('0')
const held = ref('0')
const methods = ref<Method[]>([])
const amountOptions = ref<number[]>([])
const orders = ref<Order[]>([])
const ledger = ref<LedgerEntry[]>([])
const selectedAmount = ref(20)
const selectedProvider = ref('')
const creating = ref(false)
const error = ref('')
// FE-5：区分"加载失败"与"确实为空"——加载失败时不该显示"暂无…"误导用户
const methodsErr = ref(false)
const ordersErr = ref(false)
const ledgerErr = ref(false)

// 充值阶梯优惠（gap P1-D）：从 methods 接口拿配置，前端按所选金额预览赠额。
const bonusTiers = ref<{ min_usd: string; rate: string }[]>([])
const bonusPreview = computed(() => {
  let bestMin = -1
  let bestRate = 0
  for (const tRow of bonusTiers.value) {
    const min = Number(tRow.min_usd)
    const rate = Number(tRow.rate)
    if (!Number.isFinite(min) || !Number.isFinite(rate) || rate <= 0 || min < 0) continue
    if (selectedAmount.value >= min && min > bestMin) {
      bestMin = min
      bestRate = rate
    }
  }
  if (bestRate <= 0) return ''
  return (selectedAmount.value * bestRate).toFixed(2).replace(/\.?0+$/, '')
})

const noMethods = computed(() => methods.value.length === 0)

function statusLabel(s: number): string {
  if (s === 1) return t('wallet.paid')
  if (s === 2) return t('wallet.failed')
  if (s === 4) return t('wallet.expired')
  return t('wallet.pending')
}

// 流水类型 → 友好标签（kind 是英文枚举，做本地化映射，未知回退原值）
function kindLabel(kind: string): string {
  const key = `wallet.kind_${kind}`
  const tr = t(key)
  return tr === key ? kind : tr
}

async function load() {
  const [bal, m, o, lg] = await Promise.allSettled([
    api<{ balance: string; held: string }>('GET', '/api/balance'),
    api<{ methods: Method[]; amount_options: number[]; bonus_tiers?: string }>('GET', '/api/payment/methods'),
    api<{ orders: Order[] }>('GET', '/api/payment/orders'),
    api<{ items: LedgerEntry[] }>('GET', '/api/ledger'),
  ])
  if (bal.status === 'fulfilled') {
    balance.value = bal.value.balance
    held.value = bal.value.held
  }
  methodsErr.value = m.status === 'rejected'
  if (m.status === 'fulfilled') {
    methods.value = m.value.methods
    amountOptions.value = m.value.amount_options
    if (m.value.methods.length) selectedProvider.value = m.value.methods[0].provider
    try {
      bonusTiers.value = m.value.bonus_tiers ? JSON.parse(m.value.bonus_tiers) : []
    } catch {
      bonusTiers.value = []
    }
  }
  ordersErr.value = o.status === 'rejected'
  if (o.status === 'fulfilled') orders.value = o.value.orders
  ledgerErr.value = lg.status === 'rejected'
  if (lg.status === 'fulfilled') ledger.value = lg.value.items
}

async function topup() {
  if (!selectedProvider.value) return
  creating.value = true
  error.value = ''
  try {
    const r = await api<{ pay_url?: string }>('POST', '/api/payment/create', {
      provider: selectedProvider.value,
      amount_usd: String(selectedAmount.value),
    })
    if (r.pay_url) {
      window.location.href = r.pay_url // 跳转去支付
    } else {
      // 防御：x402 等渠道不返回 pay_url（改返回 requirements，需链上结算 UI，当前未接入）。
      // 不跳到 undefined，并复位按钮避免永久卡在“跳转中”。
      error.value = t('common.error')
      creating.value = false
    }
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
    creating.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('surface.walletEyebrow') }}</p>
        <h1 class="app-page-title">{{ t('wallet.title') }}</h1>
        <p class="app-page-subtitle">{{ t('surface.walletSubtitle') }}</p>
      </div>
    </div>

    <section class="grid gap-4 lg:grid-cols-[1fr_0.72fr]">
      <!-- 余额 -->
      <div class="console-hero p-6 text-ink">
        <p class="text-xs text-muted">{{ t('dashboard.balance') }}</p>
        <p class="mt-2 text-4xl font-semibold font-mono">${{ balance }}</p>
        <div class="mt-5 grid gap-3 sm:grid-cols-2">
          <div class="soft-panel p-3">
            <p class="text-xs text-muted">{{ t('wallet.held') }}</p>
            <p class="mt-1 font-mono text-lg">${{ held }}</p>
          </div>
          <div class="soft-panel p-3">
            <p class="text-xs text-muted">{{ t('wallet.transactions') }}</p>
            <p class="mt-1 font-mono text-lg">{{ ledger.length }}</p>
          </div>
        </div>
      </div>

      <div class="app-panel p-5">
        <p class="text-sm font-medium">{{ t('wallet.history') }}</p>
        <p class="mt-2 text-sm leading-6 text-muted">{{ t('surface.walletHistoryHint') }}</p>
        <div class="mt-5 grid grid-cols-2 gap-3 text-xs">
          <div class="soft-panel p-3">
            <p class="text-muted">{{ t('wallet.method') }}</p>
            <p class="mt-1 font-mono text-ink">{{ methods.length }}</p>
          </div>
          <div class="soft-panel p-3">
            <p class="text-muted">{{ t('wallet.history') }}</p>
            <p class="mt-1 font-mono text-ink">{{ orders.length }}</p>
          </div>
        </div>
      </div>
    </section>

    <!-- 充值 -->
    <div class="app-panel p-6 space-y-5">
      <div>
        <h2 class="font-semibold">{{ t('wallet.topup') }}</h2>
        <p class="mt-1 text-sm text-muted">{{ t('surface.walletTopupHint') }}</p>
      </div>
      <p v-if="methodsErr" class="text-sm text-danger" role="alert">{{ t('common.error') }}</p>
      <p v-else-if="noMethods" class="text-sm text-muted">{{ t('wallet.noMethods') }}</p>
      <template v-else>
        <div>
          <label class="label">{{ t('wallet.amount') }}</label>
          <div class="flex flex-wrap gap-2">
            <button
              v-for="a in amountOptions"
              :key="a"
              type="button"
              class="rounded-lg border px-4 py-2 text-sm font-mono transition-colors"
              :class="selectedAmount === a ? 'border-accent bg-accent/10 text-accent' : 'border-line hover:border-muted/40'"
              @click="selectedAmount = a"
            >
              ${{ a }}
            </button>
          </div>
          <p v-if="bonusPreview" class="mt-2 text-sm text-ok">
            {{ t('wallet.bonusPreview', { bonus: bonusPreview }) }}
          </p>
        </div>
        <div>
          <label class="label">{{ t('wallet.method') }}</label>
          <div class="flex flex-wrap gap-2">
            <button
              v-for="m in methods"
              :key="m.provider"
              type="button"
              class="rounded-lg border px-4 py-2 text-sm transition-colors"
              :class="selectedProvider === m.provider ? 'border-accent bg-accent/10 text-accent' : 'border-line hover:border-muted/40'"
              @click="selectedProvider = m.provider"
            >
              {{ m.label }}
            </button>
          </div>
        </div>
        <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
        <button class="btn-primary" :disabled="creating" @click="topup">
          {{ creating ? t('wallet.redirecting') : t('wallet.payNow', { amount: selectedAmount }) }}
        </button>
      </template>
    </div>

    <!-- 资金流水（统一账本：充值/赠送/消费/释放） -->
    <div class="space-y-3">
      <h2 class="font-semibold">{{ t('wallet.transactions') }}</h2>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3 font-medium">{{ t('usage.time') }}</th>
              <th class="px-4 py-3 font-medium">{{ t('wallet.txType') }}</th>
              <th class="px-4 py-3 font-medium">{{ t('wallet.txDetail') }}</th>
              <th class="px-4 py-3 font-medium text-right">{{ t('wallet.txAmount') }}</th>
              <th class="px-4 py-3 font-medium text-right">{{ t('wallet.txBalance') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="ledgerErr">
              <td colspan="5" class="px-4 py-8 text-center text-danger">{{ t('common.error') }}</td>
            </tr>
            <tr v-else-if="ledger.length === 0">
              <td colspan="5" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
            </tr>
            <tr v-for="e in ledger" :key="e.id" class="border-b border-line last:border-0">
              <td class="px-4 py-2.5 text-muted">{{ d(new Date(e.created_at), { dateStyle: 'short', timeStyle: 'short' }) }}</td>
              <td class="px-4 py-2.5"><span class="badge-muted">{{ kindLabel(e.kind) }}</span></td>
              <td class="px-4 py-2.5 text-muted">{{ e.reason || e.ref_type }}</td>
              <td class="px-4 py-2.5 text-right font-mono" :class="Number(e.delta) >= 0 ? 'text-ok' : 'text-danger'">
                {{ Number(e.delta) >= 0 ? '+' : '' }}{{ e.delta }}
              </td>
              <td class="px-4 py-2.5 text-right font-mono text-muted">${{ e.balance_after }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 充值记录 -->
    <div class="space-y-3">
      <h2 class="font-semibold">{{ t('wallet.history') }}</h2>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3 font-medium">{{ t('usage.time') }}</th>
              <th class="px-4 py-3 font-medium">{{ t('wallet.amount') }}</th>
              <th class="px-4 py-3 font-medium">{{ t('wallet.method') }}</th>
              <th class="px-4 py-3 font-medium">{{ t('keys.status') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="ordersErr">
              <td colspan="4" class="px-4 py-8 text-center text-danger">{{ t('common.error') }}</td>
            </tr>
            <tr v-else-if="orders.length === 0">
              <td colspan="4" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
            </tr>
            <tr v-for="o in orders" :key="o.id" class="border-b border-line last:border-0">
              <td class="px-4 py-2.5 text-muted">{{ d(new Date(o.created_at * 1000), { dateStyle: 'short', timeStyle: 'short' }) }}</td>
              <td class="px-4 py-2.5 font-mono">${{ o.amount_usd }}</td>
              <td class="px-4 py-2.5">{{ o.provider }}</td>
              <td class="px-4 py-2.5">
                <span :class="o.status === 1 ? 'badge-ok' : o.status === 0 ? 'badge-muted' : 'badge-danger'">
                  {{ statusLabel(o.status) }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
