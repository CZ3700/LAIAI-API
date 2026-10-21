<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, ApiError, errorMessage, uuid } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import { cellKey, isOfficialVideoModel, type InitialPricingSetup, type InitialProviderScope, type PriceCell, type SupplierBudget } from './types'
import { exactRate } from './videoPriceDecimal'

const props = defineProps<{ setup: InitialPricingSetup; model: string; channelId: number; contextKey: string }>()
const emit = defineEmits<{ published: [model: string, channelId: number, contextKey: string]; 'busy-change': [value: boolean]; 'dirty-change': [value: boolean]; reload: [] }>()
const { t } = useI18n(), auth = useAuth()
type PricingMode = 'official' | 'template'
interface InitialPreview {
  model: string; channel_id: number; pricing_mode: PricingMode; publishable: boolean; live_tested: false
  config_version: string; pricing_version: string; cells: PriceCell[]; template_ref: { version: string; model: string; sha256: string }
  minimum_rule_version: string; minimum_cell_count: number; provider_scope: InitialProviderScope; supplier_budgets: SupplierBudget[]
  enabled: boolean; publication: Record<string, unknown>
}
interface PublicationResult { public_model: string; version: string; pricing_version: string; provider_scope_id: number }
interface Readback { model: string; channel_id: number; config_version: string; pricing_version: string }
const form = ref({ templateVersion: '', mode: 'official' as PricingMode, scopeId: 0, account: '', project: '', fx: '', daily: '', monthly: '', identityConfirmed: false, fxConfirmed: false, enabled: false })
const preview = ref<InitialPreview | null>(null), confirmed = ref(false), busy = ref(false), preparing = ref(false), retryPending = ref(false), requiresReload = ref(false), error = ref(''), notice = ref('')
const snapshot = ref(''), idempotencyKey = ref(''), initialForm = ref('')
let generation = 0, active = true
const selectedTemplate = computed(() => props.setup.templates.find(item => item.version === form.value.templateVersion))
const selectedScope = computed(() => props.setup.provider_scopes.find(item => item.id === form.value.scopeId))
const allowed = computed(() => active && auth.isSuperadmin && !!auth.token && isOfficialVideoModel(props.model) && props.setup.model === props.model && props.setup.channel_id === props.channelId && !props.setup.configuration_exists)
const dirty = computed(() => JSON.stringify(form.value) !== initialForm.value || !!preview.value || retryPending.value)
const locked = computed(() => busy.value || retryPending.value)
const hasMinimum = computed(() => !!selectedTemplate.value?.minimum_rule_version && (selectedTemplate.value?.minimum_cell_count ?? 0) > 0)
function positive(value: string, precision: number) { return new RegExp(`^(0|[1-9][0-9]*)(\\.[0-9]{1,${precision}})?$`).test(value) && /[1-9]/.test(value) }
const formValid = computed(() => {
  if (!allowed.value || !props.setup.available || !selectedTemplate.value || !hasMinimum.value || !form.value.identityConfirmed) return false
  if (form.value.scopeId) return !!selectedScope.value?.enabled
  return /^[1-9][0-9]{0,63}$/.test(form.value.account.trim()) && !!form.value.project.trim() && form.value.project.trim().length <= 128
    && positive(form.value.fx, 12) && positive(form.value.daily, 8) && positive(form.value.monthly, 8) && form.value.fxConfirmed
})
const canPreview = computed(() => formValid.value && !locked.value && !requiresReload.value)
const canPublish = computed(() => allowed.value && preview.value?.enabled === true && confirmed.value && !!snapshot.value && !!idempotencyKey.value && !busy.value && !requiresReload.value)
const blockerKeys: Record<string, string> = {
  configuration_exists: 'initialBlockExists', channel_unavailable: 'initialBlockChannel', channel_model_mismatch: 'initialBlockIdentity', channel_credential_required: 'initialBlockCredential',
  provider_scope_required: 'initialBlockScope', provider_scope_exists: 'initialBlockScopeExists', provider_scope_unavailable: 'initialBlockScopeUnavailable', supplier_budgets_required: 'initialBlockBudgets', scope_fields_conflict: 'initialBlockScopeConflict',
  minimum_evidence_conflict: 'initialBlockMinimum', initial_setup_changed: 'initialBlockChanged', initial_preview_expired: 'initialBlockExpired', initial_preview_invalid: 'initialBlockChanged', template_unavailable: 'initialBlockTemplate', video_runtime_required: 'initialBlockRuntime', pricing_mode_invalid: 'initialBlockMode',
}
function blockerMessage(code: string) { return t(`pricingAdmin.${blockerKeys[code] ?? 'initialBlockUnknown'}`) }
function message(e: unknown, fallback: string) {
  if (e instanceof ApiError && e.code && blockerKeys[e.code]) return `${blockerMessage(e.code)}${e.requestId ? ` (ID: ${e.requestId})` : ''}`
  return errorMessage(e, t(`pricingAdmin.${fallback}`))
}
function clearPreview() {
  ++generation; preview.value = null; confirmed.value = false; snapshot.value = ''; idempotencyKey.value = ''; error.value = ''; notice.value = ''; requiresReload.value = false; busy.value = false; preparing.value = false
}
function reset() {
  clearPreview(); busy.value = false; preparing.value = false; retryPending.value = false
  const template = props.setup.templates[0]
  form.value = { templateVersion: template?.version ?? '', mode: 'official', scopeId: 0, account: '', project: '', fx: template?.fx_usd_per_cny ?? '', daily: '', monthly: '', identityConfirmed: false, fxConfirmed: false, enabled: false }
  initialForm.value = JSON.stringify(form.value)
}
function previewRequest() {
  const value = form.value
  const shared = { channel_id: props.channelId, template_version: value.templateVersion, pricing_mode: value.mode, provider_scope_id: value.scopeId, enabled: value.enabled }
  return value.scopeId ? shared : { ...shared, account_ref: value.account.trim(), project_name: value.project.trim(), fx_usd_per_cny: value.fx, daily_budget_cny: value.daily, monthly_budget_cny: value.monthly }
}
function matchesPreview(value: InitialPreview) {
  const scope = value.provider_scope
  const identityMatches = form.value.scopeId
    ? scope?.id === selectedScope.value?.id && scope?.account_ref === selectedScope.value?.account_ref && scope?.project_name === selectedScope.value?.project_name
    : scope?.account_ref === form.value.account.trim() && scope?.project_name === form.value.project.trim() && exactRate(scope.fx_usd_per_cny) === exactRate(form.value.fx)
  return value.model === props.model && value.channel_id === props.channelId && value.pricing_mode === form.value.mode && value.enabled === form.value.enabled
    && value.publishable === true && value.live_tested === false && !!value.config_version && !!value.pricing_version
    && value.template_ref?.version === form.value.templateVersion && value.template_ref?.model === props.model && /^[a-f0-9]{64}$/.test(value.template_ref.sha256)
    && !!value.minimum_rule_version && Number.isSafeInteger(value.minimum_cell_count) && value.minimum_cell_count > 0
    && Array.isArray(value.cells) && value.cells.length > 0 && new Set(value.cells.map(cellKey)).size === value.cells.length
    && identityMatches && Array.isArray(value.supplier_budgets) && value.supplier_budgets.length > 0
    && !!value.publication && typeof value.publication === 'object' && !Array.isArray(value.publication)
}
async function preparePreview() {
  if (!active || !canPreview.value) return
  clearPreview()
  const current = generation, model = props.model, body = previewRequest()
  busy.value = true; preparing.value = true
  try {
    const value = await api<InitialPreview>('POST', `/api/admin/video/models/${encodeURIComponent(model)}/pricing/initial-preview`, body)
    if (!active || current !== generation) return
    if (!matchesPreview(value)) throw new Error('initial_preview_mismatch')
    exactRate(value.provider_scope.fx_usd_per_cny)
    for (const cell of value.cells) { exactRate(cell.retail_usd_per_1m); exactRate(cell.supplier_cny_per_1m); exactRate(cell.risk_multiplier) }
    preview.value = value; snapshot.value = JSON.stringify(value.publication); idempotencyKey.value = uuid()
  } catch (e) {
    if (active && current === generation) {
      error.value = message(e, 'initialPreviewFailed')
      if (e instanceof ApiError && e.code && ['configuration_exists', 'initial_setup_changed', 'provider_scope_exists'].includes(e.code)) requiresReload.value = true
    }
  } finally { if (active && current === generation) { busy.value = false; preparing.value = false } }
}
async function readback(expected: InitialPreview, current: number) {
  const value = await api<Readback>('GET', `/api/admin/video/models/${encodeURIComponent(expected.model)}/pricing`)
  return active && current === generation && value.model === expected.model && value.channel_id === expected.channel_id && value.config_version === expected.config_version && value.pricing_version === expected.pricing_version
}
function completed(current: number, model: string, channel: number, context: string) {
  if (!active || current !== generation) return
  retryPending.value = false; snapshot.value = ''; idempotencyKey.value = ''; preview.value = null; confirmed.value = false
  initialForm.value = JSON.stringify(form.value); notice.value = t('pricingAdmin.initialPublished')
  emit('published', model, channel, context)
}
async function publish() {
  if (!active || !canPublish.value || !preview.value) return
  const current = generation, expected = preview.value, model = props.model, channel = props.channelId, context = props.contextKey, key = idempotencyKey.value, body = JSON.parse(snapshot.value)
  busy.value = true; error.value = ''; notice.value = ''
  try {
    // A lost response may already have committed. Reconcile before replaying the same operation.
    if (retryPending.value) {
      try { if (await readback(expected, current)) { completed(current, model, channel, context); return } } catch { /* Replay remains tied to the original body and key. */ }
      if (!active || current !== generation) return
    }
    const result = await api<PublicationResult>('POST', '/api/admin/video/configurations', body, { headers: { 'Idempotency-Key': key } })
    if (!active || current !== generation) return
    if (result.public_model !== model || result.version !== expected.config_version || result.pricing_version !== expected.pricing_version || !Number.isSafeInteger(result.provider_scope_id) || result.provider_scope_id <= 0) throw new Error('initial_publication_unconfirmed')
    if (!await readback(expected, current)) throw new Error('initial_publication_readback_mismatch')
    completed(current, model, channel, context)
  } catch (e) {
    if (!active || current !== generation) return
    try { if (await readback(expected, current)) { completed(current, model, channel, context); return } } catch { /* Keep the immutable request for an explicit retry. */ }
    if (!active || current !== generation) return
    if (e instanceof ApiError && (e.status === 409 || e.status === 422)) { requiresReload.value = true; retryPending.value = false; error.value = message(e, 'initialPublishConflict') }
    else { retryPending.value = true; error.value = message(e, 'initialPublishUnconfirmed') }
  } finally { if (active && current === generation) busy.value = false }
}
function periodLabel(period: string) { return t(`pricingAdmin.${period === 'day' ? 'initialDailyBudget' : period === 'month' ? 'initialMonthlyBudget' : period === 'lifetime' ? 'initialLifetimeBudget' : 'initialOtherBudget'}`) }
function budgetAmount(budget: SupplierBudget) {
  if (budget.metric === 'cny' || budget.metric === 'usd') return `${budget.limit_amount ?? '—'} ${budget.metric.toUpperCase()}`
  if (budget.metric === 'tokens') return `${budget.limit_units ?? '—'} Token`
  if (budget.metric === 'tasks') return `${budget.limit_units ?? '—'} ${t('pricingAdmin.initialTasks')}`
  return '—'
}
function historicalCell(cell: PriceCell) { return selectedTemplate.value?.cells.find(item => cellKey(item) === cellKey(cell)) }
function reload() { if (!busy.value && !retryPending.value) emit('reload') }
watch(form, () => { if (!retryPending.value) clearPreview() }, { deep: true, flush: 'sync' })
watch(() => [props.model, props.channelId, props.contextKey, props.setup, auth.orgId, auth.token, auth.role, auth.isSuperadmin], reset, { flush: 'sync', immediate: true })
watch(busy, value => emit('busy-change', value), { flush: 'sync' })
watch(dirty, value => emit('dirty-change', value), { flush: 'sync' })
onActivated(() => { active = true })
onDeactivated(() => { active = false; reset() })
onBeforeUnmount(() => { active = false; reset(); emit('busy-change', false); emit('dirty-change', false) })
</script>

