<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { adminApi, ApiError, errorMessage } from '@/lib/api'

type Channel = { id: number; name: string; provider: string; status: number }
type Org = { id: number; name: string }
type Group = { id: number; slug: string; name: string; status: string; policy_version: number }
type Binding = { channel_id: number; channel?: Channel; channel_enabled: boolean }
type Grant = { group_id: number; status: string; grant_version: number; is_default: boolean }
type GrantRow = { grant: Grant; group?: Group; configuration_valid: boolean }
type Default = { group_id: number; grant_version: number; default_valid: boolean; invalid_reasons: string[] }
type Project = { group_id: number; provider_scope_id: number; scope_version: number; revision: number }
type ScopeConfig = { provider_scope_id: number; version: number; enabled: boolean; credential_present: boolean; identity_matches_provider: boolean; capabilities_valid: boolean }
type ProviderScope = { id: number; account_ref: string; region: string; project_name: string; enabled: boolean }
type AssetSetup = { provider_scopes: ProviderScope[]; scope_configs: ScopeConfig[]; configuration_ready: boolean; issues: string[] }
type UserLimit = { version: number; create_task_rpm: number; max_inflight_tasks: number; daily_task_limit: number; monthly_task_limit: number; timezone: string }
type ProviderQuota = {
  account_ref: string; foundation_model: string; manual_version: number; official_observed_at: string | null
  official_create_task_rpm: number | null; official_concurrent_requests: number | null
  official_create_task_rpm_4k: number | null; official_concurrent_requests_4k: number | null
  manual_create_task_rpm: number | null; manual_concurrent_requests: number | null
  manual_create_task_rpm_4k: number | null; manual_concurrent_requests_4k: number | null
}

const props = defineProps<{ channels: Channel[]; orgs: Org[] }>()
const groups = ref<Group[]>([])
const selectedGroupId = ref(0)
const selectedGroup = computed(() => groups.value.find(g => g.id === selectedGroupId.value))
const bindings = ref<Binding[]>([])
const channelId = ref(0)
const project = ref<Project | null>(null)
const projectChoice = ref('')
const groupForm = reactive({ slug: '', name: '' })
const orgId = ref(0)
const grantRows = ref<GrantRow[]>([])
const currentDefault = ref<Default | null>(null)
const setup = ref<AssetSetup | null>(null)
const userId = ref(0)
const quotaScopeId = ref(0)
const foundationModel = ref('doubao-seedance-2-5')
const providerQuota = ref<ProviderQuota | null>(null)
const officialStale = ref(false)
const quotaManual = reactive({ create_task_rpm: '', concurrent_requests: '', create_task_rpm_4k: '', concurrent_requests_4k: '' })
const limitLoaded = ref(false)
const limitForm = reactive({ version: 0, create_task_rpm: 0, max_inflight_tasks: 0, daily_task_limit: 0, monthly_task_limit: 0, timezone: 'Asia/Shanghai', daily_usd_limit: '', monthly_usd_limit: '' })
const busy = ref(false)
const loading = ref(false)
const error = ref('')
const notice = ref('')

const selectedGrant = computed(() => grantRows.value.find(row => row.grant.group_id === selectedGroupId.value)?.grant)
const availableChannels = computed(() => props.channels.filter(ch => ch.status === 1 && !bindings.value.some(b => b.channel_id === ch.id)))
const eligibleConfigs = computed(() => (setup.value?.scope_configs ?? []).filter(c => c.enabled && c.credential_present && c.identity_matches_provider && c.capabilities_valid && setup.value?.provider_scopes.some(p => p.id === c.provider_scope_id && p.enabled)))
const quotaEligibleScopes = computed(() => {
  const latest = new Map<number, ScopeConfig>()
  for (const cfg of setup.value?.scope_configs ?? []) {
    if (!latest.has(cfg.provider_scope_id) || latest.get(cfg.provider_scope_id)!.version < cfg.version) latest.set(cfg.provider_scope_id, cfg)
  }
  return [...latest.values()].filter(c => c.enabled && c.credential_present && c.identity_matches_provider && setup.value?.provider_scopes.some(p => p.id === c.provider_scope_id && p.enabled))
})
const quotaScope = computed(() => setup.value?.provider_scopes.find(p => p.id === quotaScopeId.value))

