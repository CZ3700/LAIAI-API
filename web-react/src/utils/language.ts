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
  if (lower === 'zh-tw' || lower.startsWith('zh-hant') || lower === 'zh-hk' || lower === 'zh-mo') {
    return 'zh-TW';
  }
  return raw.split('-')[0] || 'zh';
}

/** Intl/Date 用的 BCP 47 locale */
export function toDisplayLocale(lang?: string): string {
  const raw = (lang || 'zh').toLowerCase().replace('_', '-');
  if (raw === 'zh-tw' || raw.startsWith('zh-hant') || raw === 'zh-hk' || raw === 'zh-mo') return 'zh-TW';
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
