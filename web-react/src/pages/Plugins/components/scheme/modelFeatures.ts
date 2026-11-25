/** 创作中心2026 功能特性目录。用户端视频分类按模型绑定方案的 IO 启用口展示。 */

export type ModelFeatureKind = 'image' | 'video' | 'audio' | 'chat';

export type ModelFeature = {
  key: string;
  label: string;
  kind: ModelFeatureKind;
};

const MODEL_FEATURES: ModelFeature[] = [
  { key: 'create-image', label: '创建图片', kind: 'image' },
  { key: 'edit-image', label: '编辑图片', kind: 'image' },
  { key: 'edit-mask', label: '蒙版', kind: 'image' },
  { key: 'edit-bbox', label: '坐标框', kind: 'image' },
  { key: 'upscale-image', label: '放大图片', kind: 'image' },
  { key: 'reference-i2v', label: '参考生视频', kind: 'video' },
  { key: 'text-to-video', label: '文生视频', kind: 'video' },
  { key: 'image-to-video', label: '图生视频', kind: 'video' },
  { key: 'starting-frame', label: '首尾帧生视频', kind: 'video' },
  { key: 'edit-video', label: '编辑视频', kind: 'video' },
  { key: 'extend', label: '延长视频', kind: 'video' },
  { key: 'lipsync', label: '对口型', kind: 'video' },
  { key: 'speech', label: '语音合成', kind: 'audio' },
  { key: 'audio-gen', label: '音频生成', kind: 'audio' },
  { key: 'understand-image', label: '理解图片', kind: 'chat' },
  { key: 'understand-audio', label: '理解音频', kind: 'chat' },
  { key: 'understand-video', label: '理解视频', kind: 'chat' },
  { key: 'understand-file', label: '理解文件', kind: 'chat' },
];

export const CHAT_FEATURE_IMAGE = 'understand-image';
export const CHAT_FEATURE_AUDIO = 'understand-audio';
export const CHAT_FEATURE_VIDEO = 'understand-video';
export const CHAT_FEATURE_FILE = 'understand-file';
export const CHAT_UNDERSTAND_ATTRS = ['理解图片', '理解音频', '理解视频', '理解文件'];

/** 图片生成的二级功能属性。旧名「图生图」并入「多图文生图」。 */
export const IMAGE_GENERATION_ATTRS = ['文生图', '图文生图', '多图文生图'] as const;

const IMAGE_ATTR_ALIAS: Record<string, string> = {
  图生图: '多图文生图',
  创建图片: '文生图',
  编辑图片: '图文生图',
};

export function normalizeImageFeatureAttrs(attrs: string[]): string[] {
  const out: string[] = [];
  for (const raw of attrs) {
    const mapped = IMAGE_ATTR_ALIAS[raw] || raw;
    if (!mapped || out.includes(mapped)) continue;
    out.push(mapped);
  }
  return out;
}

/** 选项固定为文生图 / 图文生图 / 多图文生图，蒙版、坐标框等其余属性留在后面。 */
export function imageFeatureOptions(attrs: string[]): string[] {
  const normalized = normalizeImageFeatureAttrs(attrs);
  const extras = normalized.filter((item) => !IMAGE_GENERATION_ATTRS.includes(item as (typeof IMAGE_GENERATION_ATTRS)[number]));
  return [...IMAGE_GENERATION_ATTRS, ...extras];
}

export function featureKindFromTypeName(typeName?: string | null): ModelFeatureKind | null {
  const n = typeName || '';
  if (n.includes('图片')) return 'image';
  if (n.includes('聊天') || n.includes('对话')) return 'chat';
  if (n.includes('视频')) return 'video';
  if (n.includes('音频')) return 'audio';
  return null;
}

export function modelHasChatFeature(
  model: { feature_keys?: string[] } | null | undefined,
  key: string,
): boolean {
  return Array.isArray(model?.feature_keys) && model.feature_keys.includes(key);
}

export function parseFeatureAttrList(raw: unknown): string[] {
  if (Array.isArray(raw)) {
    const out: string[] = [];
    for (const x of raw) {
      if (typeof x !== 'string') continue;
      const t = x.trim();
      if (!t || out.includes(t)) continue;
      out.push(t);
    }
    return out;
  }
  if (typeof raw === 'string' && raw.trim()) {
    try {
      return parseFeatureAttrList(JSON.parse(raw));
    } catch {
      return [];
    }
  }
  return [];
}

function featureLabel(key: string): string {
  return MODEL_FEATURES.find((f) => f.key === key)?.label || key;
}

export const DEFAULT_VIDEO_FEATURE = 'reference-i2v';

export function videoFeatures(): ModelFeature[] {
  return MODEL_FEATURES.filter((f) => f.kind === 'video');
}

