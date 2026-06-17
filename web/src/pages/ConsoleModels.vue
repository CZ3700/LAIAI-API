<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { api, copyText, errorMessage } from '@/lib/api'
import { filterModels, modelModality, type ModelModality, type PricedModel } from '@/lib/modelCatalog'

const { t } = useI18n()
const router = useRouter()

const models = ref<PricedModel[]>([])
const search = ref('')
const loading = ref(true)
const error = ref('')
const selected = ref<PricedModel | null>(null)
const copied = ref('')
const view = ref<'grid' | 'list'>('grid')
const modeFilter = ref<'all' | ModelModality>('all')
const sort = ref<'name' | 'input' | 'output'>('name')

const modalities: Array<'all' | ModelModality> = ['all', 'chat', 'image', 'video', 'embedding']

const filtered = computed(() => {
  const bySearch = filterModels(models.value, search.value)
  const byMode = modeFilter.value === 'all'
    ? bySearch
    : bySearch.filter((m) => modelModality(m) === modeFilter.value)
  return [...byMode].sort((a, b) => {
    if (sort.value === 'input') return Number(a.input_usd_per_1m) - Number(b.input_usd_per_1m)
    if (sort.value === 'output') return Number(a.output_usd_per_1m) - Number(b.output_usd_per_1m)
    return a.model.localeCompare(b.model)
  })
})
const providerCount = computed(() => new Set(models.value.map((m) => m.provider)).size)
const modalityCounts = computed(() => {
  const counts = new Map<ModelModality, number>()
  for (const m of models.value) counts.set(modelModality(m), (counts.get(modelModality(m)) ?? 0) + 1)
  return counts
})
const cheapest = computed(() =>
  [...models.value].sort((a, b) => Number(a.input_usd_per_1m) - Number(b.input_usd_per_1m))[0] ?? null,
)

function modalityLabel(mode: 'all' | ModelModality): string {
  if (mode === 'all') return t('models.modAll')
  const labels: Record<ModelModality, string> = {
    chat: t('models.modChat'),
    image: t('models.modImage'),
    video: t('models.modVideo'),
    embedding: t('models.modEmbed'),
  }
  return labels[mode]
}

function selectModel(model: PricedModel) {
  selected.value = model
}

async function copyName(model = selected.value?.model) {
  if (model && (await copyText(model))) {
    copied.value = model
    window.setTimeout(() => (copied.value = ''), 1200)
  }
}

function openInPlayground() {
  if (selected.value) router.push({ path: '/app/playground', query: { model: selected.value.model } })
}

