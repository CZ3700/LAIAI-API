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

export type ChartMetric = 'calls' | 'tokens' | 'cost' | 'success'

// creditsFx：传入汇率则成本 tooltip 以 credits 展示（credits = USD × fx × 1e6），与 credits 主题一致；
// 不传则回退美元（$），保持其它调用方行为不变。
const props = withDefaults(defineProps<{ items: SummaryDay[]; days: number; metric?: ChartMetric; creditsFx?: number }>(), {
  metric: 'calls',
})
const { t } = useI18n()

// 每个指标一种色调（柱 + 渐变），让切换有清晰视觉区分
const METRIC_COLOR: Record<ChartMetric, string> = {
  calls: 'var(--c-accent)',
  tokens: 'var(--c-accent-2)',
  cost: 'var(--c-ok)',
  success: '245 158 11', // amber
}

interface Bar {
  label: string
  calls: number
  tokens: number
  cost: number
  success: number // 成功率 0..1
  value: number // 当前指标取值
}

// 补齐无调用的日期为 0，柱高按当期所选指标最大值归一
const chart = computed(() => {
  const byDay = new Map(props.items.map((s) => [s.day, s]))
  const out: Bar[] = []
  const today = new Date()
  for (let i = props.days - 1; i >= 0; i--) {
    const dt = new Date(today)
    dt.setDate(today.getDate() - i)
    const key = dt.toISOString().slice(0, 10)
    const s = byDay.get(key)
    const calls = s?.calls ?? 0
    const tokens = (s?.prompt_tokens ?? 0) + (s?.completion_tokens ?? 0)
    const cost = Number(s?.cost_usd ?? '0')
    const success = calls > 0 ? (s?.succeeded ?? 0) / calls : 0
    const value = props.metric === 'tokens' ? tokens : props.metric === 'cost' ? cost : props.metric === 'success' ? success : calls
    out.push({ label: key.slice(5), calls, tokens, cost, success, value })
  }
  const max = Math.max(props.metric === 'success' ? 1 : Number.EPSILON, ...out.map((x) => x.value))
  return { days: out, max }
})

const color = computed(() => METRIC_COLOR[props.metric])
function fmtTip(b: Bar): string {
  const costStr = props.creditsFx ? `${Math.round(b.cost * props.creditsFx * 1e6).toLocaleString()} cr` : `$${b.cost.toFixed(4)}`
  return `${b.label} · ${b.calls} calls · ${b.tokens.toLocaleString()} tok · ${costStr} · ${(b.success * 100).toFixed(0)}% ok`
}
</script>

<template>
  <div class="card p-4">
    <div class="flex h-28 items-end gap-1" role="img" :aria-label="t('usage.chartAlt')">
      <div
        v-for="dy in chart.days"
        :key="dy.label"
        class="group relative min-h-[2px] flex-1 rounded-t transition-opacity hover:opacity-100"
        :style="{
          height: `${(dy.value / chart.max) * 100}%`,
          opacity: 0.78,
          background: `linear-gradient(180deg, rgb(${color}), rgb(${color} / 0.55))`,
        }"
        :title="fmtTip(dy)"
      ></div>
    </div>
    <div class="mt-2 flex justify-between font-mono text-[10px] text-muted">
      <span>{{ chart.days[0]?.label }}</span>
      <span>{{ chart.days[chart.days.length - 1]?.label }}</span>
    </div>
  </div>
</template>
