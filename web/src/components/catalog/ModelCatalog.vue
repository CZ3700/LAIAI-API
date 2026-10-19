<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import DetailDrawer from '@/components/DetailDrawer.vue'
import ModelPriceSummary from './ModelPriceSummary.vue'
import { api, copyText, errorMessage } from '@/lib/api'
import { catalogCategory, catalogPricing, catalogTags, filterModels, modelDocs, modelSnippet, type CatalogCategory, type PricedModel, type SnippetLanguage } from '@/lib/modelCatalog'
defineProps<{ console?: boolean }>()
const { t } = useI18n()
const route = useRoute(), router = useRouter()
const models = ref<PricedModel[]>([]), loading = ref(true), error = ref(''), copied = ref('')
const search = ref(''), provider = ref(''), category = ref(''), unit = ref(''), tag = ref(''), endpoint = ref(''), view = ref('cards'), selectedID = ref('')
const showFilters = ref(true), detailTab = ref('overview'), language = ref<SnippetLanguage>('curl')
const filterRefs = { search, provider, type: category, unit, tag, endpoint, view, model: selectedID }
const categories: CatalogCategory[] = ['text', 'image', 'video', 'embedding', 'audio', 'other']
const categoryLabel = (v: string) => t(`catalog.${v === 'image' ? 'imageType' : v}`)
const unitKeys: Record<string, string> = { 'USD/1M_tokens': 'unitToken', 'USD/image': 'unitImage', 'USD/call': 'unitCall', 'USD/1K_characters': 'unitChars', 'USD/1M_audio_tokens': 'unitAudio', 'USD/1M_video_tokens': 'unitVideo', mixed: 'mixed', unknown: 'unknown' }
const unitLabel = (v: string) => unitKeys[v] ? t(`catalog.${unitKeys[v]}`) : v
const providers = computed(() => [...new Set(models.value.map(m => m.provider))].sort())
const units = computed(() => [...new Set(models.value.flatMap(m => { const p = catalogPricing(m); return p ? [p.unit, ...p.rates.map(r => r.unit)] : [] }))].sort())
const tags = computed(() => [...new Set(models.value.flatMap(catalogTags))].sort())
const endpoints = computed(() => [...new Set(models.value.flatMap(m => catalogPricing(m)?.endpoints || []))].sort())
const counts = computed(() => Object.fromEntries(categories.map(c => [c, models.value.filter(m => catalogCategory(m) === c).length])))
const filtered = computed(() => filterModels(models.value, search.value).filter(m => {
  const p = catalogPricing(m)
  return (!provider.value || provider.value === m.provider) && (!category.value || category.value === catalogCategory(m)) && (!unit.value || p?.unit === unit.value || p?.rates.some(r => r.unit === unit.value)) && (!tag.value || catalogTags(m).includes(tag.value)) && (!endpoint.value || p?.endpoints.includes(endpoint.value))
}))
const selected = computed(() => models.value.find(m => m.model === selectedID.value))
const selectedPricing = computed(() => selected.value ? catalogPricing(selected.value) : undefined)
const snippet = computed(() => selected.value ? modelSnippet(selected.value, language.value, window.location.origin) : '')
const activeFilters = computed(() => !!(search.value || provider.value || category.value || unit.value || tag.value || endpoint.value))
let copyTimer: ReturnType<typeof setTimeout> | undefined, loadGeneration = 0
watch(() => route.query, query => {
  for (const [key, target] of Object.entries(filterRefs)) { const value = query[`catalog_${key}`]; target.value = typeof value === 'string' ? value : key === 'view' ? 'cards' : '' }
}, { immediate: true })
watch(Object.values(filterRefs), () => {
  const query = { ...route.query }
  for (const [key, value] of Object.entries(filterRefs)) { if (!value.value || (key === 'view' && value.value === 'cards')) delete query[`catalog_${key}`]; else query[`catalog_${key}`] = value.value }
  if (JSON.stringify(query) !== JSON.stringify(route.query)) void router.replace({ query })
}, { flush: 'post' })
watch(selectedID, () => { detailTab.value = 'overview'; copied.value = '' })
function resetFilters() { search.value = provider.value = category.value = unit.value = tag.value = endpoint.value = '' }
function selectModel(model: PricedModel) { selectedID.value = model.model }
function moveDetailTab(event: KeyboardEvent) {
  const tabs = ['overview', 'prices', 'api'], index = tabs.indexOf(detailTab.value)
  const target = event.key === 'ArrowRight' ? (index + 1) % tabs.length : event.key === 'ArrowLeft' ? (index + tabs.length - 1) % tabs.length : event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : -1
  if (target < 0) return
  event.preventDefault(); detailTab.value = tabs[target]!
  document.getElementById('catalog-tab-' + detailTab.value)?.focus()
}
async function copy(value: string, key: string) { if (await copyText(value)) { copied.value = key; clearTimeout(copyTimer); copyTimer = setTimeout(() => { copied.value = '' }, 1500) } }
async function load() {
  const generation = ++loadGeneration
  loading.value = true; error.value = ''
  try { const response = await api<{ models: PricedModel[] }>('GET', '/api/pricing', undefined, { auth: false }); if (generation === loadGeneration) models.value = response.models }
  catch (e) { if (generation === loadGeneration) error.value = errorMessage(e, t('common.error')) }
  finally { if (generation === loadGeneration) loading.value = false }
}
onMounted(load)
onBeforeUnmount(() => { loadGeneration++; clearTimeout(copyTimer) })
</script>
<template>
  <div :class="console ? 'app-page catalog-page' : 'catalog-page mx-auto max-w-[1440px] px-4 py-10 sm:px-6 lg:px-8'">
    <header class="catalog-header"><div><p class="app-eyebrow">{{ t('surface.modelsCatalog') }}</p><h1 class="app-page-title mt-2">{{ t('models.title') }}</h1><p class="mt-3 max-w-3xl text-sm leading-6 text-muted">{{ t('catalog.intro') }}</p></div><div class="catalog-count"><strong>{{ loading ? '—' : models.length }}</strong><span>{{ t('surface.statModels') }}</span><span>{{ providers.length }} {{ t('surface.statProviders') }}</span></div></header>
    <div class="mt-7 flex flex-wrap items-center gap-3"><button class="btn-ghost text-sm" type="button" :aria-expanded="showFilters" aria-controls="catalog-filters" @click="showFilters = !showFilters">{{ t('catalog.filters') }}</button><label class="grow min-w-0"><span class="sr-only">{{ t('models.search') }}</span><input v-model="search" class="input w-full" :placeholder="t('models.search')" type="search" /></label><div class="catalog-view" role="group" :aria-label="t('catalog.cards') + ' / ' + t('catalog.list')"><button v-for="mode in ['cards', 'list']" :key="mode" type="button" :aria-pressed="view === mode" @click="view = mode">{{ t('catalog.' + mode) }}</button></div></div>
    <div class="mt-4 flex flex-wrap gap-2" role="group" :aria-label="t('catalog.type')"><button type="button" class="catalog-chip" :aria-pressed="!category" @click="category = ''">{{ t('catalog.all') }} <span>{{ models.length }}</span></button><button v-for="kind in categories.filter(c => counts[c] || category === c)" :key="kind" type="button" class="catalog-chip" :aria-pressed="category === kind" @click="category = category === kind ? '' : kind">{{ categoryLabel(kind) }} <span>{{ counts[kind] }}</span></button></div>
    <div class="catalog-layout mt-6" :class="{ 'with-filters': showFilters }">
      <aside v-if="showFilters" id="catalog-filters" class="catalog-filters" :aria-label="t('catalog.filters')">
        <label><span>{{ t('catalog.provider') }}</span><select v-model="provider" class="input"><option value="">{{ t('catalog.all') }} ({{ providers.length }})</option><option v-for="p in providers" :key="p" :value="p">{{ p }}</option></select></label>
        <label><span>{{ t('catalog.unit') }}</span><select v-model="unit" class="input"><option value="">{{ t('catalog.all') }}</option><option v-for="u in units" :key="u" :value="u">{{ unitLabel(u) }}</option></select></label>
        <label v-if="tags.length"><span>{{ t('catalog.tags') }}</span><select v-model="tag" class="input"><option value="">{{ t('catalog.all') }}</option><option v-for="v in tags" :key="v">{{ v }}</option></select></label>
        <label v-if="endpoints.length"><span>{{ t('catalog.endpoint') }}</span><select v-model="endpoint" class="input"><option value="">{{ t('catalog.all') }}</option><option v-for="v in endpoints" :key="v">{{ v }}</option></select></label>
        <button v-if="activeFilters" type="button" class="btn-ghost text-xs" @click="resetFilters">{{ t('catalog.reset') }}</button><p class="mt-2 text-xs leading-5 text-muted">{{ t('catalog.publicNote') }}</p>
      </aside>
      <div class="min-w-0"><div class="mb-3 flex items-center justify-between text-xs text-muted" aria-live="polite"><span>{{ filtered.length }} {{ t('surface.statShown') }}</span><span>USD</span></div>
        <div v-if="error" class="inline-alert mb-4" role="alert">{{ error }} <button type="button" class="underline ml-2" @click="load">{{ t('catalog.retry') }}</button></div>
        <div v-if="loading" class="catalog-grid"><div v-for="i in 6" :key="i" class="model-card p-5"><div class="skeleton h-5 w-2/3"></div><div class="skeleton h-24 mt-5"></div></div></div>
        <div v-else-if="!filtered.length" class="empty-state"><p>{{ t('catalog.empty') }}</p><button v-if="activeFilters" class="btn-ghost mt-4" @click="resetFilters">{{ t('catalog.reset') }}</button></div>
        <div v-else-if="view !== 'list'" class="catalog-grid"><article v-for="model in filtered" :key="model.model" class="catalog-card" @click="selectModel(model)">
          <div class="flex gap-3 items-start"><span class="catalog-mark" aria-hidden="true">{{ model.provider.slice(0, 2).toUpperCase() }}</span><div class="min-w-0"><button type="button" class="catalog-name" @click.stop="selectModel(model)">{{ model.model }}</button><p class="mt-1 text-xs text-muted">{{ model.vendor || model.provider }}<span v-if="model.vendor && model.vendor !== model.provider"> · {{ model.provider }}</span></p></div></div>
          <div class="mt-4 flex flex-wrap gap-1.5"><span class="badge-muted">{{ categoryLabel(catalogCategory(model)) }}</span><span class="badge-muted">{{ t('catalog.' + (catalogPricing(model)?.status || 'unknown')) }}</span></div><ModelPriceSummary :model="model" class="mt-5 flex-1" />
          <div class="catalog-card-footer"><button type="button" class="text-sm text-accent" @click.stop="selectModel(model)">{{ t('catalog.detail') }} <span aria-hidden="true">→</span></button><button type="button" class="btn-ghost !min-h-0 !px-2 !py-1 text-xs" @click.stop="copy(model.model, model.model)">{{ copied === model.model ? t('common.copied') : t('catalog.copyModel') }}</button></div>
        </article></div>
        <div v-else class="data-table-wrap"><table class="table w-full text-sm"><thead><tr><th>{{ t('models.model') }}</th><th>{{ t('catalog.provider') }}</th><th>{{ t('catalog.type') }}</th><th class="min-w-[250px]">{{ t('catalog.price') }}</th></tr></thead><tbody><tr v-for="model in filtered" :key="model.model"><td class="max-w-[320px]"><button type="button" class="table-link break-all text-left font-mono" @click="selectModel(model)">{{ model.model }}</button></td><td>{{ model.provider }}</td><td>{{ categoryLabel(catalogCategory(model)) }}</td><td><ModelPriceSummary :model="model" /></td></tr></tbody></table></div>
      </div>
    </div>
    <DetailDrawer :open="!!selected" :title="selected?.model || t('models.model')" size="lg" @update:open="open => { if (!open) selectedID = '' }">
      <template v-if="selected"><div class="flex flex-wrap gap-2"><span class="badge-muted">{{ selected.provider }}</span><span class="badge-muted">{{ categoryLabel(catalogCategory(selected)) }}</span><span class="badge-muted">{{ t('catalog.' + (selectedPricing?.status || 'unknown')) }}</span></div>
        <div class="catalog-tabs mt-5" role="tablist" :aria-label="t('catalog.detail')"><button v-for="tab in ['overview', 'prices', 'api']" :id="'catalog-tab-' + tab" :key="tab" role="tab" :aria-selected="detailTab === tab" aria-controls="catalog-detail-panel" :tabindex="detailTab === tab ? 0 : -1" @keydown="moveDetailTab" @click="detailTab = tab">{{ t('catalog.' + tab) }}</button></div>
        <section id="catalog-detail-panel" class="py-5" role="tabpanel" :aria-labelledby="'catalog-tab-' + detailTab">
          <template v-if="detailTab === 'overview'"><dl class="grid grid-cols-1 sm:grid-cols-2 gap-5 text-sm"><div><dt class="text-muted mb-1">{{ t('models.model') }}</dt><dd class="font-mono break-all">{{ selected.model }}</dd></div><div><dt class="text-muted mb-1">{{ t('catalog.provider') }}</dt><dd>{{ selected.provider }}</dd></div><div v-if="selected.context_length"><dt class="text-muted mb-1">{{ t('catalog.context') }}</dt><dd>{{ selected.context_length.toLocaleString() }}</dd></div><div><dt class="text-muted mb-1">{{ t('catalog.unit') }}</dt><dd>{{ unitLabel(selectedPricing?.unit || 'unknown') }}</dd></div></dl><p v-if="selected.description" class="mt-6 text-sm leading-6">{{ selected.description }}</p><div v-if="catalogTags(selected).length" class="flex flex-wrap gap-2 mt-5"><span v-for="item in catalogTags(selected)" :key="item" class="badge-muted">{{ item }}</span></div><div class="mt-6 rounded-xl border border-line p-5"><h2 class="font-semibold mb-4">{{ t('catalog.price') }}</h2><ModelPriceSummary :model="selected" /></div></template>
          <template v-else-if="detailTab === 'prices'"><ModelPriceSummary :model="selected" detailed /><p v-if="selectedPricing?.billing_kind === 'video'" class="mt-5 text-sm leading-6 text-muted">{{ t('catalog.videoNote') }}</p><p class="mt-4 text-xs leading-5 text-muted">{{ t('catalog.publicNote') }}</p></template>
          <template v-else><ul class="space-y-2"><li v-for="path in selectedPricing?.endpoints || []" :key="path" class="font-mono text-xs rounded border border-line p-3 break-all">{{ path }}</li></ul><div class="flex items-center gap-2 mt-5"><button v-for="lang in (['curl', 'python', 'js'] as const)" :key="lang" type="button" class="catalog-chip" :aria-pressed="language === lang" @click="language = lang">{{ lang }}</button><button v-if="snippet" class="btn-ghost !min-h-0 text-xs ml-auto" @click="copy(snippet, 'snippet')">{{ copied === 'snippet' ? t('common.copied') : t('common.copy') }}</button></div><pre v-if="snippet" class="mt-3 overflow-x-auto rounded-lg border border-line bg-panel p-4 font-mono text-xs leading-6">{{ snippet }}</pre><p class="mt-4 text-xs leading-6 text-muted">{{ t(snippet ? 'catalog.snippetNote' : 'catalog.noSnippet') }}</p></template>
        </section>
      </template>
      <template #footer><div v-if="selected" class="flex flex-wrap gap-3 justify-end"><button class="btn-ghost" @click="copy(selected.model, selected.model)">{{ copied === selected.model ? t('common.copied') : t('catalog.copyModel') }}</button><RouterLink class="btn-primary" :to="modelDocs(selected)">{{ t('catalog.docs') }}</RouterLink></div></template>
    </DetailDrawer>
  </div>
