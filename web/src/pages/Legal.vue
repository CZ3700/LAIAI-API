<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { api } from '@/lib/api'

const { t } = useI18n()
const route = useRoute()
const terms = ref('')
const privacy = ref('')
const loaded = ref(false)

const isPrivacy = computed(() => route.path === '/privacy')
const title = computed(() => (isPrivacy.value ? t('legal.privacy') : t('legal.terms')))
const body = computed(() => (isPrivacy.value ? privacy.value : terms.value))

onMounted(async () => {
  try {
    const cfg = await api<{ terms_of_service: string; privacy_policy: string }>('GET', '/api/config', undefined, { auth: false })
    terms.value = cfg.terms_of_service
    privacy.value = cfg.privacy_policy
  } catch {
    // 加载失败显示空态
  } finally {
    loaded.value = true
  }
})
</script>

<template>
  <div class="mx-auto max-w-4xl px-4 py-14 sm:px-6 lg:px-8">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('surface.legalEyebrow') }}</p>
        <h1 class="app-page-title">{{ title }}</h1>
      </div>
    </div>
    <div v-if="loaded" class="app-panel mt-8 p-6 sm:p-8">
      <p v-if="body" class="whitespace-pre-wrap text-sm leading-7 text-muted">{{ body }}</p>
      <p v-else class="text-sm text-muted">{{ t('legal.empty') }}</p>
    </div>
  </div>
</template>
