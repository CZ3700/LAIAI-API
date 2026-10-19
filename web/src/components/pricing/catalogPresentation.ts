import { genericPriceFields, isOfficialVideoModel, type PricingCatalog, type PricingModel, type PricingStatus } from './types'

export type ModelTypeFilter = '' | 'text' | 'image' | 'video' | 'audio' | 'other'
export const modelTypes: Exclude<ModelTypeFilter, ''>[] = ['text', 'image', 'video', 'audio', 'other']
export const pricingStatuses: PricingStatus[] = ['published', 'partial', 'missing', 'blocked', 'disabled']
export const fieldUnits: Record<string, string> = {
  input_usd_per_1m: 'USD/1M_tokens', output_usd_per_1m: 'USD/1M_tokens', cached_input_usd_per_1m: 'USD/1M_tokens',
  image_usd: 'USD/image', per_call_usd: 'USD/call', tts_usd_per_1k_chars: 'USD/1K_characters',
  transcription_usd_per_call: 'USD/call', audio_input_usd_per_1m: 'USD/1M_audio_tokens', audio_output_usd_per_1m: 'USD/1M_audio_tokens',
}
export const unitKeys: Record<string, string> = {
  'USD/1M_tokens': 'unitTokens', 'USD/image': 'unitImage', 'USD/call': 'unitCall',
  'USD/1K_characters': 'unitCharacters', 'USD/1M_audio_tokens': 'unitAudio', 'USD/1M_video_tokens': 'unitVideo', mixed: 'unitMixed', unknown: 'unitUnknown',
}
export function modelType(row: PricingModel): Exclude<ModelTypeFilter, ''> {
  const kind = row.pricing?.billing_kind ?? row.pricing_kind
  if (kind === 'video' || kind === 'generic_video' || row.video_model) return 'video'
  if (kind === 'image') return 'image'
  if (kind === 'audio') return 'audio'
  if (kind === 'token' && ['embedding', 'embeddings'].includes(row.meta?.category ?? '')) return 'other'
  if (kind === 'token') return 'text'
  return 'other'
}
export function pricingStatus(row: PricingModel, catalog: PricingCatalog | null): PricingStatus {
  const channel = catalog?.channels.find(channel => channel.id === row.channel_id)
  if (channel && channel.status !== 1) return 'disabled'
  if (row.pricing) return row.pricing.status
  if (row.unsupported_reason) return 'blocked'
  // A legacy generic price is editable, but never infer video saleability from a configuration alone.
  if (row.video_model || row.pricing_kind === 'video') return 'missing'
  return row.generic_price ? 'published' : 'missing'
}
export function officialPriceEvidence(row: PricingModel | null) {
  const evidence = row?.official_price_evidence
  return evidence?.status === 'verified' && evidence.unit === 'CNY/1M_tokens' && ['seedance-2.0-fast', 'seedance-2.0-mini'].includes(evidence.model) ? evidence : null
}
// A reference-only row has no executable model configuration. Evidence remains
// visible after configuration without replacing its published USD price/editor.
export function configuredOfficialVideo(row: PricingModel | null) {
  return !!row && isOfficialVideoModel(row.video_model) && !!row.video_configuration && row.video_configuration.channel_id === row.channel_id && !row.unsupported_reason
}
export function evidenceOnlyVariant(row: PricingModel | null) { return !!officialPriceEvidence(row) && !configuredOfficialVideo(row) }
export function displayRates(row: PricingModel) {
  if (row.pricing) return row.pricing.rates
  if (!row.generic_price || row.video_model || row.pricing_kind === 'video') return []
  const keys = row.pricing_kind === 'token' ? ['input_usd_per_1m', 'output_usd_per_1m']
    : row.pricing_kind === 'image' ? ['image_usd']
      : row.pricing_kind === 'per_call' ? ['per_call_usd'] : genericPriceFields
  return keys.flatMap(key => {
    const value = row.generic_price?.[key as keyof typeof row.generic_price]
    return value === undefined || value === null || value === '' ? [] : [{ key, amount: value, unit: fieldUnits[key] ?? 'unknown' }]
  })
}

// Compare positive decimal strings without rounding prices through Number.
function decimalCompare(left: string, right: string): number {
  const [li = '0', lf = ''] = left.split('.'), [ri = '0', rf = ''] = right.split('.')
  const a = li.replace(/^0+(?=\d)/, ''), b = ri.replace(/^0+(?=\d)/, '')
  if (a.length !== b.length) return a.length - b.length
  if (a !== b) return a < b ? -1 : 1
  const size = Math.max(lf.length, rf.length)
  return lf.padEnd(size, '0').localeCompare(rf.padEnd(size, '0'))
}
export function videoPriceRange(row: PricingModel) {
  const values = row.pricing?.combinations.filter(cell => cell.mode === 'generate' && cell.service_tier === 'default' && !cell.draft).map(cell => cell.amount).filter(value => /^(0|[1-9]\d*)(\.\d+)?$/.test(value)).sort(decimalCompare) ?? []
  if (!values.length) return null
  return { min: values[0]!, max: values[values.length - 1]!, count: values.length }
}
