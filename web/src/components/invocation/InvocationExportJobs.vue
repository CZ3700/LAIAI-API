<script setup lang="ts">
import { onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import DetailDrawer from '@/components/DetailDrawer.vue'
import { ApiError, api, apiFetch, uuid } from '@/lib/api'
import { invocationQueryParams, type InvocationFilterSpec } from '@/lib/invocationQuery'
import { useAuth } from '@/stores/auth'
import { useCurrency } from '@/stores/currency'

type Job = {
  id: string
  status: 'pending' | 'processing' | 'ready' | 'failed' | 'expired'
  row_count: number
  byte_count: number
  error_code: string
  created_at: string
  updated_at: string
  expires_at: string
  scanned_since: string
  display_currency: 'USD' | 'CNY'
  filters: {
    timezone: string; since: string; until: string; model?: string; request_id?: string
    api_key_id?: string | number; result?: string; status?: string; modality?: string; is_stream?: boolean
  }
}

const props = defineProps<{ filter: InvocationFilterSpec | null; disabled: boolean }>()
const { t, locale } = useI18n()
const auth = useAuth(), currency = useCurrency()
const endpoint = '/api/usage/export-jobs'
const maxBytes = 128 * 1024 * 1024
const open = ref(false), submitting = ref(false), loading = ref(false), downloading = ref('')
const jobs = ref<Job[]>([]), error = ref('')
let context = new AbortController()
let pendingRequest: { query: string; key: string } | null = null

function contextChanged() {
  context.abort()
  context = new AbortController()
  pendingRequest = null
  open.value = false
  jobs.value = []
  error.value = ''
  submitting.value = false
  loading.value = false
  downloading.value = ''
}
watch(() => [auth.token, auth.session, auth.orgId], contextChanged, { flush: 'sync' })

function jobError(cause: unknown) {
  return t(cause instanceof ApiError && cause.code === 'export_job_capacity'
    ? 'invocationLogs.jobsCapacity' : 'invocationLogs.jobsUnavailable')
}

async function refresh() {
  if (loading.value || submitting.value || !open.value) return
  const signal = context.signal
  loading.value = true
  try {
    const data = await api<{ items: Job[] }>('GET', endpoint, undefined, { signal, cache: 'no-store' })
    if (!Array.isArray(data.items)) throw new Error('Invalid export job list')
    if (!signal.aborted) jobs.value = data.items
  } catch (cause) {
    if (!signal.aborted) error.value = jobError(cause)
  } finally {
    if (!signal.aborted) loading.value = false
  }
}

function showJobs() {
  error.value = ''
  open.value = true
  void refresh()
}

async function create() {
  if (props.disabled || props.filter?.scope !== 'org' || submitting.value || loading.value) return
  const signal = context.signal
  const query = invocationQueryParams(props.filter, currency.effectiveCurrency).toString()
  if (pendingRequest?.query !== query) pendingRequest = { query, key: uuid() }
  error.value = ''
  open.value = true
  submitting.value = true
  try {
    const created = await api<Job>('POST', `${endpoint}?${query}`, undefined, {
      signal, headers: { 'Idempotency-Key': pendingRequest.key },
    })
    if (signal.aborted) return
    pendingRequest = null
    jobs.value = [created, ...jobs.value.filter(j => j.id !== created.id)]
  } catch (cause) {
    if (!signal.aborted) error.value = jobError(cause)
  } finally {
    if (!signal.aborted) {
      submitting.value = false
      if (!error.value) void refresh()
    }
  }
}

async function download(job: Job) {
  if (downloading.value || job.status !== 'ready' || !/^[1-9]\d{0,19}$/.test(job.id)) return
  const controller = new AbortController(), signal = context.signal
  const stop = () => controller.abort()
  signal.addEventListener('abort', stop, { once: true })
  const timeout = setTimeout(stop, 120_000)
  downloading.value = job.id
  error.value = ''
  try {
    const response = await apiFetch(`${endpoint}/${job.id}/download`, { admin: false, signal: controller.signal })
    // Content-Length can change when the edge compresses the response.
    const length = Number(response.headers.get('X-Invocation-Export-Bytes'))
    if (!response.ok || !/^text\/csv(?:;|$)/i.test(response.headers.get('Content-Type') ?? '')
      || length !== job.byte_count || !Number.isSafeInteger(length) || length < 1 || length > maxBytes || !response.body) {
      void response.body?.cancel().catch(() => {})
      throw new Error('Invalid export response')
    }
    const reader = response.body.getReader()
    const parts: BlobPart[] = []
    let received = 0
    try {
      for (;;) {
        const part = await reader.read()
        if (part.done) break
        received += part.value.byteLength
        if (received > length) throw new Error('Invalid export length')
        parts.push(part.value as BlobPart)
      }
    } finally {
      void reader.cancel().catch(() => {})
      reader.releaseLock()
    }
    if (controller.signal.aborted || signal.aborted) return
    if (received !== length) throw new Error('Incomplete export')
    const url = URL.createObjectURL(new Blob(parts, { type: 'text/csv;charset=utf-8' }))
    const link = document.createElement('a')
    link.href = url
    link.download = `invocation-log-${job.id}.csv`
    document.body.append(link)
    try { link.click() } finally { link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000) }
  } catch (cause) {
    if (!signal.aborted) error.value = jobError(cause)
  } finally {
    clearTimeout(timeout)
    signal.removeEventListener('abort', stop)
    if (!signal.aborted) downloading.value = ''
  }
}

