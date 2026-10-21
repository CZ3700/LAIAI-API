<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { adminApi, adminToken, ApiError, copyText, errorMessage, uuid } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import Sparkline from '@/components/Sparkline.vue'
import DetailDrawer from '@/components/DetailDrawer.vue'
import AdminPricingPanel from '@/components/pricing/AdminPricingPanel.vue'
import type { ModelMeta } from '@/components/pricing/types'

const auth = useAuth()
const route = useRoute(), router = useRouter()
const pricingPanel = ref<InstanceType<typeof AdminPricingPanel> | null>(null)

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
  success_rate: number
  avg_latency_ms: number
  errors: number
  proxy: string
  param_override: string
  header_override: string
  credential_mode: string
  key_count: number
  converter: string
  upstream_path: string
  model_sync_enabled: boolean
  detected_models: string
  ignored_models: string
  last_sync_at: string | null
  suggest_disable_reason: string
  suggest_disabled_at: string | null
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
  group: string
  status: number
  calls: number
  spend: string
  last_active: string
  feature_images: boolean
  feature_video: boolean
  max_members: number
  max_api_keys: number
  shard_count: number
}
interface GroupRatio {
  name: string
  ratio: string
}
interface LogRow {
  id: number
  created_at: string
  request_id: string
  org_id: number
  user_id: number | null
  api_key_id: number
  model: string
  channel_id: number
  provider: string
  status: string
  status_code: number
  is_stream: boolean
  attempts: number
  prompt_tokens: number
  completion_tokens: number
  cost_usd: string
  usage_source: string
  frt_ms: number
  duration_ms: number
  upstream_request_id: string
  error_brief: string
  payload_object_key: string
}

// —— 门禁：role≥admin 自动放行；否则 root-token 破玻璃 ——
const tokenInput = ref('')
const unlocked = ref(adminToken.get() !== '')
const roleLoading = ref(true) // fetchMe 解析前不闪 token 表单
const error = ref('')

function unlock() {
  adminToken.set(tokenInput.value.trim())
  tokenInput.value = ''
  unlocked.value = true
  error.value = ''
  void loadAll()
}
function lock() {
  if (tab.value === 'prices' && pricingPanel.value && !pricingPanel.value.confirmLeave()) return
  adminToken.clear()
  unlocked.value = false
}

function showError(e: unknown) {
  error.value = errorMessage(e, t('common.error'))
  if (e instanceof ApiError && e.status === 401) unlocked.value = false
}

const { t, d } = useI18n()

// —— tabs ——
const tab = ref<'overview' | 'analytics' | 'health' | 'channels' | 'payments' | 'prices' | 'groups' | 'orgs' | 'users' | 'logs' | 'settings' | 'audit'>('overview')

// 标签分级（对标后端 RBAC）：资金/系统/定价类仅 superadmin 可见，admin 看运营类。
// 减法：analytics 并入 overview（同页展示运营分析）、health 并入 channels（渠道+健康同看）、
// groups 并入 prices（都是定价配置，分组倍率区块用 isSuperadmin 守住原超管权限边界）。12→9。
const ALL_TABS = ['overview', 'channels', 'payments', 'prices', 'orgs', 'users', 'logs', 'settings', 'audit'] as const
// logs（全局调用明细）跨租户 → superadmin-only（配合后端 P2-3 把 /invocations 收归超管）
const SUPER_ONLY_TABS = new Set(['payments', 'settings', 'logs'])
const visibleTabs = computed(() => ALL_TABS.filter((tb) => auth.isSuperadmin || !SUPER_ONLY_TABS.has(tb)))
function requestedTab(value: unknown): typeof ALL_TABS[number] {
  if (value === 'pricing') return 'prices'
  return ALL_TABS.includes(value as typeof ALL_TABS[number]) ? value as typeof ALL_TABS[number] : 'overview'
}
function selectAdminTab(value: typeof ALL_TABS[number]) {
  if (value !== tab.value && tab.value === 'prices' && pricingPanel.value && !pricingPanel.value.confirmLeave()) return
  tab.value = value
  void router.replace({ query: { ...route.query, tab: value } })
}
watch(() => route.query.tab, value => { tab.value = requestedTab(value) }, { immediate: true })
onBeforeRouteUpdate(to => {
  if (tab.value === 'prices' && requestedTab(to.query.tab) !== 'prices' && pricingPanel.value) return pricingPanel.value.confirmLeave()
})

// —— 运营分析（资金 / 用户 / 质量 颗粒度） ——
interface Analytics {
  days: number
  finance: {
    cash_in: string; consumption: string; float: string
    topup_paid: number; topup_pending: number; topup_failed: number; topup_conversion: number
    by_provider: { provider: string; paid_count: number; amount: string }[]
  }
  users: { total_users: number; new_users: number; total_orgs: number; active_orgs: number; arpu: string }
  quality: {
    avg_duration_ms: number; avg_frt_ms: number
    errors: { status: string; n: number }[]
    by_provider: { provider: string; calls: number; cost: string }[]
  }
  top_orgs: { org_id: number; name: string; calls: number; cost: string }[]
  series: { day: string; calls: number; cost: string; cash_in: string }[]
}
const analytics = ref<Analytics | null>(null)
const analyticsDays = ref(14)
const analyticsErr = ref('')
async function loadAnalytics() {
  analyticsErr.value = ''
  try {
    analytics.value = await adminApi<Analytics>('GET', `/api/admin/analytics?days=${analyticsDays.value}`)
  } catch (e) {
    analyticsErr.value = errorMessage(e, t('common.error')) // 让失败可见而非空白
    throw e
  }
}
// 序列抽取成 number[]，喂给 Sparkline
const cashSeries = computed(() => (analytics.value?.series ?? []).map((p) => Number(p.cash_in)))
const costSeries = computed(() => (analytics.value?.series ?? []).map((p) => Number(p.cost)))
const callsSeries = computed(() => (analytics.value?.series ?? []).map((p) => p.calls))
// 错误率：非 success 占比
const errorRate = computed(() => {
  const errs = analytics.value?.quality.errors ?? []
  const total = errs.reduce((s, e) => s + e.n, 0)
  if (total === 0) return 0
  const ok = errs.find((e) => e.status === 'success')?.n ?? 0
  return (total - ok) / total
})

// —— 支付渠道总览（脱敏状态 + 路由预览） ——
// 灵活支付渠道（#5）：多实例、可启停、优先级、加密配置、连通性测试、路由预览
interface PayChannelRow {
  id: number; code: string; display_name: string; protocol: string
  enabled: number; priority: number; pay_methods: string; currency: string
  last_test_at: number; last_test_ok: number; last_test_msg: string
}
interface PayRoute { method: string; channel_code: string; channel_name: string; currency: string }
const payChannels = ref<PayChannelRow[]>([])
const payRoutes = ref<PayRoute[]>([])
const payLoaded = ref(false)
async function loadPayChannels() {
  const [cs, rs] = await Promise.all([
    adminApi<{ channels: PayChannelRow[] }>('GET', '/api/admin/payment-channels'),
    adminApi<{ routes: PayRoute[] }>('GET', '/api/admin/payment-channels/route-preview'),
  ])
  payChannels.value = cs.channels
  payRoutes.value = rs.routes
  payLoaded.value = true
  // 结算全局设置（汇率/最低充值/返佣）从 settings 取——支付相关统一收到支付页（#2 去重）
  if (Object.keys(settings.value).length === 0) await loadSettings()
}
// 保存支付页里的全局结算设置（仅这 3 个键）
const payGlobalsBusy = ref(false)
const payGlobalsMsg = ref('')
async function savePayGlobals() {
  payGlobalsBusy.value = true
  payGlobalsMsg.value = ''
  try {
    await adminApi('PUT', '/api/admin/settings', {
      cny_per_usd: settings.value.cny_per_usd ?? '',
      min_topup_usd: settings.value.min_topup_usd ?? '',
      recharge_bonus_tiers: settings.value.recharge_bonus_tiers ?? '',
      invite_grant_usd: settings.value.invite_grant_usd ?? '',
    })
    payGlobalsMsg.value = t('settings.saved')
  } catch (e) {
    showError(e)
  } finally {
    payGlobalsBusy.value = false
  }
}
// 渠道编辑器（新建/编辑共用）。密钥字段编辑时留空 = 保留原配置。
// 渠道协议的配置字段元数据（与后端 buildPaymentEngine 一一对应）。新增协议只需在此登记，
// 模态框按当前协议动态渲染对应字段——这正是「灵活热接入」：crypto 加密货币网关无需改前端硬编码。
// 渠道字段元数据：与后端 buildPaymentEngine 的 cfg 键一一对应。新增协议只需在此登记，模态框动态渲染。
interface PcFieldMeta { key: string; label: string; labelKey?: string; secret?: boolean; placeholder?: string; phKey?: string; required?: boolean; wide?: boolean; textarea?: boolean; bool?: boolean }
const PROTOCOL_FIELDS: Record<string, PcFieldMeta[]> = {
  stripe: [
    { key: 'secret_key', label: 'Secret Key', secret: true, placeholder: 'sk_live_…', required: true },
    { key: 'webhook_secret', label: 'Webhook Secret', secret: true, placeholder: 'whsec_…' },
  ],
  epay: [
    { key: 'pid', label: 'PID / Merchant ID', labelKey: 'adminx.pcfPidLabel', placeholder: '1000', required: true },
    { key: 'key', label: 'Merchant Key', labelKey: 'adminx.pcfMerchantKeyLabel', secret: true, required: true },
    { key: 'api_url', label: 'API URL', labelKey: 'adminx.pcfApiUrlLabel', placeholder: 'https://pay.example.com', wide: true, required: true },
  ],
  // 通用加密货币网关（EBPay / OxaPay / NOWPayments 等三段式处理商）——验签 fail-closed + 实付校验。
  crypto: [
    { key: 'processor', label: 'Processor', labelKey: 'adminx.pcfProcessorLabel', placeholder: 'ebpay / oxapay / nowpayments', required: true },
    { key: 'api_base', label: 'API Base URL', placeholder: 'https://api.ebpay.com', wide: true },
    { key: 'api_key', label: 'API Key', secret: true, required: true },
    { key: 'webhook_secret', label: 'Webhook / Callback Secret', secret: true, required: true },
    { key: 'sign_algo', label: 'Sign Algorithm', placeholder: 'sha256 (default) / sha512' },
    { key: 'create_path', label: 'Create Path', placeholder: '/api/v1/invoice', wide: true },
    { key: 'query_path', label: 'Query Path', placeholder: '/api/v1/invoice/query', wide: true },
  ],
  // PayPal Orders v2：client_id+secret+webhook_id 必填（webhook_id 是官方验签必需，缺则回调全 fail-closed）。
  paypal: [
    { key: 'client_id', label: 'Client ID', required: true },
    { key: 'client_secret', label: 'Client Secret', secret: true, required: true },
    { key: 'webhook_id', label: 'Webhook ID (required for verification)', labelKey: 'adminx.pcfWebhookIdLabel', required: true },
    { key: 'sandbox', label: 'Sandbox', labelKey: 'adminx.pcfSandboxLabel', bool: true },
  ],
  // 通用 Webhook（REST 建单 + Webhook 回调；Airwallex/UseePay/Adyen 等）。webhook_secret 必填=强制验签防伪造。
  webhook: [
    { key: 'api_base_url', label: 'API Base URL', placeholder: 'https://api.airwallex.com', wide: true, required: true },
    { key: 'webhook_secret', label: 'Webhook Verification Secret', labelKey: 'adminx.pcfWebhookSecretLabel', secret: true, required: true },
    { key: 'auth_type', label: 'Auth Type', placeholder: 'bearer / basic / header' },
    { key: 'auth_endpoint', label: 'Auth Endpoint', placeholder: '/authentication/login' },
    { key: 'auth_creds', label: 'Auth Creds (JSON)', placeholder: '{"client_id":"..","api_key":".."}', wide: true, textarea: true },
    { key: 'sig_header', label: 'Sig Header', placeholder: 'x-signature' },
    { key: 'sig_algo', label: 'Sig Algo', placeholder: 'hmac-sha256 / hmac-md5' },
    { key: 'sig_payload_format', label: 'Sig Payload', placeholder: 'body / timestamp+body' },
    { key: 'create_method', label: 'Create Method', placeholder: 'POST' },
    { key: 'create_path', label: 'Create Path', placeholder: '/v1/payment_intents' },
    { key: 'create_body_template', label: 'Create Body Template', labelKey: 'adminx.pcfCreateBodyTplLabel', placeholder: '{"amount":"{{amount}}","order":"{{order_no}}"}', wide: true, textarea: true },
    { key: 'pay_url_extract', label: 'Pay URL Field Path', labelKey: 'adminx.pcfPayUrlPathLabel', placeholder: 'next_action.redirect_url' },
    { key: 'ref_extract', label: 'Ref Field Path', labelKey: 'adminx.pcfRefPathLabel', placeholder: 'id' },
    { key: 'success_event', label: 'Success Event', placeholder: 'payment_intent.succeeded' },
    { key: 'event_type_extract', label: 'Event Type Path', labelKey: 'adminx.pcfEventTypePathLabel', placeholder: 'type' },
    { key: 'order_extract', label: 'Order No Path', labelKey: 'adminx.pcfOrderNoPathLabel', placeholder: 'data.object.metadata.order_no' },
    { key: 'amount_extract', label: 'Amount Path', labelKey: 'adminx.pcfAmountPathLabel', placeholder: 'data.object.amount' },
    { key: 'trade_no_extract', label: 'Trade No Path', labelKey: 'adminx.pcfTradeNoPathLabel', placeholder: 'data.object.id' },
    { key: 'currency_extract', label: 'Currency Path', labelKey: 'adminx.pcfCurrencyPathLabel' },
    { key: 'amount_unit', label: 'Amount Unit', placeholder: 'major / minor' },
  ],
  // 自定义可编程 HTTP 模板（接入任何非标准接口）。webhook_secret 必填=强制验签（默认 hmac-sha256）防伪造。
  custom: [
    { key: 'create_url', label: 'Create URL Template', labelKey: 'adminx.pcfCreateUrlTplLabel', placeholder: 'https://gw.example.com/pay?order={{order_no}}', wide: true, required: true },
    { key: 'webhook_secret', label: 'Webhook Verification Secret', labelKey: 'adminx.pcfWebhookSecretLabel', secret: true, required: true },
    { key: 'api_key', label: 'API Key ({{api_key}} variable)', labelKey: 'adminx.pcfApiKeyVarLabel', secret: true },
    { key: 'create_method', label: 'Create Method', placeholder: 'POST / GET' },
    { key: 'create_headers', label: 'Create Headers (JSON)', placeholder: '{"Authorization":"Bearer {{api_key}}"}', wide: true, textarea: true },
    { key: 'create_body', label: 'Create Body Template', labelKey: 'adminx.pcfCreateBodyTplLabel', placeholder: '{"order":"{{order_no}}","amount":"{{amount}}"}', wide: true, textarea: true },
    { key: 'pay_url_path', label: 'Pay URL Field Path', labelKey: 'adminx.pcfPayUrlPathLabel', placeholder: 'pay_url' },
    { key: 'ref_path', label: 'Ref Field Path', labelKey: 'adminx.pcfRefPathLabel', placeholder: 'id' },
    { key: 'sig_header', label: 'Sig Header', placeholder: 'x-signature' },
    { key: 'sig_algo', label: 'Sig Algo', placeholder: 'hmac-sha256 / hmac-md5' },
    { key: 'sig_payload', label: 'Sig Payload', placeholder: 'body / timestamp+body' },
    { key: 'success_condition', label: 'Success Condition', placeholder: 'status=PAID (empty = success when order_no present)', phKey: 'adminx.pcfSuccessCondPh' },
    { key: 'order_path', label: 'Order No Path', labelKey: 'adminx.pcfOrderNoPathLabel', placeholder: 'order_no' },
    { key: 'amount_path', label: 'Amount Path', labelKey: 'adminx.pcfAmountPathLabel', placeholder: 'amount' },
    { key: 'trade_no_path', label: 'Trade No Path', labelKey: 'adminx.pcfTradeNoPathLabel', placeholder: 'trade_no' },
    { key: 'currency_path', label: 'Currency Path', labelKey: 'adminx.pcfCurrencyPathLabel' },
  ],
}
// 协议选择网格（卡片）：图标用首字母彩色徽标（与渠道列表一致）；desc 走 i18n。
const PROTOCOL_OPTIONS = [
  { value: 'epay', label: 'EPay', labelKey: 'adminx.protoEpay', color: '#16a34a' },
  { value: 'stripe', label: 'Stripe', color: '#635bff' },
  { value: 'paypal', label: 'PayPal', color: '#0070ba' },
  { value: 'webhook', label: 'Webhook', color: '#0891b2' },
  { value: 'crypto', label: 'Crypto', color: '#f59e0b' },
  { value: 'custom', label: 'Custom', color: '#64748b' },
] as const
// 协议选项 label：带 labelKey 的走 i18n（如 EPay 易支付），其余英文/品牌名原样
function protoOptLabel(p: { label: string; labelKey?: string }): string {
  return p.labelKey ? t(p.labelKey) : p.label
}
// 支付方式 + 币种选项
const PAY_METHOD_OPTIONS = [
  { value: 'alipay', label: 'Alipay', labelKey: 'adminx.payMethodAlipay' }, { value: 'wechat', label: 'WeChat Pay', labelKey: 'adminx.payMethodWechat' },
  { value: 'card', label: 'Bank Card', labelKey: 'adminx.payMethodCard' }, { value: 'usdt', label: 'USDT' }, { value: 'paypal', label: 'PayPal' },
]
// 支付方式 label：带 labelKey 的走 i18n（支付宝/微信/银行卡），USDT/PayPal 原样
function payMethodLabel(pm: { label: string; labelKey?: string }): string {
  return pm.labelKey ? t(pm.labelKey) : pm.label
}
// 字段 label / placeholder：带 key 的走 i18n（中文字段名），纯英文/技术标识原样
function fieldLabel(f: PcFieldMeta): string {
  return f.labelKey ? t(f.labelKey) : f.label
}
function fieldPlaceholder(f: PcFieldMeta): string {
  return f.phKey ? t(f.phKey) : (f.placeholder ?? '')
}
const CURRENCY_OPTIONS = ['USD', 'CNY', 'EUR', 'GBP', 'HKD', 'JPY', 'SGD', 'USDT']
// 选协议时的合理默认（pay_methods / 币种）
const PROTOCOL_DEFAULTS: Record<string, { pm: string; cur: string }> = {
  epay: { pm: 'alipay,wechat', cur: 'CNY' },
  stripe: { pm: 'card', cur: 'USD' },
  paypal: { pm: 'paypal', cur: 'USD' },
  webhook: { pm: 'card', cur: 'USD' },
  crypto: { pm: 'usdt', cur: 'USDT' },
  custom: { pm: 'card', cur: 'USD' },
}
interface PayChannelEdit {
  id: number; code: string; display_name: string; protocol: string
  pay_methods: string; currency: string; priority: number; enabled: boolean
  config: Record<string, string>
}
const pcPicker = ref(false) // 协议选择网格弹窗
const pcEdit = ref<PayChannelEdit | null>(null)
const pcBusy = ref(false)
// 当前协议的字段元数据（协议切换即变）
const pcFields = computed<PcFieldMeta[]>(() => (pcEdit.value ? PROTOCOL_FIELDS[pcEdit.value.protocol] ?? [] : []))
const pcProtocolLabel = computed(() => {
  const p = PROTOCOL_OPTIONS.find((o) => o.value === pcEdit.value?.protocol)
  return p ? protoOptLabel(p) : pcEdit.value?.protocol ?? ''
})
// 第一步：打开协议选择网格
function openPayPicker() { pcPicker.value = true }
// 第二步：选定协议 → 打开 rich form（新建渠道默认 disabled，测试连通后再手动启用——资金红线）
function pickProtocol(proto: string) {
  pcPicker.value = false
  const d = PROTOCOL_DEFAULTS[proto] ?? { pm: '', cur: 'USD' }
  pcEdit.value = { id: 0, code: '', display_name: '', protocol: proto, pay_methods: d.pm, currency: d.cur, priority: 100, enabled: false, config: {} }
}
function editPayChannel(ch: PayChannelRow) {
  // 编辑时密钥字段一律留空（=保留原密文，密钥永不回显）
  pcEdit.value = { id: ch.id, code: ch.code, display_name: ch.display_name, protocol: ch.protocol, pay_methods: ch.pay_methods, currency: ch.currency, priority: ch.priority, enabled: ch.enabled === 1, config: {} }
}
// 支付方式 CSV ↔ 勾选
function hasPayMethod(m: string): boolean {
  return (pcEdit.value?.pay_methods ?? '').split(',').map((s) => s.trim()).includes(m)
}
function togglePayMethod(m: string, on: boolean) {
  if (!pcEdit.value) return
  const set = new Set(pcEdit.value.pay_methods.split(',').map((s) => s.trim()).filter(Boolean))
  if (on) set.add(m)
  else set.delete(m)
  pcEdit.value.pay_methods = Array.from(set).join(',')
}
function protoColor(p: string): string {
  return PROTOCOL_OPTIONS.find((o) => o.value === p)?.color ?? '#06f'
}
async function savePayChannel() {
  const e = pcEdit.value
  if (!e) return
  pcBusy.value = true
  try {
    // 仅收集当前协议登记的、非空的字段（编辑时留空=不改）
    const config: Record<string, string> = {}
    for (const f of PROTOCOL_FIELDS[e.protocol] ?? []) {
      const v = (e.config[f.key] ?? '').trim()
      if (v) config[f.key] = v
    }
    const body: Record<string, unknown> = {
      code: e.code, display_name: e.display_name, protocol: e.protocol,
      pay_methods: e.pay_methods, currency: e.currency, priority: e.priority, enabled: e.enabled,
    }
    if (Object.keys(config).length) body.config = config
    await adminApi('POST', '/api/admin/payment-channels', body)
    pcEdit.value = null
    await loadPayChannels()
    error.value = ''
  } catch (err) {
    showError(err)
  } finally {
    pcBusy.value = false
  }
}
async function togglePayChannel(ch: PayChannelRow) {
  try {
    await adminApi('POST', '/api/admin/payment-channels', {
      code: ch.code, display_name: ch.display_name, protocol: ch.protocol,
      pay_methods: ch.pay_methods, currency: ch.currency, priority: ch.priority, enabled: ch.enabled !== 1,
    })
    await loadPayChannels()
  } catch (e) { showError(e) }
}
async function testPayChannel(ch: PayChannelRow) {
  try {
    await adminApi('POST', `/api/admin/payment-channels/${ch.id}/test`)
    await loadPayChannels()
  } catch (e) { showError(e) }
}
async function deletePayChannel(ch: PayChannelRow) {
  if (!confirm(t('admin.pcDeleteConfirm', { code: ch.code }))) return
  try {
    await adminApi('DELETE', `/api/admin/payment-channels/${ch.id}`)
    await loadPayChannels()
  } catch (e) { showError(e) }
}
// 回调地址预览（webhook 注册到网关后台用）：以当前站点为基准
const callbackBase = `${window.location.origin}/api/payment/callback`

