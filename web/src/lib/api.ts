// API client：统一错误封套 + 401 自动登出（红线：UI 不自行解释资金语义，只展示后端事实）
import { useAuth } from '@/stores/auth'

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    // 后端在每个响应上回传的 X-Request-Id：报错时透出给用户，便于工单溯源
    public requestId: string | null = null,
  ) {
    super(message)
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
  opts: { auth?: boolean } = { auth: true },
): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  const auth = useAuth()
  if (opts.auth !== false && auth.token) {
    headers.Authorization = `Bearer ${auth.token}`
  }
  const resp = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const data: unknown = await resp.json().catch(() => ({}))
  if (!resp.ok) {
    if (resp.status === 401 && opts.auth !== false) {
      auth.logout()
    }
    const msg =
      typeof data === 'object' && data !== null && 'error' in data
        ? String((data as { error: unknown }).error)
        : `request failed (${resp.status})`
    throw new ApiError(resp.status, msg, resp.headers.get('X-Request-Id'))
  }
  return data as T
}

// —— 管理 API（X-Admin-Token）——
// root 级凭证只存 sessionStorage：关标签页即失效，不落 localStorage
const ADMIN_KEY = 'laiai.adminToken'

export const adminToken = {
  get: () => sessionStorage.getItem(ADMIN_KEY) ?? '',
  set: (v: string) => sessionStorage.setItem(ADMIN_KEY, v),
  clear: () => sessionStorage.removeItem(ADMIN_KEY),
}

export async function adminApi<T>(method: string, path: string, body?: unknown): Promise<T> {
  const resp = await fetch(path, {
    method,
    headers: {
      'Content-Type': 'application/json',
      'X-Admin-Token': adminToken.get(),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const data: unknown = await resp.json().catch(() => ({}))
  if (!resp.ok) {
    const msg =
      typeof data === 'object' && data !== null && 'error' in data
        ? String((data as { error: unknown }).error)
        : `request failed (${resp.status})`
    throw new ApiError(resp.status, msg, resp.headers.get('X-Request-Id'))
  }
  return data as T
}
