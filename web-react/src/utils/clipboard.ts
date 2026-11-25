import { message } from 'antd';

/**
 * 极简通用的剪贴板复制工具函数
 * 兼容 HTTPS、HTTP、局域网/公网 IP 直接访问以及各种浏览器环境
 */
export async function copyToClipboard(text: string): Promise<boolean> {
  if (!text && text !== '') return false;

  // 1. 优先使用安全上下文下的现代 Clipboard API
  if (typeof navigator !== 'undefined' && navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // 浏览器权限受限或失败时继续降级
    }
  }

  // 2. IP / 无 SSL HTTP / 降级方案 (textarea + execCommand)
  let ta: HTMLTextAreaElement | null = null;
  try {
    ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.top = '0';
    ta.style.left = '0';
    ta.style.width = '1px';
    ta.style.height = '1px';
    ta.style.padding = '0';
    ta.style.border = 'none';
    ta.style.outline = 'none';
    ta.style.boxShadow = 'none';
    ta.style.background = 'transparent';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    return document.execCommand('copy');
  } catch (err) {
    console.warn('copyToClipboard failed:', err);
    return false;
  } finally {
    if (ta && ta.parentNode) {
      ta.parentNode.removeChild(ta);
    }
  }
}

/**
 * 便捷复制并触发 Ant Design 提示消息
 */
export async function copyWithFeedback(
  text: string,
  successMsg: string = '已复制到剪贴板',
  errorMsg: string = '复制失败'
): Promise<boolean> {
  const ok = await copyToClipboard(text);
  if (ok) {
    if (successMsg) message.success(successMsg);
  } else {
    if (errorMsg) message.error(errorMsg);
  }
  return ok;
}
