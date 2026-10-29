import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { setTimeout as delay } from 'node:timers/promises'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'
import * as vue from 'vue'

const filename = fileURLToPath(new URL('./VideoAssets.vue', import.meta.url))
const { descriptor } = parse(await readFile(filename, 'utf8'), { filename })
const compiled = compileScript(descriptor, { id: 'video-assets-delete-test' })
const built = await build({
  stdin: { contents: compiled.content, loader: 'ts', sourcefile: filename }, bundle: true, format: 'esm', write: false,
  plugins: [{ name: 'asset-deletion-boundaries', setup(b) {
    b.onResolve({ filter: /.*/ }, args => ({ path: args.path, namespace: 'boundary' }))
    b.onLoad({ filter: /.*/, namespace: 'boundary' }, args => {
      if (args.path === 'vue') return { contents: `export const defineComponent = v => v; export const ref = (...a) => globalThis.__videoAssets.vue.ref(...a); export const computed = (...a) => globalThis.__videoAssets.vue.computed(...a); export const watch = (...a) => globalThis.__videoAssets.vue.watch(...a); export const nextTick = (...a) => globalThis.__videoAssets.vue.nextTick(...a); export const onMounted = f => globalThis.__videoAssets.mounted.push(f); export const onUnmounted = f => globalThis.__videoAssets.unmounted.push(f);` }
      if (args.path === '@/stores/auth') return { contents: 'export const useAuth = () => globalThis.__videoAssets.auth;' }
      if (args.path === '@/lib/api') return { contents: `export class ApiError extends Error { constructor(status, message, requestId = null, code = null) { super(message); Object.assign(this,{status,requestId,code}); } } export const api = (...a) => { globalThis.__videoAssets.ApiError = ApiError; return globalThis.__videoAssets.api(...a); }; export const copyText = () => {}; export const errorMessage = (e,f) => e?.message ?? f;` }
      if (args.path === '@/lib/videoAssets') return { contents: `export const portraitFailureText = p => globalThis.__videoAssets.helpers.portraitFailureText(p); export const validPortraitDiagnosticID = p => globalThis.__videoAssets.helpers.validPortraitDiagnosticID(p); export const validPortraitHTTPStatus = p => globalThis.__videoAssets.helpers.validPortraitHTTPStatus(p); export const assetWrite = (...a) => globalThis.__videoAssets.api(...a); export const assetDelete = path => globalThis.__videoAssets.delete(path); export const assetQuery = v => new URLSearchParams(Object.entries(v).filter(([,x])=>x!==undefined&&x!=='')).toString(); export const assetPreview = () => ''; export const assetRegistrationURL = (...a) => globalThis.__videoAssets.helpers.assetRegistrationURL(...a); export const syncIsRunning = (...a) => globalThis.__videoAssets.helpers.syncIsRunning(...a); export const syncIsTerminal = (...a) => globalThis.__videoAssets.helpers.syncIsTerminal(...a); export const validSyncJob = (...a) => globalThis.__videoAssets.helpers.validSyncJob(...a); export const syncPhaseText = (...a) => globalThis.__videoAssets.helpers.syncPhaseText(...a); export const syncErrorText = (...a) => globalThis.__videoAssets.helpers.syncErrorText(...a); export const syncSummary = (...a) => globalThis.__videoAssets.helpers.syncSummary(...a); export const groupStateText = (...a) => globalThis.__videoAssets.helpers.groupStateText(...a); export const assetStateText = (...a) => globalThis.__videoAssets.helpers.assetStateText(...a); export const assetReviewExplanation = (...a) => globalThis.__videoAssets.helpers.assetReviewExplanation(...a); export const assetProviderState = (...a) => globalThis.__videoAssets.helpers.assetProviderState(...a); export const assetStateTone = (...a) => globalThis.__videoAssets.helpers.assetStateTone(...a); export const assetMediaStateText = (...a) => globalThis.__videoAssets.helpers.assetMediaStateText(...a); export const assetGenerationText = (...a) => globalThis.__videoAssets.helpers.assetGenerationText(...a); export const assetSizeText = (...a) => globalThis.__videoAssets.helpers.assetSizeText(...a); export const assetDurationText = (...a) => globalThis.__videoAssets.helpers.assetDurationText(...a); ` }
      if (args.path.endsWith('.vue')) return { contents: 'export default {};' }
      throw new Error(`Unexpected import: ${args.path}`)
    })
  } }],
})
const Component = (await import(`data:text/javascript;base64,${Buffer.from(built.outputFiles[0].text).toString('base64')}`)).default
const clientFile = fileURLToPath(new URL('../lib/videoAssets.ts', import.meta.url))
const clientBuild = await build({
  stdin: { contents: await readFile(clientFile, 'utf8'), loader: 'ts', sourcefile: clientFile }, bundle: true, format: 'esm', write: false,
  plugins: [{ name: 'asset-delete-http-boundaries', setup(b) {
    b.onResolve({ filter: /.*/ }, args => ({ path: args.path, namespace: 'boundary' }))
    b.onLoad({ filter: /.*/, namespace: 'boundary' }, args => {
      if (args.path === '@/stores/auth') return { contents: 'export const useAuth = () => globalThis.__videoAssets.auth;' }
      if (args.path === '@/lib/api') return { contents: `export class ApiError extends Error { constructor(status, message, requestId = null, code = null) { super(message); Object.assign(this,{status,requestId,code}); } } export const uuid = () => 'fixture-idempotency'; export const api = () => { throw new Error('unexpected shared API call'); };` }
      throw new Error(`Unexpected import: ${args.path}`)
    })
  } }],
})
const client = await import(`data:text/javascript;base64,${Buffer.from(clientBuild.outputFiles[0].text).toString('base64')}`)
test('portrait provider failure diagnostics use safe copy and validated request identifiers', () => {
  const failed = { session_id: 'pas_fixture', status: 'failed', error: { code: 'provider_permission_denied', message: 'untrusted raw provider response', retryable: false }, request_id: 'req-fixture', upstream_request_id: 'provider-fixture', provider_http_status: 403 }
  assert.match(client.portraitFailureText(failed), /火山拒绝了真人认证请求/)
  assert.equal(client.portraitFailureText(failed).includes('untrusted raw provider response'), false)
  assert.equal(client.portraitFailureText({ ...failed, status: 'submission_unknown' }), '请求结果尚未确认，请勿重复发起；先查询会话状态并联系管理员核对。')
  assert.equal(client.validPortraitDiagnosticID(failed.request_id), true)
  assert.equal(client.validPortraitDiagnosticID('private data with spaces'), false)
  assert.equal(client.validPortraitDiagnosticID(''), false)
})
const group = (overrides = {}) => ({ id: 'grp_fixture', kind: 'virtual', name: 'xxx', description: '', status: 'ready', asset_count: 0, authorization_status: 'not_required', version: 1, source: 'platform', deletion_mode: 'official', deletion_block_reason: '', ...overrides })
const operation = (status = 'pending', overrides = {}) => ({ operation_id: 'op_fixture', resource_id: 'grp_fixture', kind: 'DeleteAssetGroup', status, ...overrides })
const writes = h => h.calls.filter(call => call.method !== 'GET')
function deferred() { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
async function until(predicate) { const end = performance.now() + 3000; while (performance.now() < end) { if (predicate()) return; await delay(5) }; assert.fail('condition not reached') }
async function harness(t, rows = [group()]) {
  const keys = ['__videoAssets', 'window', 'document', 'setInterval', 'clearInterval']
  const previous = new Map(keys.map(key => [key, globalThis[key]]))
  const h = { vue, helpers: client, auth: vue.reactive({ orgId: 1, token: 'fixture-session', isSuperadmin: true }), mounted: [], unmounted: [], calls: [], groups: rows, assets: [], op: operation(), failGroupReads: 0 }
  h.respond = async call => {
    if (call.method === 'DELETE') {
      h.groups = h.groups.map(row => ({ ...row, status: 'deleting', deletion_mode: 'blocked', deletion_block_reason: 'operation_pending', delete_operation: operation() }))
      return structuredClone(h.op)
    }
    if (call.path === '/api/video/asset-capabilities') return { ready: true, scope_id: 'scope_fixture', project: 'LAIAI-Gateway', sync: true, virtual: true, delete_empty_group: true, delete: true, update: true, upload: false, local_upload: false, url_registration: true, route_source: 'shared_default', resource_visibility: 'owner', media_types: ['image','video','audio'] }
    if (call.path.startsWith('/api/video/asset-sync-jobs/latest?')) return {job:null}
    if (call.path.startsWith('/api/video/asset-operations/')) return structuredClone(h.op)
    if (call.path.startsWith('/api/video/asset-groups?')) {
      if (h.failGroupReads > 0) { h.failGroupReads--; throw new Error('sensitive raw upstream fixture') }
      return { data: structuredClone(h.groups), total: h.groups.length }
    }
    if (call.path.startsWith('/api/video/assets?')) return { data: structuredClone(h.assets), total: h.assets.length }
    return { data: [], total: 0 }
  }
  h.api = async (method, path, body) => { const call = { method, path, body }; h.calls.push(call); return h.respond(call) }
  h.upload = async (id, file) => h.api('POST', `/api/video/media-uploads/${id}/complete`, file)
  h.delete = async path => { const response = await h.api('DELETE', path); return response?.operation_id ? {completed: false, operation: response} : {completed: true} }
  globalThis.__videoAssets = h
  globalThis.window = { addEventListener() {}, removeEventListener() {} }
  globalThis.document = { hidden: false, body: { style: { overflow: '' } }, activeElement: null, querySelector() { return null }, querySelectorAll() { return [] } }
  globalThis.setInterval = fn => { h.tick = fn; return 1 }
  globalThis.clearInterval = () => {}
  const scope = vue.effectScope()
  h.vm = scope.run(() => Component.setup({}, { expose() {} }))
  t.after(() => { h.unmounted.forEach(fn => fn()); scope.stop(); for (const [key, value] of previous) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value } })
  h.mounted.forEach(fn => fn())
  await until(() => !h.vm.busy.value && !!h.vm.caps.value)
  return h
}

