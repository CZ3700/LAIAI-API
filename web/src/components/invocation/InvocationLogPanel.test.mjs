import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'
import * as vue from 'vue'

// Mount the actual compiled Vue template. Only the controller/application
// boundary and drawer shell are replaced; rendering and reactivity are real.
const filename = fileURLToPath(new URL('./InvocationLogPanel.vue', import.meta.url))
const { descriptor } = parse(await readFile(filename, 'utf8'), { filename })
const compiled = compileScript(descriptor, { id: 'invocation-panel-render', inlineTemplate: true, templateOptions: { compilerOptions: { hoistStatic: false } } })
const money = await readFile(new URL('../../lib/decimalMoney.ts', import.meta.url), 'utf8')
const filters = await readFile(new URL('../../lib/invocationQuery.ts', import.meta.url), 'utf8')
const built = await build({ stdin: { contents: compiled.content, loader: 'ts', sourcefile: filename }, bundle: true, format: 'esm', write: false,
  plugins: [{ name: 'panel-boundaries', setup(b) {
    b.onResolve({ filter: /.*/ }, args => ({ path: args.path, namespace: 'test-boundary' }))
    b.onLoad({ filter: /.*/, namespace: 'test-boundary' }, args => {
      if (args.path === 'vue') return { contents: Object.keys(vue).filter(key => /^[A-Za-z_$][\w$]*$/.test(key)).map(key => `export const ${key} = globalThis.__invocationPanelVue.${key};`).join('\n') }
      if (args.path === 'vue-i18n') return { contents: `export const useI18n = () => ({ t: (key, params) => key + (params ? ' ' + JSON.stringify(params) : ''), locale: globalThis.__invocationPanelVue.ref('en-US') });` }
      if (args.path === '@/composables/useInvocationLogs') return { contents: `export const useInvocationLogs = options => { globalThis.__invocationPanelState.options = options; return globalThis.__invocationPanelState; };` }
      if (args.path === '@/lib/api') return { contents: `export const ApiError = globalThis.__invocationPanelApiError; export const copyText = async () => true;` }
      if (args.path === '@/lib/decimalMoney') return { contents: money, loader: 'ts' }
      if (args.path === '@/lib/invocationQuery') return { contents: filters, loader: 'ts' }
      if (args.path === '@/components/DetailDrawer.vue') return { contents: `export default { props:['open'], setup(props, {slots}) { return () => props.open ? globalThis.__invocationPanelVue.h('aside', {'data-drawer':'right'}, slots.default?.()) : null; } };` }
      throw new Error(`Unexpected panel dependency ${args.path}`)
    })
  } }] })
