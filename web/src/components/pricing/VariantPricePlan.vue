<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, ApiError, errorMessage, uuid } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import { officialPriceEvidence } from './catalogPresentation'
import type { PricingModel, VariantPricePlan, VariantPriceProposal } from './types'

const props = defineProps<{ row: PricingModel; contextKey: string }>()
const emit = defineEmits<{ saved: [rowKey: string, contextKey: string]; 'busy-change': [value: boolean] }>()
const { t } = useI18n(), auth = useAuth()
const data = ref<VariantPricePlan | null>(null), loading = ref(false), busy = ref(false)
const error = ref(''), notice = ref(''), requiresReload = ref(false), retryPending = ref(false)
const idempotencyKey = ref(''), requestSnapshot = ref<{ expected_context_sha256: string } | null>(null)
const evidence = computed(() => officialPriceEvidence(props.row))
const allowed = computed(() => auth.isSuperadmin && !!auth.token && !!evidence.value && !props.row.video_configuration && props.row.unsupported_reason === 'official_video_configuration_missing' && Number.isSafeInteger(props.row.channel_id) && props.row.channel_id > 0)
const endpoint = computed(() => `/api/admin/video/variant-price-plans/${props.row.channel_id}/${encodeURIComponent(props.row.model)}`)
const canSave = computed(() => allowed.value && !!data.value && !!requestSnapshot.value && !data.value.saved_matches_proposal && !loading.value && !busy.value && !requiresReload.value)
const statusKey = computed(() => data.value?.saved_matches_proposal ? 'variantPlanSavedState' : data.value?.saved ? 'variantPlanUpdatePending' : 'variantPlanUnsaved')
let generation = 0, active = true
type Price = VariantPriceProposal['prices'][number]
function priceKey(price: Pick<Price, 'resolution' | 'input_video'>) { return `${price.resolution}/${price.input_video}` }
function previousPrice(price: Price) { return data.value?.saved?.proposal.prices.find(saved => priceKey(saved) === priceKey(price)) }
function minimumLabel(price: Price) { return t(price.input_video ? 'pricingAdmin.variantPlanMinimumUnverified' : 'pricingAdmin.variantPlanMinimumNotApplicable') }
function matchesContext(value: VariantPricePlan) {
  return value.channel_id === props.row.channel_id && value.model === props.row.model && value.upstream_model === props.row.upstream_model
    && value.canonical_model === evidence.value?.model && value.adapter_status === 'supported_configuration_required' && value.invocable === false
    && /^[a-f0-9]{64}$/i.test(value.context_sha256) && validProposal(value.proposal) && (!value.saved || validProposal(value.saved.proposal))
}
function validProposal(value: VariantPriceProposal) {
  if (!value || value.risk_source !== 'existing_standard_2_0_policy' || value.minimum_status !== 'unverified' || !Array.isArray(value.prices) || value.prices.length !== 4) return false
  const keys = new Set(value.prices.map(priceKey))
  return ['480p/false', '480p/true', '720p/false', '720p/true'].every(key => keys.has(key))
    && value.prices.every(price => [price.retail_usd_per_1m, price.supplier_cny_per_1m, price.risk_multiplier].every(amount => typeof amount === 'string' && /^(0|[1-9]\d*)(\.\d+)?$/.test(amount) && /[1-9]/.test(amount)))
}
function reset() {
  ++generation; data.value = null; loading.value = false; busy.value = false; error.value = ''; notice.value = ''
  requiresReload.value = false; retryPending.value = false; requestSnapshot.value = null; idempotencyKey.value = ''
}
async function load() {
  if (busy.value || loading.value || retryPending.value || !active) return
  reset()
  if (!allowed.value) return
  const current = generation, path = endpoint.value
  loading.value = true
  try {
    const result = await api<VariantPricePlan>('GET', path)
    if (current !== generation || !active) return
    if (!matchesContext(result)) throw new Error('variant_plan_response_mismatch')
    data.value = result; requestSnapshot.value = { expected_context_sha256: result.context_sha256 }; idempotencyKey.value = uuid()
  } catch (e) { if (current === generation && active) error.value = errorMessage(e, t('pricingAdmin.variantPlanLoadFailed')) }
  finally { if (current === generation && active) loading.value = false }
}
async function save() {
  if (!canSave.value || !requestSnapshot.value) return
  const current = generation, path = endpoint.value, rowKey = props.row.key, contextKey = props.contextKey
  const request = { ...requestSnapshot.value }, key = idempotencyKey.value
  busy.value = true; error.value = ''; notice.value = ''; retryPending.value = false
  try {
    const result = await api<VariantPricePlan>('POST', path, request, { headers: { 'Idempotency-Key': key } })
    if (current !== generation || !active) return
    if (!matchesContext(result) || !result.saved || !result.saved_matches_proposal) throw new Error('variant_plan_save_unconfirmed')
    data.value = result; notice.value = t('pricingAdmin.variantPlanSavedNotice')
    emit('saved', rowKey, contextKey)
  } catch (e) {
    if (current !== generation || !active) return
    if (e instanceof ApiError && e.status === 409) { requiresReload.value = true; error.value = t('pricingAdmin.variantPlanConflict') }
    else { retryPending.value = true; error.value = errorMessage(e, t('pricingAdmin.variantPlanSaveFailed')) }
  } finally { if (current === generation && active) busy.value = false }
}
function sourceURL(value: string) {
  try { const url = new URL(value); return url.protocol === 'https:' && !url.username && !url.password ? url.href : undefined } catch { return undefined }
}
watch(busy, value => emit('busy-change', value), { flush: 'sync' })
watch(() => [props.row.key, props.row.channel_id, props.row.model, props.row.upstream_model, props.row.video_model, props.row.video_configuration, props.row.unsupported_reason, evidence.value?.model, props.contextKey, auth.orgId, auth.token, auth.role, auth.isSuperadmin], () => { reset(); if (active) void load() }, { flush: 'sync' })
onMounted(load)
onActivated(() => { active = true; if (!data.value && !loading.value) void load() })
onDeactivated(() => { active = false; reset() })
onBeforeUnmount(() => { active = false; reset() })
</script>

