<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'

interface PricedModel {
  model: string
  provider: string
}

const { t } = useI18n()
const models = ref<PricedModel[]>([])
const providerCount = ref(0)

onMounted(async () => {
  try {
    const r = await api<{ models: PricedModel[] }>('GET', '/api/pricing', undefined, { auth: false })
    models.value = r.models
    providerCount.value = new Set(r.models.map((m) => m.provider)).size
  } catch {
    // 首页不因价目接口失败而崩
  }
})

const features = [
  { icon: 'M9 12.75 11.25 15 15 9.75M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z', key: 'unified' },
  { icon: 'M13 10V3L4 14h7v7l9-11h-7Z', key: 'streaming' },
  { icon: 'M2.25 18.75a60.07 60.07 0 0 1 15.797 2.101c.727.198 1.453-.342 1.453-1.096V18.75M3.75 4.5v.75A.75.75 0 0 1 3 6h-.75m0 0v-.375c0-.621.504-1.125 1.125-1.125H20.25M2.25 6v9m18-10.5v.75c0 .414.336.75.75.75h.75m-1.5-1.5h.375c.621 0 1.125.504 1.125 1.125v9.75c0 .621-.504 1.125-1.125 1.125h-.375m1.5-1.5H21a.75.75 0 0 0-.75.75v.75m0 0H3.75m0 0h-.375a1.125 1.125 0 0 1-1.125-1.125V15m1.5 1.5v-.75A.75.75 0 0 0 3 15h-.75M15 10.5a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z', key: 'billing' },
  { icon: 'M12 21a9.004 9.004 0 0 0 8.716-6.747M12 21a9.004 9.004 0 0 1-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 0 1 7.843 4.582M12 3a8.997 8.997 0 0 0-7.843 4.582m15.686 0A11.953 11.953 0 0 1 12 10.5c-2.998 0-5.74-1.1-7.843-2.918m15.686 0A8.959 8.959 0 0 1 21 12c0 .778-.099 1.533-.284 2.253m0 0A17.919 17.919 0 0 1 12 16.5c-3.162 0-6.133-.815-8.716-2.247m0 0A9.015 9.015 0 0 1 3 12c0-1.605.42-3.113 1.157-4.418', key: 'global' },
]
</script>

<template>
  <div>
    <!-- Hero -->
    <section class="aurora">
      <div class="mx-auto max-w-4xl px-6 py-24 text-center sm:py-32">
        <span class="badge-muted">{{ t('landing.tag') }}</span>
        <h1 class="mt-6 text-4xl font-bold tracking-tight sm:text-6xl">
          {{ t('landing.h1a') }}
          <span
            class="bg-clip-text text-transparent"
            style="background-image: linear-gradient(135deg, rgb(var(--c-accent)), rgb(var(--c-accent-2)))"
          >{{ t('landing.h1b') }}</span>
        </h1>
        <p class="mx-auto mt-6 max-w-2xl text-lg text-muted">{{ t('landing.sub') }}</p>
        <div class="mt-10 flex flex-wrap justify-center gap-3">
          <router-link to="/register" class="btn-primary px-6 !py-2.5">{{ t('landing.ctaStart') }}</router-link>
          <router-link to="/models" class="btn-ghost px-6 !py-2.5">{{ t('landing.ctaModels') }}</router-link>
        </div>
        <p v-if="models.length" class="mt-8 text-sm text-muted">
          {{ t('landing.stat', { models: models.length, providers: providerCount }) }}
        </p>
      </div>
    </section>

    <!-- Features -->
    <section class="mx-auto max-w-6xl px-6 py-16">
      <div class="grid gap-6 sm:grid-cols-2 lg:grid-cols-4">
        <div v-for="f in features" :key="f.key" class="card p-6">
          <div
            class="grid h-10 w-10 place-items-center rounded-lg text-accent"
            style="background: rgb(var(--c-accent) / 0.12)"
          >
            <svg class="h-5 w-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <path :d="f.icon" />
            </svg>
          </div>
          <h3 class="mt-4 font-semibold">{{ t(`landing.f_${f.key}_t`) }}</h3>
          <p class="mt-1.5 text-sm text-muted">{{ t(`landing.f_${f.key}_d`) }}</p>
        </div>
      </div>
    </section>

    <!-- Code sample -->
    <section class="mx-auto max-w-4xl px-6 pb-16">
      <div class="card overflow-hidden">
        <div class="border-b border-line px-4 py-2 text-xs text-muted">{{ t('landing.codeTitle') }}</div>
        <pre class="overflow-x-auto p-5 text-xs leading-relaxed"><code>curl https://your-domain/v1/chat/completions \
  -H "Authorization: Bearer sk-laiai-..." \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.2",
    "messages": [{"role": "user", "content": "Hello"}]
  }'</code></pre>
      </div>
    </section>

    <!-- CTA -->
    <section class="mx-auto max-w-4xl px-6 pb-24 text-center">
      <div class="card p-12" style="background: linear-gradient(135deg, rgb(var(--c-accent) / 0.08), rgb(var(--c-accent-2) / 0.08))">
        <h2 class="text-2xl font-semibold tracking-tight">{{ t('landing.ctaTitle') }}</h2>
        <p class="mx-auto mt-3 max-w-xl text-muted">{{ t('landing.ctaSub') }}</p>
        <router-link to="/register" class="btn-primary mt-6 inline-flex px-6 !py-2.5">{{ t('landing.ctaStart') }}</router-link>
      </div>
    </section>
  </div>
</template>
