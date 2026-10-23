<script setup lang="ts">
import { fmtUsd, formatNativeMoney, displayCurrencyCode } from '@/lib/money'
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import DetailDrawer from '@/components/DetailDrawer.vue'
import { useAuth } from '@/stores/auth'
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
  trade_no: string
  created_at: number
}

const { t, d } = useI18n()
const auth = useAuth()
const selectedOrder = ref<Order | null>(null)
let loadEpoch = 0
const amount = ref(50)
const methods = ref<Method[]>([])
const amountOptions = ref<number[]>([])
const selectedProvider = ref('')
const orders = ref<Order[]>([])
const loading = ref(true)
const creating = ref(false)
const error = ref('')

const selectedMethod = computed(() => methods.value.find((m) => m.provider === selectedProvider.value))
const estimated = computed(() => fmtUsd(String(amount.value), { precision: 2 }))

function statusLabel(s: number): string {
  if (s === 1) return t('recharge.statusPaid')
  if (s === 2) return t('recharge.statusFailed')
  if (s === 4) return t('recharge.statusExpired')
  return t('recharge.statusPending')
}

async function load() {
  const epoch = ++loadEpoch, org = auth.orgId
  selectedOrder.value = null
  loading.value = true
  const [m, o] = await Promise.allSettled([
    api<{ methods: Method[]; amount_options: number[] }>('GET', '/api/payment/methods'),
    api<{ orders: Order[] }>('GET', '/api/payment/orders'),
  ])
  if (epoch !== loadEpoch || org !== auth.orgId) return
  if (m.status === 'fulfilled') {
    methods.value = m.value.methods
    amountOptions.value = m.value.amount_options ?? []
    selectedProvider.value = m.value.methods[0]?.provider ?? ''
    if (amountOptions.value.includes(50)) amount.value = 50
    else if (amountOptions.value[0]) amount.value = amountOptions.value[0]
  }
  if (o.status === 'fulfilled') orders.value = o.value.orders
  loading.value = false
}

async function pay() {
  if (!selectedProvider.value) return
  creating.value = true
  error.value = ''
  try {
    const r = await api<{ pay_url?: string }>('POST', '/api/payment/create', {
      provider: selectedProvider.value,
      amount_usd: String(amount.value),
    })
    if (r.pay_url) {
      window.location.href = r.pay_url
    } else {
      error.value = t('recharge.errUnsupportedMethod')
      creating.value = false
    }
  } catch (e) {
    error.value = errorMessage(e, t('recharge.errCreateOrder'))
    creating.value = false
  }
}

