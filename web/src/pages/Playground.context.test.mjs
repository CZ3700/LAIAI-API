import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { setImmediate as nextTurn, setTimeout as delay } from 'node:timers/promises'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'
import * as vue from 'vue'

// Execute the production setup with real Vue refs/watchers. Only app services,
// lifecycle registration and network/browser boundaries are replaced; no live API.
const filename = fileURLToPath(new URL('./Playground.vue', import.meta.url))
const { descriptor } = parse(await readFile(filename, 'utf8'), { filename })
const compiled = compileScript(descriptor, { id: 'playground-context-regression' })
const built = await build({
  stdin: { contents: compiled.content, loader: 'ts', sourcefile: filename },
  bundle: true, format: 'esm', write: false,
  plugins: [{ name: 'component-boundaries', setup(b) {
    b.onResolve({ filter: /.*/ }, args => ({ path: args.path, namespace: 'boundary' }))
    b.onLoad({ filter: /.*/, namespace: 'boundary' }, args => {
      if (args.path === 'vue') return { contents: `
        export const defineComponent = value => value;
        export const ref = (...args) => globalThis.__pgHarness.vue.ref(...args);
        export const computed = (...args) => globalThis.__pgHarness.vue.computed(...args);
        export const watch = (...args) => globalThis.__pgHarness.vue.watch(...args);
        export const nextTick = (...args) => globalThis.__pgHarness.vue.nextTick(...args);
        export const onMounted = fn => globalThis.__pgHarness.mounted.push(fn);
        export const onUnmounted = fn => globalThis.__pgHarness.unmounted.push(fn);
        export const onActivated = fn => globalThis.__pgHarness.activated.push(fn);
        export const onDeactivated = fn => globalThis.__pgHarness.deactivated.push(fn);
      ` }
      if (args.path === 'vue-i18n') return { contents: 'export const useI18n = () => ({ t: key => key });' }
      if (args.path === 'vue-router') return { contents: 'export const useRoute = () => ({ query: {} });' }
      if (args.path === '@/stores/auth') return { contents: 'export const useAuth = () => globalThis.__pgHarness.auth;' }
      if (args.path === '@/lib/api') return { contents: 'export const api = (...args) => globalThis.__pgHarness.api(...args);' }
      if (args.path === '@/lib/money') return { contents: 'export const fmtUsd = value => value;' }
      if (args.path.endsWith('/workflow')) return { contents: 'export const isOfficialSeedance = () => false;' }
      if (args.path.endsWith('.vue')) return { contents: 'export default {};' }
      throw new Error(`Unexpected dependency: ${args.path}`)
    })
  } }],
})
const { default: Playground } = await import(`data:text/javascript;base64,${Buffer.from(built.outputFiles[0].text).toString('base64')}`)

function storage(seed = {}) {
  const values = new Map(Object.entries(seed))
  return { values, getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, String(value)), removeItem: key => values.delete(key) }
}
function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
function json(value) { return new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } }) }
async function until(predicate, message = 'condition did not become true') {
  const deadline = performance.now() + 3000
  while (performance.now() < deadline) { if (predicate()) return; await delay(5) }
  assert.fail(message)
}

async function harness(t, { session = {}, local = {} } = {}) {
  const originals = Object.fromEntries(['window', 'sessionStorage', 'localStorage', 'fetch', '__pgHarness'].map(key => [key, globalThis[key]]))
  const originalCreate = URL.createObjectURL, originalRevoke = URL.revokeObjectURL
  const h = {
    vue, auth: vue.reactive({ orgId: 1 }), mounted: [], unmounted: [], activated: [], deactivated: [],
    calls: [], intervals: new Map(), timeouts: new Map(), blobs: [], revoked: [], timerId: 0,
    api: async () => ({ models: [{ model: 'veo-3', provider: 'fixture', category: 'video' }] }),
    response: () => { throw new Error('Unexpected fetch') },
  }
  globalThis.__pgHarness = h
  globalThis.sessionStorage = storage({ laiai_pg_key: 'fixture-key-A', ...session })
  globalThis.localStorage = storage(local)
  h.session = sessionStorage; h.local = localStorage
  globalThis.window = {
    setInterval: fn => { const id = ++h.timerId; h.intervals.set(id, fn); return id },
    clearInterval: id => h.intervals.delete(id),
    setTimeout: fn => { const id = ++h.timerId; h.timeouts.set(id, fn); return id },
    clearTimeout: id => h.timeouts.delete(id),
    open: () => assert.fail('Unexpected new window'),
  }
  globalThis.fetch = async (url, options = {}) => {
    const call = { url, method: options.method ?? 'GET', ...options }
    h.calls.push(call)
    return h.response(call)
  }
  URL.createObjectURL = blob => { const url = `blob:fixture-${h.blobs.length}`; h.blobs.push({ blob, url }); return url }
  URL.revokeObjectURL = url => h.revoked.push(url)
  const scope = vue.effectScope()
  h.vm = scope.run(() => Playground.setup({}, { expose() {} }))
  t.after(() => {
    h.unmounted.forEach(fn => fn()); scope.stop()
    for (const [key, value] of Object.entries(originals)) {
      if (value === undefined) delete globalThis[key]
      else globalThis[key] = value
    }
    URL.createObjectURL = originalCreate; URL.revokeObjectURL = originalRevoke
  })
  for (const fn of h.mounted) await fn()
  return h
}
function prepareVideo(h) { h.vm.model.value = 'veo-3'; h.vm.videoPrompt.value = 'Fixture video'; assert.equal(h.vm.canSubmitVideo.value, true) }
function assertPrivateCleared(h) {
  assert.equal(h.vm.videoTask.value, null)
  assert.equal(h.vm.videoSrc.value, '')
  assert.equal(h.vm.lastDetail.value, null)
  assert.deepEqual(h.vm.messages.value, [])
  assert.deepEqual(h.vm.images.value, [])
  assert.equal(h.vm.lastUsage.value, null)
}

