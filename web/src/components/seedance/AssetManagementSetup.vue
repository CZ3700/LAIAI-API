<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, ApiError } from '@/lib/api'
import { useAuth } from '@/stores/auth'

const props = defineProps<{ orgId: number }>()
const emit = defineEmits<{ 'busy-change': [busy: boolean]; bound: [] }>()
const auth = useAuth()
const capabilityOptions = [
  ['VirtualGroups', '创建虚拟素材组'], ['Images', '图片素材'], ['Videos', '视频素材'], ['Audio', '音频素材'],
  ['Portrait', '真人认证与素材组'], ['Sync', '同步官方素材'], ['Update', '修改素材信息'],
  ['DeleteAssets', '删除素材'], ['DeleteVirtualEmptyGroups', '删除虚拟空组'],
  ['ExclusiveProjectWrites', '独占项目写入（已确认没有其他系统写入该项目）'],
] as const
type Capability = typeof capabilityOptions[number][0]
type Capabilities = Record<Capability, boolean>
interface ProviderScope { id: number; account_ref: string; region: string; project_name: string; enabled: boolean }
interface ScopeConfig { provider_scope_id: number; version: number; enabled: boolean; credential_present: boolean; capabilities: Capabilities; capabilities_valid: boolean; identity_matches_provider: boolean }
interface OrgRoute { org_id: number; provider_scope_id: number; scope_version: number; enabled: boolean; default_route: boolean }
interface Setup {
  org_id: number; org_active: boolean; configuration_ready: boolean; official_access: 'not_checked'; provider_scopes: ProviderScope[]; scope_configs: ScopeConfig[]; org_routes: OrgRoute[]
  policy: { present: boolean; async_execution_allowed: boolean; capture_history: boolean; retention_days: number }
  deployment: { console_configured: boolean; media_configured: boolean; platform_callback_configured: boolean; portrait_return_configured: boolean; portrait_target_ready: boolean }
  issues: string[]
}
const emptyCapabilities = () => Object.fromEntries(capabilityOptions.map(([key]) => [key, false])) as Capabilities
const setup = ref<Setup | null>(null), loading = ref(false), busy = ref(false), error = ref(''), notice = ref('')
const scopeId = ref(0), selectedVersion = ref(0), version = ref(''), enabled = ref(false), capabilities = ref(emptyCapabilities())
const accessKey = ref(''), secretKey = ref(''), sessionToken = ref(''), registrationConfirmed = ref(false), bindingConfirmed = ref(false), refreshRequired = ref(false)
let generation = 0, operation = 0, installing = false
const allowed = computed(() => auth.isSuperadmin && !!auth.token && props.orgId > 0 && props.orgId === auth.orgId)
const provider = computed(() => setup.value?.provider_scopes.find(item => item.id === scopeId.value))
const versions = computed(() => setup.value?.scope_configs.filter(item => item.provider_scope_id === scopeId.value) ?? [])
const currentRoute = computed(() => setup.value?.org_routes.find(item => item.default_route && item.enabled))
const savedVersion = computed(() => versions.value.find(item => item.version === selectedVersion.value))
const versionNumber = computed(() => /^[1-9]\d*$/.test(version.value) && Number.isSafeInteger(Number(version.value)) ? Number(version.value) : 0)
const canRegister = computed(() => allowed.value && setup.value?.org_active && provider.value?.enabled && !loading.value && !busy.value && !refreshRequired.value && registrationConfirmed.value && versionNumber.value > 0 && !versions.value.some(item => item.version === versionNumber.value) && validSecret(accessKey.value, 8, 1024) && validSecret(secretKey.value, 8, 4096) && validSecret(sessionToken.value, 0, 8192))
const canBind = computed(() => allowed.value && setup.value?.org_active && provider.value?.enabled && savedVersion.value?.enabled && savedVersion.value.credential_present && savedVersion.value.capabilities_valid && savedVersion.value.identity_matches_provider && bindingConfirmed.value && !loading.value && !busy.value && !refreshRequired.value)
function validSecret(value: string, min: number, max: number) { return value.length >= min && value.length <= max && !/[\r\n\u0000]/.test(value) }
function clearSecrets() { accessKey.value = ''; secretKey.value = ''; sessionToken.value = '' }
function resetRegistration() { clearSecrets(); enabled.value = false; capabilities.value = emptyCapabilities(); registrationConfirmed.value = false; version.value = '' }
function reset() {
  ++generation; ++operation; clearSecrets(); resetRegistration(); setup.value = null; scopeId.value = 0; selectedVersion.value = 0
  bindingConfirmed.value = false; refreshRequired.value = false; error.value = ''; notice.value = ''; loading.value = false; busy.value = false
}
function safeError(e: unknown, fallback: string) {
  if (!(e instanceof ApiError)) return fallback
  if (e.status === 401 || e.status === 403) return '当前会话没有素材管理配置权限，请重新确认超级管理员身份。'
  if (e.status === 409) return '配置版本已存在或发生冲突。请刷新已保存版本后继续，不要重复登记。'
  if (e.status === 422) return '配置字段不符合要求，请检查版本、账号项目与所选能力。'
  return fallback
}
async function readSetup(org: number) {
  const data = await api<Setup>('GET', `/api/admin/video/asset-setup?org_id=${org}`)
  if (data.org_id !== org || !Array.isArray(data.provider_scopes) || !Array.isArray(data.scope_configs) || !Array.isArray(data.org_routes)) throw new Error('invalid setup projection')
  return data
}
function install(data: Setup, desiredScope = scopeId.value, desiredVersion = selectedVersion.value) {
  setup.value = data
  const preferred = data.provider_scopes.some(item => item.id === desiredScope) ? desiredScope : data.org_routes.find(item => item.default_route && item.enabled)?.provider_scope_id ?? (data.provider_scopes.length === 1 ? data.provider_scopes[0]!.id : 0)
  installing = true
  try { scopeId.value = preferred } finally { installing = false }
  selectedVersion.value = versions.value.some(item => item.version === desiredVersion) ? desiredVersion : 0
}
async function load() {
  if (!allowed.value || busy.value || loading.value) return
  const epoch = ++generation, org = props.orgId
  clearSecrets(); registrationConfirmed.value = false; bindingConfirmed.value = false; error.value = ''; loading.value = true
  try { const data = await readSetup(org); if (epoch !== generation || !allowed.value) return; install(data); refreshRequired.value = false }
  catch (e) { if (epoch === generation) { refreshRequired.value = true; error.value = safeError(e, '无法读取素材管理配置，请稍后刷新。') } }
  finally { if (epoch === generation) loading.value = false }
}
function snapshot(data: Setup, id: number) {
  return JSON.stringify({ org: data.org_id, active: data.org_active, provider: data.provider_scopes.find(item => item.id === id), configs: data.scope_configs.filter(item => item.provider_scope_id === id), routes: data.org_routes, policy: data.policy, deployment: data.deployment })
}
async function preflight(epoch: number, op: number, org: number, id: number, before: string) {
  const data = await readSetup(org)
  if (epoch !== generation || op !== operation || !allowed.value || scopeId.value !== id) return false
  if (snapshot(data, id) !== before) {
    install(data); clearSecrets(); registrationConfirmed.value = false; bindingConfirmed.value = false
    error.value = '配置或组织路由已变化，已刷新当前状态；请重新核对并确认后操作。'
    return false
  }
  return true
}
async function register() {
  if (!canRegister.value || !setup.value) return
  const epoch = generation, op = ++operation, org = props.orgId, id = scopeId.value, targetVersion = versionNumber.value, before = snapshot(setup.value, id)
  const current = () => epoch === generation && op === operation && allowed.value && scopeId.value === id
  const body = { provider_scope_id: id, version: targetVersion, enabled: enabled.value, capabilities: { ...capabilities.value }, access_key: accessKey.value, secret_key: secretKey.value, session_token: sessionToken.value }
  const unchanged = () => registrationConfirmed.value && targetVersion === versionNumber.value && enabled.value === body.enabled && JSON.stringify(capabilities.value) === JSON.stringify(body.capabilities) && accessKey.value === body.access_key && secretKey.value === body.secret_key && sessionToken.value === body.session_token
  busy.value = true; error.value = ''; notice.value = ''
  let sent = false
  try {
    if (!await preflight(epoch, op, org, id, before) || !current()) return
    if (!unchanged()) { error.value = '登记内容已变化，请重新核对并确认。'; return }
    sent = true
    try { await api('POST', '/api/admin/video/asset-scopes', body) }
    finally { body.access_key = ''; body.secret_key = ''; body.session_token = ''; if (current()) clearSecrets() }
    if (!current()) return
    registrationConfirmed.value = false; refreshRequired.value = true
    notice.value = `管理版本 ${targetVersion} 已保存，尚未绑定当前组织。保存不代表官方权限验证通过。`
    const data = await readSetup(org)
    if (!current()) return
    install(data, id, targetVersion); refreshRequired.value = false
  } catch (e) {
    if (current()) {
      refreshRequired.value = true
      error.value = safeError(e, sent ? '无法确认登记或刷新结果，请先刷新已保存版本；若该版本已存在，可直接选择并绑定，无需再次填写密钥。' : '提交前无法确认最新配置，请刷新后再操作。')
    }
  } finally { body.access_key = ''; body.secret_key = ''; body.session_token = ''; if (current()) { clearSecrets(); registrationConfirmed.value = false; busy.value = false } }
}
async function bind() {
  if (!canBind.value || !setup.value || !savedVersion.value) return
  const epoch = generation, op = ++operation, org = props.orgId, id = scopeId.value, targetVersion = selectedVersion.value, before = snapshot(setup.value, id)
  const current = () => epoch === generation && op === operation && allowed.value && scopeId.value === id
  busy.value = true; error.value = ''; notice.value = ''; clearSecrets()
  try {
    if (!await preflight(epoch, op, org, id, before) || !current() || selectedVersion.value !== targetVersion || !bindingConfirmed.value) return
    await api('PUT', '/api/admin/video/asset-org-routes', { org_id: org, provider_scope_id: id, scope_version: targetVersion, enabled: true, default_route: true })
    if (!current() || selectedVersion.value !== targetVersion) return
    bindingConfirmed.value = false; clearSecrets(); emit('bound')
  } catch (e) { if (current()) { refreshRequired.value = true; error.value = safeError(e, '无法确认组织绑定结果，请刷新当前路由后决定是否继续。已保存管理版本无需重复登记。') } }
  finally { if (current()) busy.value = false }
}
watch(busy, value => emit('busy-change', value), { flush: 'sync' })
watch(scopeId, () => { if (!installing) { ++generation; ++operation; busy.value = false }; resetRegistration(); selectedVersion.value = 0; bindingConfirmed.value = false; error.value = ''; notice.value = '' }, { flush: 'sync' })
watch(selectedVersion, () => { bindingConfirmed.value = false; clearSecrets() }, { flush: 'sync' })
watch([version, enabled, capabilities], () => { registrationConfirmed.value = false }, { deep: true, flush: 'sync' })
watch(() => [props.orgId, auth.orgId, auth.token, auth.isSuperadmin], () => { reset(); if (allowed.value) void load() }, { flush: 'sync' })
onMounted(load)
onBeforeUnmount(reset)
</script>

