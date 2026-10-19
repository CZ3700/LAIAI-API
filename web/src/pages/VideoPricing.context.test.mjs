import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { setTimeout as delay } from 'node:timers/promises'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'
import * as vue from 'vue'

// Run the production setup with real Vue state/watchers and deterministic API
// boundaries. No browser, authenticated service, price publication or DB is used.
const filename = fileURLToPath(new URL('./VideoPricing.vue', import.meta.url))
const { descriptor } = parse(await readFile(filename, 'utf8'), { filename })
const compiled = compileScript(descriptor, { id: 'video-pricing-context-regression' })
const built = await build({
  stdin: { contents: compiled.content, loader: 'ts', sourcefile: filename },
  bundle: true, format: 'esm', write: false,
  plugins: [{ name: 'component-boundaries', setup(b) {
    b.onResolve({ filter: /.*/ }, args => ({ path: args.path, namespace: 'boundary' }))
    b.onLoad({ filter: /.*/, namespace: 'boundary' }, args => {
      if (args.path === 'vue') return { contents: `
        export const defineComponent = value => value;
        export const useId = () => 'fixture-tabs';
        export const ref = (...args) => globalThis.__pricingHarness.vue.ref(...args);
        export const computed = (...args) => globalThis.__pricingHarness.vue.computed(...args);
        export const watch = (...args) => globalThis.__pricingHarness.vue.watch(...args);
        export const onMounted = fn => globalThis.__pricingHarness.mounted.push(fn);
        export const onBeforeUnmount = fn => globalThis.__pricingHarness.unmounted.push(fn);
        export const onDeactivated = fn => globalThis.__pricingHarness.deactivated.push(fn);
        export const onActivated = fn => globalThis.__pricingHarness.activated.push(fn);
      ` }
      if (args.path === '@/lib/api') return { contents: `
        export const api = (...args) => globalThis.__pricingHarness.api(...args);
        export const errorMessage = error => error.message;
        export const uuid = () => 'fixture-id-' + ++globalThis.__pricingHarness.id;
      ` }
      if (args.path === '@/stores/auth') return { contents: 'export const useAuth = () => globalThis.__pricingHarness.auth;' }
      if (args.path === 'vue-i18n') return { contents: 'export const useI18n = () => ({t: key => key});' }
      if (args.path === '@/components/pricing/types') return readFile(new URL('../components/pricing/types.ts', import.meta.url), 'utf8').then(contents => ({ contents, loader: 'ts' }))
      if (args.path === '@/components/pricing/videoPriceDecimal') return readFile(new URL('../components/pricing/videoPriceDecimal.ts', import.meta.url), 'utf8').then(contents => ({ contents, loader: 'ts' }))
      if (args.path === '@/i18n/pricingRedesignVideo') return { contents: 'export default {};' }
      if (args.path.endsWith('.vue')) return { contents: 'export default {};' }
      throw new Error(`Unexpected dependency: ${args.path}`)
    })
  } }],
})
const { default: VideoPricing } = await import(`data:text/javascript;base64,${Buffer.from(built.outputFiles[0].text).toString('base64')}`)
const decimalBuild = await build({ entryPoints: [fileURLToPath(new URL('../components/pricing/videoPriceDecimal.ts', import.meta.url))], bundle: true, format: 'esm', write: false })
const {exactRate, adjustRate} = await import(`data:text/javascript;base64,${Buffer.from(decimalBuild.outputFiles[0].text).toString('base64')}`)
const legacyBuild = await build({ entryPoints: [fileURLToPath(new URL('../components/pricing/legacyVideoPricing.ts', import.meta.url))], bundle: true, format: 'esm', write: false })
const { legacyVideoPricingTarget } = await import(`data:text/javascript;base64,${Buffer.from(legacyBuild.outputFiles[0].text).toString('base64')}`)
const simpleFilename = fileURLToPath(new URL('../components/pricing/SimpleVideoPriceCell.vue', import.meta.url))
const simpleScript = compileScript(parse(await readFile(simpleFilename, 'utf8'), {filename: simpleFilename}).descriptor, {id: 'simple-price-cell-guard'})
const simpleBuild = await build({
  stdin: {contents: simpleScript.content, loader: 'ts', sourcefile: simpleFilename}, bundle: true, format: 'esm', write: false,
  plugins: [{name: 'simple-price-boundaries', setup(b) {
    b.onResolve({filter: /.*/}, args => ({path: args.path, namespace: 'simple'}))
    b.onLoad({filter: /.*/, namespace: 'simple'}, args => {
      if (args.path === 'vue') return {contents: 'export const defineComponent = v => v; export const computed = (...args) => globalThis.__pricingHarness.vue.computed(...args);'}
      if (args.path === 'vue-i18n') return {contents: 'export const useI18n = () => ({t: key => key});'}
      if (args.path === '@/i18n/pricingRedesignVideo') return {contents: 'export default {};'}
      if (args.path === './types' || args.path === './videoPriceDecimal') return readFile(new URL(`../components/pricing/${args.path.slice(2)}.ts`, import.meta.url), 'utf8').then(contents => ({contents, loader: 'ts'}))
      throw new Error(`Unexpected simple cell dependency: ${args.path}`)
    })
  }}],
})
const {default: SimpleVideoPriceCell} = await import(`data:text/javascript;base64,${Buffer.from(simpleBuild.outputFiles[0].text).toString('base64')}`)

