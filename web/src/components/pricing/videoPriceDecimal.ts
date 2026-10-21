// Rates are DECIMAL(24,12). Keep all arithmetic in scaled integers; displayed
// bulk changes are rounded half-up to 12 places before the user applies them.
const scale = 10n ** 12n
// Same upper bound as videobilling.validMoney; rate precision remains 12 places.
const maximum = 999999999999999999990000n
function scaled(value: string, signed = false): bigint {
  const expression = signed ? /^-?(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$/ : /^(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$/
  if (!expression.test(value)) throw new Error('invalid_decimal')
  const negative = value.startsWith('-')
  const [whole, fraction = ''] = (negative ? value.slice(1) : value).split('.')
  const amount = BigInt(whole!) * scale + BigInt(fraction.padEnd(12, '0'))
  return negative ? -amount : amount
}
function formatted(amount: bigint): string {
  if (amount <= 0n || amount > maximum) throw new Error('invalid_decimal')
  const fraction = (amount % scale).toString().padStart(12, '0').replace(/0+$/, '')
  return `${amount / scale}${fraction ? `.${fraction}` : ''}`
}
export function exactRate(value: string): string { return formatted(scaled(value.trim())) }
export function convertOfficialRate(cny: string, usdPerCny: string): string {
  const cost = scaled(cny.trim()), fx = scaled(usdPerCny.trim())
  if (cost <= 0n || fx <= 0n) throw new Error('invalid_decimal')
  return formatted((cost * fx + scale / 2n) / scale)
}
export function adjustRate(value: string, percentage: string): string {
  const rate = scaled(value.trim()), multiplier = 100n * scale + scaled(percentage.trim(), true)
  if (rate <= 0n || multiplier <= 0n) throw new Error('invalid_decimal')
  const divisor = 100n * scale
  return formatted((rate * multiplier + divisor / 2n) / divisor)
}
