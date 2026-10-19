import type { LocationQuery, LocationQueryRaw } from 'vue-router'
import type { PricingModel } from './types'

type LegacyModel = Pick<PricingModel, 'key' | 'model' | 'video_model' | 'channel_id' | 'video_configuration'>
const standardModels = new Set(['seedance-2.0', 'seedance-2.5'])

// A legacy link names a public video model, not a catalog row. Resolve it from
// authorized catalog data before constructing a new deep link; never invent IDs.
export function legacyVideoPricingTarget(query: LocationQuery, models: LegacyModel[]) {
  const wanted = typeof query.model === 'string' && query.model.trim() ? query.model.trim() : query.model === undefined ? 'seedance-2.0' : ''
  const rawChannel = query.channel_id
  const requestedChannel = typeof rawChannel === 'string' && /^[1-9][0-9]*$/.test(rawChannel) && Number.isSafeInteger(Number(rawChannel)) ? Number(rawChannel) : null
  const hasChannel = rawChannel !== undefined
  const candidates = models.filter(row => standardModels.has(row.video_model ?? '')
    && (row.video_model === wanted || row.model === wanted)
    && (!hasChannel || row.channel_id === requestedChannel))
  const bound = candidates.filter(row => row.video_configuration?.channel_id === row.channel_id)
  const preferred = bound.length ? bound : candidates
  const exact = preferred.filter(row => row.model === wanted)
  const matches = exact.length ? exact : preferred
  const chosen = matches.length === 1 ? matches[0] : undefined
  const next: LocationQueryRaw = { ...query, tab: 'prices', pricing_type: 'video' }
  delete next.model; delete next.channel_id; delete next.pricing_model; delete next.pricing_legacy
  if (chosen) next.pricing_model = chosen.key
  else {
    next.pricing_search = wanted
    next.pricing_legacy = matches.length > 1 ? 'ambiguous' : 'missing'
  }
  return { path: '/app/admin', query: next }
}
