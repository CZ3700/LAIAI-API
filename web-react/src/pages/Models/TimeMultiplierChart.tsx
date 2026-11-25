import React, { useMemo } from 'react';
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import ErrorBoundary from '../../components/ErrorBoundary';
import {
  buildDayMultiplierSegments,
  formatClockMinutes,
  formatRate,
  isFreeTimeMultiplier,
  type TimeMultiplierRule,
} from '../../utils/timeMultipliers';

const WEEKDAY_FULL = ['周一', '周二', '周三', '周四', '周五', '周六', '周日'];

function rateFill(rate: number): string {
  if (Math.abs(rate - 1) < 1e-9) return 'var(--muted-foreground)';
  if (rate < 1) return 'hsl(142, 71%, 45%)';
  return 'hsl(24, 95%, 53%)';
}

function rateFillSoft(rate: number, isLight: boolean): string {
  if (Math.abs(rate - 1) < 1e-9) return isLight ? '#e4e4e7' : '#3f3f46';
  if (rate < 1) return isLight ? 'rgba(34,197,94,0.55)' : 'rgba(74,222,128,0.45)';
  const t = Math.min(1, (rate - 1) / 2);
  const a = 0.45 + t * 0.35;
  return isLight ? `rgba(249,115,22,${a})` : `rgba(251,146,60,${a})`;
}

type ChartRow = {
  day: string;
  [key: string]: string | number;
};

function ScheduleTooltip({
  active,
  payload,
}: {
  active?: boolean;
  payload?: Array<{ payload?: ChartRow }>;
}) {
  if (!active || !payload?.length) return null;
  const row = payload[0]?.payload;
  if (!row) return null;
  const segs: Array<{ range: string; rate: number; fill: string }> = [];
  for (let i = 0; i < 24; i++) {
    const dur = Number(row[`d${i}`] || 0);
    if (dur <= 0) continue;
    segs.push({
      range: `${row[`a${i}`]}–${row[`b${i}`]}`,
      rate: Number(row[`r${i}`]),
      fill: rateFill(Number(row[`r${i}`])),
    });
  }
  return (
    <div className="grid min-w-[11rem] gap-1.5 rounded-lg border border-border/50 bg-background px-2.5 py-1.5 text-xs shadow-xl">
      <div className="font-medium text-foreground">{row.day}</div>
      {segs.map((seg) => (
        <div key={seg.range} className="flex items-center gap-2">
          <span className="h-2.5 w-2.5 shrink-0 rounded-[2px]" style={{ backgroundColor: seg.fill }} />
          <span className="text-muted-foreground tabular-nums">{seg.range}</span>
          <span className="ml-auto font-mono font-medium tabular-nums text-foreground">
            {isFreeTimeMultiplier(seg.rate) ? '免费' : `${formatRate(seg.rate)} 倍`}
          </span>
        </div>
      ))}
    </div>
  );
}

const SegShape = (props: any) => {
  const { x, y, width, height, fill, payload, dataKey } = props;
  if (!width || width <= 0) return null;
  const idx = Number(String(dataKey).slice(1));
  const rate = Number(payload?.[`r${idx}`]);
  const label = !Number.isFinite(rate) ? '' : (isFreeTimeMultiplier(rate) ? '免费' : formatRate(rate));
  return (
    <g>
      <rect x={x} y={y} width={width} height={height} fill={fill} rx={width > 10 ? 3 : 0} />
      {width >= 36 && label ? (
        <text
          x={x + width / 2}
          y={y + height / 2}
          textAnchor="middle"
          dominantBaseline="middle"
          fontSize={10}
          fontFamily="ui-monospace, SFMono-Regular, Menlo, monospace"
          fill="var(--foreground)"
        >
          {label}
        </text>
      ) : null}
    </g>
  );
};