<template>
  <section class="initial-setup" :aria-busy="busy">
    <header><h3>{{ t('pricingAdmin.initialTitle') }}</h3><span>{{ setup.channel_name }} · #{{ channelId }}</span></header>
    <p>{{ t('pricingAdmin.initialHelp') }}</p>
    <ul v-if="setup.blockers.length" class="setup-warning" role="alert"><li v-for="code in setup.blockers" :key="code">{{ blockerMessage(code) }}</li></ul>
    <fieldset :disabled="locked || !allowed">
      <legend>{{ t('pricingAdmin.initialAccountTitle') }}</legend>
      <label>{{ t('pricingAdmin.initialScope') }}<select v-model.number="form.scopeId" class="input"><option :value="0">{{ t('pricingAdmin.initialNewScope') }}</option><option v-for="scope in setup.provider_scopes" :key="scope.id" :value="scope.id" :disabled="!scope.enabled">{{ scope.account_ref }} · {{ scope.project_name }} · {{ scope.region }}{{ scope.enabled ? '' : ` (${t('pricingAdmin.disabled')})` }}</option></select></label>
      <template v-if="selectedScope">
        <p class="setup-note">{{ t('pricingAdmin.initialSharedScope') }}</p>
        <dl><dt>{{ t('pricingAdmin.initialAccount') }}</dt><dd>{{ selectedScope.account_ref }}</dd><dt>{{ t('pricingAdmin.initialProject') }}</dt><dd>{{ selectedScope.project_name }} · {{ selectedScope.region }}</dd><dt>{{ t('pricingAdmin.variantPlanFX') }}</dt><dd>{{ selectedScope.fx_usd_per_cny }} USD / CNY<small>{{ selectedScope.fx_version }}</small></dd></dl>
        <ul class="budget-list"><li v-for="budget in selectedScope.supplier_budgets" :key="budget.id">{{ periodLabel(budget.period) }}: {{ budgetAmount(budget) }} · {{ budget.timezone }} · {{ budget.model || t('pricingAdmin.initialAllModels') }}<small>{{ budget.version }}</small></li></ul>
      </template>
      <div v-else class="field-grid">
        <label>{{ t('pricingAdmin.initialAccount') }}<input v-model.trim="form.account" class="input" inputmode="numeric" autocomplete="off" maxlength="64" :placeholder="t('pricingAdmin.initialAccountPlaceholder')"></label>
        <label>{{ t('pricingAdmin.initialProject') }}<input v-model.trim="form.project" class="input" autocomplete="off" maxlength="128" :placeholder="t('pricingAdmin.initialProjectPlaceholder')"></label>
        <label>{{ t('pricingAdmin.initialDailyBudget') }} (CNY)<input v-model="form.daily" class="input" inputmode="decimal" autocomplete="off"></label>
        <label>{{ t('pricingAdmin.initialMonthlyBudget') }} (CNY)<input v-model="form.monthly" class="input" inputmode="decimal" autocomplete="off"></label>
        <p class="full-field">{{ t('pricingAdmin.initialBudgetHelp') }}</p>
        <label class="full-field">{{ t('pricingAdmin.variantPlanFX') }} (USD / CNY)<input v-model="form.fx" class="input" inputmode="decimal" autocomplete="off"><small>{{ t('pricingAdmin.initialFXReference', { rate: selectedTemplate?.fx_usd_per_cny || '—', date: selectedTemplate?.fx_observed_at || '—' }) }}</small></label>
        <label class="check full-field"><input v-model="form.fxConfirmed" type="checkbox">{{ t('pricingAdmin.initialFXConfirm') }}</label>
      </div>
      <label class="check"><input v-model="form.identityConfirmed" type="checkbox">{{ t('pricingAdmin.initialIdentityConfirm') }}</label>
    </fieldset>
    <fieldset :disabled="locked || !allowed">
      <legend>{{ t('pricingAdmin.initialPricingTitle') }}</legend>
      <label>{{ t('pricingAdmin.initialTemplate') }}<select v-model="form.templateVersion" class="input"><option v-for="template in setup.templates" :key="template.version" :value="template.version">{{ template.label }} · {{ template.version }}</option></select></label>
      <label>{{ t('pricingAdmin.initialPricingMode') }}<select v-model="form.mode" class="input"><option value="official">{{ t('pricingAdmin.initialOfficialMode') }}</option><option value="template">{{ t('pricingAdmin.initialTemplateMode') }}</option></select></label>
      <p class="setup-note">{{ t(form.mode === 'official' ? 'pricingAdmin.initialOfficialHelp' : 'pricingAdmin.initialTemplateHelp') }}</p>
      <p>{{ t('pricingAdmin.initialMinimum', { version: selectedTemplate?.minimum_rule_version || '—', count: selectedTemplate?.minimum_cell_count || 0 }) }}</p>
      <label class="check"><input v-model="form.enabled" type="checkbox">{{ t('pricingAdmin.initialEnable') }}</label>
      <p v-if="!form.enabled" class="setup-note" role="status">{{ t('pricingAdmin.initialEnableRequired') }}</p>
      <p>{{ t('pricingAdmin.initialNotTested') }}</p>
    </fieldset>
    <section v-if="preview" class="setup-preview">
      <h3>{{ t('pricingAdmin.initialPreviewTitle') }}</h3>
      <dl><dt>{{ t('pricingAdmin.initialAccount') }}</dt><dd>{{ preview.provider_scope.account_ref }}</dd><dt>{{ t('pricingAdmin.initialProject') }}</dt><dd>{{ preview.provider_scope.project_name }} · {{ preview.provider_scope.region }}</dd><dt>{{ t('pricingAdmin.variantPlanFX') }}</dt><dd>{{ preview.provider_scope.fx_usd_per_cny }} USD / CNY</dd><dt>{{ t('pricingAdmin.status') }}</dt><dd>{{ t(preview.enabled ? 'pricingAdmin.rebindEnabled' : 'pricingAdmin.rebindDisabled') }}</dd></dl>
      <ul class="budget-list"><li v-for="(budget, index) in preview.supplier_budgets" :key="index">{{ periodLabel(budget.period) }}: {{ budgetAmount(budget) }} · {{ budget.timezone }} · {{ budget.model || t('pricingAdmin.initialAllModels') }}</li></ul>
      <p>{{ t('pricingAdmin.initialMinimum', { version: preview.minimum_rule_version, count: preview.minimum_cell_count }) }}</p>
      <div class="setup-table"><table><thead><tr><th>{{ t('pricingAdmin.resolution') }}</th><th>{{ t('pricingAdmin.referenceVideo') }}</th><th>{{ t('pricingAdmin.variantPlanCost') }}<small>CNY / 1M Token</small></th><th>{{ t('pricingAdmin.price') }}<small>USD / 1M Token</small></th><th>{{ t('pricingAdmin.variantPlanRisk') }}</th><th>{{ t('pricingAdmin.variantPlanMinimum') }}</th><th>{{ t('pricingAdmin.initialTemplateReference') }}</th></tr></thead><tbody><tr v-for="cell in preview.cells" :key="cellKey(cell)"><td>{{ cell.resolution }}</td><td>{{ t(cell.input_video ? 'pricingAdmin.withReferenceVideo' : 'pricingAdmin.withoutReferenceVideo') }}</td><td>{{ cell.supplier_cny_per_1m }}</td><td>{{ cell.retail_usd_per_1m }}</td><td>{{ cell.risk_multiplier }}</td><td>{{ cell.minimum_rule_version || '—' }}</td><td>{{ historicalCell(cell)?.retail_usd_per_1m || '—' }} USD / 1M<small>{{ t('pricingAdmin.variantPlanRisk') }}: {{ historicalCell(cell)?.risk_multiplier || '—' }}</small></td></tr></tbody></table></div>
      <label class="check"><input v-model="confirmed" type="checkbox" :disabled="busy || retryPending">{{ t('pricingAdmin.initialPublishConfirm') }}</label>
    </section>
    <p v-if="error" role="alert" class="setup-error">{{ error }}</p><p v-if="retryPending" role="status" class="setup-note">{{ t('pricingAdmin.initialRetryNotice') }}</p><p v-if="notice" role="status">{{ notice }}</p>
    <footer><button v-if="requiresReload || setup.blockers.length" class="btn-ghost" type="button" :disabled="locked" @click="reload">{{ t('pricingx.reload') }}</button><button class="btn-ghost" type="button" :disabled="!canPreview" @click="preparePreview">{{ t(preparing ? 'common.loading' : 'pricingAdmin.initialPreviewAction') }}</button><button v-if="preview" class="btn-primary" type="button" :disabled="!canPublish" @click="publish">{{ t(busy && !preparing ? 'common.loading' : retryPending ? 'pricingAdmin.rebindRetry' : 'pricingAdmin.initialPublish') }}</button></footer>
  </section>