test('accepted official deletion cannot be submitted twice and confirmed completion removes the group with clear feedback', async t => {
  const h = await harness(t)
  h.op = operation('pending', {request_id: 'req-original-delete'})
  h.vm.openDelete(h.vm.groups.value[0], true)
  await h.vm.remove()
  assert.equal(writes(h).length, 1)
  assert.equal(writes(h)[0].path, '/api/video/asset-groups/grp_fixture')
  assert.equal(h.vm.pendingOperations.value[0].operation_id, 'op_fixture')
  assert.equal(h.vm.pendingOperations.value[0].request_id, 'req-original-delete')
  assert.match(h.vm.groupDeleteReason(h.vm.groups.value[0]), /已提交/)
  await h.vm.remove()
  assert.equal(writes(h).length, 1)
  h.op = operation('confirmed'); h.groups = []
  await h.tick()
  assert.equal(h.vm.groups.value.length, 0)
  assert.equal(h.vm.pendingOperations.value.length, 0)
  assert.match(h.vm.notice.value, /删除已完成/)
  assert.match(h.vm.notice.value, /req-original-delete/)
  assert.equal(writes(h).length, 1)
})

test('reopening a deleting group restores its operation and legacy deleting rows still refresh', async t => {
  const h = await harness(t, [group({ status: 'deleting', deletion_mode: 'blocked', deletion_block_reason: 'operation_pending', delete_operation: operation() })])
  assert.equal(h.vm.pendingOperations.value.length, 1)
  h.op = operation('confirmed'); h.groups = []
  await h.tick()
  assert.ok(h.calls.some(call => call.path === '/api/video/asset-operations/op_fixture'))
  assert.equal(h.vm.groups.value.length, 0)
  assert.match(h.vm.notice.value, /删除已完成/)
  h.groups = [group({ status: 'deleting', deletion_mode: 'blocked' })]
  await h.vm.loadScope()
  h.calls = []; h.groups = []
  await h.tick()
  assert.ok(h.calls.some(call => call.path.startsWith('/api/video/asset-groups?')))
  assert.equal(h.vm.groups.value.length, 0)
  assert.equal(writes(h).length, 0)
})

test('confirmed operation survives failed list readback and the next poll retries only reads', async t => {
  const h = await harness(t)
  h.vm.openDelete(h.vm.groups.value[0], true); await h.vm.remove()
  h.op = operation('confirmed'); h.groups = []; h.failGroupReads = 1
  await h.tick()
  assert.equal(h.vm.pendingOperations.value.length, 1)
  assert.equal(h.vm.groups.value[0].status, 'deleting')
  assert.equal(h.vm.error.value.includes('sensitive'), false)
  assert.ok(h.vm.deleteLocks.value.grp_fixture)
  await h.tick()
  assert.equal(h.vm.pendingOperations.value.length, 0)
  assert.equal(h.vm.groups.value.length, 0)
  assert.match(h.vm.notice.value, /删除已完成/)
  assert.equal(writes(h).length, 1)
})

test('failed local records use explicit cleanup without requiring official delete capability or issuing another delete', async t => {
  const h = await harness(t, [group({ status: 'failed', deletion_mode: 'local_failed' })])
  h.vm.caps.value.delete_empty_group = false
  h.vm.openDelete(h.vm.groups.value[0], true)
  assert.equal(h.vm.localCleanup.value, true)
  assert.equal(h.vm.deleteBlocked.value, '')
  const respond = h.respond
  h.respond = call => { if (call.method === 'DELETE') { h.groups = []; return {} }; return respond(call) }
  await h.vm.remove()
  assert.equal(h.vm.groups.value.length, 0)
  assert.equal(h.vm.pendingOperations.value.length, 0)
  assert.match(h.vm.notice.value, /失败记录已清理，未调用官方删除/)
  assert.equal(writes(h).length, 1)
  assert.equal(h.calls.some(call => call.path.includes('/asset-operations/')), false)
})

test('204 cleanup keeps its completion readback pending across a failed refresh', async t => {
  const h = await harness(t, [group({ status: 'failed', deletion_mode: 'local_failed' })])
  const respond = h.respond
  h.respond = call => { if (call.method === 'DELETE') { h.groups = []; h.failGroupReads = 1; return {} }; return respond(call) }
  h.vm.openDelete(h.vm.groups.value[0], true); await h.vm.remove()
  assert.equal(h.vm.completedDeletions.value.length, 1)
  assert.ok(h.vm.deleteLocks.value.grp_fixture)
  await h.vm.remove()
  assert.equal(writes(h).length, 1)
  await h.tick()
  assert.equal(h.vm.completedDeletions.value.length, 0)
  assert.equal(h.vm.groups.value.length, 0)
  assert.match(h.vm.notice.value, /失败记录已清理/)
  assert.equal(writes(h).length, 1)
})

test('confirmed or 204 responses do not claim completion while the refreshed list still contains the group', async t => {
  const h = await harness(t)
  h.vm.openDelete(h.vm.groups.value[0], true); await h.vm.remove()
  h.op = operation('confirmed')
  await h.tick()
  assert.equal(h.vm.pendingOperations.value.length, 1)
  assert.match(h.vm.notice.value, /列表仍在更新/)
  assert.equal(h.vm.notice.value.includes('删除已完成'), false)
  h.groups = []; await h.tick()
  assert.equal(h.vm.pendingOperations.value.length, 0)
  h.groups = [group({ status: 'failed', deletion_mode: 'local_failed' })]; await h.vm.loadScope()
  const respond = h.respond
  h.respond = call => call.method === 'DELETE' ? {} : respond(call)
  h.vm.openDelete(h.vm.groups.value[0], true); await h.vm.remove()
  assert.equal(h.vm.completedDeletions.value.length, 1)
  assert.match(h.vm.notice.value, /列表仍包含该记录/)
  assert.equal(h.vm.notice.value.includes('失败记录已清理'), false)
  h.groups = []; await h.tick()
  assert.equal(h.vm.completedDeletions.value.length, 0)
  assert.match(h.vm.notice.value, /失败记录已清理/)
})

