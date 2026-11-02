import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'

const result = await build({ entryPoints: [fileURLToPath(new URL('./billingPresentation.ts', import.meta.url))], bundle: true, format: 'esm', write: false })
const billing = await import(`data:text/javascript;base64,${Buffer.from(result.outputFiles[0].text).toString('base64')}`)

const overrun = {
  state: 'settled', settled_amount: '0.40452632', net_charged_amount: '0.40452632', billable_tokens: 38830,
  unit_price_usd_per_1m: '10.526315789500', actual_usage: { completion_tokens: 38830, total_tokens: 38830 },
  entries: [{ type: 'reserve', amount: '0.40452632' }, { type: 'capture', amount: '0.40452632' }],
}

test('historical API response reconstructs the over-reserve calculation without floating point loss', () => {
  assert.deepEqual(billing.initialChargeExplanation(overrun), {
    calculatedUsd: '0.40873684', absorbedUsd: '0.00421052', source: 'snapshot_recalculation',
  })
  assert.equal(billing.hasAbsorbedOverage(billing.initialChargeExplanation(overrun)), true)
})

test('persisted settlement amounts remain authoritative after a reviewed adjustment', () => {
  const adjusted = { ...overrun, billable_tokens: 40000, entries: [...overrun.entries, { type: 'adjustment_debit', amount: '0.01' }] }
  assert.equal(billing.initialChargeExplanation(adjusted), null)
  assert.deepEqual(billing.initialChargeExplanation({ ...adjusted, initial_calculated_amount: '0.40873684', initial_absorbed_amount: '0.00421052' }), {
    calculatedUsd: '0.40873684', absorbedUsd: '0.00421052', source: 'settlement',
  })
})

test('unknown usage and incompatible snapshots do not invent an actual charge', () => {
  assert.equal(billing.initialChargeExplanation({ ...overrun, settled_amount: null }), null)
  assert.equal(billing.initialChargeExplanation({ ...overrun, actual_usage: null }), null)
  assert.equal(billing.initialChargeExplanation({ ...overrun, billable_tokens: 38831 }), null)
  assert.equal(billing.initialChargeExplanation({ ...overrun, net_charged_amount: '0.41452632' }), null)
  assert.equal(billing.initialChargeExplanation({ ...overrun, unit_price_usd_per_1m: 'not-a-price' }), null)
})
