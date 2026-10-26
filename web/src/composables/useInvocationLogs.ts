import { computed, onScopeDispose, reactive, ref, shallowRef, watch } from 'vue'
import { useAuth } from '@/stores/auth'
import { useCurrency } from '@/stores/currency'
import { adminToken, ApiError } from '@/lib/api'
import { buildInvocationFilter, defaultInvocationDraft, invocationQueryParams, type InvocationFilterSpec, type InvocationPageSize, type InvocationScope } from '@/lib/invocationQuery'
import { fetchInvocationExport, fetchInvocationLogs, type InvocationExportResult, type InvocationQueryBundle, type InvocationRow } from '@/lib/invocationQueryTransport'

export function useInvocationLogs(options: { scope: () => InvocationScope; active: () => boolean }) {
  const auth = useAuth(), currency = useCurrency()
  const draft = reactive(defaultInvocationDraft())
  const applied = shallowRef<Readonly<InvocationFilterSpec> | null>(null)
  const bundle = shallowRef<InvocationQueryBundle | null>(null)
  const page = ref(1), pageSize = ref<InvocationPageSize>(20), loading = ref(false), error = shallowRef<Error | null>(null)
  const exporting = ref(false), exportError = shallowRef<Error | null>(null), exportResult = shallowRef<InvocationExportResult | null>(null)
  const selectedRow = shallowRef<InvocationRow | null>(null)
  const appliedDraft = ref('')
  let contextEpoch = 0, listSeq = 0, exportSeq = 0, filterRevision = 0, disposed = false
  let acceptedContext: Context | null = null
  let listController: AbortController | null = null, exportController: AbortController | null = null
  const context = () => ({ scope: options.scope(), active: options.active(), token: auth.token, session: auth.session,
    orgId: auth.orgId, role: auth.role, root: adminToken.get(), rootRevision: adminToken.revision.value, currency: currency.effectiveCurrency })
  type Context = ReturnType<typeof context>
  const contextEqual = (a: Context, b: Context) => a.scope === b.scope && a.active === b.active && a.token === b.token && a.session === b.session && a.orgId === b.orgId && a.role === b.role && a.root === b.root && a.rootRevision === b.rootRevision && a.currency === b.currency
  const identityEqual = (a: Context, b: Context) => a.scope === b.scope && a.token === b.token && a.session === b.session && a.orgId === b.orgId && a.role === b.role && a.root === b.root && a.rootRevision === b.rootRevision
  const authorized = (value: Context) => value.scope === 'org' ? !!value.token && value.orgId > 0 : value.scope === 'admin' && (!!value.root || !!value.token && value.role === 'superadmin')
  const allowed = computed(() => { const value = context(); return value.active && authorized(value) })
  const dirty = computed(() => JSON.stringify(draft) !== appliedDraft.value)
  const safeError = (value: unknown): Error => value instanceof Error ? value : new Error('Invocation query unavailable')
  const isAbort = (value: unknown) => value instanceof Error && value.name === 'AbortError'
  function closeDetail() { selectedRow.value = null }
  function clearData() { bundle.value = null; acceptedContext = null; closeDetail(); error.value = null; exportResult.value = null; exportError.value = null }
  function cancelExport() {
    exportSeq++; exportController?.abort(); exportController = null; exporting.value = false
  }
  function invalidate() {
    contextEpoch++; listSeq++; listController?.abort(); listController = null; loading.value = false
    cancelExport(); clearData()
  }
  function valid(ctx: Context, epoch: number, revision: number) {
    return !disposed && epoch === contextEpoch && revision === filterRevision && contextEqual(ctx, context()) && ctx.active && authorized(ctx)
  }
  function denyCurrent() {
    // Do not retry with a different scope or fall back to the legacy endpoint.
    bundle.value = null; closeDetail(); cancelExport(); exportResult.value = null
  }
  async function load() {
    const ctx = context(), filter = applied.value
    if (disposed || !ctx.active || !authorized(ctx) || !filter) return
    listController?.abort()
    const controller = new AbortController(), seq = ++listSeq, epoch = contextEpoch, revision = filterRevision
    listController = controller
    const requestedPage = page.value, requestedSize = pageSize.value
    const current = () => valid(ctx, epoch, revision) && seq === listSeq && requestedPage === page.value && requestedSize === pageSize.value
    bundle.value = null; acceptedContext = null; closeDetail(); error.value = null; loading.value = true
    let timedOut = false
    const timeout = setTimeout(() => { timedOut = true; controller.abort() }, 10000)
    try {
      const result = await fetchInvocationLogs(filter, ctx.currency, requestedPage, requestedSize, controller.signal)
      if (current() && !controller.signal.aborted) { bundle.value = result; acceptedContext = ctx }
    } catch (cause) {
      if (!current()) return
      if (timedOut) error.value = new ApiError(503, 'Invocation query timed out', null, 'query_timeout')
      else if (!isAbort(cause) || !controller.signal.aborted) error.value = safeError(cause)
      if (cause instanceof ApiError && (cause.status === 401 || cause.status === 403)) denyCurrent()
    } finally {
      clearTimeout(timeout)
      if (current()) { loading.value = false; listController = null }
      else if (!disposed && epoch === contextEpoch && seq === listSeq) invalidate()
    }
  }
  async function apply() {
    if (!allowed.value || disposed) return
    let next: Readonly<InvocationFilterSpec>
    try { next = buildInvocationFilter({ ...draft }, options.scope()) }
    catch (cause) { invalidate(); error.value = safeError(cause); return }
    filterRevision++; applied.value = next; appliedDraft.value = JSON.stringify(draft); page.value = 1
    cancelExport(); exportResult.value = null; exportError.value = null
    await load()
  }
  async function reset() { Object.assign(draft, defaultInvocationDraft()); await apply() }
  async function refresh() { await load() }
  async function setPage(value: number) {
    if (!applied.value || !allowed.value) return
    try { invocationQueryParams(applied.value, currency.effectiveCurrency, { page: value, pageSize: pageSize.value }) }
    catch (cause) { invalidate(); error.value = safeError(cause); return }
    if (value === page.value) return
    page.value = value; await load()
  }
  async function setPageSize(value: InvocationPageSize) {
    if (!applied.value || !allowed.value) return
    try { invocationQueryParams(applied.value, currency.effectiveCurrency, { page: 1, pageSize: value }) }
    catch (cause) { invalidate(); error.value = safeError(cause); return }
    if (value === pageSize.value) return
    pageSize.value = value; page.value = 1; await load()
  }
  function openDetail(row: InvocationRow) {
    if (!acceptedContext || !contextEqual(acceptedContext, context())) { invalidate(); return }
    if (!disposed && allowed.value && !loading.value && bundle.value?.items.includes(row)) selectedRow.value = row
  }
  async function exportCSV() {
    const ctx = context(), filter = applied.value
    if (disposed || !ctx.active || !authorized(ctx) || !filter || exporting.value) return
    const controller = new AbortController(), seq = ++exportSeq, epoch = contextEpoch, revision = filterRevision
    exportController = controller; exporting.value = true; exportError.value = null; exportResult.value = null
    const current = () => valid(ctx, epoch, revision) && seq === exportSeq
    let timedOut = false, objectURL: string | null = null, anchor: HTMLAnchorElement | null = null
    const timeout = setTimeout(() => { timedOut = true; controller.abort() }, 10000)
    try {
      const result = await fetchInvocationExport(filter, ctx.currency, controller.signal)
      if (!current() || controller.signal.aborted) return
      objectURL = URL.createObjectURL(result.blob)
      anchor = document.createElement('a'); anchor.href = objectURL; anchor.download = result.filename; anchor.hidden = true
      document.body.appendChild(anchor)
      // The browser cannot retract a completed click. Check actual credential
      // values again here, not only a reactive revision observed earlier.
      if (!current() || controller.signal.aborted) return
      anchor.click()
      if (current()) exportResult.value = result.metadata
    } catch (cause) {
      if (!current()) return
      if (timedOut) exportError.value = new ApiError(503, 'Invocation export timed out', null, 'query_timeout')
      else if (!isAbort(cause) || !controller.signal.aborted) exportError.value = safeError(cause)
      if (cause instanceof ApiError && (cause.status === 401 || cause.status === 403)) denyCurrent()
    } finally {
      clearTimeout(timeout); anchor?.remove(); if (objectURL) URL.revokeObjectURL(objectURL)
      if (current()) { exporting.value = false; exportController = null }
      else if (!disposed && epoch === contextEpoch && seq === exportSeq) invalidate()
    }
  }
  let previous: Context | undefined
  const stop = watch(() => { const c = context(); return [c.scope, c.active, c.token, c.session, c.orgId, c.role, c.rootRevision, c.currency] }, () => {
    const next = context(), identityChanged = !previous || !identityEqual(previous, next)
    previous = next
    invalidate()
    if (identityChanged) {
      Object.assign(draft, defaultInvocationDraft()); applied.value = null; appliedDraft.value = ''; filterRevision++; page.value = 1
    }
    if (!next.active || !authorized(next)) return
    if (!applied.value) {
      try { applied.value = buildInvocationFilter({ ...draft }, next.scope); appliedDraft.value = JSON.stringify(draft); filterRevision++ }
      catch (cause) { error.value = safeError(cause); return }
    }
    void load()
  }, { immediate: true, flush: 'sync' })
  onScopeDispose(() => { disposed = true; stop(); invalidate() })
  return { draft, applied, bundle, page, pageSize, loading, error, exporting, exportError, exportResult, selectedRow, allowed, dirty,
    apply, reset, refresh, setPage, setPageSize, exportCSV, cancelExport, openDetail, closeDetail }
}
