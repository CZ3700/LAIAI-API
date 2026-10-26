// API client：统一错误封套 + 401 自动登出（红线：UI 不自行解释资金语义，只展示后端事实）
import { useAuth } from '@/stores/auth'
import { readonly, ref } from 'vue'

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    // 后端在每个响应上回传的 X-Request-Id：报错时透出给用户，便于工单溯源
    public requestId: string | null = null,
    public code: string | null = null, // 后端结构化错误码（如 VERIFICATION_REQUIRED）
    public retryAfter: string | null = null,
  ) {
    super(message)
  }
}

// uuid：幂等键生成。crypto.randomUUID 在非安全上下文（http+IP）被禁用，
// 改用 crypto.getRandomValues（任何上下文可用）手工构造 v4 UUID。
export function uuid(): string {
  const b = new Uint8Array(16)
  crypto.getRandomValues(b)
  b[6] = (b[6] & 0x0f) | 0x40
  b[8] = (b[8] & 0x3f) | 0x80
  const h = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

// copyText：剪贴板复制 + 非安全上下文兜底。
// navigator.clipboard 仅在 HTTPS/localhost 可用（http+IP 被禁），失败时
// 退到隐藏 textarea + execCommand('copy')，这在非安全上下文仍可工作。
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 落到下面的兜底
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.focus()
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}

// 统一错误文案：ApiError 带 requestId 时追加 (ID: …)，便于用户报工单时溯源
export function errorMessage(e: unknown, fallback: string): string {
  if (e instanceof ApiError) {
    return e.requestId ? `${e.message} (ID: ${e.requestId})` : e.message
  }
  return fallback
}

export async function api<T>(
  method: string,
  path: string,
  body?: unknown,
  opts: { auth?: boolean; headers?: Record<string, string>; signal?: AbortSignal; cache?: RequestCache } = { auth: true },
): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json', ...(opts.headers ?? {}) }
  const auth = useAuth()
  const session = auth.session
  const token = auth.token
  if (opts.auth !== false && auth.token) {
    headers.Authorization = `Bearer ${auth.token}`
  }
  const resp = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: opts.signal, cache: opts.cache,
  })
  const data: unknown = await resp.json().catch(() => ({}))
  if (!resp.ok) {
    if (resp.status === 401 && opts.auth !== false && token && auth.token === token && auth.session === session && !opts.signal?.aborted) {
      auth.logout()
    }
    const msg =
      typeof data === 'object' && data !== null && 'error' in data
        ? String((data as { error: unknown }).error)
        : `request failed (${resp.status})`
    const code =
      typeof data === 'object' && data !== null && 'code' in data
        ? String((data as { code: unknown }).code)
        : null
    throw new ApiError(resp.status, msg, resp.headers.get('X-Request-Id'), code)
  }
  return data as T
}

// —— 管理 API（X-Admin-Token）——
// root 级凭证只存 sessionStorage：关标签页即失效，不落 localStorage
const ADMIN_KEY = 'laiai.adminToken'
const adminRevision = ref(0)

export const adminToken = {
  get: () => sessionStorage.getItem(ADMIN_KEY) ?? '',
  revision: readonly(adminRevision),
  set: (v: string) => { sessionStorage.setItem(ADMIN_KEY, v); adminRevision.value++ },
  clear: () => { sessionStorage.removeItem(ADMIN_KEY); adminRevision.value++ },
}

export async function adminApi<T>(method: string, path: string, body?: unknown, opts: { signal?: AbortSignal; cache?: RequestCache } = {}): Promise<T> {
  // 后台访问可由两种身份放行（RBAC 0020）：
  //   1. X-Admin-Token（root 破玻璃）  2. 会话 Bearer + 用户角色≥admin
  // 两者都带上，后端择一通过；普通用户没 admin-token 时靠会话角色进入。
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  const t = adminToken.get()
  if (t) headers['X-Admin-Token'] = t
  const auth = useAuth()
  if (auth.token) headers.Authorization = `Bearer ${auth.token}`
  const resp = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: opts.signal, cache: opts.cache,
  })
  const data: unknown = await resp.json().catch(() => ({}))
  if (!resp.ok) {
    const msg =
      typeof data === 'object' && data !== null && 'error' in data
        ? String((data as { error: unknown }).error)
        : `request failed (${resp.status})`
    const code =
      typeof data === 'object' && data !== null && 'code' in data
        ? String((data as { code: unknown }).code)
        : null
    throw new ApiError(resp.status, msg, resp.headers.get('X-Request-Id'), code)
  }
  return data as T
}

// Raw authenticated GET for bounded response transports. The caller owns body
// decoding; authentication and stale-401 behavior stay shared with this client.
export async function apiFetch(path: string, options: { admin: boolean; signal: AbortSignal }): Promise<Response> {
  const auth = useAuth(), session = auth.session, token = auth.token
  const headers: Record<string, string> = {}
  if (token) headers.Authorization = `Bearer ${token}`
  if (options.admin) {
    const root = adminToken.get()
    if (root) headers['X-Admin-Token'] = root
  }
  const response = await fetch(path, { headers, signal: options.signal, cache: 'no-store' })
  if (response.status === 401 && !options.admin && token && auth.token === token && auth.session === session && !options.signal.aborted) auth.logout()
  return response
}

// Endpoint errors are fixed codes, not arbitrary server/proxy text. In
// particular a gateway HTML failure must never be rendered as a CSV download.
export function apiResponseError(response: Response, data: unknown): ApiError {
  const value = typeof data === 'object' && data !== null ? data as Record<string, unknown> : {}
  const code = typeof value.code === 'string' && /^[a-zA-Z0-9_]{1,80}$/.test(value.code) ? value.code : null
  const requestID = response.headers.get('X-Request-Id')
  const safeID = requestID && /^[a-zA-Z0-9_-]{1,128}$/.test(requestID) ? requestID : null
  const retry = response.headers.get('Retry-After')
  return new ApiError(response.status, 'Request unavailable', safeID, code, retry && /^\d{1,6}$/.test(retry) ? retry : null)
}
