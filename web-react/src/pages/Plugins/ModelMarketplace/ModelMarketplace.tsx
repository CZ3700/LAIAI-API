/**
 * 模型广场 - 用户端独立全屏页面
 * 参考 NeuralGrid 设计风格：深色主题 + 左侧筛选 + 模型卡片网格
 */
import React, { useState, useEffect, useMemo, useRef } from 'react';
import { getAnnouncementLabel, getAnnouncementDisplayTime } from '../../../utils/announcement';
import { modelMatchesKeyword } from '../../../utils/modelKeywordMatch';
import {
  parseNotificationPreferences,
  shouldShowWebNotifications,
  maybeShowBrowserPush,
} from '../../../utils/notificationPrefs';
import { Sidebar as SidebarIcon, Terminal } from 'lucide-react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { ConfigProvider, theme, Input, Checkbox, Avatar, Dropdown, Empty, Tooltip, Popover, Button, Layout, Grid, Result, Descriptions, Tag, Breadcrumb, Badge, List, message, Switch } from 'antd';
import {
  RocketOutlined, CompassOutlined, SearchOutlined, ArrowLeftOutlined, AppstoreOutlined,
  MessageOutlined, PictureOutlined, VideoCameraOutlined,
  AudioOutlined, CodeOutlined, ApiOutlined, ShopOutlined,
  FilterOutlined, SortAscendingOutlined, MenuOutlined, CloseOutlined,
  DashboardOutlined, WalletOutlined, LogoutOutlined, MenuUnfoldOutlined, MenuFoldOutlined,
  LockOutlined, InfoCircleOutlined, UnorderedListOutlined, GlobalOutlined, BellOutlined, ScheduleOutlined,
  CopyOutlined, DollarOutlined, SettingOutlined
} from '@ant-design/icons';

const { Header, Sider, Content } = Layout;
const { useBreakpoint } = Grid;
import request from '../../../utils/request';
import { setLobeIconVersions, withLobeIconVersion } from '../../../components/SmartSvgIcon';
import { fetchActivePlugins, peekActivePlugins } from '../../../utils/activePlugins';
import { persistUserLanguagePreference, LANG_NAME_MAP } from '../../../utils/language';
import { useThemeStore } from '../../../store/theme';
import useSettingsStore from '../../../store/settings';
import useAuthStore from '../../../store/auth';
import UserAvatarMenu from '../../../components/UserAvatarMenu';
import { SunOutlined, MoonOutlined, FireOutlined } from '@ant-design/icons';
import { formatApiDateTime, parseApiTimeAsUtc } from '../../../utils/timedisplay';
import { resolveFreeImageCount } from '../../../utils/billingFreeImages';
import {
  formatRate,
  formatWeekdayRange,
  invertRemainderFromExt,
  isFreeTimeMultiplier,
  normalizeWeekdays,
  resolveTimeMultiplierAt,
  type TimeMultiplierRule,
} from '../../../utils/timeMultipliers';
import TrendingPage from './TrendingPage';
import { copyToClipboard } from '../../../utils/clipboard';

interface Announcement {
  id: number;
  title: string;
  content: string;
  is_pinned: number;
  sort_order?: number;
  created_at: string;
  updated_at?: string;
  display_time_mode?: string;
}

interface MarketplaceModel {
  id: number | string;
  mid: string;
  name: string;
  model_id: string;
  original_id?: string;
  provider_id: number | string;
  provider_name: string;
  provider_name_en?: string;
  provider_logo?: string;
  type_id: number | string;
  type_name: string;
  type_name_en?: string;
  type_logo?: string;
  logo?: string;
  sort_order: number;
  description: string;
  description_en?: string;
  model_description?: string;
  global_discount?: number;
  global_discount_enabled?: number | boolean;
  base_global_discount?: number;
  base_site_discount?: number;
  site_discount?: number;
  site_discount_enabled?: number | boolean;
  discount_schedule?: string;
  resolution_discounts?: Record<string, number>;
  billing: any;
  /** 未挂计费模板或模板已停用/删除：调用会被拒绝，展示计费失效 */
  billing_invalid?: boolean;
  created_at: string;
  has_ha?: boolean;
  ha_sub_count?: number;
  variant_count?: number;
  variants?: MarketplaceModel[];
  feature_attributes?: string | string[];
}

interface FilterItem {
  id: number | string;
  name: string;
  name_en?: string;
  logo?: string;
  provider_type?: string;
}

const MP_GRID_COL_MIN = 340;
const MP_GRID_GAP = 12;
const MP_GRID_MAX_COLS = 5;
const MP_GRID_MAX_WIDTH = MP_GRID_MAX_COLS * 420 + (MP_GRID_MAX_COLS - 1) * MP_GRID_GAP;
const MP_GRID_TEMPLATE_COLUMNS = `repeat(auto-fill, minmax(max(${MP_GRID_COL_MIN}px, calc((100% - ${(MP_GRID_MAX_COLS - 1) * MP_GRID_GAP}px) / ${MP_GRID_MAX_COLS})), 1fr))`;
const MARKETPLACE_BATCH = 30;

type MarketplaceQueryPatch = {
  tab?: 'trending' | 'models';
  type?: number | string | null;
  provider?: number | string | null;
  q?: string | null;
  closeModel?: boolean;
};

function marketplaceVariants(model: MarketplaceModel): MarketplaceModel[] {
  return model.variants && model.variants.length > 0 ? model.variants : [model];
}

function marketplaceModelMatchesFilters(
  model: MarketplaceModel,
  typeId: number | string | null,
  providerId: number | string | null,
): boolean {
  return marketplaceVariants(model).some(
    (v) =>
      (typeId === null || String(v.type_id) === String(typeId)) &&
      (providerId === null || String(v.provider_id) === String(providerId)),
  );
}

function firstQueryToken(raw: string | null): string {
  if (!raw) return '';
  return raw.split(',')[0].trim();
}

type MarketplaceLang = 'zh' | 'zh-TW' | 'en' | 'ja' | 'ko' | 'vi' | 'th' | 'ms' | 'fr' | 'de' | 'es' | 'pt' | 'ru' | 'ar';

function marketplaceLang(language?: string): MarketplaceLang {
  const lang = (language || 'zh').toLowerCase();
  if (lang.startsWith('zh-tw') || lang.startsWith('zh-hk') || lang.startsWith('zh-mo') || lang.includes('hant')) return 'zh-TW';
  if (lang.startsWith('zh')) return 'zh';
  if (lang.startsWith('ja')) return 'ja';
  if (lang.startsWith('ko')) return 'ko';
  if (lang.startsWith('vi')) return 'vi';
  if (lang.startsWith('th')) return 'th';
  if (lang.startsWith('ms')) return 'ms';
  if (lang.startsWith('fr')) return 'fr';
  if (lang.startsWith('de')) return 'de';
  if (lang.startsWith('es')) return 'es';
  if (lang.startsWith('pt')) return 'pt';
  if (lang.startsWith('ru')) return 'ru';
  if (lang.startsWith('ar')) return 'ar';
  return 'en';
}

const WEEKDAY_LABELS: Record<MarketplaceLang, string[]> = {
  zh: ['周一', '周二', '周三', '周四', '周五', '周六', '周日'],
  'zh-TW': ['週一', '週二', '週三', '週四', '週五', '週六', '週日'],
  en: ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'],
  ja: ['月', '火', '水', '木', '金', '土', '日'],
  ko: ['월', '화', '수', '목', '금', '토', '일'],
  vi: ['T2', 'T3', 'T4', 'T5', 'T6', 'T7', 'CN'],
  th: ['จ.', 'อ.', 'พ.', 'พฤ.', 'ศ.', 'ส.', 'อา.'],
  ms: ['Isn', 'Sel', 'Rab', 'Kha', 'Jum', 'Sab', 'Ahd'],
  fr: ['Lun', 'Mar', 'Mer', 'Jeu', 'Ven', 'Sam', 'Dim'],
  de: ['Mo', 'Di', 'Mi', 'Do', 'Fr', 'Sa', 'So'],
  es: ['Lun', 'Mar', 'Mié', 'Jue', 'Vie', 'Sáb', 'Dom'],
  pt: ['Seg', 'Ter', 'Qua', 'Qui', 'Sex', 'Sáb', 'Dom'],
  ru: ['Пн', 'Вт', 'Ср', 'Чт', 'Пт', 'Сб', 'Вс'],
  ar: ['إثنين', 'ثلاثاء', 'أربعاء', 'خميس', 'جمعة', 'سبت', 'أحد'],
};

const ALL_DAY_LABEL: Record<MarketplaceLang, string> = {
  zh: '每天',
  'zh-TW': '每天',
  en: 'Every day',
  ja: '毎日',
  ko: '매일',
  vi: 'Mỗi ngày',
  th: 'ทุกวัน',
  ms: 'Setiap hari',
  fr: 'Tous les jours',
  de: 'Jeden Tag',
  es: 'Todos los días',
  pt: 'Todos os dias',
  ru: 'Каждый день',
  ar: 'كل يوم',
};

function marketplaceWeekdayLabels(lang: string): string[] {
  return WEEKDAY_LABELS[marketplaceLang(lang)];
}

function marketplaceMultiplierMeta(tm: any, invert: boolean, lang: string, remainder?: number) {
  const labels = marketplaceWeekdayLabels(lang);
  const allLabel = ALL_DAY_LABEL[marketplaceLang(lang)];
  const daysText = formatWeekdayRange(tm.days, labels, allLabel);
  const ratio = Number(tm.multiplier);
  const allDay = !!tm.all_day;
  const remOk = remainder != null && Number.isFinite(remainder) && remainder >= 0;
  const selected = (!allDay && invert && !remOk) ? 1 : ratio;
  const rest = !allDay && invert
    ? (remOk ? remainder as number : (ratio > 0 ? 1 / ratio : 1))
    : 1;
  return { daysText, selected, rest, invert: !allDay && invert, allDay, rawMultiplier: ratio };
}

function parseClockMin(v: unknown): number | null {
  if (v == null || v === '') return null;
  const m = /^(\d{1,2}):(\d{2})/.exec(String(v).trim());
  if (!m) return null;
  return Number(m[1]) * 60 + Number(m[2]);
}

function findActiveTimeRuleIndex(items: TimeMultiplierRule[], weekday: number, minutes: number): number {
  const prevDay = weekday === 1 ? 7 : weekday - 1;
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    if (!item || item.all_day) continue;
    const start = parseClockMin(item.start);
    const end = parseClockMin(item.end);
    if (start == null || end == null || start === end) continue;
    const days = normalizeWeekdays(item.days);
    if (start > end) {
      if (minutes >= start && days.includes(weekday)) return i;
      if (minutes < end && days.includes(prevDay)) return i;
    } else if (minutes >= start && minutes < end && days.includes(weekday)) {
      return i;
    }
  }
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    if (!item || !item.all_day) continue;
    if (normalizeWeekdays(item.days).includes(weekday)) return i;
  }
  return -1;
}

function getSiteClockParts(tz: string): { weekday: number; minutes: number } {
  const mapWeekday = (shortDay: string): number => {
    const k = shortDay.slice(0, 3).toLowerCase();
    if (k === 'mon') return 1;
    if (k === 'tue') return 2;
    if (k === 'wed') return 3;
    if (k === 'thu') return 4;
    if (k === 'fri') return 5;
    if (k === 'sat') return 6;
    if (k === 'sun') return 7;
    return 1;
  };
  try {
    const parts = new Intl.DateTimeFormat('en-US', {
      timeZone: tz,
      weekday: 'short',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    }).formatToParts(new Date());
    const wdStr = parts.find((p) => p.type === 'weekday')?.value || 'Mon';
    const hr = Number(parts.find((p) => p.type === 'hour')?.value || 0) % 24;
    const mn = Number(parts.find((p) => p.type === 'minute')?.value || 0);
    return {
      weekday: mapWeekday(wdStr),
      minutes: (Number.isFinite(hr) ? hr : 0) * 60 + (Number.isFinite(mn) ? mn : 0),
    };
  } catch {
    const now = new Date();
    const d = now.getDay();
    return {
      weekday: d === 0 ? 7 : d,
      minutes: now.getHours() * 60 + now.getMinutes(),
    };
  }
}

type ParsedMarketplaceDiscountSlot = {
  all_day: boolean;
  start: string;
  end: string;
  days: number[];
  daysText: string;
  global_discount: number;
  site_discount: number;
};

function parseVariantDiscountSchedule(variant: any, lang: string): {
  enabled: boolean;
  slots: ParsedMarketplaceDiscountSlot[];
  baseGlobalDiscount: number;
  hasUnplannedPeriod: boolean;
} {
  const globalEnabled = Number(variant?.global_discount_enabled) === 1 || variant?.global_discount_enabled === true;
  const rawBase = variant?.base_global_discount != null
    ? Number(variant.base_global_discount)
    : (variant?.global_discount != null ? Number(variant.global_discount) : 1);
  const baseGlobalDiscount = Number.isFinite(rawBase) && rawBase >= 0 ? rawBase : 1;
  if (!globalEnabled || !variant?.discount_schedule) {
    return { enabled: false, slots: [], baseGlobalDiscount, hasUnplannedPeriod: false };
  }
  const parsed = safeParseJson(variant.discount_schedule, null);
  if (!parsed || !parsed.enabled || !Array.isArray(parsed.slots)) {
    return { enabled: false, slots: [], baseGlobalDiscount, hasUnplannedPeriod: false };
  }
  const labels = marketplaceWeekdayLabels(lang);
  const allLabel = ALL_DAY_LABEL[marketplaceLang(lang)];
  const slots: ParsedMarketplaceDiscountSlot[] = parsed.slots
    .map((s: any) => {
      const days = normalizeWeekdays(s?.days);
      return {
        all_day: !!s?.all_day,
        start: String(s?.start || '').trim(),
        end: String(s?.end || '').trim(),
        days,
        daysText: formatWeekdayRange(days, labels, allLabel),
        global_discount: Number.isFinite(Number(s?.global_discount)) && Number(s?.global_discount) >= 0
          ? Number(s.global_discount)
          : baseGlobalDiscount,
        site_discount: Number.isFinite(Number(s?.site_discount)) && Number(s?.site_discount) >= 0
          ? Number(s.site_discount)
          : 1,
      };
    })
    .filter((s: ParsedMarketplaceDiscountSlot) => s.all_day || (s.start && s.end));
  if (slots.length === 0) {
    return { enabled: false, slots: [], baseGlobalDiscount, hasUnplannedPeriod: false };
  }
  const coveredAllDayDays = new Set<number>();
  slots.forEach((s) => {
    if (s.all_day) s.days.forEach((d) => coveredAllDayDays.add(d));
  });
  return {
    enabled: true,
    slots,
    baseGlobalDiscount,
    hasUnplannedPeriod: coveredAllDayDays.size < 7,
  };
}

function resolveFilterId(
  raw: string | null,
  items: FilterItem[],
  extraMatch?: (item: FilterItem, token: string) => boolean,
): number | string | null {
  const token = firstQueryToken(raw);
  if (!token) return null;
  const lower = token.toLowerCase();
  const found = items.find(
    (item) =>
      String(item.id) === token ||
      item.name.toLowerCase() === lower ||
      extraMatch?.(item, lower) === true,
  );
  if (found) return found.id;
  if (/^\d+$/.test(token)) {
    return Number.isSafeInteger(Number(token)) ? Number(token) : token;
  }
  return null;
}

import { Image as ImageIcon, Video, AudioLines, MessageSquare, Cuboid, ListOrdered, Code, LayoutGrid, Sparkles, Clock, Info, Store } from 'lucide-react';

// 类型图标映射
const getTypeIcon = (typeName: string) => {
  const style = { width: '1em', height: '1em' };
  const normalized = typeName.toLowerCase();
  if (typeName.includes('封装') || normalized.includes('encapsulation')) return <Sparkles style={style} />;
  if (typeName.includes('聊天') || typeName.includes('对话') || normalized.includes('chat') || normalized.includes('llm')) return <MessageSquare style={style} />;
  if (typeName.includes('图片') || typeName.includes('图像') || normalized.includes('image')) return <ImageIcon style={style} />;
  if (typeName.includes('视频') || normalized.includes('video')) return <Video style={style} />;
  if (typeName.includes('音频') || typeName.includes('语音') || normalized.includes('audio') || normalized.includes('speech')) return <AudioLines style={style} />;
  if (typeName.includes('代码') || normalized.includes('code')) return <Code style={style} />;
  if (typeName.includes('嵌入') || typeName.includes('向量') || normalized.includes('embedding') || normalized.includes('vector')) return <Cuboid style={style} />;
  if (typeName.includes('排序') || typeName.includes('重排') || normalized.includes('rerank')) return <ListOrdered style={style} />;
  if (typeName.includes('服务商') || typeName.includes('服務商') || normalized === 'provider' || normalized.includes('service provider')) return <Store style={style} />;
  return <LayoutGrid style={style} />;
};

const CJK_PATTERN = /[\u3400-\u9fff]/;

