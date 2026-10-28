export function isOfficialSeedance(model: string) { return /seedance[-_]?2/i.test(model) }
export function generationActive(status: string) { return ['accepted', 'submitting', 'submission_unknown', 'queued', 'running'].includes(status) }
// 单个任务 GET 为官方数据面形状，不再携带 billing_state/artifact_status；
// 账务终局改由 billing 接口的 state 传入（列表 DTO 仍可直接用 task.billing_state）。
export function canDelete(task: { status: string; billing_state?: string; deletion_state?: string }, billingState?: string | null) {
  return ['succeeded', 'failed', 'cancelled', 'expired'].includes(task.status) && ['settled', 'released', 'adjusted'].includes(billingState ?? task.billing_state ?? '') && task.deletion_state !== 'deleted'
}
export function createBody(input: { model: string; prompt: string; image: string; video: string; audio: string; duration: string; resolution: string; ratio: string }) {
  const content: Record<string, unknown>[] = [{ type: 'text', text: input.prompt.trim() }]
  for (const kind of ['image', 'video', 'audio'] as const) {
    const url = input[kind].trim()
    if (url) content.push({ type: `${kind}_url`, [`${kind}_url`]: { url }, role: `reference_${kind}` })
  }
  const body: Record<string, unknown> = { model: input.model, content }
  if (input.duration !== 'default') body.duration = Number(input.duration)
  if (input.resolution !== 'default') body.resolution = input.resolution
  if (input.ratio !== 'default') body.ratio = input.ratio
  return body
}
export function shouldPoll(task: { status: string; artifact_status?: string; billing_state?: string; content?: { video_url?: string } }, billingState?: string | null) {
  if (generationActive(task.status)) return true
  if (['reserved', 'pending_usage'].includes(billingState ?? task.billing_state ?? '')) return true
  if (task.status !== 'succeeded') return false
  // 官方形状无 artifact_status：产物链接（content.video_url）尚未回来时继续观察。
  if (task.artifact_status !== undefined) return ['pending', 'downloading'].includes(task.artifact_status)
  return !task.content?.video_url
}
export function authorizedRequest(body: Record<string, unknown>, quote: { reserved_amount: string; pricing_version: string }) {
  if (!/^(0|[1-9]\d*)(\.\d{1,8})?$/.test(quote.reserved_amount) || !quote.pricing_version) throw new Error('Invalid quote')
  return { ...body, max_charge_usd: quote.reserved_amount, expected_pricing_version: quote.pricing_version }
}
export function safeMediaURL(value?: unknown): string {
  if (typeof value !== 'string' || !/^https:\/\//i.test(value) || /[\s\\]/.test(value)) return ''
  try {
    const url = new URL(value)
    const host = url.hostname.toLowerCase().replace(/\.$/, '')
    if (url.protocol !== 'https:' || url.username || url.password || url.hash || url.port && url.port !== '443') return ''
    if (!host.includes('.') || host.startsWith('[') || /^(\d+\.){3}\d+$/.test(host) || /\.(localhost|local|internal)$/.test(host)) return ''
    // Preserve the exact signed URL. Only the task API gets the platform Key;
    // browsers load this URL directly with no fetch or authorization header.
    return value
  } catch { return '' }
}
