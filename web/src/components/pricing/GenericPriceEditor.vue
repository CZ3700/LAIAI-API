<script setup lang="ts">
import { computed, onBeforeUnmount, onDeactivated, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminApi, errorMessage } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import { genericDraft, genericPriceFields, type GenericPrice, type PriceField, type PricingModel } from './types'
import { fieldUnits, unitKeys } from './catalogPresentation'

const props = defineProps<{ row: PricingModel; contextKey: string }>()
const emit = defineEmits<{ saved: [rowKey: string, contextKey: string]; 'dirty-change': [value: boolean]; 'busy-change': [value: boolean] }>()
const { t } = useI18n()
const auth = useAuth()
const draft = ref<GenericPrice>(genericDraft(props.row))
const busy = ref(false), error = ref(''), notice = ref('')
const baseline = ref<GenericPrice>(genericDraft(props.row))
const preview = ref<GenericPrice | null>(null)
let generation = 0
const labels: Record<PriceField, string> = { input_usd_per_1m: 'admin.prInput', output_usd_per_1m: 'admin.prOutput', cached_input_usd_per_1m: 'admin.prCached', image_usd: 'admin.prImage', per_call_usd: 'admin.prPerCall', tts_usd_per_1k_chars: 'admin.prTTS', transcription_usd_per_call: 'admin.prTranscription', audio_input_usd_per_1m: 'admin.prAudioInput', audio_output_usd_per_1m: 'admin.prAudioOutput' }
const primaryFields = computed<readonly PriceField[]>(() => {
  switch (props.row.pricing_kind) {
    case 'token': return ['input_usd_per_1m', 'cached_input_usd_per_1m', 'output_usd_per_1m']
    case 'image': return ['image_usd']
    case 'per_call': return ['per_call_usd']
    case 'generic_video': return props.row.billing_modes.includes('per_call') ? ['per_call_usd'] : ['output_usd_per_1m']
    case 'audio': return ['tts_usd_per_1k_chars', 'transcription_usd_per_call', 'audio_input_usd_per_1m', 'audio_output_usd_per_1m']
    default: return genericPriceFields
  }
})
const otherFields = computed(() => genericPriceFields.filter(field => !primaryFields.value.includes(field)))
const changes = computed(() => genericPriceFields.filter(field => draft.value[field] !== baseline.value[field]))
const dirty = computed(() => changes.value.length > 0)
const invalidFields = computed(() => genericPriceFields.filter(field => draft.value[field] !== '' && !/^(0|[1-9]\d*)(\.\d{1,8})?$/.test(draft.value[field])))
const canSave = computed(() => !busy.value && dirty.value && !invalidFields.value.length && primaryFields.value.some(field => draft.value[field] !== ''))
function reset() { ++generation; draft.value = genericDraft(props.row); baseline.value = genericDraft(props.row); preview.value = null; busy.value = false; error.value = ''; notice.value = '' }
watch(() => [props.row, props.contextKey, auth.orgId, auth.token, auth.role], reset, { flush: 'sync' })
watch(draft, () => { preview.value = null; notice.value = '' }, { deep: true, flush: 'sync' })
watch(dirty, value => emit('dirty-change', value), { flush: 'sync' })
watch(busy, value => emit('busy-change', value), { flush: 'sync' })
function preparePreview() { if (canSave.value) { preview.value = { ...draft.value }; error.value = '' } }
function unit(field: PriceField) { return t(`pricingAdmin.${unitKeys[fieldUnits[field] ?? 'unknown'] ?? 'unitUnknown'}`) }
async function save() {
  if (!canSave.value || !preview.value || JSON.stringify(preview.value) !== JSON.stringify(draft.value)) return
  const current = generation
  const rowKey = props.row.key, contextKey = props.contextKey
  const snapshot = { ...preview.value, model: props.row.model }
  // Required legacy API columns have no effect on image/other dedicated rates.
  // Existing values always survive, including columns hidden by this form.
  snapshot.input_usd_per_1m ||= '0'
  snapshot.output_usd_per_1m ||= '0'
  busy.value = true; error.value = ''; notice.value = ''
  try {
    await adminApi('POST', '/api/admin/model-prices', snapshot)
    if (current !== generation) return
    baseline.value = { ...draft.value }; preview.value = null
    notice.value = t('pricingx.saved'); emit('saved', rowKey, contextKey)
  } catch (e) { if (current === generation) error.value = errorMessage(e, t('pricingx.saveFailed')) }
  finally { if (current === generation) busy.value = false }
}
onDeactivated(reset)
onBeforeUnmount(() => { ++generation })
defineExpose({ hasUnsavedChanges: () => dirty.value, discardChanges: reset })
</script>

