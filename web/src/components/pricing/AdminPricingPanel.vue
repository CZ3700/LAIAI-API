<script setup lang="ts">
import { computed, nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { adminApi, errorMessage } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import DetailDrawer from '@/components/DetailDrawer.vue'
import VideoPricing from '@/pages/VideoPricing.vue'
import GenericPriceEditor from './GenericPriceEditor.vue'
import AdminPriceSummary from './AdminPriceSummary.vue'
import OfficialPriceEvidence from './OfficialPriceEvidence.vue'
import VariantPricePlan from './VariantPricePlan.vue'
import { modelType, modelTypes, officialPriceEvidence, pricingStatus, pricingStatuses, type ModelTypeFilter } from './catalogPresentation'
import type { PricingCatalog, PricingModel, PricingStatus } from './types'

const props = defineProps<{ refreshKey?: number }>()
const emit = defineEmits<{ 'edit-metadata': [row: { model: string; meta?: PricingModel['meta'] }]; 'dirty-change': [value: boolean]; 'busy-change': [value: boolean] }>()
const { t } = useI18n(), auth = useAuth(), route = useRoute(), router = useRouter()
const catalog = ref<PricingCatalog | null>(null), loading = ref(false), error = ref(''), notice = ref('')
const provider = ref(''), channel = ref(''), modelKey = ref(''), query = ref(''), detailsOpen = ref(false)
const kind = ref<ModelTypeFilter>(''), status = ref<PricingStatus | ''>(''), view = ref<'cards' | 'list'>('cards')
const drawerTab = ref<'pricing' | 'overview'>('pricing'), dirty = ref(false), editorBusy = ref(false), confirmDiscard = ref(false)
const keepEditingButton = ref<HTMLButtonElement | null>(null)
let generation = 0, applyingQuery = false, active = true
const channels = computed(() => catalog.value?.channels.filter(c => !provider.value || c.provider === provider.value) ?? [])
const rows = computed(() => {
  const search = query.value.trim().toLocaleLowerCase()
  return catalog.value?.models.filter(m => (!provider.value || m.provider === provider.value)
    && (!channel.value || m.channel_id === Number(channel.value))
    && (!kind.value || modelType(m) === kind.value) && (!status.value || pricingStatus(m, catalog.value) === status.value)
    && (!search || `${m.model} ${m.upstream_model} ${m.provider} ${providerLabel(m.provider)}`.toLocaleLowerCase().includes(search))) ?? []
})
const selected = computed(() => catalog.value?.models.find(row => row.key === modelKey.value) ?? null)
const selectedOfficialEvidence = computed(() => officialPriceEvidence(selected.value))
const drawerTabs = computed(() => selectedOfficialEvidence.value ? ['overview'] as const : ['pricing', 'overview'] as const)
const videoAllowed = computed(() => auth.isSuperadmin && !!catalog.value?.permissions.video_details)
const summary = computed(() => ({ total: catalog.value?.models.length ?? 0, published: catalog.value?.models.filter(row => ['published', 'partial'].includes(pricingStatus(row, catalog.value))).length ?? 0, attention: catalog.value?.models.filter(row => ['missing', 'blocked', 'partial'].includes(pricingStatus(row, catalog.value))).length ?? 0 }))
const impact = computed(() => catalog.value?.models.filter(row => row.model === selected.value?.model).length ?? 0)
const reasonKeys: Record<string, string> = { pricing_kind_unknown: 'reasonUnknown', official_video_configuration_missing: 'reasonMissing', official_video_channel_mismatch: 'reasonMismatch', official_video_configuration_ambiguous: 'reasonAmbiguous', seedance_variant_not_supported: 'reasonVariant', seedance_1x_excluded: 'reasonExcluded' }
function reason(code: string) { return t(`pricingx.${reasonKeys[code] ?? 'unsupported'}`) }
function statusLabel(row: PricingModel) { return t(pricingStatus(row, catalog.value) === 'blocked' && officialPriceEvidence(row) ? 'pricingAdmin.adapterNotIntegrated' : `pricingAdmin.${pricingStatus(row, catalog.value)}`) }
const contextKey = computed(() => `${generation}:${provider.value}:${channel.value}:${modelKey.value}`)
function providerLabel(id: string) { return catalog.value?.providers.find(p => p.name === id)?.display_name ?? id }
function channelLabel(id: number) { const ch = catalog.value?.channels.find(c => c.id === id); return ch ? `${ch.name} · ${t(ch.status === 1 ? 'pricingx.active' : 'pricingx.disabled')}` : `#${id}` }
function closeImmediately() { detailsOpen.value = false; modelKey.value = ''; dirty.value = false; editorBusy.value = false; confirmDiscard.value = false }
function resetSelection() { closeImmediately(); provider.value = ''; channel.value = ''; query.value = ''; kind.value = ''; status.value = '' }
function clearFilters() { provider.value = ''; channel.value = ''; query.value = ''; kind.value = ''; status.value = '' }
watch(provider, () => { channel.value = ''; closeImmediately() }, { flush: 'sync' })
watch(channel, closeImmediately, { flush: 'sync' })
watch(modelKey, () => { dirty.value = false; editorBusy.value = false; confirmDiscard.value = false }, { flush: 'sync' })
watch(dirty, value => emit('dirty-change', value), { flush: 'sync' })
watch(editorBusy, value => emit('busy-change', value), { flush: 'sync' })

const queryNames = ['provider', 'channel', 'type', 'status', 'search', 'view', 'model'] as const
function syncURL() {
  if (applyingQuery || !active || loading.value) return
  const values = [provider.value, channel.value, kind.value, status.value, query.value, view.value === 'list' ? 'list' : '', modelKey.value]
  const next = { ...route.query }
  queryNames.forEach((name, index) => { const key = `pricing_${name}`; if (values[index]) next[key] = values[index]; else delete next[key] })
  if (JSON.stringify(next) !== JSON.stringify(route.query)) void router.replace({ query: next })
}
function readQuery() {
  const read = (name: string) => typeof route.query[`pricing_${name}`] === 'string' ? route.query[`pricing_${name}`] as string : ''
  applyingQuery = true
  provider.value = catalog.value?.providers.some(p => p.name === read('provider')) ? read('provider') : ''
  channel.value = channels.value.some(c => String(c.id) === read('channel')) ? read('channel') : ''
  kind.value = modelTypes.includes(read('type') as Exclude<ModelTypeFilter, ''>) ? read('type') as ModelTypeFilter : ''
  status.value = pricingStatuses.includes(read('status') as PricingStatus) ? read('status') as PricingStatus : ''
  query.value = read('search'); view.value = read('view') === 'list' ? 'list' : 'cards'
  const wanted = catalog.value?.models.find(m => m.key === read('model'))
  modelKey.value = wanted?.key ?? ''; detailsOpen.value = !!wanted
  if (officialPriceEvidence(wanted ?? null)) drawerTab.value = 'overview'
  applyingQuery = false
}
watch([provider, channel, kind, status, query, view, modelKey, detailsOpen], syncURL, { flush: 'post' })
watch(() => route.query, () => {
  if (!active || loading.value || !catalog.value) return
  const requestedModel = typeof route.query.pricing_model === 'string' ? route.query.pricing_model : ''
  const changingContext = requestedModel !== modelKey.value || (route.query.pricing_provider ?? '') !== provider.value || (route.query.pricing_channel ?? '') !== channel.value
  if (changingContext && !confirmLeave()) { syncURL(); return }
  readQuery()
})
async function load(keepSelection = false) {
  const current = ++generation
  const previous = keepSelection ? { provider: provider.value, channel: channel.value, model: modelKey.value, query: query.value, kind: kind.value, status: status.value, view: view.value, open: detailsOpen.value } : null
  applyingQuery = true; resetSelection(); catalog.value = null; error.value = ''; loading.value = true; applyingQuery = false
  try {
    const data = await adminApi<PricingCatalog>('GET', '/api/admin/pricing-catalog')
    if (current !== generation) return
    catalog.value = data
    if (previous) {
      applyingQuery = true
      provider.value = previous.provider; channel.value = previous.channel; query.value = previous.query; kind.value = previous.kind; status.value = previous.status; view.value = previous.view
      if (data.models.some(m => m.key === previous.model)) { modelKey.value = previous.model; detailsOpen.value = previous.open }
      applyingQuery = false
    } else readQuery()
  } catch (e) { if (current === generation) error.value = errorMessage(e, t('pricingx.loadFailed')) }
  finally { if (current === generation) loading.value = false }
}
async function saved(rowKey: string, sourceContext: string) {
  if (rowKey !== modelKey.value || sourceContext !== contextKey.value) return
  dirty.value = false; editorBusy.value = false
  const expected = generation + 1
  await load(true)
  if (expected === generation && !error.value) notice.value = t('pricingx.saved')
}
function videoPublished() { if (selected.value) void saved(selected.value.key, contextKey.value) }
function verificationSynced() { void load(true) }
async function variantPlanSaved(rowKey: string, sourceContext: string) {
  if (rowKey !== modelKey.value || sourceContext !== contextKey.value) return
  editorBusy.value = false
  const expected = generation + 1
  await load(true)
  if (expected === generation && !error.value) notice.value = t('pricingAdmin.variantPlanSavedNotice')
}
function openModel(row: PricingModel, tab: 'pricing' | 'overview' = 'pricing') { modelKey.value = row.key; drawerTab.value = officialPriceEvidence(row) ? 'overview' : tab; detailsOpen.value = true; notice.value = '' }
async function requestClose(open = false) {
  if (open) { detailsOpen.value = true; return }
  if (editorBusy.value) return
  if (!dirty.value) { closeImmediately(); return }
  confirmDiscard.value = true; await nextTick(); keepEditingButton.value?.focus()
}
function confirmLeave() { return !editorBusy.value && (!dirty.value || window.confirm(t('pricingAdmin.leavePrompt'))) }
function beforeUnload(event: BeforeUnloadEvent) { if (dirty.value || editorBusy.value) { event.preventDefault(); event.returnValue = '' } }
function editMetadata() { if (selected.value && confirmLeave()) { const row = selected.value; closeImmediately(); emit('edit-metadata', { model: row.model, meta: row.meta }) } }
function tabKeydown(event: KeyboardEvent) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  if (selectedOfficialEvidence.value) return
  drawerTab.value = event.key === 'Home' ? 'pricing' : event.key === 'End' ? 'overview' : drawerTab.value === 'pricing' ? 'overview' : 'pricing'
  const container = (event.currentTarget as HTMLElement).parentElement
  void nextTick(() => container?.querySelector<HTMLButtonElement>(`#pricing-drawer-tab-${drawerTab.value}`)?.focus())
}
watch(() => [auth.orgId, auth.token, auth.role, props.refreshKey], () => { ++generation; notice.value = ''; void load() }, { flush: 'sync' })
onBeforeRouteLeave(() => confirmLeave())
onMounted(() => { window.addEventListener('beforeunload', beforeUnload); return load() })
onActivated(() => { active = true; if (!catalog.value && !loading.value) void load() })
onDeactivated(() => { active = false; ++generation; resetSelection(); catalog.value = null; loading.value = false; notice.value = '' })
onBeforeUnmount(() => { active = false; ++generation; resetSelection(); window.removeEventListener('beforeunload', beforeUnload) })
defineExpose({ hasUnsavedChanges: () => dirty.value, isBusy: () => editorBusy.value, confirmLeave })
</script>

