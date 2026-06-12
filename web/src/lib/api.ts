// API client：统一错误封套 + 401 自动登出（红线：UI 不自行解释资金语义，只展示后端事实）
import { useAuth } from '@/stores/auth'

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
  }
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
    throw new ApiError(resp.status, msg)
  }
  return data as T
}
