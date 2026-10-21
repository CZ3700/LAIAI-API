import type { PricingDescriptor } from '../components/pricing/types'

export type ModelModality = 'chat' | 'image' | 'video' | 'embedding' | 'audio' | 'other'

export interface PricedModel {
  model: string
  provider: string
  input_usd_per_1m?: string
  output_usd_per_1m?: string
  image_usd?: string
  per_call_usd?: string // 按次固定价（MJ action/转写类；非零才下发）
  cached_input_usd_per_1m?: string // 缓存命中输入折扣价（非零才下发）
  vendor?: string
  category?: string
  tags?: string
  context_length?: number
  icon?: string
  description?: string
  pricing?: PricingDescriptor
}

const modalitySearchAliases: Record<ModelModality, string> = {
  chat: 'chat conversation 对话 聊天',
  image: 'image img picture 图像 图片',
  video: 'video 视频 影片',
  embedding: 'embedding embeddings embed 向量',
  audio: 'audio speech transcription 音频 语音',
  other: 'other 其他',
}

function hasPositivePrice(value?: string): boolean {
  if (!value) return false
  const n = Number(value)
  return Number.isFinite(n) && n > 0
}

function normalized(value?: string): string {
  return (value ?? '').trim().toLowerCase()
}

function fieldText(m: PricedModel): string {
  return [m.model, m.provider, m.vendor, m.category, m.tags, m.description].map((v) => normalized(v)).join(' ')
}

function hasWord(text: string, words: string[]): boolean {
  return words.some((word) => new RegExp(`(^|[^a-z0-9])${word}([^a-z0-9]|$)`, 'i').test(text))
}

export function modelModality(m: PricedModel): ModelModality {
  if (m.pricing) {
    const { billing_kind: kind, endpoints } = m.pricing
    if (kind === 'video' || kind === 'generic_video') return 'video'
    if (kind === 'image') return 'image'
    if (kind === 'audio') return 'audio'
    if (endpoints.includes('/v1/embeddings')) return 'embedding'
    if (endpoints.includes('/v1/chat/completions')) return 'chat'
    if (endpoints.includes('/v1/images/generations')) return 'image'
    return 'other'
  }
  const text = fieldText(m)
  if (
    hasWord(text, ['video', 'seedance', 'sora', 'veo', 'kling', 'runway', 'hailuo', 'pika', 'wan']) ||
    /minimax[._-]?video/i.test(m.model)
  ) {
    return 'video'
  }
  if (hasPositivePrice(m.image_usd) || hasWord(text, ['image', 'img', 'dall', 'imagen', 'seedream', 'flux', 'sdxl'])) {
    return 'image'
  }
  if (hasWord(text, ['embedding', 'embeddings', 'embed'])) {
    return 'embedding'
  }
  return 'chat'
}

export type CatalogCategory = 'text' | 'image' | 'video' | 'embedding' | 'audio' | 'other'
export function catalogCategory(m: PricedModel): CatalogCategory {
  const kind = m.pricing?.billing_kind
  if (kind === 'video' || kind === 'generic_video') return 'video'
  if (kind === 'image') return 'image'
  if (kind === 'audio') return 'audio'
  const endpoints = m.pricing?.endpoints || []
  if (endpoints.includes('/v1/embeddings') || /^(embedding|embeddings)$/i.test(m.category || '')) return 'embedding'
  if (endpoints.includes('/v1/images/generations') && !endpoints.includes('/v1/chat/completions')) return 'image'
  if (endpoints.includes('/v1/chat/completions') && !endpoints.includes('/v1/images/generations')) return 'text'
  if (kind === 'token') return 'text'
  if (kind) return 'other'
  // Legacy catalogs retain type hints, but never invent a video selling price.
  const modality = modelModality(m)
  return modality === 'chat' ? 'text' : modality
}

export function catalogTags(m: PricedModel): string[] {
  return (m.tags || '').split(/[,，;；]/).map(s => s.trim()).filter(Boolean)
}

export function catalogPricing(m: PricedModel): PricingDescriptor | undefined {
  if (m.pricing) return m.pricing
  if (modelModality(m) === 'video') return undefined
  const fields = [
    ['input_usd_per_1m', 'USD/1M_tokens'], ['output_usd_per_1m', 'USD/1M_tokens'],
    ['cached_input_usd_per_1m', 'USD/1M_tokens'], ['image_usd', 'USD/image'], ['per_call_usd', 'USD/call'],
  ] as const
  const modality = modelModality(m)
  const rates = fields.flatMap(([key, unit]) => {
    const value = m[key]
    const primary = (key === 'input_usd_per_1m' || key === 'output_usd_per_1m') ? ['chat', 'embedding'].includes(modality) : key === 'image_usd' && modality === 'image'
    return value !== undefined && /^(0|[1-9][0-9]*)(\.[0-9]+)?$/.test(value) && (primary || hasPositivePrice(value)) ? [{ key, amount: value, unit }] : []
  })
  if (!rates.length) return undefined
  return { billing_kind: 'mixed', currency: 'USD', unit: 'mixed', status: 'published', rates, combinations: [], endpoints: [] }
}

