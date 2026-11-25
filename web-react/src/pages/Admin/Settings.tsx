import React, { useEffect, useRef, useState } from 'react';
import { Card, Form, Input, Button, InputNumber, message, Typography, Space, Switch, Radio, Tabs, Select, Tag, Alert, Table, Spin, Upload, Modal, DatePicker, TimePicker, Divider, Descriptions, Row, Col, Popconfirm } from 'antd';
import { CloudServerOutlined, ApiOutlined, DatabaseOutlined, UploadOutlined, PlusOutlined, DeleteOutlined, EyeOutlined, SettingOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import * as Icons from '@ant-design/icons';
import { SecurityIpModal, validateSiteIpLists } from './SecurityIpModal';
import { SecurityEmailModal } from './SecurityEmailModal';
import { RegionFlagSvg } from './CurrencySettingsTab';
import { useTranslation } from 'react-i18next';
import { useSearchParams, useNavigate } from 'react-router-dom';
import ReactQuill from 'react-quill-new';
import 'react-quill-new/dist/quill.snow.css';
import request from '../../utils/request';
import useSettingsStore from '../../store/settings';
import { enterFreshSetup } from '../../utils/freshSetup';
import { toCalendarDateParam } from '../../utils/dateRangeParams';
import { DEFAULT_TOS_ZH, DEFAULT_TOS_EN, DEFAULT_PRIVACY_ZH, DEFAULT_PRIVACY_EN, DEFAULT_AGREEMENT_SETTINGS } from '../../constants/agreements';
import { StorageConfigPanel } from '../../components/Storage';
import {
  DndContext,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core';
import {
  SortableContext,
  arrayMove,
  rectSortingStrategy,
  useSortable,
} from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import dayjs from 'dayjs';

const { Text } = Typography;

function liveUtcMs(anchor: { utcMs: number; localMs: number } | null, nowMs: number) {
  if (!anchor) return nowMs;
  return anchor.utcMs + (nowMs - anchor.localMs);
}

function clockParts(ms: number, timeZone: string) {
  const fmt = new Intl.DateTimeFormat('en-US', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
    timeZone,
    timeZoneName: 'shortOffset',
  });
  return Object.fromEntries(fmt.formatToParts(new Date(ms)).map((p) => [p.type, p.value]));
}

function formatClockBody(p: Record<string, string>) {
  const hour = p.hour === '24' ? '00' : p.hour;
  return `${p.year}-${p.month}-${p.day} ${hour}:${p.minute}:${p.second}`;
}

function formatUtcClock(anchor: { utcMs: number; localMs: number } | null, nowMs: number) {
  return formatClockBody(clockParts(liveUtcMs(anchor, nowMs), 'UTC'));
}

function formatTzClock(anchor: { utcMs: number; localMs: number } | null, nowMs: number, tz?: string) {
  const name = tz?.trim();
  if (!name) return '—';
  try {
    const p = clockParts(liveUtcMs(anchor, nowMs), name);
    let offset = p.timeZoneName || 'UTC';
    if (offset.startsWith('GMT')) offset = offset.replace('GMT', 'UTC');
    if (offset === 'UTC') offset = 'UTC+0';
    return `${formatClockBody(p)} (${offset})`;
  } catch {
    return '—';
  }
}

/** 低余额在途默认档（与后端对齐） */
const DEFAULT_INFLIGHT_TIERS = [
  { max_available: 20, max_inflight: 1 },
  { max_available: 50, max_inflight: 3 },
];

const INFLIGHT_CATS: { key: 'video' | 'image' | 'chat' | 'other'; label: string; hint?: string }[] = [
  { key: 'video', label: '视频', hint: '含封装' },
  { key: 'image', label: '图片' },
  { key: 'chat', label: '聊天' },
  { key: 'other', label: '其它', hint: '音频 / 向量 / 排序等' },
];

function normalizeInflightCat(raw: any) {
  const tiers =
    Array.isArray(raw?.tiers) && raw.tiers.length > 0
      ? raw.tiers.map((t: any) => ({
          max_available: t?.max_available ?? null,
          max_inflight: typeof t?.max_inflight === 'number' ? t.max_inflight : 1,
        }))
      : DEFAULT_INFLIGHT_TIERS.map((t) => ({ ...t }));
  return { enabled: raw?.enabled === true, tiers };
}

/** 后台已 prepared（含旧配置迁移），前端直接读 inflight_limits */
function loadInflightLimits(relay: any) {
  const lim = relay?.inflight_limits || {};
  return {
    video: normalizeInflightCat(lim.video),
    image: normalizeInflightCat(lim.image),
    chat: normalizeInflightCat(lim.chat),
    other: normalizeInflightCat(lim.other),
  };
}

function dumpInflightLimits(values: any) {
  const out: Record<string, { enabled: boolean; tiers: { max_available: number | null; max_inflight: number }[] }> = {};
  for (const { key } of INFLIGHT_CATS) {
    const cat = values?.inflight_limits?.[key] || {};
    out[key] = {
      enabled: cat.enabled === true,
      tiers: (cat.tiers || []).map((t: any) => ({
        max_available:
          t?.max_available === undefined || t?.max_available === null || t?.max_available === ''
            ? null
            : Number(t.max_available),
        max_inflight: Math.max(0, Number(t?.max_inflight) || 0),
      })),
    };
  }
  return out;
}

/** 后台轮询周期：与后端 RelaySettings 对齐（5–300，默认 30） */
const clampPollTickSecs = (v: unknown) => Math.min(300, Math.max(5, Number(v) || 30));

/** SSE 流式空闲超时：与后端 StreamIdleTimeouts 对齐（0=关闭，开启时 60–3600，默认 900） */
const clampStreamIdleSecs = (v: unknown) => {
  if (v === undefined || v === null || v === '') return 900;
  const n = Math.floor(Number(v));
  if (!Number.isFinite(n) || n < 0) return 900;
  if (n === 0) return 0;
  return Math.min(3600, Math.max(60, n));
};

const DB_RESET_CONFIRM_TEXT = '确认清空当前数据';
const DB_RESET_COUNTDOWN_SECS = 10;

const ALL_LANGUAGES = [
  { code: 'zh', name: '简体中文', nativeName: 'Simplified Chinese' },
  { code: 'zh-TW', name: '繁體中文', nativeName: 'Traditional Chinese' },
  { code: 'en', name: 'English', nativeName: '英语' },
  { code: 'ja', name: '日本語', nativeName: '日语' },
  { code: 'ko', name: '한국어', nativeName: '韩语' },
  { code: 'vi', name: 'Tiếng Việt', nativeName: '越南语' },
  { code: 'th', name: 'ไทย', nativeName: '泰语' },
  { code: 'ms', name: 'Bahasa Melayu', nativeName: '马来语' },
  { code: 'fr', name: 'Français', nativeName: '法语' },
  { code: 'de', name: 'Deutsch', nativeName: '德语' },
  { code: 'es', name: 'Español', nativeName: '西班牙语' },
  { code: 'pt', name: 'Português', nativeName: '葡萄牙语' },
  { code: 'ru', name: 'Русский', nativeName: '俄语' },
  { code: 'ar', name: 'العربية', nativeName: '阿拉伯语' },
];

const SortableLanguageTag: React.FC<{
  lang: (typeof ALL_LANGUAGES)[number];
  index: number;
  isDefault: boolean;
  onUncheck: () => void;
}> = ({ lang, index, isDefault, onUncheck }) => {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: lang.code,
  });
  return (
    <span
      ref={setNodeRef}
      {...attributes}
      {...listeners}
      title="按住拖动调整排序，点击取消启用"
      style={{
        display: 'inline-flex',
        transform: CSS.Transform.toString(transform),
        transition,
        opacity: isDragging ? 0.5 : 1,
        zIndex: isDragging ? 10 : undefined,
        touchAction: 'none',
      }}
    >
      <Tag.CheckableTag
        checked={true}
        onChange={() => onUncheck()}
        style={{
          margin: 0,
          padding: '2px 8px',
          fontSize: 12,
          cursor: isDragging ? 'grabbing' : 'grab',
          userSelect: 'none',
          border: '1px solid transparent',
          display: 'inline-flex',
          alignItems: 'center',
          gap: 4,
        }}
      >
        <Icons.HolderOutlined style={{ fontSize: 11, opacity: 0.75 }} />
        <span style={{ opacity: 0.75, fontSize: 11 }}>{index + 1}.</span>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
          <RegionFlagSvg code={lang.code} />
          <span>{lang.name} {isDefault && '(默认)'}</span>
        </span>
      </Tag.CheckableTag>
    </span>
  );
};

// 登录页风格选择器组件 (简洁单选切换)
const LoginStyleSelector: React.FC<{
  value?: 'split' | 'classic';
  onChange?: (val: 'split' | 'classic') => void;
}> = ({ value, onChange }) => {
  return (
    <Radio.Group value={value || 'split'} onChange={(e) => onChange?.(e.target.value)} buttonStyle="solid">
      <Radio.Button value="split">左右分栏风格</Radio.Button>
      <Radio.Button value="classic">经典居中风格</Radio.Button>
    </Radio.Group>
  );
};

const timezoneOptions = (() => {
  const timezones = Intl.supportedValuesOf ? Intl.supportedValuesOf('timeZone') : [
    'Asia/Shanghai', 'Asia/Tokyo', 'America/New_York', 'Europe/London'
  ];

  const grouped: Record<string, { value: string, label: string }[]> = {};

  timezones.forEach(tz => {
    const parts = tz.split('/');
    if (parts.length >= 2) {
      const group = parts[0];
      const city = parts.slice(1).join('/').replace(/_/g, ' ');

      const date = new Date();
      const str = date.toLocaleString('en-US', { timeZone: tz, timeZoneName: 'shortOffset' });
      const match = str.match(/(GMT|UTC)([+-]\d{1,2}(:\d{2})?)/);
      let offset = '';
      if (match) {
        offset = ` (UTC${match[2]})`;
      } else if (str.includes('GMT') || str.includes('UTC')) {
        offset = ' (UTC+0)';
      }

      if (!grouped[group]) grouped[group] = [];
      grouped[group].push({ value: tz, label: `${tz.replace(/_/g, ' ')}${offset}` });
    }
  });

  return Object.entries(grouped)
    .map(([group, options]) => ({
      label: group,
      options: options.sort((a, b) => a.label.localeCompare(b.label))
    }))
    .sort((a, b) => a.label.localeCompare(b.label));
})();

