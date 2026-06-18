import { ref } from 'vue'
import { api } from './api'

// 计价/记账单位 = 美元（USD），全局统一（参考 OpenRouter）。法币支付在下单时按汇率折算为对应法币收款，
// 但入账永远是 USD（见后端 creditPaidOrder：Delta=order.AmountUSD）。前端一律以 $ 展示，可附 ≈¥ 辅助估算。
// 汇率（cny_per_usd）由 /api/config 暴露（运营在管理后台热配，与 epay 下单折算同一来源），首次用时拉取缓存。
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

// USD 金额展示：≥1 两位小数；(0,1) 四位小数（成本常很小，避免显示成 $0.00 丢失精度）；0 显示 $0.00。
export function fmtUsd(usd: string | number): string {
  const v = num(usd)
  if (v === 0) return '$0.00'
  return `$${v.toFixed(v >= 1 ? 2 : 4)}`
}

// ≈¥ 辅助估算（仅提示；与下单时 epay 折算同一汇率 cny_per_usd）。
export function fmtCny(usd: string | number): string {
  return `≈¥${(num(usd) * fxRate.value).toFixed(2)}`
}

export { fxRate }
