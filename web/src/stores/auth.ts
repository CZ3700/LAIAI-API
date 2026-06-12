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
  state: () => ({ session: loadSession() }),
  getters: {
    token: (s) => s.session?.token ?? '',
    email: (s) => s.session?.email ?? '',
    isAuthed: (s) => s.session !== null,
  },
  actions: {
    async login(email: string, password: string) {
      const r = await api<{ token: string; expires_at: number; org_id: number }>(
        'POST',
        '/api/auth/login',
        { email, password },
        { auth: false },
      )
      this.session = { token: r.token, expiresAt: r.expires_at, orgId: r.org_id, email }
      localStorage.setItem(STORAGE_KEY, JSON.stringify(this.session))
    },
    async register(email: string, password: string) {
      await api('POST', '/api/auth/register', { email, password }, { auth: false })
      await this.login(email, password)
    },
    logout() {
      this.session = null
      localStorage.removeItem(STORAGE_KEY)
      // 避免循环依赖，用 location 而非 router 实例
      if (!location.pathname.startsWith('/login')) location.href = '/login'
    },
  },
})