const CLASSIFICATION_NAMES: Record<Exclude<MarketplaceLang, 'zh'>, Record<string, string>> = {
  en: {
    '视频 [NSFW]': 'Video [NSFW]',
    '视频［NSFW］': 'Video [NSFW]',
    '图片 [NSFW]': 'Image [NSFW]',
    '图片［NSFW］': 'Image [NSFW]',
    封装: 'Wrapped',
    视频: 'Video',
    图片: 'Image',
    图像: 'Image',
    聊天: 'Chat',
    对话: 'Chat',
    音频: 'Audio',
    语音: 'Speech',
    代码: 'Code',
    向量: 'Embedding',
    嵌入: 'Embedding',
    排序: 'Rerank',
    重排: 'Rerank',
    火山引擎: 'Volcengine',
    字节跳动: 'ByteDance',
    字节: 'ByteDance',
    谷歌: 'Google',
    阿里云: 'Alibaba Cloud',
    阿里: 'Alibaba',
    通义千问: 'Qwen',
    通义: 'Qwen',
    腾讯云: 'Tencent Cloud',
    腾讯: 'Tencent',
    '可灵 AI': 'Kling AI',
    可灵: 'Kling AI',
    快手: 'Kuaishou',
    智谱: 'Zhipu AI',
    智谱AI: 'Zhipu AI',
    '智谱 AI': 'Zhipu AI',
    百川: 'Baichuan',
    百川智能: 'Baichuan',
    百度: 'Baidu',
    文心一言: 'ERNIE',
    月之暗面: 'Moonshot AI',
    阶跃星辰: 'StepFun',
    零一万物: '01.AI',
    商汤: 'SenseTime',
    深度求索: 'DeepSeek',
    海螺: 'Hailuo AI',
    海螺AI: 'Hailuo AI',
    '海螺 AI': 'Hailuo AI',
    即梦: 'Jimeng AI',
    即梦AI: 'Jimeng AI',
    '即梦 AI': 'Jimeng AI',
    '全能参考生视频': 'Omni-Ref Video',
    '参考生视频': 'Ref to Video',
    '视频参考': 'Video Reference',
    '文生视频': 'Text to Video',
    '图生视频': 'Image to Video',
    '图生视频（首帧）': 'Image to Video (First Frame)',
    '图生视频(首帧)': 'Image to Video (First Frame)',
    '首帧生视频': 'First Frame to Video',
    '首尾帧生视频': 'First & Last Frame',
    '视频生视频': 'Video to Video',
    '编辑视频': 'Video Editing',
    '视频编辑': 'Video Editing',
    '延长视频': 'Video Extension',
    '视频延长': 'Video Extension',
    '文生图': 'Text to Image',
    '图文生图': 'Image to Image',
    '多图文生图': 'Multi-Image to Image',
    '图层拆解': 'Layer Decomposition',
    '垫图': 'Image Prompt',
    '蒙版': 'Inpainting Mask',
    '坐标框': 'Bounding Box',
    '创建图片': 'Image Generation',
    '编辑图片': 'Image Editing',
    '放大图片': 'Image Upscaling',
    '理解图片': 'Image Understanding',
    '理解音频': 'Audio Understanding',
    '理解视频': 'Video Understanding',
    '理解文件': 'File Understanding',
    '输入-文字输入': 'Text Input',
    '输入-语音输入': 'Audio Input',
    '输入-视频输入': 'Video Input',
    '输出-文字输出': 'Text Output',
    '文本转语音': 'Text to Speech',
    '语音合成': 'Speech Synthesis',
  },
  'zh-TW': {
    '视频 [NSFW]': '影片 [NSFW]',
    '视频［NSFW］': '影片 [NSFW]',
    '图片 [NSFW]': '圖片 [NSFW]',
    '图片［NSFW］': '圖片 [NSFW]',
    封装: '封裝',
    视频: '影片',
    图片: '圖片',
    图像: '圖像',
    聊天: '對話',
    对话: '對話',
    音频: '音訊',
    语音: '語音',
    代码: '程式碼',
    向量: '向量',
    嵌入: '嵌入',
    排序: '重排',
    重排: '重排',
    火山引擎: '火山引擎',
    字节跳动: '字節跳動',
    字节: '字節跳動',
    谷歌: 'Google',
    阿里云: '阿里雲',
    阿里: '阿里雲',
    通义千问: '通義千問',
    通义: '通義',
    腾讯云: '騰訊雲',
    腾讯: '騰訊',
    '可灵 AI': '可靈 AI',
    可灵: '可靈 AI',
    快手: '快手',
    智谱: '智譜 AI',
    智谱AI: '智譜 AI',
    '智谱 AI': '智譜 AI',
    百川: '百川智能',
    百川智能: '百川智能',
    百度: '百度',
    文心一言: '文心一言',
    月之暗面: '月之暗面',
    阶跃星辰: '階躍星辰',
    零一万物: '零一萬物',
    商汤: '商湯',
    深度求索: '深度求索',
    海螺: '海螺 AI',
    海螺AI: '海螺 AI',
    '海螺 AI': '海螺 AI',
    即梦: '即夢 AI',
    即梦AI: '即夢 AI',
    '即梦 AI': '即夢 AI',
    '全能参考生视频': '全能參考生影片',
    '参考生视频': '參考生影片',
    '视频参考': '影片參考',
    '文生视频': '文生影片',
    '图生视频': '圖生影片',
    '图生视频（首帧）': '圖生影片（首幀）',
    '图生视频(首帧)': '圖生影片（首幀）',
    '首帧生视频': '首幀生影片',
    '首尾帧生视频': '首尾幀生影片',
    '视频生视频': '影片生影片',
    '编辑视频': '編輯影片',
    '视频编辑': '影片編輯',
    '延长视频': '延長影片',
    '视频延长': '影片延長',
    '文生图': '文生圖',
    '图文生图': '圖文生圖',
    '多图文生图': '多圖文生圖',
    '图层拆解': '圖層拆解',
    '垫图': '墊圖',
    '蒙版': '蒙版',
    '坐标框': '座標框',
    '创建图片': '建立圖片',
    '编辑图片': '編輯圖片',
    '放大图片': '放大圖片',
    '理解图片': '理解圖片',
    '理解音频': '理解音訊',
    '理解视频': '理解影片',
    '理解文件': '理解檔案',
    '输入-文字输入': '輸入-文字輸入',
    '输入-语音输入': '輸入-語音輸入',
    '输入-视频输入': '輸入-影片輸入',
    '输出-文字输出': '輸出-文字輸出',
    '文本转语音': '文本轉語音',
    '语音合成': '語音合成',
  },
  ja: {
    '视频 [NSFW]': '動画 [NSFW]',
    '视频［NSFW］': '動画 [NSFW]',
    '图片 [NSFW]': '画像 [NSFW]',
    '图片［NSFW］': '画像 [NSFW]',
    封装: 'ラッパー',
    视频: '動画',
    图片: '画像',
    图像: '画像',
    聊天: 'チャット',
    对话: 'チャット',
    音频: '音声',
    语音: '音声',
    代码: 'コード',
    向量: '埋め込み',
    嵌入: '埋め込み',
    排序: 'リランク',
    重排: 'リランク',
    火山引擎: 'Volcengine',
    字节跳动: 'ByteDance',
    字节: 'ByteDance',
    谷歌: 'Google',
    阿里云: 'Alibaba Cloud',
    阿里: 'Alibaba',
    通义千问: 'Qwen',
    通义: 'Qwen',
    腾讯云: 'Tencent Cloud',
    腾讯: 'Tencent',
    '可灵 AI': 'Kling AI',
    可灵: 'Kling AI',
    智谱: 'Zhipu AI',
    智谱AI: 'Zhipu AI',
    '智谱 AI': 'Zhipu AI',
    百川: 'Baichuan',
    百川智能: 'Baichuan',
    百度: 'Baidu',
    文心一言: 'ERNIE',
    月之暗面: 'Moonshot AI',
    阶跃星辰: 'StepFun',
    零一万物: '01.AI',
    商汤: 'SenseTime',
    深度求索: 'DeepSeek',
    海螺: 'Hailuo AI',
    海螺AI: 'Hailuo AI',
    '海螺 AI': 'Hailuo AI',
    即梦: 'Jimeng AI',
    即梦AI: 'Jimeng AI',
    '即梦 AI': 'Jimeng AI',
    '全能参考生视频': 'マルチ参照動画生成',
    '参考生视频': '参照動画生成',
    '视频参考': '動画参照',
    '文生视频': 'テキスト→動画',
    '图生视频': '画像→動画',
    '图生视频（首帧）': '画像→動画（開始フレーム）',
    '图生视频(首帧)': '画像→動画（開始フレーム）',
    '首帧生视频': '開始フレーム動画生成',
    '首尾帧生视频': '開始・終了フレーム動画',
    '视频生视频': '動画→動画',
    '编辑视频': '動画編集',
    '视频编辑': '動画編集',
    '延长视频': '動画延長',
    '视频延长': '動画延長',
    '文生图': 'テキスト→画像',
    '图文生图': '画像→画像',
    '多图文生图': '複数画像→画像',
    '图层拆解': 'レイヤー分解',
    '垫图': '参照画像',
    '蒙版': 'マスク',
    '坐标框': 'バウンディングボックス',
    '创建图片': '画像生成',
    '编辑图片': '画像編集',
    '放大图片': '高解像度化',
    '理解图片': '画像理解',
    '理解音频': '音声理解',
    '理解视频': '動画理解',
    '理解文件': 'ファイル理解',
    '输入-文字输入': 'テキスト入力',
    '输入-语音输入': '音声入力',
    '输入-视频输入': '動画入力',
    '输出-文字输出': 'テキスト出力',
    '文本转语音': '音声合成 (TTS)',
    '语音合成': '音声合成',
  },
  ko: {
    '视频 [NSFW]': '영상 [NSFW]',
    '视频［NSFW］': '영상 [NSFW]',
    '图片 [NSFW]': '이미지 [NSFW]',
    '图片［NSFW］': '이미지 [NSFW]',
    封装: '래퍼',
    视频: '영상',
    图片: '이미지',
    图像: '이미지',
    聊天: '채팅',
    对话: '채팅',
    音频: '오디오',
    语音: '음성',
    代码: '코드',
    向量: '임베딩',
    嵌入: '임베딩',
    排序: '리랭크',
    重排: '리랭크',
    火山引擎: 'Volcengine',
    字节跳动: 'ByteDance',
    字节: 'ByteDance',
    谷歌: 'Google',
    阿里云: 'Alibaba Cloud',
    阿里: 'Alibaba',
    通义千问: 'Qwen',
    通义: 'Qwen',
    腾讯云: 'Tencent Cloud',
    腾讯: 'Tencent',
    '可灵 AI': 'Kling AI',
    可灵: 'Kling AI',
    智谱: 'Zhipu AI',
    智谱AI: 'Zhipu AI',
    '智谱 AI': 'Zhipu AI',
    百川: 'Baichuan',
    百川智能: 'Baichuan',
    百度: 'Baidu',
    文心一言: 'ERNIE',
    月之暗面: 'Moonshot AI',
    阶跃星辰: 'StepFun',
    零一万物: '01.AI',
    商汤: 'SenseTime',
    深度求索: 'DeepSeek',
    海螺: 'Hailuo AI',
    海螺AI: 'Hailuo AI',
    '海螺 AI': 'Hailuo AI',
    即梦: 'Jimeng AI',
    即梦AI: 'Jimeng AI',
    '即梦 AI': 'Jimeng AI',
    '全能参考生视频': '통합 참조 영상 생성',
    '参考生视频': '참조 영상 생성',
    '视频参考': '영상 참조',
    '文生视频': '텍스트→영상',
    '图生视频': '이미지→영상',
    '图生视频（首帧）': '이미지→영상 (첫 프레임)',
    '图生视频(首帧)': '이미지→영상 (첫 프레임)',
    '首帧生视频': '첫 프레임 영상 생성',
    '首尾帧生视频': '시작/끝 프레임 영상',
    '视频生视频': '영상→영상',
    '编辑视频': '영상 편집',
    '视频编辑': '영상 편집',
    '延长视频': '영상 연장',
    '视频延长': '영상 연장',
    '文生图': '텍스트→이미지',
    '图文生图': '이미지→이미지',
    '多图文生图': '다중 이미지 생성',
    '图层拆解': '레이어 분리',
    '垫图': '참조 이미지',
    '蒙版': '마스크',
    '坐标框': '바운딩 박스',
    '创建图片': '이미지 생성',
    '编辑图片': '이미지 편집',
    '放大图片': '이미지 업스케일',
    '理解图片': '이미지 이해',
    '理解音频': '오디오 이해',
    '理解视频': '영상 이해',
    '理解文件': '파일 이해',
    '输入-文字输入': '텍스트 입력',
    '输入-语音输入': '음성 입력',
    '输入-视频输入': '영상 입력',
    '输出-文字输出': '텍스트 출력',
    '文本转语音': '음성 합성 (TTS)',
    '语音合成': '음성 합성',
  },
  vi: {
    '视频 [NSFW]': 'Video [NSFW]',
    '视频［NSFW］': 'Video [NSFW]',
    '图片 [NSFW]': 'Hình ảnh [NSFW]',
    '图片［NSFW］': 'Hình ảnh [NSFW]',
    封装: 'Đóng gói',
    视频: 'Video',
    图片: 'Hình ảnh',
    图像: 'Hình ảnh',
    聊天: 'Trò chuyện',
    对话: 'Trò chuyện',
    音频: 'Âm thanh',
    语音: 'Giọng nói',
    代码: 'Mã nguồn',
    向量: 'Embedding',
    嵌入: 'Embedding',
    排序: 'Rerank',
    重排: 'Rerank',
    火山引擎: 'Volcengine',
    字节跳动: 'ByteDance',
    字节: 'ByteDance',
    谷歌: 'Google',
    阿里云: 'Alibaba Cloud',
    阿里: 'Alibaba',
    通义千问: 'Qwen',
    通义: 'Qwen',
    腾讯云: 'Tencent Cloud',
    腾讯: 'Tencent',
    '可灵 AI': 'Kling AI',
    可灵: 'Kling AI',
    智谱: 'Zhipu AI',
    智谱AI: 'Zhipu AI',
    '智谱 AI': 'Zhipu AI',
    百川: 'Baichuan',
    百川智能: 'Baichuan',
    百度: 'Baidu',
    文心一言: 'ERNIE',
    月之暗面: 'Moonshot AI',
    阶跃星辰: 'StepFun',
    零一万物: '01.AI',
    商汤: 'SenseTime',
    深度求索: 'DeepSeek',
    海螺: 'Hailuo AI',
    海螺AI: 'Hailuo AI',
    '海螺 AI': 'Hailuo AI',
    即梦: 'Jimeng AI',
    即梦AI: 'Jimeng AI',
    '即梦 AI': 'Jimeng AI',
    '全能参考生视频': 'Video tham chiếu đa năng',
    '参考生视频': 'Video tham chiếu',
    '视频参考': 'Tham chiếu video',
    '文生视频': 'Văn bản sang video',
    '图生视频': 'Ảnh sang video',
    '图生视频（首帧）': 'Ảnh sang video (khung đầu)',
    '图生视频(首帧)': 'Ảnh sang video (khung đầu)',
    '首帧生视频': 'Khung đầu sang video',
    '首尾帧生视频': 'Khung đầu & cuối video',
    '视频生视频': 'Video sang video',
    '编辑视频': 'Chỉnh sửa video',
    '视频编辑': 'Chỉnh sửa video',
    '延长视频': 'Kéo dài video',
    '视频延长': 'Kéo dài video',
    '文生图': 'Văn bản sang ảnh',
    '图文生图': 'Ảnh sang ảnh',
    '多图文生图': 'Nhiều ảnh sang ảnh',
    '图层拆解': 'Tách lớp ảnh',
    '垫图': 'Ảnh gợi ý',
    '蒙版': 'Mặt nạ (Mask)',
    '坐标框': 'Khung tọa độ',
    '创建图片': 'Tạo ảnh',
    '编辑图片': 'Sửa ảnh',
    '放大图片': 'Phóng đại ảnh',
    '理解图片': 'Hiểu hình ảnh',
    '理解音频': 'Hiểu âm thanh',
    '理解视频': 'Hiểu video',
    '理解文件': 'Hiểu tài liệu',
    '输入-文字输入': 'Đầu vào văn bản',
    '输入-语音输入': 'Đầu vào giọng nói',
    '输入-视频输入': 'Đầu vào video',
    '输出-文字输出': 'Đầu ra văn bản',
    '文本转语音': 'Văn bản sang giọng nói',
    '语音合成': 'Tổng hợp giọng nói',
  },
  th: {
    '视频 [NSFW]': 'วิดีโอ [NSFW]',
    '视频［NSFW］': 'วิดีโอ [NSFW]',
    '图片 [NSFW]': 'รูปภาพ [NSFW]',
    '图片［NSFW］': 'รูปภาพ [NSFW]',
    封装: 'ห่อหุ้ม',
    视频: 'วิดีโอ',
    图片: 'รูปภาพ',
    图像: 'รูปภาพ',
    聊天: 'แชท',
    对话: 'แชท',
    音频: 'เสียง',
    语音: 'เสียงพูด',
    代码: 'โค้ด',
    向量: 'Embedding',
    嵌入: 'Embedding',
    排序: 'Rerank',
    重排: 'Rerank',
    火山引擎: 'Volcengine',
    字节跳动: 'ByteDance',
    字节: 'ByteDance',
    谷歌: 'Google',
    阿里云: 'Alibaba Cloud',
    阿里: 'Alibaba',
    通义千问: 'Qwen',
    通义: 'Qwen',
    腾讯云: 'Tencent Cloud',
    腾讯: 'Tencent',
    '可灵 AI': 'Kling AI',
    可灵: 'Kling AI',
    智谱: 'Zhipu AI',
    智谱AI: 'Zhipu AI',
    '智谱 AI': 'Zhipu AI',
    百川: 'Baichuan',
    百川智能: 'Baichuan',
    百度: 'Baidu',
    文心一言: 'ERNIE',
    月之暗面: 'Moonshot AI',
    阶跃星辰: 'StepFun',
    零一万物: '01.AI',
    商汤: 'SenseTime',
    深度求索: 'DeepSeek',
    海螺: 'Hailuo AI',
    海螺AI: 'Hailuo AI',
    '海螺 AI': 'Hailuo AI',
    即梦: 'Jimeng AI',
    即梦AI: 'Jimeng AI',
    '即梦 AI': 'Jimeng AI',
    '全能参考生视频': 'สร้างวิดีโอจากข้อมูลอ้างอิงรอบด้าน',
    '参考生视频': 'สร้างวิดีโอจากข้อมูลอ้างอิง',
    '视频参考': 'วิดีโออ้างอิง',
    '文生视频': 'ข้อความเป็นวิดีโอ',
    '图生视频': 'รูปภาพเป็นวิดีโอ',
    '图生视频（首帧）': 'รูปภาพเป็นวิดีโอ (เฟรมแรก)',
    '图生视频(首帧)': 'รูปภาพเป็นวิดีโอ (เฟรมแรก)',
    '首帧生视频': 'เฟรมแรกเป็นวิดีโอ',
    '首尾帧生视频': 'เฟรมแรกและเฟรมสุดท้ายเป็นวิดีโอ',
    '视频生视频': 'วิดีโอเป็นวิดีโอ',
    '编辑视频': 'แก้ไขวิดีโอ',
    '视频编辑': 'แก้ไขวิดีโอ',
    '延长视频': 'ขยายความยาววิดีโอ',
    '视频延长': 'ขยายความยาววิดีโอ',
    '文生图': 'ข้อความเป็นรูปภาพ',
    '图文生图': 'รูปภาพเป็นรูปภาพ',
    '多图文生图': 'หลายรูปภาพเป็นรูปภาพ',
    '图层拆解': 'แยกเลเยอร์ภาพ',
    '垫图': 'ภาพอ้างอิง',
    '蒙版': 'มาสก์ (Mask)',
    '坐标框': 'กรอบพิกัด',
    '创建图片': 'สร้างรูปภาพ',
    '编辑图片': 'แก้ไขรูปภาพ',
    '放大图片': 'ขยายความละเอียดภาพ',
    '理解图片': 'เข้าใจรูปภาพ',
    '理解音频': 'เข้าใจเสียง',
    '理解视频': 'เข้าใจวิดีโอ',
    '理解文件': 'เข้าใจเอกสาร',
    '输入-文字输入': 'อินพุตข้อความ',
    '输入-语音输入': 'อินพุตเสียงพูด',
    '输入-视频输入': 'อินพุตวิดีโอ',
    '输出-文字输出': 'เอาต์พุตข้อความ',
    '文本转语音': 'ข้อความเป็นเสียงพูด',
    '语音合成': 'สังเคราะห์เสียงพูด',
  },
  ms: {
    '视频 [NSFW]': 'Video [NSFW]',
    '视频［NSFW］': 'Video [NSFW]',
    '图片 [NSFW]': 'Imej [NSFW]',
    '图片［NSFW］': 'Imej [NSFW]',
    封装: 'Bungkusan',
    视频: 'Video',
    图片: 'Imej',
    图像: 'Imej',
    聊天: 'Sembang',
    对话: 'Sembang',
    音频: 'Audio',
    语音: 'Suara',
    代码: 'Kod',
    向量: 'Embedding',
    嵌入: 'Embedding',
    排序: 'Rerank',
    重排: 'Rerank',
    火山引擎: 'Volcengine',
    字节跳动: 'ByteDance',
    字节: 'ByteDance',
    谷歌: 'Google',
    阿里云: 'Alibaba Cloud',
    阿里: 'Alibaba',
    通义千问: 'Qwen',
    通义: 'Qwen',
    腾讯云: 'Tencent Cloud',
    腾讯: 'Tencent',
    '可灵 AI': 'Kling AI',
    可灵: 'Kling AI',
    智谱: 'Zhipu AI',
    智谱AI: 'Zhipu AI',
    '智谱 AI': 'Zhipu AI',
    百川: 'Baichuan',
    百川智能: 'Baichuan',
    百度: 'Baidu',
    文心一言: 'ERNIE',
    月之暗面: 'Moonshot AI',
    阶跃星辰: 'StepFun',
    零一万物: '01.AI',
    商汤: 'SenseTime',
    深度求索: 'DeepSeek',
    海螺: 'Hailuo AI',
    海螺AI: 'Hailuo AI',
    '海螺 AI': 'Hailuo AI',
    即梦: 'Jimeng AI',
    即梦AI: 'Jimeng AI',
    '即梦 AI': 'Jimeng AI',
    '全能参考生视频': 'Video Rujukan Omni',
    '参考生视频': 'Rujukan ke Video',
    '视频参考': 'Rujukan Video',
    '文生视频': 'Teks ke Video',
    '图生视频': 'Imej ke Video',
    '图生视频（首帧）': 'Imej ke Video (Bingkai Pertama)',
    '图生视频(首帧)': 'Imej ke Video (Bingkai Pertama)',
    '首帧生视频': 'Bingkai Pertama ke Video',
    '首尾帧生视频': 'Bingkai Awal & Akhir ke Video',
    '视频生视频': 'Video ke Video',
    '编辑视频': 'Suntingan Video',
    '视频编辑': 'Suntingan Video',
    '延长视频': 'Lanjutan Video',
    '视频延长': 'Lanjutan Video',
    '文生图': 'Teks ke Imej',
    '图文生图': 'Imej ke Imej',
    '多图文生图': 'Berbilang Imej ke Imej',
    '图层拆解': 'Pemisahan Lapisan',
    '垫图': 'Imej Rujukan',
    '蒙版': 'Topeng (Mask)',
    '坐标框': 'Kotak Sempadan',
    '创建图片': 'Cipta Imej',
    '编辑图片': 'Sunting Imej',
    '放大图片': 'Besarkan Imej',
    '理解图片': 'Pemahaman Imej',
    '理解音频': 'Pemahaman Audio',
    '理解视频': 'Pemahaman Video',
    '理解文件': 'Pemahaman Fail',
    '输入-文字输入': 'Input Teks',
    '输入-语音输入': 'Input Suara',
    '输入-视频输入': 'Input Video',
    '输出-文字输出': 'Output Teks',
    '文本转语音': 'Teks ke Suara',
    '语音合成': 'Sintesis Suara',
  },
  fr: {
    '视频 [NSFW]': 'Vidéo [NSFW]',
    '视频［NSFW］': 'Vidéo [NSFW]',
    '图片 [NSFW]': 'Image [NSFW]',
    '图片［NSFW］': 'Image [NSFW]',
    封装: 'Encapsulé',
    视频: 'Vidéo',
    图片: 'Image',
    图像: 'Image',
    聊天: 'Chat',
    对话: 'Chat',
    音频: 'Audio',
    语音: 'Voix',
    代码: 'Code',
    向量: 'Embedding',
    嵌入: 'Embedding',
    排序: 'Rerank',
    重排: 'Rerank',
    火山引擎: 'Volcengine',
    字节跳动: 'ByteDance',
    字节: 'ByteDance',
    谷歌: 'Google',
    阿里云: 'Alibaba Cloud',
    阿里: 'Alibaba',
    通义千问: 'Qwen',
    通义: 'Qwen',
    腾讯云: 'Tencent Cloud',
    腾讯: 'Tencent',
    '可灵 AI': 'Kling AI',
    可灵: 'Kling AI',
    智谱: 'Zhipu AI',
    智谱AI: 'Zhipu AI',
    '智谱 AI': 'Zhipu AI',
    百川: 'Baichuan',
    百川智能: 'Baichuan',
    百度: 'Baidu',
    文心一言: 'ERNIE',
    月之暗面: 'Moonshot AI',
    阶跃星辰: 'StepFun',
    零一万物: '01.AI',
    商汤: 'SenseTime',
    深度求索: 'DeepSeek',
    海螺: 'Hailuo AI',
    海螺AI: 'Hailuo AI',
    '海螺 AI': 'Hailuo AI',
    即梦: 'Jimeng AI',
    即梦AI: 'Jimeng AI',
    '即梦 AI': 'Jimeng AI',
    '全能参考生视频': 'Vidéo réf. omni',
    '参考生视频': 'Réf. vers vidéo',
    '视频参考': 'Référence vidéo',
    '文生视频': 'Texte → Vidéo',
    '图生视频': 'Image → Vidéo',
    '图生视频（首帧）': 'Image → Vidéo (1re trame)',
    '图生视频(首帧)': 'Image → Vidéo (1re trame)',
    '首帧生视频': '1re trame → Vidéo',
    '首尾帧生视频': ' Première & dernière trame',
    '视频生视频': 'Vidéo → Vidéo',
    '编辑视频': 'Édition vidéo',
    '视频编辑': 'Édition vidéo',
    '延长视频': 'Extension vidéo',
    '视频延长': 'Extension vidéo',
    '文生图': 'Texte → Image',
    '图文生图': 'Image → Image',
    '多图文生图': 'Multi-images → Image',
    '图层拆解': 'Décomposition de calques',
    '垫图': 'Image de réf.',
    '蒙版': 'Masque',
    '坐标框': 'Cadre de sélection',
    '创建图片': 'Création d’image',
    '编辑图片': 'Édition d’image',
    '放大图片': 'Agrandi d’image',
    '理解图片': 'Analyse d’image',
    '理解音频': 'Analyse audio',
    '理解视频': 'Analyse vidéo',
    '理解文件': 'Analyse de fichier',
    '输入-文字输入': 'Entrée texte',
    '输入-语音输入': 'Entrée vocale',
    '输入-视频输入': 'Entrée vidéo',
    '输出-文字输出': 'Sortie texte',
    '文本转语音': 'Synthèse vocale',
    '语音合成': 'Synthèse vocale',
  },
  de: {
    '视频 [NSFW]': 'Video [NSFW]',
    '视频［NSFW］': 'Video [NSFW]',
    '图片 [NSFW]': 'Bild [NSFW]',
    '图片［NSFW］': 'Bild [NSFW]',
    封装: 'Wrapper',
    视频: 'Video',
    图片: 'Bild',
    图像: 'Bild',
    聊天: 'Chat',
    对话: 'Chat',
    音频: 'Audio',
    语音: 'Sprache',
    代码: 'Code',
    向量: 'Embedding',
    嵌入: 'Embedding',
    排序: 'Rerank',
    重排: 'Rerank',
    火山引擎: 'Volcengine',
    字节跳动: 'ByteDance',
    字节: 'ByteDance',
    谷歌: 'Google',
    阿里云: 'Alibaba Cloud',
    阿里: 'Alibaba',
    通义千问: 'Qwen',
    通义: 'Qwen',
    腾讯云: 'Tencent Cloud',
    腾讯: 'Tencent',
    '可灵 AI': 'Kling AI',
    可灵: 'Kling AI',
    智谱: 'Zhipu AI',
    智谱AI: 'Zhipu AI',
    '智谱 AI': 'Zhipu AI',
    百川: 'Baichuan',
    百川智能: 'Baichuan',
    百度: 'Baidu',
    文心一言: 'ERNIE',
    月之暗面: 'Moonshot AI',
    阶跃星辰: 'StepFun',
    零一万物: '01.AI',
    商汤: 'SenseTime',
    深度求索: 'DeepSeek',
    海螺: 'Hailuo AI',
    海螺AI: 'Hailuo AI',
    '海螺 AI': 'Hailuo AI',
    即梦: 'Jimeng AI',
    即梦AI: 'Jimeng AI',
    '即梦 AI': 'Jimeng AI',
    '全能参考生视频': 'Omni-Ref-Video',
    '参考生视频': 'Referenz-Video',
    '视频参考': 'Videoreferenz',
    '文生视频': 'Text → Video',
    '图生视频': 'Bild → Video',
    '图生视频（首帧）': 'Bild → Video (Startframe)',
    '图生视频(首帧)': 'Bild → Video (Startframe)',
    '首帧生视频': 'Startframe → Video',
    '首尾帧生视频': 'Start- & Endframe-Video',
    '视频生视频': 'Video → Video',
    '编辑视频': 'Videobearbeitung',
    '视频编辑': 'Videobearbeitung',
    '延长视频': 'Videoverlängerung',
    '视频延长': 'Videoverlängerung',
    '文生图': 'Text → Bild',
    '图文生图': 'Bild → Bild',
    '多图文生图': 'Multi-Bild → Bild',
    '图层拆解': 'Ebenenzerlegung',
    '垫图': 'Bild-Prompt',
    '蒙版': 'Maske',
    '坐标框': 'Begrenzungsrahmen',
    '创建图片': 'Bilderstellung',
    '编辑图片': 'Bildbearbeitung',
    '放大图片': 'Bild-Upscaling',
    '理解图片': 'Bildverständnis',
    '理解音频': 'Audioverständnis',
    '理解视频': 'Videoverständnis',
    '理解文件': 'Dateiverständnis',
    '输入-文字输入': 'Texteingabe',
    '输入-语音输入': 'Spracheingabe',
    '输入-视频输入': 'Videoeingabe',
    '输出-文字输出': 'Textausgabe',
    '文本转语音': 'Text-zu-Sprache',
    '语音合成': 'Sprachsynthese',
  },
  es: {
    '视频 [NSFW]': 'Video [NSFW]',
    '视频［NSFW］': 'Video [NSFW]',
    '图片 [NSFW]': 'Imagen [NSFW]',
    '图片［NSFW］': 'Imagen [NSFW]',
    封装: 'Encapsulado',
    视频: 'Video',
    图片: 'Imagen',
    图像: 'Imagen',
    聊天: 'Chat',
    对话: 'Chat',
    音频: 'Audio',
    语音: 'Voz',
    代码: 'Código',
    向量: 'Embedding',
    嵌入: 'Embedding',
    排序: 'Rerank',
    重排: 'Rerank',
    火山引擎: 'Volcengine',
    字节跳动: 'ByteDance',
    字节: 'ByteDance',
    谷歌: 'Google',
    阿里云: 'Alibaba Cloud',
    阿里: 'Alibaba',
    通义千问: 'Qwen',
    通义: 'Qwen',
    腾讯云: 'Tencent Cloud',
    腾讯: 'Tencent',
    '可灵 AI': 'Kling AI',
    可灵: 'Kling AI',
    智谱: 'Zhipu AI',
    智谱AI: 'Zhipu AI',
    '智谱 AI': 'Zhipu AI',
    百川: 'Baichuan',
    百川智能: 'Baichuan',
    百度: 'Baidu',
    文心一言: 'ERNIE',
    月之暗面: 'Moonshot AI',
    阶跃星辰: 'StepFun',
    零一万物: '01.AI',
    商汤: 'SenseTime',
    深度求索: 'DeepSeek',
    海螺: 'Hailuo AI',
    海螺AI: 'Hailuo AI',
    '海螺 AI': 'Hailuo AI',
    即梦: 'Jimeng AI',
    即梦AI: 'Jimeng AI',
    '即梦 AI': 'Jimeng AI',
    '全能参考生视频': 'Video ref. omni',
    '参考生视频': 'Ref. a video',
    '视频参考': 'Referencia de video',
    '文生视频': 'Texto a video',
    '图生视频': 'Imagen a video',
    '图生视频（首帧）': 'Imagen a video (1er fotograma)',
    '图生视频(首帧)': 'Imagen a video (1er fotograma)',
    '首帧生视频': '1er fotograma a video',
    '首尾帧生视频': 'Fotograma inicial y final',
    '视频生视频': 'Video a video',
    '编辑视频': 'Edición de video',
    '视频编辑': 'Edición de video',
    '延长视频': 'Extensión de video',
    '视频延长': 'Extensión de video',
    '文生图': 'Texto a imagen',
    '图文生图': 'Imagen a imagen',
    '多图文生图': 'Multi-imagen a imagen',
    '图层拆解': 'Desglose de capas',
    '垫图': 'Imagen guía',
    '蒙版': 'Máscara',
    '坐标框': 'Cuadro delimitador',
    '创建图片': 'Crear imagen',
    '编辑图片': 'Editar imagen',
    '放大图片': 'Escalar imagen',
    '理解图片': 'Comprensión de imagen',
    '理解音频': 'Comprensión de audio',
    '理解视频': 'Comprensión de video',
    '理解文件': 'Comprensión de archivos',
    '输入-文字输入': 'Entrada de texto',
    '输入-语音输入': 'Entrada de voz',
    '输入-视频输入': 'Entrada de video',
    '输出-文字输出': 'Salida de texto',
    '文本转语音': 'Texto a voz',
    '语音合成': 'Síntesis de voz',
  },
  pt: {
    '视频 [NSFW]': 'Vídeo [NSFW]',
    '视频［NSFW］': 'Vídeo [NSFW]',
    '图片 [NSFW]': 'Imagem [NSFW]',
    '图片［NSFW］': 'Imagem [NSFW]',
    封装: 'Encapsulado',
    视频: 'Vídeo',
    图片: 'Imagem',
    图像: 'Imagem',
    聊天: 'Chat',
    对话: 'Chat',
    音频: 'Áudio',
    语音: 'Voz',
    代码: 'Código',
    向量: 'Embedding',
    嵌入: 'Embedding',
    排序: 'Rerank',
    重排: 'Rerank',
    火山引擎: 'Volcengine',
    字节跳动: 'ByteDance',
    字节: 'ByteDance',
    谷歌: 'Google',
    阿里云: 'Alibaba Cloud',
    阿里: 'Alibaba',
    通义千问: 'Qwen',
    通义: 'Qwen',
    腾讯云: 'Tencent Cloud',
    腾讯: 'Tencent',
    '可灵 AI': 'Kling AI',
    可灵: 'Kling AI',
    智谱: 'Zhipu AI',
    智谱AI: 'Zhipu AI',
    '智谱 AI': 'Zhipu AI',
    百川: 'Baichuan',
    百川智能: 'Baichuan',
    百度: 'Baidu',
    文心一言: 'ERNIE',
    月之暗面: 'Moonshot AI',
    阶跃星辰: 'StepFun',
    零一万物: '01.AI',
    商汤: 'SenseTime',
    深度求索: 'DeepSeek',
    海螺: 'Hailuo AI',
    海螺AI: 'Hailuo AI',
    '海螺 AI': 'Hailuo AI',
    即梦: 'Jimeng AI',
    即梦AI: 'Jimeng AI',
    '即梦 AI': 'Jimeng AI',
    '全能参考生视频': 'Vídeo ref. omni',
    '参考生视频': 'Ref. para vídeo',
    '视频参考': 'Referência de vídeo',
    '文生视频': 'Texto para vídeo',
    '图生视频': 'Imagem para vídeo',
    '图生视频（首帧）': 'Imagem para vídeo (1º quadro)',
    '图生视频(首帧)': 'Imagem para vídeo (1º quadro)',
    '首帧生视频': '1º quadro para vídeo',
    '首尾帧生视频': 'Quadro inicial e final',
    '视频生视频': 'Vídeo para vídeo',
    '编辑视频': 'Edição de vídeo',
    '视频编辑': 'Edição de vídeo',
    '延长视频': 'Extensão de vídeo',
    '视频延长': 'Extensão de vídeo',
    '文生图': 'Texto para imagem',
    '图文生图': 'Imagem para imagem',
    '多图文生图': 'Multi-imagem para imagem',
    '图层拆解': 'Separação de camadas',
    '垫图': 'Imagem base',
    '蒙版': 'Máscara',
    '坐标框': 'Caixa delimitadora',
    '创建图片': 'Criar imagem',
    '编辑图片': 'Editar imagem',
    '放大图片': 'Ampliar imagem',
    '理解图片': 'Compreensão de imagem',
    '理解音频': 'Compreensão de áudio',
    '理解视频': 'Compreensão de vídeo',
    '理解文件': 'Compreensão de arquivo',
    '输入-文字输入': 'Entrada de texto',
    '输入-语音输入': 'Entrada de voz',
    '输入-视频输入': 'Entrada de vídeo',
    '输出-文字输出': 'Saída de texto',
    '文本转语音': 'Texto para voz',
    '语音合成': 'Síntese de voz',
  },
  ru: {
    '视频 [NSFW]': 'Видео [NSFW]',
    '视频［NSFW］': 'Видео [NSFW]',
    '图片 [NSFW]': 'Изображения [NSFW]',
    '图片［NSFW］': 'Изображения [NSFW]',
    封装: 'Сценарии',
    视频: 'Видео',
    图片: 'Изображения',
    图像: 'Изображения',
    聊天: 'Чат',
    对话: 'Чат',
    音频: 'Аудио',
    语音: 'Речь',
    代码: 'Код',
    向量: 'Эмбеддинги',
    嵌入: 'Эмбеддинги',
    排序: 'Реранк',
    重排: 'Реранк',
    火山引擎: 'Volcengine',
    字节跳动: 'ByteDance',
    字节: 'ByteDance',
    谷歌: 'Google',
    阿里云: 'Alibaba Cloud',
    阿里: 'Alibaba',
    通义千问: 'Qwen',
    通义: 'Qwen',
    腾讯云: 'Tencent Cloud',
    腾讯: 'Tencent',
    '可灵 AI': 'Kling AI',
    可灵: 'Kling AI',
    智谱: 'Zhipu AI',
    智谱AI: 'Zhipu AI',
    '智谱 AI': 'Zhipu AI',
    百川: 'Baichuan',
    百川智能: 'Baichuan',
    百度: 'Baidu',
    文心一言: 'ERNIE',
    月之暗面: 'Moonshot AI',
    阶跃星辰: 'StepFun',
    零一万物: '01.AI',
    商汤: 'SenseTime',
    深度求索: 'DeepSeek',
    海螺: 'Hailuo AI',
    海螺AI: 'Hailuo AI',
    '海螺 AI': 'Hailuo AI',
    即梦: 'Jimeng AI',
    即梦AI: 'Jimeng AI',
    '即梦 AI': 'Jimeng AI',
    '全能参考生视频': 'Мульти-референс видео',
    '参考生视频': 'Видео по референсу',
    '视频参考': 'Видео-референс',
    '文生视频': 'Текст → Видео',
    '图生视频': 'Фото → Видео',
    '图生视频（首帧）': 'Фото → Видео (1-й кадр)',
    '图生视频(首帧)': 'Фото → Видео (1-й кадр)',
    '首帧生视频': 'Видео по 1-му кадру',
    '首尾帧生视频': 'Первый и последний кадр',
    '视频生视频': 'Видео → Видео',
    '编辑视频': 'Редактирование видео',
    '视频编辑': 'Редактирование видео',
    '延长视频': 'Продление видео',
    '视频延长': 'Продление видео',
    '文生图': 'Текст → Фото',
    '图文生图': 'Фото → Фото',
    '多图文生图': 'Мульти-фото → Фото',
    '图层拆解': 'Разделение слоёв',
    '垫图': 'Референс-фото',
    '蒙版': 'Маска',
    '坐标框': 'Рамка координат',
    '创建图片': 'Генерация фото',
    '编辑图片': 'Редактирование фото',
    '放大图片': 'Апскейл фото',
    '理解图片': 'Анализ изображений',
    '理解音频': 'Анализ аудио',
    '理解视频': 'Анализ видео',
    '理解文件': 'Анализ файлов',
    '输入-文字输入': 'Текстовый вход',
    '输入-语音输入': 'Голосовой вход',
    '输入-视频输入': 'Видеовход',
    '输出-文字输出': 'Текстовый выход',
    '文本转语音': 'Синтез речи',
    '语音合成': 'Синтез речи',
  },
  ar: {
    '视频 [NSFW]': 'فيديو [NSFW]',
    '视频［NSFW］': 'فيديو [NSFW]',
    '图片 [NSFW]': 'صور [NSFW]',
    '图片［NSFW］': 'صور [NSFW]',
    封装: 'مغلّف',
    视频: 'فيديو',
    图片: 'صور',
    图像: 'صور',
    聊天: 'محادثة',
    对话: 'محادثة',
    音频: 'صوتيات',
    语音: 'صوت',
    代码: 'برمجة',
    向量: 'تضمين',
    嵌入: 'تضمين',
    排序: 'إعادة ترتيب',
    重排: 'إعادة ترتيب',
    火山引擎: 'Volcengine',
    字节跳动: 'ByteDance',
    字节: 'ByteDance',
    谷歌: 'Google',
    阿里云: 'Alibaba Cloud',
    阿里: 'Alibaba',
    通义千问: 'Qwen',
    通义: 'Qwen',
    腾讯云: 'Tencent Cloud',
    腾讯: 'Tencent',
    '可灵 AI': 'Kling AI',
    可灵: 'Kling AI',
    智谱: 'Zhipu AI',
    智谱AI: 'Zhipu AI',
    '智谱 AI': 'Zhipu AI',
    百川: 'Baichuan',
    百川智能: 'Baichuan',
    百度: 'Baidu',
    文心一言: 'ERNIE',
    月之暗面: 'Moonshot AI',
    阶跃星辰: 'StepFun',
    零一万物: '01.AI',
    商汤: 'SenseTime',
    深度求索: 'DeepSeek',
    海螺: 'Hailuo AI',
    海螺AI: 'Hailuo AI',
    '海螺 AI': 'Hailuo AI',
    即梦: 'Jimeng AI',
    即梦AI: 'Jimeng AI',
    '即梦 AI': 'Jimeng AI',
    '全能参考生视频': 'فيديو بمرجع شامل',
    '参考生视频': 'فيديو بمرجع',
    '视频参考': 'مرجع فيديو',
    '文生视频': 'نص إلى فيديو',
    '图生视频': 'صورة إلى فيديو',
    '图生视频（首帧）': 'صورة إلى فيديو (الإطار الأول)',
    '图生视频(首帧)': 'صورة إلى فيديو (الإطار الأول)',
    '首帧生视频': 'الإطار الأول إلى فيديو',
    '首尾帧生视频': 'إطار البداية والنهاية',
    '视频生视频': 'فيديو إلى فيديو',
    '编辑视频': 'تعديل الفيديو',
    '视频编辑': 'تعديل الفيديو',
    '延长视频': 'تمديد الفيديو',
    '视频延长': 'تمديد الفيديو',
    '文生图': 'نص إلى صورة',
    '图文生图': 'صورة إلى صورة',
    '多图文生图': 'صور متعددة إلى صورة',
    '图层拆解': 'تفكيك الطبقات',
    '垫图': 'صورة مرجعية',
    '蒙版': 'قناع',
    '坐标框': 'مربع إحداثيات',
    '创建图片': 'إنشاء صورة',
    '编辑图片': 'تعديل صورة',
    '放大图片': 'تكبير صورة',
    '理解图片': 'فهم الصور',
    '理解音频': 'فهم الصوت',
    '理解视频': 'فهم الفيديو',
    '理解文件': 'فهم الملفات',
    '输入-文字输入': 'إدخال نصي',
    '输入-语音输入': 'إدخال صوتي',
    '输入-视频输入': 'إدخال فيديو',
    '输出-文字输出': 'إخراج نصي',
    '文本转语音': 'نص إلى صوت',
    '语音合成': 'توليد الصوت',
  },
};

function localizedClassificationName(name: string | undefined, nameEn: string | undefined, language: string | undefined, fallback: string) {
  const lang = marketplaceLang(language);
  const source = (name || '').trim();
  const english = (nameEn || '').trim();
  if (lang === 'zh') return source || english || fallback;
  const mapped = CLASSIFICATION_NAMES[lang]?.[source] || (english ? CLASSIFICATION_NAMES[lang]?.[english] : undefined);
  if (mapped) return mapped;
  if (lang !== 'zh-TW') {
    if (english) return english;
    const enMapped = CLASSIFICATION_NAMES.en?.[source];
    if (enMapped) return enMapped;
    if (fallback && fallback !== 'Other' && fallback !== '其它' && fallback !== '其他' && !CJK_PATTERN.test(fallback)) return fallback;
    return source && !CJK_PATTERN.test(source) ? source : fallback;
  }
  return source || english || fallback;
}

