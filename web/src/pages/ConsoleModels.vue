<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { api, copyText, errorMessage } from '@/lib/api'
import { filterModels, modelModality, type ModelModality, type PricedModel } from '@/lib/modelCatalog'

const { t } = useI18n()
const router = useRouter()
const models = ref<PricedModel[]>([])
const search = ref('')
const loading = ref(true)
const error = ref('')
const selected = ref<PricedModel | null>(null)
const copied = ref(false)

const filtered = computed(() => {
  return filterModels(models.value, search.value)
})
const providerCount = computed(() => new Set(models.value.map((m) => m.provider)).size)

function modalityLabel(m: PricedModel): string {
  const labels: Record<ModelModality, string> = {
    chat: t('models.modChat'),
    image: t('models.modImage'),
    video: t('models.modVideo'),
    embedding: t('models.modEmbed'),
  }
  return labels[modelModality(m)]
}

async function copyName() {
  if (selected.value && (await copyText(selected.value.model))) {
    copied.value = true
    setTimeout(() => (copied.value = false), 1200)
  }
}
function openInPlayground() {
  if (selected.value) router.push({ path: '/app/playground', query: { model: selected.value.model } })
}

// —— 接入代码片段（批 D-2）：按模态预填 model + 网关地址，复制即用 ——
type SnippetLang = 'curl' | 'python' | 'js'
const snippetLang = ref<SnippetLang>('curl')
const snippetCopied = ref(false)
const snippet = computed(() => {
  const m = selected.value
  if (!m) return ''
  const base = window.location.origin
  const mod = modelModality(m)
  if (snippetLang.value === 'curl') {
    if (mod === 'image')
      return `curl ${base}/v1/images/generations \\\n  -H "Authorization: Bearer $LAIAI_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model": "${m.model}", "prompt": "a cat", "n": 1}'`
    if (mod === 'video')
      return `curl ${base}/v1/video/generations \\\n  -H "Authorization: Bearer $LAIAI_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model": "${m.model}", "content": [{"type": "text", "text": "a cat running"}]}'`
    if (mod === 'embedding')
      return `curl ${base}/v1/embeddings \\\n  -H "Authorization: Bearer $LAIAI_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model": "${m.model}", "input": "hello"}'`
    return `curl ${base}/v1/chat/completions \\\n  -H "Authorization: Bearer $LAIAI_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model": "${m.model}", "messages": [{"role": "user", "content": "Hello"}]}'`
  }
  if (snippetLang.value === 'python') {
    if (mod === 'image')
      return `from openai import OpenAI\n\nclient = OpenAI(base_url="${base}/v1", api_key="sk-laiai-...")\nimg = client.images.generate(model="${m.model}", prompt="a cat", n=1)\nprint(img.data[0].url)`
    if (mod === 'embedding')
      return `from openai import OpenAI\n\nclient = OpenAI(base_url="${base}/v1", api_key="sk-laiai-...")\nemb = client.embeddings.create(model="${m.model}", input="hello")\nprint(len(emb.data[0].embedding))`
    return `from openai import OpenAI\n\nclient = OpenAI(base_url="${base}/v1", api_key="sk-laiai-...")\nresp = client.chat.completions.create(\n    model="${m.model}",\n    messages=[{"role": "user", "content": "Hello"}],\n)\nprint(resp.choices[0].message.content)`
  }
  // js
  if (mod === 'image')
    return `import OpenAI from "openai";\n\nconst client = new OpenAI({ baseURL: "${base}/v1", apiKey: "sk-laiai-..." });\nconst img = await client.images.generate({ model: "${m.model}", prompt: "a cat", n: 1 });\nconsole.log(img.data[0].url);`
  if (mod === 'embedding')
    return `import OpenAI from "openai";\n\nconst client = new OpenAI({ baseURL: "${base}/v1", apiKey: "sk-laiai-..." });\nconst emb = await client.embeddings.create({ model: "${m.model}", input: "hello" });\nconsole.log(emb.data[0].embedding.length);`
  return `import OpenAI from "openai";\n\nconst client = new OpenAI({ baseURL: "${base}/v1", apiKey: "sk-laiai-..." });\nconst resp = await client.chat.completions.create({\n  model: "${m.model}",\n  messages: [{ role: "user", content: "Hello" }],\n});\nconsole.log(resp.choices[0].message.content);`
})
async function copySnippet() {
  if (await copyText(snippet.value)) {
    snippetCopied.value = true
    setTimeout(() => (snippetCopied.value = false), 1200)
  }
}

