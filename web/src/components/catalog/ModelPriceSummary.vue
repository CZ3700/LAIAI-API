<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { catalogPricing, standardVideoRows, type PricedModel } from '@/lib/modelCatalog'
const props = defineProps<{ model: PricedModel; detailed?: boolean }>()
const { t } = useI18n()
const pricing = computed(() => catalogPricing(props.model))
const rows = computed(() => standardVideoRows(props.model))
const rateLabels: Record<string, string> = { input_usd_per_1m: 'input', output_usd_per_1m: 'output', cached_input_usd_per_1m: 'cached', image_usd: 'image', per_call_usd: 'perCall', tts_usd_per_1k_chars: 'tts', transcription_usd_per_call: 'transcription', audio_input_usd_per_1m: 'audioInput', audio_output_usd_per_1m: 'audioOutput' }
const units: Record<string, string> = { 'USD/1M_tokens': 'unitToken', 'USD/image': 'unitImage', 'USD/call': 'unitCall', 'USD/1K_characters': 'unitChars', 'USD/1M_audio_tokens': 'unitAudio', 'USD/1M_video_tokens': 'unitVideo' }
const others = computed(() => pricing.value?.combinations.filter(c => c.mode !== 'generate' || c.service_tier !== 'default' || c.draft) || [])
</script>
<template>
  <div class="price-summary">
    <p v-if="!pricing || (!pricing.rates.length && !pricing.combinations.length)" class="text-sm text-muted py-3">{{ t('catalog.unknown') }}</p>
    <template v-else-if="pricing.billing_kind === 'video'">
      <p class="text-xs text-muted mb-3">{{ t('catalog.unitHint') }}</p>
      <table v-if="rows.length" class="w-full text-xs">
        <thead><tr class="text-muted"><th class="text-left font-normal pb-2">{{ t('catalog.resolution') }}</th><th class="text-right font-normal pb-2">{{ t('catalog.noReference') }}</th><th class="text-right font-normal pb-2">{{ t('catalog.withReference') }}</th></tr></thead>
        <tbody><tr v-for="row in rows" :key="row.resolution" class="border-t border-line"><th class="py-2.5 text-left font-medium">{{ row.resolution }}</th><td class="py-2.5 text-right font-mono">{{ row.without === undefined ? '—' : '$' + row.without }}</td><td class="py-2.5 text-right font-mono">{{ row.with === undefined ? '—' : '$' + row.with }}</td></tr></tbody>
      </table>
      <p v-if="rows.some(row => row.with === undefined || row.without === undefined)" class="mt-2 text-xs text-muted">— {{ t('catalog.unavailable') }}</p>
      <div v-if="detailed && others.length" class="mt-4 space-y-2">
        <dl v-for="cell in others" :key="cell.key" class="rounded-lg border border-line p-3 text-xs grid grid-cols-2 gap-2">
          <dt>{{ t('catalog.mode') }}</dt><dd>{{ cell.mode }}</dd>
          <dt>{{ t('catalog.resolution') }}</dt><dd>{{ cell.resolution }}</dd>
          <dt>{{ t('catalog.withReference') }}</dt><dd>{{ t(cell.input_video ? 'catalog.yes' : 'catalog.no') }}</dd>
          <dt>{{ t('catalog.tier') }}</dt><dd>{{ cell.service_tier }}</dd>
          <dt>{{ t('catalog.draft') }}</dt><dd>{{ t(cell.draft ? 'catalog.yes' : 'catalog.no') }}</dd>
          <dt>{{ t('catalog.price') }}</dt><dd class="font-mono">${{ cell.amount }}</dd>
        </dl>
      </div>
      <p v-else-if="others.length" class="text-xs text-muted mt-3">{{ t('catalog.more') }}</p>
    </template>
    <dl v-else class="space-y-3">
      <div v-for="rate in pricing.rates" :key="rate.key" class="flex justify-between gap-3 text-sm">
        <dt class="min-w-0 text-muted">{{ rateLabels[rate.key] ? t('catalog.' + rateLabels[rate.key]) : rate.key }}<span class="block text-[11px]">{{ units[rate.unit] ? t('catalog.' + units[rate.unit]) : rate.unit }}</span></dt>
        <dd class="font-mono font-semibold shrink-0">${{ rate.amount }}</dd>
      </div>
    </dl>
  </div>
</template>
