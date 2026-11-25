import dayjs from 'dayjs';
import { formatWallClock } from '../../utils/timedisplay';
import {
  ALL_WEEKDAYS,
  expandTimeMultiplierIntervals,
  findOverlappingAllDayDays,
  findOverlappingTimeMultipliers,
  isAllDayMultiplier,
  normalizeWeekdays,
  type TimeMultiplierRule,
} from '../../utils/timeMultipliers';

export type DiscountSlotForm = {
  start?: unknown;
  end?: unknown;
  days?: number[];
  all_day?: boolean;
  site_discount?: number;
  global_discount?: number;
};

function clockString(v: unknown): string {
  if (v && typeof v === 'object' && typeof (v as { format?: unknown }).format === 'function') {
    return (v as { format: (pattern: string) => string }).format('HH:mm');
  }
  const matched = /^(\d{1,2}):(\d{2})/.exec(String(v ?? ''));
  if (!matched) return '';
  return `${matched[1].padStart(2, '0')}:${matched[2]}`;
}

function clockMinutes(v: unknown): number | null {
  if (v && typeof v === 'object' && typeof (v as { hour?: unknown }).hour === 'function') {
    const clock = v as { hour: () => number; minute: () => number };
    return clock.hour() * 60 + clock.minute();
  }
  const text = clockString(v);
  const matched = /^(\d{2}):(\d{2})$/.exec(text);
  if (!matched) return null;
  return Number(matched[1]) * 60 + Number(matched[2]);
}

export function unpackDiscountSchedule(raw?: string | null): {
  enabled: boolean;
  slots: DiscountSlotForm[];
  edit_tz: 'operation' | 'admin_display';
  /** 按天规划有效期结束时间（站点运营时区），空串为长期有效 */
  end_at_raw: string;
  limited: {
    enabled: boolean;
    discount?: number;
    site_discount?: number;
    resolution_discounts: Record<string, number>;
    has_end: boolean;
    end_at: dayjs.Dayjs | null;
    end_at_raw: string;
    edit_tz: 'operation' | 'admin_display';
  };
} {
  let enabled = false;
  let slots: DiscountSlotForm[] = [];
  let edit_tz: 'operation' | 'admin_display' = 'operation';
  let scheduleEndRaw = '';
  let limitedEnabled = false;
  let limitedDiscount: number | undefined;
  let limitedSiteDiscount: number | undefined;
  let limitedEditTz: 'operation' | 'admin_display' = 'operation';
  const resolutionDiscounts: Record<string, number> = {};
  let hasEnd = false;
  let endAt: dayjs.Dayjs | null = null;
  let endAtRaw = '';
  try {
    if (raw) {
      const parsed = JSON.parse(raw);
      enabled = !!parsed?.enabled;
      if (Array.isArray(parsed?.slots)) slots = parsed.slots;
      if (parsed?.edit_tz === 'admin_display') edit_tz = 'admin_display';
      if (typeof parsed?.end_at === 'string' && parsed.end_at.trim() && dayjs(parsed.end_at.trim()).isValid()) {
        scheduleEndRaw = parsed.end_at.trim();
      }
      const rawLim = parsed?.limited;
      if (rawLim && typeof rawLim === 'object') {
        limitedEnabled = !!rawLim.enabled;
        if (rawLim.edit_tz === 'admin_display') limitedEditTz = 'admin_display';
        if (rawLim.discount != null && rawLim.discount !== '' && Number.isFinite(Number(rawLim.discount))) {
          limitedDiscount = Number(rawLim.discount);
        }
        if (rawLim.site_discount != null && rawLim.site_discount !== '' && Number.isFinite(Number(rawLim.site_discount)) && Number(rawLim.site_discount) >= 0) {
          limitedSiteDiscount = Number(rawLim.site_discount);
        }
        if (rawLim.resolution_discounts && typeof rawLim.resolution_discounts === 'object') {
          Object.entries(rawLim.resolution_discounts).forEach(([k, v]) => {
            const n = Number(v);
            if (v != null && v !== '' && Number.isFinite(n) && n >= 0) {
              resolutionDiscounts[k.trim().toLowerCase()] = n;
            }
          });
        }
        if (typeof rawLim.end_at === 'string' && rawLim.end_at.trim()) {
          endAtRaw = rawLim.end_at.trim();
          const parsedDt = dayjs(endAtRaw);
          if (parsedDt.isValid()) {
            hasEnd = true;
            endAt = parsedDt;
          }
        }
      }
    }
  } catch {
    enabled = false;
    slots = [];
  }
  if (limitedEnabled) {
    enabled = false;
  }
  return {
    enabled,
    edit_tz,
    end_at_raw: scheduleEndRaw,
    slots: slots
      .map((item) => ({
        all_day: !!item.all_day,
        start: item.start ? dayjs(String(item.start), 'HH:mm') : null,
        end: item.end ? dayjs(String(item.end), 'HH:mm') : null,
        days: normalizeWeekdays(item.days),
        site_discount: Number(item.site_discount ?? 1),
        global_discount: Number(item.global_discount ?? 1),
      }))
      .filter((item) => item.all_day || (item.start && item.end)),
    limited: {
      enabled: limitedEnabled,
      discount: limitedDiscount,
      site_discount: limitedSiteDiscount,
      resolution_discounts: resolutionDiscounts,
      has_end: hasEnd,
      end_at: endAt,
      end_at_raw: endAtRaw,
      edit_tz: limitedEditTz,
    },
  };
}