export function standardVideoRows(m: PricedModel) {
  const cells = catalogPricing(m)?.combinations.filter(c => c.mode === 'generate' && c.service_tier === 'default' && !c.draft) || []
  const resolutions = [...new Set(cells.map(c => c.resolution))].sort((a, b) => {
    const ranks: Record<string, number> = { '480p': 0, '720p': 1, '1080p': 2, '4k': 3 }
    return (ranks[a] ?? 99) - (ranks[b] ?? 99) || a.localeCompare(b)
  })
  return resolutions.map(resolution => ({
    resolution,
    without: cells.find(c => c.resolution === resolution && !c.input_video)?.amount,
    with: cells.find(c => c.resolution === resolution && c.input_video)?.amount,
  }))
}

export function modelDocs(m: PricedModel): string {
  const category = catalogCategory(m)
  return `/docs?section=${category === 'video' ? 'video-intro' : category === 'image' ? 'image-intro' : category === 'embedding' ? 'embeddings' : 'text-intro'}`
}

export type SnippetLanguage = 'curl' | 'python' | 'js'
export function modelSnippet(m: PricedModel, language: SnippetLanguage, base: string): string {
  const pricing = catalogPricing(m)
  if (!pricing) return ''
  const endpoints = pricing.endpoints
  let path = '', body: Record<string, unknown> = {}, idempotent = false
  if (pricing.billing_kind === 'video') {
    const cell = pricing.combinations.find(c => c.mode === 'generate' && c.service_tier === 'default' && !c.draft && !c.input_video)
    if (!cell || !endpoints.includes('/v1/video/generations')) return ''
    path = '/v1/video/generations'
    body = { model: m.model, content: [{ type: 'text', text: 'A paper boat floating on a calm lake.' }], duration: 5, resolution: cell.resolution, ratio: '16:9', generate_audio: false }
    idempotent = true
  } else if (endpoints.includes('/v1/embeddings')) {
    path = '/v1/embeddings'; body = { model: m.model, input: 'Hello' }
  } else if (endpoints.includes('/v1/images/generations')) {
    path = '/v1/images/generations'; body = { model: m.model, prompt: 'A paper boat on a calm lake', n: 1 }
  } else if (catalogCategory(m) === 'text' && endpoints.includes('/v1/chat/completions')) {
    path = '/v1/chat/completions'; body = { model: m.model, messages: [{ role: 'user', content: 'Hello' }] }
  } else return ''
  const json = JSON.stringify(body, null, 2)
  const url = base.replace(/\/$/, '') + path
  if (language === 'curl') {
    const lines = [`curl -sS -X POST '${url}'`, '  -H "Authorization: Bearer $LAIAI_API_KEY"', '  -H "Content-Type: application/json"']
    if (idempotent) lines.push('  -H "Idempotency-Key: $REQUEST_ID"')
    lines.push("  --data-binary @- <<'JSON'")
    return lines.join(' \\\n') + '\n' + json + '\nJSON'
  }
  if (language === 'python') {
    return `import json, os\nfrom urllib.request import Request, urlopen\n\npayload = json.loads(${JSON.stringify(json)})\nheaders = {"Authorization": "Bearer " + os.environ["LAIAI_API_KEY"],\n           "Content-Type": "application/json"}${idempotent ? '\nheaders["Idempotency-Key"] = os.environ["REQUEST_ID"]' : ''}\nrequest = Request(${JSON.stringify(url)}, data=json.dumps(payload).encode(), headers=headers, method="POST")\nwith urlopen(request) as response:\n    print(response.read().decode())`
  }
  return `const response = await fetch(${JSON.stringify(url)}, {\n  method: "POST",\n  headers: {\n    "Authorization": "Bearer " + process.env.LAIAI_API_KEY,\n    "Content-Type": "application/json"${idempotent ? ',\n    "Idempotency-Key": process.env.REQUEST_ID' : ''}\n  },\n  body: JSON.stringify(${json})\n});\nif (!response.ok) throw new Error(await response.text());\nconsole.log(await response.json());`
}

export function modelSearchText(m: PricedModel): string {
  const kind = modelModality(m)
  return `${fieldText(m)} ${kind} ${modalitySearchAliases[kind]}`.trim()
}

export function filterModels<T extends PricedModel>(models: T[], query: string): T[] {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean)
  if (terms.length === 0) return models
  return models.filter((model) => {
    const text = modelSearchText(model)
    return terms.every((term) => text.includes(term))
  })
}
