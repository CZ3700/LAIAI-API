export type ModelModality = 'chat' | 'image' | 'video' | 'embedding'

export interface PricedModel {
  model: string
  provider: string
  input_usd_per_1m: string
  output_usd_per_1m: string
  image_usd: string
  vendor?: string
  category?: string
  tags?: string
  context_length?: number
  icon?: string
}

const modalitySearchAliases: Record<ModelModality, string> = {
  chat: 'chat conversation 对话 聊天',
  image: 'image img picture 图像 图片',
  video: 'video 视频 影片',
  embedding: 'embedding embeddings embed 向量',
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
  return [m.model, m.provider, m.vendor, m.category, m.tags].map((v) => normalized(v)).join(' ')
}

function hasWord(text: string, words: string[]): boolean {
  return words.some((word) => new RegExp(`(^|[^a-z0-9])${word}([^a-z0-9]|$)`, 'i').test(text))
}

export function modelModality(m: PricedModel): ModelModality {
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
