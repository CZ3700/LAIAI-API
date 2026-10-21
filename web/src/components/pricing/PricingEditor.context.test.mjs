import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { setTimeout as delay } from 'node:timers/promises'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'
import * as vue from 'vue'

async function component(relative) {
  const filename = fileURLToPath(new URL(relative, import.meta.url))
  const { descriptor } = parse(await readFile(filename, 'utf8'), { filename })
  const source = compileScript(descriptor, { id: 'pricing-editor-regression' })
  const built = await build({ stdin: { contents: source.content, loader: 'ts', sourcefile: filename }, bundle: true, format: 'esm', write: false,
    plugins: [{ name: 'test-boundaries', setup(b) {
      b.onResolve({ filter: /.*/ }, args => ({ path: args.path, namespace: 'boundary' }))
      b.onLoad({ filter: /.*/, namespace: 'boundary' }, async args => {
        if (args.path === 'vue') return { contents: `
          export const defineComponent = value => value;
          export const ref = (...args) => globalThis.__editorHarness.vue.ref(...args);
          export const computed = (...args) => globalThis.__editorHarness.vue.computed(...args);
          export const watch = (...args) => globalThis.__editorHarness.vue.watch(...args);
          export const nextTick = (...args) => globalThis.__editorHarness.vue.nextTick(...args);
          export const onMounted = fn => globalThis.__editorHarness.mounted.push(fn);
          export const onActivated = fn => globalThis.__editorHarness.activated.push(fn);
          export const onDeactivated = fn => globalThis.__editorHarness.deactivated.push(fn);
          export const onBeforeUnmount = fn => globalThis.__editorHarness.unmounted.push(fn);
        ` }
        if (args.path === 'vue-i18n') return { contents: 'export const useI18n = () => ({t: key => key});' }
        if (args.path === 'vue-router') return { contents: 'export const useRoute = () => globalThis.__editorHarness.route; export const useRouter = () => globalThis.__editorHarness.router; export const onBeforeRouteLeave = fn => globalThis.__editorHarness.guards.push(fn);' }
        if (args.path === '@/stores/auth') return { contents: 'export const useAuth = () => globalThis.__editorHarness.auth;' }
        if (args.path === '@/lib/api') return { contents: 'export class ApiError extends Error { constructor(status,message,requestId=null,code=null) { super(message); this.status = status; this.requestId = requestId; this.code = code; } } export const adminApi = (...args) => globalThis.__editorHarness.api(...args); export const api = (...args) => { globalThis.__editorHarness.ApiError = ApiError; return adminApi(...args); }; export const errorMessage = (e,fallback) => e instanceof ApiError ? e.message : fallback; export const uuid = () => "fixture-key-" + ++globalThis.__editorHarness.id;' }
        if (args.path === './types') return { contents: await readFile(new URL('./types.ts', import.meta.url), 'utf8'), loader: 'ts' }
        if (args.path === './catalogPresentation') return { contents: await readFile(new URL('./catalogPresentation.ts', import.meta.url), 'utf8'), loader: 'ts' }
        if (args.path === './videoPriceDecimal') return { contents: await readFile(new URL('./videoPriceDecimal.ts', import.meta.url), 'utf8'), loader: 'ts' }
        if (args.path.endsWith('.vue')) return { contents: 'export default {};' }
        throw new Error(`Unexpected dependency ${args.path}`)
      })
    } }],
  })
  return (await import(`data:text/javascript;base64,${Buffer.from(built.outputFiles[0].text).toString('base64')}`)).default
}
const Generic = await component('./GenericPriceEditor.vue')
const Panel = await component('./AdminPricingPanel.vue')
const Official = await component('../OfficialVideoPriceCatalog.vue')
const Summary = await component('./AdminPriceSummary.vue')
const VariantEvidence = await component('./OfficialPriceEvidence.vue')
const VariantPlan = await component('./VariantPricePlan.vue')
const OfficialDefaults = await component('./OfficialVideoPricingDefaults.vue')
const InitialSetup = await component('./InitialVideoPricingSetup.vue')
const presentationBuild = await build({ entryPoints: [fileURLToPath(new URL('./catalogPresentation.ts', import.meta.url))], bundle: true, format: 'esm', write: false })
const presentation = await import(`data:text/javascript;base64,${Buffer.from(presentationBuild.outputFiles[0].text).toString('base64')}`)
function deferred() { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
async function until(predicate) { const end = performance.now() + 3000; while (performance.now() < end) { if (predicate()) return; await delay(5) } assert.fail('condition timed out') }
function price(model = 'same-public-name') { return { model, input_usd_per_1m: '1.12345678', output_usd_per_1m: '2.23456789', cached_input_usd_per_1m: '0.25', image_usd: '0.11111111', per_call_usd: '0.2', tts_usd_per_1k_chars: '0.3', transcription_usd_per_call: '0.4', audio_input_usd_per_1m: '0.5', audio_output_usd_per_1m: '0.6' } }
function row(key = '1:same-public-name', provider = 'one') { return { key, provider, channel_id: Number(key.split(':')[0]), model: 'same-public-name', upstream_model: `${provider}-upstream`, pricing_kind: 'token', recognition_source: 'existing_price', billing_modes: ['token'], generic_price: price(), video_model: null, video_configuration: null, unsupported_reason: '' } }
function catalog() { return { providers: [{ name: 'one', display_name: 'First provider' }, { name: 'two', display_name: 'Second provider' }], channels: [{ id: 1, name: 'active-channel', provider: 'one', status: 1 }, { id: 2, name: 'disabled-channel', provider: 'one', status: 0 }, { id: 3, name: 'other-provider', provider: 'two', status: 1 }], models: [row(), row('2:same-public-name'), row('3:same-public-name', 'two')], permissions: { video_details: true }, price_scope: 'public_model_global' } }
async function harness(t, Component, props = {}, response = () => catalog()) {
  const original = globalThis.__editorHarness
  const oldWindow = globalThis.window
  const h = { vue, auth: vue.reactive({ orgId: 1, token: 'fixture-a', role: 'superadmin', isSuperadmin: true }), props: vue.reactive(props), calls: [], events: [], mounted: [], activated: [], deactivated: [], unmounted: [], guards: [], route: vue.reactive({ query: { tab: 'prices' } }), respond: response, confirmResult: false, confirmations: 0, id: 0 }
  h.router = { replace(next) { h.route.query = next.query; return Promise.resolve() } }
  globalThis.window = { addEventListener() {}, removeEventListener() {}, confirm() { h.confirmations++; return h.confirmResult } }
  h.api = async (method, path, body, options) => { const call = { method, path, body, options, org: h.auth.orgId }; h.calls.push(call); return h.respond(call) }
  globalThis.__editorHarness = h
  const scope = vue.effectScope()
  h.vm = scope.run(() => Component.setup(h.props, { expose() {}, emit: (...args) => h.events.push(args) }))
  t.after(() => { h.unmounted.forEach(fn => fn()); scope.stop(); if (original === undefined) delete globalThis.__editorHarness; else globalThis.__editorHarness = original; if (oldWindow === undefined) delete globalThis.window; else globalThis.window = oldWindow })
  for (const fn of h.mounted) await fn()
  return h
}

test('provider and channel select actual rows, keep disabled channels, and never write on selection', async t => {
  const h = await harness(t, Panel)
  assert.equal(h.vm.selected.value, null)
  h.vm.provider.value = 'one'
  assert.deepEqual(h.vm.channels.value.map(c => c.id), [1, 2])
  assert.equal(h.vm.rows.value.length, 2)
  h.vm.modelKey.value = '2:same-public-name'
  h.vm.detailsOpen.value = true
  assert.equal(h.vm.selected.value.channel_id, 2)
  h.vm.provider.value = 'two'
  assert.equal(h.vm.selected.value, null)
  assert.equal(h.vm.detailsOpen.value, false)
  assert.equal(h.vm.rows.value[0].upstream_model, 'two-upstream')
  assert.ok(h.calls.every(c => c.method === 'GET'))
  h.auth.isSuperadmin = false
  assert.equal(h.vm.videoAllowed.value, false)
})

test('late catalog response cannot restore a prior organization and deactivation clears selection', async t => {
  const h = await harness(t, Panel), late = deferred()
  h.respond = call => call.org === 1 ? late.promise : { ...catalog(), providers: [{ name: 'new', display_name: 'new-context' }], models: [] }
  const previous = h.vm.load()
  h.auth.orgId = 2
  await until(() => !h.vm.loading.value)
  late.resolve(catalog()); await previous
  assert.equal(h.vm.catalog.value.providers[0].name, 'new')
  h.deactivated.forEach(fn => fn())
  assert.equal(h.vm.catalog.value, null)
  assert.equal(h.vm.modelKey.value, '')
})

test('a saved event from a previous provider or channel cannot refresh the current selection', async t => {
  const h = await harness(t, Panel)
  h.vm.provider.value = 'one'
  h.vm.modelKey.value = '1:same-public-name'
  const previousContext = h.vm.contextKey.value
  h.vm.channel.value = '2'
  h.vm.modelKey.value = '2:same-public-name'
  await h.vm.saved('1:same-public-name', previousContext)
  assert.equal(h.calls.length, 1)
  assert.equal(h.vm.selected.value.channel_id, 2)
  h.vm.provider.value = 'two'
  h.vm.modelKey.value = '3:same-public-name'
  await h.vm.saved('1:same-public-name', previousContext)
  assert.equal(h.calls.length, 1)
  assert.equal(h.vm.selected.value.provider, 'two')
  assert.equal(h.vm.notice.value, '')
})

test('editing a token price submits every hidden price as the original decimal string', async t => {
  const original = row()
  const h = await harness(t, Generic, { row: original, contextKey: 'A' }, () => ({}))
  assert.deepEqual([...h.vm.primaryFields.value], ['input_usd_per_1m', 'cached_input_usd_per_1m', 'output_usd_per_1m'])
  h.vm.draft.value.output_usd_per_1m = '9.87654321'
  h.vm.preparePreview()
  await h.vm.save()
  assert.deepEqual(h.calls[0].body, { ...original.generic_price, output_usd_per_1m: '9.87654321' })
  assert.equal(original.generic_price.output_usd_per_1m, '2.23456789', 'editing must not mutate the catalog cache')
  assert.equal(h.events.filter(event => event[0] === 'saved').length, 1)
})

test('late save cannot overwrite new model context, emit saved, or unlock its request', async t => {
  const late = deferred(), current = deferred()
  const h = await harness(t, Generic, { row: row(), contextKey: 'A' }, () => late.promise)
  h.vm.draft.value.output_usd_per_1m = '3'; h.vm.preparePreview()
  const previous = h.vm.save()
  h.props.row = { ...row('2:next'), model: 'next', generic_price: price('next') }
  h.props.contextKey = 'B'
  h.respond = () => current.promise
  h.vm.draft.value.output_usd_per_1m = '4'; h.vm.preparePreview()
  const next = h.vm.save()
  late.resolve({}); await previous
  assert.equal(h.vm.busy.value, true)
  assert.equal(h.vm.notice.value, '')
  assert.deepEqual(h.events.filter(event => event[0] === 'saved'), [])
  current.resolve({}); await next
  assert.equal(h.calls[1].body.model, 'next')
  assert.equal(h.events.filter(event => event[0] === 'saved').length, 1)
})

test('image form shows image units and unknown models keep all existing fields without guessing', async t => {
  const h = await harness(t, Generic, { row: { ...row(), pricing_kind: 'image' }, contextKey: 'A' })
  assert.deepEqual([...h.vm.primaryFields.value], ['image_usd'])
  h.props.row = { ...row(), pricing_kind: 'unknown' }
  assert.equal(h.vm.primaryFields.value.length, 9)
  assert.equal(h.vm.draft.value.audio_output_usd_per_1m, '0.6')
  h.props.row = { ...row(), pricing_kind: 'unknown', generic_price: null }
  assert.equal(h.vm.canSave.value, false)
  await h.vm.save()
  assert.equal(h.calls.length, 0, 'an unknown unpriced model must not become free by an empty save')
})

test('catalog filters use billing descriptors and channel state while retaining upstream search', async t => {
  const data = catalog()
  data.models[0].pricing = { billing_kind: 'image', currency: 'USD', unit: 'USD/image', status: 'published', rates: [{ key: 'image_usd', amount: '0.125', unit: 'USD/image' }], combinations: [], endpoints: ['/v1/images/generations'] }
  data.models[1].pricing = { billing_kind: 'token', currency: 'USD', unit: 'USD/1M_tokens', status: 'published', rates: [], combinations: [], endpoints: [] }
  data.models[2].pricing = { billing_kind: 'video', currency: 'USD', unit: 'USD/1M_video_tokens', status: 'blocked', rates: [], combinations: [], endpoints: [] }
  const h = await harness(t, Panel, {}, () => data)
  h.vm.kind.value = 'image'
  assert.deepEqual(h.vm.rows.value.map(row => row.key), ['1:same-public-name'])
  h.vm.kind.value = ''; h.vm.status.value = 'disabled'
  assert.deepEqual(h.vm.rows.value.map(row => row.key), ['2:same-public-name'])
  h.vm.status.value = ''; h.vm.query.value = 'two-upstream'
  assert.deepEqual(h.vm.rows.value.map(row => row.key), ['3:same-public-name'])
  assert.ok(h.calls.every(call => call.method === 'GET'))
})

test('model links and filters preserve unrelated URL state and resolve real channel identities', async t => {
  const h = await harness(t, Panel)
  h.vm.provider.value = 'one'; h.vm.view.value = 'list'; h.vm.query.value = 'same-public'
  h.vm.openModel(h.vm.catalog.value.models[1])
  await vue.nextTick()
  assert.equal(h.route.query.tab, 'prices')
  assert.equal(h.route.query.pricing_model, '2:same-public-name')
  assert.equal(h.route.query.pricing_view, 'list')
  h.route.query = { tab: 'prices', pricing_provider: 'two', pricing_model: '3:same-public-name', pricing_view: 'cards' }
  await vue.nextTick()
  assert.equal(h.vm.selected.value.provider, 'two')
  assert.equal(h.vm.detailsOpen.value, true)
  h.route.query = { tab: 'prices', pricing_model: '999:invented-model' }
  await vue.nextTick()
  assert.equal(h.vm.selected.value, null)
  assert.equal(h.vm.detailsOpen.value, false)
})

test('dirty drawer rejects close and navigation until explicitly discarded, and busy cannot close', async t => {
  const h = await harness(t, Panel)
  h.vm.openModel(h.vm.catalog.value.models[0]); await vue.nextTick()
  h.vm.dirty.value = true
  await h.vm.requestClose(false)
  assert.equal(h.vm.detailsOpen.value, true)
  assert.equal(h.vm.confirmDiscard.value, true)
  assert.equal(h.guards[0](), false)
  h.route.query = { tab: 'prices', pricing_model: '3:same-public-name' }
  await vue.nextTick()
  assert.equal(h.vm.selected.value.key, '1:same-public-name')
  assert.equal(h.vm.dirty.value, true)
  h.vm.editorBusy.value = true; h.confirmResult = true
  assert.equal(h.guards[0](), false)
  await h.vm.requestClose(false)
  assert.equal(h.vm.detailsOpen.value, true)
  h.vm.editorBusy.value = false
  h.vm.closeImmediately()
  assert.equal(h.vm.selected.value, null)
  assert.equal(h.vm.dirty.value, false)
})

test('generic publish requires a fresh exact preview and valid decimal strings', async t => {
  const h = await harness(t, Generic, { row: row(), contextKey: 'A' }, () => ({}))
  h.vm.draft.value.output_usd_per_1m = '9.12345678'
  await h.vm.save()
  assert.equal(h.calls.length, 0, 'editing without preview cannot publish')
  h.vm.preparePreview()
  h.vm.draft.value.output_usd_per_1m = '8.12345678'
  assert.equal(h.vm.preview.value, null)
  await h.vm.save()
  assert.equal(h.calls.length, 0, 'an edit invalidates the preceding preview')
  h.vm.draft.value.output_usd_per_1m = '-1'
  assert.equal(h.vm.canSave.value, false)
  h.vm.draft.value.output_usd_per_1m = '0.123456789'
  assert.equal(h.vm.canSave.value, false)
  h.vm.draft.value.output_usd_per_1m = '0.00000001'
  h.vm.preparePreview(); await h.vm.save()
  assert.equal(h.calls[0].body.output_usd_per_1m, '0.00000001')
  assert.equal(h.calls[0].body.image_usd, '0.11111111')
})

test('video ranges compare exact prices within one task mode and never invent legacy video prices', () => {
  const video = { ...row(), video_model: 'seedance-2.0', pricing_kind: 'video' }
  assert.deepEqual(presentation.displayRates(video), [])
  assert.equal(presentation.videoPriceRange(video), null)
  video.pricing = { billing_kind: 'video', currency: 'USD', unit: 'USD/1M_video_tokens', status: 'published', rates: [], endpoints: [], combinations: [
    { mode: 'generate', resolution: '480p', service_tier: 'default', draft: false, amount: '9007199254740993.00000001' },
    { mode: 'generate', resolution: '720p', service_tier: 'default', draft: false, amount: '9007199254740993.00000002' },
    { mode: 'extend', resolution: '480p', service_tier: 'default', draft: false, amount: '0.01' },
    { mode: 'generate', resolution: '480p', service_tier: 'default', draft: true, amount: '0.001' },
  ] }
  assert.deepEqual(presentation.videoPriceRange(video), { min: '9007199254740993.00000001', max: '9007199254740993.00000002', count: 2 })
  const data = catalog(); data.channels[0].status = 2
  assert.equal(presentation.pricingStatus(video, data), 'disabled', 'auto-disabled channels cannot be shown as published')
})

test('official catalog scoped to standard 2.0 excludes 1.x, Fast and Mini original rows and views', async t => {
  const headers = ['series', 'cost'], columns = [{ semantic: 'series' }, { semantic: 'supplier_token_price' }]
  const data = { tables: [
    { id: 'v1', series: ['1.5'], headers, columns, raw_rows: [['1.5', '1']] },
    { id: 'v2', series: ['2.0', '2.0fast', '2.0mini'], headers, columns, raw_rows: [['2.0', '46'], ['2.0fast', '37'], ['2.0mini', '23']], captures: ['2.0', '2.0fast', '2.0mini'].map((series, index) => ({ view_id: series, title: series, first_row: index + 1, expected_rows: 1, headers, columns })) },
    { id: 'v25', series: ['2.5'], headers, columns, raw_rows: [['2.5', '70']] },
  ] }
  const h = await harness(t, Official, { allowedSeries: ['2.0'] }, () => data)
  assert.deepEqual([...h.vm.seriesOptions.value], ['2.0'])
  assert.deepEqual(h.vm.rows.value.map(row => row.cells), [['2.0', '46']])
  assert.deepEqual(h.vm.visibleCaptures.value.map(c => c.view_id), ['2.0'])
  for (const [series, amount] of [['2.0fast', '37'], ['2.0mini', '23']]) {
    h.props.allowedSeries = [series]
    await vue.nextTick()
    assert.deepEqual([...h.vm.seriesOptions.value], [series])
    assert.deepEqual(h.vm.rows.value.map(row => row.cells), [[series, amount]])
    assert.deepEqual(h.vm.visibleCaptures.value.map(c => c.view_id), [series])
  }
  h.props.allowedSeries = ['2.5']
  await vue.nextTick()
  assert.deepEqual(h.vm.rows.value.map(row => row.cells), [['2.5', '70']])
  assert.ok(h.calls.every(c => c.method === 'GET'))
})

function variantRow(variant = 'fast') {
  const model = `seedance-2.0-${variant}`
  return {...row(`1:${model}`), model, upstream_model: variant === 'fast' ? 'doubao-seedance-2-0-fast-260128' : 'doubao-seedance-2-0-mini-260615', video_model: model, pricing_kind: 'video', generic_price: null, unsupported_reason: 'official_video_configuration_missing', pricing: {billing_kind: 'video', currency: 'USD', unit: 'USD/1M_video_tokens', status: 'blocked', rates: [], combinations: [], endpoints: []}, official_price_evidence: {
    status: 'verified', model, verified_at: '2026-10-19T00:00:00Z', unit: 'CNY/1M_tokens',
    prices: ['480p','720p'].flatMap(resolution => [false,true].map(input_video => ({resolution,input_video,supplier_cny_per_1m: input_video ? '22.00000001' : '37.12345678'}))),
    sources: [{id: `${variant}-source`, url: 'https://example.com/official-prices', sha256: 'a'.repeat(64)}],
  }}
}

test('unconfigured Fast and Mini open official defaults while keeping their reference-only status', async t => {
  const data = {...catalog(), models: [variantRow('fast'),variantRow('mini')]}
  const h = await harness(t, Panel, {}, () => data)
  for (const item of h.vm.catalog.value.models) {
    assert.equal(presentation.pricingStatus(item, data), 'blocked')
    assert.equal(h.vm.statusLabel(item), 'pricingAdmin.configurationRequired')
    assert.deepEqual(presentation.displayRates(item), [])
    assert.equal(presentation.videoPriceRange(item), null)
    h.vm.openModel(item, 'pricing')
    assert.equal(h.vm.drawerTab.value, 'pricing')
    assert.deepEqual([...h.vm.drawerTabs.value], ['pricing', 'overview'])
    assert.equal(h.vm.selectedDefaultsModel.value, item.model)
    assert.equal(h.vm.selectedVariantPlanAvailable.value, false)
    assert.equal(h.vm.selectedOfficialEvidence.value.model, item.model)
    assert.equal(h.vm.selectedOfficialEvidence.value.prices.length, 4)
    assert.equal(h.vm.selectedOfficialEvidence.value.unit, 'CNY/1M_tokens')
    assert.equal(h.vm.selected.value.video_model, item.model)
  }
  h.route.query = {tab: 'prices', pricing_model: '1:seedance-2.0-fast'}
  await vue.nextTick()
  assert.equal(h.vm.drawerTab.value, 'pricing')
  assert.equal(h.vm.selectedOfficialEvidence.value.model, 'seedance-2.0-fast')
  assert.equal(h.vm.summary.value.published, 0)
  assert.ok(h.calls.every(call => call.method === 'GET'))
})

test('official cost summaries preserve exact CNY evidence and never invent USD prices or unverified evidence', async t => {
  const variant = variantRow()
  const h = await harness(t, Summary, {row: variant, expanded: false})
  assert.equal(h.vm.evidence.value.prices[0].supplier_cny_per_1m, '37.12345678')
  assert.equal(h.vm.range.value, null)
  assert.deepEqual([...h.vm.rates.value], [])
  h.props.row = {...variant, official_price_evidence: {...variant.official_price_evidence, unit: 'USD/1M_tokens'}}
  assert.equal(h.vm.evidence.value, null)
  h.props.row = {...variant, official_price_evidence: {...variant.official_price_evidence, status: 'pending'}}
  assert.equal(h.vm.evidence.value, null)
  h.props.row = {...variant, official_price_evidence: undefined}
  assert.equal(h.vm.evidence.value, null)
  assert.equal(h.calls.length, 0)
})

test('standard unconfigured and mismatched channels show official defaults; unknown upstream variants do not', async t => {
  const data = {...catalog(), models: [
    {...variantRow(), key: '1:standard', video_model: 'seedance-2.0', official_price_evidence: undefined},
    {...variantRow(), key: '1:2.5', video_model: 'seedance-2.5', official_price_evidence: undefined},
    {...variantRow(), key: '1:mismatch', unsupported_reason: 'official_video_channel_mismatch', video_configuration: {channel_id: 99, provider_scope_id: 7, config_version: 'foreign', pricing_version: 'foreign', enabled: true}},
  ]}
  const h = await harness(t, Panel, {}, () => data)
  for (const item of data.models) {
    h.vm.openModel(item)
    assert.equal(h.vm.selectedDefaultsModel.value, item.video_model)
    assert.equal(h.vm.selectedVideoConfigured.value, false)
    assert.equal(h.vm.selectedVariantPlanAvailable.value, false)
    assert.equal(h.vm.drawerTab.value, 'pricing')
  }
  assert.equal(h.vm.officialModel({...row(), provider: 'modelark', upstream_model: 'doubao-seedance-2-0-260128'}), 'seedance-2.0')
  assert.equal(h.vm.officialModel({...row(), provider: 'volcengine', upstream_model: 'doubao-seedance-2-5-260628'}), 'seedance-2.5')
  assert.equal(h.vm.officialModel({...row(), provider: 'modelark', upstream_model: 'doubao-seedance-2-0-fast-unknown'}), '')
  assert.equal(h.vm.officialModel({...row(), upstream_model: 'doubao-seedance-2-0-260128'}), '')
  assert.ok(h.calls.every(call => call.method === 'GET'))
})

test('variant plan entry requires an enabled standard configuration on the same channel', async t => {
  const fast = variantRow()
  const standard = {...configuredVariant(), key: '1:standard', video_model: 'seedance-2.0', official_price_evidence: undefined}
  const h = await harness(t, Panel, {}, () => ({...catalog(), models: [fast, standard]}))
  h.vm.openModel(fast)
  assert.equal(h.vm.selectedVariantPlanAvailable.value, true)
  const bound = h.vm.catalog.value.models[1]
  bound.channel_id = 2
  assert.equal(h.vm.selectedVariantPlanAvailable.value, false)
  bound.channel_id = 1; bound.video_configuration.enabled = false
  assert.equal(h.vm.selectedVariantPlanAvailable.value, false)
  bound.video_configuration.enabled = true; bound.unsupported_reason = 'official_video_channel_mismatch'
  assert.equal(h.vm.selectedVariantPlanAvailable.value, false)
  assert.ok(h.calls.every(call => call.method === 'GET'))
})

function defaultCandidates(model = 'seedance-2.0', channelId = 1) {
  return {model, channel_id: channelId, catalog_version: 'captured-v1', config: {channel_id: 99, provider_scope_id: 7, config_version: 'existing-config'}, prices: [false, true].map(input_video => ({
    mode: 'generate', resolution: '720p', input_video, service_tier: 'default', draft: false,
    key: `generate/720p/${input_video}/default/false`, unit: 'CNY/1M_tokens', precision: 'exact',
    supplier_cny_per_1m: input_video ? '22.123456789012' : '46', retail_usd_per_1m: '99', risk_multiplier: '4', enabled: true, configured: true,
  }))}
}

function initialSetup(model = 'seedance-2.0', channel = 1) {
  const cells = defaultCandidates(model, channel).prices.map(cell => ({...cell, minimum_rule_version: cell.input_video ? 'verified-minimum-v1' : '', available: true}))
  return {model, channel_id: channel, channel_name: 'fixture-channel', configuration_exists: false, available: true, blockers: [], provider_scopes: [], templates: [{version: 'template-v1', model, label: 'Verified template', body_sha256: 'a'.repeat(64), fx_usd_per_cny: '0.15', fx_version: 'historical-fx', fx_observed_at: '2026-09-01T00:00:00Z', minimum_rule_version: 'verified-minimum-v1', minimum_cell_count: 56, cells}]}
}
function supplierBudgets() {
  return ['day', 'month'].map((period, index) => ({id: index + 1, model: '', metric: 'cny', period, timezone: 'Asia/Shanghai', limit_amount: index ? '200' : '20', limit_units: null, version: 'budget-v1'}))
}
function supplierScope() { return {id: 7, account_ref: '111222333', project_name: 'production-project', region: 'cn-beijing', enabled: true, fx_usd_per_cny: '0.125', fx_version: 'scope-fx', fx_observed_at: '2026-10-01T00:00:00Z', supplier_budgets: supplierBudgets()} }
function initialPreview(body, model = 'seedance-2.0') {
  return {model, channel_id: body.channel_id, pricing_mode: body.pricing_mode, publishable: true, live_tested: false, config_version: `initial-config-${body.channel_id}`, pricing_version: `initial-pricing-${body.channel_id}`, template_ref: {version: body.template_version, model, sha256: 'a'.repeat(64)}, minimum_rule_version: 'verified-minimum-v1', minimum_cell_count: 56, provider_scope: body.provider_scope_id ? supplierScope() : {...supplierScope(), id: 0, account_ref: body.account_ref, project_name: body.project_name, fx_usd_per_cny: body.fx_usd_per_cny.replace(/0+$/, '')}, supplier_budgets: supplierBudgets(), enabled: body.enabled, cells: initialSetup(model).templates[0].cells.map(cell => ({...cell, retail_usd_per_1m: body.pricing_mode === 'official' ? '5.75' : cell.retail_usd_per_1m, risk_multiplier: body.pricing_mode === 'official' ? '1' : cell.risk_multiplier})), publication: {opaque: 'must-return-this-exactly', configuration: {Version: `initial-config-${body.channel_id}`}, proof: 'server-only-context'}}
}
function initialReadback(channel = 1) { return {model: 'seedance-2.0', channel_id: channel, config_version: `initial-config-${channel}`, pricing_version: `initial-pricing-${channel}`} }
function initialResult(channel = 1) { return {public_model: 'seedance-2.0', version: `initial-config-${channel}`, pricing_version: `initial-pricing-${channel}`, provider_scope_id: 7} }
function completeInitialForm(h) { Object.assign(h.vm.form.value, {account: '111222333', project: 'production-project', daily: '20', monthly: '200', fx: '0.1250', fxConfirmed: true, identityConfirmed: true}) }
async function initialHarness(t, setup = initialSetup()) {
  return harness(t, InitialSetup, {setup, model: setup.model, channelId: setup.channel_id, contextKey: 'A'}, call => call.path.endsWith('/initial-preview') ? initialPreview(call.body) : call.method === 'GET' ? initialReadback() : initialResult())
}

test('a genuinely unconfigured video channel opens first-time setup and never calls the rebind preview', async t => {
  const setup = initialSetup()
  const h = await harness(t, OfficialDefaults, {model: setup.model, channelId: 1, contextKey: 'A'}, call => call.path.includes('initial-setup') ? setup : {...defaultCandidates(), config: null})
  assert.equal(h.vm.initialSetup.value.model, setup.model)
  assert.equal(h.vm.canPreview.value, false)
  await h.vm.previewPublication()
  assert.deepEqual(h.calls.map(call => call.path), ['/api/admin/video/models/seedance-2.0/pricing/reference-candidates?channel_id=1', '/api/admin/video/models/seedance-2.0/pricing/initial-setup?channel_id=1'])
  assert.ok(h.calls.every(call => call.method === 'GET'))
})

test('first-time fields stay empty, require explicit account/FX confirmation, and preview canonical decimal FX', async t => {
  const h = await initialHarness(t)
  assert.equal(h.vm.form.value.account, '')
  assert.equal(h.vm.form.value.project, '')
  assert.equal(h.vm.form.value.daily, '')
  assert.equal(h.vm.form.value.monthly, '')
  assert.equal(h.vm.form.value.enabled, false)
  assert.equal(h.vm.form.value.fx, '0.15', 'only the labelled historical FX reference may be prefilled')
  await h.vm.preparePreview(); assert.equal(h.calls.length, 0)
  completeInitialForm(h)
  h.vm.form.value.fxConfirmed = false
  assert.equal(h.vm.canPreview.value, false)
  h.vm.form.value.fxConfirmed = true
  await h.vm.preparePreview()
  assert.equal(h.calls.length, 1)
  assert.deepEqual(h.calls[0].body, {channel_id: 1, template_version: 'template-v1', pricing_mode: 'official', provider_scope_id: 0, enabled: false, account_ref: '111222333', project_name: 'production-project', fx_usd_per_cny: '0.1250', daily_budget_cny: '20', monthly_budget_cny: '200'})
  assert.equal(h.vm.error.value, '')
  assert.equal(h.vm.preview.value.provider_scope.fx_usd_per_cny, '0.125')
  assert.equal(h.vm.canPublish.value, false, 'a fresh preview still needs publication confirmation')
  assert.ok(h.vm.preview.value.cells.every(cell => cell.risk_multiplier === '1'))
})

test('existing account scopes reuse saved policies without sending manual budgets, identity or FX fields', async t => {
  const setup = initialSetup(); setup.provider_scopes = [supplierScope()]
  const h = await initialHarness(t, setup)
  completeInitialForm(h); h.vm.form.value.scopeId = 7; h.vm.form.value.fxConfirmed = false
  await h.vm.preparePreview()
  assert.deepEqual(h.calls[0].body, {channel_id: 1, template_version: 'template-v1', pricing_mode: 'official', provider_scope_id: 7, enabled: false})
  assert.equal(h.vm.preview.value.provider_scope.id, 7)
  assert.equal(h.vm.budgetAmount({...supplierBudgets()[0], metric: 'usd', limit_amount: '15'}), '15 USD')
  assert.equal(h.vm.budgetAmount({...supplierBudgets()[0], metric: 'tokens', limit_amount: null, limit_units: 1000}), '1000 Token')
  assert.equal(h.vm.budgetAmount({...supplierBudgets()[0], metric: 'tasks', limit_amount: null, limit_units: 5}), '5 pricingAdmin.initialTasks')
})

test('changing a first-time form invalidates its preview and template mode preserves historical reserve policy', async t => {
  const h = await initialHarness(t); completeInitialForm(h)
  await h.vm.preparePreview(); const firstKey = h.vm.idempotencyKey.value
  h.vm.confirmed.value = true; h.vm.form.value.mode = 'template'
  assert.equal(h.vm.preview.value, null); assert.equal(h.vm.confirmed.value, false)
  assert.equal(h.vm.canPublish.value, false)
  await h.vm.preparePreview()
  assert.notEqual(h.vm.idempotencyKey.value, firstKey)
  assert.ok(h.vm.preview.value.cells.every(cell => cell.risk_multiplier === '4' && cell.retail_usd_per_1m === '99'))
  h.vm.form.value.enabled = true
  assert.equal(h.vm.preview.value, null)
})

test('first-time publication posts the opaque preview unchanged and confirms versions by readback', async t => {
  const h = await initialHarness(t); completeInitialForm(h)
  await h.vm.preparePreview(); h.vm.confirmed.value = true
  const expected = JSON.parse(h.vm.snapshot.value), key = h.vm.idempotencyKey.value, late = deferred()
  h.respond = call => call.method === 'GET' ? late.promise : initialResult()
  const pending = h.vm.publish()
  await until(() => h.calls.some(call => call.path.endsWith('/pricing')))
  assert.equal(h.events.filter(event => event[0] === 'published').length, 0)
  const post = h.calls.find(call => call.path === '/api/admin/video/configurations')
  assert.deepEqual(post.body, expected); assert.equal(post.options.headers['Idempotency-Key'], key)
  late.resolve(initialReadback()); await pending
  assert.deepEqual(h.events.filter(event => event[0] === 'published'), [['published', 'seedance-2.0', 1, 'A']])
  assert.equal(h.vm.retryPending.value, false); assert.equal(h.vm.dirty.value, false)
})

test('a lost publication response is reconciled by exact readback without repeating the write', async t => {
  const h = await initialHarness(t); completeInitialForm(h)
  await h.vm.preparePreview(); h.vm.confirmed.value = true
  h.respond = call => { if (call.method === 'POST') throw new Error('response lost'); return initialReadback() }
  await h.vm.publish()
  assert.equal(h.calls.filter(call => call.path === '/api/admin/video/configurations').length, 1)
  assert.equal(h.events.filter(event => event[0] === 'published').length, 1)
  assert.equal(h.vm.retryPending.value, false)
})

test('an unconfirmed initial publication locks edits and retries the identical body/key after checking saved versions', async t => {
  const h = await initialHarness(t); completeInitialForm(h)
  await h.vm.preparePreview(); h.vm.confirmed.value = true
  h.respond = () => { throw new Error('network unavailable') }
  await h.vm.publish()
  assert.equal(h.vm.retryPending.value, true); assert.equal(h.vm.locked.value, true); assert.equal(h.vm.canPreview.value, false)
  const first = h.calls.find(call => call.path === '/api/admin/video/configurations')
  let reads = 0
  h.respond = call => { if (call.method === 'GET') return ++reads === 1 ? {...initialReadback(), config_version: 'other-config'} : initialReadback(); return initialResult() }
  const start = h.calls.length
  await h.vm.publish()
  assert.equal(h.calls[start].method, 'GET')
  const posts = h.calls.filter(call => call.path === '/api/admin/video/configurations')
  assert.equal(posts.length, 2); assert.deepEqual(posts[1].body, first.body); assert.deepEqual(posts[1].options, first.options)
  assert.equal(h.events.filter(event => event[0] === 'published').length, 1)
})

test('initial publication conflicts explain the missing condition and require a fresh setup', async t => {
  const h = await initialHarness(t); completeInitialForm(h)
  await h.vm.preparePreview(); h.vm.confirmed.value = true
  h.respond = call => { if (call.method === 'POST') throw new h.ApiError(422, 'opaque technical reason', null, 'initial_preview_expired'); return {...initialReadback(), config_version: 'not-this-operation'} }
  await h.vm.publish()
  assert.equal(h.vm.error.value, 'pricingAdmin.initialBlockExpired')
  assert.equal(h.vm.requiresReload.value, true); assert.equal(h.vm.canPublish.value, false)
  assert.equal(h.events.filter(event => event[0] === 'published').length, 0)
})

test('late first-time previews cannot populate another channel or authorization context', async t => {
  const h = await initialHarness(t), late = deferred(); completeInitialForm(h)
  h.respond = call => late.promise
  const previous = h.vm.preparePreview(), body = h.calls[0].body
  h.props.channelId = 2; h.props.setup = initialSetup('seedance-2.0', 2); h.props.contextKey = 'B'
  late.resolve(initialPreview(body)); await previous
  assert.equal(h.vm.preview.value, null); assert.equal(h.vm.busy.value, false); assert.equal(h.vm.form.value.account, '')
  completeInitialForm(h); h.auth.isSuperadmin = false
  await h.vm.preparePreview()
  assert.equal(h.calls.length, 1)
})

test('late first-time publication and readback cannot report success after channel change or deactivation', async t => {
  const h = await initialHarness(t), late = deferred(); completeInitialForm(h)
  await h.vm.preparePreview(); h.vm.confirmed.value = true
  h.respond = () => late.promise
  const previous = h.vm.publish()
  h.props.contextKey = 'B'; h.deactivated.forEach(fn => fn())
  late.resolve(initialResult()); await previous
  assert.equal(h.vm.preview.value, null); assert.equal(h.vm.busy.value, false)
  assert.equal(h.events.filter(event => event[0] === 'published').length, 0)
})

for (const model of ['seedance-2.0', 'seedance-2.5', 'seedance-2.0-fast', 'seedance-2.0-mini']) {
  test(`${model} defaults request the selected channel and display only captured CNY plus multiplier one`, async t => {
    const result = defaultCandidates(model, 3)
    // Even stale joined commercial values cannot become this channel's defaults.
    result.config = {channel_id: 99, provider_scope_id: 9, pricing_version: 'another-account'}
    const h = await harness(t, OfficialDefaults, {model, channelId: 3, contextKey: 'A'}, () => result)
    assert.equal(h.vm.error.value, '')
    assert.equal(h.vm.loading.value, false)
    assert.equal(h.vm.catalogVersion.value, 'captured-v1')
    assert.deepEqual(h.calls.map(call => [call.method, call.path]), [['GET', `/api/admin/video/models/${model}/pricing/reference-candidates?channel_id=3`]])
    assert.deepEqual(JSON.parse(JSON.stringify(h.vm.prices.value)), [
      {key: 'generate/720p/false/default/false', resolution: '720p', input_video: false, supplier_cny_per_1m: '46', risk_multiplier: '1'},
      {key: 'generate/720p/true/default/false', resolution: '720p', input_video: true, supplier_cny_per_1m: '22.123456789012', risk_multiplier: '1'},
    ])
    assert.equal(h.events.length, 0, 'loading defaults must not report a saved or published result')
  })
}

test('official defaults filter nonstandard references and reject malformed or ambiguous amounts', async t => {
  const h = await harness(t, OfficialDefaults, {model: 'seedance-2.0', channelId: 1, contextKey: 'A'}, () => defaultCandidates())
  const source = defaultCandidates()
  for (const fields of [{mode: 'extend'}, {draft: true}, {service_tier: 'flex'}, {unit: 'USD/1M_tokens'}, {precision: 'rounded'}, {resolution: 'unknown'}]) source.prices.push({...source.prices[0], ...fields})
  h.respond = () => source
  await h.vm.load()
  assert.equal(h.vm.prices.value.length, 2)
  for (const change of [
    value => { value.model = 'seedance-2.5' },
    value => { value.channel_id = 2 },
    value => { value.prices.push({...value.prices[0]}) },
    value => { value.prices[0].supplier_cny_per_1m = '1e2' },
  ]) {
    const invalid = defaultCandidates(); change(invalid)
    h.respond = () => invalid
    await h.vm.load()
    assert.equal(h.vm.error.value, 'pricingAdmin.officialDefaultsFailed')
    assert.deepEqual(h.vm.prices.value, [])
    assert.equal(h.vm.catalogVersion.value, '')
  }
  h.respond = () => ({...defaultCandidates(), prices: []})
  await h.vm.load()
  assert.equal(h.vm.error.value, '')
  assert.deepEqual(h.vm.prices.value, [])
  assert.ok(h.calls.every(call => call.method === 'GET'))
})

test('late official defaults cannot replace the currently selected model or channel', async t => {
  const h = await harness(t, OfficialDefaults, {model: 'seedance-2.0-fast', channelId: 1, contextKey: 'A'}, () => defaultCandidates('seedance-2.0-fast'))
  const late = deferred()
  h.respond = call => call.path.includes('seedance-2.0-mini') ? defaultCandidates('seedance-2.0-mini', 2) : late.promise
  const pending = h.vm.load()
  assert.equal(h.vm.loading.value, true)
  assert.deepEqual(h.vm.prices.value, [])
  h.props.channelId = 2
  h.props.model = 'seedance-2.0-mini'
  await until(() => !h.vm.loading.value)
  late.resolve(defaultCandidates('seedance-2.0-fast')); await pending
  assert.equal(h.vm.catalogVersion.value, 'captured-v1')
  assert.equal(h.vm.error.value, '')
  assert.equal(h.calls.at(-1).path, '/api/admin/video/models/seedance-2.0-mini/pricing/reference-candidates?channel_id=2')
  assert.equal(h.vm.prices.value.length, 2)
  assert.ok(h.calls.every(call => call.method === 'GET'))
})

test('official defaults clear on access loss and deactivation and ignore late failures', async t => {
  const h = await harness(t, OfficialDefaults, {model: 'seedance-2.0', channelId: 1, contextKey: 'A'}, () => defaultCandidates())
  const late = deferred()
  h.respond = () => late.promise
  const pending = h.vm.load()
  h.auth.isSuperadmin = false
  late.reject(new Error('old context failed')); await pending
  assert.equal(h.vm.allowed.value, false)
  assert.equal(h.vm.loading.value, false)
  assert.equal(h.vm.error.value, '')
  assert.deepEqual(h.vm.prices.value, [])
  const before = h.calls.length
  await h.vm.load()
  assert.equal(h.calls.length, before)
  h.respond = () => defaultCandidates()
  h.auth.isSuperadmin = true
  await until(() => !h.vm.loading.value)
  assert.equal(h.vm.prices.value.length, 2)
  h.deactivated.forEach(fn => fn())
  assert.deepEqual(h.vm.prices.value, [])
  await h.vm.load()
  assert.equal(h.calls.length, before + 1)
  assert.ok(h.calls.every(call => call.method === 'GET'))
})

function rebindPreview(model = 'seedance-2.0', channelId = 1, context = 'a'.repeat(64)) {
  const cells = defaultCandidates(model, channelId).prices.map(cell => ({...cell, retail_usd_per_1m: cell.input_video ? '3.5' : '7', risk_multiplier: '1', minimum_rule_version: cell.input_video ? 'minimum-existing' : '', available: true}))
  return {model, current_channel_id: 2, target_channel_id: channelId, target_channel_name: 'new-channel', provider_scope_id: 4,
    config_version: 'old-config', pricing_version: 'old-prices', capability_version: 'kept-capability', fx_usd_per_cny: '0.14', fx_version: 'kept-fx',
    context_sha256: context, catalog_version: 'captured-v1', has_active_credential: true, publishable: true, live_tested: false,
    cells, diff: cells.map(cell => ({key: cell.key, change: 'changed', before: {...cell, risk_multiplier: '4'}, after: cell})), sources: []}
}
function rebindResult(model = 'seedance-2.0', channelId = 1) {
  return {model, channel_id: channelId, publication: {id: 42, model, version: 'new-prices', config_version: 'new-config'}}
}
async function rebindHarness(t) {
  return harness(t, OfficialDefaults, {model: 'seedance-2.0', channelId: 1, contextKey: 'A'}, call => call.path.includes('/reference-candidates') ? defaultCandidates() : call.method === 'GET' ? rebindPreview() : rebindResult())
}

test('inherited publication requires a server preview then posts only its target and context with an idempotency key', async t => {
  const h = await rebindHarness(t)
  await h.vm.publish()
  assert.equal(h.calls.length, 1, 'loading official defaults cannot publish')
  await h.vm.previewPublication()
  assert.equal(h.calls.at(-1).method, 'GET')
  assert.equal(h.calls.at(-1).path, '/api/admin/video/models/seedance-2.0/pricing/rebind-preview?channel_id=1')
  assert.equal(h.vm.preview.value.current_channel_id, 2)
  assert.deepEqual([...h.vm.minimumVersions.value], ['minimum-existing'])
  assert.equal(h.vm.preview.value.diff[0].before.risk_multiplier, '4')
  assert.equal(h.vm.preview.value.diff[0].after.risk_multiplier, '1')
  assert.equal(h.vm.canPublish.value, true)
  assert.equal(h.calls.filter(call => call.method === 'POST').length, 0)
  await h.vm.publish()
  const publication = h.calls.at(-1)
  assert.equal(publication.path, '/api/admin/video/models/seedance-2.0/pricing/rebind-publications')
  assert.deepEqual(publication.body, {channel_id: 1, expected_context_sha256: 'a'.repeat(64)})
  assert.ok(publication.options.headers['Idempotency-Key'])
  assert.equal(h.vm.canPublish.value, false)
  assert.deepEqual(h.events.filter(event => event[0] === 'published'), [['published', 'seedance-2.0', 1, 'A']])
  assert.equal(h.vm.notice.value, 'pricingAdmin.rebindPublished')
  assert.equal(h.events.at(-1)[1], false)
})

test('channel-only repair accepts empty or entirely unchanged price diffs and still publishes', async t => {
  const h = await rebindHarness(t)
  for (const unchangedRows of [false, true]) {
    const preview = rebindPreview()
    preview.diff = unchangedRows ? preview.diff.map(diff => ({...diff, change: 'unchanged', before: {...diff.after}})) : []
    h.respond = call => call.method === 'GET' ? preview : rebindResult()
    await h.vm.previewPublication()
    assert.equal(h.vm.publicationError.value, '')
    assert.equal(h.vm.priceChanges.value.length, 0)
    assert.equal(h.vm.canPublish.value, true)
    await h.vm.publish()
    assert.equal(h.vm.notice.value, 'pricingAdmin.rebindPublished')
  }
  assert.equal(h.calls.filter(call => call.method === 'POST').length, 2)
  assert.equal(h.events.filter(event => event[0] === 'published').length, 2)
})

test('unconfirmed inherited publication retries the identical request and prevents a second concurrent operation', async t => {
  const h = await rebindHarness(t), response = deferred()
  await h.vm.previewPublication()
  h.respond = () => response.promise
  const pending = h.vm.publish()
  const first = h.calls.at(-1)
  await h.vm.publish(); await h.vm.previewPublication(); await h.vm.load()
  assert.equal(h.calls.at(-1), first)
  response.reject(new Error('response lost')); await pending
  assert.equal(h.vm.retryPending.value, true)
  assert.equal(h.vm.canPreview.value, false)
  assert.equal(h.vm.canPublish.value, true)
  await h.vm.previewPublication(); await h.vm.load()
  assert.equal(h.calls.at(-1), first)
  h.respond = () => rebindResult()
  await h.vm.publish()
  const retry = h.calls.at(-1)
  assert.deepEqual(retry.body, first.body)
  assert.deepEqual(retry.options, first.options)
  assert.equal(h.vm.retryPending.value, false)
  assert.equal(h.events.filter(event => event[0] === 'published').length, 1)
})

test('a rebind conflict requires a fresh preview and new operation key', async t => {
  const h = await rebindHarness(t)
  await h.vm.previewPublication()
  const previousKey = h.vm.idempotencyKey.value
  h.respond = () => { throw new h.ApiError(409, 'stale context') }
  await h.vm.publish()
  assert.equal(h.vm.publicationError.value, 'pricingAdmin.rebindConflict')
  assert.equal(h.vm.requiresReload.value, true)
  assert.equal(h.vm.canPublish.value, false)
  const count = h.calls.length
  await h.vm.publish()
  assert.equal(h.calls.length, count)
  h.respond = () => rebindPreview('seedance-2.0', 1, 'b'.repeat(64))
  await h.vm.previewPublication()
  assert.equal(h.vm.canPublish.value, true)
  assert.notEqual(h.vm.idempotencyKey.value, previousKey)
  assert.equal(h.vm.requestSnapshot.value.expected_context_sha256, 'b'.repeat(64))
})

test('inherited publication refuses mismatched previews and treats mismatched success as unconfirmed', async t => {
  const h = await rebindHarness(t)
  for (const fields of [{model: 'seedance-2.5'}, {target_channel_id: 9}, {context_sha256: 'invalid'}, {publishable: false}, {has_active_credential: false}, {cells: []}]) {
    h.respond = () => ({...rebindPreview(), ...fields})
    await h.vm.previewPublication()
    assert.equal(h.vm.preview.value, null)
    assert.equal(h.vm.canPublish.value, false)
    assert.equal(h.vm.publicationError.value, 'pricingAdmin.rebindPreviewFailed')
  }
  h.respond = () => rebindPreview()
  await h.vm.previewPublication()
  h.respond = () => rebindResult('seedance-2.5')
  await h.vm.publish()
  assert.equal(h.vm.retryPending.value, true)
  assert.equal(h.events.filter(event => event[0] === 'published').length, 0)
})

test('late rebind previews cannot populate a different channel or organization', async t => {
  const h = await rebindHarness(t), late = deferred()
  h.respond = call => call.path.includes('/rebind-preview') ? late.promise : defaultCandidates('seedance-2.0', h.props.channelId)
  const pending = h.vm.previewPublication()
  h.props.channelId = 3
  await until(() => !h.vm.loading.value)
  h.auth.orgId = 2
  await until(() => !h.vm.loading.value)
  late.resolve(rebindPreview()); await pending
  assert.equal(h.vm.preview.value, null)
  assert.equal(h.vm.requestSnapshot.value, null)
  assert.equal(h.vm.canPublish.value, false)
  assert.equal(h.vm.busy.value, false)
  assert.equal(h.events.filter(event => event[0] === 'published').length, 0)
})

test('late rebind publication cannot report success or unlock another channel operation', async t => {
  const h = await rebindHarness(t), oldResponse = deferred(), currentResponse = deferred()
  await h.vm.previewPublication()
  h.respond = call => call.method === 'POST' ? oldResponse.promise : defaultCandidates('seedance-2.0', 3)
  const old = h.vm.publish()
  h.props.channelId = 3; h.props.contextKey = 'B'
  await until(() => !h.vm.loading.value)
  h.respond = call => call.method === 'GET' ? rebindPreview('seedance-2.0', 3) : currentResponse.promise
  await h.vm.previewPublication()
  const current = h.vm.publish()
  oldResponse.resolve(rebindResult()); await old
  assert.equal(h.vm.busy.value, true)
  assert.equal(h.events.filter(event => event[0] === 'published').length, 0)
  currentResponse.resolve(rebindResult('seedance-2.0', 3)); await current
  assert.deepEqual(h.events.filter(event => event[0] === 'published'), [['published', 'seedance-2.0', 3, 'B']])
})

test('deactivation invalidates a pending publication without a success event', async t => {
  const h = await rebindHarness(t), late = deferred()
  await h.vm.previewPublication()
  h.respond = () => late.promise
  const pending = h.vm.publish()
  h.deactivated.forEach(fn => fn())
  late.resolve(rebindResult()); await pending
  assert.equal(h.vm.busy.value, false)
  assert.equal(h.vm.preview.value, null)
  assert.equal(h.vm.requestSnapshot.value, null)
  assert.equal(h.events.filter(event => event[0] === 'published').length, 0)
})

test('confirmed inherited publication refreshes its catalog row into the normal configured editor', async t => {
  const missing = {...variantRow(), unsupported_reason: 'official_video_channel_mismatch'}
  const h = await harness(t, Panel, {}, () => ({...catalog(), models: [missing]}))
  h.vm.openModel(missing)
  const context = h.vm.contextKey.value
  const published = {...configuredVariant(), pricing: {...configuredVariant().pricing, status: 'published'}}
  h.respond = () => ({...catalog(), models: [published]})
  await h.vm.defaultsPublished('seedance-2.0-fast', 99, context)
  await h.vm.defaultsPublished('seedance-2.0-mini', 1, context)
  await h.vm.defaultsPublished('seedance-2.0-fast', 1, 'old-context')
  assert.equal(h.calls.length, 1)
  await h.vm.defaultsPublished('seedance-2.0-fast', 1, context)
  assert.equal(h.calls.length, 2)
  assert.equal(h.vm.selectedVideoConfigured.value, true)
  assert.equal(h.vm.selectedDefaultsModel.value, '')
  assert.equal(h.vm.statusLabel(h.vm.selected.value), 'pricingAdmin.published')
  assert.equal(h.vm.summary.value.published, 1)
})

function configuredVariant(variant = 'fast') {
  const item = variantRow(variant)
  return {...item, unsupported_reason: '', video_configuration: {channel_id: item.channel_id, provider_scope_id: 4, config_version: `${variant}-cfg`, pricing_version: `${variant}-prices`, enabled: true}, pricing: {...item.pricing, status: 'partial', combinations: [{key: 'generate/720p/false/default/false', mode: 'generate', resolution: '720p', input_video: false, service_tier: 'default', draft: false, amount: '5.238900001234'}]}}
}

test('configured Fast and Mini use their bound normal price editor and preserve separate official evidence', async t => {
  const data = {...catalog(), models: [configuredVariant('fast'), configuredVariant('mini')]}
  const h = await harness(t, Panel, {}, () => data)
  for (const item of h.vm.catalog.value.models) {
    h.vm.openModel(item, 'pricing')
    assert.equal(h.vm.selectedEvidenceOnly.value, false)
    assert.equal(h.vm.selectedVideoConfigured.value, true)
    assert.equal(h.vm.selectedDefaultsModel.value, '')
    assert.equal(h.vm.drawerTab.value, 'pricing')
    assert.deepEqual([...h.vm.drawerTabs.value], ['pricing','overview'])
    assert.equal(h.vm.selectedOfficialEvidence.value.model, item.video_model)
    assert.equal(h.vm.statusLabel(item), 'pricingAdmin.partial')
    assert.deepEqual(presentation.videoPriceRange(item), {min: '5.238900001234', max: '5.238900001234', count: 1})
    assert.equal(presentation.configuredOfficialVideo({...item, video_configuration: null}), false)
    assert.equal(presentation.configuredOfficialVideo({...item, video_configuration: {...item.video_configuration, channel_id: 99}}), false)
    assert.equal(presentation.configuredOfficialVideo({...item, unsupported_reason: 'official_video_channel_mismatch'}), false)
  }
  assert.ok(h.calls.every(call => call.method === 'GET'), 'opening a configured model does not publish or synchronize it')
  h.auth.isSuperadmin = false
  assert.equal(h.vm.videoAllowed.value, false)
})

test('configured variant summaries use published USD combinations instead of reference CNY costs', async t => {
  const h = await harness(t, Summary, {row: configuredVariant(), expanded: false})
  assert.equal(h.vm.referenceOnly.value, false)
  assert.equal(h.vm.evidence.value.unit, 'CNY/1M_tokens')
  assert.deepEqual(h.vm.range.value, {min: '5.238900001234', max: '5.238900001234', count: 1})
  h.props.row = variantRow()
  assert.equal(h.vm.referenceOnly.value, true)
  assert.equal(h.vm.range.value, null)
})

test('read-only official evidence restricts links to public HTTPS URLs and performs no API calls', async t => {
  const h = await harness(t, VariantEvidence, {evidence: variantRow().official_price_evidence})
  assert.equal(h.vm.sourceURL('https://example.com/official-prices'), 'https://example.com/official-prices')
  for (const url of ['javascript:alert(1)', 'http://example.com/', 'https://user:password@example.com/']) assert.equal(h.vm.sourceURL(url), undefined)
  assert.equal(h.calls.length, 0)
})

function variantPlan(row = variantRow(), saved = false, context = 'b'.repeat(64)) {
  const proposal = {
    fx_usd_per_cny: '0.1412345678', fx_version: 'scope-fx-v1', fx_observed_at: '2026-10-19T01:00:00Z', source_config_version: 'standard-cfg-v1', source_pricing_version: 'standard-price-v1', risk_source: 'existing_standard_2_0_policy', minimum_status: 'unverified',
    prices: row.official_price_evidence.prices.map(price => ({...price, retail_usd_per_1m: '5.238900001234', risk_multiplier: '4'})), sources: row.official_price_evidence.sources, evidence_sha256: 'e'.repeat(64),
  }
  return {channel_id: row.channel_id, model: row.model, upstream_model: row.upstream_model, canonical_model: row.official_price_evidence.model, context_sha256: context, adapter_status: 'supported_configuration_required', invocable: false, proposal, saved: saved ? {id: 8, version: 'plan-v1', created_at: '2026-10-19T02:00:00Z', created_by: 1, proposal: structuredClone(proposal)} : null, saved_matches_proposal: saved}
}

test('new variant plans accept the default multiplier while preserving historical policies', async t => {
  const row = variantRow(), preview = variantPlan(row)
  preview.proposal.risk_source = 'default'
  preview.proposal.prices.forEach(price => { price.risk_multiplier = '1' })
  const h = await harness(t, VariantPlan, {row, contextKey: 'A'}, () => preview)
  assert.equal(h.vm.canSave.value, true)
  assert.ok(h.vm.data.value.proposal.prices.every(price => price.risk_multiplier === '1'))
  assert.equal(h.vm.validProposal(variantPlan(row, true).saved.proposal), true)
  assert.equal(h.vm.validProposal({...preview.proposal, risk_source: 'unknown'}), false)
  assert.equal(h.calls.filter(call => call.method !== 'GET').length, 0)
})

test('variant plan GET is the exact server proposal and saving sends only the reviewed context with idempotency', async t => {
  const row = {...variantRow(), key: '1:public/video fast', model: 'public/video fast'}, preview = variantPlan(row), saved = variantPlan(row, true)
  const h = await harness(t, VariantPlan, {row, contextKey: 'A'}, call => call.method === 'GET' ? preview : saved)
  assert.equal(h.calls.length, 1)
  assert.equal(h.calls[0].path, '/api/admin/video/variant-price-plans/1/public%2Fvideo%20fast')
  assert.deepEqual(JSON.parse(JSON.stringify(h.vm.data.value.proposal)), preview.proposal)
  assert.equal(h.vm.data.value.proposal.prices[0].retail_usd_per_1m, '5.238900001234', 'the browser never recomputes USD from CNY and FX')
  assert.equal(h.vm.minimumLabel(h.vm.data.value.proposal.prices[0]), 'pricingAdmin.variantPlanMinimumNotApplicable')
  assert.equal(h.vm.minimumLabel(h.vm.data.value.proposal.prices[1]), 'pricingAdmin.variantPlanMinimumUnverified')
  assert.equal(h.vm.statusKey.value, 'variantPlanUnsaved')
  assert.equal(h.vm.canSave.value, true)
  const key = h.vm.idempotencyKey.value
  await h.vm.save()
  assert.deepEqual(h.calls[1].body, {expected_context_sha256: preview.context_sha256})
  assert.deepEqual(h.calls[1].options, {headers: {'Idempotency-Key': key}})
  assert.equal(h.vm.statusKey.value, 'variantPlanSavedState')
  assert.equal(h.vm.canSave.value, false)
  assert.equal(h.vm.data.value.invocable, false)
  assert.equal(h.vm.data.value.proposal.minimum_status, 'unverified')
  assert.equal(row.pricing.status, 'blocked')
  assert.deepEqual(h.events.filter(([name]) => name === 'saved'), [['saved', row.key, 'A']])
  assert.ok(h.events.some(([name,value]) => name === 'busy-change' && value))
  await h.vm.save()
  assert.equal(h.calls.length, 2, 'an unchanged saved plan cannot create a second save')
})

test('variant save prevents duplicate clicks and retries an unknown outcome with the original key and body', async t => {
  const row = variantRow(), response = deferred()
  const h = await harness(t, VariantPlan, {row, contextKey: 'A'}, () => variantPlan(row))
  const key = h.vm.idempotencyKey.value
  h.respond = () => response.promise
  const pending = h.vm.save()
  await h.vm.save()
  assert.equal(h.calls.filter(call => call.method === 'POST').length, 1)
  response.reject(new h.ApiError(502, 'unknown result'))
  await pending
  assert.equal(h.vm.retryPending.value, true)
  assert.equal(h.vm.canSave.value, true)
  assert.equal(h.vm.idempotencyKey.value, key)
  const count = h.calls.length
  await h.vm.load()
  assert.equal(h.calls.length, count, 'an unknown outcome keeps its original operation available for retry')
  h.respond = () => variantPlan(row, true)
  await h.vm.save()
  const writes = h.calls.filter(call => call.method === 'POST')
  assert.deepEqual(writes[0].body, writes[1].body)
  assert.deepEqual(writes[0].options, writes[1].options)
  assert.equal(h.vm.retryPending.value, false)
  assert.equal(h.vm.statusKey.value, 'variantPlanSavedState')
})

test('variant context conflict blocks saving until a new GET proposal is reviewed', async t => {
  const row = variantRow(), h = await harness(t, VariantPlan, {row, contextKey: 'A'}, () => variantPlan(row))
  const oldKey = h.vm.idempotencyKey.value
  h.respond = () => { throw new h.ApiError(409, 'context conflict') }
  await h.vm.save()
  assert.equal(h.vm.requiresReload.value, true)
  assert.equal(h.vm.canSave.value, false)
  assert.equal(h.vm.error.value, 'pricingAdmin.variantPlanConflict')
  await h.vm.save()
  assert.equal(h.calls.filter(call => call.method === 'POST').length, 1)
  h.respond = () => variantPlan(row, false, 'c'.repeat(64))
  await h.vm.load()
  assert.equal(h.vm.requiresReload.value, false)
  assert.equal(h.vm.canSave.value, true)
  assert.notEqual(h.vm.idempotencyKey.value, oldKey)
  assert.equal(h.vm.requestSnapshot.value.expected_context_sha256, 'c'.repeat(64))
})

test('a late variant proposal cannot replace the current model, channel or authentication context', async t => {
  const fast = variantRow(), mini = {...variantRow('mini'), key: '3:mini-alias', channel_id: 3, model: 'mini-alias'}, late = deferred()
  const h = await harness(t, VariantPlan, {row: fast, contextKey: 'A'}, () => variantPlan(fast))
  h.respond = call => call.path.endsWith('/mini-alias') ? variantPlan(mini) : late.promise
  const previous = h.vm.load()
  h.props.row = mini; h.props.contextKey = 'B'
  await until(() => !h.vm.loading.value && h.vm.data.value?.model === 'mini-alias')
  late.resolve(variantPlan(fast))
  await previous
  assert.equal(h.vm.data.value.channel_id, 3)
  assert.equal(h.vm.data.value.canonical_model, 'seedance-2.0-mini')
  assert.equal(h.vm.error.value, '')
  h.auth.isSuperadmin = false
  assert.equal(h.vm.data.value, null)
  assert.equal(h.vm.requestSnapshot.value, null)
  const count = h.calls.length
  await h.vm.load(); await h.vm.save()
  assert.equal(h.calls.length, count)
})

test('late variant save cannot emit success or unlock another model save', async t => {
  const fast = variantRow(), mini = variantRow('mini'), previous = deferred(), current = deferred()
  const h = await harness(t, VariantPlan, {row: fast, contextKey: 'A'}, () => variantPlan(fast))
  h.respond = call => call.method === 'GET' ? variantPlan(mini) : call.path.endsWith('/seedance-2.0-fast') ? previous.promise : current.promise
  const oldSave = h.vm.save()
  h.props.row = mini; h.props.contextKey = 'B'
  await until(() => !h.vm.loading.value && h.vm.data.value?.model === mini.model)
  const newSave = h.vm.save()
  previous.resolve(variantPlan(fast, true)); await oldSave
  assert.equal(h.vm.busy.value, true)
  assert.equal(h.vm.notice.value, '')
  assert.equal(h.events.filter(([name]) => name === 'saved').length, 0)
  current.resolve(variantPlan(mini, true)); await newSave
  assert.deepEqual(h.events.filter(([name]) => name === 'saved'), [['saved', mini.key, 'B']])
  assert.equal(h.vm.data.value.canonical_model, mini.model)
})

test('variant proposal validates response identity and keeps older saved amounts separate from new proposals', async t => {
  const row = variantRow(), proposed = variantPlan(row, true)
  proposed.saved_matches_proposal = false
  proposed.saved.proposal.prices[0].retail_usd_per_1m = '3.000000000001'
  const h = await harness(t, VariantPlan, {row, contextKey: 'A'}, () => proposed)
  assert.equal(h.vm.statusKey.value, 'variantPlanUpdatePending')
  assert.equal(h.vm.previousPrice(h.vm.data.value.proposal.prices[0]).retail_usd_per_1m, '3.000000000001')
  assert.equal(h.vm.data.value.proposal.prices[0].retail_usd_per_1m, '5.238900001234')
  for (const altered of [{channel_id: 9}, {canonical_model: 'seedance-2.0'}, {upstream_model: 'different'}, {adapter_status: 'enabled', invocable: true}]) {
    h.respond = () => ({...variantPlan(row), ...altered})
    await h.vm.load()
    assert.equal(h.vm.data.value, null)
    assert.equal(h.vm.canSave.value, false)
    assert.equal(h.vm.error.value, 'pricingAdmin.variantPlanLoadFailed')
  }
})

test('variant plan deactivation discards private state and late callbacks cannot restore it', async t => {
  const row = variantRow(), response = deferred()
  const h = await harness(t, VariantPlan, {row, contextKey: 'A'}, () => variantPlan(row))
  h.respond = () => response.promise
  const pending = h.vm.save()
  h.deactivated.forEach(fn => fn())
  response.resolve(variantPlan(row, true)); await pending
  assert.equal(h.vm.data.value, null)
  assert.equal(h.vm.idempotencyKey.value, '')
  assert.equal(h.vm.canSave.value, false)
  assert.equal(h.vm.notice.value, '')
  assert.equal(h.events.filter(([name]) => name === 'saved').length, 0)
})

test('formal configuration hides the historical plan and invalidates a pending save without auto-publication', async t => {
  const item = variantRow(), response = deferred()
  const h = await harness(t, VariantPlan, {row: item, contextKey: 'A'}, () => variantPlan(item))
  h.respond = () => response.promise
  const pending = h.vm.save()
  const count = h.calls.length
  h.props.row = configuredVariant()
  await vue.nextTick()
  assert.equal(h.vm.allowed.value, false)
  assert.equal(h.vm.data.value, null)
  response.resolve(variantPlan(item, true))
  await pending
  await h.vm.load(); await h.vm.save()
  assert.equal(h.calls.length, count)
  assert.equal(h.vm.canSave.value, false)
  assert.equal(h.vm.notice.value, '')
  assert.equal(h.events.filter(([name]) => name === 'saved').length, 0)
})

test('variant plan saved events refresh only their source drawer and do not report published prices', async t => {
  const data = {...catalog(), models: [variantRow(),variantRow('mini')]}
  const h = await harness(t, Panel, {}, () => data)
  h.vm.openModel(data.models[0]); await vue.nextTick()
  const oldContext = h.vm.contextKey.value
  h.vm.openModel(data.models[1]); await vue.nextTick()
  await h.vm.variantPlanSaved(data.models[0].key, oldContext)
  assert.equal(h.calls.length, 1)
  const currentContext = h.vm.contextKey.value
  h.vm.editorBusy.value = true
  await h.vm.requestClose(false)
  assert.equal(h.vm.detailsOpen.value, true)
  await h.vm.variantPlanSaved(data.models[1].key, currentContext)
  assert.equal(h.calls.length, 2)
  assert.equal(h.vm.notice.value, 'pricingAdmin.variantPlanSavedNotice')
  assert.equal(h.vm.summary.value.published, 0)
  assert.equal(h.vm.selected.value.pricing.status, 'blocked')
})
