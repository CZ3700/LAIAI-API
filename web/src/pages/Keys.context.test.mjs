import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'
import * as vue from 'vue'

// Run the production component and body builder. Mock only app, lifecycle and
// browser/network boundaries; no API key is created by these regression tests.
const filename = fileURLToPath(new URL('./Keys.vue', import.meta.url))
const { descriptor } = parse(await readFile(filename, 'utf8'), { filename })
const compiled = compileScript(descriptor, { id: 'keys-context-regression' })
const helper = await readFile(new URL('../lib/keyPermissions.ts', import.meta.url), 'utf8')
const built = await build({
  stdin: { contents: compiled.content, loader: 'ts', sourcefile: filename }, bundle: true, format: 'esm', write: false,
  plugins: [{ name: 'component-boundaries', setup(b) {
    b.onResolve({ filter: /.*/ }, args => ({ path: args.path, namespace: 'boundary' }))
    b.onLoad({ filter: /.*/, namespace: 'boundary' }, args => {
      if (args.path === 'vue') return { contents: `
        export const defineComponent = value => value;
        export const ref = (...args) => globalThis.__keysHarness.vue.ref(...args);
        export const computed = (...args) => globalThis.__keysHarness.vue.computed(...args);
        export const watch = (...args) => globalThis.__keysHarness.vue.watch(...args);
        export const nextTick = (...args) => globalThis.__keysHarness.vue.nextTick(...args);
        export const onMounted = fn => globalThis.__keysHarness.mounted.push(fn);
        export const onBeforeUnmount = fn => globalThis.__keysHarness.unmounted.push(fn);
      ` }
      if (args.path === 'vue-i18n') return { contents: 'export const useI18n = () => ({ t: key => key, d: value => value });' }
      if (args.path === '@/lib/money') return { contents: 'export const fmtUsd = value => value; export const displayCurrencyCode = () => "USD";' }
      if (args.path === '@/stores/auth') return { contents: 'export const useAuth = () => globalThis.__keysHarness.auth;' }
      if (args.path === '@/lib/api') return { contents: `
        export const api = (...args) => globalThis.__keysHarness.api(...args);
        export const ApiError = globalThis.__keysApiError;
        export const errorMessage = error => error.message;
        export const copyText = async () => true;
      ` }
      if (args.path === '@/lib/keyPermissions') return { contents: helper, loader: 'ts' }
      if (args.path.endsWith('.vue')) return { contents: 'export default {};' }
      throw new Error(`Unexpected dependency: ${args.path}`)
    })
  } }],
})
class ApiError extends Error { constructor(code) { super(code); this.code = code } }
globalThis.__keysApiError = ApiError
const { default: Keys } = await import(`data:text/javascript;base64,${Buffer.from(built.outputFiles[0].text).toString('base64')}`)
delete globalThis.__keysApiError
function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
async function harness(t) {
  const originalDocument = globalThis.document, originalHarness = globalThis.__keysHarness
  const h = {
    vue, auth: vue.reactive({ orgId: 1, token: 'fixture-session-A' }), calls: [], mounted: [], unmounted: [],
    response: call => call.path === '/api/keys/video-access' ? { state: 'enabled', can_enable: false } : call.method === 'GET' ? { keys: [] } : { key: 'fixture-once-only' },
    api: async (method, path, body, options) => {
      const call = { method, path, body, options, org: h.auth.orgId, token: h.auth.token }
      h.calls.push(call)
      return h.response(call)
    },
  }
  globalThis.__keysHarness = h
  globalThis.document = { activeElement: null, addEventListener() {}, removeEventListener() {} }
  const scope = vue.effectScope()
  h.vm = scope.run(() => Keys.setup({}, { expose() {} }))
  let mounted = true
  h.unmount = () => { if (mounted) { h.unmounted.forEach(fn => fn()); scope.stop(); mounted = false } }
  t.after(async () => {
    h.unmount(); await vue.nextTick()
    if (originalDocument === undefined) delete globalThis.document; else globalThis.document = originalDocument
    if (originalHarness === undefined) delete globalThis.__keysHarness; else globalThis.__keysHarness = originalHarness
  })
  for (const fn of h.mounted) await fn()
  return h
}
function creationCalls(h) { return h.calls.filter(call => call.method === 'POST' && call.path === '/api/keys') }
async function fill(h, permission = 'read_write') {
  h.vm.openCreate()
  h.vm.newName.value = 'fixture-video'
  h.vm.newVideoPermission.value = permission
  h.vm.newQuota.value = '0.00100000'
  h.vm.newModels.value = 'mini, fast'
  h.vm.newIps.value = '192.0.2.5'
  h.vm.newExpiry.value = 1
  h.vm.newRPM.value = 2
  h.vm.newRoute.value = 'latency'
  await vue.nextTick()
}
async function requireVerification(h) {
  h.response = call => {
    if (call.path === '/api/keys' && call.method === 'POST') throw new ApiError('VERIFICATION_REQUIRED')
    return { keys: [] }
  }
  await h.vm.create()
  assert.equal(h.vm.stepUp.value.open, true)
  h.vm.stepUp.value.pwd = 'fixture-password'
  h.vm.stepUp.value.code = '000000'
}
function assertCleared(h) {
  assert.equal(h.vm.createdKey.value, '')
  assert.equal(h.vm.showCreate.value, false)
  assert.equal(h.vm.stepUp.value.open, false)
  assert.equal(h.vm.stepUp.value.pwd, '')
  assert.equal(h.vm.stepUp.value.code, '')
  assert.equal(h.vm.stepUp.value.body, null)
  assert.equal(h.vm.newVideoPermission.value, 'none')
  assert.equal(h.vm.enableVideoAsync.value, false)
}

