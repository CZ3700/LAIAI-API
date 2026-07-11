<script setup lang="ts">
import { computed, onActivated, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/lib/api'
import { modelModality, type PricedModel } from '@/lib/modelCatalog'

// 多模型对比（批 D-1）：同一提示词并行打 2~4 个模型，列式对比输出/首字延迟/总耗时/token/估算费用。
// 复用体验场的 Key（sessionStorage 同键）与 /v1 直打模式；chat 模态 only（图/视频对比无意义且贵）。
// keep-alive（Shell include）：切页保留对比结果。
defineOptions({ name: 'Compare' })

// 非 chat 的 category 排除轴：modelModality 的按名兜底覆盖 image/video/embedding，
// audio/tts/rerank 等只有元数据 category 能识别——双信号都过才算 chat（对抗 review）。
const NON_CHAT = new Set([
  'image', 'images', 'audio', 'tts', 'stt', 'speech', 'transcription',
  'embedding', 'embeddings', 'rerank', 'moderation', 'video', 'music',
])

interface RunState {
  model: string
  status: 'idle' | 'running' | 'done' | 'error'
  content: string
  error: string
  frtMs: number | null // 首字延迟
  totalMs: number | null
  usage: { prompt_tokens: number; completion_tokens: number } | null
}

const { t } = useI18n()

const KEY_STORE = 'laiai_pg_key' // 与体验场同一把 Key（sessionStorage，关标签页即失效）
const apiKey = ref(sessionStorage.getItem(KEY_STORE) ?? '')
function rememberKey() {
  if (apiKey.value) sessionStorage.setItem(KEY_STORE, apiKey.value)
  else sessionStorage.removeItem(KEY_STORE)
}

const models = ref<PricedModel[]>([])
// 双信号过滤：category 排除（audio/rerank 等）+ 共享按名兜底（无元数据行的模型 category 缺失，
// modelModality 用 image_usd/模型名启发判 image/video/embedding——对抗 review CONFIRMED）
const chatModels = computed(() =>
  models.value.filter((m) => !NON_CHAT.has(m.category?.toLowerCase() ?? '') && modelModality(m) === 'chat'),
)
const MAX_MODELS = 4
const selected = ref<string[]>([])
function toggleModel(name: string) {
  const i = selected.value.indexOf(name)
  if (i >= 0) selected.value.splice(i, 1)
  else if (selected.value.length < MAX_MODELS) selected.value.push(name)
}

const prompt = ref('')
const system = ref('')
const temperature = ref(1)
const maxTokens = ref(1024)
const runs = ref<RunState[]>([])
const running = ref(false)
let aborts: AbortController[] = []

const canRun = computed(
  () => !!apiKey.value && selected.value.length >= 2 && !!prompt.value.trim() && !running.value,
)

// 估算费用：标价 × usage（展示用途；实际账单含分组倍率/缓存折扣等，以请求日志为准）
function estCost(r: RunState): string {
  if (!r.usage) return ''
  const p = models.value.find((m) => m.model === r.model)
  if (!p) return ''
  const cost =
    (r.usage.prompt_tokens / 1e6) * parseFloat(p.input_usd_per_1m || '0') +
    (r.usage.completion_tokens / 1e6) * parseFloat(p.output_usd_per_1m || '0')
  return cost > 0 ? `$${cost.toFixed(6)}` : ''
}

async function runOne(run: RunState, ac: AbortController) {
  const t0 = performance.now()
  run.status = 'running'
  try {
    const resp = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${apiKey.value}` },
      body: JSON.stringify({
        model: run.model,
        messages: [
          ...(system.value ? [{ role: 'system', content: system.value }] : []),
          { role: 'user', content: prompt.value.trim() },
        ],
        // 清空输入框时 v-model.number 得 ''，Number('') === 0 会把 max_tokens:0 发上游（400）
        // ——非法/空值回退默认（对抗 review）
        temperature: Number.isFinite(Number(temperature.value)) && String(temperature.value) !== '' ? Number(temperature.value) : 1,
        max_tokens: Number(maxTokens.value) >= 1 ? Math.floor(Number(maxTokens.value)) : 1024,
        stream: true,
      }),
      signal: ac.signal,
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
      const { done, value } = await reader.read()
      if (done) break
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
          if (j.error) {
            streamErr = typeof j.error === 'object' ? (j.error.message ?? JSON.stringify(j.error)) : String(j.error)
            continue
          }
          const delta = j.choices?.[0]?.delta?.content
          if (delta) {
            if (run.frtMs === null) run.frtMs = Math.round(performance.now() - t0)
            run.content += delta
          }
          if (j.usage) run.usage = j.usage
        } catch {
          // 单帧解析失败容忍（心跳/注释帧）
        }
      }
    }
    if (!run.content) throw new Error(streamErr || t('play.noReply'))
    run.totalMs = Math.round(performance.now() - t0)
    if (streamErr) {
      // 流中途上游错误帧（已有部分内容也不能标 Done——截断输出冒充完整结果会误导对比，
      // 对抗 review CONFIRMED）：保留已流出的内容，状态记失败
      run.error = streamErr
      run.status = 'error'
      return
    }
    run.status = 'done'
  } catch (e) {
    run.totalMs = Math.round(performance.now() - t0)
    run.error = ac.signal.aborted ? t('cmp.aborted') : e instanceof Error ? e.message : String(e)
    run.status = 'error'
  }
}

async function runAll() {
  if (!canRun.value) return
  stopAll()
  running.value = true
  runs.value = selected.value.map((m) => ({
    model: m, status: 'idle', content: '', error: '', frtMs: null, totalMs: null, usage: null,
  }))
  aborts = runs.value.map(() => new AbortController())
  await Promise.allSettled(runs.value.map((r, i) => runOne(r, aborts[i])))
  running.value = false
}

function stopAll() {
  aborts.forEach((a) => a.abort())
  aborts = []
}

async function copyOut(r: RunState) {
  try {
    await navigator.clipboard.writeText(r.content)
  } catch {
    // clipboard 不可用（非 https 等）：忽略
  }
}

async function loadModels() {
  try {
    const r = await api<{ models: PricedModel[] }>('GET', '/api/pricing')
    models.value = r.models
  } catch {
    // 无价目不阻塞
  }
}
onMounted(loadModels)
// keep-alive 下 onMounted 只跑一次：首拉失败（后端重启/网络抖动）会让本页整个会话无模型可选
// ——切回时空则重拉（对抗 review）
onActivated(() => {
  if (!models.value.length) void loadModels()
})
</script>

<template>
  <div class="app-page">
    <div class="app-page-header">
      <div>
        <p class="app-eyebrow">{{ t('cmp.eyebrow') }}</p>
        <h1 class="app-page-title">{{ t('cmp.title') }}</h1>
        <p class="app-page-subtitle">{{ t('cmp.subtitle') }}</p>
      </div>
    </div>

    <div class="grid gap-5 xl:grid-cols-[22rem_1fr]">
      <!-- 左侧设置 -->
      <aside class="app-panel h-fit overflow-hidden xl:sticky xl:top-7">
        <div class="border-b border-line px-4 py-3">
          <p class="text-sm font-medium">{{ t('cmp.settings') }}</p>
          <p class="mt-1 text-xs text-muted">{{ t('cmp.settingsDesc', { n: MAX_MODELS }) }}</p>
        </div>
        <div class="space-y-4 p-4">
          <div>
            <label class="label" for="cmp-key">{{ t('play.apiKey') }}</label>
            <input
              id="cmp-key" v-model="apiKey" class="input font-mono text-xs" type="password"
              autocomplete="off" placeholder="sk-laiai-..." @change="rememberKey"
            />
            <p v-if="!apiKey" class="mt-2 text-xs text-muted">
              {{ t('play.keyHint') }}
              <router-link class="text-accent hover:underline" to="/app/keys">{{ t('nav.keys') }}</router-link>
            </p>
          </div>

          <div>
            <p class="label">{{ t('cmp.models') }} ({{ selected.length }}/{{ MAX_MODELS }})</p>
            <div class="max-h-64 space-y-1 overflow-y-auto rounded-lg border border-line p-2">
              <label
                v-for="m in chatModels" :key="m.model"
                class="flex cursor-pointer items-center gap-2 rounded px-2 py-1 text-xs hover:bg-line/40"
                :class="{ 'opacity-40': !selected.includes(m.model) && selected.length >= MAX_MODELS }"
              >
                <input
                  type="checkbox" :checked="selected.includes(m.model)"
                  :disabled="!selected.includes(m.model) && selected.length >= MAX_MODELS"
                  @change="toggleModel(m.model)"
                />
                <span class="truncate font-mono">{{ m.model }}</span>
                <span class="ml-auto shrink-0 text-muted">{{ m.provider }}</span>
              </label>
              <p v-if="!chatModels.length" class="px-2 py-1 text-xs text-muted">{{ t('cmp.noModels') }}</p>
            </div>
          </div>

          <div>
            <label class="label" for="cmp-system">{{ t('play.system') }}</label>
            <textarea id="cmp-system" v-model="system" class="input min-h-16 resize-y" :placeholder="t('play.systemPlaceholder')" />
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label class="label" for="cmp-temp">{{ t('play.temperature') }}</label>
              <input id="cmp-temp" v-model.number="temperature" type="number" min="0" max="2" step="0.1" class="input" />
            </div>
            <div>
              <label class="label" for="cmp-max">{{ t('play.maxTokens') }}</label>
              <input id="cmp-max" v-model.number="maxTokens" type="number" min="1" max="32000" class="input" />
            </div>
          </div>
        </div>
      </aside>

      <!-- 右侧：提示词 + 对比列 -->
      <div class="space-y-4">
        <div class="app-panel p-4">
          <label class="label" for="cmp-prompt">{{ t('cmp.prompt') }}</label>
          <textarea
            id="cmp-prompt" v-model="prompt" class="input min-h-24 resize-y"
            :placeholder="t('cmp.promptPlaceholder')" @keydown.ctrl.enter="runAll"
          />
          <div class="mt-3 flex items-center gap-3">
            <button class="btn-primary" :disabled="!canRun" @click="runAll">
              {{ running ? t('cmp.running') : t('cmp.run') }}
            </button>
            <button v-if="running" class="btn-ghost" @click="stopAll">{{ t('play.stop') }}</button>
            <p v-if="selected.length < 2" class="text-xs text-muted">{{ t('cmp.pickAtLeastTwo') }}</p>
          </div>
        </div>

        <div v-if="runs.length" class="grid gap-4" :class="runs.length === 2 ? 'lg:grid-cols-2' : 'lg:grid-cols-2 2xl:grid-cols-4'">
          <div v-for="r in runs" :key="r.model" class="app-panel flex min-h-64 flex-col overflow-hidden">
            <div class="flex items-center gap-2 border-b border-line px-4 py-2.5">
              <span class="truncate font-mono text-xs font-semibold">{{ r.model }}</span>
              <span
                class="ml-auto shrink-0 rounded-full px-2 py-0.5 text-[10px] font-medium"
                :class="{
                  'bg-line/60 text-muted': r.status === 'idle',
                  'bg-accent/15 text-accent': r.status === 'running',
                  'bg-emerald-500/15 text-emerald-500': r.status === 'done',
                  'bg-red-500/15 text-red-500': r.status === 'error',
                }"
              >{{ t('cmp.status.' + r.status) }}</span>
              <button v-if="r.content" class="btn-ghost !min-h-0 !px-2 !py-1 text-[10px]" @click="copyOut(r)">{{ t('cmp.copy') }}</button>
            </div>
            <div class="flex-1 overflow-y-auto whitespace-pre-wrap break-words px-4 py-3 text-sm">
              <span v-if="r.content">{{ r.content }}</span>
              <span v-else-if="r.status === 'running'" class="text-muted">…</span>
              <span v-else-if="r.error" class="text-red-500">{{ r.error }}</span>
            </div>
            <div class="flex flex-wrap gap-x-4 gap-y-1 border-t border-line px-4 py-2 font-mono text-[11px] text-muted">
              <span v-if="r.frtMs !== null">{{ t('cmp.frt') }} {{ r.frtMs }}ms</span>
              <span v-if="r.totalMs !== null">{{ t('cmp.total') }} {{ r.totalMs }}ms</span>
              <span v-if="r.usage">{{ r.usage.prompt_tokens }}+{{ r.usage.completion_tokens }} tok</span>
              <span v-if="estCost(r)">≈{{ estCost(r) }}</span>
            </div>
          </div>
        </div>
        <p v-if="runs.length && runs.some((r) => r.usage)" class="text-xs text-muted">{{ t('cmp.costNote') }}</p>
      </div>
    </div>
  </div>
</template>
