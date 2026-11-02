import { addMoney, multiplyMoney, negateMoney, roundMoney } from '../../lib/decimalMoney'
import type { Billing } from './types'

export interface InitialChargeExplanation {
  calculatedUsd: string
  absorbedUsd: string
  source: 'settlement' | 'snapshot_recalculation'
}

// The persisted settlement wins. Until the API exposing it reaches production,
// reconstruct only unadjusted bills with matching confirmed provider usage.
export function initialChargeExplanation(billing: Billing | null | undefined): InitialChargeExplanation | null {
  if (!billing || billing.settled_amount == null) return null
  if (billing.initial_calculated_amount != null && billing.initial_absorbed_amount != null) {
    return { calculatedUsd: billing.initial_calculated_amount, absorbedUsd: billing.initial_absorbed_amount, source: 'settlement' }
  }
  if (billing.net_charged_amount !== billing.settled_amount || billing.entries.some(entry => entry.type.startsWith('adjustment_'))) return null
  const tokens = billing.actual_usage?.completion_tokens
  const capture = billing.entries.filter(entry => entry.type === 'capture')
  if (tokens == null || !Number.isSafeInteger(tokens) || tokens < 0 || tokens !== billing.billable_tokens || capture.length !== 1 || capture[0]!.amount !== billing.settled_amount || !billing.unit_price_usd_per_1m) return null
  const unrounded = multiplyMoney(tokens, billing.unit_price_usd_per_1m, 6)
  const calculatedUsd = roundMoney(unrounded, 8)
  if (!calculatedUsd) return null
  const difference = addMoney(calculatedUsd, negateMoney(capture[0]!.amount))
  if (!difference || difference.startsWith('-')) return null
  const absorbedUsd = roundMoney(difference, 8)
  return absorbedUsd ? { calculatedUsd, absorbedUsd, source: 'snapshot_recalculation' } : null
}

export function hasAbsorbedOverage(value: InitialChargeExplanation | null): boolean {
  return !!value && !/^0(?:\.0+)?$/.test(value.absorbedUsd)
}
