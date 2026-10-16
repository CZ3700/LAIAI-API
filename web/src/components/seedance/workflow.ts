export function isOfficialSeedance(model: string) { return /seedance[-_]?2/i.test(model) }
export function generationActive(status: string) { return ['accepted', 'submitting', 'submission_unknown', 'queued', 'running'].includes(status) }
export function canDelete(task: { status: string; billing_state?: string; deletion_state?: string }) {
  return ['succeeded', 'failed', 'cancelled', 'expired'].includes(task.status) && ['settled', 'released', 'adjusted'].includes(task.billing_state ?? '') && task.deletion_state !== 'deleted'
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
export function shouldPoll(task: { status: string; artifact_status?: string; billing_state?: string }) {
  if (generationActive(task.status)) return true
  if (['reserved', 'pending_usage'].includes(task.billing_state ?? '')) return true
  return task.status === 'succeeded' && ['pending', 'downloading'].includes(task.artifact_status ?? '')
}
export function authorizedRequest(body: Record<string, unknown>, quote: { reserved_amount: string; pricing_version: string }) {
  if (!/^(0|[1-9]\d*)(\.\d{1,8})?$/.test(quote.reserved_amount) || !quote.pricing_version) throw new Error('Invalid quote')
  return { ...body, max_charge_usd: quote.reserved_amount, expected_pricing_version: quote.pricing_version }
}
export function safeContentPath(id: string, path?: string) {
  const expected = `/v1/video/generations/${encodeURIComponent(id)}/content`
  return path === expected ? path : expected
}
