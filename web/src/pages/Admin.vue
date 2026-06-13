<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminApi, adminToken, ApiError, copyText, errorMessage, uuid } from '@/lib/api'
import Sparkline from '@/components/Sparkline.vue'

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
}
interface GroupRatio {
  name: string
  ratio: string
}
interface Price {
  Model: string
  InputUSDPer1M: string
  OutputUSDPer1M: string
  ImageUSD: string
  CachedInputUSDPer1M: string
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

const { t, d } = useI18n()

// —— tabs ——
const tab = ref<'overview' | 'analytics' | 'channels' | 'payments' | 'prices' | 'groups' | 'orgs' | 'users' | 'logs' | 'settings' | 'audit'>('overview')

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
      affiliate_rate: settings.value.affiliate_rate ?? '',
    })
    payGlobalsMsg.value = t('settings.saved')
  } catch (e) {
    showError(e)
  } finally {
    payGlobalsBusy.value = false
  }
}
// 渠道编辑器（新建/编辑共用）。密钥字段编辑时留空 = 保留原配置。
interface PayChannelEdit {
  id: number; code: string; display_name: string; protocol: string
  pay_methods: string; currency: string; priority: number; enabled: boolean
  secret_key: string; webhook_secret: string; pid: string; key: string; api_url: string
}
const pcEdit = ref<PayChannelEdit | null>(null)
const pcBusy = ref(false)
function newPayChannel() {
  pcEdit.value = { id: 0, code: '', display_name: '', protocol: 'stripe', pay_methods: 'card', currency: 'USD', priority: 0, enabled: true, secret_key: '', webhook_secret: '', pid: '', key: '', api_url: '' }
}
function editPayChannel(ch: PayChannelRow) {
  pcEdit.value = { id: ch.id, code: ch.code, display_name: ch.display_name, protocol: ch.protocol, pay_methods: ch.pay_methods, currency: ch.currency, priority: ch.priority, enabled: ch.enabled === 1, secret_key: '', webhook_secret: '', pid: '', key: '', api_url: '' }
}
async function savePayChannel() {
  const e = pcEdit.value
  if (!e) return
  pcBusy.value = true
  try {
    const config: Record<string, string> = {}
    if (e.protocol === 'stripe') {
      if (e.secret_key) config.secret_key = e.secret_key
      if (e.webhook_secret) config.webhook_secret = e.webhook_secret
    } else {
      if (e.pid) config.pid = e.pid
      if (e.key) config.key = e.key
      if (e.api_url) config.api_url = e.api_url
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
const setSec = ref<'general' | 'signup' | 'channels' | 'oauth'>('general')
const SET_SECTIONS = ['general', 'signup', 'channels', 'oauth'] as const
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

// —— 用户管理 ——
interface AdminUser {
  id: number; email: string; display_name: string; status: number
  org_id: number; balance: string; group: string
  invited: number; earned: string; inviter_org_id: number
}
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

// —— 审计日志 ——
interface AuditRow { id: number; actor: string; action: string; target: string; detail: string; ip: string; created_at: number }
const audit = ref<AuditRow[]>([])
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
  if (v === 'analytics' && !analytics.value) void loadAnalytics().catch(showError)
  if (v === 'payments' && !payLoaded.value) void loadPayChannels().catch(showError)
  if (v === 'users' && users.value.length === 0) void loadUsers().catch(showError)
  if (v === 'audit' && audit.value.length === 0) void loadAudit().catch(showError)
})

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
async function setOrgGroup(o: OrgRow, group: string) {
  try {
    await adminApi('PUT', `/api/admin/orgs/${o.id}/group`, { group })
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
const expandedLog = ref<number | null>(null)
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
function toggleLog(id: number) {
  expandedLog.value = expandedLog.value === id ? null : id
}
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
const editForm = ref({ base_url: '', models: '', api_key: '', weight: 1, priority: 0, strict_response: true, mapping: '', proxy: '' })
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
    proxy: ch.proxy,
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
      proxy: editForm.value.proxy, // 空串 = 清空代理（直连）
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
// 从组织行打开授信弹窗
function grantTo(o: OrgRow) {
  openGrant(o.id, o.name || `#${o.id}`)
}

// —— 定价 ——
const prices = ref<Price[]>([])
const pr = ref({ model: '', input_usd_per_1m: '0', output_usd_per_1m: '0', image_usd: '', cached_input_usd_per_1m: '' })
const prBusy = ref(false)

// 模型目录：上游已上货模型 → 承载渠道 + 是否已定价（定价管理一眼看缺口）
interface CatalogChannel { id: number; name: string; provider: string; status: number }
interface CatalogModel { model: string; channels: CatalogChannel[]; priced: boolean; input_usd_per_1m?: string; output_usd_per_1m?: string }
const catalog = ref<{ models: CatalogModel[]; total: number; priced: number; unpriced: number } | null>(null)
const catalogQuery = ref('')
const catalogOnlyUnpriced = ref(false)
async function loadCatalog() {
  catalog.value = await adminApi<{ models: CatalogModel[]; total: number; priced: number; unpriced: number }>('GET', '/api/admin/model-catalog')
}
const catalogFiltered = computed(() => {
  const c = catalog.value
  if (!c) return []
  const q = catalogQuery.value.trim().toLowerCase()
  return c.models.filter((m) => {
    if (catalogOnlyUnpriced.value && m.priced) return false
    if (q && !m.model.toLowerCase().includes(q)) return false
    return true
  })
})
// 从目录挑一个模型 → 填入定价表单（已定价的带回现价，便于改价）
function pickModelForPricing(m: CatalogModel) {
  pr.value = {
    model: m.model,
    input_usd_per_1m: m.input_usd_per_1m ?? '0',
    output_usd_per_1m: m.output_usd_per_1m ?? '0',
    image_usd: '',
    cached_input_usd_per_1m: '',
  }
}

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
    if (pr.value.cached_input_usd_per_1m) body.cached_input_usd_per_1m = pr.value.cached_input_usd_per_1m
    await adminApi('POST', '/api/admin/model-prices', body)
    pr.value = { model: '', input_usd_per_1m: '0', output_usd_per_1m: '0', image_usd: '', cached_input_usd_per_1m: '' }
    await loadPrices()
    error.value = ''
  } catch (e) {
    showError(e)
  } finally {
    prBusy.value = false
  }
}

// —— 授信入账（加余额）——
// 统一弹窗，用户管理 / 组织列表共用。幂等键在「打开弹窗」时生成一次并固定，
// 重复点击/网络重试用同一 key → 永不重复入账（旧实现每次 grant() 新 key 是漏洞）。
const grantModal = ref<{ orgId: number; label: string; key: string } | null>(null)
const grantForm = ref({ amount_usd: '', reason: '' })
const grantBusy = ref(false)
const grantResult = ref<{ balance_after: string; already_applied: boolean } | null>(null)

function openGrant(orgId: number, label: string) {
  if (!orgId) {
    showError(new Error(t('admin.crNoOrg')))
    return
  }
  grantModal.value = { orgId, label, key: uuid() }
  grantForm.value = { amount_usd: '', reason: '' }
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
    await Promise.all([loadOverview(), loadChannels(), loadPrices(), loadCatalog(), loadOrgs(), loadGroupRatios()])
  } catch (e) {
    showError(e)
  }
}
onMounted(() => {
  if (unlocked.value) void loadAll()
})
</script>

