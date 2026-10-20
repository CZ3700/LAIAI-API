import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { setTimeout as delay } from 'node:timers/promises'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'
import * as vue from 'vue'

const filename = fileURLToPath(new URL('./AssetManagementSetup.vue', import.meta.url))
const { descriptor } = parse(await readFile(filename, 'utf8'), { filename })
const compiled = compileScript(descriptor, { id: 'asset-management-setup-test' })
const built = await build({ stdin: { contents: compiled.content, loader: 'ts', sourcefile: filename }, bundle: true, format: 'esm', write: false,
  plugins: [{ name: 'asset-setup-boundaries', setup(b) {
    b.onResolve({ filter: /.*/ }, args => ({ path: args.path, namespace: 'boundary' }))
    b.onLoad({ filter: /.*/, namespace: 'boundary' }, args => {
      if (args.path === 'vue') return { contents: `export const defineComponent = v => v; export const ref = (...a) => globalThis.__assetSetup.vue.ref(...a); export const computed = (...a) => globalThis.__assetSetup.vue.computed(...a); export const watch = (...a) => globalThis.__assetSetup.vue.watch(...a); export const onMounted = f => globalThis.__assetSetup.mounted.push(f); export const onBeforeUnmount = f => globalThis.__assetSetup.unmounted.push(f);` }
      if (args.path === '@/stores/auth') return { contents: 'export const useAuth = () => globalThis.__assetSetup.auth;' }
      if (args.path === '@/lib/api') return { contents: 'export class ApiError extends Error { constructor(status,message) { super(message); this.status = status; } } export const api = (...args) => { globalThis.__assetSetup.ApiError = ApiError; return globalThis.__assetSetup.api(...args); };' }
      throw new Error(`Unexpected import: ${args.path}`)
    })
  } }],
})
const Component = (await import(`data:text/javascript;base64,${Buffer.from(built.outputFiles[0].text).toString('base64')}`)).default
function setup(org = 1) { return {org_id: org, org_active: true, configuration_ready: false, official_access: 'not_checked', provider_scopes: [{id: 7, account_ref: 'fixture-account', region: 'cn-beijing', project_name: 'default', enabled: true}], scope_configs: [], org_routes: [], policy: {present: true, async_execution_allowed: true, capture_history: true, retention_days: 30}, deployment: {console_configured: true, media_configured: true, platform_callback_configured: false, portrait_return_configured: false, portrait_target_ready: false}, issues: []} }
function saved(version = 1) { return {provider_scope_id: 7, version, enabled: true, credential_present: true, capabilities: {VirtualGroups: true, Images: true}, capabilities_valid: true, identity_matches_provider: true} }
function deferred() { let resolve, reject; const promise = new Promise((yes, no) => {resolve = yes; reject = no}); return {promise, resolve, reject} }
async function until(predicate) { const end = performance.now() + 3000; while (performance.now() < end) { if (predicate()) return; await delay(5) }; assert.fail('condition not reached') }
async function harness(t, data = setup()) {
  const previous = globalThis.__assetSetup
  const h = {vue, props: vue.reactive({orgId: data.org_id}), auth: vue.reactive({orgId: data.org_id, token: 'fixture-session', isSuperadmin: true}), mounted: [], unmounted: [], calls: [], events: [], data, respond: () => undefined}
  h.api = async (method, path, body) => { const call = {method, path, body: body ? structuredClone(body) : undefined}; h.calls.push(call); return method === 'GET' ? h.read(call) : h.respond(call) }
  h.read = () => structuredClone(h.data)
  globalThis.__assetSetup = h
  const scope = vue.effectScope()
  h.vm = scope.run(() => Component.setup(h.props, {expose() {}, emit: (...args) => h.events.push(args)}))
  t.after(() => {h.unmounted.forEach(fn => fn()); scope.stop(); if (previous === undefined) delete globalThis.__assetSetup; else globalThis.__assetSetup = previous})
  for (const fn of h.mounted) await fn()
  return h
}
function fillRegistration(h) { h.vm.version.value = '1'; h.vm.enabled.value = true; h.vm.capabilities.value.VirtualGroups = true; h.vm.accessKey.value = 'fixture-access'; h.vm.secretKey.value = 'fixture-secret'; h.vm.sessionToken.value = 'fixture-session-token'; h.vm.registrationConfirmed.value = true }
const writes = h => h.calls.filter(call => call.method !== 'GET')

test('setup starts with all capabilities off, then saves and explicitly binds an existing version without resending credentials', async t => {
  const h = await harness(t)
  assert.equal(h.vm.loading.value, false)
  assert.equal(h.vm.scopeId.value, 7)
  assert.ok(Object.values(h.vm.capabilities.value).every(value => value === false))
  assert.equal(h.vm.enabled.value, false)
  assert.equal(h.vm.canRegister.value, false)
  assert.equal(writes(h).length, 0)
  fillRegistration(h)
  h.respond = call => {assert.equal(call.path, '/api/admin/video/asset-scopes'); assert.equal(call.body.capabilities.VirtualGroups, true); assert.ok(Object.entries(call.body.capabilities).filter(([key]) => key !== 'VirtualGroups').every(([,value]) => value === false)); h.data.scope_configs = [saved()]; return undefined}
  await h.vm.register()
  assert.equal(writes(h).length, 1)
  assert.equal(h.vm.selectedVersion.value, 1)
  assert.equal(h.vm.accessKey.value + h.vm.secretKey.value + h.vm.sessionToken.value, '')
  assert.equal(h.vm.canBind.value, false, 'saving never automatically binds')
  h.vm.bindingConfirmed.value = true
  h.respond = call => { assert.deepEqual(call.body, {org_id: 1, provider_scope_id: 7, scope_version: 1, enabled: true, default_route: true}); assert.equal(call.path, '/api/admin/video/asset-org-routes') }
  await h.vm.bind()
  assert.equal(writes(h).length, 2)
  assert.ok(h.events.some(([event]) => event === 'bound'))
})

