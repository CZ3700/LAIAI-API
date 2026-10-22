// Exact decimal arithmetic for presentation. No Number multiplication or rounding.
export type Amount = string | number
type Decimal = { n: bigint; scale: number }
function parse(v: Amount): Decimal | null {
  const s = String(v)
  if (s.length > 256 || !/^[+-]?\d+(\.\d+)?$/.test(s)) return null
  const [whole, fraction = ''] = s.replace(/^[+-]/, '').split('.')
  return { n: BigInt(whole + fraction) * (s.startsWith('-') ? -1n : 1n), scale: fraction.length }
}
function text(d: Decimal): string {
  const abs = (d.n < 0n ? -d.n : d.n).toString().padStart(d.scale + 1, '0')
  const value = d.scale ? `${abs.slice(0, -d.scale)}.${abs.slice(-d.scale)}`.replace(/\.?0+$/, '') : abs
  return `${d.n < 0n ? '-' : ''}${value || '0'}`
}
export function addMoney(...values: Amount[]): string {
  let out: Decimal = { n: 0n, scale: 0 }
  for (const value of values) {
    const d = parse(value); if (!d) return ''
    const scale = Math.max(out.scale, d.scale)
    out = { n: out.n * 10n ** BigInt(scale - out.scale) + d.n * 10n ** BigInt(scale - d.scale), scale }
  }
  return text(out)
}
export function negateMoney(value: Amount): string { const d = parse(value); return d ? text({ ...d, n: -d.n }) : '' }
export function multiplyMoney(a: Amount, b: Amount, dividePower = 0): string {
  const x = parse(a), y = parse(b)
  if (!x || !y || !Number.isInteger(dividePower) || dividePower < 0 || dividePower > 20) return ''
  return text({ n: x.n * y.n, scale: x.scale + y.scale + dividePower })
}
export function validRate(rate: string): boolean {
  if (!/^(0|[1-9]\d*)(\.\d{1,8})?$/.test(rate) || rate.length > 16) return false
  const d = parse(rate)!
  return d.n * 100n >= 10n ** BigInt(d.scale) && d.n <= 100n * 10n ** BigInt(d.scale)
}
export function roundMoney(value: Amount, precision: number): string | null {
  const d = parse(value)
  if (!d || !Number.isInteger(precision) || precision < 0 || precision > 12) return null
  const sign = d.n < 0n ? -1n : 1n
  let n = d.n * sign
  if (d.scale > precision) {
    const divisor = 10n ** BigInt(d.scale - precision)
    n = (n + divisor / 2n) / divisor // half away from zero
  } else n *= 10n ** BigInt(precision - d.scale)
  const abs = n.toString().padStart(precision + 1, '0')
  return `${n !== 0n && sign < 0n ? '-' : ''}${precision ? `${abs.slice(0, -precision)}.${abs.slice(-precision)}` : abs}`
}
export type Currency = 'USD' | 'CNY'
export interface MoneyOptions { currency?: Currency; rate?: string; precision?: number; signed?: boolean; ready?: boolean }
export function convertUsdToDisplay(value: Amount, currency: Currency = 'USD', rate = '7.3'): { amount: string; currency: Currency } {
  const d = parse(value)
  if (!d) return { amount: '', currency: 'USD' }
  if (currency === 'CNY' && validRate(rate)) return { amount: multiplyMoney(value, rate), currency }
  return { amount: text(d), currency: 'USD' }
}
export function formatNativeMoney(value: Amount | null | undefined, currency: string, precision = 2, signed = false): string {
  if (value == null) return '—'
  const rounded = roundMoney(value, precision)
  if (rounded === null) return '—'
  const symbol = currency === 'USD' ? '$' : currency === 'CNY' ? '¥' : `${currency} `
  const negative = rounded.startsWith('-')
  const abs = negative ? rounded.slice(1) : rounded
  const raw = parse(value)!
  // A positive sub-unit amount is never shown as a false zero.
  if (raw.n !== 0n && /^0(\.0+)?$/.test(abs)) {
    const threshold = precision ? `0.${'0'.repeat(precision - 1)}1` : '1'
    return `${raw.n < 0n ? '−' : signed ? '+' : ''}<${symbol}${threshold}`
  }
  return `${negative ? '−' : signed && raw.n > 0n ? '+' : ''}${symbol}${abs}`
}
export function formatDisplayMoney(value: Amount | null | undefined, options: MoneyOptions = {}): string {
  if (options.ready === false) return '…'
  if (value == null) return '—'
  const converted = convertUsdToDisplay(value, options.currency, options.rate)
  if (!converted.amount) return '—'
  const d = parse(converted.amount)!
  const abs = d.n < 0n ? -d.n : d.n
  const unit = 10n ** BigInt(d.scale)
  const precision = options.precision ?? (abs === 0n || abs >= unit ? 2 : abs * 10000n >= unit ? 4 : 6)
  return formatNativeMoney(converted.amount, converted.currency, precision, options.signed)
}
