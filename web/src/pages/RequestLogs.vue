<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import InvocationLogPanel from '@/components/invocation/InvocationLogPanel.vue'
import SeedanceRequestLogs from '@/components/seedance/SeedanceRequestLogs.vue'
import { useSeedanceI18n } from '@/components/seedance/messages'
const { t } = useI18n()
useSeedanceI18n()
const route = useRoute(), router = useRouter()
const seedanceTab = computed(() => route.query.tab === 'seedance')
function switchTab(seedance: boolean) { void router.replace({ query: seedance ? { tab: 'seedance' } : {} }) }
</script>

<template>
  <div class="app-page">
    <div class="app-page-header"><h1 class="app-page-title">{{ t('reqlog.title') }}</h1></div>
    <div class="segmented-control mb-4" role="tablist"><button type="button" role="tab" :aria-selected="!seedanceTab" :class="{ active: !seedanceTab }" @click="switchTab(false)">{{ t('seedance.general') }}</button><button type="button" role="tab" :aria-selected="seedanceTab" :class="{ active: seedanceTab }" @click="switchTab(true)">{{ t('seedance.tab') }}</button></div>
    <SeedanceRequestLogs v-if="seedanceTab" />
    <InvocationLogPanel v-else scope="org" />
  </div>
</template>
