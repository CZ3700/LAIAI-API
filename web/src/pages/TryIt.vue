<script setup lang="ts">
// 公开体验场（营销门面，替代排行榜页）：注册即试的引导漏斗——未登录访客看到 3 步引导
// （注册领额度 → 建 Key → 粘贴试跑），登录/有 Key 者直接流式体验任意模型。零后端匿名转发面。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { api } from '@/lib/api'
import { modelModality, type PricedModel } from '@/lib/modelCatalog'
import { useAuth } from '@/stores/auth'

const { t } = useI18n()
const route = useRoute()
const auth = useAuth()

const KEY_STORE = 'laiai_pg_key' // 与控制台体验场同键（sessionStorage，关标签页即失效）
const apiKey = ref(sessionStorage.getItem(KEY_STORE) ?? '')
function rememberKey() {
  if (apiKey.value) sessionStorage.setItem(KEY_STORE, apiKey.value)
  else sessionStorage.removeItem(KEY_STORE)
}

const models = ref<PricedModel[]>([])
// 共享分类器（含无元数据时的按名兜底）——避免嵌入/音频/图像模型混进 chat 选择器
const chatModels = computed(() => models.value.filter((m) => modelModality(m) === 'chat'))
const model = ref('')
const prompt = ref('')
const answer = ref('')
const sending = ref(false)
const error = ref('')
const done = ref(false)
let abort: AbortController | null = null

const canRun = computed(() => !!apiKey.value && !!model.value && !!prompt.value.trim() && !sending.value)

