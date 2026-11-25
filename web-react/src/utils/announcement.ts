import i18n from 'i18next';
import { sanitizeHtml } from './sanitize';

/**
 * 解析公告多语言内容并净化 HTML
 * 支持三种格式：JSON对象、JSON数组、[lang]标签
 * 返回经过 DOMPurify 净化的安全 HTML 字符串
 */
export const getAnnouncementLabel = (rawText: string): string => {
    if (!rawText) return '';
    let result = rawText;
    try {
        if (rawText.trim().startsWith('{') || rawText.trim().startsWith('[')) {
            const data = JSON.parse(rawText);
            if (data && typeof data === 'object') {
                result = data[i18n.language] || data['en'] || data['zh'] || Object.values(data)[0] || '';
                return sanitizeHtml(String(result));
            }
        }
    } catch (e) {
        // 非 JSON 格式，继续尝试标签解析
    }
    // 尝试 [lang]...[/lang] 标签格式
    const tagReg = new RegExp(`\\[${i18n.language}\\]([\\s\\S]*?)\\[\\/${i18n.language}\\]`, 'i');
    const match = rawText.match(tagReg);
    if (match) return sanitizeHtml(match[1]);
    // 回退到 en / zh
    for (const fb of ['en', 'zh']) {
        const fbReg = new RegExp(`\\[${fb}\\]([\\s\\S]*?)\\[\\/${fb}\\]`, 'i');
        const fbMatch = rawText.match(fbReg);
        if (fbMatch) return sanitizeHtml(fbMatch[1]);
    }
    return sanitizeHtml(rawText);
};

/** 用户端弹窗 / 铃铛展示的时间：按发布时选择的新建时间或最后修改时间 */
export function getAnnouncementDisplayTime(item: {
  created_at?: string;
  updated_at?: string;
  display_time_mode?: string | null;
}): string {
  if (item.display_time_mode === 'updated' && item.updated_at) {
    return item.updated_at;
  }
  return item.created_at || '';
}
