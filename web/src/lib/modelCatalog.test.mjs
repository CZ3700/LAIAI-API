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
const { filterModels, modelModality } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)

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
