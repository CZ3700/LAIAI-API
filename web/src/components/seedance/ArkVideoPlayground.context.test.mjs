import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { setTimeout as delay } from 'node:timers/promises'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'
import * as vue from 'vue'

// Execute the actual Seedance setup and workflow; mock only browser/network
// boundaries so these regressions cannot create paid upstream requests.
const filename = fileURLToPath(new URL('./ArkVideoPlayground.vue', import.meta.url))
const compiled = compileScript(parse(await readFile(filename, 'utf8'), {filename}).descriptor, {id: 'ark-playground-context'})
const built = await build({
  stdin: {contents: compiled.content, loader: 'ts', sourcefile: filename}, bundle: true, format: 'esm', write: false,
  plugins: [{name: 'ark-boundaries', setup(b) {
    b.onResolve({filter: /.*/}, args => ({path: args.path, namespace: 'boundary'}))
    b.onLoad({filter: /.*/, namespace: 'boundary'}, async args => {
      if (args.path === 'vue') return {contents: `
        export const defineComponent = value => value;
        export const ref = (...args) => globalThis.__arkHarness.vue.ref(...args);
        export const computed = (...args) => globalThis.__arkHarness.vue.computed(...args);
        export const watch = (...args) => globalThis.__arkHarness.vue.watch(...args);
        export const onMounted = fn => globalThis.__arkHarness.mounted.push(fn);
        export const onActivated = fn => globalThis.__arkHarness.activated.push(fn);
        export const onDeactivated = fn => globalThis.__arkHarness.deactivated.push(fn);
        export const onBeforeUnmount = fn => globalThis.__arkHarness.unmounted.push(fn);
      `}
      if (args.path === '@/stores/auth') return {contents: 'export const useAuth = () => globalThis.__arkHarness.auth;'}
      if (args.path === '@/lib/api') return {contents: 'export const uuid = () => "fixture-operation-" + ++globalThis.__arkHarness.uuid;'}
      if (args.path === './messages') return {contents: 'export const useSeedanceI18n = () => ({t: key => key});'}
      if (args.path === './workflow') return {contents: await readFile(new URL('./workflow.ts', import.meta.url), 'utf8'), loader: 'ts'}
      if (args.path.endsWith('.vue')) return {contents: 'export default {};'}
      throw new Error(`Unexpected dependency ${args.path}`)
    })
  }}],
})
const {default: ArkVideoPlayground} = await import(`data:text/javascript;base64,${Buffer.from(built.outputFiles[0].text).toString('base64')}`)
function deferred() { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return {promise, resolve, reject} }
function json(value, status = 200, headers = {}) { return new Response(JSON.stringify(value), {status, headers: {'Content-Type': 'application/json', ...headers}}) }
function task(id, values = {}) { return {id, model: 'seedance-2.0', status: 'running', billing_state: 'reserved', artifact_status: 'pending', ...values} }
async function until(predicate) { const end = performance.now() + 3000; while (performance.now() < end) { if (predicate()) return; await delay(5) } assert.fail('condition timed out') }
async function harness(t) {
  const originals = Object.fromEntries(['window', 'sessionStorage', 'fetch', '__arkHarness'].map(key => [key, globalThis[key]]))
  const originalCreate = URL.createObjectURL, originalRevoke = URL.revokeObjectURL
  const values = new Map()
  const h = {
    vue, auth: vue.reactive({orgId: 1}), props: vue.reactive({model: 'seedance-2.0', apiKey: 'fixture-key-A'}),
    calls: [], events: [], mounted: [], activated: [], deactivated: [], unmounted: [], intervals: new Map(), blobs: [], revoked: [], uuid: 0, timer: 0,
    respond: () => { throw new Error('Unexpected mock fetch') },
    storage: {getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key)},
  }
  globalThis.__arkHarness = h
  globalThis.sessionStorage = h.storage
  globalThis.window = {setInterval: fn => {const id = ++h.timer; h.intervals.set(id, fn); return id}, clearInterval: id => h.intervals.delete(id), confirm: () => false}
  URL.createObjectURL = blob => { h.blobs.push(blob); return `blob:fixture-${h.blobs.length}` }
  URL.revokeObjectURL = url => h.revoked.push(url)
  globalThis.fetch = async (path, options) => {
    const call = {path, options}
    h.calls.push(call)
    if (path === '/v1/video/models') return json({data: [{id: 'seedance-2.0', enabled: true, verified: true, capabilities: {resolutions: ['720p'], input_video: true, input_audio: true}}]})
    return h.respond(call)
  }
  const scope = vue.effectScope()
  h.vm = scope.run(() => ArkVideoPlayground.setup(h.props, {expose() {}, emit: (...args) => h.events.push(args)}))
  t.after(() => {
    h.unmounted.forEach(fn => fn()); scope.stop()
    URL.createObjectURL = originalCreate; URL.revokeObjectURL = originalRevoke
    for (const [key, value] of Object.entries(originals)) { if (value === undefined) delete globalThis[key]; else globalThis[key] = value }
  })
  h.mounted.forEach(fn => fn())
  await until(() => !!h.vm.capabilities.value)
  return h
}