test('rejected deletion releases its lock after readback while unknown outcomes remain blocked', async t => {
  const h = await harness(t)
  h.vm.openDelete(h.vm.groups.value[0], true); await h.vm.remove()
  h.op = operation('rejected', { error: { code: 'provider_unavailable', message: 'Provider operation requires verification.', retryable: false }, request_id: 'req-safe-1' })
  h.groups = [group({ delete_operation: h.op })]
  await h.tick()
  assert.equal(h.vm.pendingOperations.value.length, 0)
  assert.equal(h.vm.groupDeleteReason(h.vm.groups.value[0]), '')
  assert.match(h.vm.error.value, /删除未成功/)
  assert.match(h.vm.error.value, /官方处理暂未确认/)
  assert.match(h.vm.error.value, /req-safe-1/)
  h.groups = [group({ status: 'deleting', deletion_mode: 'blocked', deletion_block_reason: 'submission_uncertain', delete_operation: operation('unknown') })]
  await h.vm.loadScope()
  h.op = operation('unknown', { error: { code: 'provider_unavailable', message: 'sensitive raw upstream payload', retryable: false } })
  await h.tick()
  assert.match(h.vm.groupDeleteReason(h.vm.groups.value[0]), /待核验/)
  assert.equal(h.vm.error.value, '')
  assert.match(h.vm.resourceOperationNotice(h.vm.groups.value[0]), /官方.*尚未确认/)
  assert.equal(h.vm.error.value.includes('sensitive'), false)
  assert.equal(h.vm.pendingOperations.value.length, 1)
  h.vm.openDelete(h.vm.groups.value[0], true); await h.vm.remove()
  assert.equal(writes(h).length, 1)
})

test('unknown transport results cannot issue another DELETE and later projected operations restore tracking', async t => {
  const h = await harness(t)
  const respond = h.respond
  h.respond = call => {
    if (call.method === 'DELETE') { h.groups = [group({ status: 'deleting', deletion_mode: 'blocked', delete_operation: operation() })]; throw new Error('sensitive request payload') }
    return respond(call)
  }
  h.vm.openDelete(h.vm.groups.value[0], true); await h.vm.remove()
  assert.equal(h.vm.deleteLocks.value.grp_fixture, 'unknown')
  assert.equal(h.vm.error.value.includes('sensitive'), false)
  await h.vm.remove()
  assert.equal(writes(h).length, 1)
  await h.vm.refresh()
  assert.equal(h.vm.pendingOperations.value.length, 1)
  h.op = operation('confirmed'); h.groups = []
  await h.tick()
  assert.equal(h.vm.groups.value.length, 0)
  assert.equal(writes(h).length, 1)
})

test('server rejection uses safe error codes and request id, and actual row kind and deletion mode gate the action', async t => {
  const h = await harness(t)
  const respond = h.respond
  h.respond = call => { if (call.method === 'DELETE') throw new h.ApiError(409, 'raw private details', 'request-fixture-1', 'group_not_empty'); return respond(call) }
  h.vm.openDelete(h.vm.groups.value[0], true); await h.vm.remove()
  assert.match(h.vm.error.value, /素材组非空/)
  assert.match(h.vm.error.value, /request-fixture-1/)
  assert.equal(h.vm.error.value.includes('raw private'), false)
  assert.equal(h.vm.deleteLocks.value.grp_fixture, undefined)
  assert.match(h.vm.groupDeleteReason(group({ kind: 'real_person' })), /真人/)
  assert.match(h.vm.groupDeleteReason(group({ asset_count: 1 })), /仍有素材/)
  assert.match(h.vm.groupDeleteReason(group({ deletion_mode: 'blocked', deletion_block_reason: 'exclusive_project_required' })), /仅由本平台写入/)
  assert.notEqual(h.vm.groupDeleteReason(group({ deletion_mode: undefined })), '', 'missing server permission fails closed')
  h.vm.caps.value.delete_empty_group = false
  assert.equal(h.vm.groupDeleteReason(group()), '', 'per-group official eligibility belongs to the fixed provider scope')
})

test('late DELETE replies cannot change a new organization or session, or close its dialog', async t => {
  const h = await harness(t)
  for (const changed of ['organization', 'session']) {
    h.groups = [group()]; await h.vm.loadScope()
    const respond = h.respond, late = deferred()
    h.respond = call => call.method === 'DELETE' ? late.promise : respond(call)
    h.vm.openDelete(h.vm.groups.value[0], true)
    const count = writes(h).length, pending = h.vm.remove()
    await until(() => writes(h).length > count)
    if (changed === 'organization') h.auth.orgId = 2
    else h.auth.token = 'fixture-replacement-session'
    await until(() => !h.vm.busy.value && h.vm.groups.value.length > 0)
    h.vm.openDelete(h.vm.groups.value[0], true)
    late.resolve(operation()); await pending
    assert.equal(h.vm.pendingOperations.value.length, 0)
    assert.deepEqual(h.vm.deleteLocks.value, {})
    assert.equal(h.vm.notice.value, '')
    assert.equal(h.vm.modal.value, 'delete')
    h.vm.close(); h.respond = respond
  }
})

test('late operation poll responses and concurrent poll ticks cannot cross the active scope', async t => {
  const h = await harness(t, [group({ status: 'deleting', deletion_mode: 'blocked', delete_operation: operation() })])
  const respond = h.respond, late = deferred()
  h.respond = call => call.path.includes('/asset-operations/') ? late.promise : respond(call)
  const pending = h.tick()
  await h.tick()
  assert.equal(h.calls.filter(call => call.path.includes('/asset-operations/')).length, 1)
  h.groups = [group({ id: 'grp_other', name: 'other' })]; h.auth.orgId = 2
  await until(() => !h.vm.busy.value && h.vm.groups.value[0]?.id === 'grp_other')
  late.resolve(operation('confirmed')); await pending
  assert.equal(h.vm.groups.value[0].id, 'grp_other')
  assert.equal(h.vm.pendingOperations.value.length, 0)
  assert.equal(h.vm.notice.value, '')
  assert.equal(h.vm.error.value, '')
  assert.equal(writes(h).length, 0)
})

test('DELETE HTTP client distinguishes 204, valid 202 and malformed receipts without retrying or exposing bodies', async t => {
  const h = await harness(t), previousFetch = globalThis.fetch, calls = []
  t.after(() => { globalThis.fetch = previousFetch })
  let response = new Response(null, {status: 204})
  globalThis.fetch = async (path, options) => { calls.push({path, options}); return response }
  assert.deepEqual(await client.assetDelete('/api/video/asset-groups/grp_fixture'), {completed: true})
  assert.equal(calls[0].options.method, 'DELETE')
  assert.equal(calls[0].options.headers['Idempotency-Key'], 'fixture-idempotency')
  response = new Response(JSON.stringify(operation()), {status: 202, headers: {'X-Request-Id': 'req-accepted'}})
  assert.deepEqual(await client.assetDelete('/api/video/asset-groups/grp_fixture'), {completed: false, operation: operation('pending', {request_id: 'req-accepted'})})
  response = new Response('{}', {status: 202, headers: {'X-Request-Id': 'req-malformed'}})
  await assert.rejects(client.assetDelete('/api/video/asset-groups/grp_fixture'), error => error.status === 502 && error.code === 'provider_response_invalid' && error.requestId === 'req-malformed')
  response = new Response(JSON.stringify({error: {message: 'sensitive response payload'}}), {status: 503})
  await assert.rejects(client.assetDelete('/api/video/asset-groups/grp_fixture'), error => error.status === 503 && error.message === 'operation_failed')
  let loggedOut = false; h.auth.logout = () => { loggedOut = true }
  response = new Response(JSON.stringify({error: 'unauthorized'}), {status: 401})
  await assert.rejects(client.assetDelete('/api/video/asset-groups/grp_fixture'), error => error.status === 401)
  assert.equal(loggedOut, true)
  assert.equal(calls.length, 5, 'each explicit call sends once, without hidden retries')
})