// —— 运营设置 ——
const settings = ref<Record<string, string>>({})
const settingsBusy = ref(false)
// 设置分区子导航（避免一页拉太长）
const setSec = ref<'general' | 'signup' | 'channels' | 'oauth' | 'logs'>('general')
const SET_SECTIONS = ['general', 'signup', 'channels', 'oauth', 'logs'] as const

// —— 日志量 + 保留期一览（gap P2-C）——
interface LogStats {
  invocations: { total: number; oldest_at: string | null }
  audit_logs: { total: number; oldest_at_ms: number | null }
  retention: { log_retention_days: number; audit_log_retention_days: number }
}
const logStats = ref<LogStats | null>(null)
async function loadLogStats() {
  try {
    logStats.value = await adminApi<LogStats>('GET', '/api/admin/log-stats')
  } catch (e) {
    showError(e)
  }
}
const settingsMsg = ref('')
async function loadSettings() {
  settings.value = (await adminApi<{ settings: Record<string, string> }>('GET', '/api/admin/settings')).settings
}
async function saveSettings() {
  settingsBusy.value = true
  settingsMsg.value = ''
  try {
    await adminApi('PUT', '/api/admin/settings', settings.value)
    settingsMsg.value = t('settings.saved')
  } catch (e) {
    showError(e)
  } finally {
    settingsBusy.value = false
  }
}
// 邮箱域名白名单：CSV ↔ 标签（chip）UI（防临时邮箱批量注册，参考旧平台便捷管理）
const wlInput = ref('')
function csvTags(csv: string): string[] {
  return (csv ?? '').split(',').map((s) => s.trim()).filter(Boolean)
}
function addWhitelistDomain() {
  const v = wlInput.value.trim().toLowerCase()
  wlInput.value = ''
  if (!v) return
  const set = new Set(csvTags(settings.value.email_domain_whitelist ?? ''))
  set.add(v)
  settings.value.email_domain_whitelist = Array.from(set).join(',')
}
function removeWhitelistDomain(d: string) {
  settings.value.email_domain_whitelist = csvTags(settings.value.email_domain_whitelist ?? '').filter((x) => x !== d).join(',')
}

// —— 用户管理 ——
interface AdminUser {
  id: number; email: string; display_name: string; status: number; role: string
  org_id: number; balance: string; group: string
  invited: number; inviter_org_id: number; admin_remark: string
}
const PLAT_ROLES = ['user', 'support', 'admin', 'superadmin'] as const
const users = ref<AdminUser[]>([])
const userQuery = ref('')
async function loadUsers() {
  const q = userQuery.value ? `?q=${encodeURIComponent(userQuery.value)}` : ''
  users.value = (await adminApi<{ items: AdminUser[] }>('GET', `/api/admin/users${q}`)).items
}
async function manageUser(u: AdminUser, action: 'ban' | 'unban') {
  try {
    await adminApi('POST', `/api/admin/users/${u.id}/manage`, { action })
    await loadUsers()
  } catch (e) {
    showError(e)
  }
}
// 改用户分组（=层级/tier；复用组织分组端点，作用于用户的个人组织）
async function setUserGroup(u: AdminUser, group: string) {
  if (!u.org_id) return
  try {
    await adminApi('PUT', `/api/admin/orgs/${u.org_id}/group`, { group })
    await loadUsers()
  } catch (e) {
    showError(e)
  }
}
// 改平台角色（仅 superadmin；后端再次校验 + 自我保护，RBAC 0020）
async function setUserRole(u: AdminUser, role: string) {
  try {
    await adminApi('POST', `/api/admin/users/${u.id}/role`, { role })
    await loadUsers()
  } catch (e) {
    showError(e)
  }
}
// 运营备注（gap P2-B）：行内编辑，失焦保存；后端含等级守卫与长度上限
async function saveUserRemark(u: AdminUser, remark: string) {
  if (remark === u.admin_remark) return
  try {
    await adminApi('POST', `/api/admin/users/${u.id}/remark`, { remark })
    u.admin_remark = remark
  } catch (e) {
    showError(e)
    await loadUsers() // 失败回滚到服务端真值
  }
}

// —— 按用户聚合用量（gap P2-B）——
interface UsageByUser {
  user_id: number; email: string; calls: number; succeeded: number
  prompt_tokens: number; completion_tokens: number; cost_usd: string
}
const usageByUser = ref<UsageByUser[]>([])
const usageDays = ref(30)
const usageLoading = ref(false)
async function loadUsageByUser() {
  usageLoading.value = true
  try {
    usageByUser.value = (await adminApi<{ items: UsageByUser[] }>(
      'GET', `/api/admin/usage/by-user?days=${usageDays.value}`,
    )).items ?? []
  } catch (e) {
    showError(e)
  } finally {
    usageLoading.value = false
  }
}
// CSV 导出：带上 X-Admin-Token（沿用 adminApi 的鉴权头），fetch → blob → 触发下载
function exportUsageByUser() {
  const headers: Record<string, string> = {}
  const tok = adminToken.get()
  if (tok) headers['X-Admin-Token'] = tok
  const sess = auth.token
  if (sess) headers['Authorization'] = `Bearer ${sess}`
  fetch(`/api/admin/usage/by-user/export?days=${usageDays.value}`, { headers })
    .then((r) => (r.ok ? r.blob() : Promise.reject(new Error('export failed'))))
    .then((blob) => {
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `usage_by_user_${usageDays.value}d.csv`
      a.click()
      URL.revokeObjectURL(url)
    })
    .catch((e) => showError(e))
}

// —— 审计日志 ——
interface AuditRow { id: number; actor: string; action: string; target: string; detail: string; ip: string; created_at: number }
const audit = ref<AuditRow[]>([])
const selectedAudit = ref<AuditRow | null>(null)
async function loadAudit() {
  audit.value = (await adminApi<{ items: AuditRow[] }>('GET', '/api/admin/audit-logs')).items
}
// 动作码本身是英文（credit_grant 等），下划线转空格即可读；不为每个码做 9 语言键
function auditActionLabel(a: string): string {
  return a.replace(/_/g, ' ')
}
// 资金/封禁/安全类动作高亮，便于审计时一眼扫到敏感操作
const AUDIT_SENSITIVE = new Set(['credit_grant', 'user_ban', 'user_unban', 'settings_update', '2fa_disable', 'channel_disable', 'topup_paid'])
function auditSensitive(a: string): boolean {
  return AUDIT_SENSITIVE.has(a)
}

// 切到这些 tab 时懒加载
watch(tab, (v) => {
  if (v === 'settings' && Object.keys(settings.value).length === 0) void loadSettings().catch(showError)
  if (v === 'settings' && !logStats.value) void loadLogStats()
  if (v === 'overview' && !analytics.value) void loadAnalytics().catch(showError) // analytics 并入 overview
  if (v === 'payments' && !payLoaded.value) void loadPayChannels().catch(showError)
  if (v === 'users' && users.value.length === 0) void loadUsers().catch(showError)
  if (v === 'users' && usageByUser.value.length === 0) void loadUsageByUser().catch(showError)
  if (v === 'audit' && audit.value.length === 0) void loadAudit().catch(showError)
  if (v === 'channels') void loadHealth().catch(showError) // health 并入 channels
})

// —— 渠道健康看板（M2，只读可观测；不反馈选路）——
interface HealthRow {
  id: number
  name: string
  provider: string
  status: number
  calls: number
  succeeded: number
  failed: number
  errors_1h: number
  avg_latency_ms: number
  avg_frt_ms: number
  last_active: string
  success_rate: number
}
const health = ref<HealthRow[]>([])
async function loadHealth() {
  health.value = (await adminApi<{ channels: HealthRow[] }>('GET', '/api/admin/channel-health')).channels
  mcStats.value = (await adminApi<{ rows: MCStatRow[] }>('GET', '/api/admin/model-channel-stats?hours=24')).rows
}

// (model×channel) 交叉看板（§5 health dashboard；数据源 = 24h invocations 现算）
interface MCStatRow {
  model: string
  channel_id: number
  channel_name: string
  calls: number
  success_rate: number
  p50: number
  p95: number
  avg_frt_ms: number
}
const mcStats = ref<MCStatRow[]>([])

// —— 分组倍率 ——
const groupRatios = ref<GroupRatio[]>([])
const gr = ref({ name: '', ratio: '1' })
async function loadGroupRatios() {
  groupRatios.value = (await adminApi<{ groups: GroupRatio[] }>('GET', '/api/admin/group-ratios')).groups
}
async function upsertGroupRatio() {
  try {
    await adminApi('POST', '/api/admin/group-ratios', { name: gr.value.name, ratio: gr.value.ratio })
    gr.value = { name: '', ratio: '1' }
    await loadGroupRatios()
    error.value = ''
  } catch (e) {
    showError(e)
  }
}
async function deleteGroupRatio(name: string) {
  if (!confirm(t('admin.grDeleteConfirm', { name }))) return
  try {
    await adminApi('DELETE', `/api/admin/group-ratios/${encodeURIComponent(name)}`)
    await loadGroupRatios()
  } catch (e) {
    showError(e)
  }
}

// —— 二层 (分组,模型) 倍率（gap P2-F）——
interface ModelGroupRatio { group_name: string; model: string; ratio: string }
const modelGroupRatios = ref<ModelGroupRatio[]>([])
const mgr = ref({ group_name: '', model: '', ratio: '1' })
async function loadModelGroupRatios() {
  modelGroupRatios.value = (await adminApi<{ items: ModelGroupRatio[] }>('GET', '/api/admin/model-group-ratios')).items ?? []
}
async function upsertModelGroupRatio() {
  try {
    await adminApi('POST', '/api/admin/model-group-ratios', { group_name: mgr.value.group_name, model: mgr.value.model, ratio: mgr.value.ratio })
    mgr.value = { group_name: '', model: '', ratio: '1' }
    await loadModelGroupRatios()
    error.value = ''
  } catch (e) {
    showError(e)
  }
}
async function deleteModelGroupRatio(row: ModelGroupRatio) {
  try {
    await adminApi('DELETE', `/api/admin/model-group-ratios?group_name=${encodeURIComponent(row.group_name)}&model=${encodeURIComponent(row.model)}`)
    await loadModelGroupRatios()
  } catch (e) {
    showError(e)
  }
}
async function setOrgGroup(o: OrgRow, group: string) {
  try {
    await adminApi('PUT', `/api/admin/orgs/${o.id}/group`, { group })
    await loadOrgs()
    error.value = ''
  } catch (e) {
    showError(e)
  }
}

// —— Entitlements 功能调配（P0-2）：超管按 org 开关功能 / 设资源上限 ——
// 整行覆盖语义：保存时提交全部 4 个字段（与后端 UpsertEntitlements 一致）。
const entOpen = ref<number | null>(null)
const entForm = reactive({ feature_images: true, feature_video: true, max_members: 0, max_api_keys: 0, shard_count: 1 })
function toggleEnt(o: OrgRow) {
  if (entOpen.value === o.id) { entOpen.value = null; return }
  entForm.feature_images = o.feature_images
  entForm.feature_video = o.feature_video
  entForm.max_members = o.max_members
  entForm.max_api_keys = o.max_api_keys
  entForm.shard_count = o.shard_count || 1
  entOpen.value = o.id
}
async function saveEnt(o: OrgRow) {
  try {
    await adminApi('PUT', `/api/admin/orgs/${o.id}/entitlements`, {
      feature_images: entForm.feature_images,
      feature_video: entForm.feature_video,
      max_members: Math.max(0, Math.trunc(Number(entForm.max_members) || 0)),
      max_api_keys: Math.max(0, Math.trunc(Number(entForm.max_api_keys) || 0)),
    })
    entOpen.value = null
    await loadOrgs()
    error.value = ''
  } catch (e) {
    showError(e)
  }
}
// 规模化：单独保存账户分片数（巨客户写吞吐扩容；走 /shards 端点，与功能调配分开提交）。
async function saveShards(o: OrgRow) {
  try {
    await adminApi('PUT', `/api/admin/orgs/${o.id}/shards`, {
      shard_count: Math.min(64, Math.max(1, Math.trunc(Number(entForm.shard_count) || 1))),
    })
    entOpen.value = null
    await loadOrgs()
    error.value = ''
  } catch (e) {
    showError(e)
  }
}

// —— 日志（全局调用） ——
const logs = ref<LogRow[]>([])
const logTotal = ref(0)
const logPage = ref(1)
const logFilter = ref({ status: '', model: '', channel_id: '', org_id: '', since: '', until: '' })
const selectedLog = ref<LogRow | null>(null)
const LOG_PAGE_SIZE = 20

