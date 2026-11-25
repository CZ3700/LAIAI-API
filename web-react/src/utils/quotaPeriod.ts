import dayjs from 'dayjs';
import utc from 'dayjs/plugin/utc';
import timezone from 'dayjs/plugin/timezone';
import customParseFormat from 'dayjs/plugin/customParseFormat';
import useSettingsStore from '../store/settings';

dayjs.extend(utc);
dayjs.extend(timezone);
dayjs.extend(customParseFormat);

/** 渠道共享额度周期：与后端一致，使用站点默认时区 */
function siteTimezone(): string {
  return (
    useSettingsStore.getState().settings?.site?.default_timezone?.trim() ||
    'Asia/Shanghai'
  );
}

/** InputNumber 展示：-1 → 无限额 */
export function formatQuotaLimitDisplay(val: number | string | null | undefined) {
  return val === -1 || val === '-1' ? '无限额' : `${val ?? ''}`;
}

/** InputNumber 解析：清空 / 无限文案 → -1，避免误存为 0 */
export function parseQuotaLimitInput(val: string | undefined): number {
  if (val == null || val === '' || val === '无限额' || val === '不限制') return -1;
  const n = parseFloat(val);
  return Number.isFinite(n) ? n : -1;
}

/** 有限额度（≥0）；-1 / null / undefined 视为不限制，不参与大小比较 */
export function isFiniteQuotaLimit(n: unknown): n is number {
  return typeof n === 'number' && Number.isFinite(n) && n >= 0;
}

/**
 * 校验额度层级：日 ≤ 周 ≤ 月 ≤ 总（仅双方均为有限额度时比较）。
 * @returns 错误文案；合法则返回 null
 */
export function validateQuotaHierarchy(values: {
  quota_limit?: number | null;
  daily_quota_limit?: number | null;
  weekly_quota_limit?: number | null;
  monthly_quota_limit?: number | null;
}): string | null {
  const total = values.quota_limit;
  const day = values.daily_quota_limit;
  const week = values.weekly_quota_limit;
  const month = values.monthly_quota_limit;

  for (const [label, v] of [
    ['总额度', total],
    ['日额度', day],
    ['周额度', week],
    ['月额度', month],
  ] as const) {
    if (v != null && Number.isFinite(Number(v)) && Number(v) < -1) {
      return `${label}不能小于 -1`;
    }
  }

  const check = (
    smaller: number | null | undefined,
    larger: number | null | undefined,
    msg: string,
  ) => {
    if (isFiniteQuotaLimit(smaller) && isFiniteQuotaLimit(larger) && smaller > larger) {
      return msg;
    }
    return null;
  };

  return (
    check(day, week, '日额度不能大于周额度') ||
    check(day, month, '日额度不能大于月额度') ||
    check(day, total, '日额度不能大于总额度') ||
    check(week, month, '周额度不能大于月额度') ||
    check(week, total, '周额度不能大于总额度') ||
    check(month, total, '月额度不能大于总额度') ||
    null
  );
}

/** 根据其它字段计算某项额度的动态上限（用于 InputNumber max） */
function getQuotaInputMax(

  field: 'quota_limit' | 'daily_quota_limit' | 'weekly_quota_limit' | 'monthly_quota_limit',
  values: {
    quota_limit?: number | null;
    daily_quota_limit?: number | null;
    weekly_quota_limit?: number | null;
    monthly_quota_limit?: number | null;
  },
): number | undefined {
  const caps: number[] = [];
  if (field === 'daily_quota_limit') {
    if (isFiniteQuotaLimit(values.weekly_quota_limit)) caps.push(values.weekly_quota_limit);
    if (isFiniteQuotaLimit(values.monthly_quota_limit)) caps.push(values.monthly_quota_limit);
    if (isFiniteQuotaLimit(values.quota_limit)) caps.push(values.quota_limit);
  } else if (field === 'weekly_quota_limit') {
    if (isFiniteQuotaLimit(values.monthly_quota_limit)) caps.push(values.monthly_quota_limit);
    if (isFiniteQuotaLimit(values.quota_limit)) caps.push(values.quota_limit);
  } else if (field === 'monthly_quota_limit') {
    if (isFiniteQuotaLimit(values.quota_limit)) caps.push(values.quota_limit);
  }
  if (caps.length === 0) return undefined;
  return Math.min(...caps);
}