// 智能兜底计费规则生成
const getFallbackBilling = (variant: any) => {
  if (variant.billing) return variant.billing;
  const mid = variant.mid || '';
  const typeName = variant.type_name || '';

  // 1. 火山引擎画质增强与字幕擦除预置模型兜底
  if (mid.startsWith('vve-') || mid.startsWith('vvs-')) {
    const isErase = mid.startsWith('vvs-');
    return {
      billing_type: 'duration',
      billing_rule: isErase ? 'per_second' : 'video_quality',
      fixed_rate: 0,
      duration_rate: 0,
      pricing_tiers: '[]',
      extended_config: '{}',
      name: isErase ? '火山字幕擦除默认规则' : '火山画质增强默认规则',
    };
  }

  // 2. 图像/生图/视频等其它模型按类型智能推断
  if (typeName.includes('图') || typeName.includes('封装') || typeName.includes('Image') || typeName.includes('image')) {
    return {
      billing_type: 'requests',
      billing_rule: 'per_image',
      fixed_rate: 0,
      pricing_tiers: '[]',
      extended_config: '{}',
      name: '图片按张计费默认规则',
    };
  }

  if (typeName.includes('视频') || typeName.includes('Video') || typeName.includes('video')) {
    return {
      billing_type: 'duration',
      billing_rule: 'video_resolution',
      fixed_rate: 0,
      duration_rate: 0,
      pricing_tiers: '[]',
      extended_config: '{}',
      name: '视频按秒计费默认规则',
    };
  }

  // 3. 默认兜底为 tokens
  return {
    billing_type: 'tokens',
    billing_rule: 'standard',
    prompt_rate: 0,
    completion_rate: 0,
    pricing_tiers: '[]',
    extended_config: '{}',
    name: 'Tokens计费默认规则',
  };
};

// 计费类型中文
const getBillingLabel = (billing: any, tp: any) => {
  if (!billing) return null;
  switch (billing.billing_type) {
    case 'tokens': return tp('billing_tokens');
    case 'requests': return tp('billing_requests');
    case 'duration': return tp('billing_duration');
    case 'tiered': return tp('billing_tiered');
    default: return billing.billing_type;
  }
};

const LOBE_DEFAULT_ICON = '/assets/icons/lobe/default-model.svg';

const KNOWN_BRAND_LOGO_MAP: Record<string, string> = {
  // 中文厂商与品牌
  火山引擎: 'volcengine',
  字节跳动: 'bytedance',
  字节: 'bytedance',
  谷歌: 'google',
  阿里云: 'alibaba',
  阿里: 'alibaba',
  通义千问: 'qwen',
  通义: 'qwen',
  腾讯云: 'tencent',
  腾讯: 'tencent',
  '可灵 ai': 'kling',
  可灵: 'kling',
  快手: 'kling',
  智谱: 'zhipu',
  智谱ai: 'zhipu',
  百川: 'baichuan',
  百川智能: 'baichuan',
  百度: 'baidu',
  文心一言: 'baidu',
  文心: 'baidu',
  月之暗面: 'kimi',
  阶跃星辰: 'stepfun',
  零一万物: 'yi',
  商汤: 'sensenova',
  日日新: 'sensenova',
  深度求索: 'deepseek',
  海螺: 'minimax',
  海螺ai: 'minimax',
  // 常见英文与别名（含空格与连字符变体）
  volcengine: 'volcengine',
  bytedance: 'bytedance',
  byteplus: 'byteplus',
  alibaba: 'alibaba',
  alibabacloud: 'alibabacloud',
  'alibaba cloud': 'alibaba',
  tencent: 'tencent',
  'tencent cloud': 'tencent',
  google: 'google',
  'google cloud': 'google',
  kling: 'kling',
  'kling ai': 'kling',
  zhipu: 'zhipu',
  'zhipu ai': 'zhipu',
  chatglm: 'chatglm',
  glm: 'glm',
  baichuan: 'baichuan',
  baidu: 'baidu',
  stepfun: 'stepfun',
  sensenova: 'sensenova',
  deepseek: 'deepseek',
  minimax: 'minimax',
  openai: 'openai',
  anthropic: 'anthropic',
  claude: 'claude',
  meta: 'meta',
  mistral: 'mistral',
  cohere: 'cohere',
  stability: 'stability',
  midjourney: 'midjourney',
  runway: 'runway',
  suno: 'suno',
  udio: 'udio',
};

let iconVersions: Record<string, string> = {};

const withIconVersion = (src: string) => {
  if (!src.startsWith('/assets/icons/')) return src;
  const file = src.split('/').pop() || '';
  const name = file.replace(/\.svg(?:\?.*)?$/i, '');
  const version = iconVersions[name];
  if (version && !src.includes('v=')) {
    return `${src}${src.includes('?') ? '&' : '?'}v=${encodeURIComponent(version)}`;
  }
  return withLobeIconVersion(src);
};

const resolveDirectLobeIcon = (candidate?: string | null): string | null => {
  const raw = (candidate || '').trim();
  if (!raw) return null;
  if (
    raw.startsWith('http://') ||
    raw.startsWith('https://') ||
    raw.startsWith('/') ||
    raw.startsWith('data:')
  ) {
    return withIconVersion(raw);
  }
  const cleaned = raw.replace(/\.svg$/i, '').trim();
  if (/^[a-z0-9_-]+$/i.test(cleaned)) {
    const slug = iconVersions[cleaned] ? cleaned : cleaned.toLowerCase();
    return withIconVersion(`/assets/icons/lobe/${slug}.svg`);
  }
  return null;
};

const lobeIconSrc = (
  logo?: string | null,
  providerLogo?: string | null,
  _isLight: boolean = true,
  fallbackName?: string | null
) => {
  const direct = resolveDirectLobeIcon(logo) || resolveDirectLobeIcon(providerLogo);
  if (direct) return direct;

  const rawCandidate = (logo || providerLogo || fallbackName || '').trim();
  if (!rawCandidate) return LOBE_DEFAULT_ICON;

  // 完整 URL、Data URI 或站内绝对路径直接返回。图标文件与站点图标库预览同一份，不另换资源。
  if (
    rawCandidate.startsWith('http://') ||
    rawCandidate.startsWith('https://') ||
    rawCandidate.startsWith('/') ||
    rawCandidate.startsWith('data:')
  ) {
    return withIconVersion(rawCandidate);
  }

  // 常见品牌中英文别名智能映射
  const cleaned = rawCandidate.replace(/\.svg$/i, '').trim().toLowerCase();
  const matchedLogo = KNOWN_BRAND_LOGO_MAP[cleaned]
    || KNOWN_BRAND_LOGO_MAP[rawCandidate.trim()]
    || Object.keys(KNOWN_BRAND_LOGO_MAP).find(k => {
      if (k.length < 2) return false;
      if (/^[a-z0-9_-]+$/i.test(k)) {
        const idx = cleaned.indexOf(k);
        if (idx === -1) return false;
        const before = idx === 0 ? '' : cleaned[idx - 1];
        if (before && /[a-z]/i.test(before)) return false;
        return true;
      }
      return cleaned.includes(k);
    });
  const name = matchedLogo ? (KNOWN_BRAND_LOGO_MAP[matchedLogo] || matchedLogo) : cleaned.replace(/\s+/g, '');
  return withIconVersion(`/assets/icons/lobe/${name}.svg`);
};

/** 图标加载失败时降级到默认图；保持默认图标显示 */
const handleLobeIconError = (e: React.SyntheticEvent<HTMLImageElement>) => {
  const img = e.currentTarget;
  img.onerror = null;
  img.style.filter = 'none';
  if (!img.src.endsWith(LOBE_DEFAULT_ICON)) {
    img.src = LOBE_DEFAULT_ICON;
  }
};

/** 所有变体均无绑定可用渠道时视为暂不可用 */
const isModelUnavailable = (model: any) =>
  (model?.variants || [model]).every(
    (v: any) => !Array.isArray(v?.ha_subchannels) || v.ha_subchannels.length === 0
  );

