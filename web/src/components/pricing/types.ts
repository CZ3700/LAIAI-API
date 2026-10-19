export const genericPriceFields = ['input_usd_per_1m', 'output_usd_per_1m', 'cached_input_usd_per_1m', 'image_usd', 'per_call_usd', 'tts_usd_per_1k_chars', 'transcription_usd_per_call', 'audio_input_usd_per_1m', 'audio_output_usd_per_1m'] as const
export type PriceField = typeof genericPriceFields[number]
export type GenericPrice = { model: string } & Record<PriceField, string>
export type PricingKind = 'token' | 'image' | 'per_call' | 'audio' | 'mixed' | 'video' | 'generic_video' | 'unknown'
export type PricingStatus = 'published' | 'partial' | 'missing' | 'blocked' | 'disabled'
export interface PricingDescriptor {
  billing_kind: PricingKind
  currency: 'USD'
  unit: string
  status: PricingStatus
  rates: { key: string; amount: string; unit: string }[]
  combinations: { key: string; mode: string; resolution: string; input_video: boolean; service_tier: string; draft: boolean; amount: string }[]
  endpoints: string[]
}
export interface ModelMeta { model: string; vendor: string; category: string; tags: string; description: string; context_length: number; icon: string }
export interface OfficialPriceEvidence {
  status: 'verified'; model: 'seedance-2.0-fast' | 'seedance-2.0-mini'; verified_at: string; unit: 'CNY/1M_tokens'
  prices: { resolution: '480p' | '720p'; input_video: boolean; supplier_cny_per_1m: string }[]
  sources: { id: string; url: string; sha256: string }[]
}
export interface PricingModel {
  key: string; provider: string; channel_id: number; model: string; upstream_model: string
  pricing_kind: PricingKind
  pricing?: PricingDescriptor
  official_price_evidence?: OfficialPriceEvidence
  recognition_source: string; billing_modes: string[]; generic_price: GenericPrice | null; meta: ModelMeta | null
  video_model: string | null; video_configuration: { channel_id: number; provider_scope_id: number; config_version: string; pricing_version: string; enabled: boolean } | null
  unsupported_reason: string
}
export interface PricingCatalog {
  providers: { name: string; display_name: string }[]
  channels: { id: number; name: string; provider: string; status: number }[]
  models: PricingModel[]; permissions: { video_details: boolean }; price_scope: 'public_model_global'
}
export interface PriceCell { mode: string; resolution: string; input_video: boolean; service_tier: string; draft: boolean; enabled: boolean; retail_usd_per_1m: string; supplier_cny_per_1m: string; risk_multiplier: string; minimum_rule_version: string; available: boolean; unavailable_reason?: string }
export interface SourceRow { source_id: string; row: number }
export interface ReferenceSource { id: string; table_id: string; view_id: string; source_url: string; captured_at: string; raw_sha256: string; headers: string[]; columns: { semantic: string; unit: string; precision: string }[]; raw_rows: string[][] }
export interface ReferenceCandidates {
  model: string; catalog_version: string; manifest_sha256: string; usage: string
  config: { channel_id: number; provider_scope_id: number; config_version: string; pricing_version: string; capability_version: string; enabled: boolean } | null
  prices: (PriceCell & { key: string; unit: string; precision: string; configured: boolean; source_rows: SourceRow[] })[]
  minimum: { precision: string; purpose: string; status: string; cells: { resolution: string; ratio: string; output_duration_ms: number; fps: number | null; minimum_tokens: number; source_rows: SourceRow[] }[]; issues: string[] }
  sources: ReferenceSource[]
}

// The existing save API replaces every price column. Copy all fields even when
// the current modality hides them; never round monetary strings in the browser.
export function genericDraft(row: PricingModel): GenericPrice {
  const draft = { model: row.model } as GenericPrice
  for (const field of genericPriceFields) draft[field] = row.generic_price?.[field] ?? ''
  return draft
}
export function cellKey(cell: Pick<PriceCell, 'mode' | 'resolution' | 'input_video' | 'service_tier' | 'draft'>): string {
  return `${cell.mode}/${cell.resolution}/${cell.input_video}/${cell.service_tier}/${cell.draft}`
}
export function fillMissingOfficialCosts(cells: PriceCell[], candidates: ReferenceCandidates): number {
  let count = 0
  if (!['seedance-2.0', 'seedance-2.5'].includes(candidates.model)) return count
  for (const cell of cells) {
    if (cell.supplier_cny_per_1m?.trim() || cell.mode !== 'generate' || cell.service_tier !== 'default' || cell.draft) continue
    const matches = candidates.prices.filter(c => cellKey(c) === cellKey(cell) && c.unit === 'CNY/1M_tokens' && c.precision === 'exact')
    if (matches.length !== 1) continue
    const cost = matches[0]!.supplier_cny_per_1m
    if (!/^(0|[1-9][0-9]*)(\.[0-9]+)?$/.test(cost) || !/[1-9]/.test(cost)) continue
    cell.supplier_cny_per_1m = cost
    count++
  }
  return count
}
