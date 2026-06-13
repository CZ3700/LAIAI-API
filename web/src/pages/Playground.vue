<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'

interface PricedModel {
  model: string
  provider: string
}
interface Msg {
  role: 'user' | 'assistant'
  content: string
}

const { t } = useI18n()

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
const system = ref('')
const temperature = ref(1)
const maxTokens = ref(1024)
const stream = ref(true)
const showSettings = ref(false)

const messages = ref<Msg[]>([])
const input = ref('')
const sending = ref(false)
const error = ref('')
const lastUsage = ref<{ prompt_tokens: number; completion_tokens: number } | null>(null)
let abort: AbortController | null = null

const scrollEl = ref<HTMLElement | null>(null)
const canSend = computed(() => !!apiKey.value && !!model.value && !!input.value.trim() && !sending.value)

async function toBottom() {
  await nextTick()
  scrollEl.value?.scrollTo({ top: scrollEl.value.scrollHeight, behavior: 'smooth' })
}

onMounted(async () => {
  try {
    const r = await api<{ models: PricedModel[] }>('GET', '/api/pricing')
    models.value = r.models
    if (r.models.length) model.value = r.models[0].model
  } catch {
    // 无价目不阻塞：用户仍可手填模型名（input fallback 见模板）
  }
})

function clearChat() {
  messages.value = []
  error.value = ''
  lastUsage.value = null
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
    } else {
      const j = (await resp.json()) as {
        choices?: { message?: { content?: string } }[]
        usage?: { prompt_tokens: number; completion_tokens: number }
      }
      assistant.value.content = j.choices?.[0]?.message?.content ?? ''
      if (j.usage) lastUsage.value = j.usage
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
</script>

<template>
  <div class="flex h-[calc(100vh-4rem)] flex-col gap-4">
    <!-- 顶栏：模型 + Key + 设置 -->
    <div class="flex flex-wrap items-end gap-3">
      <h1 class="text-2xl font-semibold tracking-tight mr-2">{{ t('play.title') }}</h1>
      <div class="min-w-44">
        <label class="label" for="pg-model">{{ t('play.model') }}</label>
        <select v-if="models.length" id="pg-model" v-model="model" class="input">
          <option v-for="m in models" :key="m.model" :value="m.model">{{ m.model }} · {{ m.provider }}</option>
        </select>
        <input v-else id="pg-model" v-model="model" class="input font-mono text-xs" :placeholder="t('play.modelManual')" />
      </div>
      <div class="min-w-56 flex-1">
        <label class="label" for="pg-key">{{ t('play.apiKey') }}</label>
        <input
          id="pg-key" v-model="apiKey" class="input font-mono text-xs" type="password"
          autocomplete="off" placeholder="sk-laiai-…" @change="rememberKey"
        />
      </div>
      <button class="btn-ghost" @click="showSettings = !showSettings">{{ t('play.settings') }}</button>
      <button class="btn-ghost" :disabled="messages.length === 0" @click="clearChat">{{ t('play.clear') }}</button>
    </div>

    <p v-if="!apiKey" class="text-xs text-muted">
      {{ t('play.keyHint') }}
      <router-link class="text-accent hover:underline" to="/app/keys">{{ t('nav.keys') }} →</router-link>
    </p>

    <!-- 设置面板 -->
    <div v-if="showSettings" class="card grid gap-4 p-4 sm:grid-cols-2 lg:grid-cols-4">
      <div class="sm:col-span-2 lg:col-span-4">
        <label class="label" for="pg-system">{{ t('play.system') }}</label>
        <input id="pg-system" v-model="system" class="input" :placeholder="t('play.systemPlaceholder')" />
      </div>
      <div>
        <label class="label" for="pg-temp">{{ t('play.temperature') }}: {{ temperature }}</label>
        <input id="pg-temp" v-model.number="temperature" type="range" min="0" max="2" step="0.1" class="w-full accent-accent" />
      </div>
      <div>
        <label class="label" for="pg-max">{{ t('play.maxTokens') }}</label>
        <input id="pg-max" v-model.number="maxTokens" type="number" min="1" max="32000" class="input" />
      </div>
      <label class="flex items-end gap-2 pb-2 text-sm">
        <input v-model="stream" type="checkbox" class="accent-accent" />
        {{ t('play.stream') }}
      </label>
    </div>

    <!-- 对话区 -->
    <div ref="scrollEl" class="card flex-1 space-y-4 overflow-y-auto p-5">
      <div v-if="messages.length === 0" class="grid h-full place-items-center text-center text-muted">
        <div>
          <p class="text-sm">{{ t('play.emptyHint') }}</p>
        </div>
      </div>
      <div v-for="(m, i) in messages" :key="i" class="flex" :class="m.role === 'user' ? 'justify-end' : 'justify-start'">
        <div
          class="max-w-[80%] whitespace-pre-wrap rounded-xl px-4 py-2.5 text-sm leading-relaxed"
          :class="m.role === 'user' ? 'bg-accent text-white' : 'bg-elev border border-line'"
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

    <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <p v-if="lastUsage" class="text-xs text-muted">
      {{ t('play.usage', { input: lastUsage.prompt_tokens, output: lastUsage.completion_tokens }) }}
    </p>

    <!-- 输入栏 -->
    <form class="flex gap-2" @submit.prevent="send">
      <input
        v-model="input"
        class="input"
        :placeholder="t('play.inputPlaceholder')"
        :disabled="sending"
      />
      <button v-if="sending" type="button" class="btn-danger shrink-0" @click="stop">{{ t('play.stop') }}</button>
      <button v-else type="submit" class="btn-primary shrink-0" :disabled="!canSend">{{ t('play.send') }}</button>
    </form>
  </div>
</template>