<template>
  <section class="variant-plan" :aria-busy="loading || busy">
    <header><h3>{{ t('pricingAdmin.variantPlanTitle') }}</h3><span v-if="data" class="plan-state">{{ t(`pricingAdmin.${statusKey}`) }}</span></header>
    <p>{{ t('pricingAdmin.variantPlanHelp') }}</p>
    <p v-if="!allowed" class="plan-note">{{ t('pricingx.superadmin') }}</p><p v-else-if="loading" role="status">{{ t('pricingAdmin.variantPlanLoading') }}</p>
    <template v-if="data">
      <dl class="plan-metadata"><dt>{{ t('pricingAdmin.variantPlanFX') }}</dt><dd>{{ data.proposal.fx_usd_per_cny }} USD / CNY<small>{{ data.proposal.fx_observed_at }}</small></dd><dt>{{ t('pricingAdmin.variantPlanRiskSource') }}</dt><dd>{{ t('pricingAdmin.variantPlanRiskStandard') }}</dd><dt>{{ t('pricingAdmin.variantPlanMinimum') }}</dt><dd>{{ t('pricingAdmin.variantPlanMinimumUnverified') }}</dd></dl>
      <p class="plan-note">{{ t('pricingAdmin.variantPlanFormula') }}</p>
      <div class="plan-table"><table><caption>{{ t(data.saved_matches_proposal ? 'pricingAdmin.variantPlanSavedTable' : 'pricingAdmin.variantPlanProposalTable') }}</caption><thead><tr><th>{{ t('pricingAdmin.resolution') }}</th><th>{{ t('pricingAdmin.referenceVideo') }}</th><th>{{ t('pricingAdmin.variantPlanRetail') }}<small>USD / 1M Token</small></th><th>{{ t('pricingAdmin.variantPlanCost') }}<small>CNY / 1M Token</small></th><th>{{ t('pricingAdmin.variantPlanRisk') }}</th><th>{{ t('pricingAdmin.variantPlanMinimum') }}</th></tr></thead><tbody><tr v-for="price in data.proposal.prices" :key="priceKey(price)"><td>{{ price.resolution }}</td><td>{{ t(price.input_video ? 'pricingAdmin.withReferenceVideo' : 'pricingAdmin.withoutReferenceVideo') }}</td><td>{{ price.retail_usd_per_1m }}<small v-if="data.saved && !data.saved_matches_proposal">{{ t('pricingAdmin.variantPlanPrevious') }}: {{ previousPrice(price)?.retail_usd_per_1m ?? '—' }}</small></td><td>{{ price.supplier_cny_per_1m }}<small v-if="data.saved && !data.saved_matches_proposal">{{ t('pricingAdmin.variantPlanPrevious') }}: {{ previousPrice(price)?.supplier_cny_per_1m ?? '—' }}</small></td><td>{{ price.risk_multiplier }}<small v-if="data.saved && !data.saved_matches_proposal">{{ t('pricingAdmin.variantPlanPrevious') }}: {{ previousPrice(price)?.risk_multiplier ?? '—' }}</small></td><td>{{ minimumLabel(price) }}</td></tr></tbody></table></div>
      <div v-if="data.saved" class="saved-version"><strong>{{ t('pricingAdmin.variantPlanSavedVersion') }}</strong><span>{{ data.saved.version }} · #{{ data.saved.created_by }} · {{ data.saved.created_at }}</span><small v-if="!data.saved_matches_proposal">{{ t('pricingAdmin.variantPlanPreviousFX') }}: {{ data.saved.proposal.fx_usd_per_cny }} USD / CNY · {{ data.saved.proposal.fx_version }}</small></div>
      <details class="plan-sources"><summary>{{ t('pricingAdmin.variantPlanSources') }}</summary><dl class="plan-metadata source-metadata"><dt>{{ t('pricingAdmin.variantPlanFX') }}</dt><dd>{{ data.proposal.fx_version }}</dd><dt>{{ t('pricingAdmin.variantPlanSourceVersion') }}</dt><dd>{{ data.proposal.source_config_version }}<small>{{ data.proposal.source_pricing_version }}</small></dd></dl><code>{{ data.proposal.evidence_sha256 }}</code><article v-for="source in data.proposal.sources" :key="source.id"><strong>{{ source.id }}</strong><a v-if="sourceURL(source.url)" :href="sourceURL(source.url)" target="_blank" rel="noopener noreferrer">{{ t('pricingAdmin.officialSource') }} ↗</a><span v-else>{{ source.url }}</span><code>SHA-256 · {{ source.sha256 }}</code></article></details>
    </template>
    <p v-if="error" role="alert" class="plan-error">{{ error }}</p><p v-if="retryPending" class="plan-note">{{ t('pricingAdmin.variantPlanRetry') }}</p><p v-if="notice" role="status" class="plan-success">{{ notice }}</p>
    <footer v-if="allowed"><button type="button" class="btn-ghost" :disabled="busy || loading || retryPending" @click="load">{{ t('pricingx.reload') }}</button><button type="button" class="btn-primary" :disabled="!canSave" @click="save">{{ busy ? t('common.loading') : t(retryPending ? 'pricingAdmin.variantPlanRetrySave' : 'pricingAdmin.variantPlanSave') }}</button></footer>
  </section>