/** 列表展示用只读解析缓存：同一配置串只解析一次（模型列表每行多次判断不再重复 JSON 解析）。编辑表单回填仍用 unpackDiscountSchedule 拿全新对象 */
const displayCache = new Map<string, ReturnType<typeof unpackDiscountSchedule>>();

export function readDiscountSchedule(raw?: string | null): ReturnType<typeof unpackDiscountSchedule> {
  const key = raw || '';
  let parsed = displayCache.get(key);
  if (!parsed) {
    if (displayCache.size >= 1000) displayCache.clear();
    parsed = unpackDiscountSchedule(raw);
    displayCache.set(key, parsed);
  }
  return parsed;
}

export function discountScheduleOn(raw?: string | null): boolean {
  const parsed = readDiscountSchedule(raw);
  return parsed.enabled && parsed.slots.length > 0;
}

export function limitedDiscountOn(raw?: string | null): boolean {
  const parsed = readDiscountSchedule(raw);
  return parsed.limited.enabled;
}

export function isLimitedDiscountExpired(raw?: string | null, tz = 'Asia/Shanghai'): boolean {
  const parsed = readDiscountSchedule(raw);
  if (!parsed.limited.enabled || !parsed.limited.end_at_raw) return false;
  const nowStr = formatWallClock(tz).slice(0, 19);
  return nowStr >= parsed.limited.end_at_raw;
}

export function isDiscountScheduleExpired(raw?: string | null, tz = 'Asia/Shanghai'): boolean {
  const parsed = readDiscountSchedule(raw);
  if (!parsed.enabled || !parsed.end_at_raw) return false;
  return formatWallClock(tz).slice(0, 19) >= parsed.end_at_raw;
}

const RESOLUTION_SORT_ORDER = ['480p', '720p', '1080p', '2k', '4k'];

export function sortResolutionKeys(keys: string[]): string[] {
  return [...keys].sort((a, b) => {
    const ia = RESOLUTION_SORT_ORDER.indexOf(a.toLowerCase());
    const ib = RESOLUTION_SORT_ORDER.indexOf(b.toLowerCase());
    if (ia !== -1 && ib !== -1) return ia - ib;
    if (ia !== -1) return -1;
    if (ib !== -1) return 1;
    return a.localeCompare(b);
  });
}

export function getSeedance2Resolutions(br: any): string[] {
  const list: string[] = [];
  if (br?.extended_config) {
    try {
      const ext = typeof br.extended_config === 'string'
        ? JSON.parse(br.extended_config)
        : br.extended_config;
      if (ext?.resolution_rates && typeof ext.resolution_rates === 'object') {
        Object.keys(ext.resolution_rates).forEach((k) => {
          const norm = k.trim().toLowerCase();
          if (norm && !list.includes(norm)) list.push(norm);
        });
      }
    } catch {
      // ignore
    }
  }
  if (list.length === 0) {
    return ['480p', '720p', '1080p', '4k'];
  }
  return sortResolutionKeys(list);
}

function datetimeString(v: unknown): string {
  if (v && typeof v === 'object' && typeof (v as { format?: unknown }).format === 'function') {
    return (v as { format: (pattern: string) => string }).format('YYYY-MM-DD HH:mm:ss');
  }
  return String(v ?? '').trim();
}