test('a delayed DELETE 401 cannot sign out a replacement session', async t => {
  const h = await harness(t), previousFetch = globalThis.fetch, late = deferred()
  t.after(() => { globalThis.fetch = previousFetch })
  let loggedOut = false; h.auth.logout = () => { loggedOut = true }
  globalThis.fetch = () => late.promise
  const pending = client.assetDelete('/api/video/asset-groups/grp_fixture')
  h.auth.token = 'fixture-replacement-session'
  late.resolve(new Response(JSON.stringify({error: 'unauthorized'}), {status: 401}))
  await assert.rejects(pending, error => error.status === 401)
  assert.equal(loggedOut, false)
  assert.equal(h.auth.token, 'fixture-replacement-session')
})

test('management setup readback refreshes capabilities without closing the drawer before organization policy completion', async t => {
  const h = await harness(t)
  h.vm.setupOpen.value = true
  const respond = h.respond
  h.respond = call => call.path === '/api/video/asset-capabilities' ? {ready: false, reason: 'execution_policy_required', virtual: false} : respond(call)
  const before = h.calls.filter(call => call.path === '/api/video/asset-capabilities').length
  await h.vm.setupBound()
  assert.equal(h.vm.setupOpen.value, true)
  assert.equal(h.calls.filter(call => call.path === '/api/video/asset-capabilities').length, before + 1)
  assert.equal(h.vm.caps.value.reason, 'execution_policy_required')
  assert.match(h.vm.errors.execution_policy_required, /第 3 步/)
  assert.match(h.vm.errors.execution_policy_required, /异步执行许可/)
  assert.equal(h.vm.errors.execution_policy_required.includes('AK/SK'), false, 'execution policy is distinct from missing credentials')
  h.vm.report(new h.ApiError(403, 'sensitive details', 'req-policy', 'execution_policy_required'))
  assert.match(h.vm.error.value, /第 3 步/)
  assert.match(h.vm.error.value, /req-policy/)
  assert.equal(h.vm.error.value.includes('sensitive'), false)
})

const asset = (overrides = {}) => ({ id:'asset_fixture',group_id:'grp_fixture',media_type:'image',name:'asset',status:'ready',stage:'ready',version:1,bytes:20,duration_ms:0,created_at:'2026-10-21T00:00:00Z',source:'platform',deletion_mode:'official',deletion_block_reason:'',...overrides })

test('URL registration requires explicit official capability, independently of local media deployment', async t => {
  const h=await harness(t)
  h.vm.caps.value.url_registration=false
  assert.match(h.vm.uploadBlockedReason.value,/官方网址注册尚未开通/)
  h.vm.openUpload(); assert.equal(h.vm.modal.value,'')
  h.vm.sourceURL.value='https://example.com/media.png'; await h.vm.upload()
  assert.equal(writes(h).length,0)
  h.vm.caps.value.url_registration=true; h.vm.caps.value.upload=false; h.vm.caps.value.local_upload=false; h.vm.caps.value.media_types=[]
  assert.match(h.vm.uploadBlockedReason.value,/未启用图片、视频或音频/)
  h.vm.caps.value.media_types=['video']; h.vm.openUpload()
  assert.equal(h.vm.modal.value,'upload'); assert.equal(h.vm.uploadType.value,'video')
  h.vm.groups.value=[group({status:'needs_review'})]
  assert.match(h.vm.uploadBlockedReason.value,/创建结果待核验/)
  h.vm.groups.value=[group({kind:'real_person',authorization_status:'needs_review'})]
  assert.match(h.vm.uploadBlockedReason.value,/有效使用许可/)
})

test('unknown group creation exposes its receipt and safe error without enabling deletion or resubmitting', async t => {
  const created=operation('unknown',{operation_id:'create_group',kind:'CreateAssetGroup',request_id:'req-create',error:{code:'provider_unavailable',message:'private upstream body',retryable:false}})
  const h=await harness(t,[group({status:'needs_review',deletion_mode:'blocked',deletion_block_reason:'submission_uncertain',create_operation:created})])
  h.op=created
  assert.equal(h.vm.text(h.vm.groups.value[0].status),'待核验')
  h.vm.showGroupDetails(h.vm.groups.value[0])
  assert.equal(h.vm.resourceOperations(h.vm.shownGroup.value)[0].request_id,'req-create')
  assert.match(h.vm.operationError(created),/官方处理暂未确认/)
  assert.equal(h.vm.operationError(created).includes('private'),false)
  assert.match(h.vm.groupDeleteReason(h.vm.groups.value[0]),/待核验/)
  await h.tick()
  assert.equal(h.vm.error.value,'')
  assert.equal(h.vm.resourceOperations(h.vm.shownGroup.value)[0].request_id,'req-create')
  assert.equal(writes(h).length,0)
  assert.equal(h.vm.pendingOperations.value[0].operation_id,'create_group')
  h.auth.orgId=2; await until(()=>!h.vm.busy.value)
  assert.equal(h.vm.detailGroup.value,undefined)
})

test('asset deletion uses the fixed resource eligibility and restores unknown operations after refresh', async t => {
  const h=await harness(t)
  h.vm.caps.value.delete=false
  assert.equal(h.vm.assetDeleteReason(asset()),'','current default route does not govern a historic resource')
  assert.notEqual(h.vm.assetDeleteReason(asset({deletion_mode:undefined})),'','missing server permission fails closed')
  assert.match(h.vm.assetDeleteReason(asset({deletion_mode:'blocked',deletion_block_reason:'asset_not_ready'})),/尚未就绪/)
  const op=operation('unknown',{resource_id:'asset_fixture',kind:'DeleteAsset',request_id:'req-delete-asset'})
  h.assets=[asset({status:'deleting',stage:'needs_review',deletion_mode:'blocked',deletion_block_reason:'submission_uncertain',delete_operation:op})]
  await h.vm.loadScope(); h.op=op
  assert.equal(h.vm.pendingOperations.value[0].operation_id,op.operation_id)
  assert.match(h.vm.assetDeleteReason(h.vm.assets.value[0]),/待核验/)
  h.vm.openDelete(h.vm.assets.value[0],false); await h.vm.remove()
  await h.tick()
  assert.equal(writes(h).length,0)
  assert.equal(h.vm.error.value,'')
  assert.equal(h.vm.resourceOperations(h.vm.assets.value[0])[0].request_id,'req-delete-asset')
  h.op={...op,status:'confirmed'}; h.assets=[]; await h.tick()
  assert.equal(h.vm.pendingOperations.value.length,0)
  assert.equal(h.vm.deleteLocks.value.asset_fixture,undefined)
  assert.match(h.vm.notice.value,/删除已完成/)
})

test('confirmed failed asset registration can be cleaned locally with 204 and refreshed lists', async t => {
  const h=await harness(t); h.assets=[asset({status:'failed',stage:'failed',deletion_mode:'local_failed'})]
  await h.vm.loadAssets(); h.vm.caps.value.delete=false
  h.vm.openDelete(h.vm.assets.value[0],false)
  assert.equal(h.vm.localCleanup.value,true)
  const respond=h.respond
  h.respond=call=>{if(call.method==='DELETE'){h.assets=[];return {}};return respond(call)}
  await h.vm.remove()
  assert.equal(writes(h).length,1); assert.equal(writes(h)[0].path,'/api/video/assets/asset_fixture')
  assert.equal(h.vm.assets.value.length,0); assert.match(h.vm.notice.value,/未调用官方删除/)
})