class ApiError extends Error { constructor(status, requestId, code, retryAfter = null) { super('must-not-render-server-message'); this.status = status; this.requestId = requestId; this.code = code; this.retryAfter = retryAfter } }
globalThis.__invocationPanelVue = vue
globalThis.__invocationPanelApiError = ApiError
const { default: Panel } = await import(`data:text/javascript;base64,${Buffer.from(built.outputFiles[0].text).toString('base64')}`)
globalThis.Document ??= class Document { activeElement = null }
globalThis.ShadowRoot ??= class ShadowRoot {}
globalThis.document ??= new globalThis.Document()
function node(tag, text = '') { return { tag, tagName: tag.toUpperCase(), text, props: {}, children: [], parent: null, listeners: {},
  addEventListener(name, callback) { this.listeners[name] = callback }, removeEventListener(name) { delete this.listeners[name] },
  getRootNode() { return globalThis.document },
  get options() { return this.children.filter(n => n.tag === 'option') },
} }
const renderer = vue.createRenderer({
  createElement: tag => node(tag), createText: text => node('#text', text), createComment: text => node('#comment', text),
  setText(n, text) { n.text = text }, setElementText(n, text) { n.children = []; n.text = text },
  parentNode: n => n.parent, nextSibling: n => n.parent?.children[n.parent.children.indexOf(n) + 1] ?? null,
  patchProp(n, key, _old, value) { n.props[key] = value; if (key === 'value') { n._value = value; n.value = value } if (key === 'type') n.type = value },
  insert(n, parent, before) { if (n.parent) { const old = n.parent.children.indexOf(n); if (old >= 0) n.parent.children.splice(old, 1) } n.parent = parent; const index = before ? parent.children.indexOf(before) : -1; if (index < 0) parent.children.push(n); else parent.children.splice(index, 0, n) },
  remove(n) { if (n.parent) { const index = n.parent.children.indexOf(n); if (index >= 0) n.parent.children.splice(index, 1); n.parent = null } },
})
function flatten(n) { return [n, ...n.children.flatMap(flatten)] }
function text(n) { return [n.tag === '#comment' ? '' : n.text, ...n.children.map(text)].join(' ') }
function response() {
  return { items: [{ id: 41, request_id: 'synthetic-request', created_at: '2026-10-26T00:00:00Z', api_key_id: 7, model: 'historical-model', modality: 'chat', status: 'success', status_code: 200, is_stream: false, cost_usd: '0.00000001', usage_exact: { prompt_tokens: '9007199254740993', completion_tokens: '2', cached_tokens: '0', reasoning_tokens: '0' }, duration_ms: 10, frt_ms: 2,
    org_id: 991, channel_id: 992, user_id: 993, provider: 'private-provider', upstream_request_id: 'private-upstream', payload_object_key: 'never-render-payload' }],
    total: 237, page: 2, page_size: 20, totals: { record_count: 237, success_count: 180, non_success_count: 57, prompt_tokens: '18446744073709551614', completion_tokens: '400', cached_tokens: '0', reasoning_tokens: '0', cost_usd: '1', cost_display: '6.65', usage_presence: 'legacy_not_distinguishable', missing_usage_count: null },
    query: { as_of: '2026-10-26T01:00:00Z', normalized_filters: { since: '2026-10-20T00:00:00Z', until: '2026-10-27T00:00:00Z' }, filter_hash: 'synthetic-hash', display_money: { currency: 'CNY', cny_per_usd: '6.65', rate_snapshot_at: '2026-10-26T01:00:00Z', rate_source: 'setting' } } }
}
function harness(t, scope = 'org') {
  const bundle = response(), calls = []
  const state = { draft: vue.reactive({ range: '7d', timezone: 'UTC', start: '', end: '', model: '', requestId: '', apiKeyId: '', orgId: '', channelId: '', modality: 'all', result: 'all', status: '', stream: 'all' }),
    applied: vue.shallowRef({ scope, timezone: 'UTC', since: '2026-10-20', until: '2026-10-27', model: 'historical-model' }), bundle: vue.shallowRef(bundle), page: vue.ref(2), pageSize: vue.ref(20), loading: vue.ref(false), error: vue.shallowRef(null), exporting: vue.ref(false), exportResult: vue.shallowRef(null), exportError: vue.shallowRef(null), selectedRow: vue.shallowRef(null), allowed: vue.ref(true), dirty: vue.ref(false) }
  for (const name of ['apply','reset','refresh','setPage','setPageSize','exportCSV','cancelExport','closeDetail']) state[name] = (...args) => calls.push([name, ...args])
  state.openDetail = row => { calls.push(['openDetail',row]); state.selectedRow.value = row }
  globalThis.__invocationPanelState = state
  const root = node('root'), props = vue.reactive({ scope, active: true })
  const app = renderer.createApp({ setup: () => () => vue.h(Panel, props) }); app.mount(root)
  t.after(() => app.unmount())
  return { root, state, props, calls, all: () => flatten(root), text: () => text(root) }
}

