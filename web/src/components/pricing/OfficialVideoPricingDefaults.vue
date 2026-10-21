<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, ApiError, errorMessage, uuid } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import { cellKey, isOfficialVideoModel, type PriceCell, type ReferenceCandidates } from './types'
import { exactRate } from './videoPriceDecimal'

const props = defineProps<{ model: string; channelId: number; contextKey: string }>()
const emit = defineEmits<{ published: [model: string, channelId: number, contextKey: string]; 'busy-change': [value: boolean] }>()
const { t } = useI18n(), auth = useAuth()
type DefaultPrice = { key: string; resolution: string; input_video: boolean; supplier_cny_per_1m: string; risk_multiplier: '1' }
interface PublicationPreview {
  model: string; current_channel_id: number; target_channel_id: number; target_channel_name: string; provider_scope_id: number
  config_version: string; pricing_version: string; capability_version: string; fx_usd_per_cny: string; fx_version: string
  context_sha256: string; catalog_version: string; has_active_credential: boolean; publishable: boolean; live_tested: boolean
  cells: PriceCell[]; diff: { key: string; change: string; before: PriceCell | null; after: PriceCell | null }[]
}
interface PublicationResult { model: string; channel_id: number; publication: { id: number; model: string; version: string; config_version: string } }
const prices = ref<DefaultPrice[]>([]), loading = ref(false), error = ref(''), catalogVersion = ref('')
const preview = ref<PublicationPreview | null>(null), busy = ref(false), preparing = ref(false), publicationError = ref(''), notice = ref('')
const retryPending = ref(false), requiresReload = ref(false), idempotencyKey = ref('')
const requestSnapshot = ref<{ channel_id: number; expected_context_sha256: string } | null>(null)
const allowed = computed(() => auth.isSuperadmin && !!auth.token && isOfficialVideoModel(props.model) && Number.isSafeInteger(props.channelId) && props.channelId > 0)
const canPreview = computed(() => allowed.value && prices.value.length > 0 && !loading.value && !busy.value && !retryPending.value)
const canPublish = computed(() => allowed.value && !!preview.value && !!requestSnapshot.value && preview.value.model === props.model && preview.value.target_channel_id === props.channelId && !busy.value && !loading.value && !requiresReload.value)
const minimumVersions = computed(() => [...new Set(preview.value?.cells.map(cell => cell.minimum_rule_version).filter(Boolean) ?? [])])
const priceChanges = computed(() => preview.value?.diff.filter(diff => diff.change !== 'unchanged') ?? [])
let generation = 0, active = true
function reset() {
  ++generation; prices.value = []; loading.value = false; error.value = ''; catalogVersion.value = ''
  preview.value = null; busy.value = false; preparing.value = false; publicationError.value = ''; notice.value = ''
  retryPending.value = false; requiresReload.value = false; idempotencyKey.value = ''; requestSnapshot.value = null
}
function collectedPrices(result: ReferenceCandidates & { channel_id: number }): DefaultPrice[] {
  if (result.model !== props.model || result.channel_id !== props.channelId || !Array.isArray(result.prices) || typeof result.catalog_version !== 'string') throw new Error('official_defaults_response_mismatch')
  const resolutions = props.model === 'seedance-2.0' ? ['480p', '720p', '1080p', '4k'] : props.model === 'seedance-2.5' ? ['480p', '720p', '1080p'] : ['480p', '720p']
  const seen = new Set<string>()
  return result.prices.flatMap(cell => {
    if (cell.mode !== 'generate' || cell.service_tier !== 'default' || cell.draft !== false || typeof cell.input_video !== 'boolean' || !resolutions.includes(cell.resolution) || cell.unit !== 'CNY/1M_tokens' || cell.precision !== 'exact') return []
    const key = cellKey(cell)
    if (seen.has(key)) throw new Error('official_defaults_ambiguous')
    seen.add(key)
    // The response can include a different channel's current configuration.
    // Only the captured supplier rate is reusable; no account FX or policy is.
    return [{ key, resolution: cell.resolution, input_video: cell.input_video, supplier_cny_per_1m: exactRate(cell.supplier_cny_per_1m), risk_multiplier: '1' as const }]
  })
}
async function load() {
  if (!active || loading.value || busy.value || retryPending.value) return
  reset()
  if (!allowed.value) return
  const current = generation, model = props.model
  loading.value = true
  try {
    const result = await api<ReferenceCandidates & { channel_id: number }>('GET', `/api/admin/video/models/${encodeURIComponent(model)}/pricing/reference-candidates?channel_id=${props.channelId}`)
    if (!active || current !== generation) return
    prices.value = collectedPrices(result); catalogVersion.value = result.catalog_version
  } catch (e) { if (active && current === generation) error.value = errorMessage(e, t('pricingAdmin.officialDefaultsFailed')) }
  finally { if (active && current === generation) loading.value = false }
}
function matchesPreview(value: PublicationPreview) {
  return value.model === props.model && value.target_channel_id === props.channelId && Number.isSafeInteger(value.current_channel_id) && value.current_channel_id > 0
    && value.publishable === true && value.has_active_credential === true && /^[a-f0-9]{64}$/.test(value.context_sha256)
    && !!value.config_version && !!value.pricing_version && !!value.capability_version && !!value.fx_version
    && Array.isArray(value.cells) && value.cells.length > 0 && new Set(value.cells.map(cellKey)).size === value.cells.length
    && Array.isArray(value.diff)
}
async function previewPublication() {
  if (!active || !canPreview.value) return
  const current = ++generation, model = props.model, channelId = props.channelId
  preview.value = null; requestSnapshot.value = null; idempotencyKey.value = ''; publicationError.value = ''; notice.value = ''; requiresReload.value = false
  busy.value = true; preparing.value = true
  try {
    const result = await api<PublicationPreview>('GET', `/api/admin/video/models/${encodeURIComponent(model)}/pricing/rebind-preview?channel_id=${channelId}`)
    if (!active || current !== generation) return
    if (!matchesPreview(result)) throw new Error('rebind_preview_mismatch')
    exactRate(result.fx_usd_per_cny)
    preview.value = result; requestSnapshot.value = { channel_id: channelId, expected_context_sha256: result.context_sha256 }; idempotencyKey.value = uuid()
  } catch (e) { if (active && current === generation) publicationError.value = errorMessage(e, t('pricingAdmin.rebindPreviewFailed')) }
  finally { if (active && current === generation) { busy.value = false; preparing.value = false } }
}
async function publish() {
  if (!active || !canPublish.value || !requestSnapshot.value) return
  const current = generation, model = props.model, channelId = props.channelId, contextKey = props.contextKey
  const body = { ...requestSnapshot.value }, key = idempotencyKey.value
  busy.value = true; publicationError.value = ''; notice.value = ''; retryPending.value = false
  try {
    const result = await api<PublicationResult>('POST', `/api/admin/video/models/${encodeURIComponent(model)}/pricing/rebind-publications`, body, { headers: { 'Idempotency-Key': key } })
    if (!active || current !== generation) return
    if (result.model !== model || result.channel_id !== channelId || result.publication?.model !== model || !result.publication.id || !result.publication.config_version || !result.publication.version) throw new Error('rebind_publication_unconfirmed')
    requestSnapshot.value = null; idempotencyKey.value = ''; notice.value = t('pricingAdmin.rebindPublished')
    emit('published', model, channelId, contextKey)
  } catch (e) {
    if (active && current === generation) {
      if (e instanceof ApiError && e.status === 409) { requiresReload.value = true; publicationError.value = t('pricingAdmin.rebindConflict') }
      else { retryPending.value = true; publicationError.value = errorMessage(e, t('pricingAdmin.rebindPublishFailed')) }
    }
  } finally { if (active && current === generation) busy.value = false }
}
function cellLabel(cell: PriceCell | null) { return cell ? `${cell.resolution} · ${t(cell.input_video ? 'pricingAdmin.withReferenceVideo' : 'pricingAdmin.withoutReferenceVideo')}${cell.mode !== 'generate' || cell.draft || cell.service_tier !== 'default' ? ` · ${cell.mode} / ${cell.service_tier}${cell.draft ? ' / Draft' : ''}` : ''}` : '—' }
watch(busy, value => emit('busy-change', value), { flush: 'sync' })
watch(() => [props.model, props.channelId, props.contextKey, auth.orgId, auth.token, auth.role, auth.isSuperadmin], () => { reset(); if (active) void load() }, { flush: 'sync' })
onMounted(load)
onActivated(() => { active = true; if (!catalogVersion.value && !loading.value) void load() })
onDeactivated(() => { active = false; reset() })
onBeforeUnmount(() => { active = false; reset() })
</script>

