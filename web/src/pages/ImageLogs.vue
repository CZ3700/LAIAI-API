<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'

const { t } = useI18n()

interface ModelPrice {
  model: string
  image_usd: string
}
interface KeyRow { id: number; name: string; prefix: string }

const range = ref<'7d' | '30d' | 'month' | 'prev' | 'custom'>('7d')
const model = ref('')
const key = ref('')
const requestId = ref('')
const result = ref<'all' | 'success' | 'failed'>('all')
const customStart = ref('')
const customEnd = ref('')
const models = ref<ModelPrice[]>([])
const keys = ref<KeyRow[]>([])
const loading = ref(true)

onMounted(async () => {
  const [pricing, keyRows] = await Promise.allSettled([
    api<{ models: ModelPrice[] }>('GET', '/api/pricing', undefined, { auth: false }),
    api<{ keys: KeyRow[] }>('GET', '/api/keys'),
  ])
  if (pricing.status === 'fulfilled') models.value = pricing.value.models.filter((m) => Number(m.image_usd) > 0)
  if (keyRows.status === 'fulfilled') keys.value = keyRows.value.keys
  loading.value = false
})
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <h1 class="app-page-title">{{ t('imglog.title') }}</h1>
      </div>
    </div>

    <div class="toolbar-panel items-end">
      <div class="filter-group">
        <label>{{ t('imglog.filterTimeRange') }}</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: range === '7d' }" @click="range = '7d'">{{ t('imglog.range7d') }}</button>
          <button type="button" :class="{ active: range === '30d' }" @click="range = '30d'">{{ t('imglog.range30d') }}</button>
          <button type="button" :class="{ active: range === 'month' }" @click="range = 'month'">{{ t('imglog.rangeMonth') }}</button>
          <button type="button" :class="{ active: range === 'prev' }" @click="range = 'prev'">{{ t('imglog.rangePrev') }}</button>
          <button type="button" :class="{ active: range === 'custom' }" @click="range = 'custom'">{{ t('imglog.rangeCustom') }}</button>
        </div>
      </div>
      <div v-if="range === 'custom'" class="filter-group min-w-[260px]">
        <label>{{ t('imglog.filterCustomDate') }}</label>
        <div class="grid grid-cols-2 gap-2">
          <input v-model="customStart" class="input" type="date" :placeholder="t('imglog.startDate')" />
          <input v-model="customEnd" class="input" type="date" :placeholder="t('imglog.endDate')" />
        </div>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>{{ t('imglog.filterModel') }}</label>
        <select v-model="model" class="input">
          <option value="">{{ t('imglog.allModels') }}</option>
          <option v-for="m in models" :key="m.model" :value="m.model">{{ m.model }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>{{ t('imglog.filterApiKey') }}</label>
        <select v-model="key" class="input">
          <option value="">{{ t('imglog.allKeys') }}</option>
          <option v-for="k in keys" :key="k.id" :value="String(k.id)">{{ k.name || k.prefix }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>{{ t('imglog.filterRequestId') }}</label>
        <input v-model="requestId" class="input" :placeholder="t('imglog.requestIdPlaceholder')" />
      </div>
      <div class="filter-group">
        <label>{{ t('imglog.filterResult') }}</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: result === 'all' }" @click="result = 'all'">{{ t('imglog.resultAll') }}</button>
          <button type="button" :class="{ active: result === 'success' }" @click="result = 'success'">{{ t('imglog.resultSuccess') }}</button>
          <button type="button" :class="{ active: result === 'failed' }" @click="result = 'failed'">{{ t('imglog.resultFailed') }}</button>
        </div>
      </div>
      <button class="btn-primary !min-h-0 !px-4 !py-2" type="button" disabled>{{ t('imglog.btnPendingApi') }}</button>
    </div>

    <p class="text-xs text-muted">{{ t('imglog.placeholderNote') }}</p>

    <p v-if="loading" class="text-sm text-muted">{{ t('imglog.loading') }}</p>
    <div v-else class="data-table-wrap">
      <table class="table w-full text-sm whitespace-nowrap">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3">{{ t('imglog.colRequestId') }}</th>
            <th class="px-4 py-3">{{ t('imglog.colTime') }}</th>
            <th class="px-4 py-3">{{ t('imglog.colDriver') }}</th>
            <th class="px-4 py-3">{{ t('imglog.colModel') }}</th>
            <th class="px-4 py-3 text-right">{{ t('imglog.colImages') }}</th>
            <th class="px-4 py-3 text-right">{{ t('imglog.colCost') }}</th>
            <th class="px-4 py-3 text-right">{{ t('imglog.colGiftSpent') }}</th>
            <th class="px-4 py-3 text-right">{{ t('imglog.colPaidSpent') }}</th>
            <th class="px-4 py-3 text-right">{{ t('imglog.colDuration') }}</th>
            <th class="px-4 py-3">{{ t('imglog.colStatus') }}</th>
            <th class="px-4 py-3 text-right">{{ t('imglog.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td colspan="11" class="px-4 py-12 text-center text-muted">
              {{ t('imglog.emptyData') }}
              <p class="mt-2 text-xs">{{ t('imglog.emptyDataHint') }}</p>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
