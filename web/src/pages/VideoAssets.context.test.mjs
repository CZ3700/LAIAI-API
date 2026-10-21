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
      if (args.path === '@/lib/videoAssets') return { contents: `export const assetWrite = (...a) => globalThis.__videoAssets.api(...a); export const assetDelete = path => globalThis.__videoAssets.delete(path); export const assetQuery = v => new URLSearchParams(Object.entries(v).filter(([,x])=>x!==undefined&&x!=='')).toString(); export const assetPreview = () => ''; export const uploadAssetFile = () => {};` }
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
const group = (overrides = {}) => ({ id: 'grp_fixture', kind: 'virtual', name: 'xxx', description: '', status: 'ready', asset_count: 0, authorization_status: 'not_required', version: 1, source: 'platform', deletion_mode: 'official', deletion_block_reason: '', ...overrides })
const operation = (status = 'pending', overrides = {}) => ({ operation_id: 'op_fixture', resource_id: 'grp_fixture', kind: 'DeleteAssetGroup', status, ...overrides })
const writes = h => h.calls.filter(call => call.method !== 'GET')
function deferred() { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
async function until(predicate) { const end = performance.now() + 3000; while (performance.now() < end) { if (predicate()) return; await delay(5) }; assert.fail('condition not reached') }
async function harness(t, rows = [group()]) {
  const keys = ['__videoAssets', 'window', 'document', 'setInterval', 'clearInterval']
  const previous = new Map(keys.map(key => [key, globalThis[key]]))
  const h = { vue, auth: vue.reactive({ orgId: 1, token: 'fixture-session', isSuperadmin: true }), mounted: [], unmounted: [], calls: [], groups: rows, op: operation(), failGroupReads: 0 }
  h.respond = async call => {
    if (call.method === 'DELETE') {
      h.groups = h.groups.map(row => ({ ...row, status: 'deleting', deletion_mode: 'blocked', deletion_block_reason: 'operation_pending', delete_operation: operation() }))
      return structuredClone(h.op)
    }
    if (call.path === '/api/video/asset-capabilities') return { ready: true, virtual: true, delete_empty_group: true, delete: true, update: true }
    if (call.path.startsWith('/api/video/asset-operations/')) return structuredClone(h.op)
    if (call.path.startsWith('/api/video/asset-groups?')) {
      if (h.failGroupReads > 0) { h.failGroupReads--; throw new Error('sensitive raw upstream fixture') }
      return { data: structuredClone(h.groups), total: h.groups.length }
    }
    return { data: [], total: 0 }
  }
  h.api = async (method, path, body) => { const call = { method, path, body }; h.calls.push(call); return h.respond(call) }
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
  assert.match(h.vm.error.value, /Provider operation requires verification\./)
  assert.match(h.vm.error.value, /req-safe-1/)
  h.groups = [group({ status: 'deleting', deletion_mode: 'blocked', deletion_block_reason: 'submission_uncertain', delete_operation: operation('unknown') })]
  await h.vm.loadScope()
  h.op = operation('unknown', { error: { code: 'provider_unavailable', message: 'sensitive raw upstream payload', retryable: false } })
  await h.tick()
  assert.match(h.vm.groupDeleteReason(h.vm.groups.value[0]), /待核验/)
  assert.match(h.vm.error.value, /不能重复提交/)
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
