import { ref } from 'vue'
import { api } from './api'

// 资金统一以 credits 展示：1 credit = 0.000001 元（credits = USD × 汇率 × 100 万），并提供 ≈¥ 估算。
// 汇率（cny_per_usd）由 /api/config 暴露（运营可在管理后台热配），首次用时拉取并全局缓存。
const fxRate = ref(7.3)
let loaded = false

export async function ensureFx(): Promise<void> {
  if (loaded) return
  loaded = true
  try {
    const c = await api<{ cny_per_usd: string }>('GET', '/api/config', undefined, { auth: false })
    const r = Number(c.cny_per_usd)
    if (Number.isFinite(r) && r > 0) fxRate.value = r
  } catch {
    /* 保留默认 7.3 */
  }
}

function num(v: string | number): number {
  const out = Number(v)
  return Number.isFinite(out) ? out : 0
}

export function toCredits(usd: string | number): number {
  return Math.round(num(usd) * fxRate.value * 1_000_000)
}

export function toCny(usd: string | number): number {
  return num(usd) * fxRate.value
}

export { fxRate }