test('unknown registration outcome clears secrets, suppresses server messages and requires readback before another write', async t => {
  const h = await harness(t)
  fillRegistration(h)
  h.respond = () => { h.data.scope_configs = [saved()]; throw new h.ApiError(503, 'fixture-secret must not be displayed') }
  await h.vm.register()
  assert.equal(h.vm.refreshRequired.value, true)
  assert.equal(h.vm.error.value.includes('fixture-secret'), false)
  assert.equal(h.vm.secretKey.value, '')
  fillRegistration(h); await h.vm.register()
  assert.equal(writes(h).length, 1, 'unknown writes are not retried')
  await h.vm.load()
  h.vm.selectedVersion.value = 1; h.vm.bindingConfirmed.value = true
  assert.equal(h.vm.canRegister.value, false)
  assert.equal(h.vm.canBind.value, true, 'the saved version can be used without credentials')
})

test('fresh preflight detects concurrent route changes and stops binding until explicit reconfirmation', async t => {
  const data = setup(); data.scope_configs = [saved()]
  const h = await harness(t, data)
  h.vm.selectedVersion.value = 1; h.vm.bindingConfirmed.value = true
  h.data.org_routes = [{org_id: 1, provider_scope_id: 9, scope_version: 4, enabled: true, default_route: true}]
  await h.vm.bind()
  assert.equal(writes(h).length, 0)
  assert.equal(h.vm.bindingConfirmed.value, false)
  assert.equal(h.vm.currentRoute.value.provider_scope_id, 9)
  assert.match(h.vm.error.value, /路由已变化/)
})

test('late requests cannot update another organization and closing clears every secret', async t => {
  const h = await harness(t), late = deferred()
  fillRegistration(h)
  h.respond = () => late.promise
  const pending = h.vm.register()
  await until(() => writes(h).length === 1)
  h.data = setup(2); h.props.orgId = 2; h.auth.orgId = 2
  await until(() => !h.vm.loading.value && h.vm.setup.value?.org_id === 2)
  late.resolve(); await pending
  assert.equal(h.vm.notice.value, '')
  assert.equal(h.vm.selectedVersion.value, 0)
  assert.equal(h.vm.accessKey.value + h.vm.secretKey.value + h.vm.sessionToken.value, '')
  assert.equal(h.vm.busy.value, false)
  h.vm.accessKey.value = 'fixture-access'; h.vm.secretKey.value = 'fixture-secret'
  h.unmounted.forEach(fn => fn())
  assert.equal(h.vm.accessKey.value + h.vm.secretKey.value + h.vm.sessionToken.value, '')
})

test('selection and draft changes while preflight is pending cannot issue stale writes', async t => {
  const h = await harness(t), late = deferred()
  fillRegistration(h); h.read = () => late.promise
  const pending = h.vm.register()
  h.vm.capabilities.value.Portrait = true
  late.resolve(structuredClone(h.data)); await pending
  assert.equal(writes(h).length, 0)
  assert.equal(h.vm.registrationConfirmed.value, false)
  assert.equal(h.vm.busy.value, false)
  h.read = () => structuredClone(h.data); h.data.scope_configs = [saved(1), saved(2)]; await h.vm.load()
  h.vm.selectedVersion.value = 1; h.vm.bindingConfirmed.value = true
  const binding = deferred(); h.read = () => binding.promise
  const pendingBind = h.vm.bind(); h.vm.selectedVersion.value = 2
  binding.resolve(structuredClone(h.data)); await pendingBind
  assert.equal(writes(h).length, 0)
  assert.equal(h.vm.busy.value, false)
})

test('disabled versions, identity mismatch and superadmin loss cannot bind or retain credentials', async t => {
  const data = setup(); data.scope_configs = [{...saved(), enabled: false}]
  const h = await harness(t, data)
  h.vm.selectedVersion.value = 1; h.vm.bindingConfirmed.value = true
  assert.equal(h.vm.canBind.value, false)
  h.vm.savedVersion.value.enabled = true; h.vm.savedVersion.value.identity_matches_provider = false
  assert.equal(h.vm.canBind.value, false)
  h.vm.secretKey.value = 'fixture-secret'; h.auth.isSuperadmin = false
  assert.equal(h.vm.setup.value, null)
  assert.equal(h.vm.secretKey.value, '')
  await h.vm.load(); await h.vm.bind(); await h.vm.register()
  assert.equal(writes(h).length, 0)
})
