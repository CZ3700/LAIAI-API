import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'

const src = fileURLToPath(new URL('../', import.meta.url))
const compiled = await build({ stdin: { contents: `export * from './lib/invocationQueryTransport'; export * from './lib/invocationQuery'; export {api,adminApi,apiFetch,adminToken,ApiError} from './lib/api'`, resolveDir: src, loader: 'ts' }, bundle: true, write: false, format: 'esm', platform: 'node', alias: { '@': src },
  plugins: [{ name: 'auth-boundary', setup(b) {
    b.onResolve({ filter: /(?:@\/stores\/auth|\/stores\/auth\.ts)$/ }, () => ({ path: 'auth', namespace: 'fixture' }))
    b.onLoad({ filter: /.*/, namespace: 'fixture' }, () => ({ contents: 'export const useAuth=()=>globalThis.__invocationAuth' }))
  } }] })
const m = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString('base64')}`)
const now = new Date('2026-10-26T00:00:00Z')
const spec = (scope = 'org', patch = {}) => m.buildInvocationFilter({ ...m.defaultInvocationDraft(), ...patch }, scope, now)
const copy = value => structuredClone(value)
const csvCommon = 'time,request_id,model,status,modality,api_key_id,prompt_tokens,completion_tokens,cached_tokens,reasoning_tokens,cost_usd,cost_display,currency,cny_per_usd,rate_snapshot_at,rate_source,as_of,filter_hash,amount_source,completeness,usage_presence'
const csvBody = (admin=false, count=237) => csvCommon+(admin?',org_id,user_id,channel_id,provider':'')+'\n'+Array.from({length:count},()=>Array(admin?25:21).fill('').join(',')+'\n').join('')
const deferred = () => { let resolve, reject; const promise = new Promise((a,b) => { resolve=a;reject=b }); return {promise,resolve,reject} }
function fixture(t) {
  const values = new Map(), oldFetch = globalThis.fetch, oldStorage = globalThis.sessionStorage
  globalThis.sessionStorage = { getItem: k => values.get(k) ?? null, setItem: (k,v) => values.set(k,v), removeItem: k => values.delete(k) }
  const auth = { session: { token: 'session-A', orgId: 7 }, get token() { return this.session?.token ?? '' }, logouts: 0, logout() { this.logouts++; this.session=null } }
  globalThis.__invocationAuth = auth
  t.after(() => { globalThis.fetch = oldFetch; globalThis.sessionStorage = oldStorage; delete globalThis.__invocationAuth })
  return { auth, values }
}
function bundle(filter = spec(), currency = 'CNY') {
  const normalized = { source: 'invocation_log', time_basis: 'recorded_at', timezone: filter.timezone, since: `${filter.since}T00:00:00Z`, until: `${filter.until}T00:00:00Z` }
  for (const key of ['model','request_id','modality','status','result','is_stream']) if (filter[key] !== undefined) normalized[key]=filter[key]
  for (const key of ['api_key_id','org_id','channel_id']) if (filter[key] !== undefined) normalized[key]=Number(filter[key])
  return { items: [{ id: 31, created_at: '2026-10-25T12:00:00Z', request_id: 'r31', api_key_id: 11, model: 'historical', modality: '', status: 'success', status_code: 200, is_stream: false,
    cost_usd: '0.00000001', usage_exact: { prompt_tokens: '9007199254740993', completion_tokens: '9223372036854775807', cached_tokens: '0', reasoning_tokens: '0' }, frt_ms: 1, duration_ms: 2,
    org_id: 7, user_id: 8, channel_id: 9, provider: 'private-provider', attempts: 1, payload_object_key: 'private-object', client_ip: 'private-ip' }],
    total: 237, page: 1, page_size: 20,
    totals: { record_count: 237, success_count: 180, non_success_count: 57, prompt_tokens: '18446744073709551614', completion_tokens: '9007199254740993', cached_tokens: '0', reasoning_tokens: '0', cost_usd: '1.00000001', cost_display: '6.6500000665', usage_presence: 'legacy_not_distinguishable', missing_usage_count: null },
    query: { filter_version: 1, normalized_filters: normalized, filter_hash: `sha256:${'a'.repeat(64)}`, as_of: now.toISOString(), data_watermark: { kind: 'observation', recorded_through_id: null }, consistency: 'response_snapshot', cross_request_consistency: 'live', data_source: 'invocation_log', amount_source: 'log_recorded_cost', completeness: 'best_effort', display_money: { currency, cny_per_usd: '6.65', rate_snapshot_at: now.toISOString(), rate_source: 'setting' } } }
}
function csvHeaders(patch = {}) {
  return new Headers({ 'Content-Type':'text/csv; charset=utf-8', 'Content-Disposition':'attachment; filename="invocation-log-CNY-20261026T000000Z.csv"',
    'X-Invocation-Filter-Version':'1', 'X-Invocation-Record-Count':'237', 'X-Invocation-Success-Count':'180', 'X-Invocation-Non-Success-Count':'57',
    'X-Invocation-Prompt-Tokens':'18446744073709551614', 'X-Invocation-Completion-Tokens':'9007199254740993', 'X-Invocation-Cached-Tokens':'0', 'X-Invocation-Reasoning-Tokens':'0',
    'X-Invocation-Cost-USD':'1.00000001', 'X-Invocation-Cost-Display':'6.6500000665', 'X-Invocation-As-Of':now.toISOString(), 'X-Invocation-Filter-Hash':`sha256:${'a'.repeat(64)}`,
    'X-Invocation-Amount-Source':'log_recorded_cost', 'X-Invocation-Completeness':'best_effort', 'X-Invocation-Usage-Presence':'legacy_not_distinguishable',
    'X-Invocation-Consistency':'response_snapshot', 'X-Invocation-Cross-Request-Consistency':'live', 'X-Display-Currency':'CNY', 'X-CNY-Per-USD':'6.65', 'X-Rate-Snapshot-At':now.toISOString(), 'X-Rate-Source':'setting', ...patch })
}

test('strict v1 bundle keeps exact money and usage; ordinary scope removes internal fields', () => {
  const filter=spec(), input=bundle(filter), result=m.decodeInvocationBundle(input,filter,'CNY',1,20)
  assert.equal(result.items[0].usage_exact.prompt_tokens,'9007199254740993')
  assert.equal(result.totals.prompt_tokens,'18446744073709551614')
  assert.equal(result.totals.cost_display,'6.6500000665')
  assert.equal(result.query.display_money.cny_per_usd,'6.65')
  for(const key of ['org_id','user_id','channel_id','provider','attempts','payload_object_key','client_ip','prompt_tokens'])assert.equal(key in result.items[0],false,key)
  const adminFilter=spec('admin'), admin=m.decodeInvocationBundle(bundle(adminFilter),adminFilter,'CNY',1,20)
  assert.equal(admin.items[0].org_id,7);assert.equal(admin.items[0].channel_id,9)
  assert.equal('payload_object_key' in admin.items[0],false)
  for(const change of [v=>delete v.items[0].usage_exact, v=>v.items[0].usage_exact.prompt_tokens=9007199254740992,
    v=>v.items[0].id=9007199254740992, v=>v.items[0].api_key_id=9007199254740992,
    v=>v.totals.cost_usd=1, v=>v.totals.missing_usage_count=0, v=>v.query.filter_version=2,
    v=>v.query.display_money.cny_per_usd='0', v=>v.query.display_money.cny_per_usd=6.65,
    v=>v.query.amount_source='ledger', v=>v.totals.record_count=236, v=>v.query.normalized_filters.org_id=7]){
    const bad=copy(input);change(bad);assert.throws(()=>m.decodeInvocationBundle(bad,filter,'CNY',1,20),m.InvocationContractError)
  }
})

test('exact normalized filters reject mismatches without accepting a weaker response', () => {
  const filter=spec('org',{modality:'historical',stream:'false',model:'missing-from-dictionary',apiKeyId:'11',result:'failed'}), input=bundle(filter)
  assert.equal(m.decodeInvocationBundle(input,filter,'CNY',1,20).query.normalized_filters.modality,'')
  for(const change of [v=>delete v.query.normalized_filters.modality, v=>v.query.normalized_filters.is_stream=true,
    v=>v.query.normalized_filters.model='other', v=>v.query.normalized_filters.api_key_id=12, v=>v.page=2,
    v=>v.query.display_money.currency='USD', v=>v.query.normalized_filters.since='2026-10-01T00:00:00Z']){
    const bad=copy(input);change(bad);assert.throws(()=>m.decodeInvocationBundle(bad,filter,'CNY',1,20),m.InvocationContractError)
  }
})

test('normalized UTC boundaries must represent the requested calendar midnights across DST', () => {
  const filter=spec('org',{range:'custom',timezone:'America/New_York',start:'2026-11-01',end:'2026-11-01'}), input=bundle(filter)
  input.query.normalized_filters.since='2026-11-01T04:00:00Z'
  input.query.normalized_filters.until='2026-11-02T05:00:00Z'
  assert.equal(m.decodeInvocationBundle(input,filter,'CNY',1,20).query.normalized_filters.until,'2026-11-02T05:00:00Z')
  input.query.normalized_filters.until='2026-11-02T04:00:00Z'
  assert.throws(()=>m.decodeInvocationBundle(input,filter,'CNY',1,20),m.InvocationContractError)
  input.query.normalized_filters.until='2026-11-02T05:00:00.000000001Z'
  assert.throws(()=>m.decodeInvocationBundle(input,filter,'CNY',1,20),m.InvocationContractError)
})

test('API old 401 cannot log out a newer session, while the same current credential still logs out', async t => {
  const {auth}=fixture(t)
  for(const sameToken of [false,true]){
    auth.session={token:'session-A',orgId:7};const held=deferred();globalThis.fetch=()=>held.promise
    const pending=m.api('GET','/api/me');auth.session={token:sameToken?'session-A':'session-B',orgId:7}
    held.resolve(new Response('{"error":"unauthorized"}',{status:401,headers:{'Content-Type':'application/json'}}))
    await assert.rejects(pending,e=>e.status===401);assert.equal(auth.logouts,0)
  }
  globalThis.fetch=async()=>new Response('{"error":"unauthorized"}',{status:401})
  await assert.rejects(m.api('GET','/api/me'),e=>e.status===401);assert.equal(auth.logouts,1)
  auth.session={token:'session-C',orgId:7}
  await assert.rejects(m.api('GET','/api/auth/login',undefined,{auth:false}),e=>e.status===401);assert.equal(auth.logouts,1)
  await assert.rejects(m.adminApi('GET','/api/admin/invocations'),e=>e.status===401);assert.equal(auth.logouts,1)
})

test('raw list/export share auth, abort and explicit v1 conditions; root token revisions are reactive', async t => {
  const {auth}=fixture(t), start=m.adminToken.revision.value
  m.adminToken.set('synthetic-root');assert.equal(m.adminToken.revision.value,start+1)
  const filter=spec('admin',{modality:'historical',stream:'false',orgId:'7'}), calls=[]
  globalThis.fetch=async(url,options)=>{calls.push({url,options});return url.includes('/export?')?new Response(csvBody(true), {headers:csvHeaders()}):new Response(JSON.stringify(bundle(filter)),{headers:{'Content-Type':'application/json'}})}
  const signal=new AbortController().signal
  await m.fetchInvocationLogs(filter,'CNY',1,20,signal)
  const csv=await m.fetchInvocationExport(filter,'CNY',signal)
  assert.equal(csv.metadata.recordCount,237);assert.equal(csv.metadata.money.cny_per_usd,'6.65')
  const list=new URL(calls[0].url,'https://local.test'), exp=new URL(calls[1].url,'https://local.test')
  list.searchParams.delete('page');list.searchParams.delete('page_size');assert.equal(list.search,exp.search)
  assert.equal(exp.searchParams.get('modality'),'');assert.equal(exp.searchParams.get('is_stream'),'false')
  for(const call of calls){assert.equal(call.options.headers.Authorization,`Bearer ${auth.token}`);assert.equal(call.options.headers['X-Admin-Token'],'synthetic-root');assert.equal(call.options.cache,'no-store');assert.equal(call.options.signal,signal)}
  m.adminToken.clear();assert.equal(m.adminToken.revision.value,start+2)
})

test('CSV validates immutable metadata and filename before any content is downloaded', () => {
  const decoded=m.decodeInvocationExportHeaders(csvHeaders(),'CNY')
  assert.equal(decoded.metadata.costDisplay,'6.6500000665')
  for(const patch of [{'Content-Type':'text/html'}, {'Content-Disposition':'attachment; filename="../../secrets.csv"'}, {'X-Invocation-Record-Count':'5001'},
    {'X-Invocation-Filter-Version':'2'}, {'X-CNY-Per-USD':'NaN'}, {'X-Invocation-Success-Count':'181'}, {'X-Invocation-Usage-Presence':'known'},
    {'X-Invocation-Filter-Hash':'wrong'}, {'X-Display-Currency':'USD'}])assert.throws(()=>m.decodeInvocationExportHeaders(csvHeaders(patch),'CNY'),m.InvocationContractError)
})

test('body reader bounds each chunk, cancels oversize and aborts during a stalled read', async () => {
  let cancelled=0
  const oversized=new Response(new ReadableStream({start(c){c.enqueue(new Uint8Array(7));c.enqueue(new Uint8Array(7))},cancel(){cancelled++}}))
  await assert.rejects(m.readInvocationBody(oversized,new AbortController().signal,10),e=>e.code==='export_size_exceeded')
  assert.equal(cancelled,1)
  const exact=new Response(new Uint8Array(10))
  assert.equal((await m.readInvocationBody(exact,new AbortController().signal,10)).length,10)
  let stalledCancelled=0
  const stalled=new Response(new ReadableStream({pull(){return new Promise(()=>{})},cancel(){stalledCancelled++}})), controller=new AbortController()
  const pending=m.readInvocationBody(stalled,controller.signal);controller.abort()
  await assert.rejects(pending,e=>e.name==='AbortError');assert.equal(stalledCancelled,1)
  await assert.rejects(m.readInvocationBody({headers:new Headers(),body:null},new AbortController().signal),m.InvocationContractError)
  await assert.rejects(m.readInvocationBody(new Response('',{headers:{'Content-Length':'8388609'}}),new AbortController().signal),e=>e.code==='export_size_exceeded')
})

test('CSV rejects empty, malformed UTF8 and wrong body headers; legitimate zero records retain their header', async t => {
  fixture(t)
  const zeroHeaders=csvHeaders({'X-Invocation-Record-Count':'0','X-Invocation-Success-Count':'0','X-Invocation-Non-Success-Count':'0'})
  globalThis.fetch=async()=>new Response(csvBody(false,0),{headers:zeroHeaders})
  const empty=m.fetchInvocationExport(spec(),'CNY',new AbortController().signal)
  assert.equal((await empty).metadata.recordCount,0)
  for(const bytes of ['', 'request_id\nvalue\n', csvBody(false,0), new Uint8Array([0xff]), '\uFEFF'+csvBody(), csvBody().slice(0,-1)]){
    globalThis.fetch=async()=>new Response(bytes,{headers:csvHeaders()})
    await assert.rejects(m.fetchInvocationExport(spec(),'CNY',new AbortController().signal),m.InvocationContractError)
  }
  globalThis.fetch=async()=>new Response(csvBody(),{headers:csvHeaders({'Content-Type':'text/csv; charset=ISO-8859-1'})})
  await assert.rejects(m.fetchInvocationExport(spec(),'CNY',new AbortController().signal),m.InvocationContractError)
})

test('export 422/429/503 expose fixed errors and request IDs, never proxy text or a CSV', async t => {
  fixture(t)
  for(const [status,code] of [[422,'export_limit_exceeded'],[429,null],[503,null]]){
    globalThis.fetch=async()=>new Response(JSON.stringify({error:'SECRET DATABASE DRIVER SQL',code}),{status,headers:{'Content-Type':'application/json','X-Request-Id':'safe-request-1','Retry-After':'1'}})
    await assert.rejects(m.fetchInvocationExport(spec(),'CNY',new AbortController().signal),e=>e instanceof m.ApiError&&e.status===status&&e.code===code&&e.requestId==='safe-request-1'&&e.retryAfter==='1'&&!e.message.includes('SECRET'))
  }
})