function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
async function until(predicate, message = 'condition did not become true') {
  const deadline = performance.now() + 3000
  while (performance.now() < deadline) { if (predicate()) return; await delay(5) }
  assert.fail(message)
}
function pricing(orgId, model = 'seedance-2.0') {
  return {
    model, channel_id: 10, config_version: `cfg-${orgId}`, pricing_version: `price-${orgId}`,
    cells: [{ mode: 'generate', resolution: '720p', input_video: false, service_tier: 'default', draft: false, available: true, enabled: false, retail_usd_per_1m: '10.00000000', supplier_cny_per_1m: '', risk_multiplier: '1.15', minimum_rule_version: 'existing-minimum' }],
  }
}
function reference(orgId, model = 'seedance-2.0') {
  return { model, catalog_version: 'fixture-reference', manifest_sha256: 'b'.repeat(64), config: { channel_id: 10, config_version: `cfg-${orgId}` },
    prices: [{ ...pricing(orgId, model).cells[0], key: 'generate/720p/false/default/false', unit: 'CNY/1M_tokens', precision: 'exact', supplier_cny_per_1m: '46', source_rows: [] }],
    minimum: { cells: [], issues: ['estimated_reference_requires_verification'] }, sources: [] }
}
async function harness(t, props = {}) {
  const original = globalThis.__pricingHarness
  const h = {
    vue, auth: vue.reactive({ orgId: 1, token: 'fixture-token-1', isAuthed: true, isSuperadmin: true, role: 'superadmin' }),
    mounted: [], unmounted: [], deactivated: [], activated: [], calls: [], events: [], id: 0,
    respond: () => { throw new Error('Unexpected write request in test') },
  }
  h.defaultGet = call => {
    const model = decodeURIComponent(call.path.split('/models/')[1]?.split('/')[0] ?? '')
    return call.path.endsWith('/revisions') || call.path.endsWith('/minimum-rules')
      ? { items: [] } : call.path.endsWith('/reference-candidates') ? reference(call.orgId, model) : pricing(call.orgId, model)
  }
  h.prepare = call => ({ request: { ...call.body, config_version: `server-config-${call.orgId}`, pricing_version: `server-price-${call.orgId}`, evidence_hash: 'a'.repeat(64), source_set: { schema_version: 1, sha256: 'b'.repeat(64), refs: [] } }, diff: call.body.cells.map(cell => ({key: `${cell.mode}/${cell.resolution}/${cell.input_video}/${cell.service_tier}/${cell.draft}`, change: 'changed', before: null, after: cell})), sources: [] })
  h.api = async (method, path, body, options) => {
    const call = { method, path, body, options, orgId: h.auth.orgId, token: h.auth.token }
    h.calls.push(call)
    return method === 'GET' ? h.defaultGet(call) : path.endsWith('/prepare') ? h.prepare(call) : h.respond(call)
  }
  globalThis.__pricingHarness = h
  const scope = vue.effectScope()
  h.props = vue.reactive(props)
  h.vm = scope.run(() => VideoPricing.setup(h.props, { expose(value) { h.exposed = value }, emit: (...args) => h.events.push(args) }))
  t.after(() => {
    h.unmounted.forEach(fn => fn()); scope.stop()
    if (original === undefined) delete globalThis.__pricingHarness
    else globalThis.__pricingHarness = original
  })
  for (const fn of h.mounted) await fn()
  h.switchOrg = async orgId => {
    h.auth.orgId = orgId; h.auth.token = `fixture-token-${orgId}`
    await vue.nextTick()
    await until(() => !h.vm.loading.value && h.vm.pricing.value?.config_version === `cfg-${orgId}`)
  }
  return h
}

