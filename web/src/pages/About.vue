<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import BrandMark from '@/components/BrandMark.vue'
const { t } = useI18n()

const principles = [
  { n: '01', key: 'missionTitle', body: 'missionBody' },
  { n: '02', key: 'howTitle', body: 'howBody' },
  { n: '03', key: 'contactTitle', body: 'contactBody' },
]

const signals = [
  { label: 'Routing', value: 'healthy' },
  { label: 'Billing', value: 'ledgered' },
  { label: 'Models', value: 'global' },
]
</script>

<template>
  <div class="about-page overflow-hidden">
    <section class="about-hero border-b border-line">
      <div class="mx-auto grid min-h-[calc(82svh-65px)] max-w-7xl gap-12 px-4 py-16 sm:px-6 lg:grid-cols-[0.9fr_1.1fr] lg:items-center lg:px-8">
        <div>
          <p class="enter text-sm font-medium text-accent" style="animation-delay: 0ms">{{ t('about.title') }}</p>
          <h1 class="enter about-hero-title mt-5" style="animation-delay: 70ms">
            LAIAI
          </h1>
          <p class="enter about-hero-lead mt-5 max-w-2xl" style="animation-delay: 120ms">
            {{ t('about.lead') }}
          </p>
          <p class="enter mt-6 max-w-xl text-base leading-7 text-muted" style="animation-delay: 170ms">
            {{ t('about.missionBody') }}
          </p>
          <div class="enter mt-9 flex flex-wrap gap-3" style="animation-delay: 210ms">
            <router-link to="/register" class="btn-primary px-5 !py-2.5">{{ t('landing.ctaStart') }}</router-link>
            <router-link to="/models" class="btn-ghost px-5 !py-2.5">{{ t('landing.ctaModels') }}</router-link>
          </div>
        </div>

        <div class="enter about-system" style="animation-delay: 180ms" aria-hidden="true">
          <div class="about-orbit about-orbit-a"></div>
          <div class="about-orbit about-orbit-b"></div>
          <div class="about-core">
            <BrandMark size="lg" :show-word="false" />
            <small>gateway</small>
          </div>
          <div class="about-node about-node-openai">OpenAI</div>
          <div class="about-node about-node-claude">Claude</div>
          <div class="about-node about-node-gemini">Gemini</div>
          <div class="about-node about-node-qwen">Qwen</div>
          <div class="about-node about-node-deepseek">DeepSeek</div>
          <div class="about-node about-node-glm">GLM</div>
          <div class="about-node about-node-minimax">MiniMax</div>
          <div class="about-pulse"></div>
        </div>
      </div>
    </section>

    <section class="mx-auto max-w-7xl px-4 py-16 sm:px-6 lg:px-8">
      <div class="grid gap-10 lg:grid-cols-[0.82fr_1.18fr]">
        <div v-reveal>
          <p class="text-sm font-medium text-accent">{{ t('surface.aboutOperatingModel') }}</p>
          <h2 class="mt-3 max-w-md text-3xl font-semibold leading-tight sm:text-4xl">{{ t('about.howTitle') }}</h2>
          <p class="mt-4 max-w-sm text-sm leading-6 text-muted">{{ t('about.howBody') }}</p>
        </div>

        <div class="about-principles">
          <section
            v-for="(item, i) in principles"
            :key="item.n"
            v-reveal="i * 80"
            class="about-principle"
          >
            <span>{{ item.n }}</span>
            <div>
              <h3>{{ t(`about.${item.key}`) }}</h3>
              <p>{{ t(`about.${item.body}`) }}</p>
            </div>
          </section>
        </div>
      </div>
    </section>

    <section class="about-band border-y border-line">
      <div class="mx-auto grid max-w-7xl gap-8 px-4 py-14 sm:px-6 lg:grid-cols-[1fr_1fr] lg:items-center lg:px-8">
        <div v-reveal>
          <h2 class="text-3xl font-semibold leading-tight">{{ t('landing.ctaTitle') }}</h2>
          <p class="mt-4 max-w-xl text-sm leading-6 text-muted">{{ t('landing.ctaSub') }}</p>
        </div>
        <div v-reveal="80" class="grid gap-px overflow-hidden rounded-lg border border-line bg-line sm:grid-cols-3">
          <div v-for="s in signals" :key="s.label" class="bg-panel p-5">
            <p class="text-xs uppercase text-muted">{{ s.label }}</p>
            <p class="mt-2 text-lg font-semibold">{{ s.value }}</p>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>