export function packDiscountSchedule(
  enabled: boolean,
  slots: DiscountSlotForm[] | undefined,
  editTz: 'operation' | 'admin_display' = 'operation',
  limited?: {
    enabled: boolean;
    isSeedance2: boolean;
    discount?: number | null;
    siteDiscount?: number | null;
    resolutionDiscounts?: Record<string, number | null | undefined>;
    hasEnd?: boolean;
    endAt?: unknown;
    editTz?: 'operation' | 'admin_display';
  },
  siteFallback = 1,
  globalFallback = 1,
  /** 按天规划有效期结束时间（已折算为站点运营时区），空串为长期有效 */
  scheduleEndAt = '',
): string {
  const limitedOn = !!limited?.enabled;
  const scheduleOn = limitedOn ? false : !!enabled;
  const cleanRes: Record<string, number> = {};
  if (limited?.isSeedance2 && limited?.resolutionDiscounts && typeof limited.resolutionDiscounts === 'object') {
    Object.entries(limited.resolutionDiscounts).forEach(([k, v]) => {
      if (v != null && (v as unknown) !== '') {
        const n = Number(v);
        if (Number.isFinite(n) && n >= 0) {
          cleanRes[k.trim().toLowerCase()] = n;
        }
      }
    });
  }
  const singleDiscount = !limited?.isSeedance2
    && limited?.discount != null
    && (limited.discount as unknown) !== ''
    && Number.isFinite(Number(limited.discount))
    ? Number(limited.discount)
    : undefined;
  const endAtStr = limited?.hasEnd && limited?.endAt ? datetimeString(limited.endAt) : '';
  const limitedSite = limited?.siteDiscount != null
    && (limited.siteDiscount as unknown) !== ''
    && Number.isFinite(Number(limited.siteDiscount))
    && Number(limited.siteDiscount) >= 0
    ? Number(limited.siteDiscount)
    : undefined;

  return JSON.stringify({
    enabled: scheduleOn,
    edit_tz: editTz === 'admin_display' ? 'admin_display' : 'operation',
    end_at: scheduleEndAt.trim(),
    slots: (slots || []).map((item) => ({
      all_day: !!item.all_day,
      start: item.all_day ? '' : clockString(item.start),
      end: item.all_day ? '' : clockString(item.end),
      days: normalizeWeekdays(item.days),
      site_discount: item.site_discount == null || (item.site_discount as unknown) === '' ? siteFallback : Number(item.site_discount),
      global_discount: item.global_discount == null || (item.global_discount as unknown) === '' ? globalFallback : Number(item.global_discount),
    })),
    limited: {
      enabled: limitedOn,
      discount: singleDiscount,
      site_discount: limitedSite,
      resolution_discounts: cleanRes,
      end_at: endAtStr,
      edit_tz: limited?.editTz === 'admin_display' ? 'admin_display' : 'operation',
    },
  });
}

export function validateLimitedDiscountForm(
  limitedOn: boolean,
  globalOn: boolean,
  isSeedance2: boolean,
  discount: number | null | undefined,
  resolutionDiscounts: Record<string, number | null | undefined> | undefined,
  hasEnd: boolean,
  endAt: unknown,
): string | null {
  if (!limitedOn) return null;
  if (!globalOn) return '开启全站折扣后才能开启限时折扣';
  if (isSeedance2) {
    const entries = Object.entries(resolutionDiscounts || {}).filter(
      ([, v]) => v != null && (v as unknown) !== '',
    );
    if (entries.length === 0) {
      return '已开启限时折扣，请至少为一个分辨率设置限时折扣倍率';
    }
    for (const [res, val] of entries) {
      const n = Number(val);
      if (!Number.isFinite(n) || n < 0) {
        return `分辨率 ${res} 的限时折扣必须大于等于 0（0 为免费）`;
      }
    }
  } else {
    if (discount == null || (discount as unknown) === '') {
      return '已开启限时折扣，请填写限时折扣倍率（0 为免费）';
    }
    const n = Number(discount);
    if (!Number.isFinite(n) || n < 0) {
      return '限时折扣倍率必须大于等于 0（0 为免费）';
    }
  }
  if (hasEnd) {
    const str = datetimeString(endAt);
    if (!str || !dayjs(str).isValid()) {
      return '请选择有效的限时折扣结束时间，或切换为长期有效';
    }
  }
  return null;
}

/** 限时折扣中免费（0）或低于折扣限价的倍率，返回提示文案；用于保存前二次确认 */
export function limitedDiscountRiskItems(
  isSeedance2: boolean,
  discount: number | null | undefined,
  resolutionDiscounts: Record<string, number | null | undefined> | undefined,
  siteOn: boolean,
  siteDiscount: number,
): string[] {
  const describe = (label: string, raw: unknown): string | null => {
    if (raw == null || raw === '') return null;
    const n = Number(raw);
    if (!Number.isFinite(n) || n < 0) return null;
    if (n === 0) return `${label}：免费`;
    if (siteOn && Number.isFinite(siteDiscount) && n < siteDiscount - 1e-6) {
      return `${label}：${n}（低于折扣限价 ${siteDiscount}）`;
    }
    return null;
  };
  if (isSeedance2) {
    return sortResolutionKeys(Object.keys(resolutionDiscounts || {}))
      .map((k) => describe(k, resolutionDiscounts?.[k]))
      .filter((v): v is string => !!v);
  }
  const single = describe('限时折扣', discount);
  return single ? [single] : [];
}

