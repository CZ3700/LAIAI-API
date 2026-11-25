import React, { useState, useMemo } from 'react';
import {
  Calendar as CalendarIcon,
  Clock,
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  ChevronsRight,
  ChevronDown,
  X,
  RotateCcw,
  Check,
  Timer,
  CalendarDays,
  ArrowRight,
} from 'lucide-react';
import type { Dayjs } from 'dayjs';
import { useTranslation } from 'react-i18next';
import { Popover, theme } from 'antd';
import {
  LOG_DATETIME_FORMAT,
  clampUserLogRange,
  timedisplayNow,
  userLogDisabledDate,
} from '../utils/dateRangeParams';

type Props = {
  value?: [Dayjs, Dayjs] | null;
  onChange?: (value: [Dayjs, Dayjs] | null) => void;
  isAdmin?: boolean;
  style?: React.CSSProperties;
  className?: string;
  placeholder?: [string, string];
  disabled?: boolean;
};

// ── 工具函数：数字补零 ─────────────────────────────────────────────
const pad2 = (n: number) => String(n).padStart(2, '0');

// ── 预设时间段定义 ──────────────────────────────────────────────────
interface TimePreset {
  key: string;
  label: string;
  category: 'relative' | 'calendar';
  getValue: () => [Dayjs, Dayjs];
}

const buildTimePresets = (t: (k: string, d: string) => string): TimePreset[] => {
  return [
    // 相对时间段（近X分钟/小时）
    {
      key: '15m',
      label: t('logs.range_last_15m', '近15分钟'),
      category: 'relative',
      getValue: () => {
        const now = timedisplayNow();
        return [now.subtract(15, 'minute'), now];
      },
    },
    {
      key: '30m',
      label: t('logs.range_last_30m', '近30分钟'),
      category: 'relative',
      getValue: () => {
        const now = timedisplayNow();
        return [now.subtract(30, 'minute'), now];
      },
    },
    {
      key: '1h',
      label: t('logs.range_last_1h', '近1小时'),
      category: 'relative',
      getValue: () => {
        const now = timedisplayNow();
        return [now.subtract(1, 'hour'), now];
      },
    },
    {
      key: '3h',
      label: t('logs.range_last_3h', '近3小时'),
      category: 'relative',
      getValue: () => {
        const now = timedisplayNow();
        return [now.subtract(3, 'hour'), now];
      },
    },
    {
      key: '6h',
      label: t('logs.range_last_6h', '近6小时'),
      category: 'relative',
      getValue: () => {
        const now = timedisplayNow();
        return [now.subtract(6, 'hour'), now];
      },
    },
    {
      key: '12h',
      label: t('logs.range_last_12h', '近12小时'),
      category: 'relative',
      getValue: () => {
        const now = timedisplayNow();
        return [now.subtract(12, 'hour'), now];
      },
    },
    {
      key: '24h',
      label: t('logs.range_last_24h', '近24小时'),
      category: 'relative',
      getValue: () => {
        const now = timedisplayNow();
        return [now.subtract(24, 'hour'), now];
      },
    },

    // 自然周期时间段
    {
      key: 'today_full',
      label: t('logs.range_today_full', '今天 (整天)'),
      category: 'calendar',
      getValue: () => {
        const now = timedisplayNow();
        return [now.startOf('day'), now.endOf('day')];
      },
    },
    {
      key: 'today_until_now',
      label: t('logs.range_today_now', '今天 (至今)'),
      category: 'calendar',
      getValue: () => {
        const now = timedisplayNow();
        return [now.startOf('day'), now];
      },
    },
    {
      key: 'yesterday',
      label: t('logs.range_yesterday', '昨天'),
      category: 'calendar',
      getValue: () => {
        const now = timedisplayNow();
        const y = now.subtract(1, 'day');
        return [y.startOf('day'), y.endOf('day')];
      },
    },
    {
      key: '3d',
      label: t('logs.range_last_3d', '近3天'),
      category: 'calendar',
      getValue: () => {
        const now = timedisplayNow();
        return [now.subtract(2, 'day').startOf('day'), now.endOf('day')];
      },
    },
    {
      key: '7d',
      label: t('logs.range_last_7d', '近7天'),
      category: 'calendar',
      getValue: () => {
        const now = timedisplayNow();
        return [now.subtract(6, 'day').startOf('day'), now.endOf('day')];
      },
    },
    {
      key: '30d',
      label: t('logs.range_last_30d', '近30天'),
      category: 'calendar',
      getValue: () => {
        const now = timedisplayNow();
        return [now.subtract(29, 'day').startOf('day'), now.endOf('day')];
      },
    },
    {
      key: 'this_month',
      label: t('logs.range_this_month', '本月'),
      category: 'calendar',
      getValue: () => {
        const now = timedisplayNow();
        return [now.startOf('month'), now.endOf('day')];
      },
    },
  ];
};

// ── 子组件：时分秒数字输入与微调组件 ──────────────────────────────
interface TimePickerInputProps {
  label: string;
  badgeDate?: string;
  time: Dayjs;
  onChange: (newTime: Dayjs) => void;
  isEnd?: boolean;
}