const changePrice = (h, value = '20.000000000001') => { h.vm.pricing.value.cells[0].retail_usd_per_1m = value }
const writes = h => h.calls.filter(c => c.method !== 'GET')

test('loading preserves every price, cost, flag and rule; references never edit the draft', async t => {
  const h = await harness(t)
  assert.deepEqual(JSON.parse(JSON.stringify(h.vm.pricing.value)), pricing(1))
  assert.equal(h.vm.dirty.value, false)
  assert.equal(h.vm.pricing.value.cells[0].supplier_cny_per_1m, '')
  assert.equal(h.vm.referenceCost(h.vm.pricing.value.cells[0]), '46')
  assert.ok(h.calls.every(c => c.method === 'GET'))
  await h.vm.validate()
  assert.equal(writes(h).length, 0, 'unchanged configurations never produce a publication request')
})

test('one visible edit preserves the complete hidden matrix and forwards prepared evidence unchanged', async t => {
  const h = await harness(t), fallback = h.defaultGet
  h.defaultGet = call => {
    const data = fallback(call)
    if (call.path.endsWith('/pricing')) data.cells.push(
      { ...data.cells[0], mode: 'extend', service_tier: 'flex', enabled: true, supplier_cny_per_1m: '99', retail_usd_per_1m: '123.123456789012' },
      { ...data.cells[0], resolution: '4k', enabled: true, available: false },
      { ...data.cells[0], mode: 'generate', draft: true, enabled: false })
    return data
  }
  await h.vm.load()
  const original = JSON.parse(JSON.stringify(h.vm.pricing.value.cells))
  changePrice(h)
  h.respond = () => ({ preview: [] })
  await h.vm.validate()
  const prep = writes(h).find(c => c.path.endsWith('/prepare'))
  const validate = writes(h).find(c => c.path.endsWith('/validate'))
  assert.deepEqual(Object.keys(prep.body).sort(), ['cells','channel_id','expected_config_version'])
  assert.deepEqual(prep.body.cells.slice(1), original.slice(1))
  assert.equal(h.vm.otherPublished.value.length, 1)
  assert.ok(h.vm.primaryCells.value.some(c => c.resolution === '4k'), 'previously enabled rows stay visible even when capability is now blocked')
  assert.deepEqual(validate.body, h.prepare(prep).request)
  assert.equal(h.vm.canPublish.value, true)
  await h.vm.publish()
  assert.deepEqual(writes(h).at(-1).body, validate.body)
  assert.ok(writes(h).at(-1).options.headers['Idempotency-Key'])
  assert.ok(h.events.some(([name]) => name === 'published'))
})

test('edits during preparation cannot validate or publish the earlier draft', async t => {
  const h = await harness(t), response = deferred()
  h.prepare = () => response.promise
  changePrice(h)
  const pending = h.vm.validate(), submitted = h.calls.at(-1)
  changePrice(h, '30')
  response.resolve({ request: { cells: submitted.body.cells }, diff: [], sources: [] })
  await pending
  assert.equal(submitted.body.cells[0].retail_usd_per_1m, '20.000000000001')
  assert.equal(writes(h).length, 1)
  assert.equal(h.vm.canPublish.value, false)
  assert.equal(h.vm.busy.value, false)
})

