import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'
import { createPinia, setActivePinia } from 'pinia'
import { computed } from 'vue'

const root = fileURLToPath(new URL('../../', import.meta.url))
const result = await build({ stdin: { contents: `export * from './src/lib/money'; export * from './src/lib/decimalMoney'; export * from './src/stores/currency';`, resolveDir: root, loader: 'ts' }, bundle: true, write: false, format: 'esm', platform: 'node', tsconfig: `${root}/tsconfig.json`, plugins: [{ name: 'shared-runtime', setup(b) { b.onResolve({ filter: /^(pinia|vue)$/ }, args => ({ path: import.meta.resolve(args.path), external: true })) } }] })
const money = await import(`data:text/javascript;base64,${Buffer.from(result.outputFiles[0].text).toString('base64')}`)
const { formatDisplayMoney: format, formatNativeMoney: native, convertUsdToDisplay: convert, addMoney, negateMoney, multiplyMoney, useCurrency, fmtUsd, displayCurrencyCode } = money

test('exact decimal conversion, rounding, negative zero, micro and large amounts', () => {
  assert.equal(format('10'), '$10.00')
  assert.equal(format('10', { currency:'CNY',rate:'7.3' }), '¥73.00')
  assert.equal(format('-10', { currency:'CNY',rate:'7.3' }), '−¥73.00')
  assert.equal(format('1.005', {precision:2}), '$1.01')
  assert.equal(format('-1.005', {precision:2}), '−$1.01')
  assert.equal(format('-0.00000000'), '$0.00')
  assert.equal(format('0.00000001'), '<$0.000001')
  assert.equal(format('9007199254740993.15', { currency:'CNY',rate:'7.3' }), '¥65752554559609250.00')
  assert.equal(format('0.1', {currency:'CNY',rate:'6.65',precision:4}), '¥0.6650')
  assert.equal(addMoney('0.1','0.2'), '0.3')
  assert.equal(addMoney('10',negateMoney('7.31')), '2.69')
  assert.equal(multiplyMoney('1250','0.15',6), '0.0001875')
  assert.equal(format('0.001',{signed:true}), '+$0.0010')
})
test('fail-safe invalid input and native payment/Ark CNY are not converted twice', () => {
  for (const v of ['NaN','Infinity','', 'junk',null,undefined]) assert.equal(format(v), '—')
  for (const rate of ['0','-1','NaN','1e2','7.123456789','1000']) assert.equal(format('10',{currency:'CNY',rate}), '$10.00')
  assert.equal(native('73','CNY'), '¥73.00')
  assert.equal(native('20.12345678','CNY',8), '¥20.12345678')
  assert.equal(native('10','USD'), '$10.00')
  assert.equal(format(native('73','CNY')), '—')
  assert.deepEqual(convert('10','CNY','7.3'),{amount:'73',currency:'CNY'})
  assert.equal(format('10',{ready:false}), '…')
})
function harness(t) {
  setActivePinia(createPinia())
  const oldFetch = globalThis.fetch
  const users = { a:{user_id:1,display_currency:'CNY',currency_display_available:true}, b:{user_id:2,display_currency:'USD',currency_display_available:true} }
  const config={cny_per_usd:'7.3',currency_display_enabled:true}
  let intercept = null
  const calls=[]
  globalThis.fetch = async (url,opts) => {
    const token=opts.headers.Authorization?.replace('Bearer ','')
    calls.push({url,token,body:opts.body})
    if(intercept) { const override=intercept(url,token,opts); if(override) return override }
    if(opts.method==='PUT') Object.assign(users[token],JSON.parse(opts.body))
    return {ok:true,json:async()=>structuredClone(url==='/api/config'?config:users[token])}
  }
  t.after(()=>{globalThis.fetch=oldFetch})
  return { store:useCurrency(),users,config,calls,setIntercept(fn){intercept=fn} }
}
test('server preference drives reactive amounts, reload, organization sessions and rollback',async t=>{
  const h=harness(t), s=h.store
  const output=computed(()=>fmtUsd('10')), unit=computed(()=>displayCurrencyCode())
  assert.equal(output.value,'…')
  await s.load('a'); assert.equal(output.value,'¥73.00');assert.equal(unit.value,'CNY')
  await s.save('USD');assert.equal(output.value,'$10.00')
  await s.save('CNY');assert.equal(output.value,'¥73.00')
  await s.load('a');assert.equal(output.value,'¥73.00')
  h.users['a-org2']=h.users.a;await s.load('a-org2');assert.equal(output.value,'¥73.00')
  h.config.cny_per_usd='6.65';await s.load('a-org2');assert.equal(output.value,'¥66.50')
  h.config.currency_display_enabled=false;await s.load('a-org2');assert.equal(output.value,'$10.00');assert.equal(s.displayCurrency,'CNY')
  assert.deepEqual(JSON.parse(h.calls.find(c=>c.body).body),{display_currency:'USD'})
})
test('late account response cannot leak preference; logout and failed config safely use USD',async t=>{
  const h=harness(t),s=h.store
  let resolveOld
  h.setIntercept((url,token)=>url==='/api/me'&&token==='a'?new Promise(resolve=>{resolveOld=resolve}):null)
  const pending=s.load('a');await s.load('b');assert.equal(fmtUsd('10'),'$10.00')
  resolveOld({ok:true,json:async()=>h.users.a});await pending
  assert.equal(s.userId,2);assert.equal(s.displayCurrency,'USD')
  h.setIntercept(null);await s.load('a');await s.load('');assert.equal(fmtUsd('10'),'$10.00');assert.equal(s.userId,0)
  await s.load('a');h.setIntercept(()=>Promise.reject(new Error('offline')));await s.load('a');assert.equal(fmtUsd('10'),'$10.00');assert.equal(s.failed,true)
})
test('failed save retains preference and late save cannot overwrite another login',async t=>{
  const h=harness(t),s=h.store;await s.load('a')
  h.setIntercept((_url,_token,opts)=>opts.method==='PUT'?Promise.resolve({ok:false,status:500}):null)
  await assert.rejects(s.save('USD'));assert.equal(s.displayCurrency,'CNY');assert.equal(s.saving,false)
  let complete
  h.setIntercept((_url,_token,opts)=>opts.method==='PUT'?new Promise(resolve=>{complete=resolve}):null)
  const saving=s.save('USD');await s.load('b');complete({ok:true,json:async()=>({display_currency:'CNY',currency_display_available:true})});await saving
  assert.equal(s.userId,2);assert.equal(s.displayCurrency,'USD');assert.equal(s.saving,false)
})
