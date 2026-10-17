<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { uuid } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import DetailDrawer from '@/components/DetailDrawer.vue'
import { useSeedanceI18n } from './messages'
import { authorizedRequest, canDelete, createBody, generationActive, safeContentPath, shouldPoll } from './workflow'
import HistoryJSON from './HistoryJSON.vue'
import type { Billing } from './types'

const props = defineProps<{ model: string; apiKey: string }>()
const emit = defineEmits<{ locked: [value: boolean] }>()
const { t } = useSeedanceI18n()
const auth = useAuth()
interface Task { id: string; model: string; status: string; billing_state: string; artifact_status: string; content_url?: string; usage?: { completion_tokens: number | null; total_tokens: number | null }; deletion_state?: string }
interface Quote { reserved_amount: string; estimated_tokens: number; pricing_version: string; billing_rule_version: string; capability_version: string }
interface Model { id: string; enabled: boolean; verified: boolean; capabilities: { resolutions: string[]; max_duration_seconds: number; automatic_duration: boolean; input_video: boolean; input_audio: boolean } }
const prompt = ref('')
const image = ref('')
const video = ref('')
const audio = ref('')
const duration = ref('default')
const resolution = ref('default')
const ratio = ref('default')
const capabilities = ref<Model | null>(null)
const error = ref('')
const busy = ref(false)
const quote = ref<Quote | null>(null)
const quotedBody = ref<Record<string, unknown> | null>(null)
const pending = ref<{ key: string; body: Record<string, unknown> } | null>(null)
const uncertain = ref(false)
const task = ref<Task | null>(null)
const billing = ref<Billing | null>(null)
const requestID = ref('')
const submitted = ref<unknown>(null)
const received = ref<unknown>(null)
const responseStatus = ref<number | null>(null)
const src = ref('')
const showDetails = ref(false)
function closeDetails() { showDetails.value = false }
const cancelKey = ref('')
const cancelOperation = ref<{ operation_id: string; status: string } | null>(null)
const mutationKind = ref<'cancel' | 'delete'>('cancel')
const currentTab = ref<'received' | 'result' | 'response'>('received')
let timer: number | undefined
let polling = false
let epoch = 0
let contentLoading = false
const active = computed(() => task.value ? generationActive(task.value.status) : false)
const locked = computed(() => busy.value || active.value || uncertain.value)
watch(locked, value => emit('locked', value), { immediate: true })
const storeKey = computed(() => `laiai.ark.task.${auth.orgId}`)
const headers = () => ({ Authorization: `Bearer ${props.apiKey}`, 'Content-Type': 'application/json' })
function errorText(data: unknown, status: number) {
  const e = (data as { error?: unknown })?.error
  if (typeof e === 'string') return e
  if (e && typeof e === 'object' && 'message' in e && typeof e.message === 'string') return e.message
  return `HTTP ${status}`
}
function body() {
  return createBody({ model: props.model, prompt: prompt.value, image: image.value, video: video.value, audio: audio.value, duration: duration.value, resolution: resolution.value, ratio: ratio.value })
}
function stop() { if (timer) window.clearInterval(timer); timer = undefined }
function clearMedia() { if (src.value) URL.revokeObjectURL(src.value); src.value = '' }
function clearPrivate() { epoch++; stop(); clearMedia(); busy.value = false; quote.value = null; quotedBody.value = null; submitted.value = null; received.value = null; responseStatus.value = null; pending.value = null; uncertain.value = false; task.value = null; billing.value = null; requestID.value = ''; error.value = ''; cancelKey.value = ''; cancelOperation.value = null; capabilities.value = null; prompt.value = ''; image.value = ''; video.value = ''; audio.value = ''; duration.value = 'default'; resolution.value = 'default'; ratio.value = 'default'; showDetails.value = false; emit('locked', false) }
async function loadModels() {
  const current = epoch
  if (!props.apiKey) return
  try {
    const r = await fetch('/v1/video/models', { headers: headers(), cache: 'no-store' })
    const data = await r.json()
    if (!r.ok) throw new Error(errorText(data, r.status))
    if (current === epoch) capabilities.value = (data.data as Model[]).find(x => x.id === props.model) ?? null
  } catch (e) { if (current === epoch) error.value = (e as Error).message }
}
async function estimate() {
  if (!prompt.value.trim() || !props.apiKey || locked.value) return
  const current = epoch
  busy.value = true; error.value = ''; quote.value = null
  const value = body()
  try {
    const r = await fetch('/v1/video/estimates', { method: 'POST', headers: headers(), body: JSON.stringify(value), cache: 'no-store' })
    const data = await r.json()
    if (!r.ok) throw new Error(errorText(data, r.status))
    if (current !== epoch) return
    authorizedRequest(value, data)
    quote.value = data; quotedBody.value = value
  } catch (e) { if (current === epoch) error.value = (e as Error).message }
  finally { if (current === epoch) busy.value = false }
}
async function submit() {
  if (busy.value || active.value || !props.apiKey) return
  if (!pending.value) {
    if (!quote.value || !quotedBody.value) { error.value = t('seedance.noQuote'); return }
    pending.value = { key: uuid(), body: authorizedRequest(quotedBody.value, quote.value) }
    clearMedia(); task.value = null; billing.value = null; received.value = null; requestID.value = ''; cancelOperation.value = null; cancelKey.value = ''
  }
  const current = epoch
  const attempt = pending.value
  submitted.value = attempt.body; busy.value = true; error.value = ''; uncertain.value = false
  try {
    const r = await fetch('/v1/video/generations', { method: 'POST', headers: { ...headers(), 'Idempotency-Key': attempt.key }, body: JSON.stringify(attempt.body), cache: 'no-store' })
    const data = await r.json().catch(() => ({}))
    if (current !== epoch) return
    requestID.value = r.headers.get('X-Request-Id') ?? ''
    responseStatus.value = r.status; received.value = data
    if (!r.ok) {
      if (r.status >= 500 || r.status === 429) uncertain.value = true
      else { pending.value = null; quote.value = null }
      throw new Error(errorText(data, r.status))
    }
    if (typeof data.id !== 'string' || !data.id.startsWith('task_')) { uncertain.value = true; throw new Error(t('seedance.uncertain')) }
    task.value = data; pending.value = null; quote.value = null
    sessionStorage.setItem(storeKey.value, JSON.stringify({ id: data.id, request_id: requestID.value, model: props.model }))
    start()
  } catch (e) { if (current === epoch) { if (pending.value) uncertain.value = true; error.value = (e as Error).message } }
  finally { if (current === epoch) busy.value = false }
}
function start() { stop(); timer = window.setInterval(() => void poll(), 5000); void poll() }
async function poll() {
  if (!task.value || polling || !props.apiKey) return
  const current = epoch; const id = task.value.id
  polling = true
  try {
    if (cancelOperation.value && !['confirmed', 'rejected'].includes(cancelOperation.value.status)) {
      const op = await fetch(`/v1/video/operations/${encodeURIComponent(cancelOperation.value.operation_id)}`, { headers: headers(), cache: 'no-store' })
      if (op.ok) { const value = await op.json(); if (current === epoch) cancelOperation.value = value }
    }
    if (current !== epoch) return
    if (mutationKind.value === 'delete' && cancelOperation.value?.status === 'confirmed') {
      stop(); clearMedia(); task.value = { ...task.value!, deletion_state: 'deleted' }; sessionStorage.removeItem(storeKey.value); return
    }
    const r = await fetch(`/v1/video/generations/${encodeURIComponent(id)}`, { headers: headers(), cache: 'no-store' })
    const data = await r.json().catch(() => ({}))
    if (current !== epoch) return
    if (!r.ok) { if ([401,403,404].includes(r.status)) stop(); throw new Error(errorText(data, r.status)) }
    task.value = data
    const b = await fetch(`/v1/video/generations/${encodeURIComponent(id)}/billing`, { headers: headers(), cache: 'no-store' })
    const bill = await b.json().catch(() => ({}))
    if (current !== epoch) return
    if (b.ok) billing.value = bill
    if (current !== epoch) return
    if (data.artifact_status === 'ready' && !src.value) await loadContent(data)
    const mutationPending = cancelOperation.value && !['confirmed', 'rejected'].includes(cancelOperation.value.status)
    if (!shouldPoll(data) && !mutationPending) { stop(); sessionStorage.removeItem(storeKey.value) }
  } catch (e) { if (current === epoch) error.value = (e as Error).message }
  finally { polling = false }
}
async function loadContent(value: Task) {
  if (contentLoading) return
  const current = epoch; contentLoading = true
  try {
    const r = await fetch(safeContentPath(value.id, value.content_url), { headers: { Authorization: `Bearer ${props.apiKey}` }, cache: 'no-store' })
    if (!r.ok) throw new Error(`HTTP ${r.status}`)
    const blob = await r.blob()
    if (current !== epoch) return
    clearMedia(); src.value = URL.createObjectURL(blob)
  } catch (e) { if (current === epoch) error.value = (e as Error).message }
  finally { contentLoading = false }
}
async function mutate(kind: 'cancel' | 'delete') {
  if (!task.value || busy.value) return
  if (kind === 'delete' && !window.confirm(t('seedance.confirmDelete'))) return
  if (!cancelKey.value) cancelKey.value = uuid()
  const id = task.value.id; const current = epoch; busy.value = true; error.value = ''
  try {
    const r = await fetch(`/v1/video/generations/${encodeURIComponent(id)}${kind === 'cancel' ? '/cancel' : ''}`, { method: kind === 'cancel' ? 'POST' : 'DELETE', headers: { ...headers(), ...(kind === 'cancel' ? { 'Idempotency-Key': cancelKey.value } : {}) }, cache: 'no-store' })
    const data = await r.json().catch(() => ({}))
    if (current !== epoch) return
    if (!r.ok) throw new Error(errorText(data, r.status))
    mutationKind.value = kind
    if (kind === 'delete' && (r.status === 204 || data.status === 'confirmed')) { stop(); clearMedia(); task.value = { ...task.value!, deletion_state: 'deleted' }; sessionStorage.removeItem(storeKey.value) }
    else { cancelOperation.value = data; start() }
  } catch (e) { if (current === epoch) error.value = (e as Error).message }
  finally { if (current === epoch) busy.value = false }
}
function restore() {
  try {
    const value = JSON.parse(sessionStorage.getItem(storeKey.value) ?? 'null')
    if (value?.model === props.model && /^task_[a-zA-Z0-9_-]+$/.test(value.id) && props.apiKey) {
      task.value = { id: value.id, model: value.model, status: 'accepted', billing_state: 'reserved', artifact_status: 'pending' }; requestID.value = value.request_id ?? ''; start()
    }
  } catch { sessionStorage.removeItem(storeKey.value) }
}
watch([prompt, image, video, audio, duration, resolution, ratio], () => { quote.value = null; quotedBody.value = null })
watch(() => [auth.orgId, props.model, props.apiKey], () => { clearPrivate(); void loadModels(); restore() })
onMounted(() => { void loadModels(); restore() })
onActivated(() => { if (task.value && shouldPoll(task.value)) start() })
onDeactivated(closeDetails)
onBeforeUnmount(clearPrivate)
</script>

