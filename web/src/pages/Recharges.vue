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
  trade_no: string
  created_at: number
}

const { d } = useI18n()
const activeTab = ref<'online' | 'bank'>('online')
const amount = ref(50)
const productName = ref('账户余额充值')
const methods = ref<Method[]>([])
const selectedProvider = ref('')
const orders = ref<Order[]>([])
const loading = ref(true)
const creating = ref(false)
const error = ref('')

const selectedMethod = computed(() => methods.value.find((m) => m.provider === selectedProvider.value))
const estimated = computed(() => `$${amount.value.toFixed(2)}`)

function statusLabel(s: number): string {
  if (s === 1) return '已到账'
  if (s === 2) return '失败'
  if (s === 4) return '已过期'
  return '待支付'
}

async function load() {
  loading.value = true
  const [m, o] = await Promise.allSettled([
    api<{ methods: Method[]; amount_options: number[] }>('GET', '/api/payment/methods'),
    api<{ orders: Order[] }>('GET', '/api/payment/orders'),
  ])
  if (m.status === 'fulfilled') {
    methods.value = m.value.methods
    selectedProvider.value = m.value.methods[0]?.provider ?? ''
    if (m.value.amount_options.includes(50)) amount.value = 50
    else if (m.value.amount_options[0]) amount.value = m.value.amount_options[0]
  }
  if (o.status === 'fulfilled') orders.value = o.value.orders
  loading.value = false
}

async function pay() {
  if (!selectedProvider.value) return
  creating.value = true
  error.value = ''
  try {
    const r = await api<{ pay_url: string }>('POST', '/api/payment/create', {
      provider: selectedProvider.value,
      amount_usd: String(amount.value),
    })
    window.location.href = r.pay_url
  } catch (e) {
    error.value = errorMessage(e, '创建充值订单失败')
  } finally {
    creating.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <h1 class="app-page-title">充值记录</h1>
        <p class="app-page-subtitle">在线充值或对公转账，为账户添加余额。</p>
      </div>
    </div>

    <div class="border-b border-line">
      <div class="tabs-row">
        <button type="button" :class="{ active: activeTab === 'online' }" @click="activeTab = 'online'">在线充值</button>
        <button type="button" :class="{ active: activeTab === 'bank' }" @click="activeTab = 'bank'">对公入账</button>
      </div>
    </div>

    <div v-if="activeTab === 'online'" class="app-panel max-w-[42rem] p-5">
      <h2 class="font-semibold">发起充值</h2>
      <div class="mt-5 space-y-4">
        <div>
          <label class="label">充值金额（USD）</label>
          <input v-model.number="amount" class="input" type="number" min="1" step="1" placeholder="请输入金额" />
          <p class="mt-2 text-xs text-muted">预计到账 {{ estimated }}</p>
        </div>
        <div>
          <label class="label">商品名称</label>
          <input v-model="productName" class="input" />
        </div>
        <div>
          <label class="label">支付方式</label>
          <select v-model="selectedProvider" class="input">
            <option v-if="methods.length === 0" value="">暂无可用支付方式</option>
            <option v-for="m in methods" :key="m.provider" :value="m.provider">{{ m.label }} · {{ m.currency }}</option>
          </select>
        </div>
        <p v-if="error" class="inline-alert">{{ error }}</p>
        <button class="btn-primary w-full" type="button" :disabled="creating || !selectedMethod" @click="pay">
          {{ creating ? '创建订单中…' : '前往支付' }}
        </button>
      </div>
    </div>

    <div v-else class="app-panel max-w-[42rem] p-5">
      <h2 class="font-semibold">对公付款信息</h2>
      <p class="mt-2 text-sm text-muted">请联系财务获取对公账户信息，到账后将由后台写入账本并出现在下方记录中。</p>
      <dl class="mt-5 grid gap-3 text-sm sm:grid-cols-2">
        <div class="soft-panel p-3"><dt class="text-muted">银行账号</dt><dd class="mt-1 font-mono">待配置</dd></div>
        <div class="soft-panel p-3"><dt class="text-muted">开户行</dt><dd class="mt-1">待配置</dd></div>
        <div class="soft-panel p-3"><dt class="text-muted">账户名称</dt><dd class="mt-1">待配置</dd></div>
        <div class="soft-panel p-3"><dt class="text-muted">客服邮箱</dt><dd class="mt-1 font-mono">support@laiai.local</dd></div>
      </dl>
    </div>

    <div class="space-y-3">
      <div class="flex items-center justify-between">
        <h2 class="font-semibold">充值记录</h2>
        <button class="btn-ghost !py-1 text-xs" type="button" @click="load">查看全部</button>
      </div>
      <div class="data-table-wrap">
        <table class="table w-full text-sm whitespace-nowrap">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3">订单号</th>
              <th class="px-4 py-3">外部会话</th>
              <th class="px-4 py-3 text-right">金额</th>
              <th class="px-4 py-3">支付方式</th>
              <th class="px-4 py-3">状态</th>
              <th class="px-4 py-3">交易号</th>
              <th class="px-4 py-3">创建时间</th>
              <th class="px-4 py-3 text-right">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="loading"><td colspan="8" class="px-4 py-8 text-center text-muted">加载中…</td></tr>
            <tr v-else-if="orders.length === 0"><td colspan="8" class="px-4 py-8 text-center text-muted">暂无充值记录</td></tr>
            <tr v-for="o in orders" :key="o.id">
              <td class="px-4 py-2.5 font-mono text-xs">{{ o.order_no }}</td>
              <td class="px-4 py-2.5 font-mono text-xs text-muted">—</td>
              <td class="px-4 py-2.5 text-right font-mono">{{ o.pay_amount }} {{ o.pay_currency }}</td>
              <td class="px-4 py-2.5">{{ o.provider }}</td>
              <td class="px-4 py-2.5"><span :class="o.status === 1 ? 'badge-ok' : o.status === 0 ? 'badge-muted' : 'badge-danger'">{{ statusLabel(o.status) }}</span></td>
              <td class="px-4 py-2.5 font-mono text-xs text-muted">{{ o.trade_no || '—' }}</td>
              <td class="px-4 py-2.5 text-muted">{{ d(new Date(o.created_at * 1000), { dateStyle: 'short', timeStyle: 'short' }) }}</td>
              <td class="px-4 py-2.5 text-right"><span class="text-muted">—</span></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
