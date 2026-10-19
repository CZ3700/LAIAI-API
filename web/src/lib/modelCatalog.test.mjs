import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

const sourcePath = join(dirname(fileURLToPath(import.meta.url)), 'modelCatalog.ts')
const source = await readFile(sourcePath, 'utf8')
const compiled = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
}).outputText
const { filterModels, modelModality, catalogCategory, catalogPricing, standardVideoRows, modelSnippet } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)

const seedance = {
  model: 'seedance-2.0-pro',
  provider: 'byteplus',
  vendor: 'BytePlus',
  category: 'video',
  tags: 'generation,cinematic',
  input_usd_per_1m: '0',
  output_usd_per_1m: '0',
  image_usd: '0',
}

const chat = {
  model: 'gpt-4o-mini',
  provider: 'openai',
  vendor: 'OpenAI',
  category: 'chat',
  tags: 'tools,vision',
  input_usd_per_1m: '0.15',
  output_usd_per_1m: '0.60',
  image_usd: '0',
}

test('classifies video models from metadata and known video model names', () => {
  assert.equal(modelModality(seedance), 'video')
  assert.equal(modelModality({ ...seedance, category: '', tags: '' }), 'video')
})

test('search matches video category and tags as well as model and vendor fields', () => {
  assert.deepEqual(
    filterModels([seedance, chat], 'video').map((m) => m.model),
    ['seedance-2.0-pro'],
  )
  assert.deepEqual(
    filterModels([seedance, chat], 'byteplus').map((m) => m.model),
    ['seedance-2.0-pro'],
  )
  assert.deepEqual(
    filterModels([seedance, chat], 'tools').map((m) => m.model),
    ['gpt-4o-mini'],
  )
  assert.deepEqual(
    filterModels([seedance, chat], '视频').map((m) => m.model),
    ['seedance-2.0-pro'],
  )
})

const combination = (resolution, input_video, amount, mode = 'generate') => ({ key: `${mode}/${resolution}/${input_video}/default/false`, mode, resolution, input_video, amount, service_tier: 'default', draft: false })
const officialVideo = { ...seedance, model: 'seedance-2.0', pricing: { billing_kind: 'video', currency: 'USD', unit: 'USD/1M_video_tokens', status: 'partial', rates: [], combinations: [combination('480p', false, '12.00000001'), combination('1080p', true, '7.12345678'), combination('720p', true, '4.1', 'edit')], endpoints: ['/v1/video/generations'] } }
test('video display preserves exact prices and missing combinations without generic zero fallback', () => {
  assert.equal(catalogPricing(seedance), undefined)
  assert.deepEqual(standardVideoRows(officialVideo), [
    { resolution: '480p', without: '12.00000001', with: undefined },
    { resolution: '1080p', without: undefined, with: '7.12345678' },
  ])
  assert.equal(catalogPricing(officialVideo).combinations.length, 3)
})
test('authoritative metadata takes priority over model-name heuristics', () => {
  assert.equal(catalogCategory({ ...chat, model: 'seedance-named-text', pricing: { ...officialVideo.pricing, billing_kind: 'token', endpoints: ['/v1/chat/completions'] } }), 'text')
  assert.equal(catalogCategory({ ...chat, pricing: { ...officialVideo.pricing, billing_kind: 'audio', endpoints: ['/v1/audio/speech'] } }), 'audio')
  assert.equal(modelModality({ ...chat, pricing: { ...officialVideo.pricing, billing_kind: 'audio', endpoints: ['/v1/audio/speech'] } }), 'audio')
  assert.equal(modelModality({ ...chat, model: 'seedance-named-text', pricing: { ...officialVideo.pricing, billing_kind: 'token', endpoints: ['/v1/chat/completions'] } }), 'chat')
  assert.equal(catalogCategory({ ...chat, pricing: { ...officialVideo.pricing, billing_kind: 'per_call', endpoints: ['/v1/chat/completions'] } }), 'text')
  assert.equal(catalogCategory({ ...chat, pricing: { ...officialVideo.pricing, billing_kind: 'token', endpoints: ['/v1/images/generations'] } }), 'image')
  assert.equal(catalogCategory({ ...chat, pricing: { ...officialVideo.pricing, billing_kind: 'token', endpoints: ['/v1/embeddings'] } }), 'embedding')
})
test('video snippets in every language use the official task endpoint and stable idempotency', () => {
  for (const language of ['curl', 'python', 'js']) {
    const snippet = modelSnippet(officialVideo, language, 'https://api.example.invalid')
    assert.match(snippet, /\/v1\/video\/generations/)
    assert.match(snippet, /Idempotency-Key/)
    assert.match(snippet, /REQUEST_ID/)
    assert.match(snippet, /480p/)
    assert.doesNotMatch(snippet, /chat\/completions/)
  }
  assert.equal(modelSnippet({ ...officialVideo, pricing: { ...officialVideo.pricing, combinations: [combination('720p', true, '5')] } }, 'curl', ''), '')
  assert.equal(modelSnippet({ ...officialVideo, pricing: { ...officialVideo.pricing, billing_kind: 'generic_video' } }, 'python', ''), '')
})
test('explicit free rate remains distinct from unavailable pricing', () => {
  const zero = { ...chat, pricing: { ...officialVideo.pricing, billing_kind: 'token', rates: [{ key: 'input_usd_per_1m', amount: '0', unit: 'USD/1M_tokens' }], combinations: [] } }
  assert.equal(catalogPricing(zero).rates[0].amount, '0')
  assert.equal(catalogPricing({ ...chat, input_usd_per_1m: '0', output_usd_per_1m: '0' }).rates.length, 2)
  assert.equal(catalogPricing({ ...chat, input_usd_per_1m: '', output_usd_per_1m: '' }), undefined)
})
