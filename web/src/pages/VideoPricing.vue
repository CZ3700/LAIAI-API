<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage, uuid } from '@/lib/api'
import OfficialVideoPriceCatalog from '@/components/OfficialVideoPriceCatalog.vue'
import DetailDrawer from '@/components/DetailDrawer.vue'
import SimpleVideoPriceCell from '@/components/pricing/SimpleVideoPriceCell.vue'
import { useAuth } from '@/stores/auth'
import { cellKey, type PriceCell as Cell, type ReferenceCandidates, type SourceRow } from '@/components/pricing/types'
import { exactRate, adjustRate } from '@/components/pricing/videoPriceDecimal'
import messages from '@/i18n/pricingRedesignVideo'
const props = defineProps<{ selectedModel?: string; selectedChannelId?: number; embedded?: boolean; contextKey?: string }>()
const emit = defineEmits<{ 'dirty-change': [dirty: boolean]; 'busy-change': [busy: boolean]; published: [] }>()
const { t } = useI18n({ useScope: 'local', messages })
const tabs = ['retail', 'rules', 'reference', 'history'], tabsId = `video-pricing-${useId()}`
interface Pricing { model: string; channel_id?: number; config_version: string; pricing_version: string; capability_version: string; enabled: boolean; cells: Cell[]; fx_usd_per_cny: string; fx_version: string; fx_observed_at: string; ratios: string[]; min_duration: number; max_duration: number }
interface Preview { key: string; maximum_hold_usd: string; maximum_estimated_tokens: number; supplier_reserved_cny: string; minimum_rule_version: string }
interface Revision { id: number; version: string; config_version: string; created_by: number; created_at: string; snapshot_json: string }
interface Minimum { id: number; version: string; source_url: string; evidence_hash: string; observed_at: string; verified_by: number; verified_at: string }
interface Diff { key: string; change: 'added' | 'changed' | 'unchanged' | 'disabled'; before: Cell | null; after: Cell | null }
interface Prepared { request: Record<string, unknown>; diff: Diff[]; sources: unknown[] }
interface BulkChange { key: string; before: string; after: string }
const auth = useAuth(), model = ref(props.selectedModel ?? 'seedance-2.0'), pricing = ref<Pricing | null>(null)
const loading = ref(false), busy = ref(false), error = ref(''), notice = ref(''), tab = ref('retail')
const initialCells = ref(''), initialPricing = ref<Pricing | null>(null)
const previews = ref<Preview[]>([]), revisions = ref<Revision[]>([]), minimums = ref<Minimum[]>([])
const minimumJSON = ref(''), selectedRevision = ref<Revision | null>(null)
const candidates = ref<ReferenceCandidates | null>(null), referenceError = ref('')
const selectedEvidence = ref<SourceRow[] | null>(null), showMinimum = ref(false)
const evidenceQuery = ref(''), evidencePage = ref(1), evidencePageSize = 20
const selectedKeys = ref<string[]>([]), bulkMode = ref('same'), bulkValue = ref(''), bulkChanges = ref<BulkChange[]>([]), bulkError = ref('')
const prepared = ref<Prepared | null>(null), validatedBody = ref(''), validatedDraft = ref(''), previewOpen = ref(false), publishKey = ref(uuid())
const base = computed(() => `/api/admin/video/models/${encodeURIComponent(model.value)}/pricing`)
const supported = computed(() => ['seedance-2.0', 'seedance-2.5'].includes(model.value))
const bindingMatches = computed(() => !props.selectedChannelId || (pricing.value?.channel_id ?? candidates.value?.config?.channel_id) === props.selectedChannelId)
const canEdit = computed(() => !!pricing.value && bindingMatches.value && auth.isSuperadmin && supported.value)
const body = computed(() => ({ expected_config_version: pricing.value?.config_version ?? '', channel_id: props.selectedChannelId ?? pricing.value?.channel_id ?? candidates.value?.config?.channel_id ?? 0, cells: pricing.value?.cells ?? [] }))
const dirty = computed(() => !!pricing.value && (JSON.stringify(pricing.value.cells) !== initialCells.value || !!minimumJSON.value.trim()))
const priceDirty = computed(() => !!pricing.value && JSON.stringify(pricing.value.cells) !== initialCells.value)
const canPublish = computed(() => canEdit.value && !!validatedBody.value && validatedDraft.value === JSON.stringify(body.value) && !busy.value)
const originalByKey = computed(() => new Map((initialPricing.value?.cells ?? []).map(c => [cellKey(c), c])))
const defaultCells = computed(() => (pricing.value?.cells ?? []).filter(c => c.mode === 'generate' && c.service_tier === 'default' && !c.draft))
const editableCell = (cell: Cell) => cell.available || cell.unavailable_reason === 'verified_minimum_coverage_missing'
const primaryCells = computed(() => defaultCells.value.filter(c => c.enabled || originalByKey.value.get(cellKey(c))?.enabled || (['480p', '720p', '1080p'].includes(c.resolution) && editableCell(c))))
const pendingCells = computed(() => defaultCells.value.filter(c => !primaryCells.value.includes(c)))
const otherCells = computed(() => (pricing.value?.cells ?? []).filter(c => !defaultCells.value.includes(c)))
const otherPublished = computed(() => otherCells.value.filter(c => c.enabled || originalByKey.value.get(cellKey(c))?.enabled))
const otherPending = computed(() => otherCells.value.filter(c => !otherPublished.value.includes(c)))
const resolutions = computed(() => [...new Set(primaryCells.value.map(c => c.resolution))])
const referenceOpen = computed({ get: () => selectedEvidence.value !== null || showMinimum.value, set: (open: boolean) => { if (!open) { selectedEvidence.value = null; showMinimum.value = false } } })
const revisionOpen = computed({ get: () => selectedRevision.value !== null, set: (open: boolean) => { if (!open) selectedRevision.value = null } })
const allSourceEvidence = computed(() => (selectedEvidence.value ?? []).map(row => ({ ...row, source: candidates.value?.sources.find(s => s.id === row.source_id) })).filter(row => !evidenceQuery.value.trim() || `${row.source_id} ${row.row} ${row.source?.raw_rows[row.row - 1]?.join(' ')}`.toLocaleLowerCase().includes(evidenceQuery.value.trim().toLocaleLowerCase())))
const allMinimumEvidence = computed(() => candidates.value?.minimum.cells.filter(cell => !evidenceQuery.value.trim() || [cell.resolution, cell.ratio, cell.output_duration_ms, cell.fps, cell.minimum_tokens].join(' ').toLocaleLowerCase().includes(evidenceQuery.value.trim().toLocaleLowerCase())) ?? [])
const evidenceCount = computed(() => showMinimum.value ? allMinimumEvidence.value.length : allSourceEvidence.value.length)
const evidencePages = computed(() => Math.max(1, Math.ceil(evidenceCount.value / evidencePageSize)))
const sourceEvidence = computed(() => allSourceEvidence.value.slice((evidencePage.value - 1) * evidencePageSize, evidencePage.value * evidencePageSize))
const minimumEvidence = computed(() => allMinimumEvidence.value.slice((evidencePage.value - 1) * evidencePageSize, evidencePage.value * evidencePageSize))
let loadGeneration = 0, operationGeneration = 0, editGeneration = 0
const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value))
function invalidate() { ++editGeneration; prepared.value = null; validatedBody.value = ''; validatedDraft.value = ''; previews.value = []; previewOpen.value = false; publishKey.value = uuid() }
watch(body, () => { invalidate(); bulkChanges.value = [] }, { deep: true, flush: 'sync' })
watch([minimumJSON, selectedKeys, bulkMode, bulkValue], () => { invalidate(); bulkChanges.value = [] }, { deep: true, flush: 'sync' })
watch(dirty, value => emit('dirty-change', value), { immediate: true, flush: 'sync' })
watch(busy, value => emit('busy-change', value), { immediate: true, flush: 'sync' })
watch([selectedEvidence, showMinimum], () => { evidenceQuery.value = ''; evidencePage.value = 1 }, { flush: 'sync' })
watch(evidenceQuery, () => { evidencePage.value = 1 }, { flush: 'sync' })
function clear() {
  ++operationGeneration; busy.value = false; error.value = ''; notice.value = ''; pricing.value = null; initialPricing.value = null; initialCells.value = ''; invalidate()
  selectedRevision.value = null; revisions.value = []; minimums.value = []; minimumJSON.value = ''; candidates.value = null; referenceError.value = ''; selectedEvidence.value = null; showMinimum.value = false
  selectedKeys.value = []; bulkValue.value = ''; bulkChanges.value = []; bulkError.value = ''; tab.value = 'retail'
}
async function load() {
  const generation = ++loadGeneration
  clear(); loading.value = true
  if (!auth.isSuperadmin || !supported.value) { loading.value = false; return }
  const endpoint = base.value
  try {
    const [data, history, minimum, reference] = await Promise.allSettled([api<Pricing>('GET', endpoint), api<{ items: Revision[] }>('GET', `${endpoint}/revisions`), api<{ items: Minimum[] }>('GET', `${endpoint}/minimum-rules`), api<ReferenceCandidates>('GET', `${endpoint}/reference-candidates`)])
    if (generation !== loadGeneration) return
    if (reference.status === 'fulfilled' && reference.value.model === model.value) candidates.value = reference.value
    else referenceError.value = t('referenceFailed')
    if (history.status === 'fulfilled') revisions.value = history.value.items
    if (minimum.status === 'fulfilled') minimums.value = minimum.value.items
    if (data.status !== 'fulfilled') throw data.reason
    if (data.value.model !== model.value) throw new Error(t('loadFailed'))
    initialPricing.value = clone(data.value); initialCells.value = JSON.stringify(data.value.cells); pricing.value = clone(data.value)
  } catch (e) { if (generation === loadGeneration) error.value = errorMessage(e, t('loadFailed')) }
  finally { if (generation === loadGeneration) loading.value = false }
}
function discardChanges() {
  if (busy.value) return
  pricing.value = initialPricing.value ? clone(initialPricing.value) : null
  minimumJSON.value = ''; selectedKeys.value = []; bulkChanges.value = []; bulkError.value = ''; error.value = ''; notice.value = ''; invalidate()
}
function selectCell(key: string, value: boolean) { selectedKeys.value = value ? [...new Set([...selectedKeys.value, key])] : selectedKeys.value.filter(k => k !== key) }
function previewBulk() {
  bulkError.value = ''; bulkChanges.value = []
  try {
    if (!canEdit.value || busy.value || !selectedKeys.value.length) throw new Error()
    const rows = selectedKeys.value.map(key => pricing.value?.cells.find(c => cellKey(c) === key))
    if (rows.some(c => !c || !editableCell(c))) throw new Error()
    bulkChanges.value = rows.map(c => ({ key: cellKey(c!), before: c!.retail_usd_per_1m, after: bulkMode.value === 'same' ? exactRate(bulkValue.value) : adjustRate(c!.retail_usd_per_1m, bulkValue.value) }))
  } catch { bulkError.value = t('bulkError') }
}
function applyBulk() {
  if (!canEdit.value || busy.value || !bulkChanges.value.length) return
  const changes = clone(bulkChanges.value)
  if (changes.some(change => pricing.value?.cells.find(c => cellKey(c) === change.key)?.retail_usd_per_1m !== change.before)) { bulkChanges.value = []; return }
  for (const change of changes) { const cell = pricing.value!.cells.find(c => cellKey(c) === change.key)!; cell.retail_usd_per_1m = change.after }
  bulkChanges.value = []
}
async function validate() {
  if (!canEdit.value || busy.value || !priceDirty.value) return
  const generation = loadGeneration, operation = ++operationGeneration, endpoint = base.value, snapshot = JSON.stringify(body.value)
  const current = () => generation === loadGeneration && operation === operationGeneration
  busy.value = true; error.value = ''; notice.value = ''; invalidate()
  const edit = editGeneration
  const unchanged = () => edit === editGeneration && snapshot === JSON.stringify(body.value)
  try {
    const result = await api<Prepared>('POST', `${endpoint}/prepare`, JSON.parse(snapshot))
    if (!current() || !unchanged()) return
    const request = JSON.stringify(result.request)
    if (!result.request || !Array.isArray(result.request.cells) || !Array.isArray(result.diff)) throw new Error(t('prepareFailed'))
    // Forward the complete server-prepared request, including typed source evidence.
    const checked = await api<{ preview: Preview[] }>('POST', `${endpoint}/validate`, JSON.parse(request))
    if (!current() || !unchanged()) return
    prepared.value = result; previews.value = checked.preview; validatedBody.value = request; validatedDraft.value = snapshot; previewOpen.value = true; notice.value = t('previewSuccess')
  } catch (e) { if (current() && unchanged()) error.value = errorMessage(e, t('prepareFailed')) }
  finally { if (current()) busy.value = false }
}
async function publish() {
  if (!canPublish.value) return
  const generation = loadGeneration, operation = ++operationGeneration, endpoint = base.value, snapshot = validatedBody.value, idempotencyKey = publishKey.value
  const current = () => generation === loadGeneration && operation === operationGeneration
  busy.value = true; error.value = ''
  try {
    await api('POST', `${endpoint}/publications`, JSON.parse(snapshot), { headers: { 'Idempotency-Key': idempotencyKey } })
    if (!current()) return
    const refreshedGeneration = loadGeneration + 1
    await load()
    if (loadGeneration === refreshedGeneration) { notice.value = t('publishedNotice'); emit('published') }
  } catch (e) { if (current()) error.value = errorMessage(e, t('publishFailed')) }
  finally { if (current()) busy.value = false }
}
async function importMinimum() {
  if (!canEdit.value || busy.value) return
  const generation = loadGeneration, operation = ++operationGeneration, endpoint = base.value, snapshot = minimumJSON.value
  const current = () => generation === loadGeneration && operation === operationGeneration
  busy.value = true; error.value = ''; notice.value = ''; invalidate()
  try {
    await api('POST', `${endpoint}/minimum-rules`, JSON.parse(snapshot))
    if (!current()) return
    const result = await api<{ items: Minimum[] }>('GET', `${endpoint}/minimum-rules`)
    if (!current()) return
    minimums.value = result.items
    if (minimumJSON.value === snapshot) minimumJSON.value = ''
    notice.value = t('importSuccess')
  } catch (e) { if (current()) error.value = errorMessage(e, t('importFailed')) }
  finally { if (current()) busy.value = false }
}
function referenceCost(cell: Cell) { const matches = candidates.value?.prices.filter(c => cellKey(c) === cellKey(cell)); return matches?.length === 1 ? matches[0]!.supplier_cny_per_1m : undefined }
function revisionText(r: Revision) { try { return JSON.stringify(JSON.parse(r.snapshot_json), null, 2) } catch { return r.snapshot_json } }
function changeLabel(change: Diff['change']) { return t(change === 'disabled' ? 'turnedOff' : change) }
function beforeUnload(event: BeforeUnloadEvent) { if (dirty.value || busy.value) { event.preventDefault(); event.returnValue = '' } }
function tabKeydown(event: KeyboardEvent) {
  const index = tabs.indexOf(tab.value)
  const next = event.key === 'ArrowRight' ? (index + 1) % tabs.length : event.key === 'ArrowLeft' ? (index + tabs.length - 1) % tabs.length : event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : -1
  if (next < 0) return
  event.preventDefault(); tab.value = tabs[next]!
  ;((event.currentTarget as HTMLElement).parentElement?.querySelectorAll<HTMLButtonElement>('button')[next])?.focus()
}
watch(() => [auth.orgId, auth.token, auth.role, auth.isSuperadmin, props.selectedModel, props.selectedChannelId, props.contextKey], () => {
  ++loadGeneration; clear()
  if (props.selectedModel && model.value !== props.selectedModel) { model.value = props.selectedModel; return }
  if (auth.isAuthed) void load()
}, { flush: 'sync' })
watch(model, () => { void load() }, { flush: 'sync' })
onDeactivated(() => { ++loadGeneration; clear(); loading.value = false })
onBeforeUnmount(() => { ++loadGeneration; ++operationGeneration; if (typeof window !== 'undefined') window.removeEventListener('beforeunload', beforeUnload) })
onMounted(() => { if (typeof window !== 'undefined') window.addEventListener('beforeunload', beforeUnload); return load() })
onActivated(() => { if (!pricing.value && !loading.value) void load() })
defineExpose({ hasUnsavedChanges: () => dirty.value, discardChanges })
</script>

