<script setup lang="ts">
import { ref } from 'vue'

const tab = ref<'apply' | 'records'>('apply')
const mode = ref<'recharge' | 'usage'>('recharge')
const titleType = ref<'personal' | 'enterprise'>('personal')
const amount = ref('0.00')
const email = ref('')
const invoiceType = ref('增值税普通发票')
const name = ref('')
const taxNo = ref('')
const remark = ref('')
const notice = ref('')

function fullAmount() {
  amount.value = '0.00'
}
function submit() {
  notice.value = '开票申请接口尚未接入；当前页面仅完成控制台 UI 和交互态对齐。'
}
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <h1 class="app-page-title">开票管理</h1>
      </div>
    </div>

    <div class="border-b border-line">
      <div class="tabs-row">
        <button type="button" :class="{ active: tab === 'apply' }" @click="tab = 'apply'">申请开票</button>
        <button type="button" :class="{ active: tab === 'records' }" @click="tab = 'records'">开票记录</button>
      </div>
    </div>

    <form v-if="tab === 'apply'" class="max-w-[44rem] space-y-5" @submit.prevent="submit">
      <div class="filter-group">
        <label>开票方式</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: mode === 'recharge' }" @click="mode = 'recharge'">按充值</button>
          <button type="button" :class="{ active: mode === 'usage' }" @click="mode = 'usage'">按消耗</button>
        </div>
        <p class="mt-2 text-sm text-muted">总额: $0.00 · 已开: $0.00 · 可开: $0.00</p>
      </div>

      <div>
        <label class="label">开票金额 *</label>
        <div class="flex gap-2">
          <input v-model="amount" class="input" placeholder="请输入开票金额" />
          <button class="btn-ghost shrink-0" type="button" @click="fullAmount">全额开票</button>
        </div>
        <p class="mt-1 text-xs text-muted">金额不低于 $1.00</p>
      </div>

      <div>
        <label class="label">收票邮箱 *</label>
        <input v-model="email" class="input" type="email" placeholder="请输入收票邮箱" />
      </div>

      <div class="filter-group">
        <label>抬头类型</label>
        <div class="segmented-control w-full justify-start">
          <button type="button" :class="{ active: titleType === 'personal' }" @click="titleType = 'personal'">个人</button>
          <button type="button" :class="{ active: titleType === 'enterprise' }" @click="titleType = 'enterprise'">企业</button>
        </div>
      </div>

      <div>
        <label class="label">发票类型</label>
        <input v-model="invoiceType" class="input" disabled />
      </div>

      <template v-if="titleType === 'personal'">
        <div>
          <label class="label">姓名 *</label>
          <input v-model="name" class="input" placeholder="请输入姓名" />
        </div>
        <div>
          <label class="label">身份证号 *</label>
          <input v-model="taxNo" class="input" placeholder="请输入身份证号" />
        </div>
      </template>
      <template v-else>
        <div>
          <label class="label">发票抬头 *</label>
          <input v-model="name" class="input" placeholder="请输入发票抬头" />
        </div>
        <div>
          <label class="label">纳税人识别号 *</label>
          <input v-model="taxNo" class="input" placeholder="请输入纳税人识别号" />
        </div>
      </template>

      <div>
        <label class="label">备注</label>
        <textarea v-model="remark" class="input min-h-24" placeholder="选填备注信息"></textarea>
      </div>

      <p v-if="notice" class="inline-alert">{{ notice }}</p>
      <button class="btn-primary w-full" type="submit">提交申请</button>
    </form>

    <div v-else class="data-table-wrap">
      <table class="table w-full text-sm whitespace-nowrap">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3">ID</th>
            <th class="px-4 py-3">状态</th>
            <th class="px-4 py-3">开票方式</th>
            <th class="px-4 py-3 text-right">金额</th>
            <th class="px-4 py-3">收票邮箱</th>
            <th class="px-4 py-3">发票类型</th>
            <th class="px-4 py-3">抬头类型</th>
            <th class="px-4 py-3">发票抬头</th>
            <th class="px-4 py-3">纳税人识别号</th>
            <th class="px-4 py-3">原因</th>
            <th class="px-4 py-3">备注</th>
            <th class="px-4 py-3">申请时间</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td colspan="12" class="px-4 py-10 text-center text-muted">暂无开票记录</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
