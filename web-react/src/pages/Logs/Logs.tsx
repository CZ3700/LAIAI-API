import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Navigate, useSearchParams } from 'react-router-dom';
import { Table, Tag, Card, Typography, Space, Input, Button, Row, Col, Descriptions, theme, Grid, Tooltip, message, Modal, Spin, Select, Checkbox, Segmented, Dropdown } from 'antd';
import { QuestionCircleOutlined } from '@ant-design/icons';
import MobileCardList, { MobileCard, CardRow } from '../../components/MobileCardList';
import { listPagination, useListPager } from '../../components/ListPagination';
import { RefreshCw, Search, Download, Copy, MoreHorizontal } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import { QueryGuard, isRequestAborted } from '../../utils/queryGuard';
import useSettingsStore from '../../store/settings';
import useAuthStore from '../../store/auth';
import { useThemeStore } from '../../store/theme';
import type { RequestLog } from '../../types';
import dayjs from 'dayjs';
import utc from 'dayjs/plugin/utc';
import { formatApiDateTime } from '../../utils/timedisplay';
import { defaultLogDayRange, toDateRangeParams } from '../../utils/dateRangeParams';
import LogDateTimeRangePicker from '../../components/LogDateTimeRangePicker';
import { useLogDetailLoader } from '../../hooks/useLogDetailLoader';
import { useDragScroll } from '../../hooks/useDragScroll';
import { parsePluginTagMeta } from '../../utils/pluginTagMeta';
import { copyToClipboard } from '../../utils/clipboard';
import { LOG_ERROR_STATUS_CODES } from '../../constants/relayStatusCodes';
import { useLogTypeRoute } from './logTypeRoutes';
import { LogTypeTabs, buildLogTypeTabOptions } from './LogTypeTabs';
dayjs.extend(utc);

const { Text } = Typography;
const { useBreakpoint } = Grid;

const STATUS_CODE_FILTER_VALUES = LOG_ERROR_STATUS_CODES;

const LOG_EXPORT_LIMIT = 100_000;

/** 与后端 `LOG_EXPORT_COLUMNS` 及列表列序对齐 */
const LOG_EXPORT_FIELDS: { key: string; label: string }[] = [
  { key: 'created_at', label: '时间' },
  { key: 'log_id', label: '日志ID' },
  { key: 'task_id', label: '任务ID' },
  { key: 'upstream_request_id', label: '厂商排查ID' },
  { key: 'id', label: 'ID' },
  { key: 'channel_group_aid', label: '渠道AID' },
  { key: 'is_ha', label: '渠道HA' },
  { key: 'yid', label: '上游YID' },
  { key: 'sub_channel_name', label: '实际上游' },
  { key: 'user_nickname', label: '用户昵称' },
  { key: 'user_uid', label: 'UID' },
  { key: 'user_id', label: '用户ID' },
  { key: 'user_admin_remark', label: '管理员备注' },
  { key: 'token_name', label: '令牌' },
  { key: 'token_kid', label: '密钥KID' },
  { key: 'token_ha', label: '令牌HA' },
  { key: 'token_pg', label: '令牌PG' },
  { key: 'status_code', label: '状态码' },
  { key: 'model', label: '模型' },
  { key: 'billing_pid', label: '计费PID' },
  { key: 'forward_eid', label: '转发EID' },
  { key: 'latency_ms', label: '耗时(ms)' },
  { key: 'is_stream', label: '类型' },
  { key: 'prompt_tokens', label: '输入Tokens' },
  { key: 'completion_tokens', label: '输出Tokens' },
  { key: 'cached_tokens', label: '缓存Tokens' },
  { key: 'cost', label: '费用' },
  { key: 'billing_detail', label: '计费明细' },
  { key: 'error_message', label: '错误信息' },
  { key: 'action_type', label: '动作类型' },
  { key: 'endpoint', label: '请求路径' },
];

const LOG_EXPORT_ALL_KEYS = LOG_EXPORT_FIELDS.map((f) => f.key);

/** 筛选栏错误码 → 请求参数；仅非负整数，否则不传 */
function parseStatusCodeFilter(raw?: string): number | undefined {
  const s = raw?.trim() ?? '';
  return /^\d+$/.test(s) ? Number(s) : undefined;
}

/** 精简金额格式化（最多 6 位小数，去除末尾无效的 0 与小数点） */
function formatCleanCost(cost: number | undefined | null): string {
  if (cost === undefined || cost === null || isNaN(Number(cost))) return '0';
  return String(Number(Number(cost).toFixed(6)));
}

type TokenUnit = 'k' | 'm' | 'yi';
const TOKEN_UNIT_STORAGE_KEY = 'logs_token_unit';
const TOKEN_UNIT_DIVISOR: Record<TokenUnit, number> = { k: 1_000, m: 1_000_000, yi: 100_000_000 };

function readTokenUnit(): TokenUnit {
  try {
    const v = localStorage.getItem(TOKEN_UNIT_STORAGE_KEY);
    if (v === 'k' || v === 'm' || v === 'yi') return v;
  } catch { /* ignore */ }
  return 'k';
}

function formatTokenTotal(n: number | undefined | null, unit: TokenUnit): string {
  const v = Number(n) / TOKEN_UNIT_DIVISOR[unit];
  if (!Number.isFinite(v)) return '0';
  return String(Number(v.toFixed(3)));
}

type LogListFilters = {
  modelFilter?: string;
  searchKeyword?: string;
  userFilter?: string;
  statusFilter?: string;
  tokenTypeFilter?: string;
  statusCodeFilter?: string;
  dateRange?: [any, any] | null;
  actionTypeFilter?: string;
  subTypeFilter?: string;
  routerEp?: string;
  userGroupFilter?: string;
};

/** 列表/导出共用查询条件（不含 page/per_page） */
function buildLogListParams(f: LogListFilters): Record<string, unknown> {
  const params: Record<string, unknown> = {};
  const model = f.modelFilter?.trim();
  if (model) params.model = model;
  if (f.routerEp) params.router_ep = f.routerEp;
  const keyword = f.searchKeyword?.trim();
  if (keyword) params.search_keyword = keyword;
  const uid = f.userFilter?.trim();
  if (uid) params.user_id = uid;
  if (f.userGroupFilter) params.user_group = f.userGroupFilter;
  if (f.statusFilter) params.status = f.statusFilter;
  if (f.tokenTypeFilter) params.token_type = f.tokenTypeFilter;
  const statusCode = parseStatusCodeFilter(f.statusCodeFilter);
  if (statusCode !== undefined) params.status_code = statusCode;
  Object.assign(params, toDateRangeParams(f.dateRange));
  if (f.actionTypeFilter === '视觉' && f.subTypeFilter) {
    params.action_type = f.subTypeFilter;
  } else if (f.actionTypeFilter && f.actionTypeFilter !== '全部') {
    params.action_type = f.actionTypeFilter;
  }
  return params;
}

/** overrides 显式传入 undefined 时覆盖当前值（勿用 ?? / !== undefined） */
function pickFilter<T>(overrides: object | undefined, key: string, current: T): T {
  if (overrides != null && Object.prototype.hasOwnProperty.call(overrides, key)) {
    return (overrides as Record<string, unknown>)[key] as T;
  }
  return current;
}


const CopyButton: React.FC<{ text: string, color?: string }> = ({ text, color }) => {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);

  return (
    <Tooltip title={copied ? t('logs.copy_success', '已复制') : t('logs.copy', '复制')} open={copied || undefined}>
      <Button 
        size="small" 
        type="text" 
        icon={<Copy size={14} />} 
        onClick={async () => {
          const ok = await copyToClipboard(text);
          if (ok) {
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
          }
        }} 
        style={{ color: color, height: 22, padding: '0 8px' }}
      >
        {t('logs.copy', '复制')}
      </Button>
    </Tooltip>
  );
};


/** 列表用量：缓存创建（后端解析）+ 缓存读取（cached_tokens）+ 联网次数 */
function billingUsageMetrics(record: RequestLog) {
  const cacheCreation = record.billing_cache_creation ?? 0;
  const cacheRead = record.cached_tokens ?? 0;
  const webSearch = record.billing_web_search ?? 0;
  return { cacheCreation, cacheRead, webSearch };
}

