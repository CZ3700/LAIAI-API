<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'
import heroImage from '@/assets/ai-routing-hero.jpg'

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
  { value: 'Gateway', key: 'unified' },
  { value: 'Stream', key: 'streaming' },
  { value: 'Ledger', key: 'billing' },
  { value: 'Global', key: 'global' },
]

const lanes = [
  { name: 'GPT-5.2', side: 'OpenAI', load: '91%', tone: 'bg-accent' },
  { name: 'Claude', side: 'Anthropic', load: '84%', tone: 'bg-ok' },
  { name: 'Gemini', side: 'Google', load: '76%', tone: 'bg-accent2' },
  { name: 'Qwen / DeepSeek', side: 'CN models', load: '88%', tone: 'bg-accent' },
  { name: 'GLM / MiniMax', side: 'CN models', load: '82%', tone: 'bg-ok' },
]
</script>

<template>
  <div class="overflow-hidden">
    <section class="landing-hero relative min-h-[calc(100svh-65px)] border-b border-line">
      <img
        :src="heroImage"
        alt=""
        class="landing-hero-image absolute inset-0 h-full w-full object-cover"
        aria-hidden="true"
      />
      <div class="landing-hero-wash absolute inset-0"></div>
      <div class="landing-hero-grid absolute inset-0" aria-hidden="true"></div>

      <div class="relative mx-auto flex min-h-[calc(100svh-65px)] max-w-7xl items-center px-4 py-16 sm:px-6 lg:px-8">
        <div class="max-w-2xl">
          <span class="enter landing-kicker inline-flex items-center gap-2 px-2.5 py-1 text-xs font-medium backdrop-blur" style="animation-delay: 0ms">
            <span class="h-1.5 w-1.5 rounded-full bg-ok"></span>
            {{ t('landing.tag') }}
          </span>
          <h1 class="enter landing-title mt-7 text-5xl font-semibold leading-[0.98] sm:text-7xl lg:text-8xl" style="animation-delay: 70ms">
            LAIAI
          </h1>
          <p class="enter landing-headline mt-5 max-w-xl text-xl leading-8 sm:text-2xl" style="animation-delay: 140ms">
            {{ t('landing.h1a') }} {{ t('landing.h1b') }}
          </p>
          <p class="enter landing-sub mt-5 max-w-lg text-sm leading-6 sm:text-base" style="animation-delay: 190ms">{{ t('landing.sub') }}</p>
          <div class="enter mt-9 flex flex-wrap gap-3" style="animation-delay: 250ms">
            <router-link to="/register" class="btn-primary px-5 !py-2.5">{{ t('landing.ctaStart') }}</router-link>
            <router-link to="/models" class="landing-secondary-btn px-5 !py-2.5">{{ t('landing.ctaModels') }}</router-link>
          </div>
          <div class="enter landing-stat-strip mt-10 grid max-w-xl grid-cols-3 divide-x py-4" style="animation-delay: 310ms">
            <div>
              <p class="text-2xl font-semibold">{{ models.length || '120+' }}</p>
              <p class="mt-1 text-xs uppercase">{{ t('surface.statModels') }}</p>
            </div>
            <div class="pl-5">
              <p class="text-2xl font-semibold">{{ providerCount || '18+' }}</p>
              <p class="mt-1 text-xs uppercase">{{ t('surface.statProviders') }}</p>
            </div>
            <div class="pl-5">
              <p class="text-2xl font-semibold">SSE</p>
              <p class="mt-1 text-xs uppercase">{{ t('surface.statStream') }}</p>
            </div>
          </div>
        </div>
      </div>
    </section>

    <section class="mx-auto max-w-7xl px-4 py-20 sm:px-6 lg:px-8">
      <div class="grid gap-10 lg:grid-cols-[0.78fr_1.22fr] lg:items-start">
        <div v-reveal>
          <p class="text-sm font-medium text-accent">{{ t('surface.landingControlPlane') }}</p>
          <h2 class="mt-3 max-w-md text-3xl font-semibold leading-tight sm:text-4xl">{{ t('landing.ctaTitle') }}</h2>
          <p class="mt-4 max-w-sm text-sm leading-6 text-muted">{{ t('landing.ctaSub') }}</p>
        </div>
        <div class="landing-feature-grid grid gap-px overflow-hidden border border-line bg-line sm:grid-cols-2">
          <div
            v-for="(f, i) in features"
            :key="f.key"
            v-reveal="i * 80"
            class="landing-feature bg-panel p-6"
          >
            <p class="font-mono text-xs text-accent">{{ f.value }}</p>
            <h3 class="mt-10 font-semibold">{{ t(`landing.f_${f.key}_t`) }}</h3>
            <p class="mt-2 text-sm leading-6 text-muted">{{ t(`landing.f_${f.key}_d`) }}</p>
          </div>
        </div>
      </div>
    </section>

    <section class="mx-auto grid max-w-7xl gap-8 px-4 pb-20 sm:px-6 lg:grid-cols-[1fr_0.9fr] lg:px-8">
      <div v-reveal class="landing-terminal overflow-hidden border border-line bg-[#05080d] text-slate-200 shadow-card">
        <div class="flex items-center justify-between border-b border-white/10 px-4 py-3">
          <span class="text-xs text-slate-500">{{ t('landing.codeTitle') }}</span>
          <span class="text-xs text-cyan-300">{{ t('surface.landingCompat') }}</span>
        </div>
        <pre class="overflow-x-auto p-5 text-xs leading-relaxed"><code>curl https://your-domain/v1/chat/completions \
  -H "Authorization: Bearer sk-laiai-..." \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.2",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'</code></pre>
      </div>

      <div v-reveal class="space-y-5">
        <div class="landing-routing p-5">
          <div class="flex items-center justify-between">
            <p class="text-sm font-medium">{{ t('surface.landingLiveRouting') }}</p>
            <span class="rounded-full bg-ok/10 px-2 py-0.5 text-xs font-medium text-ok">{{ t('surface.statusHealthy') }}</span>
          </div>
          <div class="mt-5 space-y-3">
            <div v-for="lane in lanes" :key="lane.name" class="landing-lane">
              <div class="flex items-center justify-between gap-4 text-sm">
                <span class="font-medium">{{ lane.name }}</span>
                <span class="text-muted">{{ lane.side }}</span>
              </div>
              <div class="mt-2 h-1.5 overflow-hidden rounded-full bg-line">
                <div class="landing-lane-bar h-full rounded-full" :class="lane.tone" :style="{ width: lane.load }"></div>
              </div>
            </div>
          </div>
        </div>
        <div
          class="landing-ledger p-5"
        >
          <p class="text-sm font-medium">{{ t('surface.landingBillingKernel') }}</p>
          <p class="mt-3 text-sm leading-6 text-muted">{{ t('landing.f_billing_d') }}</p>
          <div class="mt-5 grid grid-cols-3 gap-px overflow-hidden rounded-lg bg-line text-center text-xs">
            <div class="bg-panel p-3">
              <p class="font-semibold text-ink">{{ t('surface.ledgerHold') }}</p>
              <p class="mt-1 text-muted">{{ t('surface.ledgerReserve') }}</p>
            </div>
            <div class="bg-panel p-3">
              <p class="font-semibold text-ink">{{ t('surface.ledgerCapture') }}</p>
              <p class="mt-1 text-muted">{{ t('surface.ledgerMeter') }}</p>
            </div>
            <div class="bg-panel p-3">
              <p class="font-semibold text-ink">{{ t('surface.ledgerLedger') }}</p>
              <p class="mt-1 text-muted">{{ t('surface.ledgerAudit') }}</p>
            </div>
          </div>
          <router-link to="/docs" class="mt-5 inline-flex text-sm font-medium text-accent hover:underline">{{ t('nav.docs') }}</router-link>
        </div>
      </div>
    </section>

    <section class="landing-final border-t border-line">
      <div class="mx-auto flex max-w-7xl flex-col gap-5 px-4 py-12 sm:px-6 lg:flex-row lg:items-center lg:justify-between lg:px-8">
        <div>
          <h2 class="text-2xl font-semibold">{{ t('landing.ctaTitle') }}</h2>
          <p class="mt-2 max-w-2xl text-sm text-muted">{{ t('landing.ctaSub') }}</p>
        </div>
        <router-link to="/register" class="btn-primary w-fit px-5 !py-2.5">{{ t('landing.ctaStart') }}</router-link>
      </div>
    </section>
  </div>
</template>