<template>
  <form class="generic-pricing" @submit.prevent="preview ? save() : preparePreview()">
    <p v-if="row.pricing_kind === 'unknown'" class="classification-note">{{ t('pricingx.unknownNote') }}</p>
    <p v-if="row.pricing_kind === 'generic_video'" class="classification-note">{{ t('pricingx.taskUnits') }}</p>
    <template v-if="!preview"><div class="price-fields">
      <label v-for="field in primaryFields" :key="field">{{ t(labels[field]) }}
        <input v-model="draft[field]" :name="field" inputmode="decimal" autocomplete="off" :disabled="busy" :aria-invalid="invalidFields.includes(field)" :required="field !== 'cached_input_usd_per_1m' && (row.pricing_kind === 'token' || row.pricing_kind === 'image' || row.pricing_kind === 'per_call')" placeholder="—"><small>{{ unit(field) }}</small>
      </label>
    </div>
    <details v-if="otherFields.length"><summary>{{ t('pricingx.advanced') }}</summary><div class="price-fields">
      <label v-for="field in otherFields" :key="field">{{ t(labels[field]) }}<input v-model="draft[field]" :name="field" inputmode="decimal" autocomplete="off" :disabled="busy" :aria-invalid="invalidFields.includes(field)" placeholder="—"><small>{{ unit(field) }}</small></label>
    </div></details>
    <p class="help">{{ t('pricingx.preserve') }}</p><p v-if="invalidFields.length" role="alert" class="validation-error">{{ t('pricingAdmin.validPrice') }}</p></template>
    <section v-else class="preview-section"><p>{{ t('pricingAdmin.previewNote') }}</p><div class="preview-table"><table><thead><tr><th>{{ t('pricingAdmin.field') }}</th><th>{{ t('pricingAdmin.before') }}</th><th>{{ t('pricingAdmin.after') }}</th></tr></thead><tbody><tr v-for="field in changes" :key="field"><th>{{ t(labels[field]) }}<small>{{ unit(field) }}</small></th><td>{{ baseline[field] || t('pricingAdmin.unpriced') }}</td><td>{{ preview[field] || t('pricingAdmin.unpriced') }}</td></tr></tbody></table></div><p class="help">{{ t('pricingAdmin.unchanged', { count: genericPriceFields.length - changes.length }) }}</p></section>
    <p v-if="error" role="alert" class="text-danger">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
    <footer class="editor-footer"><span>{{ t(dirty ? 'pricingAdmin.dirty' : 'pricingAdmin.noChanges') }}</span><div><button v-if="preview" type="button" class="btn-ghost" :disabled="busy" @click="preview = null">{{ t('pricingAdmin.backToEditing') }}</button><button class="btn-primary" :disabled="!canSave">{{ busy ? t('common.loading') : t(preview ? 'pricingAdmin.publish' : 'pricingAdmin.preview') }}</button></div></footer>
  </form>
</template>

<style scoped>
.generic-pricing{display:grid;gap:18px}.generic-pricing p{margin:0;line-height:1.65;color:rgb(var(--c-muted));font-size:13px}.price-fields{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:16px}.price-fields label{display:grid;gap:7px;font-size:13px}.price-fields input{border:1px solid rgb(var(--c-line));border-radius:8px;background:rgb(var(--c-bg));padding:10px;color:rgb(var(--c-ink));width:100%;min-width:0}.price-fields input[aria-invalid=true]{border-color:rgb(var(--c-danger))}.price-fields small,.preview-table small{font-size:11px;color:rgb(var(--c-muted));display:block}.help{font-size:12px}.classification-note{border-left:3px solid rgb(var(--c-accent));padding:10px 14px;background:rgb(var(--c-elev))}summary{cursor:pointer;padding:8px 0 16px}.btn-primary{justify-self:start}.editor-footer{position:sticky;bottom:-20px;display:flex;align-items:center;justify-content:space-between;gap:14px;margin:8px -20px -20px;padding:16px 20px;background:rgb(var(--c-panel));border-top:1px solid rgb(var(--c-line));z-index:1}.editor-footer>span{font-size:12px;color:rgb(var(--c-muted))}.editor-footer>div{display:flex;gap:10px;flex-shrink:0}.preview-section{display:grid;gap:16px}.preview-table{overflow-x:auto;border:1px solid rgb(var(--c-line));border-radius:9px}.preview-table table{width:100%;border-collapse:collapse;text-align:start;font-size:13px}.preview-table th,.preview-table td{padding:13px;border-bottom:1px solid rgb(var(--c-line));font-weight:400}.preview-table thead{background:rgb(var(--c-elev));font-size:12px;color:rgb(var(--c-muted))}.preview-table tbody td{font-variant-numeric:tabular-nums}.generic-pricing .validation-error{color:rgb(var(--c-danger))}@media(max-width:600px){.editor-footer{bottom:-16px;margin-inline:-16px;margin-bottom:-16px;padding:14px 16px;align-items:stretch;flex-direction:column}.editor-footer>div{justify-content:flex-end}.price-fields{grid-template-columns:1fr}}
</style>
