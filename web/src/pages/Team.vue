<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, errorMessage } from '@/lib/api'
import { useAuth } from '@/stores/auth'

interface Member {
  user_id: number
  email: string
  role: string
}
interface Org {
  org_id: number
  name: string
  kind: number
  role: string
}

const { t } = useI18n()
const auth = useAuth()
const members = ref<Member[]>([])
const myRole = ref('')
const loading = ref(true)
const error = ref('')

const addEmail = ref('')
const addRole = ref<'member' | 'admin' | 'billing'>('member')
const adding = ref(false)

// 角色感知 UX：member 角色看不到管理控件（而非点了才 403）
const canManage = computed(() => myRole.value === 'owner' || myRole.value === 'admin')

function roleLabel(role: string): string {
  const map: Record<string, string> = {
    owner: t('team.roleOwner'),
    admin: t('team.roleAdmin'),
    member: t('team.roleMember'),
    billing: t('team.roleBilling'),
  }
  return map[role] ?? role
}

async function load() {
  loading.value = true
  try {
    const [m, o] = await Promise.all([
      api<{ members: Member[] }>('GET', '/api/org/members'),
      api<{ orgs: Org[] }>('GET', '/api/orgs'),
    ])
    members.value = m.members
    myRole.value = o.orgs.find((x) => x.org_id === auth.orgId)?.role ?? ''
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    loading.value = false
  }
}

async function addMember() {
  adding.value = true
  try {
    await api('POST', '/api/org/members', { email: addEmail.value, role: addRole.value })
    addEmail.value = ''
    await load()
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    adding.value = false
  }
}

async function removeMember(userID: number) {
  if (!confirm(t('team.removeConfirm'))) return
  try {
    await api('DELETE', `/api/org/members/${userID}`)
    await load()
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  }
}

onMounted(load)
</script>

<template>
  <div class="max-w-3xl space-y-6">
    <h1 class="text-xl font-semibold">{{ t('team.title') }}</h1>

    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <p v-if="loading" class="text-sm text-muted">{{ t('common.loading') }}</p>

    <template v-else>
      <form v-if="canManage" class="card p-5 flex items-end gap-3" @submit.prevent="addMember">
        <div class="flex-1">
          <label class="label" for="memail">{{ t('team.email') }}</label>
          <input id="memail" v-model="addEmail" class="input" type="email" required />
          <p class="mt-1 text-xs text-muted">{{ t('team.addHint') }}</p>
        </div>
        <div>
          <label class="label" for="mrole">{{ t('team.role') }}</label>
          <select id="mrole" v-model="addRole" class="input !w-auto cursor-pointer">
            <option value="member">{{ t('team.roleMember') }}</option>
            <option value="admin">{{ t('team.roleAdmin') }}</option>
            <option value="billing">{{ t('team.roleBilling') }}</option>
          </select>
        </div>
        <button class="btn-primary mb-5" :disabled="adding">{{ t('team.add') }}</button>
      </form>

      <div class="card overflow-hidden">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3 font-medium">{{ t('team.email') }}</th>
              <th class="px-4 py-3 font-medium">{{ t('team.role') }}</th>
              <th class="px-4 py-3"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in members" :key="m.user_id" class="border-b border-line last:border-0">
              <td class="px-4 py-3">{{ m.email }}</td>
              <td class="px-4 py-3">
                <span :class="m.role === 'owner' ? 'text-accent' : ''">{{ roleLabel(m.role) }}</span>
              </td>
              <td class="px-4 py-3 text-right">
                <button
                  v-if="canManage && m.role !== 'owner'"
                  class="btn-danger !py-1 text-xs"
                  @click="removeMember(m.user_id)"
                >
                  {{ t('team.remove') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>