const TimeMultiplierChart: React.FC<{
  items: TimeMultiplierRule[];
  invert: boolean;
  remainder?: number | null;
  isLight: boolean;
  title?: string;
  description?: string;
}> = ({ items, invert, remainder, isLight, title, description }) => {
  const { data, maxSegs } = useMemo(() => {
    const rows: ChartRow[] = WEEKDAY_FULL.map((day, idx) => {
      const segs = buildDayMultiplierSegments(items, invert, idx + 1, remainder);
      const row: ChartRow = { day };
      segs.forEach((seg, i) => {
        row[`d${i}`] = seg.endMin - seg.startMin;
        row[`r${i}`] = seg.rate;
        row[`a${i}`] = formatClockMinutes(seg.startMin);
        row[`b${i}`] = formatClockMinutes(seg.endMin);
      });
      return row;
    });
    const max = Math.max(1, ...rows.map((row) => {
      let n = 0;
      while (row[`d${n}`] != null) n += 1;
      return n;
    }));
    return { data: rows, maxSegs: max };
  }, [items, invert, remainder]);

  const bars = Array.from({ length: maxSegs }, (_, i) => i);

  return (
    <div className={`mt-3 overflow-hidden rounded-xl border border-border shadow-sm ${isLight ? 'bg-[#fafafa]' : 'bg-[#141414]'}`}>
      <div className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div>
          <div className="text-sm font-semibold leading-none tracking-tight">{title || '一周倍率图'}</div>
          <p className="m-0 mt-1.5 text-xs text-muted-foreground">
            {description || '横轴 00:00–24:00。1.00 即上方默认价，其它色块是规划后的时段倍率。只取一档、不叠加。'}
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-3 text-[11px] text-muted-foreground">
          <span className="inline-flex items-center gap-1.5">
            <span className="h-2.5 w-2.5 rounded-[2px]" style={{ backgroundColor: rateFill(0.6) }} />
            &lt; 1.00
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span className="h-2.5 w-2.5 rounded-[2px]" style={{ backgroundColor: isLight ? '#e4e4e7' : '#3f3f46' }} />
            1.00 默认价
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span className="h-2.5 w-2.5 rounded-[2px]" style={{ backgroundColor: rateFill(2) }} />
            &gt; 1.00
          </span>
        </div>
      </div>
      <div className="h-[280px] w-full px-2 pt-2 pb-1">
        <ErrorBoundary fallbackTitle="倍率图渲染失败">
          <ResponsiveContainer width="100%" height="100%">
            <BarChart
              accessibilityLayer
              layout="vertical"
              data={data}
              margin={{ top: 4, right: 12, left: 4, bottom: 4 }}
              barCategoryGap={10}
            >
              <CartesianGrid horizontal={false} stroke="var(--border-custom, var(--border))" />
              <XAxis
                type="number"
                domain={[0, 1440]}
                ticks={[0, 360, 720, 1080, 1440]}
                tickFormatter={(v) => formatClockMinutes(Number(v))}
                tickLine={false}
                axisLine={false}
                tickMargin={6}
                tick={{ fontSize: 10, fill: 'var(--muted-foreground)' }}
              />
              <YAxis
                type="category"
                dataKey="day"
                width={40}
                tickLine={false}
                axisLine={false}
                tick={{ fontSize: 12, fill: 'var(--foreground)' }}
              />
              <Tooltip
                cursor={{ fill: isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.04)' }}
                content={(props) => (
                  <ScheduleTooltip
                    active={props.active}
                    payload={props.payload as unknown as Array<{ payload?: ChartRow }>}
                  />
                )}
              />
              {bars.map((i) => (
                <Bar
                  key={i}
                  dataKey={`d${i}`}
                  stackId="sch"
                  barSize={22}
                  isAnimationActive={false}
                  shape={SegShape}
                >
                  {data.map((row) => (
                    <Cell
                      key={`${row.day}-${i}`}
                      fill={rateFillSoft(Number(row[`r${i}`] ?? 1), isLight)}
                    />
                  ))}
                </Bar>
              ))}
            </BarChart>
          </ResponsiveContainer>
        </ErrorBoundary>
      </div>
    </div>
  );
};

export default TimeMultiplierChart;