onMounted(async () => {
  try {
    const r = await api<{ models: PricedModel[] }>('GET', '/api/pricing', undefined, { auth: false })
    models.value = r.models
    selected.value = r.models[0] ?? null
    error.value = ''
  } catch (e) {
    error.value = errorMessage(e, t('common.error'))
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="app-page models-console-page">
    <div class="console-hero p-5 sm:p-6">
      <div class="flex flex-col gap-5 lg:flex-row lg:items-end lg:justify-between">
      <div>
        <p class="app-eyebrow">Catalog</p>
        <div>
          <h1 class="app-page-title">{{ t('models.title') }}</h1>
          <p class="app-page-subtitle">{{ t('models.consoleHint') }}</p>
        </div>
      </div>
        <div class="w-full max-w-md">
          <label class="label" for="console-model-search">{{ t('models.search') }}</label>
          <input id="console-model-search" v-model="search" class="input" :placeholder="t('models.search')" />
        </div>
      </div>
      <div class="mt-6 grid gap-px overflow-hidden rounded-lg border border-line bg-line sm:grid-cols-3">
        <div class="bg-panel p-4">
          <p class="font-mono text-2xl font-semibold">{{ models.length || '—' }}</p>
          <p class="mt-1 text-xs uppercase text-muted">{{ t('surface.statModels') }}</p>
        </div>
        <div class="bg-panel p-4">
          <p class="font-mono text-2xl font-semibold">{{ providerCount || '—' }}</p>
          <p class="mt-1 text-xs uppercase text-muted">{{ t('surface.statProviders') }}</p>
        </div>
        <div class="bg-panel p-4">
          <p class="font-mono text-2xl font-semibold">{{ filtered.length }}</p>
          <p class="mt-1 text-xs uppercase text-muted">{{ t('surface.statMatched') }}</p>
        </div>
      </div>
    </div>

    <p v-if="error" class="inline-alert" role="alert">{{ error }}</p>

    <div v-if="loading" class="app-panel p-4 space-y-3">
      <div v-for="i in 6" :key="i" class="skeleton h-5" :style="{ maxWidth: `${50 + ((i * 13) % 40)}%` }"></div>
    </div>

    <div v-else class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_24rem]">
      <!-- 模型列表 -->
      <div class="data-table-wrap max-h-[34rem]">
        <table class="table w-full text-sm">
          <thead class="sticky top-0 z-10 bg-panel/70 backdrop-blur">
            <tr class="text-left text-muted">
              <th class="px-4 py-2.5 font-medium">{{ t('models.model') }}</th>
              <th class="px-4 py-2.5 font-medium">{{ t('models.provider') }}</th>
              <th class="px-4 py-2.5 font-medium text-right">{{ t('models.input') }}/{{ t('models.output') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="filtered.length === 0">
              <td colspan="3" class="px-4 py-8 text-center text-muted">{{ t('common.empty') }}</td>
            </tr>
            <tr
              v-for="m in filtered"
              :key="m.model"
              class="cursor-pointer border-b border-line last:border-0 transition-colors"
              :class="selected?.model === m.model ? 'bg-accent/10' : ''"
              @click="selected = m"
            >
              <td class="px-4 py-3">
                <div class="font-mono text-xs font-semibold">{{ m.model }}</div>
                <div class="mt-1 text-xs text-muted">{{ modalityLabel(m) }}</div>
              </td>
              <td class="px-4 py-3"><span class="badge-muted">{{ m.provider }}</span></td>
              <td class="px-4 py-3 text-right font-mono text-xs">
                <span class="text-ink">${{ m.input_usd_per_1m }}</span>
                <span class="text-muted"> / </span>
                <span class="text-ink">${{ m.output_usd_per_1m }}</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- 详情面板 -->
      <div v-if="selected" class="model-inspector app-panel h-fit p-5 xl:sticky xl:top-6">
        <div class="flex items-start justify-between gap-3">
          <div>
            <p class="text-xs font-medium text-accent">{{ t('surface.selectedModel') }}</p>
            <p class="mt-2 break-all font-mono text-base font-semibold">{{ selected.model }}</p>
            <div class="mt-2 flex flex-wrap gap-1.5">
              <span class="badge-muted">{{ selected.provider }}</span>
              <span class="badge-muted">{{ modalityLabel(selected) }}</span>
            </div>
          </div>
          <button class="btn-ghost !py-1 text-xs" @click="copyName">{{ copied ? t('common.copied') : t('common.copy') }}</button>
        </div>

        <div class="mt-4 grid grid-cols-2 gap-3">
          <div class="model-price-tile p-3">
            <p class="text-[11px] text-muted">{{ t('models.input') }} /1M</p>
            <p class="mt-0.5 font-mono font-semibold">${{ selected.input_usd_per_1m }}</p>
          </div>
          <div class="model-price-tile p-3">
            <p class="text-[11px] text-muted">{{ t('models.output') }} /1M</p>
            <p class="mt-0.5 font-mono font-semibold">${{ selected.output_usd_per_1m }}</p>
          </div>
          <div v-if="selected.image_usd !== '0'" class="model-price-tile p-3">
            <p class="text-[11px] text-muted">{{ t('models.image') }}</p>
            <p class="mt-0.5 font-mono font-semibold">${{ selected.image_usd }}</p>
          </div>
          <div v-if="selected.per_call_usd" class="model-price-tile p-3">
            <p class="text-[11px] text-muted">{{ t('models.perCall') }}</p>
            <p class="mt-0.5 font-mono font-semibold">${{ selected.per_call_usd }}</p>
          </div>
          <div v-if="selected.cached_input_usd_per_1m" class="model-price-tile p-3">
            <p class="text-[11px] text-muted">{{ t('models.cachedInput') }} /1M</p>
            <p class="mt-0.5 font-mono font-semibold">${{ selected.cached_input_usd_per_1m }}</p>
          </div>
          <div class="model-price-tile p-3">
            <p class="text-[11px] text-muted">{{ t('models.modality') }}</p>
            <p class="mt-0.5 font-semibold">{{ modalityLabel(selected) }}</p>
          </div>
          <div v-if="selected.context_length" class="model-price-tile p-3">
            <p class="text-[11px] text-muted">{{ t('models.context') }}</p>
            <p class="mt-0.5 font-mono font-semibold">{{ selected.context_length.toLocaleString() }}</p>
          </div>
        </div>

        <button class="btn-primary mt-5 w-full" @click="openInPlayground">{{ t('models.openPlayground') }}</button>

        <!-- 接入片段（批 D-2）：模态感知，复制即用 -->
        <div class="mt-5">
          <div class="flex items-center gap-1.5">
            <p class="mr-auto text-xs font-medium">{{ t('models.integrate') }}</p>
            <button
              v-for="l in (['curl', 'python', 'js'] as const)" :key="l"
              class="rounded px-2 py-0.5 text-[11px]"
              :class="snippetLang === l ? 'bg-accent/15 text-accent' : 'text-muted hover:bg-line/40'"
              @click="snippetLang = l"
            >{{ l }}</button>
            <button class="btn-ghost !min-h-0 !px-2 !py-0.5 text-[11px]" @click="copySnippet">
              {{ snippetCopied ? t('common.copied') : t('common.copy') }}
            </button>
          </div>
          <pre class="mt-2 overflow-x-auto rounded-lg border border-line bg-panel p-3 font-mono text-[11px] leading-relaxed">{{ snippet }}</pre>
        </div>
        <p class="mt-3 text-xs text-muted">{{ t('models.unitHint') }}</p>
      </div>
    </div>
  </div>
</template>