test('old preparation cannot validate a newly selected org or unlock its operation', async t => {
  const h = await harness(t), oldResponse = deferred(), currentResponse = deferred()
  h.prepare = () => oldResponse.promise
  changePrice(h)
  const old = h.vm.validate()
  await h.switchOrg(2)
  changePrice(h, '40')
  h.prepare = () => currentResponse.promise
  h.respond = () => ({ preview: [{ key: 'org-2' }] })
  const current = h.vm.validate()
  oldResponse.resolve({ request: { cells: pricing(1).cells }, diff: [], sources: [] })
  await old
  assert.equal(h.vm.busy.value, true)
  assert.equal(h.vm.validatedBody.value, '')
  assert.equal(writes(h).filter(c => c.path.endsWith('/validate')).length, 0)
  currentResponse.resolve({ request: { cells: h.vm.pricing.value.cells, source_set: { refs: [] } }, diff: [], sources: [] })
  await current
  assert.equal(h.vm.canPublish.value, true)
  assert.equal(h.vm.previews.value[0].key, 'org-2')
})

test('late validation result and errors stay isolated after context changes', async t => {
  const h = await harness(t), oldResponse = deferred(), currentResponse = deferred()
  h.respond = () => oldResponse.promise
  changePrice(h)
  const old = h.vm.validate()
  await until(() => h.calls.at(-1).path.endsWith('/validate'))
  await h.switchOrg(2)
  changePrice(h, '42')
  h.respond = () => currentResponse.promise
  const current = h.vm.validate()
  await until(() => h.calls.at(-1).path.endsWith('/validate'))
  oldResponse.reject(new Error('old-private-error'))
  await old
  assert.equal(h.vm.error.value, '')
  assert.equal(h.vm.busy.value, true)
  currentResponse.resolve({ preview: [{key: 'current'}] })
  await current
  assert.equal(h.vm.canPublish.value, true)
  h.vm.pricing.value.cells[0].risk_multiplier = '1.2'
  assert.equal(h.vm.canPublish.value, false)
  assert.deepEqual(h.vm.previews.value, [])
})

test('editing while validation is pending cannot mark the new contents as validated', async t => {
  const h = await harness(t), response = deferred()
  h.respond = () => response.promise
  changePrice(h)
  const pending = h.vm.validate()
  await until(() => h.calls.at(-1).path.endsWith('/validate'))
  h.vm.pricing.value.cells[0].enabled = true
  response.resolve({ preview: [{key: 'old-only'}] })
  await pending
  assert.equal(h.vm.canPublish.value, false)
  assert.equal(h.vm.validatedBody.value, '')
  assert.deepEqual(h.vm.previews.value, [])
})

test('late publication cannot reload the new org or unlock its operation', async t => {
  const h = await harness(t), publication = deferred(), preparation = deferred()
  changePrice(h)
  h.respond = () => ({ preview: [] })
  await h.vm.validate()
  h.respond = () => publication.promise
  const old = h.vm.publish()
  await h.switchOrg(2)
  changePrice(h)
  h.prepare = () => preparation.promise
  h.respond = () => ({preview: []})
  const current = h.vm.validate(), count = h.calls.length
  publication.resolve({})
  await old
  assert.equal(h.calls.length, count)
  assert.equal(h.vm.busy.value, true)
  assert.equal(h.vm.notice.value, '')
  assert.equal(h.vm.pricing.value.config_version, 'cfg-2')
  preparation.resolve({request: {cells: h.vm.pricing.value.cells}, diff: [], sources: []})
  await current
  assert.equal(h.vm.canPublish.value, true)
})

test('minimum import and its refresh cannot populate a different org', async t => {
  const h = await harness(t), refresh = deferred(), fallback = h.defaultGet
  h.respond = () => ({})
  h.defaultGet = call => call.orgId === 1 && call.path.endsWith('/minimum-rules') ? refresh.promise : fallback(call)
  h.vm.minimumJSON.value = '{"version":"old"}'
  const pending = h.vm.importMinimum()
  await until(() => h.calls.at(-1).method === 'GET' && h.calls.at(-1).path.endsWith('/minimum-rules'))
  await h.switchOrg(2)
  h.vm.minimumJSON.value = '{"version":"new-draft"}'
  refresh.resolve({items: [{id: 1, version: 'old-private'}]})
  await pending
  assert.deepEqual(h.vm.minimums.value, [])
  assert.equal(h.vm.minimumJSON.value, '{"version":"new-draft"}')
  assert.equal(h.vm.notice.value, '')
})

