import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import ts from 'typescript'

const here = dirname(fileURLToPath(import.meta.url))
const root = resolve(here, '../../..')
const source = await readFile(resolve(here, 'apiDocs.ts'), 'utf8')
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 } }).outputText
const { docPages, docGroups, searchPages, curlFor } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)
// JSON is a YAML 1.2 subset; the served document deliberately needs no extra parser.
const spec = JSON.parse(await readFile(resolve(root, 'web/public/openapi.yaml'), 'utf8'))
const canonical = await readFile(resolve(root, 'docs/api/seedance-v1.openapi.yaml'), 'utf8')
const examples = docPages.flatMap(page => page.sections.flatMap(s => s.examples || []))
const byID = id => examples.find(e => e.id === id)
const methodNames = ['get', 'post', 'put', 'patch', 'delete', 'head', 'options']
const pathFor = path => path.split('?')[0].replace(/\$TASK_ID/g, '{task_id}').replace(/\$GROUP_ID/g, '{group_id}').replace(/\$ASSET_ID/g, '{asset_id}').replace(/\$SESSION_ID/g, '{session_id}').replace(/\$OPERATION_ID/g, '{operation_id}')
function dereference(ref) {
  assert.ok(ref.startsWith('#/'), `unexpected external reference ${ref}`)
  let current = spec
  for (const part of ref.slice(2).split('/').map(s => s.replaceAll('~1', '/').replaceAll('~0', '~'))) {
    assert.ok(current && Object.hasOwn(current, part), `unresolved ${ref}`)
    current = current[part]
  }
  return current
}
const expanded = value => value?.$ref ? dereference(value.$ref) : value
// Check the JSON Schema keywords used by our request/response examples. This is
// intentionally a documentation regression check, not a general OAS validator.
function validate(schema, value, at = '$') {
  if (schema === false) assert.fail(`${at}: forbidden schema`)
  if (!schema || schema === true) return
  if (schema.$ref) validate(dereference(schema.$ref), value, at)
  const passes = candidate => { try { validate(candidate, value, at); return true } catch { return false } }
  if (schema.allOf) schema.allOf.forEach(s => validate(s, value, at))
  if (schema.oneOf) assert.equal(schema.oneOf.filter(passes).length, 1, `${at}: oneOf`)
  if (schema.anyOf) assert.ok(schema.anyOf.some(passes), `${at}: anyOf`)
  if (schema.not) assert.ok(!passes(schema.not), `${at}: not`)
  if (schema.if) validate(passes(schema.if) ? schema.then : schema.else, value, at)
  if (Object.hasOwn(schema, 'const')) assert.deepEqual(value, schema.const, `${at}: const`)
  if (schema.enum) assert.ok(schema.enum.includes(value), `${at}: enum ${value}`)
  const type = value === null ? 'null' : Array.isArray(value) ? 'array' : typeof value === 'object' ? 'object' : typeof value
  if (schema.type) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type]
    assert.ok(types.includes(type) || type === 'number' && Number.isInteger(value) && types.includes('integer'), `${at}: type ${type}`)
  }
  if (type === 'object') {
    for (const key of schema.required || []) assert.ok(Object.hasOwn(value, key), `${at}: missing ${key}`)
    for (const [key, item] of Object.entries(value)) {
      if (schema.properties?.[key]) validate(schema.properties[key], item, `${at}.${key}`)
      else if (schema.additionalProperties === false) assert.fail(`${at}: unknown ${key}`)
    }
  }
  if (type === 'array') {
    if (schema.minItems !== undefined) assert.ok(value.length >= schema.minItems, `${at}: minItems`)
    if (schema.maxItems !== undefined) assert.ok(value.length <= schema.maxItems, `${at}: maxItems`)
    if (schema.contains) {
      const matches = value.filter(v => { try { validate(schema.contains, v, at); return true } catch { return false } }).length
      assert.ok(matches >= (schema.minContains ?? 1), `${at}: contains`)
      if (schema.maxContains !== undefined) assert.ok(matches <= schema.maxContains, `${at}: maxContains`)
    }
    value.forEach((v, i) => validate(schema.items, v, `${at}[${i}]`))
  }
  if (type === 'string') {
    if (schema.minLength !== undefined) assert.ok([...value].length >= schema.minLength, `${at}: minLength`)
    if (schema.maxLength !== undefined) assert.ok([...value].length <= schema.maxLength, `${at}: maxLength`)
    if (schema.pattern) assert.match(value, new RegExp(schema.pattern), `${at}: pattern`)
    if (schema.format === 'date-time') assert.ok(!Number.isNaN(Date.parse(value)), `${at}: date-time`)
    if (schema.format === 'uri') assert.doesNotThrow(() => new URL(value), `${at}: URI`)
  }
  if (type === 'number') {
    if (schema.minimum !== undefined) assert.ok(value >= schema.minimum, `${at}: minimum`)
    if (schema.maximum !== undefined) assert.ok(value <= schema.maximum, `${at}: maximum`)
  }
}

