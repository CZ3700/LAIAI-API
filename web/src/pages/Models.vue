<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'

interface PricedModel {
  model: string
  provider: string
  input_usd_per_1m: string
  output_usd_per_1m: string
  image_usd: string
}

const { t } = useI18n()
const models = ref<PricedModel[]>([])
const search = ref('')
const loading = ref(true)
const error = ref('')
const copiedModel = ref('')
let copyTimer: ReturnType<typeof setTimeout> | undefined

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return models.value
  return models.value.filter(
    (m) => m.model.toLowerCase().includes(q) || m.provider.toLowerCase().includes(q),
  )
})

async function copyName(model: string) {
  try {
    await navigator.clipboard.writeText(model)
    copiedModel.value = model
    clearTimeout(copyTimer)
    copyTimer = setTimeout(() => (copiedModel.value = ''), 1200)
  } catch {
    // 非安全上下文 clipboard 不可用：模型名本身可选中，静默降级
  }
}

onMounted(async () => {
  try {
    const r = await api<{ models: PricedModel[] }>('GET', '/api/pricing')
    models.value = r.models
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="max-w-4xl space-y-6">
    <div class="flex items-center justify-between gap-4">
      <h1 class="text-2xl font-semibold tracking-tight">{{ t('models.title') }}</h1>
      <input v-model="search" class="input max-w-xs" :placeholder="t('models.search')" />
    </div>

    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <p v-if="loading" class="text-sm text-muted">{{ t('common.loading') }}</p>

    <div v-else class="card overflow-x-auto">
      <table class="table w-full text-sm whitespace-nowrap">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3 font-medium">{{ t('models.model') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('models.provider') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('models.input') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('models.output') }}</th>
            <th class="px-4 py-3 font-medium text-right">{{ t('models.image') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="filtered.length === 0">
            <td colspan="5" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
          </tr>
          <tr v-for="m in filtered" :key="m.model">
            <td class="px-4 py-2.5">
              <button
                class="group inline-flex items-center gap-2 font-mono text-left hover:text-accent transition-colors"
                :title="t('common.copy')"
                @click="copyName(m.model)"
              >
                {{ m.model }}
                <span v-if="copiedModel === m.model" class="text-xs text-ok">{{ t('common.copied') }}</span>
                <svg
                  v-else
                  class="h-3.5 w-3.5 opacity-0 transition-opacity group-hover:opacity-60"
                  viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"
                  stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"
                >
                  <path d="M15.75 17.25v3.375c0 .621-.504 1.125-1.125 1.125h-9.75a1.125 1.125 0 0 1-1.125-1.125V7.875c0-.621.504-1.125 1.125-1.125H6.75a9.06 9.06 0 0 1 1.5.124m7.5 10.376h3.375c.621 0 1.125-.504 1.125-1.125V11.25c0-4.46-3.243-8.161-7.5-8.876a9.06 9.06 0 0 0-1.5-.124H9.375c-.621 0-1.125.504-1.125 1.125v3.5m7.5 10.375H9.375a1.125 1.125 0 0 1-1.125-1.125v-9.25m12 6.625v-1.875a3.375 3.375 0 0 0-3.375-3.375h-1.5a1.125 1.125 0 0 1-1.125-1.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H9.75" />
                </svg>
              </button>
            </td>
            <td class="px-4 py-2.5">
              <span class="badge-muted">{{ m.provider }}</span>
            </td>
            <td class="px-4 py-2.5 text-right font-mono">${{ m.input_usd_per_1m }}</td>
            <td class="px-4 py-2.5 text-right font-mono">${{ m.output_usd_per_1m }}</td>
            <td class="px-4 py-2.5 text-right font-mono text-muted">
              {{ m.image_usd !== '0' ? `$${m.image_usd}` : '—' }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p class="text-xs text-muted">{{ t('models.unitHint') }}</p>
  </div>
</template>
