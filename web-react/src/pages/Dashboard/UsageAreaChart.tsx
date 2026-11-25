import React, { useEffect, useMemo, useState } from 'react';
import { Card, Select, Spin, Typography } from 'antd';
import { useTranslation } from 'react-i18next';
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';
import dayjs from 'dayjs';
import ErrorBoundary from '../../components/ErrorBoundary';
import useSettingsStore from '../../store/settings';
import request from '../../utils/request';
import { statsCalendarNow } from '../../utils/dateRangeParams';
import type { ModelTrend30dResponse } from '../../types';

const { Title, Text } = Typography;

const SERIES_LIMIT = 10;
const OTHER_MODEL = '__other__';
const SERIES_COLORS = [
  'hsl(217, 91%, 60%)',
  'hsl(199, 89%, 48%)',
  'hsl(221, 83%, 53%)',
  'hsl(210, 100%, 66%)',
  'hsl(226, 70%, 45%)',
  'hsl(205, 85%, 42%)',
  'hsl(230, 75%, 62%)',
  'hsl(195, 80%, 40%)',
  'hsl(214, 90%, 72%)',
  'hsl(222, 55%, 36%)',
  'hsl(200, 90%, 55%)',
  'hsl(235, 60%, 55%)',
  'hsl(190, 70%, 45%)',
  'hsl(215, 70%, 48%)',
  'hsl(208, 95%, 35%)',
];

type RangeKey = '90d' | '30d' | '7d';

type SeriesMeta = {
  key: string;
  label: string;
  color: string;
};

type ChartRow = Record<string, string | number>;

function rangeLength(range: RangeKey): number {
  if (range === '7d') return 7;
  if (range === '30d') return 30;
  return 90;
}

function axisDigits(max: number): number {
  if (max >= 100) return 0;
  if (max >= 1) return 2;
  if (max >= 0.01) return 4;
  return 6;
}

function formatAxisCost(value: number, symbol: string, digits: number): string {
  const abs = Math.abs(value);
  if (abs >= 1_000_000) return `${symbol}${(value / 1_000_000).toFixed(1)}M`;
  if (abs >= 10_000) return `${symbol}${(value / 1_000).toFixed(1)}K`;
  return `${symbol}${value.toFixed(digits)}`;
}

function axisTickCeiling(max: number): number {
  if (max <= 0) return 0;
  const exp = 10 ** Math.floor(Math.log10(max));
  const n = max / exp;
  const nice = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
  return nice * exp;
}

function axisWidth(label: string): number {
  return Math.ceil(label.length * 8) + 12;
}

function formatCost(value: number, symbol: string): string {
  return `${symbol}${value.toFixed(6)}`;
}

function buildUsageArea(
  data: ModelTrend30dResponse | null,
  range: RangeKey,
  otherLabel: string,
): { rows: ChartRow[]; series: SeriesMeta[] } {
  const n = rangeLength(range);
  const today = statsCalendarNow().startOf('day');
  const dates: string[] = [];
  for (let i = n - 1; i >= 0; i -= 1) {
    dates.push(today.subtract(i, 'day').format('YYYY-MM-DD'));
  }
  const dateSet = new Set(dates);
  const byModel = new Map<string, Map<string, number>>();
  for (const item of data?.daily_data ?? []) {
    if (!dateSet.has(item.date)) continue;
    let dayMap = byModel.get(item.model);
    if (!dayMap) {
      dayMap = new Map();
      byModel.set(item.model, dayMap);
    }
    dayMap.set(item.date, (dayMap.get(item.date) || 0) + (item.total_cost || 0));
  }

  const ranked = [...byModel.entries()]
    .map(([model, dayMap]) => ({
      model,
      total: [...dayMap.values()].reduce((sum, value) => sum + value, 0),
    }))
    .filter((item) => item.total > 0)
    .sort((a, b) => b.total - a.total);

  const other = ranked.find((item) => item.model === OTHER_MODEL);
  const top = ranked.filter((item) => item.model !== OTHER_MODEL).slice(0, SERIES_LIMIT);
  if (other) top.push(other);
  const series: SeriesMeta[] = top.map((item, index) => ({
    key: `m${index}`,
    label: item.model === OTHER_MODEL ? otherLabel : item.model,
    color: SERIES_COLORS[index % SERIES_COLORS.length],
  }));

  const rows = dates.map((date) => {
    const row: ChartRow = { date };
    top.forEach((item, index) => {
      row[`m${index}`] = byModel.get(item.model)?.get(date) || 0;
    });
    return row;
  });

  return { rows, series };
}

