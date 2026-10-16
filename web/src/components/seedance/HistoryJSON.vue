<script setup lang="ts">
import { computed, ref } from 'vue'
import { copyText } from '@/lib/api'
import { useSeedanceI18n } from './messages'
const props = defineProps<{ body: unknown; name: string }>()
const { t } = useSeedanceI18n()
const expanded = ref(true)
const query = ref('')
const copied = ref(false)
const raw = computed(() => JSON.stringify(props.body, null, 2))
const displayed = computed(() => query.value ? raw.value.split('\n').filter(line => line.toLowerCase().includes(query.value.toLowerCase())).join('\n') : raw.value)
async function copy() { copied.value = await copyText(raw.value) }
function download() {
  const url = URL.createObjectURL(new Blob([raw.value], { type: 'application/json' }))
  const a = document.createElement('a'); a.href = url; a.download = `${props.name.replace(/[^\w-]/g, '_')}.json`; a.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}
</script>
<template>
  <div class="space-y-2 rounded border border-line p-3">
    <div class="flex flex-wrap items-center gap-2">
      <button type="button" class="table-link" @click="expanded = !expanded">{{ t(expanded ? 'seedance.collapse' : 'seedance.expand') }}</button>
      <button type="button" class="table-link" @click="copy">{{ t(copied ? 'seedance.copied' : 'seedance.copy') }}</button>
      <button type="button" class="table-link" @click="download">{{ t('seedance.download') }}</button>
      <input v-model="query" class="input !min-h-8 flex-1" :aria-label="t('seedance.jsonSearch')" :placeholder="t('seedance.jsonSearch')" />
    </div>
    <pre v-if="expanded" class="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded bg-panel p-3 text-xs">{{ displayed }}</pre>
  </div>
</template>