/** 用户端 public-config.feature_catalog.groups[].items */
export function videoFeaturesFromCatalog(catalog: unknown): ModelFeature[] {
  const fallback = videoFeatures();
  if (!catalog || typeof catalog !== 'object') return fallback;
  const groups = (catalog as { groups?: unknown }).groups;
  if (!Array.isArray(groups)) return fallback;
  const video = groups.find(
    (g) => g && typeof g === 'object' && (g as { kind?: string }).kind === 'video',
  );
  const items =
    video && typeof video === 'object' ? (video as { items?: unknown }).items : null;
  if (!Array.isArray(items) || items.length === 0) return fallback;
  const parsed: ModelFeature[] = [];
  for (const item of items) {
    if (!item || typeof item !== 'object') continue;
    const key = (item as { key?: unknown }).key;
    if (typeof key !== 'string' || !key) continue;
    const label = (item as { label?: unknown }).label;
    parsed.push({
      key,
      label: typeof label === 'string' && label ? label : featureLabel(key),
      kind: 'video',
    });
  }
  return parsed.length ? parsed : fallback;
}

export function isVideoFeatureKey(key: string, features?: ModelFeature[]): boolean {
  const list = features && features.length ? features : videoFeatures();
  return list.some((f) => f.key === key);
}

/** 方案 IO 启用口 → 用户端视频功能分类 */
const VIDEO_MODE_FROM_IO: Array<{ key: string; ports: string[] }> = [
  {
    key: 'reference-i2v',
    ports: ['reference_images', 'reference_videos', 'reference_audio', 'reference_file'],
  },
  { key: 'text-to-video', ports: ['prompt'] },
  { key: 'image-to-video', ports: ['start_frame'] },
  { key: 'starting-frame', ports: ['end_frame'] },
  { key: 'edit-video', ports: ['edit_video'] },
  { key: 'extend', ports: ['extend_video'] },
];

function strField(v: unknown): string {
  return typeof v === 'string' ? v.trim().toLowerCase() : '';
}

/** 把启用口归到目录 key，供功能分类判断（含 bind_key / 文案别名） */
function canonicalVideoIoKeys(port: Record<string, unknown>): string[] {
  if (port.enabled === false) return [];
  const k = strField(port.key);
  const bind = strField(port.bind_key);
  const label = strField(port.label);
  const modality = strField(port.modality);
  if (!k && !bind && !modality) return [];

  const keys: string[] = [];
  const isNegPrompt = k === 'negative_prompt' || bind === 'negative_prompt';
  const isStart =
    k === 'start_frame' ||
    k === 'first_frame' ||
    k.includes('start_frame') ||
    bind === 'image_url' ||
    bind === 'first_frame' ||
    label.includes('首帧') ||
    label.includes('起始帧');
  const isEnd =
    k === 'end_frame' ||
    k === 'last_frame' ||
    k.includes('end_frame') ||
    k.includes('last_frame') ||
    bind === 'end_image_url' ||
    bind === 'last_frame' ||
    label.includes('尾帧') ||
    label.includes('结束帧');
  const isRefImg =
    !isStart &&
    !isEnd &&
    (k === 'reference_images' || bind === 'image_urls' || bind === 'reference_urls');
  const isEdit =
    k === 'edit_video' ||
    bind === 'feature:edit_video' ||
    label.includes('编辑视频');
  const isExtend =
    k === 'extend_video' ||
    bind === 'feature:extend_video' ||
    label.includes('延长视频');
  const isRefVid =
    !isEdit &&
    !isExtend &&
    (k === 'reference_videos' ||
      k === 'source_video' ||
      bind === 'video_urls' ||
      (modality === 'video' && !isStart && !isEnd));
  const isRefAud = k === 'reference_audio' || bind === 'audio_urls' || modality === 'audio';
  const isOmniFile =
    k === 'reference_file' ||
    k === 'webpage_link' ||
    k === 'files' ||
    k === 'links' ||
    modality === 'file';
  const isPrompt = !isNegPrompt && (k === 'prompt' || bind === 'prompt' || modality === 'text');

  if (isPrompt) keys.push('prompt');
  if (isStart) keys.push('start_frame');
  if (isEnd) keys.push('end_frame');
  if (isRefImg) keys.push('reference_images');
  if (isRefVid) keys.push('reference_videos');
  if (isRefAud) keys.push('reference_audio');
  if (isEdit) keys.push('edit_video');
  if (isExtend) keys.push('extend_video');
  if (isOmniFile) keys.push('reference_file');
  if (k) keys.push(k);
  return keys;
}

function enabledSchemeInputKeys(scheme: { inputs?: unknown }): Set<string> {
  const out = new Set<string>();
  const inputs = Array.isArray(scheme.inputs) ? scheme.inputs : [];
  for (const p of inputs) {
    if (!p || typeof p !== 'object') continue;
    const rec = p as Record<string, unknown>;
    if (rec.enabled === false) continue;
    for (const key of canonicalVideoIoKeys(rec)) {
      out.add(key);
    }
  }
  return out;
}

