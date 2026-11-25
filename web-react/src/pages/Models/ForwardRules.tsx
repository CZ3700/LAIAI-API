import React, { useState, useEffect, useRef } from 'react';
import { Card, Table, Button, Space, Form, Input, Switch, message, Popconfirm, Modal, Tag, Select, Alert, Popover, Grid, Typography, Tooltip, Radio, InputNumber } from 'antd';
import MobileCardList, { MobileCard, CardRow, CardActions } from '../../components/MobileCardList';
import { listPagination, useListPager } from '../../components/ListPagination';
import { PlusOutlined, EditOutlined, DeleteOutlined, CodeOutlined, QuestionCircleOutlined, CopyOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { useLocation, useNavigate, useParams } from 'react-router-dom';
import request from '../../utils/request';
import { useThemeStore } from '../../store/theme';
import { fetchActivePlugins } from '../../utils/activePlugins';
import useSettingsStore from '../../store/settings';
import { forwardRulesEditPath, forwardRulesListPath, forwardRulesNewPath } from './modelPaths';

const { TextArea } = Input;
const { Text } = Typography;
const { useBreakpoint } = Grid;

const RES_MUL_KEYS = ['480p', '540p', '720p', '1080p', '2k', '4k'] as const;
type ResKey = (typeof RES_MUL_KEYS)[number];
const DEFAULT_CASCADE_RES: string[] = ['480p', '720p', '1080p', '2k', '4k'];
const PRESET_CATEGORIES = ['视频', '图片', '聊天', '音频', '封装', '排序', '向量'] as const;
const PRESET_IMAGE_RESOLUTIONS = ['1k', '2k', '4k', '1024x1024'] as const;
const PRESET_IMAGE_QUALITIES = ['low', 'medium', 'high', 'auto'] as const;

const filterActiveKeys = <T,>(map?: Record<string, T>, active?: string[]): Record<string, T> | undefined => {
  if (!map || !active) return map;
  const out: Record<string, T> = {};
  for (const k of active) {
    const key = k.trim().toLowerCase();
    if (map[key] !== undefined) out[key] = map[key];
  }
  return Object.keys(out).length ? out : undefined;
};

const ENHANCE_OPTIONS = [
  { value: 'standard', label: '标准' },
  { value: 'fast', label: '极速' },
  { value: 'pro', label: '专业' },
  { value: 'ai', label: '大模型' },
] as const;

/** 大模型增强允许的目标分辨率（与后端 resolve_volc_enhance 对齐） */
const AI_ENHANCE_RES = new Set<ResKey>(['720p', '1080p', '2k', '4k']);

/** 标准版场景（仅 standard 生效），默认 common */
const SCENE_OPTIONS = [
  { value: 'common', label: '通用' },
  { value: 'ugc', label: 'UGC 短视频' },
  { value: 'short_series', label: '短剧' },
  { value: 'aigc', label: 'AIGC' },
  { value: 'old_film', label: '老片修复' },
] as const;

/** 各目标分辨率允许的底座（首项为默认一级；单选项即锁定） */
const BASE_OPTIONS: Record<ResKey, string[]> = {
  '480p': ['480p'],
  '540p': ['540p', '480p'],
  '720p': ['480p', '720p'],
  '1080p': ['720p', '480p', '1080p'],
  '2k': ['1080p', '720p', '480p'],
  '4k': ['1080p', '720p', '480p'],
};

const getBaseOptions = (k: string): string[] => {
  const key = k.trim().toLowerCase();
  return (BASE_OPTIONS as Record<string, string[]>)[key] || [key, '720p', '480p'];
};

const ENHANCE_VALUES = new Set<string>(ENHANCE_OPTIONS.map((o) => o.value));
const SCENE_VALUES = new Set<string>(SCENE_OPTIONS.map((o) => o.value));
type SelectOpt = { value: string; label: string };
const SCENE_SELECT = SCENE_OPTIONS as unknown as SelectOpt[];

const isEnhanceValid = (k: string, v: string) =>
  ENHANCE_VALUES.has(v) && (v !== 'ai' || AI_ENHANCE_RES.has(k as ResKey));

const enhanceSelectOpts = (k: string): SelectOpt[] =>
  ENHANCE_OPTIONS.filter((o) => o.value !== 'ai' || AI_ENHANCE_RES.has(k as ResKey)) as unknown as SelectOpt[];

const mapResKeys = <T,>(fn: (k: ResKey) => T): Record<string, T> =>
  Object.fromEntries(RES_MUL_KEYS.map((k) => [k, fn(k)]));

const defaultResMul = (): Record<string, number> => mapResKeys(() => 1);
const defaultResEnhance = (): Record<string, string> => mapResKeys(() => 'standard');
const defaultResScene = (): Record<string, string> => mapResKeys(() => 'common');
const defaultResBase = (): Record<string, string> => mapResKeys((k) => BASE_OPTIONS[k][0]);
const defaultResEnhanceEnabled = (): Record<string, boolean> => ({ '480p': true, '540p': true });

const parseResEnhanceEnabled = (raw: unknown): Record<string, boolean> => {
  const out = defaultResEnhanceEnabled();
  if (!raw || typeof raw !== 'object') return out;
  const src = raw as Record<string, unknown>;
  for (const k of ['480p', '540p'] as const) {
    if (src[k] !== undefined) {
      out[k] = Boolean(src[k]);
    }
  }
  return out;
};

const compactResEnhanceEnabled = (raw: Record<string, boolean>): Record<string, boolean> | undefined => {
  const out: Record<string, boolean> = {};
  for (const k of ['480p', '540p'] as const) {
    if (raw[k] === false) {
      out[k] = false;
    }
  }
  return Object.keys(out).length ? out : undefined;
};

/** 字符串映射：非法/缺失保留 defaults，支持自定义 key */
const parseStrMap = (
  raw: unknown,
  defaults: () => Record<string, string>,
  valid: Set<string> | ((k: string, v: string) => boolean),
): Record<string, string> => {
  const out = defaults();
  if (!raw || typeof raw !== 'object') return out;
  const src = raw as Record<string, unknown>;
  for (const [rawK, rawV] of Object.entries(src)) {
    const k = rawK.trim().toLowerCase();
    const v = String(rawV ?? '').trim().toLowerCase();
    const ok = typeof valid === 'function' ? valid(k, v) : valid.has(v);
    if (ok) out[k] = v;
  }
  return out;
};

/** 仅保留 keep 为真的档位；全默认则不落库 */
const compactStrMap = (
  raw: Record<string, string>,
  keep: (k: string, v: string) => boolean,
): Record<string, string> | undefined => {
  const out: Record<string, string> = {};
  for (const [rawK, rawV] of Object.entries(raw)) {
    const k = rawK.trim().toLowerCase();
    const v = (rawV || '').trim().toLowerCase();
    if (v && keep(k, v)) out[k] = v;
  }
  return Object.keys(out).length ? out : undefined;
};

const parseResMul = (raw: unknown): Record<string, number> => {
  const out = defaultResMul();
  if (!raw || typeof raw !== 'object') return out;
  const src = raw as Record<string, unknown>;
  for (const [rawK, rawV] of Object.entries(src)) {
    const k = rawK.trim().toLowerCase();
    const v = Number(rawV);
    if (v > 0) out[k] = v;
  }
  return out;
};

const parseResEnhance = (raw: unknown) => parseStrMap(raw, defaultResEnhance, isEnhanceValid);
const parseResScene = (raw: unknown) => parseStrMap(raw, defaultResScene, SCENE_VALUES);
const parseResBase = (raw: unknown) =>
  parseStrMap(raw, defaultResBase, (k, v) => getBaseOptions(k).includes(v));

const compactResEnhance = (raw: Record<string, string>) =>
  compactStrMap(raw, (_k, v) => v !== 'standard');

/** scene 已由 parseResScene 校验，此处只过滤非标准档与默认 common */
const compactResScene = (enhance: Record<string, string>, scene: Record<string, string>) =>
  compactStrMap(scene, (k, v) => (enhance[k] || 'standard') === 'standard' && v !== 'common');

const compactResBase = (raw: Record<string, string>) =>
  compactStrMap(raw, (k, v) => (k in BASE_OPTIONS ? v !== getBaseOptions(k)[0] : Boolean(v && v.trim())));

const LIMITS_16_9: Record<string, number> = { '480p': 496, '540p': 540, '1080p': 1080, '2k': 1440, '4k': 2160 };
const LIMITS_3_4: Record<string, number> = { '480p': 560, '540p': 624, '720p': 834, '1080p': 1248, '2k': 1774, '4k': 2494 };
const RATIO_PRESET_LIMITS: Record<string, Record<string, number>> = {
  '16:9': LIMITS_16_9,
  '9:16': LIMITS_16_9,
  '1:1': { '480p': 640, '540p': 720, '720p': 960, '1080p': 1440, '2k': 2048, '4k': 2880 },
  '3:4': LIMITS_3_4,
  '4:3': LIMITS_3_4,
  '21:9': { '480p': 432, '540p': 472, '720p': 630, '1080p': 946, '2k': 1342, '4k': 1886 },
};
const DEFAULT_CASCADE_RATIOS = ['16:9', '9:16', '1:1', '3:4', '4:3', '21:9'];

const isCropManagedRatioRes = (ratio: string, res: string) =>
  (ratio === '16:9' || ratio === '9:16') && res === '720p';

const getPresetLimit = (ratio: string, res: string): number | undefined =>
  isCropManagedRatioRes(ratio, res) ? undefined : RATIO_PRESET_LIMITS[ratio]?.[res];

const defaultResLimit = (
  ratios: string[] = DEFAULT_CASCADE_RATIOS,
  resKeys: string[] = DEFAULT_CASCADE_RES,
): Record<string, number> => {
  const out: Record<string, number> = {};
  for (const ratio of ratios) {
    for (const res of resKeys) {
      // 16:9 与 9:16 的 1080p/2k/4k 默认关闭短边限制，走原生标准档位直出（大模型不回退）
      if ((ratio === '16:9' || ratio === '9:16') && (res === '1080p' || res === '2k' || res === '4k')) {
        continue;
      }
      const lim = getPresetLimit(ratio, res);
      if (lim) out[`${res}@${ratio}`] = lim;
    }
  }
  return out;
};

const parseResLimit = (raw: unknown): Record<string, number> =>
  raw && typeof raw === 'object'
    ? Object.fromEntries(
        Object.entries(raw as Record<string, unknown>)
          .map(([k, v]) => [k.trim().toLowerCase(), Number(v)] as const)
          .filter(([, v]) => typeof v === 'number' && Number.isInteger(v) && v > 0),
      )
    : {};

const extractRatiosFromResLimit = (raw: Record<string, unknown>): string[] =>
  Array.from(new Set(Object.keys(raw).map((k) => k.split('@')[1]?.trim().toLowerCase()).filter(Boolean)));

const compactResLimit = (
  raw: unknown,
  activeRes: string[],
  activeRatios: string[],
): Record<string, number> | undefined => {
  if (!raw || typeof raw !== 'object') return undefined;
  const out: Record<string, number> = {};
  const validRes = new Set(activeRes.map((r) => r.trim().toLowerCase()));
  const validRatios = new Set(activeRatios.map((r) => r.trim().toLowerCase()));
  for (const [rawK, rawV] of Object.entries(raw as Record<string, unknown>)) {
    const v = Number(rawV);
    if (!Number.isInteger(v) || v <= 0) continue;
    const k = rawK.trim().toLowerCase();
    if (k.includes('@')) {
      const [res, ratio] = k.split('@');
      if (!isCropManagedRatioRes(ratio, res) && validRes.has(res) && validRatios.has(ratio)) {
        out[k] = v;
      }
    } else if (validRes.has(k)) {
      out[k] = v;
    }
  }
  return Object.keys(out).length ? out : undefined;
};

const DEFAULT_CROP_COORDS = {
  volc: { '16:9': '2, 6, 862, 490', '9:16': '6, 2, 490, 862' },
  tencent: { '16:9': '0, 5, 864, 491', '9:16': '5, 0, 491, 864' },
  post: { '16:9': '1, 0, 1281, 720', '9:16': '0, 1, 720, 1281' },
} as const;

const cropDefault = (engine: unknown, timing: unknown, ratio: '16:9' | '9:16'): string =>
  timing === 'post' ? DEFAULT_CROP_COORDS.post[ratio] : (engine === 'tencent' ? DEFAULT_CROP_COORDS.tencent : DEFAULT_CROP_COORDS.volc)[ratio];

/** 将坐标（数组或字符串）规范化为表单字符串 */
const formatCropCoords = (val: unknown, fallback: string): string => {
  if (Array.isArray(val) && val.length === 4) {
    return val.join(', ');
  }
  if (typeof val === 'string' && val.trim()) {
    return val.trim();
  }
  return fallback;
};

/** 解析字符串或数组为四元组数字数组，非法返回 null */
const parseCropCoordsInput = (str: unknown): [number, number, number, number] | null => {
  if (Array.isArray(str) && str.length === 4) {
    const nums = str.map(Number);
    if (!nums.some(isNaN) && nums[2] > nums[0] && nums[3] > nums[1]) {
      return nums as [number, number, number, number];
    }
  }
  if (typeof str === 'string') {
    const parts = str
      .split(/[, ]+/)
      .map((s) => s.trim())
      .filter(Boolean)
      .map(Number);
    if (parts.length === 4 && !parts.some(isNaN) && parts[2] > parts[0] && parts[3] > parts[1]) {
      return parts as [number, number, number, number];
    }
  }
  return null;
};

/** 从坐标计算偶数画布规格 key (如 864x486) */
const cropCanvasKey = (str: unknown): string | null => {
  const c = parseCropCoordsInput(str);
  return c ? `${(c[2] - c[0]) & ~1}x${(c[3] - c[1]) & ~1}` : null;
};

/** 级联规则的阶段二引擎；非级联返回 null。缺省火山 MediaKit。 */
const cascadeEngineOf = (configJson: string): 'volc' | 'tencent' | null => {
  try {
    const config = JSON.parse(configJson || '{}');
    const flag = config?.is_cascade;
    const on = flag === true || flag === 1 || flag === '1' || flag === 'true';
    if (!on) return null;
    return String(config?.cascade_engine || '').toLowerCase() === 'tencent' ? 'tencent' : 'volc';
  } catch {
    return null;
  }
};

/** 封装分类、MediaKit 转发依赖火山插件；腾讯级联依赖腾讯云封装。 */
const enhancePluginOf = (item: { name?: string; category?: string; config_json?: string }): 'volc' | 'tencent' | null => {
  const engine = cascadeEngineOf(item.config_json || '');
  if (engine) return engine;
  if (item.category === '封装') return 'volc';
  if ((item.name || '').includes('MediaKit')) return 'volc';
  try {
    const config = JSON.parse(item.config_json || '{}');
    if (config?.target_type === 'volcengine_media_enhance') return 'volc';
  } catch { /* ignore */ }
  return null;
};

interface ForwardRule {
  id: number;
  name: string;
  rule_type: string;
  category: string;
  config_json: string;
  description?: string;
  eid?: string;
  is_active: number;
  is_system?: number;
  sort_order: number;
  created_at: string;
}

interface TagBatchFieldProps {
  label: React.ReactNode;
  tooltip?: string;
  placeholder: string;
  inputPlaceholder: string;
  tagColor?: string;
  values: string[];
  presets: readonly string[];
  onAdd: (val: string) => void;
  onRemove: (val: string) => void;
}

const TagBatchField: React.FC<TagBatchFieldProps> = ({
  label,
  tooltip,
  placeholder,
  inputPlaceholder,
  tagColor = 'blue',
  values,
  presets,
  onAdd,
  onRemove,
}) => {
  const availableToAdd = presets.filter(
    (k) => !values.some((x) => x.toLowerCase() === k.toLowerCase())
  );

  return (
    <Form.Item label={label} tooltip={tooltip}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <div>
          {values.length === 0 ? (
            <Text type="secondary" style={{ fontSize: 13 }}>{placeholder}</Text>
          ) : (
            <Space wrap size={[6, 6]}>
              {values.map((v) => (
                <Tag
                  key={v}
                  closable
                  onClose={(e) => {
                    e.preventDefault();
                    onRemove(v);
                  }}
                  color={tagColor}
                  style={{ fontSize: 13, padding: '2px 8px', margin: 0, borderRadius: 4 }}
                >
                  {v}
                </Tag>
              ))}
            </Space>
          )}
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
          {availableToAdd.length > 0 && (
            <>
              <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>快捷添加:</span>
              {availableToAdd.map((k) => (
                <Tag
                  key={k}
                  style={{ cursor: 'pointer', margin: 0, fontSize: 12 }}
                  onClick={() => onAdd(k)}
                >
                  +{k}
                </Tag>
              ))}
            </>
          )}
          <Input
            size="small"
            placeholder={inputPlaceholder}
            style={{ width: 140, height: 24, fontSize: 12 }}
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
                onAdd(val);
                inputEl.value = '';
              }
            }}
          />
        </div>
      </div>
    </Form.Item>
  );
};

