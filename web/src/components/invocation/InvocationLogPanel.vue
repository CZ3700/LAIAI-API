<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import DetailDrawer from '@/components/DetailDrawer.vue'
import { useInvocationLogs } from '@/composables/useInvocationLogs'
import { ApiError, copyText } from '@/lib/api'
import { formatDisplayMoney, convertUsdToDisplay } from '@/lib/decimalMoney'
import { InvocationFilterError, formatInvocationTokens, invocationTotalTokens, invocationModalities, invocationStatuses, invocationTimezones, type InvocationPageSize, type InvocationScope } from '@/lib/invocationQuery'
import type { InvocationRow } from '@/lib/invocationQueryTransport'

const props = withDefaults(defineProps<{ scope: InvocationScope; active?: boolean }>(), { active: true })
const { t, locale } = useI18n()
const { draft, applied, bundle, page, pageSize, loading, error, exporting, exportResult, exportError, selectedRow, allowed, dirty,
  apply, reset, refresh, setPage, setPageSize, exportCSV, cancelExport, openDetail, closeDetail } = useInvocationLogs({ scope: () => props.scope, active: () => props.active })
const copied = ref('')
const money = computed(() => bundle.value?.query.display_money)
const timezone = computed(() => applied.value?.timezone ?? draft.timezone)
const first = computed(() => bundle.value?.items.length ? (bundle.value.page - 1) * bundle.value.page_size + 1 : 0)
const last = computed(() => bundle.value?.items.length ? first.value + bundle.value.items.length - 1 : 0)
const hasNext = computed(() => !!bundle.value && page.value * pageSize.value < bundle.value.total && page.value * pageSize.value <= 100000)
const chips = computed(() => {
  const f = applied.value
  if (!f) return []
  const out = [`${f.since} ≤ ${t('invocationLogs.recordedTime')} < ${f.until} · ${f.timezone}`]
  if (f.model) out.push(`${t('reqlog.filterModel')}: ${f.model}`)
  if (f.request_id) out.push(`${t('reqlog.filterRequestId')}: ${f.request_id}`)
  if (f.api_key_id) out.push(`${t('reqlog.filterApiKey')}: #${f.api_key_id}`)
  if (props.scope === 'admin' && f.org_id) out.push(`${t('admin.lgOrg')}: #${f.org_id}`)
  if (props.scope === 'admin' && f.channel_id) out.push(`${t('admin.lgChannel')}: #${f.channel_id}`)
  if (f.modality !== undefined) out.push(`${t('reqlog.filterModality')}: ${f.modality || t('invocationLogs.historical')}`)
  if (f.result) out.push(f.result === 'failed' ? t('invocationLogs.nonSuccess') : t('reqlog.resultSuccess'))
  if (f.status) out.push(f.status)
  if (f.is_stream !== undefined) out.push(`${t('reqlog.dtStream')}: ${t(f.is_stream ? 'common.yes' : 'common.no')}`)
  return out
})
function recordedAt(value: string): string {
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) return '—'
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'short', timeStyle: 'medium', timeZone: timezone.value }).format(date)
}
function rowAmount(row: InvocationRow): string {
  const fx = money.value
  return fx ? formatDisplayMoney(row.cost_usd, { currency: fx.currency, rate: fx.cny_per_usd, ready: true }) : '—'
}
function exactAmount(row: InvocationRow): string {
  const fx = money.value
  return fx ? `${convertUsdToDisplay(row.cost_usd, fx.currency, fx.cny_per_usd).amount} ${fx.currency}` : '—'
}
function tokenTotal(row: InvocationRow): string { return formatInvocationTokens(invocationTotalTokens(row.usage_exact.prompt_tokens, row.usage_exact.completion_tokens)) }
function failure(e: unknown): string {
  if (e instanceof InvocationFilterError) return t('invocationLogs.invalidFilter')
  if (e instanceof ApiError) {
    const key = e.status === 401 || e.status === 403 ? 'permission' : e.status === 429 ? 'busy' : e.code === 'export_limit_exceeded' || e.code === 'export_size_exceeded' ? 'exportLimit' : 'unavailable'
    const message = e.status === 429 && e.retryAfter !== null && /^(0|[1-9]\d{0,5})$/.test(e.retryAfter)
      ? t('invocationLogs.busyRetry', { seconds: e.retryAfter }) : t(`invocationLogs.${key}`)
    return `${message}${e.requestId ? ` (ID: ${e.requestId})` : ''}`
  }
  return t('invocationLogs.unavailable')
}
function chooseResult() { if (draft.result !== 'all') draft.status = '' }
function chooseStatus() { if (draft.status) draft.result = 'all' }
function changePageSize(event: Event) { const value = (event.target as HTMLSelectElement).value; setPageSize((value === '100' ? 100 : value === '50' ? 50 : 20) as InvocationPageSize) }
async function copy(value: string) { copied.value = await copyText(value) ? t('common.copied') : t('common.copyFailed') }
</script>