test('navigation has exactly three groups and unique reachable deep links', () => {
  assert.deepEqual(docGroups.map(g => g.id), ['text', 'image', 'video'])
  const ids = new Set()
  const sectionIDs = new Set()
  for (const page of docPages) {
    assert.ok(!ids.has(page.id), `duplicate page ${page.id}`); ids.add(page.id)
    assert.match(page.id, /^[a-z][a-z0-9-]*$/)
    assert.ok(docGroups.some(g => g.id === page.group))
    assert.ok(page.sections.length && page.summary)
    for (const section of page.sections) {
      assert.ok(!sectionIDs.has(section.id), `duplicate section ${section.id}`); sectionIDs.add(section.id)
      for (const row of section.table?.rows || []) assert.equal(row.length, section.table.headers.length)
    }
  }
  for (const page of docPages) for (const section of page.sections) for (const link of section.links || []) {
    const url = new URL(link.to, 'https://example.invalid')
    if (url.pathname === '/docs') assert.ok(ids.has(url.searchParams.get('section')), `broken link ${link.to}`)
    else assert.ok(['/models', '/app/usage', '/app/request-logs', '/app/video-assets', '/app/fund-activities'].includes(url.pathname), `unreviewed link ${link.to}`)
  }
})

test('search covers endpoint, parameter and model terms without changing group membership', () => {
  assert.ok(searchPages('seedance-2.5').some(p => p.id === 'video-create'))
  for (const model of ['seedance-2.0-fast', 'seedance-2.0-mini']) {
    assert.ok(searchPages(model).some(p => p.id === 'video-intro'))
    assert.ok(searchPages(model).some(p => p.id === 'video-create'))
  }
  assert.ok(searchPages('/v1/video/assets').some(p => p.id === 'video-assets'))
  assert.ok(searchPages('expected_version').some(p => p.id === 'video-assets'))
  assert.equal(searchPages('no-such-document-9382').length, 0)
  assert.equal(searchPages('  ').length, docPages.length)
})

test('Fast and Mini document only the implemented generation envelope and retain activation prerequisites', () => {
  const models = docPages.find(page => page.id === 'video-intro').sections.find(section => section.id === 'video-models')
  for (const model of ['seedance-2.0-fast', 'seedance-2.0-mini']) {
    const row = models.table.rows.find(row => row[0] === model)
    assert.match(row[1], /4–15 秒；480p \/ 720p；六种固定比例/)
    assert.match(row[2], /独立渠道配置.*最低量.*USD.*预算/)
  }
  const scope = models.paragraphs.join('\n')
  assert.match(scope, /16:9 \/ 4:3 \/ 1:1 \/ 3:4 \/ 9:16 \/ 21:9/)
  assert.match(scope, /不开放 1080p、4k、adaptive、自动时长、frames、Draft/)
  assert.match(scope, /不代表当前组织已开通/)
})

test('public OpenAPI has fully resolvable refs and unique operation IDs', t => {
  assert.equal(spec.openapi, '3.1.0')
  let references = 0
  function walk(value) {
    if (!value || typeof value !== 'object') return
    if (value.$ref) { dereference(value.$ref); references++ }
    Object.values(value).forEach(walk)
  }
  walk(spec)
  const ids = new Set()
  let operations = 0
  for (const item of Object.values(spec.paths)) for (const method of methodNames) if (item[method]) {
    operations++
    const id = item[method].operationId
    if (id) { assert.ok(!ids.has(id), `duplicate operationId ${id}`); ids.add(id) }
  }
  assert.ok(references > 100)
  t.diagnostic(`${Object.keys(spec.paths).length} paths, ${operations} operations, ${references} internal references`)
})

test('served video specification records current canonical contract digest', () => {
  const hash = createHash('sha256').update(canonical.replaceAll('\r\n', '\n')).digest('hex')
  assert.equal(spec['x-seedance-source-sha256'], hash, 'regenerate public OpenAPI after changing canonical video contract')
  assert.equal(spec.components.schemas.ReservationMetering.properties.input_token_semantics.const, 'not_projected_separately_completion_tokens_authoritative')
  assert.ok(!JSON.stringify(spec.paths).includes('failures refund in full'))
  assert.ok(!JSON.stringify(spec.paths).includes('Any extra top-level params'))
  assert.match(spec.components.schemas.Billing.properties.pricing_snapshot.description, /首次实扣=MIN\(实际量计算金额, reserved_amount\)/)
  const formula = docPages.find(p => p.id === 'video-billing').sections.find(s => s.id === 'billing-formula')
  assert.ok(formula.table.rows.some(row => row[0] === '实际量计算金额'))
  assert.ok(formula.table.rows.some(row => row[0] === '首次实扣' && row[1].includes('不自动追扣')))
})