test('switching org aborts a hung old poll and its completion cannot unlock the new poll', async t => {
  const h = await harness(t), old = deferred(), current = deferred()
  h.respond = call => call.path.endsWith('task_old') ? old.promise : call.path.endsWith('task_new') ? current.promise : json({})
  h.vm.task.value = task('task_old')
  const previous = h.vm.poll()
  const oldCall = h.calls.at(-1)
  h.auth.orgId = 2
  await vue.nextTick()
  assert.equal(oldCall.options.signal.aborted, true)
  h.vm.task.value = task('task_new')
  const next = h.vm.poll()
  assert.equal(h.calls.at(-1).path, '/v1/video/generations/task_new')
  old.resolve(json(task('task_old'))); await previous
  const count = h.calls.length
  await h.vm.poll()
  assert.equal(h.calls.length, count, 'the old finally must not release the current request lock')
  current.resolve(json(task('task_new'))); await next
  assert.equal(h.vm.task.value.id, 'task_new')
  assert.equal(h.vm.error.value, '')
})

test('switching Key during operation polling aborts the old credentials and starts the new task observation', async t => {
  const h = await harness(t), old = deferred()
  h.respond = call => call.path.includes('/operations/') ? old.promise : call.path.endsWith('/billing') ? json({}) : json(task('task_new'))
  h.vm.task.value = task('task_old')
  h.vm.cancelOperation.value = {operation_id: 'op_old', status: 'pending'}
  const previous = h.vm.poll()
  const operation = h.calls.at(-1)
  h.props.apiKey = 'fixture-key-B'
  await vue.nextTick()
  assert.equal(operation.options.signal.aborted, true)
  h.vm.task.value = task('task_new')
  await h.vm.poll()
  const next = h.calls.find(call => call.path.endsWith('task_new'))
  assert.equal(next.options.headers.Authorization, 'Bearer fixture-key-B')
  old.resolve(json({operation_id: 'op_old', status: 'confirmed'})); await previous
  assert.equal(h.calls.some(call => call.path.endsWith('task_old')), false)
  assert.equal(h.vm.cancelOperation.value, null)
})

test('changing credentials clears visible direct links and late task results cannot restore them', async t => {
  const h = await harness(t), old = deferred()
  h.vm.task.value = task('task_old', {status: 'succeeded', artifact_status: 'ready', content_url: 'https://provider.example/old.mp4'})
  assert.equal(h.vm.src.value, 'https://provider.example/old.mp4')
  h.respond = () => old.promise
  const previous = h.vm.poll()
  h.props.apiKey = 'fixture-key-B'
  await vue.nextTick()
  assert.equal(h.vm.src.value, '')
  old.resolve(json(task('task_old', {status: 'succeeded', artifact_status: 'ready', content_url: 'https://provider.example/old.mp4'})))
  await previous
  assert.equal(h.vm.src.value, '')
  assert.equal(h.vm.task.value, null)
})

test('a terminal old poll awaiting billing cannot stop the new org timer or restore its links', async t => {
  const h = await harness(t), bill = deferred(), current = deferred()
  h.respond = call => call.path.endsWith('task_old') ? json(task('task_old', {status: 'succeeded', billing_state: 'settled', artifact_status: 'ready', content_url: 'https://provider.example/old.mp4'}))
    : call.path === '/v1/video/generations/task_old/billing' ? bill.promise
    : call.path.endsWith('task_new') ? current.promise : json({})
  h.vm.task.value = task('task_old')
  const previous = h.vm.poll()
  await until(() => h.calls.some(call => call.path.endsWith('/billing')))
  assert.equal(h.vm.src.value, 'https://provider.example/old.mp4')
  const record = JSON.stringify({id: 'task_new', model: 'seedance-2.0'})
  h.storage.setItem('laiai.ark.task.2', record)
  h.auth.orgId = 2
  await vue.nextTick()
  assert.equal(h.intervals.size, 1)
  assert.equal(h.calls.at(-1).path, '/v1/video/generations/task_new')
  bill.resolve(json({})); await previous
  assert.equal(h.intervals.size, 1, 'old terminal work must not stop the new task timer')
  assert.equal(h.storage.getItem('laiai.ark.task.2'), record)
  assert.equal(h.blobs.length, 0)
  assert.equal(h.vm.src.value, '')
  current.resolve(json(task('task_new'))); await until(() => h.vm.task.value?.id === 'task_new')
})