<template>
  <section class="video-pricing-page" :class="{ embedded }">
    <header class="page-heading"><div><h1>{{ model }}</h1><p>{{ t('unit') }}</p></div><span class="state" :class="{ dirty }">{{ dirty ? t('dirty') : t('clean') }}</span><label v-if="!embedded">{{ t('title') }} <select v-model="model" :disabled="busy || loading || dirty"><option>seedance-2.0</option><option>seedance-2.5</option></select></label></header>
    <p v-if="!auth.isSuperadmin" class="message">{{ t('unauthorized') }}</p><p v-else-if="!supported" class="message">{{ t('unsupported') }}</p>
    <p v-if="error" class="message error" role="alert">{{ error }}</p><p v-if="notice" class="message success" role="status">{{ notice }}</p>
    <p v-if="loading" aria-live="polite">{{ t('loading') }}</p><p v-else-if="!bindingMatches" class="message error">{{ t('wrongBinding') }}</p>
    <template v-else-if="pricing">
      <nav class="tabs" role="tablist" :aria-label="t('title')"><button v-for="name in tabs" :id="`${tabsId}-${name}`" :key="name" type="button" role="tab" :class="{ active: tab === name }" :aria-selected="tab === name" :tabindex="tab === name ? 0 : -1" :aria-controls="`${tabsId}-panel`" @keydown="tabKeydown" @click="tab = name">{{ t(name) }}</button></nav>
      <div v-if="tab === 'retail'" class="tab-body" :id="`${tabsId}-panel`" role="tabpanel" :aria-labelledby="`${tabsId}-${tab}`">
        <p class="intro">{{ t('subtitle') }}</p>
        <div class="matrix" role="group" :aria-label="t('retail')"><div class="matrix-heading"><strong>{{ t('resolution') }}</strong><strong>{{ t('noVideo') }}</strong><strong>{{ t('withVideo') }}</strong></div>
          <div v-for="resolution in resolutions" :key="resolution" class="matrix-row"><strong class="resolution">{{ resolution }}</strong><div v-for="input in [false, true]" :key="String(input)" class="matrix-value"><template v-for="cell in primaryCells.filter(c => c.resolution === resolution && c.input_video === input)" :key="cellKey(cell)"><SimpleVideoPriceCell :cell="cell" :original="originalByKey.get(cellKey(cell))" :reference="referenceCost(cell)" :selected="selectedKeys.includes(cellKey(cell))" :disabled="!canEdit || busy" @select="selectCell" @rules="tab = 'rules'" /></template><span v-if="!primaryCells.some(c => c.resolution === resolution && c.input_video === input)" class="empty-cell">{{ t('unavailable') }}</span></div></div>
        </div>
        <details v-if="pendingCells.length" class="panel"><summary>{{ t('candidates') }} · {{ pendingCells.length }}</summary><div class="extra-grid"><article v-for="cell in pendingCells" :key="cellKey(cell)"><h3>{{ cell.resolution }} · {{ cell.input_video ? t('withVideo') : t('noVideo') }}</h3><SimpleVideoPriceCell :cell="cell" :original="originalByKey.get(cellKey(cell))" :reference="referenceCost(cell)" :selected="selectedKeys.includes(cellKey(cell))" :disabled="!canEdit || busy" @select="selectCell" @rules="tab = 'rules'" /></article></div></details>
        <section v-if="otherPublished.length" class="panel"><h2>{{ t('otherModes') }} · {{ t('published') }}</h2><div class="extra-grid"><article v-for="cell in otherPublished" :key="cellKey(cell)"><h3>{{ cellKey(cell) }}</h3><SimpleVideoPriceCell :cell="cell" :original="originalByKey.get(cellKey(cell))" :selected="selectedKeys.includes(cellKey(cell))" :disabled="!canEdit || busy" @select="selectCell" @rules="tab = 'rules'" /></article></div></section>
        <details v-if="otherPending.length" class="panel"><summary>{{ t('otherModes') }} · {{ otherPending.length }}</summary><div class="extra-grid"><article v-for="cell in otherPending" :key="cellKey(cell)"><h3>{{ cellKey(cell) }}</h3><SimpleVideoPriceCell :cell="cell" :original="originalByKey.get(cellKey(cell))" :selected="selectedKeys.includes(cellKey(cell))" :disabled="!canEdit || busy" @select="selectCell" @rules="tab = 'rules'" /></article></div></details>
        <section class="panel bulk"><div class="section-title"><h2>{{ t('bulk') }}</h2><span>{{ t('selected', { count: selectedKeys.length }) }}</span></div><div class="bulk-controls"><select v-model="bulkMode" :disabled="busy" :aria-label="t('bulk')"><option value="same">{{ t('samePrice') }}</option><option value="percentage">{{ t('percentage') }}</option></select><input v-model="bulkValue" :disabled="busy" inputmode="decimal" :aria-label="t('value')" :placeholder="bulkMode === 'percentage' ? '10 / -5 (%)' : 'USD / 1M'"><button :disabled="busy || !canEdit || !selectedKeys.length" @click="previewBulk">{{ t('bulkPreview') }}</button></div><p>{{ t('bulkHelp') }}</p><p v-if="bulkError" role="alert" class="message error">{{ bulkError }}</p><div v-if="bulkChanges.length"><div class="table-scroll"><table><thead><tr><th>{{ t('key') }}</th><th>{{ t('before') }}</th><th>{{ t('after') }}</th></tr></thead><tbody><tr v-for="change in bulkChanges" :key="change.key"><td>{{ change.key }}</td><td>{{ change.before || '—' }}</td><td>{{ change.after }}</td></tr></tbody></table></div><button class="primary" :disabled="busy" @click="applyBulk">{{ t('apply') }}</button></div></section>
        <p class="preserved">{{ t('allPreserved') }}</p>
      </div>
      <div v-else-if="tab === 'rules'" class="tab-body" :id="`${tabsId}-panel`" role="tabpanel" :aria-labelledby="`${tabsId}-${tab}`">
        <p>{{ t('rulesHelp') }}</p><p>{{ t('reserveHelp') }}</p>
        <section class="panel metadata"><div><small>{{ t('version') }}</small><strong>{{ pricing.pricing_version }}</strong></div><div><small>{{ t('config') }}</small><strong>{{ pricing.config_version }}</strong></div><div><small>{{ t('capability') }}</small><strong>{{ pricing.capability_version }}</strong></div><div><small>{{ t('fx') }}</small><strong>{{ pricing.fx_usd_per_cny }} · {{ pricing.fx_version }}</strong><small>{{ pricing.fx_observed_at }}</small></div><div><small>{{ t('status') }}</small><strong>{{ pricing.enabled ? t('published') : t('inactive') }}</strong></div><div><small>{{ t('scope') }}</small><strong>{{ pricing.ratios?.join(' / ') }} · {{ pricing.min_duration }}–{{ pricing.max_duration }} s</strong></div></section>
        <details class="panel"><summary>{{ t('advanced') }}</summary><div class="table-scroll"><table><thead><tr><th>{{ t('key') }}</th><th>{{ t('cost') }}</th><th>{{ t('risk') }}</th><th>{{ t('minimum') }}</th></tr></thead><tbody><tr v-for="cell in pricing.cells" :key="cellKey(cell)"><td>{{ cellKey(cell) }}</td><td><input v-model="cell.supplier_cny_per_1m" inputmode="decimal" :disabled="!canEdit || busy || !editableCell(cell)" :aria-label="`${t('cost')} ${cellKey(cell)}`"></td><td><input v-model="cell.risk_multiplier" inputmode="decimal" :disabled="!canEdit || busy || !editableCell(cell)" :aria-label="`${t('risk')} ${cellKey(cell)}`"></td><td><select v-if="cell.input_video" v-model="cell.minimum_rule_version" :disabled="!canEdit || busy || !editableCell(cell)" :aria-label="`${t('minimum')} ${cellKey(cell)}`"><option value="">{{ t('chooseEvidence') }}</option><option v-if="cell.minimum_rule_version && !minimums.some(m => m.version === cell.minimum_rule_version)" :value="cell.minimum_rule_version">{{ cell.minimum_rule_version }}</option><option v-for="item in minimums" :key="item.id" :value="item.version">{{ item.version }}</option></select><span v-else>{{ t('notApplicable') }}</span></td></tr></tbody></table></div></details>
      </div>
      <div v-else-if="tab === 'reference'" class="tab-body" :id="`${tabsId}-panel`" role="tabpanel" :aria-labelledby="`${tabsId}-${tab}`">
        <p>{{ t('referenceHelp') }}</p><p v-if="referenceError" class="message" role="status">{{ referenceError }}</p>
        <section v-if="candidates" class="panel"><h2>{{ t('reference') }} · {{ candidates.catalog_version }}</h2><div class="table-scroll"><table><thead><tr><th>{{ t('resolution') }}</th><th>{{ t('key') }}</th><th>{{ t('official') }}</th><th>{{ t('status') }}</th><th>{{ t('sources') }}</th></tr></thead><tbody><tr v-for="cell in candidates.prices" :key="cell.key"><td>{{ cell.resolution }}</td><td>{{ cell.input_video ? t('withVideo') : t('noVideo') }}</td><td>{{ cell.supplier_cny_per_1m }}</td><td>{{ cell.available ? t('verified') : t('unavailable') }}</td><td><button @click="selectedEvidence = cell.source_rows">{{ t('details') }}</button></td></tr></tbody></table></div><p v-if="candidates.minimum.issues.includes('fps_not_in_source')">{{ t('fpsWarning') }}</p><button @click="showMinimum = true">{{ t('minimumReference') }} · {{ candidates.minimum.cells.length }}</button></section>
        <details class="panel"><summary>{{ t('reference') }} · {{ t('details') }}</summary><OfficialVideoPriceCatalog :allowed-series="[model === 'seedance-2.5' ? '2.5' : '2.0']" /></details>
        <section class="panel"><h2>{{ t('minimum') }}</h2><ul v-if="minimums.length"><li v-for="m in minimums" :key="m.id"><strong>{{ m.version }}</strong> · <a :href="m.source_url" target="_blank" rel="noopener noreferrer">{{ t('original') }}</a> · #{{ m.verified_by }} · {{ m.verified_at }}</li></ul><p v-else>{{ t('noMinimum') }}</p><details><summary>{{ t('importTitle') }}</summary><p>{{ t('importHelp') }}</p><textarea v-model="minimumJSON" :disabled="!canEdit || busy" rows="8" :aria-label="t('importTitle')" spellcheck="false"></textarea><button :disabled="!canEdit || busy || !minimumJSON.trim()" @click="importMinimum">{{ t('importAction') }}</button></details></section>
      </div>
      <div v-else class="tab-body" :id="`${tabsId}-panel`" role="tabpanel" :aria-labelledby="`${tabsId}-${tab}`"><p v-if="!revisions.length">{{ t('historyEmpty') }}</p><button v-for="r in revisions" :key="r.id" class="revision" @click="selectedRevision = r"><strong>{{ r.version }}</strong><span>{{ t('author') }} #{{ r.created_by }} · {{ r.created_at }}</span><span>{{ t('details') }} →</span></button></div>
      <footer class="pricing-footer"><span>{{ busy ? t('busy') : dirty ? t('dirty') : t('noChanges') }}</span><div class="footer-actions"><button v-if="dirty" :disabled="busy" @click="discardChanges">{{ t('discard') }}</button><button :disabled="busy || !canEdit || !priceDirty" @click="validate">{{ t('preview') }}</button><button class="primary" :disabled="!canPublish" :title="canPublish ? '' : t('validateFirst')" @click="publish">{{ t('publish') }}</button></div></footer>
    </template>
    <section v-else-if="!loading && auth.isSuperadmin && supported" class="panel"><p>{{ t('noConfig') }}</p><button @click="load">{{ t('reload') }}</button></section>
    <DetailDrawer v-model:open="previewOpen" :title="t('previewTitle')" size="lg" :close-disabled="busy" :description="t('allPreserved')"><template v-if="prepared"><div class="table-scroll"><table><thead><tr><th>{{ t('key') }}</th><th>{{ t('change') }}</th><th>{{ t('before') }}</th><th>{{ t('after') }}</th></tr></thead><tbody><tr v-for="row in prepared.diff" :key="row.key"><td>{{ row.key }}</td><td>{{ changeLabel(row.change) }}</td><td>{{ row.before?.retail_usd_per_1m || '—' }}<small>{{ row.before?.enabled ? t('published') : t('disabled') }}</small><small v-if="row.before && row.before.supplier_cny_per_1m !== row.after?.supplier_cny_per_1m">{{ t('cost') }}: {{ row.before.supplier_cny_per_1m || '—' }}</small><small v-if="row.before && row.before.risk_multiplier !== row.after?.risk_multiplier">{{ t('risk') }}: {{ row.before.risk_multiplier || '—' }}</small><small v-if="row.before && row.before.minimum_rule_version !== row.after?.minimum_rule_version">{{ t('minimum') }}: {{ row.before.minimum_rule_version || '—' }}</small></td><td>{{ row.after?.retail_usd_per_1m || '—' }}<small>{{ row.after?.enabled ? t('published') : t('disabled') }}</small><small v-if="row.after && row.after.supplier_cny_per_1m !== row.before?.supplier_cny_per_1m">{{ t('cost') }}: {{ row.after.supplier_cny_per_1m || '—' }}</small><small v-if="row.after && row.after.risk_multiplier !== row.before?.risk_multiplier">{{ t('risk') }}: {{ row.after.risk_multiplier || '—' }}</small><small v-if="row.after && row.after.minimum_rule_version !== row.before?.minimum_rule_version">{{ t('minimum') }}: {{ row.after.minimum_rule_version || '—' }}</small></td></tr></tbody></table></div><h2>{{ t('holdPreview') }}</h2><p>{{ t('reserveHelp') }}</p><div class="table-scroll"><table><thead><tr><th>{{ t('key') }}</th><th>{{ t('estimatedTokens') }}</th><th>{{ t('holdUSD') }}</th><th>{{ t('reserveCNY') }}</th></tr></thead><tbody><tr v-for="p in previews" :key="p.key"><td>{{ p.key }}</td><td>{{ p.maximum_estimated_tokens }}</td><td>{{ p.maximum_hold_usd }}</td><td>{{ p.supplier_reserved_cny }}</td></tr></tbody></table></div><details><summary>{{ t('sources') }}</summary><pre>{{ JSON.stringify(prepared.sources, null, 2) }}</pre></details><p v-if="error" role="alert" class="message error">{{ error }}</p></template><template #footer><button :disabled="busy" @click="previewOpen = false">{{ t('previous') }}</button><button class="primary" :disabled="!canPublish" @click="publish">{{ t('publish') }}</button></template></DetailDrawer>
    <DetailDrawer v-model:open="revisionOpen" :title="t('revisionTitle')" size="lg" :description="t('revisionReadOnly')"><template v-if="selectedRevision"><p>{{ selectedRevision.version }} · {{ selectedRevision.config_version }} · #{{ selectedRevision.created_by }} · {{ selectedRevision.created_at }}</p><pre>{{ revisionText(selectedRevision) }}</pre></template></DetailDrawer>
    <DetailDrawer v-model:open="referenceOpen" :title="showMinimum ? t('minimumReference') : t('sourceTitle')" size="lg" :description="t('referenceHelp')">
      <template v-if="candidates"><p>{{ candidates.catalog_version }}</p><p class="evidence-hash">{{ candidates.manifest_sha256 }}</p><label>{{ t('evidenceSearch') }}<input v-model="evidenceQuery" type="search"></label>
        <template v-if="showMinimum"><p>{{ t('fpsWarning') }}</p><div class="table-scroll"><table><thead><tr><th>{{ t('resolution') }}</th><th>Ratio</th><th>ms</th><th>FPS</th><th>Token</th><th>{{ t('sources') }}</th></tr></thead><tbody><tr v-for="(c, i) in minimumEvidence" :key="i"><td>{{ c.resolution }}</td><td>{{ c.ratio }}</td><td>{{ c.output_duration_ms }}</td><td>{{ c.fps ?? t('unknown') }}</td><td>{{ c.minimum_tokens }}</td><td><button @click="showMinimum = false; selectedEvidence = c.source_rows">{{ t('details') }}</button></td></tr></tbody></table></div></template>
        <template v-else><article v-for="row in sourceEvidence" :key="`${row.source_id}:${row.row}`" class="source-row"><template v-if="row.source"><p><a :href="row.source.source_url" target="_blank" rel="noopener noreferrer">{{ t('original') }} ↗</a> · {{ row.row }}</p><p>{{ row.source.table_id }} / {{ row.source.view_id }} · {{ row.source.captured_at }}</p><p class="evidence-hash">{{ row.source.raw_sha256 }}</p><div class="table-scroll"><table><thead><tr><th v-for="(header, i) in row.source.headers" :key="i">{{ header }}<small>{{ row.source.columns[i]?.unit }} · {{ row.source.columns[i]?.precision }}</small></th></tr></thead><tbody><tr><td v-for="(cell, i) in row.source.raw_rows[row.row - 1]" :key="i">{{ cell || '—' }}</td></tr></tbody></table></div></template></article></template>
        <p v-if="!evidenceCount">{{ t('noEvidence') }}</p><nav class="actions"><button :disabled="evidencePage <= 1" @click="evidencePage--">{{ t('previous') }}</button><span>{{ evidencePage }} / {{ evidencePages }} · {{ evidenceCount }}</span><button :disabled="evidencePage >= evidencePages" @click="evidencePage++">{{ t('next') }}</button></nav>
      </template>
    </DetailDrawer>
  </section>
