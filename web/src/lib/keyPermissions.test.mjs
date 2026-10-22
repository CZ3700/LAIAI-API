import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { transform } from 'esbuild'

const source = await readFile(new URL('./keyPermissions.ts', import.meta.url), 'utf8')
const built = await transform(source, { loader: 'ts', format: 'esm' })
const { videoPermission, buildKeyCreateBody } = await import(`data:text/javascript;base64,${Buffer.from(built.code).toString('base64')}`)
const form = { name: 'fixture', quota: '', models: '', ips: '', expiry: 0, rpm: 0, route: '', videoPermission: 'none' }

test('new keys default to no video access and only issue the two supported explicit scope combinations', () => {
  assert.deepEqual(buildKeyCreateBody(form), { name: 'fixture', scopes: '' })
  assert.equal(buildKeyCreateBody({ ...form, videoPermission: 'read' }).scopes, 'video:read')
  assert.equal(buildKeyCreateBody({ ...form, videoPermission: 'read_write' }).scopes, 'video:read video:write')
  assert.equal(buildKeyCreateBody({ ...form, videoPermission: 'unexpected' }).scopes, '')
  for (const videoPermission of ['none', 'read', 'read_write']) {
    assert.ok(!buildKeyCreateBody({ ...form, videoPermission }).scopes.includes('*'))
  }
})

test('adding video scopes preserves quota, model, IP, expiry, RPM and routing constraints without mutating the form', () => {
  const configured = { ...form, quota: '0.01000000', models: 'mini, fast, ', ips: ' 192.0.2.1, 198.51.100.0/24 ', expiry: 1, rpm: 5, route: 'latency', videoPermission: 'read_write' }
  const original = structuredClone(configured)
  assert.deepEqual(buildKeyCreateBody(configured), {
    name: 'fixture', scopes: 'video:read video:write', quota_limit: '0.01000000',
    model_limits: ['mini', 'fast'], allow_ips: '192.0.2.1, 198.51.100.0/24', expires_in_days: 1, rpm_limit: 5, route_policy: 'latency',
  })
  assert.deepEqual(configured, original)
})

test('display uses exact gateway scope semantics, including legacy write-only and wildcard keys', () => {
  for (const value of [undefined, null, '', ' ', 'chat:read', 'VIDEO:READ', 'video:read\nvideo:write', 'video:read\tvideo:write']) {
    assert.equal(videoPermission(value), 'none', `unexpected access for ${value}`)
  }
  assert.equal(videoPermission('video:read'), 'read')
  assert.equal(videoPermission('video:write'), 'write')
  for (const value of ['video:read video:write', 'video:write,video:read', ' video:read, ,video:write ', '*', 'video:*']) {
    assert.equal(videoPermission(value), 'read_write')
  }
  assert.equal(videoPermission('video:read unknown:write'), 'read')
})
