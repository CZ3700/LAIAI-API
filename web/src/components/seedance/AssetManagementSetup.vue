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
const enableConfirmed = ref(false), projectName = ref('')
const pendingReadback = ref<{ kind: 'registration' | 'enable' | 'project'; scopeId: number; version?: number; project?: { account_ref: string; region: string; project_name: string } } | null>(null)
let generation = 0, operation = 0, installing = false
const allowed = computed(() => auth.isSuperadmin && !!auth.token && props.orgId > 0 && props.orgId === auth.orgId)
const provider = computed(() => setup.value?.provider_scopes.find(item => item.id === scopeId.value))
const versions = computed(() => setup.value?.scope_configs.filter(item => item.provider_scope_id === scopeId.value) ?? [])
const currentRoute = computed(() => setup.value?.org_routes.find(item => item.default_route && item.enabled))
const savedVersion = computed(() => versions.value.find(item => item.version === selectedVersion.value))
const versionNumber = computed(() => /^[1-9]\d*$/.test(version.value) && Number.isSafeInteger(Number(version.value)) ? Number(version.value) : 0)
const commonBlockers = computed(() => {
  const reasons: string[] = []
  if (!allowed.value) reasons.push('需要当前组织的有效超级管理员会话。')
  if (!setup.value) reasons.push('请先读取素材管理配置。')
  else if (!setup.value.org_active) reasons.push('当前组织已停用。')
  if (!provider.value) reasons.push('请选择火山账号项目。')
  else if (!provider.value.enabled) reasons.push('所选账号项目已停用。')
  if (loading.value) reasons.push('正在读取配置，请稍候。')
  if (busy.value) reasons.push('正在处理操作，请稍候。')
  if (refreshRequired.value) reasons.push('请先刷新并确认上次操作结果。')
  return reasons
})
const registerBlockers = computed(() => {
  const reasons = [...commonBlockers.value]
  if (!versionNumber.value) reasons.push('新版本号必填，请填写正整数。')
  else if (versions.value.some(item => item.version === versionNumber.value)) reasons.push('该版本号已使用，请选择已有版本或改用未占用的新版本号。')
  if (!accessKey.value) reasons.push('Access Key 必填。')
  else if (!validSecret(accessKey.value, 8, 1024)) reasons.push('Access Key 需要 8–1024 个字符，不能包含换行。')
  if (!secretKey.value) reasons.push('Secret Key 必填。')
  else if (!validSecret(secretKey.value, 8, 4096)) reasons.push('Secret Key 需要 8–4096 个字符，不能包含换行。')
  if (!validSecret(sessionToken.value, 0, 8192)) reasons.push('Session Token 不能包含换行，最多 8192 个字符。')
  if (!registrationConfirmed.value) reasons.push('请勾选“已核对当前账号项目及所选能力，确认登记此版本”。')
  return reasons
})
const savedVersionBlockers = computed(() => {
  const reasons: string[] = []
  if (!savedVersion.value) reasons.push(versions.value.length ? '请选择已保存的管理版本。' : '当前项目尚无管理版本，请先完成第 1 步保存。')
  else {
    if (!savedVersion.value.credential_present) reasons.push('所选版本缺少管理凭据。')
    if (!savedVersion.value.capabilities_valid) reasons.push('所选版本的能力数据无效。')
    if (!savedVersion.value.identity_matches_provider) reasons.push('所选版本与当前账号项目不匹配。')
  }
  return reasons
})
const bindBlockers = computed(() => {
  const reasons = [...commonBlockers.value, ...savedVersionBlockers.value]
  if (savedVersion.value && !savedVersion.value.enabled) reasons.push('所选管理版本已停用，请先明确启用该版本。')
  if (!bindingConfirmed.value) reasons.push('请勾选将所选版本绑定为当前组织默认素材路由的确认项。')
  return reasons
})
const canRegister = computed(() => registerBlockers.value.length === 0)
const canBind = computed(() => bindBlockers.value.length === 0)
const canEnable = computed(() => commonBlockers.value.length === 0 && savedVersionBlockers.value.length === 0 && savedVersion.value?.enabled === false && enableConfirmed.value)
const projectBlockers = computed(() => {
  const reasons = [...commonBlockers.value]
  if (!projectName.value) reasons.push('请填写已在火山创建的项目名称。')
  else if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(projectName.value)) reasons.push('项目名称需为 1–64 位英文字母、数字、下划线或短横线，且以字母或数字开头。')
  return reasons
})
const canAddProject = computed(() => projectBlockers.value.length === 0)
function suggestedVersion() {
  const occupied = new Set(versions.value.map(item => item.version))
  const highest = versions.value.reduce((max, item) => Number.isSafeInteger(item.version) ? Math.max(max, item.version) : max, 0)
  let candidate = highest < Number.MAX_SAFE_INTEGER ? highest + 1 : 1
  while (occupied.has(candidate)) candidate++
  return String(candidate)
}
function preferredVersion(desired = 0) {
  if (versions.value.some(item => item.version === desired)) return desired
  const usable = versions.value.filter(item => item.enabled && item.credential_present && item.capabilities_valid && item.identity_matches_provider)
  const route = setup.value?.org_routes.find(item => item.provider_scope_id === scopeId.value && item.enabled && usable.some(config => config.version === item.scope_version))
  return route?.scope_version ?? (usable.length === 1 ? usable[0]!.version : 0)
}
function validSecret(value: string, min: number, max: number) { return value.length >= min && value.length <= max && !/[\r\n\u0000]/.test(value) }
function clearSecrets() { accessKey.value = ''; secretKey.value = ''; sessionToken.value = '' }
function resetRegistration() { clearSecrets(); enabled.value = false; capabilities.value = emptyCapabilities(); registrationConfirmed.value = false; version.value = '' }
function reset() {
  ++generation; ++operation; clearSecrets(); resetRegistration(); setup.value = null; scopeId.value = 0; selectedVersion.value = 0
  bindingConfirmed.value = false; enableConfirmed.value = false; projectName.value = ''; pendingReadback.value = null; refreshRequired.value = false; error.value = ''; notice.value = ''; loading.value = false; busy.value = false
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
  selectedVersion.value = preferredVersion(desiredVersion)
  if (!version.value || versions.value.some(item => item.version === versionNumber.value)) version.value = suggestedVersion()
}
function confirmReadback(data: Setup) {
  const expected = pendingReadback.value
  if (!expected) return true
  const config = data.scope_configs.find(item => item.provider_scope_id === expected.scopeId && item.version === expected.version)
  const found = expected.kind === 'project' ? data.provider_scopes.some(item => item.id === expected.scopeId && item.account_ref === expected.project?.account_ref && item.region === expected.project?.region && item.project_name === expected.project?.project_name) : !!config && (expected.kind !== 'enable' || config.enabled)
  if (!found) {
    refreshRequired.value = true; notice.value = ''
    error.value = '服务端已受理，但刷新结果尚未确认所选项目或管理版本。请继续刷新核对，不要重复提交。'
    return false
  }
  pendingReadback.value = null
  install(data, expected.scopeId, expected.version ?? 0)
  return true
}
async function load() {
  if (!allowed.value || busy.value || loading.value) return
  const epoch = ++generation, org = props.orgId
  clearSecrets(); registrationConfirmed.value = false; bindingConfirmed.value = false; enableConfirmed.value = false; error.value = ''; loading.value = true
  try { const data = await readSetup(org); if (epoch !== generation || !allowed.value) return; install(data); if (confirmReadback(data)) refreshRequired.value = false }
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
    install(data); clearSecrets(); registrationConfirmed.value = false; bindingConfirmed.value = false; enableConfirmed.value = false
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
    pendingReadback.value = { kind: 'registration', scopeId: id, version: targetVersion }
    const data = await readSetup(org)
    if (!current()) return
    install(data, id, targetVersion)
    if (!confirmReadback(data)) return
    refreshRequired.value = false
    notice.value = `管理版本 ${targetVersion} 已保存，${savedVersion.value?.enabled ? '请在第 2 步确认绑定当前组织' : '当前已停用，请在第 2 步明确启用后再绑定'}。保存不代表官方权限验证通过。`
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
async function enableSavedVersion() {
  if (!canEnable.value || !setup.value) return
  const epoch = generation, op = ++operation, org = props.orgId, id = scopeId.value, targetVersion = selectedVersion.value, before = snapshot(setup.value, id)
  const current = () => epoch === generation && op === operation && allowed.value && scopeId.value === id
  busy.value = true; error.value = ''; notice.value = ''; clearSecrets(); registrationConfirmed.value = false
  try {
    if (!await preflight(epoch, op, org, id, before) || !current() || selectedVersion.value !== targetVersion || !enableConfirmed.value) return
    await api('PUT', `/api/admin/video/asset-scopes/${id}/${targetVersion}`, { enabled: true })
    if (!current()) return
    enableConfirmed.value = false; bindingConfirmed.value = false; refreshRequired.value = true
    pendingReadback.value = { kind: 'enable', scopeId: id, version: targetVersion }
    const data = await readSetup(org)
    if (!current()) return
    install(data, id, targetVersion)
    if (!confirmReadback(data)) return
    refreshRequired.value = false; notice.value = `管理版本 ${targetVersion} 已启用。当前组织尚未因此自动绑定，请核对后单独确认绑定。`
  } catch (e) { if (current()) { refreshRequired.value = true; error.value = safeError(e, '无法确认版本启用结果，请刷新该版本状态后继续；不会自动重发启用操作。') } }
  finally { if (current()) { enableConfirmed.value = false; clearSecrets(); busy.value = false } }
}
async function addProject() {
  if (!canAddProject.value || !setup.value || !provider.value) return
  const epoch = generation, op = ++operation, org = props.orgId, id = scopeId.value, name = projectName.value, account = provider.value.account_ref, region = provider.value.region, before = snapshot(setup.value, id)
  const current = () => epoch === generation && op === operation && allowed.value
  busy.value = true; error.value = ''; notice.value = ''; clearSecrets(); registrationConfirmed.value = false; bindingConfirmed.value = false; enableConfirmed.value = false
  try {
    if (!await preflight(epoch, op, org, id, before) || !current() || scopeId.value !== id || projectName.value !== name) return
    const result = await api<{ provider_scope: ProviderScope }>('POST', '/api/admin/video/asset-projects', { source_provider_scope_id: id, project_name: name })
    if (!current()) return
    refreshRequired.value = true
    const added = result.provider_scope
    if (!added || !Number.isSafeInteger(added.id) || added.id <= 0 || added.account_ref !== account || added.region !== region || added.project_name !== name) throw new Error('invalid project projection')
    pendingReadback.value = { kind: 'project', scopeId: added.id, project: { account_ref: account, region, project_name: name } }
    const data = await readSetup(org)
    if (!current()) return
    install(data)
    if (!confirmReadback(data)) return
    refreshRequired.value = false
    // Even a reused project requires a fresh registration draft and explicit route confirmation.
    resetRegistration(); version.value = suggestedVersion(); bindingConfirmed.value = false; enableConfirmed.value = false; projectName.value = ''
    notice.value = `项目 ${name} 已登记并选中${added.enabled ? '' : '（该项目已停用）'}。未在火山创建项目，未复制其他项目的管理凭据、能力或组织路由。`
  } catch (e) { if (current()) { refreshRequired.value = true; error.value = safeError(e, '无法确认项目登记或刷新结果，请先刷新项目列表；不会自动重发登记操作。') } }
  finally { if (current()) { clearSecrets(); busy.value = false } }
}
watch(busy, value => emit('busy-change', value), { flush: 'sync' })
watch(scopeId, () => { if (!installing) { ++generation; ++operation; busy.value = false; pendingReadback.value = null; refreshRequired.value = false }; resetRegistration(); version.value = provider.value ? suggestedVersion() : ''; selectedVersion.value = preferredVersion(); bindingConfirmed.value = false; enableConfirmed.value = false; projectName.value = ''; error.value = ''; notice.value = '' }, { flush: 'sync' })
watch(selectedVersion, () => { bindingConfirmed.value = false; enableConfirmed.value = false; clearSecrets() }, { flush: 'sync' })
watch([version, enabled, capabilities], () => { registrationConfirmed.value = false }, { deep: true, flush: 'sync' })
watch(() => [props.orgId, auth.orgId, auth.token, auth.isSuperadmin], () => { reset(); if (allowed.value) void load() }, { flush: 'sync' })
onMounted(load)
onBeforeUnmount(reset)
</script>

<template>
  <div class="asset-setup space-y-5">
    <p class="text-sm text-muted">素材管理使用火山 OpenAPI AK/SK，与视频生成渠道的 API Key 分开配置。这里只登记已在火山存在的账号项目，不会创建云项目或修改视频预算。</p>
    <p class="text-sm text-warn">关闭、刷新或切换账号项目会清空尚未保存的密钥。只填写 AK/SK 不会保存，请完成必填项并点击“保存管理版本”。</p>
    <p v-if="!allowed" class="inline-alert" role="alert">需要当前组织的有效个人超级管理员会话。</p>
    <p v-if="error" class="inline-alert" role="alert">{{ error }}</p><p v-if="notice" class="inline-success" role="status">{{ notice }}</p>
    <div class="flex items-center justify-between gap-3"><strong class="text-sm">当前组织 #{{ orgId }}</strong><button type="button" class="btn-ghost" :disabled="loading || busy || !allowed" @click="load">{{ loading ? '读取中…' : '刷新配置状态' }}</button></div>
    <template v-if="setup">
      <section class="rounded border border-line p-4 text-sm space-y-2"><p>当前默认路由：{{ currentRoute ? `账号项目 #${currentRoute.provider_scope_id} · 管理版本 ${currentRoute.scope_version}` : '尚未绑定' }}</p><p>本地配置：{{ setup.configuration_ready ? '已就绪' : '仍有未完成项' }}；官方权限：尚未实际验证。</p><p v-if="!setup.org_active" class="text-danger">当前组织已停用，不能保存或绑定。</p><p v-if="!setup.policy.present || !setup.policy.async_execution_allowed" class="text-warn">当前组织尚未获得异步执行许可，绑定后仍需管理员完成执行策略配置。</p><p v-if="!setup.deployment.console_configured" class="text-warn">服务端尚未配置火山素材管理接口，请由部署管理员补齐。</p><p v-if="!setup.deployment.media_configured" class="text-warn">媒体存储与核验服务尚未就绪，上传功能仍不可用。</p><p v-if="!setup.deployment.portrait_target_ready" class="text-muted">真人认证返回地址尚未就绪，真人功能还需完成服务端配置。</p></section>
      <label class="block text-sm">已有火山账号项目<select v-model.number="scopeId" class="input mt-1" :disabled="busy || loading"><option :value="0">请选择账号项目</option><option v-for="item in setup.provider_scopes" :key="item.id" :value="item.id" :disabled="!item.enabled">{{ item.account_ref }} · {{ item.region }} · {{ item.project_name }}{{ item.enabled ? '' : '（已停用）' }}</option></select></label>
      <p v-if="!setup.provider_scopes.length" class="text-sm text-muted">尚无已登记的火山账号项目，请先完成视频渠道的账号项目绑定。</p>
      <template v-if="provider">
        <section class="rounded border border-line p-4 space-y-3"><h3 class="font-medium">登记已有火山项目</h3><p class="text-xs text-muted">在当前火山账号和地域下添加项目名称；请先在火山控制台创建该项目并完成授权。此处只登记到本平台，不会在火山创建项目或授予权限，原有 default 项目保留。添加和切换项目会清空未保存密钥，不复制其他项目的管理凭据、能力或组织路由。</p><label class="block text-sm">项目名称（必填）<input v-model="projectName" class="input mt-1" :disabled="busy || loading || refreshRequired" required maxlength="64" pattern="[A-Za-z0-9][A-Za-z0-9_-]{0,63}" autocomplete="off" spellcheck="false" autocapitalize="off" placeholder="例如 LAIAI-Gateway"></label><p class="text-xs text-muted">保留大小写；支持 1–64 位英文字母、数字、下划线和短横线，以字母或数字开头。</p><ul v-if="projectName && projectBlockers.length" class="list-disc pl-5 text-xs text-warn" aria-live="polite"><li v-for="reason in projectBlockers" :key="reason">{{ reason }}</li></ul><button type="button" class="btn-ghost" :disabled="!canAddProject" @click="addProject">{{ busy ? '处理中…' : '添加项目名称' }}</button></section>
        <section class="rounded border border-line p-4 space-y-4"><h3 class="font-medium">1. 登记素材管理版本</h3><p class="text-xs text-muted">已有可用版本时可直接使用下方第 2 步。新版本不会覆盖旧版本，也不会自动绑定组织。</p>
          <fieldset :disabled="busy || loading || refreshRequired" class="space-y-4">
            <label class="block text-sm">新版本号（必填）<input v-model="version" class="input mt-1" inputmode="numeric" required pattern="[1-9][0-9]*" placeholder="例如 1" autocomplete="off"><small class="text-muted">已建议当前项目未占用的版本号，可自行修改为其他未使用的正整数。</small></label>
            <div class="grid gap-3 sm:grid-cols-2"><label class="block text-sm">Access Key（必填）<input v-model="accessKey" type="password" class="input mt-1" required minlength="8" autocomplete="new-password" spellcheck="false" autocapitalize="off" maxlength="1024"></label><label class="block text-sm">Secret Key（必填）<input v-model="secretKey" type="password" class="input mt-1" required minlength="8" autocomplete="new-password" spellcheck="false" autocapitalize="off" maxlength="4096"></label></div>
            <label class="block text-sm">Session Token（可选）<input v-model="sessionToken" type="password" class="input mt-1" autocomplete="new-password" spellcheck="false" autocapitalize="off" maxlength="8192"></label>
            <fieldset class="space-y-2"><legend class="text-sm font-medium mb-2">确认已在火山开通并授权的能力</legend><label v-for="[key, label] in capabilityOptions" :key="key" class="flex items-start gap-2 text-sm"><input v-model="capabilities[key]" type="checkbox" class="mt-1 shrink-0"><span>{{ label }}</span></label></fieldset>
            <label class="flex items-start gap-2 text-sm"><input v-model="enabled" type="checkbox" class="mt-1 shrink-0"><span>启用该管理版本，允许后续绑定组织</span></label>
            <label class="flex items-start gap-2 text-sm"><input v-model="registrationConfirmed" type="checkbox" class="mt-1 shrink-0"><span>已核对当前账号项目及所选能力，确认登记此版本；保存不代表官方权限验证成功。</span></label>
          </fieldset>
          <p v-if="!enabled" class="text-xs text-muted">未勾选“启用该管理版本”时会保存为停用版本；可在第 2 步单独确认启用，再绑定组织。</p>
          <p v-if="!Object.values(capabilities).some(Boolean)" class="text-xs text-muted">尚未选择任何素材能力；所有能力默认关闭，请仅勾选已在火山授权的能力。</p>
          <p v-if="versionNumber && versions.some(item => item.version === versionNumber)" class="text-xs text-warn">该版本已保存，请直接选择下方已有版本，或填写新的版本号。</p>
          <div v-if="registerBlockers.length" class="rounded border border-line p-3 text-sm" aria-live="polite"><strong>保存前还需完成：</strong><ul class="mt-2 list-disc pl-5 text-muted"><li v-for="reason in registerBlockers" :key="reason">{{ reason }}</li></ul></div>
          <button type="button" class="btn-ghost" :disabled="!canRegister" @click="register">{{ busy ? '处理中…' : '保存管理版本' }}</button>
        </section>
        <section class="rounded border border-line p-4 space-y-4"><h3 class="font-medium">2. 绑定当前组织</h3><label class="block text-sm">已保存管理版本<select v-model.number="selectedVersion" class="input mt-1" :disabled="busy || loading || refreshRequired"><option :value="0">请选择已保存版本</option><option v-for="item in versions" :key="item.version" :value="item.version">版本 {{ item.version }}{{ item.enabled ? '' : '（已停用）' }}{{ item.credential_present ? '' : '（无管理凭据）' }}</option></select></label>
          <template v-if="savedVersion"><p class="text-sm">凭据：{{ savedVersion.credential_present ? '已保存（不回显）' : '缺失' }} · 版本：{{ savedVersion.enabled ? '已启用' : '已停用' }}</p><p class="text-xs text-muted">已登记能力：{{ capabilityOptions.filter(([key]) => savedVersion?.capabilities[key]).map(([, label]) => label).join('、') || '未启用任何能力' }}</p><p v-if="!savedVersion.capabilities_valid || !savedVersion.identity_matches_provider" class="text-sm text-danger">此版本的账号项目或能力数据不匹配，不能用于组织绑定。</p></template>
          <div v-if="savedVersion && !savedVersion.enabled" class="rounded border border-line p-3 space-y-3"><p class="text-sm">启用此管理版本会影响所有引用该版本的组织；不会自动修改任何组织路由，也不代表官方权限验证成功。</p><label class="flex items-start gap-2 text-sm"><input v-model="enableConfirmed" type="checkbox" class="mt-1 shrink-0" :disabled="busy || loading || refreshRequired"><span>我已核对此管理版本，确认启用所选版本。</span></label><button type="button" class="btn-ghost" :disabled="!canEnable" @click="enableSavedVersion">{{ busy ? '处理中…' : '启用所选管理版本' }}</button></div>
          <label class="flex items-start gap-2 text-sm"><input v-model="bindingConfirmed" type="checkbox" class="mt-1 shrink-0" :disabled="busy || loading || refreshRequired || !savedVersion"><span>确认将所选管理版本设为组织 #{{ orgId }} 的默认素材路由并启用，替换该组织原有的默认素材路由。</span></label>
          <div v-if="bindBlockers.length" class="rounded border border-line p-3 text-sm" aria-live="polite"><strong>绑定前还需完成：</strong><ul class="mt-2 list-disc pl-5 text-muted"><li v-for="reason in bindBlockers" :key="reason">{{ reason }}</li></ul></div>
          <button type="button" class="btn-primary" :disabled="!canBind" @click="bind">{{ busy ? '处理中…' : '绑定当前组织并启用' }}</button>
        </section>
      </template>
      <p v-if="refreshRequired" class="text-sm text-warn" role="status">请先刷新配置状态，再核对已保存版本与当前组织路由；不会自动重发任何写入。</p>
    </template>
  </div>
</template>
