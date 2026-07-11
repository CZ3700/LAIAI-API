<script setup lang="ts">
import { computed, nextTick, onActivated, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { api } from '@/lib/api'
import { fmtUsd } from '@/lib/money'

// keep-alive（Shell.vue include="Playground"）：切页不销毁——生图/视频结果与各模态输入
// 全在组件内存态，此前导航即丢（用户实测反馈）。name 必须与 include 精确匹配。
defineOptions({ name: 'Playground' })

interface PricedModel {
  model: string
  provider: string
  category?: string // 后端模型元数据的模态/分类（chat/image/audio/embedding…）
}

// 体验场支持的三种模态：chat → /v1/chat/completions；image → /v1/images/generations；
// video → /v1/video/generations（异步任务：提交→轮询→产物播放/下载）。
// 其余模态（audio/embedding/rerank）仍引导走 API（FE-4）。
const IMAGE_CATEGORIES = new Set(['image', 'images'])
const VIDEO_CATEGORIES = new Set(['video'])
const NON_CHAT_CATEGORIES = new Set([
  'image', 'images', 'audio', 'tts', 'stt', 'speech', 'transcription',
  'embedding', 'embeddings', 'rerank', 'moderation', 'video',
])
// 无元数据分类时按模型名兜底识别常见生图族（避免漏判把生图模型当对话）
const IMAGE_NAME_RE = /(^|[-_/])(dall-?e|gpt-image|flux|stable-?diffusion|sdxl|sd[0-9]|cogview|kolors|seedream|hunyuan-image|wanx?|imagen|ideogram|recraft)/i
// 视频族名兜底（wan 不进兜底——与生图 wanx 歧义，靠元数据 category 判定）
const VIDEO_NAME_RE = /(^|[-_/])(seedance|sora|veo|kling|hailuo|vidu|pika|runway)|video/i

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

// 对话保留 3 天（localStorage，跨导航/标签页持久；过期自动丢弃）——切走再切回不再清空。
const CHAT_STORE = 'laiai_pg_chat'
const CHAT_TTL = 3 * 24 * 60 * 60 * 1000
function persistChat() {
  try {
    // 只留完成的消息（不含流式中的空占位气泡）；system 一并存
    const done = messages.value.filter((m) => m.content)
    if (done.length === 0) {
      localStorage.removeItem(CHAT_STORE)
      return
    }
    localStorage.setItem(CHAT_STORE, JSON.stringify({ at: Date.now(), system: system.value, messages: done }))
  } catch {
    // localStorage 满/禁用：忽略（保留只是便利，非核心）
  }
}
function restoreChat() {
  try {
    const raw = localStorage.getItem(CHAT_STORE)
    if (!raw) return
    const saved = JSON.parse(raw) as { at: number; system?: string; messages: Msg[] }
    if (!saved.at || Date.now() - saved.at > CHAT_TTL) {
      localStorage.removeItem(CHAT_STORE)
      return
    }
    if (Array.isArray(saved.messages)) messages.value = saved.messages
    if (saved.system) system.value = saved.system
  } catch {
    localStorage.removeItem(CHAT_STORE)
  }
}
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

// —— video 模态（异步任务）——
interface VideoTask {
  id: string
  model?: string
  status: 'queued' | 'running' | 'succeeded' | 'failed' | string
  fail_reason?: string
  result_url?: string
  cost_usd?: string
  completion_tokens?: number
}
const VIDEO_TASK_STORE = 'laiai_pg_video_task' // 刷新后恢复跟踪（sessionStorage，同 Key 纪律）
const videoPrompt = ref('')
const videoImage = ref('') // 可选参考图 URL（图生视频）
const videoDuration = ref('default')
const videoResolution = ref('default')
const videoRatio = ref('default')
const videoTask = ref<VideoTask | null>(null)
const videoSubmitting = ref(false)
const videoError = ref('')
const videoSrc = ref('') // <video> 播放源（直链或 blob objectURL）
const videoPlayFallback = ref(false) // 直连产物 <video> 拒播（octet-stream 等）→ 提示下载查看
let videoBlobUrl = '' // 待 revoke 的 objectURL
let videoPollTimer: number | null = null
let videoTickTimer: number | null = null
const videoStartMs = ref(0)
const videoElapsedSec = ref(0)
const videoActive = computed(() => {
  const s = videoTask.value?.status
  return s === 'queued' || s === 'running'
})

// —— 请求详情面板（指令 §5：原始 request/response + 耗时）——
const showDetail = ref(false)
const lastDetail = ref<{ endpoint: string; status: number; durationMs: number; request: string; response: string } | null>(null)
function recordDetail(ep: string, status: number, t0: number, requestBody: unknown, response: string) {
  lastDetail.value = {
    endpoint: ep,
    status,
    durationMs: Math.round(performance.now() - t0),
    request: JSON.stringify(requestBody, null, 2),
    response: response.length > 20000 ? response.slice(0, 20000) + '\n…(truncated)' : response,
  }
}
// 图像响应里 b64_json 动辄几 MB，入详情前收敛为占位（面板是给人看请求形状的，不是存图的）
function redactB64(j: unknown): string {
  return JSON.stringify(j, (_k, v) => (typeof v === 'string' && v.length > 2048 ? `<${v.length} chars omitted>` : v), 2)
}

// 选中模型分类（后端 /api/pricing 元数据），用于模态路由（FE-4）
const selectedCategory = computed(
  () => models.value.find((m) => m.model === model.value)?.category?.toLowerCase() ?? '',
)
const isImageModel = computed(() => {
  if (selectedCategory.value) return IMAGE_CATEGORIES.has(selectedCategory.value)
  return IMAGE_NAME_RE.test(model.value) // 无元数据时按名兜底
})
const isVideoModel = computed(() => {
  if (selectedCategory.value) return VIDEO_CATEGORIES.has(selectedCategory.value)
  return VIDEO_NAME_RE.test(model.value)
})
const isChatModel = computed(() => !selectedCategory.value || !NON_CHAT_CATEGORIES.has(selectedCategory.value))
const mode = computed<'chat' | 'image' | 'video' | 'other'>(() =>
  isVideoModel.value ? 'video' : isImageModel.value ? 'image' : isChatModel.value ? 'chat' : 'other',
)
const endpoint = computed(() =>
  mode.value === 'video' ? '/v1/video/generations' : mode.value === 'image' ? '/v1/images/generations' : '/v1/chat/completions',
)

const canSend = computed(
  () => !!apiKey.value && !!model.value && !!input.value.trim() && !sending.value && mode.value === 'chat',
)
const canGenerate = computed(
  () => !!apiKey.value && !!model.value && !!imagePrompt.value.trim() && !generating.value && mode.value === 'image',
)
const canSubmitVideo = computed(
  () => !!apiKey.value && !!model.value && !!videoPrompt.value.trim() && !videoSubmitting.value && !videoActive.value && mode.value === 'video',
)

async function toBottom() {
  await nextTick()
  scrollEl.value?.scrollTo({ top: scrollEl.value.scrollHeight, behavior: 'smooth' })
}

onMounted(async () => {
  restoreChat() // 对话保留 3 天：切走再切回/刷新后恢复
  // 恢复未完成的视频任务跟踪（刷新/误关后回到页面继续轮询）
  const savedTask = sessionStorage.getItem(VIDEO_TASK_STORE)
  if (savedTask && apiKey.value) {
    videoTask.value = { id: savedTask, status: 'running' }
    videoStartMs.value = Date.now() // 刷新后计时从回页起算（提交时刻已不可知）
    startVideoPolling()
  }
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

// keep-alive 下切回页面：Models 页"试一试"深链 ?model= 仍要生效（onMounted 只跑一次）
onActivated(() => {
  const requested = typeof route.query.model === 'string' ? route.query.model : ''
  if (requested && requested !== model.value && models.value.some((m) => m.model === requested)) {
    model.value = requested
  }
})

// keep-alive 下导航离开不触发 onUnmounted（视频轮询后台继续，切回即见结果）；
// 只在组件真正销毁（登出/布局卸载）时清理。
onUnmounted(() => {
  stopVideoPolling()
  if (videoBlobUrl) URL.revokeObjectURL(videoBlobUrl)
})

function clearAll() {
  messages.value = []
  error.value = ''
  lastUsage.value = null
  images.value = []
  genError.value = ''
  localStorage.removeItem(CHAT_STORE) // 显式清空对话 → 同时清持久化
  if (!videoActive.value) resetVideoResult() // 进行中的任务不清（余额已可能被冻结，保留跟踪）
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
  const t0 = performance.now()
  try {
    const resp = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${apiKey.value}` },
      body: JSON.stringify(body),
      signal: abort.signal,
    })
    if (!resp.ok) {
      const data = (await resp.json().catch(() => ({}))) as { error?: { message?: string } | string }
      recordDetail('/v1/chat/completions', resp.status, t0, body, JSON.stringify(data, null, 2))
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
      // 流式详情：SSE 帧不留原文（体量），记录汇总形状 + usage + 耗时
      recordDetail('/v1/chat/completions', resp.status, t0, body,
        JSON.stringify({ streamed: true, content: assistant.value.content, usage: lastUsage.value, error: streamErr || undefined }, null, 2))
      // 流已开始(200)但全程无内容：把静默"转圈"变成可见原因（上游错误帧 / 空回复 / 非 SSE 响应）
      if (!assistant.value.content) throw new Error(streamErr || t('play.noReply'))
    } else {
      const j = (await resp.json()) as {
        choices?: { message?: { content?: string } }[]
        usage?: { prompt_tokens: number; completion_tokens: number }
        error?: { message?: string } | string
      }
      recordDetail('/v1/chat/completions', resp.status, t0, body, JSON.stringify(j, null, 2))
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
    persistChat() // 每轮对话完落盘（保留 3 天）
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
  const t0 = performance.now()
  try {
    const resp = await fetch('/v1/images/generations', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${apiKey.value}` },
      body: JSON.stringify(body),
    })
    if (!resp.ok) {
      const data = (await resp.json().catch(() => ({}))) as { error?: { message?: string } | string }
      recordDetail('/v1/images/generations', resp.status, t0, body, JSON.stringify(data, null, 2))
      const msg =
        typeof data.error === 'object' ? data.error?.message : typeof data.error === 'string' ? data.error : ''
      throw new Error(msg || `request failed (${resp.status})`)
    }
    const j = (await resp.json()) as { data?: GenImage[] }
    recordDetail('/v1/images/generations', resp.status, t0, body, redactB64(j))
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

// —— 视频任务：提交 → 轮询 → 播放/下载 ——

function resetVideoResult() {
  videoTask.value = null
  videoError.value = ''
  videoSrc.value = ''
  videoPlayFallback.value = false
  if (videoBlobUrl) {
    URL.revokeObjectURL(videoBlobUrl)
    videoBlobUrl = ''
  }
  sessionStorage.removeItem(VIDEO_TASK_STORE)
}

function stopVideoPolling() {
  if (videoPollTimer !== null) window.clearInterval(videoPollTimer)
  if (videoTickTimer !== null) window.clearInterval(videoTickTimer)
  videoPollTimer = null
  videoTickTimer = null
}

function startVideoPolling() {
  stopVideoPolling()
  videoElapsedSec.value = Math.max(0, Math.round((Date.now() - videoStartMs.value) / 1000))
  videoTickTimer = window.setInterval(() => {
    videoElapsedSec.value = Math.round((Date.now() - videoStartMs.value) / 1000)
  }, 1000)
  videoPollTimer = window.setInterval(() => void pollVideoOnce(), 5000)
  void pollVideoOnce()
}

async function pollVideoOnce() {
  const id = videoTask.value?.id
  if (!id) return stopVideoPolling()
  try {
    const resp = await fetch(`/v1/video/generations/${encodeURIComponent(id)}`, {
      headers: { Authorization: `Bearer ${apiKey.value}` },
    })
    if (!resp.ok) {
      // 401/403/404 = 不可恢复（key 失效/越权/任务不存在）；5xx/网络抖动留给下一轮
      if ([401, 403, 404].includes(resp.status)) {
        videoError.value = `poll failed (${resp.status})`
        stopVideoPolling()
        sessionStorage.removeItem(VIDEO_TASK_STORE)
      }
      return
    }
    const task = (await resp.json()) as VideoTask
    videoTask.value = task
    if (task.status === 'succeeded') {
      stopVideoPolling()
      sessionStorage.removeItem(VIDEO_TASK_STORE)
      // 终态详情：轮询响应含 result_url 与实际结算费用
      recordDetail(`/v1/video/generations/${id}`, resp.status, performance.now(), { poll: true }, JSON.stringify(task, null, 2))
      await loadVideoResult(task)
    } else if (task.status === 'failed') {
      stopVideoPolling()
      sessionStorage.removeItem(VIDEO_TASK_STORE)
      videoError.value = task.fail_reason || t('play.videoFailed')
    }
  } catch {
    // 网络抖动：下一轮重试（任务在服务端继续跑，不因前端失败而丢）
  }
}

// 产物加载：**统一走平台同源代理** /v1/video/generations/{id}/content（后端对直链也代理并
// 修正 Content-Type）。此前对直链用 <video src=arkurl> 直连会黑屏——对象存储签名 URL 常返
// application/octet-stream 令 <video> 拒播，跨域又 fetch 不到（CORS）。同源代理 fetch+Bearer
// 转 blob，一举绕开 混合内容/CORS/Content-Type 三坑（playground 短片体量可接受）。
async function loadVideoResult(task: VideoTask) {
  if (!task.result_url) {
    videoError.value = t('play.videoNoResult')
    return
  }
  // 直链产物（国内厂商 seedance/hailuo/wan——result_url 是绝对 http URL）：浏览器直连播放。
  // 关键：服务端代理端点 /content 在「海外部署 + 国内对象存储」下会被跨境网络 RST（Connection
  // reset by peer，实测火山 tos-cn-beijing 从海外 VPS 拉取即被重置）——服务器根本代理不到。
  // 客户端浏览器（国内）能直连火山 TOS：<video src> 直连绕开 CORS（媒体元素跨域 src 允许），
  // 下载走 saveVideo（fetch 失败自动回退新标签右键另存）。若 octet-stream 令 <video> 拒播，
  // 用户仍可下载。海外厂商产物（sora/veo）走的是相对代理路径，服务器可达，保持代理下方逻辑。
  if (/^https?:\/\//i.test(task.result_url)) {
    videoSrc.value = task.result_url
    return
  }
  // 相对路径 = 平台代理端点（sora/veo 等海外厂商产物，服务器可达）：fetch+Bearer→blob 绕 Content-Type。
  try {
    const r = await fetch(`/v1/video/generations/${encodeURIComponent(task.id)}/content`, {
      headers: { Authorization: `Bearer ${apiKey.value}` },
    })
    if (!r.ok) throw new Error(`content fetch failed (${r.status})`)
    const blob = await r.blob()
    // 兜底：若代理未修正类型，本地按 video/mp4 重贴（<video> 需可识别的视频 MIME）
    const typed = blob.type && blob.type.startsWith('video/') ? blob : new Blob([blob], { type: 'video/mp4' })
    videoBlobUrl = URL.createObjectURL(typed)
    videoSrc.value = videoBlobUrl
  } catch (e) {
    videoError.value = (e as Error).message
  }
}

async function submitVideo() {
  if (!canSubmitVideo.value) return
  resetVideoResult()
  videoSubmitting.value = true
  const content: unknown[] = [{ type: 'text', text: videoPrompt.value.trim() }]
  if (videoImage.value.trim()) content.push({ type: 'image_url', image_url: { url: videoImage.value.trim() } })
  const body: Record<string, unknown> = { model: model.value, content }
  // 可选参数按厂商方言透传（后端 bindCreateReq 吸收任意顶层参数）；default = 不发，走厂商默认
  if (videoDuration.value !== 'default') body.duration = Number(videoDuration.value)
  if (videoResolution.value !== 'default') body.resolution = videoResolution.value
  if (videoRatio.value !== 'default') body.ratio = videoRatio.value
  const t0 = performance.now()
  try {
    const resp = await fetch('/v1/video/generations', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${apiKey.value}` },
      body: JSON.stringify(body),
    })
    if (!resp.ok) {
      const data = (await resp.json().catch(() => ({}))) as { error?: { message?: string } | string }
      recordDetail('/v1/video/generations', resp.status, t0, body, JSON.stringify(data, null, 2))
      const msg =
        typeof data.error === 'object' ? data.error?.message : typeof data.error === 'string' ? data.error : ''
      throw new Error(msg || `request failed (${resp.status})`)
    }
    const task = (await resp.json()) as VideoTask
    recordDetail('/v1/video/generations', resp.status, t0, body, JSON.stringify(task, null, 2))
    videoTask.value = task
    sessionStorage.setItem(VIDEO_TASK_STORE, task.id)
    videoStartMs.value = Date.now()
    startVideoPolling()
  } catch (e) {
    videoError.value = (e as Error).message
  } finally {
    videoSubmitting.value = false
  }
}

// 下载：blob 源直接触发下载；跨域直链 fetch-blob 失败时回退新标签页（右键另存）。
async function saveVideo() {
  const name = `laiai-${model.value}-video.mp4`.replace(/[^\w.-]+/g, '_')
  const src = videoSrc.value
  if (!src) return
  try {
    let href = src
    let revoke = ''
    if (!src.startsWith('blob:')) {
      const r = await fetch(src)
      href = URL.createObjectURL(await r.blob())
      revoke = href
    }
    const a = document.createElement('a')
    a.href = href
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    if (revoke) URL.revokeObjectURL(revoke)
  } catch {
    window.open(src, '_blank')
  }
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
        <button class="btn-ghost !min-h-0 !px-2.5 !py-1.5 text-xs" :disabled="messages.length === 0 && images.length === 0 && !videoSrc && !videoError" @click="clearAll">{{ t('play.clear') }}</button>
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

        <!-- video 模态设置（可选参数按厂商方言透传） -->
        <template v-else-if="mode === 'video'">
          <div class="grid grid-cols-3 gap-3">
            <div>
              <label class="label" for="pg-vdur">{{ t('play.videoDuration') }}</label>
              <select id="pg-vdur" v-model="videoDuration" class="input">
                <option value="default">default</option>
                <option v-for="d in [4, 5, 6, 8, 10]" :key="d" :value="String(d)">{{ d }}</option>
              </select>
            </div>
            <div>
              <label class="label" for="pg-vres">{{ t('play.videoResolution') }}</label>
              <select id="pg-vres" v-model="videoResolution" class="input">
                <option value="default">default</option>
                <option v-for="r in ['480p', '720p', '1080p']" :key="r" :value="r">{{ r }}</option>
              </select>
            </div>
            <div>
              <label class="label" for="pg-vratio">{{ t('play.videoRatio') }}</label>
              <select id="pg-vratio" v-model="videoRatio" class="input">
                <option value="default">default</option>
                <option v-for="r in ['16:9', '9:16', '1:1', '4:3']" :key="r" :value="r">{{ r }}</option>
              </select>
            </div>
          </div>
          <div>
            <label class="label" for="pg-vimg">{{ t('play.videoRefImage') }}</label>
            <input id="pg-vimg" v-model="videoImage" class="input font-mono text-xs" placeholder="https://…" />
          </div>
          <p class="text-xs text-muted">{{ t('play.videoBillingHint') }}</p>
        </template>
      </div>
      </div>
    </aside>

    <section class="app-panel flex min-h-[34rem] flex-col overflow-hidden">
      <div class="flex items-center justify-between gap-3 border-b border-line px-4 py-3">
        <div class="min-w-0">
          <p class="truncate font-mono text-sm">{{ model || t('play.modelManual') }}</p>
          <p class="text-xs text-muted">{{ (sending || generating || videoSubmitting || videoActive) ? t('surface.playStreaming') : t('surface.playReady') }}</p>
        </div>
        <div class="flex shrink-0 items-center gap-3">
          <p v-if="lastUsage && mode === 'chat'" class="text-xs text-muted">
            {{ t('play.usage', { input: lastUsage.prompt_tokens, output: lastUsage.completion_tokens }) }}
          </p>
          <button
            v-if="lastDetail" type="button" class="btn-ghost !min-h-0 !px-2.5 !py-1.5 text-xs"
            :class="{ '!text-accent': showDetail }" @click="showDetail = !showDetail"
          >{{ t('play.detailBtn') }}</button>
        </div>
      </div>

      <!-- 请求详情抽屉：endpoint/status/耗时 + 原始 request/response（图像 b64 已收敛占位） -->
      <div v-if="showDetail && lastDetail" class="max-h-72 overflow-y-auto border-b border-line bg-elev/30 p-4 text-xs">
        <p class="font-mono text-muted">
          {{ lastDetail.endpoint }} · <span :class="lastDetail.status < 400 ? 'text-ok' : 'text-danger'">{{ lastDetail.status }}</span> · {{ lastDetail.durationMs }}ms
        </p>
        <p class="mt-3 font-medium">{{ t('play.detailRequest') }}</p>
        <pre class="mt-1 overflow-x-auto rounded border border-line bg-panel p-3 font-mono leading-relaxed">{{ lastDetail.request }}</pre>
        <p class="mt-3 font-medium">{{ t('play.detailResponse') }}</p>
        <pre class="mt-1 overflow-x-auto rounded border border-line bg-panel p-3 font-mono leading-relaxed">{{ lastDetail.response }}</pre>
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

      <!-- video 模态：提交 + 轮询进度 + 成片播放/下载 -->
      <template v-else-if="mode === 'video'">
        <div class="flex-1 overflow-y-auto bg-elev/18 p-4 sm:p-5">
          <!-- 进行中：进度态 -->
          <div v-if="videoActive || videoSubmitting" class="grid h-full place-items-center text-center text-muted">
            <div class="max-w-md space-y-3">
              <span class="inline-flex gap-1 py-1">
                <span class="h-2 w-2 animate-bounce rounded-full bg-muted" style="animation-delay: 0ms"></span>
                <span class="h-2 w-2 animate-bounce rounded-full bg-muted" style="animation-delay: 150ms"></span>
                <span class="h-2 w-2 animate-bounce rounded-full bg-muted" style="animation-delay: 300ms"></span>
              </span>
              <p class="text-sm">
                {{ t('play.videoGenerating') }}
                <span class="font-mono">{{ videoTask?.status ?? '…' }}</span>
                · {{ t('play.videoElapsed', { s: videoElapsedSec }) }}
              </p>
              <p class="text-xs">{{ t('play.videoRunningHint') }}</p>
              <p v-if="videoTask?.id" class="select-all font-mono text-[10px] opacity-60">{{ videoTask.id }}</p>
            </div>
          </div>
          <!-- 成片：播放 + 下载 + 实际费用 -->
          <div v-else-if="videoSrc" class="mx-auto max-w-3xl space-y-3">
            <video :src="videoSrc" controls playsinline @error="videoPlayFallback = true" class="w-full rounded-lg border border-line bg-black shadow-card"></video>
            <p v-if="videoPlayFallback" class="text-xs text-warn" role="alert">{{ t('play.videoPlayFallback') }}</p>
            <div class="flex flex-wrap items-center justify-between gap-3">
              <p class="text-xs text-muted">
                <template v-if="videoTask?.cost_usd">{{ t('play.videoCost', { cost: fmtUsd(videoTask.cost_usd) }) }}</template>
              </p>
              <button type="button" class="btn-primary !min-h-0 !px-3 !py-2 text-sm" @click="saveVideo">
                {{ t('play.videoDownload') }}
              </button>
            </div>
          </div>
          <!-- 空态 -->
          <div v-else class="grid h-full place-items-center text-center text-muted">
            <p class="max-w-sm text-sm">{{ t('play.videoEmptyHint') }}</p>
          </div>
        </div>
        <div class="border-t border-line p-3">
          <p v-if="videoError" class="mb-2 text-sm text-danger" role="alert">{{ videoError }}</p>
          <form class="flex gap-2" @submit.prevent="submitVideo">
            <input v-model="videoPrompt" class="input" :placeholder="t('play.videoPromptPlaceholder')" :disabled="videoSubmitting || videoActive" />
            <button type="submit" class="btn-primary shrink-0" :disabled="!canSubmitVideo">
              {{ videoSubmitting || videoActive ? t('play.videoGenerating') : t('play.videoGenerate') }}
            </button>
          </form>
        </div>
      </template>

      <!-- 其余模态（audio/embedding/rerank）：引导走 API -->
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