function prettyJson(raw?: string | null): string | null | undefined {
  if (!raw) return raw;
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

function clientCtColor(ct: string) {
  return ct.includes('multipart') ? 'orange' : 'blue';
}

const Logs: React.FC<{ routerEp?: string }> = ({ routerEp }) => {
  const { t } = useTranslation();
  const { token: themeToken } = theme.useToken();
  const { settings } = useSettingsStore();
  const { themeMode } = useThemeStore();
  const _isLight = themeMode === 'light';
  const currencySymbol = settings?.currency?.currency_symbol || '$';
  const auxiliaryCurrencies = useMemo(() => {
    const list = settings?.currency?.auxiliary_currencies;
    return Array.isArray(list) ? list.filter(c => c.enabled) : [];
  }, [settings?.currency?.auxiliary_currencies]);
  const [selectedCurrencyCode, setSelectedCurrencyCode] = useState<string>('');
  const formatDisplayCost = useCallback((cost: number | undefined | null): string => {
    const num = Number(cost);
    if (cost === undefined || cost === null || isNaN(num)) return `${currencySymbol}0`;
    if (selectedCurrencyCode) {
      const curr = auxiliaryCurrencies.find(c => c.code === selectedCurrencyCode);
      if (curr) {
        return `${curr.symbol}${formatCleanCost(num * curr.exchange_rate)}`;
      }
    }
    return `${currencySymbol}${formatCleanCost(num)}`;
  }, [currencySymbol, selectedCurrencyCode, auxiliaryCurrencies]);
  const statusCodeOptions = STATUS_CODE_FILTER_VALUES.map((value) => ({
    value,
    label: t(`logs.status_code_${value}`),
  }));
  const [logs, setLogs] = useState<RequestLog[]>([]);
  const [loading, setLoading] = useState(true);
  const [total, setTotal] = useState(0);
  const { page, pageSize, setPage, onChange } = useListPager();
  const [modelFilter, setModelFilter] = useState('');
  const [searchKeyword, setSearchKeyword] = useState('');
  const [searchParams] = useSearchParams();
  const { actionTypeFilter, redirectTo, goLogType, defaultLogType, setDefaultLogType } = useLogTypeRoute('logs');
  const logTypeOptions = useMemo(() => buildLogTypeTabOptions(t), [t]);
  const [userFilter, setUserFilter] = useState<string | undefined>(searchParams.get('user_id') || undefined);
  const [subTypeFilter, setSubTypeFilter] = useState<string | undefined>(undefined);
  const [statusFilter, setStatusFilter] = useState<string | undefined>(undefined);
  const [tokenTypeFilter, setTokenTypeFilter] = useState<string | undefined>(undefined);
  const [statusCodeFilter, setStatusCodeFilter] = useState<string | undefined>(undefined);
  const [userGroupFilter, setUserGroupFilter] = useState<string | undefined>(undefined);
  const [userLevels, setUserLevels] = useState<any[]>([]);
  const [allowDetails, setAllowDetails] = useState(true);
  const [dateRange, setDateRange] = useState<[any, any] | null>(() => defaultLogDayRange());
  const [exporting, setExporting] = useState(false);
  const [exportModalOpen, setExportModalOpen] = useState(false);
  const [exportFieldKeys, setExportFieldKeys] = useState<string[]>(LOG_EXPORT_ALL_KEYS);
  const [stats, setStats] = useState<{
    total_cost: number;
    success_count: number;
    fail_count: number;
    total_prompt_tokens: number;
    total_completion_tokens: number;
    total_with_video_tokens?: number;
  }>({
    total_cost: 0,
    success_count: 0,
    fail_count: 0,
    total_prompt_tokens: 0,
    total_completion_tokens: 0,
    total_with_video_tokens: 0,
  });
  const [tokenUnit, setTokenUnit] = useState<TokenUnit>(readTokenUnit);
  const { user } = useAuthStore();
  const isAdmin = user?.role === 'admin';
  const isSuperAdmin = isAdmin && !user?.admin_group_id;
  const screens = useBreakpoint();
  const [isSettingsModalVisible, setIsSettingsModalVisible] = useState(false);
  const [tempDefaultType, setTempDefaultType] = useState<string>('视觉');
  const queryGuardRef = useRef(new QueryGuard());
  const skipNextEffectFetchRef = useRef(false);
  const dragScrollRef = useDragScroll<HTMLDivElement>();
  const rowIds = useMemo(() => logs.map((l) => l.id), [logs]);
  const {
    detailCache,
    detailLoadingIds,
    expandedRowKeys,
    handleExpand,
    resetDetailCache,
  } = useLogDetailLoader(rowIds);

  useEffect(() => {
    const guard = queryGuardRef.current;
    return () => guard.dispose();
  }, []);

  useEffect(() => {
    if (isSettingsModalVisible) {
      setTempDefaultType(defaultLogType);
    }
  }, [isSettingsModalVisible, defaultLogType]);

  useEffect(() => {
    if (user?.role !== 'admin') return;
    request.get('/user_levels').then((res: any) => setUserLevels(res.data || [])).catch(console.error);
  }, [user]);

  const buildParams = useCallback(
    () => buildLogListParams({
      modelFilter,
      searchKeyword,
      userFilter,
      statusFilter,
      tokenTypeFilter,
      statusCodeFilter,
      dateRange,
      actionTypeFilter,
      subTypeFilter,
      routerEp,
      userGroupFilter,
    }),
    [modelFilter, searchKeyword, userFilter, statusFilter, tokenTypeFilter, statusCodeFilter, dateRange, actionTypeFilter, subTypeFilter, routerEp, userGroupFilter],
  );

  const fetchLogs = useCallback(async (overrides?: {
    page?: number;
    pageSize?: number;
    modelFilter?: string;
    searchKeyword?: string;
    userFilter?: string | undefined;
    statusFilter?: string | undefined;
    tokenTypeFilter?: string | undefined;
    statusCodeFilter?: string | undefined;
    dateRange?: [any, any] | null;
    actionTypeFilter?: string;
    subTypeFilter?: string | undefined;
    userGroupFilter?: string | undefined;
  }) => {
    const signal = queryGuardRef.current.begin();
    setLoading(true);
    try {
      const o = overrides as object | undefined;
      const targetPage = overrides?.page ?? page;
      const targetPageSize = overrides?.pageSize ?? pageSize;
      if (targetPage !== page) {
        skipNextEffectFetchRef.current = true;
        setPage(targetPage);
      }
      const isPaging = targetPage > 1;

      const params: Record<string, unknown> = {
        page: targetPage,
        per_page: targetPageSize,
        ...buildLogListParams({
          modelFilter: pickFilter(o, 'modelFilter', modelFilter),
          searchKeyword: pickFilter(o, 'searchKeyword', searchKeyword),
          userFilter: pickFilter(o, 'userFilter', userFilter),
          statusFilter: pickFilter(o, 'statusFilter', statusFilter),
          tokenTypeFilter: pickFilter(o, 'tokenTypeFilter', tokenTypeFilter),
          statusCodeFilter: pickFilter(o, 'statusCodeFilter', statusCodeFilter),
          dateRange: pickFilter(o, 'dateRange', dateRange),
          actionTypeFilter: pickFilter(o, 'actionTypeFilter', actionTypeFilter),
          subTypeFilter: pickFilter(o, 'subTypeFilter', subTypeFilter),
          userGroupFilter: pickFilter(o, 'userGroupFilter', userGroupFilter),
          routerEp,
        }),
      };

      if (isPaging && total > 0) {
        params.known_total = total;
      }

      const resp = await (request.get('/logs', { params, signal }) as unknown as Promise<{
        data: RequestLog[];
        total: number;
        allow_details?: boolean;
        total_cost?: number;
        success_count?: number;
        fail_count?: number;
        total_prompt_tokens?: number;
        total_completion_tokens?: number;
        total_with_video_tokens?: number;
      }>);
      if (!queryGuardRef.current.isCurrent(signal)) return;
      setLogs(resp.data);
      if (typeof resp.total === 'number' && resp.total >= 0) {
        setTotal(resp.total);
      }
      resetDetailCache();
      if (resp.allow_details !== undefined) {
        setAllowDetails(resp.allow_details);
      }
      // 仅在非翻页（首屏/重新搜索/条件切换）时更新全局统计指标，避免翻页时清空或闪烁
      if (!isPaging) {
        setStats({
          total_cost: resp.total_cost || 0,
          success_count: resp.success_count || 0,
          fail_count: resp.fail_count || 0,
          total_prompt_tokens: resp.total_prompt_tokens || 0,
          total_completion_tokens: resp.total_completion_tokens || 0,
          total_with_video_tokens: resp.total_with_video_tokens || 0,
        });
      }
    } catch (e) {
      if (isRequestAborted(e)) return;
      console.error(e);
    } finally {
      if (queryGuardRef.current.isCurrent(signal)) {
        setLoading(false);
      }
    }
  }, [page, pageSize, total, modelFilter, searchKeyword, userFilter, statusFilter, tokenTypeFilter, statusCodeFilter, dateRange, routerEp, actionTypeFilter, subTypeFilter, userGroupFilter, resetDetailCache]);

  const openExportModal = () => {
    setExportFieldKeys(LOG_EXPORT_ALL_KEYS);
    setExportModalOpen(true);
  };

  const handleExport = async () => {
    if (exportFieldKeys.length === 0) {
      message.warning(t('logs.export_need_field', '请至少选择一个字段'));
      return;
    }
    setExporting(true);
    try {
      const params = buildParams();
      if (exportFieldKeys.length !== LOG_EXPORT_FIELDS.length) {
        params.export_fields = exportFieldKeys.join(',');
      }
      const resp = await request.get('/logs/export', {
        params,
        responseType: 'blob',
        skipErrorHandler: true,
      } as any) as any;
      // 如果返回的是 JSON 错误（blob mimetype 为 application/json），解析提示
      if (resp instanceof Blob && resp.type?.includes('json')) {
        const text = await resp.text();
        const json = JSON.parse(text);
        message.error(json.error || t('logs.export_fail', '导出失败'));
        return;
      }
      const blob = resp instanceof Blob ? resp : new Blob([resp], { type: 'text/csv' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `usage_logs_${dayjs().format('YYYYMMDDHHmmss')}.csv`;
      a.click();
      URL.revokeObjectURL(url);
      message.success(t('logs.export_success', '导出成功'));
      setExportModalOpen(false);
    } catch (e: any) {
      // 尝试从 axios 错误响应中提取后端消息
      if (e?.response?.data) {
        try {
          const blob = e.response.data;
          const text = blob instanceof Blob ? await blob.text() : JSON.stringify(blob);
          const json = JSON.parse(text);
          message.error(json.error || t('logs.export_fail', '导出失败'));
        } catch { message.error(t('logs.export_fail', '导出失败')); }
      } else {
        message.error(t('logs.export_fail', '导出失败'));
      }
    } finally {
      setExporting(false);
    }
  };

  useEffect(() => {
    if (redirectTo) return;
    if (skipNextEffectFetchRef.current) {
      skipNextEffectFetchRef.current = false;
      return;
    }
    fetchLogs();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page, pageSize, actionTypeFilter, subTypeFilter, statusFilter, tokenTypeFilter, userGroupFilter, redirectTo]);

  const columns = ([
    {
      title: t('logs.time'),
      dataIndex: 'created_at',
      key: 'created_at',
      align: 'left' as const,
      width: 190,
      render: (text: string, record: RequestLog) => (
        <Space direction="vertical" size={0} style={{ width: '100%', alignItems: 'flex-start' }}>
          <Text style={{ fontSize: 12 }}>{formatApiDateTime(text)}</Text>
          {record.log_id && (
            <Text 
              type="secondary" 
              style={{ fontSize: 10, fontFamily: 'monospace', maxWidth: 140 }} 
              ellipsis={{ tooltip: record.log_id }}
              copyable={{ text: record.log_id, tooltips: [t('logs.copy', '复制'), t('logs.copy_success', '已复制')] }}
            >
              {record.log_id}
            </Text>
          )}
          {record.task_id && (
            <Text 
              type="secondary" 
              style={{ fontSize: 10, fontFamily: 'monospace', maxWidth: 140, color: '#1677ff' }} 
              ellipsis={{ tooltip: `Task ID: ${record.task_id}` }}
              copyable={{ text: record.task_id, tooltips: [t('logs.copy', '复制'), t('logs.copy_success', '已复制')] }}
            >
              {record.task_id}
            </Text>
          )}
        </Space>
      ),
    },
    user?.role === 'admin' ? {
      title: t('logs.channel_aid', '渠道信息'),
      dataIndex: 'channel_group_aid',
      key: 'channel_group_aid',
      align: 'left' as const,
      width: 160,
      render: (text: string, record: RequestLog) => (
        <Space size={2} direction="vertical" style={{ width: '100%', alignItems: 'flex-start' }}>
          <Space size={4} style={{ maxWidth: '100%' }}>
            <Text type="secondary" style={{ fontSize: 12 }}>{text ? `AID: ${text}` : '-'}</Text>
            {record.is_ha === 1 && <Tag color="blue" style={{ fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '16px' }}>HA</Tag>}
          </Space>
          {record.sub_channel_name && (
            <Text
              style={{ fontSize: 12, maxWidth: 150, display: 'inline-block' }}
              ellipsis={{ tooltip: `${t('logs.sub_channel_name', '实际调用上游')}: ${record.sub_channel_name}` }}
            >
              {record.sub_channel_name}
            </Text>
          )}
          {record.yid && (
            <Text
              style={{ fontSize: 12, color: '#13c2c2', display: 'inline-block' }}
            >
              {t('logs.sub_channel', '上游')}: {record.yid}
            </Text>
          )}
        </Space>
      ),
    } : null,
    user?.role === 'admin' ? {
      title: t('logs.user', '用户'),
      key: 'user',
      align: 'left' as const,
      width: 180,
      render: (_: any, record: RequestLog) => {
        const name = record.user_nickname || record.user_uid || record.user_id?.slice(0, 8) || '-';
        const remark = record.user_admin_remark?.trim();
        return (
          <Space direction="vertical" size={0} style={{ width: '100%', alignItems: 'flex-start' }}>
            <Text style={{ fontSize: 12, maxWidth: 170 }} ellipsis={{ tooltip: name }}>
              {name}
              {remark ? <Text type="secondary" style={{ fontSize: 12 }}> {remark}</Text> : null}
            </Text>
            {record.user_uid && (
              <Text 
                type="secondary" 
                style={{ fontSize: 10, fontFamily: 'monospace' }}
                copyable={{ text: String(record.user_uid), tooltips: [t('logs.copy', '复制'), t('logs.copy_success', '已复制')] }}
              >
                UID: {record.user_uid}
              </Text>
            )}
          </Space>
        );
      },
    } : null,
    {
      title: t('logs.token', '令牌'),
      key: 'token_name',
      align: 'left' as const,
      width: 148,
      filters: [
        { text: t('logs.token_type_ha', 'HA令牌'), value: 'ha' },
        { text: t('logs.token_type_normal', '普通令牌'), value: 'normal' },
        { text: t('logs.token_type_pg', 'PG创作中心令牌'), value: 'pg' },
      ],
      filterMultiple: false,
      filteredValue: tokenTypeFilter ? [tokenTypeFilter] : null,
      render: (_: any, record: RequestLog) => (
        <Space direction="vertical" size={0} style={{ width: '100%', alignItems: 'flex-start' }}>
          <Space size={4}>
            <Text style={{ fontSize: 12 }}>{record.token_name || '-'}</Text>
            {record.token_pg === 1 && <Tag color="orange" style={{ fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '16px' }}>PG</Tag>}
            {record.token_ha === 1 && <Tag color="blue" style={{ fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '16px' }}>HA</Tag>}
          </Space>
          {record.token_kid && <Text type="secondary" style={{ fontSize: 10, fontFamily: 'monospace' }}>KID: {record.token_kid}</Text>}
        </Space>
      ),
    },

    {
      title: t('logs.status'),
      dataIndex: 'status_code',
      key: 'status_code',
      width: 100,
      filters: [
        { text: t('logs.success', '成功'), value: 'success' },
        { text: t('logs.status_processing', '处理中'), value: 'processing' },
        { text: t('logs.fail', '失败'), value: 'fail' },
      ],
      filterMultiple: false,
      filteredValue: statusFilter ? [statusFilter] : null,
      render: (code: number) => {
        if (code === 0) {
          return <Tag icon={<RefreshCw size={14} className="anticon-spin" />} color="processing" style={{ display: 'inline-flex', alignItems: 'center' }}>{t('logs.processing', '处理中')}</Tag>;
        }
        return <Tag color={code === 200 ? 'success' : 'error'}>{code || 400}</Tag>;
      },
    },
    {
      title: t('logs.model'),
      dataIndex: 'model',
      key: 'model',
      align: 'left' as const,
      width: 180,
      ellipsis: true,
      filters: actionTypeFilter === '视觉' ? [
        { text: t('logs.type_video', '视频'), value: '视频' },
        { text: t('logs.type_image', '图片'), value: '图片' },
      ] : undefined,
      filterMultiple: false,
      filteredValue: actionTypeFilter === '视觉' && subTypeFilter ? [subTypeFilter] : null,
      render: (text: string, record: RequestLog) => (
        <Space direction="vertical" size={0} style={{ width: '100%', alignItems: 'flex-start' }}>
          <Space size={4} align="center">
            <Text style={{ fontSize: 12 }}>{text}</Text>
            {record.endpoint?.startsWith('test|') && (
              <Tag color="cyan" style={{ fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '16px', borderRadius: 3 }}>
                {t('logs.channel_test_tag', '拨测')}
              </Tag>
            )}
          </Space>
          {user?.role === 'admin' && (record.mid || record.billing_pid || record.forward_eid) && (
            <Space size={4}>
              {record.mid && <Text type="secondary" style={{ fontSize: 10, fontFamily: 'monospace' }}>MID:{record.mid}</Text>}
              {record.billing_pid && <Text type="secondary" style={{ fontSize: 10, fontFamily: 'monospace' }}>PID:{record.billing_pid}</Text>}
              {record.forward_eid && <Text type="secondary" style={{ fontSize: 10, fontFamily: 'monospace' }}>EID:{record.forward_eid}</Text>}
            </Space>
          )}
        </Space>
      ),
    },
    {
      title: t('logs.latency'),
      dataIndex: 'latency_ms',
      key: 'latency_ms',
      width: 80,
      render: (val: number) => <Text style={{ fontSize: 12 }}>{(val / 1000).toFixed(3)}s</Text>,
    },
    {
      title: t('logs.type', '类型'),
      dataIndex: 'is_stream',
      key: 'is_stream',
      width: 60,
      render: (stream: number) => <Text type="secondary" style={{ fontSize: 12 }}>{stream === 1 ? t('logs.stream', '流') : t('logs.non_stream', '非流')}</Text>,
    },
    {
      title: t('logs.usage'),
      key: 'usage',
      width: 100,
      render: (_: any, record: RequestLog) => {
        const { cacheCreation, cacheRead, webSearch } = billingUsageMetrics(record);
        return (
          <Space direction="vertical" size={0}>
            <Text type="secondary" style={{ fontSize: 11 }}>{t('logs.input', '输入')}: {record.prompt_tokens}</Text>
            <Text type="secondary" style={{ fontSize: 11 }}>
              {t('logs.output', '输出')}: {record.completion_tokens}
              {(record.action_type === '视频' || (record.action_type === '封装' && !record.endpoint?.includes('image'))) && (record.has_video === 1 ? ` ${t('logs.with_video_short', '含视')}` : ` ${t('logs.without_video_short', '无视')}`)}
            </Text>
            {cacheCreation > 0 && <Text type="secondary" style={{ fontSize: 11, color: '#faad14' }}>{t('logs.cache_creation', '缓存创建')}: {cacheCreation}</Text>}
            {cacheRead > 0 && <Text type="secondary" style={{ fontSize: 11, color: '#52c41a' }}>{t('logs.cache_read', '缓存读取')}: {cacheRead}</Text>}
            {webSearch > 0 && <Text type="secondary" style={{ fontSize: 11, color: '#1677ff' }}>联网搜索: {webSearch}次</Text>}
          </Space>
        );
      },
    },
    {
      title: t('logs.cost'),
      dataIndex: 'cost',
      key: 'cost',
      width: 100,
      fixed: 'right' as const,
      render: (val: number, record: RequestLog) => (
        <Space direction="vertical" size={0}>
          {val === 0 || record.billing_refunded || record.billing_failed
            ? <Text type="secondary" style={{ fontSize: 12 }}>-</Text>
            : <Text strong style={{ fontSize: 12, color: themeToken.colorError }}>{formatDisplayCost(val)}</Text>
          }
          {record.billing_refunded && (
            <Tag color="orange" style={{ margin: 0, fontSize: 10, lineHeight: '14px', padding: '0 4px' }}>{t('logs.refunded', '已退费')}</Tag>
          )}
        </Space>
      ),
    },
  ].map((c: any) => c ? { align: 'center', ...c } : null).filter(Boolean)) as any[];

  const expandedRowRender = (record: RequestLog) => {
    const merged = { ...record, ...detailCache[record.id] };
    const loadingDetail = !!detailLoadingIds[record.id] && !detailCache[record.id];
    const reqJson = prettyJson(merged.request_content);
    const respJson = prettyJson(merged.response_content);
    const postRespJson = prettyJson(merged.post_response);
    // 上游出参仅超管展示；用户端 API 已不返回该字段
    const upstreamReqJson = user?.role === 'admin' ? prettyJson(merged.upstream_req_content) : null;

    const costFormula = t('logs.cost_formula_dynamic', '由绑定的计费模板动态结算');
    const { clientCt, cascadeS1TaskId, upstreamTaskId } = parsePluginTagMeta(merged.plugin_tag);

    // 使用 antd theme token 来适配深色/浅色主题
    const panelBg = themeToken.colorBgElevated;
    const codeBg = themeToken.colorFillQuaternary;
    const codeBorder = themeToken.colorBorderSecondary;

    return (
      <div style={{
        padding: 16, background: panelBg, borderRadius: 8,
        maxWidth: screens.xs ? 'calc(100vw - 64px)' : 'calc(100vw - 320px)',
        overflowX: 'auto', boxSizing: 'border-box'
      }}>
        <Descriptions size="small" column={1} styles={{ label: { width: 110, whiteSpace: 'nowrap', color: themeToken.colorTextSecondary }, content: { wordBreak: 'break-all', whiteSpace: 'pre-wrap' } }}>
          <Descriptions.Item label={t('logs.system_endpoint', '系统请求路径')}>
            {record.endpoint.startsWith('http') ? record.endpoint : `${window.location.origin}${record.endpoint.startsWith('/') ? '' : '/'}${record.endpoint}`}
          </Descriptions.Item>
          {clientCt && (
            <Descriptions.Item label={t('logs.client_ct', '请求提交类型')}>
              <Tag color={clientCtColor(clientCt)}>{clientCt}</Tag>
            </Descriptions.Item>
          )}
          {record.upstream_url && (
            <Descriptions.Item label={t('logs.upstream_url', '真实上游地址')}>
              {record.upstream_url}
            </Descriptions.Item>
          )}
          {user?.role === 'admin' && (
            <Descriptions.Item label={t('logs.channel_aid', '渠道信息')}>
              <Space size={4}>
                <span>{record.channel_group_aid ? `AID: ${record.channel_group_aid}` : '-'}</span>
                {record.is_ha === 1 && <Tag color="blue" style={{ fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '16px' }}>HA</Tag>}
              </Space>
            </Descriptions.Item>
          )}
          {user?.role === 'admin' && record.sub_channel_name && (
            <Descriptions.Item label={t('logs.sub_channel_name', '实际调用上游')}>
              <Tag color="cyan">{record.sub_channel_name}</Tag>
            </Descriptions.Item>
          )}
          {merged.upstream_request_id && (
            <Descriptions.Item label={
              <span style={{ display: 'inline-flex', alignItems: 'center' }}>
                {t('logs.upstream_request_id', '厂商排查ID')}
                <Tooltip title={t('logs.upstream_request_id_tip', '上游响应追踪凭据，用于找厂商排障')}>
                  <QuestionCircleOutlined style={{ marginLeft: 4, color: themeToken.colorTextTertiary, cursor: 'help' }} />
                </Tooltip>
              </span>
            }>
              <Typography.Text
                copyable={{ text: merged.upstream_request_id, tooltips: [t('logs.copy', '复制'), t('logs.copy_success', '已复制')] }}
                style={{ fontSize: 12, fontFamily: 'monospace' }}
              >
                {merged.upstream_request_id}
              </Typography.Text>
            </Descriptions.Item>
          )}
          {upstreamTaskId && (
            <Descriptions.Item label={
              <span style={{ display: 'inline-flex', alignItems: 'center' }}>
                {t('logs.upstream_task_id', '上游任务ID')}
                <Tooltip title={t('logs.upstream_task_id_tip', '上游返回的真实任务号，用于到厂商控制台查单')}>
                  <QuestionCircleOutlined style={{ marginLeft: 4, color: themeToken.colorTextTertiary, cursor: 'help' }} />
                </Tooltip>
              </span>
            }>
              <Typography.Text
                copyable={{ text: upstreamTaskId, tooltips: [t('logs.copy', '复制'), t('logs.copy_success', '已复制')] }}
                style={{ fontSize: 12, fontFamily: 'monospace' }}
              >
                {upstreamTaskId}
              </Typography.Text>
            </Descriptions.Item>
          )}
          {isSuperAdmin && cascadeS1TaskId && (
            <Descriptions.Item label={
              <span style={{ display: 'inline-flex', alignItems: 'center' }}>
                {t('logs.cascade_s1_task_id', '底座任务ID')}
                <Tooltip title={t('logs.cascade_s1_task_id_tip', '级联底座原始任务号，用于上游控制台查单')}>
                  <QuestionCircleOutlined style={{ marginLeft: 4, color: themeToken.colorTextTertiary, cursor: 'help' }} />
                </Tooltip>
              </span>
            }>
              <Typography.Text
                copyable={{ text: cascadeS1TaskId, tooltips: [t('logs.copy', '复制'), t('logs.copy_success', '已复制')] }}
                style={{ fontSize: 12, fontFamily: 'monospace' }}
              >
                {cascadeS1TaskId}
              </Typography.Text>
            </Descriptions.Item>
          )}
          <Descriptions.Item label={t('logs.error_msg', '错误信息')}>
            {(() => {
              if (!record.error_message) {
                if (record.billing_refunded || record.billing_failed) {
                  return <Text type="danger">{t('logs.see_response', '查看下方的响应结果')}</Text>;
                }
                return <Text type="secondary">{t('logs.none', '无')}</Text>;
              }
              try {
                const parsed = JSON.parse(record.error_message);
                return (
                  <div style={{
                    marginTop: 4, maxHeight: 300, overflow: 'auto',
                    background: codeBg, border: `1px solid ${codeBorder}`,
                    padding: 8, borderRadius: 6,
                  }}>
                    <pre style={{ margin: 0, fontSize: 11, whiteSpace: 'pre-wrap', wordBreak: 'break-all', color: themeToken.colorError }}>
                      {JSON.stringify(parsed, null, 2)}
                    </pre>
                  </div>
                );
              } catch (e) {
                return <Text type="danger">{record.error_message}</Text>;
              }
            })()}
          </Descriptions.Item>
          {user?.role === 'admin' && (
          <Descriptions.Item label={t('logs.match_rule', '匹配规则')}>
            <Space size={16} wrap>
              <Text type="secondary" style={{ fontSize: 12 }}>{t('logs.billing_rule', '计费规则 (PID)')}: {record.billing_pid ? <Typography.Text keyboard>{record.billing_pid}</Typography.Text> : '-'}</Text>
              <Text type="secondary" style={{ fontSize: 12 }}>{t('logs.forward_rule', '转发规则 (EID)')}: {record.forward_eid ? <Typography.Text keyboard>{record.forward_eid}</Typography.Text> : '-'}</Text>
            </Space>
          </Descriptions.Item>
          )}
          <Descriptions.Item label={t('logs.billing_detail', '计费明细')}>
            <div>
              {merged.billing_detail ? (
                <Text type="secondary" style={{ fontSize: 12 }}>{t('logs.calc_basis', '计算依据')}: {merged.billing_detail}</Text>
              ) : (
                <Text type="secondary" style={{ fontSize: 12 }}>
                  {loadingDetail ? t('logs.loading_detail', '加载详情中...') : costFormula}
                </Text>
              )}
              <br />
              <Text strong style={{ fontSize: 13, textDecoration: record.billing_refunded ? 'line-through' : 'none' }}>
                {record.billing_refunded ? t('logs.pre_deduct', '预扣费') : t('logs.actual_deduct', '实际扣费')}: {formatDisplayCost(record.cost)}
              </Text>
              {record.billing_refunded && <Text type="danger" style={{ fontSize: 13, marginLeft: 8 }}>{t('logs.fully_refunded', '已全额退回')}</Text>}
              <Text type="secondary" style={{ fontSize: 11, marginLeft: 8 }}>{t('logs.cost_priority', '（优先扣令牌配额，不足扣用户余额）')}</Text>
            </div>
          </Descriptions.Item>
        </Descriptions>

        {loadingDetail ? (
          <div style={{ padding: 32, textAlign: 'center' }}><Spin tip={t('logs.loading_detail', '加载详情中...')} /></div>
        ) : (
        <Row gutter={[16, 16]} style={{ marginTop: 16 }}>
          <Col span={24} style={{ maxWidth: '100%', overflow: 'hidden' }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <Text strong style={{ fontSize: 13 }}>{t('logs.req_params', '请求参数（入参）')}</Text>
              {reqJson && <CopyButton text={reqJson} color={themeToken.colorTextSecondary} />}
            </div>
            <div style={{
              marginTop: 8, maxHeight: 300, overflow: 'auto',
              background: codeBg, border: `1px solid ${codeBorder}`,
              padding: 12, borderRadius: 6,
            }}>
              <pre style={{ margin: 0, fontSize: 11, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                {reqJson || t('logs.no_context', '该模型未开启「记录上下文」或内容为空')}
              </pre>
            </div>
          </Col>
          {user?.role === 'admin' && (
          <Col span={24} style={{ maxWidth: '100%', overflow: 'hidden' }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <Text strong style={{ fontSize: 13 }}>{t('logs.upstream_req_params', '真实转发给上游的请求参数（出参）')}</Text>
              {upstreamReqJson && <CopyButton text={upstreamReqJson} color={themeToken.colorTextSecondary} />}
            </div>
            <div style={{
              marginTop: 8, maxHeight: 300, overflow: 'auto',
              background: codeBg, border: `1px solid ${codeBorder}`,
              padding: 12, borderRadius: 6,
            }}>
              <pre style={{ margin: 0, fontSize: 11, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                {upstreamReqJson || t('logs.same_as_req', '同【请求参数】或未记录。')}
              </pre>
            </div>
          </Col>
          )}
          <Col span={24} style={{ maxWidth: '100%', overflow: 'hidden' }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <Text strong style={{ fontSize: 13 }}>{t('logs.resp_content', '响应结果')}</Text>
              {respJson && <CopyButton text={respJson} color={themeToken.colorTextSecondary} />}
            </div>
            <div style={{
              marginTop: 8, maxHeight: 300, overflow: 'auto',
              background: codeBg, border: `1px solid ${codeBorder}`,
              padding: 12, borderRadius: 6,
            }}>
              <pre style={{ margin: 0, fontSize: 11, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                {respJson || t('logs.no_context', '该模型未开启「记录上下文」或内容为空')}
              </pre>
            </div>
          </Col>
          {postRespJson && (
            <Col span={24} style={{ maxWidth: '100%', overflow: 'hidden' }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                <Text strong style={{ fontSize: 13 }}>{t('logs.post_resp_content', 'POST 提交响应结果')}</Text>
                <CopyButton text={postRespJson} color={themeToken.colorTextSecondary} />
              </div>
              <div style={{
                marginTop: 8, maxHeight: 300, overflow: 'auto',
                background: codeBg, border: `1px solid ${codeBorder}`,
                padding: 12, borderRadius: 6,
              }}>
                <pre style={{ margin: 0, fontSize: 11, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                  {postRespJson}
                </pre>
              </div>
            </Col>
          )}
        </Row>
        )}
      </div>
    );
  };

  if (redirectTo) {
    return <Navigate to={redirectTo} replace />;
  }

  return (
    <Card 
      variant="borderless" 
      style={{ 
        background: screens.xs ? 'transparent' : (_isLight ? '#fff' : 'rgba(255,255,255,0.02)'), 
        borderRadius: 12,
        boxShadow: screens.xs ? 'none' : undefined,
        maxWidth: '100%',
        overflow: 'hidden'
      }} 
      styles={{ body: { padding: screens.xs ? 0 : '12px 20px 16px', maxWidth: '100%', overflowX: 'hidden' } }}
    >
      <div style={{ marginBottom: 12 }}>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 12 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 16, flexWrap: 'wrap' }}>
            <Typography.Title level={4} style={{ margin: 0, fontSize: 18 }}>
              {t('menu.usage_logs', '使用日志')}
            </Typography.Title>
            <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
              <LogTypeTabs
                value={actionTypeFilter}
                defaultFirst={defaultLogType}
                isLight={_isLight}
                themeToken={themeToken}
                options={logTypeOptions}
                onChange={(v: string) => {
                  setSubTypeFilter(undefined);
                  setPage(1);
                  goLogType(v);
                }}
              />
              <Tooltip title={t('logs.configure_default_type', '配置默认视图')}>
                <Button
                  type="text"
                  icon={<MoreHorizontal size={16} />}
                  onClick={() => setIsSettingsModalVisible(true)}
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    borderRadius: '8px',
                    width: '32px',
                    height: '32px',
                    padding: 0,
                    background: themeToken?.colorFillAlter || (_isLight ? '#fafafa' : '#1d1d1d'),
                    border: 'none',
                    color: themeToken?.colorTextSecondary || (_isLight ? '#71717a' : '#a1a1aa'),
                  }}
                />
              </Tooltip>
            </div>
          </div>
        </div>
        {total > 0 && (
          <div style={{ marginTop: 8 }}>
            <Space size={12} wrap split={<span style={{ color: themeToken.colorBorder }}>|</span>}>
              <Text type="secondary" style={{ fontSize: 12 }}>{t('logs.requests', '请求')} <strong style={{ color: themeToken.colorText }}>{total}</strong></Text>
              <Text type="secondary" style={{ fontSize: 12 }}>{t('logs.success', '成功')} <strong style={{ color: themeToken.colorText }}>{stats.success_count}</strong></Text>
              <Text type="secondary" style={{ fontSize: 12 }}>{t('logs.fail', '失败')} <strong style={{ color: themeToken.colorText }}>{stats.fail_count}</strong></Text>
              <Text type="secondary" style={{ fontSize: 12 }}>{t('logs.success_rate', '成功率')} <strong style={{ color: themeToken.colorText }}>{total > 0 ? ((stats.success_count / total) * 100).toFixed(1) : '0.0'}%</strong></Text>
              <Text type="secondary" style={{ fontSize: 12 }}>{t('logs.total_cost', '成本合计')} <strong style={{ color: themeToken.colorText }}>{formatDisplayCost(stats.total_cost)}</strong></Text>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, fontSize: 12, color: themeToken.colorTextSecondary }}>
                <Tooltip title={`${Number(stats.total_prompt_tokens || 0).toLocaleString()} Token`}>
                  <Text type="secondary" style={{ fontSize: 12 }}>
                    {t('logs.total_prompt_tokens', '输入 Token')}{' '}
                    <strong style={{ color: themeToken.colorText }}>{formatTokenTotal(stats.total_prompt_tokens, tokenUnit)}</strong>
                  </Text>
                </Tooltip>
                <span style={{ color: themeToken.colorBorder }}>|</span>
                <Tooltip title={`${Number(stats.total_completion_tokens || 0).toLocaleString()} Token`}>
                  <Text type="secondary" style={{ fontSize: 12 }}>
                    {t('logs.total_completion_tokens', '输出 Token')}{' '}
                    <strong style={{ color: themeToken.colorText }}>{formatTokenTotal(stats.total_completion_tokens, tokenUnit)}</strong>
                  </Text>
                </Tooltip>
                {(stats.total_with_video_tokens || 0) > 0 && (
                  <>
                    <span style={{ color: themeToken.colorBorder }}>|</span>
                    <Tooltip title={`${Number(stats.total_with_video_tokens || 0).toLocaleString()} Token`}>
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        {t('logs.with_video_short', '含视')}{' '}
                        <strong style={{ color: themeToken.colorText }}>{formatTokenTotal(stats.total_with_video_tokens, tokenUnit)}</strong>
                      </Text>
                    </Tooltip>
                    <span style={{ color: themeToken.colorBorder }}>|</span>
                    <Tooltip title={`${Number(Math.max(0, (stats.total_completion_tokens || 0) - (stats.total_with_video_tokens || 0))).toLocaleString()} Token`}>
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        {t('logs.without_video_short', '无视')}{' '}
                        <strong style={{ color: themeToken.colorText }}>{formatTokenTotal(Math.max(0, (stats.total_completion_tokens || 0) - (stats.total_with_video_tokens || 0)), tokenUnit)}</strong>
                      </Text>
                    </Tooltip>
                  </>
                )}
                <Segmented
                  size="small"
                  value={tokenUnit}
                  onChange={(val) => {
                    const next = val as TokenUnit;
                    setTokenUnit(next);
                    try { localStorage.setItem(TOKEN_UNIT_STORAGE_KEY, next); } catch { /* ignore */ }
                  }}
                  options={[
                    { label: t('logs.token_unit_k', '千'), value: 'k' },
                    { label: t('logs.token_unit_m', '百万'), value: 'm' },
                    { label: t('logs.token_unit_yi', '亿'), value: 'yi' },
                  ]}
                />
              </span>
            </Space>
          </div>
        )}
      </div>

      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, marginBottom: 12, width: '100%' }}>
          {user?.role === 'admin' && (
            <>
              <Input
                placeholder={t('logs.search_user_id', '搜索用户 UID/用户名')}
                prefix={<Search size={16} />}
                value={userFilter || ''}
                onChange={e => setUserFilter(e.target.value || undefined)}
                onPressEnter={() => fetchLogs({ page: 1 })}
                style={{ width: screens.xs ? '100%' : 180, fontSize: 12, height: 32 }}
                allowClear
              />
              <Select
                placeholder={t('logs.search_user_group', '用户等级')}
                value={userGroupFilter}
                onChange={(v) => {
                  setUserGroupFilter(v ?? undefined);
                  setPage(1);
                }}
                options={userLevels.map((l: any) => ({ value: l.group_key, label: `${l.name} (${l.discount}x)` }))}
                style={{ width: screens.xs ? '100%' : 130, fontSize: 12, height: 32 }}
                popupMatchSelectWidth={false}
                allowClear
                showSearch
                optionFilterProp="label"
              />
            </>
          )}
          {user?.role !== 'admin' && userFilter && (
            <Input
              value={userFilter}
              style={{ width: screens.xs ? '100%' : 120, fontSize: 12, height: 32 }}
              disabled
            />
          )}
          <Input
            placeholder={t('logs.search_keyword', '日志ID/任务ID/渠道AID/密钥KID/上游YID')}
            prefix={<Search size={16} />}
            value={searchKeyword}
            onChange={e => setSearchKeyword(e.target.value)}
            onPressEnter={() => fetchLogs({ page: 1 })}
            style={{ width: screens.xs ? '100%' : 270, fontSize: 12, height: 32 }}
            allowClear
          />
          <Input
            placeholder={t('logs.model_name', '模型名称')}
            prefix={<Search size={16} />}
            value={modelFilter}
            onChange={e => setModelFilter(e.target.value)}
            onPressEnter={() => fetchLogs({ page: 1 })}
            style={{ width: screens.xs ? '100%' : 220, fontSize: 12, height: 32 }}
          />
          <Select
            placeholder={t('logs.search_status_code', '错误码')}
            value={statusCodeFilter}
            onChange={(v) => setStatusCodeFilter(v ?? undefined)}
            options={statusCodeOptions}
            style={{ width: screens.xs ? '100%' : 120, fontSize: 12, height: 32 }}
            popupMatchSelectWidth={false}
            allowClear
            showSearch
            optionFilterProp="label"
          />
          <LogDateTimeRangePicker
            value={dateRange}
            onChange={setDateRange}
            isAdmin={isAdmin}
            className="font-size-12"
          />
          <Space size={8} style={{ marginLeft: screens.xs ? 0 : 'auto' }}>
            {auxiliaryCurrencies.length > 0 && (
              <Dropdown
                menu={{
                  items: [
                    { key: '', label: `${t('marketplace.default_currency', '默认货币')} (${currencySymbol})` },
                    ...auxiliaryCurrencies.map(curr => ({ key: curr.code, label: `${curr.code} (${curr.symbol})` })),
                  ],
                  style: { maxHeight: 176, overflowY: 'auto' },
                  onClick: ({ key }) => setSelectedCurrencyCode(key),
                }}
                placement="bottomRight"
              >
                <Button style={{ height: 32, borderRadius: 6, fontSize: 12, display: 'inline-flex', alignItems: 'center', justifyContent: 'center' }}>
                  {(() => {
                    if (!selectedCurrencyCode) return `${t('marketplace.default_currency', '默认货币')} (${currencySymbol})`;
                    const curr = auxiliaryCurrencies.find(c => c.code === selectedCurrencyCode);
                    return curr ? `${curr.code} (${curr.symbol})` : t('marketplace.switch_currency', '切换货币显示');
                  })()}
                </Button>
              </Dropdown>
            )}
            <Button type="primary" icon={<Search size={14} />} onClick={() => fetchLogs({ page: 1 })} loading={loading} disabled={loading} style={{ height: 32, borderRadius: 6, fontSize: 12, display: 'inline-flex', alignItems: 'center', justifyContent: 'center' }}>{t('logs.query', '查询')}</Button>
            {user?.role === 'admin' && (
              <Tooltip title={t('logs.export_tooltip', '按当前筛选与列表顺序导出 CSV，可勾选字段（上限10万条）')}>
                <Button icon={<Download size={14} />} loading={exporting} disabled={loading} onClick={openExportModal} style={{ height: 32, borderRadius: 6, fontSize: 12, display: 'inline-flex', alignItems: 'center', justifyContent: 'center' }}>{t('logs.export', '导出')}</Button>
              </Tooltip>
            )}
          </Space>
        </div>

      {screens.xs ? (
        <MobileCardList
          dataSource={logs}
          loading={loading}
          rowKey="id"
          compact={true}
          gap={4}
          pagination={listPagination({
            total,
            current: page,
            pageSize,
            onChange,
          })}
          renderCard={(record: any) => {
            const userName = record.user_nickname || record.user_uid || record.user_id?.slice(0, 8) || '-';
            return (
              <MobileCard
                compact={true}
                style={{ background: _isLight ? '#fff' : '#141414', border: 'none' }}
                title={<Tag color="blue">{record.model}</Tag>}
                extra={record.status_code === 0
                  ? <Tag icon={<RefreshCw size={14} className="anticon-spin" />} color="processing" style={{ display: 'inline-flex', alignItems: 'center' }}>{t('logs.processing', '处理中')}</Tag>
                  : <Tag color={record.status_code === 200 ? 'success' : 'error'}>{record.status_code || 400}</Tag>
                }
              >
                <CardRow label={t('logs.time', '时间')}>
                  <Space direction="vertical" size={0} align="end">
                    <Text type="secondary" style={{ fontSize: 12 }}>{formatApiDateTime(record.created_at)}</Text>
                    {record.log_id && (
                      <Text 
                        type="secondary" 
                        style={{ fontSize: 10, fontFamily: 'monospace', maxWidth: '45vw' }} 
                        ellipsis={{ tooltip: record.log_id }}
                        copyable={{ text: record.log_id }}
                      >
                        {record.log_id}
                      </Text>
                    )}
                  </Space>
                </CardRow>
                {user?.role === 'admin' && (
                  <CardRow label={t('logs.user', '用户')}>
                    <Space direction="vertical" size={0}>
                      <Text style={{ fontSize: 12 }}>
                        {userName}
                        {record.user_admin_remark?.trim() ? <Text type="secondary" style={{ fontSize: 12 }}> {record.user_admin_remark.trim()}</Text> : null}
                      </Text>
                      {record.user_uid && (
                        <Text 
                          type="secondary" 
                          style={{ fontSize: 10, fontFamily: 'monospace' }}
                          copyable={{ text: String(record.user_uid), tooltips: [t('logs.copy', '复制'), t('logs.copy_success', '已复制')] }}
                        >
                          UID: {record.user_uid}
                        </Text>
                      )}
                    </Space>
                  </CardRow>
                )}
                {user?.role === 'admin' && record.channel_group_aid && <CardRow label={t('logs.channel_aid', '渠道信息')}>
                  <Space size={4}>
                    <Text type="secondary" style={{ fontSize: 12 }}>AID: {record.channel_group_aid}</Text>
                    {record.is_ha === 1 && <Tag color="blue" style={{ fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '16px' }}>HA</Tag>}
                  </Space>
                </CardRow>}
                {user?.role === 'admin' && (record.sub_channel_name || record.yid) && (
                  <CardRow label={t('logs.sub_channel_name', '实际调用上游')}>
                    <Space size={4} align="center" wrap>
                      {record.sub_channel_name && <Text style={{ fontSize: 11 }}>{record.sub_channel_name}</Text>}
                      {record.yid && <Tag color="cyan" style={{ fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '16px' }}>{record.yid}</Tag>}
                    </Space>
                  </CardRow>
                )}
                {user?.role === 'admin' && (record.mid || record.billing_pid || record.forward_eid) && (
                  <CardRow label={t('logs.match_rule', '匹配规则')}>
                    <Space size={8}>
                      {record.mid && <Text type="secondary" style={{ fontSize: 11 }}>MID:<Typography.Text keyboard style={{ fontSize: 10 }}>{record.mid}</Typography.Text></Text>}
                      {record.billing_pid && <Text type="secondary" style={{ fontSize: 11 }}>PID:<Typography.Text keyboard style={{ fontSize: 10 }}>{record.billing_pid}</Typography.Text></Text>}
                      {record.forward_eid && <Text type="secondary" style={{ fontSize: 11 }}>EID:<Typography.Text keyboard style={{ fontSize: 10 }}>{record.forward_eid}</Typography.Text></Text>}
                    </Space>
                  </CardRow>
                )}
                <CardRow label={t('logs.token', '令牌')}>
                  <Space size={6} align="center" style={{ justifyContent: 'flex-end', width: '100%' }}>
                    <Tag color="cyan" style={{ fontSize: 11, margin: 0 }}>{record.token_name || '-'}</Tag>
                    {record.token_pg === 1 && <Tag color="orange" style={{ fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '16px' }}>PG</Tag>}
                    {record.token_ha === 1 && <Tag color="blue" style={{ fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '16px' }}>HA</Tag>}
                    {record.token_kid && <Text type="secondary" style={{ fontSize: 10, fontFamily: 'monospace' }}>KID: {record.token_kid}</Text>}
                  </Space>
                </CardRow>
                <CardRow label={t('logs.latency', '耗时')}><Text style={{ fontSize: 12 }}>{(record.latency_ms / 1000).toFixed(3)}s</Text></CardRow>
                <CardRow label={t('logs.type', '类型')}><Tag color={record.is_stream === 1 ? 'geekblue' : 'default'}>{record.is_stream === 1 ? t('logs.stream', '流') : t('logs.non_stream', '非流')}</Tag></CardRow>
                <CardRow label={t('logs.usage', '用量')}>
                  <Space direction="vertical" size={0} align="end">
                    <Text type="secondary" style={{ fontSize: 12 }}>
                      输入:{record.prompt_tokens} / 输出:{record.completion_tokens}
                      {(record.action_type === '视频' || (record.action_type === '封装' && !record.endpoint?.includes('image'))) && (record.has_video === 1 ? ` ${t('logs.with_video_short', '含视')}` : ` ${t('logs.without_video_short', '无视')}`)}
                    </Text>
                    {(() => {
                      const { cacheCreation: cc, cacheRead: cr } = billingUsageMetrics(record);
                      return (
                        <>
                          {cc > 0 && <Text type="secondary" style={{ fontSize: 11, color: '#faad14' }}>{t('logs.cache_creation', '缓存创建')}:{cc}</Text>}
                          {cr > 0 && <Text type="secondary" style={{ fontSize: 11, color: '#52c41a' }}>{t('logs.cache_read', '缓存读取')}:{cr}</Text>}
                        </>
                      );
                    })()}
                  </Space>
                </CardRow>
                <CardRow label={t('logs.cost', '成本')}>
                  <Space direction="vertical" size={0} align="end">
                    {record.cost === 0 || record.billing_refunded || record.billing_failed
                      ? <Text type="secondary" style={{ fontSize: 12 }}>-</Text>
                      : <Text strong style={{ fontSize: 12, color: themeToken.colorError }}>{formatDisplayCost(record.cost)}</Text>
                    }
                    {record.billing_refunded && (
                      <Tag color="orange" style={{ margin: 0, fontSize: 10, lineHeight: '14px', padding: '0 4px' }}>{t('logs.refunded', '已退费')}</Tag>
                    )}
                  </Space>
                </CardRow>
              </MobileCard>
            );
          }}
        />
      ) : (
        <div ref={dragScrollRef} style={{ width: '100%', maxWidth: '100%' }}>
          <Table
            className="compact-table"
            dataSource={logs}
            columns={columns}
            rowKey="id"
            loading={loading}
            expandable={
              allowDetails ? {
                expandedRowKeys,
                expandedRowRender,
                onExpand: handleExpand,
                expandRowByClick: false
              } : undefined
            }
            pagination={listPagination({
              total,
              current: page,
              pageSize,
              onChange,
            })}
            onChange={(pagination, filters: any) => {
              let shouldResetPage = false;
              
              const stFilter = filters.status_code ? filters.status_code[0] as string : undefined;
              if (stFilter !== statusFilter) {
                setStatusFilter(stFilter);
                shouldResetPage = true;
              }

              const tkFilter = filters.token_name ? filters.token_name[0] as string : undefined;
              if (tkFilter !== tokenTypeFilter) {
                setTokenTypeFilter(tkFilter);
                shouldResetPage = true;
              }

              const mdSubFilter = filters.model ? filters.model[0] as string : undefined;
              if (mdSubFilter !== subTypeFilter) {
                setSubTypeFilter(mdSubFilter);
                shouldResetPage = true;
              }

              if (shouldResetPage) {
                setPage(1);
              }
            }}
            size="small"
            locale={{ emptyText: t('dashboard.no_data') }}
            scroll={{ x: 1200 }}
          />
        </div>
      )}

      <Modal
        title={null}
        open={isSettingsModalVisible}
        onCancel={() => setIsSettingsModalVisible(false)}
        footer={null}
        width={420}
        styles={{
          mask: { backgroundColor: 'rgba(0, 0, 0, 0.45)' },
          body: { padding: '24px' },
          content: { 
            backgroundColor: _isLight ? 'rgba(255, 255, 255, 0.95)' : 'rgba(28, 29, 31, 0.85)', 
            backdropFilter: 'blur(16px) saturate(120%)', 
            WebkitBackdropFilter: 'blur(16px) saturate(120%)',
            borderRadius: 16, 
            padding: 0, 
            overflow: 'hidden',
            border: _isLight ? '1px solid rgba(0,0,0,0.1)' : '1px solid rgba(255,255,255,0.1)',
            boxShadow: _isLight ? '0 12px 32px rgba(0,0,0,0.1)' : '0 24px 48px rgba(0,0,0,0.4)'
          },
        } as any}
      >
        <div style={{ marginBottom: 20 }}>
          <h3 style={{ fontSize: 16, fontWeight: 600, margin: 0, color: _isLight ? '#1f2937' : '#E8EAED' }}>
            {t('logs.default_type_modal_title', '配置默认查看日志类型')}
          </h3>
          <p style={{ fontSize: 12, color: _isLight ? '#71717a' : '#a1a1aa', margin: '4px 0 0 0' }}>
            {t('logs.default_type_modal_desc', '选择在进入日志查询页面时，默认加载并展示的日志类别。')}
          </p>
        </div>

        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: 10, marginBottom: 24 }}>
          {logTypeOptions.map((opt) => {
            const isSelected = tempDefaultType === opt.value;
            return (
              <div
                key={opt.value}
                onClick={() => setTempDefaultType(opt.value)}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  padding: '10px 12px',
                  borderRadius: 8,
                  border: `1px solid ${isSelected ? themeToken.colorPrimary : (_isLight ? 'rgba(0,0,0,0.08)' : 'rgba(255,255,255,0.08)')}`,
                  background: isSelected 
                    ? (_isLight ? 'rgba(22, 119, 255, 0.05)' : 'rgba(22, 119, 255, 0.15)') 
                    : (_isLight ? 'transparent' : 'rgba(255,255,255,0.02)'),
                  cursor: 'pointer',
                  transition: 'all 0.2s',
                  color: isSelected ? themeToken.colorPrimary : (_isLight ? '#1f2937' : '#E8EAED'),
                }}
                onMouseEnter={(e) => {
                  if (!isSelected) {
                    e.currentTarget.style.borderColor = _isLight ? 'rgba(0,0,0,0.15)' : 'rgba(255,255,255,0.15)';
                    e.currentTarget.style.background = _isLight ? 'rgba(0,0,0,0.01)' : 'rgba(255,255,255,0.04)';
                  }
                }}
                onMouseLeave={(e) => {
                  if (!isSelected) {
                    e.currentTarget.style.borderColor = _isLight ? 'rgba(0,0,0,0.08)' : 'rgba(255,255,255,0.08)';
                    e.currentTarget.style.background = _isLight ? 'transparent' : 'rgba(255,255,255,0.02)';
                  }
                }}
              >
                <span>{opt.icon}</span>
                <span style={{ fontSize: 13, fontWeight: isSelected ? 500 : 400 }}>{opt.label}</span>
              </div>
            );
          })}
        </div>

        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 12 }}>
          <Button
            onClick={() => setIsSettingsModalVisible(false)}
            style={{
              borderRadius: 6,
              height: 32,
              padding: '0 16px',
              fontSize: 13,
              border: _isLight ? '1px solid rgba(0,0,0,0.15)' : '1px solid rgba(255,255,255,0.15)',
              background: 'transparent',
              color: _isLight ? '#1f2937' : '#E8EAED',
            }}
          >
            {t('common.cancel', '取消')}
          </Button>
          <Button
            onClick={() => {
              setDefaultLogType(tempDefaultType);
              setSubTypeFilter(undefined);
              setPage(1);
              goLogType(tempDefaultType);
              message.success(t('logs.default_type_saved', '默认日志类型配置已保存'));
              setIsSettingsModalVisible(false);
            }}
            style={{
              borderRadius: 6,
              height: 32,
              padding: '0 16px',
              fontSize: 13,
              background: themeToken.colorPrimary,
              color: '#fff',
              border: 'none',
            }}
          >
            {t('common.confirm', '确认')}
          </Button>
        </div>
      </Modal>
      <Modal
        title={t('logs.export_modal_title', '导出日志')}
        open={exportModalOpen}
        onCancel={() => !exporting && setExportModalOpen(false)}
        footer={null}
        width={screens.xs ? '100%' : 640}
      >
        <Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 12 }}>
          {t('logs.export_modal_hint', '默认导出当前列表全部数据，共 {{total}} 条，按列表时间倒序。可取消勾选不需要的字段。', { total })}
        </Text>
        {total === 0 && (
          <Text type="warning" style={{ fontSize: 12, display: 'block', marginBottom: 12 }}>
            {t('logs.export_empty', '当前没有可导出的数据')}
          </Text>
        )}
        {total > LOG_EXPORT_LIMIT && (
          <Text type="danger" style={{ fontSize: 12, display: 'block', marginBottom: 12 }}>
            {t('logs.export_limit_warn', '当前筛选超过 10 万条，请缩小时间范围或增加筛选后再导出')}
          </Text>
        )}
        <Space size={8} style={{ marginBottom: 12 }}>
          <Button size="small" onClick={() => setExportFieldKeys(LOG_EXPORT_ALL_KEYS)}>{t('logs.export_select_all', '全选')}</Button>
          <Button
            size="small"
            onClick={() => {
              const selected = new Set(exportFieldKeys);
              setExportFieldKeys(LOG_EXPORT_ALL_KEYS.filter((k) => !selected.has(k)));
            }}
          >
            {t('logs.export_invert', '反选')}
          </Button>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {t('logs.export_selected_count', '已选 {{count}} / {{total}} 个字段', { count: exportFieldKeys.length, total: LOG_EXPORT_FIELDS.length })}
          </Text>
        </Space>
        <Checkbox.Group
          value={exportFieldKeys}
          onChange={(v) => setExportFieldKeys(v as string[])}
          style={{ width: '100%' }}
        >
          <div style={{
            display: 'grid',
            gridTemplateColumns: screens.xs ? '1fr 1fr' : '1fr 1fr 1fr',
            gap: '8px 12px',
            maxHeight: 360,
            overflowY: 'auto',
            padding: '4px 0',
          }}>
            {LOG_EXPORT_FIELDS.map((f) => (
              <Checkbox key={f.key} value={f.key}>{f.label}</Checkbox>
            ))}
          </div>
        </Checkbox.Group>
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 12, marginTop: 16 }}>
          <Button onClick={() => setExportModalOpen(false)} disabled={exporting}>
            {t('common.cancel', '取消')}
          </Button>
          <Button
            type="primary"
            icon={<Download size={14} />}
            loading={exporting}
            disabled={total === 0 || total > LOG_EXPORT_LIMIT || exportFieldKeys.length === 0}
            onClick={handleExport}
          >
            {t('logs.export_confirm', '导出 CSV')}
          </Button>
        </div>
      </Modal>
    </Card>
  );
};

export default Logs;
