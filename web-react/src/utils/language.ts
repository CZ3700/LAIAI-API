/** 语言代码 → 显示名称 */
export const LANG_NAME_MAP: Record<string, string> = {
  zh: '简体中文',
  'zh-TW': '繁體中文',
  en: 'English',
};

export function normalizeLanguage(lang?: string): string {
  const c = resolveContentLang(lang);
  if (c === 'zh-TW') return 'zhTW';
  if (c === 'en') return 'en';
  return 'zh';
}

/** 内容/文档语言：保留 zh-TW，其余区域码收成主语言（zh-CN → zh） */
export function resolveContentLang(lang?: string): string {
  const raw = (lang || 'zh').trim();
  const lower = raw.toLowerCase().replace('_', '-');
  if (
    lower === 'zh-tw' ||
    lower === 'zhtw' ||
    lower.startsWith('zh-hant') ||
    lower === 'zh-hk' ||
    lower === 'zh-mo'
  ) {
    return 'zh-TW';
  }
  if (lower === 'zh-cn' || lower === 'zh-hans' || lower === 'zh-sg' || lower === 'zh') {
    return 'zh';
  }
  return lower.split('-')[0] || 'zh';
}

export function isEnglishLang(lang?: string): boolean {
  return resolveContentLang(lang) === 'en';
}

export function isTraditionalChineseLang(lang?: string): boolean {
  return resolveContentLang(lang) === 'zh-TW';
}

const SYS_CLASSIFICATION_KEY_MAP: Record<string, string> = {
  聊天: 'chat',
  视频: 'video',
  图片: 'image',
  音频: 'audio',
  向量: 'embedding',
  排序: 'rerank',
  封装: 'encapsulation',
  未分类: 'unclassified',
  其他: 'other',
  火山引擎: 'volcengine',
  字节火山: 'byte_volc',
  字节跳动: 'bytedance',
  阿里百炼: 'bailian',
  阿里云: 'aliyun',
  腾讯云: 'tencent',
  腾讯混元: 'hunyuan',
  可灵: 'kling',
  快手可灵: 'kuaishou_kling',
  即梦: 'jimeng',
  智谱: 'zhipu',
  月之暗面: 'moonshot',
  深度求索: 'deepseek',
  硅基流动: 'siliconflow',
  百度千帆: 'qianfan',
  讯飞星火: 'spark',
  阶跃星辰: 'stepfun',
  百川智能: 'baichuan',
  其他厂商: 'other_provider',
};

const FEATURE_TAG_KEY_MAP: Record<string, string> = {
  多模态: 'multimodal',
  图片理解: 'understand_image',
  理解图片: 'understand_image',
  音频理解: 'understand_audio',
  理解音频: 'understand_audio',
  视频理解: 'understand_video',
  理解视频: 'understand_video',
  文件理解: 'understand_file',
  理解文件: 'understand_file',
  '输入-文字输入': 'input_text',
  '输入-语音输入': 'input_audio',
  '输入-视频输入': 'input_video',
  '输出-文字输出': 'output_text',
  深度思考: 'deep_thinking',
  工具调用: 'tool_calling',
  联网搜索: 'web_search',
  结构化输出: 'structured_output',
  长上下文: 'long_context',
  全能参考生视频: 'omni_reference_video',
  文生视频: 'text_to_video',
  图生视频: 'image_to_video',
  首尾帧: 'start_end_frame',
  首尾帧生视频: 'start_end_frame_video',
  参考生视频: 'reference_video',
  多图参考: 'multi_image_ref',
  视频参考: 'video_ref',
  音频驱动: 'audio_driven',
  有声视频: 'audio_video',
  动作控制: 'motion_control',
  视频编辑: 'video_edit',
  编辑视频: 'video_edit',
  视频延长: 'video_extend',
  延长视频: 'video_extend',
  对口型: 'lipsync',
  文生图: 'text_to_image',
  图文生图: 'text_image_to_image',
  多图文生图: 'multi_image_to_image',
  图生图: 'image_to_image',
  创建图片: 'create_image',
  编辑图片: 'edit_image',
  局部重绘: 'inpainting',
  高清放大: 'upscale',
  放大图片: 'upscale_image',
  蒙版: 'mask',
  坐标框: 'bbox',
  语音合成: 'tts',
  语音识别: 'asr',
  声音克隆: 'voice_clone',
  音乐生成: 'music_gen',
  音效生成: 'sfx_gen',
  实时语音: 'realtime_audio',
  音频生成: 'audio_gen',
  文本向量: 'text_embedding',
  多模态向量: 'multimodal_embedding',
  语义检索: 'semantic_search',
  重排序: 'rerank',
  文本重排: 'text_rerank',
  多模态重排: 'multimodal_rerank',
  文档检索: 'doc_retrieval',
  多语言排序: 'multilingual_rerank',
  视频画质增强: 'video_enhance',
  视频字幕擦除: 'video_subtitle_erase',
  图像画质增强: 'image_enhance',
  图像背景移除: 'image_bg_remove',
};

