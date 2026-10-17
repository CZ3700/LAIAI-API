<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api, errorMessage } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import DetailDrawer from '@/components/DetailDrawer.vue'
import HistoryJSON from './HistoryJSON.vue'
import MeteringFacts from './MeteringFacts.vue'
import { useSeedanceI18n } from './messages'
import type { BodyInfo, BodyRead, ExecutionRow, LogDetail, LogRow, Page } from './types'

const { t, locale } = useSeedanceI18n()
const auth = useAuth()
const route = useRoute()
const rows = ref<LogRow[]>([])
const loading = ref(false)
const error = ref('')
const filter = ref({ request_id: '', task_id: '', model: '', api_key_id: '', operation: 'create_task', http_status: '', task_status: '', billing_state: '', from: '', to: '' })
const cursors = ref<string[]>([''])
const next = ref<string | null>(null)
const detail = ref<LogDetail | null>(null)
const selected = ref('')
const tab = ref<'received' | 'result' | 'response'>('received')
const reason = ref('request_debugging')
const bodies = ref<Record<string, BodyRead>>({})
const bodyLoading = ref('')
const events = ref<ExecutionRow[]>([])
const attempts = ref<ExecutionRow[]>([])
const eventsNext = ref<string | null>(null)
const attemptsNext = ref<string | null>(null)
let epoch = 0
let listVersion = 0
function base() { return auth.isSuperadmin ? '/api/admin/video/request-logs' : '/api/video/request-logs' }
function url(path = '', query = new URLSearchParams()) { if (auth.isSuperadmin) query.set('org_id', String(auth.orgId)); return `${base()}${path}?${query}` }
function time(value: string | null) { return value ? new Intl.DateTimeFormat(locale.value, { dateStyle: 'short', timeStyle: 'medium' }).format(new Date(value)) : '—' }
function close() { epoch++; selected.value = ''; detail.value = null; bodies.value = {}; events.value = []; attempts.value = []; eventsNext.value = null; attemptsNext.value = null; bodyLoading.value = '' }
function reset() { close(); rows.value = []; cursors.value = ['']; next.value = null; error.value = ''; void load() }
async function load(restart = false) {
  if (restart) cursors.value = ['']
  const version = ++listVersion; const org = auth.orgId
  loading.value = true; error.value = ''
  try {
    const q = new URLSearchParams({ limit: '20', cursor: cursors.value[cursors.value.length - 1] })
    for (const [key, value] of Object.entries(filter.value)) if (value) q.set(key, key === 'from' || key === 'to' ? new Date(value).toISOString() : value.trim())
    const page = await api<Page<LogRow>>('GET', url('', q))
    if (version !== listVersion || org !== auth.orgId) return
    rows.value = page.data; next.value = page.next_cursor
  } catch (e) { if (version === listVersion) error.value = errorMessage(e, t('seedance.failed')) }
  finally { if (version === listVersion) loading.value = false }
}
function pageForward() { if (next.value) { cursors.value.push(next.value); void load() } }
function pageBack() { if (cursors.value.length > 1) { cursors.value.pop(); void load() } }
async function open(id: string) {
  close(); selected.value = id; tab.value = 'received'; error.value = ''
  const current = epoch
  try {
    const value = await api<LogDetail>('GET', url(`/${encodeURIComponent(id)}`))
    if (current !== epoch) return
    detail.value = value
    await Promise.all([loadExecution('events'), loadExecution('attempts')])
  } catch (e) { if (current === epoch) error.value = errorMessage(e, t('seedance.failed')) }
}
async function loadExecution(kind: 'events' | 'attempts', more = false) {
  if (!selected.value) return
  const current = epoch
  const cursor = more ? (kind === 'events' ? eventsNext.value : attemptsNext.value) : ''
  const q = new URLSearchParams({ limit: '20' }); if (cursor) q.set('cursor', cursor)
  try {
    const result = await api<Page<ExecutionRow>>('GET', url(`/${encodeURIComponent(selected.value)}/${kind}`, q))
    if (current !== epoch) return
    if (kind === 'events') { events.value = more ? [...events.value, ...result.data] : result.data; eventsNext.value = result.next_cursor }
    else { attempts.value = more ? [...attempts.value, ...result.data] : result.data; attemptsNext.value = result.next_cursor }
  } catch (e) { if (current === epoch) error.value = errorMessage(e, t('seedance.failed')) }
}
async function read(section: string, attempt = '') {
  const key = `${section}/${attempt}`; const current = epoch
  bodyLoading.value = key; error.value = ''
  try {
    const value = await api<BodyRead>('POST', url(`/${encodeURIComponent(selected.value)}/payload-reads`), { section, attempt_id: attempt, reason_code: reason.value })
    if (current === epoch) bodies.value = { ...bodies.value, [key]: value }
  } catch (e) { if (current === epoch) error.value = errorMessage(e, t('seedance.failed')) }
  finally { if (current === epoch) bodyLoading.value = '' }
}
function bodyStatus(info?: BodyInfo) { return t(`seedance.${info?.availability ?? 'not_captured'}`) }
watch(() => auth.orgId, reset)
watch(() => route.query.request_id, id => { if (typeof id === 'string' && id) void open(id) })
onMounted(() => { void load(); if (typeof route.query.request_id === 'string') void open(route.query.request_id) })
onBeforeUnmount(() => { listVersion++; close() })
</script>

