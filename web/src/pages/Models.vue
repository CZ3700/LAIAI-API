<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, copyText, errorMessage } from '@/lib/api'
import { filterModels, modelModality, type ModelModality, type PricedModel } from '@/lib/modelCatalog'

const { t, n } = useI18n()
const models = ref<PricedModel[]>([])
const search = ref('')
const modality = ref<'all' | ModelModality>('all') // 模态筛选（chip 点击切换，再点还原 all）
const loading = ref(true)
const error = ref('')
const copiedModel = ref('')
let copyTimer: ReturnType<typeof setTimeout> | undefined

function modalityLabel(m: PricedModel): string {
  const labels: Record<ModelModality, string> = {
    chat: t('models.modChat'),
    image: t('models.modImage'),
    video: t('models.modVideo'),
    embedding: t('models.modEmbed'),
  }
  return labels[modelModality(m)]
}

const filtered = computed(() => {
  const base = filterModels(models.value, search.value)
  if (modality.value === 'all') return base
  return base.filter((m) => modelModality(m) === modality.value)
})
function toggleModality(kind: ModelModality) {
  modality.value = modality.value === kind ? 'all' : kind
}
const providerCount = computed(() => new Set(models.value.map((m) => m.vendor || m.provider)).size)
const modalityCounts = computed(() => {
  let chat = 0
  let image = 0
  let video = 0
  let embed = 0
  for (const m of models.value) {
    const kind = modelModality(m)
    if (kind === 'image') image++
    else if (kind === 'video') video++
    else if (kind === 'embedding') embed++
    else chat++
  }
  return { chat, image, video, embed }
})

async function copyName(model: string) {
  if (await copyText(model)) {
    copiedModel.value = model
    clearTimeout(copyTimer)
    copyTimer = setTimeout(() => (copiedModel.value = ''), 1200)
  }
}

