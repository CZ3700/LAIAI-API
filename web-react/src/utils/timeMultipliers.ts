export const ALL_WEEKDAYS = [1, 2, 3, 4, 5, 6, 7] as const;

const WEEKDAY_SET = new Set<number>(ALL_WEEKDAYS);

export function normalizeWeekdays(days?: number[] | null): number[] {
  if (!Array.isArray(days) || days.length === 0) {
    return [...ALL_WEEKDAYS];
  }
  return [...new Set(days.filter((d) => WEEKDAY_SET.has(Number(d))).map(Number))].sort((a, b) => a - b);
}

export function isAllWeekdays(days?: number[] | null): boolean {
  return normalizeWeekdays(days).length === 7;
}

/** labels 长度为 7，下标 0=周一 */
export function formatWeekdayRange(days: number[] | undefined, labels: string[], allLabel = ''): string {
  const n = normalizeWeekdays(days);
  if (n.length === 7) return allLabel;
  const ranges: string[] = [];
  let i = 0;
  while (i < n.length) {
    let j = i;
    while (j + 1 < n.length && n[j + 1] === n[j] + 1) j += 1;
    const left = labels[n[i] - 1] || String(n[i]);
    const right = labels[n[j] - 1] || String(n[j]);
    ranges.push(j > i ? `${left}–${right}` : left);
    i = j + 1;
  }
  return ranges.join('、');
}

function nextWeekday(day: number): number {
  return day === 7 ? 1 : day + 1;
}

export type TimeOverlapInterval = {
  start: number;
  end: number;
  day: number;
  index: number;
};

/** 跨天窗口拆成当日后半夜 + 次日凌晨，次日片段记到起始日的下一天 */
export function expandTimeMultiplierIntervals(
  startMin: number,
  endMin: number,
  days: number[],
  index: number,
): TimeOverlapInterval[] {
  const out: TimeOverlapInterval[] = [];
  const wd = days.length ? days : [...ALL_WEEKDAYS];
  if (startMin > endMin) {
    for (const d of wd) {
      out.push({ start: startMin, end: 1440, day: d, index });
      out.push({ start: 0, end: endMin, day: nextWeekday(d), index });
    }
  } else {
    for (const d of wd) {
      out.push({ start: startMin, end: endMin, day: d, index });
    }
  }
  return out;
}

export function isAllDayMultiplier(item: { all_day?: boolean } | null | undefined): boolean {
  return !!item?.all_day;
}

/** 时段倍率为 0：该时段按默认价乘 0，即不收费 */
export function isFreeTimeMultiplier(rate: number): boolean {
  return Number.isFinite(rate) && Math.abs(rate) <= 1e-9;
}

/** 两条全天规则覆盖同一星期则冲突；全天与时段允许同日并存（时段优先） */
export function findOverlappingAllDayDays(
  items: { all_day?: boolean; days?: number[] }[],
): { a: number; b: number; day: number } | null {
  const allDay = items
    .map((item, index) => ({ index, days: isAllDayMultiplier(item) ? normalizeWeekdays(item.days) : [] }))
    .filter((row) => row.days.length > 0);
  for (let i = 0; i < allDay.length; i++) {
    for (let j = i + 1; j < allDay.length; j++) {
      const day = allDay[i].days.find((d) => allDay[j].days.includes(d));
      if (day != null) {
        return { a: allDay[i].index, b: allDay[j].index, day };
      }
    }
  }
  return null;
}

export function findOverlappingTimeMultipliers(
  intervals: TimeOverlapInterval[],
): TimeOverlapInterval[] | null {
  for (let a = 0; a < intervals.length; a++) {
    for (let b = a + 1; b < intervals.length; b++) {
      const intA = intervals[a];
      const intB = intervals[b];
      if (intA.index === intB.index || intA.day !== intB.day) continue;
      if (Math.max(intA.start, intB.start) < Math.min(intA.end, intB.end)) {
        return [intA, intB];
      }
    }
  }
  return null;
}

export type TimeMultiplierRule = {
  start?: unknown;
  end?: unknown;
  multiplier?: number;
  days?: number[];
  all_day?: boolean;
};

type DayMultiplierSegment = {
  startMin: number;
  endMin: number;
  rate: number;
};

function toClockMinutes(v: unknown): number | null {
  if (v == null || v === '') return null;
  if (typeof v === 'object' && typeof (v as { hour?: unknown }).hour === 'function') {
    const d = v as { hour: () => number; minute: () => number };
    return d.hour() * 60 + d.minute();
  }
  const m = /^(\d{1,2}):(\d{2})/.exec(String(v));
  if (!m) return null;
  return Number(m[1]) * 60 + Number(m[2]);
}

function weekdayApplies(days: number[] | undefined, weekday: number): boolean {
  return normalizeWeekdays(days).includes(weekday);
}

function prevWeekday(weekday: number): number {
  return weekday === 1 ? 7 : weekday - 1;
}

function intervalMatches(item: TimeMultiplierRule, minutes: number, weekday: number): boolean {
  if (isAllDayMultiplier(item)) return false;
  const start = toClockMinutes(item.start);
  const end = toClockMinutes(item.end);
  if (start == null || end == null || start === end) return false;
  if (start > end) {
    if (minutes >= start) return weekdayApplies(item.days, weekday);
    if (minutes < end) return weekdayApplies(item.days, prevWeekday(weekday));
    return false;
  }
  return minutes >= start && minutes < end && weekdayApplies(item.days, weekday);
}

function parseInvertRemainder(remainder?: number | null): number | undefined {
  if (remainder == null || (remainder as unknown) === '') return undefined;
  const n = Number(remainder);
  return Number.isFinite(n) && n >= 0 ? n : undefined;
}