const TimePickerInput: React.FC<TimePickerInputProps> = ({
  label,
  badgeDate,
  time,
  onChange,
  isEnd = false,
}) => {
  const { t } = useTranslation();
  const hour = time.hour();
  const minute = time.minute();
  const second = time.second();

  const handleHourChange = (val: number) => {
    const clamped = Math.max(0, Math.min(23, isNaN(val) ? 0 : val));
    onChange(time.hour(clamped));
  };

  const handleMinuteChange = (val: number) => {
    const clamped = Math.max(0, Math.min(59, isNaN(val) ? 0 : val));
    onChange(time.minute(clamped));
  };

  const handleSecondChange = (val: number) => {
    const clamped = Math.max(0, Math.min(59, isNaN(val) ? 0 : val));
    onChange(time.second(clamped));
  };

  const stepValue = (
    current: number,
    delta: number,
    max: number,
    setter: (v: number) => void
  ) => {
    let next = current + delta;
    if (next < 0) next = max;
    if (next > max) next = 0;
    setter(next);
  };

  return (
    <div className={`flex flex-col justify-between gap-2 p-2.5 rounded-lg border transition-all ${
      isEnd
        ? 'bg-white/70 dark:bg-white/[0.03] border-zinc-200/80 dark:border-white/10'
        : 'bg-white/70 dark:bg-white/[0.03] border-zinc-200/80 dark:border-white/10'
    }`}>
      {/* 头部：标签 + 日期标记 */}
      <div className="flex items-center justify-between">
        <span className="text-xs font-semibold text-zinc-700 dark:text-zinc-200 flex items-center gap-1">
          <Clock size={12} className="text-zinc-400 dark:text-zinc-500" />
          {label}
        </span>
        {badgeDate ? (
          <span className="text-[11px] font-mono font-medium px-1.5 py-0.5 rounded bg-zinc-200/70 dark:bg-white/10 text-zinc-700 dark:text-zinc-300">
            {badgeDate}
          </span>
        ) : (
          <span className="text-[10px] text-zinc-400 dark:text-zinc-500 select-none">{t('logs.no_date_selected', '未选日期')}</span>
        )}
      </div>

      {/* 时分秒输入框组合 (上下左右完全居中对称) */}
      <div className="flex items-center justify-center bg-white/90 dark:bg-black/40 px-2.5 py-2 rounded-md border border-zinc-200/80 dark:border-white/10 shadow-xs">
        <div className="flex items-center justify-center gap-1.5">
          {/* 小时 */}
          <div className="flex flex-col items-center justify-center gap-0.5">
            <input
              type="text"
              inputMode="numeric"
              value={pad2(hour)}
              onFocus={(e) => e.target.select()}
              onChange={(e) => handleHourChange(parseInt(e.target.value, 10))}
              onKeyDown={(e) => {
                if (e.key === 'ArrowUp') {
                  e.preventDefault();
                  stepValue(hour, 1, 23, handleHourChange);
                } else if (e.key === 'ArrowDown') {
                  e.preventDefault();
                  stepValue(hour, -1, 23, handleHourChange);
                }
              }}
              onWheel={(e) => {
                e.preventDefault();
                stepValue(hour, e.deltaY < 0 ? 1 : -1, 23, handleHourChange);
              }}
              className="w-9 h-7 text-center text-xs font-mono font-semibold rounded text-zinc-900 dark:text-zinc-100 bg-zinc-100/70 dark:bg-white/5 outline-none focus:ring-1 focus:ring-zinc-400 dark:focus:ring-zinc-600 cursor-pointer flex items-center justify-center"
              title={t('logs.hour', '时 (00-23)')}
            />
            <span className="text-[9px] text-zinc-400 dark:text-zinc-500 select-none leading-none">{t('logs.hour_unit', '时')}</span>
          </div>

          <span className="text-zinc-400 dark:text-zinc-500 font-mono text-xs select-none self-start mt-1.5 font-bold">:</span>

          {/* 分钟 */}
          <div className="flex flex-col items-center justify-center gap-0.5">
            <input
              type="text"
              inputMode="numeric"
              value={pad2(minute)}
              onFocus={(e) => e.target.select()}
              onChange={(e) => handleMinuteChange(parseInt(e.target.value, 10))}
              onKeyDown={(e) => {
                if (e.key === 'ArrowUp') {
                  e.preventDefault();
                  stepValue(minute, 1, 59, handleMinuteChange);
                } else if (e.key === 'ArrowDown') {
                  e.preventDefault();
                  stepValue(minute, -1, 59, handleMinuteChange);
                }
              }}
              onWheel={(e) => {
                e.preventDefault();
                stepValue(minute, e.deltaY < 0 ? 1 : -1, 59, handleMinuteChange);
              }}
              className="w-9 h-7 text-center text-xs font-mono font-semibold rounded text-zinc-900 dark:text-zinc-100 bg-zinc-100/70 dark:bg-white/5 outline-none focus:ring-1 focus:ring-zinc-400 dark:focus:ring-zinc-600 cursor-pointer flex items-center justify-center"
              title={t('logs.minute', '分 (00-59)')}
            />
            <span className="text-[9px] text-zinc-400 dark:text-zinc-500 select-none leading-none">{t('logs.minute_unit', '分')}</span>
          </div>

          <span className="text-zinc-400 dark:text-zinc-500 font-mono text-xs select-none self-start mt-1.5 font-bold">:</span>

          {/* 秒 */}
          <div className="flex flex-col items-center justify-center gap-0.5">
            <input
              type="text"
              inputMode="numeric"
              value={pad2(second)}
              onFocus={(e) => e.target.select()}
              onChange={(e) => handleSecondChange(parseInt(e.target.value, 10))}
              onKeyDown={(e) => {
                if (e.key === 'ArrowUp') {
                  e.preventDefault();
                  stepValue(second, 1, 59, handleSecondChange);
                } else if (e.key === 'ArrowDown') {
                  e.preventDefault();
                  stepValue(second, -1, 59, handleSecondChange);
                }
              }}
              onWheel={(e) => {
                e.preventDefault();
                stepValue(second, e.deltaY < 0 ? 1 : -1, 59, handleSecondChange);
              }}
              className="w-9 h-7 text-center text-xs font-mono font-semibold rounded text-zinc-900 dark:text-zinc-100 bg-zinc-100/70 dark:bg-white/5 outline-none focus:ring-1 focus:ring-zinc-400 dark:focus:ring-zinc-600 cursor-pointer flex items-center justify-center"
              title={t('logs.second', '秒 (00-59)')}
            />
            <span className="text-[9px] text-zinc-400 dark:text-zinc-500 select-none leading-none">{t('logs.second_unit', '秒')}</span>
          </div>
        </div>
      </div>
    </div>
  );
};