test('late group creation and legacy registration cannot mutate replacement organization or session views', async t => {
  for(const action of ['create','register'])for(const change of ['org','session'])await t.test(`${action}-${change}`,async t=>{
    const h=await harness(t),late=deferred(),respond=h.respond
    h.respond=call=>call.method==='POST'?late.promise:respond(call)
    h.vm.name.value='new'; h.vm.selectedUpload.value={id:'upload_fixture'}; h.vm.caps.value.local_upload=true
    const pending=action==='create'?h.vm.createGroup():h.vm.registerUpload()
    await until(()=>writes(h).length===1)
    if(change==='org')h.auth.orgId=2;else h.auth.token='replacement-session'
    await until(()=>!h.vm.busy.value); h.vm.modal.value='rename'
    late.resolve(action==='create'?group():asset()); await pending
    assert.equal(h.vm.modal.value,'rename'); assert.equal(h.vm.notice.value,''); assert.equal(h.vm.pendingOperations.value.length,0)
    assert.equal(writes(h).length,1)
  })
})

test('URL registration responses cannot mutate a replacement organization or session and never create local upload sessions', async t => {
  for(const change of ['org','session'])await t.test(change,async t=>{
    const h=await harness(t),late=deferred(),respond=h.respond
    h.respond=call=>{
      if(call.method!=='POST')return respond(call)
      if(call.path==='/api/video/assets')return late.promise
      throw new Error('unexpected write')
    }
    h.vm.sourceURL.value='https://example.com/media.png'; h.vm.name.value='new'
    const pending=h.vm.upload()
    await until(()=>writes(h).length===1)
    if(change==='org')h.auth.orgId=2;else h.auth.token='replacement-session'
    await until(()=>!h.vm.busy.value); h.vm.modal.value='rename'
    late.resolve(asset()); await pending
    assert.equal(writes(h).length,1,'never create local upload or continue under the replacement context')
    assert.equal(writes(h)[0].path,'/api/video/assets')
    assert.equal(h.vm.modal.value,'rename'); assert.equal(h.vm.notice.value,''); assert.equal(h.vm.uploadID.value,'')
  })
})

test('URL registration sends the original group and signed URL once, without caller-supplied ownership or media facts', async t => {
  const h=await harness(t),late=deferred(),respond=h.respond
  h.groups=[group(),group({id:'grp_other'})]; await h.vm.loadScope()
  h.respond=call=>{
    if(call.path==='/api/video/assets')return late.promise
    return respond(call)
  }
  h.vm.sourceURL.value='https://example.com/original.png?signature=fixture%2Bvalue'; h.vm.name.value='original'
  const pending=h.vm.upload(); await until(()=>writes(h).length===1)
  h.vm.selected.value='grp_other'; h.vm.sourceURL.value='https://example.com/replacement.png'; h.vm.name.value='replacement'
  late.resolve(asset()); await pending
  assert.deepEqual(writes(h)[0].body,{group_id:'grp_fixture',url:'https://example.com/original.png?signature=fixture%2Bvalue',media_type:'image',name:'original'})
  assert.equal(writes(h).length,1)
  assert.match(h.vm.notice.value,/网址已提交官方注册/)
})

test('invalid URLs and uncertain registration outcomes cannot trigger duplicate or local file submissions', async t=>{
  const h=await harness(t),respond=h.respond
  for(const url of ['','http://example.com/a','https://user:secret@example.com/a','file:///a','https://example.com/a#part']){
    h.vm.sourceURL.value=url;await h.vm.upload();assert.equal(writes(h).length,0)
  }
  h.respond=call=>{if(call.method==='POST')throw new Error('private upstream source');return respond(call)}
  h.vm.sourceURL.value='https://example.com/a';await h.vm.upload()
  assert.equal(h.vm.registrationUncertain.value,true);assert.equal(h.vm.error.value.includes('private'),false)
  await h.vm.upload();h.vm.close();h.vm.openUpload();h.vm.sourceURL.value='https://example.com/a';await h.vm.upload()
  assert.equal(writes(h).length,1)
})

test('mismatched registration receipts remain uncertain only for their original group',async t=>{
  const h=await harness(t),respond=h.respond
  h.groups=[group(),group({id:'grp_other'})];await h.vm.loadScope()
  h.respond=call=>call.method==='POST'?asset({group_id:'grp_wrong'}):respond(call)
  h.vm.sourceURL.value='https://example.com/image.png';await h.vm.upload()
  assert.equal(h.vm.registrationUncertain.value,true);assert.equal(writes(h).length,1)
  h.vm.selected.value='grp_other';assert.equal(h.vm.registrationUncertain.value,false)
  h.vm.selected.value='grp_fixture';assert.equal(h.vm.registrationUncertain.value,true)
})

test('losing superadmin privileges clears organization-wide details and reloads the owner view',async t=>{
  const h=await harness(t),row=asset({source_available:true}),respond=h.respond
  h.vm.showGroupDetails(h.vm.groups.value[0]);h.vm.previewAsset.value=row;h.vm.previewURL.value='https://example.com/old.png';h.vm.setupOpen.value=true
  h.groups=[]
  h.respond=call=>call.path==='/api/video/asset-capabilities'?{ready:true,url_registration:true,media_types:['image'],route_source:'shared_default',resource_visibility:'owner'}:respond(call)
  h.auth.isSuperadmin=false
  assert.equal(h.vm.detailGroup.value,undefined);assert.equal(h.vm.previewURL.value,'');assert.equal(h.vm.setupOpen.value,false)
  await until(()=>!h.vm.busy.value)
  assert.deepEqual(h.vm.groups.value,[]);assert.equal(h.vm.caps.value.resource_visibility,'owner')
  assert.equal(writes(h).length,0)
})

test('official source preview requests an authenticated same-origin URL then displays the returned HTTPS address directly', async t=>{
  const h=await harness(t),respond=h.respond,previousFetch=globalThis.fetch
  globalThis.fetch=()=>assert.fail('official media must be displayed directly, never fetched with platform authorization')
  t.after(()=>{globalThis.fetch=previousFetch})
  h.respond=call=>call.path==='/api/video/assets/asset_fixture/source'?{url:'https://official.example.com/image.png?signature=fixture%2Bvalue',source:'upstream'}:respond(call)
  await h.vm.showPreview(asset({source_available:true}))
  assert.equal(h.vm.previewURL.value,'https://official.example.com/image.png?signature=fixture%2Bvalue')
  assert.ok(h.calls.some(call=>call.method==='GET'&&call.path==='/api/video/assets/asset_fixture/source'))
  assert.equal(writes(h).length,0)
  h.vm.closePreview();assert.equal(h.vm.previewURL.value,'');assert.equal(h.vm.previewAsset.value,undefined)
})

test('late official preview cannot survive drawer close or session replacement, and unsafe sources stay hidden',async t=>{
  for(const change of ['close','session'])await t.test(change,async t=>{
    const h=await harness(t),respond=h.respond,late=deferred()
    h.respond=call=>call.path.endsWith('/source')?late.promise:respond(call)
    const pending=h.vm.showPreview(asset({source_available:true}))
    await until(()=>h.calls.some(call=>call.path.endsWith('/source')))
    if(change==='close')h.vm.closePreview();else h.auth.token='replacement-session'
    late.resolve({url:'https://official.example.com/private.png',source:'upstream'});await pending
    assert.equal(h.vm.previewURL.value,'');assert.equal(h.vm.previewAsset.value,undefined)
    assert.equal(writes(h).length,0)
  })
  const h=await harness(t),respond=h.respond
  h.respond=call=>call.path.endsWith('/source')?{url:'https://secret@example.com/private.png',source:'upstream'}:respond(call)
  await h.vm.showPreview(asset({source_available:true}))
  assert.equal(h.vm.previewURL.value,'');assert.equal(h.vm.previewError.value.includes('secret'),false)
})