</template>

<style scoped>
.initial-setup{display:grid;gap:18px;min-width:0}.initial-setup header{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap}.initial-setup h3{font-size:16px;margin:0}.initial-setup header span,.initial-setup p,.initial-setup small{font-size:12px;color:rgb(var(--c-muted));line-height:1.65}.initial-setup p{margin:0}.initial-setup small{display:block}.initial-setup fieldset{display:grid;gap:14px;border:1px solid rgb(var(--c-line));border-radius:10px;padding:18px;min-width:0}.initial-setup legend{font-size:14px;font-weight:600;padding:0 7px}.initial-setup label{display:grid;gap:6px;font-size:12px}.initial-setup .input{width:100%;min-width:0}.field-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}.full-field{grid-column:1/-1}.initial-setup .check{display:flex;align-items:flex-start;gap:9px;line-height:1.65}.check input{margin-top:4px;flex-shrink:0}.setup-note,.setup-warning{padding:12px;background:rgb(var(--c-elev));border-radius:8px}.setup-warning{margin:0;padding-left:28px;font-size:12px}.initial-setup dl{display:grid;grid-template-columns:140px minmax(0,1fr);gap:8px;margin:0;font-size:12px}.initial-setup dt{color:rgb(var(--c-muted))}.initial-setup dd{margin:0;overflow-wrap:anywhere}.budget-list{font-size:12px;margin:0;padding-left:18px;line-height:1.7}.setup-preview{display:grid;gap:14px;border-top:1px solid rgb(var(--c-line));padding-top:18px}.setup-table{overflow:auto}.setup-table table{width:100%;border-collapse:collapse;font-size:12px}.setup-table th,.setup-table td{padding:9px;text-align:left;border-bottom:1px solid rgb(var(--c-line));font-variant-numeric:tabular-nums}.setup-table th{font-weight:500;color:rgb(var(--c-muted))}.setup-table small{white-space:nowrap}.initial-setup .setup-error{color:rgb(var(--c-danger))}.initial-setup footer{display:flex;justify-content:flex-end;flex-wrap:wrap;gap:10px}@media(max-width:600px){.field-grid{grid-template-columns:1fr}.initial-setup fieldset{padding:12px}.initial-setup dl{grid-template-columns:1fr;gap:5px}.initial-setup dd{margin-bottom:7px}.setup-table table{min-width:600px}}
</style>
