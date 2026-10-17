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
        export const ref = (...args) => globalThis.__pricingHarness.vue.ref(...args);
        export const computed = (...args) => globalThis.__pricingHarness.vue.computed(...args);
        export const watch = (...args) => globalThis.__pricingHarness.vue.watch(...args);
        export const onMounted = fn => globalThis.__pricingHarness.mounted.push(fn);
        export const onBeforeUnmount = fn => globalThis.__pricingHarness.unmounted.push(fn);
        export const onDeactivated = fn => globalThis.__pricingHarness.deactivated.push(fn);
      ` }
      if (args.path === '@/lib/api') return { contents: `
        export const api = (...args) => globalThis.__pricingHarness.api(...args);
        export const errorMessage = error => error.message;
        export const uuid = () => 'fixture-id-' + ++globalThis.__pricingHarness.id;
      ` }
      if (args.path === '@/stores/auth') return { contents: 'export const useAuth = () => globalThis.__pricingHarness.auth;' }
      if (args.path.endsWith('.vue')) return { contents: 'export default {};' }
      throw new Error(`Unexpected dependency: ${args.path}`)
    })
  } }],
})
const { default: VideoPricing } = await import(`data:text/javascript;base64,${Buffer.from(built.outputFiles[0].text).toString('base64')}`)

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
function pricing(orgId) {
  return {
    model: 'seedance-2.0', config_version: `cfg-${orgId}`, pricing_version: `price-${orgId}`,
    cells: [{ mode: 'standard', resolution: '720p', input_video: false, enabled: false, retail_usd_per_1m: '10.00000000' }],
  }
}
async function harness(t) {
  const original = globalThis.__pricingHarness
  const h = {
    vue, auth: vue.reactive({ orgId: 1, token: 'fixture-token-1', isAuthed: true }),
    mounted: [], unmounted: [], deactivated: [], calls: [], id: 0,
    respond: () => { throw new Error('Unexpected write request in test') },
  }
  h.defaultGet = call => call.path.endsWith('/revisions') || call.path.endsWith('/minimum-rules')
    ? { items: [] } : pricing(call.orgId)
  h.api = async (method, path, body, options) => {
    const call = { method, path, body, options, orgId: h.auth.orgId, token: h.auth.token }
    h.calls.push(call)
    return method === 'GET' ? h.defaultGet(call) : h.respond(call)
  }
  globalThis.__pricingHarness = h
  const scope = vue.effectScope()
  h.vm = scope.run(() => VideoPricing.setup({}, { expose() {} }))
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
function fillDraft(vm, suffix) {
  vm.configVersion.value = `draft-${suffix}`
  vm.priceVersion.value = `retail-${suffix}`
  vm.evidenceHash.value = 'a'.repeat(64)
}

test('old validation cannot validate the newly loaded org body or overwrite a new operation', async t => {
  const h = await harness(t), oldResponse = deferred(), currentResponse = deferred()
  h.respond = () => oldResponse.promise
  fillDraft(h.vm, 'A')
  const old = h.vm.validate()
  const submitted = h.calls.at(-1)
  await h.switchOrg(2)
  assert.equal(h.vm.canPublish.value, false)
  assert.equal(h.vm.validatedBody.value, '')
  fillDraft(h.vm, 'B')
  h.respond = () => currentResponse.promise
  const current = h.vm.validate()
  oldResponse.resolve({ preview: [{ key: 'A-only-preview' }] })
  await old
  assert.equal(h.vm.busy.value, true, 'an old finally must not unlock the current operation')
  assert.deepEqual(h.vm.previews.value, [])
  assert.equal(h.vm.notice.value, '')
  assert.equal(h.vm.validatedBody.value, '')
  assert.equal(h.vm.canPublish.value, false)
  assert.equal(submitted.body.config_version, 'draft-A')
  currentResponse.resolve({ preview: [{ key: 'B-only-preview' }] })
  await current
  assert.equal(h.vm.canPublish.value, true)
  assert.equal(h.vm.previews.value[0].key, 'B-only-preview')
})

test('editing body during validation never marks the edited body as validated', async t => {
  const h = await harness(t), response = deferred()
  h.respond = () => response.promise
  fillDraft(h.vm, 'before')
  const pending = h.vm.validate()
  const submitted = h.calls.at(-1).body
  h.vm.configVersion.value = 'draft-after'
  h.vm.pricing.value.cells[0].retail_usd_per_1m = '25.00000000'
  response.resolve({ preview: [{ key: 'old-body-only' }] })
  await pending
  assert.equal(submitted.config_version, 'draft-before')
  assert.equal(submitted.cells[0].retail_usd_per_1m, '10.00000000')
  assert.equal(h.vm.validatedBody.value, '')
  assert.equal(h.vm.canPublish.value, false)
  assert.deepEqual(h.vm.previews.value, [])
  assert.equal(h.vm.busy.value, false)
})

test('late validation errors cannot replace errors or busy state in the new org', async t => {
  const h = await harness(t), oldResponse = deferred(), currentResponse = deferred()
  h.respond = () => oldResponse.promise
  const old = h.vm.validate()
  await h.switchOrg(2)
  h.respond = () => currentResponse.promise
  const current = h.vm.validate()
  oldResponse.reject(new Error('A-private-error'))
  await old
  assert.equal(h.vm.error.value, '')
  assert.equal(h.vm.busy.value, true)
  currentResponse.reject(new Error('B-current-error'))
  await current
  assert.equal(h.vm.error.value, 'B-current-error')
  assert.equal(h.vm.busy.value, false)
})

test('old minimum import does not refresh the new org or clear its in-progress draft', async t => {
  const h = await harness(t), oldResponse = deferred(), currentResponse = deferred()
  h.respond = () => oldResponse.promise
  h.vm.minimumJSON.value = '{"version":"fixture-A"}'
  const old = h.vm.importMinimum()
  await h.switchOrg(2)
  h.vm.minimumJSON.value = '{"version":"fixture-B"}'
  h.respond = () => currentResponse.promise
  const current = h.vm.importMinimum()
  const beforeOld = h.calls.length
  oldResponse.resolve({})
  await old
  assert.equal(h.calls.length, beforeOld, 'old import must not issue a GET using the new session')
  assert.equal(h.vm.minimumJSON.value, '{"version":"fixture-B"}')
  assert.equal(h.vm.busy.value, true)
  assert.equal(h.vm.notice.value, '')
  currentResponse.resolve({})
  await current
  assert.equal(h.calls.at(-1).method, 'GET')
  assert.equal(h.calls.at(-1).orgId, 2)
  assert.equal(h.vm.minimumJSON.value, '')
  assert.equal(h.vm.busy.value, false)
})

test('minimum refresh body arriving after an org switch cannot refill the new list', async t => {
  const h = await harness(t), refresh = deferred()
  h.respond = () => ({})
  const defaultGet = h.defaultGet
  h.defaultGet = call => call.orgId === 1 && call.path.endsWith('/minimum-rules') ? refresh.promise : defaultGet(call)
  h.vm.minimumJSON.value = '{"version":"fixture-A"}'
  const pending = h.vm.importMinimum()
  await until(() => h.calls.at(-1).method === 'GET' && h.calls.at(-1).path.endsWith('/minimum-rules'))
  await h.switchOrg(2)
  h.vm.minimumJSON.value = '{"version":"fixture-B-draft"}'
  refresh.resolve({ items: [{ id: 1, version: 'A-only-evidence' }] })
  await pending
  assert.deepEqual(h.vm.minimums.value, [])
  assert.equal(h.vm.minimumJSON.value, '{"version":"fixture-B-draft"}')
  assert.equal(h.vm.notice.value, '')
})

test('late publication cannot reload the new org or unlock its new validation', async t => {
  const h = await harness(t), publication = deferred(), validation = deferred()
  h.respond = () => ({ preview: [] })
  fillDraft(h.vm, 'A')
  await h.vm.validate()
  assert.equal(h.vm.canPublish.value, true)
  h.respond = () => publication.promise
  const pending = h.vm.publish()
  const published = h.calls.at(-1)
  await h.switchOrg(2)
  fillDraft(h.vm, 'B')
  h.respond = () => validation.promise
  const current = h.vm.validate()
  const beforeOld = h.calls.length
  publication.resolve({})
  await pending
  assert.equal(h.calls.length, beforeOld, 'old publication must not reload the new session')
  assert.equal(h.vm.busy.value, true)
  assert.equal(h.vm.notice.value, '')
  assert.equal(h.vm.pricing.value.config_version, 'cfg-2')
  assert.equal(published.body.config_version, 'draft-A')
  assert.ok(published.options.headers['Idempotency-Key'].startsWith('fixture-id-'))
  validation.resolve({ preview: [{ key: 'B-preview' }] })
  await current
  assert.equal(h.vm.canPublish.value, true)
  assert.equal(h.vm.previews.value[0].key, 'B-preview')
})