// ── 子组件：单月日历视图 ───────────────────────────────────────────
interface MonthCalendarProps {
  viewMonth: Dayjs;
  onPrevMonth?: () => void;
  onNextMonth?: () => void;
  onPrevYear?: () => void;
  onNextYear?: () => void;
  rangeStart: Dayjs | null;
  rangeEnd: Dayjs | null;
  hoverDate: Dayjs | null;
  onDateClick: (d: Dayjs) => void;
  onDateHover: (d: Dayjs | null) => void;
  isAdmin?: boolean;
}

const MonthCalendar: React.FC<MonthCalendarProps> = ({
  viewMonth,
  onPrevMonth,
  onNextMonth,
  onPrevYear,
  onNextYear,
  rangeStart,
  rangeEnd,
  hoverDate,
  onDateClick,
  onDateHover,
  isAdmin = false,
}) => {
  const { t, i18n } = useTranslation();
  const lang = (i18n.language || 'zh').toLowerCase();
  const isZh = lang.startsWith('zh');
  const isJa = lang.startsWith('ja');
  const isKo = lang.startsWith('ko');
  const isVi = lang.startsWith('vi');
  const now = useMemo(() => timedisplayNow(), []);

  const weekdays = useMemo(() => {
    if (isZh) return ['日', '一', '二', '三', '四', '五', '六'];
    if (isJa) return ['日', '月', '火', '水', '木', '金', '土'];
    if (isKo) return ['일', '월', '화', '수', '목', '금', '토'];
    if (isVi) return ['CN', 'T2', 'T3', 'T4', 'T5', 'T6', 'T7'];
    if (lang.startsWith('th')) return ['อา.', 'จ.', 'อ.', 'พ.', 'พฤ.', 'ศ.', 'ส.'];
    if (lang.startsWith('ms')) return ['Ahd', 'Isn', 'Sel', 'Rab', 'Kha', 'Jum', 'Sab'];
    if (lang.startsWith('fr')) return ['Di', 'Lu', 'Ma', 'Me', 'Je', 'Ve', 'Sa'];
    if (lang.startsWith('de')) return ['So', 'Mo', 'Di', 'Mi', 'Do', 'Fr', 'Sa'];
    if (lang.startsWith('es')) return ['Do', 'Lu', 'Ma', 'Mi', 'Ju', 'Vi', 'Sá'];
    if (lang.startsWith('pt')) return ['Do', 'Se', 'Te', 'Qu', 'Qu', 'Se', 'Sá'];
    if (lang.startsWith('ru')) return ['Вс', 'Пн', 'Вт', 'Ср', 'Чт', 'Пт', 'Сб'];
    if (lang.startsWith('ar')) return ['أح', 'إث', 'ثل', 'أر', 'خم', 'جم', 'سب'];
    return ['Su', 'Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa'];
  }, [isZh, isJa, isKo, isVi, lang]);

  const monthTitle = useMemo(() => {
    if (isZh || isJa) return viewMonth.format('YYYY年 M月');
    if (isKo) return viewMonth.format('YYYY년 M월');
    if (isVi) return viewMonth.format('MM/YYYY');
    return viewMonth.format('MMM YYYY');
  }, [viewMonth, isZh, isJa, isKo, isVi]);

  // 生成该月份日历网格
  const calendarCells = useMemo(() => {
    const startOfMonth = viewMonth.startOf('month');
    const daysInMonth = viewMonth.daysInMonth();
    const startWeekday = startOfMonth.day(); // 0 是周日

    const cells: { date: Dayjs; isCurrentMonth: boolean }[] = [];

    // 前置填充上个月末尾
    for (let i = startWeekday - 1; i >= 0; i--) {
      cells.push({
        date: startOfMonth.subtract(i + 1, 'day'),
        isCurrentMonth: false,
      });
    }

    // 当月日期
    for (let d = 1; d <= daysInMonth; d++) {
      cells.push({
        date: viewMonth.date(d),
        isCurrentMonth: true,
      });
    }

    // 后置填充下个月起始（补齐到 35 或 42 格）
    const totalNeeded = cells.length > 35 ? 42 : 35;
    const remaining = totalNeeded - cells.length;
    const nextMonthStart = viewMonth.add(1, 'month').startOf('month');
    for (let i = 0; i < remaining; i++) {
      cells.push({
        date: nextMonthStart.add(i, 'day'),
        isCurrentMonth: false,
      });
    }

    return cells;
  }, [viewMonth]);

  // 计算当前的动态生效范围 (考虑用户鼠标悬浮预览)
  const effectiveRange = useMemo(() => {
    if (rangeStart && !rangeEnd && hoverDate) {
      if (hoverDate.isBefore(rangeStart, 'day')) {
        return { start: hoverDate.startOf('day'), end: rangeStart.endOf('day'), isHovering: true };
      }
      return { start: rangeStart.startOf('day'), end: hoverDate.endOf('day'), isHovering: true };
    }
    if (rangeStart && rangeEnd) {
      return { start: rangeStart.startOf('day'), end: rangeEnd.endOf('day'), isHovering: false };
    }
    return null;
  }, [rangeStart, rangeEnd, hoverDate]);

  return (
    <div className="w-[235px] select-none flex flex-col">
      {/* 月历头部 */}
      <div className="flex items-center justify-between mb-2 px-1">
        <div className="flex items-center gap-0.5">
          {onPrevYear && (
            <button
              type="button"
              onClick={onPrevYear}
              className="p-1 rounded-md hover:bg-zinc-100 dark:hover:bg-zinc-800 text-zinc-500 hover:text-zinc-900 dark:hover:text-zinc-100 transition-colors cursor-pointer"
              title={t('logs.prev_year', '上一年')}
            >
              <ChevronsLeft size={14} />
            </button>
          )}
          {onPrevMonth && (
            <button
              type="button"
              onClick={onPrevMonth}
              className="p-1 rounded-md hover:bg-zinc-100 dark:hover:bg-zinc-800 text-zinc-500 hover:text-zinc-900 dark:hover:text-zinc-100 transition-colors cursor-pointer"
              title={t('logs.prev_month', '上一月')}
            >
              <ChevronLeft size={14} />
            </button>
          )}
        </div>

        <div className="text-xs font-semibold text-zinc-800 dark:text-zinc-200">
          {monthTitle}
        </div>

        <div className="flex items-center gap-0.5">
          {onNextMonth && (
            <button
              type="button"
              onClick={onNextMonth}
              className="p-1 rounded-md hover:bg-zinc-100 dark:hover:bg-zinc-800 text-zinc-500 hover:text-zinc-900 dark:hover:text-zinc-100 transition-colors cursor-pointer"
              title={t('logs.next_month', '下一月')}
            >
              <ChevronRight size={14} />
            </button>
          )}
          {onNextYear && (
            <button
              type="button"
              onClick={onNextYear}
              className="p-1 rounded-md hover:bg-zinc-100 dark:hover:bg-zinc-800 text-zinc-500 hover:text-zinc-900 dark:hover:text-zinc-100 transition-colors cursor-pointer"
              title={t('logs.next_year', '下一年')}
            >
              <ChevronsRight size={14} />
            </button>
          )}
        </div>
      </div>

      {/* 星期表头 */}
      <div className="grid grid-cols-7 mb-1 text-center">
        {weekdays.map((w, i) => (
          <div
            key={i}
            className="text-[11px] font-medium text-zinc-400 dark:text-zinc-500 py-1"
          >
            {w}
          </div>
        ))}
      </div>

      {/* 日历格子 */}
      <div className="grid grid-cols-7 gap-y-1">
        {calendarCells.map((cell, idx) => {
          const { date, isCurrentMonth } = cell;
          const isToday = date.isSame(now, 'day');
          const isDisabled = !isAdmin && userLogDisabledDate(date, rangeStart && !rangeEnd ? [rangeStart, null] : null);

          let isStart = false;
          let isEnd = false;
          let isInMiddle = false;

          if (effectiveRange && !isDisabled) {
            isStart = date.isSame(effectiveRange.start, 'day');
            isEnd = date.isSame(effectiveRange.end, 'day');
            isInMiddle = date.isAfter(effectiveRange.start, 'day') && date.isBefore(effectiveRange.end, 'day');
          }

          const isSoloSelection = isStart && isEnd;

          return (
            <div
              key={idx}
              className={`relative h-7 flex items-center justify-center p-0 text-center ${
                isInMiddle
                  ? 'bg-zinc-100 dark:bg-zinc-800/70'
                  : ''
              } ${
                isStart && !isSoloSelection
                  ? 'bg-gradient-to-r from-transparent via-zinc-100 dark:via-zinc-800/70 to-zinc-100 dark:to-zinc-800/70'
                  : ''
              } ${
                isEnd && !isSoloSelection
                  ? 'bg-gradient-to-l from-transparent via-zinc-100 dark:via-zinc-800/70 to-zinc-100 dark:to-zinc-800/70'
                  : ''
              }`}
            >
              <button
                type="button"
                disabled={isDisabled}
                onClick={() => !isDisabled && onDateClick(date)}
                onMouseEnter={() => !isDisabled && onDateHover(date)}
                className={`w-7 h-7 text-xs flex items-center justify-center rounded-md transition-all relative z-10 cursor-pointer ${
                  !isCurrentMonth
                    ? 'text-zinc-300 dark:text-zinc-600 opacity-60'
                    : 'text-zinc-700 dark:text-zinc-300'
                } ${
                  isDisabled
                    ? 'opacity-20 cursor-not-allowed pointer-events-none line-through'
                    : ''
                } ${
                  isStart || isEnd
                    ? '!bg-zinc-900 !text-white dark:!bg-zinc-100 dark:!text-zinc-900 font-semibold shadow-xs'
                    : isInMiddle
                    ? 'text-zinc-900 dark:text-zinc-100 font-medium'
                    : !isDisabled
                    ? 'hover:bg-zinc-200/80 dark:hover:bg-zinc-800'
                    : ''
                }`}
              >
                {date.date()}
                {isToday && !isStart && !isEnd && (
                  <span className="absolute bottom-0.5 left-1/2 -translate-x-1/2 w-1 h-1 rounded-full bg-zinc-900 dark:bg-zinc-100 opacity-80" />
                )}
              </button>
            </div>
          );
        })}
      </div>
    </div>
  );
};

