import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'
import * as vue from 'vue'

async function component(name) {
 const filename=fileURLToPath(new URL(`./${name}.vue`,import.meta.url))
 const {descriptor}=parse(await readFile(filename,'utf8'),{filename})
 const compiled=compileScript(descriptor,{id:`currency-${name}`})
 const built=await build({stdin:{contents:compiled.content,loader:'ts',sourcefile:filename},bundle:true,format:'esm',write:false,
 plugins:[{name:'boundaries',setup(b){
  b.onResolve({filter:/.*/},a=>({path:a.path,namespace:'boundary'}))
  b.onLoad({filter:/.*/,namespace:'boundary'},a=>{
   if(a.path==='vue')return{contents:`export const defineComponent=v=>v; export const ref=(...a)=>globalThis.__bill.vue.ref(...a);export const computed=(...a)=>globalThis.__bill.vue.computed(...a);export const watch=(...a)=>globalThis.__bill.vue.watch(...a);export const onMounted=f=>globalThis.__bill.mounted.push(f);export const onBeforeUnmount=f=>globalThis.__bill.unmounted.push(f);`}
   if(a.path==='vue-i18n')return{contents:'export const useI18n=()=>({t:k=>k,n:v=>v,d:v=>v});'}
   if(a.path==='@/stores/auth')return{contents:'export const useAuth=()=>globalThis.__bill.auth;'}
   if(a.path==='@/lib/api')return{contents:'export const api=(...a)=>globalThis.__bill.api(...a);export const errorMessage=(e,f)=>f;export const uuid=()=>"fixture-export";'}
   if(a.path==='@/lib/money')return{contents:'export const fmtUsd=v=>v;export const addMoney=(...a)=>String(a.reduce((x,y)=>x+Number(y),0));export const negateMoney=v=>String(-Number(v));export const displayCurrencyCode=()=>globalThis.__bill.currency;export const formatNativeMoney=v=>v;'}
   if(a.path.endsWith('.vue'))return{contents:'export default {};'}
   throw Error(a.path)
  })
 }}]})
 return (await import(`data:text/javascript;base64,${Buffer.from(built.outputFiles[0].text).toString('base64')}`)).default
}
const components=Object.fromEntries(await Promise.all(['Billing','Dashboard','Usage','FundActivities'].map(async n=>[n,await component(n)])))
function fixture(t,name,response) {
 const h={vue,auth:vue.reactive({orgId:3}),currency:'CNY',mounted:[],unmounted:[],api:async(...a)=>response(...a)}
 globalThis.__bill=h
 const scope=vue.effectScope();h.vm=scope.run(()=>components[name].setup({},{expose(){}}))
 t.after(()=>{h.unmounted.forEach(f=>f());scope.stop();delete globalThis.__bill})
 return h
}
function empty(method,path){
 if(path==='/api/balance')return{balance:'12.34',held:'0'}
 if(path==='/api/payment/recharge-stats')return{month_usd:'0',last7_usd:'0'}
 return{items:null,keys:null,orders:null,total:0}
}
for(const name of ['Billing','Dashboard','Usage'])test(`${name}: no usage handles legacy null collections`,async t=>{
 const h=fixture(t,name,empty)
 await Promise.all(h.mounted.map(f=>f()))
 assert.equal(h.vm.loading.value,false)
 assert.deepEqual(h.vm.summary.value,[])
 if(name==='Usage'){assert.equal(h.vm.totals.value.cost,'0');assert.deepEqual(h.vm.filteredModels.value,[])}
 else{assert.equal(h.vm.balance.value,'12.34');assert.equal(h.vm.balanceLoading.value,false);assert.equal(h.vm.balanceError.value,'')}
})
for(const name of ['Billing','Dashboard']){
 test(`${name}: successful balance is visible before slow usage`,async t=>{
  let resolve
  const slow=new Promise(r=>{resolve=r})
  const h=fixture(t,name,(m,p)=>p.startsWith('/api/usage/summary')?slow:empty(m,p))
  const work=Promise.all(h.mounted.map(f=>f()))
  await new Promise(r=>setImmediate(r))
  assert.equal(h.vm.balance.value,'12.34');assert.equal(h.vm.balanceLoading.value,false)
  resolve({items:null});await work
 })
 test(`${name}: balance error is explicit and retry recovers`,async t=>{
  let fail=true
  const h=fixture(t,name,(m,p)=>{if(p==='/api/balance'&&fail)throw Error('offline');return empty(m,p)})
  await Promise.all(h.mounted.map(f=>f()))
  assert.equal(h.vm.balanceLoading.value,false);assert.notEqual(h.vm.balanceError.value,'')
  fail=false;await h.vm.load()
  assert.equal(h.vm.balanceError.value,'');assert.equal(h.vm.balance.value,'12.34')
 })
}
test('fund export captures selected currency, without browser supplied rate',async t=>{
 let body
 const h=fixture(t,'FundActivities',(m,p,b)=>{if(m==='POST'){body=b;return{id:1,status:'pending',row_count:0,expires_at:'fixture',display_money:{display_currency:b.display_currency,cny_per_usd:'6.65'}}}return{items:[],next_before:0}})
 await h.vm.download()
 assert.equal(body.display_currency,'CNY');assert.equal(body.cny_per_usd,undefined)
 h.currency='USD';await h.vm.download();assert.equal(body.display_currency,'USD')
})
