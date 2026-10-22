<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, copyText, errorMessage } from '@/lib/api'
import CurrencyPreference from '@/components/CurrencyPreference.vue'
import QrCode from '@/components/QrCode.vue'

interface Me {
  user_id: number
  email: string
  display_name: string
  org_id: number
  role: string
  backup_codes_remaining?: number
}

const { t } = useI18n()
const me = ref<Me | null>(null)

// 显示名
const displayName = ref('')
const profileBusy = ref(false)
const profileMsg = ref('')
const profileErr = ref('')

// 密码
const oldPw = ref('')
const newPw = ref('')
const newPw2 = ref('')
const pwBusy = ref(false)
const pwMsg = ref('')
const pwErr = ref('')

// —— 2FA ——
const totpEnabled = ref(false)
const setupUri = ref('')
const setupSecret = ref('')
const twofaCode = ref('')
const backupCodes = ref<string[]>([])
const twofaBusy = ref(false)
const twofaErr = ref('')
const twofaMsg = ref('')

async function start2fa() {
  twofaErr.value = ''
  try {
    const r = await api<{ secret: string; otpauth_uri: string }>('POST', '/api/me/2fa/setup')
    setupSecret.value = r.secret
    setupUri.value = r.otpauth_uri
  } catch (e) {
    twofaErr.value = errorMessage(e, t('common.error'))
  }
}
async function enable2fa() {
  twofaBusy.value = true
  twofaErr.value = ''
  try {
    const r = await api<{ backup_codes: string[] }>('POST', '/api/me/2fa/enable', { code: twofaCode.value })
    totpEnabled.value = true
    setupUri.value = ''
    twofaCode.value = ''
    backupCodes.value = r.backup_codes ?? []
    if (me.value) me.value.backup_codes_remaining = backupCodes.value.length // 刷新剩余计数
    twofaMsg.value = t('settings.twofaEnabled')
  } catch (e) {
    twofaErr.value = errorMessage(e, t('common.error'))
  } finally {
    twofaBusy.value = false
  }
}
async function disable2fa() {
  twofaBusy.value = true
  twofaErr.value = ''
  try {
    await api('POST', '/api/me/2fa/disable', { code: twofaCode.value })
    totpEnabled.value = false
    twofaCode.value = ''
    backupCodes.value = []
    if (me.value) me.value.backup_codes_remaining = undefined // 关闭后清零计数，状态一致
    twofaMsg.value = t('settings.twofaDisabled')
  } catch (e) {
    twofaErr.value = errorMessage(e, t('common.error'))
  } finally {
    twofaBusy.value = false
  }
}
async function copyBackup() {
  await copyText(backupCodes.value.join('\n'))
  twofaMsg.value = t('settings.twofaBackupCopied')
}
async function regenBackup() {
  twofaBusy.value = true
  twofaErr.value = ''
  try {
    const r = await api<{ backup_codes: string[] }>('POST', '/api/me/2fa/backup-codes', { code: twofaCode.value })
    twofaCode.value = ''
    backupCodes.value = r.backup_codes ?? []
    if (me.value) me.value.backup_codes_remaining = backupCodes.value.length // 刷新剩余计数
    twofaMsg.value = t('settings.twofaBackupRegen')
  } catch (e) {
    twofaErr.value = errorMessage(e, t('common.error'))
  } finally {
    twofaBusy.value = false
  }
}

// 余额低预警（0042）：org 级阈值，可用余额低于阈值给 owner 发邮件（24h 至多一封）。
// 成员可见；仅 owner/admin 可保存（后端 403 兜底）。
const alertThreshold = ref('')
const alertBusy = ref(false)
const alertMsg = ref('')
const alertErr = ref('')

async function saveBalanceAlert() {
  alertBusy.value = true
  alertMsg.value = ''
  alertErr.value = ''
  try {
    await api('PUT', '/api/org/balance-alert', { low_balance_alert_usd: alertThreshold.value.trim() || '0' })
    alertMsg.value = t('settings.saved')
  } catch (e) {
    alertErr.value = errorMessage(e, t('common.error'))
  } finally {
    alertBusy.value = false
  }
}

// 统一通知订阅（§4.9 Notify）：任务完成/失败/余额低 三事件 × 邮件/webhook 两通道。
const NOTIFY_EVENTS = ['task.completed', 'task.failed', 'balance.low'] as const
const notifyEmail = ref(false)
const notifyWebhookUrl = ref('')
const notifyWebhookSecret = ref('')
const notifyWebhookConfigured = ref(false)
const notifyEvents = ref<Set<string>>(new Set())
const notifyBusy = ref(false)
const notifyMsg = ref('')
const notifyErr = ref('')

