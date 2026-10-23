<script setup lang="ts">
import { fmtUsd, displayCurrencyCode } from '@/lib/money'
import { computed, onMounted, onBeforeUnmount, ref, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, ApiError, copyText, errorMessage } from '@/lib/api'
import { buildKeyCreateBody, videoPermission, type VideoPermissionChoice } from '@/lib/keyPermissions'
import ByokPanel from '@/components/ByokPanel.vue'
import DetailDrawer from '@/components/DetailDrawer.vue'
import { useAuth } from '@/stores/auth'

interface KeyRow {
  id: number
  prefix: string
  name: string
  scopes?: string
  status: number
  expires_at: string | null
  quota_limit: string
  used_amount: string
  model_limits: string
  allow_ips: string
  rpm_limit: number
  route_policy: string
}

const { t, d } = useI18n()
const auth = useAuth()
const selectedKey = ref<KeyRow | null>(null)
let loadEpoch = 0
let contextEpoch = 0
type KeyContext = { epoch: number; org: number; token: string }
const context = (): KeyContext => ({ epoch: contextEpoch, org: auth.orgId, token: auth.token })
const current = (value: KeyContext) => value.epoch === contextEpoch && value.org === auth.orgId && value.token === auth.token
const keys = ref<KeyRow[]>([])
const loading = ref(true)
const error = ref('')

const showCreate = ref(false)
const newName = ref('')
const newQuota = ref('')
const newModels = ref('')
const newIps = ref('')
const newExpiry = ref(0)
const newRPM = ref(0) // key 级每分钟请求上限，0=不限（0041）
const newRoute = ref('') // key 级路由策略，''=order 默认（0043）
const newVideoPermission = ref<VideoPermissionChoice>('none')
const videoChoices: VideoPermissionChoice[] = ['none', 'read', 'read_write']
const permissionLabels = { none: 'keys.videoNone', read: 'keys.videoRead', read_write: 'keys.videoReadWrite', write: 'keys.videoWriteOnly' }
const permissionHints = { none: 'keys.videoNoneHint', read: 'keys.videoReadHint', read_write: 'keys.videoReadWriteHint' }
const creating = ref(false)
// 明文只显示一次（红线 8 的 UI 面）：弹窗关闭即不可再取
const createdKey = ref('')
const copied = ref(false)
const copyError = ref(false)
const revoking = ref<number | null>(null)
const activeKeys = computed(() => keys.value.filter((k) => k.status === 1).length)
const restrictedKeys = computed(() => keys.value.filter((k) => k.model_limits || k.allow_ips).length)
const cappedKeys = computed(() => keys.value.filter((k) => k.quota_limit !== '0').length)

// —— 弹窗焦点管理 + Escape（无障碍：role=dialog 焦点陷阱 + 关闭即恢复焦点）——
const createDialog = ref<HTMLElement | null>(null)
const createdDialog = ref<HTMLElement | null>(null)
const stepUpDialog = ref<HTMLElement | null>(null)
let lastFocused: HTMLElement | null = null
let copyResetTimer: ReturnType<typeof setTimeout> | undefined

function focusDialog(el: HTMLElement | null) {
  const focusable = el?.querySelector<HTMLElement>(
    'input, select, button, [tabindex]:not([tabindex="-1"])',
  )
  focusable?.focus()
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    if (stepUp.value.open) closeStepUp()
    else if (createdKey.value) createdKey.value = ''
    else if (showCreate.value) showCreate.value = false
    return
  }
  // 焦点陷阱：Tab 在弹窗内循环
  if (e.key !== 'Tab') return
  const dialog = stepUp.value.open ? stepUpDialog.value : createdKey.value ? createdDialog.value : showCreate.value ? createDialog.value : null
  if (!dialog) return
  const nodes = dialog.querySelectorAll<HTMLElement>(
    'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])',
  )
  if (nodes.length === 0) return
  const first = nodes[0]
  const last = nodes[nodes.length - 1]
  if (e.shiftKey && document.activeElement === first) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && document.activeElement === last) {
    e.preventDefault()
    first.focus()
  }
}
onMounted(() => document.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
  clearTimeout(copyResetTimer)
})

