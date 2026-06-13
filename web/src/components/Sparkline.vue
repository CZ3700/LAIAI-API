<script setup lang="ts">
import { computed } from 'vue'

// 迷你趋势折线（纯 SVG，无依赖）。统计卡右下角点缀，体现趋势密度。
const props = withDefaults(
  defineProps<{ data: number[]; width?: number; height?: number; fill?: boolean }>(),
  { width: 96, height: 32, fill: true },
)

const path = computed(() => {
  const d = props.data
  if (d.length < 2) return { line: '', area: '' }
  const max = Math.max(...d)
  const min = Math.min(...d)
  const range = max - min || 1
  const stepX = props.width / (d.length - 1)
  const pts = d.map((v, i) => {
    const x = i * stepX
    const y = props.height - 2 - ((v - min) / range) * (props.height - 4)
    return [x, y] as const
  })
  const line = pts.map(([x, y], i) => `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`).join(' ')
  const area = `${line} L${props.width},${props.height} L0,${props.height} Z`
  return { line, area }
})
const gid = computed(() => 'spark-' + Math.round(props.data.reduce((a, b) => a + b, 0) * 7 + props.data.length))
</script>

<template>
  <svg :width="width" :height="height" :viewBox="`0 0 ${width} ${height}`" class="overflow-visible" aria-hidden="true">
    <defs>
      <linearGradient :id="gid" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stop-color="rgb(var(--c-accent))" stop-opacity="0.28" />
        <stop offset="100%" stop-color="rgb(var(--c-accent))" stop-opacity="0" />
      </linearGradient>
    </defs>
    <path v-if="fill && path.area" :d="path.area" :fill="`url(#${gid})`" />
    <path
      v-if="path.line"
      :d="path.line"
      fill="none"
      stroke="rgb(var(--c-accent))"
      stroke-width="1.6"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
  </svg>
</template>