function UsageTooltip({
  active,
  payload,
  label,
  series,
  totalLabel,
  currencySymbol,
}: {
  active?: boolean;
  payload?: readonly { payload?: ChartRow }[];
  label?: string;
  series: SeriesMeta[];
  totalLabel: string;
  currencySymbol: string;
}) {
  if (!active || !payload?.length) return null;
  const row = payload[0]?.payload;
  if (!row) return null;
  const items = series
    .map((item) => ({
      ...item,
      value: Number(row[item.key] || 0),
    }))
    .filter((item) => item.value > 0);
  if (items.length === 0) return null;
  const total = items.reduce((sum, item) => sum + item.value, 0);
  return (
    <div className="grid min-w-[10rem] gap-1 rounded-md border border-border/50 bg-background px-2 py-1 text-[11px] leading-tight shadow-xl">
      <div className="font-medium text-foreground">{dayjs(String(label)).format('YYYY-MM-DD')}</div>
      {items.map((item) => {
        const pct = total > 0 ? (item.value / total) * 100 : 0;
        return (
          <div key={item.key} className="flex items-center gap-1.5">
            <span className="h-1.5 w-1.5 shrink-0 rounded-[2px]" style={{ backgroundColor: item.color }} />
            <span className="max-w-[8rem] truncate text-muted-foreground">{item.label}</span>
            <span className="ml-auto font-medium tabular-nums text-foreground">
              {formatCost(item.value, currencySymbol)}
              <span className="ml-1 font-normal text-muted-foreground">{pct.toFixed(1)}%</span>
            </span>
          </div>
        );
      })}
      <div className="flex items-center gap-1.5 border-t border-border/60 pt-1">
        <span className="text-muted-foreground">{totalLabel}</span>
        <span className="ml-auto font-medium tabular-nums text-foreground">{formatCost(total, currencySymbol)}</span>
      </div>
    </div>
  );
}