const ForwardRules: React.FC = () => {
  const { t } = useTranslation();
  const { settings } = useSettingsStore();
  const adminPath = settings?.site?.admin_path || 'admin';
  const navigate = useNavigate();
  const location = useLocation();
  const { id: routeParamId } = useParams<{ id?: string }>();
  const normalizedPath = location.pathname.replace(/\/$/, '');
  const isNewRoute = /\/forward-rules\/new$/.test(normalizedPath);
  const isEditRoute = /\/forward-rules\/edit\/[^/]+$/.test(normalizedPath);
  const routeEditId = isEditRoute ? routeParamId : undefined;
  const isEditorOpen = isNewRoute || isEditRoute;
  const [items, setItems] = useState<ForwardRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [copyingId, setCopyingId] = useState<number | null>(null);
  const [isConfigModalVisible, setIsConfigModalVisible] = useState(false);
  const [editingItem, setEditingItem] = useState<ForwardRule | null>(null);
  const [currentConfig, setCurrentConfig] = useState<string>('');
  const [searchQuery, setSearchQuery] = useState('');
  const [categoryFilter, setCategoryFilter] = useState('all');
  const [ruleTypeFilter, setRuleTypeFilter] = useState('all');
  const { page: currentPage, pageSize, setPage: setCurrentPage, onChange } = useListPager();
  // 动态获取的模型分类类型列表（从 model_types 接口获取，保持与后台一致）
  const [modelTypes, setModelTypes] = useState<{ id: number; name: string }[]>([]);
  const [hasTencentEnhance, setHasTencentEnhance] = useState(false);
  const [hasVolcEnhance, setHasVolcEnhance] = useState(false);
  const [enhancePluginsReady, setEnhancePluginsReady] = useState(false);
  const [tencentCropTemplates, setTencentCropTemplates] = useState<Record<string, number>>({});
  const [form] = Form.useForm();
  const category = Form.useWatch('category', form);
  const isVideoCategory = (Array.isArray(category) ? category[0] : category)?.includes('视频');
  const isImageCategory = (Array.isArray(category) ? category[0] : category)?.includes('图片');
  const [activeResKeys, setActiveResKeys] = useState<string[]>([...DEFAULT_CASCADE_RES]);
  const [activeRatios, setActiveRatios] = useState<string[]>([...DEFAULT_CASCADE_RATIOS]);
  const screens = useBreakpoint();
  const newFormReadyRef = useRef(false);
  const hydratedEditIdRef = useRef<string | null>(null);

  const handleUpdateTag = (field: 'allowed_resolutions' | 'allowed_qualities', action: 'add' | 'remove', val: string) => {
    const norm = val.trim();
    if (!norm) return;
    const cur = (form.getFieldValue(field) as string[] | undefined) || [];
    if (action === 'add') {
      if (!cur.some((x) => x.toLowerCase() === norm.toLowerCase())) {
        form.setFieldValue(field, [...cur, norm]);
      }
    } else {
      form.setFieldValue(field, cur.filter((x) => x.toLowerCase() !== norm.toLowerCase()));
    }
  };

  const fetchTencentCropTemplates = async () => {
    try {
      const res: any = await request.get('/plugins/tencent_enhance/tencent-enhance-config');
      if (res?.crop_templates && typeof res.crop_templates === 'object') {
        setTencentCropTemplates(res.crop_templates);
      }
    } catch { /* ignore */ }
  };

  useEffect(() => {
    setCurrentPage(1);
  }, [searchQuery, categoryFilter, ruleTypeFilter]);

  useEffect(() => {
    if (!enhancePluginsReady || hasVolcEnhance) return;
    if (categoryFilter === '封装') setCategoryFilter('all');
  }, [enhancePluginsReady, hasVolcEnhance, categoryFilter]);

  const isEnhanceRuleVisible = (item: ForwardRule) => {
    const plugin = enhancePluginOf(item);
    if (!plugin) return true;
    if (!enhancePluginsReady) return false;
    return plugin === 'tencent' ? hasTencentEnhance : hasVolcEnhance;
  };

  const visibleItems = items.filter(isEnhanceRuleVisible);
  const uniqueCategories = Array.from(new Set([
    ...PRESET_CATEGORIES,
    ...modelTypes.map(t => t.name),
    ...visibleItems.map(i => i.category),
  ]))
    .filter(name => Boolean(name && (hasVolcEnhance || name !== '封装')))
    .sort((a, b) => {
      const ia = PRESET_CATEGORIES.indexOf(a as any);
      const ib = PRESET_CATEGORIES.indexOf(b as any);
      return (ia === -1 ? 99 : ia) - (ib === -1 ? 99 : ib) || a.localeCompare(b);
    });
  const uniqueTypes = Array.from(new Set(visibleItems.map(i => i.rule_type).filter(Boolean)));

  const filteredItems = visibleItems.filter(item => {
    let matchQuery = true;
    if (searchQuery) {
      const lowerQuery = searchQuery.toLowerCase();
      matchQuery = Boolean(
        (item.name && item.name.toLowerCase().includes(lowerQuery)) ||
        (item.eid && String(item.eid).toLowerCase().includes(lowerQuery))
      );
    }
    const matchCategory = categoryFilter === 'all' || item.category === categoryFilter;
    const matchType = ruleTypeFilter === 'all' || item.rule_type === ruleTypeFilter;
    return matchQuery && matchCategory && matchType;
  });

  const fetchItems = async () => {
    setLoading(true);
    try {
      const resp = await (request.get('/forward-rules') as any);
      setItems(resp);
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchItems();
    // 动态加载模型分类类型（用于新增/编辑弹窗的分类选择）
    (request.get('/model-types') as any).then((types: any[]) => {
      setModelTypes(types.filter((t: any) => t.is_active === 1));
    }).catch(() => { });
    fetchActivePlugins().then((res) => {
      const active = res?.active_plugins || [];
      const hasTencent = active.some((p: any) => p.name === 'tencent_enhance');
      setHasTencentEnhance(hasTencent);
      if (hasTencent) {
        fetchTencentCropTemplates();
      }
      setHasVolcEnhance(active.some((p: any) => p.name === 'volcengine_enhance'));
    }).catch(() => {}).finally(() => setEnhancePluginsReady(true));
  }, []);

  useEffect(() => {
    if (!hasTencentEnhance) return;
    const onFocus = () => {
      fetchTencentCropTemplates();
    };
    window.addEventListener('focus', onFocus);
    return () => window.removeEventListener('focus', onFocus);
  }, [hasTencentEnhance]);

  const handleAdd = () => {
    navigate(forwardRulesNewPath(adminPath));
  };

  const syncCrop = (nextEngine?: string, nextTiming?: 'pre' | 'post') => {
    const engine = nextEngine || form.getFieldValue('cascade_engine') || 'volc';
    const timing = nextTiming || form.getFieldValue('crop_timing') || 'pre';
    const cur169 = form.getFieldValue('crop_16_9');
    const cur916 = form.getFieldValue('crop_9_16');
    const defaults = [
      cropDefault('volc', 'pre', '16:9'), cropDefault('tencent', 'pre', '16:9'), cropDefault('volc', 'post', '16:9'),
      cropDefault('volc', 'pre', '9:16'), cropDefault('tencent', 'pre', '9:16'), cropDefault('volc', 'post', '9:16'),
    ];
    if (!cur169 || defaults.includes(cur169)) form.setFieldValue('crop_16_9', cropDefault(engine, timing, '16:9'));
    if (!cur916 || defaults.includes(cur916)) form.setFieldValue('crop_9_16', cropDefault(engine, timing, '9:16'));
  };

  const initNewForwardForm = () => {
    const initialEngine = hasVolcEnhance || !hasTencentEnhance ? 'volc' : 'tencent';
    setEditingItem(null);
    setActiveResKeys([...DEFAULT_CASCADE_RES]);
    setActiveRatios([...DEFAULT_CASCADE_RATIOS]);
    form.resetFields();
    form.setFieldsValue({
      category: '视频',
      is_active: true,
      config_json: '{\n  \n}',
      sort_order: 0,
      fast_response: false,
      is_cascade: false,
      image_options: false,
      allowed_resolutions: [],
      allowed_qualities: [],
      max_ref_images: undefined,
      min_pixels: undefined,
      max_pixels: undefined,
      step: 16,
      max_aspect_ratio: 3,
      crop_480p: true,
      crop_timing: 'pre',
      crop_16_9: cropDefault(initialEngine, 'pre', '16:9'),
      crop_9_16: cropDefault(initialEngine, 'pre', '9:16'),
      res_mul: defaultResMul(),
      res_enhance: defaultResEnhance(),
      res_enhance_enabled: defaultResEnhanceEnabled(),
      res_scene: defaultResScene(),
      res_base: defaultResBase(),
      res_limit: defaultResLimit(),
      res_limit_fallback: 'pro',
      s2_timeout_mins: 20,
      cascade_engine: initialEngine,
    });
  };

  const handleAddResKey = (raw: string) => {
    const res = raw.trim().toLowerCase();
    if (!res || activeResKeys.includes(res)) return;
    setActiveResKeys((prev) => [...prev, res]);
    if (!form.getFieldValue(['res_mul', res])) form.setFieldValue(['res_mul', res], 1.0);
    if (!form.getFieldValue(['res_base', res])) form.setFieldValue(['res_base', res], getBaseOptions(res)[0]);
    if (!form.getFieldValue(['res_enhance', res])) form.setFieldValue(['res_enhance', res], 'standard');
    for (const ratio of activeRatios) {
      const key = `${res}@${ratio}`;
      if ((ratio === '16:9' || ratio === '9:16') && (res === '1080p' || res === '2k' || res === '4k')) {
        continue;
      }
      const lim = getPresetLimit(ratio, res);
      if (lim && !form.getFieldValue(['res_limit', key])) {
        form.setFieldValue(['res_limit', key], lim);
      }
    }
  };

  const handleRemoveResKey = (k: string) => {
    setActiveResKeys((prev) => prev.filter((item) => item !== k));
  };

  const handleAddRatio = (rawRatio: string) => {
    const ratio = rawRatio.trim().toLowerCase();
    if (!ratio || activeRatios.includes(ratio)) return;
    setActiveRatios((prev) => [...prev, ratio]);
    for (const res of activeResKeys) {
      const key = `${res}@${ratio}`;
      if ((ratio === '16:9' || ratio === '9:16') && (res === '1080p' || res === '2k' || res === '4k')) {
        continue;
      }
      const lim = getPresetLimit(ratio, res);
      if (lim && !form.getFieldValue(['res_limit', key])) {
        form.setFieldValue(['res_limit', key], lim);
      }
    }
  };

  const handleRemoveRatio = (ratio: string) => {
    setActiveRatios((prev) => prev.filter((r) => r !== ratio));
  };

  const handleApplyAllPresets = () => {
    const current = { ...(form.getFieldValue('res_limit') || {}) };
    for (const ratio of activeRatios) {
      for (const res of activeResKeys) {
        const lim = getPresetLimit(ratio, res);
        if (lim) {
          current[`${res}@${ratio}`] = lim;
          form.setFieldValue(['res_limit', `${res}@${ratio}`], lim);
        }
      }
    }
    form.setFieldValue('res_limit', current);
  };

  const hydrateForwardEditor = (item: ForwardRule) => {
    let pollPath = '';
    let isCascade = false;
    let fastResponse = false;
    let crop480p = true;
    let cropTiming: 'pre' | 'post' = 'pre';
    let cascadeEngine = 'volc';
    let crop169: string = cropDefault('volc', 'pre', '16:9');
    let crop916: string = cropDefault('volc', 'pre', '9:16');
    let resMul = defaultResMul();
    let resEnhance = defaultResEnhance();
    let resEnhanceEnabled = defaultResEnhanceEnabled();
    let resScene = defaultResScene();
    let resBase = defaultResBase();
    let resLimit: Record<string, number | undefined> = {};
    let resLimitFallback = 'pro';
    let s2TimeoutMins = 20;
    let configuredRes: string[] = [];
    let imageOptions = false;
    let allowedResolutions: string[] = [];
    let allowedQualities: string[] = [];
    let maxRefImages: number | undefined = undefined;
    let minPixels: number | undefined = undefined;
    let maxPixels: number | undefined = undefined;
    let autoDowngrade = false;
    let step: number | undefined = undefined;
    let maxAspectRatio: number | undefined = undefined;
    try {
      const config = JSON.parse(item.config_json);
      imageOptions = Boolean(config.image_options || config.gpt_options_enabled);
      autoDowngrade = Boolean(config.auto_downgrade);
      if (Array.isArray(config.allowed_resolutions)) {
        allowedResolutions = config.allowed_resolutions.filter(Boolean);
      }
      if (Array.isArray(config.allowed_qualities)) {
        allowedQualities = config.allowed_qualities.filter(Boolean);
      }
      const numCfg = (k: string) => (typeof config[k] === 'number' ? config[k] : undefined);
      maxRefImages = numCfg('max_ref_images');
      minPixels = numCfg('min_pixels');
      maxPixels = numCfg('max_pixels');
      step = numCfg('step');
      maxAspectRatio = numCfg('max_aspect_ratio');
      pollPath = config.poll_path || '';
      isCascade = !!config.is_cascade;
      fastResponse = !!config.fast_response;
      // 缺省 true；与后端 unwrap_or(true) 一致
      crop480p = config.crop_480p !== false;
      cropTiming = config.crop_timing === 'post' ? 'post' : 'pre';
      cascadeEngine = config.cascade_engine === 'tencent' ? 'tencent' : 'volc';
      const coordsMap = cropTiming === 'post' ? config.crop_coords_720p : config.crop_coords_480p;
      crop169 = formatCropCoords(coordsMap?.['16:9'], cropDefault(cascadeEngine, cropTiming, '16:9'));
      crop916 = formatCropCoords(coordsMap?.['9:16'], cropDefault(cascadeEngine, cropTiming, '9:16'));
      resMul = parseResMul(config.res_mul);
      resEnhance = parseResEnhance(config.res_enhance);
      resEnhanceEnabled = parseResEnhanceEnabled(config.res_enhance_enabled);
      resScene = parseResScene(config.res_scene);
      resBase = parseResBase(config.res_base);
      resLimit = parseResLimit(config.res_limit);
      if (['pro', 'standard', 'fast'].includes(config.res_limit_fallback)) {
        resLimitFallback = config.res_limit_fallback;
      }
      if (typeof config.s2_timeout_mins === 'number' && config.s2_timeout_mins > 0) {
        s2TimeoutMins = Math.round(config.s2_timeout_mins);
      }
      if (config.res_mul && typeof config.res_mul === 'object') {
        configuredRes = Object.keys(config.res_mul)
          .map((k) => k.trim().toLowerCase())
          .filter(Boolean);
      }
    } catch (e) { /* ignore */ }

    const extractedRatios = extractRatiosFromResLimit(resLimit);
    const resolvedRatios = extractedRatios.length > 0 ? extractedRatios : [...DEFAULT_CASCADE_RATIOS];
    const initialResKeys = configuredRes.length > 0 ? configuredRes : [...DEFAULT_CASCADE_RES];
    // 忠实采用已保存的实际短边限制配置，不再暴力合入默认预设，允许用户将单元格留空以关闭该项短边限制
    const mergedResLimit = resLimit || {};

    setActiveResKeys(initialResKeys);
    setActiveRatios(resolvedRatios);
    setEditingItem(item);
    form.setFieldsValue({
      ...item,
      category: item.category || '视频',
      poll_path: pollPath,
      fast_response: !!fastResponse,
      is_cascade: !!isCascade,
      crop_480p: crop480p,
      crop_timing: cropTiming,
      crop_16_9: crop169,
      crop_9_16: crop916,
      res_mul: resMul,
      res_enhance: resEnhance,
      res_enhance_enabled: resEnhanceEnabled,
      res_scene: resScene,
      res_base: resBase,
      res_limit: mergedResLimit,
      res_limit_fallback: resLimitFallback,
      s2_timeout_mins: s2TimeoutMins,
      cascade_engine: cascadeEngine,
      is_active: item.is_active === 1,
      sort_order: item.sort_order || 0,
      image_options: imageOptions,
      allowed_resolutions: allowedResolutions,
      allowed_qualities: allowedQualities,
      max_ref_images: maxRefImages,
      min_pixels: minPixels,
      max_pixels: maxPixels,
      auto_downgrade: autoDowngrade,
      step: step ?? 16,
      max_aspect_ratio: maxAspectRatio ?? 3,
    });
  };

  const handleEdit = (item: ForwardRule) => {
    navigate(forwardRulesEditPath(adminPath, item.id));
  };

  const handleCloseEditor = () => {
    navigate(forwardRulesListPath(adminPath));
  };

  useEffect(() => {
    if (!isNewRoute) {
      newFormReadyRef.current = false;
      return;
    }
    if (newFormReadyRef.current) return;
    newFormReadyRef.current = true;
    initNewForwardForm();
  }, [isNewRoute]);

  useEffect(() => {
    if (!routeEditId) {
      hydratedEditIdRef.current = null;
      return;
    }
    if (loading || !enhancePluginsReady) return;
    if (hydratedEditIdRef.current === routeEditId) return;
    const target = items.find((item) => String(item.id) === String(routeEditId));
    if (target && isEnhanceRuleVisible(target)) {
      hydratedEditIdRef.current = routeEditId;
      hydrateForwardEditor(target);
    } else if (hydratedEditIdRef.current !== `missing:${routeEditId}`) {
      hydratedEditIdRef.current = `missing:${routeEditId}`;
      message.error('未找到指定转发规则');
      handleCloseEditor();
    }
  }, [routeEditId, items, loading, enhancePluginsReady, hasVolcEnhance, hasTencentEnhance]);

  useEffect(() => {
    if (!enhancePluginsReady || !isEditorOpen) return;
    if (!hasVolcEnhance && !hasTencentEnhance) return;
    const engine = form.getFieldValue('cascade_engine');
    const valid = engine === 'tencent' ? hasTencentEnhance : hasVolcEnhance;
    if (valid) return;
    const next = hasVolcEnhance ? 'volc' : 'tencent';
    form.setFieldValue('cascade_engine', next);
    syncCrop(next);
  }, [enhancePluginsReady, hasVolcEnhance, hasTencentEnhance, isEditorOpen]);

  const handleStatusChange = async (record: ForwardRule, checked: boolean) => {
    try {
      await request.put(`/forward-rules/${record.id}`, {
        is_active: checked ? 1 : 0
      });
      message.success(t('common.success'));
      fetchItems();
    } catch (e) {
      console.error(e);
    }
  };

  const handleDelete = async (id: number) => {
    try {
      await request.delete(`/forward-rules/${id}`);
      message.success(t('common.success'));
      fetchItems();
    } catch (e) {
      console.error(e);
    }
  };

  const handleCopy = async (record: ForwardRule) => {
    try {
      setCopyingId(record.id);
      const existingNames = new Set(items.map((i) => i.name));
      let baseName = record.name;
      const copyMatch = baseName.match(/^(.*?)\s*\(副本(?:\s*\d+)?\)$/);
      if (copyMatch) {
        baseName = copyMatch[1];
      }
      let newName = `${baseName} (副本)`;
      let c = 2;
      while (existingNames.has(newName)) {
        newName = `${baseName} (副本 ${c++})`;
      }
      await request.post('/forward-rules', {
        name: newName,
        rule_type: record.rule_type,
        category: record.category,
        description: record.description,
        config_json: record.config_json,
        is_active: record.is_active,
        sort_order: record.sort_order,
      });
      message.success(t('common.copy_success', '复制成功'));
      fetchItems();
    } catch (e) {
      console.error(e);
    } finally {
      setCopyingId(null);
    }
  };

  const handleSave = async (values: any) => {
    if (isEditRoute && hydratedEditIdRef.current !== routeEditId) return;
    try {
      let configObj: any = {};
      try {
        configObj = JSON.parse(values.config_json || '{}');
      } catch (err) {
        message.error("配置内容不是合法的 JSON 格式");
        return;
      }

      // 自动同步 poll_path 到 config_json
      if (values.poll_path) {
        configObj.poll_path = values.poll_path;
      } else {
        delete configObj.poll_path;
      }

      // 级联：res_mul / res_enhance / res_scene / res_base（缺省值不落库）
      const putOpt = (key: string, val?: Record<string, any>) => {
        if (val) configObj[key] = val;
        else delete configObj[key];
      };
      const cascadeOpen = enhancePluginsReady && values.is_cascade && (hasVolcEnhance || hasTencentEnhance);
      const useTencent = hasTencentEnhance && (values.cascade_engine === 'tencent' || !hasVolcEnhance);
      if (!enhancePluginsReady) {
        // 插件列表未返回前不改写级联字段，避免把已有配置误删
      } else if (cascadeOpen) {
        configObj.is_cascade = true;
        configObj.fast_response = true;
        const s2Timeout = Math.round(Number(values.s2_timeout_mins));
        if (Number.isFinite(s2Timeout) && s2Timeout > 0 && s2Timeout !== 20) {
          configObj.s2_timeout_mins = s2Timeout;
        } else {
          delete configObj.s2_timeout_mins;
        }
        configObj.res_mul = filterActiveKeys(parseResMul(values.res_mul), activeResKeys) || {};
        putOpt('res_base', filterActiveKeys(compactResBase(parseResBase(values.res_base)), activeResKeys));
        putOpt('res_enhance_enabled', filterActiveKeys(compactResEnhanceEnabled(parseResEnhanceEnabled(values.res_enhance_enabled)), activeResKeys));
        putOpt('res_limit', compactResLimit(values.res_limit, activeResKeys, activeRatios));
        if (useTencent) {
          configObj.cascade_engine = 'tencent';
        } else {
          delete configObj.cascade_engine;
          const enhanceMap = parseResEnhance(values.res_enhance);
          putOpt('res_enhance', filterActiveKeys(compactResEnhance(enhanceMap), activeResKeys));
          putOpt('res_scene', filterActiveKeys(compactResScene(enhanceMap, parseResScene(values.res_scene)), activeResKeys));
          if (values.res_limit_fallback && values.res_limit_fallback !== 'pro') {
            configObj.res_limit_fallback = values.res_limit_fallback;
          } else {
            delete configObj.res_limit_fallback;
          }
        }
        if (values.crop_480p === false) {
          configObj.crop_480p = false;
          delete configObj.crop_timing;
          delete configObj.crop_coords_480p;
          delete configObj.crop_coords_720p;
        } else {
          delete configObj.crop_480p;
          const timing = values.crop_timing === 'post' ? 'post' : 'pre';
          if (timing === 'post') {
            configObj.crop_timing = 'post';
          } else {
            delete configObj.crop_timing;
          }

          const isTencent = useTencent;
          const engineName = useTencent ? 'tencent' : 'volc';
          const coords169 = parseCropCoordsInput(values.crop_16_9) || (isTencent ? parseCropCoordsInput(cropDefault('tencent', timing, '16:9')) : null);
          const coords916 = parseCropCoordsInput(values.crop_9_16) || (isTencent ? parseCropCoordsInput(cropDefault('tencent', timing, '9:16')) : null);
          const def169 = parseCropCoordsInput(cropDefault(engineName, timing, '16:9'));
          const def916 = parseCropCoordsInput(cropDefault(engineName, timing, '9:16'));
          const is169Custom = coords169 && (!def169 || coords169.some((v, i) => v !== def169[i]));
          const is916Custom = coords916 && (!def916 || coords916.some((v, i) => v !== def916[i]));

          const targetField = timing === 'post' ? 'crop_coords_720p' : 'crop_coords_480p';
          const otherField = timing === 'post' ? 'crop_coords_480p' : 'crop_coords_720p';
          delete configObj[otherField];

          // 腾讯云增强始终保留并显式落库此 2 个裁剪坐标，方便开箱即用无需额外调整；火山引擎在非自定义时省去冗余配置
          if (isTencent || is169Custom || is916Custom) {
            const map: Record<string, number[]> = {};
            if (coords169) map['16:9'] = coords169;
            if (coords916) map['9:16'] = coords916;
            configObj[targetField] = map;
          } else {
            delete configObj[targetField];
          }
        }
      } else {
        delete configObj.is_cascade;
        delete configObj.crop_480p;
        delete configObj.crop_timing;
        delete configObj.crop_coords_480p;
        delete configObj.crop_coords_720p;
        delete configObj.res_mul;
        delete configObj.res_enhance_enabled;
        delete configObj.res_enhance;
        delete configObj.res_scene;
        delete configObj.res_base;
        delete configObj.res_limit;
        delete configObj.res_limit_fallback;
        delete configObj.s2_timeout_mins;
        delete configObj.cascade_engine;
      }

      // 快返开关：显式勾选或级联模式下启用，否则移除保持配置精简
      if (values.fast_response || cascadeOpen) {
        configObj.fast_response = true;
      } else {
        delete configObj.fast_response;
      }

      if (values.image_options) {
        configObj.image_options = true;
        delete configObj.gpt_options_enabled;
        if (values.auto_downgrade) {
          configObj.auto_downgrade = true;
        } else {
          delete configObj.auto_downgrade;
        }
        const resList = values.allowed_resolutions ?? form.getFieldValue('allowed_resolutions') ?? [];
        if (Array.isArray(resList) && resList.length > 0) {
          configObj.allowed_resolutions = resList;
        } else {
          delete configObj.allowed_resolutions;
        }
        const qualList = values.allowed_qualities ?? form.getFieldValue('allowed_qualities') ?? [];
        if (Array.isArray(qualList) && qualList.length > 0) {
          configObj.allowed_qualities = qualList;
        } else {
          delete configObj.allowed_qualities;
        }
        const numKeys = ['max_ref_images', 'min_pixels', 'max_pixels', 'step', 'max_aspect_ratio'] as const;
        for (const k of numKeys) {
          const val = Number(values[k]);
          if (val > 0) configObj[k] = val;
          else delete configObj[k];
        }
      } else {
        const imageKeys = ['image_options', 'gpt_options_enabled', 'auto_downgrade', 'allowed_resolutions', 'allowed_qualities', 'max_ref_images', 'min_pixels', 'max_pixels', 'step', 'max_aspect_ratio'];
        for (const k of imageKeys) delete configObj[k];
      }

      const payload = {
        ...values,
        config_json: JSON.stringify(configObj, null, 2),
        category: (Array.isArray(values.category) && values.category.length > 0) ? values.category[0] : (values.category || '视频'),
        is_active: values.is_active ? 1 : 0,
      };
      // 表单辅助字段已合并进 config_json，不单独提交
      const formHelperKeys = [
        'poll_path', 'fast_response', 'is_cascade', 'crop_480p', 'crop_timing',
        'crop_16_9', 'crop_9_16', 'res_mul', 'res_enhance_enabled', 'res_enhance',
        'res_scene', 'res_base', 'res_limit', 'res_limit_fallback', 's2_timeout_mins',
        'image_options', 'gpt_options_enabled', 'auto_downgrade', 'allowed_resolutions', 'allowed_qualities',
        'max_ref_images', 'min_pixels', 'max_pixels', 'step', 'max_aspect_ratio',
      ];
      for (const k of formHelperKeys) delete (payload as Record<string, unknown>)[k];

      if (editingItem) {
        await request.put(`/forward-rules/${editingItem.id}`, payload);
      } else {
        await request.post('/forward-rules', payload);
      }
      message.success(t('common.success'));
      handleCloseEditor();
      fetchItems();
    } catch (e) {
      console.error(e);
    }
  };

  const viewConfigJson = (jsonStr: string) => {
    try {
      const formatted = JSON.stringify(JSON.parse(jsonStr), null, 2);
      setCurrentConfig(formatted);
    } catch (e) {
      setCurrentConfig(jsonStr);
    }
    setIsConfigModalVisible(true);
  };

  const columns = [
    {
      title: '转发 (EID)',
      dataIndex: 'eid',
      key: 'eid',
      width: 120,
      render: (text: string) => <Tag color="blue">{text || '-'}</Tag>
    },
    {
      title: '规则名称',
      dataIndex: 'name',
      key: 'name',
      width: 200,
      render: (text: string) => <Text strong>{text}</Text>
    },
    {
      title: '来源类型',
      dataIndex: 'is_system',
      key: 'is_system',
      width: 100,
      render: (is_system: number) => is_system === 1 ? <Tag color="blue">系统内置</Tag> : <Tag color="default">自定义</Tag>
    },
    {
      title: '模式/厂商类型',
      dataIndex: 'rule_type',
      key: 'rule_type',
      width: 150,
      render: (text: string) => <Tag color="purple">{text}</Tag>
    },
    {
      title: '所属分类',
      dataIndex: 'category',
      key: 'category',
      width: 100,
      render: (text: string) => {
        const colorMap: Record<string, string> = {
          '聊天': 'blue', '图片': 'magenta', '视频': 'volcano',
          '语音': 'green', '音频': 'green', '向量': 'geekblue', '排序': 'gold',
        };
        return <Tag color={colorMap[text] || 'cyan'}>{text || '聊天'}</Tag>;
      }
    },
    {
      title: '应用详情描述',
      dataIndex: 'description',
      key: 'description',
      ellipsis: true,
    },
    {
      title: '排序',
      dataIndex: 'sort_order',
      key: 'sort_order',
      width: 90,
      sorter: (a: ForwardRule, b: ForwardRule) => (a.sort_order || 0) - (b.sort_order || 0),
      render: (text: number) => <Text>{text || 0}</Text>
    },
    {
      title: 'JSON 配置',
      key: 'config',
      render: (_: any, record: ForwardRule) => (
        <Button size="small" type="dashed" icon={<CodeOutlined />} onClick={() => viewConfigJson(record.config_json)}>
          查看 JSON 详情
        </Button>
      ),
      width: 150,
    },
    {
      title: t('common.status'),
      dataIndex: 'is_active',
      key: 'is_active',
      render: (active: number) => {
        const isActive = active === 1;
        return (
          <span style={{
            display: 'inline-flex', alignItems: 'center', gap: 4,
            padding: '2px 6px', borderRadius: 4, fontSize: 12,
            background: isActive ? 'rgba(128,128,128,0.06)' : 'transparent',
            color: isActive ? 'var(--text-color, inherit)' : '#8c8c8c',
            border: isActive ? '1px solid rgba(128,128,128,0.15)' : '1px dashed rgba(128,128,128,0.3)'
          }}>
            <span style={{ width: 6, height: 6, borderRadius: '50%', background: isActive ? '#8c8c8c' : 'transparent', border: isActive ? 'none' : '1px solid #8c8c8c' }} />
            {isActive ? t('common.active') : t('common.disabled')}
          </span>
        );
      },
      width: 100,
    },
    {
      title: t('common.actions'),
      key: 'actions',
      render: (_: any, record: ForwardRule) => (
        <Space>
          <Button icon={<EditOutlined />} onClick={() => handleEdit(record)} size="small" />
          <Tooltip title={t('common.copy', '复制规则')}>
            <Button
              icon={<CopyOutlined />}
              onClick={() => handleCopy(record)}
              size="small"
              loading={copyingId === record.id}
            />
          </Tooltip>
          {record.is_system === 1 ? (
            <Tooltip title="系统内置规则，不可删除">
              <Button icon={<DeleteOutlined />} disabled size="small" />
            </Tooltip>
          ) : (
            <Popconfirm title={t('common.confirm_delete')} onConfirm={() => handleDelete(record.id)}>
              <Button icon={<DeleteOutlined />} danger size="small" />
            </Popconfirm>
          )}
        </Space>
      ),
      width: 136,
    },
  ];

  const { themeMode } = useThemeStore();
  const _isLight = themeMode === 'light';
  const sectionDividerStyle: React.CSSProperties = {
    paddingBottom: 16,
    marginBottom: 16,
    borderBottom: _isLight ? '1px dashed rgba(0,0,0,0.08)' : '1px dashed rgba(255,255,255,0.08)',
  };

  const CText: React.FC<{ children: React.ReactNode }> = ({ children }) => (
    <span style={{
      background: _isLight ? 'rgba(0,0,0,0.06)' : '#252526',
      color: _isLight ? '#cf222e' : '#ce9178',
      padding: '1px 5px',
      borderRadius: 4,
      fontFamily: 'monospace',
      fontSize: 12,
      wordBreak: 'break-all',
      overflowWrap: 'anywhere',
    }}>{children}</span>
  );

  const ParamNo: React.FC<{ n: string; children: React.ReactNode }> = ({ n, children }) => (
    <div style={{ display: 'flex', gap: 8, marginBottom: 8, lineHeight: 1.5, minWidth: 0 }}>
      <span style={{
        flexShrink: 0,
        minWidth: 28,
        padding: '0 6px',
        height: 22,
        lineHeight: '22px',
        textAlign: 'center',
        borderRadius: 4,
        fontSize: 12,
        fontWeight: 600,
        background: _isLight ? 'rgba(24,144,255,0.12)' : 'rgba(24,144,255,0.25)',
        color: '#1890ff',
      }}>{n}</span>
      <div style={{ flex: 1, minWidth: 0, overflowWrap: 'anywhere' }}>{children}</div>
    </div>
  );

  const helpParams: { n: string; body: React.ReactNode }[] = [
    {
      n: '1',
      body: <>
        <CText>target_type</CText>：目标协议类型。常用 <CText>openai</CText>、<CText>anthropic</CText>、<CText>gemini</CText>、<CText>volcengine</CText>、<CText>dashscope</CText>、<CText>kling_video</CText>、<CText>minimax_video</CText>、<CText>fal_video</CText>、<CText>vidu</CText>、<CText>tencent_vod_video</CText> 等。
      </>,
    },
    {
      n: '2',
      body: <>
        <CText>path_rewrites</CText>：路径重写映射数组（推荐）。支持多路由同时映射至上游，数组项包含 <CText>old</CText>（客户端入口）与 <CText>new</CText>（上游实际路径，支持 <CText>{`\${model}`}</CText> 宏）。
        <div style={{ marginTop: 4, fontSize: 12, opacity: 0.85 }}>
          示例：<CText>{`[{"old":"/v1/video/generations","new":"/minimax/v2/video_generation/tpl-3"}]`}</CText>。单条改写亦可简写为对象 <CText>path_rewrite</CText>；全透传模式支持配置 <CText>path_passthrough</CText> 数组。
        </div>
      </>,
    },
    {
      n: '3',
      body: <>
        <CText>auth_type</CText>：上游鉴权方式，默认 <CText>bearer</CText>。可选 <CText>query_key</CText>、<CText>x-goog-api-key</CText>、<CText>x-api-key</CText>、<CText>token</CText>（Authorization: Token）、<CText>key</CText>（Authorization: Key）、<CText>tencent_vod</CText>、<CText>tencent_mps</CText> 等。
      </>,
    },
    {
      n: '4',
      body: <>
        <CText>poll_path</CText>：异步任务轮询路径，支持占位宏 <CText>{`\${task_id}`}</CText> 与 <CText>{`\${model}`}</CText>。如 <CText>{`/minimax/query/\${task_id}`}</CText>。
      </>,
    },
    {
      n: '5',
      body: <>
        <CText>asset_convert</CText>：设为 <CText>true</CText> 时自动将 content 中的网络 URL / base64 转为方舟素材 ID（<CText>asset://</CText>）。
      </>,
    },
    {
      n: '6',
      body: <>
        <CText>asset_convert_ns</CText>：素材插件命名空间，默认 <CText>asset_manager</CText>，国际版 <CText>asset_manager_intl</CText>。
      </>,
    },
    {
      n: '7',
      body: <>
        <CText>fast_response</CText>：设为 <CText>true</CText> 时启用视频模型异步任务受理优先快返（先返回任务已受理响应，后台异步处理素材转换/级联封装与调用上游；适合素材转换或级联耗时较长、易造成客户端 HTTP 超时的场景）。未配置或设为 <CText>false</CText> 则保持同步等待上游响应。
      </>,
    },
    {
      n: '8',
      body: <>
        <CText>moderation</CText>：设为 <CText>true</CText> 时素材注册免审核（Skip）。
      </>,
    },
    {
      n: '9',
      body: <>
        <CText>content_to_prompt</CText>：无 prompt 字段时自动从 content 文本提取写入（部分视频通道）。
      </>,
    },
    {
      n: '10',
      body: <>
        <CText>is_cascade</CText>：启用二阶段级联（底座成片后自动封装）。
      </>,
    },
    {
      n: '11',
      body: <>
        <CText>cascade_engine</CText>：插件启用后可选 <CText>tencent</CText>（腾讯云封装）；缺省或不写则走火山 MediaKit。
      </>,
    },
    {
      n: '12',
      body: <>
        <CText>res_mul</CText>：级联各分辨率消耗倍率映射，如 <CText>{`{"720p":1.5,"1080p":2.0,"2k":2.5,"4k":4}`}</CText>。
      </>,
    },
    {
      n: '13',
      body: <>
        <CText>res_enhance</CText>：级联封装版本（<CText>fast | standard | pro | ai</CText>），默认 <CText>standard</CText>。
      </>,
    },
    {
      n: '14',
      body: <>
        <CText>res_scene</CText>：标准封装版本下的场景优化（<CText>common | aigc | short_series</CText> 等）。
      </>,
    },
    {
      n: '15',
      body: <>
        <CText>res_base</CText>：级联目标分辨率对应的底座分辨率映射，如 <CText>{`{"1080p":"720p"}`}</CText>。
      </>,
    },
    {
      n: '16',
      body: <>
        <CText>crop_480p</CText>：480p 底座升 720p 时的智能居中裁剪开关（默认 <CText>true</CText>），配合 <CText>crop_timing</CText>（<CText>pre | post</CText>）。
      </>,
    },
  ];

  const helpContent = (
    <div style={{
      width: '100%',
      maxWidth: 480,
      maxHeight: 'min(70vh, 560px)',
      overflowX: 'hidden',
      overflowY: 'auto',
      color: _isLight ? 'rgba(0, 0, 0, 0.85)' : 'rgba(255, 255, 255, 0.85)',
      boxSizing: 'border-box',
    }}>
      <p style={{ marginBottom: 10 }}>
        用于<strong>路径重写</strong>与<strong>协议转换</strong>，将标准 OpenAI 请求适配各厂商上游。配置后在「模型列表」绑定模型。
      </p>
      <b>核心参数（1–3）</b>
      <div style={{ marginTop: 8 }}>
        {helpParams.slice(0, 3).map((p) => (
          <ParamNo key={p.n} n={p.n}>{p.body}</ParamNo>
        ))}
      </div>
      <b>可选参数（4–14）</b>
      <div style={{ marginTop: 8 }}>
        {helpParams.slice(3).map((p) => (
          <ParamNo key={p.n} n={p.n}>{p.body}</ParamNo>
        ))}
      </div>
    </div>
  );

  const paginationConfig = listPagination({
    current: currentPage,
    pageSize,
    onChange,
  });

  return (
    <>
      <Card variant="borderless">
        <div style={{ display: 'flex', flexDirection: screens.xs ? 'column' : 'row', justifyContent: 'space-between', marginBottom: 16, gap: 12 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
            <Typography.Title level={4} style={{ margin: 0, fontSize: screens.xs ? 18 : 20, fontWeight: 600 }}>
              高级转发规则管理
            </Typography.Title>
            <Popover
              content={helpContent}
              title="什么是高级转发规则引擎？"
              trigger="hover"
              placement="bottomLeft"
              overlayInnerStyle={{ maxWidth: 520, overflow: 'hidden' }}
            >
              <QuestionCircleOutlined style={{ color: '#1890ff', cursor: 'pointer', fontSize: 18 }} />
            </Popover>
          </div>
          <Button type="primary" icon={<PlusOutlined />} onClick={handleAdd}>
            {screens.xs ? '新增' : '新增定制规则'}
          </Button>
        </div>
        <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap', alignItems: 'center', marginBottom: 20 }}>
          <Input.Search
            placeholder="搜索规则名称或EID"
            allowClear
            onSearch={setSearchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            style={{ width: 200 }}
          />
          <Space wrap size={[0, 8]}>
            <Text type="secondary" style={{ fontSize: 13, marginRight: 4 }}>所属分类:</Text>
            <Radio.Group
              value={categoryFilter}
              onChange={e => setCategoryFilter(e.target.value)}
              optionType="button"
              buttonStyle="solid"
              size="small"
            >
              <Radio.Button value="all">全部分类</Radio.Button>
              {uniqueCategories.map(c => <Radio.Button key={c} value={c}>{c}</Radio.Button>)}
            </Radio.Group>
          </Space>
          <Space wrap size={[0, 8]}>
            <Text type="secondary" style={{ fontSize: 13, marginRight: 4 }}>类型:</Text>
            <Radio.Group
              value={ruleTypeFilter}
              onChange={e => setRuleTypeFilter(e.target.value)}
              optionType="button"
              buttonStyle="solid"
              size="small"
            >
              <Radio.Button value="all">全部类型</Radio.Button>
              {uniqueTypes.map(t => <Radio.Button key={t} value={t}>{t}</Radio.Button>)}
            </Radio.Group>
          </Space>
        </div>
        {screens.xs ? (
          <MobileCardList
            dataSource={filteredItems}
            loading={loading}
            rowKey="id"
            pagination={paginationConfig}
            renderCard={(record: any) => {
              const mobileColorMap: Record<string, string> = {
                '聊天': 'blue', '图片': 'magenta', '视频': 'volcano',
                '语音': 'green', '音频': 'green', '向量': 'geekblue', '排序': 'gold',
              };
              const categoryColor = mobileColorMap[record.category] || 'cyan';
              return (
                <MobileCard
                  title={<Space><Text strong>{record.name}</Text></Space>}
                  extra={(() => {
                    const isActive = record.is_active === 1;
                    return (
                      <span style={{
                        display: 'inline-flex', alignItems: 'center', gap: 4,
                        padding: '2px 6px', borderRadius: 4, fontSize: 12,
                        background: isActive ? 'rgba(128,128,128,0.06)' : 'transparent',
                        color: isActive ? 'var(--text-color, inherit)' : '#8c8c8c',
                        border: isActive ? '1px solid rgba(128,128,128,0.15)' : '1px dashed rgba(128,128,128,0.3)'
                      }}>
                        <span style={{ width: 6, height: 6, borderRadius: '50%', background: isActive ? '#8c8c8c' : 'transparent', border: isActive ? 'none' : '1px solid #8c8c8c' }} />
                        {isActive ? t('common.active') : t('common.disabled')}
                      </span>
                    );
                  })()}
                >
                  <CardRow label="转发 (EID)"><Tag color="blue">{record.eid || '-'}</Tag></CardRow>
                  <CardRow label="来源类型">{record.is_system === 1 ? <Tag color="blue">系统内置</Tag> : <Tag color="default">自定义</Tag>}</CardRow>
                  <CardRow label="模式"><Tag color="purple">{record.rule_type}</Tag></CardRow>
                  <CardRow label="分类"><Tag color={categoryColor}>{record.category || '聊天'}</Tag></CardRow>
                  <CardRow label="排序"><Text>{record.sort_order || 0}</Text></CardRow>
                  {record.description && <CardRow label="描述"><Text type="secondary" style={{ fontSize: 12 }}>{record.description}</Text></CardRow>}
                  <CardActions>
                    <Button size="small" type="dashed" icon={<CodeOutlined />} onClick={() => viewConfigJson(record.config_json)}>JSON</Button>
                    <Button size="small" icon={<EditOutlined />} onClick={() => handleEdit(record)} />
                    <Tooltip title={t('common.copy', '复制规则')}>
                      <Button
                        size="small"
                        icon={<CopyOutlined />}
                        onClick={() => handleCopy(record)}
                        loading={copyingId === record.id}
                      />
                    </Tooltip>
                    {record.is_system === 1 ? (
                      <Tooltip title="系统内置规则，不可删除">
                        <Button size="small" icon={<DeleteOutlined />} disabled />
                      </Tooltip>
                    ) : (
                      <Popconfirm title={t('common.confirm_delete')} onConfirm={() => handleDelete(record.id)}>
                        <Button size="small" icon={<DeleteOutlined />} danger />
                      </Popconfirm>
                    )}
                  </CardActions>
                </MobileCard>
              );
            }}
          />
        ) : (
          <Table
            dataSource={filteredItems}
            columns={columns}
            rowKey="id"
            loading={loading}
            pagination={paginationConfig}
            size="middle"
            scroll={{ x: 1300 }}
          />
        )}
      </Card>

      <Modal
        title={editingItem ? '编辑高级规则' : '新增规则引擎接入'}
        open={isEditorOpen}
        onCancel={handleCloseEditor}
        onOk={() => form.submit()}
        width={860}
      >
        <Form form={form} layout="vertical" onFinish={handleSave}>
          <Form.Item name="name" label={'规则标识名称 (例如: Anthropic 原生转换)'} rules={[{ required: true }]}>
            <Input placeholder="输入此规则的标记名称以便于检索" />
          </Form.Item>

          <Form.Item name="rule_type" label="映射厂商及模式 (类型标识)" rules={[{ required: true }]}>
            <Input placeholder="如: openai, anthropic, gemini, kling, tencent, minimax, fal, passthrough" />
          </Form.Item>

          <Form.Item name="category" label="模型分类属类" rules={[{ required: true }]} initialValue="视频">
            <Radio.Group options={uniqueCategories} />
          </Form.Item>

          <Form.Item name="description" label="详细阐述">
            <Input.TextArea placeholder="用以描述该规则专门为了对接什么样的通道代理结构" rows={2} />
          </Form.Item>

          <Form.Item name="sort_order" label="排序" initialValue={0} tooltip="数字越大排在越前面">
            <InputNumber min={0} style={{ width: '100%' }} placeholder="请输入排序值（数字越大越靠前）" />
          </Form.Item>

          <Form.Item name="poll_path" label={<Space>异步任务轮询路径 (可选) <Popover content={`如果该模型是异步任务且上游查询路径非标准，请在此填写。例如：/v1/tasks/\${task_id}`}><QuestionCircleOutlined /></Popover></Space>}>
            <Input placeholder={`例如: /v1/tasks/\${task_id} 或 /v1/video/generations/\${task_id}`} />
          </Form.Item>

          {isVideoCategory && (
            <>
              <Form.Item
                name="fast_response"
            label={
              <Space>
                优先快返
                <Popover content="开启后，视频等异步任务请求将优先快返 200 已受理状态及统一平台任务号（client_task_id），随后在后台独立执行素材转换与上游提交，客户端可立即开始轮询结果；未开启则保持同步等待上游响应后返回。">
                  <QuestionCircleOutlined />
                </Popover>
              </Space>
            }
            valuePropName="checked"
            normalize={(v) => v === true || v === 1}
          >
            <Switch />
          </Form.Item>

          {(hasVolcEnhance || hasTencentEnhance) && (
          <>
          <Form.Item
            name="is_cascade"
            label={<Space>级联封装 <Popover content="二阶段级联；阶段二封装不计费。成功后：有 usage 则 tokens×res_mul，否则底座费用×倍率。阶段二缺省火山 MediaKit，可选腾讯云封装。"><QuestionCircleOutlined /></Popover></Space>}
            valuePropName="checked"
            normalize={(v) => v === true || v === 1}
          >
            <Switch
              onChange={(checked) => {
                if (checked) {
                  form.setFieldValue('fast_response', true);
                }
              }}
            />
          </Form.Item>

          <Form.Item
            noStyle
            shouldUpdate={(prev, cur) =>
              prev.is_cascade !== cur.is_cascade ||
              prev.cascade_engine !== cur.cascade_engine ||
              prev.crop_480p !== cur.crop_480p ||
              prev.crop_timing !== cur.crop_timing ||
              prev.crop_16_9 !== cur.crop_16_9 ||
              prev.crop_9_16 !== cur.crop_9_16 ||
              activeResKeys.some((k) => prev?.res_enhance?.[k] !== cur?.res_enhance?.[k]) ||
              ['480p', '540p'].some((k) => prev?.res_enhance_enabled?.[k] !== cur?.res_enhance_enabled?.[k])
            }
          >
            {({ getFieldValue }) => {
              if (!getFieldValue('is_cascade')) return null;
              const isTencent = hasTencentEnhance && (getFieldValue('cascade_engine') === 'tencent' || !hasVolcEnhance);
              const availableToAdd = RES_MUL_KEYS.filter((k) => !activeResKeys.includes(k));
              return (
                <>
                  <div style={sectionDividerStyle}>
                    <Form.Item
                      name="cascade_engine"
                      label={<Space>阶段二 <Popover content="只显示已启用的封装插件。缺省火山 MediaKit，与现网规则一致。选腾讯云封装则阶段二走媒体处理(MPS)，裁剪也走媒体处理转码，不依赖火山。"><QuestionCircleOutlined /></Popover></Space>}
                      initialValue={hasVolcEnhance ? 'volc' : 'tencent'}
                    >
                      <Radio.Group
                        buttonStyle="solid"
                        onChange={(e) => syncCrop(e.target.value, undefined)}
                      >
                        {hasVolcEnhance && <Radio.Button value="volc">火山 MediaKit</Radio.Button>}
                        {hasTencentEnhance && <Radio.Button value="tencent">腾讯云封装</Radio.Button>}
                      </Radio.Group>
                    </Form.Item>
                    <Form.Item
                      label={<Space>阶段二超时重提 <Popover content="当阶段二封装任务持续卡在运行中达到该时长，系统将自动废弃当前阶段二任务并重新提交新的阶段二 POST 请求（最多自动重提 3 次，默认 20 分钟）。"><QuestionCircleOutlined /></Popover></Space>}
                      style={{ marginBottom: 12 }}
                    >
                      <Space align="center" size="small">
                        <Form.Item name="s2_timeout_mins" noStyle initialValue={20}>
                          <InputNumber min={1} max={180} precision={0} placeholder="默认 20" style={{ width: 110 }} />
                        </Form.Item>
                        <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>分钟（超时仍卡在运行中则废弃旧任务并重新提交阶段二）</span>
                      </Space>
                    </Form.Item>
                    <Alert
                      type="info"
                      showIcon
                      style={{ marginBottom: 0 }}
                      message={
                        isTencent
                          ? '阶段二走腾讯云媒体处理(MPS)，裁剪与抽尾帧也走媒体处理。'
                          : '阶段二走火山 MediaKit，请确保封装模型与渠道已配置。'
                      }
                    />
                  </div>
                  <div style={sectionDividerStyle}>
                    <Form.Item
                      name="crop_480p"
                      label={<Space>720p←480 裁剪 <Popover content={<div style={{ maxWidth: 320 }}>仅目标 720p 且底座 480p 时生效。开：按时机裁到标准画布；关：原片直接封装。其它分辨率不影响。缺省开。火山 MediaKit，腾讯媒体处理转码。</div>}><QuestionCircleOutlined /></Popover></Space>}
                      valuePropName="checked"
                      initialValue={true}
                    >
                      <Switch />
                    </Form.Item>
                    {getFieldValue('crop_480p') !== false && (
                      <>
                        <Form.Item
                          name="crop_timing"
                          label={<Space>裁剪时机 <Popover content={<div style={{ maxWidth: 320 }}>仅 16:9 / 9:16。<br/><b>封装前 (480p)</b>：先裁成标准 480p 再封装（如 Seedance 2.0）。<br/><b>封装后 (720p)</b>：封装完再裁成标准 720p（如 Seedance 2.5 多 2 像素）。缺省封装前。火山与腾讯共用。</div>}><QuestionCircleOutlined /></Popover></Space>}
                          initialValue="pre"
                        >
                          <Radio.Group
                            buttonStyle="solid"
                            onChange={(e) => syncCrop(undefined, e.target.value)}
                          >
                            <Radio.Button value="pre">封装前 (480p)</Radio.Button>
                            <Radio.Button value="post">封装后 (720p)</Radio.Button>
                          </Radio.Group>
                        </Form.Item>
                        <Form.Item
                          label={<Space>裁剪坐标 (X1, Y1, X2, Y2) <Popover content={<div style={{ maxWidth: 320 }}>左上X, 左上Y, 右下X, 右下Y。火山按此矩形抠图；腾讯按此算出画布并复用插件里同比例模板。<br/><b>火山封装前默认</b>：16:9 <code>2, 6, 862, 490</code>；9:16 <code>6, 2, 490, 862</code>。<br/><b>腾讯封装前默认</b>：16:9 <code>0, 5, 864, 491</code>；9:16 <code>5, 0, 491, 864</code>。<br/>封装后默认：16:9 <code>1, 0, 1281, 720</code>；9:16 <code>0, 1, 720, 1281</code>。</div>}><QuestionCircleOutlined /></Popover></Space>}
                          style={{ marginBottom: 0 }}
                        >
                          <Space direction="vertical" style={{ width: '100%' }}>
                            {(() => {
                              const renderCropStatus = (ratio: '16:9' | '9:16') => {
                                if (!isTencent) return null;
                                const timing = getFieldValue('crop_timing') || 'pre';
                                const val = getFieldValue(ratio === '16:9' ? 'crop_16_9' : 'crop_9_16');
                                const key = cropCanvasKey(val || cropDefault('tencent', timing, ratio));
                                const defId = key ? tencentCropTemplates[key] : null;
                                return defId ? (
                                  <Tag color="success">{key} 模板已建 (#{defId})</Tag>
                                ) : (
                                  <Space size={4}>
                                    <Text type="secondary" style={{ fontSize: 12 }}>{key || '当前规格'}未建模板</Text>
                                    <Button
                                      size="small"
                                      type="link"
                                      href={`/${adminPath}/plugins/tencent_enhance/config#te_config`}
                                      target="_blank"
                                      rel="noopener noreferrer"
                                      style={{ padding: 0, fontSize: 12 }}
                                    >
                                      去插件中心创建
                                    </Button>
                                  </Space>
                                );
                              };
                              return (
                                <>
                                  <Space align="center" wrap>
                                    <Text style={{ width: 44, color: '#888' }}>16:9</Text>
                                    <Form.Item name="crop_16_9" noStyle>
                                      <Input style={{ width: 220 }} placeholder={cropDefault(getFieldValue('cascade_engine'), getFieldValue('crop_timing') || 'pre', '16:9')} />
                                    </Form.Item>
                                    {renderCropStatus('16:9')}
                                  </Space>
                                  <Space align="center" wrap>
                                    <Text style={{ width: 44, color: '#888' }}>9:16</Text>
                                    <Form.Item name="crop_9_16" noStyle>
                                      <Input style={{ width: 220 }} placeholder={cropDefault(getFieldValue('cascade_engine'), getFieldValue('crop_timing') || 'pre', '9:16')} />
                                    </Form.Item>
                                    {renderCropStatus('9:16')}
                                  </Space>
                                </>
                              );
                            })()}
                            <Button
                              size="small"
                              type="link"
                              style={{ padding: 0, fontSize: 12, height: 'auto', alignSelf: 'flex-start' }}
                              onClick={() => syncCrop()}
                            >
                              恢复当前引擎默认坐标
                            </Button>
                          </Space>
                        </Form.Item>
                      </>
                    )}
                  </div>
                  <div style={sectionDividerStyle}>
                    <Form.Item
                      label={
                        <Space size="middle" style={{ width: '100%', justifyContent: 'space-between' }}>
                          <Space>
                            比例短边限制
                            <Popover
                              content={
                                <div style={{ maxWidth: 380 }}>
                                  <div>指定各成片比例在不同分辨率下的目标短边像素（如 1:1 在 720p 为 960，避免画面缩水）。</div>
                                  <div style={{ marginTop: 4 }}>
                                    <b>启用/关闭规则：</b>输入像素即<b>启用</b>该比例短边限制；输入框留空（显示关闭）即<b>不使用短边限制</b>，直接按封装版本原生标准分辨率输出（大模型不回退）。
                                  </div>
                                  <div style={{ marginTop: 4 }}>
                                    <b>注：</b>16:9 与 9:16 的 720p 由上方裁剪规则托管，无需配置短边限制。
                                  </div>
                                  <div style={{ marginTop: 6, paddingTop: 6, borderTop: '1px dashed var(--border-color, #e8e8e8)' }}>
                                    官方像素对照：
                                    <a
                                      href="https://console.volcengine.com/ark/region:cn-beijing/tokenCalculator"
                                      target="_blank"
                                      rel="noopener noreferrer"
                                    >
                                      火山方舟 Token 计算器
                                    </a>
                                  </div>
                                </div>
                              }
                            >
                              <QuestionCircleOutlined />
                            </Popover>
                          </Space>
                          <Space size="small">
                            <Button type="link" size="small" style={{ padding: '0 4px', fontSize: 12 }} onClick={handleApplyAllPresets}>
                              恢复推荐预设
                            </Button>
                          </Space>
                        </Space>
                      }
                      style={{ marginBottom: 0 }}
                    >
                      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                        {activeRatios.map((ratio) => (
                          <Space key={ratio} wrap size="middle" align="center" style={{ padding: '2px 0' }}>
                            <Tag color="cyan" style={{ fontSize: 13, padding: '2px 8px', fontWeight: 600, minWidth: 46, textAlign: 'center' }}>
                              {ratio}
                            </Tag>
                            {activeResKeys.map((res) => {
                              const isCropManaged = isCropManagedRatioRes(ratio, res);
                              const presetVal = getPresetLimit(ratio, res);
                              return (
                                <Form.Item
                                  key={res}
                                  name={['res_limit', `${res}@${ratio}`]}
                                  label={<span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{res}</span>}
                                  style={{ marginBottom: 0 }}
                                >
                                  <InputNumber
                                    min={128}
                                    max={4320}
                                    step={10}
                                    disabled={isCropManaged}
                                    placeholder={isCropManaged ? '裁剪托管' : (presetVal ? `关闭(${presetVal})` : '关闭')}
                                    style={{ width: 90 }}
                                  />
                                </Form.Item>
                              );
                            })}
                            <Tooltip title={`移除 ${ratio} 比例配置`}>
                              <Button
                                type="text"
                                danger
                                size="small"
                                icon={<DeleteOutlined />}
                                onClick={() => handleRemoveRatio(ratio)}
                              />
                            </Tooltip>
                          </Space>
                        ))}
                        {(() => {
                          const availableRatios = DEFAULT_CASCADE_RATIOS.filter((r) => !activeRatios.includes(r));
                          return (
                            <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', marginTop: 6 }}>
                              {availableRatios.length > 0 && (
                                <>
                                  <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>快捷添加比例:</span>
                                  {availableRatios.map((r) => (
                                    <Tag
                                      key={r}
                                      style={{ cursor: 'pointer', margin: 0, fontSize: 12 }}
                                      onClick={() => handleAddRatio(r)}
                                    >
                                      +{r}
                                    </Tag>
                                  ))}
                                </>
                              )}
                          <Input
                            size="small"
                            placeholder="+ 自定义比例回车 (如 2:3)"
                            style={{ width: 160, height: 24, fontSize: 12 }}
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
                                handleAddRatio(val);
                                inputEl.value = '';
                              }
                            }}
                          />
                        </div>
                      );
                    })()}
                      </div>
                    </Form.Item>
                    {!isTencent && (
                      <div style={{ marginTop: 12, paddingTop: 10, borderTop: '1px dashed var(--border-color, #f0f0f0)' }}>
                        <Form.Item
                          name="res_limit_fallback"
                          label="大模型短边回退档位"
                          initialValue="pro"
                          style={{ marginBottom: 6 }}
                        >
                          <Select
                            style={{ width: 340 }}
                            options={[
                              { value: 'pro', label: '专业版 (pro) [推荐 - 画质最高 / 短边上限 4320]' },
                              { value: 'standard', label: '标准版 (standard) [均衡性价比 / 短边上限 4320]' },
                              { value: 'fast', label: '极速版 (fast) [耗时最短 / 短边上限 2160]' },
                            ]}
                          />
                        </Form.Item>
                        <Alert
                          type="info"
                          showIcon
                          style={{ fontSize: 12, padding: '4px 10px' }}
                          message="规则说明：大模型封装（vve-gt）按火山官方规范仅支持固定预设档位，不支持短边像素限制。遇短边限制时，系统在发起时自动决断并切换为所选档位（极速版官方短边上限为 2160，超出将平滑约束至 2160；专业/标准版上限为 4320），阶段二直接消费快照，0 额外查库。"
                        />
                      </div>
                    )}
                  </div>
                  <div style={sectionDividerStyle}>
                    <Form.Item
                      label={<Space>级联分辨率配置 <Popover content={<div style={{ maxWidth: 320 }}>每档可设：倍率、封装（默认标准；大模型支持 720p/1080p/2k/4k）、场景（仅标准版，默认 common）、底座（默认一级；480p 锁定 480p，720p 可选 480p/720p，1080p 可选 720p/480p/1080p；2k/4k 不变）。阶段二成功：有 usage 时 tokens×倍率，否则底座费用×倍率。</div>}><QuestionCircleOutlined /></Popover></Space>}
                      style={{ marginBottom: 0 }}
                    >
                      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                        {activeResKeys.map((k) => {
                          const hasEnhanceToggle = k === '480p' || k === '540p';
                          const enhanceEnabled = hasEnhanceToggle
                            ? getFieldValue(['res_enhance_enabled', k]) !== false
                            : true;
                          return (
                            <Space key={k} wrap size="middle" align="start">
                              <Text strong style={{ width: 48, display: 'inline-block', lineHeight: '32px' }}>{k}</Text>
                              <Form.Item name={['res_mul', k]} label="倍率" style={{ marginBottom: 0 }} rules={[{ required: true }]}>
                                <InputNumber min={0.001} step={0.1} precision={3} style={{ width: 88 }} />
                              </Form.Item>
                              {!isTencent && (
                                <>
                                  <Form.Item name={['res_enhance', k]} label="封装" style={{ marginBottom: 0 }}>
                                    <Select style={{ width: 100 }} options={enhanceSelectOpts(k)} disabled={!enhanceEnabled} />
                                  </Form.Item>
                                  {getFieldValue(['res_enhance', k]) === 'standard' && enhanceEnabled ? (
                                    <Form.Item name={['res_scene', k]} label="场景" style={{ marginBottom: 0 }}>
                                      <Select style={{ width: 128 }} options={SCENE_SELECT} />
                                    </Form.Item>
                                  ) : null}
                                </>
                              )}
                              <Form.Item name={['res_base', k]} label="底座" style={{ marginBottom: 0 }} rules={[{ required: true }]}>
                                <Select
                                  style={{ width: 100 }}
                                  disabled={getBaseOptions(k).length <= 1}
                                  options={getBaseOptions(k).map((b) => ({ value: b, label: b }))}
                                />
                              </Form.Item>
                              {hasEnhanceToggle && (
                                <Form.Item
                                  name={['res_enhance_enabled', k]}
                                  label="封装开关"
                                  valuePropName="checked"
                                  style={{ marginBottom: 0 }}
                                  tooltip="默认开启；关闭后出片不走二次封装直接交付，但最终消耗仍按上方倍率计算"
                                >
                                  <Switch checkedChildren="开" unCheckedChildren="关" />
                                </Form.Item>
                              )}
                              <Tooltip title="移除此分辨率配置">
                                <Button
                                  type="text"
                                  danger
                                  size="small"
                                  icon={<DeleteOutlined />}
                                  style={{ lineHeight: '32px', height: 32, padding: '0 4px' }}
                                  onClick={() => handleRemoveResKey(k)}
                                />
                              </Tooltip>
                            </Space>
                          );
                        })}
                        <div style={{
                          display: 'flex',
                          alignItems: 'center',
                          gap: 6,
                          flexWrap: 'wrap',
                          marginTop: 6,
                        }}>
                          {availableToAdd.length > 0 && (
                            <>
                              <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>快捷添加:</span>
                              {availableToAdd.map((k) => (
                                <Tag
                                  key={k}
                                  style={{ cursor: 'pointer', margin: 0, fontSize: 12 }}
                                  onClick={() => handleAddResKey(k)}
                                >
                                  +{k}
                                </Tag>
                              ))}
                            </>
                          )}
                          <Input
                            size="small"
                            placeholder="+ 自定义档位回车"
                            style={{ width: 140, height: 24, fontSize: 12 }}
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
                                handleAddResKey(val);
                                inputEl.value = '';
                              }
                            }}
                          />
                        </div>
                      </div>
                    </Form.Item>
                  </div>
                </>
              );
            }}
          </Form.Item>
          </>
          )}
            </>
          )}

          {isImageCategory && (
            <>
              <Form.Item
                name="image_options"
                label={
                  <Space>
                    图片模型规格设置
                    <Popover content="开启后，可对该规则转发的图片模型进行分辨率、画质白名单约束及参考图数量限制；未开启则默认不限制，透传所有请求。">
                      <QuestionCircleOutlined />
                    </Popover>
                  </Space>
                }
                valuePropName="checked"
              >
                <Switch />
              </Form.Item>

              <Form.Item
                noStyle
                shouldUpdate={(prev, cur) =>
                  prev.image_options !== cur.image_options ||
                  prev.allowed_resolutions !== cur.allowed_resolutions ||
                  prev.allowed_qualities !== cur.allowed_qualities ||
                  prev.min_pixels !== cur.min_pixels ||
                  prev.max_pixels !== cur.max_pixels ||
                  prev.auto_downgrade !== cur.auto_downgrade ||
                  prev.step !== cur.step ||
                  prev.max_aspect_ratio !== cur.max_aspect_ratio
                }
              >
                {({ getFieldValue }) => {
                  if (!getFieldValue('image_options')) return null;
                  const curResolutions = (getFieldValue('allowed_resolutions') as string[] | undefined) || [];
                  const curQualities = (getFieldValue('allowed_qualities') as string[] | undefined) || [];
                  return (
                    <div style={sectionDividerStyle}>
                      <div style={{ marginBottom: 14, display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                        <span style={{ fontSize: 13, fontWeight: 500, color: 'var(--text-primary)' }}>规格快捷模版:</span>
                        <Tag
                          color="blue"
                          style={{ cursor: 'pointer', padding: '2px 10px', borderRadius: 4, fontSize: 12, margin: 0 }}
                          onClick={() => {
                            form.setFieldsValue({
                              allowed_resolutions: ['1024x1024'],
                              allowed_qualities: ['low'],
                              min_pixels: 0.5,
                              max_pixels: 1.6,
                              auto_downgrade: true,
                              step: 16,
                              max_aspect_ratio: 3,
                              max_ref_images: 5,
                            });
                          }}
                        >
                          ⚡ 一键套用 1K 标准模版 (1024x1024 / low / 限5张参考图 / 开启超限自动降级)
                        </Tag>
                      </div>

                      <TagBatchField
                        label={
                          <Space>
                            支持的分辨率 / 画幅
                            <Popover content={<div style={{ maxWidth: 320 }}>同时兼容 size 与 resolution 字段。<br/>• <b>白名单匹配</b>：支持 1k/2k/4k 档位与 1024x1024 等任意分辨率，请求不在列表中则直接抛错拦截；<br/>• <b>未设置</b>：默认不限制，放行所有分辨率。</div>}>
                              <QuestionCircleOutlined />
                            </Popover>
                          </Space>
                        }
                        tooltip="点击快捷标签快速添加，或输入自定义档位回车添加；点击标签上的 × 即可快速移除"
                        placeholder="未设置，默认不限制（放行所有分辨率）"
                        inputPlaceholder="+ 自定义档位回车"
                        tagColor="blue"
                        values={curResolutions}
                        presets={PRESET_IMAGE_RESOLUTIONS}
                        onAdd={(val) => handleUpdateTag('allowed_resolutions', 'add', val)}
                        onRemove={(val) => handleUpdateTag('allowed_resolutions', 'remove', val)}
                      />

                      <Form.Item
                        label={
                          <Space>
                            总像素取值范围
                            <Popover content={<div style={{ maxWidth: 320 }}>对生图宽高乘积（W × H）的百万像素范围限制。<br/>• <b>按档位一键设定</b>：点击上方 1K/2K/4K 快捷标签，自动填充官方标准上下限；<br/>• <b>自动放行各比例</b>：只要请求的宽高像素落在区间内，16:9、9:16、4:3、1:1 等任意画幅直接放行；<br/>• <b>留空</b>：默认不限制总像素。</div>}>
                              <QuestionCircleOutlined />
                            </Popover>
                          </Space>
                        }
                        tooltip="点击下方快捷档位可一键填入官方标准像素范围（单位：百万像素），自动放行该档位下的所有横竖画幅比例"
                      >
                        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                          <Space wrap size={[6, 6]}>
                            <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>快捷档位:</span>
                            <Tag
                              style={{ cursor: 'pointer', margin: 0, fontSize: 12 }}
                              color="blue"
                              onClick={() => {
                                form.setFieldsValue({ min_pixels: 0.5, max_pixels: 1.6 });
                              }}
                            >
                              1k 范围
                            </Tag>
                            <Tag
                              style={{ cursor: 'pointer', margin: 0, fontSize: 12 }}
                              color="geekblue"
                              onClick={() => {
                                form.setFieldsValue({ min_pixels: 0.5, max_pixels: 4.62 });
                              }}
                            >
                              2k 范围
                            </Tag>
                            <Tag
                              style={{ cursor: 'pointer', margin: 0, fontSize: 12 }}
                              color="purple"
                              onClick={() => {
                                form.setFieldsValue({ min_pixels: 0.5, max_pixels: 8.5 });
                              }}
                            >
                              4k 范围
                            </Tag>
                            <Tag
                              style={{ cursor: 'pointer', margin: 0, fontSize: 12 }}
                              onClick={() => {
                                form.setFieldsValue({ min_pixels: undefined, max_pixels: undefined });
                              }}
                            >
                              清空限制
                            </Tag>
                          </Space>

                          <Space align="center" size="small">
                            <Form.Item name="min_pixels" noStyle>
                              <InputNumber
                                min={0.01}
                                max={100}
                                step={0.1}
                                placeholder="最小 (如 0.5)"
                                style={{ width: 140 }}
                              />
                            </Form.Item>
                            <span style={{ color: 'var(--text-secondary)' }}>~</span>
                            <Form.Item name="max_pixels" noStyle>
                              <InputNumber
                                min={0.01}
                                max={100}
                                step={0.1}
                                placeholder="最大 (如 1.6)"
                                style={{ width: 140 }}
                              />
                            </Form.Item>
                            <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>百万像素</span>
                          </Space>

                          <Space align="center" size="middle" style={{ marginTop: 2 }}>
                            <Space align="center" size={4}>
                              <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>步长整除:</span>
                              <Form.Item name="step" noStyle>
                                <InputNumber
                                  min={0}
                                  max={128}
                                  placeholder="默认 16"
                                  style={{ width: 95 }}
                                />
                              </Form.Item>
                              <Popover content="图片宽度和高度必须能被此数值整除。主流扩散模型（GPT / SD / 火山）通用硬性要求为 16；填 0 或留空按默认 16 校验。">
                                <QuestionCircleOutlined style={{ fontSize: 12, color: 'var(--text-secondary)', cursor: 'pointer' }} />
                              </Popover>
                            </Space>

                            <span style={{ color: 'var(--border-color, #d9d9d9)' }}>|</span>

                            <Space align="center" size={4}>
                              <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>最大长宽比:</span>
                              <Form.Item name="max_aspect_ratio" noStyle>
                                <InputNumber
                                  min={1}
                                  max={64}
                                  placeholder="默认 3"
                                  style={{ width: 95 }}
                                />
                              </Form.Item>
                              <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>: 1</span>
                              <Popover content="长边与短边的最大允许倍数。默认 3（即 [1/3, 3]，OpenAI 官方规范）；火山方舟等超宽全景图模型可配置为 16（即 [1/16, 16]）。">
                                <QuestionCircleOutlined style={{ fontSize: 12, color: 'var(--text-secondary)', cursor: 'pointer' }} />
                              </Popover>
                            </Space>
                          </Space>
                        </div>
                      </Form.Item>

                      <Form.Item
                        name="auto_downgrade"
                        valuePropName="checked"
                        label={
                          <Space>
                            超限参数自动降级兜底
                            <Popover content={
                              <div style={{ maxWidth: 320 }}>
                                开启后，请求参数超出规格时不报 400 错误，由网关自动原地降级适配：
                                <br />1. <strong>分辨率</strong>：未传自动按 1024x1024，超限等比例降级至 1K；
                                <br />2. <strong>画质</strong>：传入非白名单画质或未传时，自动兜底修正为允许的画质（如 low）；
                                <br />3. <strong>参考图</strong>：传入图片数超过上限（如 5 张）时，自动截取前 N 张，超出部分丢弃不传上游。
                                <br />未开启时则严格按规则规格返回 400 拦截。
                              </div>
                            }>
                              <QuestionCircleOutlined />
                            </Popover>
                          </Space>
                        }
                      >
                        <Switch />
                      </Form.Item>

                      <TagBatchField
                        label={
                          <Space>
                            支持的画质
                            <Popover content="限制允许传入的 quality 参数（如 low, medium, high, auto 等）。不在列表中的画质将直接抛错拦截；未设置默认不限制。">
                              <QuestionCircleOutlined />
                            </Popover>
                          </Space>
                        }
                        tooltip="点击快捷标签快速添加，或输入自定义画质回车添加；点击标签上的 × 即可快速移除"
                        placeholder="未设置，默认不限制（允许所有画质）"
                        inputPlaceholder="+ 自定义画质回车"
                        tagColor="purple"
                        values={curQualities}
                        presets={PRESET_IMAGE_QUALITIES}
                        onAdd={(val) => handleUpdateTag('allowed_qualities', 'add', val)}
                        onRemove={(val) => handleUpdateTag('allowed_qualities', 'remove', val)}
                      />

                      <Form.Item
                        name="max_ref_images"
                        label={
                          <Space>
                            参考图数量限制
                            <Popover content="设置允许传入的最大参考图片张数。请求提交图片数超出时将在本地直接拦截 400，避免无效下载与成本消耗；留空或 0 为不限制。">
                              <QuestionCircleOutlined />
                            </Popover>
                          </Space>
                        }
                        tooltip="留空或填 0 表示不限制参考图数量"
                      >
                        <InputNumber min={0} max={100} style={{ width: 150 }} placeholder="默认不限制" addonAfter="张" />
                      </Form.Item>
                    </div>
                  );
                }}
              </Form.Item>
            </>
          )}

          <Form.Item name="config_json" label="JSON 引擎路由协议参数配置 (核心)" rules={[{ required: true }]}>
            <TextArea
              style={{ fontFamily: 'monospace', fontSize: 13, background: '#1e1e1e', color: '#d4d4d4', padding: 12 }}
              rows={10}
              placeholder={'{\n  "target_type": "minimax_video",\n  "path_rewrites": [\n    {"old": "/v1/video/generations", "new": "/minimax/v2/video_generation/tpl-3"}\n  ],\n  "auth_type": "bearer",\n  "poll_path": "/minimax/query/${task_id}"\n}'}
            />
          </Form.Item>

          <Form.Item name="is_active" label={t('common.status')} valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title="JSON 重写拦截协议预览"
        open={isConfigModalVisible}
        footer={null}
        onCancel={() => setIsConfigModalVisible(false)}
        width={650}
      >
        <pre style={{
          background: '#121212',
          color: '#4af626',
          padding: 16,
          borderRadius: 8,
          border: '1px solid #333',
          overflow: 'auto',
          maxHeight: '60vh',
          fontSize: 13,
        }}>
          {currentConfig}
        </pre>
      </Modal>

    </>
  );
};

export default ForwardRules;
