<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import DetailDrawer from '@/components/DetailDrawer.vue'
import { useAuth } from '@/stores/auth'
import { api, errorMessage, uuid } from '@/lib/api'
interface Movement { id: number; operation_id: number; type: string; direction: string; status: string; currency: string; amount: string; posted_delta: string; held_delta: string; available_delta: string; ref_type: string; ref_id: string; quality: string; occurred_at: string | null; original_movement_id?: number; original_journal_id?: number; original_hold_id?: number; correction_proposal_id?: number; correction_review_id?: number; created_at: string; journal_id?: number; hold_id?: number }
interface Operation { id: number; type: string; direction: string; requested_amount: string; currency: string; status: string; ref_id: string; failure_code?: string; created_at: string }
interface Summary { posted_balance: string; held_amount: string; available_balance: string; movements: { direction: string; amount: string }[]; history_scope: string; net_change_usd: string; refund_usd: string; net_usage_usd: string }
interface Detail { operation: Operation; movements: Movement[]; related_movements: Movement[]; related_has_more: boolean; events: { id: number; status: string; code: string; created_at: string }[] }
const auth = useAuth()
const selectedOperation = ref<number | null>(null), detailLoading = ref(false), detailError = ref('')
let detailEpoch = 0, listEpoch = 0
function closeDetail() { detailEpoch++; selectedOperation.value = null; detail.value = null; detailError.value = ''; detailLoading.value = false }
const keyword = ref(''), refType = ref(''), failureCode = ref('')
const view = ref('movements'), direction = ref(''), status = ref(''), type = ref(''), refID = ref(''), from = ref(''), until = ref('')
const movements = ref<Movement[]>([]), operations = ref<Operation[]>([]), summary = ref<Summary | null>(null), detail = ref<Detail | null>(null)
const exportJob = ref<{ id: number; status: string; row_count: number; expires_at: string; error_code?: string } | null>(null)
const loading = ref(false), exporting = ref(false), error = ref(''), notice = ref(''), next = ref(0)
const directionLabels: Record<string, string> = { credit: '入账', debit: '扣费', freeze: '冻结', unfreeze: '解冻' }
const statusLabels: Record<string, string> = { success: '成功', failed: '失败', processing: '处理中', reversed: '冲正' }
const typeLabels: Record<string, string> = { topup: '充值', grant: '赠送', admin_credit: '管理入账', admin_debit: '管理扣减', admin_adjustment: '管理调整', video_adjustment: '视频账单调整', video_refund: '视频退款', video_debit_adjustment: '视频补扣', reservation: '用量预冻结', reservation_release: '历史预冻结释放', remainder_release: '差额解冻', full_release: '全额解冻', usage: '实际用量', other_credit: '其他入账', other_debit: '其他扣减', expiry: '额度到期' }
const title = computed(() => view.value === 'movements' ? '已提交的资金变动' : '资金操作记录')
function query(before = 0) { const p = new URLSearchParams({ view: view.value }); for (const [k, v] of Object.entries({ direction: direction.value, status: status.value, type: type.value, ref_id: refID.value, keyword: keyword.value, ref_type: refType.value, failure_code: view.value === 'operations' ? failureCode.value : '' })) if (v) p.set(k, v); if (from.value) p.set('from', new Date(from.value).toISOString()); if (until.value) p.set('until', new Date(until.value).toISOString()); if (before) p.set('before', String(before)); return p.toString() }
async function load(more = false) {
  const current = ++listEpoch, org = auth.orgId, targetView = view.value
  loading.value = true; error.value = ''; closeDetail()
  try {
    const data = await api<{ items: Movement[] | Operation[]; next_before: number }>('GET', `/api/fund-activities?${query(more ? next.value : 0)}`)
    if (current !== listEpoch || org !== auth.orgId) return
    if (targetView === 'movements') movements.value = more ? [...movements.value, ...(data.items as Movement[])] : data.items as Movement[]
    else operations.value = more ? [...operations.value, ...(data.items as Operation[])] : data.items as Operation[]
    next.value = data.next_before
    if (!more) {
      const value = await api<Summary>('GET', `/api/fund-summary?${query()}`)
      if (current === listEpoch && org === auth.orgId) summary.value = value
    }
  } catch (e) { if (current === listEpoch && org === auth.orgId) error.value = errorMessage(e, '无法读取资金记录') }
  finally { if (current === listEpoch) loading.value = false }
}
async function show(id: number) {
  closeDetail(); selectedOperation.value = id; detailLoading.value = true
  const current = detailEpoch, org = auth.orgId
  try {
    const value = await api<Detail>('GET', `/api/fund-activities/${id}`)
    if (current === detailEpoch && org === auth.orgId) detail.value = value
  } catch (e) { if (current === detailEpoch && org === auth.orgId) detailError.value = errorMessage(e, '记录不存在或无访问权限') }
  finally { if (current === detailEpoch) detailLoading.value = false }
}
async function download() {
  exporting.value = true; error.value = ''; notice.value = ''
  try {
    const filters = { direction: direction.value, status: status.value, type: type.value, ref_id: refID.value, keyword: keyword.value, ref_type: refType.value, from: from.value ? new Date(from.value).toISOString() : '', until: until.value ? new Date(until.value).toISOString() : '' }
    exportJob.value = await api('POST', '/api/fund-exports', filters, { headers: { 'Idempotency-Key': uuid() } })
    notice.value = '导出已排队，固定当前流水快照。可点击刷新状态；文件 15 分钟后失效。'
  } catch (e) { error.value = errorMessage(e, '导出创建失败') } finally { exporting.value = false }
}
async function refreshExport() {
  if (!exportJob.value) return
  exporting.value = true; error.value = ''
  try { exportJob.value = await api('GET', `/api/fund-exports/${exportJob.value.id}`) }
  catch (e) { error.value = errorMessage(e, '导出不存在或已过期') } finally { exporting.value = false }
}
async function downloadReady() {
  if (!exportJob.value || exportJob.value.status !== 'success') return
  exporting.value = true; error.value = ''
  try {
    const data = await api<{ csv: string; filename: string; rows: number }>('GET', `/api/fund-exports/${exportJob.value.id}/download`)
    const url = URL.createObjectURL(new Blob(['\uFEFF', data.csv], { type: 'text/csv;charset=utf-8' })); const a = document.createElement('a'); a.href = url; a.download = data.filename; a.click(); URL.revokeObjectURL(url)
    notice.value = `已下载 ${data.rows} 条快照资金变动。`
  } catch (e) { error.value = errorMessage(e, '下载失败') } finally { exporting.value = false }
}
watch(() => auth.orgId, () => { closeDetail(); movements.value = []; operations.value = []; summary.value = null; exportJob.value = null; notice.value = ''; void load() })
onBeforeUnmount(() => { listEpoch++; closeDetail() })
onMounted(() => load())
</script>