test('legacy preview helper refuses external addresses before attaching platform authorization',async t=>{
  await harness(t);const previousFetch=globalThis.fetch
  globalThis.fetch=()=>assert.fail('must not send platform bearer to an external address')
  t.after(()=>{globalThis.fetch=previousFetch})
  for(const path of ['https://official.example.com/media.png','//official.example.com/media.png','/api/video/assets/id/preview?url=https://other.invalid'])await assert.rejects(client.assetPreview(path),/preview_unavailable/)
})

test('file upload errors preserve safe codes and request IDs without exposing object payloads or expiring replacement sessions', async t => {
  const h=await harness(t),previousFetch=globalThis.fetch,late=deferred()
  t.after(()=>{globalThis.fetch=previousFetch})
  globalThis.fetch=async()=>new Response(JSON.stringify({error:{message:'private payload'}}),{status:500,headers:{'X-Request-Id':'req-upload'}})
  await assert.rejects(client.uploadAssetFile('upload_fixture',new Blob(['fixture'])),e=>e.code==='upload_failed'&&e.requestId==='req-upload'&&!e.message.includes('private'))
  let loggedOut=false;h.auth.logout=()=>{loggedOut=true};globalThis.fetch=()=>late.promise
  const pending=client.uploadAssetFile('upload_fixture',new Blob(['fixture']))
  h.auth.token='replacement-session';late.resolve(new Response(JSON.stringify({code:'unauthorized'}),{status:401}))
  await assert.rejects(pending,e=>e.status===401);assert.equal(loggedOut,false)
})


test('late discovery lists never reveal another organization project or reopen its drawer', async t=>{
  const h=await harness(t),late=deferred(),respond=h.respond
  h.vm.caps.value.scope_id='scope_old'
  h.respond=call=>call.path==='/api/admin/orgs'?{orgs:[{id:1,name:'old organization'}]}:call.path.startsWith('/api/admin/video/asset-discoveries?')?late.promise:respond(call)
  const pending=h.vm.openDiscover();await until(()=>h.calls.some(c=>c.path.startsWith('/api/admin/video/asset-discoveries?')))
  h.auth.orgId=2;await until(()=>!h.vm.busy.value);h.vm.modal.value='rename'
  late.resolve({data:[{id:'private_discovery',name:'old project resource'}],total:1});await pending
  assert.deepEqual(h.vm.discoveries.value,[]);assert.deepEqual(h.vm.organizations.value,[]);assert.equal(h.vm.modal.value,'rename')
})

test('late sync and verification responses cannot overwrite a replacement session',async t=>{
  for(const action of ['sync','discover','assign','license'])await t.test(action,async t=>{
    const h=await harness(t),late=deferred(),respond=h.respond
    h.vm.caps.value.scope_id='scope_old';h.vm.assignment.value={id:'discovery_fixture',version:1};h.vm.targetOrg.value=1;h.vm.assignmentConfirmed.value=true;h.vm.evidence.value='reviewed'
    h.vm.groups.value=[group({portrait_session_id:'portrait_fixture'})];h.vm.licenseUntil.value='2026-10-22T00:00';h.vm.reverifyAt.value='2026-10-21T12:00'
    h.respond=call=>call.method==='POST'?late.promise:respond(call)
    const pending=action==='sync'?h.vm.startSync():action==='discover'?h.vm.discover():action==='assign'?h.vm.assign():h.vm.reviewLicense()
    await until(()=>writes(h).length===1);h.auth.token='replacement-session';await until(()=>!h.vm.busy.value);h.vm.modal.value='rename'
    late.resolve({id:'old_job'});await pending
    assert.equal(h.vm.modal.value,'rename');assert.equal(h.vm.notice.value,'');assert.equal(h.vm.sync.value,undefined);assert.equal(h.vm.discoveryJob.value,undefined)
    assert.equal(writes(h).length,1)
  })
})


const syncJob=(overrides={})=>({id:'asj_fixture',status:'pending',phase:'groups',pages:0,items:0,scope_id:'scope_fixture',project:'LAIAI-Gateway',kind:'virtual',mode:'owned',...overrides})
function syncTransport(h){
  const respond=h.respond
  h.job=syncJob()
  h.respond=call=>{
    if(call.method==='POST'&&call.path==='/api/video/asset-sync-jobs')return structuredClone(h.job)
    if(call.method==='GET'&&call.path==='/api/video/asset-sync-jobs/asj_fixture')return structuredClone(h.job)
    return respond(call)
  }
}

test('sync completion keeps readback pending and retries only GET after the first list failure',async t=>{
  const h=await harness(t);syncTransport(h)
  await h.vm.startSync();assert.equal(writes(h).length,1);assert.equal(h.vm.sync.value.status,'pending')
  h.job=syncJob({status:'succeeded',phase:'assets',pages:3,items:3});h.assets=[asset(),asset({id:'asset_second'})];h.failGroupReads=1
  await h.tick()
  assert.equal(h.vm.syncReadback.value.state,'pending');assert.match(h.vm.syncReadback.value.error,/继续读取/)
  assert.equal(h.vm.assets.value.length,0);assert.equal(client.syncSummary(h.vm.sync.value,h.vm.syncReadback.value).includes('同步已完成'),false)
  const jobReads=h.calls.filter(c=>c.path==='/api/video/asset-sync-jobs/asj_fixture').length
  await h.tick()
  assert.equal(h.vm.syncReadback.value.state,'complete');assert.equal(h.vm.assets.value.length,2)
  assert.match(client.syncSummary(h.vm.sync.value,h.vm.syncReadback.value),/同步已完成，列表已刷新/)
  assert.equal(h.calls.filter(c=>c.path==='/api/video/asset-sync-jobs/asj_fixture').length,jobReads,'terminal readback does not resubmit or repoll the provider job')
  assert.equal(writes(h).length,1)
})

test('failed sync refreshes committed partial results and shows a safe failure rather than ongoing progress',async t=>{
  const h=await harness(t);syncTransport(h);await h.vm.startSync()
  h.job=syncJob({status:'failed',phase:'assets',pages:2,items:2,error_code:'provider_sync_failed',http_status:503,upstream_request_id:'official-safe-id'})
  h.assets=[asset({source:'imported',status:'needs_review',stage:'needs_review'})];await h.tick()
  assert.equal(h.vm.assets.value.length,1);assert.equal(h.vm.syncReadback.value.state,'complete')
  assert.match(client.syncSummary(h.vm.sync.value,h.vm.syncReadback.value),/同步未完成/)
  assert.match(client.syncErrorText(h.vm.sync.value),/官方读取请求未成功/)
  assert.equal(h.vm.notice.value.includes('正在同步'),false);assert.equal(h.vm.syncActionBlocked.value,false)
  assert.equal(writes(h).length,1)
})

test('read-only sync backoff remains in progress and prevents duplicate submission',async t=>{
  const h=await harness(t);syncTransport(h);await h.vm.startSync()
  h.job=syncJob({error_code:'provider_rate_limited',http_status:429,attempts:1,retry_count:1,next_attempt_at:'2026-10-22T09:00:00Z'})
  await h.tick()
  assert.match(client.syncSummary(h.vm.sync.value,h.vm.syncReadback.value),/等待自动重试/)
  assert.match(client.syncErrorText(h.vm.sync.value),/^上次读取：官方读取请求触发限流/)
  assert.equal(h.vm.syncActionBlocked.value,true);await h.vm.startSync();assert.equal(writes(h).length,1)
  h.job=syncJob({status:'succeeded',pages:1,items:1,error_code:'',retry_count:0});await h.tick()
  assert.equal(client.syncErrorText(h.vm.sync.value),'');assert.equal(h.vm.syncReadback.value.state,'complete')
})

