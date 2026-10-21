<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { OfficialPriceEvidence } from './types'
defineProps<{ evidence: OfficialPriceEvidence }>()
const { t } = useI18n()
function sourceURL(value: string) {
  try { const url = new URL(value); return url.protocol === 'https:' && !url.username && !url.password ? url.href : undefined } catch { return undefined }
}
</script>
<template>
  <section class="official-evidence">
    <h3>{{ t('pricingAdmin.officialVerified') }}</h3>
    <p>{{ t('pricingAdmin.variantEvidenceHelp') }}</p>
    <p class="evidence-meta">{{ evidence.model }} · {{ evidence.verified_at }}</p>
    <div class="evidence-table"><table><caption>{{ t('pricingAdmin.officialCNYUnit') }}</caption><thead><tr><th>{{ t('pricingAdmin.resolution') }}</th><th>{{ t('pricingAdmin.referenceVideo') }}</th><th>{{ t('pricingAdmin.officialReferencePrice') }}</th></tr></thead><tbody><tr v-for="price in evidence.prices" :key="`${price.resolution}/${price.input_video}`"><td>{{ price.resolution }}</td><td>{{ t(price.input_video ? 'pricingAdmin.withReferenceVideo' : 'pricingAdmin.withoutReferenceVideo') }}</td><td>{{ price.supplier_cny_per_1m }}</td></tr></tbody></table></div>
    <h4>{{ t('pricingAdmin.evidenceSources') }}</h4>
    <article v-for="source in evidence.sources" :key="source.id" class="evidence-source"><strong>{{ source.id }}</strong><a v-if="sourceURL(source.url)" :href="sourceURL(source.url)" target="_blank" rel="noopener noreferrer">{{ t('pricingAdmin.officialSource') }} ↗</a><span v-else>{{ source.url }}</span><code>SHA-256 · {{ source.sha256 }}</code></article>
  </section>
</template>
<style scoped>
.official-evidence{display:grid;gap:12px;border:1px solid rgb(var(--c-line));border-radius:10px;padding:18px;min-width:0}.official-evidence h3,.official-evidence h4,.official-evidence p{margin:0}.official-evidence h3{font-size:15px}.official-evidence h4{font-size:13px}.official-evidence p{color:rgb(var(--c-muted));font-size:12px;line-height:1.6}.evidence-meta{overflow-wrap:anywhere}.evidence-table{overflow-x:auto}table{border-collapse:collapse;width:100%;font-size:13px}caption{text-align:left;color:rgb(var(--c-muted));font-size:12px;padding:0 0 8px}th,td{padding:10px;text-align:left;border-bottom:1px solid rgb(var(--c-line));white-space:nowrap}th{color:rgb(var(--c-muted));font-size:12px}.evidence-source{display:grid;gap:8px;font-size:12px;padding-top:12px;border-top:1px solid rgb(var(--c-line));overflow-wrap:anywhere}.evidence-source a{color:rgb(var(--c-accent));width:fit-content}.evidence-source code{font-size:11px;color:rgb(var(--c-muted));white-space:normal}@media(max-width:600px){.official-evidence{padding:12px}}
</style>
