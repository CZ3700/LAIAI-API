<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

export interface ModelDailyRow {
  day: string
  model: string
  calls: number
  cost_usd: string
}

const props = withDefaults(defineProps<{ rows: ModelDailyRow[]; days: number; metric?: 'calls' | 'cost' }>(), {
  metric: 'calls',
})
const { t } = useI18n()

// 与全站 --chart-* / 指标卡芯片同源的品牌分类色板（明暗主题各自校准）；超出 top-N 归入「其它」中性灰。
const PALETTE = ['var(--chart-1)', 'var(--chart-2)', 'var(--chart-3)', 'var(--chart-4)', 'var(--chart-5)', 'var(--chart-6)', 'var(--chart-7)', 'var(--chart-8)']
const OTHER = 'var(--c-muted)'

const val = (r: ModelDailyRow) => (props.metric === 'cost' ? Number(r.cost_usd) : r.calls)

const data = computed(() => {
  // 各模型总量 → 取 top 8 给独立色
  const totals = new Map<string, number>()
  for (const r of props.rows) totals.set(r.model, (totals.get(r.model) ?? 0) + val(r))
  const topModels = [...totals.entries()].sort((a, b) => b[1] - a[1]).slice(0, 8).map((e) => e[0])
  const colorOf = new Map(topModels.map((m, i) => [m, PALETTE[i % PALETTE.length]]))

  const byDay = new Map<string, ModelDailyRow[]>()
  for (const r of props.rows) {
    const a = byDay.get(r.day) ?? []
    a.push(r)
    byDay.set(r.day, a)
  }
  // UTC 自然日键，与后端 by-model-daily 的 DATE(created_at) UTC 桶对齐（纯 UTC 毫秒整日位移，避免本地时区日界错位）
  const todayMs = Date.now()
  const days: { label: string; total: number; segs: { model: string; value: number; color: string }[] }[] = []
  for (let i = props.days - 1; i >= 0; i--) {
    const key = new Date(todayMs - i * 86400_000).toISOString().slice(0, 10)
    const segMap = new Map<string, number>()
    for (const r of byDay.get(key) ?? []) {
      const m = colorOf.has(r.model) ? r.model : '__other'
      segMap.set(m, (segMap.get(m) ?? 0) + val(r))
    }
    const segs = [...segMap.entries()]
      .sort((a, b) => b[1] - a[1])
      .map(([m, v]) => ({
        model: m === '__other' ? t('dashboard.otherModels') : m,
        value: v,
        color: m === '__other' ? OTHER : colorOf.get(m) ?? OTHER,
      }))
    days.push({ label: key.slice(5), total: segs.reduce((s, x) => s + x.value, 0), segs })
  }
  const max = Math.max(Number.EPSILON, ...days.map((d) => d.total))
  const legend = topModels.map((m, i) => ({ model: m, color: PALETTE[i % PALETTE.length] }))
  return { days, max, legend }
})

function fmt(v: number): string {
  return props.metric === 'cost' ? '$' + v.toFixed(4) : v.toLocaleString()
}
</script>

<template>
  <div class="card p-4">
    <div class="flex h-32 items-end gap-1" role="img" :aria-label="t('usage.chartAlt')">
      <div
        v-for="dy in data.days"
        :key="dy.label"
        class="group relative flex min-h-[2px] flex-1 flex-col"
        :style="{ height: `${(dy.total / data.max) * 100}%` }"
        :title="`${dy.label} · ${fmt(dy.total)}`"
      >
        <div
          v-for="(s, si) in dy.segs"
          :key="si"
          class="first:rounded-t"
          :style="{ height: `${dy.total ? (s.value / dy.total) * 100 : 0}%`, background: `rgb(${s.color})`, opacity: 0.85 }"
          :title="`${s.model}: ${fmt(s.value)}`"
        ></div>
      </div>
    </div>
    <div class="mt-2 flex justify-between font-mono text-[10px] text-muted">
      <span>{{ data.days[0]?.label }}</span>
      <span>{{ data.days[data.days.length - 1]?.label }}</span>
    </div>
    <div class="mt-3 flex flex-wrap gap-x-3 gap-y-1">
      <span v-for="l in data.legend" :key="l.model" class="inline-flex items-center gap-1 text-[11px] text-muted">
        <span class="h-2 w-2 rounded-full" :style="{ background: `rgb(${l.color})` }"></span>{{ l.model }}
      </span>
    </div>
  </div>
</template>
