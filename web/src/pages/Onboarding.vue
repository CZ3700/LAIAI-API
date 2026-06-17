<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'

const { t } = useI18n()

const steps = [
  { id: 'key', title: 'stepKey', hint: 'stepKeyHint', to: '/app/keys', cta: 'openKeys' },
  { id: 'model', title: 'stepModel', hint: 'stepModelHint', to: '/app/models', cta: 'openModels' },
  { id: 'play', title: 'stepPlay', hint: 'stepPlayHint', to: '/app/playground', cta: 'openPlayground' },
  { id: 'logs', title: 'stepLogs', hint: 'stepLogsHint', to: '/app/logs', cta: 'openLogs' },
] as const

const paths = [
  { key: 'platform', title: 'pathPlatform', hint: 'pathPlatformHint', to: '/app/keys' },
  { key: 'provider', title: 'pathProvider', hint: 'pathProviderHint', to: '/app/admin' },
  { key: 'enterprise', title: 'pathEnterprise', hint: 'pathEnterpriseHint', to: '/trust' },
] as const

const evidence = [
  { title: 'evidenceHeaders', hint: 'evidenceHeadersHint' },
  { title: 'evidenceSpend', hint: 'evidenceSpendHint' },
  { title: 'evidenceSupport', hint: 'evidenceSupportHint' },
] as const

// 数据驱动进度：建了 Key → 第 1 步完成；产生过调用（usage 有记录）→ 视为已选模型+试过+有日志（后 3 步）。
// 故 0 Key=0/4，建 Key=1/4，首次调用成功=4/4。避免新用户看到误导性的「4/4 已就绪」。
const hasKey = ref(false)
const hasUsage = ref(false)
const doneCount = computed(() => (hasKey.value ? (hasUsage.value ? 4 : 1) : 0))
const stepDone = (id: string): boolean => {
  if (id === 'key') return hasKey.value
  return hasUsage.value // model / play / logs：有成功调用即达成
}
const progress = computed(() => doneCount.value)

onMounted(async () => {
  const [keys, usage] = await Promise.allSettled([
    api<{ keys: unknown[] }>('GET', '/api/keys'),
    api<{ total: number }>('GET', '/api/usage?page=1&page_size=1'),
  ])
  if (keys.status === 'fulfilled') hasKey.value = (keys.value.keys?.length ?? 0) > 0
  if (usage.status === 'fulfilled') hasUsage.value = (usage.value.total ?? 0) > 0
})
</script>

<template>
  <div class="app-page onboarding-page">
    <section class="console-hero p-5 sm:p-6">
      <div class="grid gap-6 xl:grid-cols-[minmax(0,1fr)_22rem] xl:items-end">
        <div>
          <p class="app-eyebrow">{{ t('surface.onboardingEyebrow') }}</p>
          <h1 class="app-page-title">{{ t('onboarding.title') }}</h1>
          <p class="app-page-subtitle">{{ t('onboarding.subtitle') }}</p>
        </div>
        <div class="soft-panel p-4">
          <div class="flex items-end justify-between gap-3">
            <div>
              <p class="text-xs text-muted">{{ t('onboarding.progress') }}</p>
              <p class="mt-1 font-mono text-3xl font-semibold">{{ progress }}/{{ steps.length }}</p>
            </div>
            <span v-if="progress >= steps.length" class="badge-ok">{{ t('onboarding.ready') }}</span>
            <span v-else class="badge-muted">{{ t('onboarding.inProgress') }}</span>
          </div>
          <div class="mt-4 grid grid-cols-4 gap-1">
            <span v-for="(step, i) in steps" :key="step.id" class="h-1.5 rounded-full" :class="i < progress ? 'bg-accent' : 'bg-line'"></span>
          </div>
        </div>
      </div>
    </section>

    <section class="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
      <router-link
        v-for="(step, index) in steps"
        :key="step.id"
        :to="step.to"
        class="app-panel group p-5 transition-colors hover:border-accent/70"
      >
        <div class="flex items-center justify-between">
          <span class="font-mono text-xs text-accent">0{{ index + 1 }}</span>
          <span v-if="stepDone(step.id)" class="badge-ok !py-0.5 text-[10px]">✓</span>
        </div>
        <h2 class="mt-3 font-semibold">{{ t(`onboarding.${step.title}`) }}</h2>
        <p class="mt-2 min-h-[3.75rem] text-sm leading-6 text-muted">{{ t(`onboarding.${step.hint}`) }}</p>
        <span class="mt-4 inline-flex text-sm font-medium text-accent group-hover:underline">
          {{ t(`onboarding.${step.cta}`) }}
        </span>
      </router-link>
    </section>

    <section class="grid gap-4 lg:grid-cols-[minmax(0,1fr)_24rem]">
      <div class="app-panel p-5">
        <h2 class="font-semibold">{{ t('devex.launchChecklist') }}</h2>
        <div class="mt-4 grid gap-px overflow-hidden rounded-lg border border-line bg-line md:grid-cols-3">
          <router-link
            v-for="path in paths"
            :key="path.key"
            :to="path.to"
            class="bg-panel p-4 transition-colors hover:bg-elev/70"
          >
            <p class="font-medium">{{ t(`onboarding.${path.title}`) }}</p>
            <p class="mt-2 text-xs leading-5 text-muted">{{ t(`onboarding.${path.hint}`) }}</p>
          </router-link>
        </div>
      </div>

      <aside class="app-panel p-5">
        <h2 class="font-semibold">{{ t('onboarding.evidenceTitle') }}</h2>
        <div class="mt-4 space-y-3">
          <div v-for="item in evidence" :key="item.title" class="soft-panel p-3">
            <p class="text-sm font-medium">{{ t(`onboarding.${item.title}`) }}</p>
            <p class="mt-1 text-xs leading-5 text-muted">{{ t(`onboarding.${item.hint}`) }}</p>
          </div>
        </div>
      </aside>
    </section>
  </div>
</template>