function date(value: string, timezone: string, end = false) {
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeZone: timezone })
    .format(new Date(Date.parse(value) - (end ? 1 : 0)))
}

const poll = setInterval(() => {
  if (open.value && jobs.value.some(job => job.status === 'pending' || job.status === 'processing')) void refresh()
}, 2500)
onScopeDispose(() => { context.abort(); clearInterval(poll) })
</script>

<template>
  <button type="button" class="btn-primary" :disabled="disabled || !filter || submitting || loading" @click="create">{{ t('invocationLogs.exportApplied') }}</button>
  <button type="button" class="btn-ghost" @click="showJobs">{{ t('invocationLogs.jobsTitle') }}</button>
  <DetailDrawer v-model:open="open" :title="t('invocationLogs.jobsTitle')" size="lg">
    <section class="export-jobs">
      <div class="export-actions">
        <p class="text-muted text-xs">{{ t('invocationLogs.jobsHint') }}</p>
        <button type="button" class="btn-ghost" :disabled="loading || submitting" @click="error = ''; refresh()">{{ t('common.refresh') }}</button>
      </div>
      <p v-if="error" class="inline-alert" role="alert">{{ error }}</p>
      <p v-if="submitting || loading && !jobs.length" role="status">{{ t('reqlog.loading') }}</p>
      <p v-else-if="!jobs.length && !error" class="text-muted">{{ t('invocationLogs.jobsEmpty') }}</p>
      <article v-for="job in jobs" :key="job.id" class="app-panel p-3 export-job">
        <div class="export-actions">
          <strong>#{{ job.id }} · <span aria-live="polite">{{ t(`invocationLogs.job_${job.status}`) }}</span></strong>
          <button v-if="job.status === 'ready'" type="button" class="btn-primary" :disabled="!!downloading" @click="download(job)">
            {{ t(downloading === job.id ? 'invocationLogs.exporting' : 'invocationLogs.jobsDownload') }}
          </button>
        </div>
        <p>{{ date(job.filters.since, job.filters.timezone) }} – {{ date(job.filters.until, job.filters.timezone, true) }} · {{ job.filters.timezone }}</p>
        <p>{{ t('invocationLogs.jobsRows', { count: job.row_count }) }} · {{ job.display_currency }}</p>
        <p v-if="job.error_code" class="text-muted">{{ t(job.error_code === 'size_exceeded' ? 'invocationLogs.jobsSize' : 'invocationLogs.jobsFailed') }}</p>
        <details>
          <summary>{{ t('invocationLogs.jobsDetails') }}</summary>
          <dl class="export-facts">
            <dt>{{ t('invocationLogs.jobsCreated') }}</dt><dd>{{ new Date(job.created_at).toLocaleString(locale, { timeZone: job.filters.timezone }) }}</dd>
            <dt>{{ t('invocationLogs.jobsUpdated') }}</dt><dd>{{ new Date(job.updated_at).toLocaleString(locale, { timeZone: job.filters.timezone }) }}</dd>
            <dt>{{ t('invocationLogs.jobsExpires') }}</dt><dd>{{ new Date(job.expires_at).toLocaleString(locale, { timeZone: job.filters.timezone }) }}</dd>
            <dt v-if="job.status === 'processing'">{{ t('invocationLogs.jobsScanned') }}</dt>
            <dd v-if="job.status === 'processing'">{{ new Date(job.scanned_since).toLocaleString(locale, { timeZone: job.filters.timezone }) }} · {{ job.filters.timezone }}</dd>
            <template v-if="job.filters.model"><dt>{{ t('reqlog.filterModel') }}</dt><dd>{{ job.filters.model }}</dd></template>
            <template v-if="job.filters.request_id"><dt>{{ t('reqlog.filterRequestId') }}</dt><dd>{{ job.filters.request_id }}</dd></template>
            <template v-if="job.filters.api_key_id"><dt>{{ t('reqlog.filterApiKey') }} ID</dt><dd>#{{ job.filters.api_key_id }}</dd></template>
            <template v-if="job.filters.result"><dt>{{ t('reqlog.filterResult') }}</dt><dd>{{ t(job.filters.result === 'success' ? 'reqlog.resultSuccess' : 'invocationLogs.nonSuccess') }}</dd></template>
            <template v-if="job.filters.status"><dt>{{ t('reqlog.colStatus') }}</dt><dd>{{ job.filters.status }}</dd></template>
            <template v-if="job.filters.modality !== undefined"><dt>{{ t('reqlog.filterModality') }}</dt><dd>{{ job.filters.modality || t('invocationLogs.historical') }}</dd></template>
            <template v-if="job.filters.is_stream !== undefined"><dt>{{ t('reqlog.dtStream') }}</dt><dd>{{ t(job.filters.is_stream ? 'common.yes' : 'common.no') }}</dd></template>
          </dl>
        </details>
      </article>
    </section>
  </DetailDrawer>
</template>

<style scoped>
.export-jobs,.export-job{display:grid;gap:.65rem}.export-actions{display:flex;align-items:center;justify-content:space-between;gap:.75rem;flex-wrap:wrap}.export-facts{display:grid;grid-template-columns:auto 1fr;gap:.4rem .75rem;margin-top:.5rem}.export-facts dd{overflow-wrap:anywhere}.export-job summary{cursor:pointer}
</style>
