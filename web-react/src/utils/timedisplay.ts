/**
 * timedisplay：展示与筛选墙钟。
 * 后端 timesystem 固定 UTC；API 时间按 UTC 解释后再按 timedisplay 渲染。
 * 站点运营时区（default_timezone）只用于业务自然日，不走本文件的管理端展示解析。
 */
import useAuthStore from '../store/auth';
import useSettingsStore from '../store/settings';

/** 站点运营时区：计费时段、额度刷新、仪表盘/财务自然日。 */
export function resolveSiteTimedisplay(): string {
  return useSettingsStore.getState().settings?.site?.default_timezone?.trim() || 'Asia/Shanghai';
}

/** 管理后台展示时区；未配置或非法时回退站点运营时区。 */
function resolveAdminTimedisplay(): string {
  const adminTz = useSettingsStore.getState().settings?.site?.admin_display_timezone?.trim();
  if (adminTz && isValidIanaTimezone(adminTz)) return adminTz;
  return resolveSiteTimedisplay();
}

/** 浏览器 IANA 时区；读不到则空串（注册时后端再回退 UTC）。 */
export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone?.trim() || '';
  } catch {
    return '';
  }
}

function isValidIanaTimezone(tz: string): boolean {
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: tz }).format();
    return true;
  } catch {
    return false;
  }
}

/** 管理后台：展示时区。用户端：个人时区合法则用之，否则 UTC。 */
export function resolveTimedisplay(): string {
  if (useAuthStore.getState().user?.role === 'admin') {
    return resolveAdminTimedisplay();
  }
  const userTz = useAuthStore.getState().user?.timezone?.trim();
  if (userTz && isValidIanaTimezone(userTz)) return userTz;
  return 'UTC';
}

/** 是否在时间字符串后追加 (UTC+8) 一类后缀 */
export function shouldShowTimezoneSuffix(): boolean {
  return useSettingsStore.getState().settings?.site?.show_timezone !== false;
}

function tzLabel(name?: string): string {
  let n = name || 'UTC';
  if (n.startsWith('GMT')) n = n.replace('GMT', 'UTC');
  return n === 'UTC' ? 'UTC+0' : n;
}

/** 按指定 IANA 墙钟格式化，不走 timezoneInterceptor。 */
export function formatWallClock(timeZone: string, at: Date = new Date()): string {
  const tz = timeZone.trim() || 'UTC';
  try {
    const p = formatParts(at, tz);
    const hour = p.hour === '24' ? '00' : p.hour;
    return `${p.year}-${p.month}-${p.day} ${hour}:${p.minute}:${p.second} (${tzLabel(p.timeZoneName)})`;
  } catch {
    return formatWallClock('UTC', at);
  }
}

function formatParts(date: Date, timeZone: string) {
  const fmt = new Intl.DateTimeFormat('en-US', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
    timeZone,
    timeZoneName: 'shortOffset',
  });
  return Object.fromEntries(fmt.formatToParts(date).map((p) => [p.type, p.value]));
}

/**
 * 站点时区偏移后缀，如 ` (UTC+8)`。
 * 遵循基础设置「显示时区后缀」；关闭时返回空串。
 */
export function timedisplayOffsetSuffix(timeZone?: string): string {
  if (!shouldShowTimezoneSuffix()) return '';
  const tz = (timeZone || resolveTimedisplay()).trim() || 'Asia/Shanghai';
  try {
    return ` (${tzLabel(formatParts(new Date(), tz).timeZoneName)})`;
  } catch {
    return ' (UTC+0)';
  }
}

/**
 * 将后端返回的 UTC 朴素时间 / ISO / RFC3339 字符串解析为 Date。
 * 无偏移的 `YYYY-MM-DD HH:mm:ss` / `YYYY-MM-DDTHH:mm:ss` 按 UTC 解释（与 timesystem 对齐）。
 */
export function parseApiTimeAsUtc(raw: string | number | Date | null | undefined): Date | null {
  if (raw == null || raw === '') return null;
  if (raw instanceof Date) return isNaN(raw.getTime()) ? null : raw;
  if (typeof raw === 'number') {
    const d = new Date(raw);
    return isNaN(d.getTime()) ? null : d;
  }
  const s = String(raw).trim();
  if (!s) return null;
  // 已带偏移或 Z
  if (/[zZ]|[+-]\d{2}:?\d{2}$/.test(s)) {
    const d = new Date(s);
    return isNaN(d.getTime()) ? null : d;
  }
  // 无偏移 ISO / 空格分隔 → 视为 UTC
  const m = s.match(/^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})/);
  if (m) {
    const d = new Date(`${m[1]}T${m[2]}Z`);
    return isNaN(d.getTime()) ? null : d;
  }
  const d = new Date(s);
  return isNaN(d.getTime()) ? null : d;
}

/**
 * 将 API 时间格式化为 timedisplay 墙钟（不依赖浏览器本地时区）。
 * 支持常见 dayjs 模板片段：YYYY MM DD HH mm ss，以及 YYYY年M月D日。
 */
export function formatApiDateTime(
  raw: string | number | Date | null | undefined,
  template = 'YYYY-MM-DD HH:mm:ss',
): string {
  const d = parseApiTimeAsUtc(raw);
  if (!d) return '-';

  const tz = resolveTimedisplay();
  let p: Record<string, string>;
  try {
    p = formatParts(d, tz);
  } catch {
    p = formatParts(d, 'UTC');
  }

  const hour = p.hour === '24' ? '00' : p.hour;
  const monthNum = String(parseInt(p.month || '0', 10));
  const dayNum = String(parseInt(p.day || '0', 10));

  // 先替换双字符 token，再处理「年M月D日」无前导零
  let body = template
    .replace(/YYYY/g, p.year || '')
    .replace(/MM/g, p.month || '')
    .replace(/DD/g, p.day || '')
    .replace(/HH/g, hour || '')
    .replace(/mm/g, p.minute || '')
    .replace(/ss/g, p.second || '')
    .replace(/年M月/g, `年${monthNum}月`)
    .replace(/月D日/g, `月${dayNum}日`);

  const showSuffix = shouldShowTimezoneSuffix() && template.includes('HH');
  if (!showSuffix) return body;
  return `${body} (${tzLabel(p.timeZoneName)})`;
}

/** 仅日期 */
function formatApiDate(raw: string | number | Date | null | undefined): string {
  return formatApiDateTime(raw, 'YYYY-MM-DD');
}

/** 任务列表/日志按 timedisplay 自然日过滤时，附带给后端的请求头 */
function timedisplayHeaders(): Record<string, string> {
  return { 'X-Timezone': resolveTimedisplay() };
}