/** 日·周·月周期键（与后端 `%Y-%U` 周键一致；周日为一周起点） */
export function getLocalPeriodKeys(tz: string) {
  const now = dayjs().tz(tz);
  const nowDay = now.format('YYYY-MM-DD');
  const nowMonth = now.format('YYYY-MM');
  const year = now.year();
  const janFirst = dayjs.tz(`${year}-01-01`, tz);
  const days = now.startOf('day').diff(janFirst.startOf('day'), 'day');
  const weekNum = Math.floor((days + janFirst.day()) / 7);
  const nowWeek = `${year}-${String(weekNum).padStart(2, '0')}`;
  return { nowDay, nowWeek, nowMonth };
}

/**
 * 按自定义刷新时刻 + 冷却计算额度日键（与后端 `quota_day_key_with_cutover` 一致）。
 * 日键取「最近一次已到达的切点」对应的日历日；冷却跨午夜时会回退多天。
 */
function getQuotaDayKeyWithCutover(
  tz: string,
  hour = 0,
  minute = 0,
  cooldownMinutes = 0,
): string {
  const now = dayjs().tz(tz);
  const h = Math.min(23, Math.max(0, Math.floor(hour)));
  const m = Math.min(59, Math.max(0, Math.floor(minute)));
  const cool = Math.max(0, Math.floor(cooldownMinutes));
  const maxBack = 2 + Math.floor(cool / (24 * 60));

  for (let back = 0; back <= maxBack; back += 1) {
    const day = now.startOf('day').subtract(back, 'day');
    const cutoff = day.hour(h).minute(m).second(0).millisecond(0).add(cool, 'minute');
    if (!now.isBefore(cutoff)) {
      return day.format('YYYY-MM-DD');
    }
  }
  return now.startOf('day').subtract(maxBack, 'day').format('YYYY-MM-DD');
}

function getEffectiveChannelPeriodUsed(
  record: {
    last_reset_day?: string | null;
    last_reset_week?: string | null;
    last_reset_month?: string | null;
    daily_quota_used?: number | null;
    weekly_quota_used?: number | null;
    monthly_quota_used?: number | null;
    daily_reset_hour?: number | null;
    daily_reset_minute?: number | null;
    daily_reset_cooldown_minutes?: number | null;
  },
  tz?: string,
) {
  const zone = tz || siteTimezone();
  const { nowWeek, nowMonth } = getLocalPeriodKeys(zone);
  const nowDay = getQuotaDayKeyWithCutover(
    zone,
    record.daily_reset_hour ?? 0,
    record.daily_reset_minute ?? 0,
    record.daily_reset_cooldown_minutes ?? 0,
  );
  return {
    dailyUsed: record.last_reset_day === nowDay ? (record.daily_quota_used || 0) : 0,
    weeklyUsed: record.last_reset_week === nowWeek ? (record.weekly_quota_used || 0) : 0,
    monthlyUsed: record.last_reset_month === nowMonth ? (record.monthly_quota_used || 0) : 0,
  };
}

/** 列表额度排序：档位 + 指标，扩展时只加指标表项 */
export type QuotaSortPeriod = 'auto' | 'total' | 'month' | 'week' | 'day';
export type QuotaSortMetric = 'rate' | 'used' | 'remain' | 'limit';

export const QUOTA_SORT_METRICS = [
  { key: 'rate', label: '使用率' },
  { key: 'used', label: '已用' },
  { key: 'remain', label: '剩余' },
  { key: 'limit', label: '限额' },
] as const satisfies readonly { key: QuotaSortMetric; label: string }[];

type QuotaSortRecord = Parameters<typeof getEffectiveChannelPeriodUsed>[0] & {
  quota_used?: number | null;
  quota_limit?: number | null;
  daily_quota_limit?: number | null;
  weekly_quota_limit?: number | null;
  monthly_quota_limit?: number | null;
};

const QUOTA_PERIODS = [
  { key: 'total', label: '总' },
  { key: 'month', label: '月' },
  { key: 'week', label: '周' },
  { key: 'day', label: '日' },
] as const;

export const QUOTA_RING_BLUE = {
  total: '#1d4ed8',
  month: '#2563eb',
  week: '#3b82f6',
  day: '#60a5fa',
} as const;

export function quotaRingPercent(used: number, limit: number) {
  if (limit < 0) return 0;
  if (limit === 0) return used > 0 ? 100 : 0;
  return Math.min(100, Math.round((used / limit) * 100));
}