</template>

<style scoped>
.video-pricing-page{max-width:1120px;margin:auto;padding:24px;display:flex;flex-direction:column;gap:18px;color:rgb(var(--c-ink));min-width:0}
.video-pricing-page.embedded{padding:0;max-width:none;width:100%;min-height:100%}
.embedded .pricing-footer{bottom:-20px}
.page-heading,.section-title{display:flex;gap:16px;align-items:center;justify-content:space-between;flex-wrap:wrap}
.page-heading h1{font-size:22px;margin:0}.page-heading p{margin:6px 0 0}.state{border-radius:20px;background:rgb(var(--c-bg));padding:7px 10px;font-size:12px;color:rgb(var(--c-muted))}.state.dirty{color:#a65b00;background:#fef3c7}
.tabs{display:flex;gap:22px;border-bottom:1px solid rgb(var(--c-line));overflow:auto;flex-shrink:0}.tabs button{border:0;border-radius:0;padding:12px 0;background:none;white-space:nowrap;color:rgb(var(--c-muted));border-bottom:2px solid transparent}.tabs .active{border-bottom-color:rgb(var(--c-accent));color:rgb(var(--c-accent));font-weight:600}
.tab-body{display:grid;gap:18px;min-width:0;flex:1}.intro{margin:0}.matrix{border:1px solid rgb(var(--c-line));border-radius:12px;overflow:hidden}.matrix-heading,.matrix-row{display:grid;grid-template-columns:90px minmax(0,1fr) minmax(0,1fr)}.matrix-heading{background:rgb(var(--c-bg));padding:12px 0;font-size:12px;color:rgb(var(--c-muted))}.matrix-heading strong{padding:0 12px}.matrix-row{border-top:1px solid rgb(var(--c-line))}.resolution{padding:20px 12px;font-size:14px}.matrix-value{border-left:1px solid rgb(var(--c-line));min-width:0}.empty-cell{display:block;padding:20px;color:rgb(var(--c-muted));font-size:12px}
.panel{padding:18px;border:1px solid rgb(var(--c-line));border-radius:12px;background:rgb(var(--c-panel));min-width:0}.extra-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px;margin-top:12px}.extra-grid article{border:1px solid rgb(var(--c-line));border-radius:8px;overflow:hidden}.extra-grid h3{font-size:12px;margin:12px 14px 0;overflow-wrap:anywhere}.section-title span,.bulk p,.preserved{font-size:12px;color:rgb(var(--c-muted))}.bulk-controls{display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin-top:12px}.bulk-controls input{min-width:100px;flex:1}.metadata{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}.metadata strong{display:block;font-size:13px;overflow-wrap:anywhere}
.pricing-footer{position:sticky;bottom:-24px;z-index:2;margin-top:auto;background:rgb(var(--c-panel));border-top:1px solid rgb(var(--c-line));padding:16px 0;display:flex;justify-content:space-between;align-items:center;gap:14px;box-shadow:0 -8px 20px rgb(0 0 0/.025)}.pricing-footer>span{font-size:12px;color:rgb(var(--c-muted))}.footer-actions{display:flex;gap:8px;flex-wrap:wrap;justify-content:flex-end}.revision{display:flex;align-items:center;gap:14px;text-align:left;padding:18px}.revision span{font-size:12px;color:rgb(var(--c-muted))}.revision span:last-child{margin-left:auto}
h2{font-size:16px;margin:0}p,small{color:rgb(var(--c-muted));line-height:1.6}small{display:block;font-size:12px}.table-scroll{overflow-x:auto}table{width:100%;border-collapse:collapse;font-size:12px;margin:12px 0}th,td{text-align:left;border-bottom:1px solid rgb(var(--c-line));padding:10px 8px;vertical-align:top}th{color:rgb(var(--c-muted))}td input{width:120px}input,select,textarea,button{font:inherit;color:inherit;border:1px solid rgb(var(--c-line));border-radius:7px;background:rgb(var(--c-bg));padding:9px;box-sizing:border-box}button{cursor:pointer}button:disabled,input:disabled,select:disabled{opacity:.5;cursor:not-allowed}button:focus-visible,input:focus-visible,select:focus-visible{outline:2px solid rgb(var(--c-accent));outline-offset:2px}.primary{background:rgb(var(--c-accent));border-color:rgb(var(--c-accent));color:white}.actions{display:flex;gap:12px;align-items:center;margin-top:16px}.message{padding:12px;border-radius:8px;margin:0;font-size:13px}.error{background:#fff0f0;color:#a12a2a}.success{background:#ecfdf5;color:#16634c}textarea{width:100%;margin:12px 0;font-family:monospace}pre{overflow:auto;max-height:500px;padding:16px;background:rgb(var(--c-bg));font-size:12px}li{padding:8px 0;overflow-wrap:anywhere}a{color:rgb(var(--c-accent))}summary{cursor:pointer;font-size:13px}.evidence-hash{overflow-wrap:anywhere;font-family:monospace;font-size:12px}.source-row{padding:16px 0;border-bottom:1px solid rgb(var(--c-line))}
@media(max-width:600px){.video-pricing-page{padding:16px}.matrix-heading,.matrix-row{grid-template-columns:62px minmax(0,1fr) minmax(0,1fr)}.matrix-heading strong,.resolution{padding-left:8px;padding-right:8px}.matrix-heading{font-size:11px}.panel{padding:12px}.extra-grid,.metadata{grid-template-columns:1fr}.pricing-footer{bottom:-16px;align-items:flex-start;flex-direction:column}.footer-actions{width:100%;justify-content:flex-end}.tabs{gap:16px}.revision{flex-wrap:wrap}}
@media(max-width:640px){.embedded .pricing-footer{bottom:-16px}}
</style>