test('an unrelated unknown group never replaces sync feedback or repeatedly refreshes the whole list',async t=>{
  const op=operation('unknown',{operation_id:'op_unknown',kind:'CreateAssetGroup',resource_id:'grp_unknown',request_id:'req-unknown',error:{code:'provider_unavailable',message:'private upstream body',retryable:false}})
  const h=await harness(t,[group(),group({id:'grp_unknown',name:'kk',status:'needs_review',deletion_mode:'blocked',deletion_block_reason:'submission_uncertain',create_operation:op})])
  h.op=op;syncTransport(h);await h.vm.startSync();h.vm.notice.value='another explicit action notice'
  h.job=syncJob({status:'succeeded',phase:'assets',pages:2,items:2});h.assets=[asset(),asset({id:'asset_second'})]
  await h.tick()
  assert.equal(h.vm.syncReadback.value.state,'complete');assert.equal(h.vm.assets.value.length,2)
  assert.equal(h.vm.error.value,'');assert.equal(h.vm.notice.value,'another explicit action notice')
  assert.match(h.vm.resourceOperationNotice(h.vm.groups.value[1]),/官方处理暂未确认/)
  const reads=h.calls.filter(c=>c.path.startsWith('/api/video/asset-groups?')).length
  await h.tick();assert.equal(h.calls.filter(c=>c.path.startsWith('/api/video/asset-groups?')).length,reads)
  assert.equal(writes(h).length,1)
})

test('one operation lookup failure does not prevent a separate sync job from completing',async t=>{
  const op=operation('unknown',{kind:'CreateAssetGroup',resource_id:'grp_unknown'})
  const h=await harness(t,[group(),group({id:'grp_unknown',status:'needs_review',create_operation:op,deletion_mode:'blocked'})]);syncTransport(h)
  const respond=h.respond;h.respond=call=>{if(call.path.includes('/asset-operations/'))throw new Error('private upstream request');return respond(call)}
  await h.vm.startSync();h.job=syncJob({status:'succeeded',phase:'assets',pages:2,items:2});h.assets=[asset()];await h.tick()
  assert.equal(h.vm.syncReadback.value.state,'complete');assert.equal(h.vm.assets.value.length,1)
  assert.match(h.vm.operationReadErrors.value[op.operation_id],/此操作状态暂未更新/)
  assert.equal(h.vm.error.value,'');assert.equal(h.vm.operationReadErrors.value[op.operation_id].includes('private'),false)
})

test('latest owned sync restores a running task and prevents duplicate submission after reopening',async t=>{
  const h=await harness(t);syncTransport(h);const respond=h.respond
  h.respond=call=>call.path.startsWith('/api/video/asset-sync-jobs/latest?')?{job:structuredClone(h.job)}:respond(call)
  await h.vm.loadScope();assert.equal(h.vm.sync.value.id,'asj_fixture');assert.equal(h.vm.syncActionBlocked.value,true)
  await h.vm.startSync();assert.equal(writes(h).length,0)
  h.job=syncJob({status:'failed',phase:'assets',pages:1,items:1,error_code:'provider_sync_failed'});await h.tick()
  assert.equal(h.vm.syncReadback.value.state,'complete');assert.equal(h.vm.sync.value.status,'failed')
  await h.vm.loadScope();assert.equal(h.vm.sync.value.status,'failed');assert.equal(h.vm.syncReadback.value.state,'pending')
  await h.tick();assert.equal(h.vm.syncReadback.value.state,'complete');assert.equal(writes(h).length,0)
})

test('failed latest lookup blocks another POST until an explicit state refresh succeeds',async t=>{
  const h=await harness(t),respond=h.respond;let fail=true
  h.respond=call=>{if(call.path.startsWith('/api/video/asset-sync-jobs/latest?')){if(fail)throw new h.ApiError(503,'private details','req-sync-state');return {job:null}};return respond(call)}
  await h.vm.loadScope();assert.equal(h.vm.syncRecoveryRequired.value,true)
  await h.vm.startSync();assert.equal(writes(h).length,0)
  fail=false;await h.vm.refreshSyncStatus();assert.equal(h.vm.syncRecoveryRequired.value,false);assert.equal(h.vm.sync.value,undefined);assert.equal(h.vm.syncQueryError.value,'')
})

test('latest lookup rejects a different scope or kind and late results cannot replace another organization',async t=>{
  const h=await harness(t),respond=h.respond
  h.respond=call=>call.path.startsWith('/api/video/asset-sync-jobs/latest?')?{job:syncJob({scope_id:'scope_other'})}:respond(call)
  await h.vm.loadScope();assert.equal(h.vm.sync.value,undefined);assert.equal(h.vm.syncRecoveryRequired.value,true)
  const late=deferred();let latestCalls=0
  h.respond=call=>call.path.startsWith('/api/video/asset-sync-jobs/latest?')?(++latestCalls===1?late.promise:{job:null}):respond(call)
  const pending=h.vm.restoreLatestSync();await until(()=>latestCalls===1)
  h.auth.orgId=2;await until(()=>!h.vm.busy.value&&!h.vm.syncRestoring.value)
  late.resolve({job:syncJob()});await pending
  assert.equal(h.vm.sync.value,undefined);assert.equal(h.vm.syncQueryError.value,'');assert.equal(writes(h).length,0)
})

test('uncertain sync submission is recovered with GET and never automatically resent',async t=>{
  const h=await harness(t),respond=h.respond
  h.respond=call=>{if(call.method==='POST'&&call.path==='/api/video/asset-sync-jobs')throw new Error('network failure');if(call.path.startsWith('/api/video/asset-sync-jobs/latest?'))return {job:syncJob()};return respond(call)}
  await h.vm.startSync();assert.equal(h.vm.syncRecoveryRequired.value,true);assert.match(h.vm.syncQueryError.value,/是否受理尚未确认/)
  await h.vm.startSync();assert.equal(writes(h).length,1)
  await h.vm.refreshSyncStatus();assert.equal(h.vm.sync.value.id,'asj_fixture');assert.equal(h.vm.syncRecoveryRequired.value,false)
  await h.vm.startSync();assert.equal(writes(h).length,1)
})

test('official discovery completion also retries failed list readback without a second discovery POST',async t=>{
  const h=await harness(t),respond=h.respond;let job=syncJob({mode:'discover'}),failPage=false
  h.respond=call=>{
    if(call.path==='/api/admin/video/asset-discovery-jobs'||call.path.startsWith('/api/admin/video/asset-discovery-jobs/asj_fixture?'))return structuredClone(job)
    if(call.path.startsWith('/api/admin/video/asset-discoveries?')){if(failPage){failPage=false;throw new Error('private details')};return {data:[{id:'discovery_fixture'}],total:1}}
    return respond(call)
  }
  await h.vm.discover();job=syncJob({mode:'discover',status:'succeeded',pages:1,items:1});failPage=true;await h.tick()
  assert.equal(h.vm.discoveryReadback.value.state,'pending');assert.equal(h.vm.discoveries.value.length,0)
  await h.tick();assert.equal(h.vm.discoveryReadback.value.state,'complete');assert.equal(h.vm.discoveries.value.length,1)
  assert.equal(writes(h).length,1)
})

test('official Active images can be referenced and deleted without platform scanning or file uploads',async t=>{
  const h=await harness(t),row=asset({source:'imported',status:'ready',stage:'ready',provider_status:'active',media_verification_status:'unverified',generation_status:'ready',generation_block_reason:''})
  h.vm.caps.value.upload=false;h.assets=[row];await h.vm.loadAssets()
  assert.equal(client.assetStateText(row),'已入库');assert.equal(client.assetMediaStateText(row),'由官方处理，未做平台核验');assert.equal(client.assetGenerationText(row),'素材引用就绪')
  assert.equal(h.vm.assetDeleteReason(row),'');assert.equal(h.vm.uploadBlockedReason.value,'')
  assert.equal(client.groupStateText(group({create_operation:operation('unknown',{kind:'CreateAssetGroup'})})),'创建结果待核验')
  assert.equal(client.assetStateText(asset({stage:'needs_review',status:'failed',source:'imported'})),'失败')
  assert.equal(client.assetStateText(asset({stage:'needs_review',status:'processing',source:'imported'})),'处理中')
  assert.equal(writes(h).length,0)
})

