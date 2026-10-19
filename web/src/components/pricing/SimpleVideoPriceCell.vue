<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import messages from '@/i18n/pricingRedesignVideo'
import { cellKey, type PriceCell } from './types'
import { exactRate } from './videoPriceDecimal'
const props = defineProps<{ cell: PriceCell; original?: PriceCell; reference?: string; selected: boolean; disabled: boolean }>()
const emit = defineEmits<{ select: [key: string, value: boolean]; rules: [] }>()
const { t } = useI18n({ useScope: 'local', messages })
const editable = computed(() => props.cell.available || props.cell.unavailable_reason === 'verified_minimum_coverage_missing')
function validRate(value: string) { try { exactRate(value); return true } catch { return false } }
const reasons = computed(() => [
  !editable.value ? t('unavailable') : '',
  !validRate(props.cell.retail_usd_per_1m) ? t('missingRetail') : '',
  !validRate(props.cell.supplier_cny_per_1m) ? t('missingCost') : '',
  !validRate(props.cell.risk_multiplier) ? t('missingRisk') : '',
  props.cell.input_video && !props.cell.minimum_rule_version ? t('missingMinimum') : '',
].filter(Boolean))
const canEnable = computed(() => props.cell.enabled || reasons.value.length === 0)
</script>
<template>
  <div class="price-cell" :class="{ unavailable: !editable }">
    <div class="cell-heading"><label><input type="checkbox" :checked="selected" :disabled="disabled || !editable" :aria-label="`${t('select')} ${cellKey(cell)}`" @change="emit('select', cellKey(cell), ($event.target as HTMLInputElement).checked)"> {{ cell.enabled ? t('published') : t('disabled') }}</label><span v-if="!editable">{{ t('unavailable') }}</span></div>
    <label class="sr-only" :for="`price-${cellKey(cell)}`">{{ t('draft') }} {{ cellKey(cell) }}</label>
    <input :id="`price-${cellKey(cell)}`" v-model="cell.retail_usd_per_1m" class="rate" inputmode="decimal" :disabled="disabled || !editable" :placeholder="t('pricePlaceholder')">
    <small>{{ t('current') }}: {{ original?.retail_usd_per_1m || '—' }}</small>
    <small v-if="reference">{{ t('official') }}: {{ reference }}</small>
    <div class="cell-footer"><label><input v-model="cell.enabled" type="checkbox" :disabled="disabled || !canEnable"> {{ t('enable') }}</label><button type="button" @click="emit('rules')">{{ t('viewRules') }}</button></div>
    <small v-if="reasons.length" class="blocking">{{ reasons.join(' · ') }}</small>
    <small v-else-if="cell.unavailable_reason === 'verified_minimum_coverage_missing'" class="blocking">{{ t('missingMinimum') }} · {{ t('validateFirst') }}</small>
  </div>
</template>
<style scoped>
.price-cell{padding:14px;display:grid;gap:8px;min-width:0}.cell-heading,.cell-footer{display:flex;gap:10px;align-items:center;justify-content:space-between;font-size:12px}.cell-heading span,.blocking{color:rgb(var(--c-muted))}.rate{width:100%;padding:10px 12px;border:1px solid rgb(var(--c-line));border-radius:8px;background:rgb(var(--c-bg));font:inherit;color:inherit;box-sizing:border-box}.rate:focus{outline:2px solid rgb(var(--c-accent));outline-offset:2px}label{display:flex;align-items:center;gap:5px}small{font-size:11px;line-height:1.5;color:rgb(var(--c-muted))}button{border:0;background:transparent;color:rgb(var(--c-accent));cursor:pointer;white-space:nowrap;font:inherit;padding:0}input:disabled{opacity:.55;cursor:not-allowed}.unavailable{background:rgb(var(--c-bg)/.4)}.sr-only{position:absolute;width:1px;height:1px;padding:0;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}@media(max-width:480px){.cell-heading,.cell-footer{align-items:flex-start;flex-wrap:wrap}}
</style>
