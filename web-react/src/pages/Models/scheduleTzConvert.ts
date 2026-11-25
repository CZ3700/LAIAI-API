import dayjs from 'dayjs';
import { normalizeWeekdays } from '../../utils/timeMultipliers';

export type ScheduleTzMode = 'operation' | 'admin_display';

type Clock = { h: number; m: number };

export function scheduleInputTzName(
  mode: ScheduleTzMode | string | undefined,
  operationTz: string,
  adminTz: string,
): string {
  const operation = operationTz.trim() || 'Asia/Shanghai';
  if (mode === 'admin_display') return adminTz.trim() || operation;
  return operation;
}

function weekdayMon1(ms: number, tz: string): number {
  const name = new Intl.DateTimeFormat('en-US', { weekday: 'short', timeZone: tz }).format(new Date(ms));
  const map: Record<string, number> = { Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6, Sun: 7 };
  return map[name] || 1;
}

function partsInZone(ms: number, tz: string) {
  const fmt = new Intl.DateTimeFormat('en-US', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
    timeZone: tz,
  });
  const p = Object.fromEntries(fmt.formatToParts(new Date(ms)).map((x) => [x.type, x.value]));
  return {
    y: Number(p.year),
    mo: Number(p.month),
    d: Number(p.day),
    h: p.hour === '24' ? 0 : Number(p.hour),
    m: Number(p.minute),
    weekday: weekdayMon1(ms, tz),
  };
}

function wallToUtcMs(tz: string, y: number, mo: number, d: number, h: number, m: number): number {
  let utc = Date.UTC(y, mo - 1, d, h, m, 0);
  for (let i = 0; i < 4; i += 1) {
    const got = partsInZone(utc, tz);
    const delta =
      Date.UTC(y, mo - 1, d, h, m, 0) - Date.UTC(got.y, got.mo - 1, got.d, got.h, got.m, 0);
    if (delta === 0) break;
    utc += delta;
  }
  return utc;
}

function addDays(y: number, mo: number, d: number, days: number) {
  const dt = new Date(Date.UTC(y, mo - 1, d + days));
  return { y: dt.getUTCFullYear(), mo: dt.getUTCMonth() + 1, d: dt.getUTCDate() };
}

/** 墙钟日期时间 `YYYY-MM-DD HH:mm:ss` 在两个时区间折算；无法解析或同时区原样返回 */
export function convertWallDateTime(raw: string, fromTz: string, toTz: string): string {
  const from = fromTz.trim();
  const to = toTz.trim();
  const matched = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})(?::(\d{2}))?/.exec(raw.trim());
  if (!matched || !from || !to || from === to) return raw;
  const [, y, mo, d, h, mi, s = '00'] = matched;
  try {
    const p = partsInZone(wallToUtcMs(from, Number(y), Number(mo), Number(d), Number(h), Number(mi)), to);
    const pad = (n: number) => String(n).padStart(2, '0');
    return `${p.y}-${pad(p.mo)}-${pad(p.d)} ${pad(p.h)}:${pad(p.m)}:${s}`;
  } catch {
    return raw;
  }
}

function dateOfWeekday(tz: string, weekday: number, now: Date, dayOffset = 0) {
  const cur = partsInZone(now.getTime(), tz);
  return addDays(cur.y, cur.mo, cur.d, weekday - cur.weekday + dayOffset);
}

function convertWall(fromTz: string, toTz: string, weekday: number, clock: Clock, now: Date, dayOffset = 0) {
  const date = dateOfWeekday(fromTz, weekday, now, dayOffset);
  const utc = wallToUtcMs(fromTz, date.y, date.mo, date.d, clock.h, clock.m);
  const p = partsInZone(utc, toTz);
  return { weekday: p.weekday, h: p.h, m: p.m };
}

function readClock(v: unknown): Clock | null {
  if (v && typeof v === 'object' && typeof (v as { hour?: unknown }).hour === 'function') {
    const clock = v as { hour: () => number; minute: () => number };
    return { h: clock.hour(), m: clock.minute() };
  }
  const matched = /^(\d{1,2}):(\d{2})/.exec(String(v ?? ''));
  if (!matched) return null;
  return { h: Number(matched[1]), m: Number(matched[2]) };
}

function writeClock(clock: Clock) {
  return dayjs().hour(clock.h).minute(clock.m).second(0).millisecond(0);
}

function clockKey(clock: Clock) {
  return `${String(clock.h).padStart(2, '0')}:${String(clock.m).padStart(2, '0')}`;
}

export type ConvertibleSlot = {
  all_day?: boolean;
  start?: unknown;
  end?: unknown;
  days?: number[];
  [key: string]: unknown;
};

export function convertScheduleSlots<T extends ConvertibleSlot>(
  slots: T[] | undefined,
  fromTz: string,
  toTz: string,
  now: Date = new Date(),
): T[] {
  const from = fromTz.trim();
  const to = toTz.trim();
  if (!slots?.length || !from || !to || from === to) return slots || [];

  const grouped = new Map<string, T>();
  const append = (item: T, start: Clock | null, end: Clock | null, day: number, allDay: boolean) => {
    if (!allDay && start && end && start.h === end.h && start.m === end.m) {
      return;
    }
    const key = `${allDay ? 'all' : `${clockKey(start!)}-${clockKey(end!)}`}|${JSON.stringify({
      multiplier: item.multiplier,
      site_discount: item.site_discount,
      global_discount: item.global_discount,
    })}`;
    const prev = grouped.get(key);
    if (prev) {
      prev.days = normalizeWeekdays([...(prev.days || []), day]);
      return;
    }
    grouped.set(key, {
      ...item,
      all_day: allDay,
      start: allDay ? undefined : writeClock(start!),
      end: allDay ? undefined : writeClock(end!),
      days: [day],
    });
  };

  for (const item of slots) {
    const days = normalizeWeekdays(item.days);
    if (item.all_day) {
      for (const day of days) {
        append(item, null, null, day, true);
      }
      continue;
    }
    const startClock = readClock(item.start);
    const endClock = readClock(item.end);
    if (!startClock || !endClock) {
      grouped.set(`keep-${grouped.size}`, { ...item, days });
      continue;
    }
    const startMin = startClock.h * 60 + startClock.m;
    const endMin = endClock.h * 60 + endClock.m;
    if (startMin === endMin) {
      continue;
    }
    for (const day of days) {
      const start = convertWall(from, to, day, startClock, now, 0);
      const end = convertWall(from, to, day, endClock, now, startMin < endMin ? 0 : 1);
      append(item, start, end, start.weekday, false);
    }
  }

  return [...grouped.values()];
}