test('late create response after organization switch cannot refill or start polling', async t => {
  const h = await harness(t), response = deferred()
  h.response = () => response.promise
  prepareVideo(h)
  const submitted = h.vm.submitVideo()
  h.auth.orgId = 2
  assertPrivateCleared(h)
  response.resolve(json({ id: 'task_A', status: 'queued' }))
  await submitted
  assertPrivateCleared(h)
  assert.equal(h.vm.videoSubmitting.value, false)
  assert.equal(h.calls.length, 1)
  assert.equal(h.calls[0].headers.Authorization, 'Bearer fixture-key-A')
  assert.equal(h.intervals.size, 0)
})

test('accepted task restores only under original organization and Key, using GET without resubmitting', async t => {
  const h = await harness(t), oldPoll = deferred()
  let polls = 0
  h.response = call => call.method === 'POST'
    ? json({ id: 'task_A', status: 'queued' })
    : (++polls === 1 ? oldPoll.promise : json({ id: 'task_A', status: 'running' }))
  prepareVideo(h)
  await h.vm.submitVideo()
  assert.equal(h.vm.videoTask.value.id, 'task_A')
  const taskKeys = [...h.session.values.keys()].filter(key => key.startsWith('laiai_pg_video_task.v2.'))
  assert.equal(taskKeys.length, 1)
  assert.match(taskKeys[0], /^laiai_pg_video_task\.v2\.1\.[a-f0-9]{64}$/)
  h.auth.orgId = 2
  await until(() => !h.vm.restoringContext.value)
  assertPrivateCleared(h)
  assert.equal(h.intervals.size, 0)
  oldPoll.resolve(json({ id: 'task_A', status: 'succeeded', result_url: 'https://media.invalid/private-A.mp4' }))
  await nextTurn()
  assertPrivateCleared(h)
  assert.equal(h.calls.length, 2)
  h.vm.apiKey.value = 'fixture-key-B'
  h.auth.orgId = 1
  await until(() => !h.vm.restoringContext.value)
  assertPrivateCleared(h)
  assert.equal(h.calls.length, 2)
  h.vm.apiKey.value = 'fixture-key-A'
  await until(() => h.vm.videoTask.value?.status === 'running')
  assert.equal(h.vm.videoTask.value.id, 'task_A')
  assert.equal(h.calls.filter(call => call.method === 'POST').length, 1)
  assert.equal(h.calls.length, 3)
  assert.equal(h.calls[2].method, 'GET')
  assert.equal(h.calls[2].headers.Authorization, 'Bearer fixture-key-A')
})

test('returning to the original org does not validate an earlier in-flight generation', async t => {
  const h = await harness(t), response = deferred()
  h.response = () => response.promise
  prepareVideo(h)
  const submitted = h.vm.submitVideo()
  h.auth.orgId = 2
  h.auth.orgId = 1
  await until(() => !h.vm.restoringContext.value)
  response.resolve(json({ id: 'task_before_switch', status: 'queued' }))
  await submitted
  assertPrivateCleared(h)
  assert.equal(h.calls.length, 1)
})