async function load(owner = context()) {
  if (!current(owner)) return
  if (!owner.token) { loading.value = false; return }
  const epoch = ++loadEpoch
  loading.value = true
  try {
    const r = await api<{ keys: KeyRow[] }>('GET', '/api/keys')
    if (epoch !== loadEpoch || !current(owner)) return
    keys.value = r.keys
    selectedKey.value = null
    error.value = ''
  } catch (e) {
    if (epoch === loadEpoch && current(owner)) error.value = errorMessage(e, t('common.error'))
  } finally {
    if (epoch === loadEpoch && current(owner)) loading.value = false
  }
}

// 二次验证（step-up）：若运营开启 require_stepup_for_keys，创建 Key 会先回 403
// VERIFICATION_REQUIRED；此时弹窗收集密码/TOTP 换 step-up 令牌，再带 X-Secure-Verify 重试。
const stepUp = ref<{ open: boolean; pwd: string; code: string; err: string; busy: boolean; body: Record<string, unknown> | null; context: KeyContext | null }>(
  { open: false, pwd: '', code: '', err: '', busy: false, body: null, context: null },
)

watch([showCreate, createdKey, () => stepUp.value.open], async ([create, created, verify]) => {
  if (create || created !== '' || verify) {
    lastFocused ??= document.activeElement as HTMLElement
    await nextTick()
    focusDialog(stepUp.value.open ? stepUpDialog.value : createdKey.value ? createdDialog.value : showCreate.value ? createDialog.value : null)
  } else {
    lastFocused?.focus()
    lastFocused = null
  }
})

function closeStepUp() {
  stepUp.value = { open: false, pwd: '', code: '', err: '', busy: false, body: null, context: null }
}
function resetForm() {
  newName.value = newQuota.value = newModels.value = newIps.value = ''
  newExpiry.value = 0
  newRPM.value = 0
  newRoute.value = ''
  newVideoPermission.value = 'none'
}
function openCreate() {
  resetForm()
  closeStepUp()
  selectedKey.value = null
  error.value = ''
  showCreate.value = true
}
function buildKeyBody(): Record<string, unknown> {
  return buildKeyCreateBody({ name: newName.value, quota: newQuota.value, models: newModels.value, ips: newIps.value,
    expiry: newExpiry.value, rpm: newRPM.value, route: newRoute.value, videoPermission: newVideoPermission.value })
}
async function doCreate(body: Record<string, unknown>, owner: KeyContext, stepUpToken?: string) {
  if (!current(owner)) return
  const r = await api<{ key: string }>('POST', '/api/keys', body, stepUpToken ? { headers: { 'X-Secure-Verify': stepUpToken } } : {})
  if (!current(owner)) return
  createdKey.value = r.key
  copied.value = copyError.value = false
  showCreate.value = false
  resetForm()
  await load(owner)
}

async function create() {
  if (creating.value || stepUp.value.open) return
  const owner = context()
  creating.value = true
  const body = buildKeyBody()
  try {
    await doCreate(body, owner)
  } catch (e) {
    if (!current(owner)) return
    if (e instanceof ApiError && e.code === 'VERIFICATION_REQUIRED') {
      stepUp.value = { open: true, pwd: '', code: '', err: '', busy: false, body, context: owner }
    } else {
      error.value = errorMessage(e, t('common.error'))
    }
  } finally {
    if (current(owner)) creating.value = false
  }
}

async function submitStepUp() {
  const pending = stepUp.value, owner = pending.context
  if (!pending.open || pending.busy || !pending.body || !owner || !current(owner)) return
  pending.busy = true
  pending.err = ''
  try {
    const vr = await api<{ token: string }>('POST', '/api/me/verify', { password: pending.pwd, totp_code: pending.code })
    pending.pwd = pending.code = ''
    if (!current(owner) || stepUp.value !== pending || !pending.open) return
    await doCreate(pending.body, owner, vr.token)
    if (current(owner) && stepUp.value === pending) closeStepUp()
  } catch (e) {
    if (current(owner) && stepUp.value === pending) pending.err = errorMessage(e, t('common.error'))
  } finally {
    pending.pwd = pending.code = ''
    if (current(owner) && stepUp.value === pending) pending.busy = false
  }
}

