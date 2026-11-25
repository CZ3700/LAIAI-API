import React, { useEffect, useMemo, useRef, useState } from 'react';
import ModelSelector from '../../components/ModelSelector';
import { Table, Button, Space, Tag, Modal, Form, Input, InputNumber, message, Popconfirm, Card, Typography, Select, Row, Col, Switch, Grid, Segmented, Tooltip, Divider, Alert, List, Progress, Checkbox, Spin, Radio, Popover } from 'antd';
import MobileCardList, { MobileCard, CardRow, CardActions } from '../../components/MobileCardList';
import { listPagination, useListPager } from '../../components/ListPagination';
import { TableStickyHorizontalScroll } from '../../components/TableStickyHorizontalScroll';
import { PlusOutlined, EditOutlined, DeleteOutlined, ArrowLeftOutlined, ArrowRightOutlined, CloseOutlined, UnorderedListOutlined, AppstoreOutlined, PlayCircleOutlined, SearchOutlined, ApartmentOutlined, CloudServerOutlined, SettingOutlined, ThunderboltOutlined, ReloadOutlined, ClearOutlined, StopOutlined, ExperimentOutlined, VideoCameraOutlined, CalculatorOutlined, CheckSquareOutlined, CheckCircleOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { useNavigate, useLocation, useParams, useSearchParams, Navigate } from 'react-router-dom';
import request from '../../utils/request';
import useSettingsStore from '../../store/settings';
import type { Channel, ChannelCategory } from '../../types';
import { useThemeStore } from '../../store/theme';
import ChannelCategoryManager from '../../components/Channels/ChannelCategoryManager';
import { ChannelBillingSimulator } from './components/ChannelBillingSimulator';
import { channelAnalysisPath, channelEditPath, channelListPath, channelNewPath } from './channelPaths';
import { getStorageProvider, STORAGE_PROVIDERS } from '../../components/Storage';
import {
  parseQuotaLimitInput,
  formatQuotaLimitDisplay,
  validateQuotaHierarchy,
  isFiniteQuotaLimit,
  quotaPeriodItems,
  quotaRingPercent,
  QUOTA_RING_BLUE,
} from '../../utils/quotaPeriod';

/** 站点已配齐凭证的对象存储（按 STORAGE_PROVIDERS 扩展；字段与后端 from_settings_for 一致） */
function siteConfiguredProviders(storage: Record<string, any> | null | undefined) {
  if (!storage) return [];
  return STORAGE_PROVIDERS.filter((p) => {
    const f = p.fieldKeys;
    return [f.keyId, f.keySecret, f.endpoint, f.region, f.bucket]
      .every((k) => String(storage[k] || '').trim());
  });
}

const { Title, Text } = Typography;
const { Option } = Select;
const { useBreakpoint } = Grid;

/** 视频/图片默认基准分辨率档与快捷扩展池 */
const VIDEO_RES_PRESETS = ['480p', '720p', '1080p'] as const;
const IMAGE_RES_PRESETS = ['1k', '2k', '4k'] as const;
const VIDEO_EXTRA_TAGS = ['540p', '2k', '4k'] as const;

/** 与后端 normalize_resolution_label 对齐，保证存库 key 能命中请求分辨率 */
function normalizeResKey(raw: string): string {
  let res = raw.trim().toLowerCase().replace(/\*/g, 'x');
  if (res && /^[0-9]+$/.test(res)) res += 'p';
  return res;
}

/** 高级分辨率输入框占位指引：仅对视频核心档位提示底座，图片模型保持简明 */
function resPlaceholder(res: string, isVideo?: boolean): string {
  if (isVideo) {
    if (res === '480p') return '480p 上游模型名（出 480p 原画）';
    if (res === '720p') return '720p 对应上游模型（级联通常为 480p 底座模型）';
    if (res === '1080p') return '1080p 对应上游模型（级联通常为 720p 底座模型）';
    if (res === '2k' || res === '4k') return `${res} 对应上游模型（级联通常为 1080p 底座模型）`;
  }
  return `${res} 上游模型名`;
}

type ResModelMapping = Record<string, Record<string, Record<string, string>>>;

const isFilled = (v: unknown) => !!v && String(v).trim().length > 0;
const countFilled = (obj?: Record<string, unknown> | null) =>
  Object.values(obj || {}).filter(isFilled).length;

const COMFYUI_DISPATCH_DEFAULT = 'priority_weight';

function parseComfyuiServerIds(cfg: Record<string, any> | undefined): number[] {
  const raw = Array.isArray(cfg?.comfyui_server_ids) ? cfg!.comfyui_server_ids : [];
  const ids: number[] = [];
  for (const v of raw) {
    const id = Number(v);
    if (id > 0 && !ids.includes(id)) ids.push(id);
  }
  if (ids.length) return ids;
  const n = Number(cfg?.comfyui_server_id);
  return n > 0 ? [n] : [];
}

function formatMeltdownRemain(sec: number): string {
  const n = Math.max(0, Math.floor(sec));
  const h = Math.floor(n / 3600);
  const m = Math.floor((n % 3600) / 60);
  const s = n % 60;
  if (h > 0) return `${h}小时${m}分${String(s).padStart(2, '0')}秒`;
  if (m > 0) return `${m}分${String(s).padStart(2, '0')}秒`;
  return `${s}秒`;
}

/** 设置/删除某模型某 scope 的分辨率映射表 */
function patchResScope(
  prev: ResModelMapping,
  modelId: string,
  scope: string,
  map: Record<string, string> | null,
): ResModelMapping {
  const scopes = { ...(prev[modelId] || {}) };
  if (!map || Object.keys(map).length === 0) delete scopes[scope];
  else scopes[scope] = map;
  if (Object.keys(scopes).length === 0) {
    const next = { ...prev };
    delete next[modelId];
    return next;
  }
  return { ...prev, [modelId]: scopes };
}

/** 读入/保存共用：规范化分辨率 key、去掉空值；按当前模式（HA 子渠 vs 普通单渠 default）严格隔离并过滤已解绑子渠 */
function cleanResModelMapping(
  raw: ResModelMapping | Record<string, any> | null | undefined,
  allowedSubIds?: number[],
  isHaGroup?: boolean,
): ResModelMapping {
  const cleaned: ResModelMapping = {};
  for (const [modelId, scopes] of Object.entries(raw || {})) {
    const validScopes: Record<string, Record<string, string>> = {};
    for (const [scope, resMap] of Object.entries((scopes || {}) as Record<string, Record<string, string>>)) {
      if (isHaGroup === true) {
        if (scope === 'default') continue;
        if (allowedSubIds && !allowedSubIds.includes(Number(scope))) continue;
      } else if (isHaGroup === false) {
        if (scope !== 'default') continue;
      } else if (scope !== 'default' && allowedSubIds && !allowedSubIds.includes(Number(scope))) {
        continue;
      }
      const validRes: Record<string, string> = {};
      for (const [res, alias] of Object.entries(resMap || {})) {
        const key = normalizeResKey(String(res || ''));
        if (key && isFilled(alias)) validRes[key] = String(alias).trim();
      }
      if (Object.keys(validRes).length > 0) validScopes[scope] = validRes;
    }
    if (Object.keys(validScopes).length > 0) cleaned[modelId] = validScopes;
  }
  return cleaned;
}

/** 按模型类型推荐基准分辨率档（优先模型类型，其次名称/id，最后渠道分类） */
function resPresetsForHint(...hints: Array<string | null | undefined>): string[] {
  for (const h of hints) {
    const n = (h || '').toLowerCase();
    if (!n) continue;
    if (/视|video|seedance|kling|runway|luma|minimax.?video/.test(n)) return [...VIDEO_RES_PRESETS];
    if (/图|image|dall|flux|midjourney|sdxl|gpt-image|imagen/.test(n)) return [...IMAGE_RES_PRESETS];
  }
  return [...IMAGE_RES_PRESETS];
}

function resScopeKey(modelId: string, scope: string) {
  return `${modelId}:${scope}`;
}

/** 已选条目对应的当前模型请求 ID；模型表还没对上时保持原 key */
function selectionModelId(models: any[], midOrId: string): string {
  const key = String(midOrId);
  const match = models.find(m => String(m?.mid) === key);
  const id = match?.model_id != null ? String(match.model_id).trim() : '';
  return id || key;
}

/**
 * 渠道映射在库里按 model_id 存。编辑态改挂到已选 mid 上，
 * 这样模型请求 ID 被改掉之后，输入框不会因为 key 对不上把刚填的值丢掉。
 * 渠道只绑定一个模型、且只剩一个已不存在的旧 key 时，把旧 key 回挂到这个 mid。
 */
function rekeyMappingToSelection<T>(
  stored: Record<string, T> | null | undefined,
  selected: string[],
  models: any[],
): Record<string, T> {
  const src = stored || {};
  const next: Record<string, T> = {};
  const consumed = new Set<string>();
  for (const raw of selected) {
    const mid = String(raw);
    const modelId = selectionModelId(models, mid);
    if (modelId !== mid && Object.prototype.hasOwnProperty.call(src, modelId) && !consumed.has(modelId)) {
      next[mid] = src[modelId];
      consumed.add(modelId);
      continue;
    }
    if (Object.prototype.hasOwnProperty.call(src, mid) && !consumed.has(mid)) {
      next[mid] = src[mid];
      consumed.add(mid);
    }
  }
  const live = new Set<string>();
  for (const m of models) {
    if (m?.mid) live.add(String(m.mid));
    if (m?.model_id) live.add(String(m.model_id));
  }
  const pending = selected.map(String).filter(mid => !Object.prototype.hasOwnProperty.call(next, mid));
  const dead = Object.keys(src).filter(k => !consumed.has(k) && !live.has(k));
  // 渠道只绑了一个模型时，旧请求 ID 只能属于它；多个模型时不猜测，避免把别名挂错
  if (selected.length === 1 && pending.length === 1 && dead.length === 1) {
    next[pending[0]] = src[dead[0]];
    consumed.add(dead[0]);
  }
  for (const [k, v] of Object.entries(src)) {
    if (!consumed.has(k) && !live.has(k) && !Object.prototype.hasOwnProperty.call(next, k)) next[k] = v;
  }
  return next;
}

/** 保存时把已选 mid 写回当前 model_id；对不上的旧 key 原样保留 */
function rekeyMappingToModelId<T>(
  bySelection: Record<string, T>,
  models: any[],
  selected: string[],
): Record<string, T> {
  const selectedSet = new Set(selected.map(String));
  const next: Record<string, T> = {};
  for (const [key, value] of Object.entries(bySelection)) {
    const outKey = selectedSet.has(key) ? selectionModelId(models, key) : key;
    if (!Object.prototype.hasOwnProperty.call(next, outKey)) next[outKey] = value;
  }
  return next;
}

function dropRecordKeys<T>(prev: Record<string, T>, drop: Set<string>): Record<string, T> {
  let changed = false;
  const next = { ...prev };
  for (const k of Object.keys(next)) {
    if (drop.has(k)) {
      delete next[k];
      changed = true;
    }
  }
  return changed ? next : prev;
}

function channelBoundModels(record: Channel): string[] {
  if (!Array.isArray(record.models)) return [];
  return record.models.filter((m) => !!m && String(m).trim());
}

/** 按模型名称 / 模型 ID / MID（含模型 ID 别名）找出「选择模型」里绑定了它的渠道分组 */
function locateChannelIdsByModel(
  query: string,
  listed: any[],
  unlisted: any[],
  channels: Channel[],
): Set<number> | null {
  const q = query.trim().toLowerCase();
  if (!q) return null;

  const catalog = new Map<string, { name: string; modelId: string; mid: string; alias: string }>();
  const put = (m: any) => {
    const mid = String(m?.mid || '').trim();
    const modelId = String(m?.model_id || '').trim();
    const key = mid || modelId;
    if (!key || catalog.has(key)) return;
    catalog.set(key, {
      name: String(m?.name || modelId || mid),
      modelId,
      mid,
      alias: String(m?.model_id_alias || '').trim(),
    });
  };
  for (const m of listed) put(m);
  for (const m of unlisted) put(m);

  const hit = (name: string, modelId: string, mid: string, alias: string) =>
    [name, modelId, mid, alias].some((v) => v && v.toLowerCase().includes(q));

  const byKey = new Map<string, Channel[]>();
  for (const c of channels) {
    for (const k of channelBoundModels(c)) {
      const list = byKey.get(k);
      if (list) list.push(c);
      else byKey.set(k, [c]);
    }
  }

  const channelIds = new Set<number>();
  const consumed = new Set<string>();

  for (const model of catalog.values()) {
    if (!hit(model.name, model.modelId, model.mid, model.alias)) continue;
    for (const k of [model.mid, model.modelId]) {
      if (!k) continue;
      consumed.add(k);
      for (const c of byKey.get(k) || []) channelIds.add(c.id);
    }
  }

  for (const [k, chs] of byKey) {
    if (consumed.has(k) || !k.toLowerCase().includes(q)) continue;
    for (const c of chs) channelIds.add(c.id);
  }

  return channelIds;
}

const Channels: React.FC = () => {
  const { themeMode } = useThemeStore();
  const isLight = themeMode === 'light';
  const { t } = useTranslation();
  const { settings } = useSettingsStore();
  const currencySymbol = settings?.currency?.currency_symbol || '$';
  const adminPath = settings?.site?.admin_path || 'admin';
  const quotaTz = settings?.site?.default_timezone || 'Asia/Shanghai';
  const [channels, setChannels] = useState<Channel[]>([]);
  const [viewMode, setViewMode] = useState<'list' | 'card'>(() => {
    return (localStorage.getItem('channels_view_mode') as 'list' | 'card') || 'card';
  });
  const { page: currentPage, pageSize, setPage: setCurrentPage, setPageSize, onChange: onPageChange } = useListPager(
    (localStorage.getItem('channels_view_mode') as 'list' | 'card') === 'list' ? 20 : 24
  );
  const [availableModels, setAvailableModels] = useState<any[]>([]);
  const [unlistedModels, setUnlistedModels] = useState<any[]>([]);
  const [availableUserLevels, setAvailableUserLevels] = useState<any[]>([]);
  const [presets, setPresets] = useState<any[]>([]);
  const [activePlugins, setActivePlugins] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(true);
  const [editingChannel, setEditingChannel] = useState<Channel | null>(null);
  const [form] = Form.useForm();
  const navigate = useNavigate();
  const location = useLocation();
  const { id: routeParamId } = useParams<{ id?: string }>();
  const [searchParams] = useSearchParams();
  const normalizedPath = location.pathname.replace(/\/$/, '');
  const isNewRoute = /\/channels\/new$/.test(normalizedPath);
  const isEditRoute = /\/channels\/edit\/[^/]+$/.test(normalizedPath);
  const routeEditId = isEditRoute ? routeParamId : undefined;
  const isEditorOpen = isNewRoute || isEditRoute;
  const editQueryId = searchParams.get('edit');

  const handleCloseModal = () => {
    if (location.state && (location.state as { from?: string }).from === 'model-display') {
      navigate(`/${adminPath}/channels/model-display`);
    } else {
      navigate(channelListPath(adminPath));
    }
  };
  const [showMapping, setShowMapping] = useState(false);
  const [activeMappingInputs, setActiveMappingInputs] = useState<string[]>([]);
  const [modelMappingState, setModelMappingState] = useState<Record<string, string>>({});
  // 高可用渠道组：编辑态按已选 mid 存 { mid: { sub_channel_id: alias } }，保存时写成 model_id
  const [haModelMappingState, setHaModelMappingState] = useState<Record<string, Record<string, string>>>({});
  // 分辨率映射：编辑态 { mid: { "default"|subId: { "480p"|"1k": alias } } }
  const [resModelMappingState, setResModelMappingState] = useState<ResModelMapping>({});
  const [expandedHaModels, setExpandedHaModels] = useState<string[]>([]);
  // 展开的分辨率高级面板 key：`${modelId}:${scopeKey}`
  const [expandedResScopes, setExpandedResScopes] = useState<string[]>([]);
  // 用户动态添加的分辨率项：{ `${modelId}:${scope}`: ['540p', '1024x1024'] }
  const [customResScopes, setCustomResScopes] = useState<Record<string, string[]>>({});
  const [submitting, setSubmitting] = useState(false);
  const screens = useBreakpoint();
  const [isExcludeMode, setIsExcludeMode] = useState(false);
  const [activeRightPanel, setActiveRightPanel] = useState<'models' | 'levels' | 'mapping' | 'presets'>('models');
  const [presetSearchText, setPresetSearchText] = useState('');
  /** 选择上游渠道 / 高可用绑定物理上游：全部 | 激活 | 禁用 */
  const [upstreamStatusFilter, setUpstreamStatusFilter] = useState<'all' | 1 | 0>('all');
  const [enableQuota, setEnableQuota] = useState(false);
  const [upstreamTab, setUpstreamTab] = useState<'preset' | 'volcengine_enhance' | 'comfyui'>('preset');
  const [volcengineEnhanceKeys, setVolcengineEnhanceKeys] = useState<any[]>([]);
  const [comfyuiServers, setComfyuiServers] = useState<any[]>([]);
  const [comfyuiDispatchRules, setComfyuiDispatchRules] = useState<any[]>([]);
  /** 模型选择的桥接状态，同步 form store 与 ModelSelector 双向数据 */
  const [channelModelMids, setChannelModelMids] = useState<string[]>([]);
  const [selectedSubChannelAids, setSelectedSubChannelAids] = useState<any[]>([]);
  const [haRules, setHaRules] = useState<{ id: string; name: string; retries: number }[]>([]);
  const [haDef, setHaDef] = useState('');
  // 熔断状态 Map: { channelId: { channel_meltdown, sub_channels } }
  const [meltdownMap, setMeltdownMap] = useState<Record<number, any>>({});
  const [meltdownLoading, setMeltdownLoading] = useState<Record<number, boolean>>({});
  const [subMeltLoading, setSubMeltLoading] = useState<Record<string, boolean>>({});

  // 计费全链路模拟仿真状态
  const [simulatingChannel, setSimulatingChannel] = useState<Channel | null>(null);

  // 快速编辑上游渠道在当前渠道分组下的优先级、权重
  const [quickEditModalOpen, setQuickEditModalOpen] = useState(false);
  const [quickEditTarget, setQuickEditTarget] = useState<any>(null);
  const [quickEditLoading, setQuickEditLoading] = useState(false);
  const [quickEditForm] = Form.useForm();

  const getEffectiveUpstreamSchedule = (preset: any, cfgObj: Record<string, any> = configObj) => {
    const defaultRate = Number(preset?.rate ?? 1.0);
    const defaultPriority = Number(preset?.priority ?? 0);
    const defaultWeight = Number(preset?.weight ?? 1);
    const defaultStatus = (preset?.status ?? 1) === 1 ? 1 : 0;
    const rawOv = cfgObj?.upstream_overrides?.[String(preset?.id)];
    const customPriority = typeof rawOv?.priority === 'number' ? rawOv.priority : undefined;
    const customWeight = typeof rawOv?.weight === 'number' ? rawOv.weight : undefined;
    const customStatus = rawOv?.status === 0 ? 0 : undefined;
    const customRpmLimit =
      typeof rawOv?.rpm_limit === 'number' && rawOv.rpm_limit > 0
        ? Math.round(rawOv.rpm_limit)
        : undefined;
    const hasCustom =
      customPriority !== undefined ||
      customWeight !== undefined ||
      customStatus !== undefined ||
      customRpmLimit !== undefined;
    return {
      rate: defaultRate,
      priority: customPriority ?? defaultPriority,
      weight: customWeight ?? defaultWeight,
      rpmLimit: customRpmLimit ?? 0,
      status: defaultStatus === 0 ? 0 : (customStatus ?? 1),
      isGroupEnabled: customStatus !== 0,
      defaultRate,
      defaultPriority,
      defaultWeight,
      defaultStatus,
      customPriority,
      customWeight,
      customStatus,
      customRpmLimit,
      hasCustom,
    };
  };

  const renderUpstreamScheduleTags = (
    preset: any,
    size: 'xs' | 'sm' = 'sm',
    onlyRate = false,
  ) => {
    const sched = getEffectiveUpstreamSchedule(preset, configObj);
    const baseStyle: React.CSSProperties = size === 'xs'
      ? { margin: 0, fontSize: 10, lineHeight: '14px', height: 16, padding: '0 4px', borderRadius: 2 }
      : { margin: 0, fontSize: 11, borderRadius: 4, padding: '0 4px', lineHeight: '18px' };
    const defaultTagStyle: React.CSSProperties = {
      ...baseStyle,
      background: isLight ? '#f4f4f5' : '#27272a',
      color: isLight ? '#18181b' : '#f4f4f5',
      border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}`,
    };
    const rateTag = (
      <Tooltip title={`上游渠道倍率 ${sched.rate}x（全局统一）`}>
        <Tag style={defaultTagStyle}>
          倍率: {sched.rate}x
        </Tag>
      </Tooltip>
    );
    if (onlyRate) return rateTag;

    const customTagStyle: React.CSSProperties = {
      ...baseStyle,
      background: isLight ? '#e6f4ff' : 'rgba(22,119,255,0.16)',
      color: isLight ? '#0958d9' : '#69b1ff',
      border: `1px solid ${isLight ? '#91caff' : 'rgba(22,119,255,0.45)'}`,
      fontWeight: 600,
    };

    return (
      <>
        {rateTag}
        <Tooltip
          title={
            sched.customPriority !== undefined
              ? `当前渠道分组独立优先级 ${sched.priority}（预设默认: ${sched.defaultPriority}）`
              : `跟随上游预设默认优先级 ${sched.defaultPriority}`
          }
        >
          <Tag style={sched.customPriority !== undefined ? customTagStyle : defaultTagStyle}>
            优先级: {sched.priority}
          </Tag>
        </Tooltip>
        <Tooltip
          title={
            sched.customWeight !== undefined
              ? `当前渠道分组独立权重 ${sched.weight}（预设默认: ${sched.defaultWeight}）`
              : `跟随上游预设默认权重 ${sched.defaultWeight}`
          }
        >
          <Tag style={sched.customWeight !== undefined ? customTagStyle : defaultTagStyle}>
            权重: {sched.weight}
          </Tag>
        </Tooltip>
        {sched.rpmLimit > 0 && (
          <Tooltip title={`当前渠道分组限制每分钟最多 ${sched.rpmLimit} 次请求（满额自动切同组备选，不计入故障熔断）`}>
            <Tag style={customTagStyle}>
              RPM: {sched.rpmLimit}/分
            </Tag>
          </Tooltip>
        )}
      </>
    );
  };

  const handleQuickEditPreset = (target: any) => {
    if (!target || target.id === -99) return;
    const sched = getEffectiveUpstreamSchedule(target, configObj);
    setQuickEditTarget(target);
    quickEditForm.setFieldsValue({
      rate: sched.rate,
      priority: sched.priority,
      weight: sched.weight,
      rpm_limit: sched.rpmLimit,
    });
    setQuickEditModalOpen(true);
  };

  const applyQuickEditSchedule = async (newPriority: number, newWeight: number, newRpmLimit: number = 0) => {
    if (!quickEditTarget?.id) return;
    setQuickEditLoading(true);
    try {
      const defaultPriority = Number(quickEditTarget.priority ?? 0);
      const defaultWeight = Number(quickEditTarget.weight ?? 1);
      const targetKey = String(quickEditTarget.id);
      const prevOv = configObj.upstream_overrides?.[targetKey];

      const entry: Record<string, number> = {};
      if (newPriority !== defaultPriority) entry.priority = newPriority;
      if (newWeight !== defaultWeight) entry.weight = newWeight;
      if (newRpmLimit > 0) entry.rpm_limit = newRpmLimit;
      if (prevOv?.status === 0) entry.status = 0;

      const nextOverrides: Record<string, Record<string, number>> = {
        ...(configObj.upstream_overrides || {}),
      };
      const hasCustom = Object.keys(entry).length > 0;
      if (hasCustom) {
        nextOverrides[targetKey] = entry;
      } else {
        delete nextOverrides[targetKey];
      }

      const nextConfigObj: Record<string, any> = { ...configObj };
      if (Object.keys(nextOverrides).length > 0) {
        nextConfigObj.upstream_overrides = nextOverrides;
      } else {
        delete nextConfigObj.upstream_overrides;
      }
      setConfigObj(nextConfigObj);

      if (editingChannel?.id) {
        const persistedConfig: Record<string, any> = {
          ...parseChannelConfig(editingChannel),
        };
        if (Object.keys(nextOverrides).length > 0) {
          persistedConfig.upstream_overrides = nextOverrides;
        } else {
          delete persistedConfig.upstream_overrides;
        }
        const saved: any = await request.put(`/channels/${editingChannel.id}`, {
          config: persistedConfig,
        });
        const updatedChannel = (saved?.data || saved) as Channel;
        if (updatedChannel?.id) {
          setChannels((prev) =>
            prev.map((c) => (c.id === updatedChannel.id ? { ...c, ...updatedChannel } : c)),
          );
          setEditingChannel((prev) => (prev ? { ...prev, ...updatedChannel } : prev));
        }
      }

      message.success(
        hasCustom
          ? '已保存当前渠道分组的独立调度参数'
          : '已恢复跟随上游预设默认调度参数',
      );
      setQuickEditModalOpen(false);
      setQuickEditTarget(null);
    } catch (err: any) {
      console.error('更新当前分组上游调度配置失败:', err);
      message.error(err?.message || '更新当前分组上游调度配置失败');
    } finally {
      setQuickEditLoading(false);
    }
  };

  const handleSaveQuickEdit = async () => {
    try {
      const values = await quickEditForm.validateFields();
      if (!quickEditTarget?.id) return;
      const newPriority = Math.round(Number(values.priority ?? 0));
      const newWeight = Math.max(1, Math.round(Number(values.weight ?? 1)));
      const newRpmLimit = Math.max(0, Math.round(Number(values.rpm_limit ?? 0)));
      await applyQuickEditSchedule(newPriority, newWeight, newRpmLimit);
    } catch (err: any) {
      if (err?.errorFields) return;
    }
  };

  const handleResetQuickEdit = async () => {
    if (!quickEditTarget?.id) return;
    const defaultRate = Number(quickEditTarget.rate ?? 1.0);
    const defaultPriority = Number(quickEditTarget.priority ?? 0);
    const defaultWeight = Number(quickEditTarget.weight ?? 1);
    quickEditForm.setFieldsValue({
      rate: defaultRate,
      priority: defaultPriority,
      weight: defaultWeight,
      rpm_limit: 0,
    });
    await applyQuickEditSchedule(defaultPriority, defaultWeight, 0);
  };

  // useRef to hold reliable copies of models/levels/category outside AntD form store
  // (form store gets corrupted when model_mapping Form.Items are registered)
  const modelsRef = useRef<string[]>([]);
  const levelsRef = useRef<string[]>([]);
  const categoryIdRef = useRef<number | null>(null);
  const hydratedEditIdRef = useRef<string | null>(null);
  const newFormReadyRef = useRef(false);
  const saveIntentRef = useRef<'stay' | 'exit'>('exit');
  const keepEditorIdRef = useRef<string | null>(null);

  const [statusFilter, setStatusFilter] = useState<number | 'all'>(1);
  const [categoryFilter, setCategoryFilter] = useState<number | 'all' | 'unclassified'>('all');
  const [typeFilter, setTypeFilter] = useState<'all' | 'default' | 'volcengine' | 'ha' | 'comfyui'>('all');
  const [searchQuery, setSearchQuery] = useState('');
  const [modelLocateQuery, setModelLocateQuery] = useState('');
  const [configObj, setConfigObj] = useState<Record<string, any>>({});
  /** 站点存储设置（仅用于渠道 TOS 厂商选择；无密钥展示需求） */
  const [siteStorage, setSiteStorage] = useState<Record<string, any> | null>(null);
  const siteStorageProviders = useMemo(() => siteConfiguredProviders(siteStorage), [siteStorage]);
  const siteDefaultProvider = String(siteStorage?.default_provider || 'tos');
  const multiSiteStorage = siteStorageProviders.length > 1;
  const haMaxRetries = haRules.find(r => r.id === (configObj.rule || haDef))?.retries || 3;
  const [categories, setCategories] = useState<ChannelCategory[]>([]);
  const [isCategoryManagerVisible, setIsCategoryManagerVisible] = useState(false);
  const [boundEditor, setBoundEditor] = useState<{ id: number; keys: string[]; checked: string[] } | null>(null);
  const [boundSaving, setBoundSaving] = useState(false);

  // 清除媒体封装凭证关联（取消选择时复用）
  const clearVolcengineEnhance = () => {
    setUpstreamTab('preset');
    form.setFieldsValue({ provider_type: 'custom' });
    setConfigObj(prev => {
      const { volcengine_enhance_credential_id, ...rest } = prev;
      return rest;
    });
  };

  const clearComfyui = () => {
    setUpstreamTab('preset');
    form.setFieldsValue({ provider_type: 'custom' });
    setConfigObj(prev => {
      const { comfyui_workflow_id, comfyui_server_id, comfyui_server_ids, comfyui_dispatch, ...rest } = prev;
      return rest;
    });
  };

  const getChannelTypeKey = (c: Channel): 'default' | 'volcengine' | 'ha' | 'comfyui' => {
    if (c.provider_type === 'high_availability_group') return 'ha';
    if (c.provider_type === 'volcengine') return 'volcengine';
    if (c.provider_type === 'comfyui') return 'comfyui';
    return 'default';
  };

  const modelLocateIds = useMemo(
    () => locateChannelIdsByModel(modelLocateQuery, availableModels, unlistedModels, channels),
    [modelLocateQuery, availableModels, unlistedModels, channels],
  );

  const filteredChannels = channels.filter(c => {
    if (modelLocateIds && !modelLocateIds.has(c.id)) return false;

    let matchStatus = true;
    if (statusFilter !== 'all') {
      matchStatus = c.status === statusFilter;
    }
    
    let matchSearch = true;
    if (searchQuery) {
      const q = searchQuery.toLowerCase();
      matchSearch = !!((c.name && c.name.toLowerCase().includes(q)) || 
                    (c.group_aid && c.group_aid.toLowerCase().includes(q)));
    }

    let matchCategory = true;
    if (categoryFilter !== 'all') {
      if (categoryFilter === 'unclassified') {
        matchCategory = !c.category_id;
      } else {
        matchCategory = c.category_id === categoryFilter;
      }
    }

    let matchType = true;
    if (typeFilter !== 'all') {
      matchType = getChannelTypeKey(c) === typeFilter;
    }

    return matchStatus && matchSearch && matchCategory && matchType;
  });

  // ===== 当前页渠道列表（全选时仅全选当前页） =====
  const currentPageChannels = useMemo(() => {
    const startIndex = (currentPage - 1) * pageSize;
    return filteredChannels.slice(startIndex, startIndex + pageSize);
  }, [filteredChannels, currentPage, pageSize]);

  useEffect(() => {
    setCurrentPage(1);
  }, [searchQuery, modelLocateQuery, statusFilter, categoryFilter, typeFilter]);

  useEffect(() => {
    const maxPage = Math.ceil(filteredChannels.length / pageSize);
    if (maxPage > 0 && currentPage > maxPage) {
      setCurrentPage(maxPage);
    }
  }, [filteredChannels.length, pageSize, currentPage]);

  const fetchChannels = async () => {
    setLoading(true);
    try {
      const resp = await (request.get('/channels') as unknown as Promise<{ data: Channel[] }>);
      setChannels(resp.data || []);
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  const fetchModels = async () => {
    try {
      const resp = await (request.get('/models') as unknown as Promise<{ data: any[] }>);
      setAvailableModels(resp.data || []);
    } catch (e) {
      console.error(e);
    }
  };

  const fetchUnlistedModels = async () => {
    try {
      const resp = await (request.get('/models', { params: { source: 'library' } }) as unknown as Promise<{ data: any[] }>);
      const rows = Array.isArray(resp?.data) ? resp.data : [];
      setUnlistedModels(rows.filter((m) => m.library_kind === 'unlisted'));
    } catch (e) {
      console.error(e);
    }
  };

  const fetchUserLevels = async () => {
    try {
      const resp = await (request.get('/user_levels') as unknown as Promise<{ data: any[] }>);
      setAvailableUserLevels(resp.data || []);
    } catch (e) {
      console.error(e);
    }
  };

  const fetchHaRules = async () => {
    try {
      const res = await (request.get('/plugins/high_availability_channel/ha-config') as Promise<any>);
      const rules = Array.isArray(res?.rules) ? res.rules : [];
      setHaRules(rules.map((r: any) => ({ id: r.id, name: r.name, retries: r.retries || 3 })));
      setHaDef(res?.def || rules[0]?.id || '');
    } catch (e) {
      console.error('加载高可用插件配置失败:', e);
    }
  };

  const fetchPresets = async () => {
    try {
      const resp = await (request.get('/channel-configs') as unknown as Promise<{ data: any[] }>);
      const data = resp.data || [];
      const haPreset = {
        id: -99,
        name: '高可用虚拟渠道组',
        provider_type: 'high_availability_group',
        yid: 'HA',
        rate: 1.0,
      };
      setPresets([haPreset, ...data]);
    } catch (e) {
      console.error(e);
    }
  };

  const fetchCategories = async () => {
    try {
      const resp = await (request.get('/channel-categories') as any);
      setCategories(Array.isArray(resp) ? resp : []);
    } catch (e) {
      console.error(e);
    }
  };

  const resolveCategoryName = (categoryId?: number | null) => {
    if (!categoryId) return null;
    const cat = categories.find(c => c.id === categoryId);
    return cat?.name || null;
  };

  const activeCategories = categories.filter(c => !!c.is_active);

  const resolveUserLevelMeta = (rawKey: string) => {
    const idStr = String(rawKey ?? '').trim();
    const lv = availableUserLevels.find(
      (l: any) =>
        String(l.id) === idStr ||
        String(l.id).padStart(4, '0') === idStr ||
        l.group_key === idStr ||
        l.name === idStr,
    );
    const isNumeric = /^\d+$/.test(idStr);
    if (!lv) {
      const ulid = isNumeric ? idStr.padStart(4, '0') : idStr;
      const baseLabel = isNumeric ? `ULID:${ulid}` : idStr;
      return {
        level: null as any,
        exists: false,
        isDisabled: false,
        hasLevelDisc: false,
        ulid,
        isNumeric,
        name: isNumeric ? `ULID: ${ulid}` : idStr,
        tagText: `${baseLabel} (已删除)`,
        statusTag: '已删除' as const,
        discountText: '',
      };
    }
    const ulid = String(lv.id).padStart(4, '0');
    const v = lv.discount_type ?? 0;
    const mask = v >= 16 ? (v & 15) : (v === 1 ? 6 : v === 2 ? 3 : 15);
    const hasLevelDisc = (mask & 1) !== 0;
    const isDisabled = lv.status === 0 || lv.is_active === 0 || lv.enabled === false;
    const statusTag = isDisabled ? ('已禁用' as const) : (!hasLevelDisc ? ('倍率未启用' as const) : null);
    return {
      level: lv,
      exists: true,
      isDisabled,
      hasLevelDisc,
      ulid,
      isNumeric: true,
      name: lv.name,
      tagText: isDisabled ? `${lv.name} (已禁用)` : lv.name,
      statusTag,
      discountText: `${lv.discount}x`,
    };
  };

  const normalizeLevelIds = (rawIds: string[], levelsList: any[] = availableUserLevels) => {
    if (!Array.isArray(rawIds) || rawIds.length === 0) return [];
    const out: string[] = [];
    for (const val of rawIds) {
      const key = String(val ?? '').trim();
      if (!key) continue;
      const match = levelsList.find(
        (l: any) =>
          String(l.id) === key ||
          String(l.id).padStart(4, '0') === key ||
          l.group_key === key ||
          l.name === key,
      );
      const resolved = match ? String(match.id) : key;
      if (!out.includes(resolved)) out.push(resolved);
    }
    return out;
  };

  const unlistedByKey = useMemo(() => {
    const map = new Map<string, any>();
    for (const m of unlistedModels) {
      if (m.mid) map.set(String(m.mid), m);
      if (m.model_id && !map.has(String(m.model_id))) map.set(String(m.model_id), m);
    }
    return map;
  }, [unlistedModels]);

  const modelByKey = useMemo(() => {
    const map = new Map<string, { modelId: string; mid: string; isActive: boolean; listed: boolean }>();
    for (const m of availableModels) {
      const info = {
        modelId: m.model_id || m.name || m.mid,
        mid: m.mid || '',
        isActive: m.is_active !== 0 && m.is_active !== false,
        listed: true,
      };
      if (m.mid) map.set(String(m.mid), info);
      if (m.model_id) map.set(String(m.model_id), info);
    }
    return map;
  }, [availableModels]);

  const resolveBoundModel = (key: string) => {
    const hit = modelByKey.get(key);
    if (hit) return hit;
    return { modelId: key, mid: key, isActive: false, listed: false };
  };

  const confirmBoundModels = async () => {
    if (!boundEditor) return;
    const channel = channels.find((c) => c.id === boundEditor.id);
    if (!channel) {
      setBoundEditor(null);
      return;
    }
    const original = channelBoundModels(channel);
    const remove = new Set(boundEditor.checked);
    const next = original.filter((k) => !remove.has(k));
    if (remove.size === 0 || next.length === original.length) {
      setBoundEditor(null);
      return;
    }
    setBoundSaving(true);
    try {
      const saved: any = await request.put(`/channels/${boundEditor.id}`, { models: next });
      const updated = (saved?.data || saved) as Channel | undefined;
      setChannels((prev) =>
        prev.map((c) =>
          c.id === boundEditor.id
            ? { ...c, ...(updated?.id ? updated : {}), models: next }
            : c,
        ),
      );
      message.success(`已解除绑定 ${remove.size} 个模型`);
      setBoundEditor(null);
    } catch (e) {
      console.error(e);
    } finally {
      setBoundSaving(false);
    }
  };

  const toggleBoundCheck = (channelId: number, key: string) => {
    setBoundEditor((prev) => {
      if (!prev || prev.id !== channelId) return prev;
      const on = prev.checked.includes(key);
      return { ...prev, checked: on ? prev.checked.filter((k) => k !== key) : [...prev.checked, key] };
    });
  };

  const renderBoundModels = (record: Channel, compact = false) => {
    const savedKeys = channelBoundModels(record);
    const count = savedKeys.length;
    const tagStyle: React.CSSProperties = compact
      ? { margin: 0, padding: '0 5px', fontSize: 10, lineHeight: '18px', borderRadius: 4, cursor: count ? 'pointer' : 'default' }
      : { margin: 0, padding: '0 6px', fontSize: 11, height: 20, lineHeight: '18px', borderRadius: 4, cursor: count ? 'pointer' : 'default' };
    if (count === 0) {
      if (compact) return null;
      return <Text type="secondary" style={{ fontSize: 12 }}>未绑定</Text>;
    }
    const editing = boundEditor?.id === record.id;
    const listKeys = editing ? boundEditor.keys : savedKeys;
    const checkedKeys = editing ? boundEditor.checked : [];
    const checkedCount = checkedKeys.length;
    return (
      <Popover
        open={editing}
        onOpenChange={(open) => {
          if (boundSaving) return;
          if (open) setBoundEditor({ id: record.id, keys: [...savedKeys], checked: [] });
          else setBoundEditor(null);
        }}
        title={
          <div style={{ fontSize: 12, fontWeight: 600 }}>
            已绑定模型 ({listKeys.length})
          </div>
        }
        content={
          <div
            style={{ minWidth: 280, maxWidth: 380 }}
            onClick={(e) => e.stopPropagation()}
          >
            <div style={{ maxHeight: 220, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 4 }}>
              {listKeys.map((key) => {
                const item = resolveBoundModel(key);
                const midText = item.mid && item.mid !== item.modelId ? item.mid : '';
                const checked = checkedKeys.includes(key);
                return (
                  <div
                    key={key}
                    onClick={() => toggleBoundCheck(record.id, key)}
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: 8,
                      padding: '3px 6px',
                      borderRadius: 4,
                      cursor: 'pointer',
                      background: checked
                        ? (isLight ? 'rgba(255,77,79,0.06)' : 'rgba(255,77,79,0.12)')
                        : (isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)'),
                      border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.08)',
                    }}
                  >
                    <Checkbox
                      checked={checked}
                      disabled={boundSaving}
                      onClick={(e) => e.stopPropagation()}
                      onChange={() => toggleBoundCheck(record.id, key)}
                    />
                    <span
                      style={{
                        flex: 1,
                        minWidth: 0,
                        fontSize: 12,
                        lineHeight: '18px',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                        color: item.listed && item.isActive ? undefined : 'var(--text-secondary, #8c8c8c)',
                      }}
                      title={midText ? `${item.modelId}  ${midText}` : item.modelId}
                    >
                      {item.modelId}
                      {midText ? (
                        <span style={{ marginLeft: 8, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', fontSize: 11, opacity: 0.65 }}>
                          {midText}
                        </span>
                      ) : null}
                    </span>
                    {!item.listed ? (
                      <Tag style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 2, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#71717a' : '#a1a1aa', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>
                        已下架
                      </Tag>
                    ) : !item.isActive ? (
                      <Tag style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 2, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#71717a' : '#a1a1aa', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>
                        已禁用
                      </Tag>
                    ) : null}
                  </div>
                );
              })}
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end', alignItems: 'center', gap: 8, marginTop: 8, paddingTop: 8, borderTop: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.08)' }}>
              <Button
                size="small"
                disabled={boundSaving}
                onClick={() => setBoundEditor(null)}
              >
                取消
              </Button>
              <Button
                type="primary"
                danger
                size="small"
                icon={<DeleteOutlined />}
                loading={boundSaving}
                disabled={!checkedCount || boundSaving}
                onClick={() => { void confirmBoundModels(); }}
              >
                删除{checkedCount > 0 ? ` ${checkedCount}` : ''}
              </Button>
            </div>
          </div>
        }
        placement="bottom"
        trigger="click"
      >
        <Tag style={{
          ...tagStyle,
          background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)',
          color: isLight ? '#18181b' : '#f4f4f5',
          border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)',
          fontWeight: 500
        }}>
          {compact ? `${count} 模型` : `${count} 个模型`}
        </Tag>
      </Popover>
    );
  };

  const fetchPluginsAndPools = async () => {
    try {
      const resp = await (request.get('/plugins') as unknown as Promise<{ plugins: any[] }>);
      const plugins = resp.plugins || [];
      const activeMap: Record<string, boolean> = {};
      let hasVolcengineEnhance = false;
      let hasComfyui = false;

      plugins.forEach(p => {
        if (p.is_enabled === 1) {
          activeMap[p.name] = true;
          if (p.name === 'volcengine_enhance') hasVolcengineEnhance = true;
          if (p.name === 'comfyui_bridge') hasComfyui = true;
        }
      });
      setActivePlugins(activeMap);

      if (hasVolcengineEnhance) {
        request.get('/plugins/volcengine_enhance/volcengine-enhance-config').then((r: any) => {
          if (r && r.keys) {
            setVolcengineEnhanceKeys(r.keys);
          }
        }).catch(() => {});
      }
      if (hasComfyui) {
        request.get('/plugins/comfyui_bridge/servers').then((r: any) => {
          setComfyuiServers(r?.servers || []);
        }).catch(() => {});
        request.get('/plugins/comfyui_bridge/dispatch-rules').then((r: any) => {
          setComfyuiDispatchRules(r?.rules || []);
        }).catch(() => {});
      }
    } catch (e) {
      console.error(e);
    }
  };


  // 批量加载所有 HA 渠道的熔断状态
  const fetchAllMeltdownStatus = async (channelList?: Channel[]) => {
    const list = channelList || channels;
    const haChannels = list.filter(c => c.provider_type === 'high_availability_group');
    if (haChannels.length === 0) return;
    const results: Record<number, any> = {};
    await Promise.allSettled(
      haChannels.map(async (ch) => {
        try {
          const resp = await request.get(`/channels/${ch.id}/meltdown`) as any;
          results[ch.id] = resp;
        } catch { /* ignore */ }
      })
    );
    setMeltdownMap(prev => ({ ...prev, ...results }));
  };

  // 手动重置单个渠道的熔断
  const handleResetMeltdown = async (channelId: number) => {
    setMeltdownLoading(prev => ({ ...prev, [channelId]: true }));
    try {
      const resp = await request.post(`/channels/${channelId}/meltdown/reset`) as any;
      message.success(`已重置熔断状态，清除了 ${resp.cleared_count || 0} 条记录`);
      // 刷新该渠道的熔断状态
      try {
        const updated = await request.get(`/channels/${channelId}/meltdown`) as any;
        setMeltdownMap(prev => ({ ...prev, [channelId]: updated }));
      } catch { /* ignore */ }
    } catch (e) {
      console.error(e);
    } finally {
      setMeltdownLoading(prev => ({ ...prev, [channelId]: false }));
    }
  };

  const refreshChannelMeltdown = async (channelId: number) => {
    try {
      const updated = await request.get(`/channels/${channelId}/meltdown`) as any;
      setMeltdownMap(prev => ({ ...prev, [channelId]: updated }));
    } catch { /* ignore */ }
  };

  const handleToggleSubChannelStatus = async (
    channelId: number | undefined,
    configId: number,
    enabled: boolean,
  ) => {
    const targetKey = String(configId);
    const updateOverridesMap = (baseCfg: Record<string, any>) => {
      const nextOverrides: Record<string, Record<string, number>> = {
        ...(baseCfg.upstream_overrides || {}),
      };
      const entry: Record<string, number> = {
        ...(nextOverrides[targetKey] || {}),
      };
      if (enabled) {
        delete entry.status;
      } else {
        entry.status = 0;
      }
      if (Object.keys(entry).length > 0) {
        nextOverrides[targetKey] = entry;
      } else {
        delete nextOverrides[targetKey];
      }
      const nextCfg: Record<string, any> = { ...baseCfg };
      if (Object.keys(nextOverrides).length > 0) {
        nextCfg.upstream_overrides = nextOverrides;
      } else {
        delete nextCfg.upstream_overrides;
      }
      return nextCfg;
    };

    if (!channelId || editingChannel?.id === channelId) {
      setConfigObj((prev) => updateOverridesMap(prev));
    }

    if (!channelId) {
      message.success(
        enabled
          ? '已在当前渠道分组启用该上游（保存后生效）'
          : '已在当前渠道分组关闭该上游（不参与调用，保存后生效）',
      );
      return;
    }

    const loadKey = `${channelId}-${configId}`;
    setSubMeltLoading((prev) => ({ ...prev, [loadKey]: true }));
    try {
      const targetChannel =
        editingChannel?.id === channelId
          ? editingChannel
          : channels.find((c) => c.id === channelId);
      if (!targetChannel) return;
      const basePersisted =
        editingChannel?.id === channelId
          ? { ...parseChannelConfig(editingChannel), ...configObj }
          : parseChannelConfig(targetChannel);
      const persistedConfig = updateOverridesMap(basePersisted);

      const saved: any = await request.put(`/channels/${channelId}`, {
        config: persistedConfig,
      });
      const updatedChannel = (saved?.data || saved) as Channel;
      if (updatedChannel?.id) {
        setChannels((prev) =>
          prev.map((c) => (c.id === updatedChannel.id ? { ...c, ...updatedChannel } : c)),
        );
        if (editingChannel?.id === updatedChannel.id) {
          setEditingChannel((prev) => (prev ? { ...prev, ...updatedChannel } : prev));
        }
      }
      message.success(
        enabled
          ? '已在当前渠道分组启用该上游（熔断遵循高可用策略）'
          : '已在当前渠道分组关闭该上游（不参与上游调用）',
      );
      await refreshChannelMeltdown(channelId);
    } catch (e) {
      console.error(e);
    } finally {
      setSubMeltLoading((prev) => ({ ...prev, [loadKey]: false }));
    }
  };

  // 手动清零渠道已用额度（总/日/月）
  const handleResetQuota = async (channelId: number) => {
    try {
      await request.post(`/channels/${channelId}/quota/reset`);
      message.success('已清零渠道已用额度');
      fetchChannels();
    } catch (e) {
      console.error(e);
    }
  };

  useEffect(() => {
    fetchChannels();
    fetchModels();
    fetchUnlistedModels();
    fetchUserLevels();
    fetchPresets();
    fetchCategories();
    fetchPluginsAndPools();
    fetchHaRules();
  }, []);

  // 渠道列表加载后自动获取 HA 渠道熔断状态
  useEffect(() => {
    if (channels.length > 0) {
      fetchAllMeltdownStatus(channels);
    }
  }, [channels]);

  // 编辑高可用组时，熔断倒计时每秒递减
  useEffect(() => {
    if (!editingChannel || editingChannel.provider_type !== 'high_availability_group') return;
    const channelId = editingChannel.id;
    const timer = setInterval(() => {
      setMeltdownMap(prev => {
        const cur = prev[channelId];
        const subs = cur?.sub_channels;
        if (!subs?.some((s: any) => s.is_melted && (s.remaining_seconds || 0) > 0)) return prev;
        return {
          ...prev,
          [channelId]: {
            ...cur,
            sub_channels: subs.map((s: any) => {
              if (!s.is_melted || (s.remaining_seconds || 0) <= 0) return s;
              const remaining = Math.max(0, (s.remaining_seconds || 0) - 1);
              return remaining <= 0 && !s.is_demoted
                ? { ...s, is_melted: false, remaining_seconds: 0 }
                : { ...s, remaining_seconds: remaining };
            }),
          },
        };
      });
    }, 1000);
    return () => clearInterval(timer);
  }, [editingChannel?.id, editingChannel?.provider_type]);

  // 选中的渠道类型在列表中已不存在时，回退到「全部」
  useEffect(() => {
    if (typeFilter === 'all') return;
    const stillExists = channels.some((ch) => getChannelTypeKey(ch) === typeFilter);
    if (!stillExists) setTypeFilter('all');
  }, [channels, typeFilter]);

  const loadSiteStorage = async () => {
    try {
      const res: any = await request.get('/settings/full');
      setSiteStorage(res?.storage && typeof res.storage === 'object' ? res.storage : null);
    } catch {
      setSiteStorage(null);
    }
  };

  const initNewChannelForm = () => {
    setEditingChannel(null);
    setEnableQuota(false);
    form.resetFields();
    setConfigObj({});
    setSelectedSubChannelAids([]);
    setShowMapping(false);
    setActiveMappingInputs([]);
    setModelMappingState({});
    setHaModelMappingState({});
    setResModelMappingState({});
    setExpandedHaModels([]);
    setExpandedResScopes([]);
    setCustomResScopes({});
    setIsExcludeMode(false);
    setChannelModelMids([]);
    setActiveRightPanel('models');
    setPresetSearchText('');
    setUpstreamStatusFilter('all');
    setUpstreamTab('preset');
    modelsRef.current = [];
    levelsRef.current = [];
    const stateCategoryId = (location.state as { categoryId?: number | null } | null)?.categoryId;
    const defaultCategoryId = typeof stateCategoryId === 'number'
      ? stateCategoryId
      : (typeof categoryFilter === 'number' ? categoryFilter : null);
    categoryIdRef.current = defaultCategoryId;
    void loadSiteStorage();

    setTimeout(() => {
      form.setFieldsValue({
        name: '',
        provider_type: 'custom',
        base_url: '',
        api_key: '',
        sort_order: 0,
        category_id: defaultCategoryId,
        priority: 0,
        status: 1,
        weight: 1,
        rate: 1.0,
        max_rps: 0,
        quota_limit: -1,
        quota_used: 0,
        daily_quota_limit: -1,
        weekly_quota_limit: -1,
        monthly_quota_limit: -1,
        preset_id: null,
        model_mapping: {},
        models: [],
        level_select: [],
      });
    }, 0);
  };

  const handleAdd = () => {
    navigate(channelNewPath(adminPath), {
      state: { categoryId: typeof categoryFilter === 'number' ? categoryFilter : null },
    });
  };

  useEffect(() => {
    if (!isNewRoute) {
      newFormReadyRef.current = false;
      return;
    }
    if (newFormReadyRef.current) return;
    newFormReadyRef.current = true;
    initNewChannelForm();
  }, [isNewRoute]);

  const hydrateChannelEditor = (record: Channel) => {
    setEditingChannel(record);
    const isHaRecord = record.provider_type === 'high_availability_group';
    if (isHaRecord) {
      void refreshChannelMeltdown(record.id);
    }
    let mapping: Record<string, string> = {};
    if (!isHaRecord) {
      try {
        mapping = typeof record.model_mapping === 'string' ? JSON.parse(record.model_mapping) : (record.model_mapping || {});
      } catch (e) {}
    }

    // models 兼容处理：新格式存 mid，旧格式存 model_id，需要统一转为 mid
    const rawModels = Array.isArray(record.models) ? record.models : [];
    const modelsForForm = rawModels.map((val: string) => {
      const key = String(val);
      if (availableModels.find(m => String(m.mid) === key)) return key;
      const match = availableModels.find(m => String(m.model_id) === key);
      return match ? String(match.mid) : key;
    });
    const mappingBySel = rekeyMappingToSelection(mapping, modelsForForm, availableModels);

    const rawLevelIds = (record.exclude_user_groups && record.exclude_user_groups.length > 0)
      ? record.exclude_user_groups
      : (record.user_groups || []);
    const levelIds = normalizeLevelIds(rawLevelIds, availableUserLevels);

    // Sync refs (reliable source of truth for save)
    modelsRef.current = modelsForForm;
    levelsRef.current = levelIds;
    categoryIdRef.current = record.category_id || null;
    setChannelModelMids(modelsForForm);

    const hasMapping = !isHaRecord && Object.values(mapping).some(v => v && String(v).trim());
    setShowMapping(hasMapping);
    const q = record.quota_limit ?? -1;
    const dq = record.daily_quota_limit ?? -1;
    const wq = record.weekly_quota_limit ?? -1;
    const mq = record.monthly_quota_limit ?? -1;
    setEnableQuota(q >= 0 || dq >= 0 || wq >= 0 || mq >= 0);
    setModelMappingState(mappingBySel);
    setActiveMappingInputs(Object.keys(mappingBySel).filter(k => mappingBySel[k] && String(mappingBySel[k]).trim()));
    setIsExcludeMode(!!record.exclude_user_groups && record.exclude_user_groups.length > 0);
    setActiveRightPanel('models');
    setPresetSearchText('');
    setUpstreamStatusFilter('all');
    if (record.preset_id) {
      setUpstreamTab('preset');
    } else if (record.provider_type === 'volcengine') {
      setUpstreamTab('volcengine_enhance');
    } else if (record.provider_type === 'comfyui') {
      setUpstreamTab('comfyui');
    } else {
      setUpstreamTab('preset');
    }


    // 解析 config JSON 初始化存储设置状态
    let parsedConfig: Record<string, any> = {};
    try {
      parsedConfig = record.config
        ? (typeof record.config === 'string' ? JSON.parse(record.config) : record.config)
        : {};
    } catch { parsedConfig = {}; }
    setConfigObj(parsedConfig);
    const subAids = parsedConfig.sub_channels || [];
    const subAidNums = subAids.map((id: any) => Number(id));
    setSelectedSubChannelAids(subAids);
    void loadSiteStorage();

    // 恢复高可用子渠道独立映射（必须在 parsedConfig 解析之后，仅保留当前仍绑定子渠道）
    const rawHaMapping: Record<string, Record<string, string>> = {};
    if (isHaRecord && parsedConfig?.ha_model_mapping && typeof parsedConfig.ha_model_mapping === 'object') {
      for (const [mKey, subMap] of Object.entries(parsedConfig.ha_model_mapping as Record<string, Record<string, string>>)) {
        const validSubs: Record<string, string> = {};
        for (const [sid, alias] of Object.entries(subMap || {})) {
          if (subAidNums.includes(Number(sid)) && isFilled(alias)) {
            validSubs[sid] = String(alias).trim();
          }
        }
        if (Object.keys(validSubs).length > 0) {
          rawHaMapping[mKey] = validSubs;
        }
      }
    }
    const haMapping = rekeyMappingToSelection<Record<string, string>>(
      rawHaMapping,
      modelsForForm,
      availableModels,
    );
    setHaModelMappingState(haMapping);
    // 恢复分辨率映射（读入时按当前模式规范化 key 与作用域，与后端命中规则一致），再按已选 mid 回挂
    const resMapping = rekeyMappingToSelection(
      cleanResModelMapping(parsedConfig?.res_model_mapping, isHaRecord ? subAidNums : [], isHaRecord),
      modelsForForm,
      availableModels,
    );
    setResModelMappingState(resMapping);
    // 自动展开：有明文 HA 映射或分辨率映射的模型 + 已配置的分辨率高级面板
    const resExpanded = Object.entries(resMapping).flatMap(([mid, scopes]) =>
      Object.entries(scopes || {})
        .filter(([, map]) => countFilled(map) > 0)
        .map(([scope]) => resScopeKey(mid, scope)),
    );
    setExpandedResScopes(resExpanded);
    setCustomResScopes({});
    setExpandedHaModels(Array.from(new Set([
      ...Object.keys(haMapping).filter(k => countFilled(haMapping[k]) > 0),
      ...Object.keys(resMapping).filter(mid =>
        Object.values(resMapping[mid] || {}).some(m => countFilled(m) > 0),
      ),
    ])));
    const resConfiguredModelIds = !isHaRecord
      ? Object.keys(resMapping).filter(mid => countFilled(resMapping[mid]?.default) > 0)
      : [];
    if (resConfiguredModelIds.length > 0) {
      setActiveMappingInputs(prev => Array.from(new Set([...prev, ...resConfiguredModelIds])));
    }
    if (resExpanded.length > 0 || (isHaRecord && Object.values(haMapping).some(sub => countFilled(sub as Record<string, unknown>) > 0))) {
      setShowMapping(true);
    }

    const singlePreset =
      record.provider_type !== 'high_availability_group' && record.preset_id
        ? presets.find((p) => Number(p.id) === Number(record.preset_id))
        : null;
    const effSched = singlePreset ? getEffectiveUpstreamSchedule(singlePreset, parsedConfig) : null;

    setTimeout(() => {
      form.setFieldsValue({
        name: record.name,
        provider_type: record.provider_type || 'custom',
        base_url: record.base_url,
        api_key: (record as any).api_key || '',
        sort_order: record.sort_order || 0,
        category_id: record.category_id || null,
        priority: effSched ? effSched.priority : (record.priority ?? 0),
        status: record.status ?? 1,
        weight: effSched ? effSched.weight : (record.weight ?? 1),
        rate: effSched ? effSched.rate : (record.rate ?? 1.0),
        max_rps: (record as any).max_rps || 0,
        quota_limit: record.quota_limit ?? -1,
        quota_used: record.quota_used || 0,
        daily_quota_limit: record.daily_quota_limit ?? -1,
        weekly_quota_limit: record.weekly_quota_limit ?? -1,
        monthly_quota_limit: record.monthly_quota_limit ?? -1,
        preset_id: record.preset_id || null,
        model_mapping: mapping,
        models: modelsForForm,
        level_select: levelIds,
      });
    }, 0);
  };

  const handleEdit = (record: Channel) => {
    navigate(channelEditPath(adminPath, record.id));
  };

  useEffect(() => {
    if (!routeEditId) {
      if (!keepEditorIdRef.current) hydratedEditIdRef.current = null;
      return;
    }
    if (keepEditorIdRef.current === routeEditId || hydratedEditIdRef.current === routeEditId) {
      hydratedEditIdRef.current = routeEditId;
      keepEditorIdRef.current = null;
      return;
    }
    if (channels.length === 0 || availableModels.length === 0) return;
    const channelId = parseInt(routeEditId, 10);
    const ch = channels.find(c => c.id === channelId);
    if (ch) {
      hydratedEditIdRef.current = routeEditId;
      hydrateChannelEditor(ch);
    } else if (!loading) {
      message.error('渠道并未找到，请检查！');
      navigate(channelListPath(adminPath));
    }
  }, [channels, availableModels, routeEditId, loading]);

  useEffect(() => {
    if (!editingChannel || availableUserLevels.length === 0) return;
    const current: string[] = form.getFieldValue('level_select') || levelsRef.current || [];
    if (current.length === 0) return;
    const normalized = normalizeLevelIds(current, availableUserLevels);
    if (normalized.length !== current.length || normalized.some((v, idx) => v !== current[idx])) {
      levelsRef.current = normalized;
      form.setFieldsValue({ level_select: normalized });
    }
  }, [availableUserLevels, editingChannel?.id]);

  const handleDelete = async (id: number) => {
    try {
      await request.delete(`/channels/${id}`);
      message.success(t('common.success'));
      fetchChannels();
    } catch (e) {
      console.error(e);
    }
  };

  const handleToggleStatus = async (record: Channel) => {
    try {
      const newStatus = record.status === 1 ? 0 : 1;
      if (newStatus === 1 && record.provider_type === 'high_availability_group' && !activePlugins['high_availability_channel']) {
        message.warning('高可用上游渠道系统插件未开启，无法启用此渠道');
        return;
      }
      await request.put(`/channels/${record.id}`, { status: newStatus });
      setChannels(prev => prev.map(c => c.id === record.id ? { ...c, status: newStatus } : c));
      message.success(newStatus === 1 ? '已启用渠道' : '已禁用渠道');
    } catch (e) {
      console.error(e);
    }
  };

  const [isBatchEditMode, setIsBatchEditMode] = useState(false);
  const [selectedRowKeys, setSelectedRowKeys] = useState<React.Key[]>([]);
  const [batchLoading, setBatchLoading] = useState(false);
  const [batchCategoryKey, setBatchCategoryKey] = useState(0);

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
    const targets = channels.filter(c => idSet.has(c.id));
    if (targets.length === 0) return;
    const skipped = status === 1
      ? targets.filter(c => c.provider_type === 'high_availability_group' && !activePlugins['high_availability_channel'])
      : [];
    const skippedIds = new Set(skipped.map(c => c.id));
    const actionable = targets.filter(c => !skippedIds.has(c.id));
    if (actionable.length === 0) {
      message.warning('高可用上游渠道系统插件未开启，无法启用所选渠道');
      return;
    }
    setBatchLoading(true);
    try {
      await Promise.all(actionable.map(c => request.put(`/channels/${c.id}`, { status })));
      const done = new Set(actionable.map(c => c.id));
      setChannels(prev => prev.map(c => done.has(c.id) ? { ...c, status } : c));
      message.success(status === 1 ? `已激活 ${actionable.length} 个渠道分组` : `已禁用 ${actionable.length} 个渠道分组`);
      if (skipped.length > 0) {
        message.warning(`已跳过 ${skipped.length} 个高可用渠道（插件未开启）`);
      }
    } catch (e) {
      console.error(e);
      fetchChannels();
    } finally {
      setBatchLoading(false);
    }
  };

  const handleBatchChangeCategory = async (categoryId: number | null) => {
    const idSet = new Set(selectedRowKeys.map(k => Number(k)));
    const targets = channels.filter(c => idSet.has(c.id));
    if (targets.length === 0) return;
    setBatchLoading(true);
    try {
      await Promise.all(targets.map(c => request.put(`/channels/${c.id}`, { category_id: categoryId })));
      const done = new Set(targets.map(c => c.id));
      setChannels(prev => prev.map(c => done.has(c.id) ? { ...c, category_id: categoryId } : c));
      const name = categoryId == null ? '未分类' : (categories.find(c => c.id === categoryId)?.name || '所选分类');
      message.success(`已将 ${targets.length} 个渠道分组改为「${name}」`);
    } catch (e) {
      console.error(e);
      fetchChannels();
    } finally {
      setBatchLoading(false);
    }
  };

  const handleBatchDelete = async () => {
    const ids = channels.filter(c => batchChecked(c.id)).map(c => c.id);
    if (ids.length === 0) return;
    setBatchLoading(true);
    try {
      await Promise.all(ids.map(id => request.delete(`/channels/${id}`)));
      message.success(`已删除 ${ids.length} 个渠道分组`);
      setSelectedRowKeys([]);
      setChannels(prev => prev.filter(c => !ids.includes(c.id)));
    } catch (e) {
      console.error(e);
      fetchChannels();
    } finally {
      setBatchLoading(false);
    }
  };

  const handleTest = (record: Channel) => {
    navigate(channelAnalysisPath(adminPath, record.id));
  };

  const handleOpenEditorAnalysis = async (e?: React.MouseEvent) => {
    e?.stopPropagation();
    e?.preventDefault();
    if (submitting) return;
    let values: any;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    setSubmitting(true);
    try {
      const saved = await performSaveChannel(values);
      if (!saved?.id) return;
      message.success(t('common.success'));
      setChannels(prev => {
        const exists = prev.some(c => c.id === saved.id);
        return exists ? prev.map(c => c.id === saved.id ? { ...c, ...saved } : c) : [saved, ...prev];
      });
      setEditingChannel(saved);
      hydratedEditIdRef.current = String(saved.id);
      keepEditorIdRef.current = String(saved.id);
      navigate(channelAnalysisPath(adminPath, saved.id, 'edit'), {
        state: { from: 'edit', origin: (location.state as { from?: string } | null)?.from },
      });
    } finally {
      setSubmitting(false);
    }
  };

  const handleModelsChange = (nextModels: string[]) => {
    const nextSet = new Set(nextModels.map(String));
    const removed = new Set(modelsRef.current.map(String).filter(id => !nextSet.has(id)));
    for (const mid of [...removed]) {
      const modelId = selectionModelId(availableModels, mid);
      if (modelId !== mid) removed.add(modelId);
    }

    form.setFieldsValue({ models: nextModels });
    modelsRef.current = nextModels;
    setChannelModelMids(nextModels);

    if (removed.size === 0) return;

    // 只删掉这次取消勾选的模型，模型请求 ID 变化不会把仍选中的别名清掉
    setModelMappingState(prev => dropRecordKeys(prev, removed));
    setActiveMappingInputs(prev => prev.filter(id => !removed.has(id)));
    setHaModelMappingState(prev => dropRecordKeys(prev, removed));
    setResModelMappingState(prev => dropRecordKeys(prev, removed));
    setExpandedHaModels(prev => prev.filter(id => !removed.has(id)));
    setExpandedResScopes(prev => prev.filter(k => !removed.has(k.split(':')[0])));
  };

  const performSaveChannel = async (values: any): Promise<Channel | null> => {
    const isHaGroup = values.provider_type === 'high_availability_group';
    const finalMapping: Record<string, string> = {};
    if (!isHaGroup && showMapping) {
      const currentModelMapping = rekeyMappingToModelId(modelMappingState, availableModels, modelsRef.current);
      if (currentModelMapping) {
        for (const [k, v] of Object.entries(currentModelMapping)) {
          if (v && String(v).trim()) {
            finalMapping[k] = String(v).trim();
          }
        }
      }
    }

    // Read models and levels from refs (immune to form store corruption)
    const reliableModels = modelsRef.current;
    const reliableLevels = levelsRef.current;
    const reliableCategoryId = categoryIdRef.current;

    const name = String(values.name || '').trim();
    if (!name) {
      message.error('请填写渠道名称');
      return null;
    }

    const comfyServerIds = parseComfyuiServerIds(configObj);
    const comfyServerId = comfyServerIds[0];
    const hasVolcCred = Boolean(
      configObj.volcengine_enhance_credential_id &&
        String(configObj.volcengine_enhance_credential_id).trim(),
    );

    // Ensure only one upstream is used and others are explicitly cleared
    let { preset_id } = values;
    const isVolcEnhance =
      (upstreamTab === 'volcengine_enhance' || (!preset_id && values.provider_type === 'volcengine')) &&
      hasVolcCred;
    const isComfyui =
      (upstreamTab === 'comfyui' || (!preset_id && values.provider_type === 'comfyui')) &&
      comfyServerIds.length > 0;
    if (isHaGroup || isVolcEnhance || isComfyui) {
      preset_id = null;
    }

    // 校验必须选中上游渠道
    const hasUpstream = isHaGroup
      ? selectedSubChannelAids.length > 0
      : upstreamTab === 'volcengine_enhance'
        ? hasVolcCred
        : upstreamTab === 'comfyui'
          ? comfyServerIds.length > 0
          : isVolcEnhance || isComfyui || Boolean(preset_id && Number(preset_id) > 0);

    if (!hasUpstream) {
      message.error('请选择上游渠道');
      return null;
    }

    // 构建 HA 子渠道独立映射（只保留有实际值的条目）
    let finalHaModelMapping: Record<string, Record<string, string>> | undefined;
    if (isHaGroup && showMapping) {
      const cleaned: Record<string, Record<string, string>> = {};
      for (const [modelId, subMap] of Object.entries(rekeyMappingToModelId(haModelMappingState, availableModels, modelsRef.current))) {
        const validEntries: Record<string, string> = {};
        for (const [subId, alias] of Object.entries(subMap)) {
          if (alias && String(alias).trim() && selectedSubChannelAids.includes(Number(subId))) {
            validEntries[subId] = String(alias).trim();
          }
        }
        if (Object.keys(validEntries).length > 0) {
          cleaned[modelId] = validEntries;
        }
      }
      if (Object.keys(cleaned).length > 0) {
        finalHaModelMapping = cleaned;
      }
    }

    // 构建分辨率映射（HA 仅保留仍绑定的子渠作用域；非 HA 仅保留 default 作用域）
    let finalResModelMapping: ResModelMapping | undefined;
    if (showMapping) {
      const cleaned = cleanResModelMapping(
        rekeyMappingToModelId(resModelMappingState, availableModels, modelsRef.current),
        isHaGroup ? selectedSubChannelAids.map(Number) : [],
        isHaGroup,
      );
      if (Object.keys(cleaned).length > 0) {
        finalResModelMapping = cleaned;
      }
    }

    // 清理并构建分组内独立上游调度覆盖参数 (upstream_overrides，优先级、权重、本组启用状态与 RPM 限制)
    const cleanedUpstreamOverrides: Record<string, Record<string, number>> = {};
    const singlePresetTarget = !isHaGroup && preset_id ? presets.find((p) => Number(p.id) === Number(preset_id)) : null;
    if (isHaGroup) {
      for (const tid of selectedSubChannelAids) {
        const targetPreset = presets.find((p) => Number(p.id) === Number(tid));
        if (!targetPreset) continue;
        const sched = getEffectiveUpstreamSchedule(targetPreset, configObj);
        if (sched.hasCustom) {
          const entry: Record<string, number> = {};
          if (sched.customPriority !== undefined) entry.priority = sched.customPriority;
          if (sched.customWeight !== undefined) entry.weight = sched.customWeight;
          if (sched.customRpmLimit !== undefined && sched.customRpmLimit > 0) entry.rpm_limit = sched.customRpmLimit;
          if (sched.customStatus === 0) entry.status = 0;
          if (Object.keys(entry).length > 0) cleanedUpstreamOverrides[String(tid)] = entry;
        }
      }
    } else if (singlePresetTarget) {
      const entry: Record<string, number> = {};
      const numP = Number(values.priority ?? 0);
      const numW = Math.max(1, Number(values.weight ?? 1));
      if (numP !== Number(singlePresetTarget.priority ?? 0)) entry.priority = numP;
      if (numW !== Math.max(1, Number(singlePresetTarget.weight ?? 1))) entry.weight = numW;
      if (Object.keys(entry).length > 0) cleanedUpstreamOverrides[String(preset_id)] = entry;
    }
    const hasUpstreamOverrides = Object.keys(cleanedUpstreamOverrides).length > 0;

    const tosProvider =
      configObj.tos_storage_enabled && multiSiteStorage && configObj.tos_storage_provider
        ? String(configObj.tos_storage_provider)
        : undefined;

    const finalConfig = isHaGroup 
      ? {
          ...configObj,
          tos_storage_provider: tosProvider,
          sub_channels: selectedSubChannelAids,
          volcengine_enhance_credential_id: undefined,
          comfyui_workflow_id: undefined,
          comfyui_server_id: undefined,
          comfyui_server_ids: undefined,
          comfyui_dispatch: undefined,
          ...(finalHaModelMapping ? { ha_model_mapping: finalHaModelMapping } : { ha_model_mapping: undefined }),
          ...(finalResModelMapping ? { res_model_mapping: finalResModelMapping } : { res_model_mapping: undefined }),
          ...(hasUpstreamOverrides ? { upstream_overrides: cleanedUpstreamOverrides } : { upstream_overrides: undefined }),
        }
      : {
          tos_storage_enabled: configObj.tos_storage_enabled,
          tos_storage_days: configObj.tos_storage_days,
          tos_storage_b64_enabled: configObj.tos_storage_b64_enabled !== false,
          ...(tosProvider ? { tos_storage_provider: tosProvider } : {}),
          // 媒体封装/画质增强凭证关联：通过凭证 ID 实时查询最新密钥，保证数据一致性
          ...((isVolcEnhance || (values.provider_type === 'volcengine' && configObj.volcengine_enhance_credential_id))
            ? { volcengine_enhance_credential_id: configObj.volcengine_enhance_credential_id }
            : {}),
          ...(isComfyui && comfyServerId
            ? {
                comfyui_server_id: comfyServerId,
                comfyui_server_ids: comfyServerIds,
                comfyui_dispatch: configObj.comfyui_dispatch || COMFYUI_DISPATCH_DEFAULT,
              }
            : {}),
          ...(finalResModelMapping ? { res_model_mapping: finalResModelMapping } : {}),
          ...(hasUpstreamOverrides ? { upstream_overrides: cleanedUpstreamOverrides } : {}),
        };

    const data = {
      ...values,
      name,
      models: reliableModels,
      provider_type: isHaGroup
        ? 'high_availability_group'
        : isVolcEnhance
          ? 'volcengine'
          : isComfyui
            ? 'comfyui'
            : (values.provider_type || 'custom'),
      model_mapping: finalMapping,
      user_groups: isExcludeMode ? [] : reliableLevels,
      exclude_user_groups: isExcludeMode ? reliableLevels : [],
      config: finalConfig,
      sort_order: values.sort_order || 0,
      category_id: reliableCategoryId,
      priority: values.priority || 0,
      weight: values.weight || 1,
      rate: singlePresetTarget ? Number(singlePresetTarget.rate ?? 1.0) : (typeof values.rate === 'number' ? values.rate : 1.0),
      quota_limit: (!enableQuota || values.quota_limit === undefined || values.quota_limit === null) ? -1 : Number(values.quota_limit),
      daily_quota_limit: (!enableQuota || values.daily_quota_limit === undefined || values.daily_quota_limit === null) ? -1 : Number(values.daily_quota_limit),
      weekly_quota_limit: (!enableQuota || values.weekly_quota_limit === undefined || values.weekly_quota_limit === null) ? -1 : Number(values.weekly_quota_limit),
      monthly_quota_limit: (!enableQuota || values.monthly_quota_limit === undefined || values.monthly_quota_limit === null) ? -1 : Number(values.monthly_quota_limit),
      preset_id,
    };
    if (enableQuota) {
      const hierarchyErr = validateQuotaHierarchy(data);
      if (hierarchyErr) {
        message.error(hierarchyErr);
        return null;
      }
    }
    delete data.level_select;
    data.models = reliableModels;

    // 插件上游不落库密钥/基址，选渠时按凭证或服务节点实时覆盖
    if (data.provider_type === 'volcengine' || data.provider_type === 'comfyui') {
      data.api_key = '';
      data.base_url = '';
    }

    try {
      let saved: any;
      if (editingChannel) {
        // 密钥未修改（与加载时原值相同）或为空时不提交，防止覆盖（插件上游例外：必须清空）
        if (data.provider_type !== 'volcengine' && data.provider_type !== 'comfyui' && (!data.api_key || data.api_key === (editingChannel as any).api_key)) {
          delete data.api_key;
        }
        saved = await request.put(`/channels/${editingChannel.id}`, data);
      } else {
        saved = await request.post('/channels', data);
      }
      return (saved?.data || saved) as Channel;
    } catch (e) {
      console.error(e);
      return null;
    }
  };

  const handleSave = async (values: any) => {
    if (submitting) return;
    const stay = saveIntentRef.current === 'stay';
    saveIntentRef.current = 'exit';
    setSubmitting(true);
    try {
      const saved = await performSaveChannel(values);
      if (!saved?.id) return;
      message.success(t('common.success'));
      setChannels(prev => {
        const exists = prev.some(c => c.id === saved.id);
        return exists ? prev.map(c => c.id === saved.id ? { ...c, ...saved } : c) : [saved, ...prev];
      });
      if (stay) {
        setEditingChannel(saved);
        hydratedEditIdRef.current = String(saved.id);
        if (!routeEditId || String(saved.id) !== routeEditId) {
          keepEditorIdRef.current = String(saved.id);
          navigate(channelEditPath(adminPath, saved.id), { replace: true, state: location.state });
        }
        fetchChannels();
      } else {
        handleCloseModal();
        fetchChannels();
      }
    } finally {
      setSubmitting(false);
    }
  };

  const handleUpdateSortOrder = async (id: number, sort_order: number) => {
    setChannels(prev =>
      prev
        .map(c => (c.id === id ? { ...c, sort_order } : c))
        .sort(
          (a, b) =>
            (b.status ?? 1) - (a.status ?? 1) ||
            (b.sort_order ?? 0) - (a.sort_order ?? 0) ||
            (b.priority ?? 0) - (a.priority ?? 0) ||
            b.id - a.id,
        ),
    );
    try {
      await request.put(`/channels/${id}`, { sort_order });
    } catch (e) {
      console.error(e);
      fetchChannels();
    }
  };

  /** 解析渠道 config（兼容 string / object） */
  const parseChannelConfig = (record: Channel): Record<string, any> => {
    try {
      if (!record.config) return {};
      return typeof record.config === 'string' ? JSON.parse(record.config) : record.config;
    } catch {
      return {};
    }
  };

  /** 媒体封装上游：凭证名称与基址 */
  const resolveVolcEnhanceUpstream = (record: Channel) => {
    const cfg = parseChannelConfig(record);
    const credId = cfg.volcengine_enhance_credential_id;
    const cred = volcengineEnhanceKeys.find((k) => k.id === credId);
    return {
      credId,
      name: cred?.name || (credId ? `凭证 #${credId}` : '未绑定凭证'),
      baseUrl: cred?.base_url || '',
    };
  };

  const resolveComfyuiUpstream = (record: Channel) => {
    const cfg = parseChannelConfig(record);
    const ids = parseComfyuiServerIds(cfg);
    if (ids.length > 1) {
      const names = ids.map((id) => comfyuiServers.find((s) => s.id === id)?.name || `#${id}`);
      const dispatch = comfyuiDispatchRules.find((r) => r.code === cfg.comfyui_dispatch)?.name
        || cfg.comfyui_dispatch
        || '权重优先';
      return { name: `${ids.length} 个节点 · ${dispatch}`, baseUrl: names.join('、') };
    }
    const sid = ids[0];
    const server = comfyuiServers.find((s) => s.id === sid);
    if (server) {
      return { name: server.name, baseUrl: server.base_url || '' };
    }
    return {
      name: sid ? `节点 #${sid}` : '未绑定服务节点',
      baseUrl: '',
    };
  };

  const columns = [
    {
      title: '渠道分组名称',
      key: 'name_and_aid',
      width: 180,
      render: (_: any, record: Channel) => (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 1, minWidth: 120 }}>
          <Text strong style={{ fontSize: 13, lineHeight: 1.25 }}>{record.name}</Text>
          <Text type="secondary" style={{ fontSize: 11, lineHeight: 1.2, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', opacity: 0.75 }}>
            AID: {record.group_aid || '-'}
          </Text>
        </div>
      ),
    },
    {
      title: '状态',
      dataIndex: 'status',
      key: 'status',
      width: 75,
      render: (status: number) => (
        <Space size={6} style={{ color: status === 1 ? (isLight ? '#18181b' : '#f4f4f5') : (isLight ? '#a1a1aa' : '#71717a') }}>
          <div style={{ width: 6, height: 6, borderRadius: '50%', backgroundColor: status === 1 ? (isLight ? '#18181b' : '#f4f4f5') : (isLight ? '#d4d4d8' : '#3f3f46') }} />
          <span style={{ fontSize: 12, fontWeight: status === 1 ? 500 : 400 }}>{status === 1 ? '启用' : '禁用'}</span>
        </Space>
      ),
    },
    {
      title: '绑定模型',
      key: 'bound_models',
      width: 105,
      align: 'center' as const,
      sorter: (a: Channel, b: Channel) => channelBoundModels(a).length - channelBoundModels(b).length,
      render: (_: unknown, record: Channel) => renderBoundModels(record),
    },
    {
      title: '优先级',
      dataIndex: 'priority',
      key: 'priority',
      width: 75,
      sorter: (a: Channel, b: Channel) => (a.priority || 0) - (b.priority || 0),
      render: (priority: number) => <Text type="secondary" style={{ fontSize: 12 }}>{priority || 0}</Text>,
    },
    {
      title: '支持等级',
      key: 'user_groups',
      width: 150,
      render: (_: any, record: Channel) => {
        const groups = record.user_groups;
        const excludeGroups = record.exclude_user_groups;
        const tagStyle: React.CSSProperties = { borderRadius: 4, margin: 0, padding: '0 5px', fontSize: 11, height: 19, lineHeight: '17px' };
        const renderLevelTag = (id: string, isExclude: boolean) => {
          const meta = resolveUserLevelMeta(id);
          const isInvalid = !meta.exists || meta.isDisabled;
          const style: React.CSSProperties = isInvalid
            ? { ...tagStyle, background: 'transparent', color: isLight ? '#a1a1aa' : '#71717a', border: isLight ? '1px dashed #d4d4d8' : '1px dashed #3f3f46' }
            : isExclude
              ? { ...tagStyle, background: 'transparent', color: isLight ? '#71717a' : '#a1a1aa', border: isLight ? '1px dashed #d4d4d8' : '1px dashed #3f3f46' }
              : { ...tagStyle, background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)', color: isLight ? '#27272a' : '#e4e4e7', border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)' };
          const tip = !meta.exists
            ? `该用户等级已删除 (${meta.name})`
            : meta.isDisabled
              ? `${meta.name} (ULID: ${meta.ulid} · 已禁用)`
              : `${meta.name} (ULID: ${meta.ulid} · ${meta.discountText}${meta.hasLevelDisc ? '' : ' 倍率未启用'})`;
          return (
            <Tooltip key={id} title={tip}>
              <Tag style={style}>{meta.tagText}</Tag>
            </Tooltip>
          );
        };
        if (excludeGroups && excludeGroups.length > 0) {
          const visible = excludeGroups.slice(0, 2);
          const hiddenCount = excludeGroups.length - visible.length;
          const allNames = excludeGroups.map((id: string) => resolveUserLevelMeta(id).tagText).join(', ');
          return (
            <div style={{ display: 'flex', flexWrap: 'nowrap', gap: 3, alignItems: 'center' }}>
              <Tag style={{ ...tagStyle, background: isLight ? '#18181b' : '#f4f4f5', color: isLight ? '#ffffff' : '#18181b', border: 'none', fontWeight: 600 }}>排除</Tag>
              {visible.map((id: string) => renderLevelTag(id, true))}
              {hiddenCount > 0 && (
                <Tooltip title={`排除等级: ${allNames}`}>
                  <Tag style={{ ...tagStyle, background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)', color: isLight ? '#71717a' : '#a1a1aa', border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)', cursor: 'pointer' }}>+{hiddenCount}</Tag>
                </Tooltip>
              )}
            </div>
          );
        }
        if (!groups || groups.length === 0) {
          return <Tag style={{ ...tagStyle, background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)', color: isLight ? '#18181b' : '#f4f4f5', border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)', fontWeight: 500 }}>全部等级</Tag>;
        }
        const visible = groups.slice(0, 2);
        const hiddenCount = groups.length - visible.length;
        const allNames = groups.map((id: string) => resolveUserLevelMeta(id).tagText).join(', ');
        return (
          <div style={{ display: 'flex', flexWrap: 'nowrap', gap: 3, alignItems: 'center' }}>
            {visible.map((id: string) => renderLevelTag(id, false))}
            {hiddenCount > 0 && (
              <Tooltip title={`允许等级: ${allNames}`}>
                <Tag style={{ ...tagStyle, background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)', color: isLight ? '#71717a' : '#a1a1aa', border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)', cursor: 'pointer' }}>+{hiddenCount}</Tag>
              </Tooltip>
            )}
          </div>
        );
      },
    },
    {
      title: '消耗 / 额度',
      key: 'quota',
      width: 140,
      render: (_: any, record: Channel) => renderQuotaRings(record, true),
    },
    {
      title: '使用上游',
      key: 'upstream',
      width: 240,
      filters: [
        { text: '预设渠道', value: 'preset' },
        { text: '高可用渠道', value: 'ha' },
      ],
      onFilter: (value: any, record: Channel) => {
        if (value === 'preset') {
          return record.provider_type !== 'high_availability_group' && !!record.preset_id;
        }
        if (value === 'ha') {
          return record.provider_type === 'high_availability_group';
        }
        return true;
      },
      render: (_: any, record: Channel) => {
        const tagStyle: React.CSSProperties = {
          borderRadius: 4,
          margin: 0,
          padding: '0 5px',
          fontSize: 10,
          height: 18,
          lineHeight: '16px',
          flexShrink: 0,
          background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)',
          color: isLight ? '#18181b' : '#f4f4f5',
          border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)'
        };
        if (record.provider_type === 'high_availability_group') {
          const parsed = parseChannelConfig(record);
          const subIds: any[] = Array.isArray(parsed.sub_channels) ? parsed.sub_channels : [];
          const subCount = subIds.length;
          const meltdown = meltdownMap[record.id];
          const meltedSubs = (meltdown?.sub_channels || []).filter((s: any) => s.is_melted);
          const totalSubs = (meltdown?.sub_channels || []).length || subCount;
          return (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'wrap' }}>
                <Popover
                  placement="bottom"
                  trigger={['hover', 'click']}
                  title={
                    <div style={{ fontSize: 12, fontWeight: 600, paddingBottom: 4 }}>
                      已绑定的上游渠道 ({subCount})
                    </div>
                  }
                  content={
                    subCount > 0 ? (
                      <div style={{ maxHeight: 260, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 6, minWidth: 280, maxWidth: 420 }}>
                        {subIds.map((id: any) => {
                          const p = presets.find((x: any) => Number(x.id) === Number(id));
                          const disabled = p && (p.status ?? 1) !== 1;
                          const eff = p ? getEffectiveUpstreamSchedule(p, parsed) : null;
                          const isGroupEnabled = eff ? eff.isGroupEnabled : parsed?.upstream_overrides?.[String(id)]?.status !== 0;
                          const subMelt = (meltdown?.sub_channels || []).find((s: any) => Number(s.config_id) === Number(id));
                          const isSubMelted = isGroupEnabled && Boolean(subMelt?.is_melted);
                          const isSubDemoted = isSubMelted && Boolean(subMelt?.is_demoted);
                          const subRemaining = subMelt?.remaining_seconds || 0;
                          const loadKey = `${record.id}-${id}`;
                          return (
                            <div
                              key={id}
                              style={{
                                display: 'flex',
                                alignItems: 'center',
                                justifyContent: 'space-between',
                                gap: 8,
                                padding: '5px 8px',
                                borderRadius: 6,
                                background: isSubMelted
                                  ? (isSubDemoted
                                      ? (isLight ? 'rgba(250,140,22,0.06)' : 'rgba(250,140,22,0.12)')
                                      : (isLight ? 'rgba(255,77,79,0.06)' : 'rgba(255,77,79,0.12)'))
                                  : (isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)'),
                                border: isSubMelted
                                  ? (isSubDemoted ? '1px solid rgba(250,140,22,0.35)' : '1px solid rgba(255,77,79,0.35)')
                                  : (isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.08)'),
                                opacity: disabled || !isGroupEnabled ? 0.72 : 1,
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
                                    color: disabled || !isGroupEnabled ? 'var(--text-secondary, #8c8c8c)' : undefined,
                                  }}
                                  title={p ? p.name : `未知渠道 (ID: ${id})`}
                                >
                                  {p ? p.name : `未知渠道 (ID: ${id})`}
                                </span>
                                {disabled && (
                                  <Tag style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 2, flexShrink: 0, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#71717a' : '#a1a1aa', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>
                                    已禁用
                                  </Tag>
                                )}
                                {!isGroupEnabled && (
                                  <Tag color="warning" style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 2, flexShrink: 0 }}>
                                    本组已关
                                  </Tag>
                                )}
                                {isSubMelted && (
                                  <Tooltip
                                    title={
                                      isSubDemoted
                                        ? '连续软故障已自动降级（排在正常渠道之后兜底；冷却到期后单飞探测连续 2 次成功自动恢复正常优先级）'
                                        : '鉴权/余额硬熔断或手动熔断隔离中'
                                    }
                                  >
                                    <Tag color={isSubDemoted ? 'warning' : 'error'} style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '14px', height: 16, borderRadius: 2, flexShrink: 0 }}>
                                      {isSubDemoted ? (subRemaining > 0 ? `降级 ${subRemaining}s` : '降级待探') : `熔断 ${subRemaining}s`}
                                    </Tag>
                                  </Tooltip>
                                )}
                              </div>
                              <Space size={4} style={{ flexShrink: 0, alignItems: 'center' }}>
                                {eff && (
                                  <Tag
                                    color={eff.customPriority !== undefined || eff.customWeight !== undefined || eff.customRpmLimit !== undefined ? 'blue' : undefined}
                                    style={{
                                      margin: 0,
                                      fontSize: 10,
                                      padding: '0 4px',
                                      lineHeight: '14px',
                                      height: 16,
                                      borderRadius: 2,
                                      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                                    }}
                                  >
                                    P:{eff.priority} W:{eff.weight}{eff.rpmLimit > 0 ? ` RPM:${eff.rpmLimit}` : ''} {eff.rate}x
                                  </Tag>
                                )}
                                <Text
                                  type="secondary"
                                  style={{
                                    fontSize: 11,
                                    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                                    flexShrink: 0,
                                  }}
                                >
                                  {p?.yid ? `YID: ${p.yid}` : `ID: ${id}`}
                                </Text>
                                <Tooltip title={isGroupEnabled ? '当前分组已启用该上游（熔断遵循高可用策略），点击关闭后不参与本组调用' : '当前分组已关闭该上游（不参与调用），点击在当前分组启用'}>
                                  <Switch
                                    size="small"
                                    checked={isGroupEnabled}
                                    loading={Boolean(subMeltLoading[loadKey])}
                                    onChange={(checked) => handleToggleSubChannelStatus(record.id, Number(id), checked)}
                                  />
                                </Tooltip>
                              </Space>
                            </div>
                          );
                        })}
                      </div>
                    ) : (
                      <Text type="secondary" style={{ fontSize: 12 }}>未绑定上游渠道</Text>
                    )
                  }
                >
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, cursor: 'pointer' }}>
                    <Tag style={{
                      ...tagStyle,
                      background: activePlugins['high_availability_channel'] ? (isLight ? '#18181b' : '#f4f4f5') : 'transparent',
                      color: activePlugins['high_availability_channel'] ? (isLight ? '#ffffff' : '#18181b') : (isLight ? '#71717a' : '#a1a1aa'),
                      border: activePlugins['high_availability_channel'] ? 'none' : (isLight ? '1px dashed #d4d4d8' : '1px dashed #3f3f46'),
                      fontWeight: 600,
                      cursor: 'pointer',
                    }}>
                      {activePlugins['high_availability_channel'] ? '高可用' : '高可用未开启'}
                    </Tag>
                    <Tag style={{ ...tagStyle, fontWeight: 600, cursor: 'pointer' }}>HA {subCount}</Tag>
                  </span>
                </Popover>
                {meltedSubs.length > 0 && (() => {
                  const allDemoted = meltedSubs.every((s: any) => s.is_demoted);
                  return (
                    <Tag
                      color={allDemoted ? 'warning' : 'error'}
                      style={{
                        margin: 0,
                        padding: '0 5px',
                        fontSize: 10,
                        height: 18,
                        lineHeight: '16px',
                        borderRadius: 4,
                        fontWeight: 500,
                      }}
                    >
                      {allDemoted ? '降级' : '熔断'} {meltedSubs.length}/{totalSubs}
                    </Tag>
                  );
                })()}
              </div>
            </div>
          );
        }
        if (record.provider_type === 'volcengine') {
          const { name, baseUrl } = resolveVolcEnhanceUpstream(record);
          return (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 1, maxWidth: 260 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                <Tag style={tagStyle}>封装</Tag>
                <Text strong style={{ fontSize: 12, lineHeight: 1.25 }} ellipsis={{ tooltip: name }}>{name}</Text>
              </div>
              {baseUrl ? (
                <Text type="secondary" style={{ fontSize: 11, lineHeight: 1.2 }} ellipsis={{ tooltip: baseUrl }}>
                  {baseUrl}
                </Text>
              ) : null}
            </div>
          );
        }
        if (record.provider_type === 'comfyui') {
          const { name, baseUrl } = resolveComfyuiUpstream(record);
          return (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 1, maxWidth: 260 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                <Tag style={tagStyle}>ComfyUI</Tag>
                <Text strong style={{ fontSize: 12, lineHeight: 1.25 }} ellipsis={{ tooltip: name }}>{name}</Text>
              </div>
              {baseUrl ? (
                <Text type="secondary" style={{ fontSize: 11, lineHeight: 1.2 }} ellipsis={{ tooltip: baseUrl }}>
                  {baseUrl}
                </Text>
              ) : null}
            </div>
          );
        }
        if (record.preset_id) {
          const preset = presets.find(p => p.id === record.preset_id);
          const presetDisabled = preset && (preset.status ?? 1) !== 1;
          return (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'wrap' }}>
                <Tag style={tagStyle}>预设渠道</Tag>
                <Tag style={{ ...tagStyle, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' }}>
                  {preset?.yid ? `YID: ${preset.yid}` : `ID: ${record.preset_id}`}
                </Tag>
                {presetDisabled && (
                  <Tag style={{ ...tagStyle, background: 'transparent', color: isLight ? '#a1a1aa' : '#71717a', border: isLight ? '1px dashed #d4d4d8' : '1px dashed #3f3f46' }}>
                    已禁用
                  </Tag>
                )}
              </div>
              <Text strong style={{ fontSize: 12, lineHeight: 1.25 }}>{preset ? preset.name : '未知预设'}</Text>
            </div>
          );
        }
        return <Text type="secondary" style={{ fontSize: 12 }}>-</Text>;
      }
    },
    {
      title: '渠道分类',
      dataIndex: 'category_id',
      key: 'category_id',
      width: 90,
      render: (categoryId: number | null) => {
        const name = resolveCategoryName(categoryId);
        return name ? (
          <Tag style={{ margin: 0, padding: '0 5px', fontSize: 11, height: 19, lineHeight: '17px', borderRadius: 4 }}>
            {name}
          </Tag>
        ) : <Text type="secondary" style={{ fontSize: 12 }}>-</Text>;
      },
    },
    {
      title: '修改使用时间',
      dataIndex: 'updated_at',
      key: 'updated_at',
      width: 225,
      sorter: (a: Channel, b: Channel) => {
        const ta = a.updated_at || a.created_at || '';
        const tb = b.updated_at || b.created_at || '';
        return ta.localeCompare(tb);
      },
      render: (t: string, record: Channel) => {
        const fmt = (raw?: string | null) => {
          if (!raw) return '-';
          const d = new Date(raw);
          return Number.isNaN(d.getTime())
            ? raw
            : d.toLocaleString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
        };
        const modTime = t || record.created_at;
        const usedTime = record.last_used_at;
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
            <Text type="secondary" style={{ fontSize: 12, lineHeight: 1.25, whiteSpace: 'nowrap' }}>
              修改: {modTime ? fmt(modTime) : '-'}
            </Text>
            <Text type="secondary" style={{ fontSize: 12, lineHeight: 1.25, whiteSpace: 'nowrap' }}>
              使用: {usedTime ? fmt(usedTime) : '未使用'}
            </Text>
          </div>
        );
      },
    },
    {
      title: '页面排序',
      dataIndex: 'sort_order',
      key: 'sort_order',
      width: 90,
      align: 'center' as const,
      sorter: (a: Channel, b: Channel) => (a.sort_order || 0) - (b.sort_order || 0),
      render: (_sort_order: number, record: Channel) => {
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
                whiteSpace: 'nowrap',
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
      title: '操作',
      key: 'actions',
      align: 'center' as const,
      width: 170,
      fixed: 'right' as const,
      render: (_: unknown, record: Channel) => (
        <Space size={2} style={{ justifyContent: 'center', width: '100%' }}>
          <Tooltip title={record.status === 1 ? '点击禁用' : '点击启用'}>
            <Button
              type="text"
              size="small"
              className="channel-table-action-btn"
              icon={record.status === 1
                ? <PlayCircleOutlined style={{ color: isLight ? '#18181b' : '#f4f4f5' }} />
                : <StopOutlined style={{ color: isLight ? '#a1a1aa' : '#71717a' }} />}
              onClick={() => handleToggleStatus(record)}
            />
          </Tooltip>
          {record.provider_type === 'high_availability_group' && (
            <Tooltip title="重置熔断">
              <Button
                type="text"
                size="small"
                className="channel-table-action-btn"
                icon={<ReloadOutlined style={{ color: isLight ? '#52525b' : '#a1a1aa' }} />}
                onClick={() => handleResetMeltdown(record.id)}
                loading={meltdownLoading[record.id]}
              />
            </Tooltip>
          )}
          <Tooltip title="测试">
            <Button
              type="text"
              size="small"
              className="channel-table-action-btn"
              icon={<ExperimentOutlined />}
              onClick={() => handleTest(record)}
            />
          </Tooltip>
          <Tooltip title={t('channels.simulate_billing_short', '计费模拟')}>
            <Button
              type="text"
              size="small"
              className="channel-table-action-btn"
              icon={<CalculatorOutlined />}
              onClick={() => setSimulatingChannel(record)}
            />
          </Tooltip>
          <Tooltip title="清零额度">
            <Popconfirm title="确定清零该渠道的总/日/周/月已用额度吗？" onConfirm={() => handleResetQuota(record.id)}>
              <Button type="text" size="small" className="channel-table-action-btn" icon={<ClearOutlined />} />
            </Popconfirm>
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

  const renderQuotaRings = (record: Channel, compact = false) => {
    const fmt = (n: number) => (Number.isInteger(n) ? String(n) : n.toFixed(6));
    const items = quotaPeriodItems(record, quotaTz);
    const hasAnyConfigured = items.some((item) => item.limit >= 0);

    const slotWidth = compact ? 28 : 40;
    const ringSize = compact ? 24 : 36;
    const ringStroke = compact ? 5 : 10;
    const slotStyle: React.CSSProperties = {
      width: slotWidth,
      display: 'flex',
      flexDirection: 'column',
      alignItems: 'center',
      gap: compact ? 1 : 2,
      cursor: 'default',
    };
    const labelStyle: React.CSSProperties = {
      fontSize: compact ? 9 : 10,
      color: isLight ? 'rgba(0,0,0,0.4)' : 'rgba(255,255,255,0.45)',
      lineHeight: 1,
      transform: compact ? 'scale(0.92)' : undefined,
    };

    return (
      <div
        className="channel-quota-rings"
        style={{
          display: 'grid',
          gridTemplateColumns: `repeat(4, ${slotWidth}px)`,
          gap: compact ? 3 : 4,
          alignItems: 'center',
          justifyContent: 'start',
          width: compact ? 122 : 172,
        }}
      >
        {items.map((item, index) => {
          const configured = item.limit >= 0;
          const showUnlimited = !hasAnyConfigured && index === 0;
          const showRing = configured || showUnlimited;

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
            ? `额度：${currencySymbol}${fmt(item.used)} / ∞（无限）`
            : `${item.label}额度：${currencySymbol}${fmt(item.used)} / ${currencySymbol}${fmt(Number(item.limit))}（${pct}%）`;
          const stroke = showUnlimited
            ? (isLight ? '#d4d4d8' : 'rgba(255,255,255,0.2)')
            : (pct >= 100 ? (isLight ? '#71717a' : '#a1a1aa') : (isLight ? '#18181b' : '#f4f4f5'));

          return (
            <Tooltip key={item.key} title={tip}>
              <div style={slotStyle}>
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
                        fontSize: compact ? (showUnlimited ? 9 : 8) : (showUnlimited ? 11 : 10),
                        fontWeight: 600,
                        color: isLight ? 'rgba(0,0,0,0.72)' : 'rgba(255,255,255,0.88)',
                        lineHeight: 1,
                      }}
                    >
                      {showUnlimited ? '∞' : `${pct}%`}
                    </span>
                  )}
                />
                <span style={labelStyle}>{showUnlimited ? '无限' : item.label}</span>
              </div>
            </Tooltip>
          );
        })}
      </div>
    );
  };

  if (editQueryId && !isEditRoute) {
    return <Navigate to={channelEditPath(adminPath, editQueryId)} replace state={location.state} />;
  }

  return (
    <Card variant="borderless">
      <style>{`
        .channel-card-disabled {
          opacity: 0.62;
          filter: grayscale(18%);
        }
        .channels-grid-list .ant-list-items {
          display: grid !important;
          grid-template-columns: repeat(auto-fill, minmax(max(248px, calc(100% / 6)), 1fr)) !important;
          gap: 10px !important;
        }
        .channels-grid-list .ant-list-item {
          margin: 0 !important;
          padding: 0 !important;
          width: 100% !important;
          border-block-end: none !important;
        }
        .channel-dash-card {
          transition: border-color 0.15s ease, box-shadow 0.15s ease, transform 0.15s ease;
          cursor: default;
        }
        .channel-dash-card:hover {
          border-color: ${isLight ? 'rgba(22,119,255,0.35)' : 'rgba(105,177,255,0.35)'} !important;
          box-shadow: ${isLight ? '0 4px 14px rgba(15,23,42,0.08)' : '0 4px 16px rgba(0,0,0,0.35)'} !important;
          transform: translateY(-1px);
        }
        .channel-dash-card .channel-dash-actions {
          opacity: 0.72;
          transition: opacity 0.15s ease;
        }
        .channel-dash-card:hover .channel-dash-actions {
          opacity: 1;
        }
        .channel-dash-card .channel-dash-action-btn {
          width: 24px !important;
          height: 24px !important;
          min-width: 24px !important;
          padding: 0 !important;
          display: inline-flex !important;
          align-items: center;
          justify-content: center;
          border-radius: 6px !important;
        }
        .channel-dash-card .channel-dash-action-btn:hover {
          background: ${isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.08)'} !important;
        }
        @keyframes meltdownPulse {
          0%, 100% { opacity: 1; box-shadow: 0 0 0 0 rgba(255, 77, 79, 0.5); }
          50% { opacity: 0.6; box-shadow: 0 0 0 4px rgba(255, 77, 79, 0); }
        }
        .meltdown-pulse-dot {
          animation: meltdownPulse 1.5s ease-in-out infinite;
        }
        .channel-groups-table .ant-table,
        .channel-groups-table .ant-table-container,
        .channel-groups-table .ant-table-content,
        .channel-groups-table table {
          border-collapse: collapse !important;
          border-spacing: 0 !important;
        }
        .channel-groups-table .ant-table-content {
          overflow-x: auto !important;
          scrollbar-width: none !important;
        }
        .channel-groups-table .ant-table-content::-webkit-scrollbar {
          display: none !important;
          height: 0 !important;
        }
        .channel-groups-table .ant-table-thead {
          background: ${isLight ? '#f9fafb' : '#18181b'} !important;
        }
        .channel-groups-table .ant-table-thead > tr {
          height: 28px !important;
          background: ${isLight ? '#f9fafb' : '#18181b'} !important;
        }
        .channel-groups-table .ant-table-thead > tr > th,
        .channel-groups-table .ant-table-thead > tr > th.ant-table-cell {
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
        .channel-groups-table .ant-table-thead .ant-table-column-sorters {
          padding: 0 !important;
          margin: 0 !important;
          height: 20px !important;
          display: inline-flex !important;
          align-items: center !important;
        }
        .channel-groups-table .ant-table-thead .ant-table-column-title {
          line-height: 20px !important;
          font-size: 12px !important;
          font-weight: 600 !important;
        }
        .channel-groups-table .ant-table-thead .ant-table-column-sorter {
          margin-inline-start: 3px !important;
          font-size: 9px !important;
        }
        .channel-groups-table .ant-table-thead > tr > th::before {
          display: none !important;
        }
        .channel-groups-table .ant-table-tbody {
          margin: 0 !important;
          padding: 0 !important;
        }
        .channel-groups-table .ant-table-tbody > tr {
          margin: 0 !important;
          background: transparent !important;
        }
        .channel-groups-table .ant-table-tbody > tr > td {
          padding: 4px 8px !important;
          font-size: 12px !important;
          border-bottom: 1px solid ${isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.05)'} !important;
          vertical-align: middle !important;
          line-height: 1.3 !important;
        }
        .channel-groups-table .ant-table-tbody > tr:first-child > td {
          border-top: none !important;
        }
        .channel-groups-table .ant-table-measure-row,
        .channel-groups-table .ant-table-measure-row td,
        .channel-groups-table .ant-table-measure-row th,
        .channel-groups-table tr.ant-table-measure-row,
        .channel-groups-table tr.ant-table-measure-row td,
        .channel-groups-table tr.ant-table-measure-row th,
        .channel-groups-table tr.ant-table-measure-row .ant-table-cell {
          padding: 0 !important;
          height: 0 !important;
          font-size: 0 !important;
          line-height: 0 !important;
          border: none !important;
          visibility: hidden !important;
        }
        .channel-groups-table .ant-table-tbody > tr > td.ant-table-cell-fix-left,
        .channel-groups-table .ant-table-tbody > tr > td.ant-table-cell-fix-right {
          background: ${isLight ? '#ffffff' : '#141414'} !important;
        }
        .channel-groups-table .ant-table-tbody > tr:hover > td,
        .channel-groups-table .ant-table-tbody > tr:hover > td.ant-table-cell-fix-left,
        .channel-groups-table .ant-table-tbody > tr:hover > td.ant-table-cell-fix-right {
          background: ${isLight ? '#fafafa' : '#1d1d1d'} !important;
        }
        .channel-groups-table .ant-table-pagination.ant-pagination {
          margin: 10px 0 0 0 !important;
        }
        .channel-table-action-btn {
          width: 22px !important;
          height: 22px !important;
          min-width: 22px !important;
          padding: 0 !important;
          display: inline-flex !important;
          align-items: center;
          justify-content: center;
          border-radius: 4px !important;
          font-size: 12px !important;
        }
        .channel-table-action-btn:hover {
          background: ${isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.08)'} !important;
        }
        .channel-name-form-item .ant-form-item-label {
          width: 100%;
        }
        .channel-name-form-item .ant-form-item-label > label {
          width: 100%;
          max-width: 100%;
          display: flex;
        }
        .channel-name-form-item .channel-name-label-row {
          flex: 1;
          min-width: 0;
          width: 100%;
        }
      `}</style>
      {!isEditorOpen ? (
        <div style={{ width: '100%' }}>
          <div style={{ display: 'flex', flexDirection: screens.xs ? 'column' : 'row', justifyContent: 'space-between', marginBottom: 24, gap: 12 }}>
            <Title level={4} style={{ margin: 0, fontSize: screens.xs ? 18 : 20, fontWeight: 600 }}>{t('channels.title')}</Title>
            <Space wrap>
              <Segmented
                options={[
                  { label: `全部 (${channels.length})`, value: 'all' },
                  { label: `激活 (${channels.filter(c => c.status === 1).length})`, value: '1' },
                  { label: `已禁用 (${channels.filter(c => c.status === 0).length})`, value: '0' },
                ]}
                value={statusFilter === 'all' ? 'all' : statusFilter.toString()}
                onChange={(val) => setStatusFilter(val === 'all' ? 'all' : parseInt(val as string, 10))}
              />
              <Segmented
                options={[
                  { value: 'card', icon: <AppstoreOutlined /> },
                  { value: 'list', icon: <UnorderedListOutlined /> }
                ]}
                value={viewMode}
                onChange={(val) => {
                  const nextMode = val as 'list' | 'card';
                  setViewMode(nextMode);
                  localStorage.setItem('channels_view_mode', val as string);
                  setCurrentPage(1);
                  setPageSize(nextMode === 'card' ? 24 : 20);
                }}
              />
              <Input.Search
                placeholder="AID或名称"
                allowClear
                onSearch={setSearchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                style={{ width: 160 }}
              />
              <Input.Search
                placeholder="模型名称/模型ID/MID"
                allowClear
                value={modelLocateQuery}
                onChange={(e) => setModelLocateQuery(e.target.value)}
                style={{ width: 240 }}
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
              <Button type="default" icon={<ApartmentOutlined />} onClick={() => navigate(`/${adminPath}/channels/model-display`)}>模型渠道显示</Button>
              <Button type="primary" icon={<PlusOutlined />} onClick={handleAdd}>{t('channels.add_channel')}</Button>
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
              <Text type="secondary" style={{ width: 80, flexShrink: 0, fontSize: 13 }}>渠道分类</Text>
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, flexGrow: 1 }}>
                {([
                  { key: 'all' as const, label: '全部', count: channels.length },
                  ...categories.map(c => ({
                    key: c.id,
                    label: c.name,
                    count: channels.filter(ch => ch.category_id === c.id).length,
                  })),
                  {
                    key: 'unclassified' as const,
                    label: '未分类',
                    count: channels.filter(ch => !ch.category_id).length,
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

            {(() => {
              const typeCounts = {
                default: channels.filter(ch => getChannelTypeKey(ch) === 'default').length,
                volcengine: channels.filter(ch => getChannelTypeKey(ch) === 'volcengine').length,
                ha: channels.filter(ch => getChannelTypeKey(ch) === 'ha').length,
                comfyui: channels.filter(ch => getChannelTypeKey(ch) === 'comfyui').length,
              };
              const typeOptions = (
                [
                  { key: 'all' as const, label: '全部', count: channels.length },
                  { key: 'default' as const, label: '预设', count: typeCounts.default },
                  { key: 'volcengine' as const, label: '封装', count: typeCounts.volcengine },
                  { key: 'comfyui' as const, label: 'ComfyUI', count: typeCounts.comfyui },
                  { key: 'ha' as const, label: '高可用', count: typeCounts.ha },
                ] as { key: 'all' | 'default' | 'volcengine' | 'ha' | 'comfyui'; label: string; count: number }[]
              ).filter((item) => item.key === 'all' || item.count > 0);

              if (typeOptions.length <= 1) return null;

              return (
                <div style={{ display: 'flex', alignItems: 'center', padding: 0 }}>
                  <Text type="secondary" style={{ width: 80, flexShrink: 0, fontSize: 13 }}>渠道类型</Text>
                  <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, flexGrow: 1 }}>
                    {typeOptions.map((item) => {
                      const selected = typeFilter === item.key;
                      return (
                        <div
                          key={item.key}
                          onClick={() => setTypeFilter(item.key)}
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
                  </div>
                </div>
              );
            })()}
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
                    const ids = currentPageChannels.map(c => c.id);
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
                  title="确定要批量删除选中的渠道分组吗？"
                  description={`将一次性删除选中的 ${selectedRowKeys.length} 个渠道分组，此操作不可撤销。`}
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
              dataSource={currentPageChannels}
              loading={loading}
              rowKey="id"
              pagination={listPagination({
                current: currentPage,
                pageSize,
                total: filteredChannels.length,
                onChange: onPageChange,
              })}
              renderCard={(record: any) => {
                const groups = record.user_groups;
                const excludeGroups = record.exclude_user_groups;
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
                        <Text strong>{record.name}</Text>
                      </div>
                    }
                    extra={
                      <Space size={6} style={{ color: record.status === 1 ? (isLight ? '#18181b' : '#f4f4f5') : (isLight ? '#a1a1aa' : '#71717a') }}>
                        <div style={{ width: 6, height: 6, borderRadius: '50%', backgroundColor: record.status === 1 ? (isLight ? '#18181b' : '#f4f4f5') : (isLight ? '#d4d4d8' : '#3f3f46') }} />
                        <span style={{ fontSize: 12 }}>{record.status === 1 ? t('common.active') : t('common.disabled')}</span>
                      </Space>
                    }
                  >
                    {record.group_aid && <CardRow label="AID"><Text type="secondary">{record.group_aid}</Text></CardRow>}
                    <CardRow label="支持等级">
                      {excludeGroups && excludeGroups.length > 0 ? (
                        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, alignItems: 'center' }}>
                          <Tag style={{ borderRadius: 4, margin: 0, fontSize: 11, background: isLight ? '#18181b' : '#f4f4f5', color: isLight ? '#ffffff' : '#18181b', border: 'none', fontWeight: 600 }}>排除</Tag>
                          {excludeGroups.map((id: string) => {
                            const meta = resolveUserLevelMeta(id);
                            return <Tag key={id} style={{ borderRadius: 4, margin: 0, fontSize: 11, background: 'transparent', color: (!meta.exists || meta.isDisabled) ? (isLight ? '#a1a1aa' : '#71717a') : (isLight ? '#71717a' : '#a1a1aa'), border: isLight ? '1px dashed #d4d4d8' : '1px dashed #3f3f46' }}>{meta.tagText}</Tag>;
                          })}
                        </div>
                      ) : (!groups || groups.length === 0) ? (
                        <Tag style={{ borderRadius: 4, margin: 0, fontSize: 11, background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)', color: isLight ? '#18181b' : '#f4f4f5', border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)', fontWeight: 500 }}>全部等级</Tag>
                      ) : (
                        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
                          {groups.map((id: string) => {
                            const meta = resolveUserLevelMeta(id);
                            const isInvalid = !meta.exists || meta.isDisabled;
                            return <Tag key={id} style={{ borderRadius: 4, margin: 0, fontSize: 11, background: isInvalid ? 'transparent' : (isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)'), color: isInvalid ? (isLight ? '#a1a1aa' : '#71717a') : (isLight ? '#27272a' : '#e4e4e7'), border: isInvalid ? (isLight ? '1px dashed #d4d4d8' : '1px dashed #3f3f46') : (isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)') }}>{meta.tagText}</Tag>;
                          })}
                        </div>
                      )}
                    </CardRow>
                    <CardRow label="绑定模型">
                      {renderBoundModels(record)}
                    </CardRow>
                    <CardRow label="已用/额度">
                      {renderQuotaRings(record)}
                    </CardRow>
                    <CardRow label="使用上游">
                      {record.provider_type === 'high_availability_group' ? (
                        (() => {
                          const parsed = parseChannelConfig(record);
                          const subCount = parsed.sub_channels ? parsed.sub_channels.length : 0;
                          return (
                            <Space size={4}>
                              <Tag style={{
                                borderRadius: 4,
                                margin: 0,
                                fontSize: 10,
                                background: activePlugins['high_availability_channel'] ? (isLight ? '#18181b' : '#f4f4f5') : 'transparent',
                                color: activePlugins['high_availability_channel'] ? (isLight ? '#ffffff' : '#18181b') : (isLight ? '#71717a' : '#a1a1aa'),
                                border: activePlugins['high_availability_channel'] ? 'none' : (isLight ? '1px dashed #d4d4d8' : '1px dashed #3f3f46'),
                                fontWeight: 600
                              }}>
                                {activePlugins['high_availability_channel'] ? '高可用' : '高可用插件未开启'}
                              </Tag>
                              <Text style={{ fontSize: 12 }}>
                                高可用虚拟渠道组 (已绑定 {subCount} 个渠道)
                              </Text>
                            </Space>
                          );
                        })()
                      ) : record.provider_type === 'volcengine' ? (
                        (() => {
                          const { name, baseUrl } = resolveVolcEnhanceUpstream(record);
                          return (
                            <Space size={4} wrap>
                              <Tag style={{ borderRadius: 4, margin: 0, fontSize: 10, background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)', color: isLight ? '#18181b' : '#f4f4f5', border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)' }}>封装</Tag>
                              <Text style={{ fontSize: 12 }}>
                                {name}{baseUrl ? ` (${baseUrl})` : ''}
                              </Text>
                            </Space>
                          );
                        })()
                      ) : record.provider_type === 'comfyui' ? (
                        (() => {
                          const { name, baseUrl } = resolveComfyuiUpstream(record);
                          return (
                            <Space size={4} wrap>
                              <Tag style={{ borderRadius: 4, margin: 0, fontSize: 10, background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)', color: isLight ? '#18181b' : '#f4f4f5', border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)' }}>ComfyUI</Tag>
                              <Text style={{ fontSize: 12 }}>
                                {name}{baseUrl ? ` (${baseUrl})` : ''}
                              </Text>
                            </Space>
                          );
                        })()
                      ) : record.preset_id ? (
                        (() => {
                          const preset = presets.find(p => p.id === record.preset_id);
                          const presetDisabled = preset && (preset.status ?? 1) !== 1;
                          return (
                            <Space size={4} wrap>
                              <Tag style={{ borderRadius: 4, margin: 0, fontSize: 10, background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)', color: isLight ? '#18181b' : '#f4f4f5', border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)' }}>预设</Tag>
                              {presetDisabled && <Tag style={{ borderRadius: 4, margin: 0, fontSize: 10, background: 'transparent', color: isLight ? '#a1a1aa' : '#71717a', border: isLight ? '1px dashed #d4d4d8' : '1px dashed #3f3f46' }}>上游已禁用</Tag>}
                              <Text style={{ fontSize: 12 }}>
                                {preset?.name || '未知预设'} ({preset?.yid ? `YID: ${preset.yid}` : `ID: ${record.preset_id}`})
                              </Text>
                            </Space>
                          );
                        })()
                      ) : (
                        <Text type="secondary">-</Text>
                      )}
                    </CardRow>
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
                    <CardRow label="渠道分类">
                      {resolveCategoryName(record.category_id) ? (
                        <Tag style={{ margin: 0 }}>{resolveCategoryName(record.category_id)}</Tag>
                      ) : (
                        <Text type="secondary">-</Text>
                      )}
                    </CardRow>
                    <CardRow label="优先级">
                      <Text type="secondary">{record.priority || 0}</Text>
                    </CardRow>

                    <CardRow label="修改使用时间">
                      <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 1 }}>
                        <Text type="secondary" style={{ fontSize: 12 }}>修改: {new Date(record.updated_at || record.created_at).toLocaleString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })}</Text>
                        <Text type="secondary" style={{ fontSize: 12 }}>使用: {record.last_used_at ? new Date(record.last_used_at).toLocaleString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) : '未使用'}</Text>
                      </div>
                    </CardRow>

                    {/* 熔断状态可视化（仅 HA 渠道且有熔断时显示） */}
                    {record.provider_type === 'high_availability_group' && (() => {
                      const meltdown = meltdownMap[record.id];
                      if (!meltdown) return null;
                      const meltedSubs = (meltdown.sub_channels || []).filter((s: any) => s.is_melted);
                      const channelMelted = meltdown.channel_meltdown?.is_melted;
                      const totalMelted = meltedSubs.length + (channelMelted ? 1 : 0);
                      if (totalMelted === 0) return null;
                      const totalSubs = (meltdown.sub_channels || []).length;
                      return (
                        <div style={{
                          background: isLight ? 'rgba(255, 77, 79, 0.04)' : 'rgba(255, 77, 79, 0.08)',
                          borderRadius: 6,
                          padding: '8px 10px',
                          margin: '4px 0',
                          border: isLight ? '1px solid rgba(255, 77, 79, 0.12)' : '1px solid rgba(255, 77, 79, 0.2)',
                        }}>
                          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
                            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                              <div className="meltdown-pulse-dot" style={{ width: 7, height: 7, borderRadius: '50%', backgroundColor: '#ff4d4f', flexShrink: 0 }} />
                              <Text style={{ fontSize: 12, color: '#ff4d4f', fontWeight: 500 }}>
                                <ThunderboltOutlined style={{ marginRight: 3 }} />
                                熔断中 {meltedSubs.length}/{totalSubs} 个子渠道
                              </Text>
                            </div>
                            <Popconfirm
                              title="确定要重置所有熔断状态吗？"
                              description="重置后所有子渠道将立即恢复可用"
                              onConfirm={() => handleResetMeltdown(record.id)}
                              okText="重置"
                              cancelText="取消"
                            >
                              <Button
                                type="link"
                                size="small"
                                danger
                                loading={meltdownLoading[record.id]}
                                icon={<ReloadOutlined />}
                                style={{ padding: 0, fontSize: 12, height: 20 }}
                              >
                                重置熔断
                              </Button>
                            </Popconfirm>
                          </div>
                          {meltedSubs.map((sub: any) => (
                            <div key={sub.config_id} style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', fontSize: 12, padding: '2px 0' }}>
                              <Text style={{ fontSize: 12, color: isLight ? 'rgba(0,0,0,0.65)' : 'rgba(255,255,255,0.65)' }} ellipsis>
                                {sub.name}
                              </Text>
                              <Tag style={{ borderRadius: 4, margin: 0, padding: '0 4px', fontSize: 10, lineHeight: '16px', background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#18181b' : '#f4f4f5', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>
                                剩余 {sub.remaining_seconds}s
                              </Tag>
                            </div>
                          ))}
                        </div>
                      );
                    })()}

                    <CardActions>
                      <Tooltip title={record.status === 1 ? '点击禁用' : '点击启用'}>
                        <Button
                          type="text"
                          size="small"
                          icon={record.status === 1
                            ? <PlayCircleOutlined style={{ color: isLight ? '#18181b' : '#f4f4f5' }} />
                            : <StopOutlined style={{ color: isLight ? '#a1a1aa' : '#71717a' }} />}
                          onClick={() => handleToggleStatus(record)}
                        />
                      </Tooltip>
                      {record.provider_type === 'high_availability_group' && (
                        <Tooltip title="重置熔断">
                          <Button
                            type="text"
                            size="small"
                            icon={<ReloadOutlined style={{ color: isLight ? '#52525b' : '#a1a1aa' }} />}
                            onClick={() => handleResetMeltdown(record.id)}
                            loading={meltdownLoading[record.id]}
                          />
                        </Tooltip>
                      )}
                      <Tooltip title="测试">
                        <Button type="text" size="small" icon={<ExperimentOutlined />} onClick={() => handleTest(record)} />
                      </Tooltip>
                      <Tooltip title={t('channels.simulate_billing_short', '计费模拟')}>
                        <Button type="text" size="small" icon={<CalculatorOutlined />} onClick={() => setSimulatingChannel(record)} />
                      </Tooltip>
                      <Tooltip title="清零额度">
                        <Popconfirm title="确定清零该渠道的总/日/周/月已用额度吗？" onConfirm={() => handleResetQuota(record.id)}>
                          <Button type="text" size="small" icon={<ClearOutlined />} />
                        </Popconfirm>
                      </Tooltip>
                      <Tooltip title="编辑">
                        <Button type="text" size="small" icon={<EditOutlined />} onClick={() => handleEdit(record)} />
                      </Tooltip>
                      <Tooltip title="删除">
                        <Popconfirm title={t('common.confirm_delete')} onConfirm={() => handleDelete(record.id)}>
                          <Button type="text" size="small" icon={<DeleteOutlined />} danger />
                        </Popconfirm>
                      </Tooltip>
                    </CardActions>
                  </MobileCard>
                );
              }}
            />
          ) : viewMode === 'list' ? (
            <TableStickyHorizontalScroll isLight={isLight}>
              <Table
                className="channel-groups-table compact-table"
                size="small"
                dataSource={filteredChannels}
                columns={columns}
                rowKey="id"
                loading={loading}
                pagination={listPagination({
                  current: currentPage,
                  pageSize,
                  total: filteredChannels.length,
                  onChange: onPageChange,
                })}
                scroll={{ x: 1540 }}
                rowSelection={isBatchEditMode ? {
                  selectedRowKeys,
                  preserveSelectedRowKeys: true,
                  onChange: (keys) => setSelectedRowKeys(keys),
                } : undefined}
              />
            </TableStickyHorizontalScroll>
          ) : (
            <List
              className="channels-grid-list"
              dataSource={filteredChannels}
              loading={loading}
              pagination={listPagination({
                current: currentPage,
                pageSize,
                total: filteredChannels.length,
                pageSizeOptions: ['24', '48', '72', '96'],
                size: 'small',
                onChange: onPageChange,
              })}
              renderItem={(record: Channel) => {
                const groups = record.user_groups || [];
                const excludeGroups = record.exclude_user_groups || [];
                const resolveName = (idStr: string) => resolveUserLevelMeta(idStr).tagText;
                const levelIds = excludeGroups.length > 0 ? excludeGroups : groups;
                const levelMode = excludeGroups.length > 0 ? 'exclude' : (!groups.length ? 'all' : 'allow');
                const visibleLevels = levelIds.slice(0, 2);
                const moreLevels = levelIds.length - visibleLevels.length;

                const isHa = record.provider_type === 'high_availability_group';
                const hasMeltdown = isHa && meltdownMap[record.id] && (
                  (meltdownMap[record.id].sub_channels || []).some((s: any) => s.is_melted)
                  || meltdownMap[record.id].channel_meltdown?.is_melted
                );
                const categoryName = resolveCategoryName(record.category_id);

                let upstreamTag: React.ReactNode = (
                  <Tag style={{ margin: 0, padding: '0 5px', fontSize: 10, lineHeight: '18px', borderRadius: 4 }}>无上游</Tag>
                );
                if (isHa) {
                  const parsed = parseChannelConfig(record);
                  const subIds = parsed.sub_channels || [];
                  const boundChannels = subIds.map((id: any) => {
                    const p = presets.find((x: any) => x.id === id);
                    return p ? `- ${p.name} (YID: ${p.yid || '无'})` : `- 未知渠道 (ID: ${id})`;
                  });
                  const tip = boundChannels.length > 0
                    ? `高可用虚拟渠道组，已绑定:\n${boundChannels.join('\n')}`
                    : '高可用虚拟渠道组 (未绑定上游)';
                  upstreamTag = (
                    <Tooltip title={<div style={{ whiteSpace: 'pre-wrap' }}>{tip}</div>}>
                      <Tag style={{
                        margin: 0,
                        padding: '0 5px',
                        fontSize: 10,
                        lineHeight: '18px',
                        borderRadius: 4,
                        background: activePlugins['high_availability_channel'] ? (isLight ? '#18181b' : '#f4f4f5') : 'transparent',
                        color: activePlugins['high_availability_channel'] ? (isLight ? '#ffffff' : '#18181b') : (isLight ? '#71717a' : '#a1a1aa'),
                        border: activePlugins['high_availability_channel'] ? 'none' : (isLight ? '1px dashed #d4d4d8' : '1px dashed #3f3f46'),
                        fontWeight: 600
                      }}>
                        {activePlugins['high_availability_channel'] ? `高可用 · ${subIds.length}` : '高可用未开启'}
                      </Tag>
                    </Tooltip>
                  );
                } else if (record.provider_type === 'volcengine') {
                  const { name, baseUrl } = resolveVolcEnhanceUpstream(record);
                  upstreamTag = (
                    <Tooltip title={<div style={{ whiteSpace: 'pre-wrap' }}>{baseUrl ? `封装: ${name}\n${baseUrl}` : `封装: ${name}`}</div>}>
                      <Tag style={{ margin: 0, padding: '0 5px', fontSize: 10, lineHeight: '18px', borderRadius: 4, background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)', color: isLight ? '#18181b' : '#f4f4f5', border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)' }}>封装</Tag>
                    </Tooltip>
                  );
                } else if (record.provider_type === 'comfyui') {
                  const { name, baseUrl } = resolveComfyuiUpstream(record);
                  upstreamTag = (
                    <Tooltip title={<div style={{ whiteSpace: 'pre-wrap' }}>{baseUrl ? `ComfyUI: ${name}\n${baseUrl}` : `ComfyUI: ${name}`}</div>}>
                      <Tag style={{ margin: 0, padding: '0 5px', fontSize: 10, lineHeight: '18px', borderRadius: 4, background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)', color: isLight ? '#18181b' : '#f4f4f5', border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)' }}>ComfyUI</Tag>
                    </Tooltip>
                  );
                } else if (record.preset_id) {
                  const preset = presets.find(p => p.id === record.preset_id);
                  const presetDisabled = preset && (preset.status ?? 1) !== 1;
                  upstreamTag = (
                    <Tooltip title={preset ? `预设: ${preset.name} (YID: ${preset.yid || '无'})${presetDisabled ? ' · 已禁用' : ''}` : '未知预设'}>
                      <Tag style={{ margin: 0, padding: '0 5px', fontSize: 10, lineHeight: '18px', borderRadius: 4, background: presetDisabled ? 'transparent' : (isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)'), color: presetDisabled ? (isLight ? '#a1a1aa' : '#71717a') : (isLight ? '#18181b' : '#f4f4f5'), border: presetDisabled ? (isLight ? '1px dashed #d4d4d8' : '1px dashed #3f3f46') : (isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)') }}>
                        {presetDisabled ? '上游已禁用' : '预设'}
                      </Tag>
                    </Tooltip>
                  );
                }

                return (
                  <List.Item style={{ height: '100%', marginBottom: 0, width: '100%' }}>
                    <Card
                      className={`channel-dash-card${record.status === 0 ? ' channel-card-disabled' : ''}`}
                      onDoubleClick={() => { if (!isBatchEditMode) handleEdit(record); }}
                      style={{
                        background: isLight ? '#fff' : '#1a1a1a',
                        borderRadius: 8,
                        border: isLight ? '1px solid #d4d4d8' : '1px solid rgba(255, 255, 255, 0.1)',
                        boxShadow: 'none',
                        display: 'flex',
                        flexDirection: 'column',
                        width: '100%',
                        height: '100%',
                        ...(hasMeltdown ? { borderLeft: '3px solid #ff4d4f' } : {}),
                      }}
                      styles={{ body: { padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 6, flex: 1 } }}
                    >
                      {/* 头部：状态 + 名称 */}
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6, minHeight: 24 }}>
                        {isBatchEditMode && (
                          <Checkbox
                            checked={batchChecked(record.id)}
                            onClick={(e) => e.stopPropagation()}
                            onChange={(e) => toggleBatchKey(record.id, e.target.checked)}
                          />
                        )}
                        <Tooltip title={record.status === 1 ? '点击禁用' : '点击启用'}>
                          <button
                            type="button"
                            onClick={(e) => { e.stopPropagation(); handleToggleStatus(record); }}
                            style={{
                              width: 8,
                              height: 8,
                              borderRadius: '50%',
                              border: 'none',
                              padding: 0,
                              flexShrink: 0,
                              cursor: 'pointer',
                              backgroundColor: record.status === 1 ? (isLight ? '#18181b' : '#f4f4f5') : (isLight ? '#d1d5db' : 'rgba(255,255,255,0.28)'),
                            }}
                            aria-label={record.status === 1 ? '禁用' : '启用'}
                          />
                        </Tooltip>
                        <Tooltip title={`${record.name}${record.group_aid ? ` · AID ${record.group_aid}` : ''}（双击编辑）`}>
                          <Text
                            strong
                            onClick={() => handleEdit(record)}
                            style={{
                              flex: 1,
                              minWidth: 0,
                              fontSize: 13,
                              lineHeight: '20px',
                              whiteSpace: 'nowrap',
                              overflow: 'hidden',
                              textOverflow: 'ellipsis',
                              color: isLight ? '#111827' : '#f3f4f6',
                              cursor: 'pointer',
                            }}
                          >
                            {record.name}
                          </Text>
                        </Tooltip>
                      </div>

                      {/* 标签行：上游 + 绑定模型 + 等级 */}
                      <div style={{ display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'wrap', minHeight: 20 }}>
                        {upstreamTag}
                        {renderBoundModels(record, true)}
                        {levelMode === 'all' ? (
                          <Tag style={{ margin: 0, padding: '0 5px', fontSize: 10, lineHeight: '18px', borderRadius: 4, background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)', color: isLight ? '#18181b' : '#f4f4f5', border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)' }}>全部等级</Tag>
                        ) : (
                          <>
                            {levelMode === 'exclude' && (
                              <Tag style={{ margin: 0, padding: '0 5px', fontSize: 10, lineHeight: '18px', borderRadius: 4, background: isLight ? '#18181b' : '#f4f4f5', color: isLight ? '#ffffff' : '#18181b', border: 'none', fontWeight: 600 }}>排除</Tag>
                            )}
                            {visibleLevels.map((id: string) => {
                              const meta = resolveUserLevelMeta(id);
                              const isInvalid = !meta.exists || meta.isDisabled;
                              return (
                                <Tooltip key={id} title={meta.tagText}>
                                  <Tag
                                    style={{
                                      margin: 0,
                                      padding: '0 5px',
                                      fontSize: 10,
                                      lineHeight: '18px',
                                      borderRadius: 4,
                                      maxWidth: isInvalid ? 110 : 72,
                                      overflow: 'hidden',
                                      textOverflow: 'ellipsis',
                                      background: (levelMode === 'exclude' || isInvalid) ? 'transparent' : (isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)'),
                                      color: isInvalid ? (isLight ? '#a1a1aa' : '#71717a') : (levelMode === 'exclude' ? (isLight ? '#71717a' : '#a1a1aa') : (isLight ? '#27272a' : '#e4e4e7')),
                                      border: (levelMode === 'exclude' || isInvalid) ? (isLight ? '1px dashed #d4d4d8' : '1px dashed #3f3f46') : (isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)')
                                    }}
                                  >
                                    {meta.tagText}
                                  </Tag>
                                </Tooltip>
                              );
                            })}
                            {moreLevels > 0 && (
                              <Tooltip title={levelIds.slice(2).map(resolveName).join('、')}>
                                <Tag style={{ margin: 0, padding: '0 5px', fontSize: 10, lineHeight: '18px', borderRadius: 4, background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.06)', color: isLight ? '#71717a' : '#a1a1aa', border: isLight ? '1px solid #e4e4e7' : '1px solid rgba(255,255,255,0.1)' }}>+{moreLevels}</Tag>
                              </Tooltip>
                            )}
                          </>
                        )}
                      </div>

                      {/* 元信息一行 */}
                      <div
                        style={{
                          display: 'flex',
                          alignItems: 'center',
                          gap: 6,
                          fontSize: 11,
                          color: isLight ? 'rgba(0,0,0,0.4)' : 'rgba(255,255,255,0.4)',
                          lineHeight: 1.2,
                          minHeight: 16,
                        }}
                      >
                        <span>优先级 {record.priority || 0}</span>
                        <span style={{ opacity: 0.45 }}>·</span>
                        <span
                          style={{ display: 'inline-flex', alignItems: 'center', gap: 3 }}
                          onClick={(e) => e.stopPropagation()}
                        >
                          <span>排序</span>
                          <Text
                            editable={{
                              tooltip: false,
                              text: String(record.sort_order ?? 0),
                              onChange: (newVal) => {
                                const num = parseInt(newVal.trim(), 10);
                                handleUpdateSortOrder(record.id, isNaN(num) ? 0 : num);
                              },
                            }}
                            style={{
                              fontFamily: 'monospace',
                              fontSize: 11,
                              fontWeight: 600,
                              padding: '0 5px',
                              borderRadius: 4,
                              background: (record.sort_order ?? 0) > 0 ? 'rgba(59, 130, 246, 0.08)' : 'rgba(128, 128, 128, 0.06)',
                              color: (record.sort_order ?? 0) > 0 ? '#2563eb' : 'var(--text-secondary, #8c8c8c)',
                              border: (record.sort_order ?? 0) > 0 ? '1px solid rgba(59, 130, 246, 0.2)' : '1px solid rgba(128, 128, 128, 0.15)',
                              cursor: 'pointer',
                              lineHeight: '16px',
                            }}
                          >
                            {record.sort_order ?? 0}
                          </Text>
                        </span>
                        {categoryName && (
                          <>
                            <span style={{ opacity: 0.45 }}>·</span>
                            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: 72 }}>{categoryName}</span>
                          </>
                        )}
                      </div>

                      {/* 熔断条（仅有熔断时） */}
                      {hasMeltdown && (() => {
                        const meltdown = meltdownMap[record.id];
                        const meltedSubs = (meltdown.sub_channels || []).filter((s: any) => s.is_melted);
                        const totalSubs = (meltdown.sub_channels || []).length;
                        return (
                          <div
                            style={{
                              display: 'flex',
                              alignItems: 'center',
                              justifyContent: 'space-between',
                              gap: 6,
                              background: isLight ? 'rgba(255,77,79,0.05)' : 'rgba(255,77,79,0.1)',
                              borderRadius: 6,
                              padding: '4px 8px',
                            }}
                          >
                            <div style={{ display: 'flex', alignItems: 'center', gap: 5, minWidth: 0 }}>
                              <div className="meltdown-pulse-dot" style={{ width: 6, height: 6, borderRadius: '50%', backgroundColor: '#ff4d4f', flexShrink: 0 }} />
                              <Text style={{ fontSize: 11, color: '#ff4d4f', fontWeight: 500 }} ellipsis>
                                熔断 {meltedSubs.length}/{totalSubs}
                              </Text>
                            </div>
                            <Popconfirm
                              title="确定重置所有熔断？"
                              onConfirm={() => handleResetMeltdown(record.id)}
                              okText="重置"
                              cancelText="取消"
                            >
                              <Button type="link" size="small" danger loading={meltdownLoading[record.id]} style={{ padding: 0, height: 18, fontSize: 11 }}>
                                重置
                              </Button>
                            </Popconfirm>
                          </div>
                        );
                      })()}

                      {/* 额度环图（仅已配置） */}
                      <div style={{ marginTop: 'auto' }}>
                        {renderQuotaRings(record)}
                      </div>

                      {/* 底栏：AID + 操作 */}
                      <div
                        className="channel-dash-actions"
                        style={{
                          display: 'flex',
                          alignItems: 'center',
                          justifyContent: 'space-between',
                          gap: 6,
                          marginTop: 2,
                          paddingTop: 6,
                          borderTop: isLight ? '1px solid rgba(0,0,0,0.06)' : '1px solid rgba(255,255,255,0.08)',
                        }}
                        onClick={(e) => e.stopPropagation()}
                      >
                        <Text
                          type="secondary"
                          style={{
                            flex: 1,
                            minWidth: 0,
                            fontSize: 11,
                            fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                            opacity: 0.75,
                            overflow: 'hidden',
                            textOverflow: 'ellipsis',
                            whiteSpace: 'nowrap',
                            margin: 0,
                          }}
                        >
                          {record.group_aid ? `AID ${record.group_aid}` : '—'}
                        </Text>
                        <div style={{ display: 'flex', alignItems: 'center', gap: 2, flexShrink: 0 }}>
                          {isHa && (
                            <Tooltip title="重置熔断">
                              <Button
                                className="channel-dash-action-btn"
                                type="text"
                                size="small"
                                icon={<ReloadOutlined style={{ fontSize: 12, color: isLight ? '#52525b' : '#a1a1aa' }} />}
                                loading={meltdownLoading[record.id]}
                                onClick={() => handleResetMeltdown(record.id)}
                              />
                            </Tooltip>
                          )}
                          <Tooltip title="测试">
                            <Button
                              className="channel-dash-action-btn"
                              type="text"
                              size="small"
                              icon={<ExperimentOutlined style={{ fontSize: 12 }} />}
                              onClick={() => handleTest(record)}
                            />
                          </Tooltip>
                          <Tooltip title={t('channels.simulate_billing_short', '计费模拟')}>
                            <Button
                              className="channel-dash-action-btn"
                              type="text"
                              size="small"
                              icon={<CalculatorOutlined style={{ fontSize: 12 }} />}
                              onClick={() => setSimulatingChannel(record)}
                            />
                          </Tooltip>
                          <Popconfirm
                            title="确定清零该渠道的总/日/周/月已用额度吗？"
                            onConfirm={() => handleResetQuota(record.id)}
                          >
                            <Tooltip title="清零额度">
                              <Button
                                className="channel-dash-action-btn"
                                type="text"
                                size="small"
                                icon={<ClearOutlined style={{ fontSize: 12 }} />}
                              />
                            </Tooltip>
                          </Popconfirm>
                          <Tooltip title="编辑">
                            <Button
                              className="channel-dash-action-btn"
                              type="text"
                              size="small"
                              icon={<EditOutlined style={{ fontSize: 12 }} />}
                              onClick={() => handleEdit(record)}
                            />
                          </Tooltip>
                          <Popconfirm title={t('common.confirm_delete')} onConfirm={() => handleDelete(record.id)}>
                            <Tooltip title="删除">
                              <Button
                                className="channel-dash-action-btn"
                                type="text"
                                size="small"
                                danger
                                icon={<DeleteOutlined style={{ fontSize: 12 }} />}
                              />
                            </Tooltip>
                          </Popconfirm>
                        </div>
                      </div>
                    </Card>
                  </List.Item>
                );
              }}
            />
          )}
        </div>
      ) : (
        <div style={{ animation: 'fadeIn 0.3s' }}>
          <div style={{ display: 'flex', alignItems: 'center', marginBottom: 24, gap: 16 }}>
            <Button icon={<ArrowLeftOutlined />} onClick={() => handleCloseModal()}>返回</Button>
            <Title level={3} style={{ margin: 0 }}>
              {editingChannel ? t('channels.edit_channel') : t('channels.add_channel')}
            </Title>
            {editingChannel && (
              <Button
                icon={<CalculatorOutlined />}
                onClick={() => setSimulatingChannel(editingChannel)}
                style={{ marginLeft: 'auto' }}
              >
                {t('channels.simulate_billing_short', '计费模拟')}
              </Button>
            )}
          </div>
          <div style={{ maxWidth: 1600, width: '100%' }}>
            <Spin spinning={isEditRoute && !editingChannel} size="large">
              <Form
                form={form}
                layout="vertical"
                onFinish={handleSave}
                onFinishFailed={(info) => {
                  const nameErr = info.errorFields?.find((f) => f.name?.[0] === 'name');
                  if (nameErr) message.error(nameErr.errors?.[0] || '请填写名称');
                }}
                preserve={true}
              >
              <Row gutter={24}>
                {/* 左侧基本配置栏 */}
                <Col xs={24} md={10} xl={10}>
                  <div style={{ padding: 16, background: isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)', borderRadius: 8, height: '100%', position: 'sticky', top: 24 }}>
                    <Form.Item
                      name="name"
                      className="channel-name-form-item"
                      colon={false}
                      labelCol={{ style: { width: '100%' } }}
                      label={
                        <div className="channel-name-label-row" style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', width: '100%', gap: 8 }}>
                          <Space size={8}>
                            <Text strong>{t('channels.name')}</Text>
                            {editingChannel?.group_aid && (
                              <Text type="secondary" style={{ fontSize: 12 }}>AID: {editingChannel.group_aid}</Text>
                            )}
                          </Space>
                          <Button
                            size="small"
                            htmlType="button"
                            icon={<ExperimentOutlined />}
                            loading={submitting}
                            onClick={handleOpenEditorAnalysis}
                            style={{
                              height: 24,
                              padding: '0 8px',
                              fontSize: 12,
                              display: 'inline-flex',
                              alignItems: 'center',
                              borderRadius: 4,
                              marginLeft: 'auto',
                              flexShrink: 0,
                            }}
                          >
                            测试
                          </Button>
                        </div>
                      }
                      rules={[{ required: true, whitespace: true, message: '请填写名称' }]}
                    >
                      <Input placeholder="e.g. OpenAI Primary" />
                    </Form.Item>

                    <Row gutter={16}>
                      <Col span={8}>
                        <Form.Item name="sort_order" label={<Text strong>页面排序</Text>} initialValue={0}>
                          <InputNumber min={0} max={9999} style={{ width: '100%' }} placeholder="越大越靠前" />
                        </Form.Item>
                      </Col>
                      <Col span={10}>
                        <Form.Item name="category_id" label={<Text strong>渠道分类</Text>}>
                          <Select
                            allowClear
                            placeholder="选择分类"
                            onChange={(v) => { categoryIdRef.current = v ?? null; }}
                            options={categories
                              .filter(c => !!c.is_active || c.id === form.getFieldValue('category_id') || c.id === editingChannel?.category_id)
                              .map(c => ({ label: c.name, value: c.id }))}
                            dropdownRender={(menu) => (
                              <>
                                {menu}
                                <Divider style={{ margin: '8px 0' }} />
                                <Button
                                  type="link"
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
                      </Col>
                      <Col span={6}>
                        <Form.Item
                          name="status"
                          label={<Text strong>状态</Text>}
                          valuePropName="checked"
                          getValueProps={(value) => ({ checked: (value ?? 1) === 1 })}
                          getValueFromEvent={(checked: boolean) => (checked ? 1 : 0)}
                          normalize={(value) => (value ? 1 : 0)}
                          initialValue={1}
                        >
                          <Switch checkedChildren="启用" unCheckedChildren="禁用" style={{ marginTop: 4 }} />
                        </Form.Item>
                      </Col>
                    </Row>

                    <Form.Item name="provider_type" style={{ display: 'none' }}><Input /></Form.Item>
                    <Form.Item name="preset_id" style={{ display: 'none' }}><Input /></Form.Item>
                    <Form.Item name="api_key" style={{ display: 'none' }}><Input /></Form.Item>
                    <Form.Item name="base_url" style={{ display: 'none' }}><Input /></Form.Item>

                    <Form.Item shouldUpdate={(prev, curr) => 
                      prev.preset_id !== curr.preset_id || 
                      prev.provider_type !== curr.provider_type ||
                      prev.api_key !== curr.api_key
                    } noStyle>
                      {() => {
                        const providerType = form.getFieldValue('provider_type');
                        const currentPreset = form.getFieldValue('preset_id');
                        const isActive = activeRightPanel === 'presets';

                        let displayType = '无';
                        let displayName = '未选择';
                        let displayDetail: React.ReactNode = '';
                        let displayRate: React.ReactNode = null;

                        if (providerType === 'high_availability_group') {
                          displayType = '高可用组';
                          displayName = '高可用虚拟渠道组';
                          displayDetail = `已绑定 ${selectedSubChannelAids.length} 个渠道`;
                        } else if (currentPreset) {
                          const preset = presets.find(p => p.id === currentPreset);
                          displayType = '预设渠道';
                          displayName = preset ? preset.name : '未知预设';
                          displayDetail = preset?.yid ? `YID: ${preset.yid}` : `ID: ${currentPreset}`;
                          if (preset) {
                            displayRate = (
                              <Space size={4} style={{ alignItems: 'center' }}>
                                {(preset.status ?? 1) !== 1 && <Tag style={{ margin: 0, borderRadius: 4, fontSize: 10, padding: '0 4px', lineHeight: '18px', background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#71717a' : '#a1a1aa', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>已禁用</Tag>}
                                {renderUpstreamScheduleTags(preset, 'xs', true)}
                              </Space>
                            );
                          }
                        } else if (providerType === 'volcengine') {
                          displayType = '封装';
                          const credId = configObj.volcengine_enhance_credential_id;
                          const cred = volcengineEnhanceKeys.find(k => k.id === credId);
                          displayName = cred ? cred.name : '封装密钥';
                          displayDetail = cred ? `基址: ${cred.base_url || '-'}` : '';
                        } else if (providerType === 'comfyui') {
                          displayType = 'ComfyUI';
                          const comfyIds = parseComfyuiServerIds(configObj);
                          const dispatchName = comfyuiDispatchRules.find((r) => r.code === (configObj.comfyui_dispatch || COMFYUI_DISPATCH_DEFAULT))?.name
                            || '权重优先';
                          if (comfyIds.length > 1) {
                            displayName = `已选 ${comfyIds.length} 个服务节点`;
                            displayDetail = dispatchName;
                          } else {
                            const sid = comfyIds[0];
                            const server = comfyuiServers.find(s => s.id === sid);
                            displayName = server ? server.name : (sid ? `节点 #${sid}` : '未选择服务节点');
                            displayDetail = server?.base_url ? `基址: ${server.base_url}` : (sid ? dispatchName : '');
                          }
                        }

                        return (
                          <div 
                            onClick={() => setActiveRightPanel('presets')} 
                            style={{ 
                              padding: '12px 16px', 
                              borderRadius: 8, 
                              border: isActive ? '1px solid var(--text)' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), 
                              background: isActive ? (isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)') : (isLight ? '#fff' : 'rgba(255,255,255,0.02)'), 
                              cursor: 'pointer', 
                              transition: 'all 0.2s',
                              marginBottom: 12
                            }}
                          >
                            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                              <Text strong={isActive} style={{ color: isActive ? 'var(--text)' : 'inherit' }}>
                                <span style={{ color: '#ff4d4f', marginRight: 4, fontFamily: 'SimSun, sans-serif' }}>*</span>
                                选择上游渠道
                              </Text>
                              <span style={{ fontSize: 12, color: isActive ? 'var(--text)' : 'var(--text-secondary)' }}>
                                {displayType !== '无' ? (
                                  <Tag
                                    style={{
                                      margin: 0,
                                      borderRadius: 4,
                                      background: isLight ? '#f4f4f5' : '#27272a',
                                      color: isLight ? '#18181b' : '#f4f4f5',
                                      border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}`
                                    }}
                                  >
                                    {displayType}
                                  </Tag>
                                ) : '未选择'}
                                <ArrowRightOutlined style={{ marginLeft: 4 }} />
                              </span>
                            </div>
                            {displayType !== '无' && (
                              <>
                                <div style={{ marginTop: 8, padding: '6px 8px', background: isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.05)', borderRadius: 4, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                                  <span style={{ fontSize: 12, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1, marginRight: 8 }}>
                                    {displayName}
                                  </span>
                                  <Space size={6} style={{ flexShrink: 0, alignItems: 'center' }}>
                                    {displayRate}
                                    <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>{displayDetail}</span>
                                    <Button 
                                      type="text" 
                                      size="small" 
                                      icon={<CloseOutlined style={{ fontSize: 10 }} />} 
                                      style={{ width: 20, height: 20, minWidth: 20, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text-secondary)', margin: 0, padding: 0 }}
                                      onClick={(e) => {
                                        e.stopPropagation();
                                        setSelectedSubChannelAids([]);
                                        form.setFieldsValue({
                                          preset_id: null,
                                          provider_type: 'custom'
                                        });
                                        setConfigObj(prev => {
                                          const { volcengine_enhance_credential_id, comfyui_workflow_id, comfyui_server_id, comfyui_server_ids, comfyui_dispatch, ...rest } = prev;
                                          return rest;
                                        });
                                        setUpstreamTab('preset');
                                      }}
                                    />
                                  </Space>
                                </div>
                                {providerType === 'high_availability_group' && selectedSubChannelAids.length > 0 && (
                                  <div style={{ marginTop: 6, display: 'flex', flexDirection: 'column', gap: 4 }} onClick={(e) => e.stopPropagation()}>
                                    {presets
                                      .filter(p => selectedSubChannelAids.includes(p.id))
                                      .sort((a, b) => getEffectiveUpstreamSchedule(b, configObj).priority - getEffectiveUpstreamSchedule(a, configObj).priority)
                                      .map((p) => {
                                        const eff = getEffectiveUpstreamSchedule(p, configObj);
                                        const isGroupEnabled = eff.isGroupEnabled;
                                        const meltInfo = editingChannel
                                          ? (meltdownMap[editingChannel.id]?.sub_channels || []).find((s: any) => Number(s.config_id) === p.id)
                                          : undefined;
                                        const isMelted = isGroupEnabled && !!meltInfo?.is_melted;
                                        const isDemoted = isMelted && !!meltInfo?.is_demoted;
                                        const remain = Number(meltInfo?.remaining_seconds || 0);
                                        const loadKey = editingChannel ? `${editingChannel.id}-${p.id}` : '';
                                        return (
                                        <div 
                                          key={p.id} 
                                          style={{ 
                                            display: 'flex', 
                                            flexWrap: 'wrap',
                                            alignItems: 'center',
                                            justifyContent: 'space-between',
                                            rowGap: 3,
                                            columnGap: 8,
                                            padding: '4px 8px', 
                                            background: isMelted
                                              ? (isDemoted
                                                  ? (isLight ? 'rgba(250, 140, 22, 0.08)' : 'rgba(250, 140, 22, 0.12)')
                                                  : (isLight ? 'rgba(255, 77, 79, 0.08)' : 'rgba(255, 77, 79, 0.12)'))
                                              : (isLight ? 'rgba(0,0,0,0.015)' : 'rgba(255,255,255,0.015)'),
                                            borderRadius: 4,
                                            border: isMelted
                                              ? (isDemoted ? '1px solid rgba(250, 140, 22, 0.45)' : '1px solid rgba(255, 77, 79, 0.45)')
                                              : (isLight ? '1px dashed rgba(0,0,0,0.06)' : '1px dashed rgba(255,255,255,0.06)'),
                                            opacity: (p.status ?? 1) !== 1 || !isGroupEnabled ? 0.72 : 1,
                                          }}
                                        >
                                          <div style={{ display: 'flex', alignItems: 'center', overflow: 'hidden', maxWidth: '100%', lineHeight: '16px' }}>
                                            <span style={{ fontSize: 12, fontWeight: 500, lineHeight: '16px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: isMelted ? (isDemoted ? '#fa8c16' : '#ff4d4f') : (!isGroupEnabled ? 'var(--text-secondary)' : undefined) }} title={p.name}>
                                              {p.name}
                                            </span>
                                            {p.yid && (
                                              <Typography.Text keyboard style={{ color: isMelted ? (isLight ? '#18181b' : '#f4f4f5') : 'var(--text-secondary)', fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '14px', marginLeft: 6, flexShrink: 0 }}>YID: {p.yid}</Typography.Text>
                                            )}
                                            {(p.status ?? 1) !== 1 && (
                                              <Tag style={{ margin: '0 0 0 6px', fontSize: 10, lineHeight: '14px', height: 16, padding: '0 4px', borderRadius: 2, flexShrink: 0, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#71717a' : '#a1a1aa', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>已禁用</Tag>
                                            )}
                                            {!isGroupEnabled && (
                                              <Tag color="warning" style={{ margin: '0 0 0 6px', fontSize: 10, lineHeight: '14px', height: 16, padding: '0 4px', borderRadius: 2, flexShrink: 0 }}>本组已关</Tag>
                                            )}
                                            {isMelted && (
                                              <Tag color={isDemoted ? 'warning' : 'error'} style={{ margin: '0 0 0 6px', fontSize: 10, lineHeight: '14px', height: 16, padding: '0 4px', borderRadius: 2, flexShrink: 0 }}>
                                                {isDemoted ? (remain > 0 ? `降级 ${formatMeltdownRemain(remain)}` : '降级待探') : `熔断 ${formatMeltdownRemain(remain)}`}
                                              </Tag>
                                            )}
                                          </div>
                                          <div style={{ display: 'flex', flexShrink: 0, alignItems: 'center', justifyContent: 'flex-end', gap: 4, flexWrap: 'wrap', marginLeft: 'auto', lineHeight: '16px' }}>
                                            {renderUpstreamScheduleTags(p, 'xs')}
                                            <Tooltip title={isGroupEnabled ? '当前分组已启用该上游（熔断遵循高可用策略），点击关闭后不参与本组调用' : '当前分组已关闭该上游（不参与调用），点击在当前分组启用'}>
                                              <Switch
                                                size="small"
                                                checked={isGroupEnabled}
                                                loading={!!subMeltLoading[loadKey]}
                                                style={{ transform: 'scale(0.78)', transformOrigin: 'center' }}
                                                onChange={(checked) => handleToggleSubChannelStatus(editingChannel?.id, p.id, checked)}
                                              />
                                            </Tooltip>
                                            <Button
                                              type="text"
                                              size="small"
                                              icon={<EditOutlined style={{ fontSize: 11 }} />}
                                              style={{ width: 18, height: 16, minWidth: 18, display: 'flex', alignItems: 'center', justifyContent: 'center', margin: 0, padding: 0 }}
                                              onClick={(e) => {
                                                e.stopPropagation();
                                                handleQuickEditPreset(p);
                                              }}
                                            />
                                            <Button
                                              type="text"
                                              size="small"
                                              danger
                                              icon={<CloseOutlined style={{ fontSize: 10 }} />}
                                              style={{ width: 18, height: 16, minWidth: 18, display: 'flex', alignItems: 'center', justifyContent: 'center', margin: 0, padding: 0 }}
                                              onClick={(e) => {
                                                e.stopPropagation();
                                                const next = selectedSubChannelAids.filter((id: number) => id !== p.id);
                                                setSelectedSubChannelAids(next);
                                                setConfigObj(prev => ({ ...prev, sub_channels: next }));
                                              }}
                                            />
                                          </div>
                                        </div>
                                        );
                                      })
                                    }
                                  </div>
                                )}
                                {providerType === 'comfyui' && parseComfyuiServerIds(configObj).length > 0 && (
                                  <div style={{ marginTop: 6, display: 'flex', flexDirection: 'column', gap: 4 }} onClick={(e) => e.stopPropagation()}>
                                    {parseComfyuiServerIds(configObj).map((id) => {
                                      const s = comfyuiServers.find((x) => x.id === id);
                                      return (
                                        <div
                                          key={id}
                                          style={{
                                            display: 'flex',
                                            justifyContent: 'space-between',
                                            alignItems: 'center',
                                            padding: '4px 8px',
                                            background: isLight ? 'rgba(0,0,0,0.015)' : 'rgba(255,255,255,0.015)',
                                            borderRadius: 4,
                                            border: isLight ? '1px dashed rgba(0,0,0,0.06)' : '1px dashed rgba(255,255,255,0.06)',
                                            opacity: s && s.is_active !== 1 ? 0.75 : 1,
                                          }}
                                        >
                                          <span style={{ fontSize: 11, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                            {s?.name || `节点 #${id}`}
                                          </span>
                                          <Space size={4} style={{ flexShrink: 0, alignItems: 'center' }}>
                                            <Tag style={{ margin: 0, fontSize: 10, lineHeight: '14px', height: 16, padding: '0 4px', borderRadius: 2, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#18181b' : '#f4f4f5', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>优先级: {s?.priority ?? 0}</Tag>
                                            <Tag style={{ margin: 0, fontSize: 10, lineHeight: '14px', height: 16, padding: '0 4px', borderRadius: 2, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#18181b' : '#f4f4f5', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>权重: {s?.weight ?? 1}</Tag>
                                            <Tooltip title="移除此节点">
                                              <Button
                                                type="text"
                                                size="small"
                                                danger
                                                icon={<CloseOutlined style={{ fontSize: 10 }} />}
                                                style={{ width: 20, height: 20, minWidth: 20, display: 'flex', alignItems: 'center', justifyContent: 'center', margin: 0, padding: 0 }}
                                                onClick={(e) => {
                                                  e.stopPropagation();
                                                  const next = parseComfyuiServerIds(configObj).filter((x) => x !== id);
                                                  form.setFieldsValue({
                                                    preset_id: null,
                                                    provider_type: next.length ? 'comfyui' : 'custom',
                                                  });
                                                  setConfigObj(prev => {
                                                    const { volcengine_enhance_credential_id, comfyui_workflow_id, comfyui_server_id, comfyui_server_ids, ...rest } = prev;
                                                    return {
                                                      ...rest,
                                                      comfyui_server_ids: next,
                                                      comfyui_server_id: next[0],
                                                      comfyui_dispatch: prev.comfyui_dispatch || COMFYUI_DISPATCH_DEFAULT,
                                                    };
                                                  });
                                                }}
                                              />
                                            </Tooltip>
                                          </Space>
                                        </div>
                                      );
                                    })}
                                  </div>
                                )}
                              </>
                            )}
                          </div>
                        );
                      }}
                    </Form.Item>


                    <Form.Item label={<Text strong>路由与范围配置</Text>} style={{ marginBottom: 0 }}>
                      <Space direction="vertical" style={{ width: '100%' }} size={12}>
                        {/* Models */}
                        <Form.Item shouldUpdate={(prev, curr) => prev.models !== curr.models} noStyle>
                           {() => {
                            const m = form.getFieldValue('models') || [];
                            const isActive = activeRightPanel === 'models';

                            return (
                              <div onClick={() => setActiveRightPanel('models')} style={{ padding: '12px 16px', borderRadius: 8, border: isActive ? '1px solid var(--text)' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), background: isActive ? (isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)') : (isLight ? '#fff' : 'rgba(255,255,255,0.02)'), cursor: 'pointer', transition: 'all 0.2s' }}>
                                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: m.length > 0 ? 8 : 0 }}>
                                  <Text strong={isActive} style={{ color: isActive ? 'var(--text)' : 'inherit' }}>选择模型</Text>
                                  <span style={{ fontSize: 12, color: isActive ? 'var(--text)' : 'var(--text-secondary)' }}>已选 {m.length} 个 <ArrowRightOutlined style={{ marginLeft: 4 }} /></span>
                                </div>
                                {m.length > 0 && (
                                  <div style={{ display: 'flex', flexDirection: 'column', gap: 6, maxHeight: 300, overflowY: 'auto', paddingRight: 4 }}>
                                    {m.map((mid: string) => {
                                      const match = availableModels.find((model: any) => model.mid === mid);
                                      const archived = match ? undefined : unlistedByKey.get(String(mid));
                                      const delisted = !match;
                                      const label = match ? match.name : (archived?.model_id || archived?.name || mid);
                                      const remark = match?.remark || archived?.remark;
                                      const shownMid = match?.mid || archived?.mid || mid;
                                      const mutedTagStyle: React.CSSProperties = { borderRadius: 4, margin: 0, padding: '0 4px', fontSize: 10, lineHeight: '18px', marginLeft: 8, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#71717a' : '#a1a1aa', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` };
                                      return (
                                        <div key={mid} style={{ padding: '6px 8px', background: isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.05)', borderRadius: 4, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                                          <span style={{ fontSize: 12, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1, marginRight: 8, color: delisted ? 'var(--text-secondary)' : undefined }}>
                                            {label}
                                            {remark ? <span style={{ color: 'var(--text-secondary)', fontWeight: 'normal', marginLeft: 4 }}>({remark})</span> : null}
                                            {delisted && <Tag style={mutedTagStyle}>已下架</Tag>}
                                            {match && match.is_active === 0 && <Tag style={mutedTagStyle}>已禁用</Tag>}
                                          </span>
                                          <Space size={4} style={{ flexShrink: 0 }}>
                                            <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>MID: {shownMid}</span>
                                            <Button 
                                              type="text" 
                                              size="small" 
                                              icon={<CloseOutlined style={{ fontSize: 10 }} />} 
                                              style={{ width: 20, height: 20, minWidth: 20, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text-secondary)', margin: 0, padding: 0 }}
                                              onClick={(e) => {
                                                e.stopPropagation();
                                                const next = m.filter((id: string) => id !== mid);
                                                handleModelsChange(next);
                                              }}
                                            />
                                          </Space>
                                        </div>
                                      );
                                    })}
                                  </div>
                                )}
                              </div>
                            );
                          }}
                        </Form.Item>

                        {/* Model Mapping */}
                        <Form.Item shouldUpdate noStyle>
                          {() => {
                            const mapping = modelMappingState || {};
                            const isActive = activeRightPanel === 'mapping';
                            const providerType = form.getFieldValue('provider_type');
                            const isHaMode = providerType === 'high_availability_group';
                            const mappedEntries = isHaMode
                              ? []
                              : Object.entries(mapping).filter(([_, v]) => v && String(v).trim());
                            const boundSubIdSet = new Set(selectedSubChannelAids.map(String));
                            // 统计 HA 子渠道独立映射数量（仅统计当前已绑定的子渠道）
                            const haMappingCount = isHaMode
                              ? Object.values(haModelMappingState).reduce(
                                  (sum, subMap) =>
                                    sum +
                                    Object.entries(subMap || {}).filter(
                                      ([sid, v]) => boundSubIdSet.has(sid) && isFilled(v),
                                    ).length,
                                  0,
                                )
                              : 0;
                            const resMappingCount = Object.values(resModelMappingState).reduce(
                              (sum, scopes) =>
                                sum +
                                Object.entries(scopes || {}).reduce((s, [scopeKey, m]) => {
                                  if (isHaMode) {
                                    return boundSubIdSet.has(scopeKey) ? s + countFilled(m) : s;
                                  }
                                  return scopeKey === 'default' ? s + countFilled(m) : s;
                                }, 0),
                              0,
                            );
                            const primaryCount = isHaMode ? haMappingCount : mappedEntries.length;
                            return (
                              <div onClick={() => setActiveRightPanel('mapping')} style={{ padding: '12px 16px', borderRadius: 8, border: isActive ? '1px solid var(--text)' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), background: isActive ? (isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)') : (isLight ? '#fff' : 'rgba(255,255,255,0.02)'), cursor: 'pointer', transition: 'all 0.2s' }}>
                                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: (showMapping && (primaryCount > 0 || resMappingCount > 0)) ? 8 : 0 }}>
                                  <Text strong={isActive} style={{ color: isActive ? 'var(--text)' : 'inherit' }}>模型别名映射</Text>
                                  <span style={{ fontSize: 12, color: isActive ? 'var(--text)' : 'var(--text-secondary)' }}>
                                    {showMapping ? <span style={{ fontWeight: 500 }}>已开启 {(primaryCount + resMappingCount) > 0 ? `(${primaryCount}${resMappingCount > 0 ? `+R${resMappingCount}` : ''})` : ''}</span> : <span>未开启</span>} <ArrowRightOutlined style={{ marginLeft: 4 }} />
                                  </span>
                                </div>
                                {showMapping && mappedEntries.length > 0 && (
                                  <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                                    {mappedEntries.slice(0, 8).map(([k, v]) => {
                                      const label = selectionModelId(availableModels, k);
                                      return (
                                      <div key={k} style={{ padding: '6px 8px', background: isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.05)', borderRadius: 4, display: 'flex', alignItems: 'center' }}>
                                        <span style={{ fontSize: 12, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1, marginRight: 8 }} title={label}>{label}</span>
                                        <span style={{ fontSize: 11, color: 'var(--text-secondary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1, marginRight: 8, textAlign: 'right' }} title={String(v)}>➔ {String(v)}</span>
                                        <Button 
                                          type="text" 
                                          size="small" 
                                          icon={<CloseOutlined style={{ fontSize: 10 }} />} 
                                          style={{ width: 20, height: 20, minWidth: 20, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text-secondary)', margin: 0, padding: 0, flexShrink: 0 }}
                                          onClick={(e) => {
                                            e.stopPropagation();
                                            setActiveMappingInputs(prev => prev.filter(id => id !== k));
                                            setModelMappingState(prev => {
                                              const next = { ...prev };
                                              delete next[k];
                                              form.setFieldsValue({ model_mapping: next });
                                              return next;
                                            });
                                          }}
                                        />
                                      </div>
                                      );
                                    })}
                                    {mappedEntries.length > 8 && <div style={{ fontSize: 11, color: 'var(--text-secondary)', textAlign: 'center', marginTop: 2 }}>...还有 {mappedEntries.length - 8} 个</div>}
                                  </div>
                                )}
                                {showMapping && isHaMode && haMappingCount > 0 && (
                                  <div style={{ padding: '6px 8px', background: isLight ? '#f4f4f5' : '#27272a', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}`, borderRadius: 4, marginTop: mappedEntries.length > 0 ? 6 : 0, display: 'flex', alignItems: 'center', gap: 4 }}>
                                    <ApartmentOutlined style={{ fontSize: 11, color: isLight ? '#18181b' : '#f4f4f5' }} />
                                    <span style={{ fontSize: 11, color: isLight ? '#18181b' : '#f4f4f5', fontWeight: 500 }}>子渠道独立映射: {haMappingCount} 条</span>
                                  </div>
                                )}
                                {showMapping && resMappingCount > 0 && (
                                  <div style={{ padding: '6px 8px', background: isLight ? '#f4f4f5' : '#27272a', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}`, borderRadius: 4, marginTop: (mappedEntries.length > 0 || haMappingCount > 0) ? 6 : 0, display: 'flex', alignItems: 'center', gap: 4 }}>
                                    <SettingOutlined style={{ fontSize: 11, color: isLight ? '#18181b' : '#f4f4f5' }} />
                                    <span style={{ fontSize: 11, color: isLight ? '#18181b' : '#f4f4f5', fontWeight: 500 }}>分辨率映射: {resMappingCount} 档</span>
                                  </div>
                                )}
                              </div>
                            );
                          }}
                        </Form.Item>

                        {/* User Levels */}
                        <Form.Item shouldUpdate={(prev, curr) => prev.level_select !== curr.level_select} noStyle>
                          {() => {
                            const levels = form.getFieldValue('level_select') || [];
                            const isActive = activeRightPanel === 'levels';
                            return (
                              <div onClick={() => setActiveRightPanel('levels')} style={{ padding: '12px 16px', borderRadius: 8, border: isActive ? '1px solid var(--text)' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), background: isActive ? (isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)') : (isLight ? '#fff' : 'rgba(255,255,255,0.02)'), cursor: 'pointer', transition: 'all 0.2s' }}>
                                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: levels.length > 0 ? 8 : 0 }}>
                                  <Text strong={isActive} style={{ color: isActive ? 'var(--text)' : 'inherit' }}>{isExcludeMode ? '不支持用户等级' : '支持用户等级'}</Text>
                                  <span style={{ fontSize: 12, color: isActive ? 'var(--text)' : 'var(--text-secondary)' }}>
                                    <Tag style={{ marginRight: 4, border: 'none', background: isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.1)', color: 'var(--text)' }}>{isExcludeMode ? '排除模式' : '允许模式'}</Tag>
                                    <ArrowRightOutlined />
                                  </span>
                                </div>
                                {levels.length > 0 ? (
                                  <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                                    {levels.slice(0, 8).map((idStr: string) => {
                                      const meta = resolveUserLevelMeta(idStr);
                                      const isInvalid = !meta.exists || meta.isDisabled;
                                      const mutedTagStyle: React.CSSProperties = { borderRadius: 4, margin: 0, padding: '0 4px', fontSize: 10, lineHeight: '18px', marginLeft: 6, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#71717a' : '#a1a1aa', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` };
                                      return (
                                      <div key={idStr} style={{ padding: '6px 8px', background: isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.05)', borderRadius: 4, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                                        <span style={{ fontSize: 12, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1, marginRight: 8, color: isInvalid ? 'var(--text-secondary)' : undefined }}>
                                          {meta.exists ? `${meta.name} (${meta.discountText})` : meta.name}
                                          {!meta.exists && <Tag style={mutedTagStyle}>已删除</Tag>}
                                          {meta.exists && meta.isDisabled && <Tag style={mutedTagStyle}>已禁用</Tag>}
                                          {meta.exists && !meta.isDisabled && !meta.hasLevelDisc && <Tag style={mutedTagStyle}>倍率未启用</Tag>}
                                        </span>
                                        <Space size={4} style={{ flexShrink: 0 }}>
                                          <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>{meta.isNumeric ? `ULID: ${meta.ulid}` : `标识: ${idStr}`}</span>
                                          <Button 
                                            type="text" 
                                            size="small" 
                                            icon={<CloseOutlined style={{ fontSize: 10 }} />} 
                                            style={{ width: 20, height: 20, minWidth: 20, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text-secondary)', margin: 0, padding: 0 }}
                                            onClick={(e) => {
                                              e.stopPropagation();
                                              const next = levels.filter((id: string) => id !== idStr);
                                              form.setFieldsValue({ level_select: next });
                                              levelsRef.current = next;
                                            }}
                                          />
                                        </Space>
                                      </div>
                                      );
                                    })}
                                    {levels.length > 8 && <div style={{ fontSize: 11, color: 'var(--text-secondary)', textAlign: 'center', marginTop: 2 }}>...还有 {levels.length - 8} 个</div>}
                                  </div>
                                ) : (
                                  <Text type="secondary" style={{ fontSize: 12 }}>{isExcludeMode ? '未排除任何等级 (全部允许)' : '当前全部用户等级允许'}</Text>
                                )}
                              </div>
                            );
                          }}
                        </Form.Item>
                      </Space>
                    </Form.Item>

                    <div style={{ marginTop: 24 }}>
                      <Text strong style={{ display: 'block', marginBottom: 12 }}>调度策略</Text>
                      <Row gutter={12}>
                        <Col span={12}>
                          <Form.Item name="priority" label="优先级" initialValue={0}>
                            <InputNumber style={{ width: '100%' }} />
                          </Form.Item>
                        </Col>
                        <Col span={12}>
                          <Form.Item name="weight" label="权重" initialValue={1}>
                            <InputNumber min={1} style={{ width: '100%' }} />
                          </Form.Item>
                        </Col>
                        <Col span={24}>
                          <Form.Item label="额度配置" style={{ marginBottom: 12 }}>
                            <Switch 
                              checked={enableQuota}
                              onChange={(checked) => {
                                setEnableQuota(checked);
                                if (!checked) {
                                  form.setFieldsValue({
                                    quota_limit: -1,
                                    daily_quota_limit: -1,
                                    weekly_quota_limit: -1,
                                    monthly_quota_limit: -1,
                                  });
                                } else {
                                  form.setFieldsValue({
                                    quota_limit: -1,
                                    daily_quota_limit: -1,
                                    weekly_quota_limit: -1,
                                    monthly_quota_limit: -1,
                                  });
                                }
                              }}
                              checkedChildren="已开启限额"
                              unCheckedChildren="默认不限额"
                            />
                          </Form.Item>
                        </Col>
                        {enableQuota && (
                          <Col span={24}>
                            <Form.Item
                              noStyle
                              shouldUpdate={(prev, curr) =>
                                prev.quota_limit !== curr.quota_limit ||
                                prev.daily_quota_limit !== curr.daily_quota_limit ||
                                prev.weekly_quota_limit !== curr.weekly_quota_limit ||
                                prev.monthly_quota_limit !== curr.monthly_quota_limit
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
                                return (
                                  <Row gutter={12}>
                                    <Col span={12}>
                                      <Form.Item
                                        name="quota_limit"
                                        label="总额度"
                                        initialValue={-1}
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
                                    </Col>
                                    <Col span={12}>
                                      <Form.Item
                                        name="monthly_quota_limit"
                                        label="月额度"
                                        initialValue={-1}
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
                                    </Col>
                                    <Col span={12}>
                                      <Form.Item
                                        name="weekly_quota_limit"
                                        label="周额度"
                                        initialValue={-1}
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
                                    </Col>
                                    <Col span={12}>
                                      <Form.Item
                                        name="daily_quota_limit"
                                        label="日额度"
                                        initialValue={-1}
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
                                    </Col>
                                  </Row>
                                );
                              }}
                            </Form.Item>
                          </Col>
                        )}
                      </Row>
                      <Form.Item name="rate" initialValue={1.0} style={{ display: 'none' }}><InputNumber /></Form.Item>

                      {/* ─── 存储设置 ─── */}
                      <Divider style={{ margin: '12px 0 8px' }}>存储设置</Divider>
                      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
                        <div>
                          <Text strong style={{ fontSize: 13 }}>开启对象存储转存</Text>
                          <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 2 }}>
                            开启后，图片/视频响应资源自动上传到对象存储并返回永久 URL
                          </div>
                        </div>
                        <Switch
                          checked={configObj.tos_storage_enabled || false}
                          onChange={(v) => setConfigObj({
                            ...configObj,
                            tos_storage_enabled: v,
                            tos_storage_days: configObj.tos_storage_days ?? 1,
                            tos_storage_provider: v && multiSiteStorage
                              ? (configObj.tos_storage_provider || siteDefaultProvider)
                              : undefined,
                          })}
                        />
                      </div>
                      {configObj.tos_storage_enabled && (
                        <div style={{ marginTop: 8 }}>
                          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 10, padding: '6px 0', borderBottom: isLight ? '1px dashed #f0f0f0' : '1px dashed rgba(255,255,255,0.08)' }}>
                            <div>
                              <Text strong style={{ fontSize: 13 }}>Base64 数据转存</Text>
                              <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 2 }}>
                                开启后 Base64 转存至对象存储返回 URL，关闭则直接原样返回（若用户请求参数显式指定 response_format 则优先遵循用户意图）
                              </div>
                            </div>
                            <Switch
                              checked={configObj.tos_storage_b64_enabled !== false}
                              onChange={(v) => setConfigObj({
                                ...configObj,
                                tos_storage_b64_enabled: v,
                              })}
                            />
                          </div>
                          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, flexWrap: 'wrap' }}>
                            <Text style={{ fontSize: 13, whiteSpace: 'nowrap' }}>存储有效期</Text>
                            <InputNumber
                              min={0} max={365}
                              value={configObj.tos_storage_days ?? 1}
                              onChange={(v) => setConfigObj({ ...configObj, tos_storage_days: v })}
                              addonAfter="天"
                              style={{ width: 140 }}
                            />
                            <Text type="secondary" style={{ fontSize: 11 }}>0 = 永久保留</Text>
                          </div>
                          {multiSiteStorage && (
                            <div style={{ marginBottom: 8 }}>
                              <Text style={{ fontSize: 13, display: 'block', marginBottom: 6 }}>存储方式</Text>
                              <Radio.Group
                                value={
                                  siteStorageProviders.some((p) => p.key === configObj.tos_storage_provider)
                                    ? configObj.tos_storage_provider
                                    : siteDefaultProvider
                                }
                                onChange={(e) => setConfigObj({ ...configObj, tos_storage_provider: e.target.value })}
                                style={{ display: 'flex', flexWrap: 'wrap', gap: '8px 16px' }}
                              >
                                {siteStorageProviders.map((p) => (
                                  <Radio key={p.key} value={p.key} style={{ marginInlineEnd: 0 }}>
                                    {p.name}
                                    {p.key === siteDefaultProvider ? '（默认）' : ''}
                                  </Radio>
                                ))}
                              </Radio.Group>
                            </div>
                          )}
                          <Alert
                            type="info"
                            showIcon
                            style={{ fontSize: 12, padding: '6px 12px' }}
                            message={
                              multiSiteStorage
                                ? `凭证来自「系统设置 → 存储设置」；默认「${getStorageProvider(siteDefaultProvider).name}」，可切换。`
                                : siteStorageProviders.length === 1
                                  ? `使用站点已配置的「${siteStorageProviders[0].name}」（系统设置 → 存储设置）。`
                                  : '请先在「系统设置 → 存储设置」完成对象存储配置后再开启转存。'
                            }
                          />
                        </div>
                      )}
                    </div>
                  </div>
                </Col>

                {/* 右侧动态面板 */}
                <Col xs={24} md={14} xl={14}>
                  <div style={{ padding: 24, background: isLight ? '#fff' : 'rgba(255,255,255,0.02)', border: isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)', borderRadius: 8, minHeight: 600 }}>
                    
                    <div style={{ display: activeRightPanel === 'models' ? 'block' : 'none', animation: 'fadeIn 0.2s' }}>
                          <Form.Item name="models" style={{ marginBottom: 0 }} hidden>
                            <Select mode="multiple" />
                          </Form.Item>
                          <ModelSelector
                            selectedMids={channelModelMids}
                            onSelectionChange={handleModelsChange}
                            onModelsLoaded={(models) => setAvailableModels(models)}
                            allowDuplicateModelId={false}
                            isLightTheme={isLight}
                            title="选择模型"
                          />
                    </div>

                    <div style={{ display: activeRightPanel === 'presets' ? 'block' : 'none', animation: 'fadeIn 0.2s' }}>
                      <Form.Item shouldUpdate={(prev, curr) => 
                        prev.provider_type !== curr.provider_type || 
                        prev.preset_id !== curr.preset_id ||
                        prev.api_key !== curr.api_key
                      } noStyle>
                        {() => {
                          const providerType = form.getFieldValue('provider_type');
                          return (
                            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16, flexWrap: 'wrap', gap: 8 }}>
                              <Title level={4} style={{ margin: 0 }}>选择上游渠道</Title>
                              <Space size={8} style={{ flexWrap: 'wrap' }}>
                                {/* 按钮一：高可用虚拟渠道组 */}
                                <Tooltip title={!activePlugins['high_availability_channel'] ? '高可用上游渠道系统插件未开启' : ''}>
                                  <Button
                                    type={providerType === 'high_availability_group' ? 'primary' : 'dashed'}
                                    icon={<ApartmentOutlined />}
                                    size="small"
                                    disabled={!activePlugins['high_availability_channel'] && providerType !== 'high_availability_group'}
                                    onClick={() => {
                                      if (providerType === 'high_availability_group') {
                                        // 若已是高可用组，点击则取消选择并清理 HA 专属映射状态
                                        setSelectedSubChannelAids([]);
                                        setHaModelMappingState({});
                                        setExpandedHaModels([]);
                                        setResModelMappingState(prev => {
                                          const next: Record<string, Record<string, Record<string, string>>> = {};
                                          Object.entries(prev).forEach(([mid, scopes]) => {
                                            if (scopes?.default && Object.keys(scopes.default).length > 0) {
                                              next[mid] = { default: scopes.default };
                                            }
                                          });
                                          return next;
                                        });
                                        form.setFieldsValue({
                                          preset_id: null,
                                          provider_type: 'custom'
                                        });
                                      } else {
                                        if (!activePlugins['high_availability_channel']) {
                                          message.warning('高可用上游渠道系统插件未开启');
                                          return;
                                        }
                                        // 一键设置为高可用虚拟组，隔离并清理单渠道残留的模型别名映射
                                        setSelectedSubChannelAids([]);
                                        setModelMappingState({});
                                        setActiveMappingInputs([]);
                                        setResModelMappingState(prev => {
                                          const next: Record<string, Record<string, Record<string, string>>> = {};
                                          Object.entries(prev).forEach(([mid, scopes]) => {
                                            const rest = { ...(scopes || {}) };
                                            delete rest.default;
                                            if (Object.keys(rest).length > 0) next[mid] = rest;
                                          });
                                          return next;
                                        });
                                        setExpandedHaModels(channelModelMids.map(String));
                                        clearComfyui();
                                        clearVolcengineEnhance();
                                        form.setFieldsValue({
                                          preset_id: -99, // 对应预设 ID
                                          rate: 1.0,
                                          provider_type: 'high_availability_group',
                                          model_mapping: {},
                                        });
                                      }
                                    }}
                                  >
                                    {providerType === 'high_availability_group' ? '已开启高可用上游渠道' : '开启高可用上游渠道'}
                                  </Button>
                                </Tooltip>

                                {/* 按钮四：媒体封装渠道 (仅在启用时展示) */}
                                {activePlugins['volcengine_enhance'] && (
                                  <Button
                                    type={upstreamTab === 'volcengine_enhance' ? 'primary' : 'dashed'}
                                    icon={<SettingOutlined />}
                                    size="small"
                                    onClick={() => {
                                      if (upstreamTab === 'volcengine_enhance') {
                                        clearVolcengineEnhance();
                                      } else {
                                        clearComfyui();
                                        setUpstreamTab('volcengine_enhance');
                                        setSelectedSubChannelAids([]);
                                        form.setFieldsValue({
                                          preset_id: null,
                                          provider_type: 'custom'
                                        });
                                      }
                                    }}
                                  >
                                    {upstreamTab === 'volcengine_enhance' ? '已开启火山封装渠道' : '开启火山封装渠道'}
                                  </Button>
                                )}
                                {activePlugins['comfyui_bridge'] && (
                                  <Button
                                    type={upstreamTab === 'comfyui' ? 'primary' : 'dashed'}
                                    icon={<VideoCameraOutlined />}
                                    size="small"
                                    onClick={() => {
                                      if (upstreamTab === 'comfyui') {
                                        clearComfyui();
                                      } else {
                                        clearVolcengineEnhance();
                                        setUpstreamTab('comfyui');
                                        setSelectedSubChannelAids([]);
                                        form.setFieldsValue({
                                          preset_id: null,
                                          provider_type: 'custom'
                                        });
                                      }
                                    }}
                                  >
                                    {upstreamTab === 'comfyui' ? '已开启 ComfyUI 渠道' : '开启 ComfyUI 渠道'}
                                  </Button>
                                )}
                              </Space>
                            </div>
                          );
                        }}
                      </Form.Item>

                      <Form.Item shouldUpdate={(prev, curr) => prev.preset_id !== curr.preset_id || prev.provider_type !== curr.provider_type} noStyle>
                        {() => {
                          const providerType = form.getFieldValue('provider_type');
                          const currentPreset = form.getFieldValue('preset_id');

                          if (providerType === 'high_availability_group') {
                            const matchPresetSearch = (p: any) =>
                              p.name?.toLowerCase().includes(presetSearchText.toLowerCase()) ||
                              p.provider_type?.toLowerCase().includes(presetSearchText.toLowerCase()) ||
                              String(p.id).includes(presetSearchText) ||
                              (p.yid && String(p.yid).includes(presetSearchText));
                            const searchedPresets = presets.filter(p => p.id !== -99 && matchPresetSearch(p));
                            const statusCounts = {
                              all: searchedPresets.length,
                              active: searchedPresets.filter(p => (p.status ?? 1) === 1).length,
                              disabled: searchedPresets.filter(p => (p.status ?? 1) !== 1).length,
                            };
                            const subCandidates = searchedPresets.filter(p =>
                              upstreamStatusFilter === 'all' || (p.status ?? 1) === upstreamStatusFilter
                            );

                            return (
                              <>
                                {/* 搜索 + 状态筛选 */}
                                <div style={{ marginBottom: 10, display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
                                  <Input
                                    placeholder="输入关键字搜索上游渠道配置名称、类型或 YID/ID..."
                                    allowClear
                                    prefix={<SearchOutlined style={{ color: 'var(--text-secondary)' }} />}
                                    value={presetSearchText}
                                    onChange={(e) => setPresetSearchText(e.target.value)}
                                    style={{ flex: 1, minWidth: 200 }}
                                  />
                                  <Segmented
                                    size="small"
                                    options={[
                                      { label: `全部 (${statusCounts.all})`, value: 'all' },
                                      { label: `激活 (${statusCounts.active})`, value: '1' },
                                      { label: `禁用 (${statusCounts.disabled})`, value: '0' },
                                    ]}
                                    value={upstreamStatusFilter === 'all' ? 'all' : String(upstreamStatusFilter)}
                                    onChange={(val) => setUpstreamStatusFilter(val === 'all' ? 'all' : parseInt(val as string, 10) as 0 | 1)}
                                  />
                                </div>

                                <div style={{ marginBottom: 8 }}>
                                  <Text strong style={{ display: 'block', marginBottom: 6 }}>高可用策略模板</Text>
                                  <Select
                                    style={{ width: '100%', marginBottom: 10 }}
                                    value={configObj.rule || ''}
                                    onChange={(v: string) => {
                                      const retries = haRules.find(r => r.id === (v || haDef))?.retries || 3;
                                      const trimmed = selectedSubChannelAids.slice(0, retries);
                                      if (selectedSubChannelAids.length > retries) {
                                        message.info(`已按策略上限保留前 ${retries} 个绑定渠道`);
                                      }
                                      setSelectedSubChannelAids(trimmed);
                                      setConfigObj(prev => {
                                        const next: Record<string, any> = { ...prev, sub_channels: trimmed };
                                        if (v) next.rule = v; else delete next.rule;
                                        return next;
                                      });
                                    }}
                                    options={[
                                      { value: '', label: `使用默认（${haRules.find(r => r.id === haDef)?.name || '默认'}）` },
                                      ...haRules.map(r => ({ value: r.id, label: `${r.name}（切换 ${r.retries} 次）` })),
                                    ]}
                                  />
                                  <Alert
                                    message="高可用上游多选绑定"
                                    description={`您可以选择最多 ${haMaxRetries} 个上游配置绑定到该虚拟组（禁用渠道也可选，绑定后运行时不可用）。系统优先使用「优先级priority」最高的一组渠道；若最高优先级渠道有多个，则按它们各自的「权重weight」比例随机分流。选满 ${haMaxRetries} 个后，其余配置将被置灰。`}
                                    type="info"
                                    showIcon
                                    style={{ borderRadius: 6, padding: '6px 10px' }}
                                    styles={{
                                      icon: { marginInlineEnd: 8, fontSize: 14, marginTop: 1 },
                                      title: { marginBottom: 2, fontSize: 13, lineHeight: 1.3 },
                                      description: { fontSize: 12, lineHeight: 1.45 },
                                    }}
                                  />
                                </div>

                                <div style={{ marginBottom: 6, display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                                  <Text strong>
                                    绑定物理上游 ({selectedSubChannelAids.length} / {haMaxRetries})
                                  </Text>
                                  <Button
                                    size="small"
                                    disabled={subCandidates.length === 0}
                                    onClick={() => {
                                      const eligibleIds = subCandidates.map(c => c.id);
                                      if (eligibleIds.length === 0) {
                                        message.warning('当前列表没有可绑定的上游配置');
                                        return;
                                      }
                                      const next = eligibleIds.slice(0, haMaxRetries);
                                      setSelectedSubChannelAids(next);
                                      setConfigObj(prev => ({ ...prev, sub_channels: next }));
                                      if (eligibleIds.length > haMaxRetries) {
                                        message.info(`已按上限选中 ${haMaxRetries} 个上游配置`);
                                      }
                                    }}
                                  >
                                    全选
                                  </Button>
                                  <Button
                                    size="small"
                                    disabled={selectedSubChannelAids.length === 0}
                                    onClick={() => {
                                      setSelectedSubChannelAids([]);
                                      setConfigObj(prev => ({ ...prev, sub_channels: [] }));
                                    }}
                                  >
                                    取消选择
                                  </Button>
                                  {selectedSubChannelAids.length === 0 && (
                                    <span style={{ color: '#ff4d4f', fontSize: 12, fontWeight: 500 }}>
                                      请至少绑定一个上游配置
                                    </span>
                                  )}
                                </div>

                                {subCandidates.length === 0 ? (
                                  <div style={{ padding: '40px 0', textAlign: 'center', color: 'var(--text-secondary)', border: '1px dashed #e5e4e7', borderRadius: 8 }}>
                                    暂无可绑定的上游渠道配置（请检查并先在左侧选择或者全局配置中创建）
                                  </div>
                                ) : (
                                  <div style={{ 
                                    maxHeight: 'calc(100vh - 320px)', minHeight: '400px', 
                                    overflowY: 'auto', 
                                    background: isLight ? 'rgba(0,0,0,0.01)' : 'rgba(255,255,255,0.01)', 
                                    padding: '6px', 
                                    borderRadius: 8, 
                                    border: isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)' 
                                  }}>
                                    <Space direction="vertical" style={{ width: '100%' }} size={4}>
                                      {subCandidates.map(c => {
                                        const aid = c.id;
                                        const isChecked = selectedSubChannelAids.includes(aid);
                                        const isFull = selectedSubChannelAids.length >= haMaxRetries;
                                        const isConfigDisabled = (c.status ?? 1) !== 1;
                                        const isSlotFull = isFull && !isChecked;
                                        const eff = getEffectiveUpstreamSchedule(c, configObj);
                                        const isGroupEnabled = eff.isGroupEnabled;
                                        const loadKey = editingChannel ? `${editingChannel.id}-${aid}` : '';

                                        return (
                                          <div 
                                            key={aid} 
                                            style={{ 
                                              display: 'flex', 
                                              alignItems: 'center', 
                                              justifyContent: 'space-between',
                                              padding: '6px 10px', 
                                              borderRadius: 6,
                                              background: isChecked ? (isLight ? 'rgba(22,119,255,0.08)' : 'rgba(22,119,255,0.15)') : 'transparent',
                                              border: isChecked ? '1px solid #91caff' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'),
                                              opacity: isSlotFull ? 0.5 : (isConfigDisabled || (isChecked && !isGroupEnabled) ? 0.72 : 1),
                                              transition: 'all 0.15s'
                                            }}
                                          >
                                            <Checkbox
                                              checked={isChecked}
                                              disabled={isSlotFull}
                                              onChange={(e) => {
                                                let next = [...selectedSubChannelAids];
                                                if (e.target.checked) {
                                                  if (!next.includes(aid)) next.push(aid);
                                                } else {
                                                  next = next.filter(a => a !== aid);
                                                }
                                                setSelectedSubChannelAids(next);
                                                setConfigObj(prev => ({ ...prev, sub_channels: next }));
                                              }}
                                              style={{ flex: 1, marginRight: 8 }}
                                            >
                                              <span style={{ fontWeight: 600, fontSize: 13, color: isChecked ? 'var(--text)' : 'inherit' }}>{c.name}</span>
                                              <span style={{ fontSize: 11, color: 'var(--text-secondary)', marginLeft: 8 }}>
                                                {c.provider_type ? `(${c.provider_type})` : ''}
                                              </span>
                                              <Typography.Text keyboard style={{ color: 'var(--text-secondary)', fontSize: 11, marginLeft: 8 }}>YID: {c.yid || '-'}</Typography.Text>
                                              {isConfigDisabled && (
                                                <Tag style={{ margin: '0 0 0 8px', fontSize: 11, borderRadius: 4, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#71717a' : '#a1a1aa', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>已禁用</Tag>
                                              )}
                                              {isChecked && !isGroupEnabled && (
                                                <Tag color="warning" style={{ margin: '0 0 0 8px', fontSize: 11, borderRadius: 4 }}>本组已关</Tag>
                                              )}
                                            </Checkbox>
                                             <Space size={4} style={{ alignItems: 'center' }}>
                                                {renderUpstreamScheduleTags(c, 'sm')}
                                                {isChecked && (
                                                  <Tooltip title={isGroupEnabled ? '当前分组已启用该上游（熔断遵循高可用策略），点击关闭后不参与本组调用' : '当前分组已关闭该上游（不参与调用），点击在当前分组启用'}>
                                                    <Switch
                                                      size="small"
                                                      checked={isGroupEnabled}
                                                      loading={!!subMeltLoading[loadKey]}
                                                      style={{ transform: 'scale(0.82)', transformOrigin: 'center' }}
                                                      onChange={(checked, e) => {
                                                        e.stopPropagation();
                                                        handleToggleSubChannelStatus(editingChannel?.id, aid, checked);
                                                      }}
                                                    />
                                                  </Tooltip>
                                                )}
                                                  <Button
                                                    type="text"
                                                    size="small"
                                                    icon={<EditOutlined style={{ fontSize: 11 }} />}
                                                    style={{ width: 22, height: 22, minWidth: 22, display: 'flex', alignItems: 'center', justifyContent: 'center', margin: 0, padding: 0 }}
                                                    onClick={(e) => {
                                                      e.stopPropagation();
                                                      handleQuickEditPreset(c);
                                                    }}
                                                  />
                                             </Space>
                                          </div>
                                        );
                                      })}
                                    </Space>
                                  </div>
                                )}
                              </>
                            );
                          }

                          let items: any[] = [];
                          let statusCounts = { all: 0, active: 0, disabled: 0 };
                          if (upstreamTab === 'preset') {
                            const searched = presets.filter(p => p.id !== -99 && p.provider_type !== 'high_availability_group' && (
                              p.name?.toLowerCase().includes(presetSearchText.toLowerCase()) || 
                              p.provider_type?.toLowerCase().includes(presetSearchText.toLowerCase()) || 
                              String(p.id).includes(presetSearchText) ||
                              (p.yid && String(p.yid).includes(presetSearchText))
                            ));
                            statusCounts = {
                              all: searched.length,
                              active: searched.filter(p => (p.status ?? 1) === 1).length,
                              disabled: searched.filter(p => (p.status ?? 1) !== 1).length,
                            };
                            items = searched.filter(p =>
                              upstreamStatusFilter === 'all' || (p.status ?? 1) === upstreamStatusFilter
                            );
                          } else if (upstreamTab === 'volcengine_enhance') {
                            items = volcengineEnhanceKeys.filter(p =>
                              p.name?.toLowerCase().includes(presetSearchText.toLowerCase()) ||
                              p.api_key?.toLowerCase().includes(presetSearchText.toLowerCase())
                            );
                          } else if (upstreamTab === 'comfyui') {
                            items = comfyuiServers.filter(p =>
                              p.name?.toLowerCase().includes(presetSearchText.toLowerCase()) ||
                              p.base_url?.toLowerCase().includes(presetSearchText.toLowerCase()) ||
                              String(p.id).includes(presetSearchText)
                            );
                          }

                          return (
                            <>


                              {/* 2. 搜索框 + 状态筛选 */}
                              <div style={{ marginBottom: 10, display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
                                <Input
                                  placeholder="输入关键字搜索名称或 ID..."
                                  allowClear
                                  prefix={<SearchOutlined style={{ color: 'var(--text-secondary)' }} />}
                                  value={presetSearchText}
                                  onChange={(e) => setPresetSearchText(e.target.value)}
                                  style={{ flex: 1, minWidth: 200 }}
                                />
                                {upstreamTab === 'preset' && (
                                  <Segmented
                                    size="small"
                                    options={[
                                      { label: `全部 (${statusCounts.all})`, value: 'all' },
                                      { label: `激活 (${statusCounts.active})`, value: '1' },
                                      { label: `禁用 (${statusCounts.disabled})`, value: '0' },
                                    ]}
                                    value={upstreamStatusFilter === 'all' ? 'all' : String(upstreamStatusFilter)}
                                    onChange={(val) => setUpstreamStatusFilter(val === 'all' ? 'all' : parseInt(val as string, 10) as 0 | 1)}
                                  />
                                )}
                              </div>

                              {upstreamTab === 'comfyui' && (
                                <div style={{ marginBottom: 10, display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
                                  <Text strong style={{ whiteSpace: 'nowrap' }}>调用规则</Text>
                                  <Select
                                    style={{ minWidth: 220, flex: 1 }}
                                    value={configObj.comfyui_dispatch || COMFYUI_DISPATCH_DEFAULT}
                                    onChange={(v) => setConfigObj((prev) => ({ ...prev, comfyui_dispatch: v }))}
                                    options={comfyuiDispatchRules
                                      .filter((r) => r.is_active === 1)
                                      .map((r) => ({ value: r.code, label: r.name, title: r.remark || undefined }))}
                                    placeholder="选择调用规则"
                                  />
                                  <Text type="secondary" style={{ fontSize: 12 }}>
                                    {comfyuiDispatchRules.find((r) => r.code === (configObj.comfyui_dispatch || COMFYUI_DISPATCH_DEFAULT))?.remark
                                      || '可多选节点；每次请求按规则选一台'}
                                  </Text>
                                </div>
                              )}

                              {items.length === 0 ? (
                                <div style={{ padding: '40px 0', textAlign: 'center', color: 'var(--text-secondary)' }}>
                                  {upstreamTab === 'comfyui'
                                    ? '请先在「站点插件 → ComfyUI 接入」中新增服务节点'
                                    : '暂无匹配的上游渠道配置'}
                                </div>
                              ) : (
                                <div style={{ 
                                  maxHeight: 'calc(100vh - 320px)', minHeight: '400px', 
                                  overflowY: 'auto', 
                                  paddingRight: 4 
                                }}>
                                  <Space direction="vertical" style={{ width: '100%' }} size={4}>

                                    {items.map((item) => {
                                      let isSelected = false;
                                      let cardTitle = item.name;
                                      let cardSubtitle = '';
                                      let extraTag = null;
                                      const isConfigDisabled = (upstreamTab === 'preset' && (item.status ?? 1) !== 1)
                                        || (upstreamTab === 'comfyui' && item.is_active !== 1);

                                      if (upstreamTab === 'preset') {
                                        isSelected = currentPreset === item.id;
                                        cardSubtitle = item.yid ? `YID: ${item.yid}` : 'YID: -';
                                        extraTag = (
                                          <Space size={4} style={{ alignItems: 'center' }}>
                                              {isConfigDisabled && <Tag style={{ margin: 0, fontSize: 11, borderRadius: 4, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#71717a' : '#a1a1aa', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>已禁用</Tag>}
                                              <Tag style={{ margin: 0, fontSize: 11, borderRadius: 4, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#18181b' : '#f4f4f5', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>{item.provider_type}</Tag>
                                              {renderUpstreamScheduleTags(item, 'sm', true)}
                                          </Space>
                                        );
                                      } else if (upstreamTab === 'volcengine_enhance') {
                                        isSelected = configObj.volcengine_enhance_credential_id === item.id;
                                        cardSubtitle = `基址: ${item.base_url || '-'}`;
                                        extraTag = <Tag style={{ margin: 0, fontSize: 11, borderRadius: 4, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#18181b' : '#f4f4f5', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>火山凭证</Tag>;
                                      } else if (upstreamTab === 'comfyui') {
                                        const selectedIds = parseComfyuiServerIds(configObj);
                                        isSelected = selectedIds.includes(item.id);
                                        cardSubtitle = item.base_url ? `基址: ${item.base_url}` : '';
                                        extraTag = (
                                          <Space size={4}>
                                            {item.is_active !== 1 && <Tag style={{ margin: 0, fontSize: 11, borderRadius: 4, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#71717a' : '#a1a1aa', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>停用</Tag>}
                                            <Tag style={{ margin: 0, fontSize: 11, borderRadius: 4, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#18181b' : '#f4f4f5', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>优先级: {item.priority ?? 0}</Tag>
                                            <Tag style={{ margin: 0, fontSize: 11, borderRadius: 4, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#18181b' : '#f4f4f5', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>权重: {item.weight ?? 1}</Tag>
                                            <Tag style={{ margin: 0, fontSize: 11, borderRadius: 4, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#18181b' : '#f4f4f5', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>服务节点</Tag>
                                          </Space>
                                        );
                                      }

                                      return (

                                          <div
                                            onClick={() => {
                                              if (upstreamTab === 'preset') {
                                                const isHa = item.provider_type === 'high_availability_group';
                                                if (!isHa) {
                                                  setSelectedSubChannelAids([]);
                                                }
                                                const eff = getEffectiveUpstreamSchedule(item, configObj);
                                                const currentCat = categoryIdRef.current;
                                                form.setFieldsValue({
                                                  preset_id: item.id,
                                                  rate: eff.rate,
                                                  priority: eff.priority,
                                                  weight: eff.weight,
                                                  provider_type: item.provider_type || 'custom',
                                                });
                                                const targetCatId = (Array.isArray(item.category_ids) && item.category_ids.length > 0)
                                                  ? item.category_ids[0]
                                                  : item.category_id;
                                                if (currentCat == null && targetCatId) {
                                                  categoryIdRef.current = targetCatId;
                                                  form.setFieldsValue({ category_id: targetCatId });
                                                }
                                              } else {
                                                setSelectedSubChannelAids([]);
                                                if (upstreamTab === 'volcengine_enhance') {
                                                  form.setFieldsValue({
                                                    preset_id: null,
                                                    provider_type: 'volcengine',
                                                  });
                                                  setConfigObj(prev => {
                                                    const { comfyui_workflow_id, comfyui_server_id, comfyui_server_ids, comfyui_dispatch, ...rest } = prev;
                                                    return { ...rest, volcengine_enhance_credential_id: item.id };
                                                  });
                                                } else if (upstreamTab === 'comfyui') {
                                                  const selectedIds = parseComfyuiServerIds(configObj);
                                                  const next = selectedIds.includes(item.id)
                                                    ? selectedIds.filter((id: number) => id !== item.id)
                                                    : [...selectedIds, item.id];
                                                  form.setFieldsValue({
                                                    preset_id: null,
                                                    provider_type: next.length ? 'comfyui' : 'custom',
                                                  });
                                                  setConfigObj(prev => {
                                                    const { volcengine_enhance_credential_id, comfyui_workflow_id, comfyui_server_id, comfyui_server_ids, ...rest } = prev;
                                                    return {
                                                      ...rest,
                                                      comfyui_server_ids: next,
                                                      comfyui_server_id: next[0],
                                                      comfyui_dispatch: prev.comfyui_dispatch || COMFYUI_DISPATCH_DEFAULT,
                                                    };
                                                  });
                                                }
                                              }
                                            }}
                                            style={{
                                              padding: '6px 10px',
                                              borderRadius: 6,
                                              border: isSelected ? '1px solid #91caff' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'),
                                              background: isSelected ? (isLight ? 'rgba(22,119,255,0.08)' : 'rgba(22,119,255,0.15)') : 'transparent',
                                              cursor: 'pointer',
                                              opacity: isConfigDisabled ? 0.75 : 1,
                                              display: 'flex',
                                              justifyContent: 'space-between',
                                              alignItems: 'center',
                                              transition: 'all 0.15s'
                                            }}
                                          >
                                            <div style={{ flex: 1, display: 'flex', alignItems: 'center', minWidth: 0, gap: 8 }}>
                                              <span style={{ fontWeight: 600, fontSize: 13, color: isSelected ? 'var(--text)' : 'inherit', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                                                {cardTitle}
                                              </span>
                                              {upstreamTab === 'preset' ? (
                                                <Typography.Text keyboard style={{ color: 'var(--text-secondary)', fontSize: 11, margin: 0 }}>
                                                  {item.yid ? `YID: ${item.yid}` : 'YID: -'}
                                                </Typography.Text>
                                              ) : (
                                                <span style={{ fontSize: 11, color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
                                                  {cardSubtitle}
                                                </span>
                                              )}
                                            </div>
                                            <div style={{ flexShrink: 0, display: 'flex', alignItems: 'center' }}>
                                              {extraTag}
                                            </div>
                                          </div>
                                      );
                                    })}
                                  </Space>
                                </div>
                              )}
                            </>
                          );
                        }}
                      </Form.Item>
                    </div>

                    <div style={{ display: activeRightPanel === 'levels' ? 'block' : 'none', animation: 'fadeIn 0.2s' }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12, flexWrap: 'wrap', gap: 12 }}>
                          <Space size={16} align="center" wrap>
                            <Title level={4} style={{ margin: 0, whiteSpace: 'nowrap' }}>{isExcludeMode ? '不支持用户等级' : '支持用户等级'}</Title>
                            <Form.Item shouldUpdate={(prev, curr) => prev.level_select !== curr.level_select} noStyle>
                              {() => {
                                const selected = form.getFieldValue('level_select') || [];
                                const allLevelIds = availableUserLevels.map(l => l.id.toString());
                                return (
                                  <Space>
                                    <Button onClick={() => { levelsRef.current = allLevelIds; form.setFieldsValue({ level_select: allLevelIds }); }}>全选</Button>
                                    <Button onClick={() => { levelsRef.current = []; form.setFieldsValue({ level_select: [] }); }} disabled={selected.length === 0}>
                                      清空
                                    </Button>
                                  </Space>
                                );
                              }}
                            </Form.Item>
                          </Space>
                          <Space size={4} align="center">
                            <Text type="secondary" style={{ fontSize: 13, display: screens.xs ? 'none' : 'inline-block' }}>访问控制模式：</Text>
                            <Segmented
                              options={['允许模式', '排除模式']}
                              value={isExcludeMode ? '排除模式' : '允许模式'}
                              onChange={(val) => setIsExcludeMode(val === '排除模式')}
                            />
                          </Space>
                        </div>
                        <div style={{ marginBottom: 24 }}>
                          <Text type="secondary" style={{ fontSize: 13 }}>
                            {isExcludeMode ? '选中排除的等级将【不可】使用该渠道。' : '选中允许的等级才【可以】使用该渠道。'}
                          </Text>
                        </div>
                        
                        <Form.Item name="level_select" style={{ marginBottom: 0 }} hidden>
                          <Select mode="multiple" />
                        </Form.Item>
                        
                        <Form.Item shouldUpdate={(prev, curr) => prev.level_select !== curr.level_select} noStyle>
                          {() => {
                            const selectedLevels: string[] = form.getFieldValue('level_select') || [];
                            const orphanIds = selectedLevels.filter((id: string) => !resolveUserLevelMeta(id).exists);
                            const mutedTagStyle: React.CSSProperties = { borderRadius: 4, margin: 0, padding: '0 4px', fontSize: 10, lineHeight: '18px', marginLeft: 6, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#71717a' : '#a1a1aa', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` };
                            return (
                              <div style={{ maxHeight: 600, overflowY: 'auto', paddingRight: 8 }}>
                                <Row gutter={[12, 12]}>
                                  {availableUserLevels.map((l) => {
                                    const idStr = l.id.toString();
                                    const meta = resolveUserLevelMeta(idStr);
                                    const matchKeys = [idStr, String(l.id).padStart(4, '0'), l.group_key, l.name].filter(Boolean);
                                    const isSelected = selectedLevels.some((k: string) => matchKeys.includes(String(k)));
                                    return (
                                      <Col xs={24} sm={12} lg={12} key={idStr}>
                                        <div 
                                          onClick={() => {
                                            const filtered = selectedLevels.filter((id: string) => !matchKeys.includes(String(id)));
                                            const next = isSelected ? filtered : [...filtered, idStr];
                                            levelsRef.current = next;
                                            form.setFieldsValue({ level_select: next });
                                          }}
                                          style={{
                                            padding: '8px 12px',
                                            borderRadius: 6,
                                            border: isSelected ? '1px solid var(--text)' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'),
                                            background: isSelected ? (isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.08)') : 'transparent',
                                            opacity: meta.isDisabled ? 0.72 : 1,
                                            cursor: 'pointer',
                                            display: 'flex',
                                            alignItems: 'center',
                                            transition: 'all 0.2s'
                                          }}
                                        >
                                          <div style={{ flex: 1, minWidth: 0 }}>
                                            <div style={{ fontWeight: 500, fontSize: 14, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', color: isSelected ? 'var(--text)' : 'inherit' }}>
                                              {l.name} ({l.discount}x)
                                              {meta.isDisabled && <Tag style={mutedTagStyle}>已禁用</Tag>}
                                              {!meta.isDisabled && !meta.hasLevelDisc && <Tag style={mutedTagStyle}>倍率未启用</Tag>}
                                            </div>
                                            <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 2 }}>ULID: {idStr.padStart(4, '0')} | {l.group_key}</div>
                                          </div>
                                        </div>
                                      </Col>
                                    );
                                  })}
                                  {orphanIds.map((idStr: string) => {
                                    const meta = resolveUserLevelMeta(idStr);
                                    return (
                                      <Col xs={24} sm={12} lg={12} key={`orphan-${idStr}`}>
                                        <div
                                          onClick={() => {
                                            const next = selectedLevels.filter((id: string) => id !== idStr);
                                            levelsRef.current = next;
                                            form.setFieldsValue({ level_select: next });
                                          }}
                                          style={{
                                            padding: '8px 12px',
                                            borderRadius: 6,
                                            border: isLight ? '1px dashed #d4d4d8' : '1px dashed #3f3f46',
                                            background: isLight ? 'rgba(0,0,0,0.02)' : 'rgba(255,255,255,0.04)',
                                            opacity: 0.75,
                                            cursor: 'pointer',
                                            display: 'flex',
                                            alignItems: 'center',
                                            transition: 'all 0.2s'
                                          }}
                                        >
                                          <div style={{ flex: 1, minWidth: 0 }}>
                                            <div style={{ fontWeight: 500, fontSize: 14, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', color: 'var(--text-secondary)' }}>
                                              {meta.name}
                                              <Tag style={mutedTagStyle}>已删除</Tag>
                                            </div>
                                            <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 2 }}>
                                              {meta.isNumeric ? `ULID: ${meta.ulid}` : `标识: ${idStr}`} · 点击移除失效绑定
                                            </div>
                                          </div>
                                        </div>
                                      </Col>
                                    );
                                  })}
                                </Row>
                              </div>
                            );
                          }}
                        </Form.Item>
                      </div>


                    <div style={{ display: activeRightPanel === 'mapping' ? 'block' : 'none', animation: 'fadeIn 0.2s' }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 24 }}>
                          <Title level={4} style={{ margin: 0 }}>模型别名映射</Title>
                          <Switch checked={showMapping} onChange={setShowMapping} />
                        </div>
                        <Text type="secondary" style={{ display: 'block', marginBottom: 24 }}>开启后可为每个模型指定上游别名，解决上下游模型名称不一致的问题。</Text>
                        
                        {showMapping ? (
                          <Form.Item shouldUpdate={(prev, curr) => prev.provider_type !== curr.provider_type || prev.category_id !== curr.category_id} noStyle>
                            {() => {
                              const providerType = form.getFieldValue('provider_type');
                              const isHaMode = providerType === 'high_availability_group';
                              const selectedModels = channelModelMids;
                              if (selectedModels.length === 0) {
                                return <div style={{ padding: 40, textAlign: 'center', color: 'var(--text)', background: isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)', borderRadius: 8 }}>请先在左侧选择模型</div>;
                              }

                              // HA 模式下获取已绑定的子渠道信息
                              const haSubChannels = isHaMode
                                ? presets.filter((p: any) => selectedSubChannelAids.includes(p.id))
                                : [];
                              if (isHaMode && haSubChannels.length === 0) {
                                return (
                                  <div style={{ padding: 40, textAlign: 'center', color: 'var(--text)', background: isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)', borderRadius: 8 }}>
                                    请先在左侧「选择上游渠道」中绑定高可用子渠道
                                  </div>
                                );
                              }
                              const channelCategoryName = resolveCategoryName(form.getFieldValue('category_id'));

                              const toggleResScope = (modelId: string, scope: string) => {
                                const key = resScopeKey(modelId, scope);
                                setExpandedResScopes(prev =>
                                  prev.includes(key) ? prev.filter(k => k !== key) : [...prev, key]
                                );
                              };

                              const updateResAlias = (modelId: string, scope: string, res: string, alias: string) => {
                                const resKey = normalizeResKey(res);
                                if (!resKey) return;
                                setResModelMappingState(prev => {
                                  const map = { ...(prev[modelId]?.[scope] || {}) };
                                  if (!alias.trim()) delete map[resKey];
                                  else map[resKey] = alias;
                                  return patchResScope(prev, modelId, scope, Object.keys(map).length ? map : null);
                                });
                              };

                              const clearResScope = (modelId: string, scope: string) => {
                                setResModelMappingState(prev => patchResScope(prev, modelId, scope, null));
                                const key = resScopeKey(modelId, scope);
                                setExpandedResScopes(prev => prev.filter(k => k !== key));
                                setCustomResScopes(prev => {
                                  const next = { ...prev };
                                  delete next[key];
                                  return next;
                                });
                              };

                              const renderResAdvancedBtn = (modelId: string, scope: string) => {
                                const scopeMap = resModelMappingState[modelId]?.[scope] || {};
                                const scopeKey = resScopeKey(modelId, scope);
                                const isOpen = expandedResScopes.includes(scopeKey);
                                const count = countFilled(scopeMap);
                                return (
                                  <Tooltip title={isOpen ? '收起分辨率映射' : '高级：按分辨率映射不同模型名'}>
                                    <Button
                                      type={count > 0 || isOpen ? 'primary' : 'default'}
                                      size="small"
                                      icon={<SettingOutlined />}
                                      onClick={() => toggleResScope(modelId, scope)}
                                      style={{
                                        height: 24,
                                        padding: '0 8px',
                                        fontSize: 12,
                                        flexShrink: 0,
                                        ...(!(count > 0 || isOpen) ? {
                                          color: 'var(--text-secondary)',
                                          borderColor: isLight ? 'rgba(0,0,0,0.15)' : 'rgba(255,255,255,0.22)',
                                          background: isLight ? '#fff' : 'rgba(255,255,255,0.06)',
                                        } : {}),
                                      }}
                                    >
                                      高级{count > 0 ? ` ${count}` : ''}
                                    </Button>
                                  </Tooltip>
                                );
                              };

                              const renderResAdvancedPanel = (modelId: string, scope: string, presets: string[], indent: boolean = true) => {
                                const scopeMap = resModelMappingState[modelId]?.[scope] || {};
                                const scopeKey = resScopeKey(modelId, scope);
                                if (!expandedResScopes.includes(scopeKey)) return null;
                                const configured = Object.keys(scopeMap).filter(k => isFilled(scopeMap[k]));
                                const activeRows = customResScopes[scopeKey] ?? (configured.length > 0 ? configured : presets);

                                const updateAlias = (res: string, val: string) => {
                                  if (customResScopes[scopeKey] === undefined) {
                                    setCustomResScopes(prev => ({ ...prev, [scopeKey]: activeRows }));
                                  }
                                  updateResAlias(modelId, scope, res, val);
                                };

                                const handleAddRes = (raw: string) => {
                                  const res = normalizeResKey(raw);
                                  if (!res || activeRows.includes(res)) return;
                                  setCustomResScopes(prev => ({
                                    ...prev,
                                    [scopeKey]: [...(prev[scopeKey] ?? activeRows), res],
                                  }));
                                };

                                const handleRemoveRes = (res: string) => {
                                  updateResAlias(modelId, scope, res, '');
                                  setCustomResScopes(prev => ({
                                    ...prev,
                                    [scopeKey]: (prev[scopeKey] ?? activeRows).filter(k => k !== res),
                                  }));
                                };

                                const isVideo = presets.includes('480p');
                                const quickPool = isVideo ? [...presets, ...VIDEO_EXTRA_TAGS] : presets;
                                const remainingQuickTags = quickPool.filter(t => !activeRows.includes(t));

                                return (
                                  <div style={{
                                    marginTop: 6,
                                    marginLeft: indent ? 128 : 0,
                                    padding: '8px 10px',
                                    borderRadius: 6,
                                    background: isLight ? 'rgba(22,119,255,0.04)' : 'rgba(22,119,255,0.08)',
                                    border: isLight ? '1px solid rgba(22,119,255,0.15)' : '1px solid rgba(105,177,255,0.2)',
                                  }}>
                                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
                                      <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>
                                        {isVideo ? '目标分辨率 → 上游实际模型（级联通常填对应底座模型，未填回退别名）' : '分辨率/尺寸 → 上游模型（未填回退明文别名）'}
                                      </span>
                                      {countFilled(scopeMap) > 0 && (
                                        <Button type="link" size="small" danger style={{ padding: 0, height: 'auto', fontSize: 11 }} onClick={() => clearResScope(modelId, scope)}>
                                          清空
                                        </Button>
                                      )}
                                    </div>
                                    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                                      {activeRows.map(res => (
                                        <div key={res} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                                          <Tag style={{ margin: 0, minWidth: 48, textAlign: 'center', fontSize: 11 }}>{res}</Tag>
                                          <Input
                                            size="small"
                                            placeholder={resPlaceholder(res, isVideo)}
                                            value={scopeMap[res] || ''}
                                            onChange={(e) => updateAlias(res, e.target.value)}
                                            style={{ flex: 1 }}
                                          />
                                          <Tooltip title="移除此映射档位">
                                            <Button
                                              type="text"
                                              size="small"
                                              icon={<CloseOutlined style={{ fontSize: 10 }} />}
                                              style={{ width: 20, height: 20, minWidth: 20, padding: 0, color: 'var(--text-secondary)' }}
                                              onClick={() => handleRemoveRes(res)}
                                            />
                                          </Tooltip>
                                        </div>
                                      ))}
                                    </div>
                                    <div style={{
                                      display: 'flex',
                                      alignItems: 'center',
                                      gap: 6,
                                      flexWrap: 'wrap',
                                      marginTop: activeRows.length > 0 ? 8 : 2,
                                      paddingTop: activeRows.length > 0 ? 6 : 0,
                                      borderTop: activeRows.length > 0
                                        ? (isLight ? '1px dashed rgba(0,0,0,0.08)' : '1px dashed rgba(255,255,255,0.08)')
                                        : 'none',
                                    }}>
                                      {remainingQuickTags.length > 0 && (
                                        <>
                                          <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>快捷添加:</span>
                                          {remainingQuickTags.map(tag => (
                                            <Tag
                                              key={tag}
                                              style={{ cursor: 'pointer', margin: 0, fontSize: 11 }}
                                              onClick={() => handleAddRes(tag)}
                                            >
                                              +{tag}
                                            </Tag>
                                          ))}
                                        </>
                                      )}
                                      <Input
                                        size="small"
                                        placeholder="+ 自定义档位回车"
                                        style={{ width: 145, height: 22, fontSize: 11 }}
                                        onKeyDown={(e) => {
                                          if (e.key === 'Enter') {
                                            e.preventDefault();
                                            e.stopPropagation();
                                          }
                                        }}
                                        onPressEnter={(e) => {
                                          e.preventDefault();
                                          e.stopPropagation();
                                          const inputEl = e.currentTarget as HTMLInputElement;
                                          const val = (inputEl.value || '').trim();
                                          if (val) {
                                            handleAddRes(val);
                                            inputEl.value = '';
                                          }
                                        }}
                                      />
                                    </div>
                                  </div>
                                );
                              };

                              const isHaModeWithSubs = isHaMode && haSubChannels.length > 0;

                              const updateModelAlias = (modelId: string, val: string) => {
                                setModelMappingState(prev => {
                                  if (!val.trim()) {
                                    const next = { ...prev };
                                    delete next[modelId];
                                    return next;
                                  }
                                  return { ...prev, [modelId]: val };
                                });
                              };

                              const clearModelAlias = (modelId: string) => {
                                setModelMappingState(prev => {
                                  const next = { ...prev };
                                  delete next[modelId];
                                  return next;
                                });
                              };

                              const updateSubAlias = (modelId: string, subId: string, val: string) => {
                                setHaModelMappingState(prev => {
                                  const next = { ...prev };
                                  const subMap = { ...(next[modelId] || {}) };
                                  if (!val.trim()) {
                                    delete subMap[subId];
                                  } else {
                                    subMap[subId] = val;
                                  }
                                  if (Object.keys(subMap).length === 0) delete next[modelId];
                                  else next[modelId] = subMap;
                                  return next;
                                });
                              };

                              const clearSubAlias = (modelId: string, subId: string) => {
                                setHaModelMappingState(prev => {
                                  const next = { ...prev };
                                  const subMap = { ...(next[modelId] || {}) };
                                  delete subMap[subId];
                                  if (Object.keys(subMap).length === 0) delete next[modelId];
                                  else next[modelId] = subMap;
                                  return next;
                                });
                              };

                              const renderMappingRow = (
                                modelId: string,
                                scope: string,
                                label: React.ReactNode,
                                value: string,
                                placeholder: string,
                                presets: string[],
                                onChange: (val: string) => void,
                                onClear: () => void,
                                showTrash?: boolean,
                              ) => (
                                <div key={scope} style={{ marginBottom: 4 }}>
                                  <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                                    {label}
                                    <Input
                                      size="small"
                                      placeholder={placeholder}
                                      value={value}
                                      onChange={(e) => onChange(e.target.value)}
                                      style={{ flex: 1 }}
                                    />
                                    {renderResAdvancedBtn(modelId, scope)}
                                    {showTrash ? (
                                      <Button icon={<DeleteOutlined />} size="small" onClick={onClear} />
                                    ) : value ? (
                                      <Button
                                        type="text"
                                        size="small"
                                        icon={<CloseOutlined style={{ fontSize: 10 }} />}
                                        style={{ width: 20, height: 20, minWidth: 20, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text-secondary)', margin: 0, padding: 0 }}
                                        onClick={onClear}
                                      />
                                    ) : null}
                                  </div>
                                  {renderResAdvancedPanel(modelId, scope, presets, Boolean(label))}
                                </div>
                              );

                              return (
                                <div style={{ maxHeight: 'calc(100vh - 340px)', minHeight: 300, overflowY: 'auto', paddingRight: 12 }}>
                                  {isHaModeWithSubs ? (
                                    <Alert
                                      message="高可用子渠道独立映射"
                                      description="可为每个子渠道设置独立的模型别名；开启高级设置可按分辨率（视频 480p/…、图片 1k/…）映射不同上游模型名。优先级：子渠该分辨率 → 该子渠明文别名（未填档位即回退）。"
                                      type="info"
                                      showIcon
                                      style={{ borderRadius: 6, marginBottom: 16 }}
                                    />
                                  ) : (
                                    <Alert
                                      message="模型别名与分辨率映射"
                                      description="可为模型指定上游别名；开启【高级】可按分辨率（视频 480p/720p/1080p…、图片 1k/2k/4k…）分别映射不同上游模型名，未填档位自动回退到明文别名。"
                                      type="info"
                                      showIcon
                                      style={{ borderRadius: 6, marginBottom: 16 }}
                                    />
                                  )}
                                  <Row gutter={16}>
                                    {selectedModels.map((midOrId: string) => {
                                      const mapKey = String(midOrId);
                                      const match = availableModels.find(m => String(m.mid) === mapKey);
                                      const actualModelId = match?.model_id ? String(match.model_id) : mapKey;
                                      const resPresets = resPresetsForHint(
                                        match?.type_name,
                                        match?.name,
                                        actualModelId,
                                        channelCategoryName,
                                      );
                                      const isActive = activeMappingInputs.includes(mapKey);
                                      const hasValue = Boolean(modelMappingState[mapKey] && String(modelMappingState[mapKey]).trim());
                                      const resDefaultCount = countFilled(resModelMappingState[mapKey]?.default);
                                      const hasAnyMapping = Boolean(hasValue || resDefaultCount > 0);
                                      const isHaExpanded = expandedHaModels.includes(mapKey);
                                      const haSubMapping = haModelMappingState[mapKey] || {};
                                      const boundSubIdSet = new Set(haSubChannels.map((s: any) => String(s.id)));
                                      const haSubMappingCount = Object.entries(haSubMapping).filter(
                                        ([sid, v]) => boundSubIdSet.has(sid) && isFilled(v),
                                      ).length;
                                      const haResCount = Object.entries(resModelMappingState[mapKey] || {}).reduce(
                                        (sum, [sid, rmap]) => sum + (boundSubIdSet.has(sid) ? countFilled(rmap) : 0),
                                        0,
                                      );

                                      return (
                                        <Col span={24} key={midOrId}>
                                          <div style={{ 
                                            padding: '12px', 
                                            marginBottom: 12, 
                                            borderRadius: 8, 
                                            border: isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)',
                                            background: isActive ? (isLight ? 'rgba(0,0,0,0.02)' : 'rgba(255,255,255,0.02)') : 'transparent',
                                            transition: 'all 0.2s'
                                          }}>
                                            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                                              <div style={{ flex: 1, minWidth: 0, paddingRight: 16 }}>
                                                <div style={{ fontWeight: 500, fontSize: 14, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{match?.name || actualModelId}</div>
                                                <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>ID: {actualModelId} | MID: {midOrId}</div>
                                              </div>
                                              
                                              <Space size={4}>
                                                {!isActive && !isHaModeWithSubs && (
                                                  <Button 
                                                    type={hasAnyMapping ? "default" : "dashed"} 
                                                    size="small" 
                                                    icon={<PlusOutlined />}
                                                    onClick={() => setActiveMappingInputs(prev => [...prev, mapKey])}
                                                    style={{ flexShrink: 0 }}
                                                  >
                                                    {hasAnyMapping ? `编辑别名${resDefaultCount > 0 ? ` (${resDefaultCount}档)` : ''}` : "添加映射"}
                                                  </Button>
                                                )}
                                                {isHaModeWithSubs && (
                                                  <Button
                                                    type={isHaExpanded ? 'primary' : 'default'}
                                                    size="small"
                                                    icon={<ApartmentOutlined />}
                                                    onClick={() => {
                                                      setExpandedHaModels(prev =>
                                                        prev.includes(mapKey)
                                                          ? prev.filter(id => id !== mapKey)
                                                          : [...prev, mapKey]
                                                      );
                                                    }}
                                                    style={{
                                                      flexShrink: 0,
                                                      ...(!isHaExpanded ? {
                                                        color: 'var(--text)',
                                                        borderColor: isLight ? 'rgba(0,0,0,0.15)' : 'rgba(255,255,255,0.22)',
                                                        background: isLight ? '#fff' : 'rgba(255,255,255,0.06)',
                                                      } : {}),
                                                    }}
                                                  >
                                                    {isHaExpanded ? '收起' : '子渠道映射'}{(haSubMappingCount + haResCount) > 0 ? ` (${haSubMappingCount}${haResCount > 0 ? `+R${haResCount}` : ''})` : ''}
                                                  </Button>
                                                )}
                                              </Space>
                                            </div>

                                            {/* 普通渠道别名与高级分辨率设置 */}
                                            {isActive && !isHaModeWithSubs && (
                                              <div style={{ marginTop: 12, animation: 'fadeIn 0.2s' }}>
                                                {renderMappingRow(
                                                  mapKey,
                                                  'default',
                                                  null,
                                                  modelMappingState[mapKey] || '',
                                                  `请输入上游调用的实际名称，不填则默认：${actualModelId}`,
                                                  resPresets,
                                                  (val) => updateModelAlias(mapKey, val),
                                                  () => {
                                                    setActiveMappingInputs(prev => prev.filter(id => id !== mapKey));
                                                    clearModelAlias(mapKey);
                                                    clearResScope(mapKey, 'default');
                                                  },
                                                  true,
                                                )}
                                              </div>
                                            )}

                                            {/* HA 子渠道独立映射 */}
                                            {isHaModeWithSubs && isHaExpanded && (
                                              <div style={{ 
                                                marginTop: 12, 
                                                padding: '10px 12px', 
                                                background: isLight ? 'rgba(0,0,0,0.02)' : 'rgba(255,255,255,0.02)', 
                                                borderRadius: 6, 
                                                border: isLight ? '1px dashed #d9d9d9' : '1px dashed rgba(255,255,255,0.12)',
                                                animation: 'fadeIn 0.2s'
                                              }}>
                                                <div style={{ marginBottom: 8, fontSize: 12, color: 'var(--text-secondary)', fontWeight: 500 }}>
                                                  <ApartmentOutlined style={{ marginRight: 4 }} />
                                                  子渠道独立映射
                                                </div>
                                                <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                                                  {haSubChannels.map((sub: any) => {
                                                    const subIdStr = String(sub.id);
                                                    return renderMappingRow(
                                                      mapKey,
                                                      subIdStr,
                                                      <div style={{ minWidth: 120, maxWidth: 160, flexShrink: 0, fontSize: 12, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={`${sub.name} (YID: ${sub.yid || '-'})`}>
                                                        <CloudServerOutlined style={{ marginRight: 4, color: 'var(--text-secondary)' }} />
                                                        {sub.name}
                                                      </div>,
                                                      haSubMapping[subIdStr] || '',
                                                      actualModelId,
                                                      resPresets,
                                                      (val) => updateSubAlias(mapKey, subIdStr, val),
                                                      () => clearSubAlias(mapKey, subIdStr),
                                                    );
                                                  })}
                                                </div>
                                              </div>
                                            )}
                                          </div>
                                        </Col>
                                      );
                                    })}
                                  </Row>
                                </div>
                              );
                            }}
                          </Form.Item>
                        ) : (
                          <div style={{ padding: 40, textAlign: 'center', color: 'var(--text)', background: isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)', borderRadius: 8 }}>别名映射功能已关闭</div>
                        )}
                      </div>

                  </div>
                </Col>
              </Row>

              <div style={{ marginTop: 24, paddingTop: 24, borderTop: isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)', display: 'flex', justifyContent: 'flex-end', gap: 12 }}>
                <Button size="large" onClick={() => handleCloseModal()}>取消</Button>
                <Button
                  size="large"
                  htmlType="button"
                  loading={submitting}
                  onClick={async () => {
                    saveIntentRef.current = 'stay';
                    try {
                      const values = await form.validateFields();
                      await handleSave(values);
                    } catch (err: any) {
                      saveIntentRef.current = 'exit';
                      const nameErr = err?.errorFields?.find((f: { name?: (string | number)[] }) => f.name?.[0] === 'name');
                      if (nameErr) message.error(nameErr.errors?.[0] || '请填写名称');
                    }
                  }}
                >
                  保存更新
                </Button>
                <Button
                  size="large"
                  type="primary"
                  htmlType="submit"
                  loading={submitting}
                  onClick={() => { saveIntentRef.current = 'exit'; }}
                  style={{ minWidth: 120 }}
                >
                  保存退出
                </Button>
              </div>
            </Form>
          </Spin>
        </div>
        </div>
      )}

      <ChannelCategoryManager
        visible={isCategoryManagerVisible}
        onClose={() => setIsCategoryManagerVisible(false)}
        onUpdate={() => {
          fetchCategories();
          fetchChannels();
        }}
      />

      {/* 快速编辑上游配置倍率、优先级、权重（仅当前渠道组生效，未设置项继承默认） */}
      <Modal
        title={
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <EditOutlined style={{ fontSize: 15 }} />
            <span>配置上游渠道调度参数（当前渠道组）</span>
            {quickEditTarget?.name && (
              <Tag style={{ margin: 0, borderRadius: 4, background: isLight ? '#f4f4f5' : '#27272a', color: isLight ? '#18181b' : '#f4f4f5', border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}` }}>
                {quickEditTarget.name}
              </Tag>
            )}
            {quickEditTarget?.yid && (
              <Typography.Text keyboard style={{ color: 'var(--text-secondary)', fontSize: 11, margin: 0 }}>
                YID: {quickEditTarget.yid}
              </Typography.Text>
            )}
          </div>
        }
        open={quickEditModalOpen}
        onCancel={() => {
          if (!quickEditLoading) {
            setQuickEditModalOpen(false);
            setQuickEditTarget(null);
          }
        }}
        footer={[
          <Button
            key="reset"
            disabled={quickEditLoading}
            onClick={handleResetQuickEdit}
          >
            恢复默认
          </Button>,
          <Button
            key="cancel"
            disabled={quickEditLoading}
            onClick={() => {
              setQuickEditModalOpen(false);
              setQuickEditTarget(null);
            }}
          >
            取消
          </Button>,
          <Button
            key="ok"
            type="primary"
            loading={quickEditLoading}
            onClick={handleSaveQuickEdit}
          >
            保存
          </Button>,
        ]}
        width={680}
        destroyOnClose
        styles={{
          header: {
            background: 'transparent',
            borderBottom: `1px solid ${isLight ? '#f4f4f5' : '#27272a'}`,
            paddingBottom: 12,
          },
          body: {
            paddingTop: 16,
            paddingBottom: 8,
          },
          footer: {
            borderTop: `1px solid ${isLight ? '#f4f4f5' : '#27272a'}`,
            paddingTop: 12,
          },
        }}
      >
        {quickEditTarget && (
          <div
            style={{
              marginBottom: 14,
              padding: '8px 12px',
              borderRadius: 6,
              background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.04)',
              border: `1px solid ${isLight ? '#e4e4e7' : 'rgba(255,255,255,0.08)'}`,
              fontSize: 12,
              color: 'var(--text-secondary)',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              flexWrap: 'wrap',
              gap: 8,
            }}
          >
            <span>
              默认参数：优先级 <b>{quickEditTarget.priority ?? 0}</b> ｜ 权重 <b>{quickEditTarget.weight ?? 1}</b> ｜ RPM <b>不限(0)</b>（倍率 <b>{quickEditTarget.rate ?? 1.0}x</b> 全局统一）
            </span>
            <span style={{ fontSize: 11 }}>
              支持按渠道分组独立设置优先级、权重与每分钟请求上限，未修改项自动继承默认
            </span>
          </div>
        )}
        <Form form={quickEditForm} layout="vertical">
          <Row gutter={12} wrap={false}>
            <Col flex="1">
              <Form.Item
                name="rate"
                label={<span style={{ fontWeight: 500, fontSize: 13, whiteSpace: 'nowrap' }}>渠道倍率 (Rate)</span>}
                tooltip="渠道倍率由上游配置全局统一决定，不可针对不同渠道分组单独设置"
                style={{ marginBottom: 0 }}
              >
                <InputNumber
                  min={0}
                  step={0.1}
                  disabled
                  style={{ width: '100%', borderRadius: 6 }}
                  placeholder="1.0"
                  addonAfter="x"
                />
              </Form.Item>
            </Col>
            <Col flex="1">
              <Form.Item
                name="priority"
                label={<span style={{ fontWeight: 500, fontSize: 13, whiteSpace: 'nowrap' }}>调度优先级 (Priority)</span>}
                tooltip="当前渠道分组下的调度优先级；数字越大优先级越高"
                rules={[{ required: true, message: '请输入优先级' }]}
                style={{ marginBottom: 0 }}
              >
                <InputNumber
                  step={1}
                  style={{ width: '100%', borderRadius: 6 }}
                  placeholder="0"
                />
              </Form.Item>
            </Col>
            <Col flex="1">
              <Form.Item
                name="weight"
                label={<span style={{ fontWeight: 500, fontSize: 13, whiteSpace: 'nowrap' }}>分流权重 (Weight)</span>}
                tooltip="当前渠道分组下同一优先级的分流权重比例，必须大于等于 1"
                rules={[{ required: true, message: '请输入权重' }]}
                style={{ marginBottom: 0 }}
              >
                <InputNumber
                  min={1}
                  step={1}
                  style={{ width: '100%', borderRadius: 6 }}
                  placeholder="1"
                />
              </Form.Item>
            </Col>
            <Col flex="1">
              <Form.Item
                name="rpm_limit"
                label={<span style={{ fontWeight: 500, fontSize: 13, whiteSpace: 'nowrap' }}>每分钟上限 (RPM)</span>}
                tooltip="0 表示不限制；设置 >0（如 3）时按 60 秒滑动窗口计数，同优先级优先选剩余配额最多的子渠道，满额自动跳过切备选且不触发故障熔断"
                style={{ marginBottom: 0 }}
              >
                <InputNumber
                  min={0}
                  max={100000}
                  step={1}
                  style={{ width: '100%', borderRadius: 6 }}
                  placeholder="0 (不限)"
                  addonAfter="/分"
                />
              </Form.Item>
            </Col>
          </Row>
        </Form>
      </Modal>

      <ChannelBillingSimulator
        open={!!simulatingChannel}
        onClose={() => setSimulatingChannel(null)}
        channelId={simulatingChannel?.id || 0}
        channelName={simulatingChannel?.name}
        channelData={simulatingChannel}
      />

    </Card>
  );
};

export default Channels;
