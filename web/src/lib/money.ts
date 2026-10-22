import { getActivePinia } from 'pinia'
import { useCurrency } from '@/stores/currency'
import { formatDisplayMoney, type Amount, type MoneyOptions } from './decimalMoney'
export { addMoney, negateMoney, multiplyMoney, convertUsdToDisplay, formatNativeMoney } from './decimalMoney'

export function displayCurrencyCode(): 'USD' | 'CNY' { return getActivePinia() ? useCurrency().effectiveCurrency : 'USD' }
export function displayCurrencySymbol(): string { return displayCurrencyCode() === 'CNY' ? '¥' : '$' }
export function formatUsdForDisplay(value: Amount | null | undefined, options: MoneyOptions = {}): string {
  const store = getActivePinia() ? useCurrency() : null
  return formatDisplayMoney(value, { currency: store?.effectiveCurrency, rate: store?.cnyPerUsd, ready: store?.ready ?? true, ...options })
}
// Compatibility name: the argument is always a raw USD value, never formatted text.
export const fmtUsd = formatUsdForDisplay