test('bulk percentage adjustment is exact, selective, previewed and never changes enablement', async t => {
  const h = await harness(t), cell = h.vm.pricing.value.cells[0]
  cell.retail_usd_per_1m = '0.100000000001'
  h.vm.pricing.value.cells.push({...cell, resolution: '1080p', retail_usd_per_1m: '998.123456789012'})
  h.vm.selectCell('generate/720p/false/default/false', true)
  h.vm.bulkMode.value = 'percentage'; h.vm.bulkValue.value = '10'
  h.vm.previewBulk()
  assert.equal(cell.retail_usd_per_1m, '0.100000000001')
  assert.equal(h.vm.bulkChanges.value[0].after, '0.110000000001')
  h.vm.applyBulk()
  assert.equal(cell.retail_usd_per_1m, '0.110000000001')
  assert.equal(cell.enabled, false)
  assert.equal(h.vm.pricing.value.cells[1].retail_usd_per_1m, '998.123456789012')
  assert.ok(h.calls.every(c => c.method === 'GET'))
  h.vm.bulkValue.value = '-100'; h.vm.previewBulk()
  assert.equal(h.vm.bulkChanges.value.length, 0)
  assert.ok(h.vm.bulkError.value)
})

test('changing bulk selection or inputs drops stale previews and never applies them to other rows', async t => {
  const h = await harness(t)
  h.vm.selectCell('generate/720p/false/default/false', true)
  h.vm.bulkValue.value = '12'
  h.vm.previewBulk()
  assert.equal(h.vm.bulkChanges.value.length, 1)
  h.vm.bulkValue.value = '13'
  h.vm.applyBulk()
  assert.equal(h.vm.pricing.value.cells[0].retail_usd_per_1m, '10.00000000')
  h.vm.previewBulk(); h.vm.selectCell('generate/720p/false/default/false', false)
  h.vm.applyBulk()
  assert.equal(h.vm.pricing.value.cells[0].retail_usd_per_1m, '10.00000000')
})

test('discard restores the entire baseline and informs the outer drawer', async t => {
  const h = await harness(t)
  changePrice(h)
  h.vm.minimumJSON.value = 'private-evidence-draft'
  assert.equal(h.exposed.hasUnsavedChanges(), true)
  assert.ok(h.events.some(([name,value]) => name === 'dirty-change' && value))
  h.exposed.discardChanges()
  assert.equal(h.exposed.hasUnsavedChanges(), false)
  assert.deepEqual(JSON.parse(JSON.stringify(h.vm.pricing.value)), pricing(1))
  assert.equal(h.vm.minimumJSON.value, '')
  assert.deepEqual(h.events.at(-1), ['dirty-change', false])
})

test('channel and variant restrictions prevent all writes', async t => {
  const h = await harness(t, {selectedModel: 'seedance-2.0', selectedChannelId: 99, embedded: true})
  changePrice(h)
  assert.equal(h.vm.bindingMatches.value, false)
  await h.vm.validate(); await h.vm.publish(); await h.vm.importMinimum()
  assert.equal(writes(h).length, 0)
  for (const model of ['seedance-1.5', 'seedance-2.0-fast', 'seedance-2.0-mini']) {
    h.props.selectedModel = model
    await vue.nextTick()
    assert.equal(h.vm.supported.value, false)
    assert.equal(h.vm.pricing.value, null)
  }
})

test('minimum evidence searches and paginates without inventing FPS or registering rules', async t => {
  const h = await harness(t)
  h.vm.candidates.value.minimum.cells = Array.from({length: 46}, (_,i) => ({resolution: i < 25 ? '480p' : '720p', ratio: '16:9', fps: null, output_duration_ms: i * 1000, minimum_tokens: i+100, source_rows: []}))
  h.vm.showMinimum.value = true
  assert.equal(h.vm.evidencePages.value, 3)
  h.vm.evidencePage.value = 3
  assert.equal(h.vm.minimumEvidence.value.length, 6)
  h.vm.evidenceQuery.value = '720p'
  assert.equal(h.vm.evidencePage.value, 1)
  assert.equal(h.vm.evidenceCount.value, 21)
  assert.equal(h.vm.minimumEvidence.value[0].fps, null)
  assert.equal(writes(h).length, 0)
})