<template>
  <section class="invocation-workspace" :aria-label="t('reqlog.title')" :aria-busy="loading">
    <header class="invocation-heading">
      <div><h2>{{ t(scope === 'admin' ? 'invocationLogs.platform' : 'invocationLogs.organization') }}</h2><p class="text-muted text-xs">{{ t('invocationLogs.notice') }}</p></div>
      <div class="invocation-actions"><span v-if="money" class="badge-muted">{{ money.currency }}</span><button type="button" class="btn-ghost" :disabled="!allowed || loading" @click="refresh">{{ t('common.refresh') }}</button><button type="button" class="btn-primary" :disabled="!allowed || !applied || exporting || loading" @click="exportCSV">{{ t('invocationLogs.exportApplied') }}</button><button v-if="exporting" type="button" class="btn-ghost" @click="cancelExport">{{ t('common.cancel') }}</button></div>
    </header>
    <p v-if="!allowed" role="alert" class="inline-alert">{{ t('invocationLogs.permission') }}</p>
    <form v-if="allowed" class="app-panel invocation-filters" @submit.prevent="apply()">
      <label class="filter-group"><span>{{ t('reqlog.filterTimeRange') }}</span><select v-model="draft.range" class="input"><option value="7d">{{ t('reqlog.range7d') }}</option><option value="30d">{{ t('reqlog.range30d') }}</option><option value="month">{{ t('reqlog.rangeMonth') }}</option><option value="prev">{{ t('reqlog.rangePrev') }}</option><option value="custom">{{ t('reqlog.rangeCustom') }}</option></select></label>
      <label class="filter-group"><span>{{ t('invocationLogs.timezone') }}</span><select v-model="draft.timezone" class="input"><option v-for="zone in invocationTimezones" :key="zone" :value="zone">{{ zone }}</option></select></label>
      <template v-if="draft.range === 'custom'"><label class="filter-group"><span>{{ t('reqlog.startDate') }}</span><input v-model="draft.start" class="input" type="date" /></label><label class="filter-group"><span>{{ t('invocationLogs.inclusiveEnd') }}</span><input v-model="draft.end" class="input" type="date" /></label></template>
      <label class="filter-group"><span>{{ t('reqlog.filterModel') }}</span><input v-model="draft.model" class="input" maxlength="256" :placeholder="t('invocationLogs.exactMatch')" /></label>
      <label class="filter-group"><span>{{ t('reqlog.filterRequestId') }}</span><input v-model="draft.requestId" class="input" maxlength="64" autocomplete="off" :placeholder="t('invocationLogs.exactMatch')" /></label>
      <label class="filter-group"><span>{{ t('reqlog.filterApiKey') }} ID</span><input v-model="draft.apiKeyId" class="input" inputmode="numeric" maxlength="20" :placeholder="t('reqlog.allKeys')" /></label>
      <label class="filter-group"><span>{{ t('reqlog.filterResult') }}</span><select v-model="draft.result" class="input" @change="chooseResult"><option value="all">{{ t('reqlog.resultAll') }}</option><option value="success">{{ t('reqlog.resultSuccess') }}</option><option value="failed">{{ t('invocationLogs.nonSuccess') }}</option></select></label>
      <label class="filter-group"><span>{{ t('reqlog.colStatus') }}</span><select v-model="draft.status" class="input" @change="chooseStatus"><option value="">{{ t('reqlog.resultAll') }}</option><option v-for="status in invocationStatuses" :key="status" :value="status">{{ status }}</option></select></label>
      <label class="filter-group"><span>{{ t('reqlog.filterModality') }}</span><select v-model="draft.modality" class="input"><option value="all">{{ t('reqlog.modAll') }}</option><option value="historical">{{ t('invocationLogs.historical') }}</option><option v-for="kind in invocationModalities" :key="kind" :value="kind">{{ kind }}</option></select></label>
      <label class="filter-group"><span>{{ t('reqlog.dtStream') }}</span><select v-model="draft.stream" class="input"><option value="all">{{ t('reqlog.resultAll') }}</option><option value="true">{{ t('common.yes') }}</option><option value="false">{{ t('common.no') }}</option></select></label>
      <template v-if="scope === 'admin'"><label class="filter-group"><span>{{ t('admin.lgOrg') }} ID</span><input v-model="draft.orgId" class="input" inputmode="numeric" maxlength="20" /></label><label class="filter-group"><span>{{ t('admin.lgChannel') }} ID</span><input v-model="draft.channelId" class="input" inputmode="numeric" maxlength="20" /></label></template>
      <div class="invocation-actions filter-buttons"><button type="submit" class="btn-primary" :disabled="loading">{{ t('reqlog.btnQuery') }}</button><button type="button" class="btn-ghost" @click="reset">{{ t('invocationLogs.reset') }}</button></div>
    </form>
    <div v-if="applied" class="invocation-chips" :aria-label="t('invocationLogs.applied')"><strong>{{ t('invocationLogs.applied') }}</strong><span v-for="chip in chips" :key="chip" class="badge-muted">{{ chip }}</span></div>
    <p v-if="dirty" class="text-muted text-xs">{{ t('invocationLogs.dirty') }}</p>
    <p v-if="error" class="inline-alert" role="alert">{{ failure(error) }}</p>
    <p v-if="exportError" class="inline-alert" role="alert">{{ failure(exportError) }}</p>
    <p v-if="exporting" role="status" class="text-muted text-sm">{{ t('invocationLogs.exporting') }}</p>
    <div v-if="exportResult" class="app-panel p-3 text-sm" role="status"><p>{{ t('invocationLogs.exportDone', { count: exportResult.recordCount }) }}</p><p class="text-xs text-muted">{{ t('invocationLogs.exportObservation') }} {{ exportResult.asOf }} · {{ exportResult.money.currency }} · 1 USD = {{ exportResult.money.cny_per_usd }} CNY · {{ exportResult.costDisplay }} {{ exportResult.money.currency }}</p></div>
    <p v-if="loading" role="status" class="invocation-loading text-muted">{{ t('reqlog.loading') }}</p>
    <template v-else-if="bundle">
      <dl class="invocation-summary">
        <div><dt>{{ t('invocationLogs.requests') }}</dt><dd>{{ bundle.totals.record_count }}</dd></div><div><dt>{{ t('reqlog.resultSuccess') }} / {{ t('invocationLogs.nonSuccess') }}</dt><dd>{{ bundle.totals.success_count }} / {{ bundle.totals.non_success_count }}</dd></div>
        <div><dt>{{ t('reqlog.dtInputTokens') }}</dt><dd>{{ formatInvocationTokens(bundle.totals.prompt_tokens) }}</dd></div><div><dt>{{ t('reqlog.dtOutputTokens') }}</dt><dd>{{ formatInvocationTokens(bundle.totals.completion_tokens) }}</dd></div>
        <div class="summary-amount"><dt>{{ t('invocationLogs.logAmount') }}</dt><dd class="select-all">{{ bundle.totals.cost_display }} <small>{{ money?.currency }}</small></dd></div>
      </dl>
      <p class="text-xs text-muted">{{ t('invocationLogs.asOf') }} {{ recordedAt(bundle.query.as_of) }} · {{ timezone }} · {{ t('invocationLogs.fx') }} 1 USD = {{ money?.cny_per_usd }} CNY · {{ t(money?.rate_source === 'setting' ? 'invocationLogs.settingRate' : 'invocationLogs.defaultRate') }}</p>
      <details class="invocation-method"><summary>{{ t('invocationLogs.method') }}</summary><p>{{ t('invocationLogs.usageNotice') }}</p><p>{{ t('reqlog.dtCacheReadTokens') }}: {{ formatInvocationTokens(bundle.totals.cached_tokens) }} · {{ t('reqlog.dtReasoningTokens') }}: {{ formatInvocationTokens(bundle.totals.reasoning_tokens) }}</p><p>{{ t('invocationLogs.fx') }}: {{ money?.rate_snapshot_at }}</p><p class="select-all break-all">filter_hash: {{ bundle.query.filter_hash }}</p><pre>{{ JSON.stringify(bundle.query.normalized_filters, null, 2) }}</pre></details>
      <div class="data-table-wrap"><table class="table invocation-table"><thead><tr><th>{{ t('reqlog.colRequestId') }}</th><th>{{ t('invocationLogs.recordedTime') }} ({{ timezone }})</th><th>{{ t('reqlog.colModel') }}</th><th v-if="scope === 'admin'">{{ t('admin.lgOrg') }} / {{ t('admin.lgChannel') }}</th><th>{{ t('reqlog.colTokens') }}</th><th>{{ t('reqlog.colCost', { currency: money?.currency }) }}</th><th>{{ t('reqlog.colLatency') }}</th><th>{{ t('reqlog.colStatus') }}</th></tr></thead>
      <tbody><tr v-if="!bundle.items.length"><td :colspan="scope === 'admin' ? 8 : 7" class="invocation-empty">{{ t('reqlog.emptyData') }}</td></tr><tr v-for="row in bundle.items" :key="row.id"><td><button type="button" class="table-link invocation-request" :title="row.request_id" @click="openDetail(row)">{{ row.request_id }}</button></td><td>{{ recordedAt(row.created_at) }}</td><td class="invocation-model">{{ row.model }}</td><td v-if="scope === 'admin'">#{{ row.org_id }} / #{{ row.channel_id }}<br />{{ row.provider }}</td><td class="font-mono select-all">{{ tokenTotal(row) }}</td><td class="font-mono" :title="exactAmount(row)">{{ rowAmount(row) }}</td><td>{{ row.duration_ms }} ms</td><td><span :class="row.status === 'success' ? 'badge-ok' : 'badge-danger'">{{ row.status }}</span></td></tr></tbody></table></div>
      <footer class="invocation-pagination"><span>{{ t('invocationLogs.pagination', { first, last, total: bundle.total }) }}</span><div class="invocation-actions"><label>{{ t('invocationLogs.pageSize') }} <select class="input page-size" :value="pageSize" @change="changePageSize"><option v-for="size in [20,50,100]" :key="size" :value="size">{{ size }}</option></select></label><button type="button" class="btn-ghost" :disabled="page <= 1" @click="setPage(page - 1)">{{ t('common.prev') }}</button><span>{{ t('common.page', { page }) }}</span><button type="button" class="btn-ghost" :disabled="!hasNext" @click="setPage(page + 1)">{{ t('common.next') }}</button></div></footer>
    </template>
    <DetailDrawer :open="!!selectedRow && !!bundle && allowed" :title="t('reqlog.drawerTitle', { id: selectedRow?.id ?? '' })" size="lg" @update:open="value => { if (!value) closeDetail() }">
      <div v-if="selectedRow && bundle" class="space-y-5">
        <p class="text-xs text-muted">{{ t('invocationLogs.notice') }}</p>
        <section class="drawer-section"><h3>{{ t('reqlog.sectionIdentity') }}</h3><dl class="detail-grid"><div class="sm:col-span-2"><dt>{{ t('reqlog.dtRequestId') }}</dt><dd class="select-all break-all">{{ selectedRow.request_id }} <button type="button" class="table-link" @click="copy(selectedRow.request_id)">{{ t('common.copy') }}</button></dd></div><div><dt>{{ t('invocationLogs.recordedTime') }}</dt><dd>{{ recordedAt(selectedRow.created_at) }} · {{ timezone }}</dd></div><div><dt>{{ t('reqlog.dtModel') }}</dt><dd>{{ selectedRow.model }}</dd></div><div><dt>{{ t('reqlog.filterApiKey') }}</dt><dd>{{ selectedRow.api_key_id ? `#${selectedRow.api_key_id}` : '—' }}</dd></div><div><dt>{{ t('reqlog.filterModality') }}</dt><dd>{{ selectedRow.modality || t('invocationLogs.historical') }}</dd></div><template v-if="scope === 'admin'"><div><dt>{{ t('admin.lgOrg') }}</dt><dd>#{{ selectedRow.org_id }}</dd></div><div><dt>{{ t('admin.lgChannel') }}</dt><dd>#{{ selectedRow.channel_id }} · {{ selectedRow.provider }}</dd></div><div><dt>{{ t('admin.lgUser') }}</dt><dd>{{ selectedRow.user_id ?? '—' }}</dd></div><div><dt>{{ t('admin.lgAttempts') }}</dt><dd>{{ selectedRow.attempts }}</dd></div><div class="sm:col-span-2"><dt>{{ t('admin.lgUpstreamId') }}</dt><dd class="select-all break-all">{{ selectedRow.upstream_request_id || '—' }}</dd></div></template></dl></section>
        <section class="drawer-section"><h3>{{ t('reqlog.sectionUsage') }}</h3><p class="text-xs text-muted">{{ t('invocationLogs.usageNotice') }}</p><dl class="detail-grid"><div><dt>{{ t('reqlog.dtInputTokens') }}</dt><dd class="select-all">{{ formatInvocationTokens(selectedRow.usage_exact.prompt_tokens) }}</dd></div><div><dt>{{ t('reqlog.dtOutputTokens') }}</dt><dd class="select-all">{{ formatInvocationTokens(selectedRow.usage_exact.completion_tokens) }}</dd></div><div><dt>{{ t('reqlog.dtCacheReadTokens') }}</dt><dd>{{ formatInvocationTokens(selectedRow.usage_exact.cached_tokens) }}</dd></div><div><dt>{{ t('reqlog.dtReasoningTokens') }}</dt><dd>{{ formatInvocationTokens(selectedRow.usage_exact.reasoning_tokens) }}</dd></div><div><dt>{{ t('reqlog.colCost', { currency: money?.currency }) }}</dt><dd class="select-all">{{ exactAmount(selectedRow) }} <button type="button" class="table-link" @click="copy(exactAmount(selectedRow))">{{ t('common.copy') }}</button></dd></div><div><dt>{{ t('invocationLogs.fx') }}</dt><dd>1 USD = {{ money?.cny_per_usd }} CNY<br />{{ money?.rate_snapshot_at }}</dd></div></dl></section>
        <section class="drawer-section"><h3>{{ t('reqlog.sectionResult') }}</h3><dl class="detail-grid"><div><dt>{{ t('reqlog.dtStatus') }}</dt><dd>{{ selectedRow.status }} · {{ selectedRow.status_code }}</dd></div><div><dt>{{ t('reqlog.dtStream') }}</dt><dd>{{ t(selectedRow.is_stream ? 'common.yes' : 'common.no') }}</dd></div><div><dt>{{ t('reqlog.dtLatency') }}</dt><dd>{{ selectedRow.duration_ms }} ms</dd></div><div><dt>{{ t('reqlog.dtFirstToken') }}</dt><dd>{{ selectedRow.frt_ms }} ms</dd></div><div class="sm:col-span-2"><dt>{{ t('reqlog.dtErrorBrief') }}</dt><dd class="break-all">{{ selectedRow.error_brief || '—' }}</dd></div><div><dt>{{ t('reqlog.dtUsageSource') }}</dt><dd>{{ selectedRow.usage_source || '—' }}</dd></div></dl></section>
        <p role="status" class="text-xs text-muted">{{ copied }}</p>
      </div>
    </DetailDrawer>
  </section>