test('first video key requires explicit owner consent and sends it only once', async t => {
  const h = await harness(t)
  h.response = call => call.path === '/api/keys/video-access' ? { state: 'not_configured', can_enable: true } : call.method === 'GET' ? { keys: [] } : { key: 'fixture-key' }
  await fill(h)
  await h.vm.create()
  assert.equal(creationCalls(h).length, 0)
  h.vm.enableVideoAsync.value = true
  await h.vm.create()
  assert.equal(creationCalls(h)[0].body.enable_video_async, true)
  assert.equal(h.vm.enableVideoAsync.value, false)
  await fill(h, 'read')
  h.vm.enableVideoAsync.value = true
  await h.vm.create()
  assert.equal(creationCalls(h)[1].body.enable_video_async, undefined)
})

for (const access of [{ state: 'disabled', can_enable: false }, { state: 'not_configured', can_enable: false }, null]) {
  test(`video write creation blocks unavailable or unauthorized state: ${access?.state}`, async t => {
    const h = await harness(t)
    h.response = () => { if (!access) throw new Error('offline'); return access }
    await fill(h)
    h.vm.enableVideoAsync.value = true
    await h.vm.create()
    assert.equal(creationCalls(h).length, 0)
  })
}

test('late readiness from a previous organization cannot enable a new key', async t => {
  const h = await harness(t), pending = deferred()
  h.response = call => call.path === '/api/keys/video-access' ? pending.promise : { keys: [] }
  await fill(h)
  h.vm.enableVideoAsync.value = true
  h.auth.orgId = 2
  pending.resolve({ state: 'enabled', can_enable: false })
  await vue.nextTick(); await vue.nextTick()
  assert.equal(h.vm.videoAccess.value, null)
  assertCleared(h)
})

test('real create handler sends explicit video scopes and preserves all configured limits', async t => {
  const h = await harness(t)
  await fill(h)
  await h.vm.create()
  assert.deepEqual(creationCalls(h)[0].body, {
    name: 'fixture-video', scopes: 'video:read video:write', quota_limit: '0.00100000', model_limits: ['mini', 'fast'],
    allow_ips: '192.0.2.5', expires_in_days: 1, rpm_limit: 2, route_policy: 'latency',
  })
  assert.equal(h.vm.createdKey.value, 'fixture-once-only')
  h.vm.createdKey.value = ''
  h.vm.openCreate()
  await h.vm.create()
  assert.deepEqual(creationCalls(h)[1].body, { name: '', scopes: '' })
  assert.equal(creationCalls(h).length, 2)
})