onMounted(async () => {
  try {
    const r = await api<{ models: PricedModel[] }>('GET', '/api/pricing', undefined, { auth: false })
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
  <div class="models-page overflow-hidden">
    <section class="models-hero border-b border-line">
      <div class="mx-auto grid max-w-7xl gap-10 px-4 py-14 sm:px-6 lg:grid-cols-[0.95fr_1.05fr] lg:items-end lg:px-8">
        <div>
          <p class="text-sm font-medium text-accent">{{ t('surface.modelsCatalog') }}</p>
          <h1 class="models-title mt-4">{{ t('models.title') }}</h1>
          <p class="models-lead mt-5 max-w-2xl">{{ t('models.consoleHint') }}</p>
        </div>
        <div class="models-search-panel">
          <label class="label" for="model-search">{{ t('models.search') }}</label>
          <input id="model-search" v-model="search" class="input" :placeholder="t('models.search')" />
          <div class="mt-5 grid grid-cols-3 gap-px overflow-hidden rounded-lg bg-line text-sm">
            <div class="bg-panel p-4">
              <p class="font-mono text-2xl font-semibold">{{ models.length || '—' }}</p>
              <p class="mt-1 text-xs uppercase text-muted">{{ t('surface.statModels') }}</p>
            </div>
            <div class="bg-panel p-4">
              <p class="font-mono text-2xl font-semibold">{{ providerCount || '—' }}</p>
              <p class="mt-1 text-xs uppercase text-muted">{{ t('surface.statProviders') }}</p>
            </div>
            <div class="bg-panel p-4">
              <p class="font-mono text-2xl font-semibold">{{ filtered.length }}</p>
              <p class="mt-1 text-xs uppercase text-muted">{{ t('surface.statShown') }}</p>
            </div>
          </div>
          <div class="mt-4 flex flex-wrap gap-2 text-xs" role="group" :aria-label="t('models.filterByModality')">
            <button type="button" class="badge-muted transition-colors" :class="{ '!border-accent !text-accent': modality === 'all' }" @click="modality = 'all'">{{ t('models.filterAll') }} {{ models.length }}</button>
            <button type="button" class="badge-muted transition-colors" :class="{ '!border-accent !text-accent': modality === 'chat' }" @click="toggleModality('chat')">{{ t('models.modChat') }} {{ modalityCounts.chat }}</button>
            <button type="button" class="badge-muted transition-colors" :class="{ '!border-accent !text-accent': modality === 'image' }" @click="toggleModality('image')">{{ t('models.modImage') }} {{ modalityCounts.image }}</button>
            <button type="button" class="badge-muted transition-colors" :class="{ '!border-accent !text-accent': modality === 'video' }" @click="toggleModality('video')">{{ t('models.modVideo') }} {{ modalityCounts.video }}</button>
            <button type="button" class="badge-muted transition-colors" :class="{ '!border-accent !text-accent': modality === 'embedding' }" @click="toggleModality('embedding')">{{ t('models.modEmbed') }} {{ modalityCounts.embed }}</button>
          </div>
        </div>
      </div>
    </section>

    <section class="mx-auto max-w-7xl px-4 py-12 sm:px-6 lg:px-8">
      <p v-if="error" class="inline-alert" role="alert">{{ error }}</p>
    <!-- 加载骨架屏 -->
      <div v-if="loading" class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <div v-for="i in 9" :key="i" class="model-card p-5">
          <div class="skeleton h-4 w-2/3"></div>
          <div class="mt-5 grid grid-cols-3 gap-2">
            <div class="skeleton h-12"></div>
            <div class="skeleton h-12"></div>
            <div class="skeleton h-12"></div>
          </div>
        </div>
      </div>

      <div v-else-if="filtered.length === 0" class="empty-state">{{ t('common.empty') }}</div>

      <div v-else class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <article v-for="(m, i) in filtered" :key="m.model" v-reveal="(i % 9) * 35" class="model-card p-5">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <p class="truncate font-mono text-sm font-semibold" :title="m.model">{{ m.model }}</p>
              <div class="mt-2 flex flex-wrap gap-1.5">
                <span class="badge-muted">{{ m.vendor || m.provider }}</span>
                <span class="badge-muted">{{ modalityLabel(m) }}</span>
                <span v-if="m.category" class="badge-muted">{{ m.category }}</span>
              </div>
            </div>
            <div class="model-card-index">{{ String(i + 1).padStart(2, '0') }}</div>
          </div>

          <div class="mt-6 grid grid-cols-3 gap-px overflow-hidden rounded-lg bg-line text-xs">
            <div class="bg-panel p-3">
              <p class="text-muted">{{ t('models.input') }}</p>
              <p class="mt-1 font-mono font-semibold">${{ m.input_usd_per_1m }}</p>
            </div>
            <div class="bg-panel p-3">
              <p class="text-muted">{{ t('models.output') }}</p>
              <p class="mt-1 font-mono font-semibold">${{ m.output_usd_per_1m }}</p>
            </div>
            <div class="bg-panel p-3">
              <p class="text-muted">{{ t('models.image') }}</p>
              <p class="mt-1 font-mono font-semibold">{{ m.image_usd !== '0' ? `$${m.image_usd}` : '—' }}</p>
            </div>
          </div>

          <p v-if="modelModality(m) === 'video'" class="mt-3 text-xs text-muted">{{ t('models.videoBillingNote') }}</p>

          <div class="mt-5 flex items-center justify-between gap-3">
            <p class="truncate text-xs text-muted">
              <span v-if="m.context_length">{{ n(m.context_length) }} context</span>
              <span v-else>{{ t('models.unitHint') }}</span>
            </p>
              <button
                class="btn-ghost !min-h-0 shrink-0 !px-2.5 !py-1.5 text-xs"
                :title="t('common.copy')"
                @click="copyName(m.model)"
              >
                {{ copiedModel === m.model ? t('common.copied') : t('common.copy') }}
              </button>
          </div>
        </article>
      </div>
      <p class="mt-6 text-xs text-muted">{{ t('models.unitHint') }}</p>
    </section>
  </div>
</template>
