<script setup lang="ts">
import { computed, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { api, errorMessage, uuid } from '@/lib/api'
import OfficialVideoPriceCatalog from '@/components/OfficialVideoPriceCatalog.vue'
import DetailDrawer from '@/components/DetailDrawer.vue'
import { useAuth } from '@/stores/auth'

interface Cell { mode: string; resolution: string; input_video: boolean; service_tier: string; draft: boolean; enabled: boolean; retail_usd_per_1m: string; supplier_cny_per_1m: string; risk_multiplier: string; minimum_rule_version: string; available: boolean; unavailable_reason?: string }
interface Pricing { model: string; config_version: string; pricing_version: string; capability_version: string; enabled: boolean; cells: Cell[]; fx_usd_per_cny: string; fx_version: string; fx_observed_at: string; ratios: string[]; min_duration: number; max_duration: number }
interface Preview { key: string; maximum_hold_usd: string; maximum_estimated_tokens: number; supplier_reserved_cny: string; minimum_rule_version: string }
interface Revision { id: number; version: string; config_version: string; created_by: number; created_at: string; snapshot_json: string }
interface Minimum { id: number; version: string; source_url: string; evidence_hash: string; observed_at: string; verified_by: number; verified_at: string }
const model = ref('seedance-2.0'), pricing = ref<Pricing | null>(null)
const loading = ref(false), busy = ref(false), error = ref(''), notice = ref('')
const configVersion = ref(''), priceVersion = ref(''), evidenceHash = ref('')
const previews = ref<Preview[]>([]), revisions = ref<Revision[]>([]), minimums = ref<Minimum[]>([])
const minimumJSON = ref(''), selectedRevision = ref<Revision | null>(null)
const auth = useAuth()
let loadGeneration = 0
let operationGeneration = 0
const revisionOpen = computed({ get: () => selectedRevision.value !== null, set: (open: boolean) => { if (!open) selectedRevision.value = null } })
const validatedBody = ref(''), publishKey = ref(uuid())
const base = computed(() => `/api/admin/video/models/${encodeURIComponent(model.value)}/pricing`)
const body = computed(() => ({ expected_config_version: pricing.value?.config_version ?? '', config_version: configVersion.value, pricing_version: priceVersion.value, evidence_hash: evidenceHash.value, cells: pricing.value?.cells ?? [] }))
const canPublish = computed(() => validatedBody.value !== '' && validatedBody.value === JSON.stringify(body.value) && !busy.value)
watch(body, () => { publishKey.value = uuid() }, { deep: true })
async function load() {
  const generation = ++loadGeneration
  ++operationGeneration; busy.value = false
  loading.value = true; error.value = ''; notice.value = ''; pricing.value = null; previews.value = []; validatedBody.value = ''; selectedRevision.value = null; revisions.value = []; minimums.value = []
  try {
    const [data, history, minimum] = await Promise.all([api<Pricing>('GET', base.value), api<{ items: Revision[] }>('GET', `${base.value}/revisions`), api<{ items: Minimum[] }>('GET', `${base.value}/minimum-rules`)])
    if (generation !== loadGeneration) return
    pricing.value = data; revisions.value = history.items; minimums.value = minimum.items
    configVersion.value = ''; priceVersion.value = ''; evidenceHash.value = ''
  } catch (e) { if (generation === loadGeneration) error.value = errorMessage(e, '无法加载已核验的视频配置') } finally { if (generation === loadGeneration) loading.value = false }
}
async function validate() {
  const generation = loadGeneration, operation = ++operationGeneration
  const endpoint = base.value, snapshot = JSON.stringify(body.value)
  const current = () => generation === loadGeneration && operation === operationGeneration
  busy.value = true; error.value = ''; notice.value = ''; validatedBody.value = ''
  try {
    const result = await api<{ preview: Preview[] }>('POST', `${endpoint}/validate`, JSON.parse(snapshot))
    if (!current() || snapshot !== JSON.stringify(body.value)) return
    previews.value = result.preview; validatedBody.value = snapshot; notice.value = '校验通过。预览使用已核验上界，未发起生成请求。'
  } catch (e) { if (current()) error.value = errorMessage(e, '校验失败，请检查版本、金额和最低量覆盖') }
  finally { if (current()) busy.value = false }
}
async function publish() {
  if (!canPublish.value) return
  const generation = loadGeneration, operation = ++operationGeneration
  const endpoint = base.value, snapshot = JSON.stringify(body.value), idempotencyKey = publishKey.value
  const current = () => generation === loadGeneration && operation === operationGeneration
  busy.value = true; error.value = ''
  try {
    await api('POST', `${endpoint}/publications`, JSON.parse(snapshot), { headers: { 'Idempotency-Key': idempotencyKey } })
    if (!current()) return
    const refreshedGeneration = loadGeneration + 1
    await load()
    if (loadGeneration === refreshedGeneration) notice.value = '新价格版本已发布。历史任务仍使用创建时快照。'
  } catch (e) { if (current()) error.value = errorMessage(e, '发布失败；版本冲突时请刷新后重新编辑') }
  finally { if (current()) busy.value = false }
}
async function importMinimum() {
  const generation = loadGeneration, operation = ++operationGeneration
  const endpoint = base.value, snapshot = minimumJSON.value
  const current = () => generation === loadGeneration && operation === operationGeneration
  busy.value = true; error.value = ''; notice.value = ''
  try {
    await api('POST', `${endpoint}/minimum-rules`, JSON.parse(snapshot))
    if (!current()) return
    const result = await api<{ items: Minimum[] }>('GET', `${endpoint}/minimum-rules`)
    if (!current()) return
    minimums.value = result.items
    if (minimumJSON.value === snapshot) minimumJSON.value = ''
    validatedBody.value = ''; notice.value = '最低量证据版本已登记。发布价格前仍需校验完整覆盖。'
  } catch (e) { if (current()) error.value = errorMessage(e, '最低量 JSON 或证据字段无效') }
  finally { if (current()) busy.value = false }
}
function revisionText(r: Revision) { try { return JSON.stringify(JSON.parse(r.snapshot_json), null, 2) } catch { return r.snapshot_json } }
watch(() => [auth.orgId, auth.token], () => {
  ++loadGeneration; ++operationGeneration; busy.value = false; error.value = ''; notice.value = ''; selectedRevision.value = null; revisions.value = []; minimums.value = []; pricing.value = null; previews.value = []; minimumJSON.value = ''; validatedBody.value = ''
  if (auth.isAuthed) void load()
})
onDeactivated(() => { selectedRevision.value = null })
onBeforeUnmount(() => { ++loadGeneration; ++operationGeneration; selectedRevision.value = null })
onMounted(load)
</script>

<template>
  <main class="video-pricing-page">
    <header class="page-heading"><div><h1>视频定价</h1><p>创建时冻结上限，成功后按官方 Token 用量结算。输入 Token 为 0 表示官方不单独计量输入。</p></div><label>收费配置模型 <select v-model="model" :disabled="busy || loading" @change="load"><option>seedance-2.0</option><option>seedance-2.5</option></select></label></header>
    <section class="panel"><OfficialVideoPriceCatalog /></section>
    <p v-if="error" class="message error" role="alert">{{ error }}</p><p v-if="notice" class="message success" role="status">{{ notice }}</p>
    <p v-if="loading" aria-live="polite">正在读取价格和证据版本…</p>
    <template v-else-if="pricing">
      <section class="panel metadata"><div><small>当前价格 / 配置</small><strong>{{ pricing.pricing_version }} / {{ pricing.config_version }}</strong></div><div><small>能力版本</small><strong>{{ pricing.capability_version }}</strong></div><div><small>USD / CNY 汇率快照</small><strong>{{ pricing.fx_usd_per_cny }} · {{ pricing.fx_version }}</strong><small>{{ pricing.fx_observed_at }}</small></div><div><small>模型状态</small><strong>{{ pricing.enabled ? '已启用' : '已停用' }}</strong></div></section>
      <section class="panel"><h2>价格矩阵</h2><p>仅已核验能力可启用。商业售价由管理员填写；没有默认售价。缺少官方最低量覆盖的参考视频组合将被拒绝发布。</p>
        <div class="table-scroll"><table><thead><tr><th>启用</th><th>任务 / 分辨率</th><th>参考视频</th><th>草稿</th><th>售价 USD / 百万 Token</th><th>成本 CNY / 百万 Token</th><th>预冻结系数</th><th>最低量版本</th></tr></thead><tbody><tr v-for="(cell, i) in pricing.cells" :key="i" :class="{ unavailable: !cell.available }"><td><input v-model="cell.enabled" type="checkbox" :aria-label="`启用价格 ${i + 1}`" :disabled="!cell.available || busy"></td><td>{{ cell.mode }} · {{ cell.resolution }}<small v-if="!cell.available">能力尚未核验</small></td><td>{{ cell.input_video ? '有' : '无' }}</td><td>{{ cell.draft ? '是' : '否' }}</td><td><input v-model="cell.retail_usd_per_1m" inputmode="decimal" :aria-label="`售价 ${i + 1}`" :disabled="!cell.available || busy" placeholder="待管理员确定"></td><td><input v-model="cell.supplier_cny_per_1m" inputmode="decimal" :aria-label="`成本 ${i + 1}`" :disabled="!cell.available || busy" placeholder="官方核验值"></td><td><input v-model="cell.risk_multiplier" inputmode="decimal" :aria-label="`系数 ${i + 1}`" :disabled="!cell.available || busy" placeholder="≥ 1"></td><td><select v-if="cell.input_video" v-model="cell.minimum_rule_version" :disabled="!cell.available || busy" :aria-label="`最低量 ${i + 1}`"><option value="">请选择证据版本</option><option v-for="item in minimums" :key="item.id" :value="item.version">{{ item.version }}</option></select><span v-else>不适用</span></td></tr></tbody></table></div>
        <div class="fields"><label>新配置版本<input v-model="configVersion" placeholder="不可复用旧版本" :disabled="busy"></label><label>新价格版本<input v-model="priceVersion" placeholder="不可复用旧版本" :disabled="busy"></label><label class="wide">价格证据 SHA-256<input v-model="evidenceHash" placeholder="64 位十六进制原文件摘要" :disabled="busy"></label></div>
        <div class="actions"><button :disabled="busy" @click="validate">校验并预览</button><button class="primary" :disabled="!canPublish" @click="publish">发布已校验版本</button><span>启用范围：{{ pricing.ratios.join('、') }}，{{ pricing.min_duration }}–{{ pricing.max_duration }} 秒</span></div>
      </section>
      <section v-if="previews.length" class="panel"><h2>预冻结上界预览</h2><p>按有效倍率 1 计算，覆盖当前能力全部比例和时长；实际组织倍率与请求参数以任务快照为准。冻结金额不是最终扣费。</p><div class="table-scroll"><table><thead><tr><th>组合</th><th>估算 Token 上界</th><th>客户冻结上界 USD</th><th>供应商预留 CNY</th></tr></thead><tbody><tr v-for="p in previews" :key="p.key"><td>{{ p.key }}</td><td>{{ p.maximum_estimated_tokens }}</td><td>{{ p.maximum_hold_usd }}</td><td>{{ p.supplier_reserved_cny }}</td></tr></tbody></table></div></section>
      <section class="panel"><h2>官方最低量证据</h2><p>按分辨率、比例、输出时长及帧率登记，不能使用单个最小 Token 值替代整张表。登记代表当前实名管理员已人工核验来源，不代表系统自动证明真实性。上方参考目录中的估算下界不可直接作为本入口的已核验收费规则。</p><details><summary>导入新证据版本</summary><p>JSON 字段：version、source_url、evidence_hash、observed_at，以及 cells 数组；每项包含 resolution、ratio、output_duration_ms、fps、minimum_tokens。仅接受官方来源 HTTPS 地址，禁止填写未核验数值。</p><textarea v-model="minimumJSON" rows="8" aria-label="官方最低量 JSON" spellcheck="false" placeholder="粘贴经过核验的结构化最低量表"></textarea><button :disabled="busy || !minimumJSON.trim()" @click="importMinimum">确认已人工核验并登记</button></details><ul v-if="minimums.length"><li v-for="m in minimums" :key="m.id"><strong>{{ m.version }}</strong> · <a :href="m.source_url" target="_blank" rel="noopener noreferrer">官方来源</a> · 核验人 #{{ m.verified_by }} · {{ m.verified_at }}<small>{{ m.evidence_hash }}</small></li></ul><p v-else>尚无已核验最低量表。参考视频组合不可发布。</p></section>
      <section class="panel"><h2>发布历史</h2><p v-if="!revisions.length">尚无通过本页发布的版本。当前价格仍显示在上方。</p><div class="history"><button v-for="r in revisions" :key="r.id" @click="selectedRevision = r">{{ r.version }} · #{{ r.created_by }} · {{ r.created_at }}</button></div></section>
    </template>
    <section v-else-if="!loading" class="panel"><p>模型尚未配置或缺少实名超管权限。请先完成渠道与能力核验。</p><button @click="load">重新加载</button></section>
    <DetailDrawer v-model:open="revisionOpen" title="价格发布详情" size="lg" description="历史发布快照，只读查看；不修改当前价格。"><template v-if="selectedRevision"><p>{{ selectedRevision.version }} · 配置 {{ selectedRevision.config_version }} · #{{ selectedRevision.created_by }} · {{ selectedRevision.created_at }}</p><pre>{{ revisionText(selectedRevision) }}</pre></template></DetailDrawer>
  </main>
</template>

<style scoped>
.video-pricing-page{max-width:1500px;margin:0 auto;padding:28px;display:grid;gap:20px;color:rgb(var(--c-ink))}.page-heading{display:flex;justify-content:space-between;gap:24px;align-items:center}h1{font-size:26px;margin:0 0 8px}h2{font-size:18px;margin:0 0 12px}p,small{color:rgb(var(--c-muted));line-height:1.6}small{display:block;font-size:12px}.panel{padding:22px;border:1px solid rgb(var(--c-line));border-radius:12px;background:rgb(var(--c-panel));min-width:0}.metadata{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:20px}.metadata strong{display:block;overflow-wrap:anywhere}.table-scroll{overflow-x:auto}table{width:100%;border-collapse:collapse;font-size:13px}th,td{text-align:left;border-bottom:1px solid rgb(var(--c-line));padding:12px 8px}th{white-space:nowrap;color:rgb(var(--c-muted))}input,select,textarea,button{font:inherit;color:inherit;border:1px solid rgb(var(--c-line));border-radius:6px;background:rgb(var(--c-bg));padding:9px}td input:not([type=checkbox]){min-width:105px;width:135px}td select{max-width:180px}button{cursor:pointer}button:disabled,input:disabled,select:disabled{opacity:.48;cursor:not-allowed}.primary{background:#355fe5;border-color:#355fe5;color:white}.fields{display:flex;flex-wrap:wrap;gap:18px;margin-top:22px}.fields label{display:grid;gap:8px;flex:1;min-width:200px}.fields .wide{flex:2}.actions{display:flex;align-items:center;gap:14px;flex-wrap:wrap;margin-top:20px}.actions span{font-size:12px;color:rgb(var(--c-muted))}.unavailable{opacity:.6}.message{padding:14px;border-radius:8px;margin:0}.error{background:#52202a;color:#ffd9e0}.success{background:#153f34;color:#b7f2db}textarea{width:100%;box-sizing:border-box;margin:12px 0;font-family:monospace}.history{display:flex;gap:10px;flex-wrap:wrap}pre{overflow:auto;max-height:500px;padding:16px;background:rgb(var(--c-bg))}li{padding:8px 0;overflow-wrap:anywhere}a{color:rgb(var(--c-ink))}summary{cursor:pointer;padding:8px 0}@media(max-width:800px){.video-pricing-page{padding:16px}.metadata{grid-template-columns:1fr 1fr}.page-heading{align-items:flex-start;flex-direction:column}.panel{padding:16px}}@media(max-width:480px){.metadata{grid-template-columns:1fr}}
</style>
