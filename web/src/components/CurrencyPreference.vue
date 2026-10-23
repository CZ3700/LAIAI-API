<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useCurrency } from '@/stores/currency'
import type { Currency } from '@/lib/decimalMoney'
const { t } = useI18n(), currency = useCurrency()
const error = ref(false), saved = ref(false)
async function choose(value: Currency) {
  error.value = false; saved.value = false
  try { await currency.save(value); saved.value = true } catch { error.value = true }
}
</script>
<template>
  <section v-if="currency.available || currency.failed" class="app-panel p-6 space-y-4" aria-labelledby="currency-preference-title">
    <h2 id="currency-preference-title" class="font-semibold">{{ t('currency.title') }}</h2>
    <fieldset v-if="currency.available" class="flex flex-wrap gap-5" :disabled="currency.saving || !currency.ready">
      <legend class="sr-only">{{ t('currency.title') }}</legend>
      <label v-for="code in ['USD', 'CNY'] as const" :key="code" class="flex items-center gap-2 cursor-pointer"><input type="radio" name="display-currency" :value="code" :checked="currency.displayCurrency === code" @change="choose(code)">{{ t(`currency.${code}`) }}</label>
    </fieldset>
    <p v-if="currency.available" class="text-sm text-muted">{{ t('currency.hint', { rate: currency.cnyPerUsd }) }}</p>
    <p v-if="currency.failed || error" class="text-sm text-danger" role="alert">{{ t('currency.error') }} <button type="button" class="table-link" @click="currency.load(currency.sessionToken)">{{ t('currency.retry') }}</button></p>
    <p v-if="saved" class="text-sm text-ok" role="status">{{ t('settings.saved') }}</p>
  </section>
</template>