// ── 主组件：Shadcn UI 风格的专业时间段选择器 ───────────────────────
const LogDateTimeRangePicker: React.FC<Props> = ({
  value,
  onChange,
  isAdmin = false,
  style,
  className,
  placeholder,
  disabled = false,
}) => {
  const { t } = useTranslation();
  const { token } = theme.useToken();
  const [open, setOpen] = useState(false);
  const [isHovered, setIsHovered] = useState(false);

  // 内部临时草稿状态
  const [draftStart, setDraftStart] = useState<Dayjs | null>(null);
  const [draftEnd, setDraftEnd] = useState<Dayjs | null>(null);
  const [pickingState, setPickingState] = useState<'idle' | 'picked_start'>('idle');
  const [hoverDate, setHoverDate] = useState<Dayjs | null>(null);

  // 双月日历左侧与右侧视图的月份锚点
  const [leftMonth, setLeftMonth] = useState<Dayjs>(() => timedisplayNow().startOf('month'));
  const rightMonth = useMemo(() => leftMonth.add(1, 'month'), [leftMonth]);

  // 打开/关闭处理：打开时同步外部 value 到内部草稿
  const handleOpenChange = (nextOpen: boolean) => {
    if (disabled) return;
    if (nextOpen) {
      const start = value?.[0] || null;
      const end = value?.[1] || null;
      setDraftStart(start);
      setDraftEnd(end);
      setPickingState('idle');
      setHoverDate(null);

      // 若有选中时间，将左侧月份对齐到起始月
      if (start) {
        setLeftMonth(start.startOf('month'));
      } else {
        setLeftMonth(timedisplayNow().startOf('month'));
      }
    }
    setOpen(nextOpen);
  };

  // 预设列表
  const presets = useMemo(() => buildTimePresets(t), [t]);

  // 判断当前选值是否匹配某个预设
  const activePresetKey = useMemo(() => {
    if (!draftStart || !draftEnd) return null;
    for (const p of presets) {
      const v = p.getValue();
      // 容差 60 秒内视为命中
      if (
        Math.abs(v[0].diff(draftStart, 'second')) <= 60 &&
        Math.abs(v[1].diff(draftEnd, 'second')) <= 60
      ) {
        return p.key;
      }
    }
    return null;
  }, [draftStart, draftEnd, presets]);

  // 点击预设时间段
  const handleApplyPreset = (preset: TimePreset) => {
    const [start, end] = preset.getValue();
    const finalStart = isAdmin ? start : clampUserLogRange(start, end)[0];
    const finalEnd = isAdmin ? end : clampUserLogRange(start, end)[1];

    setDraftStart(finalStart);
    setDraftEnd(finalEnd);
    setLeftMonth(finalStart.startOf('month'));
    setPickingState('idle');
    setHoverDate(null);
  };

  // 日期点击交互逻辑
  const handleDateClick = (clickedDate: Dayjs) => {
    if (pickingState === 'idle' || !draftStart || draftEnd) {
      // 首次点击：确定起点，保留当前开始时间（或默认 00:00:00）
      const h = draftStart ? draftStart.hour() : 0;
      const m = draftStart ? draftStart.minute() : 0;
      const s = draftStart ? draftStart.second() : 0;
      const newStart = clickedDate.hour(h).minute(m).second(s);

      setDraftStart(newStart);
      setDraftEnd(null);
      setPickingState('picked_start');
      setHoverDate(null);
    } else {
      // 第二次点击：确定终点
      const validStart = draftStart || timedisplayNow().startOf('day');
      let newStart = validStart;
      let newEnd = clickedDate;

      // 如果点击在起点之前，则反转并完成选择
      if (newEnd.isBefore(newStart, 'day')) {
        const temp = newStart;
        newStart = newEnd.hour(newStart.hour()).minute(newStart.minute()).second(newStart.second());
        newEnd = temp.hour(23).minute(59).second(59);
      } else {
        newEnd = newEnd.hour(23).minute(59).second(59);
      }

      // 普通用户边界校正
      if (!isAdmin) {
        const [clampedStart, clampedEnd] = clampUserLogRange(newStart, newEnd);
        newStart = clampedStart;
        newEnd = clampedEnd;
      }

      setDraftStart(newStart);
      setDraftEnd(newEnd);
      setPickingState('idle');
      setHoverDate(null);
    }
  };

  // 应用当前选中
  const handleApply = () => {
    if (draftStart && draftEnd) {
      let s = draftStart;
      let e = draftEnd;
      if (e.isBefore(s)) {
        const temp = s;
        s = e;
        e = temp;
      }
      const finalRange: [Dayjs, Dayjs] = isAdmin
        ? [s, e]
        : clampUserLogRange(s, e);
      onChange?.(finalRange);
    } else if (draftStart && !draftEnd) {
      // 若只点了起点，默认结束为当天的 23:59:59
      const endOfDay = draftStart.hour(23).minute(59).second(59);
      const finalRange: [Dayjs, Dayjs] = isAdmin
        ? [draftStart, endOfDay]
        : clampUserLogRange(draftStart, endOfDay);
      onChange?.(finalRange);
    } else if (isAdmin) {
      onChange?.(null);
    }
    setOpen(false);
  };

  // 重置 / 清除
  const handleClear = (e?: React.MouseEvent) => {
    e?.stopPropagation();
    if (isAdmin) {
      setDraftStart(null);
      setDraftEnd(null);
      onChange?.(null);
    } else {
      const now = timedisplayNow();
      const defaultR: [Dayjs, Dayjs] = [now.startOf('day'), now.endOf('day')];
      setDraftStart(defaultR[0]);
      setDraftEnd(defaultR[1]);
      onChange?.(defaultR);
    }
    setPickingState('idle');
    setHoverDate(null);
  };

  // 格式化展示文本
  const displayText = useMemo(() => {
    if (value?.[0] && value?.[1]) {
      return (
        <span className="flex items-center gap-1.5 font-mono text-[12px] tabular-nums text-zinc-900 dark:text-[rgba(255,255,255,0.85)]">
          <span>{value[0].format(LOG_DATETIME_FORMAT)}</span>
          <span className="text-[rgba(0,0,0,0.45)] dark:text-[rgba(255,255,255,0.45)] font-sans text-[12px]">至</span>
          <span>{value[1].format(LOG_DATETIME_FORMAT)}</span>
        </span>
      );
    }
    const pStart = placeholder?.[0] || t('logs.start_time', '开始时间');
    const pEnd = placeholder?.[1] || t('logs.end_time', '结束时间');
    return (
      <span className="text-[rgba(0,0,0,0.25)] dark:text-[rgba(255,255,255,0.25)] text-[12px] font-sans">
        {pStart} ~ {pEnd}
      </span>
    );
  }, [value, placeholder, t]);

  const now = timedisplayNow();

  const triggerStyle: React.CSSProperties = {
    height: 32,
    boxSizing: 'border-box',
    borderRadius: token.borderRadius,
    borderColor: open
      ? token.colorPrimary
      : isHovered
      ? token.colorPrimaryHover
      : token.colorBorder,
    boxShadow: open ? `0 0 0 2px ${token.controlOutline}` : 'none',
    backgroundColor: disabled
      ? (token.colorBgContainerDisabled || token.colorFillTertiary)
      : token.colorBgContainer,
    transition: 'all 0.2s cubic-bezier(0.645, 0.045, 0.355, 1)',
    ...style,
  };

  const popoverContent = (
    <div
      className="z-[5100] bg-white dark:bg-[#141414] text-zinc-900 dark:text-zinc-100 rounded-xl border border-zinc-200/90 dark:border-[#27272a] shadow-2xl shadow-zinc-900/15 dark:shadow-black/70 overflow-hidden animate-in fade-in-0 zoom-in-95 duration-150 flex flex-col md:flex-row w-full md:w-[670px] max-w-[calc(100vw-20px)]"
    >
          {/* 左侧快捷时间段预设面板 (与站点左侧菜单同色 #fafafa / #141414) */}
          <div className="w-full md:w-[140px] p-2.5 bg-[#fafafa] dark:bg-[#141414] border-b md:border-b-0 md:border-r border-[#e4e4e7] dark:border-[#1f1f23] flex flex-col gap-2 shrink-0">
            {/* 相对时间段 */}
            <div>
              <div className="text-[10px] font-semibold tracking-wider text-zinc-400 dark:text-zinc-500 uppercase px-2 mb-1 flex items-center gap-1.5 select-none">
                <Timer size={12} className="text-zinc-400 dark:text-zinc-500" />
                {t('logs.preset_relative', '即时时间段')}
              </div>
              <div className="flex flex-row md:flex-col gap-0.5 overflow-x-auto md:overflow-x-visible">
                {presets
                  .filter((p) => p.category === 'relative')
                  .map((preset) => {
                    const isActive = activePresetKey === preset.key;
                    return (
                      <button
                        key={preset.key}
                        type="button"
                        onClick={() => handleApplyPreset(preset)}
                        className={`px-2.5 py-1 text-xs rounded-md text-left transition-all cursor-pointer select-none whitespace-nowrap flex items-center justify-between ${
                          isActive
                            ? 'bg-zinc-900 text-white dark:bg-zinc-100 dark:text-zinc-900 font-semibold shadow-xs'
                            : 'text-zinc-600 dark:text-zinc-400 hover:bg-zinc-200/60 dark:hover:bg-white/5 hover:text-zinc-900 dark:hover:text-zinc-200'
                        }`}
                      >
                        <span>{preset.label}</span>
                        {isActive && <Check size={12} className="ml-1 shrink-0" />}
                      </button>
                    );
                  })}
              </div>
            </div>

            <div className="w-full h-px bg-zinc-200/70 dark:bg-[#1f1f23] my-0.5 hidden md:block" />

            {/* 自然周期 */}
            <div>
              <div className="text-[10px] font-semibold tracking-wider text-zinc-400 dark:text-zinc-500 uppercase px-2 mb-1 flex items-center gap-1.5 select-none">
                <CalendarDays size={12} className="text-zinc-400 dark:text-zinc-500" />
                {t('logs.preset_calendar', '自然日与周期')}
              </div>
              <div className="flex flex-row md:flex-col gap-0.5 overflow-x-auto md:overflow-x-visible">
                {presets
                  .filter((p) => p.category === 'calendar')
                  .map((preset) => {
                    const isActive = activePresetKey === preset.key;
                    return (
                      <button
                        key={preset.key}
                        type="button"
                        onClick={() => handleApplyPreset(preset)}
                        className={`px-2.5 py-1 text-xs rounded-md text-left transition-all cursor-pointer select-none whitespace-nowrap flex items-center justify-between ${
                          isActive
                            ? 'bg-zinc-900 text-white dark:bg-zinc-100 dark:text-zinc-900 font-semibold shadow-xs'
                            : 'text-zinc-600 dark:text-zinc-400 hover:bg-zinc-200/60 dark:hover:bg-white/5 hover:text-zinc-900 dark:hover:text-zinc-200'
                        }`}
                      >
                        <span>{preset.label}</span>
                        {isActive && <Check size={12} className="ml-1 shrink-0" />}
                      </button>
                    );
                  })}
              </div>
            </div>
          </div>

          {/* 右侧主区域：毛玻璃透明质感（与顶部导航 Header 风格一致） */}
          <div
            className="w-full md:w-[530px] shrink-0 flex flex-col min-w-0 bg-white/70 dark:bg-[rgba(0,0,0,0.48)] backdrop-blur-[18px]"
            style={{
              backdropFilter: 'blur(18px) saturate(180%)',
              WebkitBackdropFilter: 'blur(18px) saturate(180%)',
            }}
          >
            {/* 双月日历 */}
            <div
              className="p-3.5 flex flex-col sm:flex-row gap-4 items-center sm:items-start justify-center"
              onMouseLeave={() => setHoverDate(null)}
            >
              <MonthCalendar
                viewMonth={leftMonth}
                onPrevYear={() => setLeftMonth(leftMonth.subtract(1, 'year'))}
                onPrevMonth={() => setLeftMonth(leftMonth.subtract(1, 'month'))}
                rangeStart={draftStart}
                rangeEnd={draftEnd}
                hoverDate={hoverDate}
                onDateClick={handleDateClick}
                onDateHover={setHoverDate}
                isAdmin={isAdmin}
              />
              <div className="hidden sm:block w-px bg-zinc-200/70 dark:bg-white/10 self-stretch my-1" />
              <MonthCalendar
                viewMonth={rightMonth}
                onNextMonth={() => setLeftMonth(leftMonth.add(1, 'month'))}
                onNextYear={() => setLeftMonth(leftMonth.add(1, 'year'))}
                rangeStart={draftStart}
                rangeEnd={draftEnd}
                hoverDate={hoverDate}
                onDateClick={handleDateClick}
                onDateHover={setHoverDate}
                isAdmin={isAdmin}
              />
            </div>

            {/* 精确时分秒时间段控制区域 (与上方双月对称的 2 列网格) */}
            <div className="px-3.5 py-2.5 bg-zinc-50/50 dark:bg-white/[0.02] border-t border-zinc-200/60 dark:border-white/10">
              <div className="grid grid-cols-2 gap-4">
                <TimePickerInput
                  label={t('logs.start_time', '开始时间')}
                  badgeDate={draftStart ? draftStart.format('YYYY-MM-DD') : undefined}
                  time={draftStart || now.startOf('day')}
                  onChange={(newTime) => {
                    if (draftStart) {
                      setDraftStart(
                        draftStart
                          .hour(newTime.hour())
                          .minute(newTime.minute())
                          .second(newTime.second())
                      );
                    } else {
                      setDraftStart(newTime);
                    }
                  }}
                />

                <TimePickerInput
                  label={t('logs.end_time', '结束时间')}
                  badgeDate={draftEnd ? draftEnd.format('YYYY-MM-DD') : undefined}
                  time={draftEnd || now.endOf('day')}
                  isEnd={true}
                  onChange={(newTime) => {
                    if (draftEnd) {
                      setDraftEnd(
                        draftEnd
                          .hour(newTime.hour())
                          .minute(newTime.minute())
                          .second(newTime.second())
                      );
                    } else {
                      setDraftEnd(newTime);
                    }
                  }}
                />
              </div>
            </div>

            {/* 底部确认栏：完整时间段展示 + 按钮换行显示 */}
            <div className="px-3.5 py-2.5 bg-zinc-50/70 dark:bg-black/30 border-t border-zinc-200/60 dark:border-white/10 flex flex-col gap-2">
              {/* 第一行：选择日期时间段完整展示 */}
              <div className="text-xs text-zinc-700 dark:text-zinc-300 font-mono flex items-center gap-1.5 px-0.5">
                {draftStart ? (
                  <span className="flex items-center gap-1.5 whitespace-nowrap">
                    <span className="font-semibold text-zinc-900 dark:text-zinc-100">
                      {draftStart.format('YYYY-MM-DD HH:mm:ss')}
                    </span>
                    <ArrowRight size={12} className="text-zinc-400 shrink-0" />
                    <span className="font-semibold text-zinc-900 dark:text-zinc-100">
                      {draftEnd ? draftEnd.format('YYYY-MM-DD HH:mm:ss') : t('logs.no_end_selected', '未选结束')}
                    </span>
                  </span>
                ) : (
                  <span className="text-zinc-400">{t('logs.please_select_range', '请选择时间段')}</span>
                )}
              </div>

              {/* 第二行：操作按钮换行显示并靠右对齐 */}
              <div className="flex items-center justify-end gap-2 pt-1 border-t border-zinc-200/50 dark:border-white/10">
                <button
                  type="button"
                  onClick={() => {
                    if (isAdmin) {
                      setDraftStart(null);
                      setDraftEnd(null);
                    } else {
                      const d = timedisplayNow();
                      setDraftStart(d.startOf('day'));
                      setDraftEnd(d.endOf('day'));
                    }
                  }}
                  className="px-2.5 py-1 text-xs rounded-md text-zinc-600 dark:text-zinc-400 hover:bg-zinc-200/60 dark:hover:bg-white/10 transition-colors flex items-center gap-1 cursor-pointer"
                >
                  <RotateCcw size={12} />
                  {isAdmin ? t('common.clear', '清空') : t('logs.today', '今日')}
                </button>

                <button
                  type="button"
                  onClick={() => setOpen(false)}
                  className="px-3 py-1 text-xs rounded-md border border-zinc-200 dark:border-white/15 bg-white/80 dark:bg-white/5 text-zinc-700 dark:text-zinc-300 hover:bg-zinc-50 dark:hover:bg-white/10 transition-colors cursor-pointer"
                >
                  {t('common.cancel', '取消')}
                </button>

                <button
                  type="button"
                  onClick={handleApply}
                  className="px-3.5 py-1 text-xs font-medium rounded-md bg-zinc-900 text-white dark:bg-zinc-100 dark:text-zinc-900 hover:bg-zinc-800 dark:hover:bg-zinc-200 shadow-xs transition-colors flex items-center gap-1 cursor-pointer"
                >
                  <Check size={13} />
                  {t('common.confirm', '确认')}
                </button>
              </div>
        </div>
      </div>
    </div>
  );

  return (
    <Popover
      open={open}
      onOpenChange={handleOpenChange}
      trigger="click"
      placement="bottomLeft"
      arrow={false}
      styles={{ container: { padding: 0, background: 'transparent', boxShadow: 'none' } }}
      overlayClassName="custom-datetime-popover"
      content={popoverContent}
    >
      <button
        type="button"
        disabled={disabled}
        onMouseEnter={() => setIsHovered(true)}
        onMouseLeave={() => setIsHovered(false)}
        style={triggerStyle}
        className={`inline-flex items-center justify-between gap-2 px-[11px] h-[32px] min-h-[32px] max-h-[32px] text-[12px] leading-none border box-border cursor-pointer select-none group min-w-[360px] ${
          disabled ? 'opacity-50 cursor-not-allowed' : ''
        } ${className || ''}`}
      >
        <div className="flex items-center gap-2 overflow-hidden">
          <CalendarIcon size={14} className="text-[rgba(0,0,0,0.25)] dark:text-[rgba(255,255,255,0.25)] group-hover:text-[rgba(0,0,0,0.45)] dark:group-hover:text-[rgba(255,255,255,0.45)] transition-colors shrink-0" />
          <div className="truncate">{displayText}</div>
        </div>

        <div className="flex items-center gap-1.5 shrink-0 ml-auto">
          {/* 清空按钮 */}
          {value?.[0] && value?.[1] && isAdmin && (
            <span
              onClick={handleClear}
              className="p-0.5 rounded hover:bg-zinc-200/70 dark:hover:bg-zinc-700 text-[rgba(0,0,0,0.25)] dark:text-[rgba(255,255,255,0.25)] hover:text-zinc-700 dark:hover:text-zinc-200 transition-colors"
              title={t('common.clear', '清空')}
            >
              <X size={12} />
            </span>
          )}

          <ChevronDown size={12} className="text-[rgba(0,0,0,0.25)] dark:text-[rgba(255,255,255,0.25)] group-hover:text-[rgba(0,0,0,0.45)] dark:group-hover:text-[rgba(255,255,255,0.45)] transition-colors" />
        </div>
      </button>
    </Popover>
  );
};

export default LogDateTimeRangePicker;
