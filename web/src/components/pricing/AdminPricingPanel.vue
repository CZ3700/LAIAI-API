<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminApi, errorMessage } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import DetailDrawer from '@/components/DetailDrawer.vue'
import VideoPricing from '@/pages/VideoPricing.vue'
import GenericPriceEditor from './GenericPriceEditor.vue'
import type { PricingCatalog, PricingModel } from './types'

const props = defineProps<{ refreshKey?: number }>()
const emit = defineEmits<{ 'edit-metadata': [row: { model: string; meta?: PricingModel['meta'] }] }>()
const { t } = useI18n()
const auth = useAuth()
const catalog = ref<PricingCatalog | null>(null), loading = ref(false), error = ref(''), notice = ref('')
const provider = ref(''), channel = ref(''), modelKey = ref(''), query = ref(''), detailsOpen = ref(false)
let generation = 0
const channels = computed(() => catalog.value?.channels.filter(c => c.provider === provider.value) ?? [])
const rows = computed(() => {
  const search = query.value.trim().toLocaleLowerCase()
  return catalog.value?.models.filter(m => m.provider === provider.value && (!channel.value || m.channel_id === Number(channel.value)) && (!search || `${m.model} ${m.upstream_model}`.toLocaleLowerCase().includes(search))) ?? []
})
const selected = computed(() => rows.value.find(row => row.key === modelKey.value) ?? null)
const videoAllowed = computed(() => auth.isSuperadmin && !!catalog.value?.permissions.video_details)
const reasonKeys: Record<string, string> = { pricing_kind_unknown: 'reasonUnknown', official_video_configuration_missing: 'reasonMissing', official_video_channel_mismatch: 'reasonMismatch', official_video_configuration_ambiguous: 'reasonAmbiguous', seedance_variant_not_supported: 'reasonVariant', seedance_1x_excluded: 'reasonExcluded' }
function reason(code: string) { return t(`pricingx.${reasonKeys[code] ?? 'unsupported'}`) }
const contextKey = computed(() => `${generation}:${provider.value}:${channel.value}:${modelKey.value}`)
function channelLabel(id: number) { const ch = catalog.value?.channels.find(c => c.id === id); return ch ? `${ch.name} · ${t(ch.status === 1 ? 'pricingx.active' : 'pricingx.disabled')}` : `#${id}` }
function resetSelection() { provider.value = ''; channel.value = ''; modelKey.value = ''; query.value = ''; detailsOpen.value = false }
watch(provider, () => { channel.value = ''; modelKey.value = ''; query.value = ''; detailsOpen.value = false }, { flush: 'sync' })
watch(channel, () => { modelKey.value = ''; detailsOpen.value = false }, { flush: 'sync' })
watch(selected, () => { detailsOpen.value = false }, { flush: 'sync' })
async function load(keepSelection = false) {
  const current = ++generation
  const previous = keepSelection ? { provider: provider.value, channel: channel.value, model: modelKey.value } : null
  resetSelection(); catalog.value = null; error.value = ''; loading.value = true
  try {
    const data = await adminApi<PricingCatalog>('GET', '/api/admin/pricing-catalog')
    if (current !== generation) return
    catalog.value = data
    if (previous && data.models.some(m => m.key === previous.model)) { provider.value = previous.provider; channel.value = previous.channel; modelKey.value = previous.model }
  } catch (e) { if (current === generation) error.value = errorMessage(e, t('pricingx.loadFailed')) }
  finally { if (current === generation) loading.value = false }
}
async function saved(rowKey: string, sourceContext: string) {
  if (rowKey !== modelKey.value || sourceContext !== contextKey.value) return
  const expected = generation + 1
  await load(true)
  if (expected === generation && !error.value) notice.value = t('pricingx.saved')
}
watch(() => [auth.orgId, auth.token, auth.role, props.refreshKey], () => { ++generation; notice.value = ''; void load() }, { flush: 'sync' })
onMounted(() => load())
onActivated(() => { if (!catalog.value && !loading.value) void load() })
onDeactivated(() => { ++generation; resetSelection(); catalog.value = null; loading.value = false; notice.value = '' })
onBeforeUnmount(() => { ++generation; resetSelection() })
</script>