/** 视频方案当前 IO 实际支持的生成方式（仅已启用口） */
export function videoGenerationModesFromScheme(scheme: {
  id?: string;
  type?: string;
  inputs?: unknown;
  allow_text_to_video?: boolean;
}): ModelFeature[] {
  if (scheme?.type && scheme.type !== 'video') return [];
  const enabled = enabledSchemeInputKeys(scheme);
  if (enabled.size === 0) return [];
  const blockText = scheme.allow_text_to_video === false || scheme.id === 'viduq4-preview';
  return VIDEO_MODE_FROM_IO.filter((row) => row.ports.some((k) => enabled.has(k)))
    .filter((row) => !(blockText && row.key === 'text-to-video'))
    .map((row) => MODEL_FEATURES.find((f) => f.key === row.key))
    .filter((f): f is ModelFeature => !!f);
}

/** 用户端模型：按绑定方案（含 IO 覆写）的启用口推导功能分类 */
export function videoFeatureKeysFromModel(
  m: {
    inputs?: unknown;
    scheme_id?: string;
    allow_text_to_video?: boolean;
    feature_keys?: string[];
  } | null | undefined,
): string[] {
  if (!m) return [];
  const keys = videoGenerationModesFromScheme({
    type: 'video',
    inputs: m.inputs,
    id: m.scheme_id,
    allow_text_to_video: m.scheme_id === 'viduq4-preview' ? false : m.allow_text_to_video,
  }).map((f) => f.key);
  if (m.feature_keys?.includes('lipsync') && !keys.includes('lipsync')) keys.push('lipsync');
  return keys;
}

function modelSupportsVideoFeature(m: { inputs?: unknown }, key: string): boolean {
  return videoFeatureKeysFromModel(m).includes(key);
}

export const AUDIO_TOOL_SPEECH = 'speech';
export const AUDIO_TOOL_GEN = 'audio-gen';

type AudioClassified = {
  feature_keys?: string[];
  scheme_id?: string;
};

/** 二级属性优先。还没写属性时，音频生成方案单独归到音频生成，其余音频模型归语音合成。 */
export function audioModelMatchesTool(model: AudioClassified, tool: string): boolean {
  const keys = model.feature_keys || [];
  const tagged = keys.includes(AUDIO_TOOL_SPEECH) || keys.includes(AUDIO_TOOL_GEN);
  if (tagged) return keys.includes(tool);
  const gen = model.scheme_id === 'doubao_seed_audio_1_0';
  return tool === (gen ? AUDIO_TOOL_GEN : AUDIO_TOOL_SPEECH);
}

export function audioToolOfModel(model: AudioClassified): string {
  if (audioModelMatchesTool(model, AUDIO_TOOL_GEN) && !audioModelMatchesTool(model, AUDIO_TOOL_SPEECH)) {
    return AUDIO_TOOL_GEN;
  }
  return AUDIO_TOOL_SPEECH;
}

export function modelsForFeature<T extends { inputs?: unknown }>(models: T[], key: string): T[] {
  return models.filter((m) => modelSupportsVideoFeature(m, key));
}

function modelHasFeatureKey(
  model: { feature_keys?: string[] } | null | undefined,
  key: string,
): boolean {
  return Array.isArray(model?.feature_keys) && model.feature_keys.includes(key);
}

export function modelsForImageFeatureKey<T extends { feature_keys?: string[] }>(
  models: T[],
  key: string,
): T[] {
  return models.filter((m) => modelHasFeatureKey(m, key));
}

function enabledReferenceImageMax(model: {
  inputs?: unknown;
  max_reference_images?: number;
}): number {
  const inputs = Array.isArray(model.inputs) ? model.inputs : [];
  const port = inputs.find((item) => {
    if (!item || typeof item !== 'object') return false;
    const row = item as { key?: string; bind_key?: string; enabled?: boolean };
    if (row.enabled === false) return false;
    return row.key === 'reference_images'
      || row.bind_key === 'image_urls'
      || row.bind_key === 'reference_urls';
  }) as { max?: number } | undefined;
  if (port) {
    const max = Number(port.max);
    return Number.isFinite(max) ? max : 1;
  }
  if (inputs.length > 0) return 0;
  const legacy = Number(model.max_reference_images);
  return Number.isFinite(legacy) && legacy >= 0 ? legacy : 0;
}

/** 图文生图：勾了该特性，或方案参考图口已启用且数量上限至少为 1。多图文生图同样能收一张参考图。 */
export function modelSupportsImageTextToImage(model: {
  feature_keys?: string[];
  inputs?: unknown;
  max_reference_images?: number;
} | null | undefined): boolean {
  if (!model) return false;
  if (modelHasFeatureKey(model, 'edit-image')) return true;
  return enabledReferenceImageMax(model) >= 1;
}

export function modelsForImageTextToImage<T extends {
  feature_keys?: string[];
  inputs?: unknown;
  max_reference_images?: number;
}>(models: T[]): T[] {
  return models.filter((model) => modelSupportsImageTextToImage(model));
}

export function pickVideoFeatureForModel(
  m: { inputs?: unknown },
  preferred?: string,
): string {
  const keys = videoFeatureKeysFromModel(m);
  if (preferred && keys.includes(preferred)) return preferred;
  if (keys.includes(DEFAULT_VIDEO_FEATURE)) return DEFAULT_VIDEO_FEATURE;
  return keys[0] || DEFAULT_VIDEO_FEATURE;
}
