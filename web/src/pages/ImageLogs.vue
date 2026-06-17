<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '@/lib/api'

interface ModelPrice {
  model: string
  image_usd: string
}
interface KeyRow { id: number; name: string; prefix: string }

const range = ref<'7d' | '30d' | 'month' | 'prev' | 'custom'>('7d')
const model = ref('')
const key = ref('')
const requestId = ref('')
const result = ref<'all' | 'success' | 'failed'>('all')
const customStart = ref('')
const customEnd = ref('')
const models = ref<ModelPrice[]>([])
const keys = ref<KeyRow[]>([])
const loading = ref(true)

onMounted(async () => {
  const [pricing, keyRows] = await Promise.allSettled([
    api<{ models: ModelPrice[] }>('GET', '/api/pricing', undefined, { auth: false }),
    api<{ keys: KeyRow[] }>('GET', '/api/keys'),
  ])
  if (pricing.status === 'fulfilled') models.value = pricing.value.models.filter((m) => Number(m.image_usd) > 0)
  if (keyRows.status === 'fulfilled') keys.value = keyRows.value.keys
  loading.value = false
})
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <h1 class="app-page-title">图像模型调用记录</h1>
      </div>
    </div>

    <div class="toolbar-panel items-end">
      <div class="filter-group">
        <label>时间范围</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: range === '7d' }" @click="range = '7d'">7 天</button>
          <button type="button" :class="{ active: range === '30d' }" @click="range = '30d'">30 天</button>
          <button type="button" :class="{ active: range === 'month' }" @click="range = 'month'">本月</button>
          <button type="button" :class="{ active: range === 'prev' }" @click="range = 'prev'">上月</button>
          <button type="button" :class="{ active: range === 'custom' }" @click="range = 'custom'">自定义</button>
        </div>
      </div>
      <div v-if="range === 'custom'" class="filter-group min-w-[260px]">
        <label>自定义日期</label>
        <div class="grid grid-cols-2 gap-2">
          <input v-model="customStart" class="input" type="date" placeholder="开始日期" />
          <input v-model="customEnd" class="input" type="date" placeholder="结束日期" />
        </div>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>模型</label>
        <select v-model="model" class="input">
          <option value="">全部模型</option>
          <option v-for="m in models" :key="m.model" :value="m.model">{{ m.model }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>API 密钥</label>
        <select v-model="key" class="input">
          <option value="">全部密钥</option>
          <option v-for="k in keys" :key="k.id" :value="String(k.id)">{{ k.name || k.prefix }}</option>
        </select>
      </div>
      <div class="filter-group min-w-[180px]">
        <label>请求ID</label>
        <input v-model="requestId" class="input" placeholder="输入请求ID" />
      </div>
      <div class="filter-group">
        <label>结果</label>
        <div class="segmented-control">
          <button type="button" :class="{ active: result === 'all' }" @click="result = 'all'">全部</button>
          <button type="button" :class="{ active: result === 'success' }" @click="result = 'success'">成功</button>
          <button type="button" :class="{ active: result === 'failed' }" @click="result = 'failed'">失败</button>
        </div>
      </div>
      <button class="btn-primary !min-h-0 !px-4 !py-2" type="button" disabled>待接口</button>
    </div>

    <p class="text-xs text-muted">图像日志接口尚未拆出；筛选栏和表格结构先作为 UI 占位，避免误触发不存在的查询。</p>

    <p v-if="loading" class="text-sm text-muted">加载中…</p>
    <div v-else class="data-table-wrap">
      <table class="table w-full text-sm whitespace-nowrap">
        <thead>
          <tr class="border-b border-line text-left text-muted">
            <th class="px-4 py-3">请求ID</th>
            <th class="px-4 py-3">时间</th>
            <th class="px-4 py-3">驱动</th>
            <th class="px-4 py-3">模型</th>
            <th class="px-4 py-3 text-right">图片数</th>
            <th class="px-4 py-3 text-right">费用</th>
            <th class="px-4 py-3 text-right">赠送消耗</th>
            <th class="px-4 py-3 text-right">付费消耗</th>
            <th class="px-4 py-3 text-right">耗时</th>
            <th class="px-4 py-3">状态</th>
            <th class="px-4 py-3 text-right">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td colspan="11" class="px-4 py-12 text-center text-muted">
              暂无数据
              <p class="mt-2 text-xs">当前后端尚未提供独立图像日志查询接口，页面先保留与参考站一致的筛选和表格空态。</p>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