const Settings: React.FC = () => {
  const { t, i18n } = useTranslation();
  const { settings, updateStoreSettings } = useSettingsStore();
  const currencyUnit = settings?.currency?.currency_unit || '元';
  const adminPath = settings?.site?.admin_path || 'admin';
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const rawTab = searchParams.get('tab') || 'basic';
  const rawSubTab = searchParams.get('subtab') || searchParams.get('sub_tab');
  
  // 兼容直接使用 ?tab=storage 或 ?tab=database 路由
  const tab = rawTab === 'storage' || rawTab === 'cleanup' ? 'database' : rawTab;
  const initialDbSubTab = rawTab === 'storage' ? 'storage' : rawTab === 'cleanup' ? 'cleanup' : (rawSubTab || 'db');
  const initialBasicSubTab = rawSubTab || 'site';

  const [form] = Form.useForm();
  const langDndSensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
  );
  const enableMultilingual = Form.useWatch('enable_multilingual', form);
  const supportedLanguages: string[] = Form.useWatch('supported_languages', form) || ['zh', 'en'];
  const defaultLanguage: string = Form.useWatch('default_language', form) || 'zh';
  const logoUrl = Form.useWatch('logo', form);
  const operationTimezone = Form.useWatch('default_timezone', form);
  const adminDisplayTimezone = Form.useWatch('admin_display_timezone', form);
  const [uploadingLogo, setUploadingLogo] = useState(false);

  const [loading, setLoading] = useState(false);
  const utcAnchorRef = useRef<{ utcMs: number; localMs: number } | null>(null);
  const [clockNow, setClockNow] = useState(() => Date.now());
  const [basicSubTab, setBasicSubTab] = useState(initialBasicSubTab);
  const [dbSubTab, setDbSubTab] = useState(initialDbSubTab);
  const [previewingKey, setPreviewingKey] = useState<string | null>(null);

  // 当 URL searchParams 变化时同步子标签
  useEffect(() => {
    if (rawTab === 'storage') {
      setDbSubTab('storage');
    } else if (rawTab === 'cleanup') {
      setDbSubTab('cleanup');
    } else if (rawSubTab) {
      if (tab === 'database') setDbSubTab(rawSubTab);
      if (tab === 'basic') setBasicSubTab(rawSubTab);
    }
  }, [rawTab, rawSubTab, tab]);
  const [syncDates, setSyncDates] = useState<[dayjs.Dayjs | null, dayjs.Dayjs | null]>([null, null]);
  const [syncingStats, setSyncingStats] = useState(false);
  const [maintaining, setMaintaining] = useState(false);
  const [dbVerifying, setDbVerifying] = useState(false);
  const [resetOpen, setResetOpen] = useState(false);
  const [resetPhrase, setResetPhrase] = useState('');
  const [resetCountdown, setResetCountdown] = useState<number | null>(null);
  const [resetting, setResetting] = useState(false);
  const resetStartedRef = useRef(false);
  const [dbInfo, setDbInfo] = useState<any>(null);
  const [dbInfoLoading, setDbInfoLoading] = useState(false);
  const [dbInfoError, setDbInfoError] = useState<string | null>(null);

  const [securityIpModalConfig, setSecurityIpModalConfig] = useState<{
    open: boolean;
    type: 'blacklist' | 'whitelist';
    mode: 'view' | 'edit';
  }>({
    open: false,
    type: 'blacklist',
    mode: 'view',
  });

  const getIpListFromForm = (fieldName: 'ip_blacklist_text' | 'ip_whitelist_text'): string[] => {
    const raw = form.getFieldValue(fieldName) || '';
    return raw
      .split('\n')
      .map((s: string) => s.trim())
      .filter(Boolean);
  };

  const openSecurityIpModal = (type: 'blacklist' | 'whitelist', mode: 'view' | 'edit') => {
    setSecurityIpModalConfig({
      open: true,
      type,
      mode,
    });
  };

  const [securityEmailModalConfig, setSecurityEmailModalConfig] = useState<{
    open: boolean;
    mode: 'view' | 'edit';
  }>({
    open: false,
    mode: 'view',
  });

  const getEmailWhitelistFromForm = (): string[] => {
    const raw = form.getFieldValue(['registration', 'email_whitelist']);
    if (!Array.isArray(raw)) return [];
    return raw.map((s: unknown) => String(s ?? '').trim()).filter(Boolean);
  };

  const openSecurityEmailModal = (mode: 'view' | 'edit') => {
    setSecurityEmailModalConfig({
      open: true,
      mode,
    });
  };

  const handleManualSync = async () => {
    const [start, end] = syncDates;
    if (!start || !end) {
      message.warning('请选择要同步的日期范围');
      return;
    }
    
    setSyncingStats(true);
    try {
      const startStr = encodeURIComponent(toCalendarDateParam(start));
      const endStr = encodeURIComponent(toCalendarDateParam(end));
      const r = await (request.post(`/settings/usage-stats/sync?start_date=${startStr}&end_date=${endStr}`) as any);
      if (r.success) {
        message.success(r.message || '手动同步任务已在后台异步启动，请在后台查看日志');
        setSyncDates([null, null]);
      } else {
        message.error(r.message || '启动同步任务失败');
      }
    } catch (e) {
      console.error(e);
    } finally {
      setSyncingStats(false);
    }
  };

  const handleTriggerMaintenance = async () => {
    try {
      setMaintaining(true);
      const res = await (request.post('/settings/cron/trigger-maintenance') as any);
      message.success(res?.message || '系统日常维护任务已在后台触发执行');
    } catch (err: any) {
      message.error(err?.message || '触发系统日常维护任务失败');
    } finally {
      setMaintaining(false);
    }
  };

  const closeResetModal = () => {
    if (resetting) return;
    resetStartedRef.current = false;
    setResetOpen(false);
    setResetPhrase('');
    setResetCountdown(null);
  };

  const handleVerifyDatabase = async () => {
    setDbVerifying(true);
    try {
      const r = await (request.post('/settings/database/verify', {}) as any);
      r.success ? message.success(r.message) : message.error(r.message);
    } catch { /* 全局拦截器已统一处理 */ }
    finally { setDbVerifying(false); }
  };

  const fetchDbInfo = async () => {
    setDbInfoLoading(true);
    setDbInfoError(null);
    try {
      const r = await (request.get('/settings/database/info') as any);
      setDbInfo(r);
    } catch (e: any) {
      setDbInfo(null);
      setDbInfoError(e?.response?.data?.error?.message || '无法读取数据库状态');
    } finally {
      setDbInfoLoading(false);
    }
  };

  const executeDatabaseReset = async () => {
    setResetting(true);
    try {
      const r = await (request.post(
        '/settings/database/initialize',
        { confirm: DB_RESET_CONFIRM_TEXT },
        { skipErrorHandler: true } as any,
      ) as any);
      if (r.success) {
        enterFreshSetup();
        return;
      }
      message.error(r.message || '清空失败');
      resetStartedRef.current = false;
      setResetCountdown(null);
    } catch (e: any) {
      const status = e?.response?.status;
      // 已发出清空：无响应或 5xx 时库可能已空，转入与全新安装相同的等待/初始化
      if (!status || status >= 500) {
        enterFreshSetup();
        return;
      }
      message.error(e?.response?.data?.error?.message || '清空失败');
      resetStartedRef.current = false;
      setResetCountdown(null);
    } finally {
      setResetting(false);
    }
  };

  const [userLevels, setUserLevels] = useState<any[]>([]);
  const [menuItems, setMenuItems] = useState<any[]>([]);
  const [loadingMenu, setLoadingMenu] = useState(true);

  const getTitle = () => {
    switch (tab) {
      case 'database': return '存储设置';
      default: return t('menu.basic_settings');
    }
  };

  useEffect(() => { fetchSettings(); }, [tab]);

  useEffect(() => {
    if (tab !== 'basic') return;
    const id = window.setInterval(() => setClockNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [tab]);

  useEffect(() => {
    if (resetCountdown === null) return;
    if (resetCountdown === 0) {
      if (resetStartedRef.current) return;
      resetStartedRef.current = true;
      void executeDatabaseReset();
      return;
    }
    const timer = window.setTimeout(() => {
      setResetCountdown((n) => (n === null ? n : n - 1));
    }, 1000);
    return () => window.clearTimeout(timer);
  }, [resetCountdown]);

  useEffect(() => {
    if (tab === 'database' && dbSubTab === 'db') {
      void fetchDbInfo();
    }
  }, [tab, dbSubTab]);

  const fetchSettings = async () => {
    try {
      setLoadingMenu(true);
      const [response, levelsResponse, pluginsResponse] = await Promise.all([
        request.get('/settings/full') as any,
        request.get('/user_levels') as any,
        request.get('/plugins') as any
      ]);
      const { site, currency, login, registration, smtp, database: backendDatabase, agreement, storage, log_cleanup, menu_config, relay, server_time } = response;
      updateStoreSettings(response);
      if (server_time) {
        const parsed = Date.parse(`${String(server_time).trim().replace(' ', 'T')}Z`);
        if (!Number.isNaN(parsed)) {
          utcAnchorRef.current = { utcMs: parsed, localMs: Date.now() };
          setClockNow(Date.now());
        }
      }
      const defaultDatabase = { db_type: 'postgres', host: 'postgres', port: 5432, database: 'tokensapi', username: 'tokensapi', password: 'tokensapi', ssl_mode: false };
      const loadedDatabase = { ...defaultDatabase, ...backendDatabase };
      const defaultAgreement = { ...DEFAULT_AGREEMENT_SETTINGS };
      const loadedAgreement = {
        tos_mode: agreement?.tos_mode || defaultAgreement.tos_mode,
        tos_mode_en: agreement?.tos_mode_en || defaultAgreement.tos_mode_en,
        tos_content: (agreement?.tos_content !== undefined && agreement?.tos_content !== '') ? agreement.tos_content : defaultAgreement.tos_content,
        tos_content_en: (agreement?.tos_content_en !== undefined && agreement?.tos_content_en !== '') ? agreement.tos_content_en : defaultAgreement.tos_content_en,
        tos_link: agreement?.tos_link || '',
        tos_link_en: agreement?.tos_link_en || '',
        privacy_mode: agreement?.privacy_mode || defaultAgreement.privacy_mode,
        privacy_mode_en: agreement?.privacy_mode_en || defaultAgreement.privacy_mode_en,
        privacy_content: (agreement?.privacy_content !== undefined && agreement?.privacy_content !== '') ? agreement.privacy_content : defaultAgreement.privacy_content,
        privacy_content_en: (agreement?.privacy_content_en !== undefined && agreement?.privacy_content_en !== '') ? agreement.privacy_content_en : defaultAgreement.privacy_content_en,
        tos_enabled: agreement?.tos_enabled !== undefined ? agreement.tos_enabled : defaultAgreement.tos_enabled,
        privacy_enabled: agreement?.privacy_enabled !== undefined ? agreement.privacy_enabled : defaultAgreement.privacy_enabled,
      };
      
      const allLevels = Array.isArray(levelsResponse) ? levelsResponse : (levelsResponse.data || levelsResponse.levels || []);
      setUserLevels(allLevels);

      const activePluginsList = pluginsResponse?.plugins || [];
      const isPluginActive = (pluginName: string) => {
        const p = activePluginsList.find((item: any) => item.name === pluginName);
        return p ? p.is_enabled === 1 : false;
      };

      const defaultMenuItems = [
        { key: '/dashboard', label_zh: '系统概览', label_en: 'Dashboard', icon: 'DashboardOutlined', enabled: true, sort_order: 1, allowed_levels: 'all' },
        { key: '/playground', label_zh: '创作中心', label_en: 'Playground', icon: 'ExperimentOutlined', enabled: true, sort_order: 2, allowed_levels: 'all' },
        { key: '/playground-2026', label_zh: '创作中心2026', label_en: 'Playground 2026', icon: 'ExperimentOutlined', enabled: true, sort_order: 2.5, allowed_levels: 'all' },
        { key: '/docs', label_zh: 'API教程', label_en: 'Relay API', icon: 'RocketOutlined', enabled: true, sort_order: 3, allowed_levels: 'all' },
        { key: '/apikeys', label_zh: '令牌管理', label_en: 'API Keys', icon: 'KeyOutlined', enabled: true, sort_order: 4, allowed_levels: 'all' },
        { key: '/logs', label_zh: '日志记录', label_en: 'Logs', icon: 'HistoryOutlined', enabled: true, sort_order: 5, allowed_levels: 'all' },
        { key: '/task-logs', label_zh: '任务列表', label_en: 'Task Logs', icon: 'ScheduleOutlined', enabled: true, sort_order: 6, allowed_levels: 'all' },
        { key: '/advanced-marketing', label_zh: '高级推广', label_en: 'Advanced Marketing', icon: 'TeamOutlined', enabled: true, sort_order: 10, allowed_levels: 'all' },
        { key: '/wallet', label_zh: '我的钱包', label_en: 'Wallet', icon: 'WalletOutlined', enabled: true, sort_order: 11, allowed_levels: 'all' },
        { key: '/ark-video-monitor', label_zh: '视频监控', label_en: 'Ark Video Monitor', icon: 'VideoCameraOutlined', enabled: true, sort_order: 11.5, allowed_levels: 'all' },
        { key: '/profile', label_zh: '个人中心', label_en: 'Profile', icon: 'UserOutlined', enabled: true, sort_order: 12, allowed_levels: 'all' },
      ];

      let loadedItems = [];
      if (menu_config && menu_config.items && menu_config.items.length > 0) {
        loadedItems = menu_config.items.map((item: any) => {
          if (item.key === '/relay-api') {
            return { ...item, key: '/docs' };
          }
          return item;
        });
      } else {
        loadedItems = [...defaultMenuItems];
      }

      defaultMenuItems.forEach((defItem) => {
        if (!loadedItems.some((item: any) => item.key === defItem.key)) {
          loadedItems.push({
            ...defItem,
            sort_order: loadedItems.length + 1
          });
        }
      });

      const filteredItems = loadedItems.filter((item: any) => {
        if (item.key === '/moderation-query' || item.key === '/assets' || item.key === '/assets-intl') return false;
        if (item.key === '/playground') return isPluginActive('playground');
        if (item.key === '/playground-2026') return isPluginActive('playground_2026');
        if (item.key === '/advanced-marketing') return isPluginActive('team_marketing');
        if (item.key === '/ark-video-monitor') return isPluginActive('volcengine_ark_monitor');

        return true;
      });

      setMenuItems(filteredItems.sort((a: any, b: any) => (a.sort_order || 0) - (b.sort_order || 0)));

      form.setFieldsValue({
        ...site,
        copyright: (site?.copyright !== undefined && site?.copyright !== null && site?.copyright !== '') ? site.copyright : '© 2026 laiai. All rights reserved.',
        default_timezone: site?.default_timezone || 'Asia/Shanghai',
        admin_display_timezone: site?.admin_display_timezone || site?.default_timezone || 'Asia/Shanghai',
        admin_path: site?.admin_path || 'admin',
        ip_blacklist_enabled: site?.ip_blacklist_enabled === true,
        ip_blacklist_text: (site?.ip_blacklist || []).join('\n'),
        ip_whitelist_enabled: site?.ip_whitelist_enabled === true,
        ip_whitelist_text: (site?.ip_whitelist && site.ip_whitelist.length > 0)
          ? site.ip_whitelist.join('\n')
          : (site?.ip_whitelist_enabled ? '10.0.0.0/24\n192.168.1.0/24\n172.16.0.0/24\n127.0.0.0/24' : ''),
        login_style: site?.login_style || 'split',
        login_quote: site?.login_quote || '',
        show_timezone: site?.show_timezone !== false,
        login: login || {},
        registration: {
          ...(registration || {}),
          ip_rate_limit_enabled: registration?.ip_rate_limit_enabled !== false,
          ip_daily_limit: registration?.ip_daily_limit ?? 3,
          ip_window_days: registration?.ip_window_days ?? 30,
          ip_window_limit: registration?.ip_window_limit ?? 10,
          email_whitelist: Array.isArray(registration?.email_whitelist)
            ? registration.email_whitelist.map((s: string) => String(s).trim()).filter(Boolean)
            : [],
          require_bind_mobile: registration?.require_bind_mobile === true,
          require_bind_email: registration?.require_bind_email === true,
          bind_enforcement: registration?.bind_enforcement === 'prompt_only'
            ? 'prompt_all'
            : (registration?.bind_enforcement || 'all'),
          enable_user_kyc: registration?.enable_user_kyc === true,
          max_accounts_per_email: registration?.max_accounts_per_email || 5,
          max_accounts_per_mobile: registration?.max_accounts_per_mobile || 5,
          max_accounts_per_wechat: registration?.max_accounts_per_wechat || 5,
        },
        smtp,
        database: loadedDatabase,
        storage: {
          ...(storage || {}),
          default_provider: storage?.default_provider ?? 'tos',
        },
        log_cleanup: {
          log_retention_days: log_cleanup?.log_retention_days ?? 30,
          log_row_retention_days: log_cleanup?.log_row_retention_days ?? 0,
          error_log_retention_days: log_cleanup?.error_log_retention_days ?? 0,
          daily_stats_hour: log_cleanup?.daily_stats_hour ?? 1,
          daily_stats_minute: log_cleanup?.daily_stats_minute ?? 0,
          clean_hour: log_cleanup?.clean_hour ?? 2,
          clean_minute: log_cleanup?.clean_minute ?? 30,
          archive_hour: log_cleanup?.archive_hour ?? 3,
          archive_minute: log_cleanup?.archive_minute ?? 30,
          error_clean_hour: log_cleanup?.error_clean_hour ?? 4,
          error_clean_minute: log_cleanup?.error_clean_minute ?? 0,
          storage_clean_hour: log_cleanup?.storage_clean_hour ?? 4,
          storage_clean_minute: log_cleanup?.storage_clean_minute ?? 30,
          maintenance_hour: log_cleanup?.maintenance_hour ?? 3,
          maintenance_minute: log_cleanup?.maintenance_minute ?? 0,
        },
        agreement: loadedAgreement,
        relay: {
          manual_poll_upstream: relay?.manual_poll_upstream !== false,
          poll_tick_secs: clampPollTickSecs(relay?.poll_tick_secs),
          enable_debug_log: relay?.enable_debug_log === true,
          stream_idle: {
            chat_secs: clampStreamIdleSecs(relay?.stream_idle?.chat_secs),
            image_secs: clampStreamIdleSecs(relay?.stream_idle?.image_secs),
          },
          inflight_limits: loadInflightLimits(relay),
        },
      });
    } catch (error) {
      console.error('Failed to fetch settings:', error);
      // 全局拦截器已统一弹出错误提示
    } finally {
      setLoadingMenu(false);
    }
  };


  const handleSave = async (): Promise<boolean> => {
    try {
      const values = form.getFieldsValue(true);
      let payload: any = {};

      if (tab === 'basic') {
        const nonemptyLines = (text: string) => text.split('\n').map((s) => s.trim()).filter(Boolean);
        if (values.ip_whitelist_enabled === true && nonemptyLines(values.ip_whitelist_text || '').length === 0) {
          values.ip_whitelist_text = '10.0.0.0/24\n192.168.1.0/24\n172.16.0.0/24\n127.0.0.0/24';
          form.setFieldsValue({ ip_whitelist_text: values.ip_whitelist_text });
        }
        const ipListConflictErr = validateSiteIpLists(
          nonemptyLines(values.ip_blacklist_text || ''),
          nonemptyLines(values.ip_whitelist_text || '')
        );
        if (ipListConflictErr) {
          message.error(ipListConflictErr);
          return false;
        }
        const emailDomains = values.registration?.email_whitelist;
        const normalizedEmailDomains = Array.isArray(emailDomains)
          ? emailDomains.map((d: string) => String(d).trim()).filter(Boolean)
          : [];
        if (values.registration?.email_whitelist_enabled === true && normalizedEmailDomains.length === 0) {
          message.error('已开启邮箱白名单，请至少填写一个允许的邮箱域名');
          return false;
        }
        const normalizedIpDailyLimit = Math.min(10000, Math.max(1, Math.floor(Number(values.registration?.ip_daily_limit) || 3)));
        const normalizedIpWindowDays = Math.min(3650, Math.max(1, Math.floor(Number(values.registration?.ip_window_days) || 30)));
        const normalizedIpWindowLimit = Math.min(100000, Math.max(1, Math.floor(Number(values.registration?.ip_window_limit) || 10)));
        if (
          values.registration?.ip_rate_limit_enabled === true &&
          normalizedIpWindowLimit < normalizedIpDailyLimit
        ) {
          message.error(
            `IP 注册限制参数冲突：${normalizedIpWindowDays} 天内最多注册个数（${normalizedIpWindowLimit}）不能小于每日最多注册个数（${normalizedIpDailyLimit}）`
          );
          return false;
        }
        payload.site = {
          ...settings?.site,
          name: values.name || '', logo: values.logo || '', title: values.title || '',
          intro: values.intro || '',
          keywords: values.keywords || '', description: values.description || '',
          favicon: values.favicon || '',
          logo_title_url: (values.logo_title_url || '').trim(),
          login_title: values.login_title || '',
          login_title_url: (values.login_title_url || '').trim(),
          login_subtitle: values.login_subtitle || '',
          login_style: values.login_style || 'split',
          login_quote: values.login_quote || '',
          enable_multilingual: values.enable_multilingual !== false,
          supported_languages: values.enable_multilingual === false
            ? [values.default_language || 'zh']
            : (values.supported_languages || ['zh', 'en']),
          default_language: values.default_language || 'zh',
          enable_theme_toggle: values.enable_theme_toggle !== false,
          default_theme: values.default_theme || 'dark',
          default_timezone: values.default_timezone || settings?.site?.default_timezone || 'Asia/Shanghai',
          admin_display_timezone: values.admin_display_timezone || values.default_timezone || settings?.site?.default_timezone || 'Asia/Shanghai',
          show_timezone: values.show_timezone !== false,
          copyright: values.copyright || '',
          admin_path: values.admin_path || 'admin',
          ip_blacklist_enabled: values.ip_blacklist_enabled === true,
          ip_blacklist: (values.ip_blacklist_text || '')
            .split('\n')
            .map((s: string) => s.trim())
            .filter(Boolean),
          ip_whitelist_enabled: values.ip_whitelist_enabled === true,
          ip_whitelist: (values.ip_whitelist_text || '')
            .split('\n')
            .map((s: string) => s.trim())
            .filter(Boolean),
        };
        payload.login = {
          ...settings?.login,
          ...values.login,
        };
        payload.registration = {
          ...settings?.registration,
          ...values.registration,
          ip_daily_limit: normalizedIpDailyLimit,
          ip_window_days: normalizedIpWindowDays,
          ip_window_limit: normalizedIpWindowLimit,
          email_whitelist: normalizedEmailDomains,
          require_bind_mobile: values.registration?.require_bind_mobile === true,
          require_bind_email: values.registration?.require_bind_email === true,
          bind_enforcement: (['all', 'any', 'prompt_all', 'prompt_any'].includes(values.registration?.bind_enforcement)
            ? values.registration.bind_enforcement
            : values.registration?.bind_enforcement === 'prompt_only'
              ? 'prompt_all'
              : 'all'),
          enable_user_kyc: values.registration?.enable_user_kyc === true,
          max_accounts_per_email: Math.min(99, Math.max(1, Number(values.registration?.max_accounts_per_email) || 5)),
          max_accounts_per_mobile: Math.min(99, Math.max(1, Number(values.registration?.max_accounts_per_mobile) || 5)),
          max_accounts_per_wechat: Math.min(99, Math.max(1, Number(values.registration?.max_accounts_per_wechat) || 5)),
        };
        payload.agreement = {
          ...settings?.agreement,
          ...values.agreement,
        };
        payload.menu_config = {
          ...settings?.menu_config,
          items: menuItems.map((item, idx) => ({
            ...item,
            sort_order: idx + 1
          }))
        };
        payload.relay = {
          manual_poll_upstream: values.relay?.manual_poll_upstream !== false,
          poll_tick_secs: clampPollTickSecs(values.relay?.poll_tick_secs),
          enable_debug_log: values.relay?.enable_debug_log === true,
          stream_idle: {
            chat_secs: clampStreamIdleSecs(values.relay?.stream_idle?.chat_secs),
            image_secs: clampStreamIdleSecs(values.relay?.stream_idle?.image_secs),
          },
          inflight_limits: dumpInflightLimits(values.relay),
        };
      } else if (tab === 'database') {
        if (dbSubTab === 'db') {
          return false;
        }
        if (dbSubTab === 'storage') {
          payload.storage = {
            ...settings?.storage,
            ...values.storage,
          };
        } else if (dbSubTab === 'cleanup') {
          payload.log_cleanup = {
            ...settings?.log_cleanup,
            ...values.log_cleanup,
          };
        }
      }

      const oldAdminPath = settings?.site?.admin_path || 'admin';
      setLoading(true);
      const updatedSettings = await (request.post('/settings', payload) as any);
      message.success(t('settings.save_success'));
      updateStoreSettings(updatedSettings);
      if (payload.site?.title) document.title = payload.site.title;

      const newAdminPath = updatedSettings.site?.admin_path || 'admin';
      if (oldAdminPath !== newAdminPath && tab === 'basic') {
        const newUrl = window.location.pathname.replace(new RegExp(`^/${oldAdminPath}(?=/|$)`), `/${newAdminPath}`) + window.location.search;
        window.location.replace(newUrl);
      }
      return true;
    } catch (error) {
      console.error('Failed to update settings:', error);
      // 全局拦截器已统一弹出错误提示
      return false;
    } finally {
      setLoading(false);
    }
  };

  const GoLink: React.FC<{ to: string; text: string }> = ({ to, text }) => (
    <Button type="link" size="small" onClick={() => navigate(to)} style={{ padding: 0, height: 'auto' }}>{text}</Button>
  );

  const siteSettingsContent = (
    <div style={{ maxWidth: 680 }}>
      <Form.Item label={t('settings.site_name')} name="name" rules={[{ required: true }]}><Input placeholder="laiai" /></Form.Item>
      <Form.Item
        label="站点简介"
        name="intro"
      >
        <Input.TextArea
          rows={3}
          placeholder="请输入站点简介，例如：企业级多模型聚合调度与中转分发平台..."
          maxLength={300}
          showCount
        />
      </Form.Item>
      <Form.Item label="站点 Logo" extra={<Text type="secondary">支持图片链接，建议尺寸 32x32 或 40x40，留空则显示站点名称</Text>}>
        <Space.Compact style={{ width: '100%' }}>
          <Form.Item name="logo" noStyle>
            <Input placeholder="https://example.com/logo.png" />
          </Form.Item>
          <Upload
            accept="image/*"
            showUploadList={false}
            beforeUpload={async (file) => {
              if (!file.type.startsWith('image/')) {
                message.error('只支持上传图片格式的文件！');
                return Upload.LIST_IGNORE;
              }
              if (file.size > 5 * 1024 * 1024) {
                message.error('图片大小不能超过 5MB！');
                return Upload.LIST_IGNORE;
              }
              try {
                setUploadingLogo(true);
                const formData = new FormData();
                formData.append('file', file);
                formData.append('slot', 'logo');
                
                const res = await (request.post('/settings/upload', formData, {
                  headers: {
                    'Content-Type': 'multipart/form-data',
                  },
                }) as Promise<any>);
                
                if (res?.file_url) {
                  form.setFieldsValue({ logo: res.file_url });
                  message.success('Logo 上传成功！');
                } else {
                  message.error('上传成功，但未返回有效的图片链接');
                }
              } catch (err: any) {
                console.error(err);
                const errMsg = err.response?.data?.error?.message || err.message || '图片上传失败';
                message.error(errMsg);
              } finally {
                setUploadingLogo(false);
              }
              return Upload.LIST_IGNORE;
            }}
          >
            <Button icon={<UploadOutlined />} loading={uploadingLogo}>上传图片</Button>
          </Upload>
        </Space.Compact>
        {logoUrl && (
          <div style={{ marginTop: 6, display: 'flex', alignItems: 'center', gap: 8 }}>
            <Text type="secondary" style={{ fontSize: 12 }}>预览:</Text>
            <img src={logoUrl} alt="Logo Preview" style={{ height: 26, maxWidth: 100, objectFit: 'contain', borderRadius: 4, border: '1px solid var(--ant-color-border)', padding: '2px', background: '#fff' }} />
          </div>
        )}
      </Form.Item>
      <Form.Item
        label="控制台 Logo 标题链接"
        name="logo_title_url"
        extra={<Text type="secondary">配置后，控制台侧栏/顶栏 Logo 与站点名可点击跳转；支持 https://… 或站内 /docs，留空则不可点击</Text>}
      >
        <Input placeholder="例如：https://example.com 或 /" />
      </Form.Item>
      <Form.Item label={t('settings.site_title')} name="title" rules={[{ required: true }]}><Input placeholder="laiai - LLM API Gateway" /></Form.Item>
      <Form.Item label="站点图标 (Favicon)" name="favicon" extra={<Text type="secondary">支持 .ico / .png / .svg 链接</Text>}>
        <Input placeholder="https://example.com/favicon.ico" />
      </Form.Item>
      <Form.Item label={t('settings.site_keywords')} name="keywords"><Input.TextArea rows={2} placeholder="LLM, API, Gateway" /></Form.Item>
      <Form.Item label={t('settings.site_description')} name="description"><Input.TextArea rows={3} placeholder="Description..." /></Form.Item>
      <Form.Item label="站点多语言" extra={<Text type="secondary">开启后右上角显示语言切换；关闭则全站固定使用默认语言</Text>}>
        <Form.Item name="enable_multilingual" valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item name="supported_languages" noStyle />
      {(() => {
        const implementedLangs = i18n.options.resources ? Object.keys(i18n.options.resources) : ['zh', 'zh-TW', 'en', 'ja', 'ko', 'vi', 'th', 'ms', 'fr', 'de', 'es', 'pt', 'ru', 'ar'];
        const enabledLangCodes = supportedLanguages.filter(
          code => implementedLangs.includes(code) && ALL_LANGUAGES.some(l => l.code === code),
        );
        const enabledLangObjects = enabledLangCodes
          .map(code => ALL_LANGUAGES.find(l => l.code === code)!)
          .filter(Boolean);
        const disabledLangObjects = ALL_LANGUAGES.filter(l => !enabledLangCodes.includes(l.code));
        const orderedAllLanguages = [...enabledLangObjects, ...disabledLangObjects];
        const defaultLangOptions = (enableMultilingual ? enabledLangObjects : orderedAllLanguages.filter(l => implementedLangs.includes(l.code)))
          .map(l => ({
            label: (
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                <RegionFlagSvg code={l.code} />
                <span>{l.name} ({l.nativeName})</span>
              </span>
            ),
            value: l.code,
          }));

        const handleLangDragEnd = (event: DragEndEvent) => {
          const { active, over } = event;
          if (!over || active.id === over.id) return;
          const oldIndex = enabledLangCodes.indexOf(String(active.id));
          const newIndex = enabledLangCodes.indexOf(String(over.id));
          if (oldIndex < 0 || newIndex < 0) return;
          form.setFieldsValue({ supported_languages: arrayMove(enabledLangCodes, oldIndex, newIndex) });
        };

        return (
          <div style={{ border: '1px solid var(--border-custom, var(--ant-color-border-secondary, rgba(128,128,128,0.2)))', borderRadius: 6, padding: '12px 14px', marginBottom: 14, marginTop: -4 }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8 }}>
              <Text strong style={{ fontSize: 13 }}>🌐 语言配置</Text>
              {enableMultilingual && (
                <Space size={12}>
                  <Button size="small" type="link" style={{ padding: 0, height: 'auto' }} onClick={() => {
                    const defaultOrdered = ALL_LANGUAGES
                      .map(l => l.code)
                      .filter(code => enabledLangCodes.includes(code));
                    form.setFieldsValue({ supported_languages: defaultOrdered });
                  }}>恢复默认排序</Button>
                  <Button size="small" type="link" style={{ padding: 0, height: 'auto' }} onClick={() => {
                    const remaining = ALL_LANGUAGES
                      .map(l => l.code)
                      .filter(code => implementedLangs.includes(code) && !enabledLangCodes.includes(code));
                    form.setFieldsValue({ supported_languages: [...enabledLangCodes, ...remaining] });
                  }}>全部启用已翻译语言</Button>
                </Space>
              )}
            </div>
            {enableMultilingual && (
              <div style={{ marginBottom: 10 }}>
                <Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 6 }}>
                  已启用的语言（按住拖动已启用标签可调整控制台顶部导航与创作中心2026语言菜单排序）：
                </Text>
                <DndContext sensors={langDndSensors} collisionDetection={closestCenter} onDragEnd={handleLangDragEnd}>
                  <SortableContext items={enabledLangCodes} strategy={rectSortingStrategy}>
                    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'center' }}>
                      {enabledLangObjects.map((lang, idx) => (
                        <SortableLanguageTag
                          key={lang.code}
                          lang={lang}
                          index={idx}
                          isDefault={defaultLanguage === lang.code}
                          onUncheck={() => {
                            let newLangs = enabledLangCodes.filter((l: string) => l !== lang.code);
                            if (newLangs.length === 0) newLangs = ['zh'];
                            if (defaultLanguage === lang.code) {
                              form.setFieldsValue({ default_language: newLangs[0] });
                            }
                            form.setFieldsValue({ supported_languages: newLangs });
                          }}
                        />
                      ))}
                      {disabledLangObjects.map(lang => {
                        const isImplemented = implementedLangs.includes(lang.code);
                        return (
                          <Tag.CheckableTag
                            key={lang.code}
                            checked={false}
                            disabled={!isImplemented}
                            onChange={(checked) => {
                              if (!isImplemented || !checked) return;
                              form.setFieldsValue({ supported_languages: [...enabledLangCodes, lang.code] });
                            }}
                            style={{
                              margin: 0,
                              padding: '2px 8px',
                              fontSize: 12,
                              cursor: isImplemented ? 'pointer' : 'not-allowed',
                              opacity: isImplemented ? 1 : 0.6,
                              border: '1px dashed var(--border-custom, rgba(128,128,128,0.3))',
                              display: 'inline-flex',
                              alignItems: 'center',
                              gap: 5,
                            }}
                          >
                            <RegionFlagSvg code={lang.code} />
                            <span>{lang.name}</span>
                          </Tag.CheckableTag>
                        );
                      })}
                    </div>
                  </SortableContext>
                </DndContext>
              </div>
            )}
            <Form.Item label="默认语言" name="default_language" style={{ marginBottom: 0 }}>
              <Select
                style={{ width: 220 }}
                options={defaultLangOptions}
                onChange={(code: string) => {
                  if (!enableMultilingual) {
                    form.setFieldsValue({ supported_languages: [code] });
                  }
                }}
              />
            </Form.Item>
          </div>
        );
      })()}
      <Text type="secondary" style={{ display: 'block', marginBottom: 16, fontSize: 12, lineHeight: 1.6 }}>
        系统有三层时区：底层时钟固定 UTC（当前：{formatUtcClock(utcAnchorRef.current, clockNow)} UTC）；站点运营时区决定业务自然日；管理后台展示时区只影响后台列表与筛选，类似用户个人时区。
      </Text>
      <Form.Item
        label={
          <span>
            站点运营时区
            <Text type="secondary" style={{ fontWeight: 400, marginLeft: 8, fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace" }}>
              {formatTzClock(utcAnchorRef.current, clockNow, operationTimezone)}
            </Text>
          </span>
        }
        name="default_timezone"
        extra={
          <Text type="secondary">
            计费时段、额度刷新、仪表盘/财务日统计、佣金按日、注册日限额。不改变底层 UTC，也不改变后台列表墙钟。
          </Text>
        }
      >
        <Select
          style={{ width: 320 }}
          showSearch
          placeholder="请选择站点运营时区（IANA）"
          options={timezoneOptions}
          filterOption={(input, option: any) =>
            (option?.label as string ?? '').toLowerCase().includes(input.toLowerCase()) ||
            (option?.value as string ?? '').toLowerCase().includes(input.toLowerCase())
          }
        />
      </Form.Item>
      <Form.Item
        label={
          <span>
            管理后台展示时区
            <Text type="secondary" style={{ fontWeight: 400, marginLeft: 8, fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace" }}>
              {formatTzClock(utcAnchorRef.current, clockNow, adminDisplayTimezone)}
            </Text>
          </span>
        }
        name="admin_display_timezone"
        extra={
          <Text type="secondary">
            管理后台日志、订单、用户等列表的时间展示与日期筛选。不影响业务统计自然日。
          </Text>
        }
      >
        <Select
          style={{ width: 320 }}
          showSearch
          placeholder="请选择管理后台展示时区（IANA）"
          options={timezoneOptions}
          filterOption={(input, option: any) =>
            (option?.label as string ?? '').toLowerCase().includes(input.toLowerCase()) ||
            (option?.value as string ?? '').toLowerCase().includes(input.toLowerCase())
          }
        />
      </Form.Item>
      <Form.Item
        label="显示时区后缀"
        extra={<Text type="secondary">开启后在展示绝对时间时追加 (UTC+8) 等偏移标记</Text>}
      >
        <Form.Item name="show_timezone" valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item label="允许主题切换" extra={<Text type="secondary">开启后用户可切换亮暗模式；关闭则固定使用默认主题</Text>}>
        <Form.Item name="enable_theme_toggle" valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item label="站点默认主题" name="default_theme">
        <Radio.Group buttonStyle="solid">
          <Radio.Button value="dark">🌙 暗色模式</Radio.Button>
          <Radio.Button value="light">☀️ 亮色模式</Radio.Button>
        </Radio.Group>
      </Form.Item>
      <Form.Item label="版权信息" name="copyright" extra={<Text type="secondary">展示在登录页及底部，留空则不显示</Text>}>
        <Input placeholder="© 2026 laiai. All rights reserved." />
      </Form.Item>
    </div>
  );

  const securitySettingsContent = (
    <div style={{ maxWidth: 680 }}>
      <Form.Item
        label="管理后台访问路径"
        name="admin_path"
        rules={[
          { required: true, message: '请输入管理后台访问路径' },
          { pattern: /^[a-zA-Z0-9_\-]+$/, message: '路径仅支持字母、数字、下划线和中划线' },
          {
            validator: (_, value) => {
              const reserved = new Set([
                'api', 'v1', 'v2', 'mcp', 'tasks', 'ent', 'portal', 'portal-pro', 'home', 'home-pro',
                'devdocs', 'blog', 'zh', 'en', 'ja', 'ko', 'vi', 'assets', 'assets-intl',
                'login', 'register', 'forgot-password', 'legal', 'offers', 'credits', 'orders',
                'promo', 'docs', 'models', 'playground', 'playground-2026', 'dashboard',
                'apikeys', 'tokens', 'logs', 'task-logs', 'wallet', 'profile', 'tickets',
                'affiliate', 'invoices', 'advanced-marketing', 'users', 'admins', 'channels',
                'redemptions', 'user-levels', 'admin-groups', 'finance', 'settings',
                'payment-settings', 'message-notification', 'oauth-settings', 'marketing',
                'about', 'plugins',
              ]);
              if (value && reserved.has(String(value).trim().toLowerCase())) {
                return Promise.reject(new Error('该路径为系统保留路由，请更换其他路径名称'));
              }
              return Promise.resolve();
            },
          }
        ]}
        extra={<Text type="secondary">修改后后台入口变为新路径，如 /admin，默认 admin</Text>}
      >
        <Input placeholder="admin" />
      </Form.Item>

      <Divider style={{ margin: '16px 0 12px' }}>用户实名认证 (KYC)</Divider>
      <Form.Item
        label="开启用户实名"
        extra={<Text type="secondary">开启后用户可在个人中心提交实名认证</Text>}
      >
        <Form.Item name={['registration', 'enable_user_kyc']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>

      <Divider style={{ margin: '16px 0 12px' }}>账号绑定策略</Divider>
      <Text type="secondary" style={{ display: 'block', marginBottom: 12, fontSize: 12 }}>
        开启后，未满足条件的用户登录会弹窗提醒。执行方式决定是否在创建 API 令牌时硬拦截。
      </Text>
      <Form.Item
        label="绑定手机"
        extra={<Text type="secondary">需短信通道可用。<GoLink to={`/${adminPath}/message-notification`} text={t('settings.goto_settings')} /></Text>}
      >
        <Form.Item name={['registration', 'require_bind_mobile']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item
        label="绑定邮箱"
        extra={<Text type="secondary">需邮件通道可用。<GoLink to={`/${adminPath}/message-notification`} text={t('settings.goto_settings')} /></Text>}
      >
        <Form.Item name={['registration', 'require_bind_email']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item noStyle dependencies={[['registration', 'require_bind_mobile'], ['registration', 'require_bind_email']]}>
        {({ getFieldValue }) => {
          const needMobile = getFieldValue(['registration', 'require_bind_mobile']);
          const needEmail = getFieldValue(['registration', 'require_bind_email']);
          if (!needMobile && !needEmail) return null;
          return (
            <Form.Item
              label="执行方式"
              name={['registration', 'bind_enforcement']}
              initialValue="all"
            >
              <Radio.Group buttonStyle="solid" style={{ display: 'flex', flexWrap: 'wrap', rowGap: 8 }}>
                <Radio.Button value="all">弹窗必须绑定全部</Radio.Button>
                <Radio.Button value="any">弹窗必须绑定其一</Radio.Button>
                <Radio.Button value="prompt_all">仅弹窗提示绑定全部</Radio.Button>
                <Radio.Button value="prompt_any">仅弹窗提示绑定其一</Radio.Button>
              </Radio.Group>
            </Form.Item>
          );
        }}
      </Form.Item>

      <Divider style={{ margin: '16px 0 12px' }}>安全策略</Divider>

      <Form.Item
        label="开启注册 IP 黑名单"
        extra={<Text type="secondary">开启后，黑名单内的 IP 不能登录、注册、找回密码和绑定账号，也不能发送这些操作的验证码。管理后台登录不受此限</Text>}
      >
        <Form.Item name="ip_blacklist_enabled" valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item name="ip_blacklist_text" hidden>
        <Input />
      </Form.Item>
      <Form.Item
        noStyle
        shouldUpdate={(prevValues, currentValues) =>
          prevValues.ip_blacklist_enabled !== currentValues.ip_blacklist_enabled ||
          prevValues.ip_blacklist_text !== currentValues.ip_blacklist_text
        }
      >
        {({ getFieldValue }) => {
          const enabled = getFieldValue('ip_blacklist_enabled');
          const rawText = getFieldValue('ip_blacklist_text') || '';
          const list = rawText.split('\n').map((s: string) => s.trim()).filter(Boolean);
          if (!enabled) return null;
          return (
            <div
              style={{
                marginBottom: 18,
                padding: '12px 16px',
                border: '1px solid rgba(128,128,128,0.2)',
                borderRadius: 8,
                background: 'var(--ant-color-fill-alter, rgba(0,0,0,0.02))',
              }}
            >
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 10 }}>
                <div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
                    <Text strong>黑名单 IP / CIDR 网段规则</Text>
                    {list.length > 0 ? (
                      <Tag color="error">已配置 {list.length} 条规则</Tag>
                    ) : (
                      <Tag color="warning">未配置规则 (生效拦截需至少配置 1 条)</Tag>
                    )}
                  </div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                    {list.length > 0 ? (
                      <>
                        {list.slice(0, 3).map((ip: string) => (
                          <Text key={ip} code style={{ fontSize: 12 }}>
                            {ip}
                          </Text>
                        ))}
                        {list.length > 3 && (
                          <Text type="secondary" style={{ fontSize: 12 }}>
                            +{list.length - 3} 更多
                          </Text>
                        )}
                      </>
                    ) : (
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        暂未配置任何拦截规则，点击右侧按钮进行配置
                      </Text>
                    )}
                  </div>
                </div>
                <Space>
                  <Button
                    size="small"
                    icon={<EyeOutlined />}
                    onClick={() => openSecurityIpModal('blacklist', 'view')}
                  >
                    查看明细
                  </Button>
                  <Button
                    size="small"
                    type="primary"
                    icon={<SettingOutlined />}
                    onClick={() => openSecurityIpModal('blacklist', 'edit')}
                  >
                    配置规则
                  </Button>
                </Space>
              </div>
            </div>
          );
        }}
      </Form.Item>

      <Form.Item
        label="开启注册 IP 白名单"
        extra={<Text type="secondary">默认开启。10.0.0.0/24、192.168.1.0/24、172.16.0.0/24、127.0.0.0/24 不受当日及周期注册次数限制。黑白名单同级网段互斥，跨级子网包含时按最长前缀精确匹配（LPM）决定放行或拦截</Text>}
      >
        <Form.Item name="ip_whitelist_enabled" valuePropName="checked" noStyle>
          <Switch
            onChange={(checked) => {
              if (checked && getIpListFromForm('ip_whitelist_text').length === 0) {
                form.setFieldsValue({
                  ip_whitelist_text: '10.0.0.0/24\n192.168.1.0/24\n172.16.0.0/24\n127.0.0.0/24',
                });
              }
            }}
          />
        </Form.Item>
      </Form.Item>
      <Form.Item name="ip_whitelist_text" hidden>
        <Input />
      </Form.Item>
      <Form.Item
        noStyle
        shouldUpdate={(prevValues, currentValues) =>
          prevValues.ip_whitelist_enabled !== currentValues.ip_whitelist_enabled ||
          prevValues.ip_whitelist_text !== currentValues.ip_whitelist_text
        }
      >
        {({ getFieldValue }) => {
          const enabled = getFieldValue('ip_whitelist_enabled');
          const rawText = getFieldValue('ip_whitelist_text') || '';
          const list = rawText.split('\n').map((s: string) => s.trim()).filter(Boolean);
          if (!enabled) return null;
          return (
            <div
              style={{
                marginBottom: 18,
                padding: '12px 16px',
                border: '1px solid rgba(128,128,128,0.2)',
                borderRadius: 8,
                background: 'var(--ant-color-fill-alter, rgba(0,0,0,0.02))',
              }}
            >
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 10 }}>
                <div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
                    <Text strong>白名单 IP / CIDR 网段规则</Text>
                    {list.length > 0 ? (
                      <Tag color="success">已配置 {list.length} 条放行规则</Tag>
                    ) : (
                      <Tag color="warning">未配置规则 (建议至少保留内网段)</Tag>
                    )}
                  </div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                    {list.length > 0 ? (
                      <>
                        {list.slice(0, 4).map((ip: string) => (
                          <Text key={ip} code style={{ fontSize: 12 }}>
                            {ip}
                          </Text>
                        ))}
                        {list.length > 4 && (
                          <Text type="secondary" style={{ fontSize: 12 }}>
                            +{list.length - 4} 更多
                          </Text>
                        )}
                      </>
                    ) : (
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        暂未配置任何放行规则，点击右侧按钮进行配置
                      </Text>
                    )}
                  </div>
                </div>
                <Space>
                  <Button
                    size="small"
                    icon={<EyeOutlined />}
                    onClick={() => openSecurityIpModal('whitelist', 'view')}
                  >
                    查看明细
                  </Button>
                  <Button
                    size="small"
                    type="primary"
                    icon={<SettingOutlined />}
                    onClick={() => openSecurityIpModal('whitelist', 'edit')}
                  >
                    配置规则
                  </Button>
                </Space>
              </div>
            </div>
          );
        }}
      </Form.Item>

      <Form.Item
        label={t('settings.ip_rate_limit_enabled')}
        extra={<Text type="secondary">限制同一 IP 每天及指定天数周期内的受控注册总次数（含用户名和邮箱注册）。手机号、微信、谷歌新注册不拦截，也不计入该次数</Text>}
      >
        <Form.Item name={['registration', 'ip_rate_limit_enabled']} valuePropName="checked" noStyle initialValue={true}>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item noStyle dependencies={[['registration', 'ip_rate_limit_enabled']]}>
        {({ getFieldValue }) => getFieldValue(['registration', 'ip_rate_limit_enabled']) ? (
          <Form.Item label={t('settings.ip_daily_limit')}>
            <Space wrap size={12} align="center">
              <Form.Item name={['registration', 'ip_daily_limit']} noStyle initialValue={3}>
                <InputNumber min={1} max={10000} precision={0} addonAfter={t('settings.ip_daily_limit_unit')} style={{ width: 150 }} />
              </Form.Item>
              <Space size={6} align="center">
                <Form.Item name={['registration', 'ip_window_days']} noStyle initialValue={30}>
                  <InputNumber min={1} max={3650} precision={0} addonAfter="天内最多注册" style={{ width: 190 }} />
                </Form.Item>
                <Form.Item name={['registration', 'ip_window_limit']} noStyle initialValue={10}>
                  <InputNumber min={1} max={100000} precision={0} addonAfter="个" style={{ width: 130 }} />
                </Form.Item>
              </Space>
            </Space>
          </Form.Item>
        ) : null}
      </Form.Item>

      <Form.Item
        label={t('settings.email_validation_strict')}
        extra={<Text type="secondary">开启后邮箱 @ 前仅允许数字、字母和下划线，长度≤25</Text>}
      >
        <Form.Item name={['registration', 'email_validation_strict']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>

      <Form.Item
        label={t('settings.email_whitelist_enabled')}
        extra={<Text type="secondary">开启后仅允许白名单内的邮箱域名（如 qq.com、*.edu.cn）或指定邮箱地址注册</Text>}
      >
        <Form.Item name={['registration', 'email_whitelist_enabled']} valuePropName="checked" noStyle>
          <Switch
            onChange={(checked) => {
              if (checked && getEmailWhitelistFromForm().length === 0) {
                form.setFieldValue(
                  ['registration', 'email_whitelist'],
                  ['qq.com', '163.com', 'outlook.com', 'aliyun.com', 'foxmail.com'],
                );
              }
            }}
          />
        </Form.Item>
      </Form.Item>
      <Form.Item name={['registration', 'email_whitelist']} hidden>
        <Select mode="tags" />
      </Form.Item>
      <Form.Item
        noStyle
        shouldUpdate={(prevValues, currentValues) =>
          prevValues.registration?.email_whitelist_enabled !== currentValues.registration?.email_whitelist_enabled ||
          prevValues.registration?.email_whitelist !== currentValues.registration?.email_whitelist
        }
      >
        {({ getFieldValue }) => {
          const enabled = getFieldValue(['registration', 'email_whitelist_enabled']);
          const rawList = getFieldValue(['registration', 'email_whitelist']);
          const list: string[] = Array.isArray(rawList)
            ? rawList.map((s: unknown) => String(s ?? '').trim()).filter(Boolean)
            : [];
          if (!enabled) return null;
          return (
            <div
              style={{
                marginBottom: 18,
                padding: '12px 16px',
                border: '1px solid rgba(128,128,128,0.2)',
                borderRadius: 8,
                background: 'var(--ant-color-fill-alter, rgba(0,0,0,0.02))',
              }}
            >
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 10 }}>
                <div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
                    <Text strong>允许的邮箱域名 / 邮箱白名单</Text>
                    {list.length > 0 ? (
                      <Tag color="success">已配置 {list.length} 条允许规则</Tag>
                    ) : (
                      <Tag color="warning">未配置规则 (生效需至少配置 1 条)</Tag>
                    )}
                  </div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                    {list.length > 0 ? (
                      <>
                        {list.slice(0, 5).map((item: string) => (
                          <Text key={item} code style={{ fontSize: 12 }}>
                            {item}
                          </Text>
                        ))}
                        {list.length > 5 && (
                          <Text type="secondary" style={{ fontSize: 12 }}>
                            +{list.length - 5} 更多
                          </Text>
                        )}
                      </>
                    ) : (
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        暂未配置任何允许的邮箱域名或邮箱，点击右侧按钮进行批量配置
                      </Text>
                    )}
                  </div>
                </div>
                <Space>
                  <Button
                    size="small"
                    icon={<EyeOutlined />}
                    onClick={() => openSecurityEmailModal('view')}
                  >
                    查看明细
                  </Button>
                  <Button
                    size="small"
                    type="primary"
                    icon={<SettingOutlined />}
                    onClick={() => openSecurityEmailModal('edit')}
                  >
                    配置规则
                  </Button>
                </Space>
              </div>
            </div>
          );
        }}
      </Form.Item>
    </div>
  );

  const relaySettingsContent = (
    <div style={{ maxWidth: 720 }}>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 14 }}
        message="手动轮询仅影响客户端 GET；后台自动轮询与计费不变。低余额在途限制与「余额不足」区分；可按视频/图片/聊天/其它分别开关；保存后即时生效。"
      />
      <Form.Item
        label="手动轮询请求上游"
        extra={<Text type="secondary">开：未完成任务打上游。关：优先返回 logs 缓存；无缓存再兜底上游</Text>}
      >
        <Form.Item name={['relay', 'manual_poll_upstream']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item
        label="后台自动轮询周期"
        name={['relay', 'poll_tick_secs']}
        extra={<Text type="secondary">TaskPoller 间隔（秒），建议 15–60，范围 5–300，默认 30</Text>}
        rules={[{ required: true, message: '必填' }]}
      >
        <InputNumber min={5} max={300} step={5} addonAfter="秒" style={{ width: 180 }} />
      </Form.Item>
      <Form.Item
        label="调试日志"
        extra={<Text type="secondary">开：模型调用过程输出到控制台；关：不输出。保存后即时生效</Text>}
      >
        <Form.Item name={['relay', 'enable_debug_log']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>

      <Divider style={{ margin: '16px 0 12px' }}>SSE 流式空闲超时防护</Divider>
      <Text type="secondary" style={{ display: 'block', marginBottom: 12, fontSize: 12 }}>
        上游连续无数据达到该时长后自动释放挂死连接（已产出内容按实收结算，零产出自动退回预扣费）。填 0 表示关闭，开启时范围 60–3600 秒，默认 900 秒（15 分钟）。
      </Text>
      <Row gutter={16}>
        <Col xs={24} sm={12}>
          <Form.Item
            label="聊天流空闲超时"
            name={['relay', 'stream_idle', 'chat_secs']}
            extra={<Text type="secondary">Chat / Responses / 深度思考模型，默认 900（0=关闭）</Text>}
            rules={[{ required: true, message: '必填' }]}
          >
            <InputNumber min={0} max={3600} step={60} addonAfter="秒" style={{ width: '100%' }} />
          </Form.Item>
        </Col>
        <Col xs={24} sm={12}>
          <Form.Item
            label="图片流空闲超时"
            name={['relay', 'stream_idle', 'image_secs']}
            extra={<Text type="secondary">流式生图模型（如 Gemini 4K/多图），默认 900（0=关闭）</Text>}
            rules={[{ required: true, message: '必填' }]}
          >
            <InputNumber min={0} max={3600} step={60} addonAfter="秒" style={{ width: '100%' }} />
          </Form.Item>
        </Col>
      </Row>

      <Divider style={{ margin: '16px 0 12px' }}>低余额限制未完成任务</Divider>
      <Text type="secondary" style={{ display: 'block', marginBottom: 12, fontSize: 12 }}>
        按类别分别限制在途路数；可用额低于填金额时生效，路数 0=不限制。各类互不影响。
      </Text>
      {INFLIGHT_CATS.map(({ key, label, hint }) => (
        <div
          key={key}
          style={{
            marginBottom: 14,
            padding: '10px 12px',
            border: '1px solid rgba(128,128,128,0.18)',
            borderRadius: 8,
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8 }}>
            <Text strong style={{ minWidth: 36 }}>{label}</Text>
            {hint ? <Text type="secondary" style={{ fontSize: 12 }}>{hint}</Text> : null}
            <Form.Item name={['relay', 'inflight_limits', key, 'enabled']} valuePropName="checked" noStyle>
              <Switch checkedChildren="开" unCheckedChildren="关" />
            </Form.Item>
          </div>
          <Form.List name={['relay', 'inflight_limits', key, 'tiers']}>
            {(fields, { add, remove }) => (
              <>
                <div style={{ display: 'flex', gap: 8, marginBottom: 4, paddingRight: 32 }}>
                  <Text type="secondary" style={{ fontSize: 12, width: 160 }}>可用额低于（{currencyUnit}）</Text>
                  <Text type="secondary" style={{ fontSize: 12, width: 180 }}>最大未完成路数</Text>
                </div>
                {fields.map((field) => (
                  <div key={field.key} style={{ display: 'flex', gap: 8, alignItems: 'flex-start', marginBottom: 6 }}>
                    <Form.Item {...field} name={[field.name, 'max_available']} style={{ marginBottom: 0, width: 160 }}>
                      <InputNumber min={0} step={1} style={{ width: '100%' }} placeholder="其余则留空" />
                    </Form.Item>
                    <Form.Item
                      {...field}
                      name={[field.name, 'max_inflight']}
                      style={{ marginBottom: 0, width: 180 }}
                      rules={[{ required: true, message: '必填' }]}
                    >
                      <InputNumber min={0} step={1} style={{ width: '100%' }} placeholder="0=不限制" />
                    </Form.Item>
                    <Button
                      type="text"
                      danger
                      icon={<DeleteOutlined />}
                      disabled={fields.length <= 1}
                      onClick={() => remove(field.name)}
                      style={{ marginTop: 2 }}
                    />
                  </div>
                ))}
                <Button
                  type="dashed"
                  size="small"
                  icon={<PlusOutlined />}
                  onClick={() => add({ max_available: 20, max_inflight: 1 })}
                  style={{ marginTop: 2 }}
                >
                  新增档
                </Button>
              </>
            )}
          </Form.List>
        </div>
      ))}
    </div>
  );

  const loginSettingsContent = (
    <div style={{ maxWidth: 680 }}>
      <Form.Item label="登录页标题" name="login_title" extra={<Text type="secondary">留空则使用站点名称</Text>}>
        <Input placeholder="例如：laiai" />
      </Form.Item>
      <Form.Item
        label="登录页标题链接"
        name="login_title_url"
        extra={<Text type="secondary">配置后标题和 Logo 可点击跳转；留空则使用「控制台 Logo 标题链接」</Text>}
      >
        <Input placeholder="留空则使用控制台 Logo 标题链接" />
      </Form.Item>
      <Form.Item label="登录页副标题" name="login_subtitle" extra={<Text type="secondary">留空则使用默认文字</Text>}>
        <Input placeholder="例如：Next-gen LLM API Gateway" />
      </Form.Item>
      <Form.Item label="登录页风格" name="login_style" extra={<Text type="secondary">经典居中将表单直接居中；左右风格为双栏布局</Text>}>
        <LoginStyleSelector />
      </Form.Item>

      <Form.Item noStyle shouldUpdate={(prevValues, currentValues) => prevValues.login_style !== currentValues.login_style}>
        {({ getFieldValue }) => {
          const style = getFieldValue('login_style') || 'split';
          if (style !== 'split') return null;
          return (
            <Form.Item 
              label="左下角广告语" 
              name="login_quote" 
              extra={<Text type="secondary">左右风格左侧宣传语，留空使用系统默认</Text>}
            >
              <Input.TextArea rows={2} placeholder="配置登录页左侧大背景底部所展示的宣传语" />
            </Form.Item>
          );
        }}
      </Form.Item>

      <Divider style={{ margin: '16px 0 12px' }}>第三方与登录方式</Divider>

      <Form.Item label={t('settings.enable_username_login')}>
        <Form.Item name={['login', 'enable_username_login']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item label={t('settings.enable_mobile_login')}
        extra={<Text type="secondary">{t('settings.login_hint_sms')}，<GoLink to={`/${adminPath}/message-notification`} text={t('settings.goto_settings')} /></Text>}>
        <Form.Item name={['login', 'enable_mobile_login']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item label={t('settings.enable_email_login')}
        extra={<Text type="secondary">{t('settings.login_hint_email')}，<GoLink to={`/${adminPath}/message-notification`} text={t('settings.goto_settings')} /></Text>}>
        <Form.Item name={['login', 'enable_email_login']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item label={t('settings.enable_wechat_login')}
        extra={<Text type="secondary">{t('settings.login_hint_oauth')}，<GoLink to={`/${adminPath}/oauth-settings`} text={t('settings.goto_settings')} /></Text>}>
        <Form.Item name={['login', 'enable_wechat_login']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item label={t('settings.enable_google_login')}
        extra={<Text type="secondary">{t('settings.login_hint_oauth')}，<GoLink to={`/${adminPath}/oauth-settings`} text={t('settings.goto_settings')} /></Text>}>
        <Form.Item name={['login', 'enable_google_login']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
    </div>
  );

  const registrationSettingsContent = (
    <div style={{ maxWidth: 680 }}>
      <Form.Item label={t('settings.enable_username_reg')}>
        <Form.Item name={['registration', 'enable_username_registration']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item label={t('settings.enable_email_reg')}
        extra={<Text type="secondary">{t('settings.login_hint_email')}，<GoLink to={`/${adminPath}/message-notification`} text={t('settings.goto_settings')} /></Text>}>
        <Form.Item name={['registration', 'enable_email_registration']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item label={t('settings.enable_mobile_registration')}
        extra={<Text type="secondary">{t('settings.login_hint_sms')}，<GoLink to={`/${adminPath}/message-notification`} text={t('settings.goto_settings')} /></Text>}>
        <Form.Item name={['registration', 'enable_mobile_registration']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item label={t('settings.enable_password_recovery')}>
        <Form.Item name={['registration', 'enable_password_recovery']} valuePropName="checked" noStyle>
          <Switch />
        </Form.Item>
      </Form.Item>
      <Form.Item
        label={t('settings.max_accounts_per_email')}
        name={['registration', 'max_accounts_per_email']}
        extra={<Text type="secondary">{t('settings.max_accounts_per_contact_hint')}</Text>}
      >
        <InputNumber min={1} max={99} precision={0} style={{ width: 160 }} />
      </Form.Item>
      <Form.Item
        label={t('settings.max_accounts_per_mobile')}
        name={['registration', 'max_accounts_per_mobile']}
        extra={<Text type="secondary">{t('settings.max_accounts_per_contact_hint')}</Text>}
      >
        <InputNumber min={1} max={99} precision={0} style={{ width: 160 }} />
      </Form.Item>
      <Form.Item
        label={t('settings.max_accounts_per_wechat')}
        name={['registration', 'max_accounts_per_wechat']}
        extra={<Text type="secondary">{t('settings.max_accounts_per_contact_hint')}</Text>}
      >
        <InputNumber min={1} max={99} precision={0} style={{ width: 160 }} />
      </Form.Item>
    </div>
  );

  const handleFillDefaultAgreements = (target: 'all' | 'zh' | 'en' | 'tos_zh' | 'tos_en' | 'privacy_zh' | 'privacy_en') => {
    const current = form.getFieldValue('agreement') || {};
    let updated = { ...current };
    if (target === 'all') {
      updated = {
        ...updated,
        tos_mode: 'text',
        tos_content: DEFAULT_TOS_ZH,
        privacy_mode: 'text',
        privacy_content: DEFAULT_PRIVACY_ZH,
        tos_mode_en: 'text',
        tos_content_en: DEFAULT_TOS_EN,
        privacy_mode_en: 'text',
        privacy_content_en: DEFAULT_PRIVACY_EN,
        tos_enabled: true,
        privacy_enabled: true,
      };
    } else if (target === 'zh') {
      updated.tos_mode = 'text';
      updated.tos_content = DEFAULT_TOS_ZH;
      updated.privacy_mode = 'text';
      updated.privacy_content = DEFAULT_PRIVACY_ZH;
    } else if (target === 'en') {
      updated.tos_mode_en = 'text';
      updated.tos_content_en = DEFAULT_TOS_EN;
      updated.privacy_mode_en = 'text';
      updated.privacy_content_en = DEFAULT_PRIVACY_EN;
    } else if (target === 'tos_zh') {
      updated.tos_mode = 'text';
      updated.tos_content = DEFAULT_TOS_ZH;
    } else if (target === 'tos_en') {
      updated.tos_mode_en = 'text';
      updated.tos_content_en = DEFAULT_TOS_EN;
    } else if (target === 'privacy_zh') {
      updated.privacy_mode = 'text';
      updated.privacy_content = DEFAULT_PRIVACY_ZH;
    } else if (target === 'privacy_en') {
      updated.privacy_mode_en = 'text';
      updated.privacy_content_en = DEFAULT_PRIVACY_EN;
    }
    form.setFieldsValue({ agreement: updated });
    message.success('已填入标准预设协议内容');
  };

  const handlePreviewAgreement = async (type: 'tos' | 'privacy', lang: 'zh' | 'en') => {
    if (loading || previewingKey) return;

    const agreementValues = form.getFieldValue('agreement') || {};
    const modeKey = type === 'tos'
      ? (lang === 'en' ? 'tos_mode_en' : 'tos_mode')
      : (lang === 'en' ? 'privacy_mode_en' : 'privacy_mode');
    const linkKey = type === 'tos'
      ? (lang === 'en' ? 'tos_link_en' : 'tos_link')
      : (lang === 'en' ? 'privacy_link_en' : 'privacy_link');
    const fallbackLinkKey = type === 'tos' ? 'tos_link' : 'privacy_link';

    const mode = agreementValues[modeKey] || 'text';

    let targetUrl = '';

    if (mode === 'link') {
      let link = String(agreementValues[linkKey] || '').trim();
      if (!link && lang === 'en') {
        link = String(agreementValues[fallbackLinkKey] || '').trim();
      }
      if (!link) {
        message.warning(lang === 'en' ? 'Please enter a link URL first' : '请先填写链接地址');
        return;
      }
      targetUrl = link;
      if (!/^https?:\/\//i.test(targetUrl) && !targetUrl.startsWith('/')) {
        targetUrl = `https://${targetUrl}`;
      }
    } else {
      if (lang === 'en') {
        targetUrl = type === 'tos' ? '/legal/terms_en.html' : '/legal/privacy_en.html';
      } else {
        targetUrl = type === 'tos' ? '/legal/terms.html' : '/legal/privacy.html';
      }
    }

    const key = `${type}_${lang}`;
    setPreviewingKey(key);

    try {
      const ok = await handleSave();
      if (ok) {
        // 如果是本地静态页面预览，添加随机时间戳参数，强制击穿浏览器及所有网关代理缓存
        const previewUrl = targetUrl.startsWith('/legal')
          ? `${targetUrl}?_t=${Date.now()}`
          : targetUrl;
        window.open(previewUrl, '_blank');
      }
    } finally {
      setPreviewingKey(null);
    }
  };

  const agreementSettingsContent = (
    <div style={{ maxWidth: 780 }}>
      <Alert
        message="预设协议说明"
        description="系统内置契合 AI 模型中继分发、API 令牌调度、创作中心与计费审计的标准中英文《服务条款》和《隐私协议》。无需修改即可直接使用；您也可以点击下方按钮一键填入或重置为预设模板。"
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        action={
          <Button size="small" type="primary" ghost onClick={() => handleFillDefaultAgreements('all')}>
            一键填入全套预设模板
          </Button>
        }
      />

      <div style={{ display: 'flex', gap: 32, marginBottom: 16 }}>
        <Form.Item label="启用服务条款" style={{ marginBottom: 0 }}>
          <Form.Item name={['agreement', 'tos_enabled']} valuePropName="checked" noStyle>
            <Switch />
          </Form.Item>
        </Form.Item>
        <Form.Item label="启用隐私协议" style={{ marginBottom: 0 }}>
          <Form.Item name={['agreement', 'privacy_enabled']} valuePropName="checked" noStyle>
            <Switch />
          </Form.Item>
        </Form.Item>
      </div>

      <Tabs 
        defaultActiveKey="zh"
        tabBarExtraContent={
          <Space>
            <Button size="small" onClick={() => handleFillDefaultAgreements('zh')}>填入中文预设模板</Button>
            <Button size="small" onClick={() => handleFillDefaultAgreements('en')}>Fill English Presets</Button>
          </Space>
        }
      >
        <Tabs.TabPane tab="简体中文 (默认)" key="zh">
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
            <Text strong style={{ fontSize: 13 }}>服务条款 (Terms of Service)</Text>
            <Button size="small" type="link" style={{ padding: 0 }} onClick={() => handleFillDefaultAgreements('tos_zh')}>
              填入预设服务条款
            </Button>
          </div>
          <Form.Item label="显示方式">
            <Space size="middle" wrap>
              <Form.Item name={['agreement', 'tos_mode']} noStyle>
                <Radio.Group buttonStyle="solid">
                  <Radio.Button value="text">站内富文本 (默认)</Radio.Button>
                  <Radio.Button value="link">网页链接</Radio.Button>
                </Radio.Group>
              </Form.Item>
              <Button
                icon={<EyeOutlined />}
                loading={previewingKey === 'tos_zh'}
                disabled={loading && previewingKey !== 'tos_zh'}
                onClick={() => handlePreviewAgreement('tos', 'zh')}
              >
                预览
              </Button>
            </Space>
          </Form.Item>
          <Form.Item noStyle dependencies={[['agreement', 'tos_mode']]}>
            {({ getFieldValue }) => getFieldValue(['agreement', 'tos_mode']) === 'link' ? (
              <Form.Item label="链接地址" name={['agreement', 'tos_link']}>
                <Input placeholder="https://example.com/terms" />
              </Form.Item>
            ) : (
              <Form.Item label="条款内容" name={['agreement', 'tos_content']}>
                <ReactQuill theme="snow" style={{ height: 260, marginBottom: 48, backgroundColor: 'var(--ant-color-bg-container)', color: 'var(--ant-color-text)' }} />
              </Form.Item>
            )}
          </Form.Item>

          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 24, marginBottom: 8 }}>
            <Text strong style={{ fontSize: 13 }}>隐私协议 (Privacy Policy)</Text>
            <Button size="small" type="link" style={{ padding: 0 }} onClick={() => handleFillDefaultAgreements('privacy_zh')}>
              填入预设隐私协议
            </Button>
          </div>
          <Form.Item label="显示方式">
            <Space size="middle" wrap>
              <Form.Item name={['agreement', 'privacy_mode']} noStyle>
                <Radio.Group buttonStyle="solid">
                  <Radio.Button value="text">站内富文本 (默认)</Radio.Button>
                  <Radio.Button value="link">网页链接</Radio.Button>
                </Radio.Group>
              </Form.Item>
              <Button
                icon={<EyeOutlined />}
                loading={previewingKey === 'privacy_zh'}
                disabled={loading && previewingKey !== 'privacy_zh'}
                onClick={() => handlePreviewAgreement('privacy', 'zh')}
              >
                预览
              </Button>
            </Space>
          </Form.Item>
          <Form.Item noStyle dependencies={[['agreement', 'privacy_mode']]}>
            {({ getFieldValue }) => getFieldValue(['agreement', 'privacy_mode']) === 'link' ? (
              <Form.Item label="链接地址" name={['agreement', 'privacy_link']}>
                <Input placeholder="https://example.com/privacy" />
              </Form.Item>
            ) : (
              <Form.Item label="协议内容" name={['agreement', 'privacy_content']}>
                <ReactQuill theme="snow" style={{ height: 260, marginBottom: 48, backgroundColor: 'var(--ant-color-bg-container)', color: 'var(--ant-color-text)' }} />
              </Form.Item>
            )}
          </Form.Item>
        </Tabs.TabPane>

        <Tabs.TabPane tab="English" key="en">
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
            <Text strong style={{ fontSize: 13 }}>Terms of Service</Text>
            <Button size="small" type="link" style={{ padding: 0 }} onClick={() => handleFillDefaultAgreements('tos_en')}>
              Fill Preset Terms
            </Button>
          </div>
          <Form.Item label="Display Mode">
            <Space size="middle" wrap>
              <Form.Item name={['agreement', 'tos_mode_en']} noStyle>
                <Radio.Group buttonStyle="solid">
                  <Radio.Button value="text">Rich Text (Default)</Radio.Button>
                  <Radio.Button value="link">Link URL</Radio.Button>
                </Radio.Group>
              </Form.Item>
              <Button
                icon={<EyeOutlined />}
                loading={previewingKey === 'tos_en'}
                disabled={loading && previewingKey !== 'tos_en'}
                onClick={() => handlePreviewAgreement('tos', 'en')}
              >
                Preview
              </Button>
            </Space>
          </Form.Item>
          <Form.Item noStyle dependencies={[['agreement', 'tos_mode_en']]}>
            {({ getFieldValue }) => getFieldValue(['agreement', 'tos_mode_en']) === 'link' ? (
              <Form.Item label="Link URL (English)" name={['agreement', 'tos_link_en']}>
                <Input placeholder="https://example.com/en/terms" />
              </Form.Item>
            ) : (
              <Form.Item label="Content (English)" name={['agreement', 'tos_content_en']}>
                <ReactQuill theme="snow" style={{ height: 260, marginBottom: 48, backgroundColor: 'var(--ant-color-bg-container)', color: 'var(--ant-color-text)' }} />
              </Form.Item>
            )}
          </Form.Item>

          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 24, marginBottom: 8 }}>
            <Text strong style={{ fontSize: 13 }}>Privacy Policy</Text>
            <Button size="small" type="link" style={{ padding: 0 }} onClick={() => handleFillDefaultAgreements('privacy_en')}>
              Fill Preset Privacy Policy
            </Button>
          </div>
          <Form.Item label="Display Mode">
            <Space size="middle" wrap>
              <Form.Item name={['agreement', 'privacy_mode_en']} noStyle>
                <Radio.Group buttonStyle="solid">
                  <Radio.Button value="text">Rich Text (Default)</Radio.Button>
                  <Radio.Button value="link">Link URL</Radio.Button>
                </Radio.Group>
              </Form.Item>
              <Button
                icon={<EyeOutlined />}
                loading={previewingKey === 'privacy_en'}
                disabled={loading && previewingKey !== 'privacy_en'}
                onClick={() => handlePreviewAgreement('privacy', 'en')}
              >
                Preview
              </Button>
            </Space>
          </Form.Item>
          <Form.Item noStyle dependencies={[['agreement', 'privacy_mode_en']]}>
            {({ getFieldValue }) => getFieldValue(['agreement', 'privacy_mode_en']) === 'link' ? (
              <Form.Item label="Link URL (English)" name={['agreement', 'privacy_link_en']}>
                <Input placeholder="https://example.com/en/privacy" />
              </Form.Item>
            ) : (
              <Form.Item label="Content (English)" name={['agreement', 'privacy_content_en']}>
                <ReactQuill theme="snow" style={{ height: 260, marginBottom: 48, backgroundColor: 'var(--ant-color-bg-container)', color: 'var(--ant-color-text)' }} />
              </Form.Item>
            )}
          </Form.Item>
        </Tabs.TabPane>
      </Tabs>
    </div>
  );

  const moveItem = (index: number, direction: 'up' | 'down') => {
    const newItems = [...menuItems];
    const targetIndex = direction === 'up' ? index - 1 : index + 1;
    if (targetIndex < 0 || targetIndex >= newItems.length) return;
    
    // Swap
    const temp = newItems[index];
    newItems[index] = newItems[targetIndex];
    newItems[targetIndex] = temp;
    
    setMenuItems(newItems);
  };

  const updateItem = (index: number, field: string, value: any) => {
    const newItems = [...menuItems];
    newItems[index] = {
      ...newItems[index],
      [field]: value
    };
    setMenuItems(newItems);
  };

  const menuSettingsContent = loadingMenu ? (
    <div style={{ textAlign: 'center', padding: '60px 0' }}>
      <Spin size="large" tip="正在加载菜单配置..." />
    </div>
  ) : (
    <div style={{ width: '100%', overflowX: 'auto' }}>
      <div style={{ marginBottom: 16 }}>
        <Alert
          message="菜单配置说明"
          description="在此配置用户使用端（左侧菜单栏）中各个菜单的显示顺序、启用状态以及针对不同会员等级/用户组的访问可见权限。"
          type="info"
          showIcon
        />
      </div>
      <Table
        dataSource={menuItems}
        rowKey="key"
        pagination={false}
        size="middle"
        columns={[
          {
            title: '顺序',
            key: 'sort',
            width: 100,
            align: 'center',
            render: (_, __, index) => (
              <Space size="small">
                <Button
                  size="small"
                  icon={<Icons.ArrowUpOutlined />}
                  disabled={index === 0}
                  onClick={() => moveItem(index, 'up')}
                />
                <Button
                  size="small"
                  icon={<Icons.ArrowDownOutlined />}
                  disabled={index === menuItems.length - 1}
                  onClick={() => moveItem(index, 'down')}
                />
              </Space>
            ),
          },
          {
            title: '菜单图标 & 路径',
            key: 'icon_path',
            width: 200,
            render: (_, record) => {
              const IconComp = (Icons as any)[record.icon];
              return (
                <Space direction="vertical" size={2}>
                  <Space>
                    {IconComp ? <IconComp style={{ fontSize: '18px', color: '#1677ff' }} /> : <Icons.MenuOutlined style={{ fontSize: '18px' }} />}
                    <Text strong>{record.key}</Text>
                  </Space>
                  <Text type="secondary" style={{ fontSize: '12px' }}>
                    图标类名: {record.icon}
                  </Text>
                </Space>
              );
            },
          },
          {
            title: '中文名称 (Zh)',
            dataIndex: 'label_zh',
            key: 'label_zh',
            width: 180,
            render: (text, _, index) => (
              <Input
                value={text}
                onChange={(e) => updateItem(index, 'label_zh', e.target.value)}
                placeholder="中文名称"
              />
            ),
          },
          {
            title: '英文名称 (En)',
            dataIndex: 'label_en',
            key: 'label_en',
            width: 180,
            render: (text, _, index) => (
              <Input
                value={text}
                onChange={(e) => updateItem(index, 'label_en', e.target.value)}
                placeholder="英文名称"
              />
            ),
          },
          {
            title: '启用状态',
            dataIndex: 'enabled',
            key: 'enabled',
            width: 100,
            align: 'center',
            render: (checked, _, index) => (
              <Switch
                checked={checked}
                onChange={(val) => updateItem(index, 'enabled', val)}
              />
            ),
          },
          {
            title: '可见等级权限',
            dataIndex: 'allowed_levels',
            key: 'allowed_levels',
            render: (value, _, index) => {
              const selectedKeys = value === 'all' ? ['all'] : (value ? value.split(',') : []);
              return (
                <Select
                  mode="multiple"
                  style={{ width: '100%', minWidth: 200 }}
                  placeholder="选择可见等级，为空则不可见"
                  value={selectedKeys}
                  onChange={(vals: string[]) => {
                    if (vals.includes('all')) {
                      if (vals[vals.length - 1] === 'all') {
                        updateItem(index, 'allowed_levels', 'all');
                      } else {
                        const filtered = vals.filter((v: string) => v !== 'all');
                        updateItem(index, 'allowed_levels', filtered.join(','));
                      }
                    } else {
                      updateItem(index, 'allowed_levels', vals.join(','));
                    }
                  }}
                  options={[
                    { label: '全部会员等级', value: 'all' },
                    ...userLevels.map((lv) => ({
                      label: `${lv.name} (ULID: ${lv.id})`,
                      value: lv.id.toString(),
                    })),
                  ]}
                />
              );
            },
          },
        ]}
      />
    </div>
  );

  const ScheduledTimePicker: React.FC<{
    hourField: string;
    minuteField: string;
  }> = ({ hourField, minuteField }) => {
    const hour = Form.useWatch(['log_cleanup', hourField], form) ?? 0;
    const minute = Form.useWatch(['log_cleanup', minuteField], form) ?? 0;
    const timeVal = dayjs().hour(hour).minute(minute).second(0);

    return (
      <>
        <TimePicker
          format="HH:mm"
          allowClear={false}
          style={{ width: 140 }}
          value={timeVal}
          onChange={(t) => {
            if (t) {
              form.setFieldValue(['log_cleanup', hourField], t.hour());
              form.setFieldValue(['log_cleanup', minuteField], t.minute());
            }
          }}
        />
        <Form.Item name={['log_cleanup', hourField]} hidden noStyle />
        <Form.Item name={['log_cleanup', minuteField]} hidden noStyle />
      </>
    );
  };

  const dataCleanupContent = (
    <div style={{ maxWidth: 760 }}>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 14 }}
        message="日志清理只清空请求/响应大字段；行归档将超期行迁入 logs_archive；错误日志清理彻底删除超期已结案的错误记录（排除进行中 0 与正常 200 记录）。定时任务执行时间与巡检周期可在下方按业务高峰期自定义与错峰调度。"
      />

      <div style={{ marginBottom: 12 }}>
        <Text strong style={{ fontSize: 13, display: 'block', marginBottom: 4 }}>数据与日志保留策略</Text>
        <Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 8 }}>
          控制日志大字段、归档行以及错误记录的保存期限，0 代表永不清理。
        </Text>
      </div>

      <Row gutter={16}>
        <Col xs={24} sm={8}>
          <Form.Item
            label="日志详情保留天数"
            name={['log_cleanup', 'log_retention_days']}
            extra={<Text type="secondary">0 永不清理，默认 30</Text>}
          >
            <InputNumber min={0} max={3650} style={{ width: '100%' }} addonAfter="天" placeholder="30" />
          </Form.Item>
        </Col>
        <Col xs={24} sm={8}>
          <Form.Item
            label="日志行归档天数"
            name={['log_cleanup', 'log_row_retention_days']}
            extra={<Text type="secondary">0 不归档，建议 90</Text>}
          >
            <InputNumber min={0} max={3650} style={{ width: '100%' }} addonAfter="天" placeholder="0" />
          </Form.Item>
        </Col>
        <Col xs={24} sm={8}>
          <Form.Item
            label="错误日志保留天数"
            name={['log_cleanup', 'error_log_retention_days']}
            extra={<Text type="secondary">0 永不清理，非 200/0 且已结案</Text>}
          >
            <InputNumber min={0} max={3650} style={{ width: '100%' }} addonAfter="天" placeholder="0" />
          </Form.Item>
        </Col>
      </Row>

      <Divider style={{ margin: '14px 0 16px' }} />

      <div style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', flexWrap: 'wrap', gap: 8, marginBottom: 12 }}>
          <div>
            <Text strong style={{ fontSize: 13, display: 'block', marginBottom: 2 }}>定时任务错峰调度与运维</Text>
            <Text type="secondary" style={{ fontSize: 12 }}>
              支持按业务高峰期自定义任务触发时间与周期，保存后即刻热生效，无需重启服务。
            </Text>
          </div>
          <Popconfirm
            title="立即执行系统维护？"
            description="将立即在后台异步执行一次全量定时任务维护（用量统计、日志清理、错误清理、冷行归档及临时存储清理）。"
            onConfirm={handleTriggerMaintenance}
            okText="确定执行"
            cancelText="取消"
          >
            <Button size="small" loading={maintaining}>
              立即执行一次系统维护
            </Button>
          </Popconfirm>
        </div>

        <Row gutter={[16, 16]}>
          <Col xs={24} sm={12}>
            <Form.Item
              label="每日用量增量统计时间"
              extra={<Text type="secondary">基于系统时区执行，默认 01:00（自动增量同步汇总前3天数据）</Text>}
            >
              <ScheduledTimePicker hourField="daily_stats_hour" minuteField="daily_stats_minute" />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12}>
            <Form.Item
              label="日志详情大字段清理时间"
              extra={<Text type="secondary">基于系统时区执行，默认 02:30（清空超期日志请求/响应大字段）</Text>}
            >
              <ScheduledTimePicker hourField="clean_hour" minuteField="clean_minute" />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12}>
            <Form.Item
              label="历史日志行冷归档时间"
              extra={<Text type="secondary">基于系统时区执行，建议晚于用量统计（默认 03:30，热表超期旧日志安全迁入冷表）</Text>}
            >
              <ScheduledTimePicker hourField="archive_hour" minuteField="archive_minute" />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12}>
            <Form.Item
              label="超期错误日志清理时间"
              extra={<Text type="secondary">基于系统时区执行，默认 04:00（彻底删除已结案超期非200错误）</Text>}
            >
              <ScheduledTimePicker hourField="error_clean_hour" minuteField="error_clean_minute" />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12}>
            <Form.Item
              label="临时存储与插件日志清理时间"
              extra={<Text type="secondary">基于系统时区执行，默认 04:30（清理过期对象存储临时文件与插件日志）</Text>}
            >
              <ScheduledTimePicker hourField="storage_clean_hour" minuteField="storage_clean_minute" />
            </Form.Item>
          </Col>
        </Row>
      </div>

      <Divider style={{ margin: '14px 0 16px' }} />

      <div style={{ marginBottom: 16 }}>
        <Text strong style={{ fontSize: 13, display: 'block', marginBottom: 4 }}>历史使用数据每日统计校准与补录</Text>
        <Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 8 }}>
          每天站点运营时区 00:00 自动增量同步历史日志到汇总表。若发现特定日期统计有偏差或需补录，可手动触发后台异步校准。
        </Text>
        <Space wrap>
          <DatePicker.RangePicker
            style={{ width: 260 }}
            value={syncDates}
            onChange={(val) => setSyncDates(val ? [val[0], val[1]] : [null, null])}
            disabledDate={(current) => current && current > dayjs().endOf('day')}
            placeholder={['开始日期', '结束日期']}
          />
          <Button 
            type="primary" 
            onClick={handleManualSync} 
            loading={syncingStats}
            disabled={!syncDates[0] || !syncDates[1]}
          >
            开始同步与校准
          </Button>
        </Space>
      </div>
    </div>
  );

  const storageSettingsContent = (
    <StorageConfigPanel form={form} namePrefix={['storage']} />
  );

  const dbSettingsContent = (
    <div style={{ maxWidth: 760 }}>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 14 }}
        message="当前数据库连接与运行状态（只读）"
        description="此处展示系统当前生效的 PostgreSQL 连接与运行状态。采集只读目录与共享内存统计，打开本页或点刷新时查一次，不轮询、不扫业务大表。更改连接请修改 DATABASE_URL 或数据目录 .database_url 后重启。「初始化并清空数据库」会清空全部业务数据，随后与全新安装一样设置超级管理员。"
      />

      {dbInfoError && (
        <Alert type="warning" showIcon message={dbInfoError} style={{ marginBottom: 12 }} />
      )}

      <Spin spinning={dbInfoLoading && !dbInfo}>
        <Descriptions
          bordered
          size="small"
          column={{ xs: 1, sm: 2, md: 2 }}
          style={{ marginBottom: 16 }}
          labelStyle={{ width: '150px', fontWeight: 500 }}
        >
          <Descriptions.Item label="数据库类型">
            <Tag color="blue">PostgreSQL</Tag>
          </Descriptions.Item>
          <Descriptions.Item label="连接地址 (Host:Port)">
            <Text code>{form.getFieldValue(['database', 'host']) || 'postgres'}:{form.getFieldValue(['database', 'port']) || 5432}</Text>
          </Descriptions.Item>
          <Descriptions.Item label="数据库名称">
            <Text code>{form.getFieldValue(['database', 'database']) || 'tokensapi'}</Text>
          </Descriptions.Item>
          <Descriptions.Item label="用户名">
            <Text code>{form.getFieldValue(['database', 'username']) || 'tokensapi'}</Text>
          </Descriptions.Item>
          <Descriptions.Item label="SSL 连接">
            {form.getFieldValue(['database', 'ssl_mode']) ? <Tag color="success">已开启</Tag> : <Tag>未开启</Tag>}
          </Descriptions.Item>
          <Descriptions.Item label="数据库版本">
            {dbInfo?.server_version || '—'}
          </Descriptions.Item>
          <Descriptions.Item label="运行状态 / 运行时长">
            {dbInfo?.uptime ? <Tag color="processing">{dbInfo.uptime}</Tag> : '—'}
          </Descriptions.Item>
          <Descriptions.Item label="启动时间 (UTC)">
            {dbInfo?.started_at_utc || '—'}
          </Descriptions.Item>
          <Descriptions.Item label="数据存储大小">
            <Text strong>{dbInfo?.size_pretty || '—'}</Text>
          </Descriptions.Item>
          <Descriptions.Item label="数据表数量">
            {dbInfo?.table_count !== undefined ? `${dbInfo.table_count} 张表` : '—'}
          </Descriptions.Item>
          <Descriptions.Item label="字符编码">
            {dbInfo?.encoding || 'UTF8'}
          </Descriptions.Item>
          <Descriptions.Item label="当前连接 / 上限">
            {dbInfo?.backends !== undefined ? `${dbInfo.backends} / ${dbInfo.max_connections}` : '—'}
          </Descriptions.Item>
          <Descriptions.Item label="缓存命中率">
            {typeof dbInfo?.cache_hit_pct === 'number' ? `${dbInfo.cache_hit_pct}%` : '—'}
          </Descriptions.Item>
          <Descriptions.Item label="事务提交 / 回滚">
            {dbInfo?.xact_commit !== undefined
              ? `${Number(dbInfo.xact_commit).toLocaleString()} / ${Number(dbInfo.xact_rollback).toLocaleString()}`
              : '—'}
          </Descriptions.Item>
          <Descriptions.Item label="死锁次数">
            {dbInfo?.deadlocks !== undefined ? Number(dbInfo.deadlocks).toLocaleString() : '—'}
          </Descriptions.Item>
          <Descriptions.Item label="临时文件占用">
            {dbInfo?.temp_pretty || '—'}
          </Descriptions.Item>
          <Descriptions.Item label="计数起始 (UTC)">
            {dbInfo?.stats_reset_utc || '—'}
          </Descriptions.Item>
          <Descriptions.Item label="站点进程已运行">
            {dbInfo?.process_uptime ? <Tag color="processing">{dbInfo.process_uptime}</Tag> : '—'}
          </Descriptions.Item>
          <Descriptions.Item label="站点进程启动 (UTC)">
            {dbInfo?.process_started_at_utc || '—'}
          </Descriptions.Item>
          <Descriptions.Item label="应用连接池" span={2}>
            {dbInfo?.pool_size !== undefined
              ? `使用中 ${Number(dbInfo.pool_size) - Number(dbInfo.pool_idle || 0)}，空闲 ${dbInfo.pool_idle}，合计 ${dbInfo.pool_size}`
              : '—'}
          </Descriptions.Item>
        </Descriptions>
      </Spin>

      <Space wrap style={{ marginBottom: 8 }}>
        <Button onClick={handleVerifyDatabase} loading={dbVerifying}>测试数据库连接</Button>
        <Button onClick={() => void fetchDbInfo()} loading={dbInfoLoading}>刷新状态</Button>
        <Button
          danger
          disabled={resetting}
          onClick={() => {
            resetStartedRef.current = false;
            setResetPhrase('');
            setResetCountdown(null);
            setResetOpen(true);
          }}
        >
          初始化并清空数据库
        </Button>
      </Space>
    </div>
  );

  return (
    <Card bordered={false} title={getTitle()} style={{ borderRadius: 12 }}>
      <style>{`
        .settings-compact-form .ant-form-item {
          margin-bottom: 12px;
        }
        .settings-compact-form .ant-form-item-label {
          padding-bottom: 2px;
        }
        .settings-compact-form .ant-form-item-label > label {
          font-size: 13px;
          font-weight: 500;
        }
        .settings-compact-form .ant-form-item-extra {
          font-size: 12px;
          margin-top: 2px;
          line-height: 1.4;
        }
        .settings-compact-form .ant-card-body {
          padding: 16px 20px;
        }
        .settings-compact-form .ant-tabs-nav {
          margin-bottom: 14px;
        }
      `}</style>
      <Form className="settings-compact-form" form={form} layout="vertical" autoComplete="off"
        initialValues={{ database: { db_type: 'postgres', host: 'postgres', port: 5432, database: 'tokensapi', username: 'tokensapi', password: 'tokensapi', ssl_mode: false } }}>

        {tab === 'basic' && (
          <Tabs activeKey={basicSubTab} onChange={(k) => {
            setBasicSubTab(k);
            navigate(`/${adminPath}/settings?tab=basic&subtab=${k}`, { replace: true });
          }} items={[
            { key: 'site', label: '站点信息', children: siteSettingsContent },
            { key: 'login', label: '登录设置', children: loginSettingsContent },
            { key: 'registration', label: '注册设置', children: registrationSettingsContent },
            { key: 'agreement', label: '站点协议', children: agreementSettingsContent },
            { key: 'menu', label: '菜单配置', children: menuSettingsContent },
            { key: 'security', label: '站点安全', children: securitySettingsContent },
            { key: 'relay', label: '模型调用安全', children: relaySettingsContent },
          ]} />
        )}


        {tab === 'database' && (
          <Tabs activeKey={dbSubTab} onChange={(k) => {
            setDbSubTab(k);
            navigate(`/${adminPath}/settings?tab=database&subtab=${k}`, { replace: true });
          }} items={[
            { key: 'db', label: '数据库设置', children: dbSettingsContent },
            { key: 'storage', label: '对象存储设置', children: storageSettingsContent },
            { key: 'cleanup', label: '数据清理与定时任务', children: dataCleanupContent },
          ]} />
        )}

        {!(tab === 'database' && dbSubTab === 'db') && (
          <Form.Item style={{ marginTop: 16 }}>
            <Button type="primary" onClick={handleSave} loading={loading}>{t('common.save')}</Button>
          </Form.Item>
        )}
      </Form>
      <Modal
        title={resetCountdown === null ? '初始化并清空当前数据库' : '即将清空当前数据'}
        open={resetOpen}
        onCancel={closeResetModal}
        maskClosable={false}
        closable={!resetting}
        confirmLoading={resetting}
        okText={resetCountdown === null ? '开始倒计时' : undefined}
        okButtonProps={{
          danger: true,
          disabled: resetPhrase.trim() !== DB_RESET_CONFIRM_TEXT || resetting,
          style: resetCountdown !== null ? { display: 'none' } : undefined,
        }}
        cancelText="取消"
        cancelButtonProps={{ disabled: resetting }}
        onOk={() => {
          if (resetPhrase.trim() !== DB_RESET_CONFIRM_TEXT) return;
          resetStartedRef.current = false;
          setResetCountdown(DB_RESET_COUNTDOWN_SECS);
        }}
      >
        {resetCountdown === null ? (
          <div>
            <Alert
              type="error"
              showIcon
              style={{ marginBottom: 16 }}
              message="此操作会清空当前数据库的全部业务数据（用户、令牌、日志、渠道等），并重建空表结构。"
              description="连接配置不会改变。完成后将进入与全新安装相同的超级管理员设置页。此操作不可撤销。"
            />
            <div style={{ marginBottom: 8 }}>请输入「{DB_RESET_CONFIRM_TEXT}」以继续：</div>
            <Input
              value={resetPhrase}
              placeholder={DB_RESET_CONFIRM_TEXT}
              onChange={(e) => setResetPhrase(e.target.value)}
              disabled={resetting}
              autoComplete="off"
            />
          </div>
        ) : (
          <div>
            <Alert
              type="warning"
              showIcon
              message={resetting ? '正在清空当前数据库…' : `将在 ${resetCountdown} 秒后开始清空当前数据`}
              description={resetting
                ? '清空完成后将自动进入全新安装，请设置首个超级管理员。请勿关闭页面。'
                : '倒计时期间可点击取消中止。倒计时结束后将真正执行，不可再撤销。'}
            />
          </div>
        )}
      </Modal>
      <SecurityIpModal
        open={securityIpModalConfig.open}
        type={securityIpModalConfig.type}
        mode={securityIpModalConfig.mode}
        initialList={getIpListFromForm(
          securityIpModalConfig.type === 'blacklist' ? 'ip_blacklist_text' : 'ip_whitelist_text'
        )}
        oppositeList={getIpListFromForm(
          securityIpModalConfig.type === 'blacklist' ? 'ip_whitelist_text' : 'ip_blacklist_text'
        )}
        onClose={() => setSecurityIpModalConfig((prev) => ({ ...prev, open: false }))}
        onSave={(newList) => {
          const fieldName =
            securityIpModalConfig.type === 'blacklist' ? 'ip_blacklist_text' : 'ip_whitelist_text';
          form.setFieldsValue({ [fieldName]: newList.join('\n') });
        }}
        onSaveAndApply={async (newList) => {
          const fieldName =
            securityIpModalConfig.type === 'blacklist' ? 'ip_blacklist_text' : 'ip_whitelist_text';
          form.setFieldsValue({ [fieldName]: newList.join('\n') });
          return await handleSave();
        }}
        onSwitchToEdit={() => {
          setSecurityIpModalConfig((prev) => ({ ...prev, mode: 'edit' }));
        }}
      />
      <SecurityEmailModal
        open={securityEmailModalConfig.open}
        mode={securityEmailModalConfig.mode}
        initialList={getEmailWhitelistFromForm()}
        onClose={() => setSecurityEmailModalConfig((prev) => ({ ...prev, open: false }))}
        onSave={(newList) => {
          form.setFieldValue(['registration', 'email_whitelist'], newList);
        }}
        onSaveAndApply={async (newList) => {
          form.setFieldValue(['registration', 'email_whitelist'], newList);
          return await handleSave();
        }}
        onSwitchToEdit={() => {
          setSecurityEmailModalConfig((prev) => ({ ...prev, mode: 'edit' }));
        }}
      />
    </Card>
  );
};

export default Settings;