<template>
  <section class="official-defaults" :aria-busy="loading || busy">
    <header><h3>{{ t('pricingAdmin.officialDefaultsTitle') }}</h3><span v-if="prices.length">{{ t('pricingAdmin.officialDefaultsCaptured') }}</span></header>
    <p>{{ t('pricingAdmin.officialDefaultsUnbound') }}</p>
    <p v-if="loading" role="status">{{ t('pricingAdmin.officialDefaultsLoading') }}</p>
    <p v-else-if="error" role="alert" class="defaults-error">{{ error }}</p>
    <p v-else-if="!prices.length" role="status">{{ t('pricingAdmin.officialDefaultsEmpty') }}</p>
    <template v-else>
      <div class="defaults-table"><table><thead><tr><th>{{ t('pricingAdmin.resolution') }}</th><th>{{ t('pricingAdmin.referenceVideo') }}</th><th>{{ t('pricingAdmin.variantPlanCost') }}<small>CNY / 1M Token</small></th><th>{{ t('pricingAdmin.variantPlanRisk') }}</th></tr></thead><tbody><tr v-for="price in prices" :key="price.key"><td>{{ price.resolution }}</td><td>{{ t(price.input_video ? 'pricingAdmin.withReferenceVideo' : 'pricingAdmin.withoutReferenceVideo') }}</td><td>{{ price.supplier_cny_per_1m }}</td><td>{{ price.risk_multiplier }}</td></tr></tbody></table></div>
      <p class="defaults-next">{{ t('pricingAdmin.officialDefaultsHelp') }}</p>
    </template>
    <section v-if="preview" class="publication-preview">
      <h3>{{ t('pricingAdmin.rebindTitle') }}</h3>
      <p>{{ t('pricingAdmin.rebindHelp') }}</p>
      <dl class="publication-metadata"><dt>{{ t('pricingAdmin.rebindSourceChannel') }}</dt><dd>#{{ preview.current_channel_id }}</dd><dt>{{ t('pricingAdmin.rebindTargetChannel') }}</dt><dd>{{ preview.target_channel_name }} · #{{ preview.target_channel_id }}</dd><dt>{{ t('pricingAdmin.rebindScope') }}</dt><dd>#{{ preview.provider_scope_id }}</dd><dt>{{ t('pricingAdmin.variantPlanFX') }}</dt><dd>{{ preview.fx_usd_per_cny }} USD / CNY<small>{{ preview.fx_version }}</small></dd><dt>{{ t('pricingAdmin.rebindCapability') }}</dt><dd>{{ preview.capability_version }}</dd><dt>{{ t('pricingAdmin.rebindMinimum') }}</dt><dd>{{ minimumVersions.join(' / ') || '—' }}</dd><dt>{{ t('pricingAdmin.rebindConfigVersion') }}</dt><dd>{{ preview.config_version }}</dd></dl>
      <p v-if="!priceChanges.length" class="defaults-next">{{ t('pricingAdmin.rebindNoPriceChanges') }}</p>
      <div v-else class="defaults-table"><table><thead><tr><th>{{ t('pricingAdmin.field') }}</th><th>{{ t('pricingAdmin.before') }}</th><th>{{ t('pricingAdmin.after') }}</th></tr></thead><tbody><tr v-for="diff in priceChanges" :key="diff.key"><td>{{ cellLabel(diff.after || diff.before) }}</td><td>{{ diff.before?.retail_usd_per_1m || '—' }} USD / 1M<small>{{ t('pricingAdmin.variantPlanCost') }}: {{ diff.before?.supplier_cny_per_1m || '—' }} CNY / 1M</small><small>{{ t('pricingAdmin.variantPlanRisk') }}: {{ diff.before?.risk_multiplier || '—' }}</small><small>{{ t(diff.before?.enabled ? 'pricingAdmin.rebindEnabled' : 'pricingAdmin.rebindDisabled') }}</small></td><td>{{ diff.after?.retail_usd_per_1m || '—' }} USD / 1M<small>{{ t('pricingAdmin.variantPlanCost') }}: {{ diff.after?.supplier_cny_per_1m || '—' }} CNY / 1M</small><small>{{ t('pricingAdmin.variantPlanRisk') }}: {{ diff.after?.risk_multiplier || '—' }}</small><small>{{ t(diff.after?.enabled ? 'pricingAdmin.rebindEnabled' : 'pricingAdmin.rebindDisabled') }}</small></td></tr></tbody></table></div>
    </section>
    <p v-if="publicationError" role="alert" class="defaults-error">{{ publicationError }}</p>
    <p v-if="retryPending" role="status">{{ t('pricingAdmin.rebindRetryNotice') }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <footer v-if="allowed">
      <button v-if="error" type="button" class="btn-ghost" :disabled="loading || busy || retryPending" @click="load">{{ t('pricingx.reload') }}</button>
      <button v-if="prices.length" type="button" class="btn-ghost" :disabled="!canPreview" @click="previewPublication">{{ t(preparing ? 'common.loading' : 'pricingAdmin.rebindPreviewAction') }}</button>
      <button v-if="preview" type="button" class="btn-primary" :disabled="!canPublish" @click="publish">{{ t(busy && !preparing ? 'common.loading' : retryPending ? 'pricingAdmin.rebindRetry' : 'pricingAdmin.rebindPublish') }}</button>
    </footer>
  </section>
</template>

<style scoped>
.official-defaults{display:grid;gap:14px;min-width:0;padding:18px;border:1px solid rgb(var(--c-line));border-radius:10px}.official-defaults header{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap}.official-defaults h3{font-size:16px;margin:0}.official-defaults header span{font-size:11px;color:rgb(var(--c-muted));background:rgb(var(--c-elev));padding:5px 8px;border-radius:6px}.official-defaults p{margin:0;font-size:12px;line-height:1.65;color:rgb(var(--c-muted))}.defaults-table{overflow-x:auto}table{width:100%;border-collapse:collapse;font-size:12px}th,td{text-align:left;padding:10px 8px;border-bottom:1px solid rgb(var(--c-line));font-variant-numeric:tabular-nums}th{font-weight:500;color:rgb(var(--c-muted))}small{display:block;margin-top:4px;font-size:11px;white-space:nowrap}.defaults-next{padding:10px 12px;border-radius:7px;background:rgb(var(--c-elev))}.official-defaults .defaults-error{color:rgb(var(--c-danger))}.official-defaults>button{justify-self:end}@media(max-width:600px){.official-defaults{padding:12px}.defaults-table table{min-width:440px}}
.publication-preview{display:grid;gap:14px;padding-top:20px;border-top:1px solid rgb(var(--c-line))}.publication-metadata{display:grid;grid-template-columns:140px minmax(0,1fr);gap:10px;font-size:12px;margin:0}.publication-metadata dt{color:rgb(var(--c-muted))}.publication-metadata dd{margin:0;overflow-wrap:anywhere}.official-defaults footer{display:flex;justify-content:flex-end;gap:10px;flex-wrap:wrap;padding-top:14px}.publication-preview td small{white-space:normal}@media(max-width:600px){.publication-metadata{grid-template-columns:1fr;gap:6px}.publication-metadata dd{margin-bottom:8px}}
</style>
