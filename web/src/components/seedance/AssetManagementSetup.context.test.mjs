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
function publishedPolicy(body) { return {present: true, async_execution_allowed: body.async_execution_allowed, capture_history: body.capture_history, retention_days: body.retention_days} }
function deferred() { let resolve, reject; const promise = new Promise((yes, no) => {resolve = yes; reject = no}); return {promise, resolve, reject} }
async function until(predicate) { const end = performance.now() + 3000; while (performance.now() < end) { if (predicate()) return; await delay(5) }; assert.fail('condition not reached') }
async function harness(t, data = setup(), authOverrides = {}) {
  const previous = globalThis.__assetSetup
  const h = {vue, props: vue.reactive({orgId: data.org_id}), auth: vue.reactive({orgId: data.org_id, token: 'fixture-session', isSuperadmin: true, ...authOverrides}), mounted: [], unmounted: [], calls: [], events: [], data, respond: () => undefined}
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

function sharedSetup() {
  const data = setup()
  data.scope_configs = [saved()]
  data.shared_default = { revision: 0, provider_scope_id: 0, scope_version: 0, enabled: false, allow_async: false }
  data.route_source = 'organization'
  return data
}
function confirmShared(h) { h.vm.sharedEnabled.value = true; h.vm.sharedAllowAsync.value = true; h.vm.sharedConfirmed.value = true }

test('shared default publishing reuses a saved version with explicit consent and revision readback, without credentials', async t => {
  const h = await harness(t, sharedSetup())
  assert.equal(h.vm.sharedEnabled.value, false); assert.equal(h.vm.sharedAllowAsync.value, false)
  assert.equal(h.vm.canPublishShared.value, false); assert.equal(writes(h).length, 0)
  confirmShared(h)
  assert.equal(h.vm.canPublishShared.value, true)
  h.respond = call => {
    assert.equal(call.path, '/api/admin/video/asset-shared-default')
    assert.deepEqual(call.body, { expected_revision: 0, provider_scope_id: 7, scope_version: 1, enabled: true, allow_async: true })
    h.data.shared_default = { revision: 1, provider_scope_id: 7, scope_version: 1, enabled: true, allow_async: true }
  }
  await h.vm.publishShared()
  assert.equal(writes(h).length, 1); assert.equal(h.vm.refreshRequired.value, false)
  assert.match(h.vm.notice.value, /共享默认项目已发布并回读确认/)
  assert.equal(h.vm.sharedConfirmed.value, false)
  assert.equal(h.vm.accessKey.value + h.vm.secretKey.value + h.vm.sessionToken.value, '')
})

test('shared default preflight stops concurrent revisions and changed confirmation scopes', async t => {
  const h = await harness(t, sharedSetup())
  confirmShared(h)
  h.data.shared_default = { revision: 1, provider_scope_id: 7, scope_version: 1, enabled: true, allow_async: false }
  await h.vm.publishShared()
  assert.equal(writes(h).length, 0); assert.equal(h.vm.sharedConfirmed.value, false)
  assert.equal(h.vm.setup.value.shared_default.revision, 1)
  confirmShared(h); h.vm.sharedAllowAsync.value = false
  assert.equal(h.vm.canPublishShared.value, false)
  await h.vm.publishShared(); assert.equal(writes(h).length, 0)
})

test('shared enable requires async consent and disabling publishes the normalized false permission', async t => {
  const data=sharedSetup();data.shared_default={revision:3,provider_scope_id:7,scope_version:1,enabled:true,allow_async:true}
  const h=await harness(t,data)
  h.vm.sharedAllowAsync.value=false;h.vm.sharedConfirmed.value=true
  assert.equal(h.vm.canPublishShared.value,false);await h.vm.publishShared();assert.equal(writes(h).length,0)
  h.vm.sharedAllowAsync.value=true;h.vm.sharedEnabled.value=false;h.vm.sharedConfirmed.value=true
  h.respond=call=>{
    assert.deepEqual(call.body,{expected_revision:3,provider_scope_id:7,scope_version:1,enabled:false,allow_async:false})
    h.data.shared_default={revision:4,provider_scope_id:7,scope_version:1,enabled:false,allow_async:false}
  }
  await h.vm.publishShared();assert.equal(h.vm.refreshRequired.value,false);assert.equal(writes(h).length,1)
})

test('unknown shared publication is read back rather than repeated', async t => {
  const h = await harness(t, sharedSetup())
  confirmShared(h)
  h.respond = () => {
    h.data.shared_default = { revision: 1, provider_scope_id: 7, scope_version: 1, enabled: true, allow_async: true }
    throw new h.ApiError(503, 'private fixture-secret')
  }
  await h.vm.publishShared()
  assert.equal(h.vm.refreshRequired.value, true); assert.equal(h.vm.error.value.includes('fixture-secret'), false)
  confirmShared(h); await h.vm.publishShared(); assert.equal(writes(h).length, 1)
  await h.vm.load()
  assert.equal(h.vm.refreshRequired.value, false); assert.equal(h.vm.pendingReadback.value, null)
  assert.match(h.vm.notice.value, /回读确认/); assert.equal(writes(h).length, 1)
})

test('ordinary users cannot load or publish management credentials or shared defaults', async t => {
  const h = await harness(t, sharedSetup(), { isSuperadmin: false })
  assert.equal(h.calls.length, 0); assert.equal(h.vm.setup.value, null)
  confirmShared(h); await h.vm.publishShared(); await h.vm.register(); await h.vm.bind()
  assert.equal(h.calls.length, 0)
})

test('shared publishing response cannot enter a replacement session', async t => {
  const h = await harness(t, sharedSetup()), late = deferred()
  confirmShared(h); h.respond = () => late.promise
  const pending = h.vm.publishShared(); await until(() => writes(h).length === 1)
  h.auth.token = 'replacement-session'; await until(() => !h.vm.loading.value)
  late.resolve(); await pending
  assert.equal(h.vm.notice.value, ''); assert.equal(h.vm.sharedConfirmed.value, false)
  assert.equal(h.vm.pendingReadback.value, null); assert.equal(writes(h).length, 1)
})

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
  h.respond = call => { assert.deepEqual(call.body, {org_id: 1, provider_scope_id: 7, scope_version: 1, enabled: true, default_route: true}); assert.equal(call.path, '/api/admin/video/asset-org-routes'); h.data.org_routes = [structuredClone(call.body)] }
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

test('required fields explain disabled actions and version suggestions remain editable', async t => {
  const h = await harness(t)
  assert.equal(h.vm.version.value, '1')
  assert.ok(h.vm.registerBlockers.value.some(reason => reason === 'Access Key 必填。'))
  assert.ok(h.vm.registerBlockers.value.some(reason => reason === 'Secret Key 必填。'))
  assert.ok(h.vm.registerBlockers.value.some(reason => reason.includes('请勾选')))
  assert.ok(h.vm.bindBlockers.value.some(reason => reason.includes('先完成第 1 步保存')))
  h.data.scope_configs = [saved(1), saved(4)]
  await h.vm.load()
  assert.equal(h.vm.version.value, '5')
  h.vm.version.value = '9'
  await h.vm.load()
  assert.equal(h.vm.version.value, '9', 'refresh preserves an unused custom version')
  h.vm.version.value = '4'
  assert.ok(h.vm.registerBlockers.value.some(reason => reason.includes('该版本号已使用')))
  h.vm.version.value = '0'
  assert.ok(h.vm.registerBlockers.value.some(reason => reason.includes('请填写正整数')))
})

test('existing versions select the sole usable choice or organization route without granting confirmation', async t => {
  const data = setup(); data.scope_configs = [saved(3), {...saved(4), enabled: false}]
  const h = await harness(t, data)
  assert.equal(h.vm.selectedVersion.value, 3)
  assert.equal(h.vm.bindingConfirmed.value, false)
  assert.equal(h.vm.canBind.value, false)
  h.data.scope_configs.push(saved(5)); h.vm.selectedVersion.value = 0
  await h.vm.load()
  assert.equal(h.vm.selectedVersion.value, 0, 'multiple usable versions require a choice')
  h.data.org_routes = [{org_id: 1, provider_scope_id: 7, scope_version: 5, enabled: true, default_route: true}]
  await h.vm.load()
  assert.equal(h.vm.selectedVersion.value, 5)
  assert.equal(h.vm.bindingConfirmed.value, false)
  assert.equal(h.vm.enableConfirmed.value, false)
  assert.equal(writes(h).length, 0)
})

test('acknowledged registration stays blocked until the exact saved version appears in readback', async t => {
  const h = await harness(t)
  fillRegistration(h)
  await h.vm.register()
  assert.equal(writes(h).length, 1)
  assert.equal(h.vm.refreshRequired.value, true)
  assert.equal(h.vm.notice.value, '')
  assert.equal(h.vm.selectedVersion.value, 0)
  await h.vm.load()
  assert.equal(h.vm.refreshRequired.value, true, 'an empty refresh does not release the write guard')
  await h.vm.register()
  assert.equal(writes(h).length, 1)
  h.data.scope_configs = [saved()]
  await h.vm.load()
  assert.equal(h.vm.refreshRequired.value, false)
  assert.equal(h.vm.selectedVersion.value, 1)
  assert.equal(h.vm.bindingConfirmed.value, false)
  assert.equal(h.vm.accessKey.value + h.vm.secretKey.value + h.vm.sessionToken.value, '')
})

test('enabling a disabled saved version needs its own confirmation and never binds an organization', async t => {
  const data = setup(); data.scope_configs = [{...saved(4), enabled: false}]
  const h = await harness(t, data)
  assert.equal(h.vm.selectedVersion.value, 0, 'disabled versions are not automatically chosen')
  h.vm.selectedVersion.value = 4
  await h.vm.enableSavedVersion()
  assert.equal(writes(h).length, 0)
  assert.ok(h.vm.bindBlockers.value.some(reason => reason.includes('已停用')))
  h.vm.enableConfirmed.value = true
  h.vm.secretKey.value = 'fixture-secret'
  h.respond = call => {
    assert.deepEqual(call, {method: 'PUT', path: '/api/admin/video/asset-scopes/7/4', body: {enabled: true}})
    assert.equal(h.vm.secretKey.value, '', 'enabling never sends registration secrets')
    h.data.scope_configs[0].enabled = true
  }
  await h.vm.enableSavedVersion()
  assert.equal(writes(h).length, 1)
  assert.equal(h.vm.savedVersion.value.enabled, true)
  assert.equal(h.vm.refreshRequired.value, false)
  assert.equal(h.vm.enableConfirmed.value, false)
  assert.equal(h.vm.bindingConfirmed.value, false)
  assert.equal(h.vm.canBind.value, false)
  assert.equal(h.events.some(([event]) => event === 'bound'), false)
})

test('unconfirmed enable readback remains blocked and changed selection prevents an enable write', async t => {
  const data = setup(); data.scope_configs = [{...saved(1), enabled: false}, {...saved(2), enabled: false}]
  const h = await harness(t, data)
  h.vm.selectedVersion.value = 1; h.vm.enableConfirmed.value = true
  const late = deferred(); h.read = () => late.promise
  const pending = h.vm.enableSavedVersion(); h.vm.selectedVersion.value = 2
  late.resolve(structuredClone(h.data)); await pending
  assert.equal(writes(h).length, 0)
  h.read = () => structuredClone(h.data); h.vm.enableConfirmed.value = true
  await h.vm.enableSavedVersion()
  assert.equal(writes(h).length, 1)
  assert.equal(h.vm.refreshRequired.value, true)
  assert.equal(h.vm.notice.value, '')
  await h.vm.load()
  assert.equal(h.vm.refreshRequired.value, true)
  h.vm.enableConfirmed.value = true; await h.vm.enableSavedVersion()
  assert.equal(writes(h).length, 1, 'a missing enabled readback never automatically retries PUT')
  h.data.scope_configs[1].enabled = true; await h.vm.load()
  assert.equal(h.vm.refreshRequired.value, false)
  assert.equal(h.vm.selectedVersion.value, 2)
  assert.equal(h.vm.bindingConfirmed.value, false)
})

test('project names use the exact backend character rule and project creation clears secrets without copying settings', async t => {
  const h = await harness(t)
  for (const invalid of ['', ' default', 'default ', '_name', '中文项目', 'a'.repeat(65)]) {
    h.vm.projectName.value = invalid
    assert.equal(h.vm.canAddProject.value, false, invalid)
    await h.vm.addProject()
  }
  assert.equal(writes(h).length, 0)
  fillRegistration(h); h.vm.projectName.value = 'LAIAI-Gateway'
  const added = {...h.data.provider_scopes[0], id: 8, project_name: 'LAIAI-Gateway'}
  h.respond = call => {
    assert.deepEqual(call, {method: 'POST', path: '/api/admin/video/asset-projects', body: {source_provider_scope_id: 7, project_name: 'LAIAI-Gateway'}})
    assert.equal(h.vm.accessKey.value + h.vm.secretKey.value + h.vm.sessionToken.value, '')
    h.data.provider_scopes.push(added)
    return {provider_scope: added}
  }
  await h.vm.addProject()
  assert.equal(writes(h).length, 1)
  assert.equal(h.vm.scopeId.value, 8)
  assert.equal(h.vm.provider.value.project_name, 'LAIAI-Gateway')
  assert.ok(h.vm.setup.value.provider_scopes.some(item => item.project_name === 'default'))
  assert.equal(h.vm.version.value, '1')
  assert.equal(h.vm.selectedVersion.value, 0)
  assert.ok(Object.values(h.vm.capabilities.value).every(value => value === false))
  assert.equal(h.vm.enabled.value, false)
  assert.equal(h.vm.registrationConfirmed.value, false)
  assert.equal(h.vm.bindingConfirmed.value, false)
  assert.equal(h.vm.refreshRequired.value, false)
  assert.equal(h.events.some(([event]) => event === 'bound'), false)
})

test('project readback requires the exact safe identity and can recover through refresh without another POST', async t => {
  const h = await harness(t)
  h.vm.projectName.value = 'LAIAI-Gateway'
  const added = {...h.data.provider_scopes[0], id: 8, project_name: 'LAIAI-Gateway'}
  h.respond = () => ({provider_scope: added})
  await h.vm.addProject()
  assert.equal(h.vm.refreshRequired.value, true)
  assert.equal(h.vm.scopeId.value, 7)
  assert.equal(h.vm.notice.value, '')
  for (const mismatch of [{account_ref: 'other-account'}, {region: 'other-region'}, {project_name: 'other-project'}]) {
    h.data.provider_scopes = [h.data.provider_scopes[0], {...added, ...mismatch}]
    await h.vm.load()
    assert.equal(h.vm.refreshRequired.value, true)
    assert.equal(h.vm.scopeId.value, 7, 'same id with a different identity must not be selected')
  }
  h.data.provider_scopes[1] = added
  await h.vm.load()
  assert.equal(h.vm.refreshRequired.value, false)
  assert.equal(h.vm.scopeId.value, 8)
  assert.equal(h.vm.bindingConfirmed.value, false)
  assert.equal(writes(h).length, 1)
})

test('reusing a disabled project selects it without enabling or copying its source registration draft', async t => {
  const data = setup(), added = {...data.provider_scopes[0], id: 8, project_name: 'existing', enabled: false}
  data.provider_scopes.push(added)
  const h = await harness(t, data)
  h.vm.scopeId.value = 7
  fillRegistration(h); h.vm.projectName.value = 'existing'
  h.respond = () => ({provider_scope: added})
  await h.vm.addProject()
  assert.equal(h.vm.scopeId.value, 8)
  assert.equal(h.vm.provider.value.enabled, false)
  assert.equal(h.vm.canRegister.value, false)
  assert.equal(h.vm.canBind.value, false)
  assert.equal(writes(h).length, 1)
  assert.equal(h.vm.secretKey.value, '')
  assert.match(h.vm.notice.value, /已停用/)
})

test('project preflight stops changed names and late project responses cannot cross organization or session boundaries', async t => {
  const h = await harness(t)
  h.vm.projectName.value = 'LAIAI-Gateway'
  const preflight = deferred(); h.read = () => preflight.promise
  const abandoned = h.vm.addProject(); h.vm.projectName.value = 'different'
  preflight.resolve(structuredClone(h.data)); await abandoned
  assert.equal(writes(h).length, 0)
  h.read = () => structuredClone(h.data)
  for (const change of ['organization', 'session']) {
    const added = {...h.data.provider_scopes[0], id: 8, project_name: 'LAIAI-Gateway'}, late = deferred()
    h.vm.projectName.value = 'LAIAI-Gateway'; h.respond = () => late.promise
    const pending = h.vm.addProject(), priorWrites = writes(h).length
    await until(() => writes(h).length > priorWrites)
    if (change === 'organization') { h.data = setup(2); h.props.orgId = 2; h.auth.orgId = 2 }
    else h.auth.token = 'fixture-replacement-session'
    await until(() => !h.vm.loading.value && h.vm.setup.value?.org_id === h.auth.orgId)
    late.resolve({provider_scope: added}); await pending
    assert.equal(h.vm.scopeId.value, 7)
    assert.equal(h.vm.pendingReadback.value, null)
    assert.equal(h.vm.notice.value, '')
    assert.equal(h.vm.busy.value, false)
    assert.equal(h.vm.secretKey.value, '')
  }
})

test('a missing organization policy starts disabled and saving execution never implicitly grants history capture', async t => {
  const data = setup(); data.policy = {present: false, async_execution_allowed: false, capture_history: false, retention_days: 0}
  data.scope_configs = [saved()]; data.org_routes = [{org_id: 1, provider_scope_id: 7, scope_version: 1, enabled: true, default_route: true}]
  const h = await harness(t, data)
  assert.equal(h.vm.asyncExecutionAllowed.value, false)
  assert.equal(h.vm.captureHistory.value, false)
  assert.equal(h.vm.policyConfirmed.value, false)
  assert.equal(h.vm.retentionDays.value, '1')
  assert.equal(writes(h).length, 0)
  h.vm.asyncExecutionAllowed.value = true
  await h.vm.savePolicy()
  assert.equal(writes(h).length, 0, 'the execution checkbox does not replace save confirmation')
  h.vm.policyConfirmed.value = true
  h.respond = call => {
    assert.deepEqual(call, {method: 'PUT', path: '/api/admin/video/org-policies/1', body: {async_execution_allowed: true, capture_history: false, retention_days: 1, expected_policy: {present: false, async_execution_allowed: false, capture_history: false, retention_days: 0}}})
    assert.equal(h.events.some(([event]) => event === 'bound'), false, 'wait for server readback before refreshing capabilities')
    h.data.policy = publishedPolicy(call.body)
  }
  await h.vm.savePolicy()
  assert.equal(writes(h).length, 1)
  assert.equal(h.vm.policyConfirmed.value, false)
  assert.equal(h.vm.captureHistory.value, false)
  assert.equal(h.vm.refreshRequired.value, false)
  assert.equal(h.events.filter(([event]) => event === 'bound').length, 1)
  assert.match(h.vm.notice.value, /未验证火山官方权限/)
})

test('existing policy fields load exactly and every draft change requires a fresh explicit confirmation', async t => {
  const data = setup(); data.policy = {present: true, async_execution_allowed: true, capture_history: false, retention_days: 7}
  const h = await harness(t, data)
  assert.equal(h.vm.asyncExecutionAllowed.value, true)
  assert.equal(h.vm.captureHistory.value, false)
  assert.equal(h.vm.retentionDays.value, '7')
  assert.equal(writes(h).length, 0)
  for (const [field, value] of [['captureHistory', true], ['asyncExecutionAllowed', false], ['retentionDays', '14']]) {
    h.vm.policyConfirmed.value = true; h.vm[field].value = value
    assert.equal(h.vm.policyConfirmed.value, false, field)
    await h.vm.savePolicy()
  }
  for (const days of ['0', '31', '1.5', '-1', '01', '1e1', '']) {
    h.vm.retentionDays.value = days; h.vm.policyConfirmed.value = true
    assert.equal(h.vm.canSavePolicy.value, false, days)
    await h.vm.savePolicy()
  }
  assert.equal(writes(h).length, 0)
  await h.vm.load()
  assert.equal(h.vm.retentionDays.value, '7')
  assert.equal(h.vm.asyncExecutionAllowed.value, true)
  assert.equal(h.vm.captureHistory.value, false)
  assert.equal(h.vm.policyConfirmed.value, false)
})

test('organization policy preflight detects another administrator edit and will not overwrite saved values', async t => {
  const h = await harness(t)
  h.vm.captureHistory.value = false; h.vm.policyConfirmed.value = true
  h.data.policy = {present: true, async_execution_allowed: false, capture_history: true, retention_days: 3}
  await h.vm.savePolicy()
  assert.equal(writes(h).length, 0)
  assert.equal(h.vm.asyncExecutionAllowed.value, false)
  assert.equal(h.vm.captureHistory.value, true)
  assert.equal(h.vm.retentionDays.value, '3')
  assert.equal(h.vm.policyConfirmed.value, false)
  assert.match(h.vm.error.value, /未覆盖其他管理员/)
  const late = deferred(); h.read = () => late.promise
  h.vm.policyConfirmed.value = true
  const pending = h.vm.savePolicy(); h.vm.retentionDays.value = '5'
  late.resolve(structuredClone(h.data)); await pending
  assert.equal(writes(h).length, 0, 'draft edits during preflight cannot save stale policy')
})

test('unknown policy writes require readback, do not retry, and only matching saved values refresh capabilities', async t => {
  const h = await harness(t)
  h.vm.captureHistory.value = false; h.vm.retentionDays.value = '9'; h.vm.policyConfirmed.value = true
  h.respond = call => { h.data.policy = publishedPolicy(call.body); throw new h.ApiError(503, 'sensitive upstream fixture') }
  await h.vm.savePolicy()
  assert.equal(h.vm.refreshRequired.value, true)
  assert.equal(h.vm.pendingReadback.value.kind, 'policy')
  assert.equal(h.vm.error.value.includes('sensitive'), false)
  assert.equal(h.events.some(([event]) => event === 'bound'), false)
  h.vm.policyConfirmed.value = true; await h.vm.savePolicy()
  assert.equal(writes(h).length, 1)
  await h.vm.load()
  assert.equal(h.vm.refreshRequired.value, false)
  assert.equal(h.vm.pendingReadback.value, null)
  assert.equal(h.vm.captureHistory.value, false)
  assert.equal(h.vm.retentionDays.value, '9')
  assert.equal(h.events.filter(([event]) => event === 'bound').length, 1)
  assert.equal(writes(h).length, 1)
})

test('a nonmatching policy readback reports unconfirmed state and requires reconfirmation rather than locking the form forever', async t => {
  const h = await harness(t)
  h.vm.captureHistory.value = false; h.vm.policyConfirmed.value = true
  h.respond = () => { throw new h.ApiError(503, 'private failure details') }
  await h.vm.savePolicy()
  assert.equal(h.vm.refreshRequired.value, true)
  await h.vm.load()
  assert.equal(h.vm.refreshRequired.value, false)
  assert.equal(h.vm.pendingReadback.value, null)
  assert.equal(h.vm.captureHistory.value, true, 'the actual saved state is shown')
  assert.equal(h.vm.policyConfirmed.value, false)
  assert.equal(h.vm.canSavePolicy.value, false)
  assert.match(h.vm.error.value, /未确认保存成功/)
  assert.equal(h.events.some(([event]) => event === 'bound'), false)
  assert.equal(writes(h).length, 1)
})

test('late policy responses cannot apply to a replacement organization, session, or superadmin role', async t => {
  const h = await harness(t)
  for (const change of ['organization', 'session', 'role']) {
    const late = deferred(); h.respond = () => late.promise
    h.vm.captureHistory.value = false; h.vm.policyConfirmed.value = true
    const count = writes(h).length, pending = h.vm.savePolicy()
    await until(() => writes(h).length > count)
    if (change === 'organization') { h.data = setup(2); h.props.orgId = 2; h.auth.orgId = 2 }
    else if (change === 'session') h.auth.token = 'fixture-session-replaced'
    else h.auth.isSuperadmin = false
    await until(() => !h.vm.loading.value)
    late.resolve(); await pending
    assert.equal(h.vm.notice.value, '')
    assert.equal(h.vm.pendingReadback.value, null)
    assert.equal(h.vm.policyConfirmed.value, false)
    assert.equal(h.vm.busy.value, false)
    assert.equal(h.events.some(([event]) => event === 'bound'), false)
  }
})

test('binding waits for the exact default route and leaves policy settings unchanged for the third step', async t => {
  const data = setup(); data.scope_configs = [saved()]; data.policy = {present: false, async_execution_allowed: false, capture_history: false, retention_days: 0}
  const h = await harness(t, data)
  h.vm.bindingConfirmed.value = true
  await h.vm.bind()
  assert.equal(h.vm.refreshRequired.value, true)
  assert.equal(h.events.some(([event]) => event === 'bound'), false)
  h.data.org_routes = [{org_id: 1, provider_scope_id: 7, scope_version: 1, enabled: true, default_route: true}]
  await h.vm.load()
  assert.equal(h.vm.refreshRequired.value, false)
  assert.equal(h.events.filter(([event]) => event === 'bound').length, 1)
  assert.equal(h.vm.asyncExecutionAllowed.value, false)
  assert.equal(h.vm.captureHistory.value, false)
  assert.equal(h.vm.policyConfirmed.value, false)
  assert.match(h.vm.notice.value, /第 3 步/)
  assert.equal(writes(h).length, 1, 'binding does not automatically publish an execution policy')
})

test('policy conflict between preflight and PUT refreshes existing values, clears confirmation and never retries', async t => {
  const h = await harness(t), previousPolicy = structuredClone(h.data.policy)
  h.vm.captureHistory.value = false; h.vm.retentionDays.value = '5'; h.vm.policyConfirmed.value = true
  h.respond = call => {
    assert.deepEqual(call.body.expected_policy, previousPolicy, 'compare-and-swap uses the acknowledged old policy, not the requested draft')
    h.data.policy = {present: true, async_execution_allowed: false, capture_history: true, retention_days: 2}
    throw new h.ApiError(409, 'execution_policy_conflict')
  }
  await h.vm.savePolicy()
  assert.equal(writes(h).length, 1)
  assert.equal(h.vm.refreshRequired.value, false)
  assert.equal(h.vm.pendingReadback.value, null)
  assert.equal(h.vm.asyncExecutionAllowed.value, false)
  assert.equal(h.vm.captureHistory.value, true)
  assert.equal(h.vm.retentionDays.value, '2')
  assert.equal(h.vm.policyConfirmed.value, false)
  assert.match(h.vm.error.value, /其他管理员修改/)
  assert.equal(h.events.some(([event]) => event === 'bound'), false)
  await h.vm.savePolicy()
  assert.equal(writes(h).length, 1)
})

test('a definitively rejected route update refreshes without waiting forever for an uncommitted route', async t => {
  const data = setup(); data.scope_configs = [saved()]
  const h = await harness(t, data)
  h.vm.bindingConfirmed.value = true
  h.respond = () => { throw new h.ApiError(409, 'route conflict') }
  await h.vm.bind()
  assert.equal(h.vm.refreshRequired.value, true)
  assert.equal(h.vm.pendingReadback.value, null)
  await h.vm.load()
  assert.equal(h.vm.refreshRequired.value, false)
  assert.equal(h.vm.bindingConfirmed.value, false)
  assert.equal(h.events.some(([event]) => event === 'bound'), false)
  assert.equal(writes(h).length, 1)
})

test('definitive policy rejections never turn an unchanged stored policy into a successful save receipt', async t => {
  for (const status of [400, 401, 403, 404, 422, 429]) {
    await t.test(String(status), async t => {
      const h = await harness(t)
      h.vm.policyConfirmed.value = true // The requested settings already equal the saved values.
      h.respond = () => { throw new h.ApiError(status, 'private failure details') }
      await h.vm.savePolicy()
      assert.equal(h.vm.refreshRequired.value, true)
      assert.equal(h.vm.pendingReadback.value, null, 'a definite rejection must not retain an unknown-write receipt')
      assert.equal(h.vm.notice.value, '')
      assert.equal(h.vm.error.value.includes('private'), false)
      assert.equal(h.vm.policyConfirmed.value, false)
      await h.vm.load()
      assert.equal(h.vm.refreshRequired.value, false)
      assert.equal(h.vm.notice.value, '')
      assert.equal(h.events.some(([event]) => event === 'bound'), false, 'matching pre-existing values cannot acknowledge a rejected write')
      assert.equal(writes(h).length, 1)
      assert.equal(h.vm.canSavePolicy.value, false)
    })
  }
})
