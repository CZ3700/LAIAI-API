<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { api } from '@/lib/api'

interface PricedModel {
  model: string
  provider: string
  category?: string // 后端模型元数据的模态/分类（chat/image/audio/embedding…）
}

// 体验场支持的两种模态：chat → /v1/chat/completions；image → /v1/images/generations。
// 其余模态（audio/embedding/rerank/video）仍引导走 API（FE-4）。
const IMAGE_CATEGORIES = new Set(['image', 'images'])
const NON_CHAT_CATEGORIES = new Set([
  'image', 'images', 'audio', 'tts', 'stt', 'speech', 'transcription',
  'embedding', 'embeddings', 'rerank', 'moderation', 'video',
])
// 无元数据分类时按模型名兜底识别常见生图族（避免漏判把生图模型当对话）
const IMAGE_NAME_RE = /(^|[-_/])(dall-?e|gpt-image|flux|stable-?diffusion|sdxl|sd[0-9]|cogview|kolors|seedream|hunyuan-image|wanx?|imagen|ideogram|recraft)/i

interface Msg {
  role: 'user' | 'assistant'
  content: string
}
interface GenImage { url?: string; b64_json?: string }

const { t } = useI18n()
const route = useRoute()

// API Key：playground 模拟真实外部调用，用用户自己的 Key 直打 /v1（不走 session）。
// sessionStorage 记住，关标签页即失效（不持久化凭证）。
const KEY_STORE = 'laiai_pg_key'
const apiKey = ref(sessionStorage.getItem(KEY_STORE) ?? '')
function rememberKey() {
  if (apiKey.value) sessionStorage.setItem(KEY_STORE, apiKey.value)
  else sessionStorage.removeItem(KEY_STORE)
}

const models = ref<PricedModel[]>([])
const model = ref('')

// —— chat 模态 ——
const system = ref('')
const temperature = ref(1)
const maxTokens = ref(1024)
const stream = ref(true)
const messages = ref<Msg[]>([])
const input = ref('')
const sending = ref(false)
const error = ref('')
const lastUsage = ref<{ prompt_tokens: number; completion_tokens: number } | null>(null)
let abort: AbortController | null = null
const scrollEl = ref<HTMLElement | null>(null)

// —— image 模态 ——
const IMAGE_SIZES = ['1024x1024', '1024x1792', '1792x1024', '512x512', '256x256']
const imagePrompt = ref('')
const imageN = ref(1)
const imageSize = ref('1024x1024')
const useB64 = ref(true) // 默认请求 base64：浏览器可直接保存，不受图片 URL 跨域限制
const images = ref<GenImage[]>([])
const generating = ref(false)
const genError = ref('')

// 选中模型分类（后端 /api/pricing 元数据），用于模态路由（FE-4）
const selectedCategory = computed(
  () => models.value.find((m) => m.model === model.value)?.category?.toLowerCase() ?? '',
)
const isImageModel = computed(() => {
  if (selectedCategory.value) return IMAGE_CATEGORIES.has(selectedCategory.value)
  return IMAGE_NAME_RE.test(model.value) // 无元数据时按名兜底
})
const isChatModel = computed(() => !selectedCategory.value || !NON_CHAT_CATEGORIES.has(selectedCategory.value))
const mode = computed<'chat' | 'image' | 'other'>(() =>
  isImageModel.value ? 'image' : isChatModel.value ? 'chat' : 'other',
)
const endpoint = computed(() => (mode.value === 'image' ? '/v1/images/generations' : '/v1/chat/completions'))

const canSend = computed(
  () => !!apiKey.value && !!model.value && !!input.value.trim() && !sending.value && mode.value === 'chat',
)
const canGenerate = computed(
  () => !!apiKey.value && !!model.value && !!imagePrompt.value.trim() && !generating.value && mode.value === 'image',
)

async function toBottom() {
  await nextTick()
  scrollEl.value?.scrollTo({ top: scrollEl.value.scrollHeight, behavior: 'smooth' })
}

