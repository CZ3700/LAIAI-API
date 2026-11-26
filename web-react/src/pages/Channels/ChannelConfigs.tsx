import React, { useEffect, useState, useMemo } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { Table, Button, Space, Tag, Modal, Form, Input, InputNumber, Switch, Segmented, message, Popconfirm, Card, Typography, AutoComplete, Grid, Tooltip, Progress, Select, Divider, TimePicker, Popover, Checkbox } from 'antd';
import MobileCardList, { MobileCard, CardRow, CardActions } from '../../components/MobileCardList';
import { listPagination, useListPager } from '../../components/ListPagination';
import { TableStickyHorizontalScroll } from '../../components/TableStickyHorizontalScroll';
import { PlusOutlined, EditOutlined, DeleteOutlined, SyncOutlined, ClearOutlined, StopOutlined, PlayCircleOutlined, SettingOutlined, CheckSquareOutlined, CheckCircleOutlined, CopyOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import dayjs from 'dayjs';
import request from '../../utils/request';
import { formatApiDateTime, timedisplayOffsetSuffix } from '../../utils/timedisplay';
import useSettingsStore from '../../store/settings';
import { useThemeStore } from '../../store/theme';
import type { ChannelConfig, ChannelCategory, Upstream } from '../../types';
import ChannelCategoryManager from '../../components/Channels/ChannelCategoryManager';
import UpstreamSystemDetailPanel, { type ChannelConfigSyncLog } from './components/UpstreamSystemDetailPanel';
import { channelEditPath } from './channelPaths';
import {
  formatQuotaLimitDisplay,
  parseQuotaLimitInput,
  validateQuotaHierarchy,
  isFiniteQuotaLimit,
  compareChannelQuota,
  quotaPeriodItems,
  quotaRingPercent,
  QUOTA_SORT_METRICS,
  QUOTA_RING_BLUE,
  type QuotaSortPeriod,
  type QuotaSortMetric,
} from '../../utils/quotaPeriod';

const { Title, Text } = Typography;
const { useBreakpoint } = Grid;

const UPSTREAM_SYSTEM_OPTIONS = [
  { value: '兼容', label: '兼容' },
  { value: '官方', label: '官方' },
  { value: 'newapi', label: 'newapi' },
  { value: 'Tkeapi', label: 'Tkeapi' },
  { value: '火山引擎', label: '火山引擎' },
  { value: '阿里云', label: '阿里云' },
];

const UPSTREAM_CURRENCY_OPTIONS = [
  { value: 'CNY', label: 'CNY (人民币 ¥ / RMB)' },
  { value: 'USD', label: 'USD (美元 $)' },
  { value: '', label: '同本站 / 无需换算' },
];

const SITE_CURRENCY_OPTIONS = [
  { value: 'USD', label: 'USD (美元 $)' },
  { value: 'CNY', label: 'CNY (人民币 ¥ / RMB)' },
];

type UpstreamGroupOption = { name: string; ratio: number; label: string };

function convertCurrencyRatio(
  groupRatio: number,
  upstreamCurrency?: string,
  siteCurrency?: string,
  currencyRate?: number,
): number {
  const u = (upstreamCurrency || '').trim();
  const s = (siteCurrency || '').trim();
  if (!u || !s || u.toLowerCase() === s.toLowerCase()) {
    return groupRatio;
  }
  const rate = Number(currencyRate) > 0 ? Number(currencyRate) : 1.0;
  const isCny = (c: string) => {
    const lower = c.toLowerCase();
    return lower === 'cny' || lower === 'rmb' || lower === '人民币' || lower === '元';
  };
  const isUsd = (c: string) => {
    const lower = c.toLowerCase();
    return lower === 'usd' || lower === '美元' || lower === '$';
  };

  if (isCny(u) && isUsd(s)) {
    return groupRatio / rate;
  } else if (isUsd(u) && isCny(s)) {
    return groupRatio * rate;
  }
  return groupRatio / rate;
}

function appliedChannelRate(
  groupRatio: number,
  add: number,
  upstreamCurrency?: string,
  siteCurrency?: string,
  currencyRate?: number,
) {
  const extra = Number(add);
  const ratio = Number(groupRatio) || 0;
  const converted = convertCurrencyRatio(ratio, upstreamCurrency, siteCurrency, currencyRate);
  const total = Math.max(0, converted + (Number.isFinite(extra) && extra > 0 ? extra : 0));
  return Number(total.toFixed(4));
}

/** 判断 Base URL 是否属于火山引擎端点 */
function isVolcengineBaseUrl(url?: string): boolean {
  const lower = String(url || '').trim().toLowerCase();
  if (!lower) return false;
  return (
    lower.includes('volces.com') ||
    lower.includes('volcengineapi.com') ||
    lower.includes('volcengine.com')
  );
}

/** 紧凑展示在「日额度」标签后：01:00 (UTC+8) · 冷30分 */
function formatDailyResetInline(
  hour: number,
  minute: number,
  cooldown: number,
  tzSuffix: string,
): string {
  const hh = String(Math.min(23, Math.max(0, hour))).padStart(2, '0');
  const mm = String(Math.min(59, Math.max(0, minute))).padStart(2, '0');
  const cool = Math.max(0, cooldown);
  const timeText = `${hh}:${mm}${tzSuffix}`;
  return cool > 0 ? `${timeText} · 冷${cool}分` : timeText;
}

const ChannelConfigs: React.FC = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { systemKey: routeSystemKey } = useParams<{ systemKey?: string }>();
  const activeDetailSystem = useMemo(
    () => (routeSystemKey ? decodeURIComponent(routeSystemKey) : null),
    [routeSystemKey],
  );
  const screens = useBreakpoint();
  const { themeMode } = useThemeStore();
  const isLight = themeMode === 'light';
  const { settings } = useSettingsStore();
  const quotaTz = settings?.site?.default_timezone || 'Asia/Shanghai';
  const adminPath = localStorage.getItem('laiai_admin_path') || 'admin';
  const [configs, setConfigs] = useState<ChannelConfig[]>([]);
  const [categories, setCategories] = useState<ChannelCategory[]>([]);
  const [providers, setProviders] = useState<ChannelCategory[]>([]);
  const [loading, setLoading] = useState(true);
  const [isModalVisible, setIsModalVisible] = useState(false);
  const [editingConfig, setEditingConfig] = useState<ChannelConfig | null>(null);
  const [upstreams, setUpstreams] = useState<{ id: number, name: string }[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [searchText, setSearchText] = useState('');
  const [statusFilter, setStatusFilter] = useState<number | 'all'>('all');
  const [categoryFilter, setCategoryFilter] = useState<number | 'all' | 'unclassified'>('all');
  const [providerFilter, setProviderFilter] = useState<'all' | '__unclassified__' | string>('all');
  const [upstreamSystemFilter, setUpstreamSystemFilter] = useState<'all' | '__unclassified__' | string>('all');
  const [enableQuota, setEnableQuota] = useState(false);
  const [isCategoryManagerVisible, setIsCategoryManagerVisible] = useState(false);
  const [isProviderManagerVisible, setIsProviderManagerVisible] = useState(false);
  const [dailyResetModalOpen, setDailyResetModalOpen] = useState(false);
  const [dailyResetDraft, setDailyResetDraft] = useState({ hour: 0, minute: 0, cooldown: 0 });
  const [upstreamGroups, setUpstreamGroups] = useState<UpstreamGroupOption[]>([]);
  const [selectedGroupRatio, setSelectedGroupRatio] = useState<number | null>(null);
  const [fetchingGroups, setFetchingGroups] = useState(false);
  const [syncAddEnabled, setSyncAddEnabled] = useState(false);
  const [syncLogsMap, setSyncLogsMap] = useState<Record<number, ChannelConfigSyncLog[]>>({});
  const [syncLogsLoadingMap, setSyncLogsLoadingMap] = useState<Record<number, boolean>>({});
  const [syncTriggerLoadingMap, setSyncTriggerLoadingMap] = useState<Record<number, boolean>>({});
  const [volcBalanceLoadingMap, setVolcBalanceLoadingMap] = useState<Record<number, boolean>>({});
  const [modalVolcBalance, setModalVolcBalance] = useState<number | null>(null);
  const [modalVolcLoading, setModalVolcLoading] = useState(false);
  const volcAutoFetchedRef = React.useRef<Set<number>>(new Set());
  const [quotaSortPeriod, setQuotaSortPeriod] = useState<QuotaSortPeriod>('auto');
  const [quotaSortMetric, setQuotaSortMetric] = useState<QuotaSortMetric>('rate');
  const [quotaSortOrder, setQuotaSortOrder] = useState<'ascend' | 'descend' | null>(null);
  const [form] = Form.useForm();
  const baseUrlWatch = Form.useWatch('base_url', form);
  const upstreamSystem = Form.useWatch('upstream_system', form);
  const upstreamGroup = Form.useWatch('upstream_group', form);
  const upstreamCurrency = Form.useWatch('upstream_currency', form);
  const siteCurrency = Form.useWatch('site_currency', form);
  const upstreamCurrencyRate = Form.useWatch('upstream_currency_rate', form);
  const upstreamSyncRateAdd = Form.useWatch('upstream_sync_rate_add', form);
  const upstreamSyncIntervalMinutes = Form.useWatch('upstream_sync_interval_minutes', form);
  const showVolcCredentials = Boolean(isVolcengineBaseUrl(baseUrlWatch) || upstreamSystem === '火山引擎');

  const handleQueryVolcBalance = async (record: ChannelConfig, silent = false) => {
    if (!record.id || volcBalanceLoadingMap[record.id]) return;
    setVolcBalanceLoadingMap(prev => ({ ...prev, [record.id]: true }));
    try {
      const resp = await (request.post('/channel-configs/volc-balance', {
        config_id: record.id,
      }) as unknown as Promise<{
        success?: boolean;
        available_balance?: number;
        updated_at?: string;
      }>);
      if (resp.available_balance !== undefined) {
        const nowStr = resp.updated_at || new Date().toISOString();
        setConfigs(prev => prev.map(c => c.id === record.id ? {
          ...c,
          upstream_balance: resp.available_balance!,
          upstream_balance_updated_at: nowStr,
        } : c));
        if (editingConfig && editingConfig.id === record.id) {
          setEditingConfig(prev => prev ? {
            ...prev,
            upstream_balance: resp.available_balance!,
            upstream_balance_updated_at: nowStr,
          } : prev);
          setModalVolcBalance(resp.available_balance!);
        }
        if (!silent) {
          message.success(`火山引擎账户可用余额: ¥${Number(resp.available_balance).toFixed(2)}`);
        }
      }
    } catch (e) {
      if (!silent) console.error(e);
    } finally {
      setVolcBalanceLoadingMap(prev => ({ ...prev, [record.id]: false }));
    }
  };

  const handleModalQueryVolcBalance = async () => {
    const ak = String(form.getFieldValue('volc_ak') || '').trim();
    const sk = String(form.getFieldValue('volc_sk') || '').trim();
    if (!ak && !editingConfig?.volc_ak) {
      message.warning('请先填写火山引擎 Access Key ID');
      return;
    }
    if (!sk && !editingConfig?.has_volc_credentials) {
      message.warning('请先填写火山引擎 Secret Access Key');
      return;
    }
    setModalVolcLoading(true);
    try {
      const resp = await (request.post('/channel-configs/volc-balance', {
        config_id: editingConfig?.id,
        volc_ak: ak,
        volc_sk: sk,
      }) as unknown as Promise<{
        success?: boolean;
        available_balance?: number;
        updated_at?: string;
      }>);
      if (resp.available_balance !== undefined) {
        setModalVolcBalance(resp.available_balance);
        if (editingConfig?.id) {
          const nowStr = resp.updated_at || new Date().toISOString();
          setConfigs(prev => prev.map(c => c.id === editingConfig.id ? {
            ...c,
            upstream_balance: resp.available_balance!,
            upstream_balance_updated_at: nowStr,
          } : c));
        }
        message.success(`已获取火山引擎账户余额: ¥${Number(resp.available_balance).toFixed(2)}`);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setModalVolcLoading(false);
    }
  };

  const fetchSyncLogs = async (configId: number) => {
    if (!configId) return;
    setSyncLogsLoadingMap(prev => ({ ...prev, [configId]: true }));
    try {
      const resp = await (request.get(`/channel-configs/${configId}/sync-logs`) as unknown as Promise<{ data?: ChannelConfigSyncLog[] }>);
      setSyncLogsMap(prev => ({ ...prev, [configId]: resp.data || [] }));
    } catch (e) {
      console.error(e);
    } finally {
      setSyncLogsLoadingMap(prev => ({ ...prev, [configId]: false }));
    }
  };

  const handleManualSyncRate = async (record: ChannelConfig) => {
    if (!record.id || syncTriggerLoadingMap[record.id]) return;
    setSyncTriggerLoadingMap(prev => ({ ...prev, [record.id]: true }));
    try {
      const resp = await (request.post(`/channel-configs/${record.id}/sync-rate`) as unknown as Promise<{
        success?: boolean;
        upstream_ratio?: number;
        rate?: number;
        upstream_synced_at?: string;
      }>);
      if (resp.rate !== undefined) {
        setConfigs(prev => prev.map(c => c.id === record.id ? {
          ...c,
          rate: resp.rate!,
          upstream_synced_at: resp.upstream_synced_at || c.upstream_synced_at,
        } : c));
        if (editingConfig && editingConfig.id === record.id) {
          setEditingConfig(prev => prev ? {
            ...prev,
            rate: resp.rate!,
            upstream_synced_at: resp.upstream_synced_at || prev.upstream_synced_at,
          } : prev);
          form.setFieldsValue({ rate: resp.rate });
        }
      }
      message.success(`已同步分组倍率，当前渠道倍率: ${resp.rate}x`);
      await fetchSyncLogs(record.id);
    } catch (e) {
      console.error(e);
      await fetchSyncLogs(record.id);
    } finally {
      setSyncTriggerLoadingMap(prev => ({ ...prev, [record.id]: false }));
    }
  };

  const renderSyncLogsHeader = (record: ChannelConfig) => {
    const interval = Number(record.upstream_sync_interval_minutes) || 0;
    const isSyncing = Boolean(syncTriggerLoadingMap[record.id]);
    const isLoading = Boolean(syncLogsLoadingMap[record.id]);
    return (
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, fontSize: 12, fontWeight: 600, paddingBottom: 4 }}>
        <Space size={6} wrap>
          <span>同步分组倍率历史记录</span>
          <Tag color="cyan" style={{ margin: 0, padding: '0 5px', fontSize: 10, lineHeight: '15px', height: 17, borderRadius: 3 }}>
            {interval > 0 ? `每 ${interval} 分钟` : '手动同步'}
          </Tag>
        </Space>
        <Space size={4}>
          <Button
            type="link"
            size="small"
            icon={<SyncOutlined spin={isSyncing} />}
            loading={isSyncing}
            onClick={(e) => {
              e.stopPropagation();
              handleManualSyncRate(record);
            }}
            style={{ padding: '0 4px', fontSize: 11, height: 20 }}
          >
            立即同步
          </Button>
          <Button
            type="link"
            size="small"
            disabled={isLoading}
            onClick={(e) => {
              e.stopPropagation();
              fetchSyncLogs(record.id);
            }}
            style={{ padding: '0 4px', fontSize: 11, height: 20 }}
          >
            刷新
          </Button>
        </Space>
      </div>
    );
  };

  const renderSyncLogsList = (record: ChannelConfig, maxHeight = 260) => {
    const logs = syncLogsMap[record.id] || [];
    const isLoading = Boolean(syncLogsLoadingMap[record.id]);
    if (isLoading && logs.length === 0) {
      return (
        <div style={{ padding: '16px 12px', textAlign: 'center', minWidth: 320 }}>
          <Text type="secondary" style={{ fontSize: 12 }}>加载同步记录中...</Text>
        </div>
      );
    }
    if (logs.length === 0) {
      return (
        <div style={{ padding: '14px 12px', textAlign: 'center', minWidth: 320 }}>
          <Text type="secondary" style={{ fontSize: 12 }}>
            暂无同步历史记录（将按同步间隔自动记录或点击右上角「立即同步」）
          </Text>
        </div>
      );
    }
    return (
      <div style={{ maxHeight, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 6, minWidth: 340, maxWidth: 480 }}>
        {logs.map((log) => {
          const isSuccess = log.status === 1;
          const upCurr = (log.upstream_currency || '').trim();
          const stCurr = (log.site_currency || '').trim();
          const hasConv = Boolean(upCurr && stCurr && upCurr.toLowerCase() !== stCurr.toLowerCase());
          const rateChanged = Math.abs((log.previous_rate ?? 0) - (log.applied_rate ?? 0)) > 0.00001;
          return (
            <div
              key={log.id}
              style={{
                display: 'flex',
                flexDirection: 'column',
                gap: 4,
                padding: '6px 8px',
                borderRadius: 6,
                background: isSuccess
                  ? (isLight ? '#f8fafc' : 'rgba(255,255,255,0.05)')
                  : (isLight ? '#fff2f0' : 'rgba(255,77,79,0.12)'),
                border: isSuccess
                  ? (isLight ? '1px solid #e2e8f0' : '1px solid rgba(255,255,255,0.08)')
                  : (isLight ? '1px solid #ffccc7' : '1px solid rgba(255,77,79,0.3)'),
              }}
            >
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                <Space size={4} wrap>
                  <Tag
                    color={isSuccess ? 'success' : 'error'}
                    style={{ margin: 0, padding: '0 4px', fontSize: 10, lineHeight: '14px', height: 16, borderRadius: 3 }}
                  >
                    {isSuccess ? '成功' : '失败'}
                  </Tag>
                  <Tag
                    color={log.trigger_type === 'manual' ? 'purple' : 'cyan'}
                    style={{ margin: 0, padding: '0 4px', fontSize: 10, lineHeight: '14px', height: 16, borderRadius: 3 }}
                  >
                    {log.trigger_type === 'manual'
                      ? '手动同步'
                      : (log.sync_interval_minutes > 0 ? `定时(${log.sync_interval_minutes}分)` : '自动同步')}
                  </Tag>
                  <Tag style={{ margin: 0, padding: '0 4px', fontSize: 10, lineHeight: '14px', height: 16, borderRadius: 3 }}>
                    {log.upstream_group || record.upstream_group || '-'}
                    {isSuccess ? ` ${log.upstream_ratio}x` : ''}
                  </Tag>
                  {hasConv && (
                    <Tag color="geekblue" style={{ margin: 0, padding: '0 4px', fontSize: 10, lineHeight: '14px', height: 16, borderRadius: 3 }}>
                      {upCurr}→{stCurr} ({log.currency_rate})
                    </Tag>
                  )}
                  {log.rate_add > 0 && (
                    <Tag color="orange" style={{ margin: 0, padding: '0 4px', fontSize: 10, lineHeight: '14px', height: 16, borderRadius: 3 }}>
                      +{log.rate_add}
                    </Tag>
                  )}
                </Space>
                <Text type="secondary" style={{ fontSize: 11, whiteSpace: 'nowrap', flexShrink: 0 }}>
                  {log.created_at ? formatApiDateTime(log.created_at) : '-'}
                </Text>
              </div>
              {isSuccess ? (
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', fontSize: 11 }}>
                  <span style={{ color: isLight ? '#595959' : 'rgba(255,255,255,0.65)' }}>
                    上游分组倍率 <strong>{log.upstream_ratio}x</strong>
                    {rateChanged ? ` · 原渠道倍率 ${log.previous_rate}x` : ' · 倍率保持一致'}
                  </span>
                  <Tag
                    color={rateChanged ? 'blue' : 'default'}
                    style={{ margin: 0, padding: '0 5px', fontSize: 10, lineHeight: '15px', height: 17, borderRadius: 3, fontWeight: 600 }}
                  >
                    {rateChanged ? `${log.previous_rate}x → ${log.applied_rate}x` : `渠道倍率 ${log.applied_rate}x`}
                  </Tag>
                </div>
              ) : (
                <div style={{ fontSize: 11, color: '#ff4d4f', wordBreak: 'break-all' }}>
                  {log.error_message || '同步失败'}
                </div>
              )}
            </div>
          );
        })}
      </div>
    );
  };

  const renderUpstreamSyncInline = (record: ChannelConfig) => {
    const system = (record.upstream_system || '').trim();
    const group = (record.upstream_group || '').trim();
    const hasVolcBalance = record.upstream_balance !== null && record.upstream_balance !== undefined;
    const isVolc = system === '火山引擎' || hasVolcBalance || Boolean(record.has_volc_credentials);
    const tagStyle: React.CSSProperties = { margin: 0, padding: '0 5px', fontSize: 11, height: 19, lineHeight: '17px', borderRadius: 4 };

    if (!group && isVolc) {
      const balNum = hasVolcBalance ? Number(record.upstream_balance) : null;
      const tip = [
        `上游 ${system || '火山引擎'}`,
        balNum !== null ? `账户可用余额: ¥${balNum.toFixed(2)}` : (record.has_volc_credentials ? '已配置火山引擎 AK/SK' : null),
        record.upstream_balance_updated_at ? `上次查询: ${formatApiDateTime(record.upstream_balance_updated_at)}` : null,
      ].filter(Boolean).join('\n');
      return (
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-start', gap: 1, minWidth: 0 }}>
          <Tooltip title={<span style={{ whiteSpace: 'pre-line' }}>{tip}</span>}>
            <Tag color="volcano" style={tagStyle}>{system || '火山引擎'}</Tag>
          </Tooltip>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, alignItems: 'center' }}>
            {balNum !== null && (
              <Tooltip title={<span style={{ whiteSpace: 'pre-line' }}>{tip}</span>}>
                <Tag color={balNum > 0 ? 'green' : 'error'} style={tagStyle}>
                  余额 ¥{balNum.toFixed(2)}
                </Tag>
              </Tooltip>
            )}
            <Tag
              color="processing"
              style={{ ...tagStyle, cursor: 'pointer' }}
              onClick={(e) => {
                e.stopPropagation();
                navigate(`/${adminPath}/channel-configs/system/${encodeURIComponent(system || '火山引擎')}`);
              }}
            >
              详细
            </Tag>
          </div>
        </div>
      );
    }

    if (!system || !group) return null;
    const interval = Number(record.upstream_sync_interval_minutes) || 0;
    const add = Number(record.upstream_sync_rate_add) || 0;
    const rate = record.rate ?? 1;
    const upCurr = (record.upstream_currency || '').trim();
    const stCurr = (record.site_currency || '').trim();
    const hasConversion = Boolean(upCurr && stCurr && upCurr.toLowerCase() !== stCurr.toLowerCase());
    const currRate = Number(record.upstream_currency_rate) > 0 ? Number(record.upstream_currency_rate) : 7.2;

    const tip = [
      `上游 ${system}`,
      `分组 ${group}（渠道倍率 ${rate}x）`,
      hasConversion ? `货币换算: ${upCurr} → ${stCurr} (汇率 ${currRate})` : null,
      interval > 0 ? `每 ${interval} 分钟同步` : '不自动同步',
      add > 0 ? `同步增量 +${add}` : null,
      record.upstream_synced_at ? `上次同步 ${formatApiDateTime(record.upstream_synced_at)}` : null,
    ].filter(Boolean).join('\n');
    return (
      <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-start', gap: 1, minWidth: 0 }}>
        <Tooltip title={<span style={{ whiteSpace: 'pre-line' }}>{tip}</span>}>
          <Tag color="blue" style={tagStyle}>{system}</Tag>
        </Tooltip>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, alignItems: 'center' }}>
          <Tooltip title={<span style={{ whiteSpace: 'pre-line' }}>{tip}</span>}>
            <span style={{ display: 'inline-flex', flexWrap: 'wrap', gap: 4 }}>
              <Tag style={tagStyle}>{group} {rate}x</Tag>
              {hasConversion ? <Tag color="geekblue" style={tagStyle}>{upCurr}→{stCurr}</Tag> : null}
              {add > 0 ? <Tag color="orange" style={tagStyle}>+{add}</Tag> : null}
              {interval > 0 ? <Tag color="cyan" style={tagStyle}>每{interval}分</Tag> : null}
            </span>
          </Tooltip>
          <Popover
            placement="bottom"
            trigger={['hover', 'click']}
            onOpenChange={(open) => {
              if (open) {
                fetchSyncLogs(record.id);
              }
            }}
            title={renderSyncLogsHeader(record)}
            content={renderSyncLogsList(record)}
          >
            <Tag
              color="processing"
              style={{ ...tagStyle, cursor: 'pointer' }}
              onClick={(e) => e.stopPropagation()}
            >
              详细
            </Tag>
          </Popover>
        </div>
      </div>
    );
  };

  const fetchConfigs = async () => {
    setLoading(true);
    try {
      const resp = await (request.get('/channel-configs') as unknown as Promise<{ data: ChannelConfig[] }>);
      setConfigs(resp.data || []);
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  const fetchCategories = async () => {
    try {
      const resp = await (request.get('/channel-categories') as any);
      setCategories(Array.isArray(resp) ? resp : (resp?.data || []));
    } catch (e) {
      console.error(e);
    }
  };

  const fetchProviders = async () => {
    try {
      const resp = await (request.get('/channel-configs/providers') as any);
      setProviders(Array.isArray(resp) ? resp : (resp?.data || []));
    } catch (e) {
      console.error(e);
    }
  };

  const fetchUpstreams = async () => {
    try {
      const pResp = await (request.get('/upstreams') as unknown as Promise<Upstream[]>);
      setUpstreams(pResp || []);
    } catch (e) {
      console.error(e);
    }
  };

  useEffect(() => {
    fetchConfigs();
    fetchCategories();
    fetchProviders();
    fetchUpstreams();
  }, []);

  const resolveCategoryName = (categoryId?: number | null) => {
    if (!categoryId) return '';
    return categories.find(c => c.id === categoryId)?.name || '';
  };

  const resolveRecordCategoryIds = (record: { category_ids?: number[]; category_id?: number | null }): number[] => {
    if (Array.isArray(record.category_ids) && record.category_ids.length > 0) {
      return record.category_ids;
    }
    if (record.category_id != null && record.category_id !== undefined && record.category_id > 0) {
      return [record.category_id];
    }
    return [];
  };

  const activeCategories = categories.filter(c => c.is_active === 1 || c.is_active === true);

  const allProviders = useMemo(() => {
    const seen = new Set(providers.map(p => (p.name || '').trim()).filter(Boolean));
    const extra: ChannelCategory[] = [];
    configs.forEach(c => {
      const pt = (c.provider_type || '').trim();
      if (pt && !seen.has(pt)) {
        seen.add(pt);
        extra.push({ id: -extra.length - 1, name: pt, sort_order: 0, is_active: 1, created_at: '', updated_at: '' });
      }
    });
    return [...providers, ...extra];
  }, [providers, configs]);

  const activeProviders = useMemo(
    () => allProviders.filter(p => p.is_active === 1 || p.is_active === true),
    [allProviders],
  );

  useEffect(() => {
    if (
      providerFilter !== 'all' &&
      providerFilter !== '__unclassified__' &&
      allProviders.length > 0 &&
      !allProviders.some(p => (p.name || '').trim() === providerFilter)
    ) {
      setProviderFilter('all');
    }
  }, [allProviders, providerFilter]);

  const allUpstreamSystems = useMemo(() => {
    const seen = new Set(UPSTREAM_SYSTEM_OPTIONS.map(o => o.value));
    const extra: { value: string; label: string }[] = [];
    configs.forEach(c => {
      const sys = (c.upstream_system || '').trim();
      if (sys && !seen.has(sys)) {
        seen.add(sys);
        extra.push({ value: sys, label: sys });
      }
    });
    return [...UPSTREAM_SYSTEM_OPTIONS, ...extra];
  }, [configs]);

  useEffect(() => {
    if (
      upstreamSystemFilter !== 'all' &&
      upstreamSystemFilter !== '__unclassified__' &&
      allUpstreamSystems.length > 0 &&
      !allUpstreamSystems.some(s => s.value === upstreamSystemFilter)
    ) {
      setUpstreamSystemFilter('all');
    }
  }, [allUpstreamSystems, upstreamSystemFilter]);

  const resolveConfigUpstreamSystem = (ch: ChannelConfig): string => {
    const explicit = (ch.upstream_system || '').trim();
    if (explicit) return explicit;
    if (isVolcengineBaseUrl(ch.base_url) || ch.has_volc_credentials || (ch.volc_ak || '').trim()) {
      return '火山引擎';
    }
    return '';
  };

  const upstreamSystemOptionItems = useMemo(
    () => [
      ...allUpstreamSystems.map(s => ({
        key: s.value,
        label: s.label,
        count: configs.filter(ch => resolveConfigUpstreamSystem(ch) === s.value).length,
      })),
      {
        key: '__unclassified__',
        label: '未分类',
        count: configs.filter(ch => !resolveConfigUpstreamSystem(ch)).length,
      },
    ],
    [allUpstreamSystems, configs],
  );

  const detailSystemConfigs = useMemo(() => {
    if (!activeDetailSystem) return [];
    const list =
      activeDetailSystem === '__unclassified__'
        ? configs.filter(c => !resolveConfigUpstreamSystem(c))
        : configs.filter(c => resolveConfigUpstreamSystem(c) === activeDetailSystem);
    return [...list].sort((a, b) => ((b.sort_order ?? 0) - (a.sort_order ?? 0)) || (b.id - a.id));
  }, [configs, activeDetailSystem]);

  useEffect(() => {
    if (!activeDetailSystem || detailSystemConfigs.length === 0) return;
    detailSystemConfigs.forEach(c => {
      if (c.id && syncLogsMap[c.id] === undefined && !syncLogsLoadingMap[c.id]) {
        fetchSyncLogs(c.id);
      }
      if (
        c.id &&
        (c.has_volc_credentials || (c.volc_ak || '').trim()) &&
        !volcAutoFetchedRef.current.has(c.id) &&
        !volcBalanceLoadingMap[c.id]
      ) {
        volcAutoFetchedRef.current.add(c.id);
        handleQueryVolcBalance(c, true);
      }
    });
  }, [activeDetailSystem, detailSystemConfigs]);

  const updateCalculatedRate = (
    groupName?: string,
    addValue?: number,
    upCurr?: string,
    stCurr?: string,
    cRate?: number,
    syncEnabledOverride?: boolean,
    ratioOverride?: number,
  ) => {
    const name = groupName !== undefined ? groupName : form.getFieldValue('upstream_group');
    const hit = name ? upstreamGroups.find(g => g.name === name) : undefined;

    let ratio = 3.0;
    if (ratioOverride !== undefined && Number.isFinite(ratioOverride) && ratioOverride > 0) {
      ratio = ratioOverride;
    } else if (hit && typeof hit.ratio === 'number') {
      ratio = hit.ratio;
      setSelectedGroupRatio(hit.ratio);
    } else if (selectedGroupRatio !== null && selectedGroupRatio !== undefined && selectedGroupRatio > 0) {
      ratio = selectedGroupRatio;
    }

    const isSyncAddOn = syncEnabledOverride !== undefined ? syncEnabledOverride : syncAddEnabled;
    const add = isSyncAddOn ? (addValue !== undefined ? addValue : Number(form.getFieldValue('upstream_sync_rate_add') || 0)) : 0;
    const uCurr = upCurr !== undefined ? upCurr : form.getFieldValue('upstream_currency');
    const sCurr = stCurr !== undefined ? stCurr : form.getFieldValue('site_currency');
    const rawRate = cRate !== undefined ? cRate : form.getFieldValue('upstream_currency_rate');
    const currRate = Number(rawRate) > 0 ? Number(rawRate) : 7.2;

    const nextRate = appliedChannelRate(ratio, add, uCurr, sCurr, currRate);
    form.setFieldsValue({ rate: nextRate });
  };

  const loadUpstreamGroups = async (cfg?: ChannelConfig, silent = false) => {
    const activeConfig = cfg || editingConfig;
    const baseUrl = String(cfg?.base_url || form.getFieldValue('base_url') || '').trim();
    const apiKey = String(cfg?.api_key ?? form.getFieldValue('api_key') ?? '');
    const upstreamUserId = String(cfg?.upstream_user_id ?? form.getFieldValue('upstream_user_id') ?? '').trim();
    const upstreamAccessToken = String(cfg?.upstream_access_token ?? form.getFieldValue('upstream_access_token') ?? '');
    if (!baseUrl) {
      if (!silent) message.warning('请先填写端点基础地址');
      return;
    }
    const cleanToken = upstreamAccessToken && !upstreamAccessToken.startsWith('••••') && !upstreamAccessToken.startsWith('****')
      ? upstreamAccessToken.trim()
      : '';
    if ((cleanToken && !upstreamUserId) || (!cleanToken && upstreamUserId)) {
      if (!silent) message.warning('使用系统访问令牌拉取私有分组时，请同时填写 NewAPI 用户 ID 和系统访问令牌');
      return;
    }
    setFetchingGroups(true);
    try {
      const resp = await (request.post('/channel-configs/upstream-groups', {
        config_id: activeConfig?.id,
        base_url: baseUrl,
        api_key: apiKey,
        upstream_system: 'newapi',
        upstream_user_id: upstreamUserId,
        upstream_access_token: upstreamAccessToken,
      }) as Promise<{ data?: UpstreamGroupOption[] }>);
      const list = resp.data || [];
      setUpstreamGroups(list);
      const current = cfg?.upstream_group || form.getFieldValue('upstream_group');
      if (current && list.some(g => g.name === current)) {
        const hit = list.find(g => g.name === current);
        if (hit) {
          setSelectedGroupRatio(hit.ratio);
          if (!silent) {
            updateCalculatedRate(current, undefined, undefined, undefined, undefined, undefined, hit.ratio);
          }
        }
      }
      if (!silent) {
        if (list.length === 0) {
          message.info('上游未返回分组倍率');
        } else {
          message.success(`已拉取 ${list.length} 个分组`);
        }
      }
    } catch (e) {
      if (!silent) console.error(e);
    } finally {
      setFetchingGroups(false);
    }
  };

  const handleAdd = () => {
    setEditingConfig(null);
    setEnableQuota(false);
    form.resetFields();
    const defaultSiteCurrency = settings?.currency?.default_currency || 'USD';
    form.setFieldsValue({
      provider_type: undefined,
      sort_order: 0,
      rate: 1.0,
      priority: 0,
      weight: 1,
      status: 1,
      category_ids: [],
      category_id: null,
      quota_limit: -1,
      daily_quota_limit: -1,
      weekly_quota_limit: -1,
      monthly_quota_limit: -1,
      daily_reset_hour: 0,
      daily_reset_minute: 0,
      daily_reset_cooldown_minutes: 0,
      upstream_system: undefined,
      upstream_group: undefined,
      upstream_sync_interval_minutes: 0,
      upstream_sync_rate_add: 0,
      upstream_currency: 'CNY',
      site_currency: defaultSiteCurrency,
      upstream_currency_rate: 7.2,
      volc_ak: '',
      volc_sk: '',
    });
    setUpstreamGroups([]);
    setSelectedGroupRatio(null);
    setSyncAddEnabled(false);
    setModalVolcBalance(null);
    setModalVolcLoading(false);
    setIsModalVisible(true);
  };

  const handleEdit = (record: ChannelConfig) => {
    setEditingConfig(record);
    const q = record.quota_limit ?? -1;
    const dq = record.daily_quota_limit ?? -1;
    const wq = record.weekly_quota_limit ?? -1;
    const mq = record.monthly_quota_limit ?? -1;
    setEnableQuota(q >= 0 || dq >= 0 || wq >= 0 || mq >= 0);
    form.resetFields();
    const defaultSiteCurrency = settings?.currency?.default_currency || 'USD';
    const rawRate = Number(record.rate ?? 1);
    const safeRate = Number.isFinite(rawRate) ? Number(rawRate.toFixed(4)) : 1;
    const initialCategoryIds = resolveRecordCategoryIds(record);
    const resolvedSystem = resolveConfigUpstreamSystem(record);
    form.setFieldsValue({
      ...record,
      provider_type: record.provider_type || undefined,
      rate: safeRate,
      status: record.status ?? 1,
      category_ids: initialCategoryIds,
      category_id: record.category_id ?? (initialCategoryIds[0] ?? null),
      quota_limit: record.quota_limit ?? -1,
      daily_quota_limit: record.daily_quota_limit ?? -1,
      weekly_quota_limit: record.weekly_quota_limit ?? -1,
      monthly_quota_limit: record.monthly_quota_limit ?? -1,
      daily_reset_hour: record.daily_reset_hour ?? 0,
      daily_reset_minute: record.daily_reset_minute ?? 0,
      daily_reset_cooldown_minutes: record.daily_reset_cooldown_minutes ?? 0,
      upstream_system: resolvedSystem || undefined,
      upstream_group: record.upstream_group || undefined,
      upstream_sync_interval_minutes: record.upstream_sync_interval_minutes ?? 0,
      upstream_sync_rate_add: record.upstream_sync_rate_add ?? 0,
      upstream_currency: record.upstream_currency || (record.upstream_system === 'newapi' ? 'CNY' : undefined),
      site_currency: record.site_currency || defaultSiteCurrency,
      upstream_currency_rate: (record.upstream_currency_rate && record.upstream_currency_rate > 0) ? record.upstream_currency_rate : 7.2,
      upstream_user_id: record.upstream_user_id || '',
      upstream_access_token: record.upstream_access_token || '',
      volc_ak: record.volc_ak || '',
      volc_sk: record.volc_sk || '',
    });
    setUpstreamGroups([]);
    setSelectedGroupRatio(null);
    setSyncAddEnabled((record.upstream_sync_rate_add ?? 0) > 0);
    setModalVolcBalance(record.upstream_balance ?? null);
    setModalVolcLoading(false);
    setIsModalVisible(true);
    if (record.upstream_system === 'newapi') {
      fetchSyncLogs(record.id);
      if (record.base_url) {
        loadUpstreamGroups(record, true);
      }
    }
  };

  const handleDelete = async (id: number) => {
    try {
      await request.delete(`/channel-configs/${id}`);
      message.success(t('common.success'));
      fetchConfigs();
    } catch (e) {
      console.error(e);
    }
  };

  const [copyTarget, setCopyTarget] = useState<ChannelConfig | null>(null);
  const [copyName, setCopyName] = useState('');
  const [copying, setCopying] = useState(false);

  const openCopyModal = (record: ChannelConfig) => {
    setCopyTarget(record);
    setCopyName(record.name ? `${record.name} 副本` : '副本');
  };

  const handleCopy = async () => {
    if (!copyTarget || copying) return;
    const trimmedName = copyName.trim();
    if (!trimmedName) {
      message.warning('请输入新的配置名称');
      return;
    }
    setCopying(true);
    try {
      await request.post(`/channel-configs/${copyTarget.id}/copy`, { name: trimmedName });
      message.success('复制成功');
      setCopyTarget(null);
      fetchConfigs();
    } catch (e) {
      console.error(e);
    } finally {
      setCopying(false);
    }
  };

  const handleToggleStatus = async (record: ChannelConfig) => {
    try {
      const newStatus = (record.status ?? 1) === 1 ? 0 : 1;
      await request.put(`/channel-configs/${record.id}`, { status: newStatus });
      setConfigs(prev => prev.map(c => c.id === record.id ? { ...c, status: newStatus } : c));
      message.success(newStatus === 1 ? '已启用上游渠道' : '已禁用上游渠道');
    } catch (e) {
      console.error(e);
    }
  };

  const handleUpdateSortOrder = async (id: number, sort_order: number) => {
    setConfigs(prev =>
      prev
        .map(c => (c.id === id ? { ...c, sort_order } : c))
        .sort((a, b) => ((b.sort_order ?? 0) - (a.sort_order ?? 0)) || (b.id - a.id)),
    );
    try {
      await request.put(`/channel-configs/${id}`, { sort_order });
    } catch (e) {
      console.error(e);
      fetchConfigs();
    }
  };

  const [isBatchEditMode, setIsBatchEditMode] = useState(false);
  const [selectedRowKeys, setSelectedRowKeys] = useState<React.Key[]>([]);
  const [batchLoading, setBatchLoading] = useState(false);
  const [batchCategoryKey, setBatchCategoryKey] = useState(0);
  const { page: currentPage, pageSize, setPage: setCurrentPage, onChange: onPageChange } = useListPager();

  const batchChecked = (id: number) => selectedRowKeys.some(k => Number(k) === id);

  const toggleBatchKey = (id: number, checked: boolean) => {
    setSelectedRowKeys(prev => {
      const has = prev.some(k => Number(k) === id);
      if (checked) return has ? prev : [...prev, id];
      return prev.filter(k => Number(k) !== id);
    });
  };

  const handleBatchChangeStatus = async (status: number) => {
    const idSet = new Set(selectedRowKeys.map(k => Number(k)));
    const targets = configs.filter(c => idSet.has(c.id));
    if (targets.length === 0) return;
    setBatchLoading(true);
    try {
      await Promise.all(targets.map(c => request.put(`/channel-configs/${c.id}`, { status })));
      const done = new Set(targets.map(c => c.id));
      setConfigs(prev => prev.map(c => done.has(c.id) ? { ...c, status } : c));
      message.success(status === 1 ? `已激活 ${targets.length} 个上游渠道` : `已禁用 ${targets.length} 个上游渠道`);
    } catch (e) {
      console.error(e);
      fetchConfigs();
    } finally {
      setBatchLoading(false);
    }
  };

  const handleBatchChangeCategory = async (categoryId: number | null) => {
    const idSet = new Set(selectedRowKeys.map(k => Number(k)));
    const targets = configs.filter(c => idSet.has(c.id));
    if (targets.length === 0) return;
    setBatchLoading(true);
    try {
      const category_ids = categoryId == null ? [] : [categoryId];
      await Promise.all(targets.map(c => request.put(`/channel-configs/${c.id}`, { category_id: categoryId, category_ids })));
      const done = new Set(targets.map(c => c.id));
      setConfigs(prev => prev.map(c => done.has(c.id) ? { ...c, category_id: categoryId, category_ids } : c));
      const name = categoryId == null ? '未分类' : (categories.find(c => c.id === categoryId)?.name || '所选分类');
      message.success(`已将 ${targets.length} 个上游渠道改为「${name}」`);
    } catch (e) {
      console.error(e);
      fetchConfigs();
    } finally {
      setBatchLoading(false);
    }
  };

  const handleBatchDelete = async () => {
    const ids = configs.filter(c => batchChecked(c.id)).map(c => c.id);
    if (ids.length === 0) return;
    setBatchLoading(true);
    try {
      await Promise.all(ids.map(id => request.delete(`/channel-configs/${id}`)));
      message.success(`已删除 ${ids.length} 个上游渠道`);
      setSelectedRowKeys([]);
      setConfigs(prev => prev.filter(c => !ids.includes(c.id)));
    } catch (e) {
      console.error(e);
      fetchConfigs();
    } finally {
      setBatchLoading(false);
    }
  };

  const handleResetQuota = async (id: number) => {
    try {
      await request.post(`/channel-configs/${id}/quota/reset`);
      message.success('已清零上游预设已用额度');
      fetchConfigs();
    } catch (e) {
      console.error(e);
    }
  };

  const openDailyResetModal = () => {
    setDailyResetDraft({
      hour: Number(form.getFieldValue('daily_reset_hour') ?? 0),
      minute: Number(form.getFieldValue('daily_reset_minute') ?? 0),
      cooldown: Number(form.getFieldValue('daily_reset_cooldown_minutes') ?? 0),
    });
    setDailyResetModalOpen(true);
  };

  const saveDailyResetModal = () => {
    form.setFieldsValue({
      daily_reset_hour: Math.min(23, Math.max(0, Number(dailyResetDraft.hour) || 0)),
      daily_reset_minute: Math.min(59, Math.max(0, Number(dailyResetDraft.minute) || 0)),
      daily_reset_cooldown_minutes: Math.max(0, Number(dailyResetDraft.cooldown) || 0),
    });
    setDailyResetModalOpen(false);
  };

  const closeConfigModal = () => {
    setDailyResetModalOpen(false);
    setIsModalVisible(false);
    setUpstreamGroups([]);
    setSelectedGroupRatio(null);
    setFetchingGroups(false);
    setModalVolcBalance(null);
    setModalVolcLoading(false);
  };

  const handleSave = async (values: any) => {
    if (submitting) return;
    setSubmitting(true);
    try {
      const catIds: number[] = Array.isArray(values.category_ids)
        ? values.category_ids.map(Number).filter((n: number) => Number.isFinite(n) && n > 0)
        : (values.category_id ? [Number(values.category_id)] : []);
      const primaryCatId = catIds.length > 0 ? catIds[0] : null;
      const trimmedBaseUrl = values.base_url ? values.base_url.trim() : values.base_url;
      const resolvedUpstreamSystem =
        values.upstream_system || (isVolcengineBaseUrl(trimmedBaseUrl) ? '火山引擎' : '');

      const payload = {
        ...values,
        provider_type: values.provider_type ? String(values.provider_type).trim() : '',
        base_url: trimmedBaseUrl,
        sort_order: Number(values.sort_order) || 0,
        rate: values.rate !== undefined && values.rate !== null ? Number(Number(values.rate).toFixed(4)) : 1.0,
        priority: values.priority !== undefined && values.priority !== null ? Number(values.priority) : 0,
        weight: values.weight !== undefined && values.weight !== null ? Number(values.weight) : 1,
        status: values.status === 0 ? 0 : 1,
        category_id: primaryCatId,
        category_ids: catIds,
        quota_limit: (!enableQuota || values.quota_limit === undefined || values.quota_limit === null) ? -1 : Number(values.quota_limit),
        daily_quota_limit: (!enableQuota || values.daily_quota_limit === undefined || values.daily_quota_limit === null) ? -1 : Number(values.daily_quota_limit),
        weekly_quota_limit: (!enableQuota || values.weekly_quota_limit === undefined || values.weekly_quota_limit === null) ? -1 : Number(values.weekly_quota_limit),
        monthly_quota_limit: (!enableQuota || values.monthly_quota_limit === undefined || values.monthly_quota_limit === null) ? -1 : Number(values.monthly_quota_limit),
        daily_reset_hour: Math.min(23, Math.max(0, Number(values.daily_reset_hour) || 0)),
        daily_reset_minute: Math.min(59, Math.max(0, Number(values.daily_reset_minute) || 0)),
        daily_reset_cooldown_minutes: Math.max(0, Number(values.daily_reset_cooldown_minutes) || 0),
        upstream_system: resolvedUpstreamSystem,
        upstream_group: resolvedUpstreamSystem === 'newapi' ? (values.upstream_group || '') : '',
        upstream_sync_interval_minutes: resolvedUpstreamSystem === 'newapi'
          ? Math.max(0, Number(values.upstream_sync_interval_minutes) || 0)
          : 0,
        upstream_sync_rate_add: resolvedUpstreamSystem === 'newapi' && syncAddEnabled
          ? Number((Math.max(0, Number(values.upstream_sync_rate_add) || 0)).toFixed(4))
          : 0,
        upstream_currency: resolvedUpstreamSystem === 'newapi' ? (values.upstream_currency || '') : '',
        site_currency: resolvedUpstreamSystem === 'newapi' ? (values.site_currency || '') : '',
        upstream_currency_rate: resolvedUpstreamSystem === 'newapi'
          ? (Number(values.upstream_currency_rate) > 0 ? Number(Number(values.upstream_currency_rate).toFixed(4)) : 1.0)
          : 1.0,
        upstream_user_id: resolvedUpstreamSystem === 'newapi' ? String(values.upstream_user_id || '').trim() : '',
        upstream_access_token: resolvedUpstreamSystem === 'newapi' ? values.upstream_access_token : '',
        volc_ak: values.volc_ak !== undefined ? String(values.volc_ak || '').trim() : '',
        volc_sk: values.volc_sk,
      };
      if (enableQuota) {
        const hierarchyErr = validateQuotaHierarchy(payload);
        if (hierarchyErr) {
          message.error(hierarchyErr);
          setSubmitting(false);
          return;
        }
      }
      if (editingConfig) {
        // 密钥未修改（与加载时原值相同）时或未填写时不提交，防止覆盖；但如果显式清空(等于空字符串)，则提交给后端处理
        if (payload.api_key === undefined || payload.api_key === editingConfig.api_key) {
          delete payload.api_key;
        } else if (typeof payload.api_key === 'string') {
          payload.api_key = payload.api_key.trim();
        }
        if (payload.upstream_access_token === undefined || payload.upstream_access_token === editingConfig.upstream_access_token) {
          delete payload.upstream_access_token;
        } else if (typeof payload.upstream_access_token === 'string') {
          payload.upstream_access_token = payload.upstream_access_token.trim();
        }
        if (payload.volc_sk === undefined || payload.volc_sk === editingConfig.volc_sk) {
          delete payload.volc_sk;
        } else if (typeof payload.volc_sk === 'string') {
          payload.volc_sk = payload.volc_sk.trim();
        }
        await request.put(`/channel-configs/${editingConfig.id}`, payload);
        message.success(t('common.success'));
      } else {
        if (typeof payload.api_key === 'string') {
          payload.api_key = payload.api_key.trim();
        }
        if (typeof payload.upstream_access_token === 'string') {
          payload.upstream_access_token = payload.upstream_access_token.trim();
        }
        if (typeof payload.volc_sk === 'string') {
          payload.volc_sk = payload.volc_sk.trim();
        }
        await request.post('/channel-configs', payload);
        message.success(t('common.success'));
      }
      setIsModalVisible(false);
      setDailyResetModalOpen(false);
      fetchConfigs();
    } catch (e) {
      console.error(e);
    } finally {
      setSubmitting(false);
    }
  };

  const applyQuotaSort = (period: QuotaSortPeriod, metric = quotaSortMetric) => {
    if (quotaSortPeriod === period && quotaSortMetric === metric && quotaSortOrder) {
      setQuotaSortOrder(quotaSortOrder === 'ascend' ? 'descend' : 'ascend');
      return;
    }
    setQuotaSortPeriod(period);
    setQuotaSortMetric(metric);
    setQuotaSortOrder(metric === 'remain' || metric === 'limit' ? 'ascend' : 'descend');
  };

  const quotaMetricLabel = QUOTA_SORT_METRICS.find((m) => m.key === quotaSortMetric)!.label;
  const quotaSortMark = (period: QuotaSortPeriod) =>
    quotaSortOrder && quotaSortPeriod === period ? (quotaSortOrder === 'ascend' ? '↑' : '↓') : '';

  const renderQuotaCell = (record: ChannelConfig) => {
    const items = quotaPeriodItems(record, quotaTz);
    const fmt = (n: number) => (Number.isInteger(n) ? String(n) : n.toFixed(6));
    const hasAnyConfigured = items.some((item) => item.limit >= 0);

    const slotWidth = 28;
    const ringSize = 24;
    const ringStroke = 5;

    const slotStyle: React.CSSProperties = {
      width: slotWidth,
      display: 'flex',
      flexDirection: 'column',
      alignItems: 'center',
      gap: 1,
      cursor: 'default',
    };
    const labelStyle: React.CSSProperties = {
      fontSize: 9,
      color: isLight ? 'rgba(0,0,0,0.4)' : 'rgba(255,255,255,0.45)',
      lineHeight: 1,
      transform: 'scale(0.92)',
    };

    return (
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: `repeat(4, ${slotWidth}px)`,
          gap: 3,
          alignItems: 'center',
          justifyContent: 'start',
          width: 122,
        }}
      >
        {items.map((item, index) => {
          const configured = item.limit >= 0;
          const showUnlimited = !hasAnyConfigured && index === 0;
          const showRing = configured || showUnlimited;
          const mark = quotaSortMark(item.key);

          if (!showRing) {
            return (
              <div key={item.key} style={{ ...slotStyle, visibility: 'hidden' }} aria-hidden>
                <div style={{ width: ringSize, height: ringSize }} />
                <span style={labelStyle}>{item.label}</span>
              </div>
            );
          }

          const pct = configured ? quotaRingPercent(item.used, item.limit) : 0;
          const tip = showUnlimited
            ? `额度：${fmt(item.used)} / ∞（无限） · 点击按总·${quotaMetricLabel}排序`
            : `${item.label}额度：${fmt(item.used)} / ${fmt(Number(item.limit))}（${pct}%） · 点击按${item.label}·${quotaMetricLabel}排序`;
          const stroke = showUnlimited
            ? (isLight ? '#a1a1aa' : 'rgba(255,255,255,0.28)')
            : (pct >= 100 ? '#ef4444' : QUOTA_RING_BLUE[item.key]);

          return (
            <Tooltip key={item.key} title={tip}>
              <div
                style={{ ...slotStyle, cursor: 'pointer' }}
                onClick={(e) => {
                  e.stopPropagation();
                  applyQuotaSort(item.key);
                }}
              >
                <Progress
                  type="circle"
                  percent={showUnlimited ? 100 : pct}
                  size={ringSize}
                  strokeWidth={ringStroke}
                  strokeColor={stroke}
                  trailColor={isLight ? '#e4e4e7' : 'rgba(255,255,255,0.12)'}
                  format={() => (
                    <span
                      style={{
                        fontSize: showUnlimited ? 9 : 8,
                        fontWeight: 600,
                        color: isLight ? 'rgba(0,0,0,0.72)' : 'rgba(255,255,255,0.88)',
                        lineHeight: 1,
                      }}
                    >
                      {showUnlimited ? '∞' : `${pct}%`}
                    </span>
                  )}
                />
                <span style={{
                  ...labelStyle,
                  color: mark ? '#1677ff' : labelStyle.color,
                  fontWeight: mark ? 700 : undefined,
                }}>
                  {showUnlimited ? '无限' : item.label}{mark}
                </span>
              </div>
            </Tooltip>
          );
        })}
      </div>
    );
  };

  const renderStatusBadge = (status?: number) => {
    const active = (status ?? 1) === 1;
    return (
      <Space size={5} style={{ color: active ? '#52c41a' : '#ff4d4f' }}>
        <div style={{ width: 6, height: 6, borderRadius: '50%', backgroundColor: active ? '#52c41a' : '#ff4d4f' }} />
        <span style={{ fontSize: 12 }}>{active ? t('common.active') : t('common.disabled')}</span>
      </Space>
    );
  };

  const filteredConfigs = configs.filter(config => {
    if (statusFilter !== 'all' && (config.status ?? 1) !== statusFilter) {
      return false;
    }
    const catIds = resolveRecordCategoryIds(config);
    if (categoryFilter === 'unclassified') {
      if (catIds.length > 0) return false;
    } else if (categoryFilter !== 'all') {
      if (!catIds.includes(categoryFilter as number)) return false;
    }
    if (providerFilter === '__unclassified__') {
      if ((config.provider_type || '').trim()) return false;
    } else if (providerFilter !== 'all') {
      if ((config.provider_type || '').trim() !== providerFilter) return false;
    }
    if (upstreamSystemFilter === '__unclassified__') {
      if (resolveConfigUpstreamSystem(config)) return false;
    } else if (upstreamSystemFilter !== 'all') {
      if (resolveConfigUpstreamSystem(config) !== upstreamSystemFilter) return false;
    }
    if (searchText) {
      const searchLower = searchText.toLowerCase();
      const nameMatch = config.name?.toLowerCase().includes(searchLower);
      const yidMatch = config.yid?.toLowerCase().includes(searchLower);
      const providerMatch = config.provider_type?.toLowerCase().includes(searchLower);
      return nameMatch || yidMatch || providerMatch;
    }
    return true;
  });
  const displayedConfigs = quotaSortOrder
    ? [...filteredConfigs].sort((a, b) => {
        const d = compareChannelQuota(a, b, quotaSortPeriod, quotaSortMetric, quotaTz);
        return quotaSortOrder === 'ascend' ? d : -d;
      })
    : filteredConfigs;

  // ===== 当前页渠道配置列表（全选时仅全选当前页） =====
  const currentPageConfigs = useMemo(() => {
    const startIndex = (currentPage - 1) * pageSize;
    return displayedConfigs.slice(startIndex, startIndex + pageSize);
  }, [displayedConfigs, currentPage, pageSize]);

  useEffect(() => {
    setCurrentPage(1);
  }, [searchText, statusFilter, categoryFilter, providerFilter, upstreamSystemFilter, quotaSortOrder, quotaSortMetric, quotaSortPeriod]);

  useEffect(() => {
    const maxPage = Math.ceil(displayedConfigs.length / pageSize);
    if (maxPage > 0 && currentPage > maxPage) {
      setCurrentPage(maxPage);
    }
  }, [displayedConfigs.length, pageSize, currentPage]);

  const renderScheduleCell = (record: ChannelConfig) => {
    const defPriority = record.priority ?? 0;
    const defWeight = record.weight ?? 1;
    const defRate = record.rate ?? 1.0;
    const boundChannels = record.bound_channels || [];
    const customChannels = boundChannels.filter((ch) => ch.has_custom_schedule);
    const hasCustom = customChannels.length > 0;
    const sortedBound = [...boundChannels].sort(
      (a, b) => Number(Boolean(b.has_custom_schedule)) - Number(Boolean(a.has_custom_schedule)),
    );

    return (
      <Space size={4} wrap={false} style={{ alignItems: 'center' }}>
        <Tooltip title="默认优先级（渠道分组未单独设置时采用）">
          <Text type="secondary" style={{ fontSize: 12 }}>P{defPriority}</Text>
        </Tooltip>
        <Tooltip title="默认权重（渠道分组未单独设置时采用）">
          <Text type="secondary" style={{ fontSize: 12 }}>W{defWeight}</Text>
        </Tooltip>
        <Tooltip title="渠道倍率">
          <Tag color="orange" style={{ margin: 0, padding: '0 4px', lineHeight: '16px', height: 18, fontSize: 11, borderRadius: 4 }}>
            {defRate}x
          </Tag>
        </Tooltip>
        {hasCustom && (
          <Popover
            placement="bottom"
            trigger={['hover', 'click']}
            title={
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, fontSize: 12, fontWeight: 600, paddingBottom: 4 }}>
                <span>分组调度详情 ({customChannels.length}/{boundChannels.length} 自定义)</span>
                <Space size={4}>
                  <Text type="secondary" style={{ fontSize: 11, fontWeight: 400 }}>默认:</Text>
                  <Tag style={{ margin: 0, padding: '0 4px', fontSize: 10, lineHeight: '14px', height: 16, borderRadius: 3 }}>P{defPriority}</Tag>
                  <Tag style={{ margin: 0, padding: '0 4px', fontSize: 10, lineHeight: '14px', height: 16, borderRadius: 3 }}>W{defWeight}</Tag>
                </Space>
              </div>
            }
            content={
              <div style={{ maxHeight: 260, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 6, minWidth: 300, maxWidth: 420 }}>
                {sortedBound.map((ch) => {
                  const effP = ch.effective_priority ?? defPriority;
                  const effW = ch.effective_weight ?? defWeight;
                  const isCustomP = ch.custom_priority !== undefined && ch.custom_priority !== null;
                  const isCustomW = ch.custom_weight !== undefined && ch.custom_weight !== null;

                  return (
                    <div
                      key={`${ch.id}-${ch.is_ha ? 'ha' : 'preset'}`}
                      style={{
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        gap: 8,
                        padding: '6px 8px',
                        borderRadius: 6,
                        background: ch.has_custom_schedule
                          ? (isLight ? 'rgba(22,119,255,0.06)' : 'rgba(22,119,255,0.14)')
                          : (isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)'),
                        border: ch.has_custom_schedule
                          ? (isLight ? '1px solid #91caff' : '1px solid rgba(22,119,255,0.4)')
                          : (isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.08)'),
                      }}
                    >
                      <div style={{ display: 'flex', alignItems: 'center', gap: 5, minWidth: 0, flex: 1 }}>
                        <span
                          style={{
                            fontSize: 12,
                            fontWeight: 500,
                            overflow: 'hidden',
                            textOverflow: 'ellipsis',
                            whiteSpace: 'nowrap',
                            color: ch.status === 0 ? 'var(--text-secondary, #8c8c8c)' : undefined,
                          }}
                          title={`${ch.name}${ch.group_aid ? ` (AID: ${ch.group_aid})` : ''}`}
                        >
                          {ch.name}
                        </span>
                        {ch.is_ha ? (
                          <Tag color="purple" style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 2, flexShrink: 0 }}>
                            HA
                          </Tag>
                        ) : (
                          <Tag style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 2, flexShrink: 0 }}>
                            预设
                          </Tag>
                        )}
                        {ch.status === 0 && (
                          <Tag color="error" style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 2, flexShrink: 0 }}>
                            已禁用
                          </Tag>
                        )}
                        {ch.custom_status === 0 && (
                          <Tag color="warning" style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 2, flexShrink: 0 }}>
                            本组已关
                          </Tag>
                        )}
                        {!ch.has_custom_schedule && (
                          <Tag style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 2, flexShrink: 0, opacity: 0.75 }}>
                            默认
                          </Tag>
                        )}
                      </div>
                      <Space size={4} style={{ flexShrink: 0, alignItems: 'center' }}>
                        <Tooltip title={isCustomP ? `当前分组独立设置优先级 P${effP}（默认 P${defPriority}）` : `继承默认优先级 P${defPriority}`}>
                          <Tag
                            color={isCustomP ? 'blue' : undefined}
                            style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 3, fontWeight: isCustomP ? 600 : 400 }}
                          >
                            P{effP}
                          </Tag>
                        </Tooltip>
                        <Tooltip title={isCustomW ? `当前分组独立设置权重 W${effW}（默认 W${defWeight}）` : `继承默认权重 W${defWeight}`}>
                          <Tag
                            color={isCustomW ? 'blue' : undefined}
                            style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 3, fontWeight: isCustomW ? 600 : 400 }}
                          >
                            W{effW}
                          </Tag>
                        </Tooltip>
                        {typeof ch.custom_rpm_limit === 'number' && ch.custom_rpm_limit > 0 && (
                          <Tooltip title={`当前分组独立限制每分钟最多 ${ch.custom_rpm_limit} 次请求`}>
                            <Tag
                              color="blue"
                              style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 3, fontWeight: 600 }}
                            >
                              RPM:{ch.custom_rpm_limit}
                            </Tag>
                          </Tooltip>
                        )}
                        <Link
                          to={channelEditPath(adminPath, ch.id)}
                          style={{ fontSize: 11, color: '#1677ff', flexShrink: 0, textDecoration: 'none', marginLeft: 2 }}
                        >
                          编辑
                        </Link>
                      </Space>
                    </div>
                  );
                })}
              </div>
            }
          >
            <Tag
              color="blue"
              style={{
                margin: 0,
                padding: '0 5px',
                lineHeight: '16px',
                height: 18,
                fontSize: 10,
                borderRadius: 4,
                cursor: 'pointer',
              }}
            >
              自定义({customChannels.length})
            </Tag>
          </Popover>
        )}
      </Space>
    );
  };

  const columns = [
    {
      title: '配置',
      key: 'name',
      width: 280,
      ellipsis: false,
      onCell: () => ({ style: { overflow: 'visible', whiteSpace: 'normal' } }),
      sorter: (a: ChannelConfig, b: ChannelConfig) => (a.name || '').localeCompare(b.name || '', 'zh'),
      render: (_: unknown, record: ChannelConfig) => {
        const sync = renderUpstreamSyncInline(record);
        return (
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0 }}>
            <div style={{ minWidth: 0, display: 'flex', flexDirection: 'column', gap: 1 }}>
              <div style={{ fontWeight: 600, fontSize: 13, lineHeight: 1.25, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{record.name}</div>
              <Typography.Text keyboard style={{ color: '#1677ff', fontSize: 11, lineHeight: 1.2, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', width: 'fit-content' }}>
                YID {record.yid || '-'}
              </Typography.Text>
            </div>
            {sync}
          </div>
        );
      },
    },
    {
      title: '状态',
      dataIndex: 'status',
      key: 'status',
      width: 75,
      render: (status: number) => renderStatusBadge(status),
    },
    {
      title: '上游分类',
      key: 'category_ids',
      width: 120,
      render: (_: unknown, record: ChannelConfig) => {
        const catIds = resolveRecordCategoryIds(record);
        if (catIds.length === 0) {
          return <Text type="secondary" style={{ fontSize: 12 }}>未分类</Text>;
        }
        return (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
            {catIds.map(id => {
              const name = resolveCategoryName(id);
              if (!name) return null;
              return (
                <Tag key={id} style={{ margin: 0, padding: '0 5px', fontSize: 11, height: 19, lineHeight: '17px', borderRadius: 4 }}>
                  {name}
                </Tag>
              );
            })}
          </div>
        );
      },
    },
    {
      title: '绑定分组',
      key: 'bound_channels',
      width: 105,
      align: 'center' as const,
      sorter: (a: ChannelConfig, b: ChannelConfig) => (a.bound_channel_count || 0) - (b.bound_channel_count || 0),
      render: (_: unknown, record: ChannelConfig) => {
        const count = record.bound_channel_count || 0;
        if (count === 0) {
          return <Text type="secondary" style={{ fontSize: 12 }}>未绑定</Text>;
        }
        const channels = record.bound_channels || [];
        return (
          <Popover
            title={
              <div style={{ fontSize: 12, fontWeight: 600, paddingBottom: 4 }}>
                已绑定的模型渠道分组 ({count})
              </div>
            }
            content={
              <div style={{ maxHeight: 240, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 6, minWidth: 260, maxWidth: 360 }}>
                {channels.map((ch) => (
                  <div
                    key={ch.id}
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      gap: 8,
                      padding: '5px 8px',
                      borderRadius: 6,
                      background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)',
                      border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.08)',
                    }}
                  >
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0, flex: 1 }}>
                      <span
                        style={{
                          fontSize: 12,
                          fontWeight: 500,
                          overflow: 'hidden',
                          textOverflow: 'ellipsis',
                          whiteSpace: 'nowrap',
                          color: ch.status === 0 ? 'var(--text-secondary, #8c8c8c)' : undefined,
                        }}
                        title={ch.name}
                      >
                        {ch.name}
                      </span>
                      {ch.status === 0 && (
                        <Tag color="error" style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 2 }}>
                          已禁用
                        </Tag>
                      )}
                      {ch.is_ha && (
                        <Tag color="purple" style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 2 }}>
                          HA
                        </Tag>
                      )}
                      {ch.has_custom_schedule && (
                        <Tag color="blue" style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 2 }}>
                          独立调度
                        </Tag>
                      )}
                    </div>
                    <Link
                      to={channelEditPath(adminPath, ch.id)}
                      style={{ fontSize: 11, color: '#1677ff', flexShrink: 0, textDecoration: 'none' }}
                    >
                      编辑
                    </Link>
                  </div>
                ))}
              </div>
            }
            placement="bottom"
            trigger={['hover', 'click']}
          >
            <Tag
              color="processing"
              style={{
                margin: 0,
                padding: '0 6px',
                fontSize: 11,
                height: 20,
                lineHeight: '18px',
                borderRadius: 4,
                cursor: 'pointer',
              }}
            >
              {count} 个分组
            </Tag>
          </Popover>
        );
      },
    },
    {
      title: '调度',
      key: 'schedule',
      width: 158,
      sorter: (a: ChannelConfig, b: ChannelConfig) =>
        (Number(a.rate ?? 1) - Number(b.rate ?? 1)) ||
        ((a.priority ?? 0) - (b.priority ?? 0)) ||
        ((a.weight ?? 1) - (b.weight ?? 1)),
      render: (_: unknown, record: ChannelConfig) => renderScheduleCell(record),
    },
    {
      title: (
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
          <Tooltip title={`点击按最紧档${quotaMetricLabel}排序`}>
            <span
              style={{ cursor: 'pointer' }}
              onClick={() => applyQuotaSort('auto')}
            >
              额度{quotaSortMark('auto')}
            </span>
          </Tooltip>
          <Select
            size="small"
            value={quotaSortMetric}
            options={QUOTA_SORT_METRICS.map((m) => ({ value: m.key, label: m.label }))}
            onChange={(m: QuotaSortMetric) => applyQuotaSort(quotaSortPeriod, m)}
            popupMatchSelectWidth={false}
            style={{ width: 78, fontSize: 12 }}
            variant="borderless"
          />
        </span>
      ),
      key: 'quota',
      width: 168,
      render: (_: unknown, record: ChannelConfig) => renderQuotaCell(record),
    },
    {
      title: 'Base URL',
      dataIndex: 'base_url',
      key: 'base_url',
      width: 180,
      ellipsis: true,
      sorter: (a: ChannelConfig, b: ChannelConfig) => (a.base_url || '').localeCompare(b.base_url || ''),
      render: (text: string) => (
        <Tooltip title={text}>
          <Text code style={{ fontSize: 11, lineHeight: 1.2 }}>{text}</Text>
        </Tooltip>
      ),
    },
    {
      title: '排序',
      dataIndex: 'sort_order',
      key: 'sort_order',
      width: 80,
      align: 'center' as const,
      sorter: (a: ChannelConfig, b: ChannelConfig) => (a.sort_order || 0) - (b.sort_order || 0),
      render: (_val: number, record: ChannelConfig) => {
        const order = record.sort_order ?? 0;
        return (
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
            <Text
              editable={{
                tooltip: false,
                text: String(order),
                onChange: (newVal) => {
                  const num = parseInt(newVal.trim(), 10);
                  handleUpdateSortOrder(record.id, isNaN(num) ? 0 : num);
                },
              }}
              style={{
                fontFamily: 'monospace',
                fontSize: 12,
                fontWeight: 600,
                padding: '2px 8px',
                borderRadius: 6,
                background: order > 0 ? 'rgba(59, 130, 246, 0.08)' : 'rgba(128, 128, 128, 0.06)',
                color: order > 0 ? '#2563eb' : 'var(--text-secondary, #8c8c8c)',
                border: order > 0 ? '1px solid rgba(59, 130, 246, 0.2)' : '1px solid rgba(128, 128, 128, 0.15)',
                cursor: 'pointer',
                transition: 'all 0.2s',
              }}
            >
              {order}
            </Text>
          </div>
        );
      },
    },
    {
      title: '最新更新时间',
      dataIndex: 'updated_at',
      key: 'updated_at',
      width: 150,
      ellipsis: true,
      sorter: (a: ChannelConfig, b: ChannelConfig) => {
        const ta = a.updated_at || a.created_at || '';
        const tb = b.updated_at || b.created_at || '';
        return ta.localeCompare(tb);
      },
      render: (_: unknown, record: ChannelConfig) => {
        const time = record.updated_at || record.created_at;
        return <Text type="secondary" style={{ fontSize: 12, whiteSpace: 'nowrap' }}>{time ? formatApiDateTime(time) : '-'}</Text>;
      },
    },
    {
      title: '备注',
      dataIndex: 'remark',
      key: 'remark',
      width: 100,
      ellipsis: true,
      render: (text: string) => <Text type="secondary" style={{ fontSize: 12 }}>{text || '-'}</Text>,
    },
    {
      title: t('common.actions'),
      key: 'actions',
      width: 150,
      fixed: 'right' as const,
      render: (_: unknown, record: ChannelConfig) => (
        <Space size={2} style={{ justifyContent: 'center', width: '100%' }}>
          <Tooltip title={(record.status ?? 1) === 1 ? '点击禁用' : '点击启用'}>
            <Button
              type="text"
              size="small"
              className="channel-table-action-btn"
              icon={(record.status ?? 1) === 1
                ? <PlayCircleOutlined style={{ color: '#52c41a' }} />
                : <StopOutlined style={{ color: '#ff4d4f' }} />}
              onClick={() => handleToggleStatus(record)}
            />
          </Tooltip>
          <Tooltip title="清零额度">
            <Popconfirm title="确定清零该预设的总/日/周/月已用额度吗？" onConfirm={() => handleResetQuota(record.id)}>
              <Button type="text" size="small" className="channel-table-action-btn" icon={<ClearOutlined />} />
            </Popconfirm>
          </Tooltip>
          <Tooltip title="复制">
            <Button
              type="text"
              size="small"
              className="channel-table-action-btn"
              icon={<CopyOutlined />}
              onClick={() => openCopyModal(record)}
            />
          </Tooltip>
          <Tooltip title="编辑">
            <Button type="text" size="small" className="channel-table-action-btn" icon={<EditOutlined />} onClick={() => handleEdit(record)} />
          </Tooltip>
          <Tooltip title="删除">
            <Popconfirm title={t('common.confirm_delete')} onConfirm={() => handleDelete(record.id)}>
              <Button type="text" size="small" className="channel-table-action-btn" icon={<DeleteOutlined />} danger />
            </Popconfirm>
          </Tooltip>
        </Space>
      ),
    },
  ];

  return (
    <Card bordered={false}>
      <style>{`
        .channel-configs-table .ant-table,
        .channel-configs-table .ant-table-container,
        .channel-configs-table .ant-table-content,
        .channel-configs-table table {
          border-collapse: collapse !important;
          border-spacing: 0 !important;
        }
        .channel-configs-table .ant-table-content {
          overflow-x: auto !important;
          scrollbar-width: none !important;
        }
        .channel-configs-table .ant-table-content::-webkit-scrollbar {
          display: none !important;
          height: 0 !important;
        }
        .channel-configs-table .ant-table-thead {
          background: ${isLight ? '#f9fafb' : '#18181b'} !important;
        }
        .channel-configs-table .ant-table-thead > tr {
          height: 28px !important;
          background: ${isLight ? '#f9fafb' : '#18181b'} !important;
        }
        .channel-configs-table .ant-table-thead > tr > th,
        .channel-configs-table .ant-table-thead > tr > th.ant-table-cell {
          padding: 3px 8px !important;
          height: 28px !important;
          line-height: 20px !important;
          font-size: 12px !important;
          font-weight: 600 !important;
          white-space: nowrap !important;
          background: ${isLight ? '#f9fafb' : '#18181b'} !important;
          border-bottom: 1px solid ${isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.08)'} !important;
          color: ${isLight ? '#64748b' : '#a1a1aa'} !important;
        }
        .channel-configs-table .ant-table-thead .ant-table-column-sorters {
          padding: 0 !important;
          margin: 0 !important;
          height: 20px !important;
          display: inline-flex !important;
          align-items: center !important;
        }
        .channel-configs-table .ant-table-thead .ant-table-column-title {
          line-height: 20px !important;
          font-size: 12px !important;
          font-weight: 600 !important;
        }
        .channel-configs-table .ant-table-thead .ant-table-column-sorter {
          margin-inline-start: 3px !important;
          font-size: 9px !important;
        }
        .channel-configs-table .ant-table-thead > tr > th::before {
          display: none !important;
        }
        .channel-configs-table .ant-table-tbody {
          margin: 0 !important;
          padding: 0 !important;
        }
        .channel-configs-table .ant-table-tbody > tr {
          margin: 0 !important;
          background: transparent !important;
        }
        .channel-configs-table .ant-table-tbody > tr > td {
          padding: 4px 8px !important;
          font-size: 12px !important;
          border-bottom: 1px solid ${isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.05)'} !important;
          vertical-align: middle !important;
          line-height: 1.3 !important;
        }
        .channel-configs-table .ant-table-tbody > tr:first-child > td {
          border-top: none !important;
        }
        .channel-configs-table .ant-table-measure-row,
        .channel-configs-table .ant-table-measure-row td,
        .channel-configs-table .ant-table-measure-row th,
        .channel-configs-table tr.ant-table-measure-row,
        .channel-configs-table tr.ant-table-measure-row td,
        .channel-configs-table tr.ant-table-measure-row th,
        .channel-configs-table tr.ant-table-measure-row .ant-table-cell {
          padding: 0 !important;
          height: 0 !important;
          font-size: 0 !important;
          line-height: 0 !important;
          border: none !important;
          visibility: hidden !important;
        }
        .channel-configs-table .ant-table-tbody > tr > td.ant-table-cell-fix-left,
        .channel-configs-table .ant-table-tbody > tr > td.ant-table-cell-fix-right {
          background: ${isLight ? '#ffffff' : '#141414'} !important;
        }
        .channel-configs-table .ant-table-tbody > tr:hover > td,
        .channel-configs-table .ant-table-tbody > tr:hover > td.ant-table-cell-fix-left,
        .channel-configs-table .ant-table-tbody > tr:hover > td.ant-table-cell-fix-right {
          background: ${isLight ? '#fafafa' : '#1d1d1d'} !important;
        }
        .channel-configs-table .ant-table-pagination.ant-pagination {
          margin: 10px 0 0 0 !important;
        }
        .channel-table-action-btn {
          width: 22px !important;
          height: 22px !important;
          min-width: 22px !important;
          padding: 0 !important;
          display: inline-flex !important;
          align-items: center !important;
          justify-content: center !important;
          border-radius: 4px !important;
          font-size: 12px !important;
        }
        .channel-table-action-btn:hover {
          background: ${isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.08)'} !important;
        }
      `}</style>
      {activeDetailSystem ? (
        <UpstreamSystemDetailPanel
          systemKey={activeDetailSystem}
          systemOptions={upstreamSystemOptionItems}
          configs={detailSystemConfigs}
          categories={categories}
          isLight={isLight}
          adminPath={adminPath}
          syncLogsMap={syncLogsMap}
          syncLogsLoadingMap={syncLogsLoadingMap}
          syncTriggerLoadingMap={syncTriggerLoadingMap}
          volcBalanceLoadingMap={volcBalanceLoadingMap}
          onRefreshSystemLogs={(targetConfigs) => {
            targetConfigs.forEach(c => {
              if (c.id) fetchSyncLogs(c.id);
              if (c.id && (c.has_volc_credentials || (c.volc_ak || '').trim())) {
                handleQueryVolcBalance(c, true);
              }
            });
          }}
          onManualSyncRate={handleManualSyncRate}
          onRefreshVolcBalance={handleQueryVolcBalance}
          onEditConfig={handleEdit}
          onUpdateSortOrder={handleUpdateSortOrder}
          onSwitchSystem={(nextKey) => navigate(`/${adminPath}/channel-configs/system/${encodeURIComponent(nextKey)}`)}
          onBackToList={() => navigate(`/${adminPath}/channel-configs`)}
        />
      ) : (
      <>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 16, marginBottom: 24 }}>
        <Title level={4} style={{ margin: 0, fontSize: screens.xs ? 18 : 20, fontWeight: 600 }}>上游渠道配置预设</Title>
        <Space wrap>
          <Segmented
            options={[
              { label: `全部 (${configs.length})`, value: 'all' },
              { label: `激活 (${configs.filter(c => (c.status ?? 1) === 1).length})`, value: '1' },
              { label: `已禁用 (${configs.filter(c => (c.status ?? 1) === 0).length})`, value: '0' },
            ]}
            value={statusFilter === 'all' ? 'all' : statusFilter.toString()}
            onChange={(val) => setStatusFilter(val === 'all' ? 'all' : parseInt(val as string, 10))}
          />
          <Input.Search
            placeholder="名称、YID或服务商"
            allowClear
            onSearch={setSearchText}
            onChange={(e) => !e.target.value && setSearchText('')}
            style={{ width: screens.xs ? '100%' : 220 }}
          />
          <Button
            icon={<CheckSquareOutlined />}
            type={isBatchEditMode ? 'primary' : 'default'}
            danger={isBatchEditMode}
            onClick={() => {
              setIsBatchEditMode(!isBatchEditMode);
              if (isBatchEditMode) setSelectedRowKeys([]);
            }}
          >
            {isBatchEditMode ? '退出选择' : '选择编辑'}
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={handleAdd}>添加配置</Button>
        </Space>
      </div>

      <div style={{
        backgroundColor: isLight ? 'rgba(0, 0, 0, 0.04)' : 'rgba(255, 255, 255, 0.04)',
        padding: '12px 16px',
        borderRadius: 8,
        marginBottom: 16,
        border: isLight ? '1px solid rgba(0, 0, 0, 0.06)' : '1px solid rgba(255, 255, 255, 0.08)',
        display: 'flex',
        flexDirection: 'column',
        gap: 10,
      }}>
        <div style={{ display: 'flex', alignItems: 'center', padding: 0 }}>
          <Text type="secondary" style={{ width: 80, flexShrink: 0, fontSize: 13 }}>上游分类</Text>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, flexGrow: 1 }}>
            {([
              { key: 'all' as const, label: '全部', count: configs.length },
              ...categories.map(c => ({
                key: c.id,
                label: c.name,
                count: configs.filter(ch => resolveRecordCategoryIds(ch).includes(c.id)).length,
              })),
              {
                key: 'unclassified' as const,
                label: '未分类',
                count: configs.filter(ch => resolveRecordCategoryIds(ch).length === 0).length,
              },
            ] as { key: number | 'all' | 'unclassified'; label: string; count: number }[]).map(item => {
              const selected = categoryFilter === item.key;
              return (
                <div
                  key={String(item.key)}
                  onClick={() => setCategoryFilter(item.key)}
                  style={{
                    padding: '3px 10px',
                    borderRadius: 6,
                    fontSize: 13,
                    fontWeight: selected ? 500 : 400,
                    backgroundColor: selected ? (isLight ? '#18181b' : '#f4f4f5') : (isLight ? '#ffffff' : '#141414'),
                    color: selected ? (isLight ? '#ffffff' : '#09090b') : (isLight ? '#52525b' : 'rgba(255, 255, 255, 0.65)'),
                    border: selected ? (isLight ? '1px solid #18181b' : '1px solid #f4f4f5') : (isLight ? '1px solid rgba(0, 0, 0, 0.08)' : '1px solid rgba(255, 255, 255, 0.12)'),
                    cursor: 'pointer',
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 6,
                    transition: 'all 0.15s ease',
                  }}
                >
                  {item.label}
                  <span style={{ opacity: 0.6, fontSize: 11 }}>{item.count}</span>
                </div>
              );
            })}
            <Tooltip title={t('common.manage', '管理')}>
              <Button
                type="text"
                size="small"
                icon={<SettingOutlined style={{ color: isLight ? '#71717a' : '#a1a1aa' }} />}
                onClick={() => setIsCategoryManagerVisible(true)}
                style={{ marginLeft: 8 }}
              />
            </Tooltip>
          </div>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', padding: 0 }}>
          <Text type="secondary" style={{ width: 80, flexShrink: 0, fontSize: 13 }}>服务商</Text>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, flexGrow: 1 }}>
            {([
              { key: 'all', label: '全部', count: configs.length },
              ...allProviders.map(p => ({
                key: p.name,
                label: p.name,
                count: configs.filter(ch => (ch.provider_type || '').trim() === p.name).length,
              })),
              {
                key: '__unclassified__',
                label: '未分类',
                count: configs.filter(ch => !(ch.provider_type || '').trim()).length,
              },
            ]).map(item => {
              const selected = providerFilter === item.key;
              return (
                <div
                  key={item.key}
                  onClick={() => setProviderFilter(item.key)}
                  style={{
                    padding: '3px 10px',
                    borderRadius: 6,
                    fontSize: 13,
                    fontWeight: selected ? 500 : 400,
                    backgroundColor: selected ? (isLight ? '#18181b' : '#f4f4f5') : (isLight ? '#ffffff' : '#141414'),
                    color: selected ? (isLight ? '#ffffff' : '#09090b') : (isLight ? '#52525b' : 'rgba(255, 255, 255, 0.65)'),
                    border: selected ? (isLight ? '1px solid #18181b' : '1px solid #f4f4f5') : (isLight ? '1px solid rgba(0, 0, 0, 0.08)' : '1px solid rgba(255, 255, 255, 0.12)'),
                    cursor: 'pointer',
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 6,
                    transition: 'all 0.15s ease',
                  }}
                >
                  {item.label}
                  <span style={{ opacity: 0.6, fontSize: 11 }}>{item.count}</span>
                </div>
              );
            })}
            <Tooltip title={t('common.manage', '管理')}>
              <Button
                type="text"
                size="small"
                icon={<SettingOutlined style={{ color: isLight ? '#71717a' : '#a1a1aa' }} />}
                onClick={() => setIsProviderManagerVisible(true)}
                style={{ marginLeft: 8 }}
              />
            </Tooltip>
          </div>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', padding: 0 }}>
          <Text type="secondary" style={{ width: 80, flexShrink: 0, fontSize: 13 }}>上游系统</Text>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, flexGrow: 1 }}>
            {([
              { key: 'all', label: '全部', count: configs.length },
              ...upstreamSystemOptionItems,
            ]).map(item => {
              const selected = upstreamSystemFilter === item.key;
              return (
                <div
                  key={item.key}
                  onClick={() => setUpstreamSystemFilter(item.key)}
                  style={{
                    padding: '3px 10px',
                    borderRadius: 6,
                    fontSize: 13,
                    fontWeight: selected ? 500 : 400,
                    backgroundColor: selected ? (isLight ? '#18181b' : '#f4f4f5') : (isLight ? '#ffffff' : '#141414'),
                    color: selected ? (isLight ? '#ffffff' : '#09090b') : (isLight ? '#52525b' : 'rgba(255, 255, 255, 0.65)'),
                    border: selected ? (isLight ? '1px solid #18181b' : '1px solid #f4f4f5') : (isLight ? '1px solid rgba(0, 0, 0, 0.08)' : '1px solid rgba(255, 255, 255, 0.12)'),
                    cursor: 'pointer',
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 6,
                    transition: 'all 0.15s ease',
                  }}
                >
                  {item.label}
                  <span style={{ opacity: 0.6, fontSize: 11 }}>{item.count}</span>
                  {item.key !== 'all' && (
                    <span
                      role="button"
                      tabIndex={0}
                      onClick={(e) => {
                        e.stopPropagation();
                        navigate(`/${adminPath}/channel-configs/system/${encodeURIComponent(item.key)}`);
                      }}
                      style={{
                        marginLeft: 2,
                        padding: '1px 6px',
                        fontSize: 11,
                        lineHeight: '16px',
                        borderRadius: 4,
                        fontWeight: 500,
                        color: selected
                          ? (isLight ? '#91caff' : '#1677ff')
                          : '#1677ff',
                        backgroundColor: selected
                          ? (isLight ? 'rgba(255, 255, 255, 0.16)' : 'rgba(22, 119, 255, 0.14)')
                          : (isLight ? '#e6f4ff' : 'rgba(22, 119, 255, 0.15)'),
                        border: selected
                          ? (isLight ? '1px solid rgba(145, 202, 255, 0.4)' : '1px solid rgba(22, 119, 255, 0.35)')
                          : (isLight ? '1px solid #91caff' : '1px solid rgba(22, 119, 255, 0.35)'),
                        cursor: 'pointer',
                        transition: 'all 0.15s ease',
                      }}
                    >
                      详细
                    </span>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      </div>

      {isBatchEditMode && (
        <div style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          flexWrap: 'wrap',
          gap: 8,
          padding: '8px 14px',
          marginBottom: 12,
          borderRadius: 8,
          backgroundColor: isLight ? '#e6f4ff' : '#111b26',
          border: isLight ? '1px solid #91caff' : '1px solid #154173',
        }}>
          <Space wrap size={10} style={{ alignItems: 'center' }}>
            <Button
              size="small"
              onClick={() => {
                const ids = currentPageConfigs.map(c => c.id);
                setSelectedRowKeys(prev => Array.from(new Set([...prev.map(k => Number(k)), ...ids])));
              }}
            >
              全选
            </Button>
            <Button size="small" disabled={selectedRowKeys.length === 0} onClick={() => setSelectedRowKeys([])}>
              取消选择
            </Button>
            <Text strong style={{ fontSize: 13, color: isLight ? '#0958d9' : '#1677ff', marginLeft: 4 }}>
              已选择 <span style={{ fontSize: 15, fontWeight: 700 }}>{selectedRowKeys.length}</span> 项
            </Text>
          </Space>
          <Space wrap size={8}>
            <Select
              key={batchCategoryKey}
              size="small"
              placeholder="修改分类"
              style={{ width: 140 }}
              disabled={selectedRowKeys.length === 0 || batchLoading}
              options={[
                { label: '未分类', value: 0 },
                ...activeCategories.map(c => ({ label: c.name, value: c.id })),
              ]}
              onChange={(value) => {
                handleBatchChangeCategory(value === 0 ? null : value);
                setBatchCategoryKey(k => k + 1);
              }}
            />
            <Button size="small" type="primary" icon={<CheckCircleOutlined />} disabled={selectedRowKeys.length === 0} loading={batchLoading} onClick={() => handleBatchChangeStatus(1)}>
              激活
            </Button>
            <Button size="small" icon={<StopOutlined />} disabled={selectedRowKeys.length === 0} loading={batchLoading} onClick={() => handleBatchChangeStatus(0)}>
              禁用
            </Button>
            <Popconfirm
              title="确定要批量删除选中的上游渠道吗？"
              description={`将一次性删除选中的 ${selectedRowKeys.length} 个上游渠道，此操作不可撤销。`}
              onConfirm={handleBatchDelete}
              okText="确认删除"
              cancelText="取消"
              okButtonProps={{ danger: true, loading: batchLoading }}
              disabled={selectedRowKeys.length === 0}
            >
              <Button size="small" danger type="primary" icon={<DeleteOutlined />} disabled={selectedRowKeys.length === 0} loading={batchLoading}>
                批量删除
              </Button>
            </Popconfirm>
          </Space>
        </div>
      )}

      {screens.xs ? (
        <MobileCardList
          dataSource={currentPageConfigs}
          loading={loading}
          rowKey="id"
          pagination={listPagination({
            current: currentPage,
            pageSize,
            total: displayedConfigs.length,
            onChange: onPageChange,
          })}
          renderCard={(record: ChannelConfig) => {
            const sync = renderUpstreamSyncInline(record);
            return (
            <MobileCard
              title={
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  {isBatchEditMode && (
                    <Checkbox
                      checked={batchChecked(record.id)}
                      onClick={(e) => e.stopPropagation()}
                      onChange={(e) => toggleBatchKey(record.id, e.target.checked)}
                    />
                  )}
                  <span>{record.name}</span>
                </div>
              }
              extra={<Typography.Text keyboard style={{ color: '#1677ff' }}>{record.yid || '-'}</Typography.Text>}
            >
              {sync ? <CardRow label="上游同步">{sync}</CardRow> : null}
              <CardRow label="状态">{renderStatusBadge(record.status)}</CardRow>
              <CardRow label="上游分类">
                {(() => {
                  const catIds = resolveRecordCategoryIds(record);
                  if (catIds.length === 0) return <Text type="secondary" style={{ fontSize: 12 }}>未分类</Text>;
                  return (
                    <Space size={4} wrap>
                      {catIds.map(id => {
                        const name = resolveCategoryName(id);
                        if (!name) return null;
                        return (
                          <Tag key={id} style={{ margin: 0, fontSize: 11, borderRadius: 4 }}>
                            {name}
                          </Tag>
                        );
                      })}
                    </Space>
                  );
                })()}
              </CardRow>
              <CardRow label="绑定渠道分组">
                {record.bound_channel_count && record.bound_channel_count > 0 ? (
                  <Tag color="processing" style={{ margin: 0, fontSize: 11, borderRadius: 4 }}>
                    {record.bound_channel_count} 个模型渠道分组
                  </Tag>
                ) : (
                  <Text type="secondary" style={{ fontSize: 12 }}>未绑定</Text>
                )}
              </CardRow>
              <CardRow label="服务商广场展示">{record.provider_type || '-'}</CardRow>
              <CardRow label="调度">{renderScheduleCell(record)}</CardRow>
              <CardRow label="Base URL"><Text code style={{ fontSize: 12 }}>{record.base_url}</Text></CardRow>
              <CardRow label="额度">{renderQuotaCell(record)}</CardRow>
              <CardRow label="页面排序">
                <div style={{ display: 'flex', alignItems: 'center' }}>
                  <Text
                    editable={{
                      tooltip: false,
                      text: String(record.sort_order ?? 0),
                      onChange: (val) => {
                        const num = parseInt(val.trim(), 10);
                        handleUpdateSortOrder(record.id, isNaN(num) ? 0 : num);
                      },
                    }}
                    style={{
                      fontFamily: 'monospace',
                      fontSize: 12,
                      fontWeight: 600,
                      color: (record.sort_order ?? 0) > 0 ? '#1677ff' : undefined,
                    }}
                  >
                    {record.sort_order ?? 0}
                  </Text>
                </div>
              </CardRow>
              <CardRow label="最新更新时间">
                <Text type="secondary" style={{ fontSize: 12 }}>
                  {record.updated_at || record.created_at ? formatApiDateTime(record.updated_at || record.created_at) : '-'}
                </Text>
              </CardRow>
              <CardRow label="备注">{record.remark || '-'}</CardRow>
              <CardActions>
                <Tooltip title={(record.status ?? 1) === 1 ? '点击禁用' : '点击启用'}>
                  <Button
                    type="text"
                    size="small"
                    icon={(record.status ?? 1) === 1
                      ? <PlayCircleOutlined style={{ color: '#52c41a' }} />
                      : <StopOutlined style={{ color: '#ff4d4f' }} />}
                    onClick={() => handleToggleStatus(record)}
                  />
                </Tooltip>
                <Tooltip title="清零额度">
                  <Popconfirm title="确定清零已用额度吗？" onConfirm={() => handleResetQuota(record.id)}>
                    <Button type="text" size="small" icon={<ClearOutlined />} />
                  </Popconfirm>
                </Tooltip>
                <Tooltip title="复制">
                  <Button
                    type="text"
                    size="small"
                    icon={<CopyOutlined />}
                    onClick={() => openCopyModal(record)}
                  />
                </Tooltip>
                <Button type="text" size="small" icon={<EditOutlined />} onClick={() => handleEdit(record)} />
                <Popconfirm title={t('common.confirm_delete')} onConfirm={() => handleDelete(record.id)}>
                  <Button type="text" size="small" icon={<DeleteOutlined />} danger />
                </Popconfirm>
              </CardActions>
            </MobileCard>
            );
          }}
        />
      ) : (
        <TableStickyHorizontalScroll isLight={isLight}>
          <Table
            className="channel-configs-table compact-table"
            size="small"
            dataSource={displayedConfigs}
            columns={columns}
            rowKey="id"
            loading={loading}
            pagination={listPagination({
              current: currentPage,
              pageSize,
              total: displayedConfigs.length,
              onChange: onPageChange,
            })}
            scroll={{ x: 1480 }}
            rowSelection={isBatchEditMode ? {
              selectedRowKeys,
              preserveSelectedRowKeys: true,
              onChange: (keys) => setSelectedRowKeys(keys),
            } : undefined}
            onChange={(_p, _f, sorter) => {
              const s = Array.isArray(sorter) ? sorter[0] : sorter;
              if (s?.order && s.columnKey) setQuotaSortOrder(null);
            }}
          />
        </TableStickyHorizontalScroll>
      )}
      </>
      )}

      <Modal
        title={editingConfig ? "编辑上游渠道配置" : "添加上游渠道配置"}
        open={isModalVisible}
        onCancel={closeConfigModal}
        onOk={() => form.submit()}
        confirmLoading={submitting}
        width={560}
        styles={{ body: { paddingTop: 12, paddingBottom: 4 } }}
      >
        <Form
          form={form}
          layout="vertical"
          size="small"
          onFinish={handleSave}
          autoComplete="off"
          style={{ marginBottom: 0 }}
        >
          <div style={{ display: 'flex', gap: 12 }}>
            <Form.Item
              name="name"
              label={
                <Space size={6}>
                  <span>配置名称</span>
                  {editingConfig?.yid && (
                    <Text type="secondary" style={{ fontSize: 11 }}>YID: {editingConfig.yid}</Text>
                  )}
                </Space>
              }
              rules={[{ required: true, message: '请输入配置名称' }]}
              style={{ flex: 1.4, marginBottom: 10 }}
            >
              <Input placeholder="例如：OpenAI 官方渠道" autoComplete="off" />
            </Form.Item>
            <Form.Item
              name="provider_type"
              label="服务商类型(模型广场展示)"
              style={{ flex: 1, marginBottom: 10 }}
            >
              <Select
                allowClear
                placeholder="选择服务商"
                options={activeProviders.map(p => ({ label: p.name, value: p.name }))}
                dropdownRender={(menu) => (
                  <>
                    {menu}
                    <Divider style={{ margin: '6px 0' }} />
                    <Button
                      type="link"
                      size="small"
                      icon={<SettingOutlined />}
                      onClick={() => setIsProviderManagerVisible(true)}
                      style={{ width: '100%' }}
                    >
                      管理服务商
                    </Button>
                  </>
                )}
              />
            </Form.Item>
          </div>

          <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start' }}>
            <Form.Item name="category_ids" label="上游分类" style={{ flex: 1, marginBottom: 10 }}>
              <Select
                mode="multiple"
                allowClear
                maxTagCount="responsive"
                placeholder="选择分类"
                options={activeCategories.map(c => ({ label: c.name, value: c.id }))}
                dropdownRender={(menu) => (
                  <>
                    {menu}
                    <Divider style={{ margin: '6px 0' }} />
                    <Button
                      type="link"
                      size="small"
                      icon={<SettingOutlined />}
                      onClick={() => setIsCategoryManagerVisible(true)}
                      style={{ width: '100%' }}
                    >
                      管理分类
                    </Button>
                  </>
                )}
              />
            </Form.Item>
            <Form.Item
              name="sort_order"
              label={
                <Tooltip title="数字越大在页面中越靠前">
                  <span>页面排序</span>
                </Tooltip>
              }
              style={{ width: 96, marginBottom: 10 }}
            >
              <InputNumber placeholder="0" style={{ width: '100%' }} />
            </Form.Item>
          </div>

          <div style={{ display: 'flex', gap: 12 }}>
            <Form.Item name="rate" label="渠道倍率" rules={[{ required: true }]} style={{ flex: 1, marginBottom: 10 }}>
              <InputNumber min={0} step={0.0001} precision={4} placeholder="1.0000" style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="priority" label="默认优先级" tooltip="绑定到渠道分组时的默认调度优先级；若渠道分组内未单独设置，则按此默认值生效" rules={[{ required: true }]} style={{ flex: 1, marginBottom: 10 }}>
              <InputNumber min={0} placeholder="0" style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="weight" label="默认权重" tooltip="绑定到渠道分组时的默认分流权重；若渠道分组内未单独设置，则按此默认值生效" rules={[{ required: true }]} style={{ flex: 1, marginBottom: 10 }}>
              <InputNumber min={1} placeholder="1" style={{ width: '100%' }} />
            </Form.Item>
          </div>

          <Form.Item label="额度限额" style={{ marginBottom: 10 }}>
            <Switch
              checked={enableQuota}
              onChange={(checked) => {
                setEnableQuota(checked);
                form.setFieldsValue({
                  quota_limit: -1,
                  daily_quota_limit: -1,
                  weekly_quota_limit: -1,
                  monthly_quota_limit: -1,
                });
              }}
              checkedChildren="已开启限额"
              unCheckedChildren="默认不限额"
            />
          </Form.Item>

          <Form.Item name="daily_reset_hour" hidden initialValue={0}><InputNumber /></Form.Item>
          <Form.Item name="daily_reset_minute" hidden initialValue={0}><InputNumber /></Form.Item>
          <Form.Item name="daily_reset_cooldown_minutes" hidden initialValue={0}><InputNumber /></Form.Item>

          {enableQuota && (
            <Form.Item
              noStyle
              shouldUpdate={(prev, curr) =>
                prev.quota_limit !== curr.quota_limit ||
                prev.daily_quota_limit !== curr.daily_quota_limit ||
                prev.weekly_quota_limit !== curr.weekly_quota_limit ||
                prev.monthly_quota_limit !== curr.monthly_quota_limit ||
                prev.daily_reset_hour !== curr.daily_reset_hour ||
                prev.daily_reset_minute !== curr.daily_reset_minute ||
                prev.daily_reset_cooldown_minutes !== curr.daily_reset_cooldown_minutes
              }
            >
              {() => {
                const quotaDeps = ['quota_limit', 'daily_quota_limit', 'weekly_quota_limit', 'monthly_quota_limit'] as const;
                const fieldValidator = (field: typeof quotaDeps[number]) => ({
                  validator: async (_: unknown, value: number | null) => {
                    if (value != null && Number(value) < -1) {
                      throw new Error('额度不能小于 -1');
                    }
                    const latest = {
                      ...form.getFieldsValue([...quotaDeps]),
                      [field]: value,
                    };
                    const total = latest.quota_limit;
                    const day = latest.daily_quota_limit;
                    const week = latest.weekly_quota_limit;
                    const month = latest.monthly_quota_limit;

                    const fail = (msg: string) => {
                      throw new Error(msg);
                    };

                    if (field === 'daily_quota_limit' && isFiniteQuotaLimit(day)) {
                      if (isFiniteQuotaLimit(week) && day > week) fail('日额度不能大于周额度');
                      if (isFiniteQuotaLimit(month) && day > month) fail('日额度不能大于月额度');
                      if (isFiniteQuotaLimit(total) && day > total) fail('日额度不能大于总额度');
                    }
                    if (field === 'weekly_quota_limit' && isFiniteQuotaLimit(week)) {
                      if (isFiniteQuotaLimit(month) && week > month) fail('周额度不能大于月额度');
                      if (isFiniteQuotaLimit(total) && week > total) fail('周额度不能大于总额度');
                      if (isFiniteQuotaLimit(day) && day > week) fail('周额度不能小于日额度');
                    }
                    if (field === 'monthly_quota_limit' && isFiniteQuotaLimit(month)) {
                      if (isFiniteQuotaLimit(total) && month > total) fail('月额度不能大于总额度');
                      if (isFiniteQuotaLimit(week) && week > month) fail('月额度不能小于周额度');
                      if (isFiniteQuotaLimit(day) && day > month) fail('月额度不能小于日额度');
                    }
                    if (field === 'quota_limit' && isFiniteQuotaLimit(total)) {
                      if (isFiniteQuotaLimit(month) && month > total) fail('总额度不能小于月额度');
                      if (isFiniteQuotaLimit(week) && week > total) fail('总额度不能小于周额度');
                      if (isFiniteQuotaLimit(day) && day > total) fail('总额度不能小于日额度');
                    }
                  },
                });
                const quotaItemStyle: React.CSSProperties = { marginBottom: 10 };
                const resetHour = Number(form.getFieldValue('daily_reset_hour') ?? 0);
                const resetMinute = Number(form.getFieldValue('daily_reset_minute') ?? 0);
                const resetCooldown = Number(form.getFieldValue('daily_reset_cooldown_minutes') ?? 0);
                const tzSuffix = timedisplayOffsetSuffix(quotaTz);
                const resetInline = formatDailyResetInline(resetHour, resetMinute, resetCooldown, tzSuffix);
                const hh = String(Math.min(23, Math.max(0, resetHour))).padStart(2, '0');
                const mm = String(Math.min(59, Math.max(0, resetMinute))).padStart(2, '0');
                const resetTooltip = resetCooldown > 0
                  ? `每天 ${hh}:${mm}${tzSuffix} 起，再冷却 ${resetCooldown} 分钟后清零日已用`
                  : `每天 ${hh}:${mm}${tzSuffix} 清零日已用`;
                return (
                  <div
                    style={{
                      display: 'grid',
                      gridTemplateColumns: '1fr 1fr',
                      columnGap: 12,
                      marginBottom: 4,
                    }}
                  >
                    <Form.Item
                      name="quota_limit"
                      label="总额度"
                      initialValue={-1}
                      style={quotaItemStyle}
                      dependencies={quotaDeps as unknown as string[]}
                      validateTrigger={['onChange', 'onBlur']}
                      rules={[fieldValidator('quota_limit')]}
                    >
                      <InputNumber
                        min={-1}
                        style={{ width: '100%' }}
                        formatter={formatQuotaLimitDisplay}
                        parser={parseQuotaLimitInput}
                      />
                    </Form.Item>
                    <Form.Item
                      name="monthly_quota_limit"
                      label="月额度"
                      initialValue={-1}
                      style={quotaItemStyle}
                      dependencies={quotaDeps as unknown as string[]}
                      validateTrigger={['onChange', 'onBlur']}
                      rules={[fieldValidator('monthly_quota_limit')]}
                    >
                      <InputNumber
                        min={-1}
                        style={{ width: '100%' }}
                        formatter={formatQuotaLimitDisplay}
                        parser={parseQuotaLimitInput}
                      />
                    </Form.Item>
                    <Form.Item
                      name="weekly_quota_limit"
                      label="周额度"
                      initialValue={-1}
                      style={quotaItemStyle}
                      dependencies={quotaDeps as unknown as string[]}
                      validateTrigger={['onChange', 'onBlur']}
                      rules={[fieldValidator('weekly_quota_limit')]}
                    >
                      <InputNumber
                        min={-1}
                        style={{ width: '100%' }}
                        formatter={formatQuotaLimitDisplay}
                        parser={parseQuotaLimitInput}
                      />
                    </Form.Item>
                    <Form.Item
                      name="daily_quota_limit"
                      label={
                        <span
                          style={{
                            display: 'inline-flex',
                            alignItems: 'center',
                            gap: 4,
                            maxWidth: '100%',
                            verticalAlign: 'middle',
                          }}
                        >
                          <span>日额度</span>
                          <Tooltip title={resetTooltip}>
                            <Text
                              type="secondary"
                              style={{
                                fontSize: 11,
                                fontWeight: 400,
                                lineHeight: 1.2,
                                maxWidth: 148,
                                overflow: 'hidden',
                                textOverflow: 'ellipsis',
                                whiteSpace: 'nowrap',
                              }}
                            >
                              {resetInline}
                            </Text>
                          </Tooltip>
                          <Tooltip title="配置日额度刷新时间与冷却">
                            <Button
                              type="link"
                              size="small"
                              icon={<SettingOutlined />}
                              aria-label="配置日额度刷新"
                              onClick={(e) => {
                                e.preventDefault();
                                e.stopPropagation();
                                openDailyResetModal();
                              }}
                              style={{
                                padding: 0,
                                width: 18,
                                height: 18,
                                minWidth: 18,
                                lineHeight: '18px',
                                flexShrink: 0,
                              }}
                            />
                          </Tooltip>
                        </span>
                      }
                      initialValue={-1}
                      style={quotaItemStyle}
                      dependencies={quotaDeps as unknown as string[]}
                      validateTrigger={['onChange', 'onBlur']}
                      rules={[fieldValidator('daily_quota_limit')]}
                    >
                      <InputNumber
                        min={-1}
                        style={{ width: '100%' }}
                        formatter={formatQuotaLimitDisplay}
                        parser={parseQuotaLimitInput}
                      />
                    </Form.Item>
                  </div>
                );
              }}
            </Form.Item>
          )}

          <Form.Item name="base_url" label="端点基础地址 (Base URL)" rules={[{ required: true }]} style={{ marginBottom: 10 }}>
            <AutoComplete
              options={[
                { value: 'https://ark.cn-beijing.volces.com', label: '火山方舟 (https://ark.cn-beijing.volces.com)' },
                { value: 'https://ark.ap-southeast.bytepluses.com', label: 'BytePlus(ap-southeast-1) (https://ark.ap-southeast.bytepluses.com)' },
                { value: 'https://ark.eu-west.bytepluses.com', label: 'BytePlus(eu-west-1) (https://ark.eu-west.bytepluses.com)' },
                { value: 'https://api-beijing.klingai.com', label: '可灵 (https://api-beijing.klingai.com)' },
                { value: 'https://dashscope.aliyuncs.com', label: '阿里百炼 (https://dashscope.aliyuncs.com)' },
                { value: 'https://vod.tencentcloudapi.com', label: '腾讯云 VOD AIGC (https://vod.tencentcloudapi.com)' },
                { value: 'https://visual.volcengineapi.com', label: '即梦AI/火山CV (https://visual.volcengineapi.com)' },
              ]}
              placeholder="选择预设或自由输入"
              onChange={(val) => {
                if (isVolcengineBaseUrl(val) && !form.getFieldValue('upstream_system')) {
                  form.setFieldsValue({ upstream_system: '火山引擎' });
                }
              }}
              filterOption={(inputValue, option) =>
                String(option?.label || '').toUpperCase().indexOf(inputValue.toUpperCase()) !== -1 ||
                String(option?.value || '').toUpperCase().indexOf(inputValue.toUpperCase()) !== -1
              }
            />
          </Form.Item>
          <Form.Item
            name="api_key"
            label="请求鉴权密钥 (API Key)"
            style={{ marginBottom: 10 }}
          >
            <Input.Password
              autoComplete="new-password"
              placeholder={editingConfig ? '保持当前密钥或输入新值覆盖' : 'API Key / sk-... / access_key:secret_key'}
            />
          </Form.Item>
          <Form.Item name="upstream_system" label="上游系统" style={{ marginBottom: 10 }}>
            <Select
              allowClear
              placeholder="可选"
              options={UPSTREAM_SYSTEM_OPTIONS}
              onChange={(val) => {
                form.setFieldsValue({ upstream_system: val });
                if (val === 'newapi') {
                  const defaultSiteCurrency = settings?.currency?.default_currency || 'USD';
                  if (!form.getFieldValue('upstream_currency')) {
                    form.setFieldsValue({ upstream_currency: 'CNY' });
                  }
                  if (!form.getFieldValue('site_currency')) {
                    form.setFieldsValue({ site_currency: defaultSiteCurrency });
                  }
                  if (!form.getFieldValue('upstream_currency_rate')) {
                    form.setFieldsValue({ upstream_currency_rate: 7.2 });
                  }
                  const bUrl = form.getFieldValue('base_url');
                  if (bUrl) {
                    loadUpstreamGroups(undefined, true);
                  }
                }
              }}
            />
          </Form.Item>
          {showVolcCredentials && (
            <div
              style={{
                background: isLight ? '#f9fafb' : '#141414',
                border: isLight ? '1px solid #f0f0f0' : '1px solid #303030',
                borderRadius: 8,
                padding: '10px 12px',
                marginBottom: 12,
              }}
            >
              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  flexWrap: 'wrap',
                  gap: 8,
                  marginBottom: 8,
                }}
              >
                <Space size={6} wrap>
                  <span style={{ fontSize: 13, fontWeight: 600, color: isLight ? '#262626' : '#d9d9d9' }}>
                    火山引擎账户凭证（查询上游账户余额）
                  </span>
                  {modalVolcBalance !== null && modalVolcBalance !== undefined && (
                    <Tag
                      color={modalVolcBalance > 0 ? 'success' : 'error'}
                      style={{ margin: 0, fontWeight: 600, fontSize: 11, borderRadius: 4 }}
                    >
                      可用余额: ¥{Number(modalVolcBalance).toFixed(2)}
                    </Tag>
                  )}
                </Space>
                <Button
                  size="small"
                  type="primary"
                  ghost
                  icon={<SyncOutlined spin={modalVolcLoading} />}
                  loading={modalVolcLoading}
                  onClick={handleModalQueryVolcBalance}
                  style={{ height: 22, fontSize: 11, padding: '0 8px' }}
                >
                  查询账户余额
                </Button>
              </div>
              <div style={{ display: 'grid', gridTemplateColumns: screens.xs ? '1fr' : '1fr 1fr', gap: 12 }}>
                <Form.Item
                  name="volc_ak"
                  label="Access Key ID (AK)"
                  tooltip="火山引擎访问控制 (IAM) 密钥管理中的 Access Key ID，用于调用 QueryBalanceAcct 接口获取账户可用余额"
                  style={{ marginBottom: 4 }}
                >
                  <Input placeholder="例如：AKLT..." allowClear autoComplete="off" />
                </Form.Item>
                <Form.Item
                  name="volc_sk"
                  label="Secret Access Key (SK)"
                  tooltip="火山引擎访问控制 (IAM) 密钥管理中的 Secret Access Key"
                  style={{ marginBottom: 4 }}
                >
                  <Input.Password
                    autoComplete="new-password"
                    placeholder={editingConfig?.has_volc_credentials ? '保持当前 Secret Key 或输入新值覆盖' : '填写 Secret Access Key'}
                    allowClear
                  />
                </Form.Item>
              </div>
            </div>
          )}
          {upstreamSystem === 'newapi' && (
            <>
              <div style={{ display: 'grid', gridTemplateColumns: screens.xs ? '1fr' : '180px 1fr', gap: 12 }}>
                <Form.Item
                  name="upstream_user_id"
                  label="上游用户 ID"
                  tooltip="NewAPI 个人设置中的用户数字 ID（作为 New-Api-User 请求头，配合系统访问令牌拉取账号私有分组与 VIP 专属折扣倍率）"
                  style={{ marginBottom: 10 }}
                >
                  <Input placeholder="可选，如 509" allowClear />
                </Form.Item>
                <Form.Item
                  name="upstream_access_token"
                  label="系统访问令牌 (拉取专属分组倍率)"
                  tooltip="NewAPI「个人设置 → 安全设置 → 系统访问令牌」；填写后将通过 /api/user/self/groups 拉取当前账号专属分组与 VIP 折后倍率，不影响上方模型转发密钥"
                  style={{ marginBottom: 10 }}
                >
                  <Input.Password
                    autoComplete="new-password"
                    placeholder={editingConfig?.has_upstream_access_token ? '保持当前系统访问令牌或输入新值覆盖' : '可选，填写后拉取账号私有/VIP专属分组倍率'}
                    allowClear
                  />
                </Form.Item>
              </div>
              <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start' }}>
                <Form.Item
                  name="upstream_group"
                  label={
                    <Space size={6} wrap>
                      <span>同步分组倍率</span>
                      {editingConfig?.id ? (
                        <Popover
                          placement="bottomLeft"
                          trigger={['hover', 'click']}
                          onOpenChange={(open) => {
                            if (open && editingConfig?.id) {
                              fetchSyncLogs(editingConfig.id);
                            }
                          }}
                          title={renderSyncLogsHeader({
                            ...editingConfig,
                            upstream_sync_interval_minutes: Number(upstreamSyncIntervalMinutes ?? editingConfig.upstream_sync_interval_minutes ?? 0),
                          })}
                          content={renderSyncLogsList(editingConfig)}
                        >
                          <Tag
                            color="processing"
                            style={{ margin: 0, padding: '0 5px', fontSize: 11, height: 18, lineHeight: '16px', borderRadius: 4, cursor: 'pointer' }}
                          >
                            历史记录 ({(syncLogsMap[editingConfig.id] || []).length})
                          </Tag>
                        </Popover>
                      ) : null}
                      {editingConfig?.upstream_synced_at ? (
                        <Text type="secondary" style={{ fontSize: 11, fontWeight: 400 }}>
                          上次同步: {formatApiDateTime(editingConfig.upstream_synced_at)}
                        </Text>
                      ) : null}
                    </Space>
                  }
                  extra="选中后写入上方「渠道倍率」"
                  style={{ flex: 1, marginBottom: 10 }}
                >
                  <Select
                    allowClear
                    showSearch
                    placeholder={upstreamGroups.length ? '选择要同步的分组' : '先拉取分组倍率'}
                    optionFilterProp="label"
                    onChange={(value) => {
                      form.setFieldsValue({ upstream_group: value || '' });
                      updateCalculatedRate(value || undefined);
                    }}
                    options={
                      upstreamGroups.length
                        ? upstreamGroups.map(g => ({
                            value: g.name,
                            label: `${g.name}  ${g.ratio}x${g.label && g.label !== g.name ? `  ${g.label}` : ''}`,
                          }))
                        : (form.getFieldValue('upstream_group')
                            ? [{ value: form.getFieldValue('upstream_group'), label: form.getFieldValue('upstream_group') }]
                            : [])
                    }
                  />
                </Form.Item>
                <Form.Item label=" " style={{ marginBottom: 10, width: 118 }}>
                  <Button
                    icon={<SyncOutlined spin={fetchingGroups} />}
                    loading={fetchingGroups}
                    onClick={() => loadUpstreamGroups(undefined, false)}
                    style={{ width: '100%' }}
                  >
                    拉取分组
                  </Button>
                </Form.Item>
              </div>
              <div
                style={{
                  background: isLight ? '#f9fafb' : '#141414',
                  border: isLight ? '1px solid #f0f0f0' : '1px solid #303030',
                  borderRadius: 8,
                  padding: '10px 12px',
                  marginBottom: 12,
                }}
              >
                <div style={{ fontSize: 13, fontWeight: 600, marginBottom: 8, color: isLight ? '#262626' : '#d9d9d9', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                  <span>计价货币换算设置</span>
                  <Tooltip title="针对 NewAPI 上游以人民币定价（如 GPT 3倍率 代表 3元/刀）或美元定价，折算为本站对应货币实际倍率/折扣">
                    <span style={{ fontSize: 12, fontWeight: 'normal', color: '#1890ff', cursor: 'pointer' }}>
                      换算说明
                    </span>
                  </Tooltip>
                </div>
                <div style={{ display: 'grid', gridTemplateColumns: screens.xs ? '1fr' : 'repeat(3, 1fr)', gap: 12 }}>
                  <Form.Item
                    name="upstream_currency"
                    label="上游计价货币"
                    style={{ marginBottom: 6 }}
                  >
                    <Select
                      options={UPSTREAM_CURRENCY_OPTIONS}
                      style={{ width: '100%' }}
                      onChange={(val) => {
                        form.setFieldsValue({ upstream_currency: val });
                        updateCalculatedRate(undefined, undefined, val);
                      }}
                    />
                  </Form.Item>
                  <Form.Item
                    name="site_currency"
                    label="本站计价货币"
                    style={{ marginBottom: 6 }}
                  >
                    <Select
                      options={SITE_CURRENCY_OPTIONS}
                      style={{ width: '100%' }}
                      onChange={(val) => {
                        form.setFieldsValue({ site_currency: val });
                        updateCalculatedRate(undefined, undefined, undefined, val);
                      }}
                    />
                  </Form.Item>
                  <Form.Item
                    name="upstream_currency_rate"
                    label="换算汇率"
                    tooltip="上游货币兑换本站货币的汇率，例如 1 USD = 7.2 CNY 时填 7.2"
                    style={{ marginBottom: 6 }}
                  >
                    <InputNumber
                      min={0.0001}
                      step={0.1}
                      precision={4}
                      placeholder="如 7.2000"
                      style={{ width: '100%' }}
                      onChange={(val) => {
                        const num = Number(val);
                        const validRate = Number.isFinite(num) && num > 0 ? num : 7.2;
                        form.setFieldsValue({ upstream_currency_rate: val });
                        updateCalculatedRate(undefined, undefined, undefined, undefined, validRate);
                      }}
                    />
                  </Form.Item>
                </div>
                {/* 实时公式与核算预览 */}
                {(() => {
                  const uCurr = (upstreamCurrency || '').trim().toUpperCase();
                  const sCurr = (siteCurrency || '').trim().toUpperCase();
                  const cRate = Number(upstreamCurrencyRate) > 0 ? Number(upstreamCurrencyRate) : 7.2;
                  const currentGroup = upstreamGroup ? upstreamGroups.find(g => g.name === upstreamGroup) : null;
                  const sampleRatio = currentGroup ? currentGroup.ratio : (selectedGroupRatio ?? 3.0);
                  const addVal = syncAddEnabled ? Number(upstreamSyncRateAdd || 0) : 0;
                  const converted = convertCurrencyRatio(sampleRatio, uCurr, sCurr, cRate);
                  const finalSample = Math.max(0, converted + addVal);
                  const finalFormatted = (Math.round(finalSample * 10000) / 10000).toFixed(4);
                  const isCnyToUsd = (uCurr === 'CNY' || uCurr === 'RMB' || uCurr === '人民币') && (sCurr === 'USD' || sCurr === '美元');
                  const isUsdToCny = (sCurr === 'CNY' || sCurr === 'RMB' || sCurr === '人民币') && (uCurr === 'USD' || uCurr === '美元');

                  return (
                    <div
                      style={{
                        marginTop: 6,
                        padding: '8px 12px',
                        background: isLight ? '#f0f5ff' : '#111d2c',
                        border: isLight ? '1px solid #d6e4ff' : '1px solid #1d39c4',
                        borderRadius: 6,
                        fontSize: 12,
                        lineHeight: 1.6,
                        color: isLight ? '#1d39c4' : '#adc6ff',
                      }}
                    >
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                        <span style={{ fontWeight: 600 }}>换算规则：</span>
                        <span>
                          {isCnyToUsd ? (
                            `上游 RMB 倍率 ÷ 汇率 (${cRate}) = 本站 USD 折扣倍率`
                          ) : isUsdToCny ? (
                            `上游 USD 倍率 × 汇率 (${cRate}) = 本站 CNY 倍率`
                          ) : !uCurr || !sCurr || uCurr === sCurr ? (
                            '上游与本站币种一致，按 1:1 原样继承，无需换算'
                          ) : (
                            `上游倍率 ÷ 汇率 (${cRate}) = 本站折算倍率`
                          )}
                        </span>
                      </div>
                      <div style={{ marginTop: 4, display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                        <span style={{ fontWeight: 600 }}>
                          {currentGroup ? `分组核算 (${currentGroup.name})：` : (upstreamGroup ? `分组核算 (${upstreamGroup})：` : '核算预览：')}
                        </span>
                        <span>
                          {sampleRatio}x
                          {isCnyToUsd ? ` ÷ ${cRate} = ` : isUsdToCny ? ` × ${cRate} = ` : ' = '}
                          <strong>{converted.toFixed(4)}x</strong>
                        </span>
                        {isCnyToUsd && (
                          <Tag color="blue" style={{ margin: 0, padding: '0 5px', fontSize: 11, height: 20, lineHeight: '18px', borderRadius: 4 }}>
                            相当于官方 {(converted * 10).toFixed(2)} 折
                          </Tag>
                        )}
                        {addVal > 0 && (
                          <Tag color="orange" style={{ margin: 0, padding: '0 5px', fontSize: 11, height: 20, lineHeight: '18px', borderRadius: 4 }}>
                            ＋增量 {addVal} ➔ 最终 {finalFormatted}x
                          </Tag>
                        )}
                        <Tag
                          color="green"
                          style={{ margin: 0, padding: '0 6px', fontSize: 11, height: 20, lineHeight: '18px', borderRadius: 4, cursor: 'pointer' }}
                          onClick={() => updateCalculatedRate()}
                          title="点击可重新触发写入上方渠道倍率"
                        >
                          已实时更新到渠道倍率: {finalFormatted}x
                        </Tag>
                      </div>
                    </div>
                  );
                })()}
              </div>
              <div style={{ display: 'flex', gap: 12 }}>
                <Form.Item
                  name="upstream_sync_interval_minutes"
                  label="同步间隔（分钟）"
                  extra="0 为不自动同步"
                  style={{ flex: 1, marginBottom: 10 }}
                >
                  <InputNumber min={0} max={10080} precision={0} placeholder="0" style={{ width: '100%' }} />
                </Form.Item>
                <Form.Item label="同步后叠加增量" style={{ flex: 1, marginBottom: 10 }}>
                  <Space.Compact style={{ width: '100%' }}>
                    <div style={{ display: 'flex', alignItems: 'center', paddingRight: 8 }}>
                      <Switch
                        size="small"
                        checked={syncAddEnabled}
                        onChange={(checked) => {
                          setSyncAddEnabled(checked);
                          const nextAdd = checked ? Number(form.getFieldValue('upstream_sync_rate_add') || 0) : 0;
                          if (!checked) form.setFieldsValue({ upstream_sync_rate_add: 0 });
                          updateCalculatedRate(undefined, nextAdd, undefined, undefined, undefined, checked);
                        }}
                      />
                    </div>
                    <Form.Item name="upstream_sync_rate_add" noStyle>
                      <InputNumber
                        min={0}
                        step={0.01}
                        precision={4}
                        disabled={!syncAddEnabled}
                        placeholder="0"
                        style={{ width: '100%' }}
                        onChange={(value) => {
                          const add = Number(value) || 0;
                          form.setFieldsValue({ upstream_sync_rate_add: add });
                          updateCalculatedRate(undefined, add);
                        }}
                      />
                    </Form.Item>
                  </Space.Compact>
                </Form.Item>
              </div>
              {editingConfig?.id ? (
                <div
                  style={{
                    background: isLight ? '#f9fafb' : '#141414',
                    border: isLight ? '1px solid #f0f0f0' : '1px solid #303030',
                    borderRadius: 8,
                    padding: '10px 12px',
                    marginBottom: 12,
                  }}
                >
                  {renderSyncLogsHeader({
                    ...editingConfig,
                    upstream_sync_interval_minutes: Number(upstreamSyncIntervalMinutes ?? editingConfig.upstream_sync_interval_minutes ?? 0),
                  })}
                  <div style={{ marginTop: 6 }}>
                    {renderSyncLogsList(editingConfig, 160)}
                  </div>
                </div>
              ) : null}
            </>
          )}
          <Form.Item
            name="remark"
            label={
              <Tooltip title="记录渠道归属、适用场景等，方便查阅">
                <span>备注说明</span>
              </Tooltip>
            }
            style={{ marginBottom: 10 }}
          >
            <Input.TextArea rows={1} placeholder="例如：图片生成主通道..." autoSize={{ minRows: 1, maxRows: 3 }} />
          </Form.Item>
          <Form.Item
            name="status"
            label="状态"
            initialValue={1}
            valuePropName="checked"
            getValueFromEvent={(checked: boolean) => (checked ? 1 : 0)}
            getValueProps={(value) => ({ checked: value !== 0 })}
            style={{ marginBottom: 0 }}
          >
            <Switch checkedChildren={t('common.active')} unCheckedChildren={t('common.disabled')} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title="日额度刷新设置"
        open={dailyResetModalOpen}
        onCancel={() => setDailyResetModalOpen(false)}
        onOk={saveDailyResetModal}
        okText="保存"
        cancelText="取消"
        width={360}
        destroyOnHidden
        getContainer={() => document.body}
        zIndex={1100}
      >
        <Form layout="vertical" size="small" style={{ marginTop: 8 }}>
          <Form.Item
            label={`每天刷新时间点${timedisplayOffsetSuffix(quotaTz)}`}
            style={{ marginBottom: 12 }}
          >
            <TimePicker
              format="HH:mm"
              allowClear={false}
              style={{ width: '100%' }}
              getPopupContainer={(node) => node.parentElement || document.body}
              value={dayjs().hour(dailyResetDraft.hour).minute(dailyResetDraft.minute).second(0)}
              onChange={(t) => {
                setDailyResetDraft((prev) => ({
                  ...prev,
                  hour: t ? t.hour() : 0,
                  minute: t ? t.minute() : 0,
                }));
              }}
            />
          </Form.Item>
          <Form.Item
            label="刷新冷却（分钟）"
            extra="到达时间点后再等待该时长才清零日已用"
            style={{ marginBottom: 8 }}
          >
            <InputNumber
              min={0}
              max={1440}
              placeholder="0"
              style={{ width: '100%' }}
              addonAfter="分钟"
              value={dailyResetDraft.cooldown}
              onChange={(v) => setDailyResetDraft((prev) => ({
                ...prev,
                cooldown: v == null ? 0 : Number(v),
              }))}
            />
          </Form.Item>
          <Text type="secondary" style={{ fontSize: 11 }}>
            按站点运营时区计算（非系统底层 UTC）；默认 00:00 / 冷却 0 即自然日刷新
          </Text>
        </Form>
      </Modal>

      <Modal
        title="复制上游渠道配置"
        open={!!copyTarget}
        onCancel={() => setCopyTarget(null)}
        onOk={handleCopy}
        confirmLoading={copying}
        okText={t('common.confirm', '确定')}
        cancelText={t('common.cancel', '取消')}
        destroyOnHidden
        centered
        width={440}
      >
        <div style={{ padding: '8px 0' }}>
          <Text type="secondary">请输入复制后的新上游渠道配置名称，其余参数与密钥将完整保留：</Text>
          <Input
            style={{ marginTop: 12 }}
            value={copyName}
            onChange={(e) => setCopyName(e.target.value)}
            placeholder={copyTarget?.name || '请输入新的配置名称'}
            onPressEnter={handleCopy}
            autoFocus
          />
        </div>
      </Modal>

      <ChannelCategoryManager
        visible={isCategoryManagerVisible}
        onClose={() => setIsCategoryManagerVisible(false)}
        onUpdate={fetchCategories}
      />

      <ChannelCategoryManager
        mode="provider"
        visible={isProviderManagerVisible}
        onClose={() => setIsProviderManagerVisible(false)}
        onUpdate={() => {
          fetchProviders();
          fetchConfigs();
        }}
      />
    </Card>
  );
};

export default ChannelConfigs;