/** 生效中的倍率大于 1（高于计费规则原价向用户收费）的项，返回提示文案；用于保存前二次确认，防止 0.1 误填成 10 */
export function discountOverOneItems(cfg: {
  siteOn: boolean;
  globalOn: boolean;
  site: unknown;
  global: unknown;
  limitedOn: boolean;
  isSeedance2: boolean;
  limitedRate: unknown;
  limitedSite: unknown;
  resolutionDiscounts?: Record<string, unknown>;
  scheduleOn: boolean;
  slots?: DiscountSlotForm[];
}): string[] {
  const out: string[] = [];
  const check = (label: string, raw: unknown) => {
    if (raw == null || raw === '') return;
    const n = Number(raw);
    if (Number.isFinite(n) && n > 1 + 1e-9) out.push(`${label}：${n}`);
  };
  if (cfg.siteOn) check('折扣限价倍率', cfg.site);
  if (!cfg.globalOn) return out;
  check('全站折扣倍率', cfg.global);
  if (cfg.limitedOn) {
    if (cfg.siteOn) check('活动限价', cfg.limitedSite);
    if (cfg.isSeedance2) {
      sortResolutionKeys(Object.keys(cfg.resolutionDiscounts || {}))
        .forEach((k) => check(`${k} 活动折扣`, cfg.resolutionDiscounts?.[k]));
    } else {
      check('活动折扣', cfg.limitedRate);
    }
  } else if (cfg.scheduleOn) {
    (cfg.slots || []).forEach((slot, i) => {
      if (cfg.siteOn) check(`第 ${i + 1} 条时段限价`, slot.site_discount);
      check(`第 ${i + 1} 条时段全站折扣`, slot.global_discount);
    });
  }
  return out;
}

export function validateDiscountScheduleForm(
  enabled: boolean,
  slots: DiscountSlotForm[] | undefined,
  siteOn: boolean,
  globalOn: boolean,
  siteFallback = 1,
  globalFallback = 1,
): string | null {
  if (!enabled) return null;
  if (!globalOn) return '开启全站折扣后才能规划时段折扣';
  const list = slots || [];
  if (!list.length) return '已开启时段折扣，请至少添加一条时段或全天规则';
  const intervals = [];
  for (let i = 0; i < list.length; i += 1) {
    const item = list[i];
    const site = item.site_discount == null || (item.site_discount as unknown) === '' ? siteFallback : Number(item.site_discount);
    const global = item.global_discount == null || (item.global_discount as unknown) === '' ? globalFallback : Number(item.global_discount);
    if (siteOn && !(Number.isFinite(site) && site >= 0)) return `第 ${i + 1} 条规则的折扣限价必须大于等于 0（0 为免费）`;
    if (globalOn && !(Number.isFinite(global) && global >= 0)) return `第 ${i + 1} 条规则的全站折扣必须大于等于 0（0 为免费）`;
    if (siteOn && globalOn && global < site) {
      const shown = (n: number) => (n === 0 ? '免费' : String(n));
      return `第 ${i + 1} 条规则的全站折扣 ${shown(global)} 低于折扣限价 ${shown(site)}`;
    }
    if (isAllDayMultiplier(item)) continue;
    const startMin = clockMinutes(item.start);
    const endMin = clockMinutes(item.end);
    if (startMin == null || endMin == null) return '请完整填写所有时间段的起止时间';
    if (startMin === endMin) return `第 ${i + 1} 个时间段的起止时间不能相同`;
    intervals.push(...expandTimeMultiplierIntervals(startMin, endMin, normalizeWeekdays(item.days), i));
  }
  const overlap = findOverlappingTimeMultipliers(intervals);
  if (overlap) {
    const [left, right] = overlap;
    return `配置的时间段存在重叠（第 ${left.index + 1} 组与第 ${right.index + 1} 组）`;
  }
  const allDayOverlap = findOverlappingAllDayDays(list);
  if (allDayOverlap) {
    return `全天规则重复（第 ${allDayOverlap.a + 1} 组与第 ${allDayOverlap.b + 1} 组）`;
  }
  return null;
}

export function discountChartItems(
  slots: DiscountSlotForm[] | undefined,
  pick: 'site_discount' | 'global_discount',
  fallback: number,
): TimeMultiplierRule[] {
  const items: TimeMultiplierRule[] = (slots || []).map((item) => {
    const raw = item[pick];
    const num = raw == null || (raw as unknown) === '' ? fallback : Number(raw);
    return {
      start: item.start,
      end: item.end,
      days: normalizeWeekdays(item.days),
      all_day: !!item.all_day,
      multiplier: Number.isFinite(num) && num >= 0 ? num : fallback,
    };
  });
  items.push({ all_day: true, days: [...ALL_WEEKDAYS], multiplier: fallback });
  return items;
}
