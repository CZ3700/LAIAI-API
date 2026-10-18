<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { api, errorMessage, uuid } from '@/lib/api'
import OfficialVideoPriceCatalog from '@/components/OfficialVideoPriceCatalog.vue'
import DetailDrawer from '@/components/DetailDrawer.vue'
import { useAuth } from '@/stores/auth'
import { fillMissingOfficialCosts, type PriceCell as Cell, type ReferenceCandidates, type SourceRow } from '@/components/pricing/types'

const props = defineProps<{ selectedModel?: string; selectedChannelId?: number; embedded?: boolean; contextKey?: string }>()
interface Pricing { model: string; config_version: string; pricing_version: string; capability_version: string; enabled: boolean; cells: Cell[]; fx_usd_per_cny: string; fx_version: string; fx_observed_at: string; ratios: string[]; min_duration: number; max_duration: number }
interface Preview { key: string; maximum_hold_usd: string; maximum_estimated_tokens: number; supplier_reserved_cny: string; minimum_rule_version: string }
interface Revision { id: number; version: string; config_version: string; created_by: number; created_at: string; snapshot_json: string }
interface Minimum { id: number; version: string; source_url: string; evidence_hash: string; observed_at: string; verified_by: number; verified_at: string }
const model = ref(props.selectedModel ?? 'seedance-2.0'), pricing = ref<Pricing | null>(null)
const loading = ref(false), busy = ref(false), error = ref(''), notice = ref('')
const configVersion = ref(''), priceVersion = ref(''), evidenceHash = ref('')
const previews = ref<Preview[]>([]), revisions = ref<Revision[]>([]), minimums = ref<Minimum[]>([])
const minimumJSON = ref(''), selectedRevision = ref<Revision | null>(null)
const candidates = ref<ReferenceCandidates | null>(null), referenceError = ref(''), prefilled = ref(0)
const selectedEvidence = ref<SourceRow[] | null>(null), showMinimum = ref(false)
const evidenceQuery = ref(''), evidencePage = ref(1), evidencePageSize = 20
const referenceOpen = computed({ get: () => selectedEvidence.value !== null || showMinimum.value, set: (open: boolean) => { if (!open) { selectedEvidence.value = null; showMinimum.value = false } } })
const allSourceEvidence = computed(() => (selectedEvidence.value ?? []).map(row => ({ ...row, source: candidates.value?.sources.find(s => s.id === row.source_id) })).filter(row => !evidenceQuery.value.trim() || `${row.source_id} ${row.row} ${row.source?.raw_rows[row.row - 1]?.join(' ')}`.toLocaleLowerCase().includes(evidenceQuery.value.trim().toLocaleLowerCase())))
const allMinimumEvidence = computed(() => candidates.value?.minimum.cells.filter(cell => !evidenceQuery.value.trim() || [cell.resolution, cell.ratio, cell.output_duration_ms, cell.fps, cell.minimum_tokens].join(' ').toLocaleLowerCase().includes(evidenceQuery.value.trim().toLocaleLowerCase())) ?? [])
const evidenceCount = computed(() => showMinimum.value ? allMinimumEvidence.value.length : allSourceEvidence.value.length)
const evidencePages = computed(() => Math.max(1, Math.ceil(evidenceCount.value / evidencePageSize)))
const sourceEvidence = computed(() => allSourceEvidence.value.slice((evidencePage.value - 1) * evidencePageSize, evidencePage.value * evidencePageSize))
const minimumEvidence = computed(() => allMinimumEvidence.value.slice((evidencePage.value - 1) * evidencePageSize, evidencePage.value * evidencePageSize))
watch([selectedEvidence, showMinimum], () => { evidenceQuery.value = ''; evidencePage.value = 1 }, { flush: 'sync' })
watch(evidenceQuery, () => { evidencePage.value = 1 }, { flush: 'sync' })
const bindingMatches = computed(() => !props.selectedChannelId || candidates.value?.config?.channel_id === props.selectedChannelId)
const canEdit = computed(() => !!pricing.value && bindingMatches.value && auth.isSuperadmin)
const auth = useAuth()
let loadGeneration = 0
let operationGeneration = 0
const revisionOpen = computed({ get: () => selectedRevision.value !== null, set: (open: boolean) => { if (!open) selectedRevision.value = null } })
const validatedBody = ref(''), publishKey = ref(uuid())
const base = computed(() => `/api/admin/video/models/${encodeURIComponent(model.value)}/pricing`)
const body = computed(() => ({ expected_config_version: pricing.value?.config_version ?? '', config_version: configVersion.value, pricing_version: priceVersion.value, evidence_hash: evidenceHash.value, cells: pricing.value?.cells ?? [] }))
const canPublish = computed(() => canEdit.value && validatedBody.value !== '' && validatedBody.value === JSON.stringify(body.value) && !busy.value)
watch(body, () => { publishKey.value = uuid() }, { deep: true })
async function load() {
  const generation = ++loadGeneration
  ++operationGeneration; busy.value = false
  loading.value = true; error.value = ''; notice.value = ''; pricing.value = null; previews.value = []; validatedBody.value = ''; selectedRevision.value = null; revisions.value = []; minimums.value = []; minimumJSON.value = ''; configVersion.value = ''; priceVersion.value = ''; evidenceHash.value = ''; candidates.value = null; referenceError.value = ''; prefilled.value = 0; selectedEvidence.value = null; showMinimum.value = false
  if (!auth.isSuperadmin || !['seedance-2.0', 'seedance-2.5'].includes(model.value)) { loading.value = false; return }
  const endpoint = base.value
  try {
    const [data, history, minimum, reference] = await Promise.allSettled([api<Pricing>('GET', endpoint), api<{ items: Revision[] }>('GET', `${endpoint}/revisions`), api<{ items: Minimum[] }>('GET', `${endpoint}/minimum-rules`), api<ReferenceCandidates>('GET', `${endpoint}/reference-candidates`)])
    if (generation !== loadGeneration) return
    if (reference.status === 'fulfilled' && reference.value.model === model.value) candidates.value = reference.value
    else referenceError.value = '官方候选暂不可用，本次不会自动填充成本。'
    if (history.status === 'fulfilled') revisions.value = history.value.items
    if (minimum.status === 'fulfilled') minimums.value = minimum.value.items
    if (data.status !== 'fulfilled') throw data.reason
    if (data.value.model !== model.value) throw new Error('返回的模型与当前选择不一致')
    pricing.value = data.value
    if (candidates.value && bindingMatches.value && candidates.value.config?.config_version === data.value.config_version) {
      prefilled.value = fillMissingOfficialCosts(data.value.cells, candidates.value)
    }
  } catch (e) { if (generation === loadGeneration) error.value = errorMessage(e, '无法加载已核验的视频配置') } finally { if (generation === loadGeneration) loading.value = false }
}
async function validate() {
  if (!canEdit.value || busy.value) return
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
  if (!canEdit.value || busy.value) return
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
watch(() => [auth.orgId, auth.token, auth.role, props.selectedModel, props.selectedChannelId, props.contextKey], () => {
  ++loadGeneration; ++operationGeneration; busy.value = false; error.value = ''; notice.value = ''; selectedRevision.value = null; revisions.value = []; minimums.value = []; pricing.value = null; previews.value = []; minimumJSON.value = ''; validatedBody.value = ''
  configVersion.value = ''; priceVersion.value = ''; evidenceHash.value = ''; candidates.value = null; referenceError.value = ''; prefilled.value = 0; selectedEvidence.value = null; showMinimum.value = false
  if (props.selectedModel && model.value !== props.selectedModel) { model.value = props.selectedModel; return }
  if (auth.isAuthed) void load()
}, { flush: 'sync' })
watch(model, () => { void load() }, { flush: 'sync' })
onDeactivated(() => { ++loadGeneration; ++operationGeneration; loading.value = false; busy.value = false; error.value = ''; notice.value = ''; referenceError.value = ''; selectedRevision.value = null; selectedEvidence.value = null; showMinimum.value = false; pricing.value = null; candidates.value = null; revisions.value = []; minimums.value = []; previews.value = []; minimumJSON.value = ''; validatedBody.value = ''; configVersion.value = ''; priceVersion.value = ''; evidenceHash.value = '' })
onBeforeUnmount(() => { ++loadGeneration; ++operationGeneration; selectedRevision.value = null })
onMounted(load)
onActivated(() => { if (!pricing.value && !loading.value) void load() })
</script>

<template>
  <section class="video-pricing-page" :class="{ embedded }">
    <header class="page-heading"><div><h1>视频定价<span v-if="embedded"> · {{ model }}</span></h1><p>创建时冻结上限，成功后按官方实际计费 Token 结算；参考视频按对应价格组合计费。</p></div><label v-if="!embedded">收费配置模型 <select v-model="model" :disabled="busy || loading"><option>seedance-2.0</option><option>seedance-2.5</option></select></label></header>
    <p v-if="!auth.isSuperadmin" class="message">官方成本与价格发布需要有效的个人超管会话。</p>
    <section v-if="candidates" class="panel reference-summary">
      <h2>官方成本候选 · {{ candidates.catalog_version }}</h2>
      <p>仅预填标准生成、默认服务层级的缺失 CNY 成本；USD 售价、启用状态、冻结系数和已配置成本保持现值。{{ prefilled ? `本次草稿已预填 ${prefilled} 个成本单元格。` : '当前没有需要自动填充的成本单元格。' }}</p>
      <p v-if="!bindingMatches" class="message error">此模型的官方配置绑定了其他渠道，当前仅查看参考数据，不能在此选择下发布价格。</p>
      <p v-if="!candidates.config">尚未绑定已核验配置。以下只是参考候选，不能代替模型开通或价格发布。</p>
      <div class="table-scroll"><table><thead><tr><th>分辨率</th><th>参考视频</th><th>成本 CNY / 百万 Token</th><th>当前能力</th><th>来源</th></tr></thead><tbody><tr v-for="c in candidates.prices" :key="c.key"><td>{{ c.resolution }}</td><td>{{ c.input_video ? '有' : '无' }}</td><td>{{ c.supplier_cny_per_1m }}</td><td>{{ c.available ? '已核验' : '待核验，不能启用' }}</td><td><button @click="selectedEvidence = c.source_rows">引用规则详情</button></td></tr></tbody></table></div>
      <p>最低量：官方估算参考，仅用于冻结估算候选，仍需人工核验与完整维度覆盖。</p><p v-if="candidates.minimum.issues.includes('fps_not_in_source')">原表未列帧率，不能借用当前配置猜填 FPS 或自动登记为已核验规则。</p>
      <div class="actions"><button @click="showMinimum = true">查看最低量参考（{{ candidates.minimum.cells.length }} 项）</button></div>
    </section>
    <p v-if="referenceError" class="message" role="status">{{ referenceError }}</p>
    <details v-if="auth.isSuperadmin" class="panel"><summary>查看本次支持系列的官方原始目录</summary><OfficialVideoPriceCatalog :allowed-series="[model === 'seedance-2.5' ? '2.5' : '2.0']" /></details>
    <p v-if="error" class="message error" role="alert">{{ error }}</p><p v-if="notice" class="message success" role="status">{{ notice }}</p>
    <p v-if="loading" aria-live="polite">正在读取价格和证据版本…</p>
    <template v-else-if="pricing && bindingMatches">
      <section class="panel metadata"><div><small>当前价格 / 配置</small><strong>{{ pricing.pricing_version }} / {{ pricing.config_version }}</strong></div><div><small>能力版本</small><strong>{{ pricing.capability_version }}</strong></div><div><small>USD / CNY 汇率快照</small><strong>{{ pricing.fx_usd_per_cny }} · {{ pricing.fx_version }}</strong><small>{{ pricing.fx_observed_at }}</small></div><div><small>模型状态</small><strong>{{ pricing.enabled ? '已启用' : '已停用' }}</strong></div></section>
      <section class="panel"><h2>价格矩阵</h2><p>仅已核验能力可启用。商业售价由管理员填写；没有默认售价。缺少官方最低量覆盖的参考视频组合将被拒绝发布。</p>
        <div class="table-scroll"><table><thead><tr><th>启用</th><th>任务 / 分辨率</th><th>参考视频</th><th>草稿</th><th>售价 USD / 百万 Token</th><th>成本 CNY / 百万 Token</th><th>预冻结系数</th><th>最低量版本</th></tr></thead><tbody><tr v-for="(cell, i) in pricing.cells" :key="i" :class="{ unavailable: !cell.available }"><td><input v-model="cell.enabled" type="checkbox" :aria-label="`启用价格 ${i + 1}`" :disabled="!canEdit || !cell.available || busy"></td><td>{{ cell.mode }} · {{ cell.resolution }}<small v-if="!cell.available">能力尚未核验</small></td><td>{{ cell.input_video ? '有' : '无' }}</td><td>{{ cell.draft ? '是' : '否' }}</td><td><input v-model="cell.retail_usd_per_1m" inputmode="decimal" :aria-label="`售价 ${i + 1}`" :disabled="!canEdit || !cell.available || busy" placeholder="待管理员确定"></td><td><input v-model="cell.supplier_cny_per_1m" inputmode="decimal" :aria-label="`成本 ${i + 1}`" :disabled="!canEdit || !cell.available || busy" placeholder="官方核验值"></td><td><input v-model="cell.risk_multiplier" inputmode="decimal" :aria-label="`系数 ${i + 1}`" :disabled="!canEdit || !cell.available || busy" placeholder="≥ 1"></td><td><select v-if="cell.input_video" v-model="cell.minimum_rule_version" :disabled="!canEdit || !cell.available || busy" :aria-label="`最低量 ${i + 1}`"><option value="">请选择证据版本</option><option v-for="item in minimums" :key="item.id" :value="item.version">{{ item.version }}</option></select><span v-else>不适用</span></td></tr></tbody></table></div>
        <div class="fields"><label>新配置版本<input v-model="configVersion" placeholder="不可复用旧版本" :disabled="!canEdit || busy"></label><label>新价格版本<input v-model="priceVersion" placeholder="不可复用旧版本" :disabled="!canEdit || busy"></label><label class="wide">价格证据 SHA-256<input v-model="evidenceHash" placeholder="64 位十六进制原文件摘要" :disabled="!canEdit || busy"></label></div>
        <div class="actions"><button :disabled="busy || !canEdit" @click="validate">校验并预览</button><button class="primary" :disabled="!canPublish" @click="publish">发布已校验版本</button><span>启用范围：{{ pricing.ratios.join('、') }}，{{ pricing.min_duration }}–{{ pricing.max_duration }} 秒</span></div>
      </section>
      <section v-if="previews.length" class="panel"><h2>预冻结上界预览</h2><p>按有效倍率 1 计算，覆盖当前能力全部比例和时长；实际组织倍率与请求参数以任务快照为准。冻结金额不是最终扣费。</p><div class="table-scroll"><table><thead><tr><th>组合</th><th>估算 Token 上界</th><th>客户冻结上界 USD</th><th>供应商预留 CNY</th></tr></thead><tbody><tr v-for="p in previews" :key="p.key"><td>{{ p.key }}</td><td>{{ p.maximum_estimated_tokens }}</td><td>{{ p.maximum_hold_usd }}</td><td>{{ p.supplier_reserved_cny }}</td></tr></tbody></table></div></section>
      <section class="panel"><h2>官方最低量证据</h2><p>按分辨率、比例、输出时长及帧率登记，不能使用单个最小 Token 值替代整张表。登记代表当前实名管理员已人工核验来源，不代表系统自动证明真实性。上方参考目录中的估算下界不可直接作为本入口的已核验收费规则。</p><details><summary>导入新证据版本</summary><p>JSON 字段：version、source_url、evidence_hash、observed_at，以及 cells 数组；每项包含 resolution、ratio、output_duration_ms、fps、minimum_tokens。仅接受官方来源 HTTPS 地址，禁止填写未核验数值。</p><textarea :disabled="!canEdit || busy" v-model="minimumJSON" rows="8" aria-label="官方最低量 JSON" spellcheck="false" placeholder="粘贴经过核验的结构化最低量表"></textarea><button :disabled="!canEdit || busy || !minimumJSON.trim()" @click="importMinimum">确认已人工核验并登记</button></details><ul v-if="minimums.length"><li v-for="m in minimums" :key="m.id"><strong>{{ m.version }}</strong> · <a :href="m.source_url" target="_blank" rel="noopener noreferrer">官方来源</a> · 核验人 #{{ m.verified_by }} · {{ m.verified_at }}<small>{{ m.evidence_hash }}</small></li></ul><p v-else>尚无已核验最低量表。参考视频组合不可发布。</p></section>
      <section class="panel"><h2>发布历史</h2><p v-if="!revisions.length">尚无通过本页发布的版本。当前价格仍显示在上方。</p><div class="history"><button v-for="r in revisions" :key="r.id" @click="selectedRevision = r">{{ r.version }} · #{{ r.created_by }} · {{ r.created_at }}</button></div></section>
    </template>
    <section v-else-if="!loading" class="panel"><p>模型尚未配置或缺少实名超管权限。请先完成渠道与能力核验。</p><button @click="load">重新加载</button></section>
    <DetailDrawer v-model:open="revisionOpen" title="价格发布详情" size="lg" description="历史发布快照，只读查看；不修改当前价格。"><template v-if="selectedRevision"><p>{{ selectedRevision.version }} · 配置 {{ selectedRevision.config_version }} · #{{ selectedRevision.created_by }} · {{ selectedRevision.created_at }}</p><pre>{{ revisionText(selectedRevision) }}</pre></template></DetailDrawer>
    <DetailDrawer v-model:open="referenceOpen" :title="showMinimum ? '最低量参考详情' : '引用规则详情'" size="lg" description="固化的官方参考证据；查看不会登记最低量或发布价格。">
      <template v-if="candidates"><p>目录 {{ candidates.catalog_version }}</p><p class="evidence-hash">清单 SHA-256：{{ candidates.manifest_sha256 }}</p>
        <label class="reference-search">搜索参考行<input v-model="evidenceQuery" type="search" placeholder="分辨率、比例、Token 或原文"></label>
        <template v-if="showMinimum"><p>官方估算 · 待人工核验。未列 FPS 的值保持未知；不会从示例视频金额倒推 Token。</p><div class="table-scroll"><table><thead><tr><th>分辨率</th><th>比例</th><th>输出时长 ms</th><th>FPS</th><th>最低 Token 参考</th><th>来源</th></tr></thead><tbody><tr v-for="(c, i) in minimumEvidence" :key="i"><td>{{ c.resolution }}</td><td>{{ c.ratio }}</td><td>{{ c.output_duration_ms }}</td><td>{{ c.fps ?? '未单列' }}</td><td>{{ c.minimum_tokens }}</td><td><button @click="showMinimum = false; selectedEvidence = c.source_rows">原始行</button></td></tr></tbody></table></div></template>
        <template v-else><article v-for="row in sourceEvidence" :key="`${row.source_id}:${row.row}`" class="source-row"><template v-if="row.source"><p><a :href="row.source.source_url" target="_blank" rel="noopener noreferrer">官方来源 ↗</a> · 原始行 {{ row.row }}</p><p>{{ row.source.table_id }} / {{ row.source.view_id }} · {{ row.source.captured_at }}</p><p class="evidence-hash">{{ row.source.raw_sha256 }}</p><div class="table-scroll"><table><thead><tr><th v-for="(header, i) in row.source.headers" :key="i">{{ header }}<small>{{ row.source.columns[i]?.unit }} · {{ row.source.columns[i]?.precision }}</small></th></tr></thead><tbody><tr><td v-for="(cell, i) in row.source.raw_rows[row.row - 1]" :key="i">{{ cell === '' ? '（原表空白）' : cell }}</td></tr></tbody></table></div></template></article></template>
        <p v-if="!evidenceCount">没有匹配的参考行。</p><nav class="actions" aria-label="参考行分页"><button :disabled="evidencePage <= 1" @click="evidencePage--">上一页</button><span>{{ evidencePage }} / {{ evidencePages }} 页 · {{ evidenceCount }} 项</span><button :disabled="evidencePage >= evidencePages" @click="evidencePage++">下一页</button></nav>
      </template>
    </DetailDrawer>
  </section>
</template>

<style scoped>
.video-pricing-page.embedded{padding:0;max-width:none;width:100%}.evidence-hash{overflow-wrap:anywhere;font-family:monospace;font-size:12px}.source-row{padding:16px 0;border-bottom:1px solid rgb(var(--c-line))}.reference-summary{border-left:3px solid rgb(var(--c-accent))}
.video-pricing-page{max-width:1500px;margin:0 auto;padding:28px;display:grid;gap:20px;color:rgb(var(--c-ink))}.page-heading{display:flex;justify-content:space-between;gap:24px;align-items:center}h1{font-size:26px;margin:0 0 8px}h2{font-size:18px;margin:0 0 12px}p,small{color:rgb(var(--c-muted));line-height:1.6}small{display:block;font-size:12px}.panel{padding:22px;border:1px solid rgb(var(--c-line));border-radius:12px;background:rgb(var(--c-panel));min-width:0}.metadata{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:20px}.metadata strong{display:block;overflow-wrap:anywhere}.table-scroll{overflow-x:auto}table{width:100%;border-collapse:collapse;font-size:13px}th,td{text-align:left;border-bottom:1px solid rgb(var(--c-line));padding:12px 8px}th{white-space:nowrap;color:rgb(var(--c-muted))}input,select,textarea,button{font:inherit;color:inherit;border:1px solid rgb(var(--c-line));border-radius:6px;background:rgb(var(--c-bg));padding:9px}td input:not([type=checkbox]){min-width:105px;width:135px}td select{max-width:180px}button{cursor:pointer}button:disabled,input:disabled,select:disabled{opacity:.48;cursor:not-allowed}.primary{background:#355fe5;border-color:#355fe5;color:white}.fields{display:flex;flex-wrap:wrap;gap:18px;margin-top:22px}.fields label{display:grid;gap:8px;flex:1;min-width:200px}.fields .wide{flex:2}.actions{display:flex;align-items:center;gap:14px;flex-wrap:wrap;margin-top:20px}.actions span{font-size:12px;color:rgb(var(--c-muted))}.unavailable{opacity:.6}.message{padding:14px;border-radius:8px;margin:0}.error{background:#52202a;color:#ffd9e0}.success{background:#153f34;color:#b7f2db}textarea{width:100%;box-sizing:border-box;margin:12px 0;font-family:monospace}.history{display:flex;gap:10px;flex-wrap:wrap}pre{overflow:auto;max-height:500px;padding:16px;background:rgb(var(--c-bg))}li{padding:8px 0;overflow-wrap:anywhere}a{color:rgb(var(--c-ink))}summary{cursor:pointer;padding:8px 0}@media(max-width:800px){.video-pricing-page{padding:16px}.metadata{grid-template-columns:1fr 1fr}.page-heading{align-items:flex-start;flex-direction:column}.panel{padding:16px}}@media(max-width:480px){.metadata{grid-template-columns:1fr}}
</style>
