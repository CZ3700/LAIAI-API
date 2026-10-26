import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'

const result = await build({ entryPoints: [fileURLToPath(new URL('./invocationQuery.ts', import.meta.url))], bundle: true, write: false, format: 'esm', platform: 'node' })
const q = await import(`data:text/javascript;base64,${Buffer.from(result.outputFiles[0].text).toString('base64')}`)
const draft = patch => ({ ...q.defaultInvocationDraft(), ...patch })
const now = new Date('2026-10-01T01:30:00Z')
const fails = (fn, field, reason) => assert.throws(fn, e => e instanceof q.InvocationFilterError && e.field === field && e.reason === reason)

test('calendar windows are explicit, paired and stable in the selected timezone', () => {
  assert.deepEqual(q.invocationDateWindow(draft({}), now), { since: '2026-09-25', until: '2026-10-02' })
  assert.deepEqual(q.invocationDateWindow(draft({ timezone: 'America/New_York' }), now), { since: '2026-09-24', until: '2026-10-01' })
  assert.deepEqual(q.invocationDateWindow(draft({ timezone: 'Asia/Shanghai', range: 'month' }), now), { since: '2026-10-01', until: '2026-10-02' })
  assert.deepEqual(q.invocationDateWindow(draft({ range: 'prev' }), new Date('2024-03-01T01:00:00Z')), { since: '2024-02-01', until: '2024-03-01' })
  assert.deepEqual(q.invocationDateWindow(draft({ range: '30d' }), new Date('2026-01-01T01:00:00Z')), { since: '2025-12-03', until: '2026-01-02' })
  const fixed = q.buildInvocationFilter(draft({}), 'org', now)
  assert.equal(q.invocationQueryParams(fixed, 'USD', { page: 2, pageSize: 20 }).get('since'), '2026-09-25')
  assert.ok(Object.isFrozen(fixed))
})

test('inclusive custom dates cross DST and leap days as calendar dates, with a 90-day bound', () => {
  for (const [start, end, until] of [['2026-03-08', '2026-03-08', '2026-03-09'], ['2026-11-01', '2026-11-01', '2026-11-02'], ['2024-02-28', '2024-02-29', '2024-03-01'], ['2026-01-01', '2026-03-31', '2026-04-01']]) {
    assert.deepEqual(q.invocationDateWindow(draft({ range: 'custom', timezone: 'America/New_York', start, end }), now), { since: start, until })
  }
  fails(() => q.invocationDateWindow(draft({ range: 'custom', start: '2026-01-01', end: '2026-04-01' }), now), 'range', 'invalid_range')
  fails(() => q.invocationDateWindow(draft({ range: 'custom', start: '2026-03-09', end: '2026-03-08' }), now), 'range', 'invalid_range')
  for (const value of ['', '2026-02-29', '2026-03-08T00:00:00Z', '2026-3-8']) {
    fails(() => q.invocationDateWindow(draft({ range: 'custom', start: value, end: '2026-03-08' }), now), 'start', 'invalid_date')
  }
  fails(() => q.invocationDateWindow(draft({ timezone: 'Local' }), now), 'timezone', 'invalid_timezone')
})

test('historical modality, false stream and non-success retain their distinct API meanings', () => {
  const filter = q.buildInvocationFilter(draft({ modality: 'historical', stream: 'false', result: 'failed' }), 'org', now)
  const values = q.invocationQueryParams(filter, 'CNY', { page: 1, pageSize: 50 })
  assert.equal(values.get('modality'), '')
  assert.equal(values.has('modality'), true)
  assert.equal(values.get('is_stream'), 'false')
  assert.equal(values.get('result'), 'failed')
  assert.equal(values.has('status'), false)
  assert.equal(values.get('filter_version'), '1')
  assert.equal(values.get('source'), 'invocation_log')
  assert.equal(values.get('time_basis'), 'recorded_at')
  const all = q.invocationQueryParams(q.buildInvocationFilter(draft({}), 'org', now), 'USD')
  for (const key of ['modality', 'is_stream', 'result', 'status']) assert.equal(all.has(key), false)
  fails(() => q.buildInvocationFilter(draft({ result: 'failed', status: 'upstream_error' }), 'org', now), 'status', 'conflicting_parameters')
  fails(() => q.buildInvocationFilter(draft({ status: 'failed' }), 'org', now), 'status', 'unsupported_value')
})

