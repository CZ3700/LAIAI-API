import React, { useEffect, useMemo, useState } from 'react';
import { Button, InputNumber, Spin, Typography } from 'antd';
import { ArrowLeftOutlined } from '@ant-design/icons';
import { Check, TrendingDown, TrendingUp, X } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import dayjs from 'dayjs';
import request from '../../utils/request';
import { statsCalendarNow } from '../../utils/dateRangeParams';
import type { ModelTrend30dResponse } from '../../types';

const { Title, Text } = Typography;

const CHART_COLORS = [
  'var(--chart-1)',
  'var(--chart-2)',
  'var(--chart-3)',
  'var(--chart-4)',
  'var(--chart-5)',
];

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
  'hsl(212, 80%, 52%)',
];

type DayPoint = { date: string; cost: number };
type OverviewPoint = { date: string; requests: number; cost: number };
type OverviewKey = 'requests' | 'cost';
type SeriesMeta = { key: string; label: string; color: string };
type ChartRow = Record<string, string | number>;

function formatCost(value: number, symbol: string): string {
  return `${symbol}${value.toFixed(6)}`;
}

function buildSelectedUsageArea(
  data: ModelTrend30dResponse | null,
  days: number,
  selectedModels: string[],
): { rows: ChartRow[]; series: SeriesMeta[] } {
  const n = Math.min(90, Math.max(1, Math.round(days) || 30));
  const today = statsCalendarNow().startOf('day');
  const dates: string[] = [];
  for (let i = n - 1; i >= 0; i -= 1) {
    dates.push(today.subtract(i, 'day').format('YYYY-MM-DD'));
  }
  const dateSet = new Set(dates);
  const selectedSet = new Set(selectedModels);
  const byModel = new Map<string, Map<string, number>>();
  for (const model of selectedModels) {
    byModel.set(model, new Map());
  }
  for (const item of data?.daily_data ?? []) {
    if (!dateSet.has(item.date) || !selectedSet.has(item.model)) continue;
    let dayMap = byModel.get(item.model);
    if (!dayMap) {
      dayMap = new Map();
      byModel.set(item.model, dayMap);
    }
    dayMap.set(item.date, (dayMap.get(item.date) || 0) + (item.total_cost || 0));
  }

  const ranked = selectedModels
    .map((model) => {
      const dayMap = byModel.get(model);
      const total = dayMap ? [...dayMap.values()].reduce((sum, value) => sum + value, 0) : 0;
      return { model, total };
    })
    .sort((a, b) => b.total - a.total);

  const series: SeriesMeta[] = ranked.map((item, index) => ({
    key: `m${index}`,
    label: item.model,
    color: SERIES_COLORS[index % SERIES_COLORS.length],
  }));

  const rows = dates.map((date) => {
    const row: ChartRow = { date };
    ranked.forEach((item, index) => {
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

function emptyDayMap(): Map<string, { requests: number; cost: number }> {
  const dataMap = new Map<string, { requests: number; cost: number }>();
  for (let i = 29; i >= 0; i--) {
    dataMap.set(dayjs().subtract(i, 'day').format('YYYY-MM-DD'), { requests: 0, cost: 0 });
  }
  return dataMap;
}

function buildOverviewSeries(data: ModelTrend30dResponse | null): OverviewPoint[] {
  const dataMap = emptyDayMap();
  if (data?.daily_data && Array.isArray(data.daily_data)) {
    data.daily_data.forEach((item) => {
      const prev = dataMap.get(item.date) || { requests: 0, cost: 0 };
      prev.requests += item.count || 0;
      prev.cost += item.total_cost || 0;
      dataMap.set(item.date, prev);
    });
  }
  return Array.from(dataMap.entries())
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([date, v]) => ({
      date: date.slice(5),
      requests: v.requests,
      cost: parseFloat(v.cost.toFixed(6)),
    }));
}

function buildDailySeries(data: ModelTrend30dResponse | null, model: string): DayPoint[] {
  const dataMap = new Map<string, number>();
  for (let i = 29; i >= 0; i--) {
    dataMap.set(dayjs().subtract(i, 'day').format('YYYY-MM-DD'), 0);
  }
  if (data?.daily_data && Array.isArray(data.daily_data)) {
    data.daily_data.forEach((item) => {
      if (item.model !== model) return;
      dataMap.set(item.date, parseFloat(item.total_cost.toFixed(6)));
    });
  }
  return Array.from(dataMap.entries())
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([date, cost]) => ({ date: date.slice(5), cost }));
}

function weekTrendPercent(series: DayPoint[]): number | null {
  if (series.length < 14) return null;
  const last = series.slice(-7).reduce((sum, d) => sum + d.cost, 0);
  const prev = series.slice(-14, -7).reduce((sum, d) => sum + d.cost, 0);
  if (prev === 0) return last > 0 ? 100 : 0;
  return ((last - prev) / prev) * 100;
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

function formatAxisRequests(value: number): string {
  const abs = Math.abs(value);
  if (abs >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (abs >= 10_000) return `${(value / 1_000).toFixed(1)}K`;
  return Math.round(value).toLocaleString();
}

function axisTickCeiling(max: number): number {
  if (max <= 0) return 0;
  const exp = 10 ** Math.floor(Math.log10(max));
  const n = max / exp;
  const nice = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
  return nice * exp;
}

function measureAxisWidth(max: number, format: (value: number) => string): number {
  const ceil = axisTickCeiling(max);
  const candidates = [
    0,
    max,
    ceil,
    max * 0.25,
    max * 0.5,
    max * 0.75,
    ceil * 0.25,
    ceil * 0.5,
    ceil * 0.75,
  ];
  const maxLen = candidates.reduce((len, v) => Math.max(len, format(v).length), 0);
  return Math.max(40, Math.ceil(maxLen * 7.5) + 14);
}

const DIGITS = [0, 1, 2, 3, 4, 5, 6, 7, 8, 9];

const RollingDigit: React.FC<{ digit: number; delayMs: number }> = ({ digit, delayMs }) => {
  const [activeDigit, setActiveDigit] = useState(0);

  useEffect(() => {
    const raf = window.requestAnimationFrame(() => {
      setActiveDigit(digit);
    });
    return () => window.cancelAnimationFrame(raf);
  }, [digit]);

  return (
    <span className="relative inline-block h-[1.25em] overflow-hidden align-bottom leading-[1.25em]">
      <span className="invisible select-none">{digit}</span>
      <span
        className="absolute inset-x-0 top-0 flex flex-col items-center will-change-transform"
        style={{
          transform: `translate3d(0, -${activeDigit * 10}%, 0)`,
          transition: `transform 520ms cubic-bezier(0.16, 1, 0.3, 1) ${delayMs}ms`,
        }}
      >
        {DIGITS.map((n) => (
          <span key={n} className="h-[1.25em] leading-[1.25em]">
            {n}
          </span>
        ))}
      </span>
    </span>
  );
};

const RollingCost: React.FC<{ symbol: string; value: number }> = ({ symbol, value }) => {
  const text = String(parseFloat(value.toFixed(6)));
  const [intPart, fracPart] = text.split('.');
  const intChars = intPart.split('');
  const fracChars = fracPart ? fracPart.split('') : [];

  return (
    <span className="ml-2 inline-flex items-baseline font-medium tabular-nums text-foreground">
      <span className="inline-block h-[1.25em] leading-[1.25em]">{symbol}</span>
      {intChars.map((ch, idx) => {
        const place = intChars.length - 1 - idx;
        const code = ch.charCodeAt(0) - 48;
        if (code >= 0 && code <= 9) {
          return <RollingDigit key={`int-${place}`} digit={code} delayMs={idx * 16} />;
        }
        return (
          <span key={`int-sep-${place}`} className="inline-block h-[1.25em] leading-[1.25em]">
            {ch}
          </span>
        );
      })}
      {fracChars.length > 0 && (
        <>
          <span className="inline-block h-[1.25em] leading-[1.25em]">.</span>
          {fracChars.map((ch, idx) => {
            const code = ch.charCodeAt(0) - 48;
            return (
              <RollingDigit
                key={`frac-${idx}`}
                digit={code >= 0 && code <= 9 ? code : 0}
                delayMs={(intChars.length + idx) * 16}
              />
            );
          })}
        </>
      )}
    </span>
  );
};

function ChartTooltipBox({
  active,
  payload,
  label,
  valueLabel,
  formatValue,
}: {
  active?: boolean;
  payload?: Array<{ value?: number; color?: string; fill?: string }>;
  label?: string;
  valueLabel: string;
  formatValue: (value: number) => string;
}) {
  if (!active || !payload?.length) return null;
  const item = payload[0];
  const value = Number(item.value ?? 0);
  return (
    <div className="grid min-w-[9rem] gap-1.5 rounded-lg border border-border/50 bg-background px-2.5 py-1.5 text-xs shadow-xl">
      <div className="font-medium text-foreground">{label}</div>
      <div className="flex items-center gap-2">
        <span
          className="h-2.5 w-2.5 shrink-0 rounded-[2px]"
          style={{ backgroundColor: item.fill || item.color }}
        />
        <span className="text-muted-foreground">{valueLabel}</span>
        <span className="ml-auto font-medium tabular-nums text-foreground">
          {formatValue(value)}
        </span>
      </div>
    </div>
  );
}

const ModelDistribution30d: React.FC<{
  data: ModelTrend30dResponse | null;
  loading: boolean;
  currencySymbol: string;
  onBack: () => void;
}> = ({ data, loading, currencySymbol, onBack }) => {
  const { t } = useTranslation();
  const costLabel = t('dashboard.estimated_cost');
  const requestsLabel = t('dashboard.total_requests');
  const models = useMemo(
    () => data?.top_models?.filter((m) => m.count > 0).slice(0, 16),
    [data?.top_models],
  );
  const [activeOverview, setActiveOverview] = useState<OverviewKey>('requests');
  const [selectedModels, setSelectedModels] = useState<string[]>([]);
  const [rangeDays, setRangeDays] = useState<number>(30);
  const [data90d, setData90d] = useState<ModelTrend30dResponse | null>(null);
  const [loading90d, setLoading90d] = useState(false);

  useEffect(() => {
    if (selectedModels.length === 0) {
      setRangeDays(30);
    }
  }, [selectedModels.length]);

  useEffect(() => {
    if (selectedModels.length === 0 || rangeDays <= 30 || data90d) return undefined;
    let cancelled = false;
    setLoading90d(true);
    request
      .get<ModelTrend30dResponse>('/dashboard/models_30d', {
        params: { days: 90, limit: 16 },
      })
      .then((res) => {
        if (!cancelled) setData90d(res as unknown as ModelTrend30dResponse);
      })
      .catch((error) => {
        console.error(error);
      })
      .finally(() => {
        if (!cancelled) setLoading90d(false);
      });
    return () => {
      cancelled = true;
    };
  }, [selectedModels.length, rangeDays, data90d]);

  const toggleModel = (model: string) => {
    setSelectedModels((prev) =>
      prev.includes(model) ? prev.filter((item) => item !== model) : [...prev, model],
    );
  };

  const totalLabel = t('dashboard.usage_area_total', '当日合计');
  const rangeLabel =
    rangeDays === 7
      ? t('dashboard.usage_area_range_7d', '最近 7 天')
      : rangeDays === 30
        ? t('dashboard.usage_area_range_30d', '最近 30 天')
        : rangeDays === 90
          ? t('dashboard.usage_area_range_90d', '最近 3 个月')
          : t('dashboard.usage_area_range_custom', {
              days: rangeDays,
              defaultValue: `最近 ${rangeDays} 天`,
            });

  const areaSourceData = rangeDays > 30 ? (data90d ?? data) : data;
  const { rows: areaRows, series: areaSeries } = useMemo(
    () => buildSelectedUsageArea(areaSourceData, rangeDays, selectedModels),
    [areaSourceData, rangeDays, selectedModels],
  );
  const areaYMax = useMemo(
    () =>
      areaRows.reduce((max, row) => {
        const total = areaSeries.reduce((sum, item) => sum + Number(row[item.key] || 0), 0);
        return Math.max(max, total);
      }, 0),
    [areaRows, areaSeries],
  );
  const areaYDigits = axisDigits(areaYMax);
  const areaYAxisWidth = measureAxisWidth(areaYMax, (v) =>
    formatAxisCost(v, currencySymbol, areaYDigits),
  );
  const areaTotalCost = useMemo(
    () =>
      areaRows.reduce(
        (sum, row) =>
          sum + areaSeries.reduce((rowSum, item) => rowSum + Number(row[item.key] || 0), 0),
        0,
      ),
    [areaRows, areaSeries],
  );

  const overviewSeries = useMemo(() => buildOverviewSeries(data), [data]);
  const overviewTotals = useMemo(() => ({
    requests: overviewSeries.reduce((sum, d) => sum + d.requests, 0),
    cost: overviewSeries.reduce((sum, d) => sum + d.cost, 0),
  }), [overviewSeries]);

  const overviewCostMax = useMemo(
    () => overviewSeries.reduce((max, d) => Math.max(max, d.cost), 0),
    [overviewSeries],
  );
  const overviewRequestsMax = useMemo(
    () => overviewSeries.reduce((max, d) => Math.max(max, d.requests), 0),
    [overviewSeries],
  );
  const overviewCostDigits = axisDigits(overviewCostMax);
  const overviewAxisWidth = activeOverview === 'cost'
    ? measureAxisWidth(overviewCostMax, (v) =>
        formatAxisCost(v, currencySymbol, overviewCostDigits),
      )
    : measureAxisWidth(overviewRequestsMax, formatAxisRequests);

  const seriesByModel = useMemo(() => {
    if (!models?.length) return {};
    const map: Record<string, DayPoint[]> = {};
    models.forEach((m) => {
      map[m.model] = buildDailySeries(data, m.model);
    });
    return map;
  }, [data, models]);

  return (
    <div style={{ animation: 'fadeIn 0.3s' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 24 }}>
        <Button icon={<ArrowLeftOutlined />} onClick={onBack}>{t('common.back', '返回')}</Button>
        <Title level={5} style={{ margin: 0, fontSize: 16, whiteSpace: 'nowrap' }}>
          {t('dashboard.model_distribution_30d', '模型分布 (按成本) - 最近 30 天趋势')}
        </Title>
      </div>

      {models && Array.isArray(models) && models.length > 0 ? (
        <>
        {selectedModels.length > 0 ? (
          <div className="mb-4 flex flex-col rounded-xl border border-border bg-[#fafafa] text-card-foreground shadow-sm dark:bg-[#141414]">
            <div className="flex flex-col gap-3 border-b border-border px-4 py-4 sm:flex-row sm:items-center sm:px-6 sm:py-5">
              <div className="grid min-w-0 flex-1 gap-1">
                <Title level={5} style={{ margin: 0, fontWeight: 500 }}>
                  {t('dashboard.usage_area_title', '用量趋势')}
                </Title>
                <Text type="secondary" style={{ fontSize: 13 }}>
                  {t('dashboard.usage_area_desc', { range: rangeLabel, defaultValue: `展示${rangeLabel}各模型消费金额` })}
                  <RollingCost symbol={currencySymbol} value={areaTotalCost} />
                </Text>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                {([
                  { days: 7, label: t('dashboard.usage_area_range_7d', '最近 7 天') },
                  { days: 30, label: t('dashboard.usage_area_range_30d', '最近 30 天') },
                  { days: 90, label: t('dashboard.usage_area_range_90d', '最近 3 个月') },
                ] as const).map((item) => (
                  <Button
                    key={item.days}
                    type={rangeDays === item.days ? 'primary' : 'default'}
                    onClick={() => setRangeDays(item.days)}
                  >
                    {item.label}
                  </Button>
                ))}
                <InputNumber
                  min={1}
                  max={90}
                  precision={0}
                  value={rangeDays}
                  aria-label={t('dashboard.usage_area_title', '用量趋势')}
                  suffix={t('common.days', '天')}
                  onChange={(value) => {
                    if (value === null || value === undefined) {
                      setRangeDays(30);
                      return;
                    }
                    const num = Number(value);
                    if (Number.isFinite(num) && num >= 1) {
                      setRangeDays(Math.min(90, Math.max(1, Math.round(num))));
                    }
                  }}
                  style={{ width: 110 }}
                />
              </div>
            </div>
            <div className="px-2 pt-4 pb-4 sm:px-6 sm:pt-6">
              <Spin spinning={loading || (rangeDays > 30 && loading90d)}>
                <div className="h-[250px] w-full [&_.recharts-responsive-container]:outline-none [&_.recharts-surface]:outline-none [&_.recharts-wrapper]:outline-none [&_.recharts-wrapper:focus]:outline-none [&_.recharts-wrapper:focus-visible]:outline-none">
                  <ResponsiveContainer width="100%" height="100%">
                    <AreaChart data={areaRows} margin={{ left: 8, right: 12, top: 8 }} accessibilityLayer={false} style={{ outline: 'none' }}>
                      <defs>
                        {areaSeries.map((item) => (
                          <linearGradient key={item.key} id={`modelDistUsageFill-${item.key}`} x1="0" y1="0" x2="0" y2="1">
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
                        width={areaYAxisWidth}
                        tick={{ fontSize: 12, fill: 'var(--muted-foreground)' }}
                        tickFormatter={(value: number) => formatAxisCost(value, currencySymbol, areaYDigits)}
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
                            series={areaSeries}
                            totalLabel={totalLabel}
                            currencySymbol={currencySymbol}
                          />
                        )}
                      />
                      {areaSeries.map((item) => (
                        <Area
                          key={item.key}
                          dataKey={item.key}
                          name={item.label}
                          type="monotone"
                          fill={`url(#modelDistUsageFill-${item.key})`}
                          stroke={item.color}
                          strokeWidth={1}
                          stackId="usage"
                        />
                      ))}
                    </AreaChart>
                  </ResponsiveContainer>
                </div>
                <div className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1.5 pt-2 text-[11px] leading-none">
                  {areaSeries.map((item) => (
                    <span key={item.key} className="inline-flex max-w-full items-center gap-1 text-muted-foreground">
                      <span className="h-1.5 w-1.5 shrink-0 rounded-[2px]" style={{ backgroundColor: item.color }} />
                      <span className="truncate">{item.label}</span>
                      <button
                        type="button"
                        aria-label={`${t('common.delete', '删除')} ${item.label}`}
                        onClick={() => toggleModel(item.label)}
                        className="inline-flex shrink-0 cursor-pointer items-center rounded p-0.5 text-muted-foreground/70 transition-colors hover:bg-muted hover:text-foreground"
                      >
                        <X className="size-3" />
                      </button>
                    </span>
                  ))}
                  <button
                    type="button"
                    onClick={() => setSelectedModels(models.map((m) => m.model))}
                    className="inline-flex shrink-0 cursor-pointer items-center gap-0.5 rounded border border-border/70 px-1.5 py-0.5 text-[11px] leading-none text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                  >
                    <Check className="size-3" />
                    <span>{t('common.select_all', '全选')}</span>
                  </button>
                  <button
                    type="button"
                    onClick={() => setSelectedModels([])}
                    className="inline-flex shrink-0 cursor-pointer items-center gap-0.5 rounded border border-border/70 px-1.5 py-0.5 text-[11px] leading-none text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                  >
                    <X className="size-3" />
                    <span>{t('common.delete_all', '全部删除')}</span>
                  </button>
                </div>
              </Spin>
            </div>
          </div>
        ) : (
        <div className="mb-4 flex flex-col rounded-xl border border-border bg-[#fafafa] text-card-foreground shadow-sm dark:bg-[#141414]">
          <div className="flex flex-col items-stretch border-b border-border sm:flex-row">
            <div className="flex flex-1 flex-col justify-center gap-1 px-6 py-5">
              <div className="text-base font-semibold leading-none tracking-tight">
                {t('dashboard.trend_30d_title', '最近 30 天数据趋势')}
              </div>
              <p className="m-0 text-sm text-muted-foreground">
                {t('dashboard.trend_30d_desc', '按请求次数与预估成本查看最近 30 天消耗')}
              </p>
            </div>
            <div className="flex">
              {([
                { key: 'requests' as const, label: requestsLabel, value: overviewTotals.requests.toLocaleString() },
                { key: 'cost' as const, label: costLabel, value: `${currencySymbol}${overviewTotals.cost.toFixed(3)}` },
              ]).map((item) => (
                <button
                  key={item.key}
                  type="button"
                  data-active={activeOverview === item.key}
                  className="relative flex flex-1 flex-col justify-center gap-1 border-t border-border px-6 py-4 text-left even:border-l sm:border-t-0 sm:px-8 sm:py-6 data-[active=true]:bg-muted/50"
                  onClick={() => setActiveOverview(item.key)}
                >
                  <span className="text-xs text-muted-foreground">{item.label}</span>
                  <span className="text-lg font-bold leading-none sm:text-3xl">{item.value}</span>
                </button>
              ))}
            </div>
          </div>
          <div className="px-2 pt-4 pb-4 sm:p-6">
            <div className="h-[250px] w-full [&_.recharts-responsive-container]:outline-none [&_.recharts-surface]:outline-none [&_.recharts-wrapper]:outline-none [&_.recharts-wrapper:focus]:outline-none [&_.recharts-wrapper:focus-visible]:outline-none">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart accessibilityLayer={false} data={overviewSeries} margin={{ left: 8, right: 12, top: 8 }} style={{ outline: 'none' }}>
                  <CartesianGrid vertical={false} stroke="var(--border-custom)" />
                  <YAxis
                    domain={[0, 'auto']}
                    allowDataOverflow={false}
                    allowDecimals={activeOverview === 'cost'}
                    tickLine={false}
                    axisLine={false}
                    tickMargin={8}
                    width={overviewAxisWidth}
                    tick={{ fontSize: 10, fill: 'var(--muted-foreground)' }}
                    tickFormatter={(value: number) =>
                      activeOverview === 'cost'
                        ? formatAxisCost(value, currencySymbol, overviewCostDigits)
                        : formatAxisRequests(value)
                    }
                  />
                  <XAxis
                    dataKey="date"
                    tickLine={false}
                    axisLine={false}
                    tickMargin={8}
                    minTickGap={24}
                    tick={{ fontSize: 10, fill: 'var(--muted-foreground)' }}
                  />
                  <Tooltip
                    cursor={false}
                    content={(props) => (
                      <ChartTooltipBox
                        active={props.active}
                        payload={props.payload as unknown as Array<{ value?: number; color?: string; fill?: string }>}
                        label={props.label as string | undefined}
                        valueLabel={activeOverview === 'cost' ? costLabel : requestsLabel}
                        formatValue={(v) =>
                          activeOverview === 'cost'
                            ? `${currencySymbol}${parseFloat(v.toFixed(6))}`
                            : Math.round(v).toLocaleString()
                        }
                      />
                    )}
                  />
                  <Bar
                    dataKey={activeOverview}
                    fill={activeOverview === 'cost' ? 'var(--chart-2)' : 'var(--chart-1)'}
                    radius={4}
                  />
                </BarChart>
              </ResponsiveContainer>
            </div>
          </div>
        </div>
        )}

        <div
          className={`grid gap-4 ${
            models.length === 1
              ? 'grid-cols-1'
              : models.length === 2
                ? 'grid-cols-1 lg:grid-cols-2'
                : 'grid-cols-1 lg:grid-cols-2 xl:grid-cols-4'
          }`}
        >
          {models.map((m, index) => {
            const fill = CHART_COLORS[index % CHART_COLORS.length];
            const series = seriesByModel[m.model] || [];
            const trend = weekTrendPercent(series);
            const up = trend != null && trend >= 0;
            const modelMax = series.reduce((max, d) => Math.max(max, d.cost), 0);
            const modelDigits = axisDigits(modelMax);
            const modelAxisWidth = measureAxisWidth(modelMax, (v) =>
              formatAxisCost(v, currencySymbol, modelDigits),
            );
            const isSelected = selectedModels.includes(m.model);
            return (
              <div
                key={m.model}
                role="button"
                tabIndex={0}
                aria-pressed={isSelected}
                data-selected={isSelected}
                onClick={() => toggleModel(m.model)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    toggleModel(m.model);
                  }
                }}
                className="flex cursor-pointer flex-col rounded-xl border border-border bg-[#fafafa] text-card-foreground shadow-sm outline-none transition-colors hover:border-foreground/30 focus:outline-none focus-visible:outline-none data-[selected=true]:border-foreground/70 data-[selected=true]:ring-1 data-[selected=true]:ring-foreground/50 dark:bg-[#141414]"
              >
                <div className="flex flex-col px-6 pt-4">
                  <div className="truncate text-sm font-semibold leading-none tracking-tight">
                    {m.model}
                  </div>
                </div>
                <div className="px-3 pt-2 pb-0">
                  <div className="h-[180px] w-full [&_.recharts-responsive-container]:outline-none [&_.recharts-surface]:outline-none [&_.recharts-wrapper]:outline-none [&_.recharts-wrapper:focus]:outline-none [&_.recharts-wrapper:focus-visible]:outline-none">
                    <ResponsiveContainer width="100%" height="100%">
                      <BarChart accessibilityLayer={false} data={series} margin={{ top: 8, left: 4, right: 12, bottom: 0 }} style={{ outline: 'none' }}>
                        <CartesianGrid vertical={false} stroke="var(--border-custom)" />
                        <YAxis
                          domain={[0, 'auto']}
                          allowDataOverflow={false}
                          interval={0}
                          tickLine={false}
                          axisLine={false}
                          tickSize={0}
                          tickMargin={6}
                          width={modelAxisWidth}
                          tick={{ fontSize: 11, fill: 'var(--muted-foreground)' }}
                          tickFormatter={(value: number) => formatAxisCost(value, currencySymbol, modelDigits)}
                        />
                        <XAxis
                          dataKey="date"
                          tickLine={false}
                          axisLine={false}
                          tickMargin={8}
                          minTickGap={16}
                          tick={{ fontSize: 11, fill: 'var(--muted-foreground)' }}
                        />
                        <Tooltip
                          cursor={false}
                          content={(props) => (
                            <ChartTooltipBox
                              active={props.active}
                              payload={props.payload as unknown as Array<{ value?: number; color?: string; fill?: string }>}
                              label={props.label as string | undefined}
                              valueLabel={costLabel}
                              formatValue={(v) => `${currencySymbol}${parseFloat(v.toFixed(6))}`}
                            />
                          )}
                        />
                        <Bar dataKey="cost" fill={fill} radius={4} maxBarSize={28} />
                      </BarChart>
                    </ResponsiveContainer>
                  </div>
                </div>
                <div className="flex flex-col items-center justify-center gap-1 px-6 pt-2.5 pb-4 text-center text-[11px]">
                  {trend != null && Math.abs(trend) >= 0.05 && (
                    <div className="flex items-center gap-1.5 font-medium leading-none">
                      {up
                        ? t('dashboard.cost_trend_up', { pct: Math.abs(trend).toFixed(1), defaultValue: `近 7 日成本上升 ${Math.abs(trend).toFixed(1)}%` })
                        : t('dashboard.cost_trend_down', { pct: Math.abs(trend).toFixed(1), defaultValue: `近 7 日成本下降 ${Math.abs(trend).toFixed(1)}%` })}
                      {up ? <TrendingUp className="size-3" /> : <TrendingDown className="size-3" />}
                    </div>
                  )}
                  <div className="text-muted-foreground leading-none">
                    {t('dashboard.total_spend', '总花费')} {currencySymbol}{parseFloat(Number(m.total_cost).toFixed(6))}
                    <span className="mx-1.5">·</span>
                    {t('dashboard.call_count', '调用次数')} {m.count}
                  </div>
                </div>
              </div>
            );
          })}
        </div>
        </>
      ) : (
        <div className="flex min-h-[400px] items-center justify-center text-muted-foreground">
          {loading ? <Spin size="large" /> : t('dashboard.no_data')}
        </div>
      )}
    </div>
  );
};

export default ModelDistribution30d;