<template>
  <section class="provider-pricing">
    <header class="catalog-header"><div><span class="eyebrow">{{ t('pricingx.title') }}</span><h2>{{ t('pricingAdmin.title') }}</h2><p>{{ t('pricingAdmin.intro') }}</p></div><button class="btn-ghost" :disabled="loading || editorBusy || dirty" @click="load(true)">{{ t('pricingx.reload') }}</button></header>
    <div class="catalog-stats"><div><span>{{ t('pricingAdmin.connectedModels') }}</span><strong>{{ summary.total }}</strong></div><div><span>{{ t('pricingAdmin.publishedModels') }}</span><strong>{{ summary.published }}</strong></div><div><span>{{ t('pricingAdmin.actionNeeded') }}</span><strong>{{ summary.attention }}</strong></div></div>
    <p v-if="error" role="alert" class="text-danger">{{ error }}</p><p v-if="notice" role="status" class="notice">{{ notice }}</p>
    <p v-if="route.query.pricing_legacy" role="status" class="notice">{{ t('pricingAdmin.legacyNotice') }}</p>
    <div class="catalog-toolbar"><label class="search-field"><span class="sr-only">{{ t('pricingAdmin.search') }}</span><svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 4.5 4.5"/></svg><input v-model="query" type="search" :placeholder="t('pricingAdmin.search')" :disabled="loading"></label><div class="view-toggle" role="group" :aria-label="t('pricingAdmin.viewMode')"><button v-for="mode in (['cards', 'list'] as const)" :key="mode" :aria-pressed="view === mode" :class="{ active: view === mode }" @click="view = mode">{{ t(`pricingAdmin.${mode}`) }}</button></div></div>
    <div class="catalog-filters">
      <label>{{ t('pricingx.provider') }}<select v-model="provider" :disabled="loading"><option value="">{{ t('pricingAdmin.allProviders') }}</option><option v-for="p in catalog?.providers" :key="p.name" :value="p.name">{{ p.display_name }} ({{ p.name }})</option></select></label>
      <label>{{ t('pricingAdmin.modelType') }}<select v-model="kind" :disabled="loading"><option value="">{{ t('pricingAdmin.allTypes') }}</option><option v-for="type in modelTypes" :key="type" :value="type">{{ t(`pricingAdmin.${type}`) }}</option></select></label>
      <label>{{ t('pricingx.channel') }}<select v-model="channel" :disabled="!channels.length || loading"><option value="">{{ t('pricingx.allChannels') }}</option><option v-for="ch in channels" :key="ch.id" :value="String(ch.id)">{{ channelLabel(ch.id) }}</option></select></label>
      <label>{{ t('pricingAdmin.status') }}<select v-model="status" :disabled="loading"><option value="">{{ t('pricingAdmin.allStatuses') }}</option><option v-for="value in pricingStatuses" :key="value" :value="value">{{ t(`pricingAdmin.${value}`) }}</option></select></label>
    </div>
    <div class="results-bar"><span>{{ t('pricingAdmin.results', { count: rows.length }) }}</span><button v-if="provider || kind || channel || status || query" class="text-action" @click="clearFilters">{{ t('pricingAdmin.clearFilters') }}</button></div>
    <div v-if="loading" class="catalog-empty" role="status">{{ t('pricingAdmin.loading') }}</div>
    <div v-else-if="!rows.length" class="catalog-empty" role="status"><span class="empty-symbol" aria-hidden="true">◇</span><strong>{{ provider && !channels.length ? t('pricingx.noChannels') : t(catalog?.models.length ? 'pricingAdmin.noResults' : 'pricingAdmin.noModels') }}</strong></div>
    <div v-else-if="view === 'cards'" class="model-grid">
      <article v-for="row in rows" :key="row.key" class="model-card" @click="openModel(row, 'overview')">
        <div class="card-top"><span class="provider-monogram" aria-hidden="true">{{ providerLabel(row.provider).slice(0, 1).toLocaleUpperCase() }}</span><div><span class="provider-name">{{ providerLabel(row.provider) }}</span><span class="type-label">{{ t(`pricingAdmin.${modelType(row)}`) }}</span></div><span class="status-badge" :class="`status-${pricingStatus(row, catalog)}`">{{ statusLabel(row) }}</span></div>
        <button class="model-title" @click.stop="openModel(row, 'overview')">{{ row.model }}</button><p class="channel-name">{{ channelLabel(row.channel_id) }}</p>
        <div class="card-price"><AdminPriceSummary :row="row" /></div>
        <p v-if="row.unsupported_reason && !officialPriceEvidence(row)" class="blocked-reason">{{ reason(row.unsupported_reason) }}</p>
        <button class="configure-button" @click.stop="openModel(row)">{{ t(officialPriceEvidence(row) ? 'pricingAdmin.viewEvidence' : 'pricingAdmin.configure') }}<span aria-hidden="true">↗</span></button>
      </article>
    </div>
    <div v-else class="model-table-wrap"><table class="model-table"><thead><tr><th>{{ t('pricingAdmin.model') }}</th><th>{{ t('pricingAdmin.connection') }}</th><th>{{ t('pricingAdmin.price') }}</th><th>{{ t('pricingAdmin.status') }}</th><th>{{ t('pricingAdmin.actions') }}</th></tr></thead><tbody><tr v-for="row in rows" :key="row.key"><td><button class="model-title" @click="openModel(row, 'overview')">{{ row.model }}</button><small>{{ providerLabel(row.provider) }} · {{ t(`pricingAdmin.${modelType(row)}`) }}</small></td><td>{{ channelLabel(row.channel_id) }}</td><td><AdminPriceSummary :row="row" /></td><td><span class="status-badge" :class="`status-${pricingStatus(row, catalog)}`">{{ statusLabel(row) }}</span></td><td><button class="text-action" @click="openModel(row)">{{ t(officialPriceEvidence(row) ? 'pricingAdmin.viewEvidence' : 'pricingAdmin.configure') }}</button></td></tr></tbody></table></div>
    <p class="catalog-footnote">{{ t('pricingAdmin.channelModel') }}</p>
    <DetailDrawer :open="detailsOpen" :title="selected?.model ?? t('pricingAdmin.pricing')" :description="selected ? `${providerLabel(selected.provider)} · ${channelLabel(selected.channel_id)}` : ''" :close-disabled="editorBusy" size="lg" @update:open="requestClose">
      <template v-if="selected">
        <section v-if="confirmDiscard" class="discard-banner" role="alert"><strong>{{ t('pricingAdmin.discardTitle') }}</strong><p>{{ t('pricingAdmin.discardBody') }}</p><div><button ref="keepEditingButton" class="btn-primary" @click="confirmDiscard = false">{{ t('pricingAdmin.keepEditing') }}</button><button class="btn-ghost" @click="closeImmediately">{{ t('pricingAdmin.discard') }}</button></div></section>
        <div class="drawer-status"><span class="status-badge" :class="`status-${pricingStatus(selected, catalog)}`">{{ statusLabel(selected) }}</span><span v-if="dirty" class="dirty-indicator">{{ t('pricingAdmin.dirty') }}</span><span v-if="editorBusy" role="status">{{ t('pricingAdmin.busyNote') }}</span></div>
        <div class="drawer-tabs" role="tablist"><button v-for="tab in drawerTabs" :key="tab" :id="`pricing-drawer-tab-${tab}`" role="tab" :aria-selected="drawerTab === tab" :aria-controls="`pricing-drawer-panel-${tab}`" :tabindex="drawerTab === tab ? 0 : -1" :class="{ active: drawerTab === tab }" @click="drawerTab = tab" @keydown="tabKeydown">{{ t(`pricingAdmin.${tab}`) }}</button></div>
        <div v-if="!selectedOfficialEvidence" v-show="drawerTab === 'pricing'" id="pricing-drawer-panel-pricing" role="tabpanel" aria-labelledby="pricing-drawer-tab-pricing" class="drawer-editor">
          <template v-if="selected.pricing_kind === 'video' || selected.video_model">
            <p v-if="selected.video_model && selected.unsupported_reason" class="pricing-note">{{ reason(selected.unsupported_reason) }}</p><p v-if="!videoAllowed" class="pricing-note">{{ t('pricingx.superadmin') }}</p>
            <VideoPricing v-else-if="selected.video_model && ['seedance-2.0', 'seedance-2.5'].includes(selected.video_model)" :key="selected.key" embedded :selected-model="selected.video_model" :selected-channel-id="selected.channel_id" :context-key="contextKey" @dirty-change="dirty = $event" @busy-change="editorBusy = $event" @published="videoPublished" @verification-synced="verificationSynced" />
            <p v-else class="pricing-note">{{ reason(selected.unsupported_reason) }}</p>
          </template>
          <p v-else-if="selected.unsupported_reason && selected.unsupported_reason !== 'pricing_kind_unknown'" class="pricing-note">{{ reason(selected.unsupported_reason) }}</p>
          <template v-else><p class="scope-note">{{ t('pricingAdmin.sharedScope', { count: impact }) }}</p><GenericPriceEditor :key="selected.key" :row="selected" :context-key="contextKey" @saved="saved" @dirty-change="dirty = $event" @busy-change="editorBusy = $event" /></template>
        </div>
        <div v-show="drawerTab === 'overview'" id="pricing-drawer-panel-overview" role="tabpanel" aria-labelledby="pricing-drawer-tab-overview" class="drawer-overview">
          <AdminPriceSummary :row="selected" expanded /><VariantPricePlan v-if="selectedOfficialEvidence && videoAllowed" :key="selected.key" :row="selected" :context-key="contextKey" @busy-change="editorBusy = $event" @saved="variantPlanSaved" /><OfficialPriceEvidence v-if="selectedOfficialEvidence" :evidence="selectedOfficialEvidence" /><dl class="pricing-detail"><dt>{{ t('pricingx.publicModel') }}</dt><dd>{{ selected.model }}</dd><dt>{{ t('pricingx.upstreamModel') }}</dt><dd>{{ selected.upstream_model }}</dd><dt>{{ t('pricingx.provider') }}</dt><dd>{{ providerLabel(selected.provider) }} ({{ selected.provider }})</dd><dt>{{ t('pricingx.channel') }}</dt><dd>{{ channelLabel(selected.channel_id) }}</dd><dt>{{ t('pricingx.billingModes') }}</dt><dd>{{ t(`pricingx.${selected.pricing_kind}`) }} · {{ selected.billing_modes.join(', ') || '—' }}</dd></dl>
          <p v-if="!selectedOfficialEvidence">{{ t(selected.video_model ? 'pricingAdmin.boundScope' : 'pricingx.globalScope') }}</p><p>{{ t('pricingAdmin.unitNote') }}</p><p>{{ t('pricingx.noAutomaticPrice') }}</p><button class="btn-ghost" :disabled="editorBusy" @click="editMetadata">{{ t('pricingAdmin.editMetadata') }}</button>
        </div>
      </template>
    </DetailDrawer>
  </section>
