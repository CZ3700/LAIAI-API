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
        if (args.path === '@/lib/api') return { contents: 'export const adminApi = (...args) => globalThis.__editorHarness.api(...args); export const api = adminApi; export const errorMessage = e => e.message;' }
        if (args.path === './types') return { contents: await readFile(new URL('./types.ts', import.meta.url), 'utf8'), loader: 'ts' }
        if (args.path === './catalogPresentation') return { contents: await readFile(new URL('./catalogPresentation.ts', import.meta.url), 'utf8'), loader: 'ts' }
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
  const h = { vue, auth: vue.reactive({ orgId: 1, token: 'fixture-a', role: 'superadmin', isSuperadmin: true }), props: vue.reactive(props), calls: [], events: [], mounted: [], activated: [], deactivated: [], unmounted: [], guards: [], route: vue.reactive({ query: { tab: 'prices' } }), respond: response, confirmResult: false, confirmations: 0 }
  h.router = { replace(next) { h.route.query = next.query; return Promise.resolve() } }
  globalThis.window = { addEventListener() {}, removeEventListener() {}, confirm() { h.confirmations++; return h.confirmResult } }
  h.api = async (method, path, body) => { const call = { method, path, body, org: h.auth.orgId }; h.calls.push(call); return h.respond(call) }
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
  h.props.allowedSeries = ['2.5']
  await vue.nextTick()
  assert.deepEqual(h.vm.rows.value.map(row => row.cells), [['2.5', '70']])
  assert.ok(h.calls.every(c => c.method === 'GET'))
})
