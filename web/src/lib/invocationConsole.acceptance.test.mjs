import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'
import { createPinia, setActivePinia } from 'pinia'
import { effectScope, nextTick, reactive } from 'vue'

// This executes the shipped composable, authentication transport and actual
// Pinia/Vue runtime. Only browser I/O is synthetic. It does not claim browser
// layout coverage or replace the separate real MySQL API acceptance suite.
const root = fileURLToPath(new URL('../../', import.meta.url))
const built = await build({
  stdin: { contents: `export * from './src/composables/useInvocationLogs'; export * from './src/stores/auth'; export * from './src/stores/currency'; export * from './src/lib/api'; export * from './src/lib/invocationQuery';`, resolveDir: root, loader: 'ts' },
  bundle: true, write: false, format: 'esm', platform: 'node', tsconfig: `${root}/tsconfig.json`,
  plugins: [{ name: 'real-shared-vue-pinia', setup(b) { b.onResolve({ filter: /^(pinia|vue)$/ }, args => ({ path: import.meta.resolve(args.path), external: true })) } }],
})
const app = await import(`data:text/javascript;base64,${Buffer.from(built.outputFiles[0].text).toString('base64')}`)
const declaration = await readFile(new URL('../../../core/internal/server/testdata/invocation_ui1_237.json', import.meta.url))
assert.equal(createHash('sha256').update(declaration).digest('hex'), 'dc408fb51296e248bb346a96a7d24731f6aa9769fa1e9aa46a97ec682dfcd61b')
const fixture = JSON.parse(declaration).rows
const orgIDs = { A: 1001, B: 1002, C: 1003 }, keyIDs = { A1: 11, A2: 12, B1: 21, B2: 22, C1: 31 }
const hash = `sha256:${'c'.repeat(64)}`, asOf = '2026-10-04T12:00:00Z'
const usageKeys = ['prompt_tokens', 'completion_tokens', 'cached_tokens', 'reasoning_tokens']
const dateDraft = { range: 'custom', timezone: 'UTC', start: '2026-09-30', end: '2026-10-04' }

function storage() {
  const values = new Map()
  return { getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, String(value)), removeItem: key => values.delete(key), clear: () => values.clear() }
}
async function flush() { await nextTick(); await new Promise(resolve => setImmediate(resolve)) }
async function until(predicate, label) {
  for (let i = 0; i < 100; i++) { if (predicate()) return; await flush() }
  assert.fail(`did not reach ${label}`)
}
function session(token = 'synthetic-a', orgId = 1001) { return { token, orgId, email: 'fixture@example.invalid', expiresAt: Math.floor(Date.now() / 1000) + 3600 } }

function harness(t, options = {}) {
  const old = Object.fromEntries(['fetch', 'document', 'location', 'localStorage', 'sessionStorage'].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
  const oldCreate = URL.createObjectURL, oldRevoke = URL.revokeObjectURL
  const calls = [], clicks = [], created = [], revoked = [], anchors = []
  let responder = null, appendHook = null, closed = false
  for (const [key, value] of Object.entries({ localStorage: storage(), sessionStorage: storage(), location: { pathname: '/app/request-logs', href: '/app/request-logs' } })) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
  Object.defineProperty(globalThis, 'fetch', { configurable: true, writable: true, value: (path, init = {}) => {
    let resolve, reject
    const promise = new Promise((a, b) => { resolve = a; reject = b })
    const call = { url: new URL(path, 'https://synthetic.invalid'), init, settled: false,
      resolve(value) { if (!call.settled) { call.settled = true; resolve(value) } },
      reject(error) { if (!call.settled) { call.settled = true; reject(error) } } }
    calls.push(call)
    // Deliberately do not auto-reject on abort. A stale upstream can still
    // finish; the real transport/controller must reject its publication.
    if (responder) Promise.resolve().then(() => responder(call)).then(call.resolve, call.reject)
    return promise
  } })
  Object.defineProperty(globalThis, 'document', { configurable: true, writable: true, value: {
    body: { appendChild(anchor) { anchors.push(anchor); appendHook?.() } },
    createElement(tag) { assert.equal(tag, 'a'); return { href: '', download: '', hidden: false, removed: false, click() { clicks.push({ href: this.href, filename: this.download }) }, remove() { this.removed = true } } },
  } })
  URL.createObjectURL = blob => { const url = `blob:synthetic-${created.length}`; created.push({ url, blob }); return url }
  URL.revokeObjectURL = url => revoked.push(url)
  const pinia = createPinia(); setActivePinia(pinia)
  const auth = app.useAuth(), currency = app.useCurrency()
  if (options.token !== null) auth.setSession(session(options.token, options.orgId))
  auth.role = options.role ?? 'user'
  currency.$patch({ ready: true, available: true, displayCurrency: options.currency ?? 'USD', cnyPerUsd: '6.65' })
  if (options.root) app.adminToken.set(options.root)
  const context = reactive({ scope: options.scope ?? 'org', active: options.active ?? true })
  const scope = effectScope()
  const controller = scope.run(() => app.useInvocationLogs({ scope: () => context.scope, active: () => context.active }))
  async function dispose() {
    if (closed) return
    closed = true; scope.stop()
    for (const call of calls) if (!call.settled) call.reject(new DOMException('Fixture cleanup', 'AbortError'))
    await flush(); auth.$dispose(); currency.$dispose()
    for (const [key, descriptor] of Object.entries(old)) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else delete globalThis[key] }
    URL.createObjectURL = oldCreate; URL.revokeObjectURL = oldRevoke
  }
  t.after(dispose)
  return { controller, auth, currency, context, scope, calls, clicks, created, revoked, anchors, dispose,
    responder(fn) { responder = fn }, onAppend(fn) { appendHook = fn },
    latest() { return calls.at(-1) },
  }
}

