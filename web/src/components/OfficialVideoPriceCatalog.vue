<script setup lang="ts">
import { computed, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { api, errorMessage } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import DetailDrawer from '@/components/DetailDrawer.vue'

interface Column { semantic: string; unit: string; precision: string; missing_values?: string[] }
interface Capture { title: string; source_url: string; view_id: string; captured_at: string; raw_sha256: string; expected_rows: number; first_row: number; resolution?: string; headers: string[]; columns: Column[] }
interface SourceTable { id: string; title: string; series: string[]; status: 'captured' | 'missing'; source_url: string; table_id: string; view_id: string | null; captured_at: string | null; raw_sha256: string; expected_rows: number; headers: string[] | null; columns: Column[] | null; notes: string[]; raw_rows: string[][]; captures?: Capture[]; reference_video_scope: 'per_row_input_duration' | 'requires_reference_video' | 'unspecified'; heterogeneous_views: boolean }
interface Catalog { version: string; title: string; manifest_sha256: string; complete: boolean; captured_tables: number; tables: SourceTable[] }
const auth = useAuth()
const props = defineProps<{ allowedSeries?: string[] }>()
const catalog = ref<Catalog | null>(null), error = ref(''), loading = ref(false)
const selected = ref(''), query = ref(''), series = ref('all'), view = ref('all'), resolution = ref('all'), referenceVideo = ref('all'), page = ref(1)
const selectedEvidence = ref<'manifest' | 'table' | ''>('')
const pageSize = 50
let generation = 0
const precisionLabels: Record<string, string> = { exact: '精确标示', estimated: '官方估算', rounded: '舍入展示', not_applicable: '原文' }
const unitLabels: Record<string, string> = { 'CNY/1M_tokens': 'CNY / 百万 Token', 'CNY/video': 'CNY / 示例视频', 'CNY/second': 'CNY / 秒', token: 'Token', second: '秒', pixel: '像素', fps: 'fps' }
const semanticLabels: Record<string, string> = { supplier_token_price: '官方刊例单价', estimated_tokens: '估算用量', minimum_tokens: '最低 Token 参考', minimum_amount: '最低金额参考', reference_video_amount: '示例视频金额', reference_second_price: '参考秒价' }
const seriesLabels: Record<string, string> = { '2.0fast': '2.0 Fast', '2.0mini': '2.0 Mini' }
const seriesAllowed = (value: string) => !props.allowedSeries || props.allowedSeries.includes(value)
const seriesOptions = computed(() => [...new Set(catalog.value?.tables.flatMap(t => t.series).filter(seriesAllowed) ?? [])])
const tables = computed(() => catalog.value?.tables.filter(t => t.series.some(seriesAllowed) && (series.value === 'all' || t.series.includes(series.value))) ?? [])
const table = computed(() => tables.value.find(t => t.id === selected.value) ?? tables.value[0] ?? null)
const effectiveView = computed(() => table.value?.heterogeneous_views && view.value === 'all' ? table.value.captures?.[0]?.view_id ?? '' : view.value)
const activeCapture = computed(() => table.value?.captures?.find(c => c.view_id === effectiveView.value))
const activeHeaders = computed(() => activeCapture.value?.headers ?? table.value?.headers ?? [])
const activeColumns = computed(() => activeCapture.value?.columns ?? table.value?.columns ?? [])
const resolutionColumn = computed(() => activeColumns.value.findIndex(c => c.semantic === 'resolution'))
const seriesColumn = computed(() => activeColumns.value.findIndex(c => c.semantic === 'series'))
const inputDurationColumn = computed(() => activeColumns.value.findIndex(c => c.semantic === 'input_duration'))
const viewRows = computed(() => (table.value?.raw_rows ?? []).map((cells, index) => {
  const source = table.value?.captures?.find(c => index + 1 >= c.first_row && index + 1 < c.first_row + c.expected_rows)
  return { cells, index: index + 1, source, sourceRow: source ? index + 2 - source.first_row : index + 1 }
}).filter(row => (effectiveView.value === 'all' || row.source?.view_id === effectiveView.value) && (seriesColumn.value < 0 || seriesAllowed(row.cells[seriesColumn.value] ?? ''))))
const visibleCaptures = computed(() => table.value?.captures?.filter(capture => {
  const index = capture.columns.findIndex(c => c.semantic === 'series')
  return index < 0 || table.value!.raw_rows.slice(capture.first_row - 1, capture.first_row - 1 + capture.expected_rows).some(row => seriesAllowed(row[index] ?? ''))
}) ?? [])
const resolutionOptions = computed(() => [...new Set((table.value?.heterogeneous_views ? table.value.captures?.map(c => c.resolution) : viewRows.value.map(r => resolutionColumn.value < 0 ? r.source?.resolution : r.cells[resolutionColumn.value]))?.filter((r): r is string => !!r) ?? [])])
function referenceKind(cells: string[]) {
  if (table.value?.reference_video_scope === 'requires_reference_video') return 'with'
  if (table.value?.reference_video_scope !== 'per_row_input_duration') return 'unspecified'
  const duration = cells[inputDurationColumn.value] ?? ''
  if (!/^(0|[1-9][0-9]*)(\.[0-9]+)?$/.test(duration)) return 'unspecified'
  return /[1-9]/.test(duration) ? 'with' : 'without'
}
const referenceLabels: Record<string, string> = { with: '含参考视频', without: '无参考视频', unspecified: '原表未说明' }
const referenceOptions = computed(() => [...new Set(viewRows.value.map(r => referenceKind(r.cells)))])
const filteredRows = computed(() => {
  const value = query.value.trim().toLocaleLowerCase()
  return viewRows.value.filter(row => (series.value === 'all' || seriesColumn.value < 0 || row.cells[seriesColumn.value] === series.value)
    && (resolution.value === 'all' || (resolutionColumn.value < 0 ? row.source?.resolution : row.cells[resolutionColumn.value]) === resolution.value)
    && (referenceVideo.value === 'all' || referenceKind(row.cells) === referenceVideo.value)
    && (!value || row.cells.some(cell => cell.toLocaleLowerCase().includes(value))))
})
const pages = computed(() => Math.max(1, Math.ceil(filteredRows.value.length / pageSize)))
const rows = computed(() => filteredRows.value.slice((page.value - 1) * pageSize, page.value * pageSize))
watch([selected, series, query, view, resolution, referenceVideo], () => { page.value = 1 })
watch(table, () => { view.value = table.value?.heterogeneous_views ? table.value.captures?.[0]?.view_id ?? '' : 'all'; resolution.value = 'all'; referenceVideo.value = 'all'; selectedEvidence.value = '' })
watch(view, () => { resolution.value = table.value?.heterogeneous_views ? activeCapture.value?.resolution ?? 'all' : 'all' })
watch(resolution, value => {
  if (!table.value?.heterogeneous_views || value === 'all') return
  const matchingView = table.value.captures?.find(c => c.resolution === value)
  if (matchingView) view.value = matchingView.view_id
})
watch(series, () => { if (!tables.value.some(t => t.id === selected.value)) selected.value = tables.value[0]?.id ?? '' })
watch(() => props.allowedSeries?.join(','), () => { series.value = 'all'; selected.value = tables.value[0]?.id ?? ''; selectedEvidence.value = ''; query.value = '' }, { flush: 'sync' })
async function load() {
  const current = ++generation
  catalog.value = null; error.value = ''; loading.value = true; page.value = 1; view.value = 'all'; resolution.value = 'all'; referenceVideo.value = 'all'; selectedEvidence.value = ''
  try {
    const result = await api<Catalog>('GET', '/api/admin/video/official-price-catalog')
    if (current !== generation) return
    catalog.value = result; selected.value = tables.value[0]?.id ?? ''
  } catch (e) { if (current === generation) error.value = errorMessage(e, '官方参考目录暂时不可用') }
  finally { if (current === generation) loading.value = false }
}
watch(() => [auth.orgId, auth.token], () => { ++generation; selectedEvidence.value = ''; catalog.value = null; query.value = ''; selected.value = ''; if (auth.isAuthed) void load() })
onMounted(load)
onDeactivated(() => { selectedEvidence.value = '' })
onBeforeUnmount(() => { ++generation; catalog.value = null })
</script>

<template>
  <section class="official-catalog" aria-labelledby="official-catalog-title">
    <header class="catalog-heading"><div><h2 id="official-catalog-title">官方参考目录</h2><p>官方 CNY 刊例、估算 Token 和示例视频金额分别展示。浏览目录不会修改客户 USD 售价、启用模型或发布最低量规则。</p></div><button :disabled="loading" @click="load">重新读取</button></header>
    <p v-if="error" role="alert" class="catalog-error">{{ error }}</p>
    <p v-if="loading" aria-live="polite">正在读取已固化的官方来源…</p>
    <template v-if="catalog">
      <div class="catalog-summary"><div><small>目录版本</small><strong>{{ catalog.version }}</strong></div><div><small>来源完整度</small><strong>{{ catalog.captured_tables }} / {{ catalog.tables.length }} 张表已采集</strong><span>{{ catalog.complete ? '清单内来源已全部采集' : '部分来源待采集，未补造缺失数据' }}</span></div><div><small>运行支持</small><span>{{ allowedSeries ? `本入口仅展示 ${allowedSeries.join('、')} 系列；收录不代表已启用。` : '1.0 / 1.5 等系列仅供查阅；目录收录不代表已接入或启用。' }}</span></div></div>
      <div><button @click="selectedEvidence = 'manifest'">目录信息</button></div>
      <div class="catalog-filter"><label>系列<select v-model="series"><option value="all">全部系列</option><option v-for="s in seriesOptions" :key="s" :value="s">Seedance {{ seriesLabels[s] ?? s }}</option></select></label><label>来源表<select v-model="selected"><option v-for="t in tables" :key="t.id" :value="t.id">{{ t.title }}{{ t.status === 'missing' ? ' · 待采集' : '' }}</option></select></label><label class="search">查找原始行<input v-model="query" type="search" placeholder="系列、分辨率、比例或原文数值" :disabled="table?.status !== 'captured'"></label></div>
      <article v-if="table" :key="table.id" class="catalog-table">
        <div class="source-title"><h3>{{ table.title }}</h3><a :href="table.source_url" target="_blank" rel="noopener noreferrer">查看官方来源 ↗</a></div>
        <p v-if="table.status === 'missing'" class="catalog-missing">尚未完成此表的采集与核验。没有数据不代表免费、零 Token 或不支持；当前收费配置保持原样。</p>
        <ul v-if="table.notes.length" class="source-notes"><li v-for="note in table.notes" :key="note">{{ note }}</li></ul>
        <template v-if="table.status === 'captured'">
          <p class="capture">最近采集时间（UTC）：{{ table.captured_at }} · 原始 {{ table.expected_rows }} 行 · 当前匹配 {{ filteredRows.length }} 行</p>
          <label v-if="visibleCaptures.length" class="view-filter">原始视图 <select v-model="view"><option v-if="!table.heterogeneous_views" value="all">全部已采集视图</option><option v-for="capture in visibleCaptures" :key="capture.view_id" :value="capture.view_id">{{ capture.title }} · {{ capture.expected_rows }} 行</option></select></label>
          <div class="catalog-filter"><label v-if="resolutionOptions.length">分辨率（{{ resolutionColumn < 0 ? '来源视图' : '原表枚举' }}）<select v-model="resolution"><option value="all">全部分辨率</option><option v-for="r in resolutionOptions" :key="r" :value="r">{{ r }}</option></select></label><label>参考视频场景<select v-model="referenceVideo"><option value="all">全部场景</option><option v-for="kind in referenceOptions" :key="kind" :value="kind">{{ referenceLabels[kind] }}</option></select></label></div>
          <p v-if="table.heterogeneous_views" class="capture">各原始视图列结构不同，按视图分别展示；不会补列、改列顺序或拼成同一张假原表。</p>
          <p v-if="table.reference_video_scope === 'requires_reference_video'" class="capture">本最低量表整体仅针对含参考视频场景；表内时长是参考条件，不代表当前用户提交的视频时长。</p>
          <p class="semantics">“官方估算”与“舍入展示”仅用于参考。最终视频用量以官方 completion_tokens 为准，不能从舍入金额反推最低 Token，也不把示例金额当成额外收费下限。</p>
          <div class="catalog-scroll" tabindex="0" aria-label="官方原始价格表，可横向滚动"><table><thead><tr><th scope="col">原始行</th><th v-for="(header, ci) in activeHeaders" :key="ci" scope="col">{{ header }}<small v-if="activeColumns[ci]?.unit">{{ unitLabels[activeColumns[ci]!.unit] ?? activeColumns[ci]!.unit }}</small><span v-if="activeColumns[ci]" class="column-kind" :class="activeColumns[ci]!.precision">{{ semanticLabels[activeColumns[ci]!.semantic] ?? '' }} {{ precisionLabels[activeColumns[ci]!.precision] }}</span></th></tr></thead><tbody><tr v-for="row in rows" :key="row.index"><th scope="row">{{ row.sourceRow }}<small v-if="row.source">{{ row.source.title }}</small></th><td v-for="(value, ci) in row.cells" :key="ci">{{ value === '' ? '（原表空白）' : value }}</td></tr><tr v-if="!rows.length"><td :colspan="activeHeaders.length + 1">没有匹配行。</td></tr></tbody></table></div>
          <nav class="catalog-pagination" aria-label="官方表分页"><button :disabled="page <= 1" @click="page--">上一页</button><span>{{ page }} / {{ pages }} 页</span><button :disabled="page >= pages" @click="page++">下一页</button></nav>
        </template>
        <div><button @click="selectedEvidence = 'table'">来源证据</button></div>
      </article>
    </template>
    <DetailDrawer :open="!!selectedEvidence" :title="selectedEvidence === 'manifest' ? '官方参考目录信息' : '官方来源证据'" size="md" description="只读证据；不会发布价格或修改计费配置。" @update:open="open => { if (!open) selectedEvidence = '' }">
      <div v-if="selectedEvidence === 'manifest' && catalog" class="catalog-proof"><p>{{ catalog.title }} · {{ catalog.version }}</p><p>清单 SHA-256</p><code>{{ catalog.manifest_sha256 }}</code><p>摘要用于核对本次固化内容。来源由人工核验，摘要本身不证明官方真实性，也不表示数据持续自动更新。</p></div>
      <div v-if="selectedEvidence === 'table' && table" class="catalog-proof"><p>{{ table.title }}</p><p><a :href="table.source_url" target="_blank" rel="noopener noreferrer">查看官方来源 ↗</a></p><dl><dt>官方表 / 视图</dt><dd>{{ table.table_id }} / {{ table.view_id ?? (table.captures?.length ? '多个独立视图' : '未知') }}</dd><dt v-if="!table.captures?.length">原始 TSV SHA-256</dt><dd v-if="!table.captures?.length"><code>{{ table.raw_sha256 || '尚无已核验原始表' }}</code></dd><dt>最近采集时间（UTC）</dt><dd>{{ table.captured_at ?? '尚未采集' }}</dd><dt>原始行数</dt><dd>{{ table.expected_rows ?? 0 }}</dd></dl><div v-for="capture in table.captures" :key="capture.view_id"><p><a :href="capture.source_url" target="_blank" rel="noopener noreferrer">{{ capture.title }} ↗</a> · {{ capture.view_id }} · {{ capture.expected_rows }} 行 · {{ capture.captured_at }}</p><code>{{ capture.raw_sha256 }}</code></div></div>
    </DetailDrawer>
  </section>
</template>

<style scoped>
.official-catalog{display:grid;gap:18px;color:rgb(var(--c-ink));min-width:0}.catalog-heading,.source-title{display:flex;justify-content:space-between;align-items:flex-start;gap:18px}h2,h3,p{margin:0}h2{font-size:20px}h3{font-size:17px}p{line-height:1.6;color:rgb(var(--c-muted))}.catalog-heading p{margin-top:8px;max-width:860px}button,input,select{font:inherit;color:inherit;border:1px solid rgb(var(--c-line));border-radius:7px;background:rgb(var(--c-bg));padding:9px}button{cursor:pointer;white-space:nowrap}button:disabled{opacity:.5;cursor:not-allowed}a{color:rgb(var(--c-ink));text-decoration:underline}.catalog-summary{display:grid;grid-template-columns:1fr 1fr 1.4fr;gap:20px;padding:18px;border:1px solid rgb(var(--c-line));border-radius:9px;background:rgb(var(--c-bg))}.catalog-summary div{display:grid;gap:6px}.catalog-summary small,.catalog-summary span,.capture{font-size:13px;color:rgb(var(--c-muted))}.catalog-filter{display:flex;align-items:end;gap:16px;flex-wrap:wrap}.catalog-filter label{display:grid;gap:7px;min-width:150px;font-size:13px}.catalog-filter .search{flex:1;min-width:220px}.catalog-table{display:grid;gap:14px;min-width:0}.catalog-missing,.semantics{padding:14px;border:1px solid rgb(var(--c-line));border-radius:8px;background:rgb(var(--c-bg))}.source-notes{margin:0;padding-left:22px;color:rgb(var(--c-muted));font-size:13px;line-height:1.7}.catalog-scroll{overflow:auto;max-height:650px;border:1px solid rgb(var(--c-line));border-radius:8px}table{width:100%;border-collapse:separate;border-spacing:0;font-size:13px}th,td{padding:12px;text-align:left;border-bottom:1px solid rgb(var(--c-line));white-space:nowrap}thead th{position:sticky;top:0;background:rgb(var(--c-panel));z-index:1}th small,.column-kind{display:block;font-size:11px;line-height:1.6;color:rgb(var(--c-muted));font-weight:400}.column-kind.estimated,.column-kind.rounded{font-weight:600;color:rgb(var(--c-ink))}.catalog-pagination{display:flex;justify-content:flex-end;align-items:center;gap:14px;font-size:13px}.catalog-proof{font-size:12px;color:rgb(var(--c-muted));min-width:0}.catalog-proof summary{cursor:pointer;padding:6px 0}.catalog-proof p{margin:8px 0}code,dd{overflow-wrap:anywhere}dd{margin:5px 0 12px}.catalog-error{border:1px solid currentColor;padding:14px;border-radius:8px;color:#b42318}input:focus-visible,select:focus-visible,button:focus-visible,.catalog-scroll:focus-visible{outline:2px solid #355fe5;outline-offset:2px}@media(max-width:800px){.catalog-heading,.source-title{flex-direction:column}.catalog-summary{grid-template-columns:1fr}.catalog-filter label{width:100%}.catalog-pagination{justify-content:center}}
</style>