/** 高可用组可对应多个上游；倍率相同则计价相同，按倍率升序排列且相同倍率只保留一条展示 */
function uniqueSubchannelsByRate(subs: any[]) {
  const sorted = [...subs].sort((a, b) => (Number(a?.rate) || 1) - (Number(b?.rate) || 1));
  const seen = new Set<string>();
  return sorted.filter((sub) => {
    const key = (Number(sub?.rate) || 1).toFixed(6);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

const NEW_MODEL_WINDOW_MS = 7 * 24 * 60 * 60 * 1000;
const isNewModel = (model: { created_at?: string; variants?: { created_at?: string }[] }) => {
  const cutoff = Date.now() - NEW_MODEL_WINDOW_MS;
  const stamps = [model.created_at, ...(model.variants || []).map(v => v.created_at)];
  return stamps.some(createdAt => {
    const t = parseApiTimeAsUtc(createdAt);
    return !!t && t.getTime() >= cutoff;
  });
};

/** 与站点图标库预览一致，不按主题反色。 */
const getLogoFilter = (_logoName?: string, _isLight = true, _resolvedSrc?: string) => 'none';

interface CopyModelIdButtonProps {
  modelId: string;
  isLight: boolean;
  c: any;
}

const CopyModelIdButton: React.FC<CopyModelIdButtonProps> = ({ modelId, isLight, c }) => {
  const { t: tp } = useTranslation('model_marketplace');
  const [copied, setCopied] = useState(false);

  const handleCopy = async (e: React.MouseEvent) => {
    e.stopPropagation();
    const ok = await copyToClipboard(modelId);
    if (ok) {
      setCopied(true);
      setTimeout(() => {
        setCopied(false);
      }, 1500);
    }
  };

  return (
    <Tooltip title={copied ? tp('copied') : tp('copy_model_id')}>
      <Button
        type="text"
        size="small"
        icon={<CopyOutlined style={{ fontSize: 11 }} />}
        onClick={handleCopy}
        style={{
          color: c.text3,
          padding: 0,
          width: 18,
          height: 18,
          minWidth: 18,
          display: 'inline-flex',
          alignItems: 'center',
          justifyContent: 'center',
          borderRadius: 4
        }}
      />
    </Tooltip>
  );
};

const MpSkel = ({ width, height, radius = 8, style }: { width: number | string; height: number; radius?: number; style?: React.CSSProperties }) => (
  <div className="mp-skel" style={{ width, height, borderRadius: radius, flexShrink: 0, ...style }} />
);

const cardRevealDelayMs = (index: number, id: number | string) => {
  const numId = typeof id === 'number' ? id : parseInt(String(id), 10) || 0;
  const n = Math.imul(numId | 0, 2654435761) >>> 0;
  return index * 26 + (n % 16);
};

const MarketplaceModelsSkeleton = ({ isGrid, isXs }: { isGrid: boolean; isXs: boolean }) => {
  const count = isXs ? 4 : 9;
  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16 }}>
        <MpSkel width={18} height={18} radius={6} />
        <MpSkel width={72} height={16} radius={6} />
        <MpSkel width={40} height={20} radius={10} />
      </div>
      <div
        className="mp-grid"
        style={isGrid
          ? { display: 'grid', gridTemplateColumns: isXs ? '1fr' : MP_GRID_TEMPLATE_COLUMNS, gap: MP_GRID_GAP }
          : { display: 'flex', flexDirection: 'column', gap: 12 }
        }
      >
        {Array.from({ length: count }, (_, i) => (
          <div
            key={i}
            className="mp-card mp-skel-card"
            style={{
              display: 'flex',
              flexDirection: 'column',
              padding: isGrid ? 15 : '16px 20px',
              minHeight: isGrid ? undefined : 88,
              borderRadius: 5,
              cursor: 'default',
              pointerEvents: 'none',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'flex-start', gap: isGrid ? 8 : 12 }}>
              <MpSkel width={isGrid ? 48 : 20} height={isGrid ? 48 : 20} radius={5} />
              <div style={{ flex: 1, minWidth: 0, paddingTop: 2 }}>
                <MpSkel width="72%" height={isGrid ? 18 : 16} radius={6} />
                <MpSkel width="38%" height={12} radius={6} style={{ marginTop: isGrid ? 4 : 6 }} />
              </div>
            </div>
            {isGrid && <div style={{ flex: 1, minHeight: 10 }} />}
            <div style={{
              display: 'grid',
              gridTemplateColumns: isGrid ? '1fr 1fr' : '1fr',
              gap: 16,
              marginTop: isGrid ? 0 : 12,
              paddingTop: isGrid ? 10 : 0,
            }}>
              <div>
                <MpSkel width={40} height={12} radius={4} />
                <MpSkel width={88} height={16} radius={6} style={{ marginTop: 8 }} />
              </div>
              {isGrid && (
                <div>
                  <MpSkel width={40} height={12} radius={4} />
                  <MpSkel width={96} height={16} radius={6} style={{ marginTop: 8 }} />
                </div>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
};

const ModelMarketplace: React.FC = () => {
  const { t: _t, i18n } = useTranslation();
  const { t: tp } = useTranslation('model_marketplace');
  const uiLang = marketplaceLang(i18n.language);
  const displayClassName = (
    name?: string,
    nameEn?: string,
    fallback = tp('other_category', 'Other')
  ) => localizedClassificationName(name, nameEn, i18n.language, fallback);
  const localizedModelDescription = (model: MarketplaceModel | any) => {
    if (uiLang !== 'zh' && uiLang !== 'zh-TW') {
      if (model.description_en) return model.description_en;
      const sourceDescription = model.description || model.model_description || '';
      return CJK_PATTERN.test(sourceDescription) ? '' : sourceDescription;
    }
    return model.description || model.model_description || '';
  };
  const { themeMode, toggleTheme } = useThemeStore();
  const { settings } = useSettingsStore();
  const enableThemeToggle = settings?.site?.enable_theme_toggle !== false;
  const enableMultilingual = settings?.site?.enable_multilingual !== false;
  const navigate = useNavigate();
  const { user, logout } = useAuthStore();
  const [selectedCurrencyCode, setSelectedCurrencyCode] = useState<string>('');
  const [agreement, setAgreement] = useState<any>(settings?.agreement || null);

  const [announcementsDrawerVisible, setAnnouncementsDrawerVisible] = useState(false);
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const [unreadCount, setUnreadCount] = useState(0);
  const [activePlugins, setActivePlugins] = useState<any[]>(() => peekActivePlugins() ?? []);
  const [pluginsReady, setPluginsReady] = useState<boolean>(() => peekActivePlugins() !== null);
  const headerNavReady = pluginsReady;

  const [loading, setLoading] = useState(true);
  const [forbidden, setForbidden] = useState(false);
  const [models, setModels] = useState<MarketplaceModel[]>([]);
  const [groupedModels, setGroupedModels] = useState<MarketplaceModel[]>([]);
  const [providers, setProviders] = useState<FilterItem[]>([]);
  const [types, setTypes] = useState<FilterItem[]>([]);
  const [trendingConfig, setTrendingConfig] = useState<any>(null);
  const [sortBy, setSortBy] = useState<'popular' | 'name' | 'newest'>('popular');
  const [searchParams, setSearchParams] = useSearchParams();
  const [selectedModel, _setSelectedModel] = useState<MarketplaceModel | null>(null);
  const [visibleCount, setVisibleCount] = useState(MARKETPLACE_BATCH);
  const modelsScrollRef = useRef<HTMLElement>(null);
  const loadMoreRef = useRef<HTMLDivElement>(null);

  const typeParam = searchParams.get('type') || searchParams.get('category') || searchParams.get('type_id');
  const providerParam = searchParams.get('provider') || searchParams.get('providers') || searchParams.get('provider_id');
  const qParam = searchParams.get('q') || searchParams.get('keyword');
  const tabParam = searchParams.get('tab') || searchParams.get('view');
  const selectedType = resolveFilterId(typeParam, types);
  const selectedProvider = resolveFilterId(providerParam, providers, (item, token) =>
    item.provider_type?.toLowerCase() === token,
  );
  const searchKeyword = qParam ?? '';
  const activeView: 'trending' | 'models' =
    tabParam === 'trending'
      ? 'trending'
      : tabParam === 'models' || typeParam || providerParam || qParam
        ? 'models'
        : trendingConfig?.enabled !== false
          ? 'trending'
          : 'models';

  const patchMarketplaceParams = (patch: MarketplaceQueryPatch) => {
    const next = new URLSearchParams(searchParams);
    if (patch.tab) {
      next.set('tab', patch.tab);
      next.delete('view');
    }
    if (patch.type !== undefined) {
      next.delete('category');
      next.delete('type_id');
      if (patch.type === null) next.delete('type');
      else next.set('type', String(patch.type));
    }
    if (patch.provider !== undefined) {
      next.delete('providers');
      next.delete('provider_id');
      if (patch.provider === null) next.delete('provider');
      else next.set('provider', String(patch.provider));
    }
    if (patch.q !== undefined) {
      next.delete('keyword');
      if (patch.q) next.set('q', patch.q);
      else next.delete('q');
    }
    if (patch.closeModel) {
      next.delete('model');
      _setSelectedModel(null);
    }
    setSearchParams(next);
  };

  const setSelectedModel = (model: MarketplaceModel | null) => {
    _setSelectedModel(model);
    const newParams = new URLSearchParams(searchParams);
    if (model) {
      const modelParam = model.original_id || model.model_id || model.mid || (model.id ? model.id.toString() : '');
      newParams.set('model', modelParam);
    } else {
      newParams.delete('model');
    }
    setSearchParams(newParams);
  };

  const handleNavHome = () => {
    _setSelectedModel(null);
    const newParams = new URLSearchParams();
    if (trendingConfig?.enabled !== false) {
      newParams.set('tab', 'trending');
    } else {
      newParams.set('tab', 'models');
    }
    setSearchParams(newParams);
  };

  const handleNavTrending = () => {
    patchMarketplaceParams({
      tab: 'trending',
      type: null,
      provider: null,
      q: null,
      closeModel: true,
    });
  };

  const handleNavAllModels = () => {
    patchMarketplaceParams({
      tab: 'models',
      type: null,
      closeModel: true,
    });
  };

  const handleNavType = (t: FilterItem) => {
    patchMarketplaceParams({
      tab: 'models',
      type: selectedType !== null && String(selectedType) === String(t.id) ? null : t.id,
      closeModel: true,
    });
  };

  useEffect(() => {
    const modelIdInUrl = searchParams.get('model');
    if (modelIdInUrl && (groupedModels.length > 0 || models.length > 0)) {
      const targetIdStr = modelIdInUrl.toString().trim().toLowerCase();

      // 1. Search in groupedModels (including its variants)
      let found: MarketplaceModel | undefined = groupedModels.find(m => 
        m.id?.toString().toLowerCase() === targetIdStr ||
        m.mid?.toString().toLowerCase() === targetIdStr ||
        m.model_id?.toLowerCase() === targetIdStr || 
        m.original_id?.toLowerCase() === targetIdStr || 
        m.name?.toLowerCase() === targetIdStr ||
        (m.variants && m.variants.some(v => 
          v.id?.toString().toLowerCase() === targetIdStr || 
          v.mid?.toString().toLowerCase() === targetIdStr || 
          v.original_id?.toLowerCase() === targetIdStr || 
          v.model_id?.toLowerCase() === targetIdStr ||
          v.name?.toLowerCase() === targetIdStr
        ))
      );

      // 2. If not found in groupedModels, check flat models array
      if (!found && models.length > 0) {
        const flatFound = models.find(m => 
          m.id?.toString().toLowerCase() === targetIdStr ||
          m.mid?.toString().toLowerCase() === targetIdStr ||
          m.model_id?.toLowerCase() === targetIdStr || 
          m.original_id?.toLowerCase() === targetIdStr || 
          m.name?.toLowerCase() === targetIdStr
        );

        if (flatFound) {
          found = groupedModels.find(g => 
            String(g.id) === String(flatFound.id) || 
            g.variants?.some(v => String(v.id) === String(flatFound.id) || v.original_id === flatFound.original_id)
          ) || flatFound;
        }
      }

      if (found) {
        if (!selectedModel || (String(selectedModel.id) !== String(found.id) && selectedModel.original_id !== found.original_id && selectedModel.mid !== found.mid)) {
          _setSelectedModel(found);
        }
      }
    } else if (!modelIdInUrl && selectedModel) {
      _setSelectedModel(null);
    }
  }, [searchParams, groupedModels, models]);
  const [viewMode, setViewMode] = useState<'list' | 'grid'>('grid');
  const [showDiscount, setShowDiscount] = useState(() => {
    try {
      return localStorage.getItem('model_marketplace_discount_display') !== '0';
    } catch {
      return true;
    }
  });
  const setDiscountDisplay = (on: boolean) => {
    setShowDiscount(on);
    try {
      localStorage.setItem('model_marketplace_discount_display', on ? '1' : '0');
    } catch {
      /* 隐私模式写不进本地存储时，本次会话仍然生效 */
    }
  };
  const screens = useBreakpoint();
  const [collapsed, setCollapsed] = useState(() => {
    try {
      if (typeof window !== 'undefined' && window.innerWidth <= 576) {
        return true;
      }
      const saved = localStorage.getItem('model_marketplace_sidebar_collapsed');
      return saved !== null ? JSON.parse(saved) : false;
    } catch (e) {
      return false;
    }
  });

  useEffect(() => {
    if (screens.xs) {
      setCollapsed(true);
    }
  }, [screens.xs]);

  const handleCollapsedChange = (val: boolean) => {
    setCollapsed(val);
    localStorage.setItem('model_marketplace_sidebar_collapsed', JSON.stringify(val));
  };

  // 从 settings store 派生站点信息，不再独立调 /settings 接口
  const siteName = settings?.site?.name || 'laiai';
  const siteLogo = settings?.site?.logo || '';
  const currencySymbol = settings?.currency?.currency_symbol || '¥';
  const auxiliaryCurrencies = useMemo(() => {
    const list = settings?.currency?.auxiliary_currencies;
    return Array.isArray(list) ? list.filter(c => c.enabled) : [];
  }, [settings?.currency?.auxiliary_currencies]);

  useEffect(() => {
    if (settings?.agreement) setAgreement(settings.agreement);
  }, [settings?.agreement]);

  useEffect(() => {
    fetchData();
  }, []);

  useEffect(() => {
    if (selectedModel) {
      document.title = `${selectedModel.original_id || selectedModel.name} - ${tp('page_title')}`;
    } else {
      document.title = `${siteName} - ${tp('page_title')}`;
    }
  }, [selectedModel, siteName, tp]);

  useEffect(() => {
    const fetchAnnouncements = async () => {
      const prefs = parseNotificationPreferences(
        user?.notification_preferences,
        settings?.notification?.low_balance_threshold ?? 100.0,
      );
      if (!shouldShowWebNotifications(prefs, settings?.notification)) {
        setAnnouncements([]);
        setUnreadCount(0);
        return;
      }
      try {
        const response = await (request.get('/announcements/public') as any);
        if (response.data) {
          setAnnouncements(response.data);
          setUnreadCount(response.data.length);
          if (response.data.length > 0) {
            const first = response.data[0];
            const seenKey = `notif_push_seen_${first.id}`;
            if (!sessionStorage.getItem(seenKey)) {
              sessionStorage.setItem(seenKey, '1');
              const title = getAnnouncementLabel(first.title || '') || tp('new_notification', '新通知');
              const body = getAnnouncementLabel(first.content || '').replace(/<[^>]+>/g, '').slice(0, 120);
              maybeShowBrowserPush(title, body, prefs, settings?.notification);
            }
          }
        }
      } catch (error) {
        console.error('Failed to fetch announcements:', error);
      }
    };
    fetchAnnouncements();
  }, [user?.notification_preferences, settings?.notification?.low_balance_threshold, i18n.language]);

  const fetchData = async () => {
    try {
      setLoading(true);

      const res = await (request.get('/marketplace/public') as Promise<any>);
      if (res) {
        setModels(res.models || []);
        setGroupedModels(res.grouped_models || res.models || []);
        setProviders(res.providers || []);
        setTypes(res.types || []);
        if (res.trending_config) {
          setTrendingConfig(res.trending_config);
        }
        iconVersions = res.icon_versions && typeof res.icon_versions === 'object' ? res.icon_versions : {};
        setLobeIconVersions(iconVersions);
      }
    } catch (e: any) {
      if (e?.response?.status === 403 || e?.status === 403) {
        setForbidden(true);
      } else {
        console.error('加载模型广场失败', e);
      }
    } finally {
      setLoading(false);
    }
  };

  const filteredModels = useMemo(() => {
    let result = groupedModels.filter((m) =>
      marketplaceModelMatchesFilters(m, selectedType, selectedProvider),
    );

    if (searchKeyword.trim()) {
      const kw = searchKeyword.trim().toLowerCase();
      result = result.filter(m =>
        modelMatchesKeyword(m, searchKeyword) ||
        (m.original_id && m.original_id.toLowerCase().includes(kw)) ||
        (m.provider_name || '').toLowerCase().includes(kw) ||
        (m.provider_name_en || '').toLowerCase().includes(kw) ||
        (m.type_name_en || '').toLowerCase().includes(kw) ||
        m.description?.toLowerCase()?.includes(kw) ||
        m.description_en?.toLowerCase()?.includes(kw) ||
        m.variants?.some(v =>
          modelMatchesKeyword(v, searchKeyword) ||
          (v.original_id && v.original_id.toLowerCase().includes(kw)) ||
          (v.provider_name || '').toLowerCase().includes(kw) ||
          (v.provider_name_en || '').toLowerCase().includes(kw)
        ) || false
      );
    }

    if (sortBy === 'popular') {
      const brandRank = new Map(providers.map((p, index) => [String(p.id), index]));
      const rankOf = (m: MarketplaceModel) => {
        const rank = brandRank.get(String(m.provider_id));
        return rank == null ? brandRank.size + 1 : rank;
      };
      result.sort((a, b) =>
        rankOf(a) - rankOf(b)
        || ((Number(b.sort_order) || 0) - (Number(a.sort_order) || 0))
        || ((Number(b.id) || 0) - (Number(a.id) || 0))
      );
    } else if (sortBy === 'name') {
      result.sort((a, b) => (a.original_id || a.name).localeCompare(b.original_id || b.name));
    } else if (sortBy === 'newest') {
      result.sort((a, b) => (parseApiTimeAsUtc(b.created_at)?.getTime() ?? 0) - (parseApiTimeAsUtc(a.created_at)?.getTime() ?? 0));
    }

    return result;
  }, [groupedModels, providers, selectedType, selectedProvider, searchKeyword, sortBy]);

  // 全部模型和各个类型菜单都按品牌分组，品牌顺序与顶部品牌标签一致；全部模型下各品牌内部再按模型类型分类
  const groupedModelsByType = useMemo(() => {
    const modelsToGroup = filteredModels;
    if (modelsToGroup.length === 0) return [];

    const buildTypeGroups = (list: MarketplaceModel[]) => {
      const typeMap = new Map<string, MarketplaceModel[]>();
      const uncategorized: MarketplaceModel[] = [];

      list.forEach(m => {
        const typeId = m.type_id ? String(m.type_id) : (m.variants && m.variants.find(v => v.type_id)?.type_id ? String(m.variants.find(v => v.type_id)!.type_id) : null);
        if (typeId) {
          if (!typeMap.has(typeId)) typeMap.set(typeId, []);
          typeMap.get(typeId)!.push(m);
        } else if (m.type_name) {
          const found = types.find(t => t.name.toLowerCase() === m.type_name.toLowerCase());
          const key = found ? String(found.id) : m.type_name;
          if (!typeMap.has(key)) typeMap.set(key, []);
          typeMap.get(key)!.push(m);
        } else {
          uncategorized.push(m);
        }
      });

      const groups: { typeId: number | string; typeName: string; typeNameEn?: string; models: MarketplaceModel[] }[] = [];

      // 严格按照管理后台 types 的实际排序对各类型依次列出
      types.forEach(t => {
        const tid = String(t.id);
        if (typeMap.has(tid)) {
          groups.push({
            typeId: t.id,
            typeName: t.name,
            typeNameEn: t.name_en,
            models: typeMap.get(tid)!
          });
          typeMap.delete(tid);
        }
      });

      typeMap.forEach((modelList, key) => {
        const typeObj = types.find(t => String(t.id) === String(key) || t.name === key);
        groups.push({
          typeId: key,
          typeName: typeObj ? typeObj.name : String(key),
          typeNameEn: typeObj?.name_en,
          models: modelList
        });
      });

      if (uncategorized.length > 0) {
        groups.push({
          typeId: 'uncategorized',
          typeName: tp('other_category', '其他分类'),
          models: uncategorized
        });
      }

      return groups;
    };

    if (sortBy === 'popular') {
      const byBrand = new Map<string, MarketplaceModel[]>();
      modelsToGroup.forEach((m) => {
        const key = selectedProvider !== null
          ? String(selectedProvider)
          : (m.provider_id ? String(m.provider_id) : 'none');
        const list = byBrand.get(key);
        if (list) list.push(m);
        else byBrand.set(key, [m]);
      });
      const sections: {
        typeId: number | string;
        typeName: string;
        typeNameEn?: string;
        brand?: boolean;
        logo?: string | null;
        models: MarketplaceModel[];
        typeGroups?: { typeId: number | string; typeName: string; typeNameEn?: string; models: MarketplaceModel[] }[];
      }[] = [];
      const pushBrandSection = (
        typeId: string,
        typeName: string,
        typeNameEn: string | undefined,
        logo: string | null | undefined,
        brandModels: MarketplaceModel[],
      ) => {
        if (selectedType === null) {
          const typeGroups = buildTypeGroups(brandModels);
          sections.push({
            typeId,
            typeName,
            typeNameEn,
            brand: true,
            logo,
            models: typeGroups.flatMap((g) => g.models),
            typeGroups,
          });
        } else {
          sections.push({
            typeId,
            typeName,
            typeNameEn,
            brand: true,
            logo,
            models: brandModels,
          });
        }
      };
      providers.forEach((p) => {
        const brandModels = byBrand.get(String(p.id));
        if (!brandModels?.length) return;
        pushBrandSection(`brand-${p.id}`, p.name, p.name_en, p.logo, brandModels);
        byBrand.delete(String(p.id));
      });
      byBrand.forEach((brandModels, key) => {
        const named = (brandModels[0]?.provider_name || '').trim();
        pushBrandSection(
          `brand-${key}`,
          named || tp('unknown_provider', '服务商'),
          brandModels[0]?.provider_name_en,
          named ? brandModels[0]?.provider_logo : null,
          brandModels,
        );
      });
      return sections;
    }

    return buildTypeGroups(modelsToGroup) as {
      typeId: number | string;
      typeName: string;
      typeNameEn?: string;
      brand?: boolean;
      logo?: string | null;
      models: MarketplaceModel[];
      typeGroups?: { typeId: number | string; typeName: string; typeNameEn?: string; models: MarketplaceModel[] }[];
    }[];
  }, [filteredModels, types, providers, tp, selectedType, selectedProvider, sortBy]);

  const visibleSections = useMemo(() => {
    let left = visibleCount;
    const sections: {
      sec: (typeof groupedModelsByType)[number];
      shown: MarketplaceModel[];
      total: number;
      visibleTypeGroups?: {
        typeId: number | string;
        typeName: string;
        typeNameEn?: string;
        shown: MarketplaceModel[];
        total: number;
      }[];
    }[] = [];
    for (const sec of groupedModelsByType) {
      if (left <= 0) break;
      const shown = sec.models.slice(0, left);
      left -= shown.length;
      let visibleTypeGroups: {
        typeId: number | string;
        typeName: string;
        typeNameEn?: string;
        shown: MarketplaceModel[];
        total: number;
      }[] | undefined;
      if (sec.typeGroups && sec.typeGroups.length > 0) {
        let subLeft = shown.length;
        visibleTypeGroups = [];
        for (const tg of sec.typeGroups) {
          if (subLeft <= 0) break;
          const tgShown = tg.models.slice(0, subLeft);
          subLeft -= tgShown.length;
          visibleTypeGroups.push({
            typeId: tg.typeId,
            typeName: tg.typeName,
            typeNameEn: tg.typeNameEn,
            shown: tgShown,
            total: tg.models.length,
          });
        }
      }
      sections.push({ sec, shown, total: sec.models.length, visibleTypeGroups });
    }
    return sections;
  }, [groupedModelsByType, visibleCount]);

  useEffect(() => {
    setVisibleCount(MARKETPLACE_BATCH);
    if (modelsScrollRef.current) modelsScrollRef.current.scrollTop = 0;
  }, [selectedType, selectedProvider, searchKeyword, sortBy]);

  useEffect(() => {
    const root = modelsScrollRef.current;
    const target = loadMoreRef.current;
    if (!root || !target || visibleCount >= filteredModels.length) return;
    const observer = new IntersectionObserver((entries) => {
      if (!entries.some((entry) => entry.isIntersecting)) return;
      setVisibleCount((count) => Math.min(count + MARKETPLACE_BATCH, filteredModels.length));
    }, { root, rootMargin: '320px' });
    observer.observe(target);
    return () => observer.disconnect();
  }, [visibleCount, filteredModels.length, selectedModel, loading]);

  const modelsMatchingProviders = useMemo(
    () => groupedModels.filter((m) => marketplaceModelMatchesFilters(m, null, selectedProvider)),
    [groupedModels, selectedProvider],
  );

  const typeCounts = useMemo(() => {
    const counts: Record<string | number, number> = {};
    types.forEach((t) => {
      counts[t.id] = groupedModels.filter((m) =>
        marketplaceModelMatchesFilters(m, t.id, selectedProvider),
      ).length;
    });
    return counts;
  }, [groupedModels, types, selectedProvider]);

  const providerCounts = useMemo(() => {
    const counts: Record<string | number, number> = {};
    providers.forEach((p) => {
      counts[p.id] = groupedModels.filter((m) =>
        marketplaceModelMatchesFilters(m, selectedType, p.id),
      ).length;
    });
    return counts;
  }, [groupedModels, providers, selectedType]);

  const handleProviderToggle = (id: number | string) => {
    const nextProvider = selectedProvider !== null && String(selectedProvider) === String(id) ? null : id;
    const kw = searchKeyword.trim().toLowerCase();
    const qIsProviderName = kw.length > 0 && providers.some(
      (p) => p.name.toLowerCase() === kw || (p.name_en || '').toLowerCase() === kw || p.provider_type?.toLowerCase() === kw,
    );
    patchMarketplaceParams({
      tab: 'models',
      provider: nextProvider,
      q: qIsProviderName ? null : undefined,
      closeModel: true,
    });
  };

  const hasActiveFilters = selectedType !== null || selectedProvider !== null || !!searchKeyword.trim();

  const clearFilters = () => {
    patchMarketplaceParams({
      tab: 'models',
      type: null,
      provider: null,
      q: null,
      closeModel: true,
    });
  };

  const isLight = themeMode === 'light';
  const c = {
    bg: isLight ? 'var(--dashboard)' : '#000000',
    siderBg: isLight ? '#ffffff' : '#141414',
    cardBg: isLight ? '#ffffff' : '#141414',
    cardBorder: isLight ? '#e4e4e7' : '#1f1f23',
    cardHoverBg: isLight ? '#fafafa' : '#1a1a1a',
    panelBg: isLight ? '#f4f5f7' : '#141414',
    text1: isLight ? '#1f2937' : 'rgba(255,255,255,0.95)',
    text2: isLight ? '#4b5563' : 'rgba(255,255,255,0.75)',
    text3: isLight ? '#6b7280' : 'rgba(255,255,255,0.5)',
    textMuted: isLight ? '#9ca3af' : 'rgba(255,255,255,0.25)',
    searchBg: isLight ? '#f8f9fa' : '#141414',
    searchBorder: isLight ? '#e4e4e7' : '#1f1f23',
    focusBorder: isLight ? '#434343' : '#4f4f56',
    hoverBg: isLight ? 'rgba(0,0,0,0.03)' : 'rgba(255,255,255,0.05)',
    sidebarText: isLight ? '#374151' : 'rgba(255,255,255,0.75)',
    scrollThumb: isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.06)',
    sortBorder: isLight ? '#e4e4e7' : '#1f1f23',
    link: isLight ? '#1f2937' : 'rgba(255,255,255,0.95)',
    active: isLight ? '#000000' : '#ffffff',
    activeBg: isLight ? '#f4f5f7' : 'rgba(255,255,255,0.08)',
    activeBorder: isLight ? '#1f2937' : 'rgba(255,255,255,0.3)',
    codeBg: isLight ? '#f4f5f7' : '#1a1a1a',
    codeText: isLight ? '#374151' : 'rgba(255,255,255,0.75)',
    detailBg: isLight ? '#ffffff' : '#141414',
    shadow: isLight ? 'rgba(0,0,0,0.04)' : 'rgba(0,0,0,0.4)',
  };

  const changeLanguage = (lng: string) => {
    i18n.changeLanguage(lng);
    persistUserLanguagePreference(lng);
  };

  const getEffectiveMarketplaceDiscount = (m?: MarketplaceModel | null): number | null => {
    if (!m) return null;
    const hasGlobal = Number(m.global_discount_enabled) === 1 && m.global_discount !== undefined && m.global_discount !== null;
    const hasSite = Number(m.site_discount_enabled) === 1 && m.site_discount !== undefined && m.site_discount !== null;
    const g = hasGlobal ? Number(m.global_discount) : null;
    const s = hasSite ? Number(m.site_discount) : null;
    const floor = s !== null && Number.isFinite(s) && s >= 0 ? s : null;
    if (g !== null && Number.isFinite(g) && g >= 0) {
      const eff = floor !== null ? Math.max(g, floor) : g;
      return Math.abs(eff - 1) > 1e-6 ? eff : null;
    }
    if (floor !== null && floor > 1 + 1e-6) {
      return floor;
    }
    return null;
  };

  const formatMarketplaceDiscountBadgeText = (discount: number): string => {
    if (discount <= 0) return tp('free');
    if (discount > 1) return `${Number(discount.toFixed(3))}${tp('unit_multiplier')}`;
    return tp('discount_percent_off', {
      percent: Number(((1 - discount) * 100).toFixed(1)),
      discount: Number((discount * 10).toFixed(2)),
    });
  };

  const formatMarketplacePrice = (price: number | string | undefined | null, discountModel?: MarketplaceModel, digits = 6) => {
    if (price === undefined || price === null || price === '') return '-';
    const num = Number(price);

    const getRaw = (n: number) => {
      if (isNaN(n)) return String(price);

      const formatNumber = (val: number) => parseFloat(val.toFixed(digits));

      if (selectedCurrencyCode === '') {
        return `${currencySymbol}${formatNumber(n)}`;
      }
      const curr = auxiliaryCurrencies.find(c => c.code === selectedCurrencyCode);
      if (curr) {
        return `${curr.symbol}${formatNumber(n * curr.exchange_rate)}`;
      }
      return `${currencySymbol}${formatNumber(n)}`;
    };

    const raw = getRaw(num);

    const effectiveDiscount = getEffectiveMarketplaceDiscount(discountModel);
    if (effectiveDiscount !== null) {
      if (isNaN(num)) return raw;
      const discountedNum = num * effectiveDiscount;
      const discountedRaw = getRaw(discountedNum);
      return (
        <span style={{ display: 'inline-flex', alignItems: 'baseline', gap: 6, whiteSpace: 'nowrap' }}>
          <span style={{
            textDecorationLine: 'line-through',
            textDecorationColor: isLight ? 'rgba(100, 116, 139, 0.55)' : 'rgba(255, 255, 255, 0.45)',
            textDecorationThickness: '1px',
            color: isLight ? '#4b5563' : 'rgba(255, 255, 255, 0.72)',
            fontSize: '0.95em',
            fontWeight: 500,
          }}>{raw}</span>
          <span style={{ color: '#ff4d4f', fontWeight: 700 }}>{discountedRaw}</span>
        </span>
      );
    }
    return raw;
  };

  const renderPriceGridTable = (title: string, items: { label: string, price: number | undefined | null, unit: string, itemDiscount?: number }[], variant: any, discount?: number, discountLabel: string = tp('offpeak_discount_prefix'), subRate: number = 1) => {
    const validItems = items.filter(item => item.price !== undefined && item.price !== null && Number(item.price) > 0);
    if (!validItems || validItems.length === 0) return null;
    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 2, width: '100%', maxWidth: 560 }}>
        {title && <div style={{ fontSize: 12, fontWeight: 600, color: c.text2, marginBottom: 1 }}>{title}</div>}
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', columnGap: 12, rowGap: 2 }}>
          {validItems.map((item, idx) => (
            <div key={idx} style={{ display: 'flex', alignItems: 'center', fontSize: 12, padding: '1px 0', gap: 4 }}>
              <span style={{ color: c.text3, whiteSpace: 'nowrap' }} title={item.label}>{item.label}:</span>
              <span style={{ color: c.text1, fontWeight: 600, flexShrink: 0 }}>
                {item.unit === tp('unit_multiplier')
                  ? `${Number(item.price).toFixed(2)}`
                  : formatMarketplacePrice(
                      Number(item.price) * subRate,
                      item.itemDiscount !== undefined ? { ...variant, global_discount: item.itemDiscount } : variant,
                    )}<span style={{ fontSize: 11, fontWeight: 400, marginLeft: 2, color: c.text3 }}>{item.unit}</span>
              </span>
            </div>
          ))}
        </div>
        {discount !== undefined && discount !== 1 && (
          <div style={{ fontSize: 11, color: c.text3, marginTop: 1 }}>{discountLabel}{discount}</div>
        )}
      </div>
    );
  };

  const renderCardPrice = (model: MarketplaceModel, featured = false) => {
    if (model.billing_invalid) return renderBillingInvalid();
    const formatPrice = (price: number | string | undefined | null, discountModel?: MarketplaceModel) => {
      const effectiveDiscount = getEffectiveMarketplaceDiscount(discountModel);
      if (!showDiscount) {
        const n = Number(price);
        if (effectiveDiscount !== null && !isNaN(n)) return formatMarketplacePrice(n * effectiveDiscount, undefined, 4);
        return formatMarketplacePrice(price, undefined, 4);
      }
      return formatMarketplacePrice(price, discountModel, 4);
    };
    const billing = model.billing || getFallbackBilling(model);
    if (!billing) return null;
    const { billing_type, prompt_rate, completion_rate, fixed_rate, duration_rate, billing_rule, extended_config, pricing_tiers } = billing;

    const ext = safeParseJson(extended_config);
    const tiers = safeParseJson(pricing_tiers, []);

    const priceStyle: React.CSSProperties = {
      fontSize: 12,
      fontWeight: 400,
      color: isLight ? '#1f2937' : 'rgba(255,255,255,0.92)',
      display: 'flex',
      alignItems: 'baseline',
      flexWrap: 'wrap',
      justifyContent: 'flex-start',
      columnGap: 6,
      rowGap: 2,
      lineHeight: 1.5,
      maxWidth: '100%',
      minWidth: 0,
    };
    const priceLine = (label: string, amount: React.ReactNode, unit: string) => (
      <span style={{ display: 'inline-flex', alignItems: 'baseline', gap: 6, maxWidth: '100%', whiteSpace: 'nowrap', fontSize: 12, color: priceStyle.color }}>
        <span style={{ color: isLight ? '#6b7280' : 'rgba(255,255,255,0.5)' }}>{label}</span>
        <span style={{ fontWeight: 400 }}>{amount}</span>
        <span style={{ color: isLight ? '#6b7280' : 'rgba(255,255,255,0.45)', fontSize: 11 }}>{unit}</span>
      </span>
    );

    const freeBadge = (
      <span style={{
        fontSize: 11,
        fontWeight: 500,
        padding: '2px 8px',
        borderRadius: 4,
        background: isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.06)',
        color: isLight ? '#1f2937' : 'rgba(255,255,255,0.88)',
        border: `1px solid ${isLight ? 'rgba(0,0,0,0.08)' : 'rgba(255,255,255,0.15)'}`
      }}>
        {tp('free')}
      </span>
    );

    let priceItems: React.ReactNode[] = [];
    let isFree = false;

    const pushMinPrice = (rates: (number | undefined | null)[], unit: string) => {
      const activeRates = rates.map(r => Number(r)).filter(r => !isNaN(r) && r > 0);
      if (activeRates.length > 0) {
        const amount = <span style={{ fontWeight: 400 }}>{formatPrice(Math.min(...activeRates), model)}</span>;
        priceItems.push(featured ? <>{amount}{unit}</> : <>{tp('starts_at')}: {amount}{unit}</>);
      }
    };

    if (billing_type === 'tokens') {
      if ((billing_rule === 'tiered' || billing_rule === 'doubao_chat') && Array.isArray(tiers) && tiers.length > 0) {
        const firstTier = tiers[0];
        const pRate = firstTier.prompt_rate !== undefined && firstTier.prompt_rate !== null ? Number(firstTier.prompt_rate) : 0;
        const cRate = firstTier.completion_rate !== undefined && firstTier.completion_rate !== null ? Number(firstTier.completion_rate) : 0;
        if (pRate === 0 && cRate === 0) isFree = true;
        if (pRate > 0) priceItems.push(priceLine(tp('input'), formatPrice(pRate, model), '/1M'));
        if (cRate > 0) priceItems.push(priceLine(tp('output'), formatPrice(cRate, model), '/1M'));
      }
      else if (billing_rule === 'kimi_chat' || billing_rule === 'claude') {
        const pRate = Number(prompt_rate) || 0;
        const cRate = Number(completion_rate) || 0;
        const isKimi = billing_rule === 'kimi_chat';
        const cacheRead = isKimi ? (Number(billing.cached_rate) || 0) : (Number(billing.claude_cache_read_rate) || Number(ext.claude_cache_read_rate) || 0);
        const write5m = isKimi ? (ext.cache_write_rate !== undefined ? Number(ext.cache_write_rate) : pRate) : (Number(billing.claude_cache_creation_rate) || Number(ext.claude_cache_creation_rate) || 0);
        const write1h = isKimi ? (ext.cache_write_1h_rate !== undefined ? Number(ext.cache_write_1h_rate) : (pRate * 2)) : (Number(ext.claude_cache_creation_1h_rate) || 0);
        if (pRate === 0 && cRate === 0 && cacheRead === 0 && write5m === 0 && write1h === 0) {
          isFree = true;
        } else {
          if (pRate > 0) priceItems.push(priceLine(tp('input'), formatPrice(pRate, model), '/1M'));
          if (cRate > 0) priceItems.push(priceLine(tp('output'), formatPrice(cRate, model), '/1M'));
          if (cacheRead > 0) priceItems.push(priceLine(tp('cache_read_short', '读缓'), formatPrice(cacheRead, model), '/1M'));
          if (write5m > 0) priceItems.push(priceLine(write1h > 0 ? tp('cache_write_5m_short', '写缓(5m)') : tp('cache_write_short', '写缓'), formatPrice(write5m, model), '/1M'));
          if (write1h > 0) priceItems.push(priceLine(tp('cache_write_1h_short', '写缓(1h)'), formatPrice(write1h, model), '/1M'));
        }
      }
      else if (billing_rule === 'gpt_billing') {
        const gptConfig = (ext && typeof ext.gpt_config === 'object' && ext.gpt_config !== null) ? ext.gpt_config : {};
        const items = [
          { key: 'input_text', label: tp('input_text_short') },
          { key: 'cached_input_text', label: tp('cached_input_text_short') },
          { key: 'input_image', label: tp('input_image_short') },
          { key: 'cached_input_image', label: tp('cached_input_image_short') },
          { key: 'output_image', label: tp('output_image_short') },
        ];
        const enabledItems = items.filter(item => gptConfig[item.key]?.enabled);
        if (enabledItems.length === 0) {
           priceItems.push(<span>{tp('gpt_billing_disabled')}</span>);
        } else {
           enabledItems.forEach(item => {
             priceItems.push(priceLine(item.label, formatPrice(gptConfig[item.key].rate, model), '/1M'));
           });
        }
      }
      else if (billing_rule === 'multimodal') {
        const pRate = prompt_rate !== undefined && prompt_rate !== null ? Number(prompt_rate) : 0;
        const imgRate = ext.image_prompt_rate !== undefined && ext.image_prompt_rate !== null ? Number(ext.image_prompt_rate) : 0;
        const cRate = completion_rate !== undefined && completion_rate !== null ? Number(completion_rate) : 0;
        const imgCompRate = ext.image_completion_rate !== undefined && ext.image_completion_rate !== null ? Number(ext.image_completion_rate) : 0;
        if (pRate === 0 && imgRate === 0 && cRate === 0 && imgCompRate === 0) isFree = true;
        if (pRate > 0) priceItems.push(priceLine(tp('text_prompt_short'), formatPrice(pRate, model), '/1M'));
        if (imgRate > 0) priceItems.push(priceLine(tp('image_prompt_short'), formatPrice(imgRate, model), '/1M'));
        if (cRate > 0) priceItems.push(priceLine(tp('text_completion_short', '文本出'), formatPrice(cRate, model), '/1M'));
        if (imgCompRate > 0) priceItems.push(priceLine(tp('image_completion_short', '图片出'), formatPrice(imgCompRate, model), '/1M'));
      }
      else if (billing_rule === 'seedance2.0' && ext.resolution_rates) {
        const rates = ext.resolution_rates || {};
        const resDiscMap = (model?.resolution_discounts && typeof model.resolution_discounts === 'object')
          ? model.resolution_discounts as Record<string, number>
          : null;
        if (resDiscMap && model?.global_discount_enabled === 1) {
          const baseDisc = model?.base_global_discount != null ? Number(model.base_global_discount) : (model?.global_discount ?? 1);
          let bestRaw: number | null = null;
          let bestDisc = baseDisc;
          let bestEff = Infinity;
          Object.keys(rates).forEach((k) => {
            const d = resDiscMap[k.toLowerCase()] !== undefined ? Number(resDiscMap[k.toLowerCase()]) : baseDisc;
            [rates[k]?.with_video, rates[k]?.without_video].forEach((r) => {
              const num = Number(r);
              if (!isNaN(num) && num > 0) {
                const eff = num * d;
                if (eff < bestEff) {
                  bestEff = eff;
                  bestRaw = num;
                  bestDisc = d;
                }
              }
            });
          });
          if (bestRaw !== null) {
            const amount = <span style={{ fontWeight: 400 }}>{formatPrice(bestRaw, { ...model, global_discount: bestDisc })}</span>;
            priceItems.push(featured ? <>{amount}/1M</> : <>{tp('starts_at')}: {amount}/1M</>);
          }
        } else {
          const values: number[] = [];
          Object.keys(rates).forEach(k => {
            values.push(rates[k]?.with_video, rates[k]?.without_video);
          });
          pushMinPrice(values, '/1M');
        }
      }
      else if (billing_rule === 'seedance1.5pro') {
        pushMinPrice([ext.base_rate, ext.audio_rate], '/1M');
      }
      else if (billing_rule === 'seedance1.0') {
        pushMinPrice([ext.online_rate, ext.offline_rate], '/1M');
      }
      else if (billing_rule === 'volcengine') {
        pushMinPrice([ext.volc_base_rate, ext.volc_audio_rate, ext.volc_video_rate], '/1M');
      }
    } else if (billing_type === 'requests') {
      if (billing_rule === 'image_resolution' && Array.isArray(tiers) && tiers.length > 0) {
        pushMinPrice(tiers.filter(t => t.enabled !== false).map(t => t.rate), tp('unit_per_image'));
      }
      else if (billing_rule === 'image_resolution_io' && Array.isArray(tiers) && tiers.length > 0) {
        pushMinPrice(tiers.filter(t => t.enabled !== false).flatMap(t => [t.input_rate, t.rate]), tp('unit_per_image'));
      }
      else if (billing_rule === 'image_size_pixel' && Array.isArray(tiers) && tiers.length > 0) {
        const rates: number[] = [];
        tiers.filter(t => t.enabled !== false).forEach(t => {
          rates.push(t.quality_pricing ? t.rate_low : t.rate);
        });
        pushMinPrice(rates, tp('unit_per_image'));
      }
      else if (billing_rule === 'volc_seedream_pro' && Array.isArray(tiers) && tiers.length > 0) {
        const rates = tiers.filter(t => t.enabled !== false).flatMap(t => [Number(t.rate), Number(t.layer_rate)]).filter(r => !isNaN(r) && r > 0);
        pushMinPrice(rates, tp('unit_per_image'));
      }
      else if (billing_rule === 'vidu_image') {
        const pt = ext.price_table || {};
        const disabledKeys: string[] = Array.isArray(ext.price_table_disabled) ? ext.price_table_disabled : [];
        const values = Object.entries(pt).filter(([k]) => !disabledKeys.includes(k)).map(([, v]) => v as number);
        pushMinPrice(values, tp('unit_per_image'));
      }
      else if (billing_rule === 'characters') {
        const fRate = fixed_rate !== undefined && fixed_rate !== null ? Number(fixed_rate) : 0;
        if (fRate === 0) isFree = true;
        else priceItems.push(<><span style={{ fontWeight: 400 }}>{formatPrice(fRate, model)}</span>{tp('unit_per_10k_chars')}</>);
      }
    } else if (billing_type === 'duration') {
      if ((billing_rule === 'video_resolution' || billing_rule === 'minimax_h3') && Array.isArray(tiers) && tiers.length > 0) {
        pushMinPrice(tiers.filter(t => t.enabled !== false).map(t => t.rate), tp('unit_per_second'));
      }
      else if ((billing_rule === 'video_seconds_io' || billing_rule === 'video_seconds_ref') && Array.isArray(tiers) && tiers.length > 0) {
        pushMinPrice(
          tiers.filter(t => t.enabled !== false).flatMap(t => [
            t.input_rate,
            t.rate,
            t.rate_no_ref,
            t.rate_ref,
            t.input_rate_ref,
          ].filter((p): p is number => typeof p === 'number' && p > 0)),
          tp('unit_per_second')
        );
      }
      else if (billing_rule === 'kling_video') {
        const pt = ext.price_table || {};
        const disabledKeys: string[] = Array.isArray(ext.price_table_disabled) ? ext.price_table_disabled : [];
        const values = Object.entries(pt).filter(([k]) => !disabledKeys.includes(k)).map(([, v]) => v as number);
        pushMinPrice(values, tp('unit_per_second'));
      }
      else if (billing_rule === 'kling4_video') {
        const rates = ext.resolution_rates || {};
        const values: number[] = [];
        Object.keys(rates).forEach(k => {
          if (rates[k]?.with_input !== undefined) values.push(rates[k].with_input);
          if (rates[k]?.no_input !== undefined) values.push(rates[k].no_input);
        });
        pushMinPrice(values, tp('unit_per_second'));
      }
      else if (billing_rule === 'vidu_video' || billing_rule === 'volc_enhance_cascade') {
        const pt = ext.price_table || {};
        const disabledKeys: string[] = Array.isArray(ext.price_table_disabled) ? ext.price_table_disabled : [];
        const values = Object.entries(pt).filter(([k]) => !disabledKeys.includes(k)).map(([, v]) => v as number);
        pushMinPrice(values, tp('unit_per_second'));
      }
      else if (billing_rule === 'video_quality' && Array.isArray(tiers) && tiers.length > 0) {
        pushMinPrice(tiers.filter(t => t.enabled !== false).map(t => t.rate), tp('unit_per_second'));
      }
      else if (billing_rule === 'fal_ref_video') {
        const dRate = duration_rate !== undefined && duration_rate !== null ? Number(duration_rate) : 0;
        if (dRate > 0) {
          priceItems.push(<><span style={{ fontWeight: 400 }}>{formatPrice(dRate, model)}</span>{tp('unit_per_second')}</>);
        }
      }
    }

    if (priceItems.length === 0 && !isFree) {
      if (billing_type === 'tokens') {
        const pRate = Number(prompt_rate) || 0;
        const cRate = Number(completion_rate) || 0;
        const cacheVal = Number(billing.cached_rate) || 0;
        const ccCreate = Number(billing.claude_cache_creation_rate) || Number(ext.claude_cache_creation_rate) || 0;
        const ccCreate1h = Number(ext.claude_cache_creation_1h_rate) || 0;
        const ccRead = Number(billing.claude_cache_read_rate) || Number(ext.claude_cache_read_rate) || 0;
        if (pRate === 0 && cRate === 0 && cacheVal === 0 && ccCreate === 0 && ccCreate1h === 0 && ccRead === 0) isFree = true;
        else {
          if (pRate > 0) priceItems.push(priceLine(tp('input'), formatPrice(pRate, model), '/1M'));
          if (cRate > 0) priceItems.push(priceLine(tp('output'), formatPrice(cRate, model), '/1M'));
          const effRead = cacheVal > 0 ? cacheVal : ccRead;
          if (effRead > 0) priceItems.push(priceLine(tp('cache_read_short', '读缓'), formatPrice(effRead, model), '/1M'));
          if (ccCreate > 0) {
            priceItems.push(priceLine(ccCreate1h > 0 ? tp('cache_write_5m_short', '写缓(5m)') : tp('cache_write_short', '写缓'), formatPrice(ccCreate, model), '/1M'));
          }
          if (ccCreate1h > 0) {
            priceItems.push(priceLine(tp('cache_write_1h_short', '写缓(1h)'), formatPrice(ccCreate1h, model), '/1M'));
          }
        }
      } else if (billing_type === 'requests') {
        const fRate = fixed_rate !== undefined && fixed_rate !== null ? Number(fixed_rate) : 0;
        if (fRate === 0) isFree = true;
        else {
          const isImageRule = ['per_image', 'vidu_image', 'image_resolution', 'image_size_pixel', 'volc_seedream_pro'].includes(billing_rule || '') ||
            (billing_rule || '').includes('image') || (model.type_name || '').includes('图片') || (model.type_name || '').includes('图像') || (model.type_name || '').includes('Image');
          const unit = isImageRule ? tp('unit_per_image') : tp('unit_per_request');
          priceItems.push(<><span style={{ fontWeight: 400 }}>{formatPrice(fRate, model)}</span>{unit}</>);
        }
      } else if (billing_type === 'duration') {
        const dRate = duration_rate !== undefined && duration_rate !== null ? Number(duration_rate) : 0;
        if (dRate === 0) isFree = true;
        else {
          priceItems.push(<><span style={{ fontWeight: 400 }}>{formatPrice(dRate, model)}</span>{tp('unit_per_second')}</>);
        }
      }
    }

    if (isFree) return freeBadge;

    if (priceItems.length > 0) {
      const visibleItems = priceItems.slice(0, featured ? 2 : 3);
      const hasMore = priceItems.length > visibleItems.length;
      
      return (
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'stretch', gap: 4, width: '100%', minWidth: 0, fontSize: 12, color: priceStyle.color }}>
          {visibleItems.map((item, idx) => (
             <div key={idx} style={{ maxWidth: '100%', lineHeight: 1.5 }}>{item}</div>
          ))}
          {hasMore && (
             <div style={{ fontSize: 11, color: isLight ? '#6b7280' : '#9ca3af', marginTop: 2 }}>
               +{priceItems.length - visibleItems.length} {tp('more', '更多')}...
             </div>
          )}
        </div>
      );
    }
    return null;
  };

  const renderOffpeakBadge = (model: MarketplaceModel) => {
    const variants = marketplaceVariants(model);
    let billingExt: any = null;
    for (const v of [model, ...variants]) {
      const b = v.billing || getFallbackBilling(v);
      const parsedExt = safeParseJson(b?.extended_config);
      if (parsedExt?.enable_time_multipliers && Array.isArray(parsedExt.time_multipliers) && parsedExt.time_multipliers.length > 0) {
        billingExt = parsedExt;
        break;
      }
    }
    let discountSched = parseVariantDiscountSchedule(model, i18n.language);
    if (!discountSched.enabled) {
      for (const v of variants) {
        const ds = parseVariantDiscountSchedule(v, i18n.language);
        if (ds.enabled) {
          discountSched = ds;
          break;
        }
      }
    }

    const hasMultipliers = !!billingExt;
    const hasDiscountSchedule = discountSched.enabled;
    if (!hasMultipliers && !hasDiscountSchedule) return null;

    const isDual = hasMultipliers && hasDiscountSchedule;
    const multipliers: any[] = billingExt?.time_multipliers || [];
    const billingRemOk = billingExt?.invert_remainder_multiplier != null
      && Number.isFinite(Number(billingExt.invert_remainder_multiplier))
      && Number(billingExt.invert_remainder_multiplier) >= 0;
    const billingCoveredDays = new Set<number>();
    multipliers.forEach((tm: any) => {
      if (tm?.all_day) normalizeWeekdays(tm?.days).forEach((d) => billingCoveredDays.add(d));
    });
    const showBillingRem = hasMultipliers && (billingRemOk || !!billingExt?.invert_time_multipliers || billingCoveredDays.size < 7);
    const billingRemRate = billingRemOk ? Number(billingExt.invert_remainder_multiplier) : invertRemainderFromExt(billingExt);

    const formatTooltipRate = (rate: number) => {
      if (isFreeTimeMultiplier(rate)) return tp('free');
      return `${formatRate(rate)}${tp('unit_multiplier')}${rate < 1 ? ` (${tp('discount_percent_off', { percent: Number(((1 - rate) * 100).toFixed(1)), discount: Number((rate * 10).toFixed(2)) })})` : ''}`;
    };

    return (
      <Tooltip title={
        <div style={{ padding: '6px 4px', minWidth: 230 }}>
          <div style={{ fontWeight: 600, fontSize: 12, marginBottom: 4, display: 'flex', alignItems: 'center', gap: 4 }}>
            <Clock style={{ width: 12, height: 12 }} />
            <span>
              {isDual
                ? tp('dual_schedule_enabled', '双重分时段优惠已启用')
                : hasMultipliers
                  ? tp('time_multiplier_enabled')
                  : tp('discount_schedule_enabled', '模型时段折扣已启用')}
            </span>
          </div>
          {isDual && (
            <div style={{ fontSize: 11, marginBottom: 6, opacity: 0.85, lineHeight: 1.4 }}>
              {tp('dual_schedule_formula', '实际结算价 = 默认原价 × 计费时段倍率 × 模型时段折扣')}
            </div>
          )}
          {hasMultipliers && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
              {isDual && (
                <div style={{ fontSize: 11, fontWeight: 600, opacity: 0.9, marginTop: 2 }}>
                  {tp('billing_time_multiplier_sub', '计费规则 · 时段倍率')}
                </div>
              )}
              {!isDual && billingExt.invert_time_multipliers ? (
                <div style={{ fontSize: 11, marginBottom: 4, opacity: 0.85, lineHeight: 1.4 }}>{tp('time_multiplier_invert_note')}</div>
              ) : null}
              {multipliers.map((tm: any, idx: number) => {
                const meta = marketplaceMultiplierMeta(tm, !!billingExt.invert_time_multipliers, i18n.language, Number(billingExt.invert_remainder_multiplier));
                return (
                  <div key={idx} style={{ fontSize: 11, display: 'flex', justifyContent: 'space-between', gap: 12 }}>
                    <span>{meta.allDay ? tp('all_day') : `${tm.start} ~ ${tm.end}`} {meta.daysText ? `(${meta.daysText})` : ''}</span>
                    <span style={{ color: meta.selected < 1 ? '#52c41a' : '#faad14', fontWeight: 600 }}>
                      {formatTooltipRate(meta.selected)}
                    </span>
                  </div>
                );
              })}
              {showBillingRem && (
                <div style={{ fontSize: 11, display: 'flex', justifyContent: 'space-between', gap: 12, paddingTop: 3, borderTop: '1px dashed rgba(255,255,255,0.15)' }}>
                  <span>{tp('unplanned_period', '标准时间段')}</span>
                  <span style={{ color: billingRemRate < 1 ? '#52c41a' : '#faad14', fontWeight: 600 }}>
                    {formatTooltipRate(billingRemRate)}
                  </span>
                </div>
              )}
            </div>
          )}
          {hasDiscountSchedule && (
            <div style={{
              display: 'flex',
              flexDirection: 'column',
              gap: 3,
              marginTop: isDual ? 6 : 0,
              paddingTop: isDual ? 6 : 0,
              borderTop: isDual ? '1px solid rgba(255,255,255,0.18)' : 'none',
            }}>
              {isDual && (
                <div style={{ fontSize: 11, fontWeight: 600, opacity: 0.9 }}>
                  {tp('model_discount_schedule_sub', '模型配置 · 时段折扣')}
                </div>
              )}
              {discountSched.slots.map((slot, idx) => (
                <div key={idx} style={{ fontSize: 11, display: 'flex', justifyContent: 'space-between', gap: 12 }}>
                  <span>{slot.all_day ? tp('all_day') : `${slot.start} ~ ${slot.end}`} {slot.daysText ? `(${slot.daysText})` : ''}</span>
                  <span style={{ color: slot.global_discount < 1 ? '#52c41a' : '#faad14', fontWeight: 600 }}>
                    {formatTooltipRate(slot.global_discount)}
                  </span>
                </div>
              ))}
              {discountSched.hasUnplannedPeriod && (
                <div style={{ fontSize: 11, display: 'flex', justifyContent: 'space-between', gap: 12, paddingTop: 3, borderTop: '1px dashed rgba(255,255,255,0.15)' }}>
                  <span>{tp('unplanned_period', '标准时间段')}</span>
                  <span style={{ color: discountSched.baseGlobalDiscount < 1 ? '#52c41a' : '#faad14', fontWeight: 600 }}>
                    {formatTooltipRate(discountSched.baseGlobalDiscount)}
                  </span>
                </div>
              )}
            </div>
          )}
        </div>
      }>
        <Tag
          color="orange"
          onClick={(e) => e.stopPropagation()}
          style={{ margin: 0, padding: '0 6px', fontSize: 11, lineHeight: '18px', height: 18, borderRadius: 4, border: 'none', flexShrink: 0 }}
        >
          {tp('offpeak_discount')}
        </Tag>
      </Tooltip>
    );
  };


  const renderBillingInvalid = () => (
    <Tooltip title={tp('billing_invalid_tip', '该模型未配置有效计费，暂不可调用')}>
      <span style={{
        fontSize: 11,
        fontWeight: 500,
        padding: '2px 8px',
        borderRadius: 4,
        background: 'rgba(255,77,79,0.08)',
        color: '#ff4d4f',
        border: '1px solid rgba(255,77,79,0.3)',
        whiteSpace: 'nowrap',
      }}>
        {tp('billing_invalid', '计费失效')}
      </span>
    </Tooltip>
  );

  const renderUniversalPriceDetailsInner = (variant: any, subRate: number = 1) => {
    if (variant.billing_invalid) return renderBillingInvalid();
    const billing = variant.billing || getFallbackBilling(variant);
    if (!billing) return <span style={{ color: c.text3, fontSize: 13 }}>{tp('unconfigured', 'Unconfigured')}</span>;

    const isTokens = billing.billing_type === 'tokens';
    const isRequests = billing.billing_type === 'requests';
    const isDuration = billing.billing_type === 'duration';
    const br = billing.billing_rule;

    const ext = safeParseJson(billing.extended_config);
    const tiers = safeParseJson(billing.pricing_tiers, []);

    const imgRefStr = ext.image_ref_multiplier && ext.image_ref_multiplier !== 1 ? ` (${tp('img2img', '图生图')}×${ext.image_ref_multiplier})` : '';

    // 1. 如果有阶梯定价
    if (tiers.length > 0) {
      // 如果是 tokens 相关的已知阶梯
      if (isTokens && (br === 'tiered' || br === 'doubao_chat')) {
        const hasFast = br === 'doubao_chat' && tiers.some((t: any) => t.fast_prompt_rate > 0 || t.fast_completion_rate > 0);
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {tiers.map((tier: any, idx: number) => {
              const pLabel = tier.max_prompt_tokens ? `≤${tier.max_prompt_tokens}k` : tp('unlimited');
              const cLabel = tier.max_completion_tokens ? `≤${tier.max_completion_tokens}k` : tp('unlimited');
              let title = `${tp('tier')} ${idx + 1}: ${tp('input')} ${pLabel}`;
              if (tier.max_completion_tokens) {
                title += ` | ${tp('output')} ${cLabel}`;
              }
              const items: any[] = [];
              items.push({ label: tp('text_input'), price: tier.prompt_rate, unit: '/ 1M' });
              items.push({ label: tp('text_output'), price: tier.completion_rate, unit: '/ 1M' });
              if (tier.audio_prompt_rate > 0) items.push({ label: tp('audio_input'), price: tier.audio_prompt_rate, unit: '/ 1M' });
              if (tier.cached_rate > 0) items.push({ label: tp('cache_read'), price: tier.cached_rate, unit: '/ 1M' });
              if (tier.cache_write_rate > 0) items.push({ label: tp('cache_write'), price: tier.cache_write_rate, unit: '/ 1M' });
              if (tier.audio_cached_rate > 0) items.push({ label: tp('audio_cache'), price: tier.audio_cached_rate, unit: '/ 1M' });
              if (hasFast) {
                if (tier.fast_prompt_rate > 0) items.push({ label: tp('low_latency_prompt'), price: tier.fast_prompt_rate || tier.prompt_rate, unit: '/ 1M' });
                if (tier.fast_completion_rate > 0) items.push({ label: tp('low_latency_completion'), price: tier.fast_completion_rate || tier.completion_rate, unit: '/ 1M' });
                if (tier.fast_cached_rate > 0) items.push({ label: tp('low_latency_cache'), price: tier.fast_cached_rate, unit: '/ 1M' });
                if (tier.fast_audio_prompt_rate > 0) items.push({ label: tp('low_latency_audio_prompt'), price: tier.fast_audio_prompt_rate, unit: '/ 1M' });
                if (tier.fast_audio_cached_rate > 0) items.push({ label: tp('low_latency_audio_cache'), price: tier.fast_audio_cached_rate, unit: '/ 1M' });
              }
              return <div key={idx}>{renderPriceGridTable(title, items, variant, undefined, undefined, subRate)}</div>;
            })}
          </div>
        );
      }

      // 其它阶梯情况，如 image_resolution, image_size_pixel, video_resolution, video_quality 等
      const items: any[] = [];
      const unit = isDuration ? tp('unit_per_second') : (isRequests ? tp('unit_per_image') : tp('unit_per_request'));
      let freeImageLine: React.ReactNode = null;
      if ((br === 'volc_seedream_pro' || br === 'minimax_h3' || br === 'video_seconds_io' || br === 'video_seconds_ref') && Number(billing.prompt_rate) > 0) {
        const freeCount = resolveFreeImageCount(ext.free_image_count, br);
        freeImageLine = (
          <>
            {tp('free_images_then_extra', { n: freeCount })}
            {formatMarketplacePrice(Number(billing.prompt_rate) * subRate, variant)}
            {tp('unit_per_image')}
          </>
        );
      }
      tiers.filter((tier: any) => tier.enabled !== false).forEach((tier: any) => {
        let label = tp('specification');
        if (tier.resolution && tier.fps_range) {
          label = `${tier.resolution} | ${tier.fps_range === '<=30' ? '≤30fps' : tier.fps_range === '>30' ? '>30fps' : tier.fps_range}`;
        } else if (tier.resolution) {
          label = tier.resolution;
        } else if (tier.size) {
          label = tier.size;
        } else if (tier.max_pixels_wan !== undefined) {
          label = `${tp('output_total_pixels')} <= ${tier.max_pixels_wan}${tp('ten_thousand_suffix')}`;
        }
        if (tier.quality_pricing) {
          items.push({ label: `${label} (${tp('quality_low')})`, price: tier.rate_low, unit });
          items.push({ label: `${label} (${tp('quality_medium')})`, price: tier.rate_medium, unit });
          items.push({ label: `${label} (${tp('quality_high')})`, price: tier.rate_high, unit });
        } else if (br === 'image_resolution_io') {
          items.push({ label: `${label} ${tp('image_input')}`, price: tier.input_rate, unit });
          items.push({ label: `${label} ${tp('image_generation')}`, price: tier.rate, unit });
        } else if (br === 'video_seconds_io') {
          items.push({ label: `${label} ${tp('video_input')}`, price: tier.input_rate, unit });
          items.push({ label: `${label} ${tp('video_generation')}`, price: tier.rate, unit });
        } else if (br === 'video_seconds_ref') {
          if (ext.enable_video_ref !== false && (tier.rate_no_ref !== undefined || tier.rate_ref !== undefined)) {
            items.push({ label: `${label} ${tp('no_ref_generation', '无参考-生成')}`, price: tier.rate_no_ref ?? tier.rate, unit });
            items.push({ label: `${label} ${tp('with_ref_input', '有参考-输入')}`, price: tier.input_rate_ref ?? tier.input_rate, unit });
            items.push({ label: `${label} ${tp('with_ref_generation', '有参考-生成')}`, price: tier.rate_ref ?? tier.rate, unit });
          } else {
            items.push({ label: `${label} ${tp('video_input')}`, price: tier.input_rate, unit });
            items.push({ label: `${label} ${tp('video_generation')}`, price: tier.rate, unit });
          }
        } else {
          items.push({ label, price: tier.rate, unit });
          if (br === 'volc_seedream_pro' && Number(tier.layer_rate) > 0) {
            items.push({ label: `${label} ${tp('layer_suffix', '(图层)')}`, price: tier.layer_rate, unit });
          }
        }
      });
      if (freeImageLine || items.length > 0) {
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            {freeImageLine && (
              <div style={{ fontSize: 11, color: c.text1, whiteSpace: 'normal', lineHeight: 1.5 }}>{freeImageLine}</div>
            )}
            {renderPriceGridTable(isDuration ? tp('duration_by_resolution') : '', items, variant, undefined, undefined, subRate)}
            {imgRefStr && <div style={{ fontSize: 11, color: c.text3, marginTop: 2 }}>{imgRefStr}</div>}
          </div>
        );
      }
    }

    // 2. 如果有自定义价格表 (price_table 或 resolution_rates)
    const pt = ext.price_table || ext.resolution_rates;
    if (pt && typeof pt === 'object') {
      const disabledKeys: string[] = Array.isArray(ext.price_table_disabled) ? ext.price_table_disabled : [];
      const items: any[] = [];
      const unit = isDuration ? tp('unit_per_second') : (variant.type_name?.includes('图') || isRequests ? tp('unit_per_image') : tp('unit_per_request'));

      // 扁平化遍历 price_table 或 resolution_rates
      const resDiscountMap = (br === 'seedance2.0' && variant?.resolution_discounts && typeof variant.resolution_discounts === 'object')
        ? variant.resolution_discounts as Record<string, number>
        : null;
      const baseGlobalDisc = variant?.base_global_discount != null ? Number(variant.base_global_discount) : variant?.global_discount;
      const traverseTable = (obj: any, prefix = '') => {
        Object.entries(obj).forEach(([key, val]) => {
          if (disabledKeys.includes(key)) return;
          const label = prefix ? `${prefix} | ${key}` : key;
          const friendlyLabel = translateBillingKey(label, tp, uiLang !== 'zh' && uiLang !== 'zh-TW');
          if (typeof val === 'number') {
            items.push({ label: friendlyLabel, price: val, unit });
          } else if (val && typeof val === 'object') {
            const valAny = val as any;
            // 针对 resolution_rates 中 with_video / without_video 的特殊处理
            if (valAny.with_video !== undefined || valAny.without_video !== undefined) {
              const normKey = key.trim().toLowerCase();
              const resDisc = resDiscountMap
                ? (resDiscountMap[normKey] !== undefined ? Number(resDiscountMap[normKey]) : baseGlobalDisc)
                : undefined;
              if (valAny.with_video !== undefined) items.push({ label: `${friendlyLabel} (${tp('with_video_short')})`, price: valAny.with_video, unit: '/ 1M', itemDiscount: resDisc });
              if (valAny.without_video !== undefined) items.push({ label: `${friendlyLabel} (${tp('without_video_short')})`, price: valAny.without_video, unit: '/ 1M', itemDiscount: resDisc });
            } else {
              traverseTable(val, label);
            }
          }
        });
      };

      traverseTable(pt);
      const discount = br === 'vidu_video' ? ext.offpeak_discount : undefined;
      if (items.length > 0) {
        return renderPriceGridTable('', items, variant, discount, undefined, subRate);
      }
    }

    // 针对特定规则且无大表的特探 (seedance1.5pro, seedance1.0, volcengine)
    if (isTokens) {
      if (br === 'kimi_chat' || br === 'claude') {
        const isKimi = br === 'kimi_chat';
        const pRate = Number(billing.prompt_rate) || 0;
        const cRate = Number(billing.completion_rate) || 0;
        const cacheRead = isKimi ? (Number(billing.cached_rate) || 0) : (Number(billing.claude_cache_read_rate) || Number(ext.claude_cache_read_rate) || 0);
        const write5m = isKimi ? (ext.cache_write_rate !== undefined ? Number(ext.cache_write_rate) : pRate) : (Number(billing.claude_cache_creation_rate) || Number(ext.claude_cache_creation_rate) || 0);
        const write1h = isKimi ? (ext.cache_write_1h_rate !== undefined ? Number(ext.cache_write_1h_rate) : (pRate * 2)) : (Number(ext.claude_cache_creation_1h_rate) || 0);
        const items: { label: string; price: number; unit: string }[] = [];
        if (pRate > 0) items.push({ label: tp('input', '输入'), price: pRate, unit: '/ 1M' });
        if (cRate > 0) items.push({ label: tp('output', '输出'), price: cRate, unit: '/ 1M' });
        if (cacheRead > 0) items.push({ label: tp('cache_read_short', '读缓'), price: cacheRead, unit: '/ 1M' });
        if (write5m > 0) items.push({ label: write1h > 0 ? tp('cache_write_5m_short', '写缓(5m)') : tp('cache_write_short', '写缓'), price: write5m, unit: '/ 1M' });
        if (write1h > 0) items.push({ label: tp('cache_write_1h_short', '写缓(1h)'), price: write1h, unit: '/ 1M' });
        const tableTitle = isKimi ? tp('kimi_chat_rates', 'Kimi 上下文缓存计费') : tp('claude_rates', 'Claude 计费');
        return renderPriceGridTable(tableTitle, items, variant, undefined, undefined, subRate);
      }
      if (br === 'gpt_billing') {
        const gptConfig = (ext && typeof ext.gpt_config === 'object' && ext.gpt_config !== null) ? ext.gpt_config : {};
        const items: { label: string; price: number; unit: string }[] = [];
        const configKeys = [
          { key: 'input_text', label: tp('input_text') },
          { key: 'cached_input_text', label: tp('input_text_cache') },
          { key: 'input_image', label: tp('input_image') },
          { key: 'cached_input_image', label: tp('input_image_cache') },
          { key: 'output_image', label: tp('output_image') },
        ];
        configKeys.forEach(item => {
          const cfg = gptConfig[item.key];
          if (cfg && cfg.enabled) {
            items.push({ label: item.label, price: cfg.rate, unit: '/ 1M' });
          }
        });
        return renderPriceGridTable('', items, variant, undefined, undefined, subRate);
      }
      if (br === 'multimodal') {
        const items = [
          { label: tp('text_input'), price: billing.prompt_rate, unit: '/ 1M' },
          { label: tp('image_input'), price: ext.image_prompt_rate, unit: '/ 1M' },
          { label: tp('text_output'), price: billing.completion_rate, unit: '/ 1M' },
          { label: tp('image_output', '图片输出'), price: ext.image_completion_rate, unit: '/ 1M' },
        ];
        return renderPriceGridTable('', items, variant, undefined, undefined, subRate);
      }
      if (br === 'seedance1.5pro') {
        const items = [
          { label: tp('with_audio'), price: ext.audio_rate, unit: '/ 1M' },
          { label: tp('without_audio'), price: ext.base_rate, unit: '/ 1M' }
        ];
        return renderPriceGridTable('', items, variant, ext.offline_discount, tp('offline_discount_prefix'), subRate);
      }
      if (br === 'seedance1.0') {
        const items = [
          { label: tp('online'), price: ext.online_rate, unit: '/ 1M' },
          { label: tp('offline'), price: ext.offline_rate, unit: '/ 1M' }
        ];
        return renderPriceGridTable('', items, variant, undefined, undefined, subRate);
      }
      if (br === 'volcengine') {
        const items = [];
        if (ext.volc_video_enabled) items.push({ label: tp('with_video'), price: ext.volc_video_rate, unit: '/ 1M' });
        if (ext.volc_audio_enabled) items.push({ label: tp('with_audio'), price: ext.volc_audio_rate, unit: '/ 1M' });
        if (ext.volc_base_enabled) items.push({ label: tp('text_only'), price: ext.volc_base_rate, unit: '/ 1M' });
        return renderPriceGridTable('', items, variant, undefined, undefined, subRate);
      }
    }

    if (isDuration && br === 'kling_video') {
      const mm = ext.mode_multipliers || {};
      const sm = ext.sound_multipliers || {};
      const vm = ext.video_ref_multipliers || {};
      const items = [
        { label: tp('base_unit_price'), price: billing.duration_rate, unit: tp('unit_per_second') },
        { label: tp('standard_mode_multiplier'), price: mm.std ?? 1.0, unit: tp('unit_multiplier') },
        { label: tp('pro_mode_multiplier'), price: mm.pro ?? 1.33, unit: tp('unit_multiplier') },
        { label: tp('resolution_2k_multiplier'), price: mm['2k'] ?? 1.5, unit: tp('unit_multiplier') },
        { label: tp('resolution_4k_multiplier'), price: mm['4k'] ?? 2.0, unit: tp('unit_multiplier') },
        { label: tp('sound_on_multiplier'), price: sm.on ?? 1.5, unit: tp('unit_multiplier') },
        { label: tp('sound_off_multiplier'), price: sm.off ?? 1.0, unit: tp('unit_multiplier') },
        { label: tp('with_reference_video_multiplier'), price: vm.yes ?? 1.5, unit: tp('unit_multiplier') },
        { label: tp('without_reference_video_multiplier'), price: vm.no ?? 1.0, unit: tp('unit_multiplier') }
      ];
      return renderPriceGridTable(tp('kling_video_multiplier_details'), items, variant, undefined, undefined, subRate);
    }

    if (isDuration && br === 'kling4_video') {
      const resRates = ext.resolution_rates || {};
      const items: any[] = [];
      Object.entries(resRates).forEach(([res, rates]: [string, any]) => {
        if (rates?.with_input !== undefined) {
          items.push({ label: `${res.toUpperCase()} (含视频输入)`, price: rates.with_input, unit: tp('unit_per_second') });
        }
        if (rates?.no_input !== undefined) {
          items.push({ label: `${res.toUpperCase()} (无视频输入)`, price: rates.no_input, unit: tp('unit_per_second') });
        }
      });
      return renderPriceGridTable('可灵 4.0 视频计费阶梯', items, variant, undefined, undefined, subRate);
    }

    // 3. 兜底，展示所有的非零基础单价
    const items: any[] = [];
    const pRate = billing.prompt_rate ? Number(billing.prompt_rate) : 0;
    const cRate = billing.completion_rate ? Number(billing.completion_rate) : 0;
    const fRate = billing.fixed_rate ? Number(billing.fixed_rate) : 0;
    const dRate = billing.duration_rate ? Number(billing.duration_rate) : 0;
    const cacheVal = Number(billing.cached_rate) || 0;
    const ccCreate = Number(billing.claude_cache_creation_rate) || Number(ext.claude_cache_creation_rate) || 0;
    const ccCreate1h = Number(ext.claude_cache_creation_1h_rate) || 0;
    const ccRead = Number(billing.claude_cache_read_rate) || Number(ext.claude_cache_read_rate) || 0;

    if (pRate > 0 || cRate > 0 || cacheVal > 0 || ccCreate > 0 || ccCreate1h > 0 || ccRead > 0) {
      if (pRate > 0) items.push({ label: tp('input', '输入'), price: pRate, unit: '/ 1M' });
      if (cRate > 0) items.push({ label: tp('output', '输出'), price: cRate, unit: '/ 1M' });
      const effRead = cacheVal > 0 ? cacheVal : ccRead;
      if (effRead > 0) items.push({ label: tp('cache_read_short', '读缓'), price: effRead, unit: '/ 1M' });
      if (ccCreate > 0) items.push({ label: ccCreate1h > 0 ? tp('cache_write_5m_short', '写缓(5m)') : tp('cache_write_short', '写缓'), price: ccCreate, unit: '/ 1M' });
      if (ccCreate1h > 0) items.push({ label: tp('cache_write_1h_short', '写缓(1h)'), price: ccCreate1h, unit: '/ 1M' });
    } else if (fRate > 0) {
      const isImageRule = ['per_image', 'vidu_image', 'image_resolution', 'image_size_pixel', 'volc_seedream_pro'].includes(br || '') ||
        (br || '').includes('image') ||
        (variant.type_name || '').includes('图片') ||
        (variant.type_name || '').includes('图像') ||
        (variant.type_name || '').includes('Image');
      const unit = isImageRule ? tp('unit_per_image') : (br === 'characters' ? tp('unit_per_10k_chars') : tp('unit_per_request'));
      items.push({ label: tp('fixed_rate'), price: fRate, unit });
    } else if (dRate > 0) {
      items.push({ label: tp('duration_rate'), price: dRate, unit: tp('unit_per_second') });
    }

    if (items.length > 0) {
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          {renderPriceGridTable('', items, variant, undefined, undefined, subRate)}
          {imgRefStr && <div style={{ fontSize: 11, color: c.text3, marginTop: 2 }}>{imgRefStr}</div>}
        </div>
      );
    }

    // 啥费率都没有
    return (
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 6 }}>
        <span style={{ fontSize: 16, fontWeight: 600 }}>{tp('free')}</span>
      </div>
    );
  };

  const getClockTimeInTz = (tz: string): string => {
    try {
      return new Date().toLocaleTimeString('zh-CN', {
        timeZone: tz,
        hour12: false,
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      });
    } catch (e) {
      return new Date().toLocaleTimeString('zh-CN', { hour12: false });
    }
  };

  const LiveTimeMultiplierFooter: React.FC<{
    siteTimezone: string;
    siteTimezoneOffset: string;
    isLight: boolean;
    c: any;
  }> = ({ siteTimezone, siteTimezoneOffset, isLight, c }) => {
    const [liveClock, setLiveClock] = useState(() => getClockTimeInTz(siteTimezone));

    useEffect(() => {
      const timer = setInterval(() => {
        setLiveClock(getClockTimeInTz(siteTimezone));
      }, 1000);
      return () => clearInterval(timer);
    }, [siteTimezone]);

    return (
      <div style={{
        display: 'flex',
        alignItems: 'center',
        gap: 6,
        padding: '5px 12px',
        background: isLight ? '#f9fafb' : 'rgba(255,255,255,0.015)',
        borderTop: `1px solid ${c.cardBorder}`,
        fontSize: 11,
        color: c.text3,
        lineHeight: 1.4,
      }}>
        <Info style={{ width: 11, height: 11, flexShrink: 0, color: c.text3 }} />
        <span>
          {tp('live_timezone_note_prefix', '计费时间段按站点运营时区')} ({tp('live_timezone_current_time', '当前时间')} <span style={{ color: c.text2 }} title={siteTimezone}>{liveClock} {siteTimezoneOffset}</span>)
        </span>
      </div>
    );
  };

  const renderTimeScheduleCard = (variant: any, ext: any) => {
    const multipliers = ext?.enable_time_multipliers && Array.isArray(ext?.time_multipliers) ? ext.time_multipliers : [];
    const showMultipliers = multipliers.length > 0;
    const lang = i18n.language;
    const discountSchedule = parseVariantDiscountSchedule(variant, lang);
    const showDiscountSchedule = discountSchedule.enabled;
    if (!showMultipliers && !showDiscountSchedule) return null;

    const isDual = showMultipliers && showDiscountSchedule;
    const isInvert = !!ext?.invert_time_multipliers;
    const remOk = ext?.invert_remainder_multiplier != null && Number.isFinite(Number(ext.invert_remainder_multiplier)) && Number(ext.invert_remainder_multiplier) >= 0;
    const billingCoveredDays = new Set<number>();
    multipliers.forEach((tm: any) => {
      if (tm?.all_day) normalizeWeekdays(tm?.days).forEach((d) => billingCoveredDays.add(d));
    });
    const hasRemainderRow = showMultipliers && (remOk || isInvert || billingCoveredDays.size < 7);
    const effectiveRemainderRate = remOk ? Number(ext.invert_remainder_multiplier) : invertRemainderFromExt(ext);

    const siteTimezone = settings?.site?.default_timezone?.trim() || 'Asia/Shanghai';
    const siteTimezoneOffset = (() => {
      if (!siteTimezone) return 'UTC+8';
      if (siteTimezone === 'UTC') return 'UTC+0';
      if (siteTimezone.startsWith('UTC') || siteTimezone.startsWith('GMT')) {
        return siteTimezone.replace('GMT', 'UTC');
      }
      if (siteTimezone.match(/^[+-]\d/)) {
        return `UTC${siteTimezone}`;
      }
      try {
        const date = new Date();
        const str = date.toLocaleString('en-US', { timeZone: siteTimezone, timeZoneName: 'shortOffset' });
        const match = str.match(/(GMT|UTC)([+-]\d{1,2}(:\d{2})?)/);
        if (match && match[2]) return `UTC${match[2]}`;
        if (str.includes('GMT') || str.includes('UTC')) return 'UTC+0';
      } catch (e) {}
      return 'UTC+8';
    })();

    const { weekday: curWeekday, minutes: curMinutes } = getSiteClockParts(siteTimezone);
    const activeMultiplierIdx = showMultipliers ? findActiveTimeRuleIndex(multipliers, curWeekday, curMinutes) : -2;
    const currentBillingRate = showMultipliers
      ? resolveTimeMultiplierAt(multipliers, isInvert, curWeekday, curMinutes, remOk ? Number(ext.invert_remainder_multiplier) : null)
      : 1;

    const discountRules: TimeMultiplierRule[] = discountSchedule.slots.map((s) => ({
      all_day: s.all_day,
      start: s.start,
      end: s.end,
      days: s.days,
      multiplier: s.global_discount,
    }));
    const activeDiscountIdx = showDiscountSchedule ? findActiveTimeRuleIndex(discountRules, curWeekday, curMinutes) : -2;
    const currentDiscountRate = showDiscountSchedule
      ? resolveTimeMultiplierAt(discountRules, false, curWeekday, curMinutes, discountSchedule.baseGlobalDiscount)
      : 1;
    const combinedRate = currentBillingRate * currentDiscountRate;
    const renderActiveSlotPill = () => (
      <span style={{
        fontSize: 10,
        fontWeight: 600,
        lineHeight: '15px',
        padding: '0 5px',
        borderRadius: 3,
        background: isLight ? 'rgba(0, 0, 0, 0.06)' : 'rgba(255, 255, 255, 0.12)',
        color: c.text1,
        border: `1px solid ${isLight ? 'rgba(0, 0, 0, 0.15)' : 'rgba(255, 255, 255, 0.2)'}`,
        whiteSpace: 'nowrap',
      }}>
        {tp('current_active_slot', '当前生效')}
      </span>
    );

    const renderRateBadge = (rate: number, _isRemainder = false) => {
      if (isFreeTimeMultiplier(rate)) {
        return (
          <span style={{
            display: 'inline-flex',
            alignItems: 'center',
            gap: 5,
            fontSize: 11,
            padding: '1px 7px',
            borderRadius: 4,
            background: isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.05)',
            color: c.text1,
            border: `1px solid ${c.cardBorder}`,
            lineHeight: '16px',
            whiteSpace: 'nowrap',
            fontWeight: 600,
          }}>
            {tp('free')}
          </span>
        );
      }
      const isDiscount = rate < 1;
      const isBaseline = Math.abs(rate - 1) < 0.001;

      if (isDiscount) {
        return (
          <span style={{
            display: 'inline-flex',
            alignItems: 'center',
            fontSize: 11,
            padding: '1px 7px',
            borderRadius: 4,
            background: isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.05)',
            color: c.text1,
            border: `1px solid ${c.cardBorder}`,
            lineHeight: '16px',
            whiteSpace: 'nowrap',
          }}>
            <span style={{ fontWeight: 600 }}>
              {formatRate(rate)}x
            </span>
          </span>
        );
      }

      if (isBaseline) {
        return (
          <span style={{
            display: 'inline-flex',
            alignItems: 'center',
            gap: 5,
            fontSize: 11,
            padding: '1px 7px',
            borderRadius: 4,
            background: 'transparent',
            color: c.text3,
            border: `1px solid ${c.cardBorder}`,
            lineHeight: '16px',
            whiteSpace: 'nowrap',
          }}>
            <span>
              1.00x
            </span>
          </span>
        );
      }

      return (
        <span style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 5,
          fontSize: 11,
          padding: '1px 7px',
          borderRadius: 4,
          background: isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.08)',
          color: c.text1,
          border: `1px solid ${c.cardBorder}`,
          lineHeight: '16px',
          whiteSpace: 'nowrap',
        }}>
          <span style={{ fontWeight: 600 }}>
            {formatRate(rate)}x
          </span>
          <span style={{ color: c.text3, fontSize: 10.5 }}>
            {tp('peak_rate', '高峰')}
          </span>
        </span>
      );
    };

    return (
      <div style={{
        marginTop: 8,
        borderRadius: 6,
        border: `1px solid ${c.cardBorder}`,
        background: isLight ? '#fafafa' : c.cardBg,
        overflow: 'hidden',
        whiteSpace: 'normal',
        minWidth: 280,
      }}>
        {/* 双重分时段同时生效时的顶部综合说明栏 */}
        {isDual && (
          <div style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            flexWrap: 'wrap',
            gap: 8,
            padding: '7px 10px',
            background: isLight ? 'rgba(0, 0, 0, 0.02)' : 'rgba(255, 255, 255, 0.03)',
            borderBottom: `1px solid ${c.cardBorder}`,
          }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', minWidth: 0 }}>
              <Clock style={{ width: 12, height: 12, color: c.text2 }} />
              <span style={{ fontSize: 12, fontWeight: 600, color: c.text1 }}>
                {tp('dual_schedule_title', '双重分时段规则同时生效')}
              </span>
              <span style={{ fontSize: 11, color: c.text3 }}>
                · {tp('dual_schedule_formula', '实际结算价 = 默认原价 × 计费时段倍率 × 模型时段折扣')}
              </span>
            </div>

            <span style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 5,
              fontSize: 11,
              padding: '2px 8px',
              borderRadius: 4,
              background: isLight ? '#ffffff' : 'rgba(255,255,255,0.06)',
              color: c.text1,
              border: `1px solid ${c.cardBorder}`,
              lineHeight: '16px',
              whiteSpace: 'nowrap',
              marginLeft: 'auto',
            }}>
              <span style={{ color: c.text3 }}>{tp('current_combined_rate', '当前综合')}:</span>
              <span style={{ fontWeight: 600 }}>
                {isFreeTimeMultiplier(combinedRate)
                  ? tp('free')
                  : `${formatRate(currentBillingRate)}x × ${formatRate(currentDiscountRate)}x = ${formatRate(combinedRate)}x`}
              </span>
            </span>
          </div>
        )}

        {/* 1. 计费规则 · 时段倍率 */}
        {showMultipliers && (
          <div>
            <div style={{
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              flexWrap: 'wrap',
              gap: 6,
              padding: '6px 10px',
              background: isLight ? '#f9fafb' : 'rgba(255,255,255,0.02)',
              borderBottom: `1px solid ${c.cardBorder}`,
            }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                {!isDual && <Clock style={{ width: 12, height: 12, color: c.text3 }} />}
                <span style={{ fontSize: 12, fontWeight: 600, color: c.text1 }}>
                  {isDual
                    ? tp('billing_time_multiplier_sub', '计费规则 · 时段倍率')
                    : tp('time_multiplier_schedule', '时段倍率规划')}
                </span>
                {!isDual && (
                  <span style={{ fontSize: 11, color: c.text3 }}>
                    · {tp('time_multiplier_formula', '实际价 = 默认价 × 当时倍率')}
                  </span>
                )}
              </div>
            </div>

            <div style={{ display: 'flex', flexDirection: 'column' }}>
              {multipliers.map((tm: any, idx: number) => {
                const meta = marketplaceMultiplierMeta(tm, isInvert, lang, Number(ext.invert_remainder_multiplier));
                const isLast = idx === multipliers.length - 1 && !hasRemainderRow;
                const isActive = idx === activeMultiplierIdx;

                return (
                  <div
                    key={idx}
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      gap: 10,
                      padding: '6px 10px',
                      borderBottom: isLast ? 'none' : `1px solid ${isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.03)'}`,
                      transition: 'background 0.15s ease',
                    }}
                    onMouseEnter={(e) => {
                      e.currentTarget.style.background = isLight ? '#f9fafb' : 'rgba(255,255,255,0.02)';
                    }}
                    onMouseLeave={(e) => {
                      e.currentTarget.style.background = 'transparent';
                    }}
                  >
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0, flexWrap: 'wrap' }}>
                      <span style={{
                        fontSize: 12,
                        fontWeight: 500,
                        color: c.text1,
                        whiteSpace: 'nowrap',
                      }}>
                        {meta.allDay ? tp('all_day', '全天') : `${tm.start} ~ ${tm.end}`}
                      </span>
                      <span style={{ color: c.textMuted, fontSize: 11 }}>·</span>
                      <span style={{ fontSize: 11.5, color: c.text3, whiteSpace: 'nowrap' }}>
                        {meta.daysText || tp('everyday', '每天')}
                      </span>
                      {isActive && renderActiveSlotPill()}
                    </div>

                    <div style={{ flexShrink: 0, marginLeft: 'auto' }}>
                      {renderRateBadge(meta.selected)}
                    </div>
                  </div>
                );
              })}

              {hasRemainderRow && (
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    gap: 10,
                    padding: '6px 10px',
                    borderTop: `1px dashed ${c.cardBorder}`,
                    transition: 'background 0.15s ease',
                  }}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = isLight ? '#f9fafb' : 'rgba(255,255,255,0.02)';
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = 'transparent';
                  }}
                >
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
                    <span style={{ fontSize: 12, color: c.text3, whiteSpace: 'nowrap' }}>
                      {tp('unplanned_period', '标准时间段')}
                    </span>
                    {activeMultiplierIdx === -1 && renderActiveSlotPill()}
                  </div>
                  <div style={{ flexShrink: 0, marginLeft: 'auto' }}>
                    {renderRateBadge(effectiveRemainderRate, true)}
                  </div>
                </div>
              )}
            </div>
          </div>
        )}

        {/* 2. 模型配置 · 时段折扣 */}
        {showDiscountSchedule && (
          <div style={{ borderTop: isDual ? `1px solid ${c.cardBorder}` : 'none' }}>
            <div style={{
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              flexWrap: 'wrap',
              gap: 6,
              padding: '6px 10px',
              background: isLight ? '#f9fafb' : 'rgba(255,255,255,0.02)',
              borderBottom: `1px solid ${c.cardBorder}`,
            }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                {!isDual && <Clock style={{ width: 12, height: 12, color: c.text3 }} />}
                <span style={{ fontSize: 12, fontWeight: 600, color: c.text1 }}>
                  {isDual
                    ? tp('model_discount_schedule_sub', '模型配置 · 时段折扣')
                    : tp('discount_schedule_title', '时段规划')}
                </span>
                {!isDual && (
                  <span style={{ fontSize: 11, color: c.text3 }}>
                    · {tp('discount_schedule_formula', '实际价 = 默认价 × 当时折扣（上方价格已实时折算）')}
                  </span>
                )}
              </div>
            </div>

            <div style={{ display: 'flex', flexDirection: 'column' }}>
              {discountSchedule.slots.map((slot, idx) => {
                const isLast = idx === discountSchedule.slots.length - 1 && !discountSchedule.hasUnplannedPeriod;
                const isActive = idx === activeDiscountIdx;

                return (
                  <div
                    key={idx}
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      gap: 10,
                      padding: '6px 10px',
                      borderBottom: isLast ? 'none' : `1px solid ${isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.03)'}`,
                      transition: 'background 0.15s ease',
                    }}
                    onMouseEnter={(e) => {
                      e.currentTarget.style.background = isLight ? '#f9fafb' : 'rgba(255,255,255,0.02)';
                    }}
                    onMouseLeave={(e) => {
                      e.currentTarget.style.background = 'transparent';
                    }}
                  >
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0, flexWrap: 'wrap' }}>
                      <span style={{
                        fontSize: 12,
                        fontWeight: 500,
                        color: c.text1,
                        whiteSpace: 'nowrap',
                      }}>
                        {slot.all_day ? tp('all_day', '全天') : `${slot.start} ~ ${slot.end}`}
                      </span>
                      <span style={{ color: c.textMuted, fontSize: 11 }}>·</span>
                      <span style={{ fontSize: 11.5, color: c.text3, whiteSpace: 'nowrap' }}>
                        {slot.daysText || tp('everyday', '每天')}
                      </span>
                      {isActive && renderActiveSlotPill()}
                    </div>

                    <div style={{ flexShrink: 0, marginLeft: 'auto' }}>
                      {renderRateBadge(slot.global_discount)}
                    </div>
                  </div>
                );
              })}

              {discountSchedule.hasUnplannedPeriod && (
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    gap: 10,
                    padding: '6px 10px',
                    borderTop: `1px dashed ${c.cardBorder}`,
                    transition: 'background 0.15s ease',
                  }}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = isLight ? '#f9fafb' : 'rgba(255,255,255,0.02)';
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = 'transparent';
                  }}
                >
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
                    <span style={{ fontSize: 12, color: c.text3, whiteSpace: 'nowrap' }}>
                      {tp('unplanned_period', '标准时间段')}
                    </span>
                    {activeDiscountIdx === -1 && renderActiveSlotPill()}
                  </div>
                  <div style={{ flexShrink: 0, marginLeft: 'auto' }}>
                    {renderRateBadge(discountSchedule.baseGlobalDiscount, true)}
                  </div>
                </div>
              )}
            </div>
          </div>
        )}

        {/* 底部实时时区时钟说明 */}
        <LiveTimeMultiplierFooter
          siteTimezone={siteTimezone}
          siteTimezoneOffset={siteTimezoneOffset}
          isLight={isLight}
          c={c}
        />
      </div>
    );
  };

  const renderUniversalPriceDetails = (variant: any) => {
    if (variant.billing_invalid) return renderBillingInvalid();
    const node = renderUniversalPriceDetailsInner(variant);
    const effectiveDiscount = getEffectiveMarketplaceDiscount(variant);
    const hasDiscount = effectiveDiscount !== null;
    const billing = variant.billing || getFallbackBilling(variant);
    const ext = safeParseJson(billing?.extended_config);
    const showMultipliers = Boolean(ext?.enable_time_multipliers && Array.isArray(ext.time_multipliers) && ext.time_multipliers.length > 0);
    const discountSchedule = parseVariantDiscountSchedule(variant, i18n.language);
    const showDiscountSchedule = discountSchedule.enabled;
    const showScheduleCard = showMultipliers || showDiscountSchedule;
    const allUpstreamChannels = uniqueSubchannelsByRate(
      (Array.isArray(variant.ha_subchannels) ? variant.ha_subchannels : []).filter((sub: any) => sub.is_ha || Number(sub.rate) !== 1)
    );
    const isOneRate = (rate: any) => Math.abs((Number(rate) || 1) - 1) < 1e-6;
    const hasDefaultHa = Boolean(
      variant.has_ha && (
        allUpstreamChannels.length === 0 ||
        allUpstreamChannels.some((sub: any) => sub.is_ha && isOneRate(sub.rate))
      )
    );
    const upstreamChannels = allUpstreamChannels.filter((sub: any) => !isOneRate(sub.rate));

    const showDefaultPricingHeader = hasDefaultHa || upstreamChannels.length > 0;
    const showUpstreamChannels = upstreamChannels.length > 0;

    const discountText = effectiveDiscount !== null
      ? formatMarketplaceDiscountBadgeText(effectiveDiscount)
      : '';

    const discountBadgeNode = hasDiscount ? (
      <span style={{
        fontSize: 10, fontWeight: 600, lineHeight: '16px',
        padding: '1px 6px', borderRadius: 4,
        background: 'linear-gradient(135deg, #ff4d4f 0%, #ff7875 100%)',
        color: '#fff',
        border: 'none',
        boxShadow: '0 1px 3px rgba(255,77,79,0.2)',
        display: 'inline-block'
      }}>
        {showDiscountSchedule
          ? tp('current_slot_discount', '当前时段 {{label}}', { label: discountText })
          : discountText}
      </span>
    ) : null;

    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {(showDefaultPricingHeader || hasDiscount) && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', marginBottom: 2 }}>
            {showDefaultPricingHeader && (
              <span style={{
                fontSize: 11, fontWeight: 600, padding: '2px 8px', borderRadius: 4,
                background: isLight ? '#09090b' : '#fafafa',
                color: isLight ? '#fafafa' : '#09090b',
                display: 'inline-block', lineHeight: '16px'
              }}>
                {tp('default_pricing', 'Default pricing')}
              </span>
            )}
            {hasDefaultHa && (
              <Tooltip title={tp('high_availability', '高可用')}>
                <span style={{
                  fontSize: 11,
                  padding: '1px 6px',
                  borderRadius: 4,
                  background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.05)',
                  color: isLight ? '#52525b' : c.text2,
                  border: `1px solid ${c.cardBorder}`,
                  lineHeight: '16px',
                  display: 'inline-block',
                  cursor: 'default',
                }}>
                  HA
                </span>
              </Tooltip>
            )}
            {discountBadgeNode}
          </div>
        )}
        {node}
        {showScheduleCard && renderTimeScheduleCard(variant, ext)}
        {showUpstreamChannels && (
          <div style={{
            marginTop: showScheduleCard ? 8 : 10,
            display: 'flex',
            flexDirection: 'column'
          }}>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              {upstreamChannels.map((sub: any, idx: number) => {
                const subRate = Number(sub.rate) || 1;
                return (
                  <div key={idx} style={{ 
                    fontSize: 13, 
                    display: 'flex', 
                    flexDirection: 'column',
                    gap: 4,
                    paddingBottom: idx === upstreamChannels.length - 1 ? 0 : 8,
                    paddingTop: idx === 0 ? 0 : 8,
                    borderBottom: idx === upstreamChannels.length - 1 ? 'none' : `1px dashed ${c.cardBorder}`
                  }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                        <Tooltip title={tp('high_availability', '高可用')}>
                          <span style={{ 
                            fontSize: 11, 
                            padding: '1px 6px', 
                            borderRadius: 4, 
                            background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.05)',
                            color: isLight ? '#52525b' : c.text2,
                            border: `1px solid ${c.cardBorder}`,
                            lineHeight: '16px',
                            display: 'inline-block',
                            cursor: 'default',
                          }}>
                            HA
                          </span>
                        </Tooltip>
                        <span style={{ 
                          fontSize: 11, 
                          padding: '1px 6px', 
                          borderRadius: 4, 
                          background: isLight ? '#f4f4f5' : 'rgba(255,255,255,0.05)',
                          color: isLight ? '#52525b' : c.text2,
                          border: `1px solid ${c.cardBorder}`,
                          lineHeight: '16px',
                          display: 'inline-block',
                        }}>
                          {formatRate(subRate)}x
                        </span>
                    </div>
                    <div style={{ background: isLight ? 'rgba(0,0,0,0.02)' : 'rgba(255,255,255,0.02)', padding: '6px 10px', borderRadius: 6 }}>
                      {renderUniversalPriceDetailsInner(variant, subRate)}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </div>
    );
  };

  const supportedLanguages = settings?.site?.supported_languages?.length ? settings.site.supported_languages : ['zh', 'en'];
  const implementedLangs = i18n.options.resources ? Object.keys(i18n.options.resources) : ['zh', 'en'];

  const langItems = supportedLanguages
    .filter(lng => implementedLangs.includes(lng))
    .map(lng => ({
      key: lng,
      label: LANG_NAME_MAP[lng] || lng,
      onClick: () => changeLanguage(lng),
    }));



  const announcementContent = (
    <div style={{ width: 360, display: 'flex', flexDirection: 'column' }}>
      <div style={{
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        padding: '16px 20px', borderBottom: isLight ? '1px solid #f0f0f0' : '1px solid rgba(255,255,255,0.08)'
      }}>
        <span style={{ color: c.text1, fontSize: 16, fontWeight: 500 }}>{_t('header.notifications', '通知')}</span>
      </div>

      <div style={{
        maxHeight: 480, overflowY: 'auto', padding: announcements.length > 0 ? '16px' : '60px 20px',
        display: 'flex', flexDirection: 'column',
      }}>
        {announcements.length > 0 ? (
          <List
            itemLayout="vertical"
            dataSource={announcements}
            split={false}
            renderItem={(item) => (
              <div
                key={item.id}
                style={{
                  background: isLight ? '#f9fafb' : 'rgba(255, 255, 255, 0.04)',
                  borderRadius: 12,
                  padding: '16px',
                  marginBottom: 12,
                  transition: 'all 0.3s cubic-bezier(0.4, 0, 0.2, 1)',
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.background = isLight ? '#f3f4f6' : 'rgba(255, 255, 255, 0.08)';
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.background = isLight ? '#f9fafb' : 'rgba(255, 255, 255, 0.04)';
                }}
              >
                <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 12 }}>
                  <div style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
                    {item.is_pinned === 1 && (
                      <div style={{
                        background: isLight ? 'rgba(24, 24, 27, 0.06)' : 'rgba(250, 250, 250, 0.1)',
                        color: isLight ? '#18181b' : '#fafafa',
                        border: isLight ? '1px solid rgba(24, 24, 27, 0.12)' : '1px solid rgba(250, 250, 250, 0.18)',
                        fontSize: 12,
                        padding: '2px 6px', borderRadius: 4, marginTop: 2, whiteSpace: 'nowrap',
                        flexShrink: 0
                      }}>
                        {_t('common.pinned', '置顶')}
                      </div>
                    )}
                    <div style={{ color: c.text1, fontSize: 15, fontWeight: 500, lineHeight: 1.5 }}>
                      {getAnnouncementLabel(item.title)}
                    </div>
                  </div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, color: c.text3, fontSize: 12 }}>
                    <ScheduleOutlined />
                    {formatApiDateTime(getAnnouncementDisplayTime(item), 'YYYY-MM-DD HH:mm')}
                  </div>
                </div>

                <div
                  className="quill-content"
                  dangerouslySetInnerHTML={{ __html: getAnnouncementLabel(item.content) }}
                  style={{
                    color: c.text2, fontSize: 13, lineHeight: 1.6,
                    background: 'transparent', padding: '0', overflowWrap: 'break-word', wordBreak: 'break-all'
                  }}
                />
              </div>
            )}
          />
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
            <BellOutlined style={{ fontSize: 64, color: isLight ? '#e5e7eb' : 'rgba(255,255,255,0.1)', marginBottom: 24 }} />
            <div style={{ color: isLight ? '#6b7280' : '#e5e5e5', fontSize: 15, fontWeight: 500, marginBottom: 8 }}>{_t('header.no_notifications', '你的通知将出现在这里')}</div>
            <div style={{ color: isLight ? '#9ca3af' : 'rgba(255,255,255,0.45)', fontSize: 13, lineHeight: 1.6, maxWidth: 260 }}>
              {_t('header.no_notifications_desc', '平台重要公告及更新内容将在这里展示，即可第一时间收到通知。')}
            </div>
          </div>
        )}
      </div>
    </div>
  );

  const showModelsSkeleton = loading && groupedModels.length === 0 && !forbidden;

  return (
    <>
      <style>{`
        .mp-top-nav-glass.ant-layout-header {
          line-height: inherit;
          -webkit-backdrop-filter: ${isLight ? 'blur(16px)' : 'blur(18px) saturate(180%)'} !important;
          backdrop-filter: ${isLight ? 'blur(16px)' : 'blur(18px) saturate(180%)'} !important;
        }
        .mp-search .ant-input-affix-wrapper { background: ${c.searchBg} !important; border: 1px solid ${c.searchBorder} !important; border-radius: 8px !important; height: 40px; font-size: 14px; }
        .mp-search .ant-input-affix-wrapper:hover, .mp-search .ant-input-affix-wrapper:focus-within { border-color: ${c.focusBorder} !important; }
        .mp-search .ant-input { background: transparent !important; color: ${c.text2} !important; }
        .mp-models-page { width: 100%; max-width: ${MP_GRID_MAX_WIDTH}px; margin-left: auto; margin-right: auto; }
        .mp-card { border: 1px solid ${c.cardBorder}; border-radius: 5px; padding: 15px; cursor: pointer; transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1); background: ${c.cardBg}; position: relative; font-size: 14px; font-weight: 400; }
        .mp-card h3 { font-size: 14px; font-weight: 400; }
        .mp-card:hover { border-color: ${isLight ? '#999999' : '#444448'} !important; background: ${c.cardHoverBg}; transform: translateY(-4px); box-shadow: 0 12px 30px ${c.shadow}; }
        .mp-card-icon { width: 36px; height: 36px; border-radius: 5px; display: flex; align-items: center; justify-content: center; font-size: 16px; flex-shrink: 0; }
        .mp-card-logo {
          width: 48px;
          height: 48px;
          border-radius: 5px;
          background: ${isLight ? '#ffffff' : 'rgba(255, 255, 255, 0.05)'};
          border: 1px solid ${isLight ? 'rgba(0, 0, 0, 0.08)' : 'rgba(255, 255, 255, 0.08)'};
          box-shadow: ${isLight ? '0 1px 3px rgba(0, 0, 0, 0.04)' : '0 2px 6px rgba(0, 0, 0, 0.2)'};
          display: flex;
          align-items: center;
          justify-content: center;
          flex-shrink: 0;
          overflow: hidden;
          transition: all 0.2s ease;
        }
        .mp-new-badge { font-size: 11px; font-weight: 600; line-height: 18px; padding: 0 8px; border-radius: 999px; background: #2563eb; color: #fff; flex-shrink: 0; }
        @keyframes mp-skel-shimmer {
          0% { background-position: 100% 0; }
          100% { background-position: -100% 0; }
        }
        .mp-skel {
          background-image: linear-gradient(90deg, ${isLight ? '#ececef 25%, #f7f7f8 50%, #ececef 75%' : 'rgba(255,255,255,0.05) 25%, rgba(255,255,255,0.10) 50%, rgba(255,255,255,0.05) 75%'});
          background-size: 200% 100%;
          animation: mp-skel-shimmer 1.15s ease-in-out infinite;
        }
        .mp-card.mp-skel-card:hover {
          transform: none !important;
          box-shadow: none !important;
          border-color: ${c.cardBorder} !important;
          background: ${c.cardBg} !important;
        }
        @keyframes mp-card-reveal {
          from { opacity: 0; transform: translateY(8px); }
          to { opacity: 1; transform: none; }
        }
        .mp-grid-reveal .mp-card {
          animation: mp-card-reveal 0.28s ease-out both;
        }
        .mp-sidebar-content { padding: 8px 0; overflow-y: auto; height: 100%; }
        .mp-sidebar-content::-webkit-scrollbar { width: 4px; }
        .mp-sidebar-content::-webkit-scrollbar-thumb { background: ${c.scrollThumb}; border-radius: 4px; }
        .mp-sidebar-title { font-size: 11px; font-weight: 600; color: ${c.text3}; text-transform: uppercase; letter-spacing: 0.5px; margin-bottom: 12px; padding: 0 20px; }
        .mp-sidebar-item { display: flex; align-items: center; gap: 8px; height: 36px; padding: 0 12px; margin: 2px 8px; border-radius: 8px; cursor: pointer; transition: background-color 0.12s, color 0.12s; font-size: 13.5px; color: ${isLight ? '#52525b' : 'rgba(255, 255, 255, 0.65)'}; }
        .mp-sidebar-item:hover { background: ${isLight ? 'rgba(0, 0, 0, 0.04)' : 'rgba(255, 255, 255, 0.08)'}; color: ${isLight ? '#18181b' : '#ffffff'}; }
        .mp-sidebar-item:active { background: ${isLight ? 'rgba(0, 0, 0, 0.1)' : 'rgba(255, 255, 255, 0.16)'}; }
        .mp-sidebar-item.active { background: ${isLight ? 'rgba(0, 0, 0, 0.06)' : 'rgba(255, 255, 255, 0.12)'} !important; color: ${isLight ? '#18181b' : '#ffffff'} !important; font-weight: 500; }
        .mp-sidebar-item.active .mp-sidebar-count { color: ${isLight ? 'rgba(0,0,0,0.65)' : 'rgba(255,255,255,0.85)'}; }
        .mp-sidebar-count { margin-left: auto; font-size: 12px; color: ${c.textMuted}; font-weight: 500; }
        .mp-sidebar-divider { height: 1px; background: ${c.cardBorder}; margin: 20px 8px; }
        
        @media (max-width: 767px) {
          .responsive-table-wrapper { border: none !important; background: transparent !important; box-shadow: none !important; overflow: visible !important; }
          .responsive-table-wrapper > div { overflow-x: visible !important; }
          .responsive-table { min-width: 100% !important; display: block; }
          .responsive-table thead { display: none; }
          .responsive-table tbody { display: block; }
          .responsive-table tr { display: block; background: ${c.cardBg} !important; border: 1px solid ${c.cardBorder} !important; border-radius: 8px; margin-bottom: 16px; padding: 0; box-shadow: 0 4px 12px rgba(0,0,0,0.05); }
          .responsive-table tr:hover { transform: none !important; }
          .responsive-table td { display: block; padding: 16px !important; border-bottom: 1px solid ${isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.06)'} !important; width: 100%; box-sizing: border-box; }
          .responsive-table td:last-child { border-bottom: none !important; }
          .responsive-table td::before { 
            content: attr(data-label); 
            display: block; 
            font-size: 11px; 
            font-weight: 600; 
            color: ${c.text3}; 
            margin-bottom: 8px; 
            text-transform: uppercase; 
          }
        }
      `}</style>

      <Layout style={{ height: '100vh', overflow: 'hidden', background: c.bg }}>
        <Sider
          trigger={null}
          collapsible
          collapsed={collapsed}
          theme={themeMode}
          width={240}
          collapsedWidth={screens.xs ? 0 : 68}
          style={{
            boxShadow: 'none',
            borderRight: isLight ? '1px solid #e4e4e7' : '1px solid #1f1f23',
            zIndex: screens.xs ? 1050 : 10,
            position: screens.xs ? 'fixed' : 'relative',
            height: '100%',
            left: 0,
            top: 0,
            bottom: 0,
            overflow: 'hidden',
            background: c.siderBg,
          }}
          className="custom-sider"
        >
          <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
            {/* Logo Area：展开态固定宽度裁切 + 与图标轨交叉淡入，避免文字挤压 */}
            <div
              style={{
                height: screens.xs ? 48 : 56,
                position: 'relative',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                padding: '0 8px',
                borderBottom: isLight ? '1px solid #e4e4e7' : '1px solid #1f1f23',
                cursor: 'pointer',
                overflow: 'hidden',
                flexShrink: 0,
              }}
              onClick={() => navigate('/dashboard')}
            >
              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 6,
                  justifyContent: 'center',
                  width: 224,
                  minWidth: 224,
                  maxWidth: 224,
                  opacity: collapsed ? 0 : 1,
                  transition: collapsed
                    ? 'opacity 0.1s ease'
                    : 'opacity 0.18s ease 0.12s',
                  pointerEvents: collapsed ? 'none' : 'auto',
                }}
              >
                {siteLogo ? (
                  <img src={siteLogo} alt="logo" style={{ width: 20, height: 20, objectFit: 'contain', flexShrink: 0 }} />
                ) : (
                  <div className="flex items-center justify-center w-5 h-5 rounded bg-primary text-primary-foreground flex-shrink-0">
                    <Terminal className="w-3 h-3" />
                  </div>
                )}
                <div
                  style={{
                    color: isLight ? '#1f2937' : '#fff',
                    margin: 0,
                    fontSize: siteName.length > 14 ? 12 : siteName.length > 10 ? 13 : siteName.length > 6 ? 14 : 16,
                    fontWeight: 700,
                    lineHeight: 1.2,
                    whiteSpace: 'nowrap',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    minWidth: 0,
                  }}
                  title={siteName}
                >
                  {siteName}
                </div>
              </div>
              {!screens.xs && (
                <div
                  style={{
                    position: 'absolute',
                    inset: 0,
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    opacity: collapsed ? 1 : 0,
                    transition: collapsed
                      ? 'opacity 0.18s ease 0.12s'
                      : 'opacity 0.1s ease',
                    pointerEvents: collapsed ? 'auto' : 'none',
                  }}
                >
                  {siteLogo ? (
                    <img src={siteLogo} alt="logo" style={{ width: 24, height: 24, objectFit: 'contain' }} />
                  ) : (
                    <div className="flex items-center justify-center w-6 h-6 rounded-md bg-primary text-primary-foreground">
                      <Terminal className="w-3.5 h-3.5" />
                    </div>
                  )}
                </div>
              )}
            </div>

            {/* Sidebar Content (Filters) */}
            <div className="mp-sidebar-content" style={{ flex: 1, overflowY: 'auto', overflowX: 'hidden', padding: '8px 0' }}>
              {trendingConfig?.enabled && (
                <>
                  <Tooltip title={collapsed && !screens.xs ? tp('trending_recommendations') : ""} placement="right">
                    <div
                      className={`mp-sidebar-item ${activeView === 'trending' ? 'active' : ''}`}
                      onClick={handleNavTrending}
                    >
                      <FireOutlined style={{ fontSize: 18 }} />
                      {!(collapsed && !screens.xs) && (
                        <>
                          {tp('trending_recommendations')}
                        </>
                      )}
                    </div>
                  </Tooltip>
                  <div className="mp-sidebar-divider" />
                </>
              )}

              <Tooltip title={collapsed && !screens.xs ? tp('all_models') : ""} placement="right">
                <div
                  className={`mp-sidebar-item ${activeView === 'models' && selectedType === null ? 'active' : ''}`}
                  onClick={handleNavAllModels}
                >
                  <AppstoreOutlined style={{ fontSize: 18 }} />
                  {!(collapsed && !screens.xs) && (
                    <>
                      {tp('all_models')}
                      <span className="mp-sidebar-count">{showModelsSkeleton ? '' : modelsMatchingProviders.length}</span>
                    </>
                  )}
                </div>
              </Tooltip>
              {showModelsSkeleton
                ? [0, 1, 2, 3].map(i => (
                  <div key={`skel-nav-${i}`} className="mp-sidebar-item" style={{ pointerEvents: 'none' }}>
                    <MpSkel width={16} height={16} radius={6} />
                    {!(collapsed && !screens.xs) && <MpSkel width={`${52 + (i % 3) * 10}%`} height={12} radius={6} />}
                  </div>
                ))
                : types.map(t => {
                const typeLabel = displayClassName(t.name, t.name_en, t.name);
                return (
                <Tooltip key={t.id} title={collapsed && !screens.xs ? typeLabel : ""} placement="right">
                  <div
                    className={`mp-sidebar-item ${activeView === 'models' && selectedType !== null && String(selectedType) === String(t.id) ? 'active' : ''}`}
                    onClick={() => handleNavType(t)}
                  >
                    <span style={{ fontSize: 18, display: 'flex', alignItems: 'center' }}>{getTypeIcon(t.name)}</span>
                    {!(collapsed && !screens.xs) && (
                      <>
                        {typeLabel}
                        <span className="mp-sidebar-count">{typeCounts[t.id] || 0}</span>
                      </>
                    )}
                  </div>
                </Tooltip>
                );
              })}
            </div>
          </div>
        </Sider>

        <Layout style={{
          marginLeft: (screens.xs || collapsed) ? 0 : 0,
          background: c.bg,
          position: 'relative',
          overflow: 'hidden',
          display: 'flex',
          flexDirection: 'column',
        }}>
          <Header
            className="mp-top-nav-glass"
            style={{
              position: 'absolute',
              top: 0,
              left: 0,
              right: 0,
              zIndex: 100,
              padding: '0 12px',
              background: isLight ? 'rgba(255, 255, 255, 0.92)' : 'rgba(0, 0, 0, 0.48)',
              backdropFilter: isLight ? 'blur(16px)' : 'blur(18px) saturate(180%)',
              WebkitBackdropFilter: isLight ? 'blur(16px)' : 'blur(18px) saturate(180%)',
              height: screens.xs ? 48 : 56,
              lineHeight: (screens.xs ? 48 : 56) + 'px',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              paddingRight: screens.xs ? 8 : 24,
              borderBottom: isLight
                ? '1px solid rgba(0, 0, 0, 0.08)'
                : '1px solid rgba(255, 255, 255, 0.08)',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', minWidth: 0, flexShrink: 1, overflow: 'hidden' }}>
              <Button
                type="text"
                icon={<SidebarIcon size={16} />}
                onClick={() => handleCollapsedChange(!collapsed)}
                style={{
                  width: 32,
                  height: 32,
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  color: themeMode === 'light' ? '#71717a' : '#a1a1aa',
                  borderRadius: 6,
                  flexShrink: 0
                }}
              />
              {screens.xs && (
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 6,
                    marginLeft: 6,
                    minWidth: 0,
                    overflow: 'hidden',
                    cursor: 'pointer'
                  }}
                  onClick={() => navigate('/dashboard')}
                >
                  {siteLogo ? (
                    <img src={siteLogo} alt="logo" style={{ width: 18, height: 18, objectFit: 'contain', flexShrink: 0 }} />
                  ) : (
                    <div className="flex items-center justify-center w-4.5 h-4.5 rounded bg-primary text-primary-foreground flex-shrink-0">
                      <Terminal className="w-3 h-3" />
                    </div>
                  )}
                  <span
                    style={{
                      color: c.text1,
                      fontSize: siteName.length > 14 ? 11 : siteName.length > 10 ? 12 : 13,
                      fontWeight: 600,
                      whiteSpace: 'nowrap',
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                      lineHeight: 1.2
                    }}
                    title={siteName}
                  >
                    {siteName}
                  </span>
                </div>
              )}
            </div>

            <div
              className="header-nav-actions"
              style={{ position: 'relative', display: 'flex', alignItems: 'center', gap: screens.xs ? 2 : 6, flexShrink: 0, lineHeight: 1 }}
              ref={(container) => {
                if (!container) return;
                requestAnimationFrame(() => {
                  if (!container.isConnected) return;
                  const pill = container.querySelector('.header-nav-hover-pill') as HTMLElement | null;
                  if (!pill) return;
                  const pt = (window as any).__tkeHeaderPointer as { x: number; y: number } | undefined;
                  const hovered = (
                    (pt ? document.elementFromPoint(pt.x, pt.y)?.closest('.ant-btn') : null) ||
                    container.querySelector('.ant-btn:hover')
                  ) as HTMLElement | null;
                  if (hovered && container.contains(hovered) && !hovered.closest('.header-avatar-wrap')) {
                    const cRect = container.getBoundingClientRect();
                    const bRect = hovered.getBoundingClientRect();
                    if (bRect.width > 0 && bRect.height > 0) {
                      const x = bRect.left - cRect.left;
                      const y = bRect.top - cRect.top;
                      const isCircle = hovered.classList.contains('ant-btn-circle');
                      const radius = isCircle ? `${bRect.height / 2}px` : '10px';
                      const baseTransform = `translate3d(${x}px, ${y}px, 0)`;
                      pill.dataset.lastTransform = baseTransform;
                      if (pill.dataset.visible !== '1') {
                        pill.style.transition = 'none';
                      }
                      pill.style.width = `${bRect.width}px`;
                      pill.style.height = `${bRect.height}px`;
                      pill.style.borderRadius = radius;
                      pill.style.transform = `${baseTransform} scale(1)`;
                      pill.style.opacity = '1';
                      pill.dataset.visible = '1';
                    }
                  }
                });
              }}
              onMouseDown={(e) => {
                (window as any).__tkeHeaderPointer = { x: e.clientX, y: e.clientY };
              }}
              onMouseMove={(e) => {
                (window as any).__tkeHeaderPointer = { x: e.clientX, y: e.clientY };
              }}
              onMouseOver={(e) => {
                (window as any).__tkeHeaderPointer = { x: e.clientX, y: e.clientY };
                const container = e.currentTarget;
                const pill = container.querySelector('.header-nav-hover-pill') as HTMLElement | null;
                if (!pill) return;
                const btn = (e.target as HTMLElement).closest('.ant-btn') as HTMLElement | null;
                if (!btn || !container.contains(btn) || btn.closest('.header-avatar-wrap')) {
                  if ((e.target as HTMLElement).closest('.header-avatar-wrap')) {
                    pill.dataset.visible = '0';
                    pill.style.opacity = '0';
                    if (pill.dataset.lastTransform) {
                      pill.style.transform = `${pill.dataset.lastTransform} scale(0.92)`;
                    }
                  }
                  return;
                }
                const cRect = container.getBoundingClientRect();
                const bRect = btn.getBoundingClientRect();
                const x = bRect.left - cRect.left;
                const y = bRect.top - cRect.top;
                const isCircle = btn.classList.contains('ant-btn-circle');
                const radius = isCircle ? `${bRect.height / 2}px` : '10px';
                const baseTransform = `translate3d(${x}px, ${y}px, 0)`;
                pill.dataset.lastTransform = baseTransform;
                if (pill.dataset.visible !== '1') {
                  pill.style.transition = 'none';
                  pill.style.width = `${bRect.width}px`;
                  pill.style.height = `${bRect.height}px`;
                  pill.style.borderRadius = radius;
                  pill.style.transform = `${baseTransform} scale(0.92)`;
                  void pill.offsetWidth;
                  pill.style.transition = 'transform 0.32s cubic-bezier(0.22, 1, 0.36, 1), width 0.32s cubic-bezier(0.22, 1, 0.36, 1), height 0.32s cubic-bezier(0.22, 1, 0.36, 1), border-radius 0.32s cubic-bezier(0.22, 1, 0.36, 1), opacity 0.2s ease';
                  pill.style.transform = `${baseTransform} scale(1)`;
                  pill.style.opacity = '1';
                  pill.dataset.visible = '1';
                } else {
                  pill.style.transition = 'transform 0.32s cubic-bezier(0.22, 1, 0.36, 1), width 0.32s cubic-bezier(0.22, 1, 0.36, 1), height 0.32s cubic-bezier(0.22, 1, 0.36, 1), border-radius 0.32s cubic-bezier(0.22, 1, 0.36, 1), opacity 0.2s ease';
                  pill.style.width = `${bRect.width}px`;
                  pill.style.height = `${bRect.height}px`;
                  pill.style.borderRadius = radius;
                  pill.style.transform = `${baseTransform} scale(1)`;
                  pill.style.opacity = '1';
                }
              }}
              onMouseLeave={(e) => {
                (window as any).__tkeHeaderPointer = { x: e.clientX, y: e.clientY };
                const pill = e.currentTarget.querySelector('.header-nav-hover-pill') as HTMLElement | null;
                if (!pill) return;
                pill.dataset.visible = '0';
                pill.style.opacity = '0';
                if (pill.dataset.lastTransform) {
                  pill.style.transform = `${pill.dataset.lastTransform} scale(0.92)`;
                }
              }}
            >
              <style>{`
                .header-badge.ant-badge {
                  display: flex !important;
                  align-items: center;
                  justify-content: center;
                  height: 40px;
                }
                .header-nav-hover-pill {
                  position: absolute;
                  top: 0;
                  left: 0;
                  opacity: 0;
                  pointer-events: none;
                  z-index: 0;
                  will-change: transform, width, height, border-radius, opacity;
                  background: ${
                    themeMode === 'light'
                      ? 'rgba(0, 0, 0, 0.075)'
                      : 'rgba(255, 255, 255, 0.085)'
                  };
                  backdrop-filter: blur(10px) saturate(150%);
                  -webkit-backdrop-filter: blur(10px) saturate(150%);
                }
                .header-nav-actions .ant-btn:not(.ant-btn-primary) {
                  position: relative;
                  z-index: 1;
                  display: inline-flex !important;
                  align-items: center !important;
                  justify-content: center !important;
                  line-height: 1 !important;
                  background: transparent !important;
                  transition: color 0.22s ease, transform 0.22s ease !important;
                }
                .header-nav-actions .ant-btn:not(.ant-btn-primary):hover,
                .header-nav-actions .ant-btn:not(.ant-btn-primary):focus,
                .header-nav-actions .ant-btn:not(.ant-btn-primary):active {
                  background: transparent !important;
                }
                .header-nav-actions .ant-btn .ant-btn-icon {
                  display: inline-flex !important;
                  align-items: center !important;
                  justify-content: center !important;
                  line-height: 1 !important;
                  margin-inline-end: 0 !important;
                }
                .header-nav-actions .ant-btn > span:not(.ant-btn-icon) {
                  display: inline-flex !important;
                  align-items: center !important;
                  line-height: 1 !important;
                  margin-inline-start: 0 !important;
                }
                .header-nav-actions .ant-btn svg {
                  display: block;
                  transition: transform 0.28s cubic-bezier(0.34, 1.56, 0.64, 1);
                }
                .header-nav-actions .ant-btn:hover svg {
                  transform: scale(1.06);
                }
                .header-nav-actions .ant-btn:active svg {
                  transform: scale(0.95);
                }
              `}</style>
              <div className="header-nav-hover-pill" />
              <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
                {headerNavReady && (
                  <Button
                    type="text"
                    href="/home/models"
                    icon={
                      <svg
                        width="20"
                        height="20"
                        viewBox="0 0 24 24"
                        fill="none"
                        xmlns="http://www.w3.org/2000/svg"
                      >
                        <path d="M12 2.5L19.5 6.5L12 10.5L4.5 6.5Z" fill={themeMode === 'light' ? '#e0e0e0' : '#424242'} />
                        <path d="M3.5 8L11 12V20.5L3.5 16.5Z" fill={themeMode === 'light' ? '#b0b0b0' : '#555555'} />
                        <path d="M13 12L20.5 8V16.5L13 20.5Z" fill={themeMode === 'light' ? '#757575' : '#9e9e9e'} />
                      </svg>
                    }
                    style={{
                      color: themeMode === 'light' ? '#1f2937' : '#fff',
                      gap: 4,
                      fontSize: 14,
                      fontWeight: 500,
                      height: screens.xs ? 34 : 40,
                      width: screens.xs ? 34 : undefined,
                      padding: screens.xs ? 0 : '0 10px 0 7px',
                    }}
                    onClick={(e) => {
                      if (!e.metaKey && !e.ctrlKey) {
                        e.preventDefault();
                        handleNavHome();
                      }
                    }}
                  >
                    {!screens.xs && (
                      <span>{_t('menu.model_marketplace', '模型广场')}</span>
                    )}
                  </Button>
                )}

                {headerNavReady && (
                  <Button
                    type="text"
                    href="/docs"
                    icon={
                      <svg
                        width="20"
                        height="20"
                        viewBox="0 0 24 24"
                        fill="none"
                        xmlns="http://www.w3.org/2000/svg"
                      >
                        <rect x="8" y="2.5" width="2.6" height="5.8" rx="1.2" fill={themeMode === 'light' ? '#757575' : '#9e9e9e'} />
                        <rect x="13.4" y="2.5" width="2.6" height="5.8" rx="1.2" fill={themeMode === 'light' ? '#757575' : '#9e9e9e'} />
                        <path d="M4.5 7.5H19.5V10H4.5V7.5Z" fill={themeMode === 'light' ? '#e0e0e0' : '#2e2e2e'} />
                        <path d="M5 10H12V21C7.8 21 5 18.6 5 15.2V10Z" fill={themeMode === 'light' ? '#b0b0b0' : '#555555'} />
                        <path d="M12 10H19V15.2C19 18.6 16.2 21 12 21V10Z" fill={themeMode === 'light' ? '#757575' : '#9e9e9e'} />
                        <path d="M8.5 12.2V16.8" stroke={themeMode === 'light' ? '#757575' : '#2e2e2e'} strokeWidth="1.4" strokeLinecap="round" />
                        <path d="M15.5 12.2V16.8" stroke={themeMode === 'light' ? '#b0b0b0' : '#555555'} strokeWidth="1.4" strokeLinecap="round" />
                      </svg>
                    }
                    style={{
                      color: themeMode === 'light' ? '#1f2937' : '#fff',
                      gap: 4,
                      fontSize: 14,
                      fontWeight: 500,
                      height: screens.xs ? 34 : 40,
                      width: screens.xs ? 34 : undefined,
                      padding: screens.xs ? 0 : '0 10px 0 7px',
                    }}
                    onClick={(e) => {
                      if (!e.metaKey && !e.ctrlKey) {
                        e.preventDefault();
                        navigate('/docs');
                      }
                    }}
                  >
                    {!screens.xs && (
                      <span>{_t('menu.relay_api', 'API教程')}</span>
                    )}
                  </Button>
                )}
              </div>

              {enableThemeToggle && (
                <Tooltip
                  title={themeMode === 'light' ? _t('header.switch_dark_mode', '切换暗色模式') : _t('header.switch_light_mode', '切换亮色模式')}
                  placement="bottom"
                  color={themeMode === 'light' ? '#fff' : '#2b2b2b'}
                  styles={{ container: { color: themeMode === 'light' ? '#1f2937' : '#fff' } }}
                >
                  <Button
                    type="text"
                    shape="circle"
                    onClick={toggleTheme}
                    icon={
                      themeMode === 'light'
                        ? (
                          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
                            <path d="M21 12.79A9 9 0 1111.21 3 7 7 0 0021 12.79Z" fill="#757575" />
                          </svg>
                        )
                        : (
                          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
                            <circle cx="12" cy="12" r="6" fill="#555555" />
                            <path d="M12 2v2m0 16v2M4.93 4.93l1.41 1.41m11.32 11.32l1.41 1.41M2 12h2m16 0h2M4.93 19.07l1.41-1.41m11.32-11.32l1.41-1.41" stroke="#9e9e9e" strokeWidth="2.2" strokeLinecap="round" />
                          </svg>
                        )
                    }
                    style={{ color: themeMode === 'light' ? '#1f2937' : '#fff', width: screens.xs ? 34 : 40, height: screens.xs ? 34 : 40, padding: 0 }}
                  />
                </Tooltip>
              )}

              {enableMultilingual && (
                <Dropdown menu={{ items: langItems, style: { maxHeight: 176, overflowY: 'auto' } }} placement="bottomRight">
                  <Button
                    type="text"
                    shape="circle"
                    icon={
                      <svg width="20" height="20" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
                        <circle cx="12" cy="12" r="8.5" stroke={themeMode === 'light' ? '#757575' : '#9e9e9e'} strokeWidth="2" />
                        <path d="M3.5 12h17" stroke={themeMode === 'light' ? '#b0b0b0' : '#555555'} strokeWidth="2" strokeLinecap="round" />
                        <ellipse cx="12" cy="12" rx="3.5" ry="8.5" stroke={themeMode === 'light' ? '#b0b0b0' : '#555555'} strokeWidth="2" />
                      </svg>
                    }
                    style={{ color: themeMode === 'light' ? '#1f2937' : '#fff', width: screens.xs ? 34 : 40, height: screens.xs ? 34 : 40, padding: 0 }}
                  />
                </Dropdown>
              )}

              <Popover
                content={announcementContent}
                trigger="click"
                placement="bottomRight"
                overlayClassName="custom-premium-popover"
                open={announcementsDrawerVisible}
                onOpenChange={setAnnouncementsDrawerVisible}
                styles={{ container: { padding: 0, background: 'transparent', boxShadow: 'none' } }}
                motion={{ motionName: '' }}
                arrow={false}
              >
                <Tooltip
                  title={_t('header.notifications', '通知')}
                  placement="bottom"
                  color={themeMode === 'light' ? '#fff' : '#2b2b2b'}
                  styles={{ container: { color: themeMode === 'light' ? '#1f2937' : '#fff' } }}
                >
                  <Badge count={unreadCount} overflowCount={99} offset={[-4, 4]} className="header-badge">
                    <Button
                      type="text"
                      shape="circle"
                      icon={
                        <svg
                          width="20"
                          height="20"
                          viewBox="0 0 24 24"
                          fill="none"
                          xmlns="http://www.w3.org/2000/svg"
                        >
                          <path d="M19 16v-6a7 7 0 00-14 0v6l-2 2h18l-2-2z" fill={themeMode === 'light' ? '#757575' : '#9e9e9e'} stroke={themeMode === 'light' ? '#757575' : '#9e9e9e'} strokeWidth="1.5" strokeLinejoin="round" />
                          <path d="M10 19a2 2 0 004 0" stroke={themeMode === 'light' ? '#b0b0b0' : '#555555'} strokeWidth="2.5" strokeLinecap="round" />
                        </svg>
                      }
                      style={{ color: themeMode === 'light' ? '#1f2937' : '#fff', width: screens.xs ? 34 : 40, height: screens.xs ? 34 : 40, padding: 0 }}
                      onClick={() => {
                        setUnreadCount(0);
                      }}
                    />
                  </Badge>
                </Tooltip>
              </Popover>

              <div className="header-avatar-wrap" style={{ display: 'flex', alignItems: 'center' }}>
                <UserAvatarMenu isUserEnd={true} agreement={agreement} />
              </div>
            </div>
          </Header>

          <Content ref={modelsScrollRef} style={{
            margin: 0,
            padding: !showModelsSkeleton && activeView === 'trending' && !selectedModel ? 0 : (screens.xs ? '12px 16px' : '20px 32px'),
            paddingTop: !showModelsSkeleton && activeView === 'trending' && !selectedModel
              ? (screens.xs ? 48 : 56)
              : (screens.xs ? 48 + 12 : 56 + 20),
            minHeight: 280,
            background: c.bg,
            borderRadius: 0,
            overflowY: 'auto',
            position: 'relative',
            zIndex: 1,
            flex: 1,
            height: '100%',
          }}>
            {showModelsSkeleton ? (
              <div className="mp-models-page" style={{ flex: 1, minWidth: 0 }}>
                <div className="mp-toolbar" style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 20, flexWrap: 'wrap' }}>
                  <MpSkel width={screens.xs ? '100%' : 420} height={40} radius={8} />
                  <MpSkel width={36} height={36} radius={8} />
                  <MpSkel width={36} height={36} radius={8} />
                </div>
                <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 24 }}>
                  {[88, 72, 96, 64, 80, 70].map((w, i) => (
                    <MpSkel key={i} width={w} height={30} radius={5} />
                  ))}
                </div>
                <MarketplaceModelsSkeleton isGrid={viewMode === 'grid'} isXs={!!screens.xs} />
              </div>
            ) : forbidden ? (
              <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: '100%', minHeight: 400 }}>
                <Result
                  icon={<LockOutlined style={{ color: c.text3 }} />}
                  title={<span style={{ color: c.text1 }}>{tp('no_access')}</span>}
                  subTitle={<span style={{ color: c.text3 }}>{tp('no_access_desc')}</span>}
                  extra={
                    <Button type="primary" onClick={() => navigate('/dashboard')}>{tp('back_dashboard')}</Button>
                  }
                />
              </div>
            ) : selectedModel ? (
              <div style={{ animation: 'fadeIn 0.3s ease-in-out' }}>
                <style>{`
                  @keyframes fadeIn { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } }
                  .mp-detail-desc-table.ant-descriptions {
                    border: none !important;
                  }
                  .mp-detail-desc-table table {
                    table-layout: fixed !important;
                    width: 100% !important;
                    border-collapse: separate !important;
                    border-spacing: 0 !important;
                  }
                  .mp-detail-desc-table .ant-descriptions-view {
                    border: 1px solid ${c.cardBorder} !important;
                    border-radius: 6px !important;
                    overflow: hidden !important;
                  }
                  .mp-detail-desc-table .ant-descriptions-row {
                    border-bottom: 1px solid ${c.cardBorder} !important;
                  }
                  .mp-detail-desc-table .ant-descriptions-item-label,
                  .mp-detail-desc-table .ant-descriptions-item-content {
                    border-right: 1px solid ${c.cardBorder} !important;
                    border-bottom: 1px solid ${c.cardBorder} !important;
                    border-top: none !important;
                    border-left: none !important;
                  }
                  .mp-detail-desc-table .ant-descriptions-row:last-child > .ant-descriptions-item-label,
                  .mp-detail-desc-table .ant-descriptions-row:last-child > .ant-descriptions-item-content {
                    border-bottom: none !important;
                  }
                  .mp-detail-desc-table .ant-descriptions-row > td:last-child,
                  .mp-detail-desc-table .ant-descriptions-row > th:last-child {
                    border-right: none !important;
                  }
                  /* 确保4个角部单元格的弧度与外边框内径完美贴合，彻底消除月牙缝隙 */
                  .mp-detail-desc-table .ant-descriptions-row:first-child > .ant-descriptions-item-label:first-child {
                    border-top-left-radius: 5px !important;
                  }
                  .mp-detail-desc-table .ant-descriptions-row:first-child > .ant-descriptions-item-content:last-child {
                    border-top-right-radius: 5px !important;
                  }
                  .mp-detail-desc-table .ant-descriptions-row:last-child > .ant-descriptions-item-label:first-child {
                    border-bottom-left-radius: 5px !important;
                  }
                  .mp-detail-desc-table .ant-descriptions-row:last-child > .ant-descriptions-item-content:last-child {
                    border-bottom-right-radius: 5px !important;
                  }
                `}</style>
                <div style={{ marginBottom: 14, display: 'flex', alignItems: 'center', gap: 12 }}>
                  <Button
                    type="text"
                    icon={<ArrowLeftOutlined />}
                    onClick={() => setSelectedModel(null)}
                    style={{ color: c.text2, fontSize: 13, display: 'inline-flex', alignItems: 'center', borderRadius: 5, border: `1px solid ${c.cardBorder}`, padding: '4px 12px', height: 32, background: 'transparent' }}
                  >
                    {tp('back_list')}
                  </Button>
                  <Breadcrumb
                    items={[
                      { title: <span style={{ color: c.text3, cursor: 'pointer' }} onClick={() => setSelectedModel(null)}>{tp('page_title')}</span> },
                      { title: <span style={{ color: c.text1 }}>{selectedModel.original_id || selectedModel.name}</span> },
                    ]}
                  />
                </div>

                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 16, alignItems: 'flex-start' }}>
                  {/* 左侧详情 */}
                  <div style={{ flex: '1 1 500px', background: c.cardBg, border: `1px solid ${c.cardBorder}`, borderRadius: 8, padding: screens.xs ? '14px' : '20px', boxShadow: isLight ? '0 4px 20px rgba(0,0,0,0.02)' : '0 4px 24px rgba(0,0,0,0.3)' }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 16 }}>
                      <div style={{
                        width: 46,
                        height: 46,
                        borderRadius: 10,
                        background: isLight ? 'rgba(0,0,0,0.03)' : 'rgba(255,255,255,0.04)',
                        border: `1px solid ${c.cardBorder}`,
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                        flexShrink: 0,
                      }}>
                        {(() => {
                          const resolvedModelLogo = selectedModel.logo || selectedModel.variants?.find(v => v.logo)?.logo;
                          const iconSrc = lobeIconSrc(
                            resolvedModelLogo,
                            selectedModel.provider_logo,
                            isLight,
                            selectedModel.original_id || selectedModel.name
                          );
                          return (
                            <img
                              src={iconSrc}
                              alt=""
                              style={{
                                width: 28,
                                height: 28,
                                objectFit: 'contain',
                                filter: getLogoFilter(
                                  resolvedModelLogo || selectedModel.provider_logo || selectedModel.name,
                                  isLight,
                                  iconSrc
                                ),
                              }}
                              onError={handleLobeIconError}
                            />
                          );
                        })()}
                      </div>
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
                          <h1 style={{ margin: 0, fontSize: screens.xs ? 18 : 22, fontWeight: 700, color: c.text1, letterSpacing: '-0.3px', lineHeight: 1.3 }}>{selectedModel.original_id || selectedModel.name}</h1>
                          {isModelUnavailable(selectedModel) && (
                              <span style={{
                                fontSize: 12, fontWeight: 500, lineHeight: '20px',
                                padding: '1px 8px', borderRadius: 4,
                                background: 'rgba(255,77,79,0.1)',
                                color: '#ff4d4f',
                                border: `1px solid rgba(255,77,79,0.3)`
                              }}>
                                {tp('unavailable', 'Unavailable')}
                              </span>
                          )}
                          {(selectedModel.variant_count || 0) > 1 && (
                            <span style={{
                              fontSize: 12, fontWeight: 500, lineHeight: '20px',
                              padding: '1px 8px', borderRadius: 4,
                              background: isLight ? 'rgba(0,0,0,0.05)' : 'rgba(255,255,255,0.08)',
                              color: isLight ? '#1f2937' : 'rgba(255,255,255,0.85)',
                              border: `1px solid ${c.cardBorder}`
                            }}>
                              {tp('pricing_count', { count: selectedModel.variant_count })}
                            </span>
                          )}
                        </div>
                        {/* 服务商微徽章 */}
                        <div style={{ marginTop: 6, display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                          {(() => {
                            const variants = selectedModel.variants || [selectedModel];
                            const uniqueProviders = Array.from(new Set(variants.map(v => v.provider_name).filter(Boolean)));
                            return uniqueProviders.map((pn) => {
                              const v = variants.find(vv => vv.provider_name === pn);
                              const pLogo = v?.logo || v?.provider_logo || (pn?.toLowerCase().includes('kimi') ? 'kimi' : undefined);
                              const pSrc = pLogo ? lobeIconSrc(pLogo, undefined, isLight, pn) : null;
                              return (
                                <span key={pn} style={{
                                  display: 'inline-flex',
                                  alignItems: 'center',
                                  gap: 6,
                                  padding: '2px 8px',
                                  borderRadius: 4,
                                  background: isLight ? 'rgba(0,0,0,0.03)' : 'rgba(255,255,255,0.04)',
                                  border: `1px solid ${c.cardBorder}`,
                                  fontSize: 12,
                                  color: c.text2,
                                }}>
                                  {pSrc && (
                                    <img
                                      src={pSrc}
                                      alt=""
                                      style={{
                                        width: 14,
                                        height: 14,
                                        objectFit: 'contain',
                                        filter: getLogoFilter(pLogo || pn, isLight, pSrc),
                                      }}
                                      onError={handleLobeIconError}
                                    />
                                  )}
                                  {displayClassName(pn, v?.provider_name_en, pn)}
                                </span>
                              );
                            });
                          })()}
                        </div>
                      </div>
                    </div>

                    <div style={{ marginTop: 14, marginBottom: 14 }}>
                      <div style={{ fontSize: 13, fontWeight: 600, color: c.text1, marginBottom: 6, display: 'flex', alignItems: 'center', gap: 6 }}>
                        <InfoCircleOutlined style={{ color: c.text3 }} /> {tp('model_desc', '模型简介')}
                      </div>
                      <div style={{ fontSize: 13, color: c.text2, lineHeight: 1.6, whiteSpace: 'pre-wrap' }}>
                        {(() => {
                          const modelDesc = localizedModelDescription(selectedModel)
                            || (selectedModel.variants || []).map(localizedModelDescription).find(Boolean);
                          return modelDesc || tp('no_desc');
                        })()}
                      </div>
                      <div style={{
                        marginTop: 10,
                        padding: '8px 12px',
                        borderRadius: 6,
                        background: isLight ? 'rgba(0,0,0,0.015)' : 'rgba(255,255,255,0.02)',
                        border: `1px dashed ${c.cardBorder}`,
                        display: 'flex',
                        alignItems: 'center',
                        gap: 8,
                      }}>
                        <GlobalOutlined style={{ color: c.text3, fontSize: 13, flexShrink: 0 }} />
                        <div style={{ fontSize: 12.5, color: c.text3, lineHeight: 1.5, flex: 1 }}>
                          {tp('model_online_desc1', '该模型目前已在全平台上线。您可以直接在 API 调用或')}{_t('menu.dashboard')}{tp('model_online_desc2', '中使用。如果您有大规模调用需求，请联系客服获取专属优惠。')}
                        </div>
                      </div>
                    </div>

                    <Descriptions
                      className="mp-detail-desc-table"
                      column={screens.xs ? 1 : 2}
                      bordered
                      size="small"
                      styles={{
                        label: {
                          background: isLight ? '#f9fafb' : 'rgba(255,255,255,0.02)',
                          color: c.text3,
                          width: screens.xs ? 85 : 92,
                          minWidth: screens.xs ? 85 : 92,
                          maxWidth: screens.xs ? 85 : 92,
                          fontSize: 12,
                          padding: '7px 12px',
                          fontWeight: 500,
                        },
                        content: {
                          background: 'transparent',
                          color: c.text1,
                          fontSize: 12.5,
                          padding: '7px 12px',
                          width: screens.xs ? undefined : 'calc(50% - 92px)',
                        }
                      }}
                      style={{ marginBottom: 4 }}
                    >
                      <Descriptions.Item label={tp('model_type', '模型类型')}>
                        {(() => {
                          const baseVariants = selectedModel.variants && selectedModel.variants.length > 0 ? selectedModel.variants : [selectedModel];
                          const groupKey = (selectedModel.original_id || selectedModel.model_id || '').trim().toLowerCase();
                          const sameGroupModels = groupKey
                            ? models.filter((m) => (m.original_id || m.model_id || '').trim().toLowerCase() === groupKey)
                            : [];
                          const variants = sameGroupModels.length > baseVariants.length ? [...baseVariants, ...sameGroupModels] : baseVariants;
                          const modelTypes: string[] = [];
                          const seenTypes = new Set<string>();
                          variants.forEach((v) => {
                            const typeName = displayClassName(v.type_name, v.type_name_en, v.type_name)?.trim();
                            if (typeName && !seenTypes.has(typeName)) {
                              seenTypes.add(typeName);
                              modelTypes.push(typeName);
                            }
                          });
                          if (modelTypes.length === 0) return '-';
                          return (
                            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'center' }}>
                              {modelTypes.map((typeName) => (
                                <Tag
                                  key={typeName}
                                  bordered={false}
                                  style={{ margin: 0, borderRadius: 4, padding: '2px 8px', background: isLight ? 'rgba(0,0,0,0.05)' : 'rgba(255,255,255,0.08)', color: c.text1, fontWeight: 600, border: `1px solid ${c.cardBorder}` }}
                                >
                                  {typeName}
                                </Tag>
                              ))}
                            </div>
                          );
                        })()}
                      </Descriptions.Item>
                      <Descriptions.Item label={tp('capability_attributes', '模型功能')}>
                        {(() => {
                          const baseVariants = selectedModel.variants && selectedModel.variants.length > 0 ? selectedModel.variants : [selectedModel];
                          const groupKey = (selectedModel.original_id || selectedModel.model_id || '').trim().toLowerCase();
                          const sameGroupModels = groupKey
                            ? models.filter((m) => (m.original_id || m.model_id || '').trim().toLowerCase() === groupKey)
                            : [];
                          const variants = sameGroupModels.length > baseVariants.length ? [...baseVariants, ...sameGroupModels] : baseVariants;
                          const featureAttributes: string[] = [];
                          const seenAttrs = new Set<string>();
                          variants.forEach((v) => {
                            const raw = v.feature_attributes;
                            if (!raw) return;
                            let arr: any[] = [];
                            if (Array.isArray(raw)) {
                              arr = raw;
                            } else if (typeof raw === 'string' && raw.trim()) {
                              try {
                                const parsed = JSON.parse(raw);
                                if (Array.isArray(parsed)) arr = parsed;
                              } catch {
                                arr = raw.split(',').map((s) => s.trim()).filter(Boolean);
                              }
                            }
                            arr.forEach((item) => {
                              if (typeof item === 'string') {
                                const trimmed = item.trim();
                                if (trimmed && !seenAttrs.has(trimmed)) {
                                  seenAttrs.add(trimmed);
                                  featureAttributes.push(trimmed);
                                }
                              }
                            });
                          });

                          if (featureAttributes.length === 0) {
                            return '-';
                          }

                          return (
                            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'center' }}>
                              {featureAttributes.map((attr) => (
                                <Tag
                                  key={attr}
                                  bordered={false}
                                  style={{
                                    margin: 0,
                                    borderRadius: 4,
                                    padding: '2px 8px',
                                    background: isLight ? 'rgba(0,0,0,0.02)' : 'rgba(255,255,255,0.03)',
                                    color: c.text2,
                                    border: `1px solid ${c.cardBorder}`,
                                    fontSize: 12,
                                  }}
                                >
                                  {displayClassName(attr, undefined, attr)}
                                </Tag>
                              ))}
                            </div>
                          );
                        })()}
                      </Descriptions.Item>
                      <Descriptions.Item label={tp('pricing_plan', '定价方案')}>
                        <Tag bordered={false} style={{ margin: 0, borderRadius: 4, padding: '2px 8px', background: isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.08)', color: c.text2, border: `1px solid ${c.cardBorder}` }}>
                          {tp('pricing_count', { count: selectedModel.variant_count || 1 })}
                        </Tag>
                      </Descriptions.Item>
                      <Descriptions.Item label={tp('updated_at', '更新时间')}>
                        {selectedModel.created_at ? formatApiDateTime(selectedModel.created_at, 'YYYY-MM-DD') : '-'}
                      </Descriptions.Item>
                    </Descriptions>
                  </div>
                </div>

                {/* 各定价方案 (高级专业圆角表格风格) */}
                <div style={{ marginTop: 16 }}>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 10 }}>
                    <h3 style={{ fontSize: 16, fontWeight: 600, color: c.text1, margin: 0, display: 'flex', alignItems: 'center', gap: 8, letterSpacing: '-0.3px' }}>
                      {tp('pricing', 'Pricing')}
                    </h3>
                    {auxiliaryCurrencies.length > 0 && (
                      <Dropdown menu={{
                        items: [
                          { key: '', label: `${tp('default_currency', '默认货币')} (${currencySymbol})` },
                          ...auxiliaryCurrencies.map(curr => ({ key: curr.code, label: `${curr.code} (${curr.symbol})` }))
                        ],
                        style: { maxHeight: 176, overflowY: 'auto' },
                        onClick: ({ key }) => setSelectedCurrencyCode(key)
                      }}>
                        <Button size="small">
                          {(() => {
                            if (!selectedCurrencyCode) return `${tp('default_currency', '默认货币')} (${currencySymbol})`;
                            const curr = auxiliaryCurrencies.find(c => c.code === selectedCurrencyCode);
                            return curr ? `${curr.code} (${curr.symbol})` : tp('switch_currency', '切换货币显示');
                          })()}
                        </Button>
                      </Dropdown>
                    )}
                  </div>
                  <div className="responsive-table-wrapper" style={{
                    border: `1px solid ${c.cardBorder}`,
                    borderRadius: 8,
                    overflow: 'hidden',
                    background: c.cardBg,
                    boxShadow: isLight ? '0 4px 20px rgba(0,0,0,0.02)' : '0 4px 24px rgba(0,0,0,0.3)'
                  }}>
                    <div style={{ overflowX: 'auto' }}>
                      <table className="responsive-table" style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left', fontSize: 14, minWidth: 600 }}>
                        <thead>
                          <tr style={{ background: isLight ? '#fafafa' : 'rgba(255,255,255,0.025)', borderBottom: `1px solid ${c.cardBorder}` }}>
                            <th style={{ padding: '10px 16px', fontWeight: 600, color: c.text3, whiteSpace: 'nowrap', fontSize: 12, textTransform: 'uppercase', letterSpacing: '0.5px' }}>{tp('provider_col', 'Provider')}</th>
                            <th style={{ padding: '10px 16px', fontWeight: 600, color: c.text3, whiteSpace: 'nowrap', fontSize: 12, textTransform: 'uppercase', letterSpacing: '0.5px' }}>{tp('billing_type_col', 'Billing Type')}</th>
                            <th style={{ padding: '10px 16px', fontWeight: 600, color: c.text3, whiteSpace: 'nowrap', fontSize: 12, textTransform: 'uppercase', letterSpacing: '0.5px' }}>{tp('price_details_col', '价格明细')}</th>
                          </tr>
                        </thead>
                        <tbody>
                          {(selectedModel.variants || [selectedModel]).map((variant, vIdx) => {
                            const billing = variant.billing_invalid ? null : (variant.billing || getFallbackBilling(variant));
                            const isTokens = billing?.billing_type === 'tokens';
                            const isRequests = billing?.billing_type === 'requests';
                            const isDuration = billing?.billing_type === 'duration';
                            const br = billing?.billing_rule;

                            const ext = safeParseJson(billing?.extended_config);
                            const tiers = safeParseJson(billing?.pricing_tiers, []);

                            return (
                              <tr key={variant.id || vIdx} style={{
                                borderBottom: vIdx === (selectedModel.variants || [selectedModel]).length - 1 ? 'none' : `1px solid ${c.cardBorder}`,
                              }}>
                                <td data-label={tp('provider_col', 'Provider')} style={{ padding: '8px 16px', verticalAlign: 'middle', minWidth: 240, maxWidth: 400 }}>
                                  <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                                    <div style={{
                                      fontSize: 13,
                                      color: c.text1,
                                      display: 'inline-flex',
                                      alignItems: 'center',
                                      width: 'fit-content',
                                      gap: 6,
                                      whiteSpace: 'nowrap'
                                    }}>
                                      <span style={{ color: c.text3, fontSize: 12, marginRight: 2 }}>{tp('model_id')}:</span>
                                      {variant.model_id}
                                      <CopyModelIdButton modelId={variant.model_id} isLight={isLight} c={c} />
                                    </div>
                                    {(() => {
                                      const desc = localizedModelDescription(variant);
                                      if (!desc) return null;
                                      return (
                                        <div style={{ fontSize: 13, color: c.text2, lineHeight: 1.5, whiteSpace: 'pre-wrap', wordBreak: 'break-all', marginLeft: 62 }}>
                                          {desc}
                                        </div>
                                      );
                                    })()}
                                  </div>
                                </td>
                                <td data-label={tp('billing_type_col', 'Billing Type')} style={{ padding: '8px 16px', verticalAlign: 'middle', whiteSpace: 'nowrap' }}>
                                  {billing ? (
                                    <Tag bordered={false} style={{ margin: 0, borderRadius: 5, padding: '4px 10px', background: isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.08)', color: c.text2, border: `1px solid ${c.cardBorder}`, fontWeight: 500, fontSize: 12 }}>
                                      {getBillingLabel(billing, tp)}
                                    </Tag>
                                  ) : <span style={{ color: c.text3, fontSize: 13 }}>{tp('unconfigured', 'Unconfigured')}</span>}
                                </td>
                                <td data-label={tp('price_details_col', '价格明细')} style={{ padding: '8px 16px', verticalAlign: 'middle', color: c.text1, whiteSpace: 'nowrap' }}>
                                  {renderUniversalPriceDetails(variant)}
                                </td>
                              </tr>
                            );
                          })}
                        </tbody>
                      </table>
                    </div>
                  </div>
                </div>
              </div>
            ) : activeView === 'trending' ? (
              <div style={{ flex: 1, minWidth: 0, height: '100%' }}>
                <TrendingPage
                  config={trendingConfig}
                  models={groupedModels.length > 0 ? groupedModels : models}
                  providers={providers}
                  onSelectModel={setSelectedModel}
                  onSelectProvider={(lab) => {
                    const labId = lab.id;
                    const matched = providers.find(p =>
                      (labId !== undefined && String(p.id) === String(labId)) ||
                      p.name?.toLowerCase() === (lab.name || lab.key || '').toLowerCase() ||
                      p.provider_type?.toLowerCase() === (lab.key || lab.name || '').toLowerCase()
                    );
                    const q = lab.name || lab.key || '';
                    patchMarketplaceParams({
                      tab: 'models',
                      type: null,
                      provider: matched ? matched.id : null,
                      q: matched ? null : (q || null),
                      closeModel: true,
                    });
                  }}
                  isLight={isLight}
                  c={c}
                  lobeIconSrc={(l, p, fallback) => lobeIconSrc(l, p, isLight, fallback)}
                  handleLobeIconError={handleLobeIconError}
                  getLogoFilter={(logo, light, src) => getLogoFilter(logo, light !== undefined ? light : isLight, src)}
                  formatPrice={formatMarketplacePrice}
                  displayClassName={displayClassName}
                  localizedModelDescription={localizedModelDescription}
                  onViewAllModels={(query) => {
                    const matched = query
                      ? providers.find(p =>
                          p.name?.toLowerCase() === query.toLowerCase() ||
                          p.provider_type?.toLowerCase() === query.toLowerCase()
                        )
                      : undefined;
                    patchMarketplaceParams({
                      tab: 'models',
                      type: null,
                      provider: matched ? matched.id : null,
                      q: matched ? null : (query || null),
                      closeModel: true,
                    });
                  }}
                />
              </div>
            ) : (
              <div className="mp-models-page" style={{ flex: 1, minWidth: 0 }}>
                <div className="mp-toolbar" style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16, flexWrap: 'wrap' }}>
                  <div className="mp-search" style={{ flex: 1, minWidth: 200, maxWidth: screens.xs ? '100%' : 420 }}>
                    <Input
                      placeholder={tp('search_placeholder')}
                      prefix={<SearchOutlined style={{ color: c.text3, marginRight: 8 }} />}
                      value={searchKeyword}
                      onChange={e => {
                        const val = e.target.value;
                        patchMarketplaceParams({
                          tab: 'models',
                          q: val.length > 0 ? val : null,
                        });
                      }}
                      allowClear
                    />
                  </div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 4, marginLeft: screens.xs ? 0 : 'auto' }}>
                    <Tooltip title={tp('list_layout')} placement="top">
                      <button
                        onClick={() => setViewMode('list')}
                        style={{
                          display: 'flex', alignItems: 'center', justifyContent: 'center',
                          width: 36, height: 36, borderRadius: 5,
                          border: `1px solid ${viewMode === 'list' ? c.activeBorder : c.sortBorder}`,
                          background: viewMode === 'list' ? c.activeBg : 'transparent',
                          color: viewMode === 'list' ? c.active : c.text3,
                          cursor: 'pointer', fontSize: 16, transition: 'all 0.2s',
                        }}
                      >
                        <UnorderedListOutlined />
                      </button>
                    </Tooltip>
                    <Tooltip title={tp('grid_layout')} placement="top">
                      <button
                        onClick={() => setViewMode('grid')}
                        style={{
                          display: 'flex', alignItems: 'center', justifyContent: 'center',
                          width: 36, height: 36, borderRadius: 5,
                          border: `1px solid ${viewMode === 'grid' ? c.activeBorder : c.sortBorder}`,
                          background: viewMode === 'grid' ? c.activeBg : 'transparent',
                          color: viewMode === 'grid' ? c.active : c.text3,
                          cursor: 'pointer', fontSize: 16, transition: 'all 0.2s',
                        }}
                      >
                        <AppstoreOutlined />
                      </button>
                    </Tooltip>
                  </div>
                  {auxiliaryCurrencies.length > 0 && (
                    <Dropdown
                      menu={{
                        items: [
                          { key: '', label: `${tp('default_currency', '默认货币')} (${currencySymbol})` },
                          ...auxiliaryCurrencies.map(curr => ({ key: curr.code, label: `${curr.code} (${curr.symbol})` }))
                        ],
                        style: { maxHeight: 176, overflowY: 'auto' },
                        onClick: ({ key }) => setSelectedCurrencyCode(key)
                      }}
                      placement="bottomRight"
                    >
                      <button style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '8px 14px', borderRadius: 5, border: `1px solid ${c.sortBorder}`, background: 'transparent', color: c.text3, fontSize: 13, cursor: 'pointer' }}>
                        {(() => {
                          if (!selectedCurrencyCode) return `${tp('default_currency', '默认货币')} (${currencySymbol})`;
                          const curr = auxiliaryCurrencies.find(c => c.code === selectedCurrencyCode);
                          return curr ? `${curr.code} (${curr.symbol})` : tp('switch_currency', '切换货币显示');
                        })()}
                      </button>
                    </Dropdown>
                  )}
                  <Dropdown
                    menu={{
                      items: [
                        { key: 'popular', label: tp('sort_popular') },
                        { key: 'newest', label: tp('sort_newest') },
                        { key: 'name', label: tp('sort_name_az') },
                      ],
                      onClick: ({ key }) => setSortBy(key as any)
                    }}
                    placement="bottomRight"
                  >
                    <button style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '8px 14px', borderRadius: 5, border: `1px solid ${c.sortBorder}`, background: 'transparent', color: c.text3, fontSize: 13, cursor: 'pointer' }}>
                      <SortAscendingOutlined />
                      {sortBy === 'popular' ? tp('sort_popular') : sortBy === 'newest' ? tp('sort_newest') : tp('sort_name')}
                    </button>
                  </Dropdown>
                  <Popover
                    trigger="click"
                    placement="bottomRight"
                    content={
                      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 16, minWidth: 168 }}>
                        <span style={{ fontSize: 13, color: c.text1 }}>{tp('discount_display', '折扣展示')}</span>
                        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
                          <span style={{ fontSize: 12, color: c.text3 }}>{showDiscount ? tp('discount_display_on', '开启') : tp('discount_display_off', '关闭')}</span>
                          <Switch size="small" checked={showDiscount} onChange={setDiscountDisplay} />
                        </span>
                      </div>
                    }
                  >
                    <Tooltip title={tp('discount_display', '折扣展示')} placement="top">
                      <button
                        type="button"
                        aria-label={tp('discount_display', '折扣展示')}
                        style={{
                          display: 'flex', alignItems: 'center', justifyContent: 'center',
                          width: 36, height: 36, borderRadius: 5,
                          border: `1px solid ${showDiscount ? c.sortBorder : c.activeBorder}`,
                          background: showDiscount ? 'transparent' : c.activeBg,
                          color: showDiscount ? c.text3 : c.active,
                          cursor: 'pointer', fontSize: 16,
                        }}
                      >
                        <SettingOutlined />
                      </button>
                    </Tooltip>
                  </Popover>
                </div>

                {/* 官方服务商横向筛选标签 */}
                {providers.length > 0 && (
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 20, flexWrap: 'wrap' }}>
                    <span style={{ fontSize: 13, color: c.text3, fontWeight: 500, marginRight: 4, whiteSpace: 'nowrap' }}>{tp('provider')}</span>
                    {providers.map(p => {
                      const isActive = selectedProvider !== null && String(selectedProvider) === String(p.id);
                      return (
                        <button
                          key={p.id}
                          onClick={() => handleProviderToggle(p.id)}
                          style={{
                            display: 'inline-flex', alignItems: 'center', gap: 6,
                            padding: '4px 12px', borderRadius: 20, fontSize: 13,
                            border: `1px solid ${isActive ? c.activeBorder : c.sortBorder}`,
                            background: isActive ? c.activeBg : 'transparent',
                            color: isActive ? c.active : c.text2,
                            cursor: 'pointer', transition: 'all 0.2s', fontWeight: isActive ? 500 : 400,
                            whiteSpace: 'nowrap',
                          }}
                        >
                          {(() => {
                            const pLogo = p.logo || (p.name?.toLowerCase().includes('kimi') ? 'kimi' : undefined);
                            const pSrc = pLogo ? lobeIconSrc(pLogo, undefined, isLight, p.name) : null;
                            if (!pSrc) return null;
                            return (
                              <img 
                                src={pSrc} 
                                alt="" 
                                style={{
                                  width: 14,
                                  height: 14,
                                  objectFit: 'contain',
                                  filter: getLogoFilter(pLogo || p.name, isLight, pSrc),
                                }} 
                                onError={handleLobeIconError} 
                              />
                            );
                          })()}
                          {displayClassName(p.name, p.name_en, p.name)}
                          <span style={{ fontSize: 11, opacity: 0.7 }}>{providerCounts[p.id] || 0}</span>
                        </button>
                      );
                    })}
                    {selectedProvider !== null && (
                      <button
                        onClick={() => patchMarketplaceParams({
                          tab: 'models',
                          provider: null,
                          closeModel: true,
                        })}
                        style={{ display: 'inline-flex', alignItems: 'center', gap: 4, padding: '4px 10px', borderRadius: 20, fontSize: 12, border: 'none', background: 'transparent', color: c.link, cursor: 'pointer' }}
                      >
                        <CloseOutlined style={{ fontSize: 10 }} /> {tp('clear')}
                      </button>
                    )}
                  </div>
                )}

                {filteredModels.length > 0 ? (
                  <div className="mp-grid-reveal">
                    {visibleSections.map(({ sec, shown, total, visibleTypeGroups }, secIdx) => {
                      const secBaseRevealIndex = visibleSections.slice(0, secIdx).reduce((n, s) => n + s.shown.length, 0);
                      const renderModelItems = (modelList: MarketplaceModel[], baseIndex: number) => (
                        <div className="mp-grid" style={{
                          display: viewMode === 'grid' ? 'grid' : 'flex',
                          ...(viewMode === 'grid'
                            ? { gridTemplateColumns: screens.xs ? '1fr' : MP_GRID_TEMPLATE_COLUMNS, gap: MP_GRID_GAP }
                            : { flexDirection: 'column' as const, gap: 12 }
                          ),
                        }}>
                          {modelList.map((model, modelIdx) => {
                        const revealIndex = baseIndex + modelIdx;
                        const billing = model.billing_invalid ? null : (model.billing || getFallbackBilling(model));
                        const modelTitle = model.original_id || model.name;
                        const copyId = model.original_id && model.variants?.length ? model.variants[0].model_id : model.model_id;
                        const validDiscounts = (model.variants || [model])
                          .map(v => getEffectiveMarketplaceDiscount(v))
                          .filter((d): d is number => d !== null);
                        const minDiscount = validDiscounts.length > 0 ? Math.min(...validDiscounts) : null;
                        const billingLabel = billing?.billing_type === 'requests' ? tp('billing_requests')
                          : billing?.billing_type === 'duration' ? tp('billing_duration')
                            : billing?.billing_type ? tp('billing_tokens') : null;
                        const metricValueStyle: React.CSSProperties = {
                          fontSize: 12,
                          fontWeight: 400,
                          color: c.text1,
                          lineHeight: 1.5,
                          minHeight: 18,
                        };
                        const discountBadge = showDiscount && minDiscount !== null ? (
                          <span style={{
                            fontSize: 11, fontWeight: 600, lineHeight: '18px',
                            padding: '0 8px', borderRadius: 10, flexShrink: 0,
                            background: 'linear-gradient(135deg, #ff4d4f 0%, #ff7875 100%)',
                            color: '#fff',
                            border: 'none',
                            boxShadow: '0 2px 4px rgba(255,77,79,0.2)'
                          }}>
                            {formatMarketplaceDiscountBadgeText(minDiscount)}
                          </span>
                        ) : null;
                        const unavailableBadge = isModelUnavailable(model) ? (
                          <span style={{
                            fontSize: 11, fontWeight: 500, lineHeight: '18px',
                            padding: '0 8px', borderRadius: 10, flexShrink: 0,
                            background: 'rgba(255,77,79,0.1)',
                            color: '#ff4d4f',
                            border: '1px solid rgba(255,77,79,0.3)',
                          }}>
                            {tp('unavailable', 'Unavailable')}
                          </span>
                        ) : null;

                        if (viewMode === 'grid') {
                          return (
                            <div
                              key={model.id}
                              className="mp-card"
                              onClick={() => setSelectedModel(model)}
                              style={{
                                display: 'flex',
                                flexDirection: 'column',
                                position: 'relative',
                                padding: 15,
                                background: c.cardBg,
                                borderColor: c.cardBorder,
                                borderRadius: 5,
                                animationDelay: `${cardRevealDelayMs(revealIndex, model.id)}ms`,
                              }}
                            >
                              <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8 }}>
                                <div className="mp-card-logo">
                                  {(() => {
                                    const resolvedLogo = model.logo || model.variants?.find(v => v.logo)?.logo;
                                    const cardIconSrc = lobeIconSrc(resolvedLogo, model.provider_logo, isLight, modelTitle);
                                    return (
                                      <img
                                        src={cardIconSrc}
                                        alt=""
                                        style={{
                                          width: 32,
                                          height: 32,
                                          objectFit: 'contain',
                                          filter: getLogoFilter(resolvedLogo || model.provider_logo || modelTitle, isLight, cardIconSrc),
                                        }}
                                        onError={handleLobeIconError}
                                      />
                                    );
                                  })()}
                                </div>
                                <div style={{ display: 'flex', flexDirection: 'column', flex: 1, minWidth: 0, paddingTop: 1 }}>
                                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, width: '100%', flexWrap: 'wrap' }}>
                                    <h3 style={{
                                      margin: 0,
                                      fontSize: 14,
                                      fontWeight: 400,
                                      color: c.text1,
                                      overflow: 'hidden',
                                      textOverflow: 'ellipsis',
                                      whiteSpace: 'nowrap',
                                      flex: '1 1 auto',
                                      minWidth: 0,
                                      lineHeight: 1.5,
                                    }}>
                                      {modelTitle}
                                    </h3>
                                    {isNewModel(model) && (
                                      <span className="mp-new-badge">{tp('new_badge', 'New')}</span>
                                    )}
                                    {unavailableBadge}
                                    {discountBadge}
                                  </div>
                                  <div style={{
                                    marginTop: 2,
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: 4,
                                    maxWidth: '100%',
                                  }}>
                                    {(model.variant_count || 0) > 1 ? (
                                      <span style={{ color: c.text3, fontSize: 12, fontWeight: 400 }}>
                                        {tp('model_id_count', { count: model.variant_count })}
                                      </span>
                                    ) : (
                                      <>
                                        <span style={{
                                          overflow: 'hidden',
                                          textOverflow: 'ellipsis',
                                          whiteSpace: 'nowrap',
                                          color: c.text3,
                                          fontSize: 12,
                                          fontWeight: 400,
                                          maxWidth: 'calc(100% - 22px)',
                                        }}>
                                          {copyId}
                                        </span>
                                        <CopyModelIdButton modelId={copyId} isLight={isLight} c={c} />
                                      </>
                                    )}
                                  </div>
                                </div>
                              </div>

                              <div style={{ flex: 1, minHeight: 10 }} />

                              <div style={{
                                display: 'flex',
                                flexDirection: 'column',
                                gap: 8,
                                paddingTop: 10,
                                borderTop: `1px solid ${isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.08)'}`,
                                minWidth: 0,
                              }}>
                                <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
                                  <span style={{ display: 'inline-flex', color: c.text2, flexShrink: 0, fontSize: 12 }}>{getTypeIcon(model.type_name)}</span>
                                  <span style={{ ...metricValueStyle, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }}>
                                    {displayClassName(model.type_name, model.type_name_en, model.type_name)}{billingLabel ? ` · ${billingLabel}` : ''}
                                  </span>
                                  {renderOffpeakBadge(model)}
                                </div>
                                <div style={{ minWidth: 0 }}>
                                  <div style={{ ...metricValueStyle, minWidth: 0 }}>
                                    {renderCardPrice(model, true) || '—'}
                                  </div>
                                </div>
                              </div>
                            </div>
                          );
                        }

                        return (
                          <div
                            key={model.id}
                            className="mp-card"
                            onClick={() => setSelectedModel(model)}
                            style={{
                              display: 'flex',
                              flexDirection: 'column',
                              position: 'relative',
                              padding: '16px 20px',
                              background: c.cardBg,
                              borderColor: c.cardBorder,
                              borderRadius: 5,
                              animationDelay: `${cardRevealDelayMs(revealIndex, model.id)}ms`,
                            }}
                          >
                            <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 8 }}>
                              {(() => {
                                const resolvedLogo = model.logo || model.variants?.find(v => v.logo)?.logo;
                                const listIconSrc = lobeIconSrc(resolvedLogo, model.provider_logo, isLight, modelTitle);
                                return (
                                  <div className="mp-card-icon" style={{
                                    overflow: 'hidden',
                                    width: 20,
                                    height: 20,
                                    borderRadius: 5,
                                    display: 'flex',
                                    alignItems: 'center',
                                    justifyContent: 'center',
                                    flexShrink: 0
                                  }}>
                                    <img
                                      src={listIconSrc}
                                      alt=""
                                      style={{
                                        width: 14,
                                        height: 14,
                                        objectFit: 'contain',
                                        filter: getLogoFilter(model.logo || model.provider_logo || modelTitle, isLight, listIconSrc),
                                      }}
                                      onError={handleLobeIconError}
                                    />
                                  </div>
                                );
                              })()}

                              <div style={{ display: 'flex', flexDirection: 'column', flex: 1, minWidth: 0 }}>
                                <div style={{ display: 'flex', alignItems: 'center', gap: 8, width: '100%' }}>
                                  <h3 style={{
                                    margin: 0,
                                    fontSize: 14,
                                    fontWeight: 400,
                                    color: c.text1,
                                    overflow: 'hidden',
                                    textOverflow: 'ellipsis',
                                    whiteSpace: 'nowrap',
                                    flex: 1,
                                    minWidth: 0,
                                    lineHeight: 1.5,
                                  }}>
                                    {modelTitle}
                                  </h3>
                                  {isNewModel(model) && (
                                    <span className="mp-new-badge">{tp('new_badge', 'New')}</span>
                                  )}
                                  {unavailableBadge}
                                  {discountBadge}
                                </div>

                                {(model.variant_count || 0) > 1 ? (
                                  <div style={{ marginTop: 4, color: c.text3, fontSize: 12, fontWeight: 400 }}>
                                    {tp('model_id_count', { count: model.variant_count })}
                                  </div>
                                ) : (
                                  <div style={{
                                    marginTop: 4,
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: 4,
                                    maxWidth: '100%'
                                  }}>
                                    <span style={{
                                      fontSize: 12,
                                      fontWeight: 400,
                                      color: c.text2,
                                      overflow: 'hidden',
                                      textOverflow: 'ellipsis',
                                      whiteSpace: 'nowrap',
                                      maxWidth: 'calc(100% - 22px)'
                                    }}>
                                      {copyId}
                                    </span>
                                    <CopyModelIdButton
                                      modelId={copyId}
                                      isLight={isLight}
                                      c={c}
                                    />
                                  </div>
                                )}
                              </div>
                            </div>

                            {billing && (
                              <div style={{
                                marginBottom: 6,
                                display: 'flex',
                                alignItems: 'center',
                                justifyContent: 'flex-end',
                              }}>
                                {renderCardPrice(model)}
                              </div>
                            )}

                            <div style={{
                              fontSize: 12,
                              fontWeight: 400,
                              color: c.text3,
                              display: 'flex',
                              alignItems: 'center',
                              justifyContent: 'space-between',
                              width: '100%',
                              gap: 12,
                            }}>
                              <div style={{
                                display: 'flex',
                                alignItems: 'center',
                                flexWrap: 'wrap',
                                gap: 8,
                              }}>
                                <span style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                                  {getTypeIcon(model.type_name)}
                                  {displayClassName(model.type_name, model.type_name_en, model.type_name)}
                                  {renderOffpeakBadge(model)}
                                </span>
                                <span style={{ color: c.textMuted }}>•</span>
                                <span>{tp('updated_at', 'Updated')} {formatApiDateTime(model.created_at, 'YYYY-MM-DD')}</span>
                                {billingLabel && (
                                  <>
                                    <span style={{ color: c.textMuted }}>•</span>
                                    <span>{billingLabel}</span>
                                  </>
                                )}
                              </div>
                            </div>
                          </div>
                        );
                      })}
                        </div>
                      );

                      return (
                        <div key={sec.typeId || secIdx} style={{ marginBottom: secIdx === visibleSections.length - 1 ? 0 : 20 }}>
                          <div style={{
                            position: 'sticky',
                            top: screens.xs ? -12 : -20,
                            zIndex: 20,
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'space-between',
                            margin: 0,
                            padding: '10px 0',
                            background: c.bg,
                          }}>
                            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                              <span style={{ fontSize: 16, color: c.active, display: 'inline-flex', alignItems: 'center' }}>
                                {sec.brand && sec.logo ? (
                                  <img
                                    src={lobeIconSrc(sec.logo, null, isLight, sec.typeName)}
                                    alt=""
                                    style={{ width: 18, height: 18, objectFit: 'contain' }}
                                    onError={handleLobeIconError}
                                  />
                                ) : sec.brand ? (
                                  <Store style={{ width: '1em', height: '1em' }} />
                                ) : getTypeIcon(sec.typeName)}
                              </span>
                              <h3 style={{ margin: 0, fontSize: 16, fontWeight: 700, color: c.text1 }}>
                                {displayClassName(sec.typeName, sec.typeNameEn, sec.typeName)}
                              </h3>
                              <span style={{
                                fontSize: 12,
                                padding: '2px 9px',
                                borderRadius: 12,
                                background: isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.08)',
                                color: c.text2,
                                fontWeight: 600
                              }}>
                                {total} {tp('kinds', '种')}
                              </span>
                            </div>
                          </div>

                          {visibleTypeGroups && visibleTypeGroups.length > 0 ? (
                            visibleTypeGroups.map((tg, tgIdx) => {
                              const tgBaseIndex = secBaseRevealIndex + visibleTypeGroups.slice(0, tgIdx).reduce((n, g) => n + g.shown.length, 0);
                              return (
                                <div
                                  key={`${sec.typeId}-type-${tg.typeId || tgIdx}`}
                                  style={{ marginBottom: tgIdx === visibleTypeGroups.length - 1 ? 0 : 14 }}
                                >
                                  <div style={{
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: 6,
                                    padding: '4px 0 8px 0',
                                  }}>
                                    <span style={{ fontSize: 14, color: c.text2, display: 'inline-flex', alignItems: 'center' }}>
                                      {getTypeIcon(tg.typeName)}
                                    </span>
                                    <span style={{ fontSize: 13.5, fontWeight: 600, color: c.text2 }}>
                                      {displayClassName(tg.typeName, tg.typeNameEn, tg.typeName)}
                                    </span>
                                    <span style={{
                                      fontSize: 11,
                                      padding: '1px 7px',
                                      borderRadius: 10,
                                      background: isLight ? 'rgba(0,0,0,0.05)' : 'rgba(255,255,255,0.07)',
                                      color: c.text3,
                                      fontWeight: 500,
                                    }}>
                                      {tg.total} {tp('kinds', '种')}
                                    </span>
                                  </div>
                                  {renderModelItems(tg.shown, tgBaseIndex)}
                                </div>
                              );
                            })
                          ) : (
                            renderModelItems(shown, secBaseRevealIndex)
                          )}
                        </div>
                      );
                    })}
                    {visibleCount < filteredModels.length && (
                      <div ref={loadMoreRef} style={{ height: 48 }} />
                    )}
                  </div>
                ) : (
                  <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', padding: '80px 20px', color: c.textMuted }}>
                    <ShopOutlined style={{ fontSize: 48, marginBottom: 16, opacity: 0.5 }} />
                    <div style={{ fontSize: 16, fontWeight: 500, marginBottom: 8, color: c.text3 }}>
                      {hasActiveFilters ? tp('no_matching_models') : tp('no_models')}
                    </div>
                    <div style={{ fontSize: 14 }}>
                      {hasActiveFilters ? tp('adjust_filters_hint') : tp('no_models_hint')}
                    </div>
                    {hasActiveFilters && (
                      <button onClick={clearFilters} style={{ marginTop: 16, fontSize: 14, display: 'inline-flex', alignItems: 'center', gap: 6, padding: '4px 10px', borderRadius: 6, color: c.link, cursor: 'pointer', background: 'transparent', border: 'none' }}>
                        <FilterOutlined /> {tp('clear_all_filters')}
                      </button>
                    )}
                  </div>
                )}
              </div>
            )}
          </Content>
          {screens.xs && !collapsed && (
            <div
              style={{
                position: 'fixed',
                top: 0,
                left: 0,
                right: 0,
                bottom: 0,
                background: 'rgba(0,0,0,0.5)',
                zIndex: 1040,
              }}
              onClick={() => setCollapsed(true)}
            />
          )}
        </Layout>
      </Layout>
    </>
  );
};

