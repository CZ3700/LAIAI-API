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
  <div class="mx-auto max-w-3xl px-6 py-12">
    <h1 class="text-2xl font-semibold tracking-tight">{{ title }}</h1>
    <div v-if="loaded" class="mt-6">
      <p v-if="body" class="whitespace-pre-wrap text-sm leading-relaxed text-muted">{{ body }}</p>
      <p v-else class="text-sm text-muted">{{ t('legal.empty') }}</p>
    </div>
  </div>
</template>
