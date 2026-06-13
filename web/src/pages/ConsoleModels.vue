<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { api, copyText, errorMessage } from '@/lib/api'

interface PricedModel {
  model: string
  provider: string
  input_usd_per_1m: string
  output_usd_per_1m: string
  image_usd: string
}

const { t } = useI18n()
const router = useRouter()
const models = ref<PricedModel[]>([])
const search = ref('')
const loading = ref(true)
const error = ref('')
const selected = ref<PricedModel | null>(null)
const copied = ref(false)

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return models.value
  return models.value.filter((m) => m.model.toLowerCase().includes(q) || m.provider.toLowerCase().includes(q))
})

// 简易模态推断（与 Model.vue 同源直觉）：图像价>0 → 图像；名字含 embed → 向量
function modality(m: PricedModel): string {
  if (m.image_usd !== '0') return t('models.modImage')
  if (/embed/i.test(m.model)) return t('models.modEmbed')
  return t('models.modChat')
}

async function copyName() {
  if (selected.value && (await copyText(selected.value.model))) {
    copied.value = true
    setTimeout(() => (copied.value = false), 1200)
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
  <div class="space-y-6">
    <div class="flex flex-wrap items-center justify-between gap-4">
      <div>
        <h1 class="text-2xl font-semibold tracking-tight">{{ t('models.title') }}</h1>
        <p class="mt-1 text-sm text-muted">{{ t('models.consoleHint') }}</p>
      </div>
      <input v-model="search" class="input max-w-xs" :placeholder="t('models.search')" />
    </div>

    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>

    <div v-if="loading" class="card p-4 space-y-3">
      <div v-for="i in 6" :key="i" class="skeleton h-5" :style="{ maxWidth: `${50 + ((i * 13) % 40)}%` }"></div>
    </div>

    <div v-else class="grid gap-4 lg:grid-cols-[1fr_22rem]">
      <!-- 模型列表 -->
      <div class="card max-h-[34rem] overflow-y-auto">
        <table class="table w-full text-sm">
          <thead class="sticky top-0 z-10 bg-panel/70 backdrop-blur">
            <tr class="text-left text-muted">
              <th class="px-4 py-2.5 font-medium">{{ t('models.model') }}</th>
              <th class="px-4 py-2.5 font-medium">{{ t('models.provider') }}</th>
              <th class="px-4 py-2.5 font-medium text-right">{{ t('models.input') }}/{{ t('models.output') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="filtered.length === 0">
              <td colspan="3" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
            </tr>
            <tr
              v-for="m in filtered"
              :key="m.model"
              class="cursor-pointer border-b border-line last:border-0"
              :class="selected?.model === m.model ? 'bg-accent/10' : ''"
              @click="selected = m"
            >
              <td class="px-4 py-2.5 font-mono text-xs">{{ m.model }}</td>
              <td class="px-4 py-2.5"><span class="badge-muted">{{ m.provider }}</span></td>
              <td class="px-4 py-2.5 text-right font-mono text-xs">${{ m.input_usd_per_1m }} / ${{ m.output_usd_per_1m }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- 详情面板 -->
      <div v-if="selected" class="card h-fit p-5 lg:sticky lg:top-6">
        <div class="flex items-start justify-between gap-2">
          <div>
            <p class="font-mono text-sm font-semibold break-all">{{ selected.model }}</p>
            <span class="mt-1 inline-block badge-muted">{{ selected.provider }}</span>
          </div>
          <button class="btn-ghost !py-1 text-xs" @click="copyName">{{ copied ? t('common.copied') : t('common.copy') }}</button>
        </div>

        <div class="mt-4 grid grid-cols-2 gap-3">
          <div class="rounded-xl bg-elev/60 p-3">
            <p class="text-[11px] text-muted">{{ t('models.input') }} /1M</p>
            <p class="mt-0.5 font-mono font-semibold">${{ selected.input_usd_per_1m }}</p>
          </div>
          <div class="rounded-xl bg-elev/60 p-3">
            <p class="text-[11px] text-muted">{{ t('models.output') }} /1M</p>
            <p class="mt-0.5 font-mono font-semibold">${{ selected.output_usd_per_1m }}</p>
          </div>
          <div v-if="selected.image_usd !== '0'" class="rounded-xl bg-elev/60 p-3">
            <p class="text-[11px] text-muted">{{ t('models.image') }}</p>
            <p class="mt-0.5 font-mono font-semibold">${{ selected.image_usd }}</p>
          </div>
          <div class="rounded-xl bg-elev/60 p-3">
            <p class="text-[11px] text-muted">{{ t('models.modality') }}</p>
            <p class="mt-0.5 font-semibold">{{ modality(selected) }}</p>
          </div>
        </div>

        <button class="btn-primary mt-4 w-full" @click="openInPlayground">{{ t('models.openPlayground') }}</button>
        <p class="mt-3 text-xs text-muted">{{ t('models.unitHint') }}</p>
      </div>
    </div>
  </div>
</template>