test('inline OpenAPI examples satisfy their accompanying schemas', t => {
  let checked = 0
  function walk(value, path = '$') {
    if (!value || typeof value !== 'object') return
    if (value.schema && Object.hasOwn(value, 'example')) {
      validate(value.schema, value.example, path); checked++
    }
    if (value.schema && value.examples) for (const example of Object.values(value.examples)) {
      if (Object.hasOwn(example, 'value')) { validate(value.schema, example.value, path); checked++ }
    }
    for (const [key, child] of Object.entries(value)) if (!['example', 'examples'].includes(key)) walk(child, `${path}.${key}`)
  }
  walk(spec)
  assert.ok(checked >= 4)
  t.diagnostic(`${checked} inline examples checked`)
})

test('document requests map to real published operations and examples match their schemas', t => {
  const ids = new Set()
  let requests = 0, responses = 0
  for (const example of examples) {
    assert.ok(!ids.has(example.id), `duplicate example ${example.id}`); ids.add(example.id)
    if (!example.path) continue
    const operation = spec.paths[pathFor(example.path)]?.[example.method.toLowerCase()]
    assert.ok(operation, `missing operation ${example.method} ${example.path}`)
    assert.ok(operation.responses[example.status] || operation.responses.default, `missing status ${example.id}`)
    if (example.request !== undefined) {
      const body = expanded(operation.requestBody)
      assert.ok(body?.content?.['application/json']?.schema, `missing request schema ${example.id}`)
      validate(body.content['application/json'].schema, example.request, example.id)
      const curl = curlFor(example)
      assert.deepEqual(JSON.parse(curl.split("<<'JSON'\n")[1].split('\nJSON')[0]), example.request)
      requests++
    }
    const response = expanded(operation.responses[example.status])
    if (example.response !== undefined && !example.responseExcerpt && response?.content?.['application/json']?.schema) {
      validate(response.content['application/json'].schema, example.response, example.id)
      responses++
    }
  }
  t.diagnostic(`${examples.length} unique examples, ${requests} JSON requests and ${responses} complete JSON responses schema-checked`)
})

test('video creation and cancellation preserve required idempotency and empty cancellation body', () => {
  for (const example of examples.filter(e => e.method === 'POST' && e.path?.startsWith('/v1/video/') && e.path !== '/v1/video/estimates')) {
    assert.match(example.idempotency || '', /^[!-~]{8,128}$/)
    assert.ok(curlFor(example).includes('Idempotency-Key:'))
  }
  const cancel = byID('task-cancel-call')
  assert.equal(cancel.request, undefined)
  assert.ok(!/--data|-d |Content-Type/.test(curlFor(cancel)))
  assert.equal(byID('video-submit').request.model, 'seedance-2.0')
  assert.deepEqual(byID('video-submit').response, { id: 'task_example001' })
  const receipt = byID('task-cancel-call').response
  assert.match(receipt.id, /^task_/)
  assert.equal(receipt.status, 'cancelling')
  assert.match(receipt.operation_id, /^op_/)
  assert.equal(byID('task-delete-call').response.status, 'deleting')
  const queried = byID('task-get').response
  assert.ok(!Object.hasOwn(queried, 'result_url'))
  assert.ok(!Object.hasOwn(queried, 'cost_usd'))
  for (const field of ['billing_state', 'sync_state', 'artifact_status', 'deletion_state', 'content_url']) {
    assert.ok(!Object.hasOwn(queried, field), `single task GET must stay official-shaped: ${field}`)
  }
})

