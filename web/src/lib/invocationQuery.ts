// Shared v1 filter vocabulary. Dates are calendar values; the API resolves the
// chosen IANA timezone. No credential or arbitrary URL parameter enters this DTO.
export const invocationTimezones = ['UTC', 'Asia/Shanghai', 'America/New_York'] as const
export const invocationModalities = ['chat', 'embeddings', 'rerank', 'image', 'audio_speech', 'audio_transcription', 'realtime', 'video', 'music'] as const
export const invocationStatuses = ['success', 'upstream_error', 'no_channel', 'insufficient_balance'] as const
export type InvocationScope = 'org' | 'admin'
export type InvocationCurrency = 'USD' | 'CNY'
export type InvocationTimezone = typeof invocationTimezones[number]
export type InvocationRange = '7d' | '30d' | 'month' | 'prev' | 'custom'
export type InvocationPageSize = 20 | 50 | 100
export interface InvocationFilterDraft {
  range: InvocationRange
  timezone: InvocationTimezone
  start: string
  end: string // Inclusive date in the editor; never an ISO instant.
  model: string
  requestId: string
  apiKeyId: string
  orgId: string
  channelId: string
  modality: 'all' | 'historical' | typeof invocationModalities[number]
  result: 'all' | 'success' | 'failed'
  status: '' | typeof invocationStatuses[number]
  stream: 'all' | 'true' | 'false'
}
export interface InvocationFilterSpec {
  readonly scope: InvocationScope
  readonly timezone: InvocationTimezone
  readonly since: string
  readonly until: string // Exclusive calendar date.
  readonly model?: string
  readonly request_id?: string
  readonly api_key_id?: string
  readonly org_id?: string
  readonly channel_id?: string
  readonly modality?: string // Missing means all; empty means historical.
  readonly result?: 'success' | 'failed'
  readonly status?: typeof invocationStatuses[number]
  readonly is_stream?: boolean
}
export class InvocationFilterError extends Error {
  constructor(public field: string, public reason: string) {
    super('Invalid invocation filter')
  }
}
function invalid(field: string, reason: string): never { throw new InvocationFilterError(field, reason) }
export function defaultInvocationDraft(): InvocationFilterDraft {
  return { range: '7d', timezone: 'UTC', start: '', end: '', model: '', requestId: '', apiKeyId: '',
    orgId: '', channelId: '', modality: 'all', result: 'all', status: '', stream: 'all' }
}
function calendarDay(value: string, field: string): Date {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return invalid(field, 'invalid_date')
  const date = new Date(`${value}T00:00:00Z`)
  if (!Number.isFinite(date.getTime()) || date.getUTCFullYear() < 1000 || date.toISOString().slice(0, 10) !== value) return invalid(field, 'invalid_date')
  return date
}
function moveDay(value: string, count: number): string {
  const date = calendarDay(value, 'range')
  date.setUTCDate(date.getUTCDate() + count)
  if (date.getUTCFullYear() < 1000 || date.getUTCFullYear() > 9999) return invalid('range', 'invalid_date')
  return date.toISOString().slice(0, 10)
}
function todayInZone(now: Date, timezone: InvocationTimezone): string {
  if (!Number.isFinite(now.getTime())) return invalid('range', 'invalid_date')
  const parts = new Intl.DateTimeFormat('en-US', { timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(now)
  const part = (type: string) => parts.find(p => p.type === type)?.value ?? ''
  return `${part('year')}-${part('month')}-${part('day')}`
}
export function invocationDateWindow(draft: InvocationFilterDraft, now = new Date()): { since: string; until: string } {
  if (!invocationTimezones.includes(draft.timezone)) return invalid('timezone', 'invalid_timezone')
  const today = todayInZone(now, draft.timezone)
  const monthStart = `${today.slice(0, 7)}-01`
  let since: string, until: string
  switch (draft.range) {
    case '7d': since = moveDay(today, -6); until = moveDay(today, 1); break
    case '30d': since = moveDay(today, -29); until = moveDay(today, 1); break
    case 'month': since = monthStart; until = moveDay(today, 1); break
    case 'prev': since = `${moveDay(monthStart, -1).slice(0, 7)}-01`; until = monthStart; break
    case 'custom':
      calendarDay(draft.start, 'start'); calendarDay(draft.end, 'end')
      since = draft.start; until = moveDay(draft.end, 1); break
    default: return invalid('range', 'invalid_range')
  }
  const days = (calendarDay(until, 'end').getTime() - calendarDay(since, 'start').getTime()) / 86_400_000
  if (days < 1 || days > 90) return invalid('range', 'invalid_range')
  return { since, until }
}
function optionalText(raw: string, field: string, limit: number): string | undefined {
  const value = raw.trim()
  if (!value) return undefined
  if ([...value].length > limit || /[\p{Cc}\p{Cs}]/u.test(value)) return invalid(field, 'invalid_text')
  return value
}
function optionalID(raw: string, field: string): string | undefined {
  const value = raw.trim()
  if (!value) return undefined
  if (!/^\d{1,20}$/.test(value)) return invalid(field, 'positive_integer_required')
  const id = BigInt(value)
  if (id <= 0n || id > 18446744073709551615n) return invalid(field, 'positive_integer_required')
  return id.toString()
}
export function buildInvocationFilter(draft: InvocationFilterDraft, scope: InvocationScope, now = new Date()): Readonly<InvocationFilterSpec> {
  if (scope !== 'org' && scope !== 'admin') return invalid('scope', 'invalid_scope')
  if (scope === 'org' && (draft.orgId.trim() || draft.channelId.trim())) return invalid('scope', 'admin_filter_forbidden')
  const out: { -readonly [K in keyof InvocationFilterSpec]: InvocationFilterSpec[K] } = { scope, timezone: draft.timezone, ...invocationDateWindow(draft, now),
    model: optionalText(draft.model, 'model', 128), request_id: optionalText(draft.requestId, 'request_id', 32),
    api_key_id: optionalID(draft.apiKeyId, 'api_key_id'),
    ...(scope === 'admin' ? { org_id: optionalID(draft.orgId, 'org_id'), channel_id: optionalID(draft.channelId, 'channel_id') } : {}) }
  if (draft.modality === 'historical') out.modality = ''
  else if (draft.modality !== 'all') {
    if (!invocationModalities.includes(draft.modality)) return invalid('modality', 'unsupported_value')
    out.modality = draft.modality
  }
  if (!['all', 'success', 'failed'].includes(draft.result)) return invalid('result', 'unsupported_value')
  if (draft.status) {
    if (!invocationStatuses.includes(draft.status)) return invalid('status', 'unsupported_value')
    if (draft.result !== 'all') return invalid('status', 'conflicting_parameters')
    out.status = draft.status
  } else if (draft.result !== 'all') out.result = draft.result
  if (!['all', 'true', 'false'].includes(draft.stream)) return invalid('is_stream', 'boolean_required')
  if (draft.stream !== 'all') out.is_stream = draft.stream === 'true'
  return Object.freeze(out)
}
export function invocationQueryParams(filter: InvocationFilterSpec, currency: InvocationCurrency, pagination?: { page: number; pageSize: InvocationPageSize }): URLSearchParams {
  if (currency !== 'USD' && currency !== 'CNY') return invalid('display_currency', 'unsupported_value')
  // Serialized fields are explicit: even a forged DTO cannot inject other API parameters.
  if (filter.scope !== 'org' && filter.scope !== 'admin') return invalid('scope', 'invalid_scope')
  if (filter.scope === 'org' && (filter.org_id !== undefined || filter.channel_id !== undefined)) return invalid('scope', 'admin_filter_forbidden')
  const values = new URLSearchParams({ filter_version: '1', source: 'invocation_log', time_basis: 'recorded_at',
    timezone: filter.timezone, since: filter.since, until: filter.until, display_currency: currency })
  for (const key of ['model', 'request_id', 'api_key_id', 'modality', 'result', 'status', 'is_stream'] as const) {
    if (filter[key] !== undefined) values.set(key, String(filter[key]))
  }
  if (filter.scope === 'admin') for (const key of ['org_id', 'channel_id'] as const) {
    if (filter[key] !== undefined) values.set(key, filter[key])
  }
  if (pagination) {
    if (![20, 50, 100].includes(pagination.pageSize) || !Number.isSafeInteger(pagination.page) || pagination.page < 1 || (pagination.page - 1) * pagination.pageSize > 100000) return invalid('page', 'invalid_page')
    values.set('page', String(pagination.page)); values.set('page_size', String(pagination.pageSize))
  }
  return values
}
export function invocationExactInteger(value: unknown): string {
  if (typeof value !== 'string' || value.length > 256 || !/^-?(0|[1-9]\d*)$/.test(value)) return invalid('usage_exact', 'invalid_integer')
  return BigInt(value).toString()
}
export function formatInvocationTokens(value: unknown): string {
  return invocationExactInteger(value).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}
export function invocationTotalTokens(input: unknown, output: unknown): string {
  return (BigInt(invocationExactInteger(input)) + BigInt(invocationExactInteger(output))).toString()
}