function message(e: unknown) { error.value = errorMessage(e, '操作失败，请重试') }
async function mutate(action: () => Promise<void>, success: string) {
  if (busy.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try { await action(); notice.value = success } catch (e) { message(e) } finally { busy.value = false }
}
async function loadGroups() {
  const out = await adminApi<{ items: Group[] }>('GET', '/api/admin/routing-groups?owner_org_id=0&limit=100')
  groups.value = out.items ?? []
  if (!groups.value.some(g => g.id === selectedGroupId.value)) selectedGroupId.value = groups.value[0]?.id ?? 0
}
async function loadSelected() {
  bindings.value = []; project.value = null; projectChoice.value = ''
  if (!selectedGroupId.value) return
  try {
    const out = await adminApi<{ items: Binding[] }>('GET', `/api/admin/routing-groups/${selectedGroupId.value}/channels?limit=100`)
    bindings.value = out.items ?? []
    try {
      const mapped = await adminApi<{ project: Project }>('GET', `/api/admin/video/group-asset-projects/${selectedGroupId.value}`)
      project.value = mapped.project
      projectChoice.value = `${mapped.project.provider_scope_id}:${mapped.project.scope_version}`
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 404)) throw e
    }
  } catch (e) { message(e) }
}
async function loadOrg() {
  grantRows.value = []; currentDefault.value = null; setup.value = null; limitLoaded.value = false; providerQuota.value = null; quotaScopeId.value = 0
  if (!orgId.value) return
  try {
    const [grant, asset] = await Promise.all([
      adminApi<{ items: GrantRow[]; current_default: Default | null }>('GET', `/api/admin/orgs/${orgId.value}/routing-groups?limit=100`),
      adminApi<AssetSetup>('GET', `/api/admin/video/asset-setup?org_id=${orgId.value}`),
    ])
    grantRows.value = grant.items ?? []
    currentDefault.value = grant.current_default
    setup.value = asset
    quotaScopeId.value = quotaEligibleScopes.value[0]?.provider_scope_id ?? 0
  } catch (e) { message(e) }
}
function fillManual(q: ProviderQuota | null) {
  quotaManual.create_task_rpm = q?.manual_create_task_rpm?.toString() ?? ''
  quotaManual.concurrent_requests = q?.manual_concurrent_requests?.toString() ?? ''
  quotaManual.create_task_rpm_4k = q?.manual_create_task_rpm_4k?.toString() ?? ''
  quotaManual.concurrent_requests_4k = q?.manual_concurrent_requests_4k?.toString() ?? ''
}
async function loadProviderQuota() {
  providerQuota.value = null; officialStale.value = false; fillManual(null); error.value = ''
  if (!quotaScope.value || !foundationModel.value.trim()) return
  try {
    const out = await adminApi<{ quota: ProviderQuota; official_stale: boolean }>('GET', `/api/admin/video/provider-accounts/${encodeURIComponent(quotaScope.value.account_ref)}/model-rate-limits/${encodeURIComponent(foundationModel.value.trim())}`)
    providerQuota.value = out.quota; officialStale.value = out.official_stale; fillManual(out.quota)
  } catch (e) {
    if (!(e instanceof ApiError && e.status === 404)) message(e)
  }
}
async function syncProviderQuota() {
  if (!quotaScopeId.value || !foundationModel.value.trim()) return
  await mutate(async () => {
    await adminApi('POST', `/api/admin/video/provider-scopes/${quotaScopeId.value}/model-rate-limits/sync`, { foundation_model_names: [foundationModel.value.trim()] })
    await loadProviderQuota()
  }, '火山官方账号配额已同步')
}
function optionalLimit(raw: string): number | null { return raw.trim() === '' ? null : Number(raw) }
async function saveProviderQuota() {
  if (!quotaScope.value || !foundationModel.value.trim()) return
  await mutate(async () => {
    await adminApi('PUT', `/api/admin/video/provider-accounts/${encodeURIComponent(quotaScope.value!.account_ref)}/model-rate-limits/${encodeURIComponent(foundationModel.value.trim())}/manual`, {
      expected_version: providerQuota.value?.manual_version ?? 0,
      create_task_rpm: optionalLimit(quotaManual.create_task_rpm),
      concurrent_requests: optionalLimit(quotaManual.concurrent_requests),
      create_task_rpm_4k: optionalLimit(quotaManual.create_task_rpm_4k),
      concurrent_requests_4k: optionalLimit(quotaManual.concurrent_requests_4k),
    })
    await loadProviderQuota()
  }, '主账号模型人工上限已保存')
}
async function createGroup() {
  await mutate(async () => {
    const out = await adminApi<{ group: Group }>('POST', '/api/admin/routing-groups', { owner_org_id: 0, slug: groupForm.slug.trim(), name: groupForm.name.trim() })
    await loadGroups(); selectedGroupId.value = out.group.id
    groupForm.slug = ''; groupForm.name = ''
  }, '路由组已创建，请绑定渠道并启用')
}
async function setGroupStatus(status: 'enabled' | 'disabled') {
  const group = selectedGroup.value
  if (!group) return
  await mutate(async () => {
    await adminApi('PATCH', `/api/admin/routing-groups/${group.id}`, { expected_version: group.policy_version, status })
    await loadGroups(); await loadSelected(); await loadOrg()
  }, '路由组状态已更新')
}
async function setBinding(id: number, bound: boolean) {
  const group = selectedGroup.value
  if (!group || !id) return
  await mutate(async () => {
    await adminApi(bound ? 'PUT' : 'DELETE', `/api/admin/routing-groups/${group.id}/channels/${id}`, { expected_group_version: group.policy_version })
    await loadGroups(); await loadSelected(); await loadOrg()
    channelId.value = 0
  }, bound ? '渠道已关联' : '渠道已解除关联')
}
async function saveProject() {
  const [scope, version] = projectChoice.value.split(':').map(Number)
  if (!selectedGroupId.value || !scope || !version) return
  await mutate(async () => {
    await adminApi('PUT', `/api/admin/video/group-asset-projects/${selectedGroupId.value}`, { provider_scope_id: scope, scope_version: version, expected_revision: project.value?.revision ?? 0 })
    await loadSelected()
  }, '素材项目已关联；新素材操作使用此项目')
}
async function setGrant(status: 'enabled' | 'revoked') {
  if (!orgId.value || !selectedGroupId.value) return
  await mutate(async () => {
    await adminApi('PUT', `/api/admin/orgs/${orgId.value}/routing-groups/${selectedGroupId.value}`, { expected_grant_version: selectedGrant.value?.grant_version ?? 0, status })
    await loadOrg()
  }, status === 'enabled' ? '组织已获得路由组' : '组织路由组授权已撤销')
}
async function setDefault() {
  const grant = selectedGrant.value
  if (!orgId.value || !selectedGroupId.value || !grant || grant.status !== 'enabled') return
  await mutate(async () => {
    await adminApi('PUT', `/api/admin/orgs/${orgId.value}/routing-default`, { target_group_id: selectedGroupId.value, expected_current_default: currentDefault.value ? { group_id: currentDefault.value.group_id, grant_version: currentDefault.value.grant_version } : null, expected_target_grant_version: grant.grant_version })
    await loadOrg()
  }, '组织默认路由组已更新；新令牌自动继承')
}
async function loadUserLimits() {
  if (!orgId.value || !userId.value) return
  error.value = ''; limitLoaded.value = false
  try {
    const out = await adminApi<{ limits: UserLimit; daily_usd_limit: string | null; monthly_usd_limit: string | null }>('GET', `/api/admin/video/orgs/${orgId.value}/users/${userId.value}/limits`)
    const limit = out.limits
    limitForm.version = limit.version || 0
    limitForm.create_task_rpm = limit.create_task_rpm || 0
    limitForm.max_inflight_tasks = limit.max_inflight_tasks || 0
    limitForm.daily_task_limit = limit.daily_task_limit || 0
    limitForm.monthly_task_limit = limit.monthly_task_limit || 0
    limitForm.timezone = limit.timezone || 'Asia/Shanghai'
    limitForm.daily_usd_limit = out.daily_usd_limit ?? ''
    limitForm.monthly_usd_limit = out.monthly_usd_limit ?? ''
    limitLoaded.value = true
  } catch (e) { message(e) }
}
async function saveUserLimits() {
  if (!limitLoaded.value) return
  await mutate(async () => {
    await adminApi('PUT', `/api/admin/video/orgs/${orgId.value}/users/${userId.value}/limits`, {
      expected_version: limitForm.version,
      create_task_rpm: Number(limitForm.create_task_rpm),
      max_inflight_tasks: Number(limitForm.max_inflight_tasks),
      daily_task_limit: Number(limitForm.daily_task_limit),
      monthly_task_limit: Number(limitForm.monthly_task_limit),
      timezone: limitForm.timezone,
      daily_usd_limit: limitForm.daily_usd_limit.trim() || null,
      monthly_usd_limit: limitForm.monthly_usd_limit.trim() || null,
    })
    await loadUserLimits()
  }, '用户任务与金额额度已保存')
}