<template>
  <main class="fund-page"><header><h1>资金流水</h1><p>冻结仅预留可用额度，实际扣费才是消费。全部金额以 USD 展示。</p></header>
    <p v-if="error" role="alert" class="message error">{{ error }}</p><p v-if="notice" role="status" class="message">{{ notice }}</p>
    <section v-if="summary" class="balances"><article><span>当前账面余额</span><strong>${{ summary.posted_balance }}</strong></article><article><span>当前冻结</span><strong>${{ summary.held_amount }}</strong></article><article><span>当前可用</span><strong>${{ summary.available_balance }}</strong></article></section>
    <section v-if="exportJob" class="panel"><strong>导出 #{{ exportJob.id }} · {{ exportJob.status }} · {{ exportJob.row_count }} 条</strong><p>有效期至 {{ exportJob.expires_at }}。{{ exportJob.error_code }}</p><button :disabled="exporting" @click="refreshExport">刷新导出状态</button> <button v-if="exportJob.status === 'success'" :disabled="exporting" @click="downloadReady">下载完整 CSV</button></section><section class="panel"><div class="tabs" role="group" aria-label="记录类型"><button :class="{ active: view === 'movements' }" :disabled="loading" @click="view = 'movements'; load()">资金变动</button><button :class="{ active: view === 'operations' }" :disabled="loading" @click="view = 'operations'; load()">操作状态</button></div>
      <form class="filters" @submit.prevent="load()"><label>方向<select v-model="direction"><option value="">全部</option><option v-for="(label, value) in directionLabels" :key="value" :value="value">{{ label }}</option></select></label><label>状态<select v-model="status"><option value="">全部</option><option v-for="(label, value) in statusLabels" :key="value" :value="value">{{ label }}</option></select></label><label>类型<select v-model="type"><option value="">全部</option><option v-for="(label, value) in typeLabels" :key="value" :value="value">{{ label }}</option></select></label><label>开始时间<input v-model="from" type="datetime-local"></label><label>结束时间（不含）<input v-model="until" type="datetime-local"></label><label>业务关联 ID<input v-model="refID" placeholder="任务或订单 ID"></label><label>关联 ID 关键词<input v-model="keyword" placeholder="部分 ID"></label><label>关联类型<input v-model="refType" placeholder="video / topup"></label><label v-if="view === 'operations'">安全错误码<input v-model="failureCode" placeholder="insufficient_balance"></label><button type="submit" :disabled="loading">查询</button><button type="button" :disabled="exporting || loading || view !== 'movements'" @click="download">导出资金变动</button></form>
      <p class="hint">余额卡片是当前所有账户分片合计；下方汇总只包含筛选范围的真实资金变动。旧系统上线前历史可能尚未回填。</p>
      <div v-if="summary" class="totals"><span>净账面变化 <strong>${{ summary.net_change_usd }}</strong></span><span>退款 <strong>${{ summary.refund_usd }}</strong></span><span>净用量扣费 <strong>${{ summary.net_usage_usd }}</strong></span><span v-for="s in summary.movements" :key="s.direction">{{ directionLabels[s.direction] }}合计 <strong>${{ s.amount }}</strong></span></div>
    </section>
    <section class="panel"><h2>{{ title }}</h2><p v-if="view === 'operations'">申请金额表示操作意图。失败或处理中不计作资金变动；请打开详情查看实际已提交明细。</p><p v-if="loading" aria-live="polite">正在查询…</p>
      <div class="table-scroll"><table v-if="view === 'movements'"><thead><tr><th>时间 / 类型</th><th>方向</th><th>金额</th><th>账面变化</th><th>冻结变化</th><th>可用变化</th><th>状态 / 来源</th><th>关联 / 详情</th></tr></thead><tbody><tr v-for="r in movements" :key="r.id"><td>{{ r.occurred_at || '发生时间未知' }}<small>{{ typeLabels[r.type] || r.type }}</small></td><td>{{ directionLabels[r.direction] }}</td><td>${{ r.amount }}</td><td>{{ r.posted_delta }}</td><td>{{ r.held_delta }}</td><td>{{ r.available_delta }}</td><td>{{ statusLabels[r.status] || r.status }}<small>{{ r.quality === 'live' ? '实时落库' : r.quality }}</small></td><td><button class="link" @click="show(r.operation_id)">{{ r.ref_id || `操作 #${r.operation_id}` }}</button></td></tr><tr v-if="!movements.length && !loading"><td colspan="8" class="empty">当前筛选范围没有资金变动</td></tr></tbody></table>
      <table v-else><thead><tr><th>时间 / 类型</th><th>意图方向</th><th>申请金额</th><th>状态</th><th>结果 / 关联</th><th>详情</th></tr></thead><tbody><tr v-for="r in operations" :key="r.id"><td>{{ r.created_at }}<small>{{ typeLabels[r.type] || r.type }}</small></td><td>{{ directionLabels[r.direction] }}</td><td>${{ r.requested_amount }}</td><td>{{ statusLabels[r.status] || r.status }}</td><td>{{ r.failure_code || r.ref_id || '—' }}</td><td><button @click="show(r.id)">查看 #{{ r.id }}</button></td></tr><tr v-if="!operations.length && !loading"><td colspan="6" class="empty">当前筛选范围没有操作记录</td></tr></tbody></table></div>
      <button v-if="next" class="more" :disabled="loading" @click="load(true)">加载更早记录</button>
    </section>
    <DetailDrawer :open="selectedOperation !== null" :title="`资金操作 #${selectedOperation ?? ''}`" size="lg" @update:open="open => { if (!open) closeDetail() }"><p v-if="detailLoading" role="status">正在读取详情…</p><p v-if="detailError" class="text-danger" role="alert">{{ detailError }}</p><section v-if="detail"><h2>{{ statusLabels[detail.operation.status] || detail.operation.status }}</h2><p>关联：{{ detail.operation.ref_id || '—' }} · 申请 ${{ detail.operation.requested_amount }}</p><ol><li v-for="event in detail.events" :key="event.id">{{ event.created_at }} · {{ statusLabels[event.status] || event.status }} {{ event.code }}</li></ol><p v-if="!detail.movements.length">没有已提交资金变动。该操作不计入扣费汇总。</p><p v-if="detail.related_movements.length">同一业务关联的冻结、扣费、解冻和调整（最多 100 条）</p><div v-for="m in (detail.related_movements.length ? detail.related_movements : detail.movements)" :key="m.id" class="movement-detail"><strong>{{ directionLabels[m.direction] }} ${{ m.amount }}</strong><span>账面 {{ m.posted_delta }} / 冻结 {{ m.held_delta }} / 可用 {{ m.available_delta }}</span><small>记账凭证 {{ m.journal_id || '无（冻结或解冻）' }} · 冻结单 {{ m.hold_id || '无' }} · {{ m.quality }}</small><small v-if="m.correction_proposal_id">纠正原冻结单 #{{ m.original_hold_id }} / 原凭证 #{{ m.original_journal_id || '无' }} · 提案 #{{ m.correction_proposal_id }} / 审核 #{{ m.correction_review_id }}。原记录保留，此项为已审核的退款或补扣。</small></div></section></DetailDrawer>
  </main>