function toggleNotifyEvent(ev: string) {
  if (notifyEvents.value.has(ev)) notifyEvents.value.delete(ev)
  else notifyEvents.value.add(ev)
  notifyEvents.value = new Set(notifyEvents.value) // 触发响应式
}

async function saveNotify() {
  notifyBusy.value = true
  notifyMsg.value = ''
  notifyErr.value = ''
  try {
    const body: Record<string, unknown> = {
      email_enabled: notifyEmail.value,
      webhook_url: notifyWebhookUrl.value.trim(),
      events: [...notifyEvents.value],
    }
    if (notifyWebhookSecret.value.trim()) body.webhook_secret = notifyWebhookSecret.value.trim()
    const r = await api<{ webhook_configured: boolean }>('PUT', '/api/org/notify-settings', body)
    notifyWebhookConfigured.value = r.webhook_configured
    notifyWebhookSecret.value = '' // 明文只在提交时用，绝不回显
    notifyMsg.value = t('settings.saved')
  } catch (e) {
    notifyErr.value = errorMessage(e, t('common.error'))
  } finally {
    notifyBusy.value = false
  }
}

onMounted(async () => {
  try {
    const r = await api<Me & { totp_enabled?: boolean }>('GET', '/api/me')
    me.value = r
    displayName.value = r.display_name
    totpEnabled.value = !!r.totp_enabled
  } catch (e) {
    profileErr.value = errorMessage(e, t('common.error'))
  }
  try {
    const a = await api<{ low_balance_alert_usd: string }>('GET', '/api/org/balance-alert')
    if (Number(a.low_balance_alert_usd) > 0) alertThreshold.value = a.low_balance_alert_usd
  } catch {
    // 预警读取失败不阻塞设置页其余部分
  }
  try {
    const nz = await api<{ email_enabled: boolean; webhook_url: string; webhook_configured: boolean; events: string[] }>('GET', '/api/org/notify-settings')
    notifyEmail.value = nz.email_enabled
    notifyWebhookUrl.value = nz.webhook_url || ''
    notifyWebhookConfigured.value = nz.webhook_configured
    notifyEvents.value = new Set(nz.events || [])
  } catch {
    // 通知设置读取失败不阻塞设置页
  }
})

async function saveProfile() {
  profileBusy.value = true
  profileMsg.value = ''
  profileErr.value = ''
  try {
    await api('PATCH', '/api/me', { display_name: displayName.value })
    profileMsg.value = t('settings.saved')
  } catch (e) {
    profileErr.value = errorMessage(e, t('common.error'))
  } finally {
    profileBusy.value = false
  }
}

