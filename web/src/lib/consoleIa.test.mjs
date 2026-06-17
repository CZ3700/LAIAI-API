import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const root = new URL('../', import.meta.url)
const read = (path) => readFileSync(new URL(path, root), 'utf8')

test('console information architecture exposes option B workflow routes', () => {
  const router = read('router.ts')
  const shell = read('layouts/Shell.vue')

  for (const path of ['onboarding', 'billing', 'latency']) {
    assert.match(router, new RegExp(`path:\\s*['"]${path}['"]`), `${path} route should exist`)
  }

  for (const navKey of ['nav.onboarding', 'nav.billing', 'nav.latency']) {
    assert.match(shell, new RegExp(`t\\(['"]${navKey}['"]\\)`), `${navKey} should be in shell navigation`)
  }
})

test('core locales include option B navigation labels', () => {
  const en = read('i18n/en.ts')
  const zh = read('i18n/zh-CN.ts')

  for (const key of ['onboarding', 'billing', 'latency']) {
    assert.match(en, new RegExp(`${key}:\\s*['"]`), `en missing ${key}`)
    assert.match(zh, new RegExp(`${key}:\\s*['"]`), `zh-CN missing ${key}`)
  }
})

test('reliability workspace keeps local metrics honest', () => {
  const latency = read('pages/Latency.vue')

  assert.match(latency, /status\s*===\s*['"]success['"]/, 'success metrics should use the backend success status')
  assert.doesNotMatch(latency, /status_code\s*<\s*400/, 'status_code 0 must not be counted as a success')
  assert.match(latency, /disabled/, 'CSV export should be visibly disabled until a backend export exists')
  assert.match(latency, /v-if="visibleRows\.length === 0"/, 'recent timing table should render an empty state')
})

test('billing workspace surfaces partial data load failures', () => {
  const billing = read('pages/Billing.vue')
  const en = read('i18n/en.ts')
  const zh = read('i18n/zh-CN.ts')

  assert.match(billing, /status === ['"]rejected['"]/, 'billing load should inspect rejected API calls')
  assert.match(billing, /billing\.partialError/, 'billing page should show a partial-load message')
  assert.match(en, /partialError:\s*['"]/, 'en missing billing partial-error copy')
  assert.match(zh, /partialError:\s*['"]/, 'zh-CN missing billing partial-error copy')
})
