// 计价/记账单位 = 美元（USD），全站统一（参考 OpenRouter）。法币支付在下单时按汇率折算为对应法币收款，
// 但入账永远是 USD（见后端 creditPaidOrder：Delta=order.AmountUSD）。前端一律以 $ 展示。
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
