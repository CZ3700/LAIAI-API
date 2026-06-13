import { defineStore } from 'pinia'
import { api } from '@/lib/api'

interface Session {
  token: string
  expiresAt: number // unix 秒
  orgId: number
  email: string
}

const STORAGE_KEY = 'laiai.session'

function loadSession(): Session | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const s = JSON.parse(raw) as Session
    if (!s.token || s.expiresAt * 1000 <= Date.now()) {
      localStorage.removeItem(STORAGE_KEY)
      return null
    }
    return s
  } catch {
    return null
  }
}

export const useAuth = defineStore('auth', {
  state: () => ({ session: loadSession(), pending2fa: null as { token: string; email: string } | null }),
  getters: {
    token: (s) => s.session?.token ?? '',
    email: (s) => s.session?.email ?? '',
    orgId: (s) => s.session?.orgId ?? 0,
    isAuthed: (s) => s.session !== null,
  },
  actions: {
    // 返回 '2fa' 表示需要二阶段验证（此时 token 是中间票，调 verify2fa 完成）
    async login(email: string, password: string): Promise<'ok' | '2fa'> {
      const r = await api<{ token: string; expires_at?: number; org_id?: number; requires_2fa?: boolean }>(
        'POST',
        '/api/auth/login',
        { email, password },
        { auth: false },
      )
      if (r.requires_2fa) {
        this.pending2fa = { token: r.token, email }
        return '2fa'
      }
      this.session = { token: r.token, expiresAt: r.expires_at!, orgId: r.org_id!, email }
      localStorage.setItem(STORAGE_KEY, JSON.stringify(this.session))
      return 'ok'
    },
    async verify2fa(code: string) {
      if (!this.pending2fa) throw new Error('no pending 2fa')
      const r = await api<{ token: string; expires_at: number; org_id: number }>(
        'POST',
        '/api/auth/2fa',
        { token: this.pending2fa.token, code },
        { auth: false },
      )
      this.session = { token: r.token, expiresAt: r.expires_at, orgId: r.org_id, email: this.pending2fa.email }
      localStorage.setItem(STORAGE_KEY, JSON.stringify(this.session))
      this.pending2fa = null
    },
    async register(email: string, password: string, code?: string, inviterCode?: string) {
      const body: Record<string, string> = { email, password }
      if (code) body.verification_code = code
      if (inviterCode) body.inviter_code = inviterCode
      await api('POST', '/api/auth/register', body, { auth: false })
      await this.login(email, password)
    },
    // org 切换：服务端验证成员资格后签发的新 token 落地到会话
    async switchOrg(orgId: number) {
      const r = await api<{ token: string; expires_at: number; org_id: number }>(
        'POST',
        '/api/auth/switch',
        { org_id: orgId },
      )
      if (this.session) {
        this.session = { ...this.session, token: r.token, expiresAt: r.expires_at, orgId: r.org_id }
        localStorage.setItem(STORAGE_KEY, JSON.stringify(this.session))
      }
    },
    logout() {
      this.session = null
      localStorage.removeItem(STORAGE_KEY)
      // 避免循环依赖，用 location 而非 router 实例
      if (!location.pathname.startsWith('/login')) location.href = '/login'
    },
  },
})
