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
// 我收到的邀请（被邀请人视角）
interface MyInvite {
  id: number
  org_id: number
  org_name: string
  role: string
  inviter_email: string
  created_at: number
}
// 本团队已发出、待确认的邀请（管理者视角）
interface OrgInvite {
  id: number
  email: string
  role: string
  inviter_email: string
  created_at: number
}

const { t } = useI18n()
const auth = useAuth()
const members = ref<Member[]>([])
const myInvites = ref<MyInvite[]>([])
const orgInvites = ref<OrgInvite[]>([])
const myRole = ref('')
const loading = ref(true)
const error = ref('')
const notice = ref('')

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
    // /api/invitations 永远拉（任何成员都可能收到邀请）；成员列表/组织列表照旧。
    const [m, o, inv] = await Promise.all([
      api<{ members: Member[] }>('GET', '/api/org/members'),
      api<{ orgs: Org[] }>('GET', '/api/orgs'),
      api<{ invitations: MyInvite[] }>('GET', '/api/invitations'),
    ])
    members.value = m.members
    myInvites.value = inv.invitations ?? []
    myRole.value = o.orgs.find((x) => x.org_id === auth.orgId)?.role ?? ''
    // 管理者再拉本团队待确认邀请
    if (canManage.value) {
      const oi = await api<{ invitations: OrgInvite[] }>('GET', '/api/org/invitations')
      orgInvites.value = oi.invitations ?? []
    } else {
      orgInvites.value = []
    }
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    loading.value = false
  }
}

// 发出邀请（后端落 pending，对方确认后才入队）
async function inviteMember() {
  adding.value = true
  try {
    await api('POST', '/api/org/members', { email: addEmail.value, role: addRole.value })
    addEmail.value = ''
    notice.value = t('team.inviteSent')
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

// 管理者撤销自己团队发出的待确认邀请
async function revokeInvite(id: number) {
  if (!confirm(t('team.revokeConfirm'))) return
  try {
    await api('DELETE', `/api/org/invitations/${id}`)
    await load()
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  }
}

// 被邀请人接受 → 入队
async function acceptInvite(inv: MyInvite) {
  try {
    await api('POST', `/api/invitations/${inv.id}/accept`)
    notice.value = t('team.joined', { name: inv.org_name })
    await load()
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  }
}

// 被邀请人拒绝
async function declineInvite(inv: MyInvite) {
  try {
    await api('POST', `/api/invitations/${inv.id}/decline`)
    await load()
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  }
}

onMounted(load)
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('surface.teamEyebrow') }}</p>
        <h1 class="app-page-title">{{ t('team.title') }}</h1>
        <p class="app-page-subtitle">{{ t('team.addHint') }}</p>
      </div>
    </div>

    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <p v-if="notice" class="text-sm text-accent" role="status">{{ notice }}</p>
    <div v-if="loading" class="app-panel p-5 text-sm text-muted">{{ t('common.loading') }}</div>

    <template v-else>
      <!-- 我收到的邀请：需本人确认才会加入团队（红线：未经同意不得拉人入队） -->
      <section v-if="myInvites.length" class="app-panel p-5">
        <h2 class="mb-3 text-sm font-semibold">{{ t('team.myInvites') }}</h2>
        <ul class="grid gap-2">
          <li
            v-for="inv in myInvites"
            :key="inv.id"
            class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-line px-4 py-3"
          >
            <span class="text-sm">
              {{ t('team.inviteFrom', { team: inv.org_name, role: roleLabel(inv.role) }) }}
              <span class="text-muted">· {{ inv.inviter_email }}</span>
            </span>
            <div class="flex gap-2">
              <button class="btn-primary !py-1 text-xs" @click="acceptInvite(inv)">{{ t('team.accept') }}</button>
              <button class="btn-ghost !py-1 text-xs" @click="declineInvite(inv)">{{ t('team.decline') }}</button>
            </div>
          </li>
        </ul>
      </section>

      <form v-if="canManage" class="app-panel grid gap-3 p-5 md:grid-cols-[1fr_auto_auto] md:items-end" @submit.prevent="inviteMember">
        <div class="flex-1">
          <label class="label" for="memail">{{ t('team.email') }}</label>
          <input id="memail" v-model="addEmail" class="input" type="email" required />
        </div>
        <div>
          <label class="label" for="mrole">{{ t('team.role') }}</label>
          <select id="mrole" v-model="addRole" class="input !w-auto cursor-pointer">
            <option value="member">{{ t('team.roleMember') }}</option>
            <option value="admin">{{ t('team.roleAdmin') }}</option>
            <option value="billing">{{ t('team.roleBilling') }}</option>
          </select>
        </div>
        <button class="btn-primary" :disabled="adding">{{ t('team.invite') }}</button>
      </form>

      <div class="data-table-wrap">
        <table class="table w-full text-sm">
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

      <!-- 管理者：本团队已发出、等待对方确认的邀请 -->
      <section v-if="canManage && orgInvites.length" class="app-panel p-5">
        <h2 class="mb-3 text-sm font-semibold">{{ t('team.pendingInvites') }}</h2>
        <div class="data-table-wrap">
          <table class="table w-full text-sm">
            <thead>
              <tr class="border-b border-line text-left text-muted">
                <th class="px-4 py-3 font-medium">{{ t('team.email') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('team.role') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('team.invitedBy') }}</th>
                <th class="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="inv in orgInvites" :key="inv.id" class="border-b border-line last:border-0">
                <td class="px-4 py-3">{{ inv.email }}</td>
                <td class="px-4 py-3">{{ roleLabel(inv.role) }}</td>
                <td class="px-4 py-3 text-muted">{{ inv.inviter_email }}</td>
                <td class="px-4 py-3 text-right">
                  <button class="btn-ghost !py-1 text-xs" @click="revokeInvite(inv.id)">{{ t('team.revoke') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>
  </div>
</template>