<template>
  <div class="max-w-6xl space-y-6">
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
            v-for="tb in ['overview', 'analytics', 'channels', 'payments', 'prices', 'groups', 'orgs', 'users', 'logs', 'settings', 'audit'] as const"
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

      <!-- 运营分析：资金 / 用户 / 质量 颗粒度 -->
      <template v-if="tab === 'analytics'">
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
            <div class="card p-4">
              <p class="text-xs text-muted">{{ t('admin.anCashIn') }}</p>
              <p class="mt-1 text-2xl font-semibold font-mono text-ok">${{ Number(analytics.finance.cash_in).toFixed(2) }}</p>
              <Sparkline :data="cashSeries" :width="140" :height="34" class="mt-2" />
            </div>
            <div class="card p-4">
              <p class="text-xs text-muted">{{ t('admin.anConsumption') }}</p>
              <p class="mt-1 text-2xl font-semibold font-mono">${{ Number(analytics.finance.consumption).toFixed(4) }}</p>
              <Sparkline :data="costSeries" :width="140" :height="34" class="mt-2" />
            </div>
            <div class="card p-4">
              <p class="text-xs text-muted">{{ t('admin.anFloat') }}</p>
              <p class="mt-1 text-2xl font-semibold font-mono">${{ Number(analytics.finance.float).toFixed(2) }}</p>
              <p class="mt-1 text-xs text-muted">{{ t('admin.anFloatHint') }}</p>
            </div>
            <div class="card p-4">
              <p class="text-xs text-muted">{{ t('admin.anConversion') }}</p>
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
            <div class="card p-4">
              <p class="text-xs text-muted">{{ t('admin.anUsers') }}</p>
              <p class="mt-1 text-2xl font-semibold font-mono">{{ analytics.users.total_users }}</p>
              <p class="mt-1 text-xs text-ok">+{{ analytics.users.new_users }} {{ t('admin.anNew') }}</p>
            </div>
            <div class="card p-4">
              <p class="text-xs text-muted">{{ t('admin.anActiveOrgs') }}</p>
              <p class="mt-1 text-2xl font-semibold font-mono">
                {{ analytics.users.active_orgs }}<span class="text-base text-muted">/{{ analytics.users.total_orgs }}</span>
              </p>
              <p class="mt-1 text-xs text-muted">{{ t('admin.anActiveHint') }}</p>
            </div>
            <div class="card p-4">
              <p class="text-xs text-muted">{{ t('admin.anArpu') }}</p>
              <p class="mt-1 text-2xl font-semibold font-mono">${{ Number(analytics.users.arpu).toFixed(4) }}</p>
              <p class="mt-1 text-xs text-muted">{{ t('admin.anArpuHint') }}</p>
            </div>
            <div class="card p-4">
              <p class="text-xs text-muted">{{ t('admin.anErrorRate') }}</p>
              <p class="mt-1 text-2xl font-semibold font-mono" :class="errorRate > 0.05 ? 'text-danger' : ''">{{ (errorRate * 100).toFixed(2) }}%</p>
              <p class="mt-1 text-xs text-muted">
                {{ t('admin.anLatency') }} {{ Math.round(analytics.quality.avg_duration_ms) }}ms · FRT {{ Math.round(analytics.quality.avg_frt_ms) }}ms
              </p>
            </div>
          </div>

          <div class="grid gap-4 lg:grid-cols-2">
            <!-- Top 消费组织 -->
            <div class="card p-5">
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
              <div class="card p-5">
                <h2 class="mb-3 font-semibold">{{ t('admin.anByProvider') }}</h2>
                <div v-if="analytics.quality.by_provider.length === 0" class="text-sm text-muted">{{ t('common.empty') }}</div>
                <div v-else class="space-y-2">
                  <div v-for="p in analytics.quality.by_provider" :key="p.provider" class="flex items-center justify-between text-sm">
                    <span class="font-mono">{{ p.provider || '—' }}</span>
                    <span class="text-muted">{{ p.calls }} · ${{ Number(p.cost).toFixed(4) }}</span>
                  </div>
                </div>
              </div>
              <div class="card p-5">
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
          <div class="card p-5">
            <div class="mb-3 flex items-baseline justify-between">
              <h2 class="font-semibold">{{ t('admin.anCallTrend') }}</h2>
              <span class="text-xs text-muted">{{ t('admin.ovLastDays', { days: analytics.days }) }}</span>
            </div>
            <Sparkline :data="callsSeries" :width="640" :height="48" class="w-full" />
          </div>
        </div>
        <div v-else-if="analyticsErr" class="card p-6 text-sm">
          <p class="text-danger">{{ analyticsErr }}</p>
          <button class="btn-ghost mt-3 !py-1.5 text-sm" @click="loadAnalytics().catch(showError)">{{ t('common.retry') }}</button>
        </div>
        <p v-else class="text-sm text-muted">{{ t('common.loading') }}</p>
      </template>

      <!-- 渠道 -->
      <template v-if="tab === 'channels'">
        <div v-if="channelsErr" class="card mb-3 flex items-center gap-3 p-4 text-sm">
          <span class="text-danger">{{ channelsErr }}</span>
          <button class="btn-ghost !py-1 text-xs" @click="loadChannels().catch(showError)">{{ t('common.retry') }}</button>
        </div>
        <div class="flex items-center justify-between">
          <p class="text-sm text-muted">{{ t('admin.chIntro') }}</p>
          <button class="btn-ghost" @click="showProbe = true">{{ t('admin.probe') }}</button>
        </div>

        <!-- 探测弹窗 -->
        <div v-if="showProbe" class="modal-backdrop" @click.self="showProbe = false">
          <div class="card modal-panel w-full max-w-lg p-6 space-y-4">
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
            <label class="label" for="ch-proxy">{{ t('admin.chProxy') }}</label>
            <input id="ch-proxy" v-model="ch.proxy" class="input font-mono text-xs" placeholder="http://user:pass@host:port" />
            <p class="mt-1 text-xs text-muted">{{ t('admin.chProxyHint') }}</p>
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
            <div>
              <label class="label" for="edit-proxy">{{ t('admin.chProxy') }}</label>
              <input id="edit-proxy" v-model="editForm.proxy" class="input font-mono text-xs" placeholder="http://user:pass@host:port" />
              <p class="mt-1 text-xs text-muted">{{ t('admin.chProxyHint') }}</p>
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

      <!-- 支付渠道总览：渠道卡片 + 回调地址 + 路由预览 -->
      <template v-if="tab === 'payments'">
        <div class="space-y-6">
          <div class="flex items-center justify-between">
            <p class="text-sm text-muted">{{ t('admin.pcIntro') }}</p>
            <button class="btn-primary !py-1.5 text-sm" @click="newPayChannel">{{ t('admin.pcAdd') }}</button>
          </div>

          <!-- 渠道卡片 -->
          <div v-if="payChannels.length" class="grid gap-4 lg:grid-cols-2">
            <div v-for="ch in payChannels" :key="ch.id" class="card p-5">
              <div class="flex items-start justify-between">
                <div class="flex items-center gap-2.5">
                  <span class="grid h-9 w-9 place-items-center rounded-lg text-sm font-bold text-white"
                    :style="{ background: ch.protocol === 'stripe' ? '#635bff' : '#06f' }">{{ ch.protocol[0].toUpperCase() }}</span>
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
                <button class="btn-danger !py-1 text-xs" @click="deletePayChannel(ch)">{{ t('admin.chDisable') }}</button>
              </div>
            </div>
          </div>
          <p v-else-if="payLoaded" class="card p-6 text-center text-sm text-muted">{{ t('admin.pcEmpty') }}</p>

          <!-- 支付路由预览 -->
          <div v-if="payChannels.length" class="card p-5">
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

          <!-- 渠道编辑器 -->
          <div v-if="pcEdit" class="modal-backdrop" @click.self="pcEdit = null">
            <form class="card modal-panel w-full max-w-lg p-6 space-y-4" @submit.prevent="savePayChannel">
              <h2 class="font-semibold">{{ pcEdit.id ? t('admin.chEdit') : t('admin.pcAdd') }}</h2>
              <div class="grid gap-3 sm:grid-cols-2">
                <div>
                  <label class="label">{{ t('admin.pcCode') }}</label>
                  <input v-model="pcEdit.code" class="input font-mono text-xs" :disabled="!!pcEdit.id" required placeholder="stripe_main" />
                </div>
                <div>
                  <label class="label">{{ t('admin.pcProtocol') }}</label>
                  <select v-model="pcEdit.protocol" class="input">
                    <option value="stripe">Stripe</option>
                    <option value="epay">EPay</option>
                  </select>
                </div>
                <div>
                  <label class="label">{{ t('admin.pcDisplayName') }}</label>
                  <input v-model="pcEdit.display_name" class="input" />
                </div>
                <div>
                  <label class="label">{{ t('admin.pcPayMethods') }}</label>
                  <input v-model="pcEdit.pay_methods" class="input font-mono text-xs" placeholder="card,alipay,wechat" />
                </div>
                <div>
                  <label class="label">{{ t('admin.payCurrency') }}</label>
                  <input v-model="pcEdit.currency" class="input" placeholder="USD" />
                </div>
                <div>
                  <label class="label">{{ t('admin.pcPriority') }}</label>
                  <input v-model.number="pcEdit.priority" type="number" class="input" />
                </div>
              </div>
              <!-- 协议专属密钥（编辑留空=不改）-->
              <template v-if="pcEdit.protocol === 'stripe'">
                <div class="grid gap-3 sm:grid-cols-2">
                  <div>
                    <label class="label">Secret Key</label>
                    <input v-model="pcEdit.secret_key" class="input font-mono text-xs" type="password" autocomplete="off" :placeholder="pcEdit.id ? t('admin.pcKeep') : 'sk_live_…'" />
                  </div>
                  <div>
                    <label class="label">Webhook Secret</label>
                    <input v-model="pcEdit.webhook_secret" class="input font-mono text-xs" type="password" autocomplete="off" :placeholder="pcEdit.id ? t('admin.pcKeep') : 'whsec_…'" />
                  </div>
                </div>
              </template>
              <template v-else>
                <div class="grid gap-3 sm:grid-cols-2">
                  <div>
                    <label class="label">PID</label>
                    <input v-model="pcEdit.pid" class="input font-mono text-xs" :placeholder="pcEdit.id ? t('admin.pcKeep') : ''" />
                  </div>
                  <div>
                    <label class="label">Key</label>
                    <input v-model="pcEdit.key" class="input font-mono text-xs" type="password" autocomplete="off" :placeholder="pcEdit.id ? t('admin.pcKeep') : ''" />
                  </div>
                  <div class="sm:col-span-2">
                    <label class="label">API URL</label>
                    <input v-model="pcEdit.api_url" class="input font-mono text-xs" placeholder="https://pay.example.com" />
                  </div>
                </div>
              </template>
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

          <!-- 全局结算设置（汇率 / 最低充值 / 返佣）—— 从「设置」迁来，统一在支付页 (#2) -->
          <div class="card p-5">
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
                <label class="label" for="pg-aff">{{ t('admin.setAffRate') }}</label>
                <input id="pg-aff" v-model="settings.affiliate_rate" class="input" placeholder="0" />
              </div>
            </div>
            <p class="mt-2 text-xs text-muted">{{ t('admin.setAffHint') }}</p>
            <div class="mt-4 flex items-center gap-3">
              <button class="btn-primary !py-1.5 text-sm" :disabled="payGlobalsBusy" @click="savePayGlobals">{{ t('settings.save') }}</button>
              <span v-if="payGlobalsMsg" class="text-sm text-ok">{{ payGlobalsMsg }}</span>
            </div>
          </div>
        </div>
      </template>

      <!-- 定价 -->
      <template v-if="tab === 'prices'">
        <form class="card p-5 grid grid-cols-5 gap-4 items-end" @submit.prevent="upsertPrice">
          <div>
            <label class="label" for="pr-model">{{ t('admin.prModel') }}</label>
            <input id="pr-model" v-model="pr.model" class="input font-mono text-xs" required />
          </div>
          <div>
            <label class="label" for="pr-input">{{ t('admin.prInput') }}</label>
            <input id="pr-input" v-model="pr.input_usd_per_1m" class="input" required />
          </div>
          <div>
            <label class="label" for="pr-cached">{{ t('admin.prCached') }}</label>
            <input id="pr-cached" v-model="pr.cached_input_usd_per_1m" class="input" placeholder="0" />
          </div>
          <div>
            <label class="label" for="pr-output">{{ t('admin.prOutput') }}</label>
            <input id="pr-output" v-model="pr.output_usd_per_1m" class="input" required />
          </div>
          <div>
            <label class="label" for="pr-image">{{ t('admin.prImage') }}</label>
            <input id="pr-image" v-model="pr.image_usd" class="input" placeholder="0" />
          </div>
          <div class="col-span-5 flex items-center gap-3">
            <button class="btn-primary" :disabled="prBusy">{{ t('admin.prUpsert') }}</button>
            <span class="text-xs text-muted">{{ t('admin.prCachedHint') }}</span>
          </div>
        </form>

        <!-- 模型目录：上游已上货模型 + 所在渠道 + 是否漏配价 -->
        <div v-if="catalog" class="card p-5">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
            <div>
              <h2 class="font-semibold">{{ t('admin.catTitle') }}</h2>
              <p class="mt-0.5 text-xs text-muted">
                {{ t('admin.catSummary', { total: catalog.total, priced: catalog.priced }) }}
                <span v-if="catalog.unpriced" class="text-danger">· {{ t('admin.catUnpriced', { n: catalog.unpriced }) }}</span>
              </p>
            </div>
            <div class="flex items-center gap-2">
              <input v-model="catalogQuery" class="input !w-48 !py-1.5 text-sm" :placeholder="t('admin.catSearch')" />
              <label class="flex items-center gap-1.5 text-xs text-muted whitespace-nowrap">
                <input type="checkbox" v-model="catalogOnlyUnpriced" class="accent-accent" />
                {{ t('admin.catOnlyUnpriced') }}
              </label>
            </div>
          </div>
          <div class="max-h-96 overflow-y-auto rounded-xl border border-line">
            <table class="table w-full text-sm">
              <thead class="sticky top-0 bg-panel/80 backdrop-blur">
                <tr class="text-left text-muted">
                  <th class="px-3 py-2 font-medium">{{ t('admin.prModel') }}</th>
                  <th class="px-3 py-2 font-medium">{{ t('admin.catChannels') }}</th>
                  <th class="px-3 py-2 font-medium text-right">{{ t('admin.catPrice') }}</th>
                  <th class="px-3 py-2"></th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="catalogFiltered.length === 0">
                  <td colspan="4" class="px-3 py-6 text-center text-muted">{{ t('common.empty') }}</td>
                </tr>
                <tr v-for="m in catalogFiltered" :key="m.model" class="border-b border-line last:border-0">
                  <td class="px-3 py-2 font-mono text-xs">{{ m.model }}</td>
                  <td class="px-3 py-2">
                    <span v-for="ch in m.channels" :key="ch.id"
                      class="mr-1 inline-flex items-center gap-1 rounded bg-elev px-1.5 py-0.5 text-[11px]"
                      :title="`#${ch.id} · ${ch.provider}`">
                      <span class="h-1.5 w-1.5 rounded-full" :class="ch.status === 1 ? 'bg-ok' : 'bg-muted'"></span>
                      {{ ch.name }}
                    </span>
                  </td>
                  <td class="px-3 py-2 text-right font-mono text-xs">
                    <span v-if="m.priced">{{ m.input_usd_per_1m }} / {{ m.output_usd_per_1m }}</span>
                    <span v-else class="badge-danger">{{ t('admin.catNoPrice') }}</span>
                  </td>
                  <td class="px-3 py-2 text-right">
                    <button class="btn-ghost !py-1 text-xs" @click="pickModelForPricing(m)">
                      {{ m.priced ? t('admin.catEdit') : t('admin.catSetPrice') }}
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <div class="card overflow-hidden">
          <table class="table w-full text-sm">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">{{ t('admin.prModel') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.prInput') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.prCached') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.prOutput') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.prImage') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="prices.length === 0">
                <td colspan="5" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <tr v-for="p in prices" :key="p.Model" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 font-mono text-xs">{{ p.Model }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ p.InputUSDPer1M }}</td>
                <td class="px-4 py-2.5 text-right font-mono text-muted">{{ p.CachedInputUSDPer1M !== '0' ? p.CachedInputUSDPer1M : '—' }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ p.OutputUSDPer1M }}</td>
                <td class="px-4 py-2.5 text-right font-mono">{{ p.ImageUSD }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>

      <!-- 分组倍率 -->
      <template v-if="tab === 'groups'">
        <form class="card p-5 grid grid-cols-3 gap-4 items-end" @submit.prevent="upsertGroupRatio">
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
          <p class="col-span-3 text-xs text-muted">{{ t('admin.grHint') }}</p>
        </form>

        <div class="card overflow-hidden">
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
      </template>

      <!-- 授信 -->
      <!-- 授信入账弹窗（用户管理 / 组织列表共用） -->
      <div v-if="grantModal" class="modal-backdrop" @click.self="grantModal = null">
        <form class="card modal-panel w-full max-w-sm p-6 space-y-4" @submit.prevent="submitGrant">
          <h2 class="font-semibold">{{ t('admin.crTitle') }}</h2>
          <p class="text-xs text-muted">{{ t('admin.crTarget') }}: <span class="font-mono">{{ grantModal.label }}</span> · org #{{ grantModal.orgId }}</p>
          <div>
            <label class="label" for="cr-amount">{{ t('admin.crAmount') }}</label>
            <input id="cr-amount" v-model="grantForm.amount_usd" class="input" required placeholder="10.00" autofocus />
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

      <!-- 组织 -->
      <template v-if="tab === 'orgs'">
        <div class="card overflow-x-auto">
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
                <td colspan="11" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
              </tr>
              <tr v-for="o in orgs" :key="o.id" class="border-b border-line last:border-0">
                <td class="px-4 py-2.5 text-muted">{{ o.id }}</td>
                <td class="px-4 py-2.5">{{ o.name }}</td>
                <td class="px-4 py-2.5 font-mono text-xs">{{ o.owner_email }}</td>
                <td class="px-4 py-2.5">
                  <select
                    :value="o.group"
                    class="input !w-28 !py-1 text-xs"
                    @change="setOrgGroup(o, ($event.target as HTMLSelectElement).value)"
                  >
                    <option v-for="g in groupRatios" :key="g.name" :value="g.name">{{ g.name }} ({{ g.ratio }}×)</option>
                  </select>
                </td>
                <td class="px-4 py-2.5 text-right">{{ o.members }}</td>
                <td class="px-4 py-2.5 text-right font-mono">${{ o.balance }}</td>
                <td class="px-4 py-2.5 text-right font-mono text-muted">${{ o.held }}</td>
                <td class="px-4 py-2.5 text-right font-mono">${{ Number(o.spend).toFixed(4) }}</td>
                <td class="px-4 py-2.5 text-right font-mono text-muted">{{ o.calls }}</td>
                <td class="px-4 py-2.5 text-xs text-muted">{{ o.last_active ? o.last_active.slice(0, 10) : '—' }}</td>
                <td class="px-4 py-2.5 text-right">
                  <button class="btn-ghost !py-1 text-xs" @click="grantTo(o)">{{ t('admin.orgGrant') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>

      <!-- 日志（全局调用） -->
      <template v-if="tab === 'logs'">
        <form class="card flex flex-wrap items-end gap-3 p-4" @submit.prevent="applyLogFilter">
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

        <div class="card overflow-x-auto">
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
                <tr class="cursor-pointer border-b border-line last:border-0" @click="toggleLog(l.id)">
                  <td class="px-4 py-2.5 text-muted">{{ d(new Date(l.created_at), { dateStyle: 'short', timeStyle: 'medium' }) }}</td>
                  <td class="px-4 py-2.5 font-mono text-xs">{{ l.org_id }}</td>
                  <td class="px-4 py-2.5 font-mono text-xs">{{ l.model }}</td>
                  <td class="px-4 py-2.5 font-mono text-xs">#{{ l.channel_id }} {{ l.provider }}</td>
                  <td class="px-4 py-2.5">
                    <span :class="l.status === 'success' ? 'badge-ok' : 'badge-danger'">{{ l.status }}</span>
                  </td>
                  <td class="px-4 py-2.5 text-right font-mono">{{ l.prompt_tokens }} / {{ l.completion_tokens }}</td>
                  <td class="px-4 py-2.5 text-right font-mono">{{ l.cost_usd }}</td>
                </tr>
                <tr v-if="expandedLog === l.id" class="!border-0 bg-elev/40">
                  <td colspan="7" class="px-4 pb-4 pt-1">
                    <dl class="grid grid-cols-2 gap-x-8 gap-y-1.5 text-xs sm:grid-cols-4">
                      <div><dt class="text-muted">{{ t('usage.requestId') }}</dt><dd class="font-mono select-all">{{ l.request_id }}</dd></div>
                      <div><dt class="text-muted">{{ t('usage.dStatusCode') }}</dt><dd class="font-mono">{{ l.status_code }}</dd></div>
                      <div><dt class="text-muted">{{ t('admin.lgAttempts') }}</dt><dd class="font-mono">{{ l.attempts }}</dd></div>
                      <div><dt class="text-muted">{{ t('usage.dStream') }}</dt><dd class="font-mono">{{ l.is_stream ? t('common.yes') : t('common.no') }}</dd></div>
                      <div><dt class="text-muted">{{ t('admin.lgUser') }}</dt><dd class="font-mono">{{ l.user_id ?? '—' }}</dd></div>
                      <div><dt class="text-muted">{{ t('admin.lgApiKey') }}</dt><dd class="font-mono">#{{ l.api_key_id }}</dd></div>
                      <div><dt class="text-muted">{{ t('usage.dFrt') }}</dt><dd class="font-mono">{{ l.frt_ms }} ms</dd></div>
                      <div><dt class="text-muted">{{ t('usage.dDuration') }}</dt><dd class="font-mono">{{ l.duration_ms }} ms</dd></div>
                      <div v-if="l.usage_source"><dt class="text-muted">{{ t('usage.dUsageSource') }}</dt><dd class="font-mono">{{ l.usage_source }}</dd></div>
                      <div v-if="l.upstream_request_id"><dt class="text-muted">{{ t('admin.lgUpstreamId') }}</dt><dd class="font-mono select-all break-all">{{ l.upstream_request_id }}</dd></div>
                      <div v-if="l.payload_object_key" class="col-span-2"><dt class="text-muted">{{ t('admin.lgPayloadKey') }}</dt><dd class="font-mono select-all break-all">{{ l.payload_object_key }}</dd></div>
                      <div v-if="l.error_brief" class="col-span-2 sm:col-span-4">
                        <dt class="text-muted">{{ t('usage.dError') }}</dt>
                        <dd class="font-mono text-danger break-all">{{ l.error_brief }}</dd>
                      </div>
                    </dl>
                  </td>
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
        <div class="card overflow-x-auto">
          <table class="table w-full text-sm">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">ID</th>
                <th class="px-4 py-3 font-medium">{{ t('auth.email') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('keys.status') }}</th>
                <th class="px-4 py-3 font-medium text-right">{{ t('admin.userBalance') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.orgGroup') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.userInvite') }}</th>
                <th class="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="users.length === 0">
                <td colspan="7" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
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
                <td class="px-4 py-2.5 text-right font-mono">${{ Number(u.balance).toFixed(2) }}</td>
                <td class="px-4 py-2.5">
                  <select
                    :value="u.group"
                    class="input !w-28 !py-1 text-xs"
                    :disabled="!u.org_id"
                    :title="t('admin.userTierHint')"
                    @change="setUserGroup(u, ($event.target as HTMLSelectElement).value)"
                  >
                    <option v-for="g in groupRatios" :key="g.name" :value="g.name">{{ g.name }} ({{ g.ratio }}×)</option>
                  </select>
                </td>
                <td class="px-4 py-2.5 text-xs text-muted whitespace-nowrap">
                  <span :title="t('admin.userInvited')">👥 {{ u.invited }}</span>
                  <span class="ml-2 text-ok" :title="t('admin.userEarned')">${{ Number(u.earned).toFixed(2) }}</span>
                  <span v-if="u.inviter_org_id" class="ml-2" :title="t('admin.userInviter')">← #{{ u.inviter_org_id }}</span>
                </td>
                <td class="px-4 py-2.5 text-right space-x-2 whitespace-nowrap">
                  <button class="btn-ghost !py-1 text-xs" :disabled="!u.org_id" @click="openGrant(u.org_id, u.email)">{{ t('admin.crAdd') }}</button>
                  <button v-if="u.status === 1" class="btn-danger !py-1 text-xs" @click="manageUser(u, 'ban')">{{ t('admin.userBan') }}</button>
                  <button v-else class="btn-ghost !py-1 text-xs" @click="manageUser(u, 'unban')">{{ t('admin.userUnban') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="text-xs text-muted">{{ t('admin.userTierHint') }}</p>
      </template>

      <!-- 设置（分区子导航，避免一页过长） -->
      <template v-if="tab === 'settings'">
        <form class="card max-w-3xl p-6 space-y-5" @submit.prevent="saveSettings">
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
            </div>
            <p class="mt-2 text-xs text-muted">{{ t('admin.setOAuthHint') }}</p>
          </div>

          <div class="flex items-center gap-3">
            <button class="btn-primary" :disabled="settingsBusy">{{ t('settings.save') }}</button>
            <span v-if="settingsMsg" class="text-sm text-ok">{{ settingsMsg }}</span>
          </div>
        </form>
      </template>

      <!-- 审计日志 -->
      <template v-if="tab === 'audit'">
        <div class="card mb-4 flex items-start gap-3 p-4">
          <svg class="mt-0.5 h-5 w-5 shrink-0 text-accent" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M9 12.75 11.25 15 15 9.75M21 12c0 1.268-.63 2.39-1.593 3.068a3.745 3.745 0 0 1-1.043 3.296 3.745 3.745 0 0 1-3.296 1.043A3.745 3.745 0 0 1 12 21c-1.268 0-2.39-.63-3.068-1.593a3.746 3.746 0 0 1-3.296-1.043 3.745 3.745 0 0 1-1.043-3.296A3.745 3.745 0 0 1 3 12c0-1.268.63-2.39 1.593-3.068a3.745 3.745 0 0 1 1.043-3.296 3.746 3.746 0 0 1 3.296-1.043A3.746 3.746 0 0 1 12 3c1.268 0 2.39.63 3.068 1.593a3.746 3.746 0 0 1 3.296 1.043 3.746 3.746 0 0 1 1.043 3.296A3.745 3.745 0 0 1 21 12Z" />
          </svg>
          <div>
            <h2 class="font-semibold">{{ t('admin.auTitle') }}</h2>
            <p class="mt-0.5 text-xs text-muted">{{ t('admin.auWhat') }}</p>
          </div>
        </div>
        <div class="card overflow-x-auto">
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
                <td class="px-4 py-2.5 text-muted">{{ a.detail }}</td>
                <td class="px-4 py-2.5 font-mono text-xs text-muted">{{ a.ip }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </template>
  </div>
</template>