// Independent decimal oracle: integer 10^-8 USD and rational CNY conversion.
// No production parser, filter or money formatter computes expected values.
function units(value) { const [a, b = ''] = value.split('.'); return BigInt(a) * 100000000n + BigInt(b.padEnd(8, '0')) }
function decimal8(value) { return `${value / 100000000n}.${String(value % 100000000n).padStart(8, '0')}` }
function totals(rows, currency = 'USD', rate = '6.65') {
  const cost = rows.reduce((n, row) => n + units(row.cost_usd), 0n)
  const [whole, fraction = ''] = rate.split('.'), divisor = 10n ** BigInt(fraction.length), numerator = BigInt(whole + fraction)
  const display = currency === 'USD' ? cost : (cost * numerator * 2n + divisor) / (2n * divisor)
  return { record_count: rows.length, success_count: rows.filter(row => row.status === 'success').length,
    non_success_count: rows.filter(row => row.status !== 'success').length,
    ...Object.fromEntries(usageKeys.map(key => [key, rows.reduce((n, row) => n + BigInt(row[key]), 0n).toString()])),
    cost_usd: decimal8(cost), cost_display: decimal8(display), usage_presence: 'legacy_not_distinguishable', missing_usage_count: null }
}
function wireRow(row) {
  return { id: row.ordinal, created_at: row.created_at, request_id: row.request_id, api_key_id: keyIDs[row.key], model: row.model, modality: row.modality,
    status: row.status, status_code: row.status === 'success' ? 200 : 503, is_stream: row.is_stream, cost_usd: row.cost_usd,
    usage_exact: Object.fromEntries(usageKeys.map(key => [key, row[key]])), frt_ms: 5, duration_ms: 9,
    org_id: orgIDs[row.org], user_id: orgIDs[row.org] + 4000, channel_id: row.channel_id, provider: 'private-provider-name', attempts: 1,
    payload_object_key: 'must-not-reach-panel', payload: { text: 'private-prompt' }, credential: 'not-a-real-credential' }
}
function money(currency, rate = '6.65') { return { currency, cny_per_usd: rate, rate_snapshot_at: asOf, rate_source: 'setting' } }
function normalized(params) {
  const n = { source: 'invocation_log', time_basis: 'recorded_at', timezone: params.get('timezone'), since: `${params.get('since')}T00:00:00Z`, until: `${params.get('until')}T00:00:00Z` }
  for (const key of ['model', 'request_id', 'modality', 'result', 'status']) if (params.has(key)) n[key] = params.get(key)
  for (const key of ['org_id', 'channel_id', 'api_key_id']) if (params.has(key)) n[key] = Number(params.get(key))
  if (params.has('is_stream')) n.is_stream = params.get('is_stream') === 'true'
  return n
}
function payload(call, rows = [fixture[0]], options = {}) {
  const p = call.url.searchParams, currency = p.get('display_currency'), rate = options.rate ?? '6.65'
  const page = Number(p.get('page')), size = Number(p.get('page_size'))
  return { items: rows.slice((page - 1) * size, page * size).map(wireRow), total: rows.length, page, page_size: size, totals: totals(rows, currency, rate),
    query: { filter_version: 1, normalized_filters: normalized(p), filter_hash: hash, as_of: asOf,
      data_watermark: { kind: 'observation', recorded_through_id: null }, consistency: 'response_snapshot', cross_request_consistency: 'live', data_source: 'invocation_log',
      amount_source: 'log_recorded_cost', completeness: 'best_effort', display_money: money(currency, rate) } }
}
function json(value, status = 200) { return new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json', 'X-Request-Id': 'synthetic-request' } }) }
function exportHeaders(call, rows, rate = '6.65') {
  const currency = call.url.searchParams.get('display_currency'), sum = totals(rows, currency, rate)
  return { 'Content-Type': 'text/csv;charset=utf-8', 'Content-Disposition': `attachment; filename="invocation-log-${currency}-20261004T120000Z.csv"`,
    'X-Invocation-Filter-Version': '1', 'X-Invocation-Amount-Source': 'log_recorded_cost', 'X-Invocation-Completeness': 'best_effort',
    'X-Invocation-Usage-Presence': 'legacy_not_distinguishable', 'X-Invocation-Consistency': 'response_snapshot', 'X-Invocation-Cross-Request-Consistency': 'live',
    'X-Invocation-Record-Count': String(sum.record_count), 'X-Invocation-Success-Count': String(sum.success_count), 'X-Invocation-Non-Success-Count': String(sum.non_success_count),
    ...Object.fromEntries(usageKeys.map(key => [`X-Invocation-${key.split('_')[0][0].toUpperCase() + key.split('_')[0].slice(1)}-Tokens`, sum[key]])),
    'X-Invocation-As-Of': asOf, 'X-Invocation-Filter-Hash': hash, 'X-Invocation-Cost-USD': sum.cost_usd, 'X-Invocation-Cost-Display': sum.cost_display,
    'X-Display-Currency': currency, 'X-CNY-Per-USD': rate, 'X-Rate-Snapshot-At': asOf, 'X-Rate-Source': 'setting' }
}
const csvColumns = ['time', 'request_id', 'model', 'status', 'modality', 'api_key_id', 'prompt_tokens', 'completion_tokens', 'cached_tokens', 'reasoning_tokens', 'cost_usd', 'cost_display', 'currency', 'cny_per_usd', 'rate_snapshot_at', 'rate_source', 'as_of', 'filter_hash', 'amount_source', 'completeness', 'usage_presence']
function csvText(call, rows, rate = '6.65') {
  const columns = [...csvColumns, ...(call.url.pathname.includes('/admin/') ? ['org_id', 'user_id', 'channel_id', 'provider'] : [])]
  const currency = call.url.searchParams.get('display_currency')
  const lines = rows.map(row => {
    const values = { ...row, time: row.created_at, api_key_id: keyIDs[row.key], cost_display: totals([row], currency, rate).cost_display,
      currency, cny_per_usd: rate, rate_snapshot_at: asOf, rate_source: 'setting', as_of: asOf, filter_hash: hash, amount_source: 'log_recorded_cost',
      completeness: 'best_effort', usage_presence: 'legacy_not_distinguishable', org_id: orgIDs[row.org], user_id: orgIDs[row.org] + 4000, provider: 'synthetic-provider' }
    return columns.map(key => String(values[key] ?? '')).join(',')
  })
  return `${[columns.join(','), ...lines].join('\n')}\n`
}
function csv(call, rows = [fixture[0]], rate = '6.65') { return new Response(csvText(call, rows, rate), { headers: exportHeaders(call, rows, rate) }) }
async function accept(h, rows = [fixture[0]], options) { h.latest().resolve(json(payload(h.latest(), rows, options))); await until(() => !h.controller.loading.value, 'accepted list'); assert.equal(h.controller.error.value, null); assert.ok(h.controller.bundle.value) }

test('actual Pinia/effectScope isolates Org, same-Org token, session identity, currency and stale success/failure/finally', async t => {
  const h = harness(t), c = h.controller
  const oldOrg = h.latest()
  const beforeSwitch = h.calls.length
  h.auth.setSession(session('synthetic-b', 1002))
  const newOrg = h.latest()
  assert.equal(h.calls.length, beforeSwitch + 1, 'one session replacement starts one list request')
  assert.notEqual(oldOrg, newOrg); assert.equal(oldOrg.init.signal.aborted, true)
  oldOrg.resolve(json(payload(oldOrg, [fixture[0]])))
  await flush(); assert.equal(c.bundle.value, null); assert.equal(c.loading.value, true); assert.equal(c.error.value, null)
  await accept(h, [fixture[120]])
  assert.equal(c.bundle.value.items[0].id, 121)
  c.openDetail(c.bundle.value.items[0]); assert.ok(c.selectedRow.value)
  const pending = c.refresh(), oldFailure = h.latest()
  h.auth.setSession(session('synthetic-b-new', 1002))
  assert.equal(c.bundle.value, null); assert.equal(c.selectedRow.value, null)
  oldFailure.reject(new Error('late private response error'))
  await pending; assert.equal(c.loading.value, true); assert.equal(c.error.value, null)
  await accept(h, [fixture[121]])
  const oldSession = h.auth.session, sameTokenCall = c.refresh(), obsolete = h.latest()
  h.auth.setSession({ ...oldSession }) // Same token/org but new authenticated object.
  assert.equal(obsolete.init.signal.aborted, true)
  obsolete.resolve(json({ code: 'unauthorized' }, 401)); await sameTokenCall
  assert.equal(h.auth.token, 'synthetic-b-new'); assert.equal(c.loading.value, true)
  await accept(h, [fixture[122]])
  const usdPending = c.refresh(), usd = h.latest()
  h.currency.displayCurrency = 'CNY'
  assert.equal(c.bundle.value, null); assert.equal(usd.init.signal.aborted, true)
  usd.resolve(json(payload(usd))); await usdPending
  assert.equal(c.loading.value, true); assert.equal(h.latest().url.searchParams.get('display_currency'), 'CNY')
  await accept(h, [fixture[1]])
  assert.equal(c.bundle.value.query.display_money.currency, 'CNY')
  assert.equal(c.bundle.value.totals.cost_display, '0.00000007')
})

test('active scope, role/root changes and scope disposal immediately revoke old view and pending requests', async t => {
  const h = harness(t, { scope: 'admin', role: 'superadmin' }), c = h.controller
  await accept(h); c.openDetail(c.bundle.value.items[0])
  const old = c.refresh(), stale = h.latest(), count = h.calls.length
  h.auth.role = 'admin'
  assert.equal(c.allowed.value, false); assert.equal(c.bundle.value, null); assert.equal(c.selectedRow.value, null); assert.equal(h.calls.length, count)
  stale.resolve(json(payload(stale))); await old
  assert.equal(c.bundle.value, null); assert.equal(c.loading.value, false)
  app.adminToken.set('synthetic-root-one')
  assert.equal(c.allowed.value, true); assert.equal(h.latest().init.headers['X-Admin-Token'], 'synthetic-root-one')
  const rootOld = h.latest(); app.adminToken.set('synthetic-root-two')
  assert.equal(rootOld.init.signal.aborted, true)
  rootOld.resolve(json(payload(rootOld))); await flush(); assert.equal(c.loading.value, true)
  await accept(h)
  const activePending = c.refresh(), activeOld = h.latest()
  h.context.active = false
  assert.equal(c.bundle.value, null); assert.equal(c.loading.value, false); assert.equal(activeOld.init.signal.aborted, true)
  activeOld.resolve(json(payload(activeOld))); await activePending
  h.context.active = true; await accept(h)
  app.adminToken.clear(); assert.equal(c.allowed.value, false); assert.equal(c.bundle.value, null)
  h.context.scope = 'org'; assert.equal(h.latest().url.pathname, '/api/usage'); await accept(h)
  const finalPending = c.refresh(), finalCall = h.latest(); h.scope.stop()
  finalCall.resolve(json(payload(finalCall))); await finalPending
  assert.equal(finalCall.init.signal.aborted, true); assert.equal(c.bundle.value, null); assert.equal(c.loading.value, false)
})

test('current 401 still logs out, but stale 401 cannot log out replacement credentials', async t => {
  const h = harness(t), c = h.controller, initial = h.latest()
  h.auth.setSession(session('new-session', 1001))
  initial.resolve(json({ code: 'unauthorized' }, 401)); await flush()
  assert.equal(h.auth.token, 'new-session'); assert.notEqual(globalThis.location.href, '/login')
  await accept(h)
  const p = c.refresh(); h.latest().resolve(json({ code: 'unauthorized' }, 401)); await p
  assert.equal(h.auth.token, ''); assert.equal(globalThis.location.href, '/login'); assert.equal(c.bundle.value, null); assert.equal(c.allowed.value, false)
})

test('root-only access uses only the admin endpoint; platform admin cannot inherit it after clearing root', async t => {
  const h = harness(t, { token: null, scope: 'admin', root: 'synthetic-root-only' }), c = h.controller
  assert.equal(c.allowed.value, true)
  assert.equal(h.latest().url.pathname, '/api/admin/invocations')
  assert.equal(h.latest().init.headers.Authorization, undefined)
  assert.equal(h.latest().init.headers['X-Admin-Token'], 'synthetic-root-only')
  assert.equal(h.latest().init.cache, 'no-store')
  await accept(h)
  // A direct sessionStorage mutation intentionally emits no revision. Opening
  // an existing detail must still compare the actual captured root identity.
  const row = c.bundle.value.items[0]
  sessionStorage.setItem('laiai.adminToken', 'unannounced-root-change')
  c.openDetail(row)
  assert.equal(c.selectedRow.value, null); assert.equal(c.bundle.value, null)
  h.auth.setSession(session()); h.auth.role = 'admin'
  app.adminToken.clear()
  assert.equal(c.allowed.value, false); assert.equal(c.bundle.value, null)
  const count = h.calls.length
  await c.apply(); await c.refresh(); await c.exportCSV(); c.openDetail(row)
  assert.equal(h.calls.length, count); assert.equal(c.selectedRow.value, null)
  app.adminToken.set('synthetic-retry-root')
  const denied = h.latest(); denied.resolve(json({ code: 'forbidden' }, 403))
  await until(() => !c.loading.value, 'current root denial')
  assert.equal(c.error.value.status, 403); assert.equal(c.bundle.value, null)
  assert.equal(h.latest(), denied, 'no automatic legacy or org fallback')
})

test('draft is unapplied, one apply means one request, pagination/export retain the frozen filter', async t => {
  const h = harness(t), c = h.controller; await accept(h)
  const before = h.calls.length, applied = c.applied.value
  Object.assign(c.draft, dateDraft, { model: 'unsubmitted-model', stream: 'false', modality: 'historical' }); await flush()
  assert.equal(h.calls.length, before); assert.equal(c.applied.value, applied); assert.equal(c.dirty.value, true)
  const exportOld = c.exportCSV(), ex = h.latest()
  assert.equal(ex.url.searchParams.has('model'), false); assert.equal(ex.url.searchParams.has('page'), false); assert.equal(ex.url.searchParams.has('page_size'), false)
  ex.resolve(csv(ex)); await exportOld; assert.equal(h.clicks.length, 1)
  const apply = c.apply(); assert.equal(h.calls.length, before + 2)
  const appliedCall = h.latest(), f = c.applied.value
  assert.equal(c.bundle.value, null); assert.equal(c.page.value, 1); assert.ok(Object.isFrozen(f))
  assert.equal(appliedCall.url.searchParams.get('model'), 'unsubmitted-model'); assert.equal(appliedCall.url.searchParams.get('is_stream'), 'false'); assert.equal(appliedCall.url.searchParams.get('modality'), '')
  appliedCall.resolve(json(payload(appliedCall, fixture.slice(0, 37)))); await apply
  c.draft.model = 'second-unsubmitted-model'
  const pendingExport = c.exportCSV(), exporting = h.latest()
  const paging = c.setPage(2), pageCall = h.latest()
  assert.equal(exporting.init.signal.aborted, false); assert.equal(c.applied.value, f)
  for (const call of [exporting, pageCall]) assert.equal(call.url.searchParams.get('model'), 'unsubmitted-model')
  pageCall.resolve(json(payload(pageCall, fixture.slice(0, 37)))); await paging
  exporting.resolve(csv(exporting, fixture.slice(0, 37))); await pendingExport
  assert.equal(c.bundle.value.page, 2); assert.equal(c.bundle.value.items.length, 17); assert.equal(c.exportResult.value.recordCount, 37)
  assert.equal(h.clicks.length, 2); assert.deepEqual(h.revoked, h.created.map(x => x.url)); assert.ok(h.anchors.every(x => x.removed))
})

test('download cancel/body interruption and identity change immediately before click prevent saving stale data', async t => {
  const h = harness(t), c = h.controller; await accept(h)
  let body, cancelled = false
  const saving = c.exportCSV(), call = h.latest()
  call.resolve(new Response(new ReadableStream({ start(controller) { body = controller; controller.enqueue(new TextEncoder().encode('request_id\r\n')) }, cancel() { cancelled = true } }), { headers: exportHeaders(call, [fixture[0]]) }))
  await flush(); assert.equal(c.exporting.value, true)
  c.cancelExport(); await saving
  assert.equal(cancelled, true); assert.equal(call.init.signal.aborted, true); assert.equal(h.clicks.length, 0); assert.equal(h.created.length, 0); assert.equal(c.exportError.value, null)
  // Response finished and URL exists, but browser credential changes at the
  // final append boundary. No watch/revision is required for the last guard.
  h.context.scope = 'admin'; app.adminToken.set('root-at-start'); await accept(h)
  h.onAppend(() => sessionStorage.setItem('laiai.adminToken', 'root-replaced-without-revision'))
  const lastMoment = c.exportCSV(), final = h.latest(); final.resolve(csv(final)); await lastMoment
  assert.equal(h.clicks.length, 0); assert.equal(c.exportResult.value, null); assert.equal(h.created.length, 1)
  assert.equal(c.exporting.value, false); assert.equal(c.loading.value, false); assert.equal(c.bundle.value, null)
  assert.deepEqual(h.revoked, h.created.map(x => x.url)); assert.equal(h.anchors[0].removed, true)
  void body
})

test('new download survives old failure/finally and changing applied conditions cancels it', async t => {
  const h = harness(t), c = h.controller; await accept(h)
  const first = c.exportCSV(), old = h.latest(); c.cancelExport()
  const second = c.exportCSV(), current = h.latest()
  old.reject(new Error('obsolete download failure')); await first
  assert.equal(c.exporting.value, true); assert.equal(c.exportError.value, null)
  Object.assign(c.draft, dateDraft, { model: 'new-filter' })
  const apply = c.apply(); assert.equal(current.init.signal.aborted, true)
  current.resolve(csv(current)); await second
  assert.equal(h.clicks.length, 0); assert.equal(c.exportResult.value, null); assert.equal(c.exportError.value, null)
  h.latest().resolve(json(payload(h.latest()))); await apply
})

test('download context matrix: Org/token/role/root/currency/active/scope/unmount abort without any click or stale metadata', async t => {
  const changes = [
    ['Org', h => h.auth.setSession(session('new-org', 1002))],
    ['same-Org token', h => h.auth.setSession(session('new-token', 1001))],
    ['role', h => { h.auth.role = 'admin' }],
    ['root', () => app.adminToken.set('synthetic-new-root')],
    ['currency', h => { h.currency.displayCurrency = 'CNY' }],
    ['inactive', h => { h.context.active = false }],
    ['scope', h => { h.context.scope = 'org' }],
    ['unmount', h => h.scope.stop()],
  ]
  for (const [name, change] of changes) await t.test(name, async child => {
    const h = harness(child, { scope: 'admin', role: 'superadmin' }), c = h.controller
    await accept(h)
    const pending = c.exportCSV(), old = h.latest()
    change(h)
    assert.equal(old.init.signal.aborted, true)
    assert.equal(c.bundle.value, null); assert.equal(c.exportResult.value, null); assert.equal(c.exporting.value, false)
    old.resolve(csv(old)); await pending
    assert.equal(h.clicks.length, 0); assert.equal(h.created.length, 0); assert.equal(c.exportResult.value, null); assert.equal(c.exportError.value, null)
  })
})

test('download errors retain a safe error, never emit a partial CSV, and never silently retry or export a page', async t => {
  const cases = [
    [422, 'export_limit_exceeded'], [422, 'export_size_exceeded'], [429, 'query_busy'], [503, 'query_unavailable'],
  ]
  const h = harness(t), c = h.controller; await accept(h)
  for (const [status, code] of cases) {
    const before = h.calls.length, pending = c.exportCSV(), call = h.latest()
    call.resolve(new Response(JSON.stringify({ code, error: 'private SQL or provider information must not be rendered' }), { status,
      headers: { 'Content-Type': 'application/json', 'Retry-After': '3', 'X-Request-Id': 'error-correlation' } }))
    await pending
    assert.equal(c.exportError.value.status, status); assert.equal(c.exportError.value.code, code); assert.equal(c.exportError.value.requestId, 'error-correlation')
    assert.equal(c.exportError.value.message.includes('private'), false); assert.equal(c.exporting.value, false)
    assert.equal(h.calls.length, before + 1); assert.equal(h.clicks.length, 0); assert.equal(h.created.length, 0)
    assert.equal(call.url.searchParams.has('page'), false)
  }
})

test('late aborted response streams are cancelled; empty or non-UTF8 downloads never become success', async t => {
  const h = harness(t), c = h.controller; await accept(h)
  let lateCancelled = 0
  const exp = c.exportCSV(), late = h.latest(); c.cancelExport()
  late.resolve(new Response(new ReadableStream({ cancel() { lateCancelled++ } }), { headers: exportHeaders(late, [fixture[0]]) }))
  await exp
  assert.equal(lateCancelled, 1); assert.equal(h.clicks.length, 0)
  for (const rows of [[], [fixture[0]]]) {
    const pending = c.exportCSV(), call = h.latest()
    call.resolve(new Response('', { headers: exportHeaders(call, rows) })); await pending
    assert.equal(c.exportError.value?.code, 'invocation_contract_unavailable'); assert.equal(c.exportResult.value, null); assert.equal(h.clicks.length, 0)
  }
  const badUTF8 = c.exportCSV(), bad = h.latest()
  bad.resolve(new Response(new Uint8Array([0xff, 0xfe, 0x0a]), { headers: exportHeaders(bad, [fixture[0]]) })); await badUTF8
  assert.equal(c.exportError.value?.code, 'invocation_contract_unavailable'); assert.equal(h.clicks.length, 0)
  const zero = c.exportCSV(), good = h.latest(); good.resolve(csv(good, [])); await zero
  assert.equal(c.exportResult.value.recordCount, 0); assert.equal(h.clicks.length, 1)
  assert.equal(await h.created[0].blob.text(), `${csvColumns.join(',')}\n`)
})

test('invalid apply clears an existing accepted detail and prevents an older request from republishing', async t => {
  const h = harness(t), c = h.controller; await accept(h)
  c.openDetail(c.bundle.value.items[0]); assert.ok(c.selectedRow.value)
  Object.assign(c.draft, dateDraft, { start: '2026-02-30' })
  const before = h.calls.length; await c.apply()
  assert.equal(h.calls.length, before); assert.equal(c.bundle.value, null); assert.equal(c.selectedRow.value, null); assert.ok(c.error.value)
  Object.assign(c.draft, dateDraft); const valid = c.apply(); h.latest().resolve(json(payload(h.latest()))); await valid
  const pending = c.refresh(), old = h.latest(); c.draft.channelId = '201'; await c.apply()
  assert.equal(old.init.signal.aborted, true); const error = c.error.value
  old.resolve(json(payload(old))); await pending
  assert.equal(c.bundle.value, null); assert.equal(c.selectedRow.value, null); assert.equal(c.error.value, error); assert.equal(c.loading.value, false)
})

test('real stalled stream timeout cancels the body and reports a timeout rather than empty success', async t => {
  const h = harness(t), c = h.controller; await accept(h)
  t.mock.timers.enable({ apis: ['setTimeout'] })
  let listCancelled = 0
  const loading = c.refresh(), call = h.latest()
  call.resolve(new Response(new ReadableStream({ start(controller) { controller.enqueue(new TextEncoder().encode('{')) }, cancel() { listCancelled++ } }), { headers: { 'Content-Type': 'application/json' } }))
  await flush(); assert.equal(c.loading.value, true)
  t.mock.timers.tick(10000); await loading
  assert.equal(listCancelled, 1); assert.equal(c.error.value?.code, 'query_timeout'); assert.equal(c.loading.value, false); assert.equal(c.bundle.value, null)
  let exportCancelled = 0
  const exporting = c.exportCSV(), exp = h.latest()
  exp.resolve(new Response(new ReadableStream({ start(controller) { controller.enqueue(new TextEncoder().encode(csvColumns.join(',') + '\n')) }, cancel() { exportCancelled++ } }), { headers: exportHeaders(exp, [fixture[0]]) }))
  await flush(); assert.equal(c.exporting.value, true)
  t.mock.timers.tick(10000); await exporting
  assert.equal(exportCancelled, 1); assert.equal(c.exportError.value?.code, 'query_timeout'); assert.equal(c.exporting.value, false); assert.equal(h.clicks.length, 0)
  t.mock.timers.reset()
})

test('response FX and exact usage remain stable; malicious org/admin fields never enter ordinary accepted rows', async t => {
  const h = harness(t, { currency: 'CNY' }), c = h.controller
  const huge = { ...fixture[2], prompt_tokens: '9223372036854775807' }, second = { ...fixture[1], prompt_tokens: '9223372036854775807' }
  await accept(h, [huge, second])
  assert.equal(c.bundle.value.totals.prompt_tokens, '18446744073709551614')
  assert.equal(app.formatInvocationTokens(c.bundle.value.items[0].usage_exact.prompt_tokens), '9,223,372,036,854,775,807')
  c.openDetail(c.bundle.value.items[0])
  for (const key of ['org_id', 'user_id', 'channel_id', 'provider', 'attempts', 'payload_object_key', 'payload', 'credential']) assert.equal(Object.hasOwn(c.selectedRow.value, key), false, key)
  const accepted = c.bundle.value, selected = c.selectedRow.value, count = h.calls.length
  h.currency.cnyPerUsd = '9.99'; await flush()
  assert.equal(c.bundle.value, accepted); assert.equal(c.selectedRow.value, selected); assert.equal(h.calls.length, count); assert.equal(accepted.query.display_money.cny_per_usd, '6.65')
  const exporting = c.exportCSV(), ex = h.latest(); ex.resolve(csv(ex, [huge, second], '7.30')); await exporting
  assert.equal(c.exportResult.value.money.cny_per_usd, '7.30'); assert.equal(c.bundle.value, accepted); assert.equal(c.bundle.value.query.display_money.cny_per_usd, '6.65')
  const refresh = c.refresh(), broken = payload(h.latest()); delete broken.items[0].usage_exact
  broken.items[0].prompt_tokens = 9007199254740992 // Numeric fallback is forbidden.
  h.latest().resolve(json(broken)); await refresh
  assert.equal(c.bundle.value, null); assert.equal(c.error.value.code, 'invocation_contract_unavailable')
  const noRequests = h.calls.length; c.draft.orgId = '1002'; await c.apply()
  assert.equal(h.calls.length, noRequests); assert.equal(c.error.value.field, 'scope')
})

const conditions = [
  ['all', {}, () => true],
  ['org A', { orgId: '1001' }, row => row.org === 'A'],
  ['org B', { orgId: '1002' }, row => row.org === 'B'],
  ['org C', { orgId: '1003' }, row => row.org === 'C'],
  ['non-success', { result: 'failed' }, row => row.status !== 'success'],
  ['success', { result: 'success' }, row => row.status === 'success'],
  ['exact status', { status: 'upstream_error' }, row => row.status === 'upstream_error'],
  ['historical modality', { modality: 'historical' }, row => row.modality === ''],
  ['video', { modality: 'video' }, row => row.modality === 'video'],
  ['not streamed', { stream: 'false' }, row => !row.is_stream],
  ['model and key', { model: 'fixture-model-1', apiKeyId: '11' }, row => row.model === 'fixture-model-1' && row.key === 'A1'],
  ['channel and failure', { channelId: '203', result: 'failed' }, row => row.channel_id === 203 && row.status !== 'success'],
  ['exact request', { requestId: fixture[2].request_id }, row => row.ordinal === 3],
]
// The synthetic API deliberately does not import production filter helpers.
// The independently declared cases below, and the golden overall totals, are
// the oracle; a wrong request is detected before a response is supplied.
test('237 declaration: 13 conditions, 20/50/100 page unions, all-match totals and CSV metadata use the applied collection', async t => {
  assert.equal(fixture.length, 237)
  assert.deepEqual(totals(fixture), { record_count: 237, success_count: 180, non_success_count: 57, prompt_tokens: '9007199254743333', completion_tokens: '4680', cached_tokens: '468', reasoning_tokens: '702', cost_usd: '123456789.12634435', cost_display: '123456789.12634435', usage_presence: 'legacy_not_distinguishable', missing_usage_count: null })
  const h = harness(t, { scope: 'admin', role: 'superadmin' }), c = h.controller
  await accept(h, [])
  for (const [name, fields, predicate] of conditions) {
    const expected = fixture.filter(predicate).sort((a, b) => b.ordinal - a.ordinal)
    Object.assign(c.draft, app.defaultInvocationDraft(), dateDraft, fields)
    const pending = c.apply(), request = h.latest(), p = request.url.searchParams
    assert.equal(p.get('since'), '2026-09-30', name); assert.equal(p.get('until'), '2026-10-05', name)
    const mapping = { model: 'model', requestId: 'request_id', apiKeyId: 'api_key_id', orgId: 'org_id', channelId: 'channel_id', result: 'result', status: 'status', stream: 'is_stream', modality: 'modality' }
    for (const [draftKey, queryKey] of Object.entries(mapping)) assert.equal(p.get(queryKey), fields[draftKey] === undefined ? null : fields[draftKey] === 'historical' ? '' : fields[draftKey], `${name}:${queryKey}`)
    request.resolve(json(payload(request, expected))); await pending
    for (const size of [20, 50, 100]) {
      if (c.pageSize.value !== size) { const changing = c.setPageSize(size); h.latest().resolve(json(payload(h.latest(), expected))); await changing }
      else if (c.page.value !== 1) { const changing = c.setPage(1); h.latest().resolve(json(payload(h.latest(), expected))); await changing }
      const seen = []
      for (let page = 1; page <= Math.max(1, Math.ceil(expected.length / size)); page++) {
        if (page > 1) { const paging = c.setPage(page); h.latest().resolve(json(payload(h.latest(), expected))); await paging }
        assert.deepEqual(c.bundle.value.totals, totals(expected), `${name}/${size}/${page} all-match totals`)
        seen.push(...c.bundle.value.items.map(row => row.id))
      }
      assert.deepEqual(seen, expected.map(row => row.ordinal), `${name}/${size} exact page union`)
    }
    const listBundle = c.bundle.value, exp = c.exportCSV(), call = h.latest()
    const exportParams = new URLSearchParams(call.url.searchParams), listParams = new URLSearchParams(request.url.searchParams); listParams.delete('page'); listParams.delete('page_size')
    assert.equal(exportParams.toString(), listParams.toString(), `${name} same applied filter`)
    call.resolve(csv(call, expected)); await exp
    assert.equal(c.exportResult.value.recordCount, expected.length); assert.equal(c.exportResult.value.costUSD, totals(expected).cost_usd); assert.equal(c.bundle.value, listBundle)
    const body = await h.created.at(-1).blob.text()
    assert.deepEqual(body.split('\n').slice(1).filter(Boolean).map(line => line.split(',')[1]), expected.map(row => row.request_id), `${name} complete synthetic CSV`)
  }
  assert.equal(h.clicks.length, 13)
  assert.deepEqual(h.revoked, h.created.map(x => x.url))
})