const translateBillingKey = (key: string, tp: any, isEnglish = false): string => {
  const translationKeys: Record<string, string> = {
    std: 'billing_key_std',
    pro: 'billing_key_pro',
    fast: 'billing_key_fast',
    standard: 'billing_key_standard',
    ai: 'billing_key_ai',
    text: 'billing_key_text',
    image: 'billing_key_image',
    ref: 'billing_key_ref',
    on: 'billing_key_on',
    off: 'billing_key_off',
    yes: 'billing_key_yes',
    no: 'billing_key_no',
    img2img: 'billing_key_img2img',
    ref_1_3: 'billing_key_ref_1_3',
    ref_4_7: 'billing_key_ref_4_7',
  };
  const delimiter = key.includes(' | ') ? ' | ' : '|';
  return key.split(delimiter)
    .map(part => {
      const rawPart = part.trim();
      const trimmed = rawPart.toLowerCase();
      if (translationKeys[trimmed]) return tp(translationKeys[trimmed]);
      if (isEnglish && CJK_PATTERN.test(rawPart)) return tp('specification', 'Specification');
      return rawPart;
    })
    .join(delimiter);
};

const safeParseJson = (str: any, fb: any = {}): any => {
  if (!str) return fb;
  try {
    const r = typeof str === 'string' ? JSON.parse(str) : str;
    return Array.isArray(fb) && !Array.isArray(r) ? fb : (r || fb);
  } catch {
    return fb;
  }
};

export default ModelMarketplace;
