<script setup lang="ts">
// org 切换器：切换 = 服务端签发该 org 的新 token → 整页刷新（所有页面数据随 org 重载，
// 比逐页响应式重拉简单且不会漏——org 上下文变更是全局事件）
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, ApiError } from '@/lib/api'
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
    error.value = e instanceof ApiError ? e.message : t('common.error')
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
    error.value = e instanceof ApiError ? e.message : t('common.error')
  } finally {
    busy.value = false
  }
}

function orgLabel(o: Org): string {
  return o.kind === 0 ? `${t('team.personal')} · ${o.name}` : o.name
}

onMounted(load)
</script>

<template>
  <div class="space-y-1.5">
    <label class="px-2 text-xs text-muted">{{ t('team.switcher') }}</label>
    <div class="flex gap-1">
      <select class="input !py-1.5 text-xs flex-1 cursor-pointer" :value="auth.orgId" @change="onSwitch">
        <option v-for="o in orgs" :key="o.org_id" :value="o.org_id">{{ orgLabel(o) }}</option>
      </select>
      <button class="btn-ghost !px-2.5 !py-1.5 text-xs shrink-0" :title="t('team.newTeam')" @click="showCreate = true">＋</button>
    </div>
    <p v-if="error" class="px-2 text-xs text-danger" role="alert">{{ error }}</p>

    <div v-if="showCreate" class="fixed inset-0 z-50 grid place-items-center bg-black/60 p-4" @click.self="showCreate = false">
      <form class="card w-full max-w-sm p-6 space-y-4" @submit.prevent="createTeam">
        <h2 class="font-semibold">{{ t('team.newTeam') }}</h2>
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
