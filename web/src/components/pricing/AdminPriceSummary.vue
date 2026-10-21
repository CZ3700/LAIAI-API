<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { displayRates, evidenceOnlyVariant, officialPriceEvidence, unitKeys, videoPriceRange } from './catalogPresentation'
import type { PricingModel } from './types'
const props = defineProps<{ row: PricingModel; expanded?: boolean }>()
const { t } = useI18n()
const rates = computed(() => displayRates(props.row))
const shown = computed(() => props.expanded ? rates.value : rates.value.slice(0, 3))
const range = computed(() => videoPriceRange(props.row))
const isVideo = computed(() => props.row.pricing?.billing_kind === 'video' || !!props.row.video_model)
const evidence = computed(() => officialPriceEvidence(props.row))
const referenceOnly = computed(() => evidenceOnlyVariant(props.row))
function unit(value: string) { return t(`pricingAdmin.${unitKeys[value] ?? 'unitUnknown'}`) }
</script>
<template>
  <div class="price-summary">
    <template v-if="evidence && referenceOnly"><strong>{{ t('pricingAdmin.officialVerified') }}</strong><small>{{ t('pricingAdmin.officialCNYUnit') }} · {{ t('pricingAdmin.referenceCombinations', { count: evidence.prices.length }) }}</small><small>{{ t(row.unsupported_reason === 'official_video_configuration_missing' ? 'pricingAdmin.configurationRequired' : 'pricingAdmin.blocked') }}</small></template>
    <template v-else-if="isVideo">
      <template v-if="range"><strong class="video-range">{{ range.min }}<template v-if="range.max !== range.min"> – {{ range.max }}</template></strong><small>{{ t('pricingAdmin.unitVideo') }}</small><small>{{ t('pricingAdmin.combinations', { count: row.pricing?.combinations.length ?? 0 }) }}</small><p v-if="expanded">{{ t('pricingAdmin.rangeConditions') }}</p></template>
      <template v-else-if="row.pricing?.combinations.length"><small>{{ t('pricingAdmin.combinations', { count: row.pricing.combinations.length }) }}</small><small>{{ t('pricingAdmin.unitVideo') }}</small></template>
      <span v-else class="no-price">{{ t('pricingAdmin.noPrice') }}</span>
    </template>
    <template v-else-if="shown.length"><dl><div v-for="rate in shown" :key="rate.key"><dt>{{ t(`pricingAdmin.${rate.key}`) }}</dt><dd><strong>{{ rate.amount }}</strong><small>{{ unit(rate.unit) }}</small></dd></div></dl><small v-if="!expanded && rates.length > shown.length">{{ t('pricingAdmin.moreRates', { count: rates.length - shown.length }) }}</small></template>
    <span v-else class="no-price">{{ t('pricingAdmin.noPrice') }}</span>
  </div>
</template>
<style scoped>
.price-summary{display:grid;gap:6px;min-width:0}.price-summary dl{display:grid;gap:10px;margin:0}.price-summary dl>div{display:flex;align-items:baseline;justify-content:space-between;gap:12px}.price-summary dt{font-size:12px;color:rgb(var(--c-muted))}.price-summary dd{margin:0;display:flex;align-items:baseline;gap:7px;flex-wrap:wrap;justify-content:flex-end}.price-summary strong{font-size:15px;font-variant-numeric:tabular-nums;color:rgb(var(--c-ink));font-weight:600}.price-summary .video-range{font-size:23px;letter-spacing:-.025em}.price-summary small,.price-summary p,.no-price{font-size:12px;color:rgb(var(--c-muted));line-height:1.6}.price-summary p{margin:0}
</style>
