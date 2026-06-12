<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

export interface SummaryDay {
  day: string
  calls: number
  succeeded: number
  prompt_tokens: number
  completion_tokens: number
  cost_usd: string
}

const props = defineProps<{ items: SummaryDay[]; days: number }>()
const { t } = useI18n()

// 补齐无调用的日期为 0，柱高按当期最大值归一
const chart = computed(() => {
  const byDay = new Map(props.items.map((s) => [s.day, s]))
  const out: { label: string; calls: number; cost: string }[] = []
  const today = new Date()
  for (let i = props.days - 1; i >= 0; i--) {
    const dt = new Date(today)
    dt.setDate(today.getDate() - i)
    const key = dt.toISOString().slice(0, 10)
    const s = byDay.get(key)
    out.push({ label: key.slice(5), calls: s?.calls ?? 0, cost: s?.cost_usd ?? '0' })
  }
  const max = Math.max(1, ...out.map((x) => x.calls))
  return { days: out, max }
})
</script>

<template>
  <div class="card p-4">
    <div class="flex h-28 items-end gap-1" role="img" :aria-label="t('usage.chartAlt')">
      <div
        v-for="dy in chart.days"
        :key="dy.label"
        class="group relative flex-1 rounded-t bg-accent/70 transition-colors hover:bg-accent min-h-[2px]"
        :style="{ height: `${(dy.calls / chart.max) * 100}%` }"
        :title="`${dy.label} · ${dy.calls} calls · $${dy.cost}`"
      ></div>
    </div>
    <div class="mt-2 flex justify-between text-[10px] text-muted font-mono">
      <span>{{ chart.days[0]?.label }}</span>
      <span>{{ chart.days[chart.days.length - 1]?.label }}</span>
    </div>
  </div>
</template>