async function copyKey() {
  const owner = context(), value = createdKey.value
  copyError.value = false
  const ok = await copyText(value)
  if (!current(owner) || createdKey.value !== value) return
  if (ok) {
    copied.value = true
    clearTimeout(copyResetTimer)
    copyResetTimer = setTimeout(() => (copied.value = false), 1500)
  } else {
    copyError.value = true
  }
}

async function revoke(id: number) {
  if (!confirm(t('keys.revokeConfirm'))) return
  const owner = context()
  revoking.value = id
  try {
    await api('DELETE', `/api/keys/${id}`)
    if (current(owner)) await load(owner)
  } catch (e) {
    if (current(owner)) error.value = errorMessage(e, t('common.error'))
  } finally {
    if (current(owner)) revoking.value = null
  }
}

async function remove(id: number) {
  if (!confirm(t('keys.deleteConfirm'))) return
  const owner = context()
  revoking.value = id
  try {
    await api('DELETE', `/api/keys/${id}/purge`)
    if (current(owner)) await load(owner)
  } catch (e) {
    if (current(owner)) error.value = errorMessage(e, t('common.error'))
  } finally {
    if (current(owner)) revoking.value = null
  }
}

function clearContext() {
  contextEpoch++; loadEpoch++
  selectedKey.value = null; keys.value = []; error.value = ''
  showCreate.value = false; createdKey.value = ''; creating.value = false; revoking.value = null
  copied.value = copyError.value = false
  clearTimeout(copyResetTimer)
  resetForm(); closeStepUp()
}
watch([() => auth.orgId, () => auth.token], () => { clearContext(); void load() }, { flush: 'sync' })
onBeforeUnmount(clearContext)
onMounted(load)
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('surface.keysEyebrow') }}</p>
        <h1 class="app-page-title">{{ t('keys.title') }}</h1>
        <p class="app-page-subtitle">{{ t('keys.controlsHint') }}</p>
      </div>
      <button class="btn-primary shrink-0" :disabled="creating || stepUp.busy" @click="openCreate">{{ t('keys.create') }}</button>
    </div>

    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <div class="grid gap-4 sm:grid-cols-3">
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-emerald"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M15.75 5.25a3 3 0 0 1 3 3m3 0a6 6 0 0 1-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1 1 21.75 8.25Z" /></svg></span>
          <p class="text-xs text-muted">{{ t('keys.active') }}</p>
        </div>
        <p class="mt-1 font-mono text-2xl font-semibold">{{ activeKeys }}</p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-violet"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 13.125C3 12.504 3.504 12 4.125 12h2.25c.621 0 1.125.504 1.125 1.125v6.75C7.5 20.496 6.996 21 6.375 21h-2.25A1.125 1.125 0 0 1 3 19.875v-6.75ZM9.75 8.625c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125v11.25c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V8.625ZM16.5 4.125c0-.621.504-1.125 1.125-1.125h2.25C20.496 3 21 3.504 21 4.125v15.75c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 0 1-1.125-1.125V4.125Z" /></svg></span>
          <p class="text-xs text-muted">{{ t('keys.limits') }}</p>
        </div>
        <p class="mt-1 font-mono text-2xl font-semibold">{{ restrictedKeys }}</p>
      </div>
      <div class="metric-card">
        <div class="flex items-center gap-3">
          <span class="metric-icon mi-amber"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 6v12m-3-2.818.879.659c1.171.879 3.07.879 4.242 0 1.172-.879 1.172-2.303 0-3.182C13.536 12.219 12.768 12 12 12c-.725 0-1.45-.22-2.003-.659-1.106-.879-1.106-2.303 0-3.182s2.9-.879 4.006 0l.415.33M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" /></svg></span>
          <p class="text-xs text-muted">{{ t('keys.quotaLimit') }}</p>
        </div>
        <p class="mt-1 font-mono text-2xl font-semibold">{{ cappedKeys }}</p>
      </div>
    </div>
    <div class="inline-success text-sm">
      {{ t('surface.keysSafety') }}
    </div>
    <div v-if="loading" class="app-panel p-5">
      <p class="text-sm text-muted">{{ t('common.loading') }}</p>
    </div>

    <div v-else class="data-table-wrap">
      <table class="table w-full text-sm">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3 font-medium">{{ t('keys.name') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('keys.prefix') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('keys.videoPermissionsTitle') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('keys.limits') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('keys.used') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('keys.status') }}</th>
            <th class="px-4 py-3"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="keys.length === 0">
            <td colspan="7" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
          </tr>
          <tr v-for="k in keys" :key="k.id" class="border-b border-line last:border-0">
            <td class="px-4 py-3">{{ k.name || '—' }}</td>
            <td class="px-4 py-3 font-mono text-muted">{{ k.prefix }}…</td>
            <td class="px-4 py-3 text-xs">
              <span :class="videoPermission(k.scopes) === 'none' ? 'badge-muted' : 'badge-ok'">{{ t(permissionLabels[videoPermission(k.scopes)]) }}</span>
            </td>
            <td class="px-4 py-3 text-xs">
              <span v-if="k.model_limits" class="badge-muted" :title="k.model_limits">{{ t('keys.modelLimited') }}</span>
              <span v-if="k.allow_ips" class="badge-muted ml-1" :title="k.allow_ips">IP</span>
              <span v-if="k.rpm_limit > 0" class="badge-muted ml-1">{{ k.rpm_limit }} RPM</span>
              <span v-if="k.route_policy && k.route_policy !== 'order'" class="badge-muted ml-1">{{ k.route_policy }}</span>
              <span v-if="!k.model_limits && !k.allow_ips && !(k.rpm_limit > 0) && (!k.route_policy || k.route_policy === 'order')" class="text-muted">—</span>
            </td>
            <td class="px-4 py-3 text-right font-mono text-xs">
              {{ fmtUsd(k.used_amount, { precision: 4 }) }}<span v-if="k.quota_limit !== '0'" class="text-muted"> / {{ fmtUsd(k.quota_limit, { precision: 2 }) }}</span>
            </td>
            <td class="px-4 py-3">
              <span :class="k.status === 1 ? 'badge-ok' : 'badge-muted'">
                {{ k.status === 1 ? t('keys.active') : t('keys.revoked') }}
              </span>
            </td>
            <td class="px-4 py-3 text-right">
              <button type="button" class="btn-ghost !py-1 text-xs" @click="selectedKey = k">{{ t('reqlog.btnDetail') }}</button>
              <button
                v-if="k.status === 1"
                class="btn-danger !py-1 text-xs"
                :disabled="revoking === k.id"
                @click="revoke(k.id)"
              >
                {{ t('keys.revoke') }}
              </button>
              <button
                v-else
                class="btn-ghost !py-1 text-xs"
                :disabled="revoking === k.id"
                @click="remove(k.id)"
              >
                {{ t('keys.delete') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <DetailDrawer :open="!!selectedKey" :title="selectedKey?.name || t('keys.title')" @update:open="open => { if (!open) selectedKey = null }">
      <dl v-if="selectedKey" class="detail-grid">
        <div><dt>ID</dt><dd>{{ selectedKey.id }}</dd></div><div><dt>{{ t('keys.prefix') }}</dt><dd>{{ selectedKey.prefix }}…</dd></div>
        <div><dt>{{ t('keys.name') }}</dt><dd>{{ selectedKey.name || '—' }}</dd></div><div><dt>{{ t('keys.status') }}</dt><dd>{{ t(selectedKey.status === 1 ? 'keys.active' : 'keys.revoked') }}</dd></div>
        <div class="col-span-2"><dt>{{ t('keys.videoPermissionsTitle') }}</dt><dd>{{ t(permissionLabels[videoPermission(selectedKey.scopes)]) }}</dd></div>
        <div class="col-span-2"><dt>{{ t('keys.scopeIdentifiers') }}</dt><dd class="font-mono break-all">{{ selectedKey.scopes || '—' }}</dd></div>
        <div><dt>{{ t('keys.used') }}</dt><dd>{{ fmtUsd(selectedKey.used_amount) }}</dd></div><div><dt>{{ t('currency.quota', { currency: displayCurrencyCode() }) }}</dt><dd>{{ Number(selectedKey.quota_limit) === 0 ? '∞' : fmtUsd(selectedKey.quota_limit) }}</dd></div>
        <div><dt>expires_at</dt><dd>{{ selectedKey.expires_at ? d(new Date(selectedKey.expires_at), { dateStyle: 'short', timeStyle: 'medium' }) : '—' }}</dd></div>
        <div><dt>{{ t('keys.rpmLimit') }}</dt><dd>{{ selectedKey.rpm_limit || '∞' }}</dd></div><div><dt>{{ t('keys.routePolicy') }}</dt><dd>{{ selectedKey.route_policy || 'order' }}</dd></div>
        <div class="col-span-2"><dt>{{ t('keys.modelLimitsField') }}</dt><dd>{{ selectedKey.model_limits || '—' }}</dd></div><div class="col-span-2"><dt>{{ t('keys.allowIps') }}</dt><dd>{{ selectedKey.allow_ips || '—' }}</dd></div>
      </dl>
      <div class="mt-5 space-y-3 rounded-xl border border-line p-4">
        <p class="text-sm text-muted">{{ t('keys.videoNewKeyHint') }}</p>
        <button type="button" class="btn-ghost" :disabled="creating || stepUp.busy" @click="openCreate">{{ t('keys.create') }}</button>
      </div>
      <p class="mt-5 text-sm text-muted">{{ t('surface.keysSafety') }}</p>
    </DetailDrawer>

    <!-- BYOK 自带上游 Key（0044） -->
    <ByokPanel />

    <!-- 创建弹窗 -->
    <div v-if="showCreate" class="modal-backdrop" @click.self="showCreate = false">
      <form
        ref="createDialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="create-key-title"
        class="app-panel modal-panel w-full max-w-lg max-h-[calc(100dvh-2rem)] overflow-y-auto p-6 space-y-4"
        @submit.prevent="create"
      >
        <h2 id="create-key-title" class="font-semibold">{{ t('keys.create') }}</h2>
        <div>
          <label class="label" for="keyname">{{ t('keys.name') }}</label>
          <input id="keyname" v-model="newName" class="input" :placeholder="t('keys.namePlaceholder')" />
        </div>
        <fieldset class="space-y-2" aria-describedby="key-video-permissions-hint">
          <legend class="label">{{ t('keys.videoPermissionsTitle') }}</legend>
          <label v-for="choice in videoChoices" :key="choice" class="flex cursor-pointer items-start gap-3 rounded-lg border p-3" :class="newVideoPermission === choice ? 'border-accent bg-accent/5' : 'border-line'">
            <input v-model="newVideoPermission" type="radio" name="key-video-permission" :value="choice" class="mt-1 shrink-0 accent-accent" />
            <span class="min-w-0">
              <span class="block text-sm font-medium">{{ t(permissionLabels[choice]) }}</span>
              <span class="mt-1 block text-xs text-muted">{{ t(permissionHints[choice]) }}</span>
            </span>
          </label>
          <p id="key-video-permissions-hint" class="text-xs text-muted">{{ t('keys.videoPermissionsHint') }}</p>
        </fieldset>
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <div>
            <label class="label" for="keyquota">{{ t('keys.quotaLimit') }}</label>
            <input id="keyquota" v-model="newQuota" class="input" placeholder="∞" />
          </div>
          <div>
            <label class="label" for="keyexp">{{ t('keys.expiry') }}</label>
            <input id="keyexp" v-model.number="newExpiry" class="input" type="number" min="0" placeholder="0" />
          </div>
          <div>
            <label class="label" for="keyrpm">{{ t('keys.rpmLimit') }}</label>
            <input id="keyrpm" v-model.number="newRPM" class="input" type="number" min="0" placeholder="∞" />
          </div>
        </div>
        <div>
          <label class="label" for="keymodels">{{ t('keys.modelLimitsField') }}</label>
          <input id="keymodels" v-model="newModels" class="input font-mono text-xs" placeholder="gpt-5.2, claude-4" />
        </div>
        <div>
          <label class="label" for="keyips">{{ t('keys.allowIps') }}</label>
          <input id="keyips" v-model="newIps" class="input font-mono text-xs" placeholder="1.2.3.4, 10.0.0.0/8" />
        </div>
        <div>
          <label class="label" for="keyroute">{{ t('keys.routePolicy') }}</label>
          <select id="keyroute" v-model="newRoute" class="input">
            <option value="">{{ t('keys.routeOrder') }}</option>
            <option value="latency">{{ t('keys.routeLatency') }}</option>
            <option value="throughput">{{ t('keys.routeThroughput') }}</option>
          </select>
          <p class="mt-1 text-xs text-muted">{{ t('keys.routeHint') }}</p>
        </div>
        <p class="text-xs text-muted">{{ t('keys.controlsHint') }}</p>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn-ghost" @click="showCreate = false">{{ t('common.cancel') }}</button>
          <button class="btn-primary" :disabled="creating">{{ t('common.confirm') }}</button>
        </div>
      </form>
    </div>

    <!-- 二次验证（step-up，仅运营开启时触发） -->
    <div v-if="stepUp.open" class="modal-backdrop" @click.self="closeStepUp">
      <form ref="stepUpDialog" role="dialog" aria-modal="true" aria-labelledby="key-stepup-title" class="app-panel modal-panel w-full max-w-sm max-h-[calc(100dvh-2rem)] overflow-y-auto p-6 space-y-4" @submit.prevent="submitStepUp">
        <h2 id="key-stepup-title" class="font-semibold">{{ t('keys.stepUpTitle') }}</h2>
        <p class="text-sm text-muted">{{ t('keys.stepUpHint') }}</p>
        <div>
          <label class="label" for="su-pwd">{{ t('keys.stepUpPassword') }}</label>
          <input id="su-pwd" v-model="stepUp.pwd" type="password" class="input" autocomplete="current-password" autofocus />
        </div>
        <div>
          <label class="label" for="su-code">{{ t('keys.stepUpCode') }}</label>
          <input id="su-code" v-model="stepUp.code" class="input font-mono" inputmode="numeric" maxlength="6" :placeholder="t('keys.stepUpCodePh')" />
        </div>
        <p v-if="stepUp.err" class="text-sm text-danger" role="alert">{{ stepUp.err }}</p>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn-ghost" @click="closeStepUp">{{ t('common.cancel') }}</button>
          <button class="btn-primary" :disabled="stepUp.busy">{{ t('keys.stepUpVerify') }}</button>
        </div>
      </form>
    </div>

    <!-- 明文一次性展示 -->
    <div v-if="createdKey" class="modal-backdrop">
      <div
        ref="createdDialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="created-key-title"
        class="app-panel modal-panel w-full max-w-lg p-6 space-y-4"
      >
        <h2 id="created-key-title" class="font-semibold">{{ t('keys.createdTitle') }}</h2>
        <p class="text-sm text-danger">{{ t('keys.createdWarning') }}</p>
        <div class="flex gap-2">
          <code class="input font-mono text-xs break-all select-all">{{ createdKey }}</code>
          <button class="btn-ghost shrink-0" @click="copyKey">
            {{ copied ? t('common.copied') : t('common.copy') }}
          </button>
        </div>
        <p v-if="copyError" class="text-xs text-danger" role="alert">{{ t('common.copyFailed') }}</p>
        <div class="flex justify-end">
          <button class="btn-primary" @click="createdKey = ''">{{ t('common.close') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>