onMounted(async () => {
  try {
    const r = await api<{ models: PricedModel[] }>('GET', '/api/pricing')
    models.value = r.models
    const requested = typeof route.query.model === 'string' ? route.query.model : ''
    const found = requested ? r.models.find((m) => m.model === requested) : null
    if (found) model.value = found.model
    else if (r.models.length) model.value = r.models[0].model
  } catch {
    // 无价目不阻塞：用户仍可手填模型名（input fallback 见模板）
  }
})

function clearAll() {
  messages.value = []
  error.value = ''
  lastUsage.value = null
  images.value = []
  genError.value = ''
}

function stop() {
  abort?.abort()
}

async function send() {
  if (!canSend.value) return
  error.value = ''
  lastUsage.value = null
  const userMsg: Msg = { role: 'user', content: input.value.trim() }
  messages.value.push(userMsg)
  input.value = ''
  // 占位 assistant 气泡，流式逐字填充
  const assistant = ref<Msg>({ role: 'assistant', content: '' })
  messages.value.push(assistant.value)
  sending.value = true
  void toBottom()

  const body: Record<string, unknown> = {
    model: model.value,
    messages: [
      ...(system.value ? [{ role: 'system', content: system.value }] : []),
      ...messages.value.slice(0, -1).map((m) => ({ role: m.role, content: m.content })),
    ],
    temperature: Number(temperature.value),
    max_tokens: Number(maxTokens.value),
    stream: stream.value,
  }

  abort = new AbortController()
  try {
    const resp = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${apiKey.value}` },
      body: JSON.stringify(body),
      signal: abort.signal,
    })
    if (!resp.ok) {
      const data = (await resp.json().catch(() => ({}))) as { error?: { message?: string } | string }
      const msg =
        typeof data.error === 'object' ? data.error?.message : typeof data.error === 'string' ? data.error : ''
      throw new Error(msg || `request failed (${resp.status})`)
    }

    if (stream.value && resp.body) {
      const reader = resp.body.getReader()
      const decoder = new TextDecoder()
      let buf = ''
      let streamErr = ''
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
        const lines = buf.split('\n')
        buf = lines.pop() ?? '' // 末尾可能是半行，留到下一轮
        for (const line of lines) {
          const tl = line.trim()
          if (!tl.startsWith('data:')) continue
          const payload = tl.slice(5).trim()
          if (payload === '[DONE]') continue
          try {
            const j = JSON.parse(payload)
            if (j.error) { // 上游把错误作为 SSE data 帧下发（OpenAI 风格）——不能静默
              streamErr = typeof j.error === 'object' ? (j.error.message ?? JSON.stringify(j.error)) : String(j.error)
              continue
            }
            const delta = j.choices?.[0]?.delta?.content
            if (delta) {
              assistant.value.content += delta
              void toBottom()
            }
            if (j.usage) lastUsage.value = j.usage
          } catch {
            // 容忍单帧解析失败（注释帧/心跳）
          }
        }
      }
      // 流已开始(200)但全程无内容：把静默"转圈"变成可见原因（上游错误帧 / 空回复 / 非 SSE 响应）
      if (!assistant.value.content) throw new Error(streamErr || t('play.noReply'))
    } else {
      const j = (await resp.json()) as {
        choices?: { message?: { content?: string } }[]
        usage?: { prompt_tokens: number; completion_tokens: number }
        error?: { message?: string } | string
      }
      if (j.error) throw new Error(typeof j.error === 'object' ? (j.error.message ?? JSON.stringify(j.error)) : String(j.error))
      assistant.value.content = j.choices?.[0]?.message?.content ?? ''
      if (j.usage) lastUsage.value = j.usage
      if (!assistant.value.content) throw new Error(t('play.noReply'))
    }
  } catch (e) {
    if ((e as Error).name === 'AbortError') {
      assistant.value.content += ' ⏹'
    } else {
      error.value = (e as Error).message
      // 抽掉空的占位气泡，保留用户消息便于重试
      if (!assistant.value.content) messages.value.pop()
    }
  } finally {
    sending.value = false
    abort = null
    void toBottom()
  }
}

// 生图：与 chat 同样用用户 Key 直打 /v1/images/generations（OpenAI 兼容，按张计费）。
async function generate() {
  if (!canGenerate.value) return
  genError.value = ''
  images.value = []
  generating.value = true
  const body: Record<string, unknown> = {
    model: model.value,
    prompt: imagePrompt.value.trim(),
    n: Number(imageN.value),
  }
  if (imageSize.value && imageSize.value !== 'default') body.size = imageSize.value
  if (useB64.value) body.response_format = 'b64_json'
  try {
    const resp = await fetch('/v1/images/generations', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${apiKey.value}` },
      body: JSON.stringify(body),
    })
    if (!resp.ok) {
      const data = (await resp.json().catch(() => ({}))) as { error?: { message?: string } | string }
      const msg =
        typeof data.error === 'object' ? data.error?.message : typeof data.error === 'string' ? data.error : ''
      throw new Error(msg || `request failed (${resp.status})`)
    }
    const j = (await resp.json()) as { data?: GenImage[] }
    images.value = (j.data ?? []).filter((d) => d.url || d.b64_json)
    if (!images.value.length) throw new Error(t('play.noReply'))
  } catch (e) {
    genError.value = (e as Error).message
  } finally {
    generating.value = false
  }
}