test('submitting another task takes observation ownership and clears the previous task links', async t => {
  const h = await harness(t), oldBill = deferred(), current = deferred()
  h.respond = call => {
    if (call.path === '/v1/video/estimates') return json({reserved_amount: '0.125', pricing_version: 'price-kept'})
    if (call.path === '/v1/video/generations') return json(task('task_new', {status: 'accepted'}), 202)
    if (call.path.endsWith('task_old')) return json(task('task_old', {status: 'succeeded', billing_state: 'settled', artifact_status: 'ready', content_url: 'https://provider.example/old.mp4'}))
    if (call.path === '/v1/video/generations/task_old/billing') return oldBill.promise
    if (call.path.endsWith('task_new')) return current.promise
    return json({})
  }
  h.vm.task.value = task('task_old')
  const previous = h.vm.poll()
  await until(() => h.calls.some(call => call.path.endsWith('/billing')))
  const oldRequests = h.calls.filter(call => call.path.includes('task_old'))
  h.vm.prompt.value = 'The next local fixture task'
  await vue.nextTick()
  await h.vm.estimate()
  await h.vm.submit()
  assert.ok(oldRequests.every(call => call.options.signal.aborted))
  assert.equal(h.calls.at(-1).path, '/v1/video/generations/task_new')
  oldBill.resolve(json({})); await previous
  assert.equal(h.vm.task.value.id, 'task_new')
  assert.equal(h.vm.src.value, '')
  assert.equal(h.blobs.length, 0)
  assert.equal(h.intervals.size, 1)
  assert.equal(JSON.parse(h.storage.getItem('laiai.ark.task.1')).id, 'task_new')
  const count = h.calls.length
  await h.vm.poll()
  assert.equal(h.calls.length, count, 'the new poll retains its lock after the previous task finishes')
  current.resolve(json(task('task_new')))
  await until(() => h.vm.task.value.status === 'running')
})

test('unmount aborts polling and discards a late direct result link', async t => {
  const h = await harness(t), oldPoll = deferred()
  h.respond = () => oldPoll.promise
  h.vm.task.value = task('task_old')
  const poll = h.vm.poll()
  const requests = h.calls.filter(call => call.path.includes('task_old'))
  h.unmounted.forEach(fn => fn())
  assert.ok(requests.every(call => call.options.signal.aborted))
  oldPoll.resolve(json(task('task_old', {status: 'succeeded', artifact_status: 'ready', content_url: 'https://provider.example/old.mp4'})))
  await poll
  assert.equal(h.vm.task.value, null)
  assert.equal(h.vm.src.value, '')
  assert.equal(h.blobs.length, 0)
})

