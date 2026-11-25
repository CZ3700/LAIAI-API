import React, { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { Navigate } from 'react-router-dom';
import { Table, Tag, Button, Space, Typography, Input, Select, Row, Col, Form, message, Grid, Descriptions, Card, Tooltip, theme, Radio, Popconfirm, Modal, Image, Carousel, Spin } from 'antd';
import { QuestionCircleOutlined } from '@ant-design/icons';
import MobileCardList, { MobileCard, CardRow } from '../../components/MobileCardList';
import { listPagination, useListPager } from '../../components/ListPagination';
import { RefreshCw, Search, Download, Eye, Image as ImageIcon, MessageSquare, Video, Wrench, XCircle, Cuboid, ListOrdered, Mic, MoreHorizontal } from 'lucide-react';
import request from '../../utils/request';
import { QueryGuard, isRequestAborted } from '../../utils/queryGuard';
import dayjs from 'dayjs';
import utc from 'dayjs/plugin/utc';
import { formatApiDateTime } from '../../utils/timedisplay';
import { defaultLogDayRange, toDateRangeParams } from '../../utils/dateRangeParams';
import LogDateTimeRangePicker from '../../components/LogDateTimeRangePicker';
import { useLogDetailLoader } from '../../hooks/useLogDetailLoader';
import { useDragScroll } from '../../hooks/useDragScroll';
import { parsePluginTagMeta } from '../../utils/pluginTagMeta';
import MediaPreviewModal from './components/MediaPreviewModal';
import { useTranslation } from 'react-i18next';
import useAuthStore from '../../store/auth';
import { useThemeStore } from '../../store/theme';
import { useLogTypeRoute } from './logTypeRoutes';
import { LogTypeTabs, buildLogTypeTabOptions } from './LogTypeTabs';
dayjs.extend(utc);

const { Text } = Typography;
const { useBreakpoint } = Grid;

interface TaskLog {
  id: number;
  log_id?: string;
  user_id: string;
  user_uid?: string;
  channel_id: number | null;
  model: string;
  endpoint: string;
  prompt_tokens: number;
  completion_tokens: number;
  cached_tokens: number;
  cost: number;
  latency_ms: number;
  status_code: number;
  error_message: string | null;
  request_content: string | null;
  response_content: string | null;
  post_response?: string | null;
  billing_detail?: string | null;
  billing_failed?: boolean;
  billing_frozen?: boolean;
  billing_present?: boolean;
  channel_name: string | null;
  channel_group_aid: string | null;
  is_ha?: number;
  user_nickname: string | null;
  user_admin_remark?: string | null;
  task_id: string | null;
  upstream_request_id?: string | null;
  action_type: string | null;
  has_video?: number;
  yid?: string | null;
  mid?: string | null;
  billing_pid?: string | null;
  forward_eid?: string | null;
  /** 列表直接返回的产物媒体地址（预览用，不受详情权限限制） */
  preview_urls?: string[];
  created_at: string;
}

// ── 工具函数：从记录中获取任务类型（直接读取后端返回的 action_type） ──────────────────────────
const getTaskType = (r: TaskLog, t?: any) => {
  const tr = (k: string, def?: string) => {
    if (!t) return def || k;
    try {
      return t(k, def) || t(k, { defaultValue: def }) || def || k;
    } catch {
      return def || k;
    }
  };
  const actionType = r.action_type || '';
  if (actionType === '聊天' || actionType === 'chat') {
    return { label: tr('logs.type_chat', '聊天'), color: 'blue', icon: <MessageSquare size={14} /> };
  }
  if (actionType === '图片' || actionType === '图像' || actionType === 'image') {
    return { label: tr('logs.type_image', '图片'), color: 'purple', icon: <ImageIcon size={14} /> };
  }
  if (actionType === '视频' || actionType === 'video') {
    return { label: tr('logs.type_video', '视频'), color: 'orange', icon: <Video size={14} /> };
  }
  if (actionType === '封装' || actionType === 'encapsulation') {
    return { label: tr('logs.type_encapsulation', '封装'), color: 'volcano', icon: <Video size={14} /> };
  }
  if (actionType === '音频' || actionType === 'audio') {
    return { label: tr('logs.type_audio', '音频'), color: 'green', icon: <Mic size={14} /> };
  }
  if (actionType === '向量' || actionType === 'embedding') {
    return { label: tr('logs.type_embedding', '向量'), color: 'cyan', icon: <Cuboid size={14} /> };
  }
  if (actionType === '排序' || actionType === 'rerank') {
    return { label: tr('logs.type_rerank', '排序'), color: 'geekblue', icon: <ListOrdered size={14} /> };
  }
  return { label: actionType || tr('logs.type_other', '其它'), color: 'default', icon: <Wrench size={14} /> };
};

// ── 工具函数：判断是否异步任务（后端 task_id 非空即为异步） ─────────────────
const isAsyncPost = (r: TaskLog) => !!r.task_id;

// ── 工具函数：获取异步任务终态 ─────────────────────────
const getAsyncFinalStatus = (r: TaskLog): 'pending' | 'succeeded' | 'failed' => {
  if (!isAsyncPost(r)) {
    if (r.status_code === 0) return 'pending';
    if (r.status_code >= 200 && r.status_code < 400) return 'succeeded';
    return 'failed';
  }
  
  // 1. 优先从最新的响应结果中解析状态
  if (r.response_content) {
    try {
      const v = JSON.parse(r.response_content);
      const status = v.status || v.data?.status || v.data?.task_status
        || v.output?.status || v.output?.task_status;
      if (status === 'succeeded' || status === 'succeed' || status === 'SUCCESS' || status === 'completed') return 'succeeded';
      if (status === 'failed' || status === 'FAILED') return 'failed';
      // 上游仍在跑：勿被下方「有计费明细且未冻结 → 成功」误判（如渠道测试轮询中）
      const s = String(status || '').toLowerCase();
      if (
        s === 'pending' || s === 'running' || s === 'processing' || s === 'queued'
        || s === 'submitted' || s === 'in_progress' || s === 'in-progress'
      ) {
        return 'pending';
      }
    } catch { /* ignore */ }
  }

  // 2. 列表标记优先；billing_detail 仅兼容旧缓存/展开合并
  if (r.billing_failed || r.status_code >= 400) return 'failed';
  if (r.billing_frozen || r.status_code === 0) return 'pending';
  if (r.billing_present) return 'succeeded';
  if (r.billing_detail) {
    if (r.billing_detail.includes('失败')) return 'failed';
    if (r.billing_detail.includes('冻结')) return 'pending';
    return 'succeeded';
  }

  return 'pending';
};



// ── 工具函数：获取任务 ID ──────────────────
const getTaskId = (record: TaskLog): string => record.task_id || '-';

// ── 工具函数：格式化 JSON 用于展示 ─────────────────────────────
const fmtJson = (raw: string | null): string => {
  if (!raw) return '-';
  try { return JSON.stringify(JSON.parse(raw), null, 2); } catch { return raw; }
};

// ── 工具函数：提取文本/JSON中的链接 ─────────────────────────────
const extractUrls = (content: string | null): string[] => {
  if (!content) return [];
  try {
    const parsed = JSON.parse(content);
    const root = (parsed && typeof parsed === 'object' && parsed.stage1 != null && parsed.stage2 != null)
      ? parsed.stage2
      : parsed;
    const urls: string[] = [];
    const searchUrl = (obj: any) => {
      if (typeof obj === 'string') {
        if (obj.startsWith('http://') || obj.startsWith('https://')) {
          urls.push(obj);
        }
      } else if (Array.isArray(obj)) {
        obj.forEach(searchUrl);
      } else if (obj !== null && typeof obj === 'object') {
        Object.values(obj).forEach(searchUrl);
      }
    };
    searchUrl(root);
    return Array.from(new Set(urls));
  } catch {
    // 降级使用正则匹配
    const regex = /https?:\/\/[^"'\s\\]+/g;
    const matches = content.match(regex);
    if (!matches) return [];
    return Array.from(new Set(matches.map(u => u.replace(/\\/g, ''))));
  }
};

const TaskLogs: React.FC = () => {
  const { t } = useTranslation();
  const { user } = useAuthStore();
  const isSuperAdmin = user?.role === 'admin' && !user?.admin_group_id;
  const isAdmin = user?.role === 'admin';
  const { themeMode } = useThemeStore();
  const { token: themeToken } = theme.useToken();
  const isLight = themeMode === 'light';
  const panelBg = isLight ? '#fafafa' : '#141414';
  const labelBg = isLight ? '#f5f5f5' : '#1a1a1a';
  const labelColor = isLight ? '#6b7280' : '#8c8c8c';
  const contentBg = isLight ? '#fff' : '#141414';
  const timeBadgeBg = isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.05)';
  const [data, setData] = useState<TaskLog[]>([]);
  const [loading, setLoading] = useState(false);
  const [total, setTotal] = useState(0);
  const { page, pageSize, setPage, setPageSize, onChange } = useListPager();
  const [allowDetails, setAllowDetails] = useState(true);
  const [exporting, setExporting] = useState(false);
  const [form] = Form.useForm();
  const screens = useBreakpoint();
  const { actionTypeFilter, redirectTo, goLogType, defaultLogType, setDefaultLogType } = useLogTypeRoute('task-logs');
  const logTypeOptions = useMemo(() => buildLogTypeTabOptions(t), [t]);
  const [subTypeFilter, setSubTypeFilter] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState<string | undefined>(undefined);
  const [isSettingsModalVisible, setIsSettingsModalVisible] = useState(false);
  const [tempDefaultType, setTempDefaultType] = useState<string>('视觉');
  const [userLevels, setUserLevels] = useState<any[]>([]);
  const queryGuardRef = useRef(new QueryGuard());
  const skipNextEffectFetchRef = useRef(false);
  const dragScrollRef = useDragScroll<HTMLDivElement>();
  const rowIds = useMemo(() => data.map((l) => l.id), [data]);
  const {
    detailCache,
    detailLoadingIds,
    expandedRowKeys,
    loadLogDetail,
    handleExpand,
    resetDetailCache,
  } = useLogDetailLoader(rowIds);

  useEffect(() => {
    const guard = queryGuardRef.current;
    return () => guard.dispose();
  }, []);

  useEffect(() => {
    if (isAdmin) {
      request.get('/user_levels').then((res: any) => setUserLevels(res.data || [])).catch(console.error);
    }
  }, [isAdmin]);

  useEffect(() => {
    if (isSettingsModalVisible) {
      setTempDefaultType(defaultLogType);
    }
  }, [isSettingsModalVisible, defaultLogType]);

  // 预览相关状态
  const [previewOpen, setPreviewOpen] = useState(false);
  const [previewUrls, setPreviewUrls] = useState<string[]>([]);
  const [previewType, setPreviewType] = useState<'图片' | '视频'>('图片');
  const [previewRecord, setPreviewRecord] = useState<TaskLog | null>(null);

  const handlePreview = async (record: TaskLog) => {
    let urls = (record.preview_urls || []).filter((u) => !!u?.trim());
    let currentRecord: TaskLog = { ...record, ...detailCache[record.id] };
    const resolvedType =
      record.action_type === '图片' || record.action_type === '图像' || record.action_type === 'image' || record.endpoint?.includes('image')
        ? '图片'
        : '视频';

    // 若列表已带回 preview_urls，立即秒开预览弹窗；后台异步补齐详情（用于解析右侧提示词与参数）
    if (urls.length > 0) {
      setPreviewRecord(currentRecord);
      setPreviewUrls(urls);
      setPreviewType(resolvedType);
      setPreviewOpen(true);

      if (!currentRecord.request_content && allowDetails) {
        void loadLogDetail(record.id).then((detail) => {
          if (detail) {
            setPreviewRecord((prev) => (prev && prev.id === record.id ? { ...prev, ...detail } : prev));
          }
        });
      }
      return;
    }

    if (allowDetails) {
      const detail = await loadLogDetail(record.id);
      if (detail) {
        currentRecord = { ...record, ...detail };
      }
    }

    const content = currentRecord.response_content ?? null;
    if (content) urls = extractUrls(content);

    if (urls.length === 0) {
      message.warning(t('task_logs.no_media', '未找到可预览的媒体链接'));
      return;
    }

    setPreviewRecord(currentRecord);
    setPreviewUrls(urls);
    setPreviewType(resolvedType);
    setPreviewOpen(true);
  };

  const fetchLogs = useCallback(async (current = 1, size = pageSize) => {
    const signal = queryGuardRef.current.begin();
    if (current !== page || size !== pageSize) {
      skipNextEffectFetchRef.current = true;
      setPage(current);
      setPageSize(size);
    }
    setLoading(true);
    try {
      const v = form.getFieldsValue();
      const params: any = { page: current, per_page: size };
      if (subTypeFilter) {
        if (subTypeFilter === '图片') params.action_type = 'image';
        else if (subTypeFilter === '视频') params.action_type = 'video';
        else if (subTypeFilter === '聊天') params.action_type = 'chat';
        else if (subTypeFilter === '其它') params.action_type = 'other';
        else params.action_type = subTypeFilter;
      } else if (actionTypeFilter && actionTypeFilter !== '全部') {
        if (actionTypeFilter === '视觉') params.action_type = 'vision';
        else if (actionTypeFilter === '聊天') params.action_type = 'chat';
        else if (actionTypeFilter === '其它') params.action_type = 'other';
        else params.action_type = actionTypeFilter;
      }
      if (v.model && v.model.trim()) params.model = v.model.trim();
      if (v.search_keyword && v.search_keyword.trim()) params.search_keyword = v.search_keyword.trim();
      if (v.user_id && v.user_id.trim()) params.user_id = v.user_id.trim();
      if (v.user_group) params.user_group = v.user_group;
      if (statusFilter) params.status = statusFilter;
      Object.assign(params, toDateRangeParams(v.dateRange));

      const res = await (request.get('/task_logs', { params, signal }) as any);
      if (!queryGuardRef.current.isCurrent(signal)) return;
      setData(res.data);
      setTotal(res.total);
      resetDetailCache();
      if (res.allow_details !== undefined) {
        setAllowDetails(res.allow_details);
      }
    } catch (e) {
      if (isRequestAborted(e)) return;
      console.error(e);
      message.error(t('task_logs.fetch_fail', '获取任务日志失败'));
    } finally {
      if (queryGuardRef.current.isCurrent(signal)) {
        setLoading(false);
      }
    }
  }, [form, actionTypeFilter, subTypeFilter, statusFilter, t, page, pageSize, setPage, setPageSize, resetDetailCache]);


  const handleSyncTask = async (log_id: string) => {
    try {
      const res = await (request.post(`/task_logs/${log_id}/sync`) as any);
      message.success(res.message || t('task_logs.sync_success', '任务状态已同步'));
      fetchLogs(page, pageSize);
    } catch (e) {
      console.error(e);
    }
  };

  const handleCancelTask = async (log_id: string) => {
    try {
      const res = await (request.post(`/task_logs/${log_id}/cancel`) as any);
      message.success(res.message || t('task_logs.cancel_success', '任务已取消'));
      fetchLogs(page, pageSize);
    } catch (e) { console.error(e); }
  };

  useEffect(() => {
    if (redirectTo) return;
    if (skipNextEffectFetchRef.current) {
      skipNextEffectFetchRef.current = false;
      return;
    }
    fetchLogs(page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page, pageSize, actionTypeFilter, subTypeFilter, statusFilter, redirectTo]);

  const handleExport = async () => {
    setExporting(true);
    try {
      const v = form.getFieldsValue();
      const params: any = {};
      if (subTypeFilter) {
        if (subTypeFilter === '图片') params.action_type = 'image';
        else if (subTypeFilter === '视频') params.action_type = 'video';
        else if (subTypeFilter === '聊天') params.action_type = 'chat';
        else if (subTypeFilter === '其它') params.action_type = 'other';
        else params.action_type = subTypeFilter;
      } else if (actionTypeFilter && actionTypeFilter !== '全部') {
        if (actionTypeFilter === '视觉') params.action_type = 'vision';
        else if (actionTypeFilter === '聊天') params.action_type = 'chat';
        else if (actionTypeFilter === '其它') params.action_type = 'other';
        else params.action_type = actionTypeFilter;
      }
      if (v.model && v.model.trim()) params.model = v.model.trim();
      if (v.search_keyword && v.search_keyword.trim()) params.search_keyword = v.search_keyword.trim();
      if (v.user_id && v.user_id.trim()) params.user_id = v.user_id.trim();
      if (v.user_group) params.user_group = v.user_group;
      if (statusFilter) params.status = statusFilter;
      Object.assign(params, toDateRangeParams(v.dateRange));
      const resp = await request.get('/task_logs/export', {
        params,
        responseType: 'blob',
        skipErrorHandler: true,
      } as any) as any;
      if (resp instanceof Blob && resp.type?.includes('json')) {
        const text = await resp.text();
        const json = JSON.parse(text);
        message.error(json.error || t('task_logs.export_fail', '导出失败'));
        return;
      }
      const blob = resp instanceof Blob ? resp : new Blob([resp], { type: 'text/csv' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `task_logs_${dayjs().format('YYYYMMDDHHmmss')}.csv`;
      a.click();
      URL.revokeObjectURL(url);
      message.success(t('task_logs.export_success', '导出成功'));
    } catch (e: any) {
      if (e?.response?.data) {
        try {
          const blob = e.response.data;
          const text = blob instanceof Blob ? await blob.text() : JSON.stringify(blob);
          const json = JSON.parse(text);
          message.error(json.error || t('task_logs.export_fail', '导出失败'));
        } catch { message.error(t('task_logs.export_fail', '导出失败')); }
      } else {
        message.error(t('task_logs.export_fail', '导出失败'));
      }
    } finally {
      setExporting(false);
    }
  };

  // ── 展开行：详细信息 ─────────────────────────────────────────
  const expandedRowRender = (record: TaskLog) => {
    const merged = { ...record, ...detailCache[record.id] };
    const loadingDetail = !!detailLoadingIds[record.id] && !detailCache[record.id];
    if (loadingDetail) {
      return (
        <div style={{ padding: 24, textAlign: 'center' }}>
          <Spin tip={t('logs.loading_detail', '加载详情中...')} />
        </div>
      );
    }
    const { cascadeS1TaskId, upstreamTaskId } = parsePluginTagMeta(merged.plugin_tag);
    return (
      <div style={{ padding: '8px 0' }}>
        <Descriptions size="small" column={1} bordered
          styles={{
            label: { width: 120, whiteSpace: 'nowrap', color: labelColor, background: labelBg },
            content: { background: contentBg, whiteSpace: 'pre-wrap', wordBreak: 'break-all', fontFamily: 'monospace', fontSize: 12, maxHeight: 300, overflow: 'auto' }
          }}
        >
          {merged.upstream_request_id && (
            <Descriptions.Item label={
              <span style={{ display: 'inline-flex', alignItems: 'center' }}>
                {t('logs.upstream_request_id', '厂商排查ID')}
                <Tooltip title={t('logs.upstream_request_id_tip', '上游响应追踪凭据，用于找厂商排障')}>
                  <QuestionCircleOutlined style={{ marginLeft: 4, color: labelColor, cursor: 'help' }} />
                </Tooltip>
              </span>
            }>
              <Text copyable={{ text: merged.upstream_request_id }} style={{ fontFamily: 'monospace', fontSize: 12 }}>
                {merged.upstream_request_id}
              </Text>
            </Descriptions.Item>
          )}
          {upstreamTaskId && (
            <Descriptions.Item label={
              <span style={{ display: 'inline-flex', alignItems: 'center' }}>
                {t('logs.upstream_task_id', '上游任务ID')}
                <Tooltip title={t('logs.upstream_task_id_tip', '上游返回的真实任务号，用于到厂商控制台查单')}>
                  <QuestionCircleOutlined style={{ marginLeft: 4, color: labelColor, cursor: 'help' }} />
                </Tooltip>
              </span>
            }>
              <Text copyable={{ text: upstreamTaskId }} style={{ fontFamily: 'monospace', fontSize: 12 }}>{upstreamTaskId}</Text>
            </Descriptions.Item>
          )}
          {isSuperAdmin && cascadeS1TaskId && (
            <Descriptions.Item label={
              <span style={{ display: 'inline-flex', alignItems: 'center' }}>
                {t('logs.cascade_s1_task_id', '底座任务ID')}
                <Tooltip title={t('logs.cascade_s1_task_id_tip', '级联底座原始任务号，用于上游控制台查单')}>
                  <QuestionCircleOutlined style={{ marginLeft: 4, color: labelColor, cursor: 'help' }} />
                </Tooltip>
              </span>
            }>
              <Text copyable={{ text: cascadeS1TaskId }} style={{ fontFamily: 'monospace', fontSize: 12 }}>{cascadeS1TaskId}</Text>
            </Descriptions.Item>
          )}
          <Descriptions.Item label={t('task_logs.token_usage', 'Token 用量')}>
            {t('logs.input', '输入')} {record.prompt_tokens} / {t('logs.output', '输出')} {record.completion_tokens}{record.action_type === '视频' && (record.has_video === 1 ? ` ${t('logs.with_video_short', '含视')}` : ` ${t('logs.without_video_short', '无视')}`)}{(record.cached_tokens ?? 0) > 0 ? ` / ${t('logs.cache_read', '缓存读取')} ${record.cached_tokens}` : ''}
          </Descriptions.Item>
          <Descriptions.Item label={t('task_logs.cost', '费用')}>
            {record.cost > 0 ? Number(Number(record.cost).toFixed(6)) : '0'}
          </Descriptions.Item>
          {merged.billing_detail && (
            <Descriptions.Item label={t('logs.billing_detail', '计费明细')}>{fmtJson(merged.billing_detail)}</Descriptions.Item>
          )}
          {merged.request_content && (
            <Descriptions.Item label={t('logs.req_params', '请求参数')}>{fmtJson(merged.request_content)}</Descriptions.Item>
          )}
          {merged.response_content && (
            <Descriptions.Item label={t('logs.resp_content', '响应内容')}>{fmtJson(merged.response_content)}</Descriptions.Item>
          )}
          {merged.post_response && (
            <Descriptions.Item label={t('logs.post_resp_content', 'POST 提交响应')}>{fmtJson(merged.post_response)}</Descriptions.Item>
          )}
          {!merged.request_content && !merged.response_content && !merged.post_response && (
            <Descriptions.Item label={t('logs.detail', '详情')}>
              <Text type="secondary">{t('logs.no_context', '该模型未开启「记录上下文」或内容为空')}</Text>
            </Descriptions.Item>
          )}
        </Descriptions>
      </div>
    );
  };

  // ── 表格列定义 ───────────────────────────────────────────────
  const columns: any[] = [
    {
      title: t('task_logs.submit_time', '提交时间'),
      dataIndex: 'created_at',
      key: 'submit_time',
      width: 190,
      render: (v: string, r: TaskLog) => {
        return (
          <Space vertical size={0}>
            <Text style={{ fontSize: 12 }}>{formatApiDateTime(v)}</Text>
            {r.log_id && (
              <Text 
                type="secondary" 
                style={{ fontSize: 10, fontFamily: 'monospace', maxWidth: 140 }} 
                ellipsis={{ tooltip: r.log_id }}
                copyable={{ text: r.log_id, tooltips: [t('logs.copy', '复制'), t('logs.copy_success', '已复制')] }}
              >
                {r.log_id}
              </Text>
            )}
          </Space>
        );
      },
    },
    {
      title: t('task_logs.time_spent', '花费时间'),
      key: 'time_spent',
      width: 100,
      render: (_: any, r: TaskLog) => {
        const status = getAsyncFinalStatus(r);
        if (isAsyncPost(r) && status === 'pending') {
          return <Tag color="processing">{t('task_logs.processing', '处理中...')}</Tag>;
        }
        // 统一使用 latency_ms（后端在异步任务结算时已精确更新为 CURRENT_TIMESTAMP - created_at）
        const sec = r.latency_ms / 1000;
        const display = sec >= 60 ? `${(sec / 60).toFixed(1)}m` : `${sec.toFixed(1)}s`;
        return (
          <Text type="secondary" style={{ background: timeBadgeBg, padding: '2px 8px', borderRadius: 4 }}>
            🕗 {display}
          </Text>
        );
      },
    },
    isAdmin ? {
      title: t('logs.channel_aid', '渠道信息'),
      key: 'channel',
      width: 120,
      render: (_: any, r: TaskLog) => {
        return (
          <Space size={4}>
            <Text type="secondary" style={{ fontSize: 12 }}>{r.channel_group_aid ? `AID: ${r.channel_group_aid}` : '-'}</Text>
            {r.is_ha === 1 && <Tag color="blue" style={{ fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '16px' }}>HA</Tag>}
          </Space>
        );
      },
    } : null,
    {
      title: t('logs.type', '类型'),
      key: 'type',
      width: 100,
      filters: actionTypeFilter === '视觉' ? [
        { text: t('logs.type_image', '图片'), value: '图片' },
        { text: t('logs.type_video', '视频'), value: '视频' },
      ] : actionTypeFilter === '全部' ? [
        { text: t('logs.type_image', '图片'), value: '图片' },
        { text: t('logs.type_video', '视频'), value: '视频' },
        { text: t('logs.type_chat', '聊天'), value: '聊天' },
        { text: t('logs.type_audio', '音频'), value: '音频' },
        { text: t('logs.type_embedding', '向量'), value: '向量' },
        { text: t('logs.type_rerank', '排序'), value: '排序' },
        { text: t('logs.type_other', '其它'), value: '其它' },
      ] : undefined,
      filterMultiple: false,
      filteredValue: subTypeFilter ? [subTypeFilter] : null,
      render: (_: any, r: TaskLog) => {
        const typeInfo = getTaskType(r, t);
        return (
          <Tag color={typeInfo.color} style={{ borderRadius: 4, display: 'inline-flex', alignItems: 'center', gap: '4px', padding: '2px 8px' }}>
            {typeInfo.icon}
            <span>{typeInfo.label}</span>
          </Tag>
        );
      },
    },
    {
      title: t('logs.model', '模型'),
      dataIndex: 'model',
      key: 'model',
      width: 180,
      ellipsis: true,
      render: (v: string, record: TaskLog) => (
        <Space vertical size={0}>
          <Text style={{ fontSize: 12, fontFamily: 'monospace' }}>{v || '-'}</Text>
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
      title: t('task_logs.task_id', '任务 ID'),
      key: 'task_id',
      width: 200,
      ellipsis: true,
      render: (_: any, r: TaskLog) => {
        const tid = getTaskId(r);
        return <Text style={{ fontSize: 11, fontFamily: 'monospace' }} copyable={tid !== '-' ? { text: tid } : undefined}>{tid}</Text>;
      },
    },
    {
      title: t('task_logs.status', '状态'),
      key: 'status',
      width: 156,
      fixed: 'right' as const,
      filters: [
        { text: t('task_logs.success', '成功'), value: 'success' },
        { text: t('task_logs.status_processing', '进行中'), value: 'processing' },
        { text: t('task_logs.fail', '失败'), value: 'fail' },
      ],
      filterMultiple: false,
      filteredValue: statusFilter ? [statusFilter] : null,
      render: (_: any, r: TaskLog) => {
        const status = getAsyncFinalStatus(r);
        if (status === 'pending') {
          // 视频任务支持取消（后端 cancel_task_log 会校验具体任务类型）
          const canCancel = r.action_type === '视频';
          return (
            <Space>
              <Tag color="processing">{t('task_logs.pending', '进行中')}</Tag>
              {isAsyncPost(r) && (
                <Button 
                  type="text" 
                  size="small" 
                  icon={<RefreshCw size={14} />} 
                  onClick={(e) => { e.stopPropagation(); handleSyncTask(r.log_id!); }} 
                  title={t('logs.manual_sync', '手动同步状态')}
                />
              )}
              {canCancel && (
                <Popconfirm
                  title={t('task_logs.confirm_cancel', '确认取消此视频任务？')}
                  onConfirm={(e) => { e?.stopPropagation(); handleCancelTask(r.log_id!); }}
                  onCancel={(e) => e?.stopPropagation()}
                  okText={t('common.confirm', '确认')}
                  cancelText={t('common.cancel', '取消')}
                >
                  <Button
                    type="text"
                    size="small"
                    danger
                    icon={<XCircle size={14} />}
                    onClick={(e) => e.stopPropagation()}
                    title={t('task_logs.cancel_task', '取消任务')}
                  />
                </Popconfirm>
              )}
            </Space>
          );
        }
        
        let statusTag = null;
        if (status === 'failed') {
          statusTag = <Tag color="error" style={{ margin: 0 }}>{t('task_logs.fail', '失败')}</Tag>;
        } else {
          statusTag = <Tag color="success" style={{ margin: 0 }}>{t('task_logs.success', '成功')}</Tag>;
        }

        const canPreview = (r.action_type === '图片' || r.action_type === '视频' || r.action_type === '封装') && status === 'succeeded';

        return (
          <Space size={6}>
            {statusTag}
            {canPreview && (
              <Button
                size="small"
                icon={<Eye size={12} />}
                onClick={(e) => {
                  e.stopPropagation();
                  handlePreview(r);
                }}
                style={{
                  height: 22,
                  padding: '0 8px',
                  borderRadius: 6,
                  fontSize: 12,
                  fontWeight: 500,
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 4,
                  background: isLight ? '#f4f4f5' : 'rgba(255, 255, 255, 0.08)',
                  border: `1px solid ${isLight ? '#e4e4e7' : 'rgba(255, 255, 255, 0.16)'}`,
                  color: isLight ? '#18181b' : '#fafafa',
                  boxShadow: 'none',
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.background = isLight ? '#18181b' : '#fafafa';
                  e.currentTarget.style.borderColor = isLight ? '#18181b' : '#fafafa';
                  e.currentTarget.style.color = isLight ? '#fafafa' : '#18181b';
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.background = isLight ? '#f4f4f5' : 'rgba(255, 255, 255, 0.08)';
                  e.currentTarget.style.borderColor = isLight ? '#e4e4e7' : 'rgba(255, 255, 255, 0.16)';
                  e.currentTarget.style.color = isLight ? '#18181b' : '#fafafa';
                }}
              >
                {t('task_logs.preview', '预览')}
              </Button>
            )}
          </Space>
        );
      },
    },
  ];

  // 管理员额外显示用户列
  if (isAdmin) {
    columns.splice(4, 0, {
      title: t('logs.user', '用户'),
      key: 'user',
      width: 180,
      render: (_: any, r: TaskLog) => {
        const name = r.user_nickname || r.user_uid || r.user_id;
        const remark = r.user_admin_remark?.trim();
        const displayUid = r.user_uid || r.user_id;
        return (
          <Space vertical size={0}>
            <Text style={{ fontSize: 12 }}>
              {name}
              {remark ? <Text type="secondary" style={{ fontSize: 12 }}> {remark}</Text> : null}
            </Text>
            {r.user_uid ? (
              <Text 
                type="secondary" 
                style={{ fontSize: 10, fontFamily: 'monospace' }}
                copyable={{ text: String(r.user_uid), tooltips: [t('logs.copy', '复制'), t('logs.copy_success', '已复制')] }}
              >
                UID: {r.user_uid}
              </Text>
            ) : (
              <Text type="secondary" style={{ fontSize: 10, fontFamily: 'monospace' }} copyable={{ text: displayUid, tooltips: [t('logs.copy', '复制'), t('logs.copy_success', '已复制')] }}>
                {displayUid}
              </Text>
            )}
          </Space>
        );
      },
    });
  }

  // ── 筛选栏 ───────────────────────────────────────────────────
  const filterBar = (
    <div style={{ width: '100%' }}>
      <Form form={form} style={{ width: '100%' }} initialValues={{ dateRange: defaultLogDayRange() }} onFinish={() => fetchLogs(1, pageSize)}>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, width: '100%' }}>
          {isAdmin && (
            <>
              <Form.Item name="user_id" noStyle>
                <Input placeholder={t('task_logs.search_user_id', '搜索用户 UID/用户名')} prefix={<Search size={16} />} allowClear style={{ width: 180, fontSize: 12, height: 32 }} />
              </Form.Item>
              <Form.Item name="user_group" noStyle>
                <Select
                  placeholder={t('logs.search_user_group', '用户等级')}
                  allowClear
                  showSearch
                  optionFilterProp="label"
                  onChange={() => fetchLogs(1, pageSize)}
                  style={{ width: 130, fontSize: 12, height: 32 }}
                  popupMatchSelectWidth={false}
                  options={userLevels.map((l: any) => ({ value: l.group_key, label: `${l.name} (${l.discount}x)` }))}
                />
              </Form.Item>
            </>
          )}
          <Form.Item name="search_keyword" noStyle>
            <Input placeholder={t('logs.search_keyword', '日志ID/任务ID/渠道AID/密钥KID/上游YID')} prefix={<Search size={16} />} allowClear style={{ width: 270, fontSize: 12, height: 32 }} />
          </Form.Item>
          <Form.Item name="model" noStyle>
            <Input placeholder={t('logs.model_name', '模型名称')} prefix={<Search size={16} />} allowClear style={{ width: 220, fontSize: 12, height: 32 }} />
          </Form.Item>
          <Form.Item name="dateRange" noStyle>
            <LogDateTimeRangePicker isAdmin={isAdmin} className="font-size-12" />
          </Form.Item>
          <Space size={8} style={{ marginLeft: screens.xs ? 0 : 'auto' }}>
            <Button type="primary" htmlType="submit" icon={<Search size={14} />} loading={loading} disabled={loading} style={{ height: 32, borderRadius: 6, fontSize: 12, display: 'inline-flex', alignItems: 'center', justifyContent: 'center' }}>{t('task_logs.query', '查询')}</Button>
            {isAdmin && (
              <Tooltip title={t('logs.export_tooltip', '根据当前筛选条件导出 CSV（上限10万条）')}>
                <Button icon={<Download size={14} />} loading={exporting} disabled={loading} onClick={handleExport} style={{ height: 32, borderRadius: 6, fontSize: 12, display: 'inline-flex', alignItems: 'center', justifyContent: 'center' }}>{t('task_logs.export', '导出')}</Button>
              </Tooltip>
            )}
          </Space>
        </div>
      </Form>
    </div>
  );

  // ── 移动端卡片 ───────────────────────────────────────────────
  const renderMobileCard = (record: TaskLog) => {
    const tp = getTaskType(record, t);
    const tid = getTaskId(record);
    const status = getAsyncFinalStatus(record);
    return (
      <MobileCard
        compact={true}
        style={{ background: isLight ? '#fff' : '#141414', border: 'none' }}
        title={
          <Space vertical size={2}>
            <Space>
              <Tag color={tp.color}>{tp.icon} {tp.label}</Tag>
              <Text style={{ fontSize: 12, fontFamily: 'monospace' }}>{record.model}</Text>
            </Space>
            {user?.role === 'admin' && (record.mid || record.billing_pid || record.forward_eid) && (
              <Space size={4} style={{ display: 'flex', flexWrap: 'wrap' }}>
                {record.mid && <Text type="secondary" style={{ fontSize: 10, fontFamily: 'monospace' }}>MID:{record.mid}</Text>}
                {record.billing_pid && <Text type="secondary" style={{ fontSize: 10, fontFamily: 'monospace' }}>PID:{record.billing_pid}</Text>}
                {record.forward_eid && <Text type="secondary" style={{ fontSize: 10, fontFamily: 'monospace' }}>EID:{record.forward_eid}</Text>}
              </Space>
            )}
          </Space>
        }
        extra={
          status === 'pending' ? (
            <Space>
              <Tag color="processing">{t('task_logs.pending', '进行中')}</Tag>
              {isAsyncPost(record) && <Button type="text" size="small" icon={<RefreshCw size={14} />} onClick={(e) => { e.stopPropagation(); handleSyncTask(record.log_id!); }} />}
              {record.action_type === '视频' && (
                <Popconfirm
                  title={t('task_logs.confirm_cancel', '确认取消此视频任务？')}
                  onConfirm={(e) => { e?.stopPropagation(); handleCancelTask(record.log_id!); }}
                  onCancel={(e) => e?.stopPropagation()}
                  okText={t('common.confirm', '确认')}
                  cancelText={t('common.cancel', '取消')}
                >
                  <Button type="text" size="small" danger icon={<XCircle size={14} />} onClick={(e) => e.stopPropagation()} />
                </Popconfirm>
              )}
            </Space>
          ) : status === 'failed' ? (
            <Tag color="error" style={{ margin: 0 }}>{t('task_logs.fail', '失败')}</Tag>
          ) : (
            <Space size={6}>
              <Tag color="success" style={{ margin: 0 }}>{t('task_logs.success', '成功')}</Tag>
              {(record.action_type === '图片' || record.action_type === '视频' || record.action_type === '封装') && (
                <Button
                  size="small"
                  icon={<Eye size={12} />}
                  onClick={(e) => {
                    e.stopPropagation();
                    handlePreview(record);
                  }}
                  style={{
                    height: 22,
                    padding: '0 8px',
                    borderRadius: 6,
                    fontSize: 12,
                    fontWeight: 500,
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 4,
                    background: isLight ? '#f4f4f5' : 'rgba(255, 255, 255, 0.08)',
                    border: `1px solid ${isLight ? '#e4e4e7' : 'rgba(255, 255, 255, 0.16)'}`,
                    color: isLight ? '#18181b' : '#fafafa',
                    boxShadow: 'none',
                  }}
                >
                  {t('task_logs.preview', '预览')}
                </Button>
              )}
            </Space>
          )
        }
      >
        <CardRow label={t('task_logs.submit_time', '提交')}>
          <Space vertical size={0} align="end">
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
        {isAdmin && record.channel_group_aid && (
          <CardRow label={t('logs.channel_aid', '渠道信息')}>
            <Space size={4}>
              <Text type="secondary" style={{ fontSize: 12 }}>AID: {record.channel_group_aid}</Text>
              {record.is_ha === 1 && <Tag color="blue" style={{ fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '16px' }}>HA</Tag>}
            </Space>
          </CardRow>
        )}
        {isAdmin && (
          <CardRow label={t('logs.user', '用户')}>
            <Space vertical size={0} align="end">
              <Text style={{ fontSize: 12 }}>
                {record.user_nickname || record.user_uid || record.user_id}
                {record.user_admin_remark?.trim() ? <Text type="secondary" style={{ fontSize: 12 }}> {record.user_admin_remark.trim()}</Text> : null}
              </Text>
              {record.user_uid ? (
                <Text 
                  type="secondary" 
                  style={{ fontSize: 10, fontFamily: 'monospace' }}
                  copyable={{ text: String(record.user_uid), tooltips: [t('logs.copy', '复制'), t('logs.copy_success', '已复制')] }}
                >
                  UID: {record.user_uid}
                </Text>
              ) : (
                <Text type="secondary" style={{ fontSize: 10, fontFamily: 'monospace' }} copyable={{ text: record.user_id }}>
                  {record.user_id}
                </Text>
              )}
            </Space>
          </CardRow>
        )}
        <CardRow label={t('logs.latency', '耗时')}><Text type="secondary" style={{ fontSize: 12 }}>🕗 {(() => {
          if (isAsyncPost(record) && status === 'pending') return t('task_logs.processing', '处理中...');
          const sec = record.latency_ms / 1000;
          return sec >= 60 ? `${(sec / 60).toFixed(1)}m` : `${sec.toFixed(1)}s`;
        })()}</Text></CardRow>
        {tid !== '-' && <CardRow label={t('task_logs.task_id', '任务ID')}><Text style={{ fontSize: 11, fontFamily: 'monospace' }}>{tid}</Text></CardRow>}
        {record.cost > 0 && <CardRow label={t('task_logs.cost', '费用')}><Text style={{ fontSize: 12 }}>{Number(Number(record.cost).toFixed(6))}</Text></CardRow>}
      </MobileCard>
    );
  };

  if (redirectTo) {
    return <Navigate to={redirectTo} replace />;
  }

  return (
    <Card 
      variant="borderless" 
      style={{ 
        background: screens.xs ? 'transparent' : (isLight ? '#fff' : 'rgba(255,255,255,0.02)'), 
        borderRadius: 12,
        boxShadow: screens.xs ? 'none' : undefined,
        maxWidth: '100%',
        overflow: 'hidden'
      }} 
      styles={{ body: { padding: screens.xs ? 0 : '12px 20px 16px', maxWidth: '100%', overflowX: 'hidden' } }}
    >
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 12, marginBottom: 12 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 16, flexWrap: 'wrap' }}>
          <Typography.Title level={4} style={{ margin: 0, fontSize: 18 }}>
            {t('menu.task_logs', '任务日志')}
          </Typography.Title>
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <LogTypeTabs
              value={actionTypeFilter}
              defaultFirst={defaultLogType}
              isLight={isLight}
              themeToken={themeToken}
              options={logTypeOptions}
              onChange={(v: string) => {
                setSubTypeFilter(null);
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
                  background: themeToken?.colorFillAlter || (isLight ? '#fafafa' : '#1d1d1d'),
                  border: 'none',
                  color: themeToken?.colorTextSecondary || (isLight ? '#71717a' : '#a1a1aa'),
                }}
              />
            </Tooltip>
          </div>
        </div>
      </div>

      <div style={{ marginBottom: 12 }}>
        {filterBar}
      </div>

      {screens.xs ? (
        <MobileCardList
          dataSource={data}
          loading={loading}
          rowKey="id"
          compact={true}
          gap={4}
          pagination={listPagination({ current: page, pageSize, total, onChange })}
          renderCard={renderMobileCard}
        />
      ) : (
        <div ref={dragScrollRef} style={{ width: '100%', maxWidth: '100%' }}>
          <style>{`
            .task-logs-table .ant-table-thead .ant-table-filter-column {
              justify-content: center !important;
              gap: 2px;
            }
            .task-logs-table .ant-table-thead .ant-table-filter-column .ant-table-column-title {
              flex: none !important;
            }
            .task-logs-table .ant-table-thead .ant-table-filter-trigger,
            .task-logs-table .ant-table-thead .ant-table-filter-trigger:hover,
            .task-logs-table .ant-table-thead .ant-table-filter-trigger:active,
            .task-logs-table .ant-table-thead .ant-table-filter-trigger.ant-dropdown-open {
              margin-inline: 0 !important;
              background: transparent !important;
              background-color: transparent !important;
            }
          `}</style>
          <Table
            className="compact-table task-logs-table"
            columns={columns.map((c: any) => c ? { ...c, align: 'center' } : null).filter(Boolean) as any}
            dataSource={data}
            rowKey="id"
            loading={loading}
            expandable={
              allowDetails 
                ? { expandedRowKeys, expandedRowRender, onExpand: handleExpand, expandRowByClick: false } 
                : undefined
            }
            pagination={listPagination({
              current: page,
              pageSize,
              total,
              onChange,
            })}
            onChange={(_pagination, filters) => {
              let shouldResetPage = false;
              const typeFilter = filters?.type;
              const newSubFilter = (typeFilter && typeFilter.length > 0) ? (typeFilter[0] as string) : null;
              if (newSubFilter !== subTypeFilter) {
                setSubTypeFilter(newSubFilter);
                shouldResetPage = true;
              }
              const stFilter = (filters?.status && filters.status.length > 0) ? (filters.status[0] as string) : undefined;
              if (stFilter !== statusFilter) {
                setStatusFilter(stFilter);
                shouldResetPage = true;
              }
              if (shouldResetPage) {
                setPage(1);
              }
            }}
            scroll={{ x: 1200 }}
            size="small"
          />
        </div>
      )}

      {/* 媒体预览 Modal */}
      <MediaPreviewModal
        open={previewOpen}
        onClose={() => {
          setPreviewOpen(false);
          setPreviewUrls([]);
          setPreviewRecord(null);
        }}
        record={previewRecord}
        urls={previewUrls}
        type={previewType}
      />
      {/* 默认类型设置 Modal */}
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
            backgroundColor: isLight ? 'rgba(255, 255, 255, 0.95)' : 'rgba(28, 29, 31, 0.85)', 
            backdropFilter: 'blur(16px) saturate(120%)', 
            WebkitBackdropFilter: 'blur(16px) saturate(120%)',
            borderRadius: 16, 
            padding: 0, 
            overflow: 'hidden',
            border: isLight ? '1px solid rgba(0,0,0,0.1)' : '1px solid rgba(255,255,255,0.1)',
            boxShadow: isLight ? '0 12px 32px rgba(0,0,0,0.1)' : '0 24px 48px rgba(0,0,0,0.4)'
          },
        } as any}
      >
        <div style={{ marginBottom: 20 }}>
          <h3 style={{ fontSize: 16, fontWeight: 600, margin: 0, color: isLight ? '#1f2937' : '#E8EAED' }}>
            {t('logs.default_type_modal_title', '配置默认查看日志类型')}
          </h3>
          <p style={{ fontSize: 12, color: isLight ? '#71717a' : '#a1a1aa', margin: '4px 0 0 0' }}>
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
                  border: `1px solid ${isSelected ? themeToken.colorPrimary : (isLight ? 'rgba(0,0,0,0.08)' : 'rgba(255,255,255,0.08)')}`,
                  background: isSelected 
                    ? (isLight ? 'rgba(22, 119, 255, 0.05)' : 'rgba(22, 119, 255, 0.15)') 
                    : (isLight ? 'transparent' : 'rgba(255,255,255,0.02)'),
                  cursor: 'pointer',
                  transition: 'all 0.2s',
                  color: isSelected ? themeToken.colorPrimary : (isLight ? '#1f2937' : '#E8EAED'),
                }}
                onMouseEnter={(e) => {
                  if (!isSelected) {
                    e.currentTarget.style.borderColor = isLight ? 'rgba(0,0,0,0.15)' : 'rgba(255,255,255,0.15)';
                    e.currentTarget.style.background = isLight ? 'rgba(0,0,0,0.01)' : 'rgba(255,255,255,0.04)';
                  }
                }}
                onMouseLeave={(e) => {
                  if (!isSelected) {
                    e.currentTarget.style.borderColor = isLight ? 'rgba(0,0,0,0.08)' : 'rgba(255,255,255,0.08)';
                    e.currentTarget.style.background = isLight ? 'transparent' : 'rgba(255,255,255,0.02)';
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
              border: isLight ? '1px solid rgba(0,0,0,0.15)' : '1px solid rgba(255,255,255,0.15)',
              background: 'transparent',
              color: isLight ? '#1f2937' : '#E8EAED',
            }}
          >
            {t('common.cancel', '取消')}
          </Button>
          <Button
            onClick={() => {
              setDefaultLogType(tempDefaultType);
              setSubTypeFilter(null);
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
    </Card>
  );
};

export default TaskLogs;
