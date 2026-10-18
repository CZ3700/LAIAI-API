<script setup lang="ts">
import { computed, onBeforeUnmount, onDeactivated, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminApi, errorMessage } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import { genericDraft, genericPriceFields, type GenericPrice, type PriceField, type PricingModel } from './types'

const props = defineProps<{ row: PricingModel; contextKey: string }>()
const emit = defineEmits<{ saved: [rowKey: string, contextKey: string] }>()
const { t } = useI18n()
const auth = useAuth()
const draft = ref<GenericPrice>(genericDraft(props.row))
const busy = ref(false), error = ref(''), notice = ref('')
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
const canSave = computed(() => !busy.value && genericPriceFields.some(field => draft.value[field].trim() !== ''))
function reset() { ++generation; draft.value = genericDraft(props.row); busy.value = false; error.value = ''; notice.value = '' }
watch(() => [props.row, props.contextKey, auth.orgId, auth.token, auth.role], reset, { flush: 'sync' })
async function save() {
  if (!canSave.value) return
  const current = generation
  const rowKey = props.row.key, contextKey = props.contextKey
  const snapshot = { ...draft.value, model: props.row.model }
  // Required legacy API columns have no effect on image/other dedicated rates.
  // Existing values always survive, including columns hidden by this form.
  snapshot.input_usd_per_1m ||= '0'
  snapshot.output_usd_per_1m ||= '0'
  busy.value = true; error.value = ''; notice.value = ''
  try {
    await adminApi('POST', '/api/admin/model-prices', snapshot)
    if (current !== generation) return
    notice.value = t('pricingx.saved'); emit('saved', rowKey, contextKey)
  } catch (e) { if (current === generation) error.value = errorMessage(e, t('pricingx.saveFailed')) }
  finally { if (current === generation) busy.value = false }
}
onDeactivated(reset)
onBeforeUnmount(() => { ++generation })
</script>

<template>
  <form class="generic-pricing" @submit.prevent="save">
    <p>{{ t('pricingx.globalScope') }}</p>
    <p v-if="row.pricing_kind === 'unknown'" class="classification-note">{{ t('pricingx.unknownNote') }}</p>
    <p v-if="row.pricing_kind === 'generic_video'" class="classification-note">{{ t('pricingx.taskUnits') }}</p>
    <div class="price-fields">
      <label v-for="field in primaryFields" :key="field">{{ t(labels[field]) }}
        <input v-model="draft[field]" :name="field" inputmode="decimal" autocomplete="off" :disabled="busy" :required="field !== 'cached_input_usd_per_1m' && (row.pricing_kind === 'token' || row.pricing_kind === 'image' || row.pricing_kind === 'per_call')" placeholder="—">
      </label>
    </div>
    <details v-if="otherFields.length"><summary>{{ t('pricingx.advanced') }}</summary><div class="price-fields">
      <label v-for="field in otherFields" :key="field">{{ t(labels[field]) }}<input v-model="draft[field]" :name="field" inputmode="decimal" autocomplete="off" :disabled="busy" placeholder="—"></label>
    </div></details>
    <p class="help">{{ t('pricingx.preserve') }}</p>
    <p v-if="error" role="alert" class="text-danger">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
    <button class="btn-primary" :disabled="!canSave">{{ t('pricingx.save') }}</button>
  </form>
</template>

<style scoped>
.generic-pricing{display:grid;gap:18px}.generic-pricing p{margin:0;line-height:1.65;color:rgb(var(--c-muted))}.price-fields{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:16px}.price-fields label{display:grid;gap:7px;font-size:13px}.price-fields input{border:1px solid rgb(var(--c-line));border-radius:8px;background:rgb(var(--c-bg));padding:10px;color:rgb(var(--c-ink));width:100%;min-width:0}.help{font-size:12px}.classification-note{border-left:3px solid rgb(var(--c-accent));padding:10px 14px;background:rgb(var(--c-elev))}summary{cursor:pointer;padding:8px 0 16px}.btn-primary{justify-self:start}
</style>
