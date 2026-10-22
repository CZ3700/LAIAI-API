import { defineStore } from 'pinia'
import { validRate, type Currency } from '@/lib/decimalMoney'

async function request(path: string, token: string, body?: unknown) {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), 10000)
  try {
    const response = await fetch(path, { method: body ? 'PUT' : 'GET', cache: 'no-store', signal: controller.signal,
      headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      ...(body ? { body: JSON.stringify(body) } : {}) })
    if (!response.ok) throw new Error(`Currency settings unavailable (${response.status})`)
    return await response.json()
  } finally { clearTimeout(timeout) }
}

// No preference cache: the authenticated user record is authoritative. Request
// epochs prevent an earlier account/org response from overwriting the new one.
export const useCurrency = defineStore('currency', {
  state: () => ({ displayCurrency: 'USD' as Currency, cnyPerUsd: '7.3', ready: false, available: false,
    failed: false, saving: false, userId: 0, sessionToken: '', epoch: 0 }),
  getters: { effectiveCurrency: (s): Currency => s.available && validRate(s.cnyPerUsd) ? s.displayCurrency : 'USD' },
  actions: {
    async load(token = '') {
      if (this.saving && token === this.sessionToken) return
      const changed = token !== this.sessionToken
      const current = ++this.epoch
      this.sessionToken = token
      if (changed) { this.ready = false; this.available = false; this.displayCurrency = 'USD'; this.userId = 0; this.saving = false }
      try {
        const [config, me] = await Promise.all([request('/api/config', ''), token ? request('/api/me', token) : Promise.resolve(null)])
        if (current !== this.epoch) return
        this.cnyPerUsd = config.cny_per_usd
        this.displayCurrency = me?.display_currency === 'CNY' ? 'CNY' : 'USD'
        this.userId = me?.user_id ?? 0
        this.available = config.currency_display_enabled === true && me?.currency_display_available === true && validRate(this.cnyPerUsd)
        this.failed = false
      } catch {
        if (current !== this.epoch) return
        this.available = false; this.failed = true
      } finally { if (current === this.epoch) this.ready = true }
    },
    async save(currency: Currency) {
      if (!this.sessionToken || !this.available || this.saving) throw new Error('Currency preference unavailable')
      const current = ++this.epoch, token = this.sessionToken
      this.saving = true
      try {
        const r = await request('/api/me/preferences', token, { display_currency: currency })
        if (current !== this.epoch || token !== this.sessionToken) return
        this.displayCurrency = r.display_currency === 'CNY' ? 'CNY' : 'USD'
        this.available = r.currency_display_available === true
      } finally { if (current === this.epoch) this.saving = false }
    },
  },
})