watch(() => auth.orgId, () => { selectedOrder.value = null; orders.value = []; void load() })
onBeforeUnmount(() => { loadEpoch++; selectedOrder.value = null })
onMounted(load)
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <h1 class="app-page-title">{{ t('recharge.title') }}</h1>
        <p class="app-page-subtitle">{{ t('recharge.subtitle') }}</p>
      </div>
    </div>

    <section class="grid items-start gap-4 lg:grid-cols-[24rem_1fr]">
      <!-- 发起充值 -->
      <div class="app-panel p-5">
        <h2 class="font-semibold">{{ t('recharge.startTitle') }}</h2>
        <div class="mt-5 space-y-4">
          <div>
            <label class="label">{{ t('recharge.amountLabel') }}</label>
            <div class="flex flex-wrap gap-2">
              <button
                v-for="a in amountOptions"
                :key="a"
                type="button"
                class="rounded-lg border px-4 py-2 text-sm font-mono transition-colors"
                :class="amount === a ? 'border-accent bg-accent/10 text-accent' : 'border-line hover:border-muted/40'"
                @click="amount = a"
              >
                {{ formatNativeMoney(a, 'USD') }}
              </button>
            </div>
            <input v-model.number="amount" class="input mt-2" type="number" min="1" step="1" :placeholder="t('recharge.customAmountPlaceholder')" />
            <p class="mt-2 text-xs text-muted">{{ t('recharge.estimated', { amount: estimated }) }}</p>
          </div>
          <div>
            <label class="label">{{ t('recharge.payMethodLabel') }}</label>
            <select v-model="selectedProvider" class="input">
              <option v-if="methods.length === 0" value="">{{ t('recharge.noMethods') }}</option>
              <option v-for="m in methods" :key="m.provider" :value="m.provider">{{ m.label }} · {{ m.currency }}</option>
            </select>
          </div>
          <p v-if="error" class="inline-alert">{{ error }}</p>
          <button class="btn-primary w-full" type="button" :disabled="creating || !selectedMethod" @click="pay">
            {{ creating ? t('recharge.btnCreating') : t('recharge.btnPay') }}
          </button>
        </div>
      </div>

      <!-- 充值记录 -->
      <div class="space-y-3">
        <div class="flex items-center justify-between">
          <h2 class="font-semibold">{{ t('recharge.recordsTitle') }}</h2>
          <button class="btn-ghost !py-1 text-xs" type="button" @click="load">{{ t('recharge.btnRefresh') }}</button>
        </div>
        <div class="data-table-wrap">
          <table class="table w-full text-sm whitespace-nowrap">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3">{{ t('recharge.colOrderNo') }}</th>
                <th class="px-4 py-3 text-right">{{ t('recharge.colAmount') }}</th>
                <th class="px-4 py-3">{{ t('recharge.colPayMethod') }}</th>
                <th class="px-4 py-3">{{ t('recharge.colStatus') }}</th>
                <th class="px-4 py-3">{{ t('recharge.colTradeNo') }}</th>
                <th class="px-4 py-3">{{ t('recharge.colCreatedAt') }}</th><th class="px-4 py-3">{{ t('reqlog.btnDetail') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="loading"><td colspan="7" class="px-4 py-8 text-center text-muted">{{ t('recharge.loading') }}</td></tr>
              <tr v-else-if="orders.length === 0"><td colspan="7" class="px-4 py-8 text-center text-muted">{{ t('recharge.emptyRecords') }}</td></tr>
              <tr v-for="o in orders" :key="o.id">
                <td class="px-4 py-2.5 font-mono text-xs">{{ o.order_no }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ formatNativeMoney(o.pay_amount, o.pay_currency) }}</td>
                <td class="px-4 py-2.5">{{ o.provider }}</td>
                <td class="px-4 py-2.5">
                  <span :class="o.status === 1 ? 'badge-ok' : o.status === 0 ? 'badge-warn' : 'badge-danger'">{{ statusLabel(o.status) }}</span>
                </td>
                <td class="px-4 py-2.5 font-mono text-xs text-muted">{{ o.trade_no || '—' }}</td>
                <td class="px-4 py-2.5 text-muted">{{ d(new Date(o.created_at * 1000), { dateStyle: 'short', timeStyle: 'short' }) }}</td><td class="px-4 py-2.5"><button type="button" class="table-link" @click="selectedOrder = o">{{ t('reqlog.btnDetail') }}</button></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>
    <DetailDrawer :open="!!selectedOrder" :title="selectedOrder?.order_no || t('recharge.recordsTitle')" @update:open="open => { if (!open) selectedOrder = null }">
      <dl v-if="selectedOrder" class="detail-grid"><div><dt>ID</dt><dd>{{ selectedOrder.id }}</dd></div><div><dt>{{ t('recharge.colOrderNo') }}</dt><dd>{{ selectedOrder.order_no }}</dd></div><div><dt>{{ t('recharge.colStatus') }}</dt><dd>{{ statusLabel(selectedOrder.status) }}</dd></div><div><dt>{{ t('recharge.colAmount') }}</dt><dd>{{ formatNativeMoney(selectedOrder.pay_amount, selectedOrder.pay_currency) }}</dd></div><div><dt>{{ displayCurrencyCode() }}</dt><dd>{{ fmtUsd(selectedOrder.amount_usd, { precision: 2 }) }}</dd></div><div><dt>{{ t('recharge.colPayMethod') }}</dt><dd>{{ selectedOrder.provider }}</dd></div><div><dt>{{ t('recharge.colTradeNo') }}</dt><dd>{{ selectedOrder.trade_no || '—' }}</dd></div><div><dt>{{ t('recharge.colCreatedAt') }}</dt><dd>{{ d(new Date(selectedOrder.created_at * 1000), { dateStyle: 'short', timeStyle: 'medium' }) }}</dd></div></dl>
    </DetailDrawer>
  </div>
</template>
