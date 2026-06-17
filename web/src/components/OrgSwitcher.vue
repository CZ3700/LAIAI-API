<script setup lang="ts">
// org 切换器：切换 = 服务端签发该 org 的新 token → 整页刷新（所有页面数据随 org 重载，
// 比逐页响应式重拉简单且不会漏——org 上下文变更是全局事件）
import { onBeforeUnmount, onMounted, ref, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'
import { useAuth } from '@/stores/auth'

interface Org {
  org_id: number
  name: string
  kind: number
  role: string
}

const { t } = useI18n()
const auth = useAuth()
const orgs = ref<Org[]>([])
const showCreate = ref(false)
const newName = ref('')
const busy = ref(false)
const error = ref('')

async function load() {
  try {
    const r = await api<{ orgs: Org[] }>('GET', '/api/orgs')
    orgs.value = r.orgs
  } catch {
    /* 列表失败不阻塞 Shell */
  }
}

async function onSwitch(event: Event) {
  const orgId = Number((event.target as HTMLSelectElement).value)
  if (orgId === auth.orgId) return
  try {
    await auth.switchOrg(orgId)
    location.reload()
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
    // 切换失败：把原生 select 的显示值复位回真实 orgId。:value 是单向绑定且 auth.orgId 未变，
    // 不会自动回退，否则下拉框会停在「看似已切换实则没切」的错误选项上。
    ;(event.target as HTMLSelectElement).value = String(auth.orgId)
  }
}

async function createTeam() {
  busy.value = true
  try {
    const r = await api<{ org_id: number }>('POST', '/api/orgs', { name: newName.value })
    showCreate.value = false
    newName.value = ''
    await auth.switchOrg(r.org_id)
    location.reload()
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    busy.value = false
  }
}

function orgLabel(o: Org): string {
  return o.kind === 0 ? `${t('team.personal')} · ${o.name}` : o.name
}

// 创建弹窗：焦点管理 + Escape 关闭
const createDialog = ref<HTMLElement | null>(null)
let lastFocused: HTMLElement | null = null
watch(showCreate, async (open) => {
  if (open) {
    lastFocused = document.activeElement as HTMLElement
    await nextTick()
    createDialog.value?.querySelector<HTMLElement>('input')?.focus()
  } else {
    lastFocused?.focus()
    lastFocused = null
  }
})
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && showCreate.value) showCreate.value = false
}
onMounted(() => {
  load()
  document.addEventListener('keydown', onKeydown)
})
onBeforeUnmount(() => document.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div class="space-y-1.5">
    <label for="org-switcher" class="block px-2 text-xs text-muted">{{ t('team.switcher') }}</label>
    <div class="flex gap-1">
      <select id="org-switcher" class="input !py-1.5 text-xs flex-1 cursor-pointer" :value="auth.orgId" @change="onSwitch">
        <option v-for="o in orgs" :key="o.org_id" :value="o.org_id">{{ orgLabel(o) }}</option>
      </select>
      <button
        class="btn-ghost !px-2.5 !py-1.5 text-xs shrink-0"
        :title="t('team.newTeam')"
        :aria-label="t('team.newTeam')"
        @click="showCreate = true"
      >
        <span aria-hidden="true">＋</span>
      </button>
    </div>
    <p v-if="error" class="px-2 text-xs text-danger" role="alert">{{ error }}</p>

    <div v-if="showCreate" class="modal-backdrop" @click.self="showCreate = false">
      <form
        ref="createDialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="new-team-title"
        class="card modal-panel w-full max-w-sm p-6 space-y-4"
        @submit.prevent="createTeam"
      >
        <h2 id="new-team-title" class="font-semibold">{{ t('team.newTeam') }}</h2>
        <div>
          <label class="label" for="teamname">{{ t('team.teamName') }}</label>
          <input id="teamname" v-model="newName" class="input" required maxlength="64" />
        </div>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn-ghost" @click="showCreate = false">{{ t('common.cancel') }}</button>
          <button class="btn-primary" :disabled="busy">{{ t('team.create') }}</button>
        </div>
      </form>
    </div>
  </div>
</template>
