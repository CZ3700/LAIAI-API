export type VideoPermissionChoice = 'none' | 'read' | 'read_write'
export type VideoPermission = VideoPermissionChoice | 'write'

// Match the video gateway's comma/space delimiters and exact, case-sensitive scopes.
export function videoPermission(scopes: string | null | undefined): VideoPermission {
  const values = new Set((scopes ?? '').split(/[ ,]+/).filter(Boolean))
  const wildcard = values.has('*') || values.has('video:*')
  const read = wildcard || values.has('video:read')
  const write = wildcard || values.has('video:write')
  return read && write ? 'read_write' : read ? 'read' : write ? 'write' : 'none'
}

export interface KeyCreateForm {
  name: string
  quota: string
  models: string
  ips: string
  expiry: number
  rpm: number
  route: string
  videoPermission: VideoPermissionChoice
  enableVideoAsync?: boolean
}

export function buildKeyCreateBody(form: KeyCreateForm): Record<string, unknown> {
  const scopes = form.videoPermission === 'read_write' ? 'video:read video:write'
    : form.videoPermission === 'read' ? 'video:read' : ''
  const body: Record<string, unknown> = { name: form.name, scopes }
  if (form.videoPermission === 'read_write' && form.enableVideoAsync === true) body.enable_video_async = true
  if (form.quota) body.quota_limit = form.quota
  if (form.models.trim()) body.model_limits = form.models.split(',').map(model => model.trim()).filter(Boolean)
  if (form.ips.trim()) body.allow_ips = form.ips.trim()
  if (form.expiry > 0) body.expires_in_days = form.expiry
  if (form.rpm > 0) body.rpm_limit = form.rpm
  if (form.route) body.route_policy = form.route
  return body
}