<template>
  <section class="provider-pricing">
    <header><div><h2>{{ t('pricingx.title') }}</h2><p>{{ t('pricingx.intro') }}</p></div><button class="btn-ghost" :disabled="loading" @click="load()">{{ t('pricingx.reload') }}</button></header>
    <p v-if="error" role="alert" class="text-danger">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="loading" aria-live="polite">{{ t('common.loading') }}</p>
    <div class="selection-grid">
      <label>{{ t('pricingx.provider') }}<select v-model="provider" :disabled="loading"><option value="">{{ t('pricingx.chooseProvider') }}</option><option v-for="p in catalog?.providers" :key="p.name" :value="p.name">{{ p.display_name }} ({{ p.name }})</option></select></label>
      <label>{{ t('pricingx.channel') }}<select v-model="channel" :disabled="!channels.length || loading"><option value="">{{ t('pricingx.allChannels') }}</option><option v-for="ch in channels" :key="ch.id" :value="String(ch.id)">{{ channelLabel(ch.id) }}</option></select></label>
      <label>{{ t('pricingx.search') }}<input v-model="query" type="search" :disabled="!channels.length || loading"></label>
      <label class="model-selection">{{ t('pricingx.model') }}<select v-model="modelKey" :disabled="!rows.length || loading"><option value="">{{ t('pricingx.chooseModel') }}</option><option v-for="row in rows" :key="row.key" :value="row.key">{{ row.model }} · {{ channelLabel(row.channel_id) }} · {{ t(`pricingx.${row.pricing_kind}`) }}</option></select></label>
    </div>
    <p v-if="provider && !loading && !channels.length" role="status">{{ t('pricingx.noChannels') }}</p>
    <p v-else-if="provider && !loading && !rows.length" role="status">{{ t('pricingx.noModels') }}</p>
    <template v-if="selected">
      <section class="model-summary"><div><small>{{ t('pricingx.publicModel') }}</small><strong>{{ selected.model }}</strong><small>{{ t('pricingx.upstreamModel') }} · {{ selected.upstream_model }}</small></div><span class="badge">{{ t(`pricingx.${selected.pricing_kind}`) }}</span><button class="btn-ghost" @click="detailsOpen = true">{{ t('pricingx.details') }}</button><button class="btn-ghost" @click="emit('edit-metadata', { model: selected.model, meta: selected.meta })">{{ t('pricingx.editMeta') }}</button></section>
      <template v-if="selected.pricing_kind === 'video' || selected.video_model">
        <p v-if="selected.video_model && selected.unsupported_reason" class="pricing-note">{{ reason(selected.unsupported_reason) }}</p>
        <p v-if="!videoAllowed" class="pricing-note">{{ t('pricingx.superadmin') }}</p>
        <VideoPricing v-else-if="selected.video_model && ['seedance-2.0', 'seedance-2.5'].includes(selected.video_model)" :key="selected.key" embedded :selected-model="selected.video_model" :selected-channel-id="selected.channel_id" :context-key="contextKey" />
        <p v-else class="pricing-note">{{ reason(selected.unsupported_reason) }}</p>
      </template>
      <p v-else-if="selected.unsupported_reason && selected.unsupported_reason !== 'pricing_kind_unknown'" class="pricing-note">{{ reason(selected.unsupported_reason) }}</p>
      <GenericPriceEditor v-else :key="selected.key" :row="selected" :context-key="contextKey" @saved="saved" />
    </template>
    <DetailDrawer v-model:open="detailsOpen" :title="t('pricingx.details')" size="md"><template v-if="selected">
      <dl class="pricing-detail"><dt>{{ t('pricingx.publicModel') }}</dt><dd>{{ selected.model }}</dd><dt>{{ t('pricingx.upstreamModel') }}</dt><dd>{{ selected.upstream_model }}</dd><dt>{{ t('pricingx.channel') }}</dt><dd>{{ channelLabel(selected.channel_id) }}</dd><dt>{{ t('pricingx.billingModes') }}</dt><dd>{{ t(`pricingx.${selected.pricing_kind}`) }} · {{ selected.billing_modes.join(', ') || '—' }}</dd></dl>
      <p v-if="selected.video_configuration">{{ channelLabel(selected.video_configuration.channel_id) }} · Scope #{{ selected.video_configuration.provider_scope_id }} · {{ selected.video_configuration.pricing_version }} · {{ selected.video_configuration.config_version }}</p><p v-else-if="selected.video_model">{{ t('pricingx.unbound') }}</p><p v-else-if="selected.pricing_kind !== 'video'">{{ t(selected.generic_price ? 'pricingx.currentPrice' : 'pricingx.missingPrice') }}</p><p>{{ t(selected.pricing_kind === 'video' || selected.video_model ? 'pricingx.videoScope' : 'pricingx.globalScope') }}</p><p>{{ t('pricingx.noAutomaticPrice') }}</p>
    </template></DetailDrawer>
  </section>
</template>

<style scoped>
.provider-pricing{padding:22px;border:1px solid rgb(var(--c-line));border-radius:14px;background:rgb(var(--c-panel));display:grid;gap:20px;min-width:0}.provider-pricing header{display:flex;justify-content:space-between;gap:20px;align-items:flex-start}.provider-pricing h2{font-size:20px;font-weight:600;margin:0 0 6px}.provider-pricing p{line-height:1.65;color:rgb(var(--c-muted));margin:0}.selection-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px}.selection-grid label{display:grid;gap:7px;font-size:13px}.selection-grid select,.selection-grid input{min-width:0;width:100%;color:rgb(var(--c-ink));background:rgb(var(--c-bg));border:1px solid rgb(var(--c-line));padding:10px;border-radius:8px}.model-selection{grid-column:1/-1}.model-summary{display:flex;align-items:center;flex-wrap:wrap;gap:12px;border-block:1px solid rgb(var(--c-line));padding:18px 0}.model-summary>div{flex:1;min-width:220px;overflow-wrap:anywhere}.model-summary small{display:block;color:rgb(var(--c-muted));font-size:12px}.model-summary strong{display:block;font-size:16px;margin:4px 0}.badge{font-size:12px;padding:5px 8px;border-radius:6px;background:rgb(var(--c-elev))}.pricing-note{background:rgb(var(--c-elev));padding:15px;border-radius:8px}.pricing-detail{display:grid;grid-template-columns:120px 1fr;gap:15px}.pricing-detail dt{color:rgb(var(--c-muted))}.pricing-detail dd{overflow-wrap:anywhere}@media(max-width:760px){.provider-pricing{padding:16px}.selection-grid{grid-template-columns:1fr}.provider-pricing header{flex-direction:column}.pricing-detail{grid-template-columns:1fr}}
</style>
