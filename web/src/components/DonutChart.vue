<script setup lang="ts">
// 轻量环形图（零依赖）：把 items 按 value 占比画成圆环段 + 右侧图例。
import { computed } from 'vue'

const props = defineProps<{
  items: { label: string; value: number; color: string }[]
  emptyText?: string
}>()

const R = 42
const C = 2 * Math.PI * R

const total = computed(() => props.items.reduce((s, i) => s + Math.max(0, i.value), 0))
const segments = computed(() => {
  let acc = 0
  return props.items
    .filter((i) => i.value > 0)
    .map((i) => {
      const frac = total.value ? i.value / total.value : 0
      const seg = { label: i.label, color: i.color, frac, dash: frac * C, offset: -acc * C }
      acc += frac
      return seg
    })
})
</script>

<template>
  <div class="flex items-center gap-5">
    <svg viewBox="0 0 100 100" class="h-36 w-36 shrink-0 -rotate-90">
      <circle cx="50" cy="50" :r="R" fill="none" stroke="rgb(var(--c-line))" stroke-width="12" />
      <circle
        v-for="(s, idx) in segments"
        :key="idx"
        cx="50"
        cy="50"
        :r="R"
        fill="none"
        :stroke="s.color"
        stroke-width="12"
        :stroke-dasharray="`${s.dash} ${C - s.dash}`"
        :stroke-dashoffset="s.offset"
      />
    </svg>
    <ul class="min-w-0 flex-1 space-y-1.5 text-sm">
      <li v-for="(s, idx) in segments" :key="idx" class="flex items-center gap-2">
        <span class="h-2.5 w-2.5 shrink-0 rounded-sm" :style="{ background: s.color }"></span>
        <span class="min-w-0 flex-1 truncate font-mono text-xs" :title="s.label">{{ s.label }}</span>
        <span class="shrink-0 font-mono text-xs text-muted">{{ (s.frac * 100).toFixed(0) }}%</span>
      </li>
      <li v-if="segments.length === 0" class="text-muted">{{ emptyText || '—' }}</li>
    </ul>
  </div>
</template>
