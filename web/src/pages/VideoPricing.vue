<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage, uuid } from '@/lib/api'
import OfficialVideoPriceCatalog from '@/components/OfficialVideoPriceCatalog.vue'
import DetailDrawer from '@/components/DetailDrawer.vue'
import SimpleVideoPriceCell from '@/components/pricing/SimpleVideoPriceCell.vue'
import { useAuth } from '@/stores/auth'
import { cellKey, isOfficialVideoModel, officialVideoModels, officialVideoSeries, type PriceCell as Cell, type ReferenceCandidates, type SourceRow } from '@/components/pricing/types'
import { exactRate, adjustRate, convertOfficialRate } from '@/components/pricing/videoPriceDecimal'
import messages from '@/i18n/pricingRedesignVideo'
const props = defineProps<{ selectedModel?: string; selectedChannelId?: number; embedded?: boolean; contextKey?: string }>()
const emit = defineEmits<{ 'dirty-change': [dirty: boolean]; 'busy-change': [busy: boolean]; published: []; 'verification-synced': [] }>()
const { t } = useI18n({ useScope: 'local', messages })
const tabs = ['retail', 'rules', 'reference', 'history'], tabsId = `video-pricing-${useId()}`
interface Pricing { model: string; channel_id?: number; config_version: string; pricing_version: string; capability_version: string; enabled: boolean; cells: Cell[]; fx_usd_per_cny: string; fx_version: string; fx_observed_at: string; ratios: string[]; min_duration: number; max_duration: number }
interface Preview { key: string; maximum_hold_usd: string; maximum_estimated_tokens: number; supplier_reserved_cny: string; minimum_rule_version: string }
interface Revision { id: number; version: string; config_version: string; created_by: number; created_at: string; snapshot_json: string }
interface Minimum { id: number; version: string; source_url: string; evidence_hash: string; observed_at: string; verified_by: number; verified_at: string }
interface Diff { key: string; change: 'added' | 'changed' | 'unchanged' | 'disabled'; before: Cell | null; after: Cell | null }
interface Prepared { request: Record<string, unknown>; diff: Diff[]; sources: unknown[] }
interface BulkChange { key: string; before: string; after: string }
interface Verification { model: string; profile_version: string; evidence_sha256: string; config_version: string; capability_version: string; minimum_version: string; minimum_cell_count: number; minimum_precision?: string; resolutions: string[]; ratios: string[]; min_duration: number; max_duration: number; verified: boolean; sources: { id: string; url: string; sha256: string }[] }
interface AdoptionChange { key: string; costBefore: string; costAfter: string; riskBefore: string; riskAfter: string; minimumBefore: string; minimumAfter: string }
interface TemplateRef { version: string; model: string; sha256: string }
interface PricingTemplate { version: string; model: string; upstream_model: string; label: string; body_sha256: string; currency: string; fx_usd_per_cny: string; fx_version: string; fx_observed_at: string; source_pricing_version: string; profile_version: string; minimum_rule_version: string; minimum_cell_count: number; cells: Cell[] }
type TemplateMode = 'fill_missing' | 'replace'
interface TemplatePreview { model: string; channel_id: number; config_version: string; template_ref: TemplateRef; mode: TemplateMode; cells: Cell[]; diff: Diff[] }
const auth = useAuth(), model = ref(props.selectedModel ?? 'seedance-2.0'), pricing = ref<Pricing | null>(null)
const loading = ref(false), busy = ref(false), error = ref(''), notice = ref(''), tab = ref('retail')
const initialCells = ref(''), initialPricing = ref<Pricing | null>(null)
const previews = ref<Preview[]>([]), revisions = ref<Revision[]>([]), minimums = ref<Minimum[]>([])
const minimumJSON = ref(''), selectedRevision = ref<Revision | null>(null)
const candidates = ref<ReferenceCandidates | null>(null), referenceError = ref('')
const selectedEvidence = ref<SourceRow[] | null>(null), showMinimum = ref(false)
const evidenceQuery = ref(''), evidencePage = ref(1), evidencePageSize = 20
const selectedKeys = ref<string[]>([]), bulkMode = ref('same'), bulkValue = ref(''), bulkChanges = ref<BulkChange[]>([]), bulkError = ref('')
const verification = ref<Verification | null>(null), verificationError = ref(''), verificationOpen = ref(false), verificationReloadRequired = ref(false)
const verificationProfile = ref<'standard' | 'draft'>('standard')
const adoptionOpen = ref(false), adoptionRisk = ref('1'), adoptionError = ref(''), adoptionSnapshot = ref('')
const templates = ref<PricingTemplate[]>([]), templatesLoading = ref(false), templateError = ref(''), templateOpen = ref(false)
const templateVersion = ref(''), templateMode = ref<TemplateMode>('fill_missing'), templatePreview = ref<TemplatePreview | null>(null), templateSnapshot = ref(''), templateRef = ref<TemplateRef | null>(null)
const prepared = ref<Prepared | null>(null), validatedBody = ref(''), validatedDraft = ref(''), previewOpen = ref(false), publishKey = ref(uuid())
const base = computed(() => `/api/admin/video/models/${encodeURIComponent(model.value)}/pricing`)
const verificationBase = computed(() => `/api/admin/video/models/${encodeURIComponent(model.value)}/verification`)
const verificationReadURL = computed(() => verificationBase.value + (model.value === 'seedance-2.5' && verificationProfile.value === 'draft' ? '?profile=draft' : ''))
const supported = computed(() => isOfficialVideoModel(model.value))
const referenceSeries = computed(() => officialVideoSeries(model.value))
const bindingMatches = computed(() => !props.selectedChannelId || (pricing.value?.channel_id ?? candidates.value?.config?.channel_id) === props.selectedChannelId)
const canEdit = computed(() => !!pricing.value && bindingMatches.value && auth.isSuperadmin && supported.value)
const body = computed(() => ({ expected_config_version: pricing.value?.config_version ?? '', channel_id: props.selectedChannelId ?? pricing.value?.channel_id ?? candidates.value?.config?.channel_id ?? 0, cells: pricing.value?.cells ?? [], ...(templateRef.value ? { template_ref: templateRef.value } : {}) }))
const dirty = computed(() => !!pricing.value && (JSON.stringify(pricing.value.cells) !== initialCells.value || !!minimumJSON.value.trim()))
const priceDirty = computed(() => !!pricing.value && JSON.stringify(pricing.value.cells) !== initialCells.value)
const canPublish = computed(() => canEdit.value && !!validatedBody.value && validatedDraft.value === JSON.stringify(body.value) && !busy.value)
const selectedTemplate = computed(() => templates.value.find(item => item.version === templateVersion.value))
const canPreviewTemplate = computed(() => canEdit.value && !!selectedTemplate.value && !dirty.value && !busy.value && !loading.value && !templatesLoading.value)
const templateChanges = computed(() => templatePreview.value?.diff.filter(row => row.change !== 'unchanged') ?? [])
const canApplyTemplate = computed(() => canPreviewTemplate.value && !!templatePreview.value && templateChanges.value.length > 0 && templateSnapshot.value === JSON.stringify(body.value) && templatePreview.value.template_ref.version === templateVersion.value && templatePreview.value.mode === templateMode.value)
const originalByKey = computed(() => new Map((initialPricing.value?.cells ?? []).map(c => [cellKey(c), c])))
const incompleteNewPrices = computed(() => (pricing.value?.cells ?? []).filter(cell => {
  const original = originalByKey.value.get(cellKey(cell))
  return !original?.retail_usd_per_1m && !original?.supplier_cny_per_1m && !original?.enabled
    && !cell.retail_usd_per_1m && !!(cell.supplier_cny_per_1m || cell.minimum_rule_version)
    && JSON.stringify(cell) !== JSON.stringify(original)
}).map(cellKey))
const defaultCells = computed(() => (pricing.value?.cells ?? []).filter(c => c.mode === 'generate' && c.service_tier === 'default' && !c.draft))
const capabilityReadyCell = (cell: Cell) => cell.available || cell.unavailable_reason === 'verified_minimum_coverage_missing'
const editableCell = (cell: Cell) => capabilityReadyCell(cell) || referenceCost(cell) !== undefined || !!originalByKey.value.get(cellKey(cell))?.retail_usd_per_1m || !!originalByKey.value.get(cellKey(cell))?.supplier_cny_per_1m || cell.enabled
const primaryCells = computed(() => defaultCells.value.filter(c => c.enabled || originalByKey.value.get(cellKey(c))?.enabled || capabilityReadyCell(c) || (['480p', '720p', '1080p'].includes(c.resolution) && editableCell(c))))
const pendingCells = computed(() => defaultCells.value.filter(c => !primaryCells.value.includes(c)))
const otherCells = computed(() => (pricing.value?.cells ?? []).filter(c => !defaultCells.value.includes(c)))
const otherPublished = computed(() => otherCells.value.filter(c => c.enabled || originalByKey.value.get(cellKey(c))?.enabled))
const otherPending = computed(() => otherCells.value.filter(c => !otherPublished.value.includes(c)))
const resolutions = computed(() => [...new Set(primaryCells.value.map(c => c.resolution))])
const missingSourceFPS = computed(() => !!candidates.value?.minimum.cells.some(cell => cell.fps === null || cell.fps === undefined))
const sourceFPSNotice = computed(() => missingSourceFPS.value ? t(verification.value?.verified && model.value !== 'seedance-2.5' ? 'fpsVerifiedRule' : 'fpsWarning') : '')
const currentRisk = computed(() => {
  const values = [...new Set((initialPricing.value?.cells ?? []).filter(cell => cell.retail_usd_per_1m || cell.supplier_cny_per_1m).map(cell => { try { return exactRisk(cell.risk_multiplier) } catch { return '' } }).filter(Boolean))]
  return values.length === 1 ? values[0]! : ''
})
const canSyncVerification = computed(() => auth.isSuperadmin && supported.value && bindingMatches.value && !!verification.value && !verification.value.verified && !dirty.value && !busy.value && !loading.value && !verificationReloadRequired.value)
const draftWorkflowReady = computed(() => model.value === 'seedance-2.5' && verificationProfile.value === 'draft' && verification.value?.verified && verification.value.profile_version === 'ark-official-20261030-draft-v1')
const draftWorkflowCell = (cell: Cell) => cell.service_tier === 'default' && (cell.mode === 'generate' && cell.resolution === '480p' && cell.draft || cell.mode === 'draft_conversion' && cell.resolution === '1080p' && !cell.draft)
const adoptionChanges = computed<AdoptionChange[]>(() => {
  return (pricing.value?.cells ?? []).filter(cell => selectedKeys.value.includes(cellKey(cell))).flatMap(cell => {
    const cost = referenceCost(cell)
    if (cost === undefined) return []
    const original = originalByKey.value.get(cellKey(cell))
    const newConfiguration = !original?.retail_usd_per_1m && !original?.supplier_cny_per_1m && !original?.enabled
    let risk = cell.risk_multiplier
    if (newConfiguration && !risk && adoptionRisk.value) { try { risk = exactRisk(adoptionRisk.value) } catch { /* Applying remains blocked by validation below. */ } }
    const syncedMinimum = verification.value?.verified && minimums.value.some(item => item.version === verification.value?.minimum_version) ? verification.value.minimum_version : ''
    const minimum = newConfiguration && cell.input_video && !cell.minimum_rule_version ? syncedMinimum : cell.minimum_rule_version
    const change = { key: cellKey(cell), costBefore: cell.supplier_cny_per_1m, costAfter: cell.supplier_cny_per_1m || cost, riskBefore: cell.risk_multiplier, riskAfter: risk, minimumBefore: cell.minimum_rule_version, minimumAfter: minimum }
    return change.costBefore !== change.costAfter || change.riskBefore !== change.riskAfter || change.minimumBefore !== change.minimumAfter ? [change] : []
  })
})
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
function invalidate() { ++editGeneration; prepared.value = null; validatedBody.value = ''; validatedDraft.value = ''; previews.value = []; previewOpen.value = false; publishKey.value = uuid(); templatePreview.value = null; templateSnapshot.value = '' }
watch(body, () => { invalidate(); bulkChanges.value = [] }, { deep: true, flush: 'sync' })
watch([minimumJSON, selectedKeys, bulkMode, bulkValue], () => { invalidate(); bulkChanges.value = [] }, { deep: true, flush: 'sync' })
watch(dirty, value => emit('dirty-change', value), { immediate: true, flush: 'sync' })
watch(busy, value => emit('busy-change', value), { immediate: true, flush: 'sync' })
watch([selectedEvidence, showMinimum], () => { evidenceQuery.value = ''; evidencePage.value = 1 }, { flush: 'sync' })
watch(evidenceQuery, () => { evidencePage.value = 1 }, { flush: 'sync' })
watch([templateVersion, templateMode], () => { templatePreview.value = null; templateSnapshot.value = ''; templateError.value = '' }, { flush: 'sync' })
function clear() {
  ++operationGeneration; busy.value = false; error.value = ''; notice.value = ''; pricing.value = null; initialPricing.value = null; initialCells.value = ''; invalidate()
  selectedRevision.value = null; revisions.value = []; minimums.value = []; minimumJSON.value = ''; candidates.value = null; referenceError.value = ''; selectedEvidence.value = null; showMinimum.value = false
  selectedKeys.value = []; bulkValue.value = ''; bulkChanges.value = []; bulkError.value = ''; tab.value = 'retail'
  verification.value = null; verificationError.value = ''; verificationOpen.value = false; verificationReloadRequired.value = false; adoptionOpen.value = false; adoptionRisk.value = '1'; adoptionError.value = ''; adoptionSnapshot.value = ''
  templates.value = []; templatesLoading.value = false; templateError.value = ''; templateOpen.value = false; templateVersion.value = ''; templateMode.value = 'fill_missing'; templatePreview.value = null; templateSnapshot.value = ''; templateRef.value = null
}
async function load() {
  const generation = ++loadGeneration
  clear(); loading.value = true
  if (!auth.isSuperadmin || !supported.value) { loading.value = false; return }
  void loadTemplates(generation)
  const endpoint = base.value
  try {
    const [data, history, minimum, reference, official] = await Promise.allSettled([api<Pricing>('GET', endpoint), api<{ items: Revision[] }>('GET', `${endpoint}/revisions`), api<{ items: Minimum[] }>('GET', `${endpoint}/minimum-rules`), api<ReferenceCandidates>('GET', `${endpoint}/reference-candidates`), api<Verification>('GET', verificationReadURL.value)])
    if (generation !== loadGeneration) return
    if (reference.status === 'fulfilled' && reference.value.model === model.value) candidates.value = reference.value
    else referenceError.value = t('referenceFailed')
    if (history.status === 'fulfilled') revisions.value = history.value.items
    if (minimum.status === 'fulfilled') minimums.value = minimum.value.items
    if (official.status === 'fulfilled' && official.value.model === model.value && official.value.profile_version) verification.value = official.value
    else verificationError.value = t('verificationLoadFailed')
    if (data.status !== 'fulfilled') throw data.reason
    if (data.value.model !== model.value) throw new Error(t('loadFailed'))
    initialPricing.value = clone(data.value); initialCells.value = JSON.stringify(data.value.cells); pricing.value = clone(data.value)
    inheritOfficialPricing()
  } catch (e) { if (generation === loadGeneration) error.value = errorMessage(e, t('loadFailed')) }
  finally { if (generation === loadGeneration) loading.value = false }
}
function inheritOfficialPricing() {
  if (!canEdit.value || !pricing.value || !candidates.value || candidates.value.model !== model.value) return
  const context = candidates.value.config
  if (!context || context.channel_id !== pricing.value.channel_id || context.config_version !== pricing.value.config_version) return
  let inherited = 0
  for (const cell of pricing.value.cells) {
    if (!(cell.mode === 'generate' && cell.service_tier === 'default' && !cell.draft || draftWorkflowReady.value && draftWorkflowCell(cell)) || cell.enabled || cell.retail_usd_per_1m || cell.supplier_cny_per_1m) continue
    const cost = referenceCost(cell)
    if (cost === undefined) continue
    try {
      // Collected exact prices are usable without a manual capability review.
      // Monetary values always use the target scope's existing FX snapshot.
      const retail = convertOfficialRate(cost, pricing.value.fx_usd_per_cny ?? '')
      cell.supplier_cny_per_1m = cost
      cell.retail_usd_per_1m = retail
      cell.risk_multiplier ||= '1'
      if (cell.input_video && !cell.minimum_rule_version && verification.value?.verified && minimums.value.some(item => item.version === verification.value?.minimum_version)) {
        cell.minimum_rule_version = verification.value.minimum_version
      }
      inherited++
    } catch { /* A missing FX snapshot cannot produce a USD selling price. */ }
  }
  if (inherited) notice.value = t('officialInherited', { count: inherited })
}
function discardChanges() {
  if (busy.value) return
  pricing.value = initialPricing.value ? clone(initialPricing.value) : null
  minimumJSON.value = ''; selectedKeys.value = []; bulkChanges.value = []; bulkError.value = ''; error.value = ''; notice.value = ''; invalidate()
  adoptionOpen.value = false; adoptionSnapshot.value = ''; adoptionError.value = ''
  templateRef.value = null; templatePreview.value = null; templateSnapshot.value = ''; templateError.value = ''
}
async function loadTemplates(generation = loadGeneration) {
  if (!auth.isSuperadmin || !supported.value || templatesLoading.value) return
  const selectedModel = model.value
  templatesLoading.value = true; templateError.value = ''
  try {
    const result = await api<{ items: PricingTemplate[] }>('GET', `/api/admin/video/pricing-templates?model=${encodeURIComponent(selectedModel)}`)
    if (generation !== loadGeneration) return
    if (!Array.isArray(result.items) || result.items.some(item => item.model !== selectedModel || !item.version || !/^[a-f0-9]{64}$/.test(item.body_sha256) || !Array.isArray(item.cells))) throw new Error(t('templateLoadFailed'))
    templates.value = result.items
    templateVersion.value = result.items[0]?.version ?? ''
  } catch (e) { if (generation === loadGeneration) templateError.value = errorMessage(e, t('templateLoadFailed')) }
  finally { if (generation === loadGeneration) templatesLoading.value = false }
}
function openTemplate() {
  if (!canEdit.value || busy.value || loading.value) return
  templatePreview.value = null; templateSnapshot.value = ''; templateMode.value = 'fill_missing'; templateOpen.value = true
}
async function previewTemplate() {
  if (!canPreviewTemplate.value) return
  const generation = loadGeneration, operation = ++operationGeneration, endpoint = base.value, snapshot = JSON.stringify(body.value), version = templateVersion.value, mode = templateMode.value, sha256 = selectedTemplate.value!.body_sha256
  const current = () => generation === loadGeneration && operation === operationGeneration
  busy.value = true; templateError.value = ''; templatePreview.value = null; templateSnapshot.value = ''
  const edit = editGeneration
  const unchanged = () => edit === editGeneration && snapshot === JSON.stringify(body.value) && version === templateVersion.value && mode === templateMode.value
  try {
    const result = await api<TemplatePreview>('POST', `${endpoint}/template-preview`, { channel_id: body.value.channel_id, expected_config_version: body.value.expected_config_version, template_version: version, mode })
    if (!current() || !unchanged()) return
    if (result.model !== model.value || result.channel_id !== body.value.channel_id || result.config_version !== body.value.expected_config_version || result.mode !== mode || result.template_ref?.version !== version || result.template_ref?.model !== model.value || result.template_ref?.sha256 !== sha256 || !Array.isArray(result.cells) || !Array.isArray(result.diff)) throw new Error(t('templatePreviewFailed'))
    const cells = new Map(result.cells.map(cell => [cellKey(cell), cell]))
    if (cells.size !== result.cells.length || pricing.value!.cells.some(cell => !cells.has(cellKey(cell)) || cells.get(cellKey(cell))!.enabled !== cell.enabled) || result.cells.some(cell => !originalByKey.value.has(cellKey(cell)) && cell.enabled)) throw new Error(t('templatePreviewFailed'))
    templatePreview.value = result; templateSnapshot.value = snapshot
  } catch (e) { if (current() && unchanged()) templateError.value = errorMessage(e, t('templatePreviewFailed')) }
  finally { if (current()) busy.value = false }
}
function applyTemplate() {
  if (!canApplyTemplate.value) return
  const result = clone(templatePreview.value!)
  pricing.value!.cells = result.cells; templateRef.value = result.template_ref
  templateOpen.value = false; templatePreview.value = null; templateSnapshot.value = ''; selectedKeys.value = []; bulkChanges.value = []
  notice.value = t('templateApplied'); tab.value = 'retail'
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
  if (incompleteNewPrices.value.length) { invalidate(); error.value = t('newPriceRequired', { keys: incompleteNewPrices.value.join(', ') }); return }
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
function referenceCost(cell: Cell) {
  const source = draftWorkflowReady.value && draftWorkflowCell(cell) ? { ...cell, mode: 'generate', draft: false } : cell
  const matches = candidates.value?.prices.filter(c => cellKey(c) === cellKey(source) && c.unit === 'CNY/1M_tokens' && c.precision === 'exact')
  if (matches?.length !== 1) return undefined
  try { return exactRate(matches[0]!.supplier_cny_per_1m) } catch { return undefined }
}
function exactRisk(value: string): string {
  const rate = exactRate(value), [whole, fraction = ''] = rate.split('.')
  const scaled = BigInt(whole!) * 10n ** 12n + BigInt(fraction.padEnd(12, '0'))
  if (scaled < 10n ** 12n || scaled > 100n * 10n ** 12n) throw new Error('invalid_risk')
  return rate
}
function adoptionFingerprint() { return JSON.stringify({ body: body.value, keys: selectedKeys.value, profile: verification.value?.profile_version, verified: verification.value?.verified, minimum: minimums.value.map(item => item.version) }) }
function previewOfficialAdoption() {
  if (!canEdit.value || busy.value || !selectedKeys.value.length) return
  adoptionError.value = ''; adoptionRisk.value = '1'; adoptionSnapshot.value = adoptionFingerprint(); adoptionOpen.value = true
}
function applyOfficialAdoption() {
  if (!canEdit.value || busy.value || !adoptionOpen.value || !adoptionSnapshot.value) return
  if (adoptionSnapshot.value !== adoptionFingerprint()) { adoptionError.value = t('adoptionStale'); return }
  const changes = clone(adoptionChanges.value)
  if (!changes.length) { adoptionError.value = t('adoptionEmpty'); return }
  for (const change of changes) {
    const original = originalByKey.value.get(change.key)
    if (!original?.retail_usd_per_1m && !original?.supplier_cny_per_1m && !original?.enabled && !change.riskBefore) {
      try { exactRisk(change.riskAfter) } catch { adoptionError.value = t('adoptionRiskRequired'); return }
    }
  }
  for (const change of changes) {
    const cell = pricing.value!.cells.find(item => cellKey(item) === change.key)!
    cell.supplier_cny_per_1m = change.costAfter; cell.risk_multiplier = change.riskAfter; cell.minimum_rule_version = change.minimumAfter
  }
  adoptionOpen.value = false; adoptionSnapshot.value = ''; notice.value = t('adoptionApplied')
}
async function syncVerification() {
  if (!canSyncVerification.value || !verificationOpen.value || !verification.value) return
  const generation = loadGeneration, operation = ++operationGeneration, endpoint = verificationBase.value
  const request = { expected_config_version: verification.value.config_version, profile_version: verification.value.profile_version }
  const current = () => generation === loadGeneration && operation === operationGeneration
  busy.value = true; verificationError.value = ''; error.value = ''; notice.value = ''; invalidate()
  const edit = editGeneration
  try {
    await api('POST', endpoint, request)
    if (!current()) return
    if (edit !== editGeneration || dirty.value) {
      verificationReloadRequired.value = true; verificationError.value = t('verificationChangedDuringSync'); return
    }
    const refreshedGeneration = loadGeneration + 1
    await load()
    if (loadGeneration === refreshedGeneration) {
      if (verification.value?.verified && pricing.value && !referenceError.value) notice.value = t('verificationSyncedNotice')
      else verificationError.value = t('verificationRefreshFailed')
      emit('verification-synced')
    }
  } catch (e) { if (current()) verificationError.value = errorMessage(e, t('verificationFailed')) }
  finally { if (current()) busy.value = false }
}
async function reloadVerification() { if (!busy.value && !dirty.value) await load() }
function officialSourceURL(value: string): string | undefined {
  try { const url = new URL(value); return url.protocol === 'https:' && !url.username && !url.password ? url.href : undefined } catch { return undefined }
}
function revisionText(r: Revision) { try { return JSON.stringify(JSON.parse(r.snapshot_json), null, 2) } catch { return r.snapshot_json } }
function changeLabel(change: Diff['change']) { return t(change === 'disabled' ? 'turnedOff' : change) }
function templateCellLabel(cell?: Cell | null) { return cell ? `${cell.resolution} · ${t(cell.input_video ? 'withVideo' : 'noVideo')}` : '—' }
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
watch(verificationProfile, () => { if (!busy.value && !dirty.value) void load() }, { flush: 'sync' })
onDeactivated(() => { ++loadGeneration; clear(); loading.value = false })
onBeforeUnmount(() => { ++loadGeneration; ++operationGeneration; if (typeof window !== 'undefined') window.removeEventListener('beforeunload', beforeUnload) })
onMounted(() => { if (typeof window !== 'undefined') window.addEventListener('beforeunload', beforeUnload); return load() })
onActivated(() => { if (!pricing.value && !loading.value) void load() })
defineExpose({ hasUnsavedChanges: () => dirty.value, discardChanges })
</script>

<template>
  <section class="video-pricing-page" :class="{ embedded }">
    <header class="page-heading"><div><h1>{{ model }}</h1><p>{{ t('unit') }}</p></div><span class="state" :class="{ dirty }">{{ dirty ? t('dirty') : t('clean') }}</span><label v-if="!embedded">{{ t('title') }} <select v-model="model" :disabled="busy || loading || dirty"><option v-for="name in officialVideoModels" :key="name" :value="name">{{ name }}</option></select></label></header>
    <p v-if="!auth.isSuperadmin" class="message">{{ t('unauthorized') }}</p><p v-else-if="!supported" class="message">{{ t('unsupported') }}</p>
    <p v-if="error" class="message error" role="alert">{{ error }}</p><p v-if="notice" class="message success" role="status">{{ notice }}</p>
    <label v-if="model === 'seedance-2.5' && auth.isSuperadmin && bindingMatches" class="panel">{{ t('verificationSelect') }} <select v-model="verificationProfile" :disabled="busy || loading || dirty"><option value="standard">{{ t('verificationStandard') }}</option><option value="draft">{{ t('verificationDraft') }}</option></select></label>
    <section v-if="verification && !loading && bindingMatches" class="panel verification-summary"><div><strong>{{ verification.verified ? t('verificationSynced') : t('verificationAvailable') }}</strong><p>{{ t('verificationSummary') }}</p></div><button :disabled="busy" @click="verificationOpen = true">{{ t('verificationPreview') }}</button></section>
    <p v-if="verificationError && !verificationOpen" class="message error" role="alert">{{ verificationError }}</p>
    <p v-if="loading" aria-live="polite">{{ t('loading') }}</p><p v-else-if="!bindingMatches" class="message error">{{ t('wrongBinding') }}</p>
    <template v-else-if="pricing">
      <nav class="tabs" role="tablist" :aria-label="t('title')"><button v-for="name in tabs" :id="`${tabsId}-${name}`" :key="name" type="button" role="tab" :class="{ active: tab === name }" :aria-selected="tab === name" :tabindex="tab === name ? 0 : -1" :aria-controls="`${tabsId}-panel`" @keydown="tabKeydown" @click="tab = name">{{ t(name) }}</button></nav>
      <div v-if="tab === 'retail'" class="tab-body" :id="`${tabsId}-panel`" role="tabpanel" :aria-labelledby="`${tabsId}-${tab}`">
        <p class="intro">{{ t('subtitle') }}</p><p class="intro">{{ t('automaticPricingHelp') }}</p>
        <section class="panel adoption-action"><div><h2>{{ t('templateTitle') }}</h2><p>{{ t('templateSummary') }}</p><p v-if="templateRef" class="evidence-hash">{{ t('templateDraftSource') }} · {{ templateRef.version }}</p><p v-if="templateError && !templateOpen" role="status">{{ t('templateLoadFailed') }}</p></div><button :disabled="busy || loading || !canEdit" @click="openTemplate">{{ t('templateAction') }}</button></section>
        <div class="matrix" role="group" :aria-label="t('retail')"><div class="matrix-heading"><strong>{{ t('resolution') }}</strong><strong>{{ t('noVideo') }}</strong><strong>{{ t('withVideo') }}</strong></div>
          <div v-for="resolution in resolutions" :key="resolution" class="matrix-row"><strong class="resolution">{{ resolution }}</strong><div v-for="input in [false, true]" :key="String(input)" class="matrix-value"><template v-for="cell in primaryCells.filter(c => c.resolution === resolution && c.input_video === input)" :key="cellKey(cell)"><SimpleVideoPriceCell :cell="cell" :original="originalByKey.get(cellKey(cell))" :reference="referenceCost(cell)" :selected="selectedKeys.includes(cellKey(cell))" :disabled="!canEdit || busy" @select="selectCell" @rules="tab = 'rules'" /></template><span v-if="!primaryCells.some(c => c.resolution === resolution && c.input_video === input)" class="empty-cell">{{ t('unavailable') }}</span></div></div>
        </div>
        <details v-if="pendingCells.length" class="panel"><summary>{{ t('candidates') }} · {{ pendingCells.length }}</summary><div class="extra-grid"><article v-for="cell in pendingCells" :key="cellKey(cell)"><h3>{{ cell.resolution }} · {{ cell.input_video ? t('withVideo') : t('noVideo') }}</h3><SimpleVideoPriceCell :cell="cell" :original="originalByKey.get(cellKey(cell))" :reference="referenceCost(cell)" :selected="selectedKeys.includes(cellKey(cell))" :disabled="!canEdit || busy" @select="selectCell" @rules="tab = 'rules'" /></article></div></details>
        <section v-if="otherPublished.length" class="panel"><h2>{{ t('otherModes') }} · {{ t('published') }}</h2><div class="extra-grid"><article v-for="cell in otherPublished" :key="cellKey(cell)"><h3>{{ cellKey(cell) }}</h3><SimpleVideoPriceCell :cell="cell" :original="originalByKey.get(cellKey(cell))" :selected="selectedKeys.includes(cellKey(cell))" :disabled="!canEdit || busy" @select="selectCell" @rules="tab = 'rules'" /></article></div></section>
        <details v-if="otherPending.length" class="panel"><summary>{{ t('otherModes') }} · {{ otherPending.length }}</summary><div class="extra-grid"><article v-for="cell in otherPending" :key="cellKey(cell)"><h3>{{ cellKey(cell) }}</h3><SimpleVideoPriceCell :cell="cell" :original="originalByKey.get(cellKey(cell))" :selected="selectedKeys.includes(cellKey(cell))" :disabled="!canEdit || busy" @select="selectCell" @rules="tab = 'rules'" /></article></div></details>
        <section class="panel bulk"><div class="section-title"><h2>{{ t('bulk') }}</h2><span>{{ t('selected', { count: selectedKeys.length }) }}</span></div><div class="bulk-controls"><select v-model="bulkMode" :disabled="busy" :aria-label="t('bulk')"><option value="same">{{ t('samePrice') }}</option><option value="percentage">{{ t('percentage') }}</option></select><input v-model="bulkValue" :disabled="busy" inputmode="decimal" :aria-label="t('value')" :placeholder="bulkMode === 'percentage' ? '10 / -5 (%)' : 'USD / 1M'"><button :disabled="busy || !canEdit || !selectedKeys.length" @click="previewBulk">{{ t('bulkPreview') }}</button></div><p>{{ t('bulkHelp') }}</p><p v-if="bulkError" role="alert" class="message error">{{ bulkError }}</p><div v-if="bulkChanges.length"><div class="table-scroll"><table><thead><tr><th>{{ t('key') }}</th><th>{{ t('before') }}</th><th>{{ t('after') }}</th></tr></thead><tbody><tr v-for="change in bulkChanges" :key="change.key"><td>{{ change.key }}</td><td>{{ change.before || '—' }}</td><td>{{ change.after }}</td></tr></tbody></table></div><button class="primary" :disabled="busy" @click="applyBulk">{{ t('apply') }}</button></div></section>
        <section class="panel adoption-action"><div><h2>{{ t('adoptOfficialCost') }}</h2><p>{{ t('adoptionHelp') }}</p></div><button :disabled="busy || !canEdit || !selectedKeys.length" @click="previewOfficialAdoption">{{ t('adoptOfficialCost') }} · {{ selectedKeys.length }}</button></section>
        <p class="preserved">{{ t('allPreserved') }} {{ t('disabledSaveHelp') }}</p>
      </div>
      <div v-else-if="tab === 'rules'" class="tab-body" :id="`${tabsId}-panel`" role="tabpanel" :aria-labelledby="`${tabsId}-${tab}`">
        <p>{{ t('rulesHelp') }}</p><p>{{ t('reserveHelp') }}</p>
        <section class="panel metadata"><div><small>{{ t('version') }}</small><strong>{{ pricing.pricing_version }}</strong></div><div><small>{{ t('config') }}</small><strong>{{ pricing.config_version }}</strong></div><div><small>{{ t('capability') }}</small><strong>{{ pricing.capability_version }}</strong></div><div><small>{{ t('fx') }}</small><strong>{{ pricing.fx_usd_per_cny }} · {{ pricing.fx_version }}</strong><small>{{ pricing.fx_observed_at }}</small></div><div><small>{{ t('status') }}</small><strong>{{ pricing.enabled ? t('published') : t('inactive') }}</strong></div><div><small>{{ t('scope') }}</small><strong>{{ pricing.ratios?.join(' / ') }} · {{ pricing.min_duration }}–{{ pricing.max_duration }} s</strong></div></section>
        <details class="panel"><summary>{{ t('advanced') }}</summary><div class="table-scroll"><table><thead><tr><th>{{ t('key') }}</th><th>{{ t('cost') }}</th><th>{{ t('risk') }}</th><th>{{ t('minimum') }}</th></tr></thead><tbody><tr v-for="cell in pricing.cells" :key="cellKey(cell)"><td>{{ cellKey(cell) }}</td><td><input v-model="cell.supplier_cny_per_1m" inputmode="decimal" :disabled="!canEdit || busy || !editableCell(cell)" :aria-label="`${t('cost')} ${cellKey(cell)}`"></td><td><input v-model="cell.risk_multiplier" inputmode="decimal" :disabled="!canEdit || busy || !editableCell(cell)" :aria-label="`${t('risk')} ${cellKey(cell)}`"></td><td><select v-if="cell.input_video" v-model="cell.minimum_rule_version" :disabled="!canEdit || busy || !editableCell(cell)" :aria-label="`${t('minimum')} ${cellKey(cell)}`"><option value="">{{ t('chooseEvidence') }}</option><option v-if="cell.minimum_rule_version && !minimums.some(m => m.version === cell.minimum_rule_version)" :value="cell.minimum_rule_version">{{ cell.minimum_rule_version }}</option><option v-for="item in minimums" :key="item.id" :value="item.version">{{ item.version }}</option></select><span v-else>{{ t('notApplicable') }}</span></td></tr></tbody></table></div></details>
      </div>
      <div v-else-if="tab === 'reference'" class="tab-body" :id="`${tabsId}-panel`" role="tabpanel" :aria-labelledby="`${tabsId}-${tab}`">
        <p>{{ t('referenceHelp') }}</p><p v-if="referenceError" class="message" role="status">{{ referenceError }}</p>
        <section v-if="candidates" class="panel"><h2>{{ t('reference') }} · {{ candidates.catalog_version }}</h2><div class="table-scroll"><table><thead><tr><th>{{ t('resolution') }}</th><th>{{ t('key') }}</th><th>{{ t('official') }}</th><th>{{ t('referenceStatus') }}</th><th>{{ t('capabilityStatus') }}</th><th>{{ t('sources') }}</th></tr></thead><tbody><tr v-for="cell in candidates.prices" :key="cell.key"><td>{{ cell.resolution }}</td><td>{{ cell.input_video ? t('withVideo') : t('noVideo') }}</td><td>{{ cell.supplier_cny_per_1m }}</td><td>{{ referenceCost(cell) !== undefined ? (verification?.verified ? t('verified') : t('referenceCollected')) : t('referenceIncomplete') }}</td><td>{{ capabilityReadyCell(cell) ? t('capabilityReady') : t('unavailable') }}</td><td><button @click="selectedEvidence = cell.source_rows">{{ t('details') }}</button></td></tr></tbody></table></div><p v-if="sourceFPSNotice">{{ sourceFPSNotice }}</p><button @click="showMinimum = true">{{ t('minimumReference') }} · {{ candidates.minimum.cells.length }}</button></section>
        <details class="panel"><summary>{{ t('reference') }} · {{ t('details') }}</summary><OfficialVideoPriceCatalog :allowed-series="referenceSeries" /></details>
        <section class="panel"><h2>{{ t('minimum') }}</h2><ul v-if="minimums.length"><li v-for="m in minimums" :key="m.id"><strong>{{ m.version }}</strong> · <a :href="m.source_url" target="_blank" rel="noopener noreferrer">{{ t('original') }}</a> · #{{ m.verified_by }} · {{ m.verified_at }}</li></ul><p v-else>{{ t('noMinimum') }}</p><details><summary>{{ t('importTitle') }}</summary><p>{{ t('importHelp') }}</p><textarea v-model="minimumJSON" :disabled="!canEdit || busy" rows="8" :aria-label="t('importTitle')" spellcheck="false"></textarea><button :disabled="!canEdit || busy || !minimumJSON.trim()" @click="importMinimum">{{ t('importAction') }}</button></details></section>
      </div>
      <div v-else class="tab-body" :id="`${tabsId}-panel`" role="tabpanel" :aria-labelledby="`${tabsId}-${tab}`"><p v-if="!revisions.length">{{ t('historyEmpty') }}</p><button v-for="r in revisions" :key="r.id" class="revision" @click="selectedRevision = r"><strong>{{ r.version }}</strong><span>{{ t('author') }} #{{ r.created_by }} · {{ r.created_at }}</span><span>{{ t('details') }} →</span></button></div>
      <footer class="pricing-footer"><span>{{ busy ? t('busy') : dirty ? t('dirty') : t('noChanges') }}</span><div class="footer-actions"><button v-if="dirty" :disabled="busy" @click="discardChanges">{{ t('discard') }}</button><button :disabled="busy || !canEdit || !priceDirty" @click="validate">{{ t('preview') }}</button><button class="primary" :disabled="!canPublish" :title="canPublish ? '' : t('validateFirst')" @click="publish">{{ t('publish') }}</button></div></footer>
    </template>
    <section v-else-if="!loading && auth.isSuperadmin && supported" class="panel"><p>{{ t('noConfig') }}</p><button @click="load">{{ t('reload') }}</button></section>
    <DetailDrawer v-model:open="templateOpen" :title="t('templateAction')" size="lg" :close-disabled="busy" :description="t('templateHelp')">
      <p v-if="templatesLoading" aria-live="polite">{{ t('templateLoading') }}</p>
      <p v-else-if="!templates.length" class="message">{{ t('templateEmpty') }}</p>
      <label v-if="templates.length" class="adoption-risk">{{ t('templateVersion') }}<select v-model="templateVersion" :disabled="busy"><option v-for="item in templates" :key="item.version" :value="item.version">{{ item.label || item.version }} · {{ item.version }}</option></select></label>
      <template v-if="selectedTemplate">
        <dl class="verification-metadata"><dt>{{ t('templateModel') }}</dt><dd>{{ selectedTemplate.model }}<small>{{ selectedTemplate.upstream_model }}</small></dd><dt>{{ t('templateSourceVersion') }}</dt><dd>{{ selectedTemplate.source_pricing_version }}</dd><dt>{{ t('templateSourceFX') }}</dt><dd>{{ selectedTemplate.fx_usd_per_cny }} · {{ selectedTemplate.fx_version }}<small>{{ selectedTemplate.fx_observed_at }}</small></dd><dt>{{ t('verificationProfile') }}</dt><dd>{{ selectedTemplate.profile_version }}</dd><dt>{{ t('minimum') }}</dt><dd>{{ selectedTemplate.minimum_rule_version }}<small>{{ t('minimumCellCount', { count: selectedTemplate.minimum_cell_count }) }}</small></dd><dt>{{ t('templateDigest') }}</dt><dd class="evidence-hash">{{ selectedTemplate.body_sha256 }}</dd></dl>
        <p>{{ t('templateFXHelp') }}</p>
        <div class="table-scroll"><table><thead><tr><th>{{ t('key') }}</th><th>{{ t('unit') }}</th><th>{{ t('cost') }}</th><th>{{ t('risk') }}</th></tr></thead><tbody><tr v-for="cell in selectedTemplate.cells" :key="cellKey(cell)"><td>{{ templateCellLabel(cell) }}</td><td>{{ cell.retail_usd_per_1m }}</td><td>{{ cell.supplier_cny_per_1m }}</td><td>{{ cell.risk_multiplier }}</td></tr></tbody></table></div>
        <fieldset class="template-modes" :disabled="busy"><legend>{{ t('templateMode') }}</legend><label><input v-model="templateMode" type="radio" value="fill_missing">{{ t('templateFill') }}<small>{{ t('templateFillHelp') }}</small></label><label><input v-model="templateMode" type="radio" value="replace">{{ t('templateReplace') }}<small>{{ t('templateReplaceHelp') }}</small></label></fieldset>
        <p v-if="dirty" class="message" role="status">{{ t('templateDirty') }}</p>
        <template v-if="templatePreview"><h2>{{ t('templateDiff', { count: templateChanges.length }) }}</h2><div class="table-scroll"><table><thead><tr><th>{{ t('key') }}</th><th>{{ t('change') }}</th><th>{{ t('before') }}</th><th>{{ t('after') }}</th></tr></thead><tbody><tr v-for="row in templatePreview.diff" :key="row.key"><td>{{ templateCellLabel(row.after ?? row.before) }}</td><td>{{ changeLabel(row.change) }}</td><td>{{ row.before?.retail_usd_per_1m || '—' }}<small>{{ t('cost') }}: {{ row.before?.supplier_cny_per_1m || '—' }}</small><small>{{ t('risk') }}: {{ row.before?.risk_multiplier || '—' }}</small><small>{{ t('minimum') }}: {{ row.before?.minimum_rule_version || '—' }}</small><small>{{ row.before?.enabled ? t('published') : t('disabled') }}</small></td><td>{{ row.after?.retail_usd_per_1m || '—' }}<small>{{ t('cost') }}: {{ row.after?.supplier_cny_per_1m || '—' }}</small><small>{{ t('risk') }}: {{ row.after?.risk_multiplier || '—' }}</small><small>{{ t('minimum') }}: {{ row.after?.minimum_rule_version || '—' }}</small><small>{{ row.after?.enabled ? t('published') : t('disabled') }}</small></td></tr></tbody></table></div><p v-if="!templateChanges.length" class="message">{{ t('templateNoChanges') }}</p><p v-else-if="!canApplyTemplate && !busy" class="message" role="status">{{ t('templateStale') }}</p></template>
      </template>
      <p v-if="templateError" class="message error" role="alert">{{ templateError }}</p>
      <template #footer><button v-if="!templates.length || templateError" :disabled="busy || templatesLoading" @click="loadTemplates()">{{ t('reload') }}</button><button :disabled="!canPreviewTemplate" @click="previewTemplate">{{ busy ? t('busy') : t('templatePreview') }}</button><button class="primary" :disabled="!canApplyTemplate" @click="applyTemplate">{{ t('templateApplyDraft') }}</button></template>
    </DetailDrawer>
    <DetailDrawer v-model:open="verificationOpen" :title="t('verificationPreview')" size="lg" :close-disabled="busy" :description="t('verificationHelp')">
      <template v-if="verification"><p class="message" :class="verification.verified ? 'success' : ''">{{ verification.verified ? t('verificationSynced') : t('verificationAvailable') }}</p><dl class="verification-metadata"><dt>{{ t('verificationProfile') }}</dt><dd>{{ verification.profile_version }}</dd><dt>{{ t('config') }}</dt><dd>{{ verification.config_version || '—' }}</dd><dt>{{ t('capability') }}</dt><dd>{{ verification.capability_version || '—' }}</dd><dt>{{ t('verificationEvidence') }}</dt><dd class="evidence-hash">{{ verification.evidence_sha256 }}</dd><dt>{{ t('verificationScope') }}</dt><dd>{{ verification.resolutions.join(' / ') }}<br>{{ verification.ratios.join(' / ') }}<br>{{ verification.min_duration }}–{{ verification.max_duration }} s</dd><dt>{{ t('minimum') }}</dt><dd>{{ verification.minimum_version }}<small>{{ t('minimumCellCount', { count: verification.minimum_cell_count }) }}</small></dd></dl>
      <p v-if="verification.minimum_precision">{{ t('minimumPrecision') }}: {{ verification.minimum_precision === 'estimated' ? t('estimatedMinimum') : verification.minimum_precision }}</p>
      <h2>{{ t('sources') }}</h2><article v-for="source in verification.sources" :key="source.id" class="source-row"><strong>{{ source.id }}</strong><p><a v-if="officialSourceURL(source.url)" :href="officialSourceURL(source.url)" target="_blank" rel="noopener noreferrer">{{ t('original') }} ↗</a><span v-else>{{ source.url }}</span></p><p class="evidence-hash">SHA-256 · {{ source.sha256 }}</p></article>
      <p v-if="dirty" class="message" role="status">{{ t('verificationDirty') }}</p><p v-if="verificationError" class="message error" role="alert">{{ verificationError }}</p></template>
      <template #footer><button v-if="dirty" :disabled="busy" @click="discardChanges">{{ t('discard') }}</button><button v-if="verificationReloadRequired || verificationError" :disabled="busy || dirty" @click="reloadVerification">{{ t('reload') }}</button><button v-if="!verificationReloadRequired" class="primary" :disabled="!canSyncVerification" @click="syncVerification">{{ busy ? t('busy') : t('verificationSync') }}</button></template>
    </DetailDrawer>
    <DetailDrawer v-model:open="adoptionOpen" :title="t('adoptOfficialCost')" size="lg" :close-disabled="busy" :description="t('adoptionHelp')">
      <p>{{ t('adoptionRulesHelp') }}</p><label class="adoption-risk">{{ t('risk') }}<input v-model="adoptionRisk" inputmode="decimal" :disabled="busy" :placeholder="t('adoptionRiskPlaceholder')"><small>{{ currentRisk ? t('adoptionCurrentRisk', { value: currentRisk }) : t('adoptionRiskRequired') }}</small></label>
      <div v-if="adoptionChanges.length" class="table-scroll"><table><thead><tr><th>{{ t('key') }}</th><th>{{ t('cost') }}</th><th>{{ t('risk') }}</th><th>{{ t('minimum') }}</th></tr></thead><tbody><tr v-for="change in adoptionChanges" :key="change.key"><td>{{ change.key }}</td><td>{{ change.costBefore || '—' }} → {{ change.costAfter || '—' }}</td><td>{{ change.riskBefore || '—' }} → {{ change.riskAfter || '—' }}</td><td>{{ change.minimumBefore || '—' }} → {{ change.minimumAfter || '—' }}</td></tr></tbody></table></div><p v-else>{{ t('adoptionEmpty') }}</p><p v-if="adoptionError" class="message error" role="alert">{{ adoptionError }}</p>
      <template #footer><button :disabled="busy" @click="adoptionOpen = false">{{ t('previous') }}</button><button class="primary" :disabled="busy || !canEdit || !adoptionChanges.length" @click="applyOfficialAdoption">{{ t('apply') }}</button></template>
    </DetailDrawer>
    <DetailDrawer v-model:open="previewOpen" :title="t('previewTitle')" size="lg" :close-disabled="busy" :description="t('allPreserved')"><template v-if="prepared"><div class="table-scroll"><table><thead><tr><th>{{ t('key') }}</th><th>{{ t('change') }}</th><th>{{ t('before') }}</th><th>{{ t('after') }}</th></tr></thead><tbody><tr v-for="row in prepared.diff" :key="row.key"><td>{{ row.key }}</td><td>{{ changeLabel(row.change) }}</td><td>{{ row.before?.retail_usd_per_1m || '—' }}<small>{{ row.before?.enabled ? t('published') : t('disabled') }}</small><small v-if="row.before && row.before.supplier_cny_per_1m !== row.after?.supplier_cny_per_1m">{{ t('cost') }}: {{ row.before.supplier_cny_per_1m || '—' }}</small><small v-if="row.before && row.before.risk_multiplier !== row.after?.risk_multiplier">{{ t('risk') }}: {{ row.before.risk_multiplier || '—' }}</small><small v-if="row.before && row.before.minimum_rule_version !== row.after?.minimum_rule_version">{{ t('minimum') }}: {{ row.before.minimum_rule_version || '—' }}</small></td><td>{{ row.after?.retail_usd_per_1m || '—' }}<small>{{ row.after?.enabled ? t('published') : t('disabled') }}</small><small v-if="row.after && row.after.supplier_cny_per_1m !== row.before?.supplier_cny_per_1m">{{ t('cost') }}: {{ row.after.supplier_cny_per_1m || '—' }}</small><small v-if="row.after && row.after.risk_multiplier !== row.before?.risk_multiplier">{{ t('risk') }}: {{ row.after.risk_multiplier || '—' }}</small><small v-if="row.after && row.after.minimum_rule_version !== row.before?.minimum_rule_version">{{ t('minimum') }}: {{ row.after.minimum_rule_version || '—' }}</small></td></tr></tbody></table></div><h2>{{ t('holdPreview') }}</h2><p>{{ t('reserveHelp') }}</p><div class="table-scroll"><table><thead><tr><th>{{ t('key') }}</th><th>{{ t('estimatedTokens') }}</th><th>{{ t('holdUSD') }}</th><th>{{ t('reserveCNY') }}</th></tr></thead><tbody><tr v-for="p in previews" :key="p.key"><td>{{ p.key }}</td><td>{{ p.maximum_estimated_tokens }}</td><td>{{ p.maximum_hold_usd }}</td><td>{{ p.supplier_reserved_cny }}</td></tr></tbody></table></div><details><summary>{{ t('sources') }}</summary><pre>{{ JSON.stringify(prepared.sources, null, 2) }}</pre></details><p v-if="error" role="alert" class="message error">{{ error }}</p></template><template #footer><button :disabled="busy" @click="previewOpen = false">{{ t('previous') }}</button><button class="primary" :disabled="!canPublish" @click="publish">{{ t('publish') }}</button></template></DetailDrawer>
    <DetailDrawer v-model:open="revisionOpen" :title="t('revisionTitle')" size="lg" :description="t('revisionReadOnly')"><template v-if="selectedRevision"><p>{{ selectedRevision.version }} · {{ selectedRevision.config_version }} · #{{ selectedRevision.created_by }} · {{ selectedRevision.created_at }}</p><pre>{{ revisionText(selectedRevision) }}</pre></template></DetailDrawer>
    <DetailDrawer v-model:open="referenceOpen" :title="showMinimum ? t('minimumReference') : t('sourceTitle')" size="lg" :description="t('referenceHelp')">
      <template v-if="candidates"><p>{{ candidates.catalog_version }}</p><p class="evidence-hash">{{ candidates.manifest_sha256 }}</p><label>{{ t('evidenceSearch') }}<input v-model="evidenceQuery" type="search"></label>
        <template v-if="showMinimum"><p v-if="sourceFPSNotice">{{ sourceFPSNotice }}</p><div class="table-scroll"><table><thead><tr><th>{{ t('resolution') }}</th><th>Ratio</th><th>ms</th><th>FPS</th><th>Token</th><th>{{ t('sources') }}</th></tr></thead><tbody><tr v-for="(c, i) in minimumEvidence" :key="i"><td>{{ c.resolution }}</td><td>{{ c.ratio }}</td><td>{{ c.output_duration_ms }}</td><td>{{ c.fps ?? t('unknown') }}</td><td>{{ c.minimum_tokens }}</td><td><button @click="showMinimum = false; selectedEvidence = c.source_rows">{{ t('details') }}</button></td></tr></tbody></table></div></template>
        <template v-else><article v-for="row in sourceEvidence" :key="`${row.source_id}:${row.row}`" class="source-row"><template v-if="row.source"><p><a :href="row.source.source_url" target="_blank" rel="noopener noreferrer">{{ t('original') }} ↗</a> · {{ row.row }}</p><p>{{ row.source.table_id }} / {{ row.source.view_id }} · {{ row.source.captured_at }}</p><p class="evidence-hash">{{ row.source.raw_sha256 }}</p><div class="table-scroll"><table><thead><tr><th v-for="(header, i) in row.source.headers" :key="i">{{ header }}<small>{{ row.source.columns[i]?.unit }} · {{ row.source.columns[i]?.precision }}</small></th></tr></thead><tbody><tr><td v-for="(cell, i) in row.source.raw_rows[row.row - 1]" :key="i">{{ cell || '—' }}</td></tr></tbody></table></div></template></article></template>
        <p v-if="!evidenceCount">{{ t('noEvidence') }}</p><nav class="actions"><button :disabled="evidencePage <= 1" @click="evidencePage--">{{ t('previous') }}</button><span>{{ evidencePage }} / {{ evidencePages }} · {{ evidenceCount }}</span><button :disabled="evidencePage >= evidencePages" @click="evidencePage++">{{ t('next') }}</button></nav>
      </template>
    </DetailDrawer>
  </section>
</template>

<style scoped>
.video-pricing-page{max-width:1120px;margin:auto;padding:24px;display:flex;flex-direction:column;gap:18px;color:rgb(var(--c-ink));min-width:0}
.video-pricing-page.embedded{padding:0;max-width:none;width:100%;min-height:100%}
.verification-summary,.adoption-action{display:flex;justify-content:space-between;align-items:center;gap:16px;flex-wrap:wrap}.verification-summary>div,.adoption-action>div{flex:1;min-width:220px}.verification-summary p,.adoption-action p{font-size:12px;margin:6px 0 0}.verification-metadata{display:grid;grid-template-columns:140px minmax(0,1fr);gap:14px;font-size:13px}.verification-metadata dt{color:rgb(var(--c-muted))}.verification-metadata dd{margin:0;overflow-wrap:anywhere}.adoption-risk{display:grid;gap:8px;margin:16px 0}.adoption-risk input{max-width:220px}
.template-modes{display:grid;gap:12px;border:1px solid rgb(var(--c-line));border-radius:10px;padding:16px;margin:20px 0}.template-modes legend{font-size:13px;padding:0 6px}.template-modes label{font-size:13px;cursor:pointer}.template-modes input{margin-right:8px}.template-modes small{padding-left:23px}.template-modes input:disabled+small{opacity:.5}
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
