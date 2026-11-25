import dayjs, { type Dayjs } from 'dayjs';
import utc from 'dayjs/plugin/utc';
import timezone from 'dayjs/plugin/timezone';
import useAuthStore from '../store/auth';
import { resolveSiteTimedisplay, resolveTimedisplay } from './timedisplay';

dayjs.extend(utc);
dayjs.extend(timezone);

type DayjsRange = [Dayjs, Dayjs] | [string, string] | null | undefined;

/** 日志/任务列表 RangePicker 展示格式 */
export const LOG_DATETIME_FORMAT = 'YYYY-MM-DD HH:mm:ss';

function asDayjs(v: Dayjs | string): Dayjs | null {
  const d = typeof v === 'string' ? dayjs(v) : v;
  return d?.isValid?.() ? d : null;
}

function rangeEnds(range: DayjsRange): [Dayjs, Dayjs] | null {
  const start = range?.[0] != null ? asDayjs(range[0]) : null;
  const end = range?.[1] != null ? asDayjs(range[1]) : null;
  return start && end ? [start, end] : null;
}

function pad2(n: number): string {
  return String(n).padStart(2, '0');
}

/** 从 RangePicker 读取墙钟分量（YYYY-MM-DD 不走 timezoneInterceptor） */
function pickerWall(d: Dayjs): { date: string; h: number; m: number; s: number; ms: number } {
  return {
    date: d.format('YYYY-MM-DD'),
    h: d.hour(),
    m: d.minute(),
    s: d.second(),
    ms: d.millisecond(),
  };
}

function isDayStart(w: ReturnType<typeof pickerWall>): boolean {
  return w.h === 0 && w.m === 0 && w.s === 0 && w.ms === 0;
}

function isDayEnd(w: ReturnType<typeof pickerWall>): boolean {
  return w.h === 23 && w.m === 59 && w.s === 59;
}

/** 当前时刻的展示墙钟（管理后台展示时区，用户端个人时区） */
export function timedisplayNow(): Dayjs {
  return dayjs().tz(resolveTimedisplay());
}

/** 仪表盘/财务日统计「今天」：管理端走站点运营时区，用户端走个人展示时区 */
export function statsCalendarNow(): Dayjs {
  const tz = useAuthStore.getState().user?.role === 'admin'
    ? resolveSiteTimedisplay()
    : resolveTimedisplay();
  return dayjs().tz(tz);
}

/** 日志/任务列表默认「今日」范围 */
export function defaultLogDayRange(): [Dayjs, Dayjs] {
  const now = timedisplayNow();
  return [now.startOf('day'), now.endOf('day')];
}

/** 财务明细默认「本月」范围（展示时区自然月） */
export function defaultMonthRange(): [Dayjs, Dayjs] {
  const now = timedisplayNow();
  return [now.startOf('month'), now.endOf('month')];
}

/**
 * 单侧边界 → 后端 naive 墙钟串（按 timedisplay 解释）。
 * 00:00:00 起点、23:59:59 终点仍按自然日半开，与纯日期筛选兼容。
 * 勿用 format('YYYY-MM-DD HH:mm:ss')（interceptor 会加展示后缀）。
 */
function toTimedisplayBound(d: Dayjs, asEnd: boolean): string {
  const tz = resolveTimedisplay();
  const w = pickerWall(d);
  if (!asEnd && isDayStart(w)) {
    return `${w.date} 00:00:00`;
  }
  if (asEnd && isDayEnd(w)) {
    // YYYY-MM-DD 不被 interceptor 改写
    const next = dayjs.tz(w.date, tz).add(1, 'day').format('YYYY-MM-DD');
    return `${next} 00:00:00`;
  }
  return `${w.date} ${pad2(w.h)}:${pad2(w.m)}:${pad2(w.s)}`;
}

/** RangePicker → 绝对时刻参数（日志/充值等半开 timestamptz 过滤，支持精确到秒）。 */
export function toDateRangeParams(
  range: DayjsRange,
): { start_date?: string; end_date?: string } {
  const ends = rangeEnds(range);
  if (!ends) return {};
  return {
    start_date: toTimedisplayBound(ends[0], false),
    end_date: toTimedisplayBound(ends[1], true),
  };
}

/** 单日 → 绝对 ISO；非法日期返回空串（调用方应先校验）。 */
export function toAbsoluteDateParam(d: Dayjs, endOfDay = false): string {
  const x = asDayjs(d);
  if (!x) return '';
  return toTimedisplayBound(x, endOfDay);
}

/**
 * 日历日 YYYY-MM-DD：仪表盘/财务日统计/用量同步等走 `calculate_query_slices` /
 * `NaiveDate` 的接口。勿用绝对 ISO，避免浏览器时区与站点 timedisplay 不一致时错日。
 */
export function toCalendarDateRangeParams(
  range: DayjsRange,
): { start_date?: string; end_date?: string } {
  const ends = rangeEnds(range);
  if (!ends) return {};
  return {
    start_date: ends[0].format('YYYY-MM-DD'),
    end_date: ends[1].format('YYYY-MM-DD'),
  };
}

/** 单日日历 YYYY-MM-DD。 */
export function toCalendarDateParam(d: Dayjs): string {
  const x = asDayjs(d);
  return x ? x.format('YYYY-MM-DD') : '';
}

/** 财务列表等 → `start_time` / `end_time`（半开绝对时刻）。 */
export function toTimeRangeParams(
  range: DayjsRange,
): { start_time?: string; end_time?: string } {
  const { start_date, end_date } = toDateRangeParams(range);
  if (!start_date || !end_date) {
    return {};
  }
  return { start_time: start_date, end_time: end_date };
}

/** 用户端日志：最早可选时刻（近 1 年）。 */
function userLogMinMoment(): Dayjs {
  return timedisplayNow().subtract(1, 'year').startOf('day');
}

/** 用户端日志：校正到近 1 年内且跨度 ≤ 1 个月（保留时刻精度）。 */
export function clampUserLogRange(start: Dayjs, end: Dayjs): [Dayjs, Dayjs] {
  const min = userLogMinMoment();
  const max = timedisplayNow();
  let s = start;
  let e = end;
  if (s.isBefore(min)) s = min;
  if (e.isAfter(max)) e = max;
  if (e.isBefore(s)) e = s;
  const spanEnd = s.add(1, 'month');
  if (e.isAfter(spanEnd)) e = spanEnd.isAfter(max) ? max : spanEnd;
  return [s, e];
}

/**
 * 用户端 RangePicker.disabledDate：近 1 年 + 选段中最长 1 个月。
 * `selecting` 来自 onCalendarChange（选第二端时限制跨度）。
 */
export function userLogDisabledDate(
  current: Dayjs,
  selecting?: [Dayjs | null, Dayjs | null] | null,
): boolean {
  if (!current?.isValid?.()) return false;
  const min = userLogMinMoment();
  const max = timedisplayNow().endOf('day');
  if (current.isBefore(min, 'day') || current.isAfter(max, 'day')) {
    return true;
  }
  const a = selecting?.[0];
  const b = selecting?.[1];
  if (a && !b) {
    return (
      current.isBefore(a.subtract(1, 'month').startOf('day')) ||
      current.isAfter(a.add(1, 'month').endOf('day'))
    );
  }
  if (!a && b) {
    return (
      current.isBefore(b.subtract(1, 'month').startOf('day')) ||
      current.isAfter(b.add(1, 'month').endOf('day'))
    );
  }
  return false;
}