watch(selectedGroupId, () => { void loadSelected() })
watch(orgId, () => { void loadOrg() })
watch(userId, () => { limitLoaded.value = false })
watch([quotaScopeId, foundationModel], () => { providerQuota.value = null; fillManual(null) })
watch(() => props.orgs, value => { if (!orgId.value && value.length) orgId.value = value[0].id })
onMounted(async () => {
  loading.value = true
  try {
    await loadGroups()
    orgId.value = props.orgs[0]?.id ?? 0
  } catch (e) { message(e) } finally { loading.value = false }
})
</script>

<template>
  <div class="space-y-4">
    <p v-if="error" class="inline-alert" role="alert">{{ error }}</p>
    <p v-if="notice" class="text-sm text-ok" role="status">{{ notice }}</p>
    <p v-if="loading" class="text-sm text-muted">正在读取路由组…</p>
    <section class="app-panel space-y-4 p-5">
      <div>
        <h2 class="text-lg font-semibold">火山主账号模型配额</h2>
        <p class="text-sm text-muted">通过已配置的管理 AK/SK 读取官方 CreateTask RPM 与并发。配额按主账号和基础模型共用，人工上限与官方值取更小值；空白表示不额外限制。</p>
      </div>
      <label class="text-sm">组织
        <select v-model.number="orgId" class="input mt-1 min-w-64"><option :value="0">请选择组织</option><option v-for="o in orgs" :key="o.id" :value="o.id">{{ o.name }} · #{{ o.id }}</option></select>
      </label>
      <p v-if="setup && !setup.configuration_ready" class="text-sm text-danger">素材前置配置尚未就绪：{{ setup.issues.join('、') }}</p>
      <div class="flex flex-wrap items-end gap-2">
        <label class="text-sm">管理项目
          <select v-model.number="quotaScopeId" class="input mt-1 min-w-56"><option :value="0">选择项目</option><option v-for="cfg in quotaEligibleScopes" :key="`${cfg.provider_scope_id}:${cfg.version}`" :value="cfg.provider_scope_id">{{ setup?.provider_scopes.find(p => p.id === cfg.provider_scope_id)?.project_name }} · #{{ cfg.provider_scope_id }}</option></select>
        </label>
        <label class="text-sm">基础模型名 <input v-model="foundationModel" class="input mt-1 min-w-64 font-mono text-xs" placeholder="doubao-seedance-2-5" /></label>
        <button class="btn-ghost" :disabled="!quotaScopeId || !foundationModel" @click="loadProviderQuota">读取</button>
        <button class="btn-ghost" :disabled="busy || !quotaScopeId || !foundationModel" @click="syncProviderQuota">同步官方配额</button>
      </div>
      <p v-if="quotaScope" class="text-xs text-muted">主账号 {{ quotaScope.account_ref }} · {{ quotaScope.region }}；当前官方快照：{{ providerQuota?.official_observed_at || '尚未同步' }}</p>
      <p v-if="providerQuota" class="text-sm">官方：{{ providerQuota.official_create_task_rpm ?? '—' }} RPM、{{ providerQuota.official_concurrent_requests ?? '—' }} 并发；4K {{ providerQuota.official_create_task_rpm_4k ?? '—' }} RPM、{{ providerQuota.official_concurrent_requests_4k ?? '—' }} 并发。<span v-if="officialStale" class="text-danger">快照超过 30 分钟，请检查同步。</span></p>
      <form v-if="quotaScopeId && foundationModel" class="space-y-3 border-t border-line pt-4" @submit.prevent="saveProviderQuota">
        <h3 class="font-medium">人工上限</h3>
        <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <label class="text-sm">任务 RPM <input v-model="quotaManual.create_task_rpm" class="input mt-1" type="number" min="0" placeholder="不限" /></label>
          <label class="text-sm">并发 <input v-model="quotaManual.concurrent_requests" class="input mt-1" type="number" min="0" placeholder="不限" /></label>
          <label class="text-sm">4K 任务 RPM <input v-model="quotaManual.create_task_rpm_4k" class="input mt-1" type="number" min="0" placeholder="不限" /></label>
          <label class="text-sm">4K 并发 <input v-model="quotaManual.concurrent_requests_4k" class="input mt-1" type="number" min="0" placeholder="不限" /></label>
        </div>
        <button class="btn-primary" :disabled="busy">保存人工上限</button>
      </form>
    </section>

    <section class="app-panel space-y-4 p-5">
      <div>
        <h2 class="text-lg font-semibold">渠道路由组</h2>
        <p class="text-sm text-muted">渠道绑定决定该组可使用的模型。组织授权并设为默认后，新 API 令牌自动继承官方 Seedance 视频及素材权限。</p>
      </div>
      <form class="flex flex-wrap items-end gap-2" @submit.prevent="createGroup">
        <label class="text-sm">标识 <input v-model="groupForm.slug" class="input mt-1" required maxlength="64" placeholder="ark-premium" /></label>
        <label class="text-sm">名称 <input v-model="groupForm.name" class="input mt-1" required maxlength="128" placeholder="火山高配" /></label>
        <button class="btn-primary" :disabled="busy">创建路由组</button>
      </form>
      <div class="flex flex-wrap items-end gap-2">
        <label class="text-sm">当前路由组
          <select v-model.number="selectedGroupId" class="input mt-1 min-w-56"><option :value="0">请选择</option><option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }} · {{ g.status }} · #{{ g.id }}</option></select>
        </label>
        <button v-if="selectedGroup && selectedGroup.status !== 'enabled'" class="btn-ghost" :disabled="busy" @click="setGroupStatus('enabled')">启用</button>
        <button v-if="selectedGroup?.status === 'enabled'" class="btn-ghost" :disabled="busy" @click="setGroupStatus('disabled')">停用</button>
      </div>
      <template v-if="selectedGroup">
        <div class="border-t border-line pt-4">
          <h3 class="font-medium">关联渠道</h3>
          <div class="mt-2 flex flex-wrap items-end gap-2">
            <select v-model.number="channelId" class="input min-w-64"><option :value="0">选择启用渠道</option><option v-for="ch in availableChannels" :key="ch.id" :value="ch.id">{{ ch.name }} · {{ ch.provider }} · #{{ ch.id }}</option></select>
            <button class="btn-ghost" :disabled="busy || !channelId" @click="setBinding(channelId, true)">关联</button>
          </div>
          <p v-if="!bindings.length" class="mt-2 text-sm text-muted">尚无关联渠道</p>
          <div v-for="binding in bindings" :key="binding.channel_id" class="mt-2 flex items-center justify-between gap-2 rounded-lg border border-line px-3 py-2 text-sm">
            <span>{{ binding.channel?.name || `渠道 #${binding.channel_id}` }} <span v-if="!binding.channel_enabled" class="text-danger">（已停用）</span></span>
            <button class="btn-ghost !py-1 text-xs" :disabled="busy" @click="setBinding(binding.channel_id, false)">解除</button>
          </div>
        </div>
      </template>
    </section>

    <section class="app-panel space-y-4 p-5">
      <div>
        <h2 class="text-lg font-semibold">组织授权与素材项目</h2>
        <p class="text-sm text-muted">项目关联只作用于新素材操作。历史素材保留原账号、地域和项目归属。</p>
      </div>
      <label class="text-sm">组织
        <select v-model.number="orgId" class="input mt-1 min-w-64"><option :value="0">请选择组织</option><option v-for="o in orgs" :key="o.id" :value="o.id">{{ o.name }} · #{{ o.id }}</option></select>
      </label>
      <div v-if="orgId && selectedGroup" class="flex flex-wrap items-center gap-2 text-sm">
        <span>当前授权：{{ selectedGrant?.status || '无' }}</span>
        <button v-if="selectedGrant?.status !== 'enabled'" class="btn-ghost" :disabled="busy" @click="setGrant('enabled')">授权此组</button>
        <button v-else class="btn-ghost" :disabled="busy" @click="setGrant('revoked')">撤销授权</button>
        <button class="btn-ghost" :disabled="busy || selectedGrant?.status !== 'enabled' || currentDefault?.group_id === selectedGroupId" @click="setDefault">设为组织默认组</button>
        <span v-if="currentDefault" class="text-muted">当前默认 #{{ currentDefault.group_id }}{{ currentDefault.default_valid ? '' : '（失效）' }}</span>
      </div>
      <div v-if="selectedGroup && orgId" class="border-t border-line pt-4">
        <h3 class="font-medium">此组使用的火山素材项目</h3>
        <div class="mt-2 flex flex-wrap items-end gap-2">
          <select v-model="projectChoice" class="input min-w-64">
            <option value="">选择已配置的项目</option>
            <option v-for="cfg in eligibleConfigs" :key="`${cfg.provider_scope_id}:${cfg.version}`" :value="`${cfg.provider_scope_id}:${cfg.version}`">
              {{ setup?.provider_scopes.find(p => p.id === cfg.provider_scope_id)?.project_name || `项目 #${cfg.provider_scope_id}` }} · #{{ cfg.provider_scope_id }} / v{{ cfg.version }}
            </option>
          </select>
          <button class="btn-ghost" :disabled="busy || !projectChoice" @click="saveProject">关联项目</button>
        </div>
        <p class="mt-2 text-xs text-muted">当前关联：{{ project ? `项目 #${project.provider_scope_id} / v${project.scope_version}，修订 ${project.revision}` : '未关联' }}。项目须与本组已发布 Seedance 渠道使用同一火山账号。</p>
      </div>
    </section>

    <section class="app-panel space-y-4 p-5">
      <div>
        <h2 class="text-lg font-semibold">用户 Seedance 额度</h2>
        <p class="text-sm text-muted">用户任务 RPM 是每分钟创建任务数，同一用户的所有令牌合并计数；派单 RPM 也受此值约束。0 表示该项不设用户上限，火山账号总配额仍独立生效。</p>
      </div>
      <div class="flex flex-wrap items-end gap-2">
        <label class="text-sm">用户 ID <input v-model.number="userId" class="input mt-1 w-36" type="number" min="1" /></label>
        <button class="btn-ghost" :disabled="!orgId || !userId" @click="loadUserLimits">读取额度</button>
      </div>
      <form v-if="limitLoaded" class="space-y-3" @submit.prevent="saveUserLimits">
        <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <label class="text-sm">任务 RPM <input v-model.number="limitForm.create_task_rpm" class="input mt-1" type="number" min="0" max="60000" required /></label>
          <label class="text-sm">在途任务数 <input v-model.number="limitForm.max_inflight_tasks" class="input mt-1" type="number" min="0" required /></label>
          <label class="text-sm">每日任务数 <input v-model.number="limitForm.daily_task_limit" class="input mt-1" type="number" min="0" required /></label>
          <label class="text-sm">每月任务数 <input v-model.number="limitForm.monthly_task_limit" class="input mt-1" type="number" min="0" required /></label>
          <label class="text-sm">每日金额上限（USD） <input v-model="limitForm.daily_usd_limit" class="input mt-1" inputmode="decimal" placeholder="不限" /></label>
          <label class="text-sm">每月金额上限（USD） <input v-model="limitForm.monthly_usd_limit" class="input mt-1" inputmode="decimal" placeholder="不限" /></label>
          <label class="text-sm">日历时区 <input v-model="limitForm.timezone" class="input mt-1" required placeholder="Asia/Shanghai" /></label>
        </div>
        <button class="btn-primary" :disabled="busy">保存用户额度</button>
      </form>
    </section>
  </div>
</template>