test('late media body is discarded, download aborted, and existing Blob revoked on Key switch', async t => {
  const h = await harness(t), media = deferred()
  h.response = call => call.method === 'POST'
    ? json({ id: 'task_A', status: 'queued' })
    : call.url.startsWith('https:') ? { ok: true, blob: () => media.promise }
    : json({ id: 'task_A', status: 'succeeded', result_url: 'https://media.invalid/private-A.mp4' })
  prepareVideo(h)
  await h.vm.submitVideo()
  await until(() => h.calls.length === 3)
  h.vm.apiKey.value = 'fixture-key-B'
  assert.equal(h.calls[2].signal.aborted, true)
  media.resolve(new Blob(['private fixture video']))
  await nextTurn()
  assertPrivateCleared(h)
  assert.equal(h.blobs.length, 0)
  assert.equal(h.calls.length, 3, 'stale media must not fall back to a new proxy fetch')

  await until(() => !h.vm.restoringContext.value)
  h.response = call => call.method === 'POST' ? json({ id: 'task_B', status: 'queued' })
    : call.url.startsWith('https:') ? new Response('fixture B')
    : json({ id: 'task_B', status: 'succeeded', content: { video_url: 'https://media.invalid/B.mp4' } })
  prepareVideo(h)
  await h.vm.submitVideo()
  await until(() => !!h.vm.videoSrc.value)
  const url = h.vm.videoSrc.value
  h.auth.orgId = 2
  assert.ok(h.revoked.includes(url))
  assertPrivateCleared(h)
})

test('late SSE frames cannot refill messages/usage or clear the next context busy state', async t => {
  const h = await harness(t), second = deferred()
  let source
  h.response = () => new Response(new ReadableStream({ start(controller) { source = controller } }))
  h.vm.model.value = 'gpt-fixture'
  h.vm.input.value = 'A private prompt'
  const sent = h.vm.send()
  await nextTurn()
  const firstSignal = h.calls[0].signal
  h.auth.orgId = 2
  assert.equal(firstSignal.aborted, true)
  assertPrivateCleared(h)
  await until(() => !h.vm.restoringContext.value)
  h.response = () => second.promise
  h.vm.input.value = 'B prompt'
  h.vm.stream.value = false
  const next = h.vm.send()
  source.enqueue(new TextEncoder().encode('data: {"choices":[{"delta":{"content":"A secret"}}],"usage":{"prompt_tokens":9,"completion_tokens":9}}\n\n'))
  await sent
  assert.equal(h.vm.sending.value, true)
  assert.equal(h.vm.messages.value[0].content, 'B prompt')
  assert.equal(h.vm.messages.value[1].content, '')
  assert.equal(h.vm.lastUsage.value, null)
  assert.equal(h.vm.lastDetail.value, null)
  second.resolve(json({ choices: [{ message: { content: 'B reply' } }], usage: { prompt_tokens: 1, completion_tokens: 2 } }))
  await next
  assert.equal(h.vm.messages.value[1].content, 'B reply')
  assert.deepEqual(h.vm.lastUsage.value, { prompt_tokens: 1, completion_tokens: 2 })
})

test('late image JSON and nonstream chat JSON never refill the switched Key', async t => {
  const h = await harness(t), imageBody = deferred(), chatBody = deferred()
  h.response = call => ({ ok: true, status: 200, json: () => call.url.includes('/images/') ? imageBody.promise : chatBody.promise })
  h.vm.model.value = 'gpt-image-fixture'; h.vm.imagePrompt.value = 'A image'
  const generated = h.vm.generate()
  await nextTurn()
  h.vm.model.value = 'gpt-chat-fixture'; h.vm.input.value = 'A chat'; h.vm.stream.value = false
  const sent = h.vm.send()
  await nextTurn()
  h.vm.apiKey.value = 'fixture-key-B'
  imageBody.resolve({ data: [{ b64_json: 'privateA' }] })
  chatBody.resolve({ choices: [{ message: { content: 'A reply' } }] })
  await Promise.all([generated, sent])
  assertPrivateCleared(h)
})

test('scoped chat restores only for the same org/Key; old unattributed caches are ignored and preserved', async t => {
  const oldChat = JSON.stringify({ at: Date.now(), model: 'gpt-fixture', messages: [{ role: 'user', content: 'legacy private' }] })
  const h = await harness(t, { session: { laiai_pg_video_task: 'task_legacy' }, local: { laiai_pg_chat: oldChat } })
  assertPrivateCleared(h)
  assert.equal(h.calls.length, 0)
  h.vm.model.value = 'gpt-fixture'
  h.vm.messages.value = [{ role: 'user', content: 'scoped A' }]
  h.vm.persistChat()
  const keys = [...h.local.values.keys()].filter(key => key.startsWith('laiai_pg_chat.v2.'))
  assert.equal(keys.length, 1)
  assert.match(keys[0], /^laiai_pg_chat\.v2\.1\.[a-f0-9]{64}$/)
  h.auth.orgId = 2
  await until(() => !h.vm.restoringContext.value)
  assertPrivateCleared(h)
  h.auth.orgId = 1
  await until(() => !h.vm.restoringContext.value)
  assert.equal(h.vm.messages.value[0].content, 'scoped A')
  h.vm.apiKey.value = 'fixture-key-B'
  await until(() => !h.vm.restoringContext.value)
  assertPrivateCleared(h)
  assert.equal(h.local.getItem('laiai_pg_chat'), oldChat)
  assert.equal(h.session.getItem('laiai_pg_video_task'), 'task_legacy')
  assert.equal(h.calls.length, 0)
})