export function resolveTimeMultiplierAt(
  items: TimeMultiplierRule[] | undefined,
  invert: boolean,
  weekday: number,
  minutes: number,
  remainder?: number | null,
): number {
  const list = items || [];
  if (list.length === 0) return 1;
  const rem = parseInvertRemainder(remainder);
  const matched = list.find((item) => intervalMatches(item, minutes, weekday));
  const hasAllDay = list.some((item) => isAllDayMultiplier(item) && weekdayApplies(item.days, weekday));
  if (matched) {
    if (invert && rem == null && !hasAllDay) return 1;
    const n = Number(matched.multiplier);
    return Number.isFinite(n) && n >= 0 ? n : 1;
  }
  const allDay = list.find((item) => isAllDayMultiplier(item) && weekdayApplies(item.days, weekday));
  if (allDay) {
    const n = Number(allDay.multiplier);
    return Number.isFinite(n) && n >= 0 ? n : 1;
  }
  if (rem != null) return rem;
  if (invert) {
    const first = list.find((item) => !isAllDayMultiplier(item) && weekdayApplies(item.days, weekday));
    if (!first) return 1;
    const m = Number(first.multiplier);
    return Number.isFinite(m) && m > 0 ? 1 / m : 1;
  }
  return 1;
}

export function hasExplicitRemainder(ext: { invert_remainder_multiplier?: unknown } | null | undefined): boolean {
  if (ext == null || ext.invert_remainder_multiplier == null || ext.invert_remainder_multiplier === '') {
    return false;
  }
  const n = Number(ext.invert_remainder_multiplier);
  return Number.isFinite(n) && n >= 0;
}

export function invertRemainderFromExt(ext: any): number {
  if (hasExplicitRemainder(ext)) return Number(ext.invert_remainder_multiplier);
  if (ext?.invert_time_multipliers) {
    const first = (Array.isArray(ext?.time_multipliers) ? ext.time_multipliers : [])
      .find((item: any) => !item.all_day && Number(item.multiplier) > 0);
    if (first) return 1 / Number(first.multiplier);
  }
  return 1;
}

/**
 * 旧「反向」未写 invert_remainder_multiplier：窗口实际按 1.00 计、其余按 1/窗口。
 * 打开编辑页时改写成新格式，避免再保存把峰段从 1.00 变成填写值。
 * 与同日全天规则重叠的星期仍保留填写倍率（后端当天有全天时不把窗口改成 1.00）。
 */
export function migrateLegacyInvertItems(items: TimeMultiplierRule[]): TimeMultiplierRule[] {
  const allDayDays = new Set<number>();
  items.forEach((item) => {
    if (isAllDayMultiplier(item)) {
      normalizeWeekdays(item.days).forEach((d) => allDayDays.add(d));
    }
  });
  const next: TimeMultiplierRule[] = [];
  items.forEach((item) => {
    if (isAllDayMultiplier(item)) {
      next.push(item);
      return;
    }
    const days = normalizeWeekdays(item.days);
    const keepDays = days.filter((d) => allDayDays.has(d));
    const flipDays = days.filter((d) => !allDayDays.has(d));
    if (flipDays.length > 0) {
      next.push({ ...item, days: flipDays, multiplier: 1 });
    }
    if (keepDays.length > 0) {
      next.push({ ...item, days: keepDays });
    }
  });
  return next;
}

function collectBoundaries(items: TimeMultiplierRule[], weekday: number): number[] {
  const marks = new Set<number>([0, 1440]);
  const prev = prevWeekday(weekday);
  for (const item of items) {
    if (isAllDayMultiplier(item)) continue;
    const start = toClockMinutes(item.start);
    const end = toClockMinutes(item.end);
    if (start == null || end == null || start === end) continue;
    if (start > end) {
      if (weekdayApplies(item.days, weekday)) marks.add(start);
      if (weekdayApplies(item.days, prev)) marks.add(end);
    } else if (weekdayApplies(item.days, weekday)) {
      marks.add(start);
      marks.add(end);
    }
  }
  return [...marks].filter((n) => n >= 0 && n <= 1440).sort((a, b) => a - b);
}

export function buildDayMultiplierSegments(
  items: TimeMultiplierRule[] | undefined,
  invert: boolean,
  weekday: number,
  remainder?: number | null,
): DayMultiplierSegment[] {
  const list = items || [];
  const bounds = collectBoundaries(list, weekday);
  const segs: DayMultiplierSegment[] = [];
  for (let i = 0; i < bounds.length - 1; i++) {
    const startMin = bounds[i];
    const endMin = bounds[i + 1];
    if (endMin <= startMin) continue;
    const rate = resolveTimeMultiplierAt(list, invert, weekday, startMin, remainder);
    const last = segs[segs.length - 1];
    if (last && Math.abs(last.rate - rate) < 1e-9) {
      last.endMin = endMin;
    } else {
      segs.push({ startMin, endMin, rate });
    }
  }
  if (segs.length === 0) {
    return [{ startMin: 0, endMin: 1440, rate: 1 }];
  }
  return segs;
}

export function formatClockMinutes(min: number): string {
  if (min >= 1440) return '24:00';
  const h = Math.floor(min / 60);
  const m = min % 60;
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`;
}

/** 折扣与倍率格式化：默认保留 2 位小数，第 3 位非 0 时保留 3 位（如 0.80、0.655） */
export function formatRate(v: number): string {
  const n = Number(v);
  if (!Number.isFinite(n)) return '1.00';
  const s = n.toFixed(3);
  return s.endsWith('0') ? s.slice(0, -1) : s;
}
