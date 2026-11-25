import React, { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  Button,
  Card,
  Empty,
  Grid,
  Input,
  Progress,
  Segmented,
  Select,
  Space,
  Spin,
  Tag,
  Tooltip,
  Typography,
} from 'antd';
import {
  ApiOutlined,
  ArrowLeftOutlined,
  CheckCircleOutlined,
  ClockCircleOutlined,
  CloseCircleOutlined,
  EditOutlined,
  ExclamationCircleOutlined,
  FilterOutlined,
  KeyOutlined,
  LineChartOutlined,
  LinkOutlined,
  ReloadOutlined,
  SearchOutlined,
  SwapRightOutlined,
  SyncOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import {
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip as RechartsTooltip,
  XAxis,
  YAxis,
} from 'recharts';
import dayjs from 'dayjs';
import ErrorBoundary from '../../../components/ErrorBoundary';
import type { ChannelCategory, ChannelConfig } from '../../../types';
import { formatApiDateTime } from '../../../utils/timedisplay';
import { channelEditPath } from '../channelPaths';

const { Text } = Typography;
const { useBreakpoint } = Grid;

export interface ChannelConfigSyncLog {
  id: number;
  config_id: number;
  upstream_system: string;
  upstream_group: string;
  upstream_ratio: number;
  rate_add: number;
  upstream_currency: string;
  site_currency: string;
  currency_rate: number;
  previous_rate: number;
  applied_rate: number;
  sync_interval_minutes: number;
  trigger_type: string;
  status: number;
  error_message: string;
  created_at: string;
}

interface UpstreamSystemOptionItem {
  key: string;
  label: string;
  count: number;
}

interface UpstreamSystemDetailPanelProps {
  systemKey: string;
  systemOptions?: UpstreamSystemOptionItem[];
  configs: ChannelConfig[];
  categories: ChannelCategory[];
  isLight: boolean;
  adminPath: string;
  syncLogsMap: Record<number, ChannelConfigSyncLog[]>;
  syncLogsLoadingMap: Record<number, boolean>;
  syncTriggerLoadingMap: Record<number, boolean>;
  volcBalanceLoadingMap?: Record<number, boolean>;
  onRefreshSystemLogs: (targetConfigs: ChannelConfig[]) => void;
  onManualSyncRate: (record: ChannelConfig) => Promise<void>;
  onRefreshVolcBalance?: (record: ChannelConfig, silent?: boolean) => Promise<void>;
  onEditConfig: (record: ChannelConfig) => void;
  onUpdateSortOrder?: (id: number, sort_order: number) => void;
  onSwitchSystem?: (systemKey: string) => void;
  onBackToList: () => void;
}

type RowStatusFilter = 'all' | 'active' | 'disabled' | 'auto_sync' | 'changed' | 'error';
type RowSortMode =
  | 'default'
  | 'sort_asc'
  | 'rate_asc'
  | 'rate_desc'
  | 'changes_desc'
  | 'sync_recent'
  | 'bound_desc';
type RowLogFilter = 'all' | 'changed' | 'error';
type ChartDaysRange = 15 | 30 | 60;

interface ChartPointItem {
  dateKey: string;
  timeLabel: string;
  fullTime: string;
  applied_rate: number;
  upstream_ratio: number;
  previous_rate: number;
  rate_add: number;
  upstream_group: string;
  upstream_currency: string;
  site_currency: string;
  currency_rate: number;
  delta: number;
  syncCount: number;
  changeCount: number;
  minDayRate: number;
  maxDayRate: number;
}

const SYSTEM_DESCRIPTIONS: Record<string, string> = {
  newapi: '支持拉取上游 /api/pricing 公开分组或通过「系统访问令牌 + 上游用户ID」拉取 /api/user/self/groups 账号私有专属分组倍率、跨币种汇率换算、叠加增量与定时自动同步',
  Tkeapi: 'TokensByte (Tkeapi) 标准上游系统接入，支持多模型渠道分组高可用调度与额度管控',
  官方: '官方原生 API 端点直连，提供稳定标准协议鉴权与多维度配额保护',
  兼容: 'OpenAI 兼容格式通用上游端点接入，灵活适配第三方聚合与自建推理服务',
  火山引擎: '火山方舟 / 豆包 / 即梦视频等字节跳动云原生大模型与多媒体任务端点',
  阿里云: '阿里云百炼 (DashScope) 大模型与通义万相多模态服务接入端点',
  __unclassified__: '尚未指定具体上游系统分类的渠道预设配置',
};

function resolveCategoryIds(record: { category_ids?: number[]; category_id?: number | null }): number[] {
  if (Array.isArray(record.category_ids) && record.category_ids.length > 0) {
    return record.category_ids;
  }
  if (record.category_id != null && record.category_id > 0) {
    return [record.category_id];
  }
  return [];
}

function formatNum(n?: number): string {
  const val = Number(n ?? 0);
  if (!Number.isFinite(val)) return '0';
  return Number.isInteger(val) ? String(val) : val.toFixed(4).replace(/\.?0+$/, '');
}

function buildChannelChartPoints(
  cfg: ChannelConfig,
  rawLogs: ChannelConfigSyncLog[],
  daysRange: ChartDaysRange,
): {
  points: ChartPointItem[];
  hasRealLogs: boolean;
  minRate: number;
  maxRate: number;
  changedCount: number;
  changedDaysCount: number;
} {
  const successLogs = rawLogs.filter(l => l.status === 1);
  const changedLogs = successLogs.filter(
    l => Math.abs(Number(l.applied_rate ?? 0) - Number(l.previous_rate ?? 0)) > 0.00001,
  );
  const chronological = [...successLogs].reverse();

  const currentRate = Number((cfg.rate ?? 1).toFixed(4));
  const defaultGroup = (cfg.upstream_group || '').trim() || '默认';
  const defaultUpCurr = (cfg.upstream_currency || '').trim();
  const defaultStCurr = (cfg.site_currency || '').trim();
  const defaultCurrRate = Number(cfg.upstream_currency_rate) > 0 ? Number(cfg.upstream_currency_rate) : 7.2;
  const defaultAdd = Number(cfg.upstream_sync_rate_add ?? 0);

  // 按天聚合同步记录 (key: YYYY-MM-DD)
  const logsByDay = new Map<string, ChannelConfigSyncLog[]>();
  chronological.forEach(l => {
    if (!l.created_at) return;
    const dKey = dayjs(l.created_at).format('YYYY-MM-DD');
    const arr = logsByDay.get(dKey) || [];
    arr.push(l);
    logsByDay.set(dKey, arr);
  });

  const today = dayjs().startOf('day');
  const startDayKey = today.subtract(daysRange - 1, 'day').format('YYYY-MM-DD');

  // 计算进入统计窗口第一天前的基准倍率
  let runningApplied = currentRate;
  let runningUpstream = currentRate;
  let runningGroup = defaultGroup;
  let runningUpCurr = defaultUpCurr;
  let runningStCurr = defaultStCurr;
  let runningCurrRate = defaultCurrRate;
  let runningAdd = defaultAdd;

  if (chronological.length > 0) {
    const logsBeforeWindow = chronological.filter(
      l => l.created_at && dayjs(l.created_at).format('YYYY-MM-DD') < startDayKey,
    );
    if (logsBeforeWindow.length > 0) {
      const lastBefore = logsBeforeWindow[logsBeforeWindow.length - 1];
      runningApplied = Number((lastBefore.applied_rate ?? currentRate).toFixed(4));
      runningUpstream = Number((lastBefore.upstream_ratio ?? runningApplied).toFixed(4));
      runningGroup = lastBefore.upstream_group || defaultGroup;
      runningUpCurr = lastBefore.upstream_currency || defaultUpCurr;
      runningStCurr = lastBefore.site_currency || defaultStCurr;
      runningCurrRate = lastBefore.currency_rate || defaultCurrRate;
      runningAdd = Number(lastBefore.rate_add ?? defaultAdd);
    } else {
      const oldest = chronological[0];
      runningApplied = Number((oldest.previous_rate ?? oldest.applied_rate ?? currentRate).toFixed(4));
      runningUpstream = Number((oldest.upstream_ratio ?? runningApplied).toFixed(4));
      runningGroup = oldest.upstream_group || defaultGroup;
      runningUpCurr = oldest.upstream_currency || defaultUpCurr;
      runningStCurr = oldest.site_currency || defaultStCurr;
      runningCurrRate = oldest.currency_rate || defaultCurrRate;
      runningAdd = Number(oldest.rate_add ?? defaultAdd);
    }
  }

  const points: ChartPointItem[] = [];
  let changedDaysCount = 0;

  for (let i = daysRange - 1; i >= 0; i -= 1) {
    const dayObj = today.subtract(i, 'day');
    const dateKey = dayObj.format('YYYY-MM-DD');
    const timeLabel = dayObj.format('MM-DD');
    const isToday = i === 0;
    const dayLogs = logsByDay.get(dateKey) || [];

    const dayStartRate = runningApplied;
    let dayMinRate = runningApplied;
    let dayMaxRate = runningApplied;
    let dayChangeCount = 0;

    if (dayLogs.length > 0) {
      const dayRates = dayLogs.map(l => Number((l.applied_rate ?? runningApplied).toFixed(4)));
      dayMinRate = Math.min(dayStartRate, ...dayRates);
      dayMaxRate = Math.max(dayStartRate, ...dayRates);
      dayChangeCount = dayLogs.filter(
        l => Math.abs(Number(l.applied_rate ?? 0) - Number(l.previous_rate ?? 0)) > 0.00001,
      ).length;

      const lastOfDay = dayLogs[dayLogs.length - 1];
      runningApplied = Number((lastOfDay.applied_rate ?? runningApplied).toFixed(4));
      runningUpstream = Number((lastOfDay.upstream_ratio ?? runningUpstream).toFixed(4));
      runningGroup = lastOfDay.upstream_group || runningGroup;
      runningUpCurr = lastOfDay.upstream_currency || runningUpCurr;
      runningStCurr = lastOfDay.site_currency || runningStCurr;
      runningCurrRate = lastOfDay.currency_rate || runningCurrRate;
      runningAdd = Number(lastOfDay.rate_add ?? runningAdd);
    } else if (isToday && chronological.length === 0) {
      runningApplied = currentRate;
      runningUpstream = currentRate;
      dayMinRate = currentRate;
      dayMaxRate = currentRate;
    }

    const delta = Number((runningApplied - dayStartRate).toFixed(4));
    if (dayChangeCount > 0 || Math.abs(delta) > 0.00001) {
      changedDaysCount += 1;
    }

    points.push({
      dateKey,
      timeLabel,
      fullTime: isToday ? `${dateKey}（今日）` : dateKey,
      applied_rate: runningApplied,
      upstream_ratio: runningUpstream,
      previous_rate: dayStartRate,
      rate_add: runningAdd,
      upstream_group: runningGroup,
      upstream_currency: runningUpCurr,
      site_currency: runningStCurr,
      currency_rate: runningCurrRate,
      delta,
      syncCount: dayLogs.length,
      changeCount: dayChangeCount,
      minDayRate: dayMinRate,
      maxDayRate: dayMaxRate,
    });
  }

  const allApplied = points.map(p => p.applied_rate).filter(n => Number.isFinite(n));
  const minRate = allApplied.length > 0 ? Math.min(...allApplied) : currentRate;
  const maxRate = allApplied.length > 0 ? Math.max(...allApplied) : currentRate;

  return {
    points,
    hasRealLogs: chronological.length > 0,
    minRate,
    maxRate,
    changedCount: changedLogs.length,
    changedDaysCount,
  };
}

const UpstreamSystemDetailPanel: React.FC<UpstreamSystemDetailPanelProps> = ({
  systemKey,
  systemOptions,
  configs,
  categories,
  isLight,
  adminPath,
  syncLogsMap,
  syncLogsLoadingMap,
  syncTriggerLoadingMap,
  volcBalanceLoadingMap,
  onRefreshSystemLogs,
  onManualSyncRate,
  onRefreshVolcBalance,
  onEditConfig,
  onUpdateSortOrder,
  onSwitchSystem,
  onBackToList,
}) => {
  const screens = useBreakpoint();
  const [searchKeyword, setSearchKeyword] = useState('');
  const [statusFilter, setStatusFilter] = useState<RowStatusFilter>('all');
  const [groupFilter, setGroupFilter] = useState<string>('all');
  const [sortMode, setSortMode] = useState<RowSortMode>('default');
  const [globalChartDays, setGlobalChartDays] = useState<ChartDaysRange>(15);
  const [rowChartDaysMap, setRowChartDaysMap] = useState<Record<number, ChartDaysRange>>({});
  const [rowLogFilterMap, setRowLogFilterMap] = useState<Record<number, RowLogFilter>>({});
  const [batchSyncing, setBatchSyncing] = useState(false);
  const [batchVolcRefreshing, setBatchVolcRefreshing] = useState(false);

  const systemLabel = systemKey === '__unclassified__' ? '未分类系统' : systemKey;
  const systemDesc =
    SYSTEM_DESCRIPTIONS[systemKey] || `绑定「${systemLabel}」的上游渠道配置详情与分组倍率变动监控`;

  const resolveCategoryName = (id: number) => categories.find(c => c.id === id)?.name || '';

  // 汇总统计指标
  const summary = useMemo(() => {
    const total = configs.length;
    const active = configs.filter(c => (c.status ?? 1) === 1).length;
    const disabled = total - active;

    const uniqueBoundGroupIds = new Set<number>();
    let haGroupBindings = 0;
    let directBindings = 0;
    configs.forEach(c => {
      (c.bound_channels || []).forEach(ch => {
        uniqueBoundGroupIds.add(ch.id);
        if (ch.is_ha) haGroupBindings += 1;
        else directBindings += 1;
      });
    });

    const rates = configs.map(c => Number(c.rate ?? 1)).filter(n => Number.isFinite(n));
    const minRate = rates.length ? Math.min(...rates) : 0;
    const maxRate = rates.length ? Math.max(...rates) : 0;

    const syncedGroupConfigs = configs.filter(c => Boolean((c.upstream_group || '').trim()));
    const autoSyncConfigs = configs.filter(
      c => Boolean((c.upstream_group || '').trim()) && Number(c.upstream_sync_interval_minutes) > 0,
    );

    const groupCounts = new Map<string, number>();
    configs.forEach(c => {
      const g = (c.upstream_group || '').trim();
      if (g) {
        groupCounts.set(g, (groupCounts.get(g) || 0) + 1);
      }
    });
    const uniqueUpstreamGroups = Array.from(groupCounts.entries()).map(([name, count]) => ({
      name,
      count,
    }));

    let totalLogs = 0;
    let changedLogsCount = 0;
    let changedChannelsCount = 0;
    let errorChannelsCount = 0;

    configs.forEach(c => {
      const logs = syncLogsMap[c.id] || [];
      totalLogs += logs.length;
      let hasChanged = false;
      logs.forEach(l => {
        if (l.status === 1 && Math.abs((l.previous_rate ?? 0) - (l.applied_rate ?? 0)) > 0.00001) {
          changedLogsCount += 1;
          hasChanged = true;
        }
      });
      if (hasChanged) changedChannelsCount += 1;
      if (logs.length > 0 && logs[0].status !== 1) {
        errorChannelsCount += 1;
      }
    });

    // 火山引擎账户余额统计（按 Access Key ID 去重汇总，避免同一 AK 绑定多个预设时重复累加）
    const volcConfiguredConfigs = configs.filter(
      c => Boolean(c.has_volc_credentials || (c.volc_ak || '').trim()),
    );
    const volcAccountBalanceMap = new Map<string, number>();
    let hasAnyVolcBalance = false;
    configs.forEach(c => {
      if (c.upstream_balance !== null && c.upstream_balance !== undefined && Number.isFinite(Number(c.upstream_balance))) {
        hasAnyVolcBalance = true;
        const akKey = (c.volc_ak || '').trim() || `cfg_${c.id}`;
        volcAccountBalanceMap.set(akKey, Number(c.upstream_balance));
      }
    });
    const volcUniqueAccountCount = volcAccountBalanceMap.size;
    const volcTotalBalance = Array.from(volcAccountBalanceMap.values()).reduce((sum, v) => sum + v, 0);

    return {
      total,
      active,
      disabled,
      uniqueBoundGroups: uniqueBoundGroupIds.size,
      haGroupBindings,
      directBindings,
      minRate,
      maxRate,
      syncedGroupCount: syncedGroupConfigs.length,
      autoSyncCount: autoSyncConfigs.length,
      uniqueUpstreamGroups,
      totalLogs,
      changedLogsCount,
      changedChannelsCount,
      errorChannelsCount,
      volcConfiguredCount: volcConfiguredConfigs.length,
      hasAnyVolcBalance,
      volcUniqueAccountCount,
      volcTotalBalance,
    };
  }, [configs, syncLogsMap]);

  const anyLoadingLogs = useMemo(
    () => configs.some(c => Boolean(syncLogsLoadingMap[c.id])),
    [configs, syncLogsLoadingMap],
  );

  // 过滤并排序后的渠道列表
  const displayedConfigs = useMemo(() => {
    const kw = searchKeyword.trim().toLowerCase();

    const filtered = configs.filter(cfg => {
      if (statusFilter === 'active' && (cfg.status ?? 1) !== 1) return false;
      if (statusFilter === 'disabled' && (cfg.status ?? 1) !== 0) return false;
      if (
        statusFilter === 'auto_sync' &&
        !(Boolean((cfg.upstream_group || '').trim()) && Number(cfg.upstream_sync_interval_minutes) > 0)
      ) {
        return false;
      }
      if (statusFilter === 'changed') {
        const logs = syncLogsMap[cfg.id] || [];
        const hasChange = logs.some(
          l => l.status === 1 && Math.abs((l.previous_rate ?? 0) - (l.applied_rate ?? 0)) > 0.00001,
        );
        if (!hasChange) return false;
      }
      if (statusFilter === 'error') {
        const logs = syncLogsMap[cfg.id] || [];
        const hasErr = logs.some(l => l.status !== 1);
        if (!hasErr) return false;
      }

      if (groupFilter !== 'all') {
        const g = (cfg.upstream_group || '').trim();
        if (groupFilter === '__none__' ? Boolean(g) : g !== groupFilter) return false;
      }

      if (kw) {
        const matchName = (cfg.name || '').toLowerCase().includes(kw);
        const matchYid = (cfg.yid || '').toLowerCase().includes(kw);
        const matchGroup = (cfg.upstream_group || '').toLowerCase().includes(kw);
        const matchProvider = (cfg.provider_type || '').toLowerCase().includes(kw);
        const matchUrl = (cfg.base_url || '').toLowerCase().includes(kw);
        const matchRemark = (cfg.remark || '').toLowerCase().includes(kw);
        if (!matchName && !matchYid && !matchGroup && !matchProvider && !matchUrl && !matchRemark) {
          return false;
        }
      }
      return true;
    });

    const sorted = [...filtered];
    if (sortMode === 'default') {
      sorted.sort((a, b) => (b.sort_order ?? 0) - (a.sort_order ?? 0) || b.id - a.id);
    } else if (sortMode === 'sort_asc') {
      sorted.sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0) || a.id - b.id);
    } else if (sortMode === 'rate_asc') {
      sorted.sort((a, b) => Number(a.rate ?? 1) - Number(b.rate ?? 1) || a.id - b.id);
    } else if (sortMode === 'rate_desc') {
      sorted.sort((a, b) => Number(b.rate ?? 1) - Number(a.rate ?? 1) || a.id - b.id);
    } else if (sortMode === 'changes_desc') {
      sorted.sort((a, b) => {
        const countA = (syncLogsMap[a.id] || []).filter(
          l => l.status === 1 && Math.abs((l.previous_rate ?? 0) - (l.applied_rate ?? 0)) > 0.00001,
        ).length;
        const countB = (syncLogsMap[b.id] || []).filter(
          l => l.status === 1 && Math.abs((l.previous_rate ?? 0) - (l.applied_rate ?? 0)) > 0.00001,
        ).length;
        return countB - countA || Number(b.rate ?? 1) - Number(a.rate ?? 1);
      });
    } else if (sortMode === 'sync_recent') {
      sorted.sort((a, b) => {
        const ta = a.upstream_synced_at || (syncLogsMap[a.id]?.[0]?.created_at ?? '') || '';
        const tb = b.upstream_synced_at || (syncLogsMap[b.id]?.[0]?.created_at ?? '') || '';
        return tb.localeCompare(ta) || a.id - b.id;
      });
    } else if (sortMode === 'bound_desc') {
      sorted.sort(
        (a, b) =>
          (b.bound_channel_count ?? b.bound_channels?.length ?? 0) -
            (a.bound_channel_count ?? a.bound_channels?.length ?? 0) || a.id - b.id,
      );
    }

    return sorted;
  }, [configs, searchKeyword, statusFilter, groupFilter, sortMode, syncLogsMap]);

  const syncableNewapiConfigs = useMemo(
    () =>
      configs.filter(
        c => c.upstream_system === 'newapi' && Boolean((c.upstream_group || '').trim()),
      ),
    [configs],
  );

  const syncableVolcConfigs = useMemo(
    () =>
      configs.filter(
        c => Boolean(c.has_volc_credentials || (c.volc_ak || '').trim()),
      ),
    [configs],
  );

  const handleBatchSyncAll = async () => {
    if (syncableNewapiConfigs.length === 0 || batchSyncing) return;
    setBatchSyncing(true);
    try {
      for (const cfg of syncableNewapiConfigs) {
        await onManualSyncRate(cfg);
      }
    } finally {
      setBatchSyncing(false);
    }
  };

  const handleBatchRefreshVolcBalances = async () => {
    if (!onRefreshVolcBalance || syncableVolcConfigs.length === 0 || batchVolcRefreshing) return;
    setBatchVolcRefreshing(true);
    try {
      for (const cfg of syncableVolcConfigs) {
        await onRefreshVolcBalance(cfg, true);
      }
    } finally {
      setBatchVolcRefreshing(false);
    }
  };

  const borderColor = isLight ? 'rgba(0, 0, 0, 0.08)' : 'rgba(255, 255, 255, 0.1)';
  const subtleBg = isLight ? '#f8fafc' : 'rgba(255, 255, 255, 0.03)';
  const panelBg = isLight ? '#fcfcfd' : 'rgba(255, 255, 255, 0.02)';
  const cardBg = isLight ? '#ffffff' : '#141414';
  const gridStroke = isLight ? '#e2e8f0' : 'rgba(255, 255, 255, 0.08)';
  const axisColor = isLight ? '#64748b' : '#a1a1aa';
  const textPrimary = isLight ? '#0f172a' : '#f8fafc';
  const primaryLineColor = isLight ? '#2563eb' : '#3b82f6';
  const secondaryLineColor = isLight ? '#94a3b8' : '#71717a';

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      {/* 1. 顶部系统导航与全局操作栏 */}
      <div
        style={{
          padding: '14px 18px',
          borderRadius: 10,
          border: `1px solid ${borderColor}`,
          backgroundColor: cardBg,
          display: 'flex',
          flexDirection: 'column',
          gap: 12,
        }}
      >
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            flexWrap: 'wrap',
            gap: 12,
          }}
        >
          <Space size={10} wrap style={{ alignItems: 'center' }}>
            <Button icon={<ArrowLeftOutlined />} onClick={onBackToList}>
              返回列表
            </Button>
            <Tag
              style={{
                margin: 0,
                fontSize: 14,
                fontWeight: 600,
                padding: '3px 12px',
                borderRadius: 6,
                background: isLight ? '#f1f5f9' : 'rgba(255, 255, 255, 0.08)',
                color: textPrimary,
                border: `1px solid ${borderColor}`,
              }}
            >
              <ApiOutlined style={{ marginRight: 6, color: axisColor }} />
              上游系统监控：{systemLabel}
            </Tag>
            <Text type="secondary" style={{ fontSize: 13 }}>
              {systemDesc}
            </Text>
          </Space>

          <Space size={8} wrap>
            <Button
              icon={<ReloadOutlined spin={anyLoadingLogs} />}
              onClick={() => onRefreshSystemLogs(configs)}
            >
              刷新全部记录
            </Button>
            {syncableVolcConfigs.length > 0 && onRefreshVolcBalance && (
              <Button
                type="primary"
                icon={<SyncOutlined spin={batchVolcRefreshing} />}
                loading={batchVolcRefreshing}
                onClick={handleBatchRefreshVolcBalances}
              >
                一键刷新账户余额 ({syncableVolcConfigs.length})
              </Button>
            )}
            {syncableNewapiConfigs.length > 0 && (
              <Button
                type="primary"
                icon={<SyncOutlined spin={batchSyncing} />}
                loading={batchSyncing}
                onClick={handleBatchSyncAll}
              >
                一键同步全部分组倍率 ({syncableNewapiConfigs.length})
              </Button>
            )}
          </Space>
        </div>

        {systemOptions && systemOptions.length > 0 && onSwitchSystem && (
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              flexWrap: 'wrap',
              gap: 6,
              paddingTop: 10,
              borderTop: `1px solid ${borderColor}`,
            }}
          >
            <Text type="secondary" style={{ fontSize: 12, marginRight: 6 }}>
              切换上游系统：
            </Text>
            {systemOptions.map(item => {
              const selected = item.key === systemKey;
              return (
                <div
                  key={item.key}
                  onClick={() => {
                    setGroupFilter('all');
                    setStatusFilter('all');
                    onSwitchSystem(item.key);
                  }}
                  style={{
                    padding: '3px 10px',
                    borderRadius: 6,
                    fontSize: 12,
                    fontWeight: selected ? 600 : 400,
                    backgroundColor: selected
                      ? (isLight ? '#18181b' : '#f4f4f5')
                      : (isLight ? '#f4f4f5' : 'rgba(255, 255, 255, 0.06)'),
                    color: selected
                      ? (isLight ? '#ffffff' : '#09090b')
                      : (isLight ? '#52525b' : 'rgba(255, 255, 255, 0.75)'),
                    cursor: 'pointer',
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 6,
                    transition: 'all 0.15s ease',
                  }}
                >
                  <span>{item.label}</span>
                  <span style={{ opacity: 0.65, fontSize: 11 }}>{item.count}</span>
                </div>
              );
            })}
          </div>
        )}
      </div>

      {/* 2. 核心指标概览卡片行 */}
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fit, minmax(215px, 1fr))',
          gap: 10,
        }}
      >
        <div
          style={{
            background: cardBg,
            border: `1px solid ${borderColor}`,
            borderRadius: 8,
            padding: '10px 14px',
          }}
        >
          <Text type="secondary" style={{ fontSize: 12 }}>绑定该系统的上游渠道</Text>
          <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', marginTop: 4 }}>
            <span style={{ fontSize: 20, fontWeight: 700, color: textPrimary }}>
              {summary.total} <span style={{ fontSize: 12, fontWeight: 400, color: axisColor }}>个预设</span>
            </span>
            <Space size={4}>
              <Tag style={{ margin: 0, fontSize: 11, padding: '0 5px' }}>激活 {summary.active}</Tag>
              {summary.disabled > 0 && (
                <Tag style={{ margin: 0, fontSize: 11, padding: '0 5px', color: axisColor }}>禁用 {summary.disabled}</Tag>
              )}
            </Space>
          </div>
        </div>

        <div
          style={{
            background: cardBg,
            border: `1px solid ${borderColor}`,
            borderRadius: 8,
            padding: '10px 14px',
          }}
        >
          <Text type="secondary" style={{ fontSize: 12 }}>关联下游模型渠道分组</Text>
          <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', marginTop: 4 }}>
            <span style={{ fontSize: 20, fontWeight: 700, color: textPrimary }}>
              {summary.uniqueBoundGroups} <span style={{ fontSize: 12, fontWeight: 400, color: axisColor }}>个模型组</span>
            </span>
            <Space size={4}>
              <Tag style={{ margin: 0, fontSize: 11, padding: '0 5px' }}>HA {summary.haGroupBindings}</Tag>
              <Tag style={{ margin: 0, fontSize: 11, padding: '0 5px' }}>预设直连 {summary.directBindings}</Tag>
            </Space>
          </div>
        </div>

        <div
          style={{
            background: cardBg,
            border: `1px solid ${borderColor}`,
            borderRadius: 8,
            padding: '10px 14px',
          }}
        >
          <Text type="secondary" style={{ fontSize: 12 }}>当前生效渠道倍率与绑定分组</Text>
          <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', marginTop: 4 }}>
            <span style={{ fontSize: 19, fontWeight: 700, color: textPrimary }}>
              {summary.total === 0
                ? '-'
                : summary.minRate === summary.maxRate
                  ? `${summary.minRate}x`
                  : `${summary.minRate}x ~ ${summary.maxRate}x`}
            </span>
            <Tag style={{ margin: 0, fontSize: 11, padding: '0 6px' }}>
              {summary.uniqueUpstreamGroups.length > 0
                ? `覆盖 ${summary.uniqueUpstreamGroups.length} 个上游分组`
                : '标准固定倍率'}
            </Tag>
          </div>
        </div>

        {(systemKey === '火山引擎' || summary.volcConfiguredCount > 0 || summary.hasAnyVolcBalance) && (
          <div
            style={{
              background: cardBg,
              border: `1px solid ${borderColor}`,
              borderRadius: 8,
              padding: '10px 14px',
            }}
          >
            <Text type="secondary" style={{ fontSize: 12 }}>火山引擎上游账户余额</Text>
            <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', marginTop: 4 }}>
              <span
                style={{
                  fontSize: 19,
                  fontWeight: 700,
                  color: summary.hasAnyVolcBalance
                    ? (summary.volcTotalBalance > 0 ? '#10b981' : '#ef4444')
                    : textPrimary,
                }}
              >
                {summary.hasAnyVolcBalance
                  ? `¥${summary.volcTotalBalance.toFixed(2)}`
                  : (summary.volcConfiguredCount > 0 ? '查询中 / 待刷新' : '未配置 AK/SK')}
              </span>
              <Space size={4}>
                {summary.volcUniqueAccountCount > 1 && (
                  <Tag color="blue" style={{ margin: 0, fontSize: 11, padding: '0 5px' }}>
                    {summary.volcUniqueAccountCount} 个账户汇总
                  </Tag>
                )}
                <Tag
                  color={summary.volcConfiguredCount > 0 ? 'success' : 'default'}
                  style={{ margin: 0, fontSize: 11, padding: '0 5px' }}
                >
                  已配凭证 {summary.volcConfiguredCount}/{summary.total}
                </Tag>
              </Space>
            </div>
          </div>
        )}

        <div
          style={{
            background: cardBg,
            border: `1px solid ${borderColor}`,
            borderRadius: 8,
            padding: '10px 14px',
          }}
        >
          <Text type="secondary" style={{ fontSize: 12 }}>倍率同步与变动巡检统计</Text>
          <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', marginTop: 4 }}>
            <span style={{ fontSize: 19, fontWeight: 700, color: textPrimary }}>
              {summary.changedLogsCount} <span style={{ fontSize: 12, fontWeight: 400, color: axisColor }}>次倍率变动</span>
            </span>
            <Space size={4}>
              <Tag style={{ margin: 0, fontSize: 11, padding: '0 5px' }}>
                定时 {summary.autoSyncCount}/{summary.syncedGroupCount}
              </Tag>
              {summary.errorChannelsCount > 0 ? (
                <Tag color="error" style={{ margin: 0, fontSize: 11, padding: '0 5px' }}>
                  异常 {summary.errorChannelsCount}
                </Tag>
              ) : (
                <Tag style={{ margin: 0, fontSize: 11, padding: '0 5px' }}>
                  记录 {summary.totalLogs}
                </Tag>
              )}
            </Space>
          </div>
        </div>
      </div>

      {/* 3. 筛选、分组快筛、排序与全局曲线控制工具栏 */}
      {configs.length > 0 && (
        <div
          style={{
            background: cardBg,
            border: `1px solid ${borderColor}`,
            borderRadius: 8,
            padding: '10px 14px',
            display: 'flex',
            flexDirection: 'column',
            gap: 10,
          }}
        >
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              flexWrap: 'wrap',
              gap: 10,
            }}
          >
            <Space size={8} wrap style={{ alignItems: 'center' }}>
              <Input
                placeholder="搜索渠道名称、YID、上游分组、服务商或 Base URL..."
                allowClear
                prefix={<SearchOutlined style={{ color: axisColor }} />}
                value={searchKeyword}
                onChange={e => setSearchKeyword(e.target.value)}
                style={{ width: screens.xs ? '100%' : 280 }}
                size="small"
              />
              <Segmented
                size="small"
                value={statusFilter}
                onChange={val => setStatusFilter(val as RowStatusFilter)}
                options={[
                  { label: `全部 (${configs.length})`, value: 'all' },
                  { label: `激活 (${summary.active})`, value: 'active' },
                  ...(summary.disabled > 0 ? [{ label: `已禁用 (${summary.disabled})`, value: 'disabled' }] : []),
                  ...(summary.autoSyncCount > 0 ? [{ label: `定时同步 (${summary.autoSyncCount})`, value: 'auto_sync' }] : []),
                  ...(summary.changedChannelsCount > 0
                    ? [{ label: `有变动 (${summary.changedChannelsCount})`, value: 'changed' }]
                    : []),
                  ...(summary.errorChannelsCount > 0
                    ? [{ label: `同步异常 (${summary.errorChannelsCount})`, value: 'error' }]
                    : []),
                ]}
              />
            </Space>

            <Space size={10} wrap style={{ alignItems: 'center' }}>
              <Space size={5}>
                <Text type="secondary" style={{ fontSize: 12 }}>按天周期：</Text>
                <Segmented
                  size="small"
                  value={globalChartDays}
                  onChange={val => {
                    setGlobalChartDays(val as ChartDaysRange);
                    setRowChartDaysMap({});
                  }}
                  options={[
                    { label: '近15天', value: 15 },
                    { label: '近30天', value: 30 },
                    { label: '近60天', value: 60 },
                  ]}
                />
              </Space>

              <Select
                size="small"
                value={sortMode}
                onChange={(val: RowSortMode) => setSortMode(val)}
                style={{ width: 195 }}
                options={[
                  { value: 'default', label: '排序：按渠道排序（从大到小）' },
                  { value: 'sort_asc', label: '排序：按渠道排序（从小到大）' },
                  { value: 'rate_asc', label: '排序：渠道倍率从低到高' },
                  { value: 'rate_desc', label: '排序：渠道倍率从高到低' },
                  { value: 'changes_desc', label: '排序：变动次数从多到少' },
                  { value: 'sync_recent', label: '排序：最近同步时间优先' },
                  { value: 'bound_desc', label: '排序：绑定模型组从多到少' },
                ]}
              />
            </Space>
          </div>

          {summary.uniqueUpstreamGroups.length > 0 && (
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                flexWrap: 'wrap',
                gap: 6,
                paddingTop: 8,
                borderTop: `1px dashed ${borderColor}`,
              }}
            >
              <Text type="secondary" style={{ fontSize: 12, marginRight: 2 }}>
                <FilterOutlined style={{ marginRight: 4 }} />
                按上游同步分组筛选：
              </Text>
              {([
                { key: 'all', label: `全部分组 (${configs.length})` },
                ...summary.uniqueUpstreamGroups.map(g => ({
                  key: g.name,
                  label: `${g.name} (${g.count})`,
                })),
                ...(configs.some(c => !(c.upstream_group || '').trim())
                  ? [
                      {
                        key: '__none__',
                        label: `未绑定分组 (${configs.filter(c => !(c.upstream_group || '').trim()).length})`,
                      },
                    ]
                  : []),
              ]).map(item => {
                const selected = groupFilter === item.key;
                return (
                  <div
                    key={item.key}
                    onClick={() => setGroupFilter(selected && item.key !== 'all' ? 'all' : item.key)}
                    style={{
                      padding: '2px 9px',
                      borderRadius: 5,
                      fontSize: 11,
                      fontWeight: selected ? 600 : 400,
                      backgroundColor: selected
                        ? (isLight ? '#18181b' : '#f4f4f5')
                        : (isLight ? '#f4f4f5' : 'rgba(255, 255, 255, 0.06)'),
                      color: selected
                        ? (isLight ? '#ffffff' : '#09090b')
                        : (isLight ? '#52525b' : 'rgba(255, 255, 255, 0.75)'),
                      cursor: 'pointer',
                      transition: 'all 0.15s ease',
                    }}
                  >
                    {item.label}
                  </div>
                );
              })}
            </div>
          )}
        </div>
      )}

      {/* 4. 横排列表：每个绑定的上游渠道独立一横排（左：渠道全量信息，中：倍率变动趋势曲线，右：分组同步历史记录） */}
      {configs.length === 0 ? (
        <Card size="small" style={{ background: cardBg, borderRadius: 8 }}>
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={`当前暂无绑定「${systemLabel}」的上游渠道配置预设`}
          />
        </Card>
      ) : displayedConfigs.length === 0 ? (
        <Card size="small" style={{ background: cardBg, borderRadius: 8 }}>
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description="当前筛选或搜索条件下暂无匹配的上游渠道配置"
          />
        </Card>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          {displayedConfigs.map((cfg, rowIdx) => {
            const isActive = (cfg.status ?? 1) === 1;
            const rawLogs = syncLogsMap[cfg.id] || [];
            const isLoadingLogs = Boolean(syncLogsLoadingMap[cfg.id]);
            const isSyncing = Boolean(syncTriggerLoadingMap[cfg.id]);
            const latestLog = rawLogs[0];
            const latestSuccessLog = rawLogs.find(l => l.status === 1);
            const hasSyncError = Boolean(latestLog && latestLog.status !== 1);

            const rowChartDays: ChartDaysRange = rowChartDaysMap[cfg.id] || globalChartDays;

            const chartInfo = buildChannelChartPoints(cfg, rawLogs, rowChartDays);

            const rowLogFilter: RowLogFilter = rowLogFilterMap[cfg.id] || 'all';
            const changedLogsCount = rawLogs.filter(
              l => l.status === 1 && Math.abs((l.previous_rate ?? 0) - (l.applied_rate ?? 0)) > 0.00001,
            ).length;
            const errorLogsCount = rawLogs.filter(l => l.status !== 1).length;

            const filteredRowLogs = rawLogs.filter(l => {
              if (rowLogFilter === 'changed') {
                return l.status === 1 && Math.abs((l.previous_rate ?? 0) - (l.applied_rate ?? 0)) > 0.00001;
              }
              if (rowLogFilter === 'error') {
                return l.status !== 1;
              }
              return true;
            });

            const catIds = resolveCategoryIds(cfg);
            const boundChannels = cfg.bound_channels || [];
            const upGroup = (cfg.upstream_group || '').trim();
            const upCurr = (cfg.upstream_currency || '').trim();
            const stCurr = (cfg.site_currency || '').trim();
            const hasConv = Boolean(upCurr && stCurr && upCurr.toLowerCase() !== stCurr.toLowerCase());
            const currRate = Number(cfg.upstream_currency_rate) > 0 ? Number(cfg.upstream_currency_rate) : 7.2;
            const rateAdd = Number(cfg.upstream_sync_rate_add ?? 0);
            const syncInterval = Number(cfg.upstream_sync_interval_minutes ?? 0);
            const canManualSync = cfg.upstream_system === 'newapi' && Boolean(upGroup);
            const hasApiKey = Boolean(cfg.has_api_key || cfg.api_key);
            const hasAccessToken = Boolean(cfg.has_upstream_access_token || cfg.upstream_access_token);
            const hasVolcCreds = Boolean(cfg.has_volc_credentials || (cfg.volc_ak || '').trim());
            const hasVolcBalance = cfg.upstream_balance !== null && cfg.upstream_balance !== undefined && Number.isFinite(Number(cfg.upstream_balance));
            const volcBalanceNum = hasVolcBalance ? Number(cfg.upstream_balance) : null;
            const isVolcBalanceLoading = Boolean(volcBalanceLoadingMap?.[cfg.id]);
            const showVolcSection = systemKey === '火山引擎' || hasVolcCreds || hasVolcBalance;

            const quotaItems = [
              { label: '总额度', used: Number(cfg.quota_used ?? 0), limit: Number(cfg.quota_limit ?? -1) },
              { label: '日额度', used: Number(cfg.daily_quota_used ?? 0), limit: Number(cfg.daily_quota_limit ?? -1) },
              { label: '周额度', used: Number(cfg.weekly_quota_used ?? 0), limit: Number(cfg.weekly_quota_limit ?? -1) },
              { label: '月额度', used: Number(cfg.monthly_quota_used ?? 0), limit: Number(cfg.monthly_quota_limit ?? -1) },
            ];
            const configuredQuotas = quotaItems.filter(q => q.limit >= 0);

            const rowBorderColor = hasSyncError
              ? (isLight ? 'rgba(239, 68, 68, 0.35)' : 'rgba(239, 68, 68, 0.35)')
              : borderColor;

            return (
              <div
                key={cfg.id}
                style={{
                  background: cardBg,
                  border: `1px solid ${rowBorderColor}`,
                  borderRadius: 10,
                  overflow: 'hidden',
                  transition: 'all 0.15s ease',
                }}
              >
                {/* 横排顶部标题栏：渠道身份、核心倍率与调度摘要、快捷操作 */}
                <div
                  style={{
                    padding: '10px 16px',
                    background: isLight ? '#f8fafc' : 'rgba(255, 255, 255, 0.03)',
                    borderBottom: `1px solid ${borderColor}`,
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    flexWrap: 'wrap',
                    gap: 10,
                  }}
                >
                  <div style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 8, minWidth: 0 }}>
                    <span
                      style={{
                        fontSize: 11,
                        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                        color: axisColor,
                        fontWeight: 600,
                      }}
                    >
                      #{rowIdx + 1}
                    </span>
                    <span
                      style={{
                        display: 'inline-flex',
                        alignItems: 'center',
                        gap: 5,
                        fontSize: 12,
                        color: isActive ? textPrimary : axisColor,
                        fontWeight: 500,
                      }}
                    >
                      <span
                        style={{
                          width: 6,
                          height: 6,
                          borderRadius: '50%',
                          backgroundColor: isActive ? '#10b981' : '#a1a1aa',
                        }}
                      />
                      {isActive ? '激活' : '已禁用'}
                    </span>
                    <span
                      style={{
                        fontWeight: 700,
                        fontSize: 14,
                        color: textPrimary,
                      }}
                    >
                      {cfg.name}
                    </span>
                    <Text
                      keyboard
                      style={{
                        fontSize: 11,
                        color: axisColor,
                        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                      }}
                    >
                      YID {cfg.yid || '-'}
                    </Text>

                    <Tag
                      style={{
                        margin: 0,
                        fontSize: 12,
                        fontWeight: 700,
                        padding: '1px 8px',
                        borderRadius: 5,
                        background: isLight ? '#f1f5f9' : 'rgba(255, 255, 255, 0.08)',
                        color: textPrimary,
                        border: `1px solid ${borderColor}`,
                      }}
                    >
                      生效倍率 {cfg.rate ?? 1}x
                    </Tag>

                    {showVolcSection && (
                      volcBalanceNum !== null ? (
                        <Tag
                          color={volcBalanceNum > 0 ? 'success' : 'error'}
                          style={{
                            margin: 0,
                            fontSize: 12,
                            fontWeight: 700,
                            padding: '1px 8px',
                            borderRadius: 5,
                          }}
                        >
                          账户余额 ¥{volcBalanceNum.toFixed(2)}
                        </Tag>
                      ) : hasVolcCreds ? (
                        <Tag
                          color="processing"
                          style={{ margin: 0, fontSize: 11, padding: '1px 7px', borderRadius: 4 }}
                        >
                          {isVolcBalanceLoading ? '余额查询中...' : '待查询余额'}
                        </Tag>
                      ) : (
                        <Tag style={{ margin: 0, fontSize: 11, padding: '1px 7px', borderRadius: 4, color: axisColor }}>
                          未配置火山 AK/SK
                        </Tag>
                      )
                    )}

                    {upGroup && (
                      <Tag style={{ margin: 0, fontSize: 11, padding: '1px 7px', borderRadius: 4 }}>
                        上游分组: {upGroup}
                        {latestSuccessLog ? ` (${latestSuccessLog.upstream_ratio}x)` : ''}
                      </Tag>
                    )}

                    <Tag style={{ margin: 0, fontSize: 11, padding: '1px 7px', borderRadius: 4 }}>
                      优先级 P{cfg.priority ?? 0} · 权重 W{cfg.weight ?? 1}
                    </Tag>

                    {cfg.provider_type && (
                      <Tag style={{ margin: 0, fontSize: 11, padding: '1px 6px', borderRadius: 4 }}>
                        {cfg.provider_type}
                      </Tag>
                    )}

                    {catIds.map(cid => {
                      const cname = resolveCategoryName(cid);
                      return cname ? (
                        <Tag key={cid} style={{ margin: 0, fontSize: 11, padding: '1px 6px', borderRadius: 4 }}>
                          {cname}
                        </Tag>
                      ) : null;
                    })}

                    {hasSyncError && (
                      <Tooltip title={latestLog?.error_message || '最近一次倍率同步失败'}>
                        <Tag color="error" style={{ margin: 0, fontSize: 11, padding: '1px 6px', borderRadius: 4 }}>
                          <ExclamationCircleOutlined style={{ marginRight: 3 }} />
                          最近同步失败
                        </Tag>
                      </Tooltip>
                    )}
                  </div>

                  <Space size={8} wrap>
                    {cfg.upstream_synced_at && (
                      <Text type="secondary" style={{ fontSize: 11 }}>
                        <ClockCircleOutlined style={{ marginRight: 4 }} />
                        最近同步: {formatApiDateTime(cfg.upstream_synced_at)}
                      </Text>
                    )}
                    {hasVolcCreds && onRefreshVolcBalance && (
                      <Button
                        size="small"
                        icon={<SyncOutlined spin={isVolcBalanceLoading} />}
                        loading={isVolcBalanceLoading}
                        onClick={() => onRefreshVolcBalance(cfg, false)}
                        style={{ fontSize: 12, height: 24, padding: '0 8px' }}
                      >
                        刷新余额
                      </Button>
                    )}
                    {canManualSync && (
                      <Button
                        size="small"
                        icon={<SyncOutlined spin={isSyncing} />}
                        loading={isSyncing}
                        onClick={() => onManualSyncRate(cfg)}
                        style={{ fontSize: 12, height: 24, padding: '0 8px' }}
                      >
                        立即同步倍率
                      </Button>
                    )}
                    <Tooltip title="刷新该渠道同步历史记录">
                      <Button
                        size="small"
                        icon={<ReloadOutlined spin={isLoadingLogs} />}
                        onClick={() => onRefreshSystemLogs([cfg])}
                        style={{ fontSize: 12, height: 24, padding: '0 8px' }}
                      >
                        刷新记录
                      </Button>
                    </Tooltip>
                    <Button
                      size="small"
                      icon={<EditOutlined />}
                      onClick={() => onEditConfig(cfg)}
                      style={{ fontSize: 12, height: 24, padding: '0 8px' }}
                    >
                      编辑配置
                    </Button>
                  </Space>
                </div>

                {/* 横排主体三栏布局：左（渠道配置与绑定明细） | 中（倍率变动趋势曲线） | 右（分组同步历史记录） */}
                <div
                  style={{
                    display: 'grid',
                    gridTemplateColumns: screens.xl
                      ? 'minmax(290px, 0.95fr) minmax(460px, 2.2fr) minmax(245px, 0.85fr)'
                      : screens.lg
                        ? 'minmax(265px, 0.95fr) minmax(380px, 2.05fr) minmax(235px, 0.85fr)'
                        : 'repeat(auto-fit, minmax(300px, 1fr))',
                    gap: 12,
                    padding: '12px 14px',
                    alignItems: 'stretch',
                  }}
                >
                  {/* 第一栏（左）：上游渠道详细信息、倍率换算链路、额度与关联下游模型组 */}
                  <div
                    style={{
                      background: panelBg,
                      border: `1px solid ${borderColor}`,
                      borderRadius: 8,
                      padding: '10px 12px',
                      display: 'flex',
                      flexDirection: 'column',
                      justifyContent: 'space-between',
                      gap: 8,
                      minWidth: 0,
                    }}
                  >
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 6 }}>
                        <Space size={5}>
                          <ThunderboltOutlined style={{ color: axisColor, fontSize: 13 }} />
                          <span style={{ fontWeight: 600, fontSize: 12 }}>渠道配置与倍率链路信息</span>
                        </Space>
                        <Space size={4} style={{ alignItems: 'center' }}>
                          <Text type="secondary" style={{ fontSize: 11 }}>排序</Text>
                          <Text
                            editable={
                              onUpdateSortOrder
                                ? {
                                    tooltip: '点击直接修改渠道排序值（数字越大越靠前）',
                                    text: String(cfg.sort_order ?? 0),
                                    onChange: newVal => {
                                      const num = parseInt(newVal.trim(), 10);
                                      onUpdateSortOrder(cfg.id, Number.isNaN(num) ? 0 : num);
                                    },
                                  }
                                : false
                            }
                            style={{
                              fontFamily: 'monospace',
                              fontSize: 11,
                              fontWeight: 600,
                              padding: '1px 6px',
                              borderRadius: 4,
                              background: subtleBg,
                              color: textPrimary,
                              border: `1px solid ${borderColor}`,
                              cursor: onUpdateSortOrder ? 'pointer' : 'default',
                            }}
                          >
                            {cfg.sort_order ?? 0}
                          </Text>
                        </Space>
                      </div>

                      {/* 倍率换算公式与同步策略盒 */}
                      <div
                        style={{
                          padding: '7px 10px',
                          borderRadius: 6,
                          background: subtleBg,
                          border: `1px solid ${borderColor}`,
                          display: 'flex',
                          flexDirection: 'column',
                          gap: 5,
                          fontSize: 11,
                        }}
                      >
                        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 4 }}>
                          <span style={{ color: axisColor }}>
                            同步分组：<strong style={{ color: textPrimary }}>{upGroup || '未配置自动同步分组'}</strong>
                          </span>
                          <Tag style={{ margin: 0, fontSize: 10, padding: '0 5px', lineHeight: '15px', height: 17 }}>
                            {syncInterval > 0 ? `每 ${syncInterval} 分钟自动同步` : '手动同步模式'}
                          </Tag>
                        </div>

                        <div style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 4 }}>
                          <Tag style={{ margin: 0, fontSize: 10, padding: '0 5px', lineHeight: '15px', height: 17 }}>
                            上游原始: {latestSuccessLog ? `${latestSuccessLog.upstream_ratio}x` : `${cfg.rate ?? 1}x`}
                          </Tag>
                          {hasConv ? (
                            <Tag style={{ margin: 0, fontSize: 10, padding: '0 5px', lineHeight: '15px', height: 17 }}>
                              换算: {upCurr}→{stCurr} (汇率 {currRate})
                            </Tag>
                          ) : (
                            <Tag style={{ margin: 0, fontSize: 10, padding: '0 5px', lineHeight: '15px', height: 17 }}>
                              币种: 同本站 ({stCurr || 'USD'})
                            </Tag>
                          )}
                          {rateAdd > 0 && (
                            <Tag style={{ margin: 0, fontSize: 10, padding: '0 5px', lineHeight: '15px', height: 17 }}>
                              叠加增量 +{rateAdd}
                            </Tag>
                          )}
                          <span style={{ fontWeight: 700, color: textPrimary, marginLeft: 'auto' }}>
                            = 本站 {cfg.rate ?? 1}x
                          </span>
                        </div>
                      </div>

                      {/* 端点地址与鉴权状态 */}
                      <div
                        style={{
                          padding: '6px 9px',
                          borderRadius: 6,
                          background: subtleBg,
                          border: `1px solid ${borderColor}`,
                          display: 'flex',
                          flexDirection: 'column',
                          gap: 4,
                          fontSize: 11,
                        }}
                      >
                        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                          <Text type="secondary" style={{ fontSize: 11, flexShrink: 0 }}>端点地址</Text>
                          <Tooltip title={cfg.base_url || '-'}>
                            <Text
                              code
                              style={{
                                fontSize: 11,
                                overflow: 'hidden',
                                textOverflow: 'ellipsis',
                                whiteSpace: 'nowrap',
                                maxWidth: 230,
                              }}
                            >
                              {cfg.base_url || '-'}
                            </Text>
                          </Tooltip>
                        </div>

                        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 6 }}>
                          <span style={{ color: axisColor }}>
                            <KeyOutlined style={{ marginRight: 3 }} />
                            凭证状态
                          </span>
                          <Space size={6} wrap>
                            <span style={{ color: hasApiKey ? textPrimary : axisColor, fontSize: 11 }}>
                              {hasApiKey ? <><CheckCircleOutlined style={{ color: '#10b981' }} /> API密钥已配</> : <><CloseCircleOutlined /> 未配API密钥</>}
                            </span>
                            {cfg.upstream_system === 'newapi' && (
                              <span style={{ color: axisColor, fontSize: 11 }}>
                                {hasAccessToken
                                  ? `· 令牌已配 (UID: ${cfg.upstream_user_id || '-'})`
                                  : '· 公开分组模式'}
                              </span>
                            )}
                          </Space>
                        </div>
                      </div>

                      {/* 火山引擎上游账户余额与 AK/SK 凭证状态 */}
                      {showVolcSection && (
                        <div
                          style={{
                            padding: '7px 10px',
                            borderRadius: 6,
                            background: subtleBg,
                            border: `1px solid ${borderColor}`,
                            display: 'flex',
                            flexDirection: 'column',
                            gap: 5,
                            fontSize: 11,
                          }}
                        >
                          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 6 }}>
                            <span style={{ color: axisColor, fontWeight: 500 }}>
                              火山引擎账户可用余额
                            </span>
                            <Space size={6} style={{ alignItems: 'center' }}>
                              <span
                                style={{
                                  fontSize: 14,
                                  fontWeight: 700,
                                  color: volcBalanceNum !== null
                                    ? (volcBalanceNum > 0 ? '#10b981' : '#ef4444')
                                    : axisColor,
                                }}
                              >
                                {volcBalanceNum !== null
                                  ? `¥${volcBalanceNum.toFixed(2)}`
                                  : (hasVolcCreds ? (isVolcBalanceLoading ? '查询中...' : '待查询') : '未配置')}
                              </span>
                              {hasVolcCreds && onRefreshVolcBalance ? (
                                <Button
                                  type="link"
                                  size="small"
                                  icon={<SyncOutlined spin={isVolcBalanceLoading} />}
                                  loading={isVolcBalanceLoading}
                                  onClick={() => onRefreshVolcBalance(cfg, false)}
                                  style={{ padding: 0, height: 18, fontSize: 11 }}
                                >
                                  刷新
                                </Button>
                              ) : (
                                <Button
                                  type="link"
                                  size="small"
                                  onClick={() => onEditConfig(cfg)}
                                  style={{ padding: 0, height: 18, fontSize: 11 }}
                                >
                                  去配置 AK/SK
                                </Button>
                              )}
                            </Space>
                          </div>

                          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 4 }}>
                            <span style={{ color: axisColor }}>
                              AK/SK 凭证：
                              {hasVolcCreds ? (
                                <span style={{ color: textPrimary, fontFamily: 'monospace' }}>
                                  {cfg.volc_ak ? `${cfg.volc_ak.slice(0, 10)}...` : '已配置'}
                                </span>
                              ) : (
                                <span>未填写</span>
                              )}
                            </span>
                            {cfg.upstream_balance_updated_at && (
                              <Text type="secondary" style={{ fontSize: 10 }}>
                                查询于 {formatApiDateTime(cfg.upstream_balance_updated_at)}
                              </Text>
                            )}
                          </div>
                        </div>
                      )}

                      {/* 配额状态展示 */}
                      <div
                        style={{
                          padding: '6px 9px',
                          borderRadius: 6,
                          background: subtleBg,
                          border: `1px solid ${borderColor}`,
                          display: 'flex',
                          flexDirection: 'column',
                          gap: 4,
                          fontSize: 11,
                        }}
                      >
                        {configuredQuotas.length === 0 ? (
                          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                            <Text type="secondary" style={{ fontSize: 11 }}>额度限制</Text>
                            <span>
                              无限额度 · 累计已用 <strong>{formatNum(cfg.quota_used)}</strong>
                            </span>
                          </div>
                        ) : (
                          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                            {configuredQuotas.map(q => {
                              const pct = q.limit > 0 ? Math.min(100, Math.round((q.used / q.limit) * 100)) : 0;
                              return (
                                <div key={q.label} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                                  <Text type="secondary" style={{ fontSize: 11, width: 42, flexShrink: 0 }}>{q.label}</Text>
                                  <Progress
                                    percent={pct}
                                    size="small"
                                    strokeColor={pct >= 100 ? '#ef4444' : primaryLineColor}
                                    style={{ flex: 1, margin: 0 }}
                                  />
                                  <span style={{ fontSize: 10, color: axisColor, flexShrink: 0 }}>
                                    {formatNum(q.used)} / {formatNum(q.limit)}
                                  </span>
                                </div>
                              );
                            })}
                          </div>
                        )}
                      </div>

                      {/* 绑定的下游模型渠道分组列表 */}
                      <div
                        style={{
                          padding: '6px 9px',
                          borderRadius: 6,
                          background: subtleBg,
                          border: `1px solid ${borderColor}`,
                          display: 'flex',
                          flexDirection: 'column',
                          gap: 5,
                          fontSize: 11,
                        }}
                      >
                        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                          <span style={{ color: axisColor }}>
                            <LinkOutlined style={{ marginRight: 4 }} />
                            关联下游模型渠道分组 ({boundChannels.length})
                          </span>
                          {boundChannels.some(ch => ch.has_custom_schedule) && (
                            <Tag style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16 }}>
                              含独立调度
                            </Tag>
                          )}
                        </div>

                        {boundChannels.length === 0 ? (
                          <Text type="secondary" style={{ fontSize: 11 }}>暂未绑定任何下游模型渠道分组</Text>
                        ) : (
                          <div
                            style={{
                              maxHeight: 84,
                              overflowY: 'auto',
                              display: 'flex',
                              flexDirection: 'column',
                              gap: 4,
                              paddingRight: 2,
                            }}
                          >
                            {boundChannels.map(ch => {
                              const effP = ch.effective_priority ?? cfg.priority ?? 0;
                              const effW = ch.effective_weight ?? cfg.weight ?? 1;
                              return (
                                <div
                                  key={`${ch.id}-${ch.is_ha ? 'ha' : 'direct'}`}
                                  style={{
                                    display: 'flex',
                                    alignItems: 'center',
                                    justifyContent: 'space-between',
                                    gap: 6,
                                    padding: '3px 6px',
                                    borderRadius: 4,
                                    background: cardBg,
                                    border: `1px solid ${borderColor}`,
                                  }}
                                >
                                  <div style={{ display: 'flex', alignItems: 'center', gap: 4, minWidth: 0, flex: 1 }}>
                                    <span
                                      style={{
                                        fontWeight: 500,
                                        fontSize: 11,
                                        overflow: 'hidden',
                                        textOverflow: 'ellipsis',
                                        whiteSpace: 'nowrap',
                                        color: ch.status === 0 ? axisColor : textPrimary,
                                      }}
                                      title={ch.name}
                                    >
                                      {ch.name}
                                    </span>
                                    <Tag style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 15, flexShrink: 0 }}>
                                      {ch.is_ha ? 'HA' : '预设'}
                                    </Tag>
                                    {ch.status === 0 && (
                                      <Tag style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 15, flexShrink: 0, color: axisColor }}>
                                        禁用
                                      </Tag>
                                    )}
                                    {ch.custom_status === 0 && (
                                      <Tag style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 15, flexShrink: 0, color: axisColor }}>
                                        本组关
                                      </Tag>
                                    )}
                                  </div>
                                  <Space size={4} style={{ flexShrink: 0 }}>
                                    <Tag style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 15 }}>
                                      P{effP}/W{effW}
                                    </Tag>
                                    <Link
                                      to={channelEditPath(adminPath, ch.id)}
                                      style={{ fontSize: 11, color: axisColor }}
                                    >
                                      编辑
                                    </Link>
                                  </Space>
                                </div>
                              );
                            })}
                          </div>
                        )}
                      </div>
                    </div>

                    {cfg.remark && (
                      <div style={{ fontSize: 11, color: axisColor, borderTop: `1px dashed ${borderColor}`, paddingTop: 5 }}>
                        备注：{cfg.remark}
                      </div>
                    )}
                  </div>

                  {/* 第二栏（中）：该上游渠道专属的倍率变动趋势曲线 */}
                  <div
                    style={{
                      background: panelBg,
                      border: `1px solid ${borderColor}`,
                      borderRadius: 8,
                      padding: '10px 12px',
                      display: 'flex',
                      flexDirection: 'column',
                      justifyContent: 'space-between',
                      minWidth: 0,
                    }}
                  >
                    <div>
                      <div
                        style={{
                          display: 'flex',
                          alignItems: 'center',
                          justifyContent: 'space-between',
                          flexWrap: 'wrap',
                          gap: 6,
                          marginBottom: 8,
                        }}
                      >
                        <Space size={5} wrap>
                          <LineChartOutlined style={{ color: axisColor, fontSize: 13 }} />
                          <span style={{ fontWeight: 600, fontSize: 12 }}>倍率变动趋势曲线（按天）</span>
                          <Tag style={{ margin: 0, fontSize: 10, padding: '0 5px', lineHeight: '15px', height: 17 }}>
                            区间 {chartInfo.minRate === chartInfo.maxRate ? `${chartInfo.minRate}x` : `${chartInfo.minRate}x ~ ${chartInfo.maxRate}x`}
                          </Tag>
                          {chartInfo.changedDaysCount > 0 ? (
                            <Tag style={{ margin: 0, fontSize: 10, padding: '0 5px', lineHeight: '15px', height: 17 }}>
                              变动 {chartInfo.changedDaysCount} 天 / {chartInfo.changedCount} 次
                            </Tag>
                          ) : (
                            <Tag style={{ margin: 0, fontSize: 10, padding: '0 5px', lineHeight: '15px', height: 17 }}>
                              近 {rowChartDays} 天倍率平稳
                            </Tag>
                          )}
                        </Space>

                        <Segmented
                          size="small"
                          value={rowChartDays}
                          onChange={val =>
                            setRowChartDaysMap(prev => ({
                              ...prev,
                              [cfg.id]: val as ChartDaysRange,
                            }))
                          }
                          options={[
                            { label: '15天', value: 15 },
                            { label: '30天', value: 30 },
                            { label: '60天', value: 60 },
                          ]}
                        />
                      </div>

                      <Spin spinning={isLoadingLogs && rawLogs.length === 0}>
                        <ErrorBoundary>
                          <div style={{ width: '100%', height: 225 }}>
                            <ResponsiveContainer width="100%" height="100%">
                              <LineChart
                                data={chartInfo.points}
                                margin={{ top: 8, right: 16, left: 0, bottom: 2 }}
                              >
                                <CartesianGrid strokeDasharray="3 3" stroke={gridStroke} vertical={false} />
                                <XAxis
                                  dataKey="timeLabel"
                                  stroke={axisColor}
                                  tick={{ fill: axisColor, fontSize: 10 }}
                                  tickLine={false}
                                  axisLine={{ stroke: gridStroke }}
                                  minTickGap={14}
                                />
                                <YAxis
                                  stroke={axisColor}
                                  tick={{ fill: axisColor, fontSize: 10 }}
                                  tickLine={false}
                                  axisLine={false}
                                  width={44}
                                  domain={['auto', 'auto']}
                                  tickFormatter={(v: number) => `${Number(v.toFixed(4))}x`}
                                />
                                <RechartsTooltip
                                  content={({ active, payload }) => {
                                    if (!active || !payload || payload.length === 0) return null;
                                    const row = (payload[0]?.payload || {}) as ChartPointItem;
                                    const delta = Number(row.delta ?? 0);
                                    const rowHasConv = Boolean(
                                      row.upstream_currency &&
                                        row.site_currency &&
                                        String(row.upstream_currency).toLowerCase() !==
                                          String(row.site_currency).toLowerCase(),
                                    );
                                    return (
                                      <div
                                        style={{
                                          background: isLight ? '#ffffff' : '#1f1f1f',
                                          border: `1px solid ${borderColor}`,
                                          borderRadius: 8,
                                          padding: '8px 12px',
                                          boxShadow: '0 6px 20px rgba(0,0,0,0.15)',
                                          fontSize: 12,
                                          minWidth: 225,
                                        }}
                                      >
                                        <div style={{ fontWeight: 600, marginBottom: 3 }}>
                                          {cfg.name} <Text type="secondary" style={{ fontSize: 11 }}>({cfg.yid || '-'})</Text>
                                        </div>
                                        <div style={{ color: axisColor, fontSize: 11, marginBottom: 6 }}>
                                          {row.fullTime} ·{' '}
                                          {row.syncCount > 0
                                            ? `当日同步 ${row.syncCount} 次${row.changeCount > 0 ? `（变动 ${row.changeCount} 次）` : ''}`
                                            : '当日无新同步记录（延续生效倍率）'}
                                        </div>
                                        <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
                                          <div>
                                            同步分组：<Tag style={{ margin: 0, fontSize: 11 }}>{row.upstream_group}</Tag>
                                          </div>
                                          <div>
                                            上游分组原始倍率：<strong>{row.upstream_ratio}x</strong>
                                          </div>
                                          {rowHasConv && (
                                            <div>
                                              汇率换算：{row.upstream_currency} → {row.site_currency} (汇率 {row.currency_rate})
                                            </div>
                                          )}
                                          {Number(row.rate_add) > 0 && <div>叠加增量：+{row.rate_add}</div>}
                                          {row.minDayRate !== row.maxDayRate && (
                                            <div>
                                              当日波动区间：{row.minDayRate}x ~ {row.maxDayRate}x
                                            </div>
                                          )}
                                          <div
                                            style={{
                                              marginTop: 2,
                                              paddingTop: 4,
                                              borderTop: `1px dashed ${borderColor}`,
                                            }}
                                          >
                                            当日生效倍率：<strong style={{ color: primaryLineColor, fontSize: 13 }}>{row.applied_rate}x</strong>
                                            <span
                                              style={{
                                                marginLeft: 6,
                                                fontSize: 11,
                                                color: axisColor,
                                              }}
                                            >
                                              ({delta !== 0 ? `日初 ${row.previous_rate}x, ${delta > 0 ? `+${delta}` : delta}x` : '当日保持不变'})
                                            </span>
                                          </div>
                                        </div>
                                      </div>
                                    );
                                  }}
                                />
                                <Legend wrapperStyle={{ fontSize: 11, paddingTop: 4 }} />
                                <Line
                                  type="monotone"
                                  dataKey="applied_rate"
                                  name="本站生效渠道倍率"
                                  stroke={primaryLineColor}
                                  strokeWidth={2.2}
                                  dot={{ r: rowChartDays >= 60 ? 2 : rowChartDays >= 30 ? 2.5 : 3 }}
                                  activeDot={{ r: 5 }}
                                />
                                <Line
                                  type="monotone"
                                  dataKey="upstream_ratio"
                                  name="上游分组原始倍率"
                                  stroke={secondaryLineColor}
                                  strokeWidth={1.8}
                                  strokeDasharray="5 4"
                                  dot={{ r: rowChartDays >= 60 ? 1.5 : rowChartDays >= 30 ? 2 : 2.5 }}
                                  activeDot={{ r: 4.5 }}
                                />
                              </LineChart>
                            </ResponsiveContainer>
                          </div>
                        </ErrorBoundary>
                      </Spin>
                    </div>
                  </div>

                  {/* 第三栏（右）：该上游分组的同步历史记录 */}
                  <div
                    style={{
                      background: panelBg,
                      border: `1px solid ${borderColor}`,
                      borderRadius: 8,
                      padding: '10px 12px',
                      display: 'flex',
                      flexDirection: 'column',
                      minWidth: 0,
                    }}
                  >
                    <div
                      style={{
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        flexWrap: 'wrap',
                        gap: 6,
                        marginBottom: 8,
                      }}
                    >
                      <Space size={4} wrap>
                        <ClockCircleOutlined style={{ color: axisColor, fontSize: 12 }} />
                        <span style={{ fontWeight: 600, fontSize: 12 }}>同步历史记录</span>
                      </Space>

                      <Segmented
                        size="small"
                        value={rowLogFilter}
                        onChange={val =>
                          setRowLogFilterMap(prev => ({
                            ...prev,
                            [cfg.id]: val as RowLogFilter,
                          }))
                        }
                        options={[
                          { label: `全部 (${rawLogs.length})`, value: 'all' },
                          { label: `变动 (${changedLogsCount})`, value: 'changed' },
                          ...(errorLogsCount > 0 ? [{ label: `异常 (${errorLogsCount})`, value: 'error' }] : []),
                        ]}
                      />
                    </div>

                    <div
                      style={{
                        height: 225,
                        overflowY: 'auto',
                        display: 'flex',
                        flexDirection: 'column',
                        gap: 6,
                        paddingRight: 2,
                      }}
                    >
                      {isLoadingLogs && rawLogs.length === 0 ? (
                        <div style={{ height: '100%', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                          <Text type="secondary" style={{ fontSize: 12 }}>正在加载同步记录...</Text>
                        </div>
                      ) : filteredRowLogs.length === 0 ? (
                        <div
                          style={{
                            height: '100%',
                            display: 'flex',
                            flexDirection: 'column',
                            alignItems: 'center',
                            justifyContent: 'center',
                            textAlign: 'center',
                            padding: '0 12px',
                            gap: 6,
                          }}
                        >
                          <Text type="secondary" style={{ fontSize: 12 }}>
                            {rawLogs.length === 0
                              ? upGroup
                                ? `分组「${upGroup}」暂无同步历史记录`
                                : '该渠道尚未绑定自动同步分组'
                              : '当前筛选下暂无匹配记录'}
                          </Text>
                          {rawLogs.length === 0 && canManualSync && (
                            <Button
                              size="small"
                              type="link"
                              icon={<SyncOutlined spin={isSyncing} />}
                              onClick={() => onManualSyncRate(cfg)}
                              style={{ fontSize: 12, padding: 0 }}
                            >
                              点击立即拉取并记录首次倍率
                            </Button>
                          )}
                        </div>
                      ) : (
                        filteredRowLogs.map(log => {
                          const isSuccess = log.status === 1;
                          const logUpCurr = (log.upstream_currency || '').trim();
                          const logStCurr = (log.site_currency || '').trim();
                          const logHasConv = Boolean(
                            logUpCurr && logStCurr && logUpCurr.toLowerCase() !== logStCurr.toLowerCase(),
                          );
                          const delta = Number(((log.applied_rate ?? 0) - (log.previous_rate ?? 0)).toFixed(4));
                          const rateChanged = isSuccess && Math.abs(delta) > 0.00001;

                          return (
                            <div
                              key={log.id}
                              style={{
                                padding: '6px 9px',
                                borderRadius: 6,
                                background: isSuccess
                                  ? rateChanged
                                    ? subtleBg
                                    : cardBg
                                  : (isLight ? '#fff2f0' : 'rgba(255, 77, 79, 0.12)'),
                                border: isSuccess
                                  ? `1px solid ${borderColor}`
                                  : (isLight ? '1px solid #ffccc7' : '1px solid rgba(255, 77, 79, 0.3)'),
                                display: 'flex',
                                flexDirection: 'column',
                                gap: 4,
                              }}
                            >
                              <div
                                style={{
                                  display: 'flex',
                                  alignItems: 'center',
                                  justifyContent: 'space-between',
                                  gap: 6,
                                  flexWrap: 'wrap',
                                }}
                              >
                                <Space size={4} wrap>
                                  <span
                                    style={{
                                      display: 'inline-flex',
                                      alignItems: 'center',
                                      gap: 4,
                                      fontSize: 10,
                                      color: isSuccess ? textPrimary : '#ef4444',
                                      fontWeight: 500,
                                    }}
                                  >
                                    <span
                                      style={{
                                        width: 5,
                                        height: 5,
                                        borderRadius: '50%',
                                        backgroundColor: isSuccess ? '#10b981' : '#ef4444',
                                      }}
                                    />
                                    {isSuccess ? '成功' : '失败'}
                                  </span>
                                  <Tag style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 15 }}>
                                    {log.trigger_type === 'manual'
                                      ? '手动'
                                      : log.sync_interval_minutes > 0
                                        ? `定时(${log.sync_interval_minutes}分)`
                                        : '自动'}
                                  </Tag>
                                  <Tag style={{ margin: 0, fontSize: 10, padding: '0 5px', lineHeight: '14px', height: 15 }}>
                                    {log.upstream_group || upGroup || '-'}
                                  </Tag>
                                </Space>
                                <Text type="secondary" style={{ fontSize: 10, whiteSpace: 'nowrap' }}>
                                  {log.created_at ? formatApiDateTime(log.created_at) : '-'}
                                </Text>
                              </div>

                              {isSuccess ? (
                                <div
                                  style={{
                                    display: 'flex',
                                    alignItems: 'center',
                                    justifyContent: 'space-between',
                                    flexWrap: 'wrap',
                                    gap: 4,
                                    fontSize: 11,
                                  }}
                                >
                                  <span style={{ color: axisColor, fontSize: 10 }}>
                                    上游 {log.upstream_ratio}x
                                    {logHasConv ? ` · ${logUpCurr}→${logStCurr}(${log.currency_rate})` : ''}
                                    {log.rate_add > 0 ? ` · +${log.rate_add}` : ''}
                                  </span>

                                  {rateChanged ? (
                                    <span
                                      style={{
                                        fontSize: 11,
                                        fontWeight: 600,
                                        color: textPrimary,
                                      }}
                                    >
                                      {log.previous_rate}x <SwapRightOutlined style={{ fontSize: 10, color: axisColor }} /> {log.applied_rate}x (
                                      {delta > 0 ? `+${delta}` : delta}x)
                                    </span>
                                  ) : (
                                    <span style={{ fontSize: 10, color: axisColor }}>
                                      保持 {log.applied_rate}x
                                    </span>
                                  )}
                                </div>
                              ) : (
                                <div style={{ fontSize: 11, color: '#ef4444', wordBreak: 'break-all' }}>
                                  {log.error_message || '同步失败'}
                                </div>
                              )}
                            </div>
                          );
                        })
                      )}
                    </div>
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
};

export default UpstreamSystemDetailPanel;