</template>

<style scoped>
.fund-page{max-width:1450px;margin:0 auto;padding:28px;display:grid;gap:20px;color:rgb(var(--c-ink))}h1{font-size:26px;margin:0 0 10px}h2{font-size:18px;margin:0 0 14px}p,small,.hint{color:rgb(var(--c-muted));line-height:1.6}small{display:block;font-size:12px}.balances{display:grid;grid-template-columns:repeat(3,1fr);gap:18px}.balances article,.panel{border:1px solid rgb(var(--c-line));border-radius:12px;padding:22px;background:rgb(var(--c-panel));min-width:0}.balances span{display:block;font-size:13px;color:rgb(var(--c-muted))}.balances strong{display:block;font-size:26px;margin-top:12px;overflow-wrap:anywhere}.tabs{display:flex;gap:8px;margin-bottom:22px}.tabs .active{background:#355fe5;border-color:#355fe5;color:white}.filters{display:flex;flex-wrap:wrap;align-items:end;gap:14px}.filters label{display:grid;gap:7px;font-size:13px}input,select,button{font:inherit;color:inherit;background:rgb(var(--c-bg));border:1px solid rgb(var(--c-line));border-radius:6px;padding:9px}button{cursor:pointer}button:disabled{opacity:.5;cursor:not-allowed}.totals{display:flex;gap:24px;flex-wrap:wrap;font-size:14px}.totals strong{margin-left:8px}.table-scroll{overflow-x:auto}table{width:100%;border-collapse:collapse;font-size:13px}th,td{text-align:left;border-bottom:1px solid rgb(var(--c-line));padding:13px 9px;vertical-align:top}th{white-space:nowrap;color:rgb(var(--c-muted))}td{overflow-wrap:anywhere}.link{color:rgb(var(--c-ink));max-width:260px;overflow-wrap:anywhere}.empty{text-align:center;padding:40px}.more{margin-top:18px}.message{padding:14px;border-radius:8px;background:#153f34;margin:0}.message.error{background:#52202a;color:#ffd9e0}.detail-heading{display:flex;justify-content:space-between;gap:12px}.movement-detail{display:grid;gap:7px;padding:14px 0;border-top:1px solid rgb(var(--c-line))}li{margin:9px 0;font-size:13px}@media(max-width:700px){.fund-page{padding:16px}.balances{grid-template-columns:1fr}.balances article,.panel{padding:16px}.filters label{flex:1;min-width:150px}.balances strong{font-size:23px}}
</style>