test('list and export share exactly the applied filters and currency, without export pagination', () => {
  const filter = q.buildInvocationFilter(draft({ model: '  historical-model  ', requestId: 'a'.repeat(32), orgId: '9007199254740993', apiKeyId: '00011', channelId: '18446744073709551615', stream: 'true' }), 'admin', now)
  const list = q.invocationQueryParams(filter, 'CNY', { page: 3, pageSize: 100 })
  const csv = q.invocationQueryParams(filter, 'CNY')
  assert.equal(csv.has('page'), false); assert.equal(csv.has('page_size'), false)
  list.delete('page'); list.delete('page_size')
  assert.equal(list.toString(), csv.toString())
  assert.equal(csv.get('api_key_id'), '11')
  assert.equal(csv.get('org_id'), '9007199254740993')
  assert.equal(csv.get('channel_id'), '18446744073709551615')
  assert.equal(csv.get('model'), 'historical-model')
  assert.equal(new Set([...csv.keys()]).size, [...csv.keys()].length)
})

test('scope is enforced at construction and serialization; extraneous properties never become query params', () => {
  for (const patch of [{ orgId: '1' }, { channelId: '2' }]) fails(() => q.buildInvocationFilter(draft(patch), 'org', now), 'scope', 'admin_filter_forbidden')
  const filter = q.buildInvocationFilter(draft({}), 'org', now)
  fails(() => q.invocationQueryParams({ ...filter, org_id: '1' }, 'USD'), 'scope', 'admin_filter_forbidden')
  fails(() => q.invocationQueryParams({ ...filter, scope: 'platform' }, 'USD'), 'scope', 'invalid_scope')
  const forged = q.invocationQueryParams({ ...filter, Authorization: 'synthetic', payload: 'private', page: 999 }, 'USD')
  assert.equal(forged.has('Authorization'), false); assert.equal(forged.has('payload'), false); assert.equal(forged.has('page'), false)
  for (const value of ['0', '-1', '1e3', '1.2', '18446744073709551616']) fails(() => q.buildInvocationFilter(draft({ apiKeyId: value }), 'org', now), 'api_key_id', 'positive_integer_required')
  fails(() => q.buildInvocationFilter(draft({ model: 'private\ntext' }), 'org', now), 'model', 'invalid_text')
  fails(() => q.buildInvocationFilter(draft({ requestId: 'a'.repeat(33) }), 'org', now), 'request_id', 'invalid_text')
})

test('pagination is bounded and exact tokens never pass through Number or locale numeric coercion', () => {
  const filter = q.buildInvocationFilter(draft({}), 'org', now)
  assert.equal(q.invocationQueryParams(filter, 'USD', { page: 1001, pageSize: 100 }).get('page'), '1001')
  for (const pagination of [{ page: 1002, pageSize: 100 }, { page: 0, pageSize: 20 }, { page: 1.1, pageSize: 20 }, { page: 1, pageSize: 25 }]) fails(() => q.invocationQueryParams(filter, 'USD', pagination), 'page', 'invalid_page')
  assert.equal(q.formatInvocationTokens('9007199254740993'), '9,007,199,254,740,993')
  assert.equal(q.invocationTotalTokens('9223372036854775807', '9223372036854775807'), '18446744073709551614')
  assert.equal(q.formatInvocationTokens('0'), '0')
  for (const value of [9007199254740992, null, undefined, '', '1e6', '1.0']) fails(() => q.formatInvocationTokens(value), 'usage_exact', 'invalid_integer')
})
