<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, copyText, errorMessage } from '@/lib/api'

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

// 邀请返佣
const aff = ref<{ aff_code: string; invited: number; earned: string; rate: string } | null>(null)
const inviteLink = computed(() =>
  aff.value ? `${window.location.origin}/register?ref=${aff.value.aff_code}` : '',
)
const copied = ref(false)
async function copyInvite() {
  if (await copyText(inviteLink.value)) {
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  }
}

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
  const [bal, m, o, a, lg] = await Promise.allSettled([
    api<{ balance: string; held: string }>('GET', '/api/balance'),
    api<{ methods: Method[]; amount_options: number[] }>('GET', '/api/payment/methods'),
    api<{ orders: Order[] }>('GET', '/api/payment/orders'),
    api<{ aff_code: string; invited: number; earned: string; rate: string }>('GET', '/api/affiliate'),
    api<{ items: LedgerEntry[] }>('GET', '/api/ledger'),
  ])
  if (bal.status === 'fulfilled') {
    balance.value = bal.value.balance
    held.value = bal.value.held
  }
  if (m.status === 'fulfilled') {
    methods.value = m.value.methods
    amountOptions.value = m.value.amount_options
    if (m.value.methods.length) selectedProvider.value = m.value.methods[0].provider
  }
  if (o.status === 'fulfilled') orders.value = o.value.orders
  if (a.status === 'fulfilled') aff.value = a.value
  if (lg.status === 'fulfilled') ledger.value = lg.value.items
}

async function topup() {
  if (!selectedProvider.value) return
  creating.value = true
  error.value = ''
  try {
    const r = await api<{ pay_url: string }>('POST', '/api/payment/create', {
      provider: selectedProvider.value,
      amount_usd: String(selectedAmount.value),
    })
    window.location.href = r.pay_url // 跳转去支付
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
    creating.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="max-w-3xl space-y-6">
    <h1 class="text-2xl font-semibold tracking-tight">{{ t('wallet.title') }}</h1>

    <!-- 余额 -->
    <div
      class="card p-6 text-white"
      style="background: linear-gradient(135deg, rgb(var(--c-accent)), rgb(var(--c-accent-2)))"
    >
      <p class="text-xs opacity-80">{{ t('dashboard.balance') }}</p>
      <p class="mt-1 text-4xl font-semibold font-mono">${{ balance }}</p>
      <p v-if="Number(held) > 0" class="mt-1 text-xs opacity-80">
        {{ t('wallet.held') }}: <span class="font-mono">${{ held }}</span>
      </p>
    </div>

    <!-- 充值 -->
    <div class="card p-6 space-y-5">
      <h2 class="font-semibold">{{ t('wallet.topup') }}</h2>
      <p v-if="noMethods" class="text-sm text-muted">{{ t('wallet.noMethods') }}</p>
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

    <!-- 邀请返佣 -->
    <div v-if="aff && aff.rate && aff.rate !== '0'" class="card p-6 space-y-4">
      <h2 class="font-semibold">{{ t('wallet.affTitle') }}</h2>
      <p class="text-sm text-muted">{{ t('wallet.affDesc', { rate: (Number(aff.rate) * 100).toFixed(0) }) }}</p>
      <div class="flex gap-2">
        <input :value="inviteLink" readonly class="input font-mono text-xs" @focus="($event.target as HTMLInputElement).select()" />
        <button class="btn-ghost shrink-0" @click="copyInvite">{{ copied ? t('common.copied') : t('common.copy') }}</button>
      </div>
      <div class="flex gap-8 text-sm">
        <div><span class="text-muted">{{ t('wallet.affInvited') }}</span> <span class="font-mono">{{ aff.invited }}</span></div>
        <div><span class="text-muted">{{ t('wallet.affEarned') }}</span> <span class="font-mono">${{ aff.earned }}</span></div>
      </div>
    </div>

    <!-- 资金流水（统一账本：充值/赠送/消费/释放） -->
    <div class="space-y-3">
      <h2 class="font-semibold">{{ t('wallet.transactions') }}</h2>
      <div class="card overflow-x-auto">
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
            <tr v-if="ledger.length === 0">
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
      <div class="card overflow-x-auto">
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
            <tr v-if="orders.length === 0">
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
