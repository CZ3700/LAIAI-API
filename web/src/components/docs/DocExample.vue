<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { copyText } from '@/lib/api'
import { curlFor, type ApiExample } from '@/docs/apiDocs'
const props = defineProps<{ example: ApiExample }>()
const tab = ref('curl')
const copied = ref(false)
const copyFailed = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined
const tabs = computed(() => [
  { id: 'curl', label: props.example.command ? '调用示例' : 'cURL' },
  ...(!props.example.command ? [{ id: 'request', label: props.example.form ? '输入表单' : '输入 JSON' }] : []),
  ...(props.example.response !== undefined || props.example.responseText ? [{ id: 'response', label: props.example.responseExcerpt ? '输出 JSON（节选）' : props.example.responseText ? '输出' : '输出 JSON' }] : []),
])
const code = computed(() => {
  if (tab.value === 'response') return props.example.responseText || JSON.stringify(props.example.response, null, 2)
  if (tab.value === 'request') return props.example.form || (props.example.request === undefined ? '此请求没有 JSON 请求体。路径及查询参数见上方接口地址。' : JSON.stringify(props.example.request, null, 2))
  return curlFor(props.example)
})
async function copy() {
  const ok = await copyText(code.value)
  copied.value = ok
  copyFailed.value = !ok
  clearTimeout(timer)
  timer = setTimeout(() => { copied.value = false }, 1500)
}
onBeforeUnmount(() => clearTimeout(timer))
</script>

<template>
  <div class="overflow-hidden rounded-xl border border-line bg-panel">
    <div class="flex flex-wrap items-center justify-between gap-3 border-b border-line px-4 py-3"><div class="min-w-0"><p class="text-sm font-medium">{{ example.title }}</p><p v-if="example.path" class="mt-2 break-all font-mono text-xs text-muted"><span class="mr-2 font-semibold text-accent">{{ example.method }}</span>{{ example.path }}</p></div><span v-if="example.status" class="rounded border border-line px-2 py-1 text-xs text-muted">响应 {{ example.status }}</span></div>
    <div class="flex flex-wrap items-center justify-between gap-2 border-b border-line px-3 py-2"><div class="flex flex-wrap gap-1" role="group" :aria-label="`${example.title}代码类型`"><button v-for="item in tabs" :key="item.id" :aria-pressed="tab === item.id" class="rounded px-3 py-1.5 text-xs hover:text-accent" :class="tab === item.id ? 'bg-bg font-semibold text-accent' : 'text-muted'" @click="tab = item.id">{{ item.label }}</button></div><button class="rounded px-3 py-1.5 text-xs font-medium hover:text-accent" :aria-label="`复制${example.title}`" @click="copy">{{ copied ? '已复制' : '复制代码' }}</button></div>
    <pre tabindex="0" :aria-label="`${example.title}代码`" class="max-h-[34rem] overflow-auto bg-bg p-4 text-xs leading-6 sm:p-5"><code>{{ code }}</code></pre>
    <p v-if="example.note || copyFailed" class="border-t border-line px-4 py-3 text-xs leading-6 text-muted" role="status">{{ copyFailed ? '复制未成功，请选择上方代码手动复制。' : example.note }}</p>
  </div>
</template>