test('deactivation and role loss clear private drafts and prepared requests', async t => {
  const h = await harness(t)
  changePrice(h)
  h.vm.minimumJSON.value = 'private'
  h.deactivated.forEach(fn => fn())
  assert.equal(h.vm.pricing.value, null)
  assert.equal(h.vm.candidates.value, null)
  assert.equal(h.vm.minimumJSON.value, '')
  h.auth.isSuperadmin = false; h.auth.role = 'admin'
  const count = h.calls.length
  await h.vm.load(); await h.vm.validate(); await h.vm.importMinimum()
  assert.equal(h.calls.length, count)
  assert.equal(h.vm.canPublish.value, false)
})

test('percentage arithmetic preserves 12-place rates and rejects rounded zero, overflow or exponent notation', () => {
  assert.equal(exactRate('999999999999.999999990000'), '999999999999.99999999')
  assert.equal(adjustRate('0.000000000001', '50'), '0.000000000002')
  assert.equal(adjustRate('123456789.123456789012', '-10'), '111111110.211111110111')
  for (const value of ['1e3', '0', '-1', '1.1234567890123', '999999999999.999999999999']) assert.throws(() => exactRate(value))
  assert.throws(() => adjustRate('0.000000000001', '-99'))
  assert.throws(() => adjustRate('999999999999.99999999', '0.000000000001'))
})

test('editing and reverting during validation, including evidence-only fields, invalidates the response', async t => {
  const h = await harness(t), response = deferred()
  changePrice(h)
  h.respond = () => response.promise
  const pending = h.vm.validate()
  await until(() => h.calls.at(-1).path.endsWith('/validate'))
  h.vm.minimumJSON.value = 'unpublished evidence'
  h.vm.minimumJSON.value = ''
  response.resolve({ preview: [] })
  await pending
  assert.equal(h.vm.canPublish.value, false)
  assert.equal(h.vm.validatedBody.value, '')
})

test('late model loads cannot replace the current model or clear its edits', async t => {
  const h = await harness(t), late = deferred(), fallback = h.defaultGet
  h.defaultGet = call => call.path.includes('seedance-2.0') ? late.promise : fallback(call)
  const pending = h.vm.load()
  h.vm.model.value = 'seedance-2.5'
  await until(() => !h.vm.loading.value && h.vm.pricing.value?.model === 'seedance-2.5')
  changePrice(h, '37')
  late.resolve(reference(1, 'seedance-2.0'))
  await pending
  assert.equal(h.vm.pricing.value.model, 'seedance-2.5')
  assert.equal(h.vm.pricing.value.cells[0].retail_usd_per_1m, '37')
  assert.equal(h.vm.dirty.value, true)
})

test('publication failure leaves edits intact and preserves idempotency for retry', async t => {
  const h = await harness(t)
  changePrice(h)
  h.respond = () => ({ preview: [] })
  await h.vm.validate()
  h.respond = () => { throw new Error('temporary publication error') }
  await h.vm.publish()
  const first = writes(h).at(-1)
  assert.equal(h.vm.error.value, 'temporary publication error')
  assert.equal(h.vm.dirty.value, true)
  assert.equal(h.vm.canPublish.value, true)
  await h.vm.publish()
  assert.deepEqual(writes(h).at(-1).options, first.options)
  assert.deepEqual(writes(h).at(-1).body, first.body)
})

const legacyRows = [
  {key: '10:upstream-2', model: 'doubao-seedance-2-0-260128', video_model: 'seedance-2.0', channel_id: 10, video_configuration: {channel_id: 10}},
  {key: '11:alias-2', model: 'other-channel-2', video_model: 'seedance-2.0', channel_id: 11, video_configuration: {channel_id: 10}},
  {key: '10:upstream-25', model: 'doubao-seedance-2-5-260628', video_model: 'seedance-2.5', channel_id: 10, video_configuration: {channel_id: 10}},
  {key: '10:fast', model: 'seedance-2.0-fast', video_model: null, channel_id: 10, video_configuration: null},
]