const UsageAreaChart: React.FC = () => {
  const { t } = useTranslation();
  const currencySymbol = useSettingsStore((s) => s.settings?.currency?.currency_symbol) || '$';
  const [timeRange, setTimeRange] = useState<RangeKey>('30d');
  const [data, setData] = useState<ModelTrend30dResponse | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    request
      .get<ModelTrend30dResponse>('/dashboard/models_30d', {
        params: { days: rangeLength(timeRange), limit: SERIES_LIMIT, include_other: true },
      })
      .then((res) => {
        if (!cancelled) setData(res as unknown as ModelTrend30dResponse);
      })
      .catch((error) => {
        console.error(error);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [timeRange]);

  const totalLabel = t('dashboard.usage_area_total', '当日合计');
  const otherLabel = t('dashboard.usage_area_other', '其他');
  const rangeLabel = t(`dashboard.usage_area_range_${timeRange}`, {
    '90d': '最近 3 个月',
    '30d': '最近 30 天',
    '7d': '最近 7 天',
  }[timeRange]);

  const { rows, series } = useMemo(
    () => buildUsageArea(data, timeRange, otherLabel),
    [data, timeRange, otherLabel],
  );

  const hasSeries = series.length > 0;
  const yMax = rows.reduce((max, row) => {
    const total = series.reduce((sum, item) => sum + Number(row[item.key] || 0), 0);
    return Math.max(max, total);
  }, 0);
  const yDigits = axisDigits(yMax);
  const yAxisWidth = axisWidth(formatAxisCost(axisTickCeiling(yMax), currencySymbol, yDigits));

  return (
    <ErrorBoundary>
      <Card
        variant="borderless"
        style={{ borderRadius: 12, marginTop: 24 }}
        styles={{ body: { padding: 0 } }}
      >
        <div className="flex flex-col gap-3 border-b border-border px-4 py-4 sm:flex-row sm:items-center sm:px-6 sm:py-5">
          <div className="grid min-w-0 flex-1 gap-1">
            <Title level={5} style={{ margin: 0, fontWeight: 500 }}>
              {t('dashboard.usage_area_title', '用量趋势')}
            </Title>
            <Text type="secondary" style={{ fontSize: 13 }}>
              {t('dashboard.usage_area_desc', { range: rangeLabel, defaultValue: `展示${rangeLabel}各模型消费金额` })}
            </Text>
          </div>
          <div className="w-full sm:w-[160px]">
            <Select
              value={timeRange}
              aria-label={t('dashboard.usage_area_title', '用量趋势')}
              onChange={(value) => setTimeRange(value)}
              style={{ width: '100%' }}
              options={[
                { value: '90d', label: t('dashboard.usage_area_range_90d', '最近 3 个月') },
                { value: '30d', label: t('dashboard.usage_area_range_30d', '最近 30 天') },
                { value: '7d', label: t('dashboard.usage_area_range_7d', '最近 7 天') },
              ]}
            />
          </div>
        </div>
        <div className="px-2 pt-4 pb-4 sm:px-6 sm:pt-6">
          <Spin spinning={loading}>
            {hasSeries ? (
              <div className="h-[250px] w-full [&_.recharts-responsive-container]:outline-none [&_.recharts-surface]:outline-none [&_.recharts-wrapper]:outline-none [&_.recharts-wrapper:focus]:outline-none [&_.recharts-wrapper:focus-visible]:outline-none">
                <ResponsiveContainer width="100%" height="100%">
                  <AreaChart data={rows} margin={{ left: 4, right: 12, top: 8 }} accessibilityLayer={false} style={{ outline: 'none' }}>
                    <defs>
                      {series.map((item) => (
                        <linearGradient key={item.key} id={`usageFill-${item.key}`} x1="0" y1="0" x2="0" y2="1">
                          <stop offset="5%" stopColor={item.color} stopOpacity={0.8} />
                          <stop offset="95%" stopColor={item.color} stopOpacity={0.1} />
                        </linearGradient>
                      ))}
                    </defs>
                    <CartesianGrid vertical={false} stroke="var(--border-custom)" />
                    <YAxis
                      domain={[0, 'auto']}
                      allowDataOverflow={false}
                      tickLine={false}
                      axisLine={false}
                      tickMargin={8}
                      width={yAxisWidth}
                      tick={{ fontSize: 12, fill: 'var(--muted-foreground)' }}
                      tickFormatter={(value: number) => formatAxisCost(value, currencySymbol, yDigits)}
                    />
                    <XAxis
                      dataKey="date"
                      tickLine={false}
                      axisLine={false}
                      tickMargin={8}
                      minTickGap={32}
                      tick={{ fontSize: 12, fill: 'var(--muted-foreground)' }}
                      tickFormatter={(value: string) => dayjs(value).format('MM-DD')}
                    />
                    <Tooltip
                      cursor={false}
                      content={(props) => (
                        <UsageTooltip
                          active={props.active}
                          payload={props.payload as readonly { payload?: ChartRow }[] | undefined}
                          label={props.label as string | undefined}
                          series={series}
                          totalLabel={totalLabel}
                          currencySymbol={currencySymbol}
                        />
                      )}
                    />
                    {series.map((item) => (
                      <Area
                        key={item.key}
                        dataKey={item.key}
                        name={item.label}
                        type="monotone"
                        fill={`url(#usageFill-${item.key})`}
                        stroke={item.color}
                        strokeWidth={1}
                        stackId="usage"
                      />
                    ))}
                  </AreaChart>
                </ResponsiveContainer>
              </div>
            ) : (
              <div className="flex h-[250px] items-center justify-center text-muted-foreground">
                {loading ? null : t('dashboard.no_data')}
              </div>
            )}
            {hasSeries && (
              <div className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 pt-2 text-[11px] leading-none">
                {series.map((item) => (
                  <span key={item.key} className="inline-flex max-w-full items-center gap-1 text-muted-foreground">
                    <span className="h-1.5 w-1.5 shrink-0 rounded-[2px]" style={{ backgroundColor: item.color }} />
                    <span className="truncate">{item.label}</span>
                  </span>
                ))}
              </div>
            )}
          </Spin>
        </div>
      </Card>
    </ErrorBoundary>
  );
};

export default UsageAreaChart;