async function loadLogs() {
  const q = new URLSearchParams({ page: String(logPage.value), page_size: String(LOG_PAGE_SIZE) })
  for (const [k, v] of Object.entries(logFilter.value)) if (v) q.set(k, v)
  const r = await adminApi<{ total: number; items: LogRow[] }>('GET', `/api/admin/invocations?${q}`)
  logs.value = r.items
  logTotal.value = r.total
}
function applyLogFilter() {
  logPage.value = 1
  void loadLogs().catch(showError)
}
function showLog(row: LogRow) { if (auth.isSuperadmin) selectedLog.value = row }
watch([() => auth.orgId, () => auth.role, tab, unlocked], () => { selectedLog.value = null; selectedAudit.value = null })
watch(logPage, () => void loadLogs().catch(showError))
// 切到日志 tab 时首次加载
watch(tab, (v) => {
  if (v === 'logs' && logs.value.length === 0) void loadLogs().catch(showError)
})

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
const ch = ref({ name: '', provider: 'openai', base_url: '', api_key: '', models: '', weight: 1, priority: 0, proxy: '' })
const chBusy = ref(false)
const providerDefault = computed(
  () => providers.value.find((p) => p.name === ch.value.provider)?.default_base_url ?? '',
)

const channelsErr = ref('')
async function loadChannels() {
  channelsErr.value = ''
  try {
    const [chs, provs] = await Promise.all([
      adminApi<{ channels: Channel[] }>('GET', '/api/admin/channels'),
      adminApi<{ providers: Provider[] }>('GET', '/api/admin/providers'),
    ])
    channels.value = chs.channels
    providers.value = provs.providers
  } catch (e) {
    channelsErr.value = errorMessage(e, t('common.error')) // 让失败可见而非空表
    throw e
  }
}
async function addChannel() {
  chBusy.value = true
  try {
    await adminApi('POST', '/api/admin/channels', {
      ...ch.value,
      models: ch.value.models.split(',').map((m) => m.trim()).filter(Boolean),
    })
    ch.value = { name: '', provider: ch.value.provider, base_url: '', api_key: '', models: '', weight: 1, priority: 0, proxy: '' }
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
    await adminApi('POST', `/api/admin/channels/${id}/disable`)
    await loadChannels()
  } catch (e) {
    showError(e)
  }
}
async function enableChannel(id: number) {
  try {
    await adminApi('POST', `/api/admin/channels/${id}/enable`)
    await loadChannels()
  } catch (e) {
    showError(e)
  }
}
async function deleteChannel(id: number) {
  if (!confirm(t('admin.chDeleteConfirm'))) return
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

// —— 渠道探测（上货前自检：补全 base_url + 拉模型） ——
interface ProbeResult {
  ok: boolean
  detected_base_url: string
  models: string[]
  tried: { base_url: string; status: number; ok: boolean; note: string }[]
  gateway: string
  message: string
}
const showProbe = ref(false)
const probeForm = ref({ api_key: '', base_url: '' })
const probing = ref(false)
const probeResult = ref<ProbeResult | null>(null)
const probeErr = ref('')
const copiedProbeModel = ref('')

async function runProbe() {
  probing.value = true
  probeErr.value = ''
  probeResult.value = null
  try {
    probeResult.value = await adminApi<ProbeResult>('POST', '/api/admin/channels/probe', {
      api_key: probeForm.value.api_key,
      base_url: probeForm.value.base_url,
    })
  } catch (e) {
    probeErr.value = errorMessage(e, t('common.error'))
  } finally {
    probing.value = false
  }
}
async function copyProbeModel(m: string) {
  if (await copyText(m)) {
    copiedProbeModel.value = m
    setTimeout(() => (copiedProbeModel.value = ''), 1000)
  }
}
// 探测成功 → 一键把结果填进"添加渠道"表单
function useProbe() {
  if (!probeResult.value?.ok) return
  ch.value.base_url = probeResult.value.detected_base_url
  ch.value.api_key = probeForm.value.api_key
  ch.value.models = probeResult.value.models.join(', ')
  showProbe.value = false
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
const editForm = ref({ base_url: '', models: '', api_key: '', weight: 1, priority: 0, strict_response: true, mapping: '', proxy: '', param_override: '', header_override: '', credential_mode: 'random', converter: '', upstream_path: '', model_sync_enabled: false })
const editBusy = ref(false)
const editError = ref('') // 弹窗内错误（页面顶部横幅会被弹窗遮罩盖住，校验 400 必须在弹窗里可见）

// —— 渠道多 key（0034 Multi-key）——
interface ChannelKey { id: number; fingerprint: string; status: number; mode_weight: number; disabled_reason?: string }
const editKeys = ref<ChannelKey[]>([])
const newKey = ref('')
const keyBusy = ref(false)

async function loadKeys(chID: number) {
  try {
    editKeys.value = (await adminApi<{ credentials: ChannelKey[] }>('GET', `/api/admin/channels/${chID}/credentials`)).credentials
  } catch { editKeys.value = [] }
}
async function addKey() {
  if (!editing.value || !newKey.value.trim()) return
  keyBusy.value = true
  try {
    await adminApi('POST', `/api/admin/channels/${editing.value.id}/credentials`, { api_key: newKey.value.trim() })
    newKey.value = ''
    await loadKeys(editing.value.id)
    editError.value = ''
  } catch (e) { editError.value = errorMessage(e, t('common.error')) } finally { keyBusy.value = false }
}
async function toggleKey(k: ChannelKey) {
  if (!editing.value) return
  const action = k.status === 1 ? 'disable' : 'enable'
  try {
    await adminApi('POST', `/api/admin/channels/${editing.value.id}/credentials/${k.id}/${action}`, {})
    await loadKeys(editing.value.id)
  } catch (e) { editError.value = errorMessage(e, t('common.error')) }
}
async function deleteKey(k: ChannelKey) {
  if (!editing.value) return
  try {
    await adminApi('DELETE', `/api/admin/channels/${editing.value.id}/credentials/${k.id}`, {})
    await loadKeys(editing.value.id)
    editError.value = ''
  } catch (e) { editError.value = errorMessage(e, t('common.error')) }
}

function openEdit(ch: Channel) {
  editing.value = ch
  editError.value = ''
  editForm.value = {
    base_url: ch.base_url,
    models: ch.models,
    api_key: '', // 留空=保留原凭证
    weight: ch.weight,
    priority: ch.priority,
    strict_response: ch.strict_response,
    mapping: mappingToLines(ch.model_mapping),
    proxy: ch.proxy,
    param_override: ch.param_override || '',
    header_override: ch.header_override || '',
    credential_mode: ch.credential_mode || 'random',
    converter: ch.converter || '',
    upstream_path: ch.upstream_path || '',
    model_sync_enabled: !!ch.model_sync_enabled,
  }
  void loadKeys(ch.id)
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
      proxy: editForm.value.proxy, // 空串 = 清空代理（直连）
      param_override: editForm.value.param_override.trim(), // 空串 = 清空 override
      header_override: editForm.value.header_override.trim(),
      credential_mode: editForm.value.credential_mode,
      converter: editForm.value.converter,
      upstream_path: editForm.value.upstream_path.trim(),
      model_sync_enabled: editForm.value.model_sync_enabled,
    }
    if (editForm.value.api_key) body.api_key = editForm.value.api_key
    await adminApi('PUT', `/api/admin/channels/${editing.value.id}`, body)
    editing.value = null
    editError.value = ''
    await loadChannels()
    error.value = ''
  } catch (e) {
    // 校验 400（如 override 受保护键）显示在弹窗内——页面顶部横幅被弹窗遮罩盖住，用户根本看不见
    editError.value = errorMessage(e, t('common.error'))
    if (e instanceof ApiError && e.status === 401) unlocked.value = false
  } finally {
    editBusy.value = false
  }
}

// —— 组织 ——
const orgs = ref<OrgRow[]>([])
async function loadOrgs() {
  orgs.value = (await adminApi<{ orgs: OrgRow[] }>('GET', '/api/admin/orgs')).orgs
}
// 从组织行打开授信弹窗
function grantTo(o: OrgRow) {
  openGrant(o.id, o.name || `#${o.id}`)
}

const pricingCatalogVersion = ref(0)
const metadataOpen = ref(false)

// —— 模型元数据（gap P2-D）——
const metaForm = ref({ model: '', vendor: '', category: '', tags: '', description: '', context_length: '', icon: '' })
const metaBusy = ref(false)
function pickModelForMeta(m: { model: string; meta?: ModelMeta | null }) {
  metadataOpen.value = true
  metaForm.value = {
    model: m.model,
    vendor: m.meta?.vendor ?? '',
    category: m.meta?.category ?? '',
    tags: m.meta?.tags ?? '',
    description: m.meta?.description ?? '',
    context_length: m.meta?.context_length ? String(m.meta.context_length) : '',
    icon: m.meta?.icon ?? '',
  }
}
async function upsertMeta() {
  if (!metaForm.value.model.trim()) return
  metaBusy.value = true
  try {
    await adminApi('POST', '/api/admin/model-meta', {
      model: metaForm.value.model.trim(),
      vendor: metaForm.value.vendor,
      category: metaForm.value.category,
      tags: metaForm.value.tags,
      description: metaForm.value.description,
      context_length: Number(metaForm.value.context_length) || 0,
      icon: metaForm.value.icon,
    })
    metaForm.value = { model: '', vendor: '', category: '', tags: '', description: '', context_length: '', icon: '' }
    pricingCatalogVersion.value++
    error.value = ''
  } catch (e) {
    showError(e)
  } finally {
    metaBusy.value = false
  }
}

// —— 渠道：拉取上游模型 + 缺失检测（gap P2-D）——
interface FetchModelsResult {
  ok: boolean; message?: string; detected_base_url?: string; upstream_count?: number
  missing_in_config?: string[]; extra_in_config?: string[]; unpriced?: string[]
}
const fetchModelsResult = ref<(FetchModelsResult & { channel: string; channelId: number }) | null>(null)
const fetchingModelsId = ref<number | null>(null)
const modelActionBusy = ref(false)
// 后台同步（§3.10）检测到的、尚未上货/忽略的新上游模型数——列表徽标用。
function detectedCount(c: Channel): number {
  return c.detected_models ? c.detected_models.split(',').filter(Boolean).length : 0
}
async function fetchChannelModels(c: Channel) {
  fetchingModelsId.value = c.id
  try {
    const r = await adminApi<FetchModelsResult>('GET', `/api/admin/channels/${c.id}/fetch-models`)
    fetchModelsResult.value = { ...r, channel: c.name, channelId: c.id }
  } catch (e) {
    showError(e)
  } finally {
    fetchingModelsId.value = null
  }
}

// 上货闭环（§3.10）：apply=追加进 channels.models（可路由，绝不自动定价）；
// ignore=加忽略清单（不再提示）；mark-removed=从 models 移除上游已下架模型。
async function channelModelAction(path: 'apply-models' | 'ignore-models' | 'mark-removed', models: string[]) {
  const r = fetchModelsResult.value
  if (!r || !models.length) return
  modelActionBusy.value = true
  try {
    await adminApi('POST', `/api/admin/channels/${r.channelId}/${path}`, { models })
    fetchModelsResult.value = null
    await loadChannels()
  } catch (e) {
    showError(e)
  } finally {
    modelActionBusy.value = false
  }
}

// —— 授信入账（加余额）——
// 统一弹窗，用户管理 / 组织列表共用。幂等键在「打开弹窗」时生成一次并固定，
// 重复点击/网络重试用同一 key → 永不重复入账（旧实现每次 grant() 新 key 是漏洞）。
const grantModal = ref<{ orgId: number; label: string; key: string } | null>(null)
const grantForm = ref<{ mode: string; amount_usd: string; reason: string }>({ mode: 'add', amount_usd: '', reason: '' })
const grantBusy = ref(false)
const grantResult = ref<{ balance_after: string; already_applied: boolean } | null>(null)

function openGrant(orgId: number, label: string) {
  if (!orgId) {
    showError(new Error(t('admin.crNoOrg')))
    return
  }
  grantModal.value = { orgId, label, key: uuid() }
  grantForm.value = { mode: 'add', amount_usd: '', reason: '' }
  grantResult.value = null
}
async function submitGrant() {
  if (!grantModal.value) return
  grantBusy.value = true
  try {
    const r = await adminApi<{ balance_after: string; already_applied: boolean }>(
      'POST',
      '/api/admin/credits',
      {
        org_id: grantModal.value.orgId,
        mode: grantForm.value.mode,
        amount_usd: grantForm.value.amount_usd,
        reason: grantForm.value.reason,
        idempotency_key: grantModal.value.key, // 固定 key：重提不重复入账
      },
    )
    grantResult.value = r
    error.value = ''
    // 余额已变，刷新两处来源
    if (tab.value === 'users') void loadUsers().catch(showError)
    if (tab.value === 'orgs') void loadOrgs().catch(showError)
  } catch (e) {
    showError(e)
  } finally {
    grantBusy.value = false
  }
}