<template>
  <div class="asset-setup space-y-5">
    <p class="text-sm text-muted">素材管理使用火山 OpenAPI AK/SK，与视频生成渠道的 API Key 分开配置。这里只选择已有的火山账号项目，不创建账号作用域或修改预算。</p>
    <p v-if="!allowed" class="inline-alert" role="alert">需要当前组织的有效个人超级管理员会话。</p>
    <p v-if="error" class="inline-alert" role="alert">{{ error }}</p><p v-if="notice" class="inline-success" role="status">{{ notice }}</p>
    <div class="flex items-center justify-between gap-3"><strong class="text-sm">当前组织 #{{ orgId }}</strong><button type="button" class="btn-ghost" :disabled="loading || busy || !allowed" @click="load">{{ loading ? '读取中…' : '刷新配置状态' }}</button></div>
    <template v-if="setup">
      <section class="rounded border border-line p-4 text-sm space-y-2"><p>当前默认路由：{{ currentRoute ? `账号项目 #${currentRoute.provider_scope_id} · 管理版本 ${currentRoute.scope_version}` : '尚未绑定' }}</p><p>本地配置：{{ setup.configuration_ready ? '已就绪' : '仍有未完成项' }}；官方权限：尚未实际验证。</p><p v-if="!setup.org_active" class="text-danger">当前组织已停用，不能保存或绑定。</p><p v-if="!setup.policy.present || !setup.policy.async_execution_allowed" class="text-warn">当前组织尚未获得异步执行许可，绑定后仍需管理员完成执行策略配置。</p><p v-if="!setup.deployment.console_configured" class="text-warn">服务端尚未配置火山素材管理接口，请由部署管理员补齐。</p><p v-if="!setup.deployment.media_configured" class="text-warn">媒体存储与核验服务尚未就绪，上传功能仍不可用。</p><p v-if="!setup.deployment.portrait_target_ready" class="text-muted">真人认证返回地址尚未就绪，真人功能还需完成服务端配置。</p></section>
      <label class="block text-sm">已有火山账号项目<select v-model.number="scopeId" class="input mt-1" :disabled="busy || loading"><option :value="0">请选择账号项目</option><option v-for="item in setup.provider_scopes" :key="item.id" :value="item.id" :disabled="!item.enabled">{{ item.account_ref }} · {{ item.region }} · {{ item.project_name }}{{ item.enabled ? '' : '（已停用）' }}</option></select></label>
      <p v-if="!setup.provider_scopes.length" class="text-sm text-muted">尚无已登记的火山账号项目，请先完成视频渠道的账号项目绑定。</p>
      <template v-if="provider">
        <section class="rounded border border-line p-4 space-y-4"><h3 class="font-medium">1. 登记素材管理版本</h3><p class="text-xs text-muted">已有可用版本时可直接使用下方第 2 步。新版本不会覆盖旧版本，也不会自动绑定组织。</p>
          <fieldset :disabled="busy || loading || refreshRequired" class="space-y-4">
            <label class="block text-sm">新版本号<input v-model="version" class="input mt-1" inputmode="numeric" placeholder="例如 1" autocomplete="off"></label>
            <div class="grid gap-3 sm:grid-cols-2"><label class="block text-sm">Access Key<input v-model="accessKey" type="password" class="input mt-1" autocomplete="new-password" spellcheck="false" autocapitalize="off" maxlength="1024"></label><label class="block text-sm">Secret Key<input v-model="secretKey" type="password" class="input mt-1" autocomplete="new-password" spellcheck="false" autocapitalize="off" maxlength="4096"></label></div>
            <label class="block text-sm">Session Token（可选）<input v-model="sessionToken" type="password" class="input mt-1" autocomplete="new-password" spellcheck="false" autocapitalize="off" maxlength="8192"></label>
            <fieldset class="space-y-2"><legend class="text-sm font-medium mb-2">确认已在火山开通并授权的能力</legend><label v-for="[key, label] in capabilityOptions" :key="key" class="flex items-start gap-2 text-sm"><input v-model="capabilities[key]" type="checkbox" class="mt-1 shrink-0"><span>{{ label }}</span></label></fieldset>
            <label class="flex items-start gap-2 text-sm"><input v-model="enabled" type="checkbox" class="mt-1 shrink-0"><span>启用该管理版本，允许后续绑定组织</span></label>
            <label class="flex items-start gap-2 text-sm"><input v-model="registrationConfirmed" type="checkbox" class="mt-1 shrink-0"><span>已核对当前账号项目及所选能力，确认登记此版本；保存不代表官方权限验证成功。</span></label>
          </fieldset>
          <p v-if="versionNumber && versions.some(item => item.version === versionNumber)" class="text-xs text-warn">该版本已保存，请直接选择下方已有版本，或填写新的版本号。</p>
          <button type="button" class="btn-ghost" :disabled="!canRegister" @click="register">{{ busy ? '处理中…' : '保存管理版本' }}</button>
        </section>
        <section class="rounded border border-line p-4 space-y-4"><h3 class="font-medium">2. 绑定当前组织</h3><label class="block text-sm">已保存管理版本<select v-model.number="selectedVersion" class="input mt-1" :disabled="busy || loading || refreshRequired"><option :value="0">请选择已保存版本</option><option v-for="item in versions" :key="item.version" :value="item.version">版本 {{ item.version }}{{ item.enabled ? '' : '（已停用）' }}{{ item.credential_present ? '' : '（无管理凭据）' }}</option></select></label>
          <template v-if="savedVersion"><p class="text-sm">凭据：{{ savedVersion.credential_present ? '已保存（不回显）' : '缺失' }} · 版本：{{ savedVersion.enabled ? '已启用' : '已停用' }}</p><p class="text-xs text-muted">已登记能力：{{ capabilityOptions.filter(([key]) => savedVersion?.capabilities[key]).map(([, label]) => label).join('、') || '未启用任何能力' }}</p><p v-if="!savedVersion.capabilities_valid || !savedVersion.identity_matches_provider" class="text-sm text-danger">此版本的账号项目或能力数据不匹配，不能用于组织绑定。</p></template>
          <label class="flex items-start gap-2 text-sm"><input v-model="bindingConfirmed" type="checkbox" class="mt-1 shrink-0" :disabled="busy || loading || refreshRequired || !savedVersion"><span>确认将所选管理版本设为组织 #{{ orgId }} 的默认素材路由并启用，替换该组织原有的默认素材路由。</span></label>
          <button type="button" class="btn-primary" :disabled="!canBind" @click="bind">{{ busy ? '处理中…' : '绑定当前组织并启用' }}</button>
        </section>
      </template>
      <p v-if="refreshRequired" class="text-sm text-warn" role="status">请先刷新配置状态，再核对已保存版本与当前组织路由；不会自动重发任何写入。</p>
    </template>
  </div>
</template>
