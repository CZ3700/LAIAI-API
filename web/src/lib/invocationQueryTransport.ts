import { apiFetch, apiResponseError, ApiError } from '@/lib/api'
import { validRate } from '@/lib/decimalMoney'
import { invocationExactInteger, invocationQueryParams, type InvocationCurrency, type InvocationFilterSpec, type InvocationPageSize, type InvocationScope } from '@/lib/invocationQuery'

export interface InvocationUsageExact {
  prompt_tokens: string; completion_tokens: string; cached_tokens: string; reasoning_tokens: string
}
export interface InvocationMoneySnapshot {
  currency: InvocationCurrency; cny_per_usd: string; rate_snapshot_at: string
  rate_source: 'setting' | 'default_missing' | 'default_empty'
}
export interface InvocationRow {
  id: number; created_at: string; request_id: string; api_key_id: number; model: string; modality: string
  status: string; status_code: number; is_stream: boolean; cost_usd: string; usage_exact: InvocationUsageExact
  frt_ms: number; duration_ms: number; usage_source?: string; error_brief?: string
  finish_reason?: string; protocol?: string; upstream_error_code?: string; upstream_request_id?: string
  org_id?: number; user_id?: number | null; channel_id?: number; provider?: string; attempts?: number
}
export interface InvocationTotals extends InvocationUsageExact {
  record_count: number; success_count: number; non_success_count: number
  cost_usd: string; cost_display: string; usage_presence: 'legacy_not_distinguishable'; missing_usage_count: null
}
export interface InvocationQueryBundle {
  items: InvocationRow[]; total: number; page: number; page_size: InvocationPageSize; totals: InvocationTotals
  query: {
    filter_version: 1; normalized_filters: Record<string, string | number | boolean>
    filter_hash: string; as_of: string; data_watermark: { kind: 'observation'; recorded_through_id: null }
    consistency: 'response_snapshot'; cross_request_consistency: 'live'; data_source: 'invocation_log'
    amount_source: 'log_recorded_cost'; completeness: 'best_effort'; display_money: InvocationMoneySnapshot
  }
}
export interface InvocationExportResult {
  recordCount: number; asOf: string; filterHash: string; money: InvocationMoneySnapshot; costUSD: string; costDisplay: string
}
export interface InvocationExportDownload { blob: Blob; filename: string; metadata: InvocationExportResult }
export class InvocationContractError extends Error {
  readonly code = 'invocation_contract_unavailable'
  constructor() { super('Invocation response unavailable') }
}
function invalid(): never { throw new InvocationContractError() }
function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return invalid()
  return value as Record<string, unknown>
}
function text(value: unknown, limit = 4096): string { return typeof value === 'string' && value.length <= limit ? value : invalid() }
function integer(value: unknown, minimum = 0): number { return typeof value === 'number' && Number.isSafeInteger(value) && value >= minimum ? value : invalid() }
function bool(value: unknown): boolean { return typeof value === 'boolean' ? value : invalid() }
function exact(value: unknown): string { try { return invocationExactInteger(value) } catch { return invalid() } }
function decimal(value: unknown): string { return typeof value === 'string' && value.length <= 256 && /^-?(0|[1-9]\d*)(\.\d+)?$/.test(value) ? value : invalid() }
function timestamp(value: unknown): string {
  const raw = text(value, 40)
  return /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(raw) && Number.isFinite(Date.parse(raw)) && new Date(raw).toISOString().slice(0, 19) === raw.slice(0, 19) ? raw : invalid()
}
function literal<T extends string>(value: unknown, allowed: readonly T[]): T { return typeof value === 'string' && allowed.includes(value as T) ? value as T : invalid() }
function hash(value: unknown): string { const raw = text(value, 71); return /^sha256:[a-f0-9]{64}$/.test(raw) ? raw : invalid() }
function money(value: unknown, currency: InvocationCurrency): InvocationMoneySnapshot {
  const v = object(value), rate = decimal(v.cny_per_usd)
  if (v.currency !== currency || !validRate(rate)) return invalid()
  return { currency, cny_per_usd: rate, rate_snapshot_at: timestamp(v.rate_snapshot_at), rate_source: literal(v.rate_source, ['setting', 'default_missing', 'default_empty']) }
}
function usage(value: unknown): InvocationUsageExact {
  const v = object(value)
  return { prompt_tokens: exact(v.prompt_tokens), completion_tokens: exact(v.completion_tokens), cached_tokens: exact(v.cached_tokens), reasoning_tokens: exact(v.reasoning_tokens) }
}
function row(value: unknown, scope: InvocationScope): InvocationRow {
  const v = object(value)
  const out: InvocationRow = { id: integer(v.id, 1), created_at: timestamp(v.created_at), request_id: text(v.request_id, 128), api_key_id: integer(v.api_key_id),
    model: text(v.model, 512), modality: v.modality === undefined ? '' : text(v.modality, 80), status: text(v.status, 80), status_code: integer(v.status_code),
    is_stream: bool(v.is_stream), cost_usd: decimal(v.cost_usd), usage_exact: usage(v.usage_exact), frt_ms: integer(v.frt_ms), duration_ms: integer(v.duration_ms) }
  for (const key of ['usage_source', 'error_brief', 'finish_reason', 'protocol', 'upstream_error_code', 'upstream_request_id'] as const) {
    if (v[key] !== undefined) out[key] = text(v[key])
  }
  // White-list both scopes. Payload/client identifiers and arbitrary fields
  // never enter a panel even when a malformed proxy adds them to a response.
  if (scope === 'admin') {
    out.org_id = integer(v.org_id, 1); out.channel_id = integer(v.channel_id); out.provider = text(v.provider, 128)
    out.user_id = v.user_id === null || v.user_id === undefined ? null : integer(v.user_id, 1)
    out.attempts = integer(v.attempts)
  }
  return out
}
function normalized(value: unknown, filter: InvocationFilterSpec): Record<string, string | number | boolean> {
  const v = object(value), out: Record<string, string | number | boolean> = {}
  if (v.source !== 'invocation_log' || v.time_basis !== 'recorded_at' || v.timezone !== filter.timezone) return invalid()
  out.source = 'invocation_log'; out.time_basis = 'recorded_at'; out.timezone = filter.timezone
  out.since = timestamp(v.since); out.until = timestamp(v.until)
  if (Date.parse(out.since) >= Date.parse(out.until)) return invalid()
  const calendar = new Intl.DateTimeFormat('en-US', { timeZone: filter.timezone, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' })
  for (const key of ['since', 'until'] as const) {
    const value = text(out[key], 40), date = new Date(value), parts = calendar.formatToParts(date), part = (name: string) => parts.find(p => p.type === name)?.value
    if (`${part('year')}-${part('month')}-${part('day')}` !== filter[key] || part('hour') !== '00' || part('minute') !== '00' || part('second') !== '00' || date.getUTCMilliseconds() !== 0 || /\.[0-9]*[1-9][0-9]*Z$/.test(value)) return invalid()
  }
  for (const key of ['model', 'request_id', 'modality', 'result', 'status'] as const) {
    if (v[key] !== undefined) out[key] = text(v[key])
    if (out[key] !== filter[key]) return invalid()
  }
  if (v.is_stream !== undefined) out.is_stream = bool(v.is_stream)
  if (out.is_stream !== filter.is_stream) return invalid()
  for (const key of ['api_key_id', 'org_id', 'channel_id'] as const) {
    if (filter.scope === 'org' && key !== 'api_key_id') { if (v[key] !== undefined) return invalid(); continue }
    if (v[key] !== undefined) out[key] = integer(v[key], 1)
    if (out[key] === undefined ? filter[key] !== undefined : String(out[key]) !== filter[key]) return invalid()
  }
  return out
}
export function decodeInvocationBundle(value: unknown, filter: InvocationFilterSpec, currency: InvocationCurrency, page: number, pageSize: InvocationPageSize): InvocationQueryBundle {
  const v = object(value), q = object(v.query), t = object(v.totals), watermark = object(q.data_watermark)
  if (q.filter_version !== 1 || q.consistency !== 'response_snapshot' || q.cross_request_consistency !== 'live' || q.data_source !== 'invocation_log' || q.amount_source !== 'log_recorded_cost' || q.completeness !== 'best_effort' || watermark.kind !== 'observation' || watermark.recorded_through_id !== null || t.usage_presence !== 'legacy_not_distinguishable' || t.missing_usage_count !== null) return invalid()
  if (!Array.isArray(v.items) || v.items.length > pageSize || v.page !== page || v.page_size !== pageSize) return invalid()
  const totals: InvocationTotals = { ...usage(t), record_count: integer(t.record_count), success_count: integer(t.success_count), non_success_count: integer(t.non_success_count),
    cost_usd: decimal(t.cost_usd), cost_display: decimal(t.cost_display), usage_presence: 'legacy_not_distinguishable', missing_usage_count: null }
  const total = integer(v.total)
  if (total !== totals.record_count || totals.success_count + totals.non_success_count !== total || v.items.length > total) return invalid()
  return { items: v.items.map(item => row(item, filter.scope)), total, page, page_size: pageSize, totals,
    query: { filter_version: 1, normalized_filters: normalized(q.normalized_filters, filter), filter_hash: hash(q.filter_hash), as_of: timestamp(q.as_of),
      data_watermark: { kind: 'observation', recorded_through_id: null }, consistency: 'response_snapshot', cross_request_consistency: 'live',
      data_source: 'invocation_log', amount_source: 'log_recorded_cost', completeness: 'best_effort', display_money: money(q.display_money, currency) } }
}

export const invocationResponseMaxBytes = 8 * 1024 * 1024
function aborted(signal: AbortSignal) { if (signal.aborted) throw new DOMException('Aborted', 'AbortError') }
// No body stream means we cannot bound allocation before reading. Fail closed
// rather than using an unbounded blob()/arrayBuffer() fallback.
export async function readInvocationBody(response: Response, signal: AbortSignal, limit = invocationResponseMaxBytes): Promise<Uint8Array> {
  if (signal.aborted) void response.body?.cancel().catch(() => {})
  aborted(signal)
  const length = response.headers.get('Content-Length')
  if (length !== null && (!/^\d+$/.test(length) || BigInt(length) > BigInt(limit))) {
    void response.body?.cancel().catch(() => {})
    throw new ApiError(422, 'Response too large', null, 'export_size_exceeded')
  }
  if (!response.body?.getReader) return invalid()
  const reader = response.body.getReader(), chunks: Uint8Array[] = []
  let size = 0, rejectAbort!: (reason: unknown) => void
  const abort = new Promise<never>((_, reject) => { rejectAbort = reject })
  const onAbort = () => { rejectAbort(new DOMException('Aborted', 'AbortError')); void reader.cancel().catch(() => {}) }
  signal.addEventListener('abort', onAbort, { once: true })
  try {
    for (;;) {
      aborted(signal)
      const next = await Promise.race([reader.read(), abort])
      aborted(signal)
      if (next.done) break
      if (!(next.value instanceof Uint8Array) || next.value.byteLength > limit - size) throw new ApiError(422, 'Response too large', null, 'export_size_exceeded')
      size += next.value.byteLength; chunks.push(next.value)
    }
    const bytes = new Uint8Array(size)
    let offset = 0
    for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength }
    return bytes
  } catch (error) { void reader.cancel().catch(() => {}); throw error }
  finally { signal.removeEventListener('abort', onAbort); reader.releaseLock() }
}
async function checkedResponse(path: string, scope: InvocationScope, signal: AbortSignal): Promise<Response> {
  const response = await apiFetch(path, { admin: scope === 'admin', signal })
  if (signal.aborted) void response.body?.cancel().catch(() => {})
  aborted(signal)
  if (!response.ok) {
    let data: unknown
    try { data = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(await readInvocationBody(response, signal, 64 * 1024))) }
    catch (error) { if (signal.aborted) throw error }
    throw apiResponseError(response, data)
  }
  return response
}
export async function fetchInvocationLogs(filter: InvocationFilterSpec, currency: InvocationCurrency, page: number, pageSize: InvocationPageSize, signal: AbortSignal): Promise<InvocationQueryBundle> {
  const endpoint = filter.scope === 'admin' ? '/api/admin/invocations' : '/api/usage'
  const response = await checkedResponse(`${endpoint}?${invocationQueryParams(filter, currency, { page, pageSize })}`, filter.scope, signal)
  if (!/^application\/json(?:\s*;|$)/i.test(response.headers.get('Content-Type') ?? '')) { void response.body?.cancel().catch(() => {}); return invalid() }
  const bytes = await readInvocationBody(response, signal)
  aborted(signal)
  let value: unknown
  try { value = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes)) } catch { return invalid() }
  return decodeInvocationBundle(value, filter, currency, page, pageSize)
}
export function decodeInvocationExportHeaders(headers: Headers, currency: InvocationCurrency): { filename: string; metadata: InvocationExportResult } {
  if (!/^text\/csv(?:\s*;\s*charset\s*=\s*utf-8)?\s*$/i.test(headers.get('Content-Type') ?? '')) return invalid()
  const match = /^attachment; filename="(invocation-log-(USD|CNY)-\d{8}T\d{6}Z\.csv)"$/.exec(headers.get('Content-Disposition') ?? '')
  if (!match || match[2] !== currency || headers.get('X-Invocation-Filter-Version') !== '1' || headers.get('X-Invocation-Amount-Source') !== 'log_recorded_cost' || headers.get('X-Invocation-Completeness') !== 'best_effort' || headers.get('X-Invocation-Usage-Presence') !== 'legacy_not_distinguishable' || headers.get('X-Invocation-Consistency') !== 'response_snapshot' || headers.get('X-Invocation-Cross-Request-Consistency') !== 'live') return invalid()
  const recordCount = exact(headers.get('X-Invocation-Record-Count')), success = exact(headers.get('X-Invocation-Success-Count')), failure = exact(headers.get('X-Invocation-Non-Success-Count'))
  if (BigInt(recordCount) < 0n || BigInt(recordCount) > 5000n || BigInt(success) < 0n || BigInt(failure) < 0n || BigInt(success) + BigInt(failure) !== BigInt(recordCount)) return invalid()
  for (const name of ['Prompt', 'Completion', 'Cached', 'Reasoning']) exact(headers.get(`X-Invocation-${name}-Tokens`))
  return { filename: match[1], metadata: { recordCount: Number(recordCount), asOf: timestamp(headers.get('X-Invocation-As-Of')), filterHash: hash(headers.get('X-Invocation-Filter-Hash')),
    costUSD: decimal(headers.get('X-Invocation-Cost-USD')), costDisplay: decimal(headers.get('X-Invocation-Cost-Display')),
    money: money({ currency: headers.get('X-Display-Currency'), cny_per_usd: headers.get('X-CNY-Per-USD'), rate_snapshot_at: headers.get('X-Rate-Snapshot-At'), rate_source: headers.get('X-Rate-Source') }, currency) } }
}
export async function fetchInvocationExport(filter: InvocationFilterSpec, currency: InvocationCurrency, signal: AbortSignal): Promise<InvocationExportDownload> {
  const endpoint = filter.scope === 'admin' ? '/api/admin/invocations/export' : '/api/usage/export'
  const response = await checkedResponse(`${endpoint}?${invocationQueryParams(filter, currency)}`, filter.scope, signal)
  let decoded: ReturnType<typeof decodeInvocationExportHeaders>
  try { decoded = decodeInvocationExportHeaders(response.headers, currency) }
  catch (error) { void response.body?.cancel().catch(() => {}); throw error }
  const bytes = await readInvocationBody(response, signal)
  aborted(signal)
  validateInvocationCSV(bytes, filter.scope, decoded.metadata.recordCount)
  return { ...decoded, blob: new Blob([bytes as BlobPart], { type: 'text/csv;charset=utf-8' }) }
}

function validateInvocationCSV(bytes: Uint8Array, scope: InvocationScope, count: number) {
  // These are the API's fixed columns, not user supplied values. Do not split
  // data rows: CSV fields may legally contain quoted commas or newlines.
  const common = 'time,request_id,model,status,modality,api_key_id,prompt_tokens,completion_tokens,cached_tokens,reasoning_tokens,cost_usd,cost_display,currency,cny_per_usd,rate_snapshot_at,rate_source,as_of,filter_hash,amount_source,completeness,usage_presence'
  const header = common + (scope === 'admin' ? ',org_id,user_id,channel_id,provider' : '') + '\n'
  let content: string
  try { content = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes) } catch { return invalid() }
  if (!content.startsWith(header) || !content.endsWith('\n') || (count === 0 ? content !== header : content.length <= header.length)) return invalid()
}
