<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'

interface ModelRank { model: string; calls: number; tokens: number }
interface ProviderRank { provider: string; calls: number }

const { t, n } = useI18n()
const models = ref<ModelRank[]>([])
const providers = ref<ProviderRank[]>([])
const loading = ref(true)

const maxCalls = computed(() => Math.max(1, ...models.value.map((m) => m.calls)))
const maxProvider = computed(() => Math.max(1, ...providers.value.map((p) => p.calls)))

function fmtTokens(x: number): string {
  if (x >= 1_000_000) return (x / 1_000_000).toFixed(1) + 'M'
  if (x >= 1_000) return (x / 1_000).toFixed(1) + 'K'
  return String(x)
}

onMounted(async () => {
  try {
    const r = await api<{ models: ModelRank[]; providers: ProviderRank[] }>('GET', '/api/rankings', undefined, { auth: false })
    models.value = r.models
    providers.value = r.providers
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="mx-auto max-w-5xl px-4 py-14 sm:px-6 lg:px-8">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('surface.rankingsEyebrow') }}</p>
        <h1 class="app-page-title">{{ t('rankings.title') }}</h1>
        <p class="app-page-subtitle">{{ t('rankings.lead') }}</p>
      </div>
    </div>

    <div v-if="loading" class="mt-10 space-y-3">
      <div v-for="i in 8" :key="i" class="skeleton h-8"></div>
    </div>

    <template v-else>
      <!-- 模型排行 -->
      <section class="mt-10">
        <h2 class="mb-4 text-xl font-semibold">{{ t('rankings.topModels') }}</h2>
        <div v-if="models.length === 0" class="empty-state">{{ t('common.empty') }}</div>
        <div v-else class="card divide-y divide-line">
          <div
            v-for="(m, i) in models"
            :key="m.model"
            v-reveal="i * 40"
            class="flex items-center gap-4 px-5 py-3"
          >
            <span class="w-6 shrink-0 text-right font-mono text-sm text-muted">{{ i + 1 }}</span>
            <div class="min-w-0 flex-1">
              <div class="flex items-baseline justify-between gap-2">
                <span class="truncate font-mono text-sm">{{ m.model }}</span>
                <span class="shrink-0 text-xs text-muted">{{ n(m.calls) }} · {{ fmtTokens(m.tokens) }} tok</span>
              </div>
              <div class="mt-1.5 h-1.5 rounded-full bg-line">
                <div
                  class="h-full rounded-full"
                  style="background: linear-gradient(90deg, rgb(var(--c-accent)), rgb(var(--c-accent-2)))"
                  :style="{ width: `${(m.calls / maxCalls) * 100}%` }"
                ></div>
              </div>
            </div>
          </div>
        </div>
      </section>

      <!-- 厂商排行 -->
      <section class="mt-10">
        <h2 class="mb-4 text-xl font-semibold">{{ t('rankings.topProviders') }}</h2>
        <div v-if="providers.length === 0" class="empty-state">{{ t('common.empty') }}</div>
        <div v-else class="grid gap-3 sm:grid-cols-2">
          <div v-for="p in providers" :key="p.provider" class="card card-lift p-4">
            <div class="flex items-baseline justify-between">
              <span class="font-mono text-sm">{{ p.provider }}</span>
              <span class="text-xs text-muted">{{ n(p.calls) }}</span>
            </div>
            <div class="mt-2 h-1.5 rounded-full bg-line">
              <div
                class="h-full rounded-full"
                style="background: linear-gradient(90deg, rgb(var(--c-accent)), rgb(var(--c-accent-2)))"
                :style="{ width: `${(p.calls / maxProvider) * 100}%` }"
              ></div>
            </div>
          </div>
        </div>
      </section>
    </template>
  </div>
</template>