async function loadAll() {
  try {
    // group-ratios/model-group-ratios 是 superadmin-only 端点，非超管加载会 403 → 按角色分支（FE-1）
    // analytics 随 overview（默认 tab）、health 随 channels 一并加载——减法合并后二者不再有独立 tab，
    // watch(tab) 在初始 tab 上不触发，故在此预载（admin 侧多两查询可接受）。
    const tasks = [loadOverview(), loadAnalytics(), loadChannels(), loadHealth(), loadOrgs()]
    if (auth.isSuperadmin) {
      tasks.push(loadGroupRatios(), loadModelGroupRatios())
    }
    await Promise.all(tasks)
  } catch (e) {
    showError(e)
  }
}
onMounted(async () => {
  // RBAC（0020）：取当前用户角色；admin/superadmin 直接解锁后台，无需粘 root token。
  await auth.fetchMe()
  if (auth.isAdmin) unlocked.value = true
  roleLoading.value = false
  if (unlocked.value) void loadAll()
})
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('surface.adminEyebrow') }}</p>
        <h1 class="app-page-title">{{ t('admin.title') }}</h1>
        <p class="app-page-subtitle">{{ t('surface.adminSubtitle') }}</p>
      </div>
    </div>

    <!-- 角色解析中：不闪 token 表单 -->
    <p v-if="roleLoading" class="text-sm text-muted">{{ t('common.loading') }}</p>

    <!-- 门禁：非 admin 角色 → root-token 破玻璃（引导首个超管 / 应急） -->
    <form v-else-if="!unlocked" class="app-panel max-w-md p-6 space-y-4" @submit.prevent="unlock">
      <div>
        <label class="label" for="admintoken">{{ t('admin.token') }}</label>
        <input id="admintoken" v-model="tokenInput" class="input" type="password" required autocomplete="off" />
        <p class="mt-1.5 text-xs text-muted">{{ t('admin.tokenHint') }}</p>
        <p class="mt-1 text-xs text-muted">{{ t('admin.roleHint') }}</p>
      </div>
      <button class="btn-primary">{{ t('admin.unlock') }}</button>
    </form>

    <template v-else>
      <div class="toolbar-panel">
        <div class="flex max-w-full gap-1 overflow-x-auto" role="tablist">
          <button
            v-for="tb in visibleTabs"
            :key="tb"
            type="button"
            role="tab"
            :aria-selected="tab === tb"
            class="shrink-0 rounded-md px-3 py-1.5 text-sm transition-colors"
            :class="tab === tb ? 'bg-panel text-ink shadow-sm' : 'text-muted hover:text-ink'"
            @click="selectAdminTab(tb)"
          >
            {{ t(`admin.${tb}`) }}
          </button>
        </div>
        <button class="btn-ghost !py-1.5 text-xs" @click="lock">{{ t('admin.clear') }}</button>
      </div>

      <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>

      <!-- 概览 -->
      <template v-if="tab === 'overview' && overview">
        <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <div class="metric-card">
            <div class="flex items-center gap-3">
              <span class="metric-icon mi-sky"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 13.125C3 12.504 3.504 12 4.125 12h2.25c.621 0 1.125.504 1.125 1.125v6.75C7.5 20.496 6.996 21 6.375 21h-2.25A1.125 1.125 0 0 1 3 19.875v-6.75ZM9.75 8.625c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125v11.25c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V8.625ZM16.5 4.125c0-.621.504-1.125 1.125-1.125h2.25C20.496 3 21 3.504 21 4.125v15.75c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V4.125Z" /></svg></span>
              <p class="text-xs text-muted">{{ t('admin.ovChannels') }}</p>
            </div>
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
          <div class="metric-card">
            <div class="flex items-center gap-3">
              <span class="metric-icon mi-indigo"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M7.5 21 3 16.5m0 0L7.5 12M3 16.5h13.5m0-13.5L21 7.5m0 0L16.5 12M21 7.5H7.5" /></svg></span>
              <p class="text-xs text-muted">{{ t('dashboard.todayCalls') }}</p>
            </div>
            <p class="mt-1 text-2xl font-semibold font-mono">{{ overview.calls_today }}</p>
            <p class="mt-1 text-xs text-muted">{{ t('admin.ovWindowCalls', { n: overview.calls_window, days: overview.window_days }) }}</p>
          </div>
          <div class="metric-card">
            <div class="flex items-center gap-3">
              <span class="metric-icon mi-green"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 12.75 11.25 15 15 9.75M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span>
              <p class="text-xs text-muted">{{ t('admin.ovSuccessRate') }}</p>
            </div>
            <p class="mt-1 text-2xl font-semibold font-mono">{{ (overview.success_rate * 100).toFixed(1) }}%</p>
            <p class="mt-1 text-xs text-muted">{{ t('admin.ovLastDays', { days: overview.window_days }) }}</p>
          </div>
          <div class="metric-card">
            <div class="flex items-center gap-3">
              <span class="metric-icon mi-emerald"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v12m-3-2.818.879.659c1.171.879 3.07.879 4.242 0 1.172-.879 1.172-2.303 0-3.182C13.536 12.219 12.768 12 12 12c-.725 0-1.45-.22-2.003-.659-1.106-.879-1.106-2.303 0-3.182s2.9-.879 4.006 0l.415.33M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span>
              <p class="text-xs text-muted">{{ t('admin.anConsumption') }}</p>
            </div>
            <p class="mt-1 text-2xl font-semibold font-mono">${{ Number(overview.revenue_window).toFixed(4) }}</p>
            <p class="mt-1 text-xs text-muted">{{ t('admin.ovLastDays', { days: overview.window_days }) }}</p>
          </div>
        </div>

        <div class="grid gap-4 lg:grid-cols-2">
          <!-- 模型 top -->
          <div class="app-panel p-5">
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
          <div class="app-panel p-5">
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

      <!-- 运营分析：资金 / 用户 / 质量 颗粒度 -->
      <!-- 运营分析（减法：并入概览 tab，同页展示资金/用户/质量颗粒度）-->
      <template v-if="tab === 'overview'">
        <div v-if="analytics" class="space-y-6">
          <div class="flex items-center justify-between">
            <p class="text-sm text-muted">{{ t('admin.anIntro') }}</p>
            <select v-model.number="analyticsDays" class="input !w-auto !py-1.5 text-sm" @change="loadAnalytics().catch(showError)">
              <option :value="7">{{ t('admin.anDays', { n: 7 }) }}</option>
              <option :value="14">{{ t('admin.anDays', { n: 14 }) }}</option>
              <option :value="30">{{ t('admin.anDays', { n: 30 }) }}</option>
              <option :value="90">{{ t('admin.anDays', { n: 90 }) }}</option>
            </select>
          </div>

          <!-- 资金大卡 -->
          <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <div class="app-panel p-4">
              <div class="flex items-center gap-3">
                <span class="metric-icon mi-indigo"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.25 8.25h19.5M2.25 9h19.5m-16.5 5.25h6m-6 2.25h3m-3.75 3h15a2.25 2.25 0 0 0 2.25-2.25V6.75A2.25 2.25 0 0 0 19.5 4.5h-15a2.25 2.25 0 0 0-2.25 2.25v10.5A2.25 2.25 0 0 0 4.5 19.5Z" /></svg></span>
                <p class="text-xs text-muted">{{ t('admin.anCashIn') }}</p>
              </div>
              <p class="mt-1 text-2xl font-semibold font-mono text-ok">${{ Number(analytics.finance.cash_in).toFixed(2) }}</p>
              <Sparkline :data="cashSeries" :width="140" :height="34" class="mt-2" />
            </div>
            <div class="app-panel p-4">
              <div class="flex items-center gap-3">
                <span class="metric-icon mi-emerald"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v12m-3-2.818.879.659c1.171.879 3.07.879 4.242 0 1.172-.879 1.172-2.303 0-3.182C13.536 12.219 12.768 12 12 12c-.725 0-1.45-.22-2.003-.659-1.106-.879-1.106-2.303 0-3.182s2.9-.879 4.006 0l.415.33M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span>
                <p class="text-xs text-muted">{{ t('admin.anConsumption') }}</p>
              </div>
              <p class="mt-1 text-2xl font-semibold font-mono">${{ Number(analytics.finance.consumption).toFixed(4) }}</p>
              <Sparkline :data="costSeries" :width="140" :height="34" class="mt-2" />
            </div>
            <div class="app-panel p-4">
              <div class="flex items-center gap-3">
                <span class="metric-icon mi-amber"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12a2.25 2.25 0 0 0-2.25-2.25H15a3 3 0 1 1-6 0H5.25A2.25 2.25 0 0 0 3 12m18 0v6a2.25 2.25 0 0 1-2.25 2.25H5.25A2.25 2.25 0 0 1 3 18v-6m18 0V9M3 12V9m18 0a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 9m18 0V6a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 6v3" /></svg></span>
                <p class="text-xs text-muted">{{ t('admin.anFloat') }}</p>
              </div>
              <p class="mt-1 text-2xl font-semibold font-mono">${{ Number(analytics.finance.float).toFixed(2) }}</p>
              <p class="mt-1 text-xs text-muted">{{ t('admin.anFloatHint') }}</p>
            </div>
            <div class="app-panel p-4">
              <div class="flex items-center gap-3">
                <span class="metric-icon mi-rose"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 13.125C3 12.504 3.504 12 4.125 12h2.25c.621 0 1.125.504 1.125 1.125v6.75C7.5 20.496 6.996 21 6.375 21h-2.25A1.125 1.125 0 0 1 3 19.875v-6.75ZM9.75 8.625c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125v11.25c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V8.625ZM16.5 4.125c0-.621.504-1.125 1.125-1.125h2.25C20.496 3 21 3.504 21 4.125v15.75c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V4.125Z" /></svg></span>
                <p class="text-xs text-muted">{{ t('admin.anConversion') }}</p>
              </div>
              <p class="mt-1 text-2xl font-semibold font-mono">{{ (analytics.finance.topup_conversion * 100).toFixed(1) }}%</p>
              <p class="mt-1 text-xs text-muted">
                <span class="text-ok">{{ analytics.finance.topup_paid }} {{ t('admin.anPaid') }}</span> ·
                {{ analytics.finance.topup_pending }} {{ t('admin.anPending') }} ·
                <span class="text-danger">{{ analytics.finance.topup_failed }} {{ t('admin.anFailed') }}</span>
              </p>
            </div>
          </div>

          <!-- 用户指标 -->
          <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <div class="app-panel p-4">
              <div class="flex items-center gap-3">
                <span class="metric-icon mi-sky"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.25 18 9 11.25l4.306 4.306a11.95 11.95 0 0 1 5.814-5.518l2.74-1.22m0 0-5.94-2.281m5.94 2.28-2.28 5.941" /></svg></span>
                <p class="text-xs text-muted">{{ t('admin.anUsers') }}</p>
              </div>
              <p class="mt-1 text-2xl font-semibold font-mono">{{ analytics.users.total_users }}</p>
              <p class="mt-1 text-xs text-ok">+{{ analytics.users.new_users }} {{ t('admin.anNew') }}</p>
            </div>
            <div class="app-panel p-4">
              <div class="flex items-center gap-3">
                <span class="metric-icon mi-violet"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.25 13.5h8.25l-3.696 3.696a8.25 8.25 0 1 1 0-11.668L10.5 9.75H2.25v3.75Z" /></svg></span>
                <p class="text-xs text-muted">{{ t('admin.anActiveOrgs') }}</p>
              </div>
              <p class="mt-1 text-2xl font-semibold font-mono">
                {{ analytics.users.active_orgs }}<span class="text-base text-muted">/{{ analytics.users.total_orgs }}</span>
              </p>
              <p class="mt-1 text-xs text-muted">{{ t('admin.anActiveHint') }}</p>
            </div>
            <div class="app-panel p-4">
              <div class="flex items-center gap-3">
                <span class="metric-icon mi-cyan"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v12m-3-2.818.879.659c1.171.879 3.07.879 4.242 0 1.172-.879 1.172-2.303 0-3.182C13.536 12.219 12.768 12 12 12c-.725 0-1.45-.22-2.003-.659-1.106-.879-1.106-2.303 0-3.182s2.9-.879 4.006 0l.415.33M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span>
                <p class="text-xs text-muted">{{ t('admin.anArpu') }}</p>
              </div>
              <p class="mt-1 text-2xl font-semibold font-mono">${{ Number(analytics.users.arpu).toFixed(4) }}</p>
              <p class="mt-1 text-xs text-muted">{{ t('admin.anArpuHint') }}</p>
            </div>
            <div class="app-panel p-4">
              <div class="flex items-center gap-3">
                <span class="metric-icon mi-green"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span>
                <p class="text-xs text-muted">{{ t('admin.anErrorRate') }}</p>
              </div>
              <p class="mt-1 text-2xl font-semibold font-mono" :class="errorRate > 0.05 ? 'text-danger' : ''">{{ (errorRate * 100).toFixed(2) }}%</p>
              <p class="mt-1 text-xs text-muted">
                {{ t('admin.anLatency') }} {{ Math.round(analytics.quality.avg_duration_ms) }}ms · FRT {{ Math.round(analytics.quality.avg_frt_ms) }}ms
              </p>
            </div>
          </div>

          <div class="grid gap-4 lg:grid-cols-2">
            <!-- Top 消费组织 -->
            <div class="app-panel p-5">
              <h2 class="mb-4 font-semibold">{{ t('admin.anTopOrgs') }}</h2>
              <div v-if="analytics.top_orgs.length === 0" class="text-sm text-muted">{{ t('common.empty') }}</div>
              <table v-else class="table w-full text-sm">
                <thead>
                  <tr><th class="text-left">{{ t('admin.anOrg') }}</th><th class="text-right">{{ t('admin.ovCalls') }}</th><th class="text-right">{{ t('admin.anSpend') }}</th></tr>
                </thead>
                <tbody>
                  <tr v-for="o in analytics.top_orgs" :key="o.org_id">
                    <td class="truncate">{{ o.name || `#${o.org_id}` }}</td>
                    <td class="text-right font-mono text-muted">{{ o.calls }}</td>
                    <td class="text-right font-mono">${{ Number(o.cost).toFixed(4) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>

            <!-- 厂商分布 + 充值渠道 -->
            <div class="space-y-4">
              <div class="app-panel p-5">
                <h2 class="mb-3 font-semibold">{{ t('admin.anByProvider') }}</h2>
                <div v-if="analytics.quality.by_provider.length === 0" class="text-sm text-muted">{{ t('common.empty') }}</div>
                <div v-else class="space-y-2">
                  <div v-for="p in analytics.quality.by_provider" :key="p.provider" class="flex items-center justify-between text-sm">
                    <span class="font-mono">{{ p.provider || '—' }}</span>
                    <span class="text-muted">{{ p.calls }} · ${{ Number(p.cost).toFixed(4) }}</span>
                  </div>
                </div>
              </div>
              <div class="app-panel p-5">
                <h2 class="mb-3 font-semibold">{{ t('admin.anCashByProvider') }}</h2>
                <div v-if="analytics.finance.by_provider.length === 0" class="text-sm text-muted">{{ t('common.empty') }}</div>
                <div v-else class="space-y-2">
                  <div v-for="p in analytics.finance.by_provider" :key="p.provider" class="flex items-center justify-between text-sm">
                    <span class="font-mono">{{ p.provider }}</span>
                    <span class="text-muted">{{ p.paid_count }} · <span class="text-ok">${{ Number(p.amount).toFixed(2) }}</span></span>
                  </div>
                </div>
              </div>
            </div>
          </div>

          <!-- 调用量日趋势 -->
          <div class="app-panel p-5">
            <div class="mb-3 flex items-baseline justify-between">
              <h2 class="font-semibold">{{ t('admin.anCallTrend') }}</h2>
              <span class="text-xs text-muted">{{ t('admin.ovLastDays', { days: analytics.days }) }}</span>
            </div>
            <Sparkline :data="callsSeries" :width="640" :height="48" class="w-full" />
          </div>
        </div>
        <div v-else-if="analyticsErr" class="app-panel p-6 text-sm">
          <p class="text-danger">{{ analyticsErr }}</p>
          <button class="btn-ghost mt-3 !py-1.5 text-sm" @click="loadAnalytics().catch(showError)">{{ t('common.retry') }}</button>
        </div>
        <p v-else class="text-sm text-muted">{{ t('common.loading') }}</p>
      </template>

      <!-- 渠道 -->
      <template v-if="tab === 'channels'">
        <div v-if="channelsErr" class="app-panel mb-3 flex items-center gap-3 p-4 text-sm">
          <span class="text-danger">{{ channelsErr }}</span>
          <button class="btn-ghost !py-1 text-xs" @click="loadChannels().catch(showError)">{{ t('common.retry') }}</button>
        </div>
        <div class="flex items-center justify-between">
          <p class="text-sm text-muted">{{ t('admin.chIntro') }}</p>
          <button class="btn-ghost" @click="showProbe = true">{{ t('admin.probe') }}</button>
        </div>

        <!-- 探测弹窗 -->
        <div v-if="showProbe" class="modal-backdrop" @click.self="showProbe = false">
          <div class="app-panel modal-panel w-full max-w-lg p-6 space-y-4">
            <h2 class="font-semibold">{{ t('admin.probeTitle') }}</h2>
            <p class="text-xs text-muted">{{ t('admin.probeHint') }}</p>
            <form class="space-y-3" @submit.prevent="runProbe">
              <div>
                <label class="label" for="pb-url">{{ t('admin.probeDomain') }}</label>
                <input id="pb-url" v-model="probeForm.base_url" class="input font-mono text-xs" placeholder="api.example.com" required />
              </div>
              <div>
                <label class="label" for="pb-key">{{ t('admin.chKey') }}</label>
                <input id="pb-key" v-model="probeForm.api_key" class="input font-mono text-xs" type="password" autocomplete="off" required />
              </div>
              <div class="flex justify-end gap-2">
                <button type="button" class="btn-ghost" @click="showProbe = false">{{ t('common.cancel') }}</button>
                <button class="btn-primary" :disabled="probing">{{ probing ? t('admin.probing') : t('admin.probeRun') }}</button>
              </div>
            </form>

            <p v-if="probeErr" class="text-sm text-danger" role="alert">{{ probeErr }}</p>

            <!-- 探测结果 -->
            <div v-if="probeResult" class="space-y-3 border-t border-line pt-4">
              <div v-if="probeResult.ok" class="space-y-3">
                <div class="text-sm">
                  <span class="badge-ok">OK</span>
                  <span class="ml-2 text-muted">{{ t('admin.probeDetected') }}</span>
                  <code class="ml-1 font-mono text-xs select-all">{{ probeResult.detected_base_url }}</code>
                </div>
                <p v-if="probeResult.gateway" class="text-xs text-muted">
                  {{ t('admin.probeGateway') }}: <span class="font-mono">{{ probeResult.gateway }}</span>
                </p>
                <div>
                  <p class="mb-1 text-xs text-muted">{{ t('admin.probeModels', { n: probeResult.models.length }) }}</p>
                  <div class="max-h-44 overflow-y-auto rounded-lg border border-line p-2">
                    <button
                      v-for="m in probeResult.models"
                      :key="m"
                      type="button"
                      class="m-0.5 inline-flex items-center gap-1 rounded-md bg-elev px-2 py-1 font-mono text-xs hover:text-accent"
                      :title="t('common.copy')"
                      @click="copyProbeModel(m)"
                    >
                      {{ m }}
                      <span v-if="copiedProbeModel === m" class="text-ok">✓</span>
                    </button>
                  </div>
                </div>
                <button class="btn-primary w-full" @click="useProbe">{{ t('admin.probeUse') }}</button>
              </div>
              <div v-else class="space-y-2">
                <p class="text-sm text-danger">{{ probeResult.message }}</p>
                <details class="text-xs text-muted">
                  <summary class="cursor-pointer">{{ t('admin.probeTried') }}</summary>
                  <ul class="mt-1 space-y-1">
                    <li v-for="a in probeResult.tried" :key="a.base_url" class="font-mono">
                      {{ a.base_url }} — {{ a.note }}
                    </li>
                  </ul>
                </details>
              </div>
            </div>
          </div>
        </div>

        <form class="app-panel grid gap-4 p-5 sm:grid-cols-2" @submit.prevent="addChannel">
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
          <div class="sm:col-span-2">
            <label class="label" for="ch-baseurl">{{ t('admin.chBaseUrl') }}</label>
            <input id="ch-baseurl" v-model="ch.base_url" class="input font-mono text-xs" :placeholder="providerDefault || 'https://…'" />
            <p class="mt-1 text-xs text-muted">{{ t('admin.chBaseUrlHint') }}</p>
          </div>
          <div class="sm:col-span-2">
            <label class="label" for="ch-key">{{ t('admin.chKey') }}</label>
            <input id="ch-key" v-model="ch.api_key" class="input font-mono text-xs" type="password" required autocomplete="off" />
          </div>
          <div class="sm:col-span-2">
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
          <div class="sm:col-span-2">
            <label class="label" for="ch-proxy">{{ t('admin.chProxy') }}</label>
            <input id="ch-proxy" v-model="ch.proxy" class="input font-mono text-xs" placeholder="http://user:pass@host:port" />
            <p class="mt-1 text-xs text-muted">{{ t('admin.chProxyHint') }}</p>
          </div>
          <div class="sm:col-span-2">
            <button class="btn-primary" :disabled="chBusy">{{ t('admin.chAdd') }}</button>
          </div>
        </form>

        <!-- 测试结果条 -->
        <div
          v-if="testResult"
          class="app-panel p-4 text-sm flex items-start gap-3"
          role="status"
          aria-live="polite"
        >
          <span :class="testResult.r.ok ? 'text-ok' : 'text-danger'" class="font-medium shrink-0">
            {{ testResult.name }} · {{ testResult.r.ok ? t('admin.chTestOk', { ms: testResult.r.latency_ms }) : t('admin.chTestFail') }}
          </span>
          <span v-if="!testResult.r.ok" class="text-muted break-all">{{ testResult.r.message }}</span>
          <button class="btn-ghost !py-0.5 ml-auto text-xs shrink-0" @click="testResult = null">✕</button>
        </div>

        <div class="data-table-wrap">
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
                <th class="px-4 py-3 font-medium">{{ t('admin.chQuality') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('keys.status') }}</th>
                <th class="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="channels.length === 0">
                <td colspan="10" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <tr v-for="c in channels" :key="c.id" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 text-muted">{{ c.id }}</td>
                <td class="px-4 py-2.5">
                  {{ c.name }}
                  <span v-if="c.model_mapping" class="ml-1 badge-muted" :title="t('admin.chMapping')">map</span>
                  <span v-if="!c.strict_response" class="ml-1 badge-danger" :title="t('admin.chStrict')">lax</span>
                  <button v-if="detectedCount(c)" class="ml-1 badge-accent cursor-pointer"
                    :title="t('admin.chDetectedHint', { models: c.detected_models })"
                    @click="fetchChannelModels(c)">+{{ detectedCount(c) }}</button>
                  <span v-if="c.suggest_disable_reason" class="ml-1 badge-danger cursor-help"
                    :title="t('admin.chSuggestDisableHint', { reason: c.suggest_disable_reason })">⚠ {{ t('admin.chSuggestDisable') }}</span>
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
                  <div v-if="c.calls" class="text-xs">
                    <span :class="c.success_rate >= 0.95 ? 'text-ok' : c.success_rate >= 0.8 ? 'text-amber-500' : 'text-danger'">
                      {{ (c.success_rate * 100).toFixed(1) }}%
                    </span>
                    <span class="text-muted"> · {{ Math.round(c.avg_latency_ms) }}ms</span>
                    <span v-if="c.errors" class="text-danger"> · {{ c.errors }}✕</span>
                  </div>
                  <span v-else class="text-xs text-muted">—</span>
                </td>
                <td class="px-4 py-2.5">
                  <span :class="c.status === 1 ? 'badge-ok' : c.status === 2 ? 'badge-danger' : 'badge-muted'">
                    {{ statusLabel(c.status) }}
                  </span>
                </td>
                <td class="px-4 py-2.5 text-right space-x-2">
                  <button class="btn-ghost !py-1 text-xs" :disabled="testingId === c.id" @click="testChannel(c)">
                    {{ testingId === c.id ? t('admin.chTesting') : t('admin.chTest') }}
                  </button>
                  <button class="btn-ghost !py-1 text-xs" :disabled="fetchingModelsId === c.id" @click="fetchChannelModels(c)">
                    {{ fetchingModelsId === c.id ? t('admin.chFetching') : t('admin.chFetchModels') }}
                  </button>
                  <button class="btn-ghost !py-1 text-xs" @click="openEdit(c)">{{ t('admin.chEdit') }}</button>
                  <button v-if="c.status === 1" class="btn-ghost !py-1 text-xs" @click="disableChannel(c.id)">
                    {{ t('admin.chDisable') }}
                  </button>
                  <button v-else class="btn-ghost !py-1 text-xs text-emerald-600" @click="enableChannel(c.id)">
                    {{ t('admin.chEnable') }}
                  </button>
                  <button class="btn-danger !py-1 text-xs" @click="deleteChannel(c.id)">
                    {{ t('admin.chDelete') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- 编辑渠道弹窗 -->
        <Teleport to="body">
        <div v-if="editing" class="modal-backdrop" @click.self="editing = null">
          <form class="app-panel modal-panel channel-edit-panel" @submit.prevent="saveEdit">
            <header class="channel-edit-header">
              <h2 class="font-semibold">{{ t('admin.chEdit') }} · {{ editing.name }}</h2>
            </header>
            <div class="channel-edit-body space-y-4">
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
            <div>
              <label class="label" for="edit-proxy">{{ t('admin.chProxy') }}</label>
              <input id="edit-proxy" v-model="editForm.proxy" class="input font-mono text-xs" placeholder="http://user:pass@host:port" />
              <p class="mt-1 text-xs text-muted">{{ t('admin.chProxyHint') }}</p>
            </div>
            <div>
              <label class="label" for="edit-param-override">{{ t('admin.chParamOverride') }}</label>
              <textarea
                id="edit-param-override"
                v-model="editForm.param_override"
                class="input font-mono text-xs"
                rows="3"
                :placeholder="'{&quot;service_tier&quot;:&quot;flex&quot;,&quot;parallel_tool_calls&quot;:null}'"
              ></textarea>
              <p class="mt-1 text-xs text-muted">{{ t('admin.chParamOverrideHint') }}</p>
            </div>
            <div>
              <label class="label" for="edit-header-override">{{ t('admin.chHeaderOverride') }}</label>
              <textarea
                id="edit-header-override"
                v-model="editForm.header_override"
                class="input font-mono text-xs"
                rows="3"
                :placeholder="'{&quot;X-Api-Key&quot;:&quot;{{credential}}&quot;,&quot;Authorization&quot;:null}'"
              ></textarea>
              <p class="mt-1 text-xs text-muted">{{ t('admin.chHeaderOverrideHint') }}</p>
            </div>
            <label class="flex items-center gap-2 text-sm">
              <input v-model="editForm.strict_response" type="checkbox" class="accent-accent" />
              {{ t('admin.chStrict') }}
            </label>
            <p class="text-xs text-muted">{{ t('admin.chStrictHint') }}</p>

            <!-- Advanced Custom（0036）：格式转换器 + 自定义端点 -->
            <div class="grid grid-cols-2 gap-3">
              <div>
                <label class="label" for="edit-converter">{{ t('admin.chConverter') }}</label>
                <select id="edit-converter" v-model="editForm.converter" class="input">
                  <option value="">{{ t('admin.chConverterDefault') }}</option>
                  <option value="none">none (OpenAI)</option>
                  <option value="openai_chat_to_anthropic_messages">→ Anthropic</option>
                  <option value="openai_chat_to_gemini_generate_content">→ Gemini</option>
                  <option value="openai_chat_to_openai_responses">→ OpenAI Responses</option>
                </select>
              </div>
              <div>
                <label class="label" for="edit-upath">{{ t('admin.chUpstreamPath') }}</label>
                <input id="edit-upath" v-model="editForm.upstream_path" class="input font-mono text-xs" placeholder="/chat/completions" />
              </div>
            </div>
            <p class="text-xs text-muted">{{ t('admin.chAdvancedHint') }}</p>

            <!-- 上游模型自动检测（0037 §3.10）：开启后后台周期 probe 上游 /models，新模型进徽标 -->
            <label class="flex items-center gap-2 text-sm">
              <input v-model="editForm.model_sync_enabled" type="checkbox" class="accent-accent" />
              {{ t('admin.chModelSync') }}
            </label>
            <p class="text-xs text-muted">{{ t('admin.chModelSyncHint') }}</p>

            <!-- 多 key 管理（0034 Multi-key）-->
            <div class="rounded border border-line p-3 space-y-2">
              <div class="flex items-center justify-between">
                <label class="label !mb-0">{{ t('admin.chKeys') }}</label>
                <select v-model="editForm.credential_mode" class="input !h-8 !w-32 !py-1 text-xs">
                  <option value="random">{{ t('admin.chModeRandom') }}</option>
                  <option value="polling">{{ t('admin.chModePolling') }}</option>
                  <option value="weighted">{{ t('admin.chModeWeighted') }}</option>
                </select>
              </div>
              <ul class="space-y-1">
                <li v-for="k in editKeys" :key="k.id" class="flex items-center gap-2 text-xs">
                  <span class="font-mono">{{ k.fingerprint }}…</span>
                  <span :class="k.status === 1 ? 'badge-ok' : 'badge-danger'">
                    {{ k.status === 1 ? t('admin.chKeyActive') : t('admin.chKeyDisabled') }}
                  </span>
                  <span class="flex-1"></span>
                  <button type="button" class="table-link" @click="toggleKey(k)">
                    {{ k.status === 1 ? t('admin.chKeyDisable') : t('admin.chKeyEnable') }}
                  </button>
                  <button type="button" class="table-link text-danger" @click="deleteKey(k)">{{ t('common.delete') }}</button>
                </li>
                <li v-if="editKeys.length === 0" class="text-xs text-muted">{{ t('admin.chKeysEmpty') }}</li>
              </ul>
              <div class="flex gap-2">
                <input v-model="newKey" class="input !h-8 flex-1 font-mono text-xs" :placeholder="t('admin.chKeyAddPlaceholder')" />
                <button type="button" class="btn-ghost !h-8 !py-1 !px-3" :disabled="keyBusy || !newKey.trim()" @click="addKey">{{ t('admin.chKeyAdd') }}</button>
              </div>
              <p class="text-xs text-muted">{{ t('admin.chKeysHint') }}</p>
            </div>

            </div>
            <footer class="channel-edit-footer">
              <p v-if="editError" class="inline-alert" role="alert">{{ editError }}</p>
              <div class="flex justify-end gap-2">
                <button type="button" class="btn-ghost" @click="editing = null">{{ t('common.cancel') }}</button>
                <button class="btn-primary" :disabled="editBusy">{{ t('common.confirm') }}</button>
              </div>
            </footer>
          </form>
        </div>
        </Teleport>

        <!-- 拉取上游模型结果（gap P2-D）：缺失/多余/未定价差集 -->
        <div v-if="fetchModelsResult" class="modal-backdrop" @click.self="fetchModelsResult = null">
          <div class="app-panel modal-panel w-full max-w-lg p-6 space-y-4">
            <h2 class="font-semibold">{{ t('admin.chFetchModels') }} · {{ fetchModelsResult.channel }}</h2>
            <p v-if="!fetchModelsResult.ok" class="text-sm text-danger">{{ fetchModelsResult.message }}</p>
            <template v-else>
              <p class="text-xs text-muted">
                {{ t('admin.fmDetected', { url: fetchModelsResult.detected_base_url, n: fetchModelsResult.upstream_count }) }}
              </p>
              <div>
                <p class="text-sm font-medium text-danger">{{ t('admin.fmMissing') }} ({{ fetchModelsResult.missing_in_config?.length ?? 0 }})</p>
                <p class="mt-1 break-words font-mono text-xs text-muted">{{ fetchModelsResult.missing_in_config?.join(', ') || '—' }}</p>
                <div v-if="fetchModelsResult.missing_in_config?.length" class="mt-2 flex gap-2">
                  <button class="btn-primary !py-1 text-xs" :disabled="modelActionBusy"
                    @click="channelModelAction('apply-models', fetchModelsResult!.missing_in_config!)">
                    {{ t('admin.fmApply') }}
                  </button>
                  <button class="btn-ghost !py-1 text-xs" :disabled="modelActionBusy"
                    @click="channelModelAction('ignore-models', fetchModelsResult!.missing_in_config!)">
                    {{ t('admin.fmIgnore') }}
                  </button>
                </div>
                <p class="mt-1 text-[11px] text-muted">{{ t('admin.fmApplyHint') }}</p>
              </div>
              <div>
                <p class="text-sm font-medium">{{ t('admin.fmUnpriced') }} ({{ fetchModelsResult.unpriced?.length ?? 0 }})</p>
                <p class="mt-1 break-words font-mono text-xs text-muted">{{ fetchModelsResult.unpriced?.join(', ') || '—' }}</p>
              </div>
              <div v-if="fetchModelsResult.extra_in_config?.length">
                <p class="text-sm font-medium text-muted">{{ t('admin.fmExtra') }} ({{ fetchModelsResult.extra_in_config.length }})</p>
                <p class="mt-1 break-words font-mono text-xs text-muted">{{ fetchModelsResult.extra_in_config.join(', ') }}</p>
                <button class="btn-ghost !py-1 mt-2 text-xs" :disabled="modelActionBusy"
                  @click="channelModelAction('mark-removed', fetchModelsResult!.extra_in_config!)">
                  {{ t('admin.fmMarkRemoved') }}
                </button>
              </div>
            </template>
            <div class="flex justify-end">
              <button class="btn-ghost" @click="fetchModelsResult = null">{{ t('common.close') }}</button>
            </div>
          </div>
        </div>
      </template>

      <!-- 支付渠道总览：渠道卡片 + 回调地址 + 路由预览 -->
      <template v-if="tab === 'payments'">
        <div class="space-y-6">
          <div class="flex items-center justify-between">
            <p class="text-sm text-muted">{{ t('admin.pcIntro') }}</p>
            <button class="btn-primary !py-1.5 text-sm" @click="openPayPicker">{{ t('admin.pcAdd') }}</button>
          </div>

          <!-- 渠道卡片 -->
          <div v-if="payChannels.length" class="grid gap-4 lg:grid-cols-2">
            <div v-for="ch in payChannels" :key="ch.id" class="app-panel p-5">
              <div class="flex items-start justify-between">
                <div class="flex items-center gap-2.5">
                  <span class="grid h-9 w-9 place-items-center rounded-lg text-sm font-bold text-white"
                    :style="{ background: protoColor(ch.protocol) }">{{ ch.protocol[0].toUpperCase() }}</span>
                  <div>
                    <p class="font-semibold leading-tight">{{ ch.display_name || ch.code }}</p>
                    <p class="font-mono text-xs text-muted">{{ ch.code }} · {{ ch.protocol }}</p>
                  </div>
                </div>
                <button class="rounded-full px-2.5 py-0.5 text-xs font-medium"
                  :class="ch.enabled ? 'bg-ok/15 text-ok' : 'bg-muted/15 text-muted'"
                  :title="t('admin.pcToggle')" @click="togglePayChannel(ch)">
                  {{ ch.enabled ? t('admin.payActive') : t('admin.payUnset') }}
                </button>
              </div>
              <div class="mt-4 space-y-2 text-sm">
                <div class="flex flex-wrap gap-1.5">
                  <span class="rounded bg-elev px-2 py-0.5 text-xs text-muted">{{ ch.currency }}</span>
                  <span v-for="m in ch.pay_methods.split(',').filter(Boolean)" :key="m" class="rounded bg-elev px-2 py-0.5 text-xs text-muted">{{ m }}</span>
                  <span class="rounded bg-elev px-2 py-0.5 text-xs text-muted">P{{ ch.priority }}</span>
                </div>
                <div class="flex flex-col gap-1">
                  <span class="text-xs text-muted">{{ t('admin.payCallback') }}</span>
                  <code class="break-all rounded bg-elev px-2 py-1 font-mono text-[11px]">{{ callbackBase }}/{{ ch.code }}</code>
                </div>
                <p v-if="ch.last_test_at" class="text-xs" :class="ch.last_test_ok ? 'text-ok' : 'text-danger'">
                  {{ ch.last_test_ok ? '✓' : '✕' }} {{ ch.last_test_msg }}
                </p>
              </div>
              <div class="mt-4 flex gap-2">
                <button class="btn-ghost !py-1 text-xs" @click="testPayChannel(ch)">{{ t('admin.pcTest') }}</button>
                <button class="btn-ghost !py-1 text-xs" @click="editPayChannel(ch)">{{ t('admin.chEdit') }}</button>
                <button class="btn-danger !py-1 text-xs" @click="deletePayChannel(ch)">{{ t('admin.chDelete') }}</button>
              </div>
            </div>
          </div>
          <p v-else-if="payLoaded" class="app-panel p-6 text-center text-sm text-muted">{{ t('admin.pcEmpty') }}</p>

          <!-- 支付路由预览 -->
          <div v-if="payChannels.length" class="app-panel p-5">
            <h3 class="mb-1 font-semibold">{{ t('admin.payRouteTitle') }}</h3>
            <p class="mb-4 text-xs text-muted">{{ t('admin.payRouteHint') }}</p>
            <table class="table w-full text-sm">
              <thead>
                <tr class="text-left text-muted">
                  <th class="py-2">{{ t('admin.payMethod') }}</th>
                  <th class="py-2">{{ t('admin.payEngine') }}</th>
                  <th class="py-2">{{ t('admin.payCurrency') }}</th>
                  <th class="py-2">{{ t('admin.payStatus') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in payRoutes" :key="r.method">
                  <td class="py-2 font-mono text-xs">{{ r.method }}</td>
                  <td class="py-2 text-muted">{{ r.channel_name || r.channel_code || '—' }}</td>
                  <td class="py-2 font-mono text-xs text-muted">{{ r.currency || '—' }}</td>
                  <td class="py-2">
                    <span class="inline-flex items-center gap-1 text-xs" :class="r.channel_code ? 'text-ok' : 'text-muted'">
                      <span class="h-1.5 w-1.5 rounded-full" :class="r.channel_code ? 'bg-ok' : 'bg-muted'"></span>
                      {{ r.channel_code ? t('admin.payActive') : t('admin.payUnset') }}
                    </span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- 第一步：协议选择网格（卡片）-->
          <div v-if="pcPicker" class="modal-backdrop" @click.self="pcPicker = false">
            <div class="app-panel modal-panel w-full max-w-2xl p-6 space-y-4">
              <div class="flex items-center justify-between">
                <h2 class="font-semibold">{{ t('admin.pcPickProtocol') }}</h2>
                <button type="button" class="btn-ghost !p-1.5 text-xs" @click="pcPicker = false">✕</button>
              </div>
              <div class="grid gap-3 sm:grid-cols-3">
                <button
                  v-for="p in PROTOCOL_OPTIONS"
                  :key="p.value"
                  type="button"
                  class="app-panel flex flex-col items-start gap-2 p-4 text-left transition hover:border-accent hover:shadow-sm"
                  @click="pickProtocol(p.value)"
                >
                  <span class="grid h-9 w-9 place-items-center rounded-lg text-sm font-bold text-white" :style="{ background: p.color }">{{ p.label[0] }}</span>
                  <span class="font-medium">{{ protoOptLabel(p) }}</span>
                  <span class="text-xs text-muted">{{ t('admin.pcProto_' + p.value) }}</span>
                </button>
              </div>
            </div>
          </div>

          <!-- 第二步：渠道编辑器（按协议动态渲染的 rich form）-->
          <div v-if="pcEdit" class="modal-backdrop" @click.self="pcEdit = null">
            <form class="app-panel modal-panel w-full max-w-lg max-h-[88vh] overflow-y-auto p-6 space-y-4" @submit.prevent="savePayChannel">
              <div class="flex items-center gap-2.5">
                <span class="grid h-9 w-9 place-items-center rounded-lg text-sm font-bold text-white" :style="{ background: protoColor(pcEdit.protocol) }">{{ pcEdit.protocol[0].toUpperCase() }}</span>
                <h2 class="font-semibold">{{ pcEdit.id ? t('admin.chEdit') : t('admin.pcAdd') }} · {{ pcProtocolLabel }}</h2>
              </div>
              <div class="grid gap-3 sm:grid-cols-2">
                <div>
                  <label class="label">{{ t('admin.pcCode') }}<span class="text-danger"> *</span></label>
                  <input v-model="pcEdit.code" class="input font-mono text-xs" :disabled="!!pcEdit.id" required placeholder="epay_main" />
                  <p class="mt-1 text-xs text-muted">{{ t('admin.pcCodeHint') }}</p>
                </div>
                <div>
                  <label class="label">{{ t('admin.pcDisplayName') }}</label>
                  <input v-model="pcEdit.display_name" class="input" />
                </div>
                <div>
                  <label class="label">{{ t('admin.pcPriority') }}</label>
                  <input v-model.number="pcEdit.priority" type="number" class="input" />
                  <p class="mt-1 text-xs text-muted">{{ t('admin.pcPriorityHint') }}</p>
                </div>
                <div>
                  <label class="label">{{ t('admin.payCurrency') }}</label>
                  <select v-model="pcEdit.currency" class="input cursor-pointer">
                    <option v-for="c in CURRENCY_OPTIONS" :key="c" :value="c">{{ c }}</option>
                  </select>
                </div>
              </div>
              <!-- 支付方式（多选）-->
              <div>
                <label class="label">{{ t('admin.pcPayMethods') }}</label>
                <div class="flex flex-wrap gap-x-4 gap-y-2">
                  <label v-for="pm in PAY_METHOD_OPTIONS" :key="pm.value" class="flex items-center gap-1.5 text-sm">
                    <input type="checkbox" class="accent-accent" :checked="hasPayMethod(pm.value)"
                      @change="togglePayMethod(pm.value, ($event.target as HTMLInputElement).checked)" />
                    {{ payMethodLabel(pm) }}
                  </label>
                </div>
              </div>
              <!-- 协议专属密钥/配置：按 PROTOCOL_FIELDS 动态渲染（密钥字段编辑留空=保留原配置）-->
              <div class="border-t border-line pt-3">
                <p class="mb-2 text-xs font-medium text-muted">{{ t('admin.pcKeyConfig') }}</p>
                <div class="grid gap-3 sm:grid-cols-2">
                  <div v-for="f in pcFields" :key="f.key" :class="{ 'sm:col-span-2': f.wide || f.textarea }">
                    <label v-if="f.bool" class="flex items-center gap-2 text-sm">
                      <input type="checkbox" class="accent-accent" :checked="pcEdit.config[f.key] === 'true'"
                        @change="pcEdit.config[f.key] = ($event.target as HTMLInputElement).checked ? 'true' : 'false'" />
                      {{ fieldLabel(f) }}
                    </label>
                    <template v-else>
                      <label class="label">{{ fieldLabel(f) }}<span v-if="f.required" class="text-danger"> *</span></label>
                      <textarea v-if="f.textarea" v-model="pcEdit.config[f.key]" class="input font-mono text-xs" rows="2" :placeholder="fieldPlaceholder(f)"></textarea>
                      <input v-else v-model="pcEdit.config[f.key]" class="input font-mono text-xs"
                        :type="f.secret ? 'password' : 'text'" autocomplete="off"
                        :placeholder="pcEdit.id && f.secret ? t('admin.pcKeep') : fieldPlaceholder(f)" />
                    </template>
                  </div>
                </div>
              </div>
              <p v-if="pcEdit.protocol === 'crypto'" class="text-xs text-amber-600 dark:text-amber-400">{{ t('admin.pcCryptoHint') }}</p>
              <p v-if="pcEdit.protocol === 'webhook' || pcEdit.protocol === 'custom'" class="text-xs text-amber-600 dark:text-amber-400">{{ t('admin.pcVerifyHint') }}</p>
              <label class="flex items-center gap-2 text-sm">
                <input type="checkbox" class="accent-accent" v-model="pcEdit.enabled" />
                {{ t('admin.pcEnabled') }}
              </label>
              <div class="flex justify-end gap-2">
                <button type="button" class="btn-ghost" @click="pcEdit = null">{{ t('common.cancel') }}</button>
                <button class="btn-primary" :disabled="pcBusy">{{ t('settings.save') }}</button>
              </div>
            </form>
          </div>

          <!-- 全局结算设置（汇率 / 最低充值 / 邀请赠额 / 充值阶梯）—— 从「设置」迁来，统一在支付页 (#2) -->
          <div class="app-panel p-5">
            <h3 class="mb-1 font-semibold">{{ t('admin.payGlobalsTitle') }}</h3>
            <p class="mb-4 text-xs text-muted">{{ t('admin.payGlobalsHint') }}</p>
            <div class="grid gap-3 sm:grid-cols-3">
              <div>
                <label class="label" for="pg-cny">{{ t('admin.setCnyRate') }}</label>
                <input id="pg-cny" v-model="settings.cny_per_usd" class="input" placeholder="7.3" />
              </div>
              <div>
                <label class="label" for="pg-min">{{ t('admin.setMinTopup') }}</label>
                <input id="pg-min" v-model="settings.min_topup_usd" class="input" placeholder="1" />
              </div>
              <div>
                <label class="label" for="pg-invite">{{ t('admin.setInviteGrant') }}</label>
                <input id="pg-invite" v-model="settings.invite_grant_usd" class="input" placeholder="0" />
                <p class="mt-1 text-xs text-muted">{{ t('admin.setInviteGrantHint') }}</p>
              </div>
              <div class="sm:col-span-3">
                <label class="label" for="pg-tiers">{{ t('admin.setBonusTiers') }}</label>
                <input id="pg-tiers" v-model="settings.recharge_bonus_tiers" class="input font-mono text-xs" placeholder='[{"min_usd":"50","rate":"0.05"},{"min_usd":"200","rate":"0.1"}]' />
                <p class="mt-1 text-xs text-muted">{{ t('admin.setBonusTiersHint') }}</p>
              </div>
            </div>
            <div class="mt-4 flex items-center gap-3">
              <button class="btn-primary !py-1.5 text-sm" :disabled="payGlobalsBusy" @click="savePayGlobals">{{ t('settings.save') }}</button>
              <span v-if="payGlobalsMsg" class="text-sm text-ok">{{ payGlobalsMsg }}</span>
            </div>
          </div>
        </div>
      </template>

      <!-- 定价 -->
      <template v-if="tab === 'prices'">
        <AdminPricingPanel ref="pricingPanel" :refresh-key="pricingCatalogVersion" @edit-metadata="pickModelForMeta" />

        <!-- 模型元数据（gap P2-D）：厂商/分类/标签等展示元数据，前端据此替代按名猜 -->
        <DetailDrawer :open="metadataOpen" :title="t('admin.metaTitle')" @update:open="metadataOpen = $event"><form class="grid gap-4 sm:grid-cols-2" @submit.prevent="upsertMeta">
          <div class="sm:col-span-2 xl:col-span-4 -mb-1">
            <h2 class="font-semibold">{{ t('admin.metaTitle') }}</h2>
            <p class="mt-0.5 text-xs text-muted">{{ t('admin.metaHint') }}</p>
          </div>
          <div>
            <label class="label" for="meta-model">{{ t('admin.prModel') }}</label>
            <input id="meta-model" v-model="metaForm.model" class="input font-mono text-xs" required />
          </div>
          <div>
            <label class="label" for="meta-vendor">{{ t('admin.metaVendor') }}</label>
            <input id="meta-vendor" v-model="metaForm.vendor" class="input" maxlength="64" placeholder="OpenAI" />
          </div>
          <div>
            <label class="label" for="meta-cat">{{ t('admin.metaCategory') }}</label>
            <input id="meta-cat" v-model="metaForm.category" class="input" maxlength="32" placeholder="chat" />
          </div>
          <div>
            <label class="label" for="meta-ctx">{{ t('admin.metaContext') }}</label>
            <input id="meta-ctx" v-model="metaForm.context_length" class="input" inputmode="numeric" placeholder="128000" />
          </div>
          <div>
            <label class="label" for="meta-tags">{{ t('admin.metaTags') }}</label>
            <input id="meta-tags" v-model="metaForm.tags" class="input" maxlength="255" placeholder="vision,tools" />
          </div>
          <div>
            <label class="label" for="meta-icon">{{ t('admin.metaIcon') }}</label>
            <input id="meta-icon" v-model="metaForm.icon" class="input" maxlength="128" placeholder="openai" />
          </div>
          <div class="sm:col-span-2">
            <label class="label" for="meta-desc">{{ t('admin.metaDescription') }}</label>
            <input id="meta-desc" v-model="metaForm.description" class="input" maxlength="500" />
          </div>
          <div class="sm:col-span-2 xl:col-span-4">
            <button class="btn-primary" :disabled="metaBusy">{{ t('admin.metaSave') }}</button>
          </div>
        </form></DetailDrawer>

      </template>

      <!-- 分组倍率 -->
      <!-- 分组倍率（减法：并入定价 tab；用 isSuperadmin 守住原超管权限边界，非超管不可见/不可改）-->
      <template v-if="tab === 'prices' && auth.isSuperadmin">
        <h3 class="mt-2 font-semibold">{{ t('admin.grpSectionTitle') }}</h3>
        <form class="app-panel grid gap-4 p-5 sm:grid-cols-3 sm:items-end" @submit.prevent="upsertGroupRatio">
          <div>
            <label class="label" for="gr-name">{{ t('admin.grName') }}</label>
            <input id="gr-name" v-model="gr.name" class="input font-mono text-xs" placeholder="vip" required />
          </div>
          <div>
            <label class="label" for="gr-ratio">{{ t('admin.grRatio') }}</label>
            <input id="gr-ratio" v-model="gr.ratio" class="input" placeholder="1" required />
          </div>
          <div>
            <button class="btn-primary w-full" type="submit">{{ t('admin.prUpsert') }}</button>
          </div>
          <p class="text-xs text-muted sm:col-span-3">{{ t('admin.grHint') }}</p>
        </form>

        <div class="data-table-wrap">
          <table class="table w-full text-sm">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">{{ t('admin.grName') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.grRatio') }}</th>
                <th class="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="g in groupRatios" :key="g.name" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 font-mono">{{ g.name }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ g.ratio }}×</td>
                <td class="px-4 py-2.5 text-right">
                  <button v-if="g.name !== 'default'" class="btn-danger !py-1 text-xs" @click="deleteGroupRatio(g.name)">
                    {{ t('keys.delete') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- 二层 (分组,模型) 倍率（gap P2-F）：叠加在分组倍率之上 -->
        <h3 class="mt-2 font-semibold">{{ t('admin.mgrTitle') }}</h3>
        <form class="app-panel grid gap-4 p-5 sm:grid-cols-2 xl:grid-cols-4 xl:items-end" @submit.prevent="upsertModelGroupRatio">
          <div>
            <label class="label" for="mgr-group">{{ t('admin.grName') }}</label>
            <input id="mgr-group" v-model="mgr.group_name" class="input font-mono text-xs" placeholder="vip" required />
          </div>
          <div>
            <label class="label" for="mgr-model">{{ t('admin.prModel') }}</label>
            <input id="mgr-model" v-model="mgr.model" class="input font-mono text-xs" placeholder="gpt-5.2" required />
          </div>
          <div>
            <label class="label" for="mgr-ratio">{{ t('admin.grRatio') }}</label>
            <input id="mgr-ratio" v-model="mgr.ratio" class="input" placeholder="1" required />
          </div>
          <div>
            <button class="btn-primary">{{ t('admin.prUpsert') }}</button>
          </div>
          <p class="text-xs text-muted sm:col-span-2 xl:col-span-4">{{ t('admin.mgrHint') }}</p>
        </form>
        <div v-if="modelGroupRatios.length" class="data-table-wrap">
          <table class="table w-full text-sm">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">{{ t('admin.grName') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.prModel') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.grRatio') }}</th>
                <th class="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="m in modelGroupRatios" :key="m.group_name + '/' + m.model" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 font-mono">{{ m.group_name }}</td>
                <td class="px-4 py-2.5 font-mono text-xs">{{ m.model }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ m.ratio }}×</td>
                <td class="px-4 py-2.5 text-right">
                  <button class="btn-danger !py-1 text-xs" @click="deleteModelGroupRatio(m)">{{ t('keys.delete') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>

      <!-- 授信 -->
      <!-- 授信入账弹窗（用户管理 / 组织列表共用） -->
      <div v-if="grantModal" class="modal-backdrop" @click.self="grantModal = null">
        <form class="app-panel modal-panel w-full max-w-sm p-6 space-y-4" @submit.prevent="submitGrant">
          <h2 class="font-semibold">{{ t('admin.crTitle') }}</h2>
          <p class="text-xs text-muted">{{ t('admin.crTarget') }}: <span class="font-mono">{{ grantModal.label }}</span> · org #{{ grantModal.orgId }}</p>
          <div>
            <label class="label">{{ t('admin.crMode') }}</label>
            <div class="flex gap-1">
              <button
                v-for="m in ['add', 'subtract', 'override']"
                :key="m"
                type="button"
                class="btn-ghost !py-1 flex-1 text-xs"
                :class="grantForm.mode === m ? '!bg-panel !text-ink' : 'text-muted'"
                @click="grantForm.mode = m"
              >{{ t(`admin.crMode_${m}`) }}</button>
            </div>
          </div>
          <div>
            <label class="label" for="cr-amount">{{ grantForm.mode === 'override' ? t('admin.crTargetBalance') : t('admin.crAmount') }}</label>
            <input id="cr-amount" v-model="grantForm.amount_usd" class="input" required placeholder="10.00" autofocus />
            <p v-if="grantForm.mode === 'override'" class="mt-1 text-xs text-muted">{{ t('admin.crOverrideHint') }}</p>
          </div>
          <div>
            <label class="label" for="cr-reason">{{ t('admin.crReason') }}</label>
            <input id="cr-reason" v-model="grantForm.reason" class="input" required :placeholder="t('admin.crReasonPh')" />
          </div>
          <div v-if="grantResult" class="text-sm space-y-1" role="status" aria-live="polite">
            <p class="text-ok">{{ t('admin.crBalance', { balance: grantResult.balance_after }) }}</p>
            <p v-if="grantResult.already_applied" class="text-muted">{{ t('admin.crReplayed') }}</p>
          </div>
          <div class="flex justify-end gap-2">
            <button type="button" class="btn-ghost" @click="grantModal = null">{{ grantResult ? t('common.close') : t('common.cancel') }}</button>
            <button class="btn-primary" :disabled="grantBusy">{{ t('admin.crGrant') }}</button>
          </div>
        </form>
      </div>

      <!-- 渠道健康看板（M2，只读可观测；减法：并入渠道 tab，渠道管理下方同页展示健康）-->
      <template v-if="tab === 'channels'">
        <h3 class="mt-4 font-semibold">{{ t('admin.healthSectionTitle') }}</h3>
        <p class="text-xs text-muted">{{ t('admin.healthHint') }}</p>
        <div class="data-table-wrap">
          <table class="table w-full text-sm whitespace-nowrap">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">{{ t('admin.hChannel') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.hProvider') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.hStatus') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.hSuccess') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.hCalls') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.hErrors1h') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.hLatency') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.hFrt') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.hLastActive') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="health.length === 0">
                <td colspan="9" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <tr v-for="h in health" :key="h.id" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5">{{ h.name }}</td>
                <td class="px-4 py-2.5 text-muted">{{ h.provider }}</td>
                <td class="px-4 py-2.5">
                  <span
                    class="rounded px-1.5 py-0.5 text-xs"
                    :class="h.status === 1 ? 'bg-ok/15 text-ok' : h.status === 2 ? 'bg-danger/15 text-danger' : 'bg-elev text-muted'"
                  >{{ h.status === 1 ? t('admin.hActive') : h.status === 2 ? t('admin.hAutoDisabled') : t('admin.hDisabled') }}</span>
                </td>
                <td
                  class="px-4 py-2.5 text-right font-mono"
                  :class="h.calls === 0 ? 'text-muted' : h.success_rate >= 0.99 ? 'text-ok' : h.success_rate >= 0.9 ? 'text-ink' : 'text-danger'"
                >{{ h.calls === 0 ? '—' : `${(h.success_rate * 100).toFixed(1)}%` }}</td>
                <td class="px-4 py-2.5 text-right font-mono text-muted">{{ h.calls }}</td>
                <td class="px-4 py-2.5 text-right font-mono" :class="h.errors_1h > 0 ? 'text-danger' : 'text-muted'">{{ h.errors_1h }}</td>
                <td class="px-4 py-2.5 text-right font-mono text-muted">{{ h.calls === 0 ? '—' : `${Math.round(h.avg_latency_ms)}ms` }}</td>
                <td class="px-4 py-2.5 text-right font-mono text-muted">{{ h.calls === 0 ? '—' : `${Math.round(h.avg_frt_ms)}ms` }}</td>
                <td class="px-4 py-2.5 text-xs text-muted">{{ h.last_active ? h.last_active.slice(0, 16).replace('T', ' ') : '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- (model×channel) 交叉维度（近 24h；路由策略 latency/throughput 的数据底座同口径） -->
        <div class="mt-6">
          <p class="text-sm font-medium">{{ t('admin.mcTitle') }}</p>
          <p class="mt-1 text-xs text-muted">{{ t('admin.mcHint') }}</p>
        </div>
        <div class="data-table-wrap">
          <table class="table w-full text-sm whitespace-nowrap">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">{{ t('admin.mcModel') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.hChannel') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.hCalls') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.hSuccess') }}</th>
                <th class="px-4 py-3 font-medium text-right">p50</th>
                <th class="px-4 py-3 font-medium text-right">p95</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.hFrt') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="mcStats.length === 0">
                <td colspan="7" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <tr v-for="row in mcStats" :key="`${row.model}:${row.channel_id}`" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 font-mono text-xs">{{ row.model }}</td>
                <td class="px-4 py-2.5">{{ row.channel_name }}</td>
                <td class="px-4 py-2.5 text-right font-mono text-muted">{{ row.calls }}</td>
                <td
                  class="px-4 py-2.5 text-right font-mono"
                  :class="row.success_rate >= 0.99 ? 'text-ok' : row.success_rate >= 0.9 ? 'text-ink' : 'text-danger'"
                >{{ (row.success_rate * 100).toFixed(1) }}%</td>
                <td class="px-4 py-2.5 text-right font-mono text-muted">{{ row.p50 }}ms</td>
                <td class="px-4 py-2.5 text-right font-mono text-muted">{{ row.p95 }}ms</td>
                <td class="px-4 py-2.5 text-right font-mono text-muted">{{ row.avg_frt_ms }}ms</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>

      <!-- 组织 -->
      <template v-if="tab === 'orgs'">
        <div class="data-table-wrap">
          <table class="table w-full text-sm whitespace-nowrap">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">ID</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.orgName') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.orgOwner') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.orgGroup') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.orgMembers') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.orgBalance') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.orgHeld') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.orgSpend') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.orgCalls') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.orgLastActive') }}</th>
                <th class="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="orgs.length === 0">
                <td colspan="12" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <template v-for="o in orgs" :key="o.id">
                <tr class="border-b border-line" :class="entOpen === o.id ? '' : 'last:border-0'">
                  <td class="px-4 py-2.5 text-muted">{{ o.id }}</td>
                  <td class="px-4 py-2.5">{{ o.name }}</td>
                  <td class="px-4 py-2.5 font-mono text-xs">{{ o.owner_email }}</td>
                  <td class="px-4 py-2.5">
                    <select
                      v-if="auth.isSuperadmin"
                      :value="o.group"
                      class="input !w-28 !py-1 text-xs"
                      @change="setOrgGroup(o, ($event.target as HTMLSelectElement).value)"
                    >
                      <option v-for="g in groupRatios" :key="g.name" :value="g.name">{{ g.name }} ({{ g.ratio }}×)</option>
                    </select>
                    <span v-else class="text-xs text-muted">{{ o.group }}</span>
                  </td>
                  <td class="px-4 py-2.5 text-right">{{ o.members }}{{ o.max_members ? `/${o.max_members}` : '' }}</td>
                  <td class="px-4 py-2.5 text-right font-mono">${{ Number(o.balance).toFixed(2) }}</td>
                  <td class="px-4 py-2.5 text-right font-mono text-muted">${{ Number(o.held).toFixed(2) }}</td>
                  <td class="px-4 py-2.5 text-right font-mono">${{ Number(o.spend).toFixed(4) }}</td>
                  <td class="px-4 py-2.5 text-right font-mono text-muted">{{ o.calls }}</td>
                  <td class="px-4 py-2.5 text-xs text-muted">{{ o.last_active ? o.last_active.slice(0, 10) : '—' }}</td>
                  <td class="px-4 py-2.5 text-right whitespace-nowrap">
                    <template v-if="auth.isSuperadmin">
                      <button class="btn-ghost !py-1 text-xs" @click="toggleEnt(o)">{{ t('admin.entEdit') }}</button>
                      <button class="btn-ghost !py-1 text-xs" @click="grantTo(o)">{{ t('admin.orgGrant') }}</button>
                    </template>
                    <span v-else class="text-xs text-muted">—</span>
                  </td>
                </tr>
                <tr v-if="entOpen === o.id" class="border-b border-line bg-elev/40 last:border-0">
                  <td colspan="12" class="px-4 py-3">
                    <div class="flex flex-wrap items-end gap-5">
                      <p class="w-full text-xs text-muted">{{ t('admin.entHint') }}</p>
                      <label class="flex items-center gap-2 text-sm">
                        <input v-model="entForm.feature_images" type="checkbox" class="h-4 w-4" />
                        {{ t('admin.entImages') }}
                      </label>
                      <label class="flex items-center gap-2 text-sm">
                        <input v-model="entForm.feature_video" type="checkbox" class="h-4 w-4" />
                        {{ t('admin.entVideo') }}
                      </label>
                      <div>
                        <label class="label" :for="`ent-mem-${o.id}`">{{ t('admin.entMaxMembers') }}</label>
                        <input :id="`ent-mem-${o.id}`" v-model.number="entForm.max_members" type="number" min="0" class="input !w-28" />
                      </div>
                      <div>
                        <label class="label" :for="`ent-key-${o.id}`">{{ t('admin.entMaxKeys') }}</label>
                        <input :id="`ent-key-${o.id}`" v-model.number="entForm.max_api_keys" type="number" min="0" class="input !w-28" />
                      </div>
                      <div class="flex gap-2">
                        <button class="btn-primary !py-1.5 text-xs" @click="saveEnt(o)">{{ t('settings.save') }}</button>
                        <button class="btn-ghost !py-1.5 text-xs" @click="entOpen = null">{{ t('common.cancel') }}</button>
                      </div>
                      <!-- 规模化：账户分片数（巨客户写吞吐扩容；单独保存，走 /shards 端点）-->
                      <div class="flex w-full items-end gap-3 border-t border-line pt-3">
                        <div>
                          <label class="label" :for="`ent-shard-${o.id}`">{{ t('admin.entShardCount') }}</label>
                          <input :id="`ent-shard-${o.id}`" v-model.number="entForm.shard_count" type="number" min="1" max="64" class="input !w-28" />
                        </div>
                        <button class="btn-ghost !py-1.5 text-xs" @click="saveShards(o)">{{ t('admin.entShardSave') }}</button>
                        <p class="flex-1 text-xs text-muted">{{ t('admin.entShardHint') }}</p>
                      </div>
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
      </template>

      <!-- 日志（全局调用） -->
      <template v-if="tab === 'logs'">
        <form class="app-panel flex flex-wrap items-end gap-3 p-4" @submit.prevent="applyLogFilter">
          <div>
            <label class="label" for="lg-status">{{ t('usage.status') }}</label>
            <select id="lg-status" v-model="logFilter.status" class="input !w-36">
              <option value="">{{ t('admin.lgAny') }}</option>
              <option value="success">success</option>
              <option value="upstream_error">upstream_error</option>
              <option value="insufficient_balance">insufficient_balance</option>
              <option value="no_channel">no_channel</option>
            </select>
          </div>
          <div>
            <label class="label" for="lg-model">{{ t('usage.model') }}</label>
            <input id="lg-model" v-model="logFilter.model" class="input !w-40 font-mono text-xs" />
          </div>
          <div>
            <label class="label" for="lg-ch">{{ t('admin.lgChannel') }}</label>
            <input id="lg-ch" v-model="logFilter.channel_id" class="input !w-24" type="number" min="1" />
          </div>
          <div>
            <label class="label" for="lg-org">{{ t('admin.lgOrg') }}</label>
            <input id="lg-org" v-model="logFilter.org_id" class="input !w-24" type="number" min="1" />
          </div>
          <div>
            <label class="label" for="lg-since">{{ t('admin.lgSince') }}</label>
            <input id="lg-since" v-model="logFilter.since" class="input !w-40" type="date" />
          </div>
          <div>
            <label class="label" for="lg-until">{{ t('admin.lgUntil') }}</label>
            <input id="lg-until" v-model="logFilter.until" class="input !w-40" type="date" />
          </div>
          <button class="btn-primary">{{ t('admin.lgApply') }}</button>
        </form>

        <div class="data-table-wrap">
          <table class="table w-full text-sm whitespace-nowrap">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">{{ t('usage.time') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.lgOrg') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('usage.model') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.lgChannel') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('usage.status') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('usage.tokens') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('usage.cost') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="logs.length === 0">
                <td colspan="7" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <template v-for="l in logs" :key="l.id">
                <tr class="border-b border-line last:border-0">
                  <td class="px-4 py-2.5 text-muted">{{ d(new Date(l.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
                  <td class="px-4 py-2.5 font-mono text-xs">{{ l.org_id }}</td>
                  <td class="px-4 py-2.5 font-mono text-xs"><button type="button" class="table-link" @click="showLog(l)">{{ l.model }} · {{ t('reqlog.btnDetail') }}</button></td>
                  <td class="px-4 py-2.5 font-mono text-xs">#{{ l.channel_id }} {{ l.provider }}</td>
                  <td class="px-4 py-2.5">
                    <span :class="l.status === 'success' ? 'badge-ok' : 'badge-danger'">{{ l.status }}</span>
                  </td>
                  <td class="px-4 py-2.5 text-right font-mono">{{ l.prompt_tokens }} / {{ l.completion_tokens }}</td>
                  <td class="px-4 py-2.5 text-right font-mono">${{ Number(l.cost_usd).toFixed(4) }}</td>
                </tr>

              </template>
            </tbody>
          </table>
        </div>

        <div class="flex items-center justify-between text-sm text-muted">
          <span>{{ t('usage.total', { total: logTotal }) }}</span>
          <div class="flex items-center gap-2">
            <button class="btn-ghost !py-1" :disabled="logPage <= 1" @click="logPage--">{{ t('common.prev') }}</button>
            <span>{{ t('common.page', { page: logPage }) }}</span>
            <button class="btn-ghost !py-1" :disabled="logPage * LOG_PAGE_SIZE >= logTotal" @click="logPage++">{{ t('common.next') }}</button>
          </div>
        </div>
      </template>

      <!-- 用户 -->
      <template v-if="tab === 'users'">
        <form class="flex gap-2" @submit.prevent="loadUsers">
          <input v-model="userQuery" class="input max-w-xs" :placeholder="t('admin.userSearch')" />
          <button class="btn-ghost">{{ t('admin.lgApply') }}</button>
        </form>
        <div class="data-table-wrap">
          <table class="table w-full text-sm">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">ID</th>
                <th class="px-4 py-3 font-medium">{{ t('auth.email') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('keys.status') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.userRole') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.userBalance') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.orgGroup') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.userInvite') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.userRemark') }}</th>
                <th class="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="users.length === 0">
                <td colspan="9" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <tr v-for="u in users" :key="u.id" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 text-muted">{{ u.id }}</td>
                <td class="px-4 py-2.5">
                  <div class="font-mono text-xs">{{ u.email }}</div>
                  <div v-if="u.display_name" class="text-xs text-muted">{{ u.display_name }}</div>
                </td>
                <td class="px-4 py-2.5">
                  <span :class="u.status === 1 ? 'badge-ok' : 'badge-danger'">
                    {{ u.status === 1 ? t('admin.userActive') : t('admin.userBanned') }}
                  </span>
                </td>
                <td class="px-4 py-2.5">
                  <!-- 仅 superadmin 可改角色；其余只读徽章。后端再次校验 + 自我保护 -->
                  <select
                    v-if="auth.isSuperadmin"
                    :value="u.role"
                    class="input !w-32 !py-1 text-xs"
                    :title="t('admin.userRoleHint')"
                    @change="setUserRole(u, ($event.target as HTMLSelectElement).value)"
                  >
                    <option v-for="r in PLAT_ROLES" :key="r" :value="r">{{ t(`admin.role_${r}`) }}</option>
                  </select>
                  <span v-else class="badge-muted">{{ t(`admin.role_${u.role || 'user'}`) }}</span>
                </td>
                <td class="px-4 py-2.5 text-right font-mono">${{ Number(u.balance).toFixed(2) }}</td>
                <td class="px-4 py-2.5">
                  <select
                    v-if="auth.isSuperadmin"
                    :value="u.group"
                    class="input !w-28 !py-1 text-xs"
                    :disabled="!u.org_id"
                    :title="t('admin.userTierHint')"
                    @change="setUserGroup(u, ($event.target as HTMLSelectElement).value)"
                  >
                    <option v-for="g in groupRatios" :key="g.name" :value="g.name">{{ g.name }} ({{ g.ratio }}×)</option>
                  </select>
                  <span v-else class="text-xs text-muted">{{ u.group }}</span>
                </td>
                <td class="px-4 py-2.5 text-xs text-muted whitespace-nowrap">
                  <span :title="t('admin.userInvited')">Inv {{ u.invited }}</span>
                  <span v-if="u.inviter_org_id" class="ml-2" :title="t('admin.userInviter')">← #{{ u.inviter_org_id }}</span>
                </td>
                <td class="px-4 py-2.5">
                  <input
                    :value="u.admin_remark"
                    class="input !w-40 !py-1 text-xs"
                    maxlength="500"
                    :placeholder="t('admin.userRemarkPlaceholder')"
                    :title="t('admin.userRemarkHint')"
                    @change="saveUserRemark(u, ($event.target as HTMLInputElement).value)"
                  />
                </td>
                <td class="px-4 py-2.5 text-right space-x-2 whitespace-nowrap">
                  <button v-if="auth.isSuperadmin" class="btn-ghost !py-1 text-xs" :disabled="!u.org_id" @click="openGrant(u.org_id, u.email)">{{ t('admin.crAdd') }}</button>
                  <button v-if="u.status === 1" class="btn-danger !py-1 text-xs" @click="manageUser(u, 'ban')">{{ t('admin.userBan') }}</button>
                  <button v-else class="btn-ghost !py-1 text-xs" @click="manageUser(u, 'unban')">{{ t('admin.userUnban') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="text-xs text-muted">{{ t('admin.userTierHint') }}</p>

        <!-- 按用户聚合用量 + CSV 导出（gap P2-B） -->
        <div class="app-panel p-6 space-y-4">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <h3 class="font-semibold">{{ t('admin.usageByUser') }}</h3>
            <div class="flex items-center gap-2">
              <select v-model.number="usageDays" class="input !w-28 !py-1 text-xs" @change="loadUsageByUser">
                <option :value="7">7 {{ t('admin.usageDays') }}</option>
                <option :value="30">30 {{ t('admin.usageDays') }}</option>
                <option :value="90">90 {{ t('admin.usageDays') }}</option>
              </select>
              <button class="btn-ghost !py-1 text-xs" :disabled="usageLoading" @click="loadUsageByUser">{{ t('common.refresh') }}</button>
              <button class="btn-ghost !py-1 text-xs" @click="exportUsageByUser">{{ t('admin.exportCsv') }}</button>
            </div>
          </div>
          <div v-if="usageByUser.length" class="overflow-x-auto">
            <table class="table w-full text-sm whitespace-nowrap">
              <thead>
                <tr class="border-b border-line text-left text-muted">
                  <th class="px-4 py-3 font-medium">ID</th>
                  <th class="px-4 py-3 font-medium">{{ t('auth.email') }}</th>
                  <th class="px-4 py-3 font-medium text-right">{{ t('admin.usageCalls') }}</th>
                  <th class="px-4 py-3 font-medium text-right">{{ t('usage.tokens') }}</th>
                  <th class="px-4 py-3 font-medium text-right">{{ t('usage.cost') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in usageByUser" :key="r.user_id" class="border-b border-line last:border-0">
                  <td class="px-4 py-2.5 text-muted">{{ r.user_id || '—' }}</td>
                  <td class="px-4 py-2.5 font-mono text-xs">{{ r.email || t('admin.usageNoUser') }}</td>
                  <td class="px-4 py-2.5 text-right font-mono">{{ r.succeeded }}/{{ r.calls }}</td>
                  <td class="px-4 py-2.5 text-right font-mono">{{ (r.prompt_tokens + r.completion_tokens).toLocaleString() }}</td>
                  <td class="px-4 py-2.5 text-right font-mono text-ok">${{ Number(r.cost_usd).toFixed(4) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-else class="text-sm text-muted">{{ t('admin.usageEmpty') }}</p>
        </div>
      </template>

      <!-- 设置（分区子导航，避免一页过长） -->
      <template v-if="tab === 'settings'">
        <form class="app-panel max-w-3xl p-6 space-y-5" @submit.prevent="saveSettings">
          <div class="flex flex-wrap gap-1 border-b border-line pb-4">
            <button
              v-for="sec in SET_SECTIONS"
              :key="sec"
              type="button"
              class="btn-ghost !py-1.5 text-sm"
              :class="setSec === sec ? '!bg-panel !text-ink' : '!border-transparent text-muted'"
              @click="setSec = sec"
            >
              {{ t(`admin.setGrp_${sec}`) }}
            </button>
          </div>

          <div v-show="setSec === 'general'" class="space-y-5">
          <div class="grid gap-3 sm:grid-cols-2">
            <div>
              <label class="label" for="set-site">{{ t('admin.setSiteName') }}</label>
              <input id="set-site" v-model="settings.site_name" class="input" />
            </div>
            <div>
              <label class="label" for="set-support">{{ t('admin.setSupportEmail') }}</label>
              <input id="set-support" v-model="settings.support_email" class="input" type="email" placeholder="support@example.com" />
            </div>
          </div>
          <label class="flex items-center gap-2 text-sm">
            <input type="checkbox" class="accent-accent" :checked="settings.maintenance_mode === 'true'"
              @change="settings.maintenance_mode = ($event.target as HTMLInputElement).checked ? 'true' : 'false'" />
            {{ t('admin.setMaintenance') }}
          </label>
          <div>
            <label class="label" for="set-ann">{{ t('admin.setAnnouncement') }}</label>
            <input id="set-ann" v-model="settings.announcement" class="input" :placeholder="t('admin.setAnnouncementPh')" />
          </div>
          <div>
            <label class="label" for="set-footer">{{ t('admin.setFooter') }}</label>
            <input id="set-footer" v-model="settings.footer_text" class="input" :placeholder="t('admin.setFooterPh')" />
          </div>
          <div class="grid gap-3 sm:grid-cols-2">
            <div>
              <label class="label" for="set-tos">{{ t('admin.setToS') }}</label>
              <textarea id="set-tos" v-model="settings.terms_of_service" class="input" rows="3"></textarea>
            </div>
            <div>
              <label class="label" for="set-priv">{{ t('admin.setPrivacy') }}</label>
              <textarea id="set-priv" v-model="settings.privacy_policy" class="input" rows="3"></textarea>
            </div>
          </div>
          </div>

          <div v-show="setSec === 'signup'" class="space-y-3">
              <label class="flex items-center gap-2 text-sm">
                <input type="checkbox" class="accent-accent" :checked="settings.register_enabled === 'true'"
                  @change="settings.register_enabled = ($event.target as HTMLInputElement).checked ? 'true' : 'false'" />
                {{ t('admin.setRegister') }}
              </label>
              <label class="flex items-center gap-2 text-sm">
                <input type="checkbox" class="accent-accent" :checked="settings.email_verification_enabled === 'true'"
                  @change="settings.email_verification_enabled = ($event.target as HTMLInputElement).checked ? 'true' : 'false'" />
                {{ t('admin.setEmailVerify') }}
              </label>
              <div class="max-w-xs">
                <label class="label" for="set-grant">{{ t('admin.setWelcomeGrant') }}</label>
                <input id="set-grant" v-model="settings.new_user_grant_usd" class="input" placeholder="0" />
                <p class="mt-1 text-xs text-muted">{{ t('admin.setWelcomeGrantHint') }}</p>
              </div>
              <div class="border-t border-line pt-3">
                <label class="flex items-center gap-2 text-sm">
                  <input type="checkbox" class="accent-accent" :checked="settings.require_stepup_for_keys === 'true'"
                    @change="settings.require_stepup_for_keys = ($event.target as HTMLInputElement).checked ? 'true' : 'false'" />
                  {{ t('admin.setStepUpKeys') }}
                </label>
                <p class="mt-1 text-xs text-muted">{{ t('admin.setStepUpKeysHint') }}</p>
              </div>
              <!-- 邮箱准入策略（gap P2-E） -->
              <div class="border-t border-line pt-3 space-y-3">
                <div>
                  <label class="label">{{ t('admin.setEmailWhitelist') }}</label>
                  <div class="flex flex-wrap items-center gap-1.5 rounded-lg border border-line bg-elev/40 p-2">
                    <span v-for="d in csvTags(settings.email_domain_whitelist ?? '')" :key="d" class="inline-flex items-center gap-1 rounded bg-elev px-2 py-0.5 text-xs">
                      {{ d }}
                      <button type="button" class="text-muted hover:text-danger" @click="removeWhitelistDomain(d)">✕</button>
                    </span>
                    <input v-model="wlInput" class="min-w-[8rem] flex-1 bg-transparent px-1 text-sm outline-none" :placeholder="t('admin.setEmailWhitelistAdd')"
                      @keydown.enter.prevent="addWhitelistDomain" />
                  </div>
                  <p class="mt-1 text-xs text-muted">{{ t('admin.setEmailWhitelistHint') }}</p>
                </div>
                <div>
                  <label class="label" for="set-embl">{{ t('admin.setEmailBlocklist') }}</label>
                  <input id="set-embl" v-model="settings.email_domain_blocklist" class="input font-mono text-xs" placeholder="10minutemail.com" />
                  <p class="mt-1 text-xs text-muted">{{ t('admin.setEmailBlocklistHint') }}</p>
                </div>
                <label class="flex items-center gap-2 text-sm">
                  <input type="checkbox" class="accent-accent" :checked="settings.email_alias_block === 'true'"
                    @change="settings.email_alias_block = ($event.target as HTMLInputElement).checked ? 'true' : 'false'" />
                  {{ t('admin.setEmailAliasBlock') }}
                </label>
                <p class="-mt-1 text-xs text-muted">{{ t('admin.setEmailAliasBlockHint') }}</p>
              </div>
              <!-- SMTP 邮件发送（admin 后台热配，改了即生效，无需重启/改 env）-->
              <div class="border-t border-line pt-3 space-y-3">
                <div>
                  <p class="text-sm font-medium">{{ t('admin.setSmtpTitle') }}</p>
                  <p class="text-xs text-muted">{{ t('admin.setSmtpHint') }}</p>
                </div>
                <div class="grid gap-3 sm:grid-cols-2">
                  <div>
                    <label class="label" for="smtp-host">{{ t('admin.setSmtpHost') }}</label>
                    <input id="smtp-host" v-model="settings.smtp_host" class="input font-mono text-xs" placeholder="smtppro.zoho.com" />
                  </div>
                  <div>
                    <label class="label" for="smtp-port">{{ t('admin.setSmtpPort') }}</label>
                    <input id="smtp-port" v-model="settings.smtp_port" class="input" type="number" placeholder="465" />
                  </div>
                  <div>
                    <label class="label" for="smtp-user">{{ t('admin.setSmtpUser') }}</label>
                    <input id="smtp-user" v-model="settings.smtp_username" class="input font-mono text-xs" placeholder="info@laiai.ai" autocomplete="off" />
                  </div>
                  <div>
                    <label class="label" for="smtp-from">{{ t('admin.setSmtpFrom') }}</label>
                    <input id="smtp-from" v-model="settings.smtp_from" class="input font-mono text-xs" :placeholder="t('adminx.setSmtpFromPh')" />
                  </div>
                  <div>
                    <label class="label" for="smtp-pass">{{ t('admin.setSmtpPass') }}</label>
                    <input id="smtp-pass" v-model="settings.smtp_password" class="input font-mono text-xs" type="password" autocomplete="new-password" :placeholder="t('admin.setSmtpPassPh')" />
                  </div>
                  <div class="flex items-end pb-2">
                    <label class="flex items-center gap-2 text-sm">
                      <input type="checkbox" class="accent-accent" :checked="settings.smtp_ssl === 'true'"
                        @change="settings.smtp_ssl = ($event.target as HTMLInputElement).checked ? 'true' : 'false'" />
                      {{ t('admin.setSmtpSsl') }}
                    </label>
                  </div>
                </div>
              </div>
          </div>

          <div v-show="setSec === 'channels'">
            <label class="flex items-center gap-2 text-sm">
              <input type="checkbox" class="accent-accent" :checked="settings.channel_auto_disable === 'true'"
                @change="settings.channel_auto_disable = ($event.target as HTMLInputElement).checked ? 'true' : 'false'" />
              {{ t('admin.setAutoDisable') }}
            </label>
            <div class="mt-3 max-w-xs">
              <label class="label" for="set-thresh">{{ t('admin.setDisableThreshold') }}</label>
              <input id="set-thresh" v-model="settings.channel_disable_threshold" class="input" type="number" min="1" placeholder="5" />
            </div>
            <p class="mt-2 text-xs text-muted">{{ t('admin.setAutoDisableHint') }}</p>

            <div class="mt-5 border-t border-line pt-4">
              <label class="flex items-center gap-2 text-sm">
                <input type="checkbox" class="accent-accent" :checked="settings.model_rate_limit_enabled === 'true'"
                  @change="settings.model_rate_limit_enabled = ($event.target as HTMLInputElement).checked ? 'true' : 'false'" />
                {{ t('admin.setRateLimit') }}
              </label>
              <div class="mt-3 max-w-xs">
                <label class="label" for="set-rpm">{{ t('admin.setRateLimitRpm') }}</label>
                <input id="set-rpm" v-model="settings.model_rate_limit_rpm" class="input" type="number" min="0" placeholder="0" />
              </div>
              <p class="mt-2 text-xs text-muted">{{ t('admin.setRateLimitHint') }}</p>
            </div>
          </div>

          <div v-show="setSec === 'oauth'">
            <div class="grid gap-3 sm:grid-cols-2">
              <div>
                <label class="label" for="set-gh-id">GitHub Client ID</label>
                <input id="set-gh-id" v-model="settings.github_client_id" class="input font-mono text-xs" />
              </div>
              <div>
                <label class="label" for="set-gh-sec">GitHub Client Secret</label>
                <input id="set-gh-sec" v-model="settings.github_client_secret" class="input font-mono text-xs" type="password" autocomplete="off" />
              </div>
              <div>
                <label class="label" for="set-gg-id">Google Client ID</label>
                <input id="set-gg-id" v-model="settings.google_client_id" class="input font-mono text-xs" />
              </div>
              <div>
                <label class="label" for="set-gg-sec">Google Client Secret</label>
                <input id="set-gg-sec" v-model="settings.google_client_secret" class="input font-mono text-xs" type="password" autocomplete="off" />
              </div>
              <div>
                <label class="label" for="set-dc-id">Discord Client ID</label>
                <input id="set-dc-id" v-model="settings.discord_client_id" class="input font-mono text-xs" />
              </div>
              <div>
                <label class="label" for="set-dc-sec">Discord Client Secret</label>
                <input id="set-dc-sec" v-model="settings.discord_client_secret" class="input font-mono text-xs" type="password" autocomplete="off" />
              </div>
              <div>
                <label class="label" for="set-oidc-id">OIDC Client ID</label>
                <input id="set-oidc-id" v-model="settings.oidc_client_id" class="input font-mono text-xs" />
              </div>
              <div>
                <label class="label" for="set-oidc-sec">OIDC Client Secret</label>
                <input id="set-oidc-sec" v-model="settings.oidc_client_secret" class="input font-mono text-xs" type="password" autocomplete="off" />
              </div>
              <div>
                <label class="label" for="set-oidc-auth">OIDC Authorize URL</label>
                <input id="set-oidc-auth" v-model="settings.oidc_auth_url" class="input font-mono text-xs" placeholder="https://idp/authorize" />
              </div>
              <div>
                <label class="label" for="set-oidc-token">OIDC Token URL</label>
                <input id="set-oidc-token" v-model="settings.oidc_token_url" class="input font-mono text-xs" placeholder="https://idp/token" />
              </div>
              <div>
                <label class="label" for="set-oidc-userinfo">OIDC Userinfo URL</label>
                <input id="set-oidc-userinfo" v-model="settings.oidc_userinfo_url" class="input font-mono text-xs" placeholder="https://idp/userinfo" />
              </div>
              <div>
                <label class="label" for="set-oidc-scope">OIDC Scope</label>
                <input id="set-oidc-scope" v-model="settings.oidc_scope" class="input font-mono text-xs" placeholder="openid email" />
              </div>
              <div>
                <label class="label" for="set-tg-token">Telegram Bot Token</label>
                <input id="set-tg-token" v-model="settings.telegram_bot_token" class="input font-mono text-xs" type="password" autocomplete="off" />
              </div>
              <div>
                <label class="label" for="set-tg-user">Telegram Bot Username</label>
                <input id="set-tg-user" v-model="settings.telegram_bot_username" class="input font-mono text-xs" placeholder="mybot" />
              </div>
            </div>
            <p class="mt-2 text-xs text-muted">{{ t('admin.setOAuthHint') }}</p>
          </div>

          <!-- 日志保留 + 量级一览（gap P2-C） -->
          <div v-show="setSec === 'logs'" class="space-y-5">
            <div class="grid gap-3 sm:grid-cols-2">
              <div>
                <label class="label" for="set-logret">{{ t('admin.setLogRetention') }}</label>
                <input id="set-logret" v-model="settings.log_retention_days" class="input" inputmode="numeric" placeholder="30" />
                <p class="mt-1 text-xs text-muted">{{ t('admin.setLogRetentionHint') }}</p>
              </div>
              <div>
                <label class="label" for="set-auditret">{{ t('admin.setAuditRetention') }}</label>
                <input id="set-auditret" v-model="settings.audit_log_retention_days" class="input" inputmode="numeric" placeholder="0" />
                <p class="mt-1 text-xs text-muted">{{ t('admin.setAuditRetentionHint') }}</p>
              </div>
            </div>
            <div v-if="logStats" class="rounded-lg border border-line p-4 text-sm space-y-1.5">
              <div class="flex justify-between"><span class="text-muted">{{ t('admin.logStatInvocations') }}</span>
                <span class="font-mono">{{ logStats.invocations.total.toLocaleString() }}<span v-if="logStats.invocations.oldest_at" class="ml-2 text-muted">{{ t('admin.logStatOldest') }} {{ logStats.invocations.oldest_at }}</span></span>
              </div>
              <div class="flex justify-between"><span class="text-muted">{{ t('admin.logStatAudit') }}</span>
                <span class="font-mono">{{ logStats.audit_logs.total.toLocaleString() }}<span v-if="logStats.audit_logs.oldest_at_ms" class="ml-2 text-muted">{{ t('admin.logStatOldest') }} {{ new Date(logStats.audit_logs.oldest_at_ms).toLocaleDateString() }}</span></span>
              </div>
              <button type="button" class="btn-ghost !py-1 text-xs" @click="loadLogStats">{{ t('common.refresh') }}</button>
            </div>
          </div>

          <div class="flex items-center gap-3">
            <button class="btn-primary" :disabled="settingsBusy">{{ t('settings.save') }}</button>
            <span v-if="settingsMsg" class="text-sm text-ok">{{ settingsMsg }}</span>
          </div>
        </form>
      </template>

      <!-- 审计日志 -->
      <template v-if="tab === 'audit'">
        <div class="app-panel mb-4 flex items-start gap-3 p-4">
          <svg class="mt-0.5 h-5 w-5 shrink-0 text-accent" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M9 12.75 11.25 15 15 9.75M21 12c0 1.268-.63 2.39-1.593 3.068a3.745 3.745 0 0 1-1.043 3.296 3.745 3.745 0 0 1-3.296 1.043A3.745 3.745 0 0 1 12 21c-1.268 0-2.39-.63-3.068-1.593a3.746 3.746 0 0 1-3.296-1.043 3.745 3.745 0 0 1-1.043-3.296A3.745 3.745 0 0 1 3 12c0-1.268.63-2.39 1.593-3.068a3.745 3.745 0 0 1 1.043-3.296 3.746 3.746 0 0 1 3.296-1.043A3.746 3.746 0 0 1 12 3c1.268 0 2.39.63 3.068 1.593a3.746 3.746 0 0 1 3.296 1.043 3.746 3.746 0 0 1 1.043 3.296A3.745 3.745 0 0 1 21 12Z" />
          </svg>
          <div>
            <h2 class="font-semibold">{{ t('admin.auTitle') }}</h2>
            <p class="mt-0.5 text-xs text-muted">{{ t('admin.auWhat') }}</p>
          </div>
        </div>
        <div class="data-table-wrap">
          <table class="table w-full text-sm whitespace-nowrap">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">{{ t('usage.time') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.auActor') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.auAction') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.auTarget') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.auDetail') }}</th>
                <th class="px-4 py-3 font-medium">IP</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="audit.length === 0">
                <td colspan="6" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <tr v-for="a in audit" :key="a.id" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 text-muted">{{ d(new Date(a.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
                <td class="px-4 py-2.5 font-mono text-xs">{{ a.actor }}</td>
                <td class="px-4 py-2.5"><span :class="auditSensitive(a.action) ? 'badge-danger' : 'badge-muted'">{{ auditActionLabel(a.action) }}</span></td>
                <td class="px-4 py-2.5 font-mono text-xs text-muted">{{ a.target || '—' }}</td>
                <td class="px-4 py-2.5 text-muted"><button type="button" class="table-link" @click="selectedAudit = a">{{ t('reqlog.btnDetail') }}</button></td>
                <td class="px-4 py-2.5 font-mono text-xs text-muted">{{ a.ip }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
      <DetailDrawer :open="!!selectedLog && auth.isSuperadmin" :title="t('reqlog.drawerTitle', { id: selectedLog?.id ?? '' })" size="lg" @update:open="open => { if (!open) selectedLog = null }">
        <template v-if="selectedLog">
          <dl class="detail-grid mb-6"><div><dt>{{ t('admin.lgOrg') }}</dt><dd>{{ selectedLog.org_id }}</dd></div><div><dt>{{ t('usage.model') }}</dt><dd>{{ selectedLog.model }}</dd></div><div><dt>{{ t('usage.tokens') }}</dt><dd>{{ selectedLog.prompt_tokens }} / {{ selectedLog.completion_tokens }}</dd></div><div><dt>{{ t('usage.cost') }}</dt><dd>USD {{ selectedLog.cost_usd }}</dd></div><div><dt>{{ t('usage.status') }}</dt><dd>{{ selectedLog.status }}</dd></div><div><dt>{{ t('admin.lgChannel') }}</dt><dd>#{{ selectedLog.channel_id }} {{ selectedLog.provider }}</dd></div></dl>
                    <dl class="grid grid-cols-2 gap-x-8 gap-y-1.5 text-xs sm:grid-cols-4">
                      <div><dt class="text-muted">{{ t('usage.requestId') }}</dt><dd class="font-mono select-all">{{ selectedLog.request_id }}</dd></div>
                      <div><dt class="text-muted">{{ t('usage.dStatusCode') }}</dt><dd class="font-mono">{{ selectedLog.status_code }}</dd></div>
                      <div><dt class="text-muted">{{ t('admin.lgAttempts') }}</dt><dd class="font-mono">{{ selectedLog.attempts }}</dd></div>
                      <div><dt class="text-muted">{{ t('usage.dStream') }}</dt><dd class="font-mono">{{ selectedLog.is_stream ? t('common.yes') : t('common.no') }}</dd></div>
                      <div><dt class="text-muted">{{ t('admin.lgUser') }}</dt><dd class="font-mono">{{ selectedLog.user_id ?? '—' }}</dd></div>
                      <div><dt class="text-muted">{{ t('admin.lgApiKey') }}</dt><dd class="font-mono">#{{ selectedLog.api_key_id }}</dd></div>
                      <div><dt class="text-muted">{{ t('usage.dFrt') }}</dt><dd class="font-mono">{{ selectedLog.frt_ms }} ms</dd></div>
                      <div><dt class="text-muted">{{ t('usage.dDuration') }}</dt><dd class="font-mono">{{ selectedLog.duration_ms }} ms</dd></div>
                      <div v-if="selectedLog.usage_source"><dt class="text-muted">{{ t('usage.dUsageSource') }}</dt><dd class="font-mono">{{ selectedLog.usage_source }}</dd></div>
                      <div v-if="selectedLog.upstream_request_id"><dt class="text-muted">{{ t('admin.lgUpstreamId') }}</dt><dd class="font-mono select-all break-all">{{ selectedLog.upstream_request_id }}</dd></div>
                      <div v-if="selectedLog.payload_object_key" class="col-span-2"><dt class="text-muted">{{ t('admin.lgPayloadKey') }}</dt><dd class="font-mono select-all break-all">{{ selectedLog.payload_object_key }}</dd></div>
                      <div v-if="selectedLog.error_brief" class="col-span-2 sm:col-span-4">
                        <dt class="text-muted">{{ t('usage.dError') }}</dt>
                        <dd class="font-mono text-danger break-all">{{ selectedLog.error_brief }}</dd>
                      </div>
                    </dl>
        </template>
      </DetailDrawer>
      <DetailDrawer :open="!!selectedAudit" :title="t('admin.auDetail')" @update:open="open => { if (!open) selectedAudit = null }">
        <dl v-if="selectedAudit" class="detail-grid"><div><dt>ID</dt><dd>{{ selectedAudit.id }}</dd></div><div><dt>{{ t('usage.time') }}</dt><dd>{{ d(new Date(selectedAudit.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</dd></div><div><dt>{{ t('admin.auActor') }}</dt><dd>{{ selectedAudit.actor }}</dd></div><div><dt>{{ t('admin.auAction') }}</dt><dd>{{ auditActionLabel(selectedAudit.action) }}</dd></div><div><dt>{{ t('admin.auTarget') }}</dt><dd>{{ selectedAudit.target || '—' }}</dd></div><div><dt>IP</dt><dd>{{ selectedAudit.ip }}</dd></div><div class="col-span-2"><dt>{{ t('admin.auDetail') }}</dt><dd class="whitespace-pre-wrap">{{ selectedAudit.detail || '—' }}</dd></div></dl>
      </DetailDrawer>
    </template>
  </div>
</template>