test('actual panel renders exact all-match totals and snapshot FX; ordinary DOM omits admin data', async t => {
  const h = harness(t)
  assert.match(h.text(), /18,446,744,073,709,551,614/)
  assert.match(h.text(), /9,007,199,254,740,995/)
  assert.match(h.text(), /6\.65/)
  assert.match(h.text(), /<¥0\.000001/)
  assert.match(h.text(), /"first":21,"last":21,"total":237/)
  const request = h.all().find(n => n.tag === 'button' && text(n).includes('synthetic-request'))
  request.props.onClick(); await vue.nextTick()
  assert.match(h.text(), /0\.0000000665 CNY/)
  assert.ok(h.all().some(n => n.tag === 'aside' && n.props['data-drawer'] === 'right'))
  for (const sensitive of ['private-provider','private-upstream','never-render-payload','admin.lgOrg','admin.lgChannel','admin.lgUser']) assert.ok(!h.text().includes(sensitive), sensitive)
  // The sum is the server total, not the one visible row's tiny amount.
  assert.equal(h.state.bundle.value.totals.cost_display, '6.65')
})

test('admin panel exposes only approved metadata and all field types stay exact', async t => {
  const h = harness(t, 'admin')
  h.state.selectedRow.value = h.state.bundle.value.items[0]; await vue.nextTick()
  assert.match(h.text(), /private-provider/); assert.match(h.text(), /private-upstream/)
  assert.match(h.text(), /#991/); assert.match(h.text(), /#992/)
  assert.ok(!h.text().includes('never-render-payload'))
  assert.ok(h.all().filter(n => n.tag === 'input').every(n => n.props.type !== 'number'))
  h.state.allowed.value = false; h.state.bundle.value = null; h.state.selectedRow.value = null; await vue.nextTick()
  assert.ok(!h.text().includes('private-provider')); assert.ok(!h.text().includes('synthetic-request'))
  assert.match(h.text(), /invocationLogs.permission/)
})

test('draft edits do not relabel applied results or export; form and pagination call controller once', async t => {
  const h = harness(t)
  h.state.draft.model = 'unapplied-private-draft'; h.state.dirty.value = true; await vue.nextTick()
  const chips = h.all().find(n => n.props.class === 'invocation-chips')
  assert.match(text(chips), /historical-model/); assert.ok(!text(chips).includes('unapplied-private-draft'))
  assert.match(h.text(), /invocationLogs.dirty/)
  h.all().find(n => n.tag === 'form').props.onSubmit({ preventDefault() {} })
  h.all().find(n => n.tag === 'button' && text(n).includes('invocationLogs.exportApplied')).props.onClick()
  h.all().find(n => n.tag === 'button' && text(n).includes('common.next')).props.onClick()
  assert.deepEqual(h.calls, [['apply'],['exportCSV'],['setPage',3]])
})

test('loading, failures and empty pages have distinct real DOM states without old details or amounts', async t => {
  const h = harness(t)
  h.state.loading.value = true; h.state.bundle.value = null; await vue.nextTick()
  assert.match(h.text(), /reqlog.loading/); assert.ok(!h.text().includes('synthetic-request')); assert.ok(!h.text().includes('6.65'))
  h.state.loading.value = false; h.state.error.value = new ApiError(503,'safe-request-id'); await vue.nextTick()
  assert.match(h.text(), /invocationLogs.unavailable/); assert.match(h.text(), /safe-request-id/); assert.ok(!h.text().includes('must-not-render-server-message'))
  h.state.error.value = null; const empty = response(); empty.items = []; h.state.bundle.value = empty; await vue.nextTick()
  assert.match(h.text(), /reqlog.emptyData/); assert.match(h.text(), /"first":0,"last":0,"total":237/)
  h.state.exportError.value = new ApiError(422,null,'export_limit_exceeded'); await vue.nextTick()
  assert.match(h.text(), /invocationLogs.exportLimit/)
})

test('429 renders a bounded Retry-After hint without retrying or reflecting invalid headers', async t => {
  const h = harness(t)
  h.state.bundle.value = null; h.state.error.value = new ApiError(429, 'safe-id', null, '2'); await vue.nextTick()
  assert.match(h.text(), /invocationLogs.busyRetry.*"seconds":"2"/)
  assert.deepEqual(h.calls, [])
  h.state.error.value = new ApiError(429, 'safe-id', null, 'malformed-private-value'); await vue.nextTick()
  assert.match(h.text(), /invocationLogs.busy/); assert.ok(!h.text().includes('malformed-private-value'))
})