async function run() {
  if (!canRun.value) return
  error.value = ''
  answer.value = ''
  done.value = false
  sending.value = true
  abort = new AbortController()
  try {
    const resp = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${apiKey.value}` },
      body: JSON.stringify({ model: model.value, messages: [{ role: 'user', content: prompt.value.trim() }], stream: true, max_tokens: 1024 }),
      signal: abort.signal,
    })
    if (!resp.ok || !resp.body) {
      const data = (await resp.json().catch(() => ({}))) as { error?: { message?: string } | string }
      const msg = typeof data.error === 'object' ? data.error?.message : typeof data.error === 'string' ? data.error : ''
      throw new Error(msg || `request failed (${resp.status})`)
    }
    const reader = resp.body.getReader()
    const decoder = new TextDecoder()
    let buf = ''
    let streamErr = ''
    for (;;) {
      const { value, done: rdone } = await reader.read()
      if (rdone) break
      buf += decoder.decode(value, { stream: true })
      const lines = buf.split('\n')
      buf = lines.pop() ?? ''
      for (const line of lines) {
        const tl = line.trim()
        if (!tl.startsWith('data:')) continue
        const payload = tl.slice(5).trim()
        if (payload === '[DONE]') continue
        try {
          const j = JSON.parse(payload)
          if (j.error) { streamErr = typeof j.error === 'object' ? (j.error.message ?? JSON.stringify(j.error)) : String(j.error); continue }
          const delta = j.choices?.[0]?.delta?.content
          if (delta) answer.value += delta
        } catch {
          // 容忍单帧解析失败（心跳/注释帧）
        }
      }
    }
    if (!answer.value) throw new Error(streamErr || t('tryit.noReply'))
    if (streamErr) { // 流中途上游错误帧：即便已有部分内容也不当成功（截断输出冒充完整会误导）
      error.value = streamErr
      return
    }
    done.value = true
  } catch (e) {
    error.value = abort?.signal.aborted ? '' : e instanceof Error ? e.message : String(e)
  } finally {
    sending.value = false
  }
}
function stop() { abort?.abort() }

const EXAMPLES = ['tryit.ex1', 'tryit.ex2', 'tryit.ex3']

onMounted(async () => {
  try {
    const r = await api<{ models: PricedModel[] }>('GET', '/api/pricing', undefined, { auth: false })
    models.value = r.models
    const wanted = typeof route.query.model === 'string' ? route.query.model : ''
    const found = wanted ? chatModels.value.find((m) => m.model === wanted) : null
    model.value = found?.model ?? chatModels.value[0]?.model ?? ''
  } catch {
    // 无价目不阻塞：用户仍可手填模型名
  }
})
</script>

<template>
  <div class="mx-auto max-w-5xl px-4 py-12 sm:px-6 lg:px-8">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('tryit.eyebrow') }}</p>
        <h1 class="app-page-title">{{ t('tryit.title') }}</h1>
        <p class="app-page-subtitle">{{ t('tryit.subtitle') }}</p>
      </div>
    </div>

    <!-- 未登录：3 步引导漏斗（转化） -->
    <div v-if="!auth.isAuthed" class="mt-6 grid gap-3 sm:grid-cols-3">
      <div class="try-step app-panel p-4">
        <span class="try-step-n">1</span>
        <p class="mt-2 text-sm font-semibold">{{ t('tryit.step1Title') }}</p>
        <p class="mt-1 text-xs text-muted">{{ t('tryit.step1Desc') }}</p>
        <router-link to="/register" class="btn-primary mt-3 inline-flex !py-1.5 text-sm">{{ t('tryit.step1Cta') }}</router-link>
      </div>
      <div class="try-step app-panel p-4">
        <span class="try-step-n">2</span>
        <p class="mt-2 text-sm font-semibold">{{ t('tryit.step2Title') }}</p>
        <p class="mt-1 text-xs text-muted">{{ t('tryit.step2Desc') }}</p>
        <router-link to="/login" class="btn-ghost mt-3 inline-flex !py-1.5 text-sm">{{ t('tryit.step2Cta') }}</router-link>
      </div>
      <div class="try-step app-panel p-4">
        <span class="try-step-n">3</span>
        <p class="mt-2 text-sm font-semibold">{{ t('tryit.step3Title') }}</p>
        <p class="mt-1 text-xs text-muted">{{ t('tryit.step3Desc') }}</p>
      </div>
    </div>
    <div v-else class="mt-6 rounded-lg border border-line bg-panel px-4 py-3 text-sm">
      {{ t('tryit.authedHint') }}
      <router-link to="/app/keys" class="ml-1 font-medium text-accent hover:underline">{{ t('tryit.authedCta') }}</router-link>
    </div>

    <!-- 组合区 -->
    <div class="mt-5 grid gap-4 lg:grid-cols-[1fr_1.3fr]">
      <div class="app-panel h-fit p-4">
        <div class="space-y-4">
          <div>
            <label class="label" for="try-model">{{ t('tryit.model') }}</label>
            <select v-if="chatModels.length" id="try-model" v-model="model" class="input">
              <option v-for="m in chatModels" :key="m.model" :value="m.model">{{ m.model }} · {{ m.provider }}</option>
            </select>
            <input v-else id="try-model" v-model="model" class="input font-mono text-xs" :placeholder="t('tryit.modelManual')" />
          </div>
          <div>
            <label class="label" for="try-key">{{ t('tryit.apiKey') }}</label>
            <input id="try-key" v-model="apiKey" class="input font-mono text-xs" type="password" autocomplete="off" placeholder="sk-laiai-..." @change="rememberKey" />
            <p v-if="!apiKey" class="mt-1.5 text-xs text-muted">{{ t('tryit.keyHint') }}</p>
          </div>
          <div>
            <label class="label" for="try-prompt">{{ t('tryit.prompt') }}</label>
            <textarea id="try-prompt" v-model="prompt" class="input min-h-28 resize-y" :placeholder="t('tryit.promptPlaceholder')" @keydown.ctrl.enter="run" />
            <div class="mt-2 flex flex-wrap gap-1.5">
              <button v-for="(ex, i) in EXAMPLES" :key="i" type="button" class="rounded-full border border-line px-2.5 py-1 text-xs text-muted hover:text-ink" @click="prompt = t(ex)">{{ t(ex) }}</button>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <button class="btn-primary" :disabled="!canRun" @click="run">{{ sending ? t('tryit.running') : t('tryit.run') }}</button>
            <button v-if="sending" class="btn-ghost" @click="stop">{{ t('tryit.stop') }}</button>
          </div>
        </div>
      </div>

      <div class="app-panel min-h-[18rem] p-4">
        <p class="text-xs text-muted">{{ t('tryit.output') }}</p>
        <div v-if="error" class="mt-3 inline-alert" role="alert">{{ error }}</div>
        <div v-if="answer" class="mt-3 whitespace-pre-wrap break-words text-sm leading-relaxed">{{ answer }}<span v-if="sending" class="animate-pulse">▋</span></div>
        <div v-else-if="!error" class="mt-10 text-center text-sm text-muted">{{ sending ? t('tryit.thinking') : t('tryit.outputEmpty') }}</div>
        <div v-if="done && !auth.isAuthed" class="mt-5 rounded-lg border border-accent/30 bg-accent/5 p-4 text-center">
          <p class="text-sm font-medium">{{ t('tryit.convTitle') }}</p>
          <router-link to="/register" class="btn-primary mt-3 inline-flex !py-2 px-5">{{ t('tryit.convCta') }}</router-link>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.try-step { position: relative; }
.try-step-n {
  display: inline-flex; align-items: center; justify-content: center;
  width: 1.6rem; height: 1.6rem; border-radius: 9999px;
  background: rgb(var(--c-accent) / 0.12); color: rgb(var(--c-accent));
  font-weight: 600; font-size: 0.8rem;
}
</style>