function imgSrc(img: GenImage): string {
  return img.b64_json ? `data:image/png;base64,${img.b64_json}` : (img.url ?? '')
}

// 保存：base64 直接下载；URL 先取 blob 再下载，跨域取失败则回退新标签页打开（让用户右键另存）。
async function saveImage(img: GenImage, idx: number) {
  const name = `laiai-${model.value}-${idx + 1}.png`.replace(/[^\w.-]+/g, '_')
  try {
    let href = ''
    let revoke = ''
    if (img.b64_json) {
      href = `data:image/png;base64,${img.b64_json}`
    } else if (img.url) {
      const r = await fetch(img.url)
      href = URL.createObjectURL(await r.blob())
      revoke = href
    } else {
      return
    }
    const a = document.createElement('a')
    a.href = href
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    if (revoke) URL.revokeObjectURL(revoke)
  } catch {
    if (img.url) window.open(img.url, '_blank') // 跨域无法 fetch 时的兜底
  }
}
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('surface.playEyebrow') }}</p>
        <h1 class="app-page-title">{{ t('play.title') }}</h1>
        <p class="app-page-subtitle">{{ t('surface.playSubtitle') }}</p>
      </div>
    </div>

    <div class="grid min-h-[calc(100svh-12rem)] gap-5 xl:grid-cols-[22rem_1fr]">
    <aside class="app-panel h-fit overflow-hidden xl:sticky xl:top-7">
      <div class="border-b border-line px-4 py-3">
        <p class="text-sm font-medium">{{ t('surface.playSettingsTitle') }}</p>
        <p class="mt-1 text-xs text-muted">{{ t('surface.playSettingsDesc') }}</p>
      </div>
      <div class="p-4">
      <div class="mb-5 flex items-center justify-between gap-3">
        <div>
          <p class="text-sm font-semibold">{{ t('surface.playEndpoint') }}</p>
          <p class="mt-1 font-mono text-xs text-muted">{{ endpoint }}</p>
        </div>
        <button class="btn-ghost !min-h-0 !px-2.5 !py-1.5 text-xs" :disabled="messages.length === 0 && images.length === 0" @click="clearAll">{{ t('play.clear') }}</button>
      </div>

      <div class="space-y-4">
        <div>
          <label class="label" for="pg-model">{{ t('play.model') }}</label>
          <select v-if="models.length" id="pg-model" v-model="model" class="input">
            <option v-for="m in models" :key="m.model" :value="m.model">{{ m.model }} · {{ m.provider }}</option>
          </select>
          <input v-else id="pg-model" v-model="model" class="input font-mono text-xs" :placeholder="t('play.modelManual')" />
        </div>

        <div>
          <label class="label" for="pg-key">{{ t('play.apiKey') }}</label>
          <input
            id="pg-key" v-model="apiKey" class="input font-mono text-xs" type="password"
            autocomplete="off" placeholder="sk-laiai-..." @change="rememberKey"
          />
          <p v-if="!apiKey" class="mt-2 text-xs text-muted">
            {{ t('play.keyHint') }}
            <router-link class="text-accent hover:underline" to="/app/keys">{{ t('nav.keys') }}</router-link>
          </p>
        </div>

        <!-- chat 模态设置 -->
        <template v-if="mode === 'chat'">
          <div>
            <label class="label" for="pg-system">{{ t('play.system') }}</label>
            <textarea id="pg-system" v-model="system" class="input min-h-20 resize-y" :placeholder="t('play.systemPlaceholder')" />
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label class="label" for="pg-temp">{{ t('play.temperature') }}</label>
              <input id="pg-temp" v-model.number="temperature" type="number" min="0" max="2" step="0.1" class="input" />
            </div>
            <div>
              <label class="label" for="pg-max">{{ t('play.maxTokens') }}</label>
              <input id="pg-max" v-model.number="maxTokens" type="number" min="1" max="32000" class="input" />
            </div>
          </div>
          <label class="soft-panel flex items-center justify-between gap-3 px-3 py-2 text-sm">
            <span>{{ t('play.stream') }}</span>
            <input v-model="stream" type="checkbox" class="accent-accent" />
          </label>
        </template>

        <!-- image 模态设置 -->
        <template v-else-if="mode === 'image'">
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label class="label" for="pg-n">{{ t('play.imageCount') }}</label>
              <select id="pg-n" v-model.number="imageN" class="input">
                <option v-for="c in [1, 2, 4]" :key="c" :value="c">{{ c }}</option>
              </select>
            </div>
            <div>
              <label class="label" for="pg-size">{{ t('play.imageSize') }}</label>
              <select id="pg-size" v-model="imageSize" class="input">
                <option value="default">default</option>
                <option v-for="s in IMAGE_SIZES" :key="s" :value="s">{{ s }}</option>
              </select>
            </div>
          </div>
          <label class="soft-panel flex items-center justify-between gap-3 px-3 py-2 text-sm">
            <span>{{ t('play.b64Label') }}</span>
            <input v-model="useB64" type="checkbox" class="accent-accent" />
          </label>
        </template>
      </div>
      </div>
    </aside>

    <section class="app-panel flex min-h-[34rem] flex-col overflow-hidden">
      <div class="flex items-center justify-between gap-3 border-b border-line px-4 py-3">
        <div class="min-w-0">
          <p class="truncate font-mono text-sm">{{ model || t('play.modelManual') }}</p>
          <p class="text-xs text-muted">{{ (sending || generating) ? t('surface.playStreaming') : t('surface.playReady') }}</p>
        </div>
        <p v-if="lastUsage && mode === 'chat'" class="shrink-0 text-xs text-muted">
          {{ t('play.usage', { input: lastUsage.prompt_tokens, output: lastUsage.completion_tokens }) }}
        </p>
      </div>

      <!-- chat 模态：消息流 + 输入 -->
      <template v-if="mode === 'chat'">
        <div ref="scrollEl" class="flex-1 space-y-4 overflow-y-auto bg-elev/18 p-4 sm:p-5">
          <div v-if="messages.length === 0" class="grid h-full place-items-center text-center text-muted">
            <div class="max-w-sm">
              <p class="text-sm">{{ t('play.emptyHint') }}</p>
            </div>
          </div>
          <div v-for="(m, i) in messages" :key="i" class="flex" :class="m.role === 'user' ? 'justify-end' : 'justify-start'">
            <div
              class="max-w-[86%] whitespace-pre-wrap rounded-lg px-4 py-2.5 text-sm leading-relaxed shadow-card"
              :class="m.role === 'user' ? 'bg-accent text-slate-950' : 'border border-line bg-panel/86'"
            >
              <span v-if="m.content">{{ m.content }}</span>
              <span v-else class="inline-flex gap-1 py-1">
                <span class="h-1.5 w-1.5 animate-bounce rounded-full bg-muted" style="animation-delay: 0ms"></span>
                <span class="h-1.5 w-1.5 animate-bounce rounded-full bg-muted" style="animation-delay: 150ms"></span>
                <span class="h-1.5 w-1.5 animate-bounce rounded-full bg-muted" style="animation-delay: 300ms"></span>
              </span>
            </div>
          </div>
        </div>
        <div class="border-t border-line p-3">
          <p v-if="error" class="mb-2 text-sm text-danger" role="alert">{{ error }}</p>
          <form class="flex gap-2" @submit.prevent="send">
            <input v-model="input" class="input" :placeholder="t('play.inputPlaceholder')" :disabled="sending" />
            <button v-if="sending" type="button" class="btn-danger shrink-0" @click="stop">{{ t('play.stop') }}</button>
            <button v-else type="submit" class="btn-primary shrink-0" :disabled="!canSend">{{ t('play.send') }}</button>
          </form>
        </div>
      </template>

      <!-- image 模态：提示词 + 生成 + 结果网格（可保存） -->
      <template v-else-if="mode === 'image'">
        <div class="flex-1 overflow-y-auto bg-elev/18 p-4 sm:p-5">
          <div v-if="generating" class="grid h-full place-items-center text-muted">
            <span class="inline-flex gap-1 py-1">
              <span class="h-2 w-2 animate-bounce rounded-full bg-muted" style="animation-delay: 0ms"></span>
              <span class="h-2 w-2 animate-bounce rounded-full bg-muted" style="animation-delay: 150ms"></span>
              <span class="h-2 w-2 animate-bounce rounded-full bg-muted" style="animation-delay: 300ms"></span>
            </span>
          </div>
          <div v-else-if="images.length" class="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <figure v-for="(img, i) in images" :key="i" class="group relative overflow-hidden rounded-lg border border-line bg-panel">
              <img :src="imgSrc(img)" :alt="`image ${i + 1}`" class="block w-full" loading="lazy" />
              <button
                type="button"
                class="btn-primary absolute right-2 top-2 !min-h-0 !px-2.5 !py-1.5 text-xs opacity-0 shadow-card transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
                @click="saveImage(img, i)"
              >{{ t('play.save') }}</button>
            </figure>
          </div>
          <div v-else class="grid h-full place-items-center text-center text-muted">
            <p class="max-w-sm text-sm">{{ t('play.imageEmptyHint') }}</p>
          </div>
        </div>
        <div class="border-t border-line p-3">
          <p v-if="genError" class="mb-2 text-sm text-danger" role="alert">{{ genError }}</p>
          <form class="flex gap-2" @submit.prevent="generate">
            <input v-model="imagePrompt" class="input" :placeholder="t('play.imagePromptPlaceholder')" :disabled="generating" />
            <button type="submit" class="btn-primary shrink-0" :disabled="!canGenerate">{{ generating ? t('play.generating') : t('play.generate') }}</button>
          </form>
        </div>
      </template>

      <!-- 其余模态（audio/embedding/rerank/video）：引导走 API -->
      <template v-else>
        <div class="grid flex-1 place-items-center bg-elev/18 p-6 text-center">
          <p class="max-w-sm text-sm text-amber-500" role="alert">
            {{ t('play.nonChatModel', { category: selectedCategory }) }}
          </p>
        </div>
      </template>
    </section>
    </div>
  </div>
</template>