export function localizeClassificationName(
  item: { name?: string; name_en?: string } | null | undefined,
  lang: string | undefined,
  t: (k: string, d: string) => string,
): string {
  if (!item) return '';
  const name = item.name || '';
  if (isEnglishLang(lang)) {
    if (name === '未分类') return t('common.unclassified', 'Unclassified');
    if (name === '其他') return t('common.other', 'Other');
    if (item.name_en && item.name_en.trim()) return item.name_en;
  }
  if (isTraditionalChineseLang(lang)) {
    const sysKey = SYS_CLASSIFICATION_KEY_MAP[name];
    if (sysKey) return t(`models.sys_names.${sysKey}`, name);
  }
  return name || item.name_en || '';
}

export function localizeFeatureTag(
  tag: string,
  t: (k: string, d: string) => string,
): string {
  const key = FEATURE_TAG_KEY_MAP[tag];
  if (key) {
    return t(`models.feature_tags.${key}`, tag);
  }
  return tag;
}

/** Intl/Date 用的 BCP 47 locale */
export function toDisplayLocale(lang?: string): string {
  const raw = (lang || 'zh').toLowerCase().replace('_', '-');
  if (raw === 'zh-tw' || raw === 'zhtw' || raw.startsWith('zh-hant') || raw === 'zh-hk' || raw === 'zh-mo') return 'zh-TW';
  if (raw.startsWith('zh')) return 'zh-CN';
  if (raw.startsWith('en')) return 'en-US';
  if (raw.startsWith('ja')) return 'ja-JP';
  if (raw.startsWith('ko')) return 'ko-KR';
  if (raw.startsWith('vi')) return 'vi-VN';
  if (raw.startsWith('th')) return 'th-TH';
  if (raw.startsWith('ms')) return 'ms-MY';
  if (raw.startsWith('fr')) return 'fr-FR';
  if (raw.startsWith('de')) return 'de-DE';
  if (raw.startsWith('es')) return 'es-ES';
  if (raw.startsWith('pt')) return 'pt-BR';
  if (raw.startsWith('ru')) return 'ru-RU';
  if (raw.startsWith('ar')) return 'ar-SA';
  return lang || 'zh-CN';
}

/** 仅在用户手动切换语言时写入；未设置则跟随站点 default_language */
const USER_LANGUAGE_KEY = 'laiai_user_language';

/** 读取用户手动选择的语言；无记录返回 null */
export function getUserLanguagePreference(): string | null {
  const preferred = localStorage.getItem(USER_LANGUAGE_KEY);
  return preferred && preferred.trim() ? preferred : null;
}

/** 持久化用户手动语言偏好，并清理旧 detector 缓存键 */
export function persistUserLanguagePreference(lng: string): void {
  localStorage.setItem(USER_LANGUAGE_KEY, lng);
  // 旧版 LanguageDetector 会把浏览器语言写入此键，干扰「站点默认语言」逻辑
  localStorage.removeItem('i18nextLng');
}