test('legacy video links resolve canonical models to the actual bound catalog row and retain other query state', () => {
  const defaults = legacyVideoPricingTarget({trace: 'keep', filters: ['a','b'], tab: 'old'}, legacyRows)
  assert.equal(defaults.path, '/app/admin')
  assert.deepEqual(defaults.query, {trace: 'keep', filters: ['a','b'], tab: 'prices', pricing_type: 'video', pricing_model: '10:upstream-2'})
  const chosen = legacyVideoPricingTarget({model: 'seedance-2.5', channel_id: '10', trace: 'keep'}, legacyRows)
  assert.equal(chosen.query.pricing_model, '10:upstream-25')
  assert.equal(chosen.query.model, undefined)
  assert.equal(chosen.query.channel_id, undefined)
  assert.equal(chosen.query.trace, 'keep')
  const explicit = legacyVideoPricingTarget({model: 'seedance-2.0', channel_id: '11'}, legacyRows)
  assert.equal(explicit.query.pricing_model, '11:alias-2', 'an explicit known channel must not be silently replaced by a different binding')
})

test('legacy missing, unsupported, invalid-channel and ambiguous links keep search without opening any price editor', () => {
  for (const query of [
    {model: 'unknown'}, {model: 'seedance-1.5'}, {model: 'seedance-2.0-fast'},
    {model: 'seedance-2.0', channel_id: '999'}, {model: 'seedance-2.0', channel_id: ['10','11']},
    {model: 'seedance-2.0', channel_id: '1e1'}, {model: 'seedance-2.0', channel_id: '9007199254740993'},
  ]) {
    const result = legacyVideoPricingTarget({...query, pricing_model: 'stale-row', keep: 'yes'}, legacyRows)
    assert.equal(result.query.pricing_model, undefined)
    assert.equal(result.query.pricing_legacy, 'missing')
    assert.equal(result.query.pricing_search, query.model)
    assert.equal(result.query.keep, 'yes')
  }
  const ambiguous = legacyVideoPricingTarget({model: 'seedance-2.0'}, [...legacyRows, {...legacyRows[0], key: '10:second-alias', model: 'second-alias'}])
  assert.equal(ambiguous.query.pricing_model, undefined)
  assert.equal(ambiguous.query.pricing_legacy, 'ambiguous')
  assert.equal(ambiguous.query.pricing_search, 'seedance-2.0')
})

test('capability-blocked active prices may be disabled but cannot be enabled again', async t => {
  await harness(t)
  const props = vue.reactive({cell: {...pricing(1).cells[0], available: false, enabled: true, unavailable_reason: 'capability_not_verified', supplier_cny_per_1m: '46'}, selected: false, disabled: false})
  const vm = SimpleVideoPriceCell.setup(props, {expose() {}, emit() {}})
  assert.equal(vm.editable.value, false)
  assert.equal(vm.canEnable.value, true, 'allow only switching an existing enabled row off')
  props.cell.enabled = false
  assert.equal(vm.canEnable.value, false)
  assert.ok(vm.reasons.value.includes('unavailable'))
  props.cell.available = true
  assert.equal(vm.canEnable.value, true)
})

test('minimum-coverage gaps remain repairable while capability blocks stay read-only', async t => {
  await harness(t)
  const props = vue.reactive({cell: {...pricing(1).cells[0], input_video: true, available: false, enabled: false, unavailable_reason: 'verified_minimum_coverage_missing', minimum_rule_version: '', supplier_cny_per_1m: '46'}, selected: false, disabled: false})
  const vm = SimpleVideoPriceCell.setup(props, {expose() {}, emit() {}})
  assert.equal(vm.editable.value, true)
  assert.equal(vm.canEnable.value, false)
  props.cell.minimum_rule_version = 'manually-verified-version'
  assert.equal(vm.canEnable.value, true, 'server validation still verifies the complete coverage before publishing')
  props.cell.unavailable_reason = 'capability_not_verified'
  assert.equal(vm.editable.value, false)
  assert.equal(vm.canEnable.value, false)
})
