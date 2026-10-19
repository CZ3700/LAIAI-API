import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'
import { baseCompile } from '@intlify/message-compiler'

const result = await build({ entryPoints: [fileURLToPath(new URL('./workflow.ts', import.meta.url))], bundle: true, format: 'esm', write: false })
const flow = await import(`data:text/javascript;base64,${Buffer.from(result.outputFiles[0].text).toString('base64')}`)

test('unknown submission remains active; generation completion does not stop pending artifact or billing observation', () => {
  for (const status of ['accepted', 'submitting', 'submission_unknown', 'queued', 'running']) assert.equal(flow.generationActive(status), true)
  assert.equal(flow.shouldPoll({ status: 'succeeded', artifact_status: 'pending', billing_state: 'settled' }), true)
  assert.equal(flow.shouldPoll({ status: 'failed', artifact_status: 'failed', billing_state: 'pending_usage' }), true)
  assert.equal(flow.shouldPoll({ status: 'succeeded', artifact_status: 'ready', billing_state: 'settled' }), false)
  assert.equal(flow.shouldPoll({ status: 'cancelled', artifact_status: 'failed', billing_state: 'released' }), false)
})

test('accepted quote is copied into the request without floating point conversion or mutation', () => {
  const input = { model: 'seedance-2.5', content: [{ type: 'text', text: 'fixture' }] }
  const output = flow.authorizedRequest(input, { reserved_amount: '0.00000001', pricing_version: 'immutable-v1' })
  assert.equal(output.max_charge_usd, '0.00000001')
  assert.equal(output.expected_pricing_version, 'immutable-v1')
  assert.equal('max_charge_usd' in input, false)
  for (const amount of ['-1', 'NaN', '1e3', '0.000000001']) assert.throws(() => flow.authorizedRequest(input, { reserved_amount: amount, pricing_version: 'v1' }))
  assert.throws(() => flow.authorizedRequest(input, { reserved_amount: '1', pricing_version: '' }))
})

test('official credentials are never sent to a provider content URL supplied in a task', () => {
  const expected = '/v1/video/generations/task_test/content'
  assert.equal(flow.safeContentPath('task_test', 'https://untrusted.example/video'), expected)
  assert.equal(flow.safeContentPath('task_test', '//untrusted.example/video'), expected)
  assert.equal(flow.safeContentPath('task_test', '/v1/video/generations/task_other/content'), expected)
  assert.equal(flow.safeContentPath('task_test', expected), expected)
})

test('Ark UI selection preserves unrelated and earlier legacy models', () => {
  for (const model of ['seedance-2.0', 'seedance-2.0-fast', 'seedance-2.0-mini', 'seedance-2.5', 'doubao-seedance-2-0-260128', 'doubao-seedance-2-0-fast-260128', 'doubao-seedance-2-0-mini-260615', 'doubao-seedance-2-5-260628']) assert.equal(flow.isOfficialSeedance(model), true)
  for (const model of ['seedance-test', 'seedance-1.5', 'veo-3']) assert.equal(flow.isOfficialSeedance(model), false)
})

test('media references use the official content contract and defaults remain omitted', () => {
  const input = { model: 'seedance-2.5', prompt: ' fixture ', image: ' asset://asset_image ', video: 'https://media.example/video', audio: 'asset://asset_audio', duration: 'default', resolution: 'default', ratio: 'default' }
  const result = flow.createBody(input)
  assert.deepEqual(result.content, [
    { type: 'text', text: 'fixture' },
    { type: 'image_url', image_url: { url: 'asset://asset_image' }, role: 'reference_image' },
    { type: 'video_url', video_url: { url: 'https://media.example/video' }, role: 'reference_video' },
    { type: 'audio_url', audio_url: { url: 'asset://asset_audio' }, role: 'reference_audio' },
  ])
  for (const key of ['duration', 'resolution', 'ratio', 'service_tier']) assert.equal(key in result, false)
  const automatic = flow.createBody({ ...input, duration: '-1', resolution: '480p', ratio: '1:1' })
  assert.equal(automatic.duration, -1)
  assert.equal(automatic.resolution, '480p')
  assert.equal(automatic.ratio, '1:1')
})

test('deletion waits for financial finality and never treats an unknown task as terminal', () => {
  assert.equal(flow.canDelete({ status: 'succeeded', billing_state: 'pending_usage' }), false)
  assert.equal(flow.canDelete({ status: 'submission_unknown', billing_state: 'reserved' }), false)
  assert.equal(flow.canDelete({ status: 'failed', billing_state: 'released' }), true)
  assert.equal(flow.canDelete({ status: 'succeeded', billing_state: 'settled' }), true)
  assert.equal(flow.canDelete({ status: 'succeeded', billing_state: 'settled', deletion_state: 'deleted' }), false)
  assert.equal(flow.canDelete({ status: 'unknown', billing_state: 'settled' }), false)
})

test('Seedance component translations compile and preserve the same keys', async () => {
  const compiled = await build({
    entryPoints: [fileURLToPath(new URL('./messages.ts', import.meta.url))], bundle: true, format: 'esm', write: false,
    plugins: [{ name: 'message-data-only', setup(b) {
      b.onResolve({ filter: /^vue-i18n$/ }, () => ({ path: 'vue-i18n', namespace: 'stub' }))
      b.onLoad({ filter: /.*/, namespace: 'stub' }, () => ({ contents: 'export function useI18n() { throw new Error("not a component test") }' }))
    } }],
  })
  const { seedanceMessages } = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString('base64')}`)
  const expected = Object.keys(seedanceMessages.en.seedance).sort()
  for (const locale of Object.values(seedanceMessages)) {
    assert.deepEqual(Object.keys(locale.seedance).sort(), expected)
    for (const message of Object.values(locale.seedance)) baseCompile(message, { onError(e) { throw e } })
  }
})