test('official asset status stays independent of platform verification and reference readiness',()=>{
  const row=asset({source:'imported',media_type:'video',stage:'needs_review',provider_status:'active',media_verification_status:'unverified',generation_status:'blocked',generation_block_reason:'media_facts_unavailable'})
  assert.equal(client.assetStateText(row),'已入库');assert.equal(client.assetStateTone(row),'active')
  assert.equal(client.assetMediaStateText(row),'由官方处理，未做平台核验');assert.equal(client.assetGenerationText(row),'暂不可用于生成')
  assert.match(client.assetReviewExplanation(row),/可信的视频或音频时长/)
  const ready={...row,media_verification_status:'verified',generation_status:'ready',generation_block_reason:''}
  assert.equal(client.assetGenerationText(ready),'素材引用就绪')
  assert.match(client.assetReviewExplanation(ready),/任务接口核对模型、配额及授权/)
  for(const [provider_status,label] of [['processing','处理中'],['failed','失败'],['unknown','官方状态待确认']]){
    const other={...ready,provider_status}
    assert.equal(client.assetStateText(other),label);assert.equal(client.assetGenerationText(other),'暂不可用于生成')
  }
  assert.equal(client.assetGenerationText({...ready,generation_status:'blocked',generation_block_reason:'resource_not_ready'}),'暂不可用于生成')
  assert.match(client.assetReviewExplanation({...ready,generation_status:'blocked',generation_block_reason:'resource_not_ready'}),/素材组或使用许可/)
})

test('unknown operations override an Active projection without inventing a new official outcome',()=>{
  const row=asset({provider_status:'active',media_verification_status:'verified',generation_status:'ready'})
  for(const [field,kind,label] of [['create_operation','CreateAsset','注册结果待核验'],['delete_operation','DeleteAsset','删除结果待核验']]){
    const unknown={...row,[field]:operation('unknown',{kind,resource_id:row.id})}
    assert.equal(client.assetStateText(unknown),label);assert.equal(client.assetStateTone(unknown),'unknown')
    assert.equal(client.assetProviderState(unknown),'active');assert.equal(client.assetGenerationText(unknown),'暂不可用于生成')
  }
  assert.equal(client.assetGenerationText({...row,delete_operation:operation('pending',{kind:'DeleteAsset',resource_id:row.id})}),'暂不可用于生成')
})

test('legacy or incomplete asset responses never claim verified media or infer Active from imported source',()=>{
  const row=asset({source:'imported',status:'needs_review',stage:'needs_review',bytes:999999})
  assert.equal(client.assetStateText(row),'官方状态待确认');assert.equal(client.assetMediaStateText(row),'媒体事实未确认')
  assert.equal(client.assetGenerationText(row),'暂不可用于生成');assert.equal(client.assetSizeText(row),'大小未知')
  assert.equal(client.assetStateText(asset()),'已入库');assert.equal(client.assetGenerationText(asset()),'暂不可用于生成')
  assert.equal(client.assetStateText(asset({provider_status:'unknown'})),'官方状态待确认')
  assert.equal(client.assetStateText(asset({provider_status:'private upstream text'})),'官方状态待确认')
  assert.equal(client.assetReviewExplanation({...row,generation_status:'blocked',generation_block_reason:'private upstream text'}).includes('private'),false)
})

test('unverified or unavailable media facts display unknown instead of zero-sized files',()=>{
  for(const bytes of [0,1200000])assert.equal(client.assetSizeText(asset({bytes,media_verification_status:'unverified'})),'大小未知')
  for(const bytes of [0,-1,NaN,Infinity])assert.equal(client.assetSizeText(asset({bytes,media_verification_status:'verified'})),'大小未知')
  assert.equal(client.assetSizeText(asset({bytes:20,media_verification_status:'verified'})),'20 B')
  assert.equal(client.assetSizeText(asset({bytes:1200000,media_verification_status:'verified'})),'1.2 MB')
  assert.equal(client.assetDurationText(asset({duration_ms:3000,media_verification_status:'unverified'})),'时长未知')
  assert.equal(client.assetDurationText(asset({duration_ms:3000,media_verification_status:'verified'})),'3 秒')
})

test('official status filtering uses the provider parameter and does not reinterpret the old platform status filter',async t=>{
  const h=await harness(t)
  h.vm.providerStatus.value='active';await h.vm.loadAssets()
  const request=h.calls.filter(c=>c.path.startsWith('/api/video/assets?')).at(-1),query=new URL(request.path,'http://fixture.invalid').searchParams
  assert.equal(query.get('provider_status'),'active');assert.equal(query.has('status'),false);assert.equal(query.get('group_id'),'grp_fixture')
  assert.equal(writes(h).length,0)
})

test('sync helper validates receipts and never displays unknown raw error text',()=>{
  assert.equal(client.validSyncJob(syncJob()),true)
  for(const bad of [null,{},syncJob({pages:-1}),syncJob({items:NaN}),syncJob({id:'unsafe\nvalue'}),syncJob({id:'unsafe\n'}),syncJob({status:'raw-private-response'})])assert.equal(client.validSyncJob(bad),false)
  assert.equal(client.syncErrorText(syncJob({error_code:'private raw content'})).includes('private'),false)
  for(const code of ['provider_timeout','provider_rate_limited','provider_authentication_failed','provider_permission_denied','provider_response_invalid','provider_unavailable','sync_storage_failed','sync_evidence_unavailable','resource_identity_mismatch'])assert.doesNotMatch(client.syncErrorText(syncJob({status:'failed',error_code:code})),/请凭任务 ID 核对日志/)
  assert.match(client.syncSummary(syncJob({status:'succeeded'}),{job_id:'different',state:'complete',error:''}),/正在刷新列表/)
})


test('sync readback is independent of unavailable upload history and latest state survives an initial list error',async t=>{
  const h=await harness(t);syncTransport(h);const respond=h.respond
  h.job=syncJob({status:'succeeded',phase:'assets',pages:2,items:2});h.failGroupReads=1;h.assets=[asset(),asset({id:'asset_second'})]
  h.respond=call=>{if(call.path.startsWith('/api/video/asset-sync-jobs/latest?'))return {job:h.job};if(call.path.startsWith('/api/video/media-uploads?'))throw new Error('upload history unavailable');return respond(call)}
  await h.vm.loadScope();assert.equal(h.vm.sync.value.id,'asj_fixture');assert.equal(h.vm.syncReadback.value.state,'pending')
  await h.tick();assert.equal(h.vm.syncReadback.value.state,'complete');assert.equal(h.vm.assets.value.length,2);assert.equal(writes(h).length,0)
})

test('changing material kind restores only its matching sync and discards the previous job reply',async t=>{
  const h=await harness(t);syncTransport(h);const late=deferred(),respond=h.respond
  h.respond=call=>{
    if(call.path==='/api/video/asset-sync-jobs/asj_fixture')return late.promise
    if(call.path.startsWith('/api/video/asset-sync-jobs/latest?'))return {job:syncJob({id:'asj_real',kind:'real_person',status:'failed',error_code:'provider_sync_failed'})}
    return respond(call)
  }
  h.vm.acceptSyncJob('owned',syncJob());const pending=h.tick();await until(()=>h.calls.some(c=>c.path==='/api/video/asset-sync-jobs/asj_fixture'))
  h.vm.kind.value='real_person';await until(()=>h.vm.sync.value?.id==='asj_real'&&!h.vm.busy.value)
  late.resolve(syncJob({status:'succeeded'}));await pending
  assert.equal(h.vm.sync.value.id,'asj_real');assert.equal(h.vm.sync.value.kind,'real_person');assert.equal(writes(h).length,0)
})