test('video result examples return upstream URLs and do not send platform credentials to the media download', () => {
  const queried = byID('task-get').response
  assert.equal(queried.status, 'succeeded')
  assert.ok(Number.isInteger(queried.created_at))
  assert.match(queried.content.video_url, /^https:\/\//)
  assert.equal(byID('content-video').response.url, queried.content.video_url)
  for (const [id, kind] of [['content-video', 'video'], ['content-last-frame', 'last_frame']]) {
    const example = byID(id)
    assert.deepEqual(Object.keys(example.response), ['url', 'kind', 'source'])
    assert.equal(example.response.kind, kind)
    assert.equal(example.response.source, 'upstream')
    assert.match(example.response.url, /^https:\/\//)
    assert.match(curlFor(example), /Authorization: Bearer \$LAIAI_KEY/)
    assert.equal(example.responseText, undefined)
  }
  const download = curlFor(byID('direct-video-download'))
  assert.match(download, /TASK_JSON=\$\(curl[^\n]+\$BASE_URL\/v1\/video\/generations\/\$TASK_ID/)
  assert.match(download, /jq -er '\.content\.video_url/)
  assert.doesNotMatch(download, /content_url/)
  const mediaRequest = download.slice(download.lastIndexOf('curl '))
  assert.match(mediaRequest, /--proto '=https' "\$VIDEO_URL" --output generated-video\.mp4/)
  assert.doesNotMatch(mediaRequest, /Authorization|LAIAI_KEY|--location-trusted/)
  const description = docPages.find(p => p.id === 'video-query').sections.find(s => s.id === 'query-content').paragraphs.join('\n')
  assert.match(description, /平台不下载、转存或代理/)
  assert.match(description, /可能过期/)
})

test('asset examples obtain versions, use platform IDs and submit guarded mutations', () => {
  for (const [read, edit] of [['asset-get', 'asset-rename'], ['group-get', 'group-rename']]) {
    assert.equal(byID(edit).request.expected_version, byID(read).response.Result.Version)
    assert.ok(byID(edit).idempotency)
  }
  assert.match(byID('asset-get').response.Result.URL, /^asset:\/\/asset_/)
  assert.match(byID('portrait-get').response.Result.SessionId, /^pas_/)
  assert.ok(byID('asset-delete-call').idempotency)
  assert.equal(byID('asset-delete-call').response.ResponseMetadata.Action, 'DeleteAsset')
  assert.equal(byID('asset-delete-call').response.Result.Status, 'deleting')
  for (const id of ['asset-create-call', 'group-create-call', 'asset-list', 'group-list']) {
    const metadata = byID(id).response.ResponseMetadata
    assert.equal(metadata.Version, '2024-01-01')
    assert.equal(metadata.Service, 'ark')
    assert.equal(metadata.Region, 'cn-beijing')
    assert.match(metadata.RequestId, /./)
  }
  const accepted = spec.paths['/v1/video/assets/{asset_id}'].delete.responses['202']
  assert.ok(!accepted.$ref, 'asset DELETE 202 must document the inline official envelope')
  assert.equal(accepted.content['application/json'].schema.allOf[0].$ref, '#/components/schemas/RpcEnvelope')
  for (const name of ['RpcGetAssetResult', 'RpcGetAssetGroupResult']) assert.ok(spec.components.schemas[name].required.includes('Version'), `${name} must expose Version for PATCH optimistic locking`)
})

test('asset guide documents upstream URL registration and creator isolation without promising local media scans', () => {
  const guide = docPages.find(page => page.id === 'video-assets-guide')
  const api = docPages.find(page => page.id === 'video-assets')
  const text = JSON.stringify([guide, api])
  assert.match(text, /火山直接下载、保存并处理素材/)
  assert.match(text, /平台不下载、不存储、不扫描媒体/)
  assert.match(text, /同一用户的新 Key 可复用本人素材/)
  assert.match(text, /未关联用户的组织 Key 单独隔离/)
  assert.match(text, /视频和音频缺少可信时长/)
  assert.match(text, /unknown.*不得更换幂等键重复注册/)
  assert.doesNotMatch(text, /URL 必须能被本组织可信媒体验证|并非任意公网 URL 都能直接/)
  const body = byID('asset-create-call').request
  assert.ok(body.url.startsWith('https://'))
  assert.equal(body.media_type, 'image')
  assert.deepEqual(Object.keys(body).sort(), ['group_id', 'media_type', 'name', 'url'])
})

test('negative requests are rejected by documented video and asset schemas', () => {
  const create = expanded(spec.paths['/v1/video/generations'].post.requestBody).content['application/json'].schema
  const good = byID('video-submit').request
  assert.throws(() => validate(create, { ...good, provider_params: { arbitrary: true } }))
  assert.throws(() => validate(create, { ...good, frames: 120 }))
  assert.throws(() => validate(create, { ...good, model: 'doubao-seedance-2-0' }))
  assert.throws(() => validate(create, { ...byID('draft-conversion').request, draft: true }))
  const patch = spec.paths['/v1/video/assets/{asset_id}'].patch.requestBody.content['application/json'].schema
  assert.throws(() => validate(patch, { name: '素材', expected_version: 0 }))
  assert.throws(() => validate(patch, { name: '素材', expected_version: 1, group_id: 'ag_other' }))
})