function quotaMetricOf(used: number, limit: number, metric: QuotaSortMetric): number {
  if (metric === 'used') return used;
  if (limit < 0) return metric === 'rate' ? -1 : Number.POSITIVE_INFINITY;
  if (metric === 'limit') return limit;
  if (metric === 'remain') return Math.max(0, limit - used);
  return limit === 0 ? (used > 0 ? Number.POSITIVE_INFINITY : 0) : used / limit;
}

function quotaSlices(r: QuotaSortRecord, tz?: string) {
  const { dailyUsed, weeklyUsed, monthlyUsed } = getEffectiveChannelPeriodUsed(r, tz);
  return {
    total: { used: r.quota_used || 0, limit: r.quota_limit ?? -1 },
    month: { used: monthlyUsed, limit: r.monthly_quota_limit ?? -1 },
    week: { used: weeklyUsed, limit: r.weekly_quota_limit ?? -1 },
    day: { used: dailyUsed, limit: r.daily_quota_limit ?? -1 },
  };
}

export function quotaPeriodItems(r: QuotaSortRecord, tz?: string) {
  const slices = quotaSlices(r, tz);
  return QUOTA_PERIODS.map((p) => ({ ...p, ...slices[p.key] }));
}

export function compareChannelQuota(
  a: QuotaSortRecord,
  b: QuotaSortRecord,
  period: QuotaSortPeriod,
  metric: QuotaSortMetric,
  tz?: string,
): number {
  const valueOf = (r: QuotaSortRecord) => {
    const slices = quotaSlices(r, tz);
    if (period !== 'auto') return quotaMetricOf(slices[period].used, slices[period].limit, metric);
    if (metric === 'used') return slices.total.used;
    const configured = Object.values(slices).filter((s) => s.limit >= 0);
    if (configured.length === 0) {
      return metric === 'rate' ? slices.total.used : Number.POSITIVE_INFINITY;
    }
    const vals = configured.map((s) => quotaMetricOf(s.used, s.limit, metric));
    return metric === 'rate' ? Math.max(...vals) : Math.min(...vals);
  };
  return valueOf(a) - valueOf(b);
}

/**
 * 兑换码是否过期：与后端一致。
 * - 纯日期：站点时区该日结束（次日 00:00）前仍有效
 * - 无时区日期时间：按站点时区墙钟解释
 */
export function isRedemptionExpired(
  expiresAt: string | null | undefined,
  tz?: string,
): boolean {
  if (!expiresAt || !String(expiresAt).trim()) return false;
  const zone = tz || siteTimezone();
  const exp = String(expiresAt).trim();
  const now = dayjs().tz(zone);

  if (/^\d{4}-\d{2}-\d{2}$/.test(exp)) {
    const end = dayjs.tz(exp, 'YYYY-MM-DD', zone).add(1, 'day').startOf('day');
    return !now.isBefore(end);
  }

  if (exp.includes('T') && (exp.includes('+') || exp.endsWith('Z') || /[+-]\d{2}:\d{2}$/.test(exp))) {
    return now.isAfter(dayjs(exp));
  }

  const dt = dayjs.tz(exp, 'YYYY-MM-DD HH:mm:ss', zone);
  if (dt.isValid()) return now.isAfter(dt);

  const dt2 = dayjs.tz(exp, 'YYYY-MM-DDTHH:mm:ss', zone);
  if (dt2.isValid()) return now.isAfter(dt2);

  return false;
}

/**
 * 兑换码是否尚未到开始生效时间。空值表示生成后立即生效。
 * 纯日期按站点时区该日 00:00 起算。
 */
export function isRedemptionNotStarted(
  startsAt: string | null | undefined,
  tz?: string,
): boolean {
  if (!startsAt || !String(startsAt).trim()) return false;
  const zone = tz || siteTimezone();
  const start = String(startsAt).trim();
  const now = dayjs().tz(zone);

  if (/^\d{4}-\d{2}-\d{2}$/.test(start)) {
    const begin = dayjs.tz(start, 'YYYY-MM-DD', zone).startOf('day');
    return now.isBefore(begin);
  }

  if (start.includes('T') && (start.includes('+') || start.endsWith('Z') || /[+-]\d{2}:\d{2}$/.test(start))) {
    return now.isBefore(dayjs(start));
  }

  const dt = dayjs.tz(start, 'YYYY-MM-DD HH:mm:ss', zone);
  if (dt.isValid()) return now.isBefore(dt);

  const dt2 = dayjs.tz(start, 'YYYY-MM-DDTHH:mm:ss', zone);
  if (dt2.isValid()) return now.isBefore(dt2);

  return false;
}