onMounted(async () => {
  try {
    const r = await api<{ models: PricedModel[] }>('GET', '/api/pricing', undefined, { auth: false })
    models.value = r.models
    selected.value = r.models[0] ?? null
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="app-page models-console-page">
    <section class="console-hero p-5 sm:p-6">
      <div class="grid gap-6 xl:grid-cols-[minmax(0,1fr)_24rem] xl:items-end">
        <div>
          <p class="app-eyebrow">{{ t('surface.modelsEyebrow') }}</p>
          <h1 class="app-page-title">{{ t('models.title') }}</h1>
          <p class="app-page-subtitle">{{ t('models.consoleHint') }}</p>
        </div>
        <div class="grid grid-cols-3 gap-px overflow-hidden rounded-lg border border-line bg-line">
          <div class="bg-panel p-4">
            <p class="font-mono text-2xl font-semibold">{{ models.length || '—' }}</p>
            <p class="mt-1 text-[11px] uppercase text-muted">{{ t('surface.statModels') }}</p>
          </div>
          <div class="bg-panel p-4">
            <p class="font-mono text-2xl font-semibold">{{ providerCount || '—' }}</p>
            <p class="mt-1 text-[11px] uppercase text-muted">{{ t('surface.statProviders') }}</p>
          </div>
          <div class="bg-panel p-4">
            <p class="font-mono text-2xl font-semibold">{{ filtered.length }}</p>
            <p class="mt-1 text-[11px] uppercase text-muted">{{ t('surface.statMatched') }}</p>
          </div>
        </div>
      </div>
    </section>

    <p v-if="error" class="inline-alert" role="alert">{{ error }}</p>

    <div class="toolbar-panel">
      <div class="flex min-w-0 flex-1 flex-wrap items-center gap-2">
        <input v-model="search" class="input min-w-64 max-w-md flex-1" type="search" :placeholder="t('models.search')" />
        <div class="seg-control">
          <button type="button" :class="view === 'list' ? 'is-active' : ''" @click="view = 'list'">{{ t('models.listView') }}</button>
          <button type="button" :class="view === 'grid' ? 'is-active' : ''" @click="view = 'grid'">{{ t('models.gridView') }}</button>
        </div>
        <select v-model="sort" class="input !w-36">
          <option value="name">{{ t('models.sortName') }}</option>
          <option value="input">{{ t('models.sortInput') }}</option>
          <option value="output">{{ t('models.sortOutput') }}</option>
        </select>
      </div>
      <router-link to="/app/devex" class="btn-ghost !py-1.5 text-xs">{{ t('nav.devex') }}</router-link>
    </div>

    <div class="flex flex-wrap gap-2">
      <button
        v-for="mode in modalities"
        :key="mode"
        type="button"
        class="model-chip"
        :class="modeFilter === mode ? 'is-active' : ''"
        @click="modeFilter = mode"
      >
        {{ modalityLabel(mode) }}
        <span>{{ mode === 'all' ? models.length : modalityCounts.get(mode) ?? 0 }}</span>
      </button>
    </div>

    <div v-if="loading" class="app-panel p-4 space-y-3">
      <div v-for="i in 6" :key="i" class="skeleton h-5" :style="{ maxWidth: `${50 + ((i * 13) % 40)}%` }"></div>
    </div>

    <div v-else class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_25rem]">
      <div v-if="filtered.length === 0" class="app-panel p-8 text-center text-sm text-muted">{{ t('common.empty') }}</div>

      <div v-else-if="view === 'grid'" class="grid gap-4 lg:grid-cols-2 2xl:grid-cols-3">
        <article
          v-for="m in filtered"
          :key="m.model"
          class="model-card app-panel p-4"
          :class="selected?.model === m.model ? 'ring-rainbow' : ''"
          @click="selectModel(m)"
        >
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <button class="font-mono text-sm font-semibold text-accent hover:underline" type="button" @click.stop="copyName(m.model)">
                {{ m.model }}
              </button>
              <p class="mt-1 text-xs text-muted">{{ m.provider }} · {{ modalityLabel(modelModality(m)) }}</p>
            </div>
            <span v-if="copied === m.model" class="badge-ok">{{ t('common.copied') }}</span>
          </div>
          <p class="mt-4 line-clamp-3 min-h-[4.5rem] text-sm leading-6 text-muted">
            {{ t('models.cardHint') }}
          </p>
          <div class="mt-4 grid grid-cols-2 gap-px overflow-hidden rounded-lg border border-line bg-line text-xs">
            <div class="bg-panel p-3">
              <p class="text-muted">{{ t('models.input') }} /1M</p>
              <p class="mt-1 font-mono font-semibold">${{ m.input_usd_per_1m }}</p>
            </div>
            <div class="bg-panel p-3">
              <p class="text-muted">{{ t('models.output') }} /1M</p>
              <p class="mt-1 font-mono font-semibold">${{ m.output_usd_per_1m }}</p>
            </div>
          </div>
        </article>
      </div>

      <div v-else class="data-table-wrap max-h-[42rem]">
        <table class="table w-full text-sm">
          <thead class="sticky top-0 z-10 bg-panel/80 backdrop-blur">
            <tr class="text-left text-muted">
              <th class="px-4 py-2.5 font-medium">{{ t('models.model') }}</th>
              <th class="px-4 py-2.5 font-medium">{{ t('models.provider') }}</th>
              <th class="px-4 py-2.5 font-medium">{{ t('models.modality') }}</th>
              <th class="px-4 py-2.5 font-medium text-right">{{ t('models.input') }}/{{ t('models.output') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="m in filtered"
              :key="m.model"
              class="cursor-pointer border-b border-line last:border-0 transition-colors"
              :class="selected?.model === m.model ? 'bg-accent/10' : ''"
              @click="selectModel(m)"
            >
              <td class="px-4 py-3 font-mono text-xs font-semibold">{{ m.model }}</td>
              <td class="px-4 py-3"><span class="badge-muted">{{ m.provider }}</span></td>
              <td class="px-4 py-3 text-muted">{{ modalityLabel(modelModality(m)) }}</td>
              <td class="px-4 py-3 text-right font-mono text-xs">${{ m.input_usd_per_1m }} / ${{ m.output_usd_per_1m }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <aside v-if="selected" class="model-inspector app-panel h-fit p-5 xl:sticky xl:top-6">
        <div class="flex items-start justify-between gap-3">
          <div>
            <p class="text-xs font-medium text-accent">{{ t('surface.selectedModel') }}</p>
            <p class="mt-2 break-all font-mono text-base font-semibold">{{ selected.model }}</p>
            <div class="mt-2 flex flex-wrap gap-1.5">
              <span class="badge-muted">{{ selected.provider }}</span>
              <span class="badge-muted">{{ modalityLabel(modelModality(selected)) }}</span>
            </div>
          </div>
          <button class="btn-ghost !py-1 text-xs" @click="copyName()">{{ copied === selected.model ? t('common.copied') : t('common.copy') }}</button>
        </div>

        <div class="mt-4 grid grid-cols-2 gap-3">
          <div class="model-price-tile p-3">
            <p class="text-[11px] text-muted">{{ t('models.input') }} /1M</p>
            <p class="mt-0.5 font-mono font-semibold">${{ selected.input_usd_per_1m }}</p>
          </div>
          <div class="model-price-tile p-3">
            <p class="text-[11px] text-muted">{{ t('models.output') }} /1M</p>
            <p class="mt-0.5 font-mono font-semibold">${{ selected.output_usd_per_1m }}</p>
          </div>
          <div v-if="selected.image_usd !== '0'" class="model-price-tile p-3">
            <p class="text-[11px] text-muted">{{ t('models.image') }}</p>
            <p class="mt-0.5 font-mono font-semibold">${{ selected.image_usd }}</p>
          </div>
          <div class="model-price-tile p-3">
            <p class="text-[11px] text-muted">{{ t('models.modality') }}</p>
            <p class="mt-0.5 font-semibold">{{ modalityLabel(modelModality(selected)) }}</p>
          </div>
        </div>

        <div class="mt-5 rounded-lg border border-line bg-elev/35 p-4 text-xs leading-5 text-muted">
          <p class="font-medium text-ink">{{ t('models.pickGuide') }}</p>
          <p class="mt-1">{{ t('models.pickGuideBody') }}</p>
          <p v-if="cheapest" class="mt-2">
            {{ t('models.cheapestPrefix') }} <span class="font-mono text-ink">{{ cheapest.model }}</span>
          </p>
        </div>

        <button class="btn-primary mt-5 w-full" @click="openInPlayground">{{ t('models.openPlayground') }}</button>
        <p class="mt-3 text-xs text-muted">{{ t('models.unitHint') }}</p>
      </aside>
    </div>
  </div>
</template>