test('step-up retries only the captured body, preserving scopes and restrictions when the form changes', async t => {
  const h = await harness(t)
  await fill(h, 'read')
  await requireVerification(h)
  const originalBody = structuredClone(creationCalls(h)[0].body)
  h.vm.newVideoPermission.value = 'read_write'
  h.vm.newQuota.value = ''; h.vm.newModels.value = ''; h.vm.newIps.value = ''; h.vm.newExpiry.value = 0
  h.response = call => call.path === '/api/me/verify' ? { token: 'fixture-stepup' } : call.method === 'GET' ? { keys: [] } : { key: 'fixture-read-key' }
  await h.vm.submitStepUp()
  assert.deepEqual(creationCalls(h)[1].body, originalBody)
  assert.deepEqual(creationCalls(h)[1].options, { headers: { 'X-Secure-Verify': 'fixture-stepup' } })
  assert.equal(h.vm.createdKey.value, 'fixture-read-key')
  assert.equal(h.vm.stepUp.value.pwd, '')
  assert.equal(h.vm.stepUp.value.code, '')
  assert.equal(h.vm.stepUp.value.body, null)
})

for (const transition of ['organization', 'session', 'unmount', 'close']) {
  test(`${transition} change while verifying cannot submit the captured body`, async t => {
    const h = await harness(t), response = deferred()
    await fill(h); await requireVerification(h)
    h.response = call => call.path === '/api/me/verify' ? response.promise : { keys: [] }
    const submitting = h.vm.submitStepUp()
    if (transition === 'organization') h.auth.orgId = 2
    if (transition === 'session') h.auth.token = 'fixture-session-B'
    if (transition === 'unmount') h.unmount()
    if (transition === 'close') h.vm.closeStepUp()
    response.resolve({ token: 'fixture-stale-stepup' })
    await submitting
    assert.equal(creationCalls(h).length, 1)
    assert.equal(h.vm.createdKey.value, '')
    assert.equal(h.vm.stepUp.value.pwd, '')
    assert.equal(h.vm.stepUp.value.code, '')
    if (transition !== 'close') assertCleared(h)
  })
}

for (const transition of ['organization', 'session', 'unmount']) {
  test(`late key creation after ${transition} change cannot expose another context's key`, async t => {
    const h = await harness(t), response = deferred()
    await fill(h)
    h.response = call => call.method === 'POST' ? response.promise : { keys: [] }
    const creating = h.vm.create()
    if (transition === 'organization') h.auth.orgId = 2
    if (transition === 'session') h.auth.token = 'fixture-session-B'
    if (transition === 'unmount') h.unmount()
    response.resolve({ key: 'fixture-stale-key' })
    await creating
    assertCleared(h)
    assert.equal(creationCalls(h).length, 1)
  })
}

test('switching organization clears an already displayed plaintext key and opens a fresh non-video form', async t => {
  const h = await harness(t)
  await fill(h); await h.vm.create()
  assert.equal(h.vm.createdKey.value, 'fixture-once-only')
  h.auth.orgId = 2
  assertCleared(h)
  h.vm.openCreate()
  assert.equal(h.vm.newVideoPermission.value, 'none')
  assert.equal(h.vm.newQuota.value, '')
  assert.equal(h.vm.newModels.value, '')
  assert.equal(h.vm.newIps.value, '')
  assert.equal(h.vm.newExpiry.value, 0)
})

test('an uncertain creation failure is shown without automatically creating another key', async t => {
  const h = await harness(t)
  await fill(h)
  h.response = () => { throw new Error('fixture-network-failure') }
  await h.vm.create()
  assert.equal(h.vm.error.value, 'fixture-network-failure')
  assert.equal(creationCalls(h).length, 1)
  assert.equal(h.vm.stepUp.value.open, false)
  assert.equal(h.vm.createdKey.value, '')
})