test('actual Ark setup estimates, submits and uses the provider link directly without media fetch or Blob', async t => {
  const h = await harness(t)
  h.respond = call => {
    if (call.path === '/v1/video/estimates') return json({reserved_amount: '0.125', estimated_tokens: 1000, pricing_version: 'price-kept', billing_rule_version: 'rule-kept', capability_version: 'cap-kept'})
    if (call.path === '/v1/video/generations') return json(task('task_created', {status: 'accepted'}), 202, {'X-Request-Id': 'request-fixture'})
    if (call.path.endsWith('/billing')) return json({billable_tokens: 900, reserved_amount: '0.125', settled_amount: '0.1', released_amount: '0.025'})
    return json(task('task_created', {status: 'succeeded', billing_state: 'settled', artifact_status: 'ready', content_url: 'https://provider.example/video.mp4?sig=kept%2Fexactly', content: {video_url: 'https://provider.example/video.mp4?sig=kept%2Fexactly', last_frame_url: 'https://provider.example/last.jpg'}}))
  }
  h.vm.prompt.value = 'A local fixture prompt'
  await vue.nextTick()
  await h.vm.estimate()
  assert.equal(h.vm.quote.value.reserved_amount, '0.125')
  await h.vm.submit()
  await until(() => h.vm.src.value !== '' && h.intervals.size === 0)
  const creation = h.calls.find(call => call.path === '/v1/video/generations')
  assert.equal(JSON.parse(creation.options.body).max_charge_usd, '0.125')
  assert.equal(JSON.parse(creation.options.body).expected_pricing_version, 'price-kept')
  assert.ok(creation.options.headers['Idempotency-Key'])
  assert.equal(h.vm.requestID.value, 'request-fixture')
  assert.equal(h.vm.billing.value.billable_tokens, 900)
  assert.equal(h.calls.at(-1).path, '/v1/video/generations/task_created/billing')
  assert.equal(h.calls.at(-1).options.headers.Authorization, 'Bearer fixture-key-A')
  assert.equal(h.vm.src.value, 'https://provider.example/video.mp4?sig=kept%2Fexactly')
  assert.equal(h.vm.lastFrameURL.value, 'https://provider.example/last.jpg')
  assert.equal(h.calls.some(call => !call.path.startsWith('/v1/video/') || call.path.endsWith('/content')), false)
  assert.equal(h.blobs.length, 0)
  assert.equal(h.storage.getItem('laiai.ark.task.1'), null)
})

test('refresh replaces a signed link and rejects another task response or unsafe media URLs', async t => {
  const h = await harness(t)
  h.vm.task.value = task('task_current', {status: 'succeeded', billing_state: 'settled', artifact_status: 'ready', content_url: 'https://provider.example/old.mp4'})
  let result = task('task_current', {status: 'succeeded', billing_state: 'settled', artifact_status: 'ready', content: {video_url: 'https://provider.example/new.mp4', last_frame_url: 'javascript:alert(1)'}})
  h.respond = call => call.path.endsWith('/billing') ? json({}) : json(result)
  await h.vm.poll()
  assert.equal(h.vm.src.value, 'https://provider.example/new.mp4')
  assert.equal(h.vm.lastFrameURL.value, '')
  result = {...result, id: 'task_other', content: {video_url: 'https://provider.example/other.mp4'}}
  await h.vm.poll()
  assert.equal(h.vm.src.value, 'https://provider.example/new.mp4')
  assert.equal(h.vm.error.value, 'seedance.unexpectedTask')
  for (const video_url of ['http://provider.example/video', 'javascript:alert(1)', '/v1/video/generations/task_current/content', 'https://user:secret@provider.example/video']) {
    result = {...result, id: 'task_current', content: {video_url}}
    await h.vm.poll()
    assert.equal(h.vm.src.value, '')
  }
  result = {...result, content: {video_url: 'https://provider.example/new.mp4'}, deletion_state: 'deleted'}
  await h.vm.poll()
  assert.equal(h.vm.src.value, '')
})

test('uncertain task submission retries the original idempotency key and authorized quote', async t => {
  const h = await harness(t)
  let attempts = 0
  h.respond = call => {
    if (call.path === '/v1/video/estimates') return json({reserved_amount: '0.125', pricing_version: 'price-kept'})
    if (call.path === '/v1/video/generations') { if (++attempts === 1) throw new Error('response lost'); return json(task('task_retry')) }
    return call.path.endsWith('/billing') ? json({}) : json(task('task_retry'))
  }
  h.vm.prompt.value = 'Fixture'
  await vue.nextTick()
  await h.vm.estimate(); await h.vm.submit()
  assert.equal(h.vm.uncertain.value, true)
  await h.vm.submit()
  const submissions = h.calls.filter(call => call.path === '/v1/video/generations')
  assert.equal(submissions.length, 2)
  assert.equal(submissions[0].options.body, submissions[1].options.body)
  assert.equal(submissions[0].options.headers['Idempotency-Key'], submissions[1].options.headers['Idempotency-Key'])
  assert.equal(h.vm.uncertain.value, false)
})

test('authorization failure stops polling and displays the server error without requesting billing or media', async t => {
  const h = await harness(t)
  h.respond = () => json({error: {message: 'task access denied'}}, 403)
  h.vm.task.value = task('task_denied')
  h.vm.start()
  await until(() => h.vm.error.value !== '')
  assert.equal(h.vm.error.value, 'task access denied')
  assert.equal(h.intervals.size, 0)
  assert.equal(h.calls.some(call => call.path.endsWith('/billing') || call.path.endsWith('/content')), false)
})