</template>

<style scoped>
.provider-pricing{display:grid;gap:20px;min-width:0}.catalog-header{display:flex;justify-content:space-between;gap:20px;align-items:flex-start}.eyebrow{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:rgb(var(--c-muted))}.catalog-header h2{font-size:26px;letter-spacing:-.04em;font-weight:650;margin:5px 0 8px}.catalog-header p,.catalog-footnote{margin:0;color:rgb(var(--c-muted));line-height:1.6;font-size:13px}.catalog-stats{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px}.catalog-stats>div{display:flex;align-items:center;justify-content:space-between;gap:12px;border:1px solid rgb(var(--c-line));background:rgb(var(--c-panel));border-radius:12px;padding:17px 20px}.catalog-stats span{color:rgb(var(--c-muted));font-size:12px}.catalog-stats strong{font-size:26px;font-weight:600;font-variant-numeric:tabular-nums}.catalog-toolbar{display:flex;gap:16px;align-items:center}.search-field{display:flex;gap:11px;align-items:center;border:1px solid rgb(var(--c-line));border-radius:10px;background:rgb(var(--c-panel));padding:0 13px;flex:1}.search-field svg{width:18px;flex:none;color:rgb(var(--c-muted))}.search-field input{width:100%;min-width:0;border:0;background:transparent;padding:13px 0;color:rgb(var(--c-ink));font-size:13px;outline-offset:4px}.view-toggle{display:flex;padding:4px;background:rgb(var(--c-elev));border:1px solid rgb(var(--c-line));border-radius:9px}.view-toggle button{padding:7px 14px;border-radius:6px;font-size:12px;color:rgb(var(--c-muted))}.view-toggle .active{background:rgb(var(--c-panel));color:rgb(var(--c-ink));box-shadow:0 1px 3px rgb(0 0 0 / .08)}.catalog-filters{display:grid;grid-template-columns:1.4fr 1fr 1.2fr 1fr;gap:14px}.catalog-filters label{display:grid;gap:7px;font-size:12px;color:rgb(var(--c-muted))}.catalog-filters select{min-width:0;width:100%;color:rgb(var(--c-ink));background:rgb(var(--c-panel));border:1px solid rgb(var(--c-line));padding:10px;border-radius:8px;font-size:13px}.results-bar{display:flex;justify-content:space-between;gap:16px;align-items:center;font-size:12px;color:rgb(var(--c-muted))}.text-action{font-size:12px;font-weight:600;color:rgb(var(--c-accent));text-align:start}.model-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(min(100%,290px),1fr));gap:18px}.model-card{display:flex;flex-direction:column;min-width:0;padding:20px;background:rgb(var(--c-panel));border:1px solid rgb(var(--c-line));border-radius:13px;transition:border-color .15s,box-shadow .15s}.model-card:hover{border-color:rgb(var(--c-accent) / .45);box-shadow:0 6px 20px rgb(0 0 0 / .035)}.card-top{display:flex;align-items:center;gap:10px;margin-bottom:20px}.provider-monogram{display:grid;place-items:center;flex:none;width:36px;height:36px;background:rgb(var(--c-elev));border:1px solid rgb(var(--c-line));border-radius:9px;color:rgb(var(--c-accent));font-weight:650;font-size:15px}.card-top>div{flex:1;min-width:0}.provider-name{font-size:12px;display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.type-label{display:block;font-size:11px;color:rgb(var(--c-muted));margin-top:2px}.status-badge{display:inline-flex;align-items:center;gap:5px;white-space:nowrap;padding:4px 7px;border-radius:5px;font-size:10px;font-weight:550;background:rgb(var(--c-elev));color:rgb(var(--c-muted));width:max-content}.status-badge:before{content:'';width:5px;height:5px;border-radius:50%;background:currentColor}.status-published{background:rgb(16 185 129 / .1);color:#16835f}.status-partial{background:rgb(59 130 246 / .1);color:#3979c5}.status-blocked,.status-missing{background:rgb(217 119 6 / .09);color:#ac6a13}.model-title{font-size:15px;font-weight:650;line-height:1.55;text-align:start;overflow-wrap:anywhere;color:rgb(var(--c-ink))}.model-title:hover{color:rgb(var(--c-accent))}.channel-name{font-size:11px;margin:5px 0 0;color:rgb(var(--c-muted));overflow-wrap:anywhere}.card-price{margin:18px 0;padding-top:17px;border-top:1px solid rgb(var(--c-line));min-height:82px;flex:1}.blocked-reason{font-size:11px;color:rgb(var(--c-muted));line-height:1.6;margin:0 0 12px}.configure-button{display:flex;justify-content:space-between;align-items:center;padding:10px 12px;border:1px solid rgb(var(--c-line));border-radius:7px;font-size:12px;font-weight:550}.configure-button:hover{background:rgb(var(--c-elev));color:rgb(var(--c-accent))}.model-table-wrap{overflow-x:auto;border:1px solid rgb(var(--c-line));border-radius:12px;background:rgb(var(--c-panel))}.model-table{width:100%;min-width:760px;border-collapse:collapse;font-size:12px;text-align:start}.model-table th{background:rgb(var(--c-elev));color:rgb(var(--c-muted));font-weight:500;padding:13px 17px;white-space:nowrap}.model-table td{padding:18px 17px;border-top:1px solid rgb(var(--c-line));vertical-align:middle}.model-table td:first-child{max-width:270px}.model-table small{display:block;color:rgb(var(--c-muted));font-size:11px;margin-top:5px}.catalog-empty{display:grid;place-items:center;gap:12px;min-height:220px;padding:35px;text-align:center;border:1px dashed rgb(var(--c-line));border-radius:12px;color:rgb(var(--c-muted));background:rgb(var(--c-panel));font-size:13px}.catalog-empty strong{font-weight:500}.empty-symbol{font-size:36px;opacity:.5}.notice{padding:12px 15px;border-radius:8px;background:rgb(16 185 129 / .08);font-size:13px}.drawer-status{display:flex;flex-wrap:wrap;align-items:center;gap:12px;margin-bottom:16px;font-size:12px}.dirty-indicator{color:#ac6a13}.drawer-tabs{display:flex;gap:24px;border-bottom:1px solid rgb(var(--c-line));margin-bottom:20px}.drawer-tabs button{padding:10px 0 13px;font-size:13px;color:rgb(var(--c-muted));border-bottom:2px solid transparent}.drawer-tabs button.active{border-color:rgb(var(--c-accent));color:rgb(var(--c-accent))}.drawer-editor{min-width:0}.scope-note,.pricing-note{background:rgb(var(--c-elev));padding:13px 15px;border-radius:8px;font-size:12px;line-height:1.7;margin:0 0 20px;color:rgb(var(--c-muted))}.drawer-overview{display:grid;gap:22px}.drawer-overview>p{font-size:13px;line-height:1.7;color:rgb(var(--c-muted));margin:0}.drawer-overview>.btn-ghost{justify-self:start}.pricing-detail{display:grid;grid-template-columns:140px 1fr;gap:15px;font-size:13px;border-block:1px solid rgb(var(--c-line));padding:20px 0;margin:0}.pricing-detail dt{color:rgb(var(--c-muted))}.pricing-detail dd{overflow-wrap:anywhere;margin:0}.discard-banner{position:sticky;top:-20px;z-index:3;padding:20px;margin:-20px -20px 20px;background:rgb(var(--c-panel));border-bottom:1px solid rgb(var(--c-accent));box-shadow:0 8px 20px rgb(0 0 0 / .1)}.discard-banner strong{font-size:15px}.discard-banner p{font-size:13px;line-height:1.6;color:rgb(var(--c-muted));margin:8px 0 16px}.discard-banner>div{display:flex;gap:10px}@media(max-width:900px){.catalog-filters{grid-template-columns:repeat(2,minmax(0,1fr))}.catalog-stats>div{padding:14px}}@media(max-width:560px){.catalog-header h2{font-size:23px}.catalog-stats{gap:8px}.catalog-stats>div{flex-direction:column;align-items:flex-start;padding:13px;gap:5px}.catalog-stats strong{font-size:22px}.catalog-toolbar{align-items:stretch;flex-direction:column;gap:10px}.view-toggle{align-self:flex-end}.catalog-filters{grid-template-columns:1fr}.model-card{padding:17px}.pricing-detail{grid-template-columns:1fr;gap:6px}.pricing-detail dd{margin-bottom:10px}.catalog-header{gap:10px}.catalog-header>.btn-ghost{font-size:12px;white-space:nowrap}.discard-banner{margin:-16px -16px 16px;top:-16px;padding:16px}}@media(prefers-reduced-motion:reduce){.model-card{transition:none}}
</style>
