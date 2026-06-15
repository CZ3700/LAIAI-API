<script setup lang="ts">
import { onMounted, onUnmounted, ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

interface Component {
  key: string
  status: 'operational' | 'degraded' | 'down'
}
interface StatusResp {
  status: 'operational' | 'degraded' | 'down'
  components: Component[]
  updated_at: string
}

const data = ref<StatusResp | null>(null)
const loading = ref(true)
const failed = ref(false)
let timer: number | undefined

async function load() {
  try {
    // 直接 fetch：状态页要能渲染 503(down) 响应体，不能像普通 api 那样抛错
    const resp = await fetch('/api/status')
    data.value = (await resp.json()) as StatusResp
    failed.value = false
  } catch {
    failed.value = true // 连接不上 = 整体不可用
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
  timer = window.setInterval(load, 30000) // 30s 自动刷新
})
onUnmounted(() => {
  if (timer) window.clearInterval(timer)
})

const overall = computed(() => (failed.value ? 'down' : data.value?.status ?? 'operational'))
const dot: Record<string, string> = {
  operational: 'bg-ok',
  degraded: 'bg-amber-500',
  down: 'bg-danger',
}
const text: Record<string, string> = {
  operational: 'text-ok',
  degraded: 'text-amber-500',
  down: 'text-danger',
}
function compLabel(key: string): string {
  const k = `status.comp_${key}`
  const v = t(k)
  return v === k ? key : v
}
</script>

<template>
  <div class="mx-auto max-w-3xl px-4 py-16">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('surface.statusEyebrow') }}</p>
        <h1 class="app-page-title">{{ t('status.title') }}</h1>
      </div>
    </div>

    <div v-if="loading" class="mt-8 text-muted">{{ t('common.loading') }}</div>

    <template v-else>
      <!-- 整体状态横幅 -->
      <div class="app-panel mt-6 flex items-center gap-3 px-5 py-4">
        <span class="relative flex h-3 w-3">
          <span
            class="absolute inline-flex h-full w-full animate-ping rounded-full opacity-60"
            :class="dot[overall]"
          />
          <span class="relative inline-flex h-3 w-3 rounded-full" :class="dot[overall]" />
        </span>
        <span class="text-lg font-medium" :class="text[overall]">
          {{ t(`status.overall_${overall}`) }}
        </span>
      </div>

      <!-- 组件列表 -->
      <div v-if="!failed && data" class="app-panel mt-6 divide-y divide-line">
        <div
          v-for="c in data.components"
          :key="c.key"
          class="flex items-center justify-between px-5 py-3.5"
        >
          <span class="text-sm text-ink">{{ compLabel(c.key) }}</span>
          <span class="flex items-center gap-2 text-sm" :class="text[c.status]">
            <span class="h-2 w-2 rounded-full" :class="dot[c.status]" />
            {{ t(`status.${c.status}`) }}
          </span>
        </div>
      </div>

      <p v-if="!failed && data" class="mt-4 text-xs text-muted">
        {{ t('status.updated') }}: {{ data.updated_at.slice(0, 16).replace('T', ' ') }} UTC
      </p>
    </template>
  </div>
</template>