</template>
<style scoped>
.catalog-header{display:flex;justify-content:space-between;gap:2rem;align-items:center}.catalog-count{display:flex;flex-direction:column;text-align:right;gap:.2rem;flex-shrink:0;color:rgb(var(--c-muted));font-size:.75rem}.catalog-count strong{font-size:2.5rem;line-height:1.1;color:rgb(var(--c-ink));font-variant-numeric:tabular-nums}
.catalog-layout{display:grid;grid-template-columns:minmax(0,1fr);gap:1.5rem}.catalog-layout.with-filters{grid-template-columns:220px minmax(0,1fr)}.catalog-filters{display:flex;flex-direction:column;gap:1.2rem;align-self:start;padding:1rem;border:1px solid rgb(var(--c-line));border-radius:.75rem;background:rgb(var(--c-panel))}.catalog-filters label{display:flex;flex-direction:column;gap:.5rem;font-size:.75rem}.catalog-filters select{width:100%;font-size:.75rem}
.catalog-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,290px),1fr));gap:1rem}.catalog-card{display:flex;flex-direction:column;min-width:0;border:1px solid rgb(var(--c-line));border-radius:.8rem;background:rgb(var(--c-panel));padding:1.25rem;cursor:pointer;transition:border-color .15s}.catalog-card:hover,.catalog-card:focus-within{border-color:rgb(var(--c-accent))}.catalog-name{text-align:left;font-family:var(--font-mono,monospace);font-size:.875rem;font-weight:600;overflow-wrap:anywhere}.catalog-mark{display:grid;place-items:center;width:2.5rem;height:2.5rem;flex-shrink:0;border-radius:.6rem;background:rgb(var(--c-accent)/.1);color:rgb(var(--c-accent));font-weight:600;font-size:.75rem}
.catalog-card-footer{display:flex;justify-content:space-between;flex-wrap:wrap;gap:.5rem;align-items:center;border-top:1px solid rgb(var(--c-line));margin-top:1.25rem;padding-top:.8rem}.catalog-chip{border:1px solid rgb(var(--c-line));border-radius:.5rem;padding:.4rem .7rem;font-size:.75rem}.catalog-chip span{opacity:.6;margin-left:.3rem}.catalog-chip[aria-pressed=true],.catalog-view button[aria-pressed=true]{color:rgb(var(--c-accent));background:rgb(var(--c-accent)/.08);border-color:rgb(var(--c-accent)/.4)}.catalog-view{display:flex;border:1px solid rgb(var(--c-line));border-radius:.5rem;overflow:hidden}.catalog-view button{padding:.55rem .8rem;font-size:.75rem}.catalog-tabs{display:flex;gap:1rem;border-bottom:1px solid rgb(var(--c-line))}.catalog-tabs button{padding:.7rem .25rem;border-bottom:2px solid transparent;font-size:.875rem}.catalog-tabs button[aria-selected=true]{border-color:rgb(var(--c-accent));color:rgb(var(--c-accent))}
@media(max-width:900px){.catalog-layout.with-filters{grid-template-columns:minmax(0,1fr)}.catalog-filters{display:grid;grid-template-columns:repeat(2,minmax(0,1fr))}.catalog-filters p{grid-column:1/-1}}
@media(max-width:480px){.catalog-count{display:none}.catalog-filters{grid-template-columns:minmax(0,1fr)}.catalog-card{padding:1rem}}
@media(prefers-reduced-motion:reduce){.catalog-card{transition:none}}
</style>