</template>

<style scoped>
.invocation-workspace{display:grid;gap:1rem;min-width:0}.invocation-heading,.invocation-pagination{display:flex;justify-content:space-between;align-items:center;gap:1rem;flex-wrap:wrap}.invocation-heading h2{font-size:1rem;font-weight:600;margin:0 0 .35rem}.invocation-actions{display:flex;gap:.5rem;flex-wrap:wrap;align-items:center}.invocation-actions button{min-height:2.25rem}.invocation-filters{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,12rem),1fr));gap:.85rem;padding:1rem}.filter-group{display:flex;min-width:0;gap:.35rem;flex-direction:column;font-size:.75rem;color:rgb(var(--c-muted))}.filter-group .input{width:100%;min-width:0;color:rgb(var(--c-ink))}.filter-buttons{align-self:end}.invocation-chips{display:flex;align-items:center;flex-wrap:wrap;gap:.4rem;font-size:.75rem}.invocation-chips .badge-muted{white-space:normal;overflow-wrap:anywhere}.invocation-summary{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,10rem),1fr));gap:.75rem}.invocation-summary>div{padding:1rem;border:1px solid rgb(var(--c-line));border-radius:.65rem;background:rgb(var(--c-panel));min-width:0}.invocation-summary dt{font-size:.75rem;color:rgb(var(--c-muted))}.invocation-summary dd{font-size:1.1rem;font-variant-numeric:tabular-nums;overflow-wrap:anywhere;margin-top:.5rem}.invocation-summary .summary-amount{grid-column:span 2}.invocation-method{border:1px solid rgb(var(--c-line));border-radius:.5rem;padding:.75rem;font-size:.75rem;color:rgb(var(--c-muted))}.invocation-method summary{cursor:pointer}.invocation-method p{margin-top:.5rem}.invocation-method pre{overflow:auto;max-height:14rem;margin-top:.75rem;white-space:pre-wrap}.invocation-table{width:100%;font-size:.8rem;white-space:nowrap}.invocation-table th,.invocation-table td{padding:.75rem;text-align:left;border-bottom:1px solid rgb(var(--c-line))}.invocation-table th{font-size:.7rem;color:rgb(var(--c-muted));font-weight:500}.invocation-request{max-width:16rem;overflow:hidden;text-overflow:ellipsis;display:block;font-family:monospace}.invocation-model{max-width:18rem;overflow-wrap:anywhere;white-space:normal}.invocation-empty{height:8rem;text-align:center!important;color:rgb(var(--c-muted))}.invocation-loading{padding:3rem;text-align:center}.invocation-pagination{font-size:.8rem;color:rgb(var(--c-muted))}.page-size{width:5rem;display:inline-block}.detail-grid dd{overflow-wrap:anywhere}.detail-grid{row-gap:1rem}@media(max-width:640px){.invocation-heading{align-items:flex-start}.invocation-actions{width:100%}.invocation-summary{grid-template-columns:repeat(2,minmax(0,1fr))}.invocation-summary dd{font-size:.95rem}.invocation-pagination .invocation-actions{gap:.35rem}.invocation-filters{padding:.75rem}}@media(max-width:360px){.invocation-summary{grid-template-columns:1fr}.invocation-summary .summary-amount{grid-column:auto}}
</style>