async function changePassword() {
  pwMsg.value = ''
  pwErr.value = ''
  if (newPw.value !== newPw2.value) {
    pwErr.value = t('settings.pwMismatch')
    return
  }
  pwBusy.value = true
  try {
    await api('POST', '/api/me/password', { old_password: oldPw.value, new_password: newPw.value })
    pwMsg.value = t('settings.pwChanged')
    oldPw.value = newPw.value = newPw2.value = ''
  } catch (e) {
    pwErr.value = errorMessage(e, t('common.error'))
  } finally {
    pwBusy.value = false
  }
}
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('surface.settingsEyebrow') }}</p>
        <h1 class="app-page-title">{{ t('settings.title') }}</h1>
        <p class="app-page-subtitle">{{ t('surface.settingsSubtitle') }}</p>
      </div>
    </div>

    <CurrencyPreference />
    <section class="grid gap-4 lg:grid-cols-[0.8fr_1.2fr]">
    <!-- 账户信息 -->
    <div class="app-panel-strong p-6">
      <h2 class="mb-4 font-semibold">{{ t('settings.account') }}</h2>
      <dl class="grid grid-cols-2 gap-y-3 text-sm">
        <dt class="text-muted">{{ t('auth.email') }}</dt>
        <dd class="font-mono">{{ me?.email }}</dd>
        <dt class="text-muted">{{ t('settings.userId') }}</dt>
        <dd class="font-mono">{{ me?.user_id }}</dd>
        <dt class="text-muted">{{ t('settings.orgId') }}</dt>
        <dd class="font-mono">{{ me?.org_id }}</dd>
        <dt class="text-muted">{{ t('settings.identity') }}</dt>
        <dd>
          <span
            class="rounded px-2 py-0.5 text-xs font-medium"
            :class="me?.role === 'superadmin' || me?.role === 'admin' ? 'bg-accent/15 text-accent' : 'bg-elev text-muted'"
          >{{ t(`admin.role_${me?.role || 'user'}`) }}</span>
        </dd>
      </dl>
    </div>

    <!-- 显示名 -->
    <form class="app-panel p-6 space-y-4" @submit.prevent="saveProfile">
      <h2 class="font-semibold">{{ t('settings.profile') }}</h2>
      <div>
        <label class="label" for="dn">{{ t('settings.displayName') }}</label>
        <input id="dn" v-model="displayName" class="input max-w-sm" :placeholder="t('settings.displayNamePlaceholder')" />
      </div>
      <p v-if="profileMsg" class="text-sm text-ok" role="status">{{ profileMsg }}</p>
      <p v-if="profileErr" class="text-sm text-danger" role="alert">{{ profileErr }}</p>
      <button class="btn-primary" :disabled="profileBusy">{{ t('settings.save') }}</button>
    </form>
    </section>

    <!-- 余额低预警（org 级通知，0042） -->
    <form class="app-panel p-6 space-y-4" @submit.prevent="saveBalanceAlert">
      <h2 class="font-semibold">{{ t('settings.alertTitle') }}</h2>
      <p class="text-sm text-muted">{{ t('settings.alertDesc') }}</p>
      <div>
        <label class="label" for="alert-th">{{ t('settings.alertLabel') }}</label>
        <input id="alert-th" v-model="alertThreshold" class="input max-w-sm" inputmode="decimal" placeholder="0" />
        <p class="mt-1 text-xs text-muted">{{ t('settings.alertHint') }}</p>
      </div>
      <p v-if="alertMsg" class="text-sm text-ok" role="status">{{ alertMsg }}</p>
      <p v-if="alertErr" class="text-sm text-danger" role="alert">{{ alertErr }}</p>
      <button class="btn-primary" :disabled="alertBusy">{{ t('common.confirm') }}</button>
    </form>

    <!-- 统一通知订阅（§4.9 Notify）：事件 × 通道 -->
    <form class="app-panel p-6 space-y-4" @submit.prevent="saveNotify">
      <h2 class="font-semibold">{{ t('settings.notifyTitle') }}</h2>
      <p class="text-sm text-muted">{{ t('settings.notifyDesc') }}</p>

      <div class="space-y-2">
        <label class="label">{{ t('settings.notifyEvents') }}</label>
        <label v-for="ev in NOTIFY_EVENTS" :key="ev" class="flex items-center gap-2 text-sm">
          <input type="checkbox" :checked="notifyEvents.has(ev)" @change="toggleNotifyEvent(ev)" />
          <span>{{ t(`settings.notifyEvent_${ev.replace('.', '_')}`) }}</span>
        </label>
      </div>

      <label class="flex items-center gap-2 text-sm">
        <input v-model="notifyEmail" type="checkbox" />
        <span>{{ t('settings.notifyEmailChannel') }}</span>
      </label>

      <div>
        <label class="label" for="notify-wh">{{ t('settings.notifyWebhookUrl') }}</label>
        <input id="notify-wh" v-model="notifyWebhookUrl" class="input max-w-xl font-mono" placeholder="https://your-app.example.com/webhooks/laiai" />
        <p class="mt-1 text-xs text-muted">{{ t('settings.notifyWebhookHint') }}</p>
      </div>
      <div v-if="notifyWebhookUrl.trim()">
        <label class="label" for="notify-secret">{{ t('settings.notifyWebhookSecret') }}</label>
        <input id="notify-secret" v-model="notifyWebhookSecret" class="input max-w-xl font-mono" type="password"
               :placeholder="notifyWebhookConfigured ? t('settings.notifyWebhookSecretSet') : t('settings.notifyWebhookSecretPlaceholder')" />
        <p class="mt-1 text-xs text-muted">{{ t('settings.notifyWebhookSecretHint') }}</p>
      </div>

      <p v-if="notifyMsg" class="text-sm text-ok" role="status">{{ notifyMsg }}</p>
      <p v-if="notifyErr" class="text-sm text-danger" role="alert">{{ notifyErr }}</p>
      <button class="btn-primary" :disabled="notifyBusy">{{ t('common.confirm') }}</button>
    </form>

    <section class="grid gap-4 lg:grid-cols-2">
    <!-- 改密码 -->
    <form class="app-panel p-6 space-y-4" @submit.prevent="changePassword">
      <h2 class="font-semibold">{{ t('settings.password') }}</h2>
      <div>
        <label class="label" for="op">{{ t('settings.currentPassword') }}</label>
        <input id="op" v-model="oldPw" class="input max-w-sm" type="password" required autocomplete="current-password" />
      </div>
      <div>
        <label class="label" for="np">{{ t('settings.newPassword') }}</label>
        <input id="np" v-model="newPw" class="input max-w-sm" type="password" required minlength="8" autocomplete="new-password" />
      </div>
      <div>
        <label class="label" for="np2">{{ t('settings.confirmPassword') }}</label>
        <input id="np2" v-model="newPw2" class="input max-w-sm" type="password" required minlength="8" autocomplete="new-password" />
      </div>
      <p v-if="pwMsg" class="text-sm text-ok" role="status">{{ pwMsg }}</p>
      <p v-if="pwErr" class="text-sm text-danger" role="alert">{{ pwErr }}</p>
      <button class="btn-primary" :disabled="pwBusy">{{ t('settings.changePassword') }}</button>
    </form>

    <!-- 2FA -->
    <div class="app-panel p-6 space-y-4">
      <div class="flex items-center justify-between">
        <h2 class="font-semibold">{{ t('settings.twofa') }}</h2>
        <span :class="totpEnabled ? 'badge-ok' : 'badge-muted'">
          {{ totpEnabled ? t('settings.twofaOn') : t('settings.twofaOff') }}
        </span>
      </div>
      <p class="text-sm text-muted">{{ t('settings.twofaDesc') }}</p>
      <p v-if="twofaMsg" class="text-sm text-ok" role="status">{{ twofaMsg }}</p>
      <p v-if="twofaErr" class="text-sm text-danger" role="alert">{{ twofaErr }}</p>

      <!-- 未启用：setup 流程 -->
      <template v-if="!totpEnabled">
        <button v-if="!setupUri" class="btn-primary" @click="start2fa">{{ t('settings.twofaSetup') }}</button>
        <div v-else class="space-y-4">
          <p class="text-sm">{{ t('settings.twofaScan') }}</p>
          <QrCode :value="setupUri" :size="180" />
          <p class="text-xs text-muted">{{ t('settings.twofaManual') }} <code class="select-all font-mono">{{ setupSecret }}</code></p>
          <div class="flex gap-2">
            <input v-model="twofaCode" class="input max-w-[10rem] font-mono" inputmode="numeric" maxlength="6" :placeholder="t('auth.verifyCode')" />
            <button class="btn-primary shrink-0" :disabled="twofaBusy" @click="enable2fa">{{ t('settings.twofaEnable') }}</button>
          </div>
        </div>
      </template>

      <!-- 已启用：禁用 + 重置备用码 -->
      <div v-else class="space-y-2">
        <div class="flex flex-wrap gap-2">
          <input v-model="twofaCode" class="input max-w-[10rem] font-mono" inputmode="numeric" maxlength="6" :placeholder="t('auth.verifyCode')" />
          <button class="btn-danger shrink-0" :disabled="twofaBusy" @click="disable2fa">{{ t('settings.twofaDisable') }}</button>
          <button class="btn-ghost shrink-0" :disabled="twofaBusy" @click="regenBackup">{{ t('settings.twofaBackupRegenBtn') }}</button>
        </div>
        <p class="text-xs text-muted">{{ t('settings.twofaBackupHint') }}</p>
        <p
          v-if="me && me.backup_codes_remaining !== undefined"
          class="text-xs"
          :class="me.backup_codes_remaining <= 2 ? 'text-danger' : 'text-muted'"
        >
          {{ t('settings.twofaBackupLeft', { n: me.backup_codes_remaining }) }}<span v-if="me.backup_codes_remaining <= 2"> · {{ t('settings.twofaBackupLow') }}</span>
        </p>
      </div>

      <!-- 备用码（启用/重置后一次性显示，离开即消失） -->
      <div v-if="backupCodes.length" class="rounded-lg border border-line bg-elev/40 p-4 space-y-2">
        <p class="text-sm font-medium text-ink">{{ t('settings.twofaBackupTitle') }}</p>
        <p class="text-xs text-danger">{{ t('settings.twofaBackupWarn') }}</p>
        <div class="grid grid-cols-2 gap-1.5">
          <code v-for="c in backupCodes" :key="c" class="select-all rounded bg-panel px-2 py-1 text-center font-mono text-sm">{{ c }}</code>
        </div>
        <button class="btn-ghost !py-1 text-xs" @click="copyBackup">{{ t('settings.twofaBackupCopy') }}</button>
      </div>
    </div>
    </section>
  </div>
</template>
