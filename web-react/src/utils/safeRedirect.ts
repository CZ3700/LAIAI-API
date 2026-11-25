/**
 * 仅允许站内相对路径跳转，防止 open redirect。
 * 拒绝：绝对 URL、协议相对 `//evil`、反斜杠、含 control 字符等。
 */
export function sanitizeRedirectPath(
  raw: string | null | undefined,
  fallback = '/dashboard',
): string {
  if (!raw) return fallback;
  const path = raw.trim();
  if (!path.startsWith('/')) return fallback;
  if (path.startsWith('//')) return fallback;
  if (path.includes('\\')) return fallback;
  if (path.includes('://')) return fallback;
  if (/[\u0000-\u001F\u007F]/.test(path)) return fallback;
  // 拒绝把浏览器导向 javascript: 等（经编码的变体）
  const decoded = (() => {
    try {
      return decodeURIComponent(path);
    } catch {
      return path;
    }
  })();
  if (/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(decoded)) return fallback;
  if (decoded.startsWith('//') || decoded.includes('\\')) return fallback;
  return path;
}

/** 从当前 URL 去掉敏感查询参数，避免 JWT/兑换码留在历史记录 */
export function stripAuthParamsFromUrl(keys: string[] = ['token', 'code', 'handoff', 'impersonate', 'type', 'wechat_ticket']): void {
  const url = new URL(window.location.href);
  let changed = false;
  for (const key of keys) {
    if (url.searchParams.has(key)) {
      url.searchParams.delete(key);
      changed = true;
    }
  }
  if (changed) {
    const next = `${url.pathname}${url.search}${url.hash}`;
    window.history.replaceState({}, '', next);
  }
}