<template>
  <section class="space-y-4" aria-label="Seedance">
    <p class="text-sm text-muted">{{ t('seedance.notice') }}</p>
    <form class="toolbar-panel items-end" @submit.prevent="load(true)">
      <label v-for="key in ['request_id', 'task_id', 'model', 'api_key_id'] as const" :key="key" class="filter-group">
        <span>{{ t(`seedance.${({ request_id: 'request', task_id: 'task', model: 'model', api_key_id: 'key' })[key]}`) }}</span>
        <input v-model="filter[key]" class="input" :name="key" />
      </label>
      <label class="filter-group"><span>{{ t('seedance.operation') }}</span><select v-model="filter.operation" class="input"><option v-for="op in ['create_task', 'all', 'get_task', 'cancel_task', 'delete_task', 'estimate', 'asset_operation']" :key="op" :value="op">{{ t(`seedance.${op}`) }}</option></select></label>
      <label class="filter-group"><span>{{ t('seedance.http') }}</span><input v-model="filter.http_status" class="input" type="number" min="100" max="599" /></label>
      <label class="filter-group"><span>{{ t('seedance.taskStatus') }}</span><select v-model="filter.task_status" class="input"><option value="">—</option><option v-for="status in ['accepted','submitting','submission_unknown','queued','running','succeeded','failed','cancelled','expired']" :key="status">{{ status }}</option></select></label>
      <label class="filter-group"><span>{{ t('seedance.billing') }}</span><select v-model="filter.billing_state" class="input"><option value="">—</option><option v-for="state in ['reserved','pending_usage','settled','released','adjusted']" :key="state">{{ state }}</option></select></label>
      <label class="filter-group"><span>{{ t('seedance.time') }} ≥</span><input v-model="filter.from" class="input" type="datetime-local" /></label>
      <label class="filter-group"><span>{{ t('seedance.time') }} &lt;</span><input v-model="filter.to" class="input" type="datetime-local" /></label>
      <button class="btn-primary" type="submit" :disabled="loading">{{ t('seedance.search') }}</button>
    </form>
    <p v-if="error && !selected" role="alert" class="text-danger">{{ error }}</p>
    <div class="overflow-auto rounded border border-line">
      <table class="w-full text-left text-xs"><thead><tr class="border-b border-line bg-elev"><th v-for="key in ['request','task','key','model','operation','http','taskStatus','billing','artifact','amount','time']" :key="key" class="whitespace-nowrap p-3">{{ t(`seedance.${key}`) }}</th></tr></thead>
        <tbody><tr v-for="row in rows" :key="row.request_id" class="border-b border-line"><td class="p-3"><button type="button" class="table-link font-mono" @click="open(row.request_id)">{{ row.request_id }}</button><div v-if="row.replay_state === 'replayed'" class="text-muted">{{ t('seedance.replay') }}</div></td><td class="p-3 font-mono">{{ row.task_id || '—' }}</td><td class="p-3 font-mono">{{ row.api_key_id == null ? '—' : `#${row.api_key_id}` }}</td><td class="p-3">{{ row.model || '—' }}</td><td class="p-3">{{ row.operation }}</td><td class="p-3">{{ row.http_status || '—' }}<div>{{ row.outcome }}</div></td><td class="p-3">{{ row.task_status || '—' }}</td><td class="p-3">{{ row.billing_state || '—' }}</td><td class="p-3">{{ row.artifact_status || '—' }}</td><td class="p-3 font-mono">{{ row.task_id ? (row.task_net_charged_amount ?? t('seedance.pending')) : '—' }}</td><td class="whitespace-nowrap p-3" :title="row.started_at">{{ time(row.started_at) }}</td></tr></tbody>
      </table>
      <p v-if="!rows.length" class="p-8 text-center text-muted">{{ t(loading ? 'seedance.loading' : 'seedance.empty') }}</p>
    </div>
    <div class="flex justify-end gap-2"><button type="button" class="btn-ghost" :disabled="loading || cursors.length < 2" @click="pageBack">{{ t('seedance.previous') }}</button><button type="button" class="btn-ghost" :disabled="loading || !next" @click="pageForward">{{ t('seedance.next') }}</button></div>

    <DetailDrawer :open="!!selected" :title="t('seedance.request')" :description="selected" :close-label="t('seedance.close')" size="lg" @update:open="open => { if (!open) close() }">
        <p v-if="error" class="p-4 text-danger" role="alert">{{ error }}</p>
        <template v-if="detail">
          <div class="grid grid-cols-2 gap-2 border-b border-line p-4 text-xs sm:grid-cols-3">
            <p v-if="auth.isSuperadmin">Org: {{ detail.request.org_id }}</p><p>{{ t('seedance.key') }}: {{ detail.request.api_key_id ?? '—' }}</p><p>{{ t('seedance.model') }}: {{ detail.request.model || '—' }}</p>
            <p>{{ t('seedance.http') }}: {{ detail.request.http_status || '—' }} · {{ detail.request.outcome }}</p><p>{{ t('seedance.taskStatus') }}: {{ detail.request.task_status || '—' }}</p><p>{{ t('seedance.duration') }}: {{ detail.request.duration_ms ?? '—' }} ms</p>
            <p class="col-span-2 break-all">{{ t('seedance.task') }}: {{ detail.request.task_id || '—' }}</p><p :title="detail.request.started_at">{{ time(detail.request.started_at) }}</p>
            <p v-if="detail.request.origin_request_id" class="col-span-2 break-all">{{ t('seedance.original') }}: {{ detail.request.origin_request_id }}</p>
          </div>
          <div class="segmented-control m-4" role="tablist"><button v-for="item in ['received','result','response'] as const" :key="item" type="button" role="tab" :aria-selected="tab === item" :class="{ active: tab === item }" @click="tab = item">{{ t(`seedance.${item}`) }}</button></div>
          <div class="space-y-4 p-4" role="tabpanel">
            <label class="flex items-center gap-2 text-sm">{{ t('seedance.reason') }}<select v-model="reason" class="input"><option v-for="r in ['request_debugging','billing_review','customer_support']" :key="r" :value="r">{{ t(`seedance.${r}`) }}</option></select></label>
            <template v-if="tab === 'received' || tab === 'response'">
              <p class="text-sm text-muted">{{ t(tab === 'received' ? 'seedance.receivedNotice' : 'seedance.responseNotice') }}</p>
              <p class="font-mono text-sm">{{ detail.request.method }} {{ detail.request.route }}</p>
              <div v-for="section in (tab === 'received' ? ['client_request', ...(auth.isSuperadmin ? ['effective_request'] : [])] : ['client_response'])" :key="section" class="space-y-2">
                <h3 class="font-medium">{{ section === 'effective_request' ? t('seedance.outbound') : t(`seedance.${tab}`) }}</h3>
                <p class="text-xs text-muted">{{ bodyStatus(detail.bodies[section]) }} · {{ t('seedance.source') }}: {{ detail.bodies[section]?.representation || '—' }} · {{ t('seedance.expires') }}: {{ time(detail.bodies[section]?.expires_at) }}</p>
                <button v-if="detail.bodies[section]?.availability === 'available'" type="button" class="btn-ghost" :disabled="!!bodyLoading" @click="read(section)">{{ t('seedance.read') }}</button>
                <HistoryJSON v-if="bodies[`${section}/`]" :body="bodies[`${section}/`].body" :name="`${selected}-${section}`" />
              </div>
              <template v-if="auth.isSuperadmin"><h3>{{ t('seedance.attempts') }}</h3><div v-for="a in attempts" :key="a.id" class="space-y-2 rounded border border-line p-3 text-xs"><p class="break-all">{{ a.type }} · {{ a.attempt_id }} · {{ a.http_status || '—' }} · {{ time(a.created_at) }}</p><button v-if="a.response_body?.availability === 'available'" type="button" class="table-link" :disabled="!!bodyLoading" @click="read('upstream_response', a.attempt_id)">{{ t('seedance.upstream') }} — {{ t('seedance.read') }}</button><HistoryJSON v-if="bodies[`upstream_response/${a.attempt_id}`]" :body="bodies[`upstream_response/${a.attempt_id}`].body" :name="`${selected}-${a.attempt_id}`" /></div><button v-if="attemptsNext" type="button" class="table-link" @click="loadExecution('attempts', true)">{{ t('seedance.loadMore') }}</button></template>
            </template>
            <template v-else>
              <p v-if="detail.request.deleted_at" class="text-warn">{{ t('seedance.deleted') }}</p>
              <p class="text-xs text-muted">{{ t('seedance.asOf') }}: {{ time(detail.as_of) }}</p>
              <div v-if="detail.billing" class="grid grid-cols-2 gap-3 rounded border border-line p-4 text-sm">
                <p>{{ t('seedance.reserved') }}: {{ detail.billing.reserved_amount }} USD</p><p>{{ t('seedance.settled') }}: {{ detail.billing.settled_amount ?? t('seedance.pending') }} USD</p><p>{{ t('seedance.released') }}: {{ detail.billing.released_amount }} USD</p><p>{{ t('seedance.net') }}: {{ detail.billing.net_charged_amount ?? t('seedance.pending') }} USD</p>
                <p>{{ t('seedance.videoTokens') }}: {{ detail.billing.billable_tokens ?? t('seedance.pending') }}</p><p>{{ t('seedance.inputTokens') }}</p><p class="break-all">{{ t('seedance.priceVersion') }}: {{ detail.billing.pricing_version }}</p><p class="break-all">{{ t('seedance.ruleVersion') }}: {{ detail.billing.billing_rule_version }}</p>
                <p>{{ t('seedance.estimatedTokens') }}: {{ detail.billing.estimated_tokens ?? '—' }}</p><p>{{ t('seedance.unitPrice') }}: {{ detail.billing.unit_price_usd_per_1m ?? '—' }}</p>
                <p v-for="entry in detail.billing.entries" :key="entry.entry_id" class="col-span-2">{{ entry.type }} · {{ entry.amount }} USD · {{ time(entry.created_at) }}</p>
              </div>
              <p v-if="detail.billing?.actual_usage" class="text-xs">{{ t('seedance.reportedUsage') }}: completion_tokens={{ detail.billing.actual_usage.completion_tokens ?? '—' }}, total_tokens={{ detail.billing.actual_usage.total_tokens ?? '—' }}</p>
              <MeteringFacts v-if="detail.billing?.metering" :value="detail.billing.metering" />
              <HistoryJSON v-if="detail.task" :body="detail.task" :name="`${selected}-current-task`" />
              <h3>{{ t('seedance.events') }}</h3><ol class="space-y-2"><li v-for="event in events" :key="event.id" class="rounded border border-line p-3 text-xs"><p>{{ event.type }} · {{ event.from_status }} → {{ event.to_status }}</p><p>{{ event.billing_state }} · {{ time(event.created_at) }}</p><p class="break-all font-mono">{{ event.request_id }}</p></li></ol><button v-if="eventsNext" type="button" class="table-link" @click="loadExecution('events', true)">{{ t('seedance.loadMore') }}</button>
            </template>
          </div>
        </template>
        <p v-else class="p-8 text-muted">{{ t('seedance.loading') }}</p>
    </DetailDrawer>
  </section>
</template>