</template>

<style scoped>
.variant-plan{display:grid;gap:14px;border:1px solid rgb(var(--c-line));border-radius:10px;padding:18px;min-width:0}.variant-plan header{display:flex;justify-content:space-between;gap:12px;align-items:center;flex-wrap:wrap}.variant-plan h3,.variant-plan p{margin:0}.variant-plan h3{font-size:16px}.variant-plan p{color:rgb(var(--c-muted));font-size:12px;line-height:1.65}.plan-state{font-size:11px;padding:5px 8px;background:rgb(var(--c-elev));border-radius:6px;color:rgb(var(--c-muted))}.plan-metadata{display:grid;grid-template-columns:140px minmax(0,1fr);gap:12px;font-size:12px;margin:0}.plan-metadata dt{color:rgb(var(--c-muted))}.plan-metadata dd{margin:0;overflow-wrap:anywhere}.variant-plan small{display:block;font-size:11px;color:rgb(var(--c-muted));line-height:1.6;margin-top:4px}.plan-note{padding:10px 12px;background:rgb(var(--c-elev));border-radius:7px}.plan-table{overflow-x:auto}table{width:100%;border-collapse:collapse;font-size:12px}caption{font-size:12px;color:rgb(var(--c-muted));text-align:left;padding-bottom:8px}th,td{text-align:left;padding:10px 8px;border-bottom:1px solid rgb(var(--c-line));vertical-align:top;font-variant-numeric:tabular-nums}th{color:rgb(var(--c-muted));font-weight:500}th small{white-space:nowrap}.saved-version{display:grid;gap:6px;font-size:12px;overflow-wrap:anywhere}.saved-version strong{font-weight:600}.saved-version span{color:rgb(var(--c-muted))}.plan-sources{font-size:12px}.plan-sources summary{cursor:pointer}.plan-sources .source-metadata{margin-top:14px}.plan-sources code{display:block;overflow-wrap:anywhere;white-space:normal;color:rgb(var(--c-muted));font-size:11px;margin-top:10px}.plan-sources article{display:grid;gap:8px;border-top:1px solid rgb(var(--c-line));margin-top:14px;padding-top:12px;overflow-wrap:anywhere}.plan-sources a{color:rgb(var(--c-accent));width:fit-content}.variant-plan .plan-error{color:rgb(var(--c-danger))}.variant-plan .plan-success{color:#16835f}.variant-plan footer{display:flex;gap:10px;justify-content:flex-end;flex-wrap:wrap;padding-top:14px;border-top:1px solid rgb(var(--c-line))}@media(max-width:600px){.variant-plan{padding:12px}.plan-metadata{grid-template-columns:1fr;gap:5px}.plan-metadata dd{margin-bottom:8px}.plan-table table{min-width:620px}}
</style>
