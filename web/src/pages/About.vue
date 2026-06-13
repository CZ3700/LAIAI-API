<script setup lang="ts">
import { useI18n } from 'vue-i18n'
const { t } = useI18n()
</script>

<template>
  <div class="mx-auto max-w-3xl px-6 py-16">
    <h1 class="text-3xl font-bold tracking-tight">{{ t('about.title') }}</h1>
    <p class="mt-6 text-lg text-muted">{{ t('about.lead') }}</p>

    <div class="mt-10 space-y-8">
      <section>
        <h2 class="font-semibold">{{ t('about.missionTitle') }}</h2>
        <p class="mt-2 text-muted">{{ t('about.missionBody') }}</p>
      </section>
      <section>
        <h2 class="font-semibold">{{ t('about.howTitle') }}</h2>
        <p class="mt-2 text-muted">{{ t('about.howBody') }}</p>
      </section>
      <section>
        <h2 class="font-semibold">{{ t('about.contactTitle') }}</h2>
        <p class="mt-2 text-muted">{{ t('about.contactBody') }}</p>
      </section>
    </div>
  </div>
</template>