<template>
  <div class="flex flex-1 flex-col gap-4 p-4 sm:p-5">
    <div class="grid gap-3 sm:grid-cols-3">
      <label class="text-sm">{{ t('play.videoDuration') }}<select v-model="duration" class="input mt-1" :disabled="locked"><option value="default">{{ t('seedance.defaultOption') }}</option><option v-if="capabilities?.capabilities.automatic_duration" value="-1">{{ t('seedance.automaticDuration') }}</option><option v-for="n in Array.from({ length: Math.max(0, (capabilities?.capabilities.max_duration_seconds ?? 15) - 3) }, (_, i) => i + 4)" :key="n" :value="String(n)">{{ n }}</option></select></label>
      <label class="text-sm">{{ t('play.videoResolution') }}<select v-model="resolution" class="input mt-1" :disabled="locked"><option value="default">{{ t('seedance.defaultOption') }}</option><option v-for="r in capabilities?.capabilities.resolutions ?? []" :key="r">{{ r }}</option></select></label>
      <label class="text-sm">{{ t('play.videoRatio') }}<select v-model="ratio" class="input mt-1" :disabled="locked"><option value="default">{{ t('seedance.defaultOption') }}</option><option v-for="r in ['16:9','9:16','1:1','4:3','3:4','21:9']" :key="r">{{ r }}</option></select></label>
    </div>
    <label class="text-sm">{{ t('play.videoRefImage') }}<input v-model="image" class="input mt-1" placeholder="https://… / asset://…" :disabled="locked" /></label>
    <label v-if="capabilities?.capabilities.input_video" class="text-sm">{{ t('seedance.referenceVideo') }}<input v-model="video" class="input mt-1" placeholder="https://… / asset://…" :disabled="locked" /></label>
    <label v-if="capabilities?.capabilities.input_audio" class="text-sm">{{ t('seedance.referenceAudio') }}<input v-model="audio" class="input mt-1" placeholder="https://… / asset://…" :disabled="locked" /></label>
    <p class="text-xs text-muted">{{ t('seedance.mediaNotice') }}</p>
    <p v-if="capabilities && (!capabilities.enabled || !capabilities.verified)" role="status" class="text-sm text-warn">{{ t('seedance.modelUnavailable') }}</p>
    <textarea v-model="prompt" class="input min-h-28" :placeholder="t('play.videoPromptPlaceholder')" :disabled="locked" />
    <p v-if="error" role="alert" class="break-words text-sm text-danger">{{ error }}</p>
    <p v-if="uncertain" role="status" class="text-sm text-warn">{{ t('seedance.uncertain') }}</p>
    <div v-if="quote" class="space-y-2 rounded border border-line p-4 text-sm"><p>{{ t('seedance.quoteAmount') }}: <strong class="font-mono">{{ quote.reserved_amount }}</strong></p><p>{{ t('seedance.priceVersion') }}: {{ quote.pricing_version }}</p><p>{{ t('seedance.quoteNotice') }}</p><button type="button" class="btn-primary" :disabled="locked" @click="submit">{{ t('seedance.confirm') }}</button></div>
    <div class="flex flex-wrap gap-2"><button v-if="uncertain" type="button" class="btn-primary" :disabled="busy" @click="submit">{{ t('seedance.retry') }}</button><button v-else type="button" class="btn-primary" :disabled="locked || !prompt.trim() || !apiKey || !capabilities?.enabled || !capabilities.verified" @click="estimate">{{ t(busy ? 'seedance.loading' : 'seedance.quote') }}</button><button v-if="task" type="button" class="btn-ghost" :disabled="busy" @click="poll">{{ t('seedance.refresh') }}</button><button v-if="active" type="button" class="btn-ghost" :disabled="busy || !!cancelOperation" @click="mutate('cancel')">{{ t('seedance.cancel') }}</button><button v-if="task && canDelete(task)" type="button" class="btn-ghost" :disabled="busy || !!cancelOperation && !['confirmed', 'rejected'].includes(cancelOperation.status)" @click="mutate('delete')">{{ t('seedance.remove') }}</button></div>
    <div v-if="task" class="space-y-2 rounded border border-line p-4 text-sm"><p class="break-all font-mono">{{ task.id }}</p><p>{{ t('seedance.generationState') }}: {{ task.status }} · {{ t('seedance.billing') }}: {{ task.billing_state }} · {{ t('seedance.artifact') }}: {{ task.artifact_status }}</p><p v-if="cancelOperation" class="break-all">{{ cancelOperation.operation_id }} · {{ cancelOperation.status }}</p><p v-if="task.status === 'succeeded' && task.artifact_status === 'pending'">{{ t('seedance.artifactWaiting') }}</p><p v-if="task.billing_state === 'pending_usage'">{{ t('seedance.billingWaiting') }}</p><p>{{ t('seedance.videoTokens') }}: {{ billing?.billable_tokens ?? t('seedance.pending') }}</p><p>{{ t('seedance.inputTokens') }}</p><p v-if="billing">{{ t('seedance.reserved') }}: {{ billing.reserved_amount }} · {{ t('seedance.settled') }}: {{ billing.settled_amount ?? t('seedance.pending') }} · {{ t('seedance.released') }}: {{ billing.released_amount }} USD</p></div>
    <video v-if="src" :src="src" controls playsinline class="w-full rounded border border-line bg-black" />
    <a v-if="src" class="table-link" :href="src" :download="`${task?.id ?? 'video'}.mp4`">{{ t('play.videoDownload') }}</a>
    <div class="flex flex-wrap gap-3"><router-link v-if="requestID" class="table-link" :to="{ path: '/app/request-logs', query: { tab: 'seedance', request_id: requestID } }">{{ t('seedance.logs') }} · {{ requestID }}</router-link><button v-if="submitted || task" type="button" class="table-link" @click="showDetails = true">{{ t('reqlog.btnDetail') }}</button></div>
    <DetailDrawer v-model:open="showDetails" :title="t('reqlog.btnDetail')" :description="task?.id || requestID" :close-label="t('seedance.close')" size="lg">
      <div class="space-y-3"><div class="segmented-control"><button v-for="item in ['received','result','response'] as const" :key="item" type="button" :class="{ active: currentTab === item }" @click="currentTab = item">{{ t(`seedance.${item}`) }}</button></div><HistoryJSON v-if="currentTab === 'received' && submitted" :body="submitted" name="client-request" /><HistoryJSON v-if="currentTab === 'result' && task" :body="{ task, billing }" name="current-task" /><template v-if="currentTab === 'response'"><p>HTTP {{ responseStatus ?? '—' }}</p><HistoryJSON v-if="received" :body="received" name="client-response" /></template></div>
    </DetailDrawer>
  </div>
</template>
