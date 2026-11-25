import React, { useEffect, useMemo, useState } from 'react';
import {
  Alert, Button, Divider, Form, Input, InputNumber, Radio, Select, Space, Switch, Table, Tag,
  Tooltip, Typography, message,
} from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import type { FxCurrencyConfig } from '../../types';

const { Text } = Typography;

export const RegionFlagSvg: React.FC<{ code?: string; flag?: string; style?: React.CSSProperties }> = ({
  code,
  flag,
  style,
}) => {
  const wrapStyle: React.CSSProperties = {
    width: 16,
    height: 12,
    borderRadius: 2,
    flexShrink: 0,
    display: 'inline-block',
    verticalAlign: 'middle',
    boxShadow: '0 0 0 1px rgba(128, 128, 128, 0.25)',
    ...style,
  };

  const key = code || flag || '';

  switch (key) {
    case 'zh':
    case 'CNY':
    case '🇨🇳':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#DE2910" />
          <polygon fill="#FFDE00" points="5,2.2 5.7,4.3 7.9,4.3 6.1,5.6 6.8,7.7 5,6.4 3.2,7.7 3.9,5.6 2.1,4.3 4.3,4.3" />
          <circle cx="9.6" cy="2.1" r="0.7" fill="#FFDE00" />
          <circle cx="11.1" cy="3.6" r="0.7" fill="#FFDE00" />
          <circle cx="11.1" cy="5.7" r="0.7" fill="#FFDE00" />
          <circle cx="9.6" cy="7.2" r="0.7" fill="#FFDE00" />
        </svg>
      );
    case 'zh-TW':
    case 'zh-HK':
    case 'HKD':
    case '🇭🇰':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#DE2910" />
          <g transform="translate(12,9)" fill="#FFFFFF">
            <ellipse cx="0" cy="-2.8" rx="1.2" ry="2.5" transform="rotate(0)" />
            <ellipse cx="0" cy="-2.8" rx="1.2" ry="2.5" transform="rotate(72)" />
            <ellipse cx="0" cy="-2.8" rx="1.2" ry="2.5" transform="rotate(144)" />
            <ellipse cx="0" cy="-2.8" rx="1.2" ry="2.5" transform="rotate(216)" />
            <ellipse cx="0" cy="-2.8" rx="1.2" ry="2.5" transform="rotate(288)" />
          </g>
        </svg>
      );
    case 'TWD':
    case '🇹🇼':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#FE0000" />
          <rect width="12" height="9" fill="#000095" />
          <polygon
            fill="#FFFFFF"
            points="6,1.3 6.6,2.9 8.1,2.1 7.3,3.6 8.9,4.2 7.3,4.8 8.1,6.3 6.6,5.5 6,7.1 5.4,5.5 3.9,6.3 4.7,4.8 3.1,4.2 4.7,3.6 3.9,2.1 5.4,2.9"
          />
          <circle cx="6" cy="4.2" r="1.9" fill="#000095" />
          <circle cx="6" cy="4.2" r="1.5" fill="#FFFFFF" />
        </svg>
      );
    case 'en':
    case 'USD':
    case '🇺🇸':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#FFFFFF" />
          <path d="M0,0H24V2.5H0ZM0,5H24V7.5H0ZM0,10H24V12.5H0ZM0,15H24V18H0Z" fill="#B22234" />
          <rect width="11" height="10" fill="#3C3B6E" />
          <g fill="#FFFFFF">
            <circle cx="2.5" cy="2.5" r="0.7" />
            <circle cx="5.5" cy="2.5" r="0.7" />
            <circle cx="8.5" cy="2.5" r="0.7" />
            <circle cx="4" cy="5" r="0.7" />
            <circle cx="7" cy="5" r="0.7" />
            <circle cx="2.5" cy="7.5" r="0.7" />
            <circle cx="5.5" cy="7.5" r="0.7" />
            <circle cx="8.5" cy="7.5" r="0.7" />
          </g>
        </svg>
      );
    case 'GBP':
    case '🇬🇧':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#012169" />
          <path d="M0,0 L24,18 M24,0 L0,18" stroke="#FFFFFF" strokeWidth="3.2" />
          <path d="M0,0 L24,18 M24,0 L0,18" stroke="#C8102E" strokeWidth="1.6" />
          <path d="M12,0 V18 M0,9 H24" stroke="#FFFFFF" strokeWidth="5.4" />
          <path d="M12,0 V18 M0,9 H24" stroke="#C8102E" strokeWidth="3.2" />
        </svg>
      );
    case 'EUR':
    case '🇪🇺':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#003399" />
          <g fill="#FFCC00" transform="translate(12,9)">
            <circle cx="0" cy="-5.2" r="0.75" />
            <circle cx="2.6" cy="-4.5" r="0.75" />
            <circle cx="4.5" cy="-2.6" r="0.75" />
            <circle cx="5.2" cy="0" r="0.75" />
            <circle cx="4.5" cy="2.6" r="0.75" />
            <circle cx="2.6" cy="4.5" r="0.75" />
            <circle cx="0" cy="5.2" r="0.75" />
            <circle cx="-2.6" cy="4.5" r="0.75" />
            <circle cx="-4.5" cy="2.6" r="0.75" />
            <circle cx="-5.2" cy="0" r="0.75" />
            <circle cx="-4.5" cy="-2.6" r="0.75" />
            <circle cx="-2.6" cy="-4.5" r="0.75" />
          </g>
        </svg>
      );
    case 'ja':
    case 'JPY':
    case '🇯🇵':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#FFFFFF" />
          <circle cx="12" cy="9" r="5" fill="#BC002D" />
        </svg>
      );
    case 'ko':
    case 'KRW':
    case '🇰🇷':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#FFFFFF" />
          <path d="M12,4.5 A4.5,4.5 0 0,1 16.5,9 A4.5,4.5 0 0,1 7.5,9 A4.5,4.5 0 0,1 12,4.5 Z" fill="#CD2E3A" />
          <path d="M7.5,9 A4.5,4.5 0 0,0 16.5,9 A2.25,2.25 0 0,1 12,9 A2.25,2.25 0 0,0 7.5,9 Z" fill="#0047A0" />
          <g stroke="#000000" strokeWidth="1.1" strokeLinecap="round">
            <line x1="4.5" y1="5.5" x2="6.5" y2="3.5" />
            <line x1="17.5" y1="3.5" x2="19.5" y2="5.5" />
            <line x1="4.5" y1="12.5" x2="6.5" y2="14.5" />
            <line x1="17.5" y1="14.5" x2="19.5" y2="12.5" />
          </g>
        </svg>
      );
    case 'SGD':
    case '🇸🇬':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="9" fill="#EF3340" />
          <rect y="9" width="24" height="9" fill="#FFFFFF" />
          <circle cx="5.2" cy="4.5" r="2.7" fill="#FFFFFF" />
          <circle cx="6.2" cy="4.5" r="2.3" fill="#EF3340" />
          <g fill="#FFFFFF">
            <circle cx="6.8" cy="3.1" r="0.45" />
            <circle cx="8.1" cy="4.1" r="0.45" />
            <circle cx="7.6" cy="5.6" r="0.45" />
            <circle cx="6.0" cy="5.6" r="0.45" />
            <circle cx="5.5" cy="4.1" r="0.45" />
          </g>
        </svg>
      );
    case 'AUD':
    case '🇦🇺':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#00008B" />
          <path d="M0,0 L12,9 M12,0 L0,9" stroke="#FFFFFF" strokeWidth="1.8" />
          <path d="M6,0 V9 M0,4.5 H12" stroke="#FFFFFF" strokeWidth="2.8" />
          <path d="M6,0 V9 M0,4.5 H12" stroke="#C8102E" strokeWidth="1.6" />
          <circle cx="6" cy="13.8" r="1.5" fill="#FFFFFF" />
          <circle cx="18" cy="4" r="0.8" fill="#FFFFFF" />
          <circle cx="20.5" cy="7.5" r="0.8" fill="#FFFFFF" />
          <circle cx="15.5" cy="8.2" r="0.8" fill="#FFFFFF" />
          <circle cx="18" cy="13.5" r="0.9" fill="#FFFFFF" />
        </svg>
      );
    case 'CAD':
    case '🇨🇦':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#FF0000" />
          <rect x="6" width="12" height="18" fill="#FFFFFF" />
          <polygon fill="#FF0000" points="12,3.5 13.3,6.8 16,6.3 14.2,9.2 15.5,10.5 12.4,10.5 12.4,14.2 11.6,14.2 11.6,10.5 8.5,10.5 9.8,9.2 8,6.3 10.7,6.8" />
        </svg>
      );
    case 'CHF':
    case '🇨🇭':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#DA291C" />
          <rect x="10.2" y="3.8" width="3.6" height="10.4" fill="#FFFFFF" />
          <rect x="6.8" y="7.2" width="10.4" height="3.6" fill="#FFFFFF" />
        </svg>
      );
    case 'NZD':
    case '🇳🇿':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#00247D" />
          <path d="M0,0 L12,9 M12,0 L0,9" stroke="#FFFFFF" strokeWidth="1.8" />
          <path d="M6,0 V9 M0,4.5 H12" stroke="#FFFFFF" strokeWidth="2.8" />
          <path d="M6,0 V9 M0,4.5 H12" stroke="#CC142B" strokeWidth="1.6" />
          <circle cx="18" cy="4.2" r="0.95" fill="#CC142B" stroke="#FFFFFF" strokeWidth="0.35" />
          <circle cx="20.5" cy="8" r="0.95" fill="#CC142B" stroke="#FFFFFF" strokeWidth="0.35" />
          <circle cx="15.5" cy="8.5" r="0.95" fill="#CC142B" stroke="#FFFFFF" strokeWidth="0.35" />
          <circle cx="18" cy="13.5" r="1.05" fill="#CC142B" stroke="#FFFFFF" strokeWidth="0.35" />
        </svg>
      );
    case 'ms':
    case 'MYR':
    case '🇲🇾':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#FFFFFF" />
          <path d="M0,0H24V2.5H0ZM0,5H24V7.5H0ZM0,10H24V12.5H0ZM0,15H24V18H0Z" fill="#CC0001" />
          <rect width="12" height="10" fill="#010066" />
          <circle cx="5.2" cy="5" r="2.8" fill="#FFCC00" />
          <circle cx="6.1" cy="5" r="2.3" fill="#010066" />
          <circle cx="8.3" cy="5" r="1.4" fill="#FFCC00" />
        </svg>
      );
    case 'th':
    case 'THB':
    case '🇹🇭':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#A51931" />
          <rect y="3" width="24" height="12" fill="#F4F5F8" />
          <rect y="6" width="24" height="6" fill="#2D2A4A" />
        </svg>
      );
    case 'vi':
    case 'VND':
    case '🇻🇳':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#DA251D" />
          <polygon fill="#FFFF00" points="12,3.8 13.3,7.6 17.3,7.6 14.1,9.9 15.3,13.7 12,11.4 8.7,13.7 9.9,9.9 6.7,7.6 10.7,7.6" />
        </svg>
      );
    case 'IDR':
    case '🇮🇩':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="9" fill="#FF0000" />
          <rect y="9" width="24" height="9" fill="#FFFFFF" />
        </svg>
      );
    case 'INR':
    case '🇮🇳':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="6" fill="#FF9933" />
          <rect y="6" width="24" height="6" fill="#FFFFFF" />
          <rect y="12" width="24" height="6" fill="#138808" />
          <circle cx="12" cy="9" r="2.2" fill="none" stroke="#000080" strokeWidth="0.9" />
          <circle cx="12" cy="9" r="0.6" fill="#000080" />
        </svg>
      );
    case 'AED':
    case '🇦🇪':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect x="6" width="18" height="6" fill="#00732F" />
          <rect x="6" y="6" width="18" height="6" fill="#FFFFFF" />
          <rect x="6" y="12" width="18" height="6" fill="#000000" />
          <rect width="6.5" height="18" fill="#FF0000" />
        </svg>
      );
    case 'ar':
    case 'SAR':
    case '🇸🇦':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#006C35" />
          <rect x="6" y="6.5" width="12" height="2" rx="1" fill="#FFFFFF" />
          <rect x="7.5" y="11" width="9" height="1.2" rx="0.6" fill="#FFFFFF" />
        </svg>
      );
    case 'pt':
    case 'BRL':
    case '🇧🇷':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#009C3B" />
          <polygon points="12,2.2 21.5,9 12,15.8 2.5,9" fill="#FFDF00" />
          <circle cx="12" cy="9" r="3.6" fill="#002776" />
          <path d="M8.6,8.2 Q12,6.8 15.4,9.6" stroke="#FFFFFF" strokeWidth="1" fill="none" />
        </svg>
      );
    case 'MXN':
    case '🇲🇽':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="8" height="18" fill="#006847" />
          <rect x="8" width="8" height="18" fill="#FFFFFF" />
          <rect x="16" width="8" height="18" fill="#CE1126" />
          <circle cx="12" cy="9" r="1.8" fill="#8C5A2B" />
        </svg>
      );
    case 'ru':
    case 'RUB':
    case '🇷🇺':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="6" fill="#FFFFFF" />
          <rect y="6" width="24" height="6" fill="#0039A6" />
          <rect y="12" width="24" height="6" fill="#D52B1E" />
        </svg>
      );
    case 'TRY':
    case '🇹🇷':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#E30A17" />
          <circle cx="9.8" cy="9" r="4.2" fill="#FFFFFF" />
          <circle cx="11.1" cy="9" r="3.4" fill="#E30A17" />
          <polygon fill="#FFFFFF" points="14.2,9 16.8,7.9 15.7,10.5 15.7,7.5 16.8,10.1" />
        </svg>
      );
    case 'fr':
    case '🇫🇷':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="8" height="18" fill="#002395" />
          <rect x="8" width="8" height="18" fill="#FFFFFF" />
          <rect x="16" width="8" height="18" fill="#ED2939" />
        </svg>
      );
    case 'de':
    case '🇩🇪':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="6" fill="#000000" />
          <rect y="6" width="24" height="6" fill="#DD0000" />
          <rect y="12" width="24" height="6" fill="#FFCE00" />
        </svg>
      );
    case 'es':
    case '🇪🇸':
      return (
        <svg viewBox="0 0 24 18" style={wrapStyle} aria-hidden="true">
          <rect width="24" height="18" fill="#AA151B" />
          <rect y="4.5" width="24" height="9" fill="#F1BF00" />
          <rect x="5" y="6.8" width="2.8" height="4.4" rx="0.8" fill="#AA151B" />
        </svg>
      );
    default:
      return flag ? <span style={style}>{flag}</span> : null;
  }
};

const CurrencyFlag: React.FC<{ flag: string; code?: string; style?: React.CSSProperties }> = ({ flag, code, style }) => (
  <RegionFlagSvg code={code} flag={flag} style={style} />
);

interface FxCatalogItem {
  code: string;
  symbol: string;
  unit: string;
  name_zh: string;
  flag: string;
  langs: string[];
}

interface FxRateRow {
  code: string;
  /** 在线实时汇率 */
  auto_rate: number | null;
  adjust?: '' | 'percent' | 'amount' | 'manual';
  /** 规则数值：百分比、加数；手动时看 manual_rate */
  adjust_value?: number;
  manual_rate?: number;
  effective_rate: number | null;
  source: 'same' | 'manual' | 'auto' | null;
  payment_rate: number | null;
  /** 站点当前实际使用的收款汇率 */
  site_rate: number | null;
}

/** 后端 GET /settings/currency/fx 概览 */
export interface FxOverview {
  default_currency: string;
  catalog: FxCatalogItem[];
  snapshot: { source: string; provider_updated_at: number; fetched_at: number; fresh: boolean } | null;
  last_error: string | null;
  rates: FxRateRow[];
  channels: {
    id: string;
    capable: string[];
    currencies: string[];
    pay_currency: string | null;
    pay_rate: number | null;
    needs_conversion: boolean;
  }[];
}

interface Props {
  currency: any;
  supportedLanguages: string[];
  overview: FxOverview | null;
  onOverviewChange: (overview: FxOverview) => void;
  onSaved: (updatedSettings: any) => void;
}

const formatRate = (v?: number | null): string => {
  if (v == null || !Number.isFinite(v)) return '—';
  if (v >= 100) return v.toFixed(2);
  if (v >= 1) return v.toFixed(4);
  return String(Number(v.toPrecision(6)));
};

const formatTime = (ts?: number) => (ts ? dayjs.unix(ts).format('YYYY-MM-DD HH:mm') : '—');

/** 每点一次 − / + 约调整 0.1%，取整到 10 的幂 */
const rateStep = (rate?: number | null): number => {
  if (!rate || !Number.isFinite(rate) || rate <= 0) return 0.0001;
  return 10 ** Math.round(Math.log10(rate * 0.001));
};

const stepDecimals = (step: number) => Math.max(0, Math.round(-Math.log10(step)));

type RateMode = 'live' | 'percent' | 'amount' | 'manual';

/** 表单里每个币种的草稿 */
interface RateDraft {
  mode?: RateMode;
  /** percent 为在线汇率的百分比（100 = 原值），amount 为加在实时汇率上的数，manual 为手填汇率 */
  value?: number | null;
  display?: boolean;
}

const RATE_MODE_OPTIONS: { value: RateMode; label: string }[] = [
  { value: 'live', label: '跟随实时' },
  { value: 'percent', label: '百分比' },
  { value: 'amount', label: '加数值' },
  { value: 'manual', label: '手动' },
];

/** 该币种最终汇率：1 默认货币 = 返回值 */
const previewRate = (mode: RateMode, value: number | null | undefined, online: number | null): number | null => {
  if (mode === 'manual') return typeof value === 'number' && value > 0 ? value : null;
  if (online == null) return null;
  if (mode === 'percent') {
    const pct = typeof value === 'number' && Number.isFinite(value) ? value : 100;
    if (pct <= 0) return null;
    return online * (Math.min(1000, pct) / 100);
  }
  if (mode === 'amount') {
    const add = typeof value === 'number' && Number.isFinite(value) ? Math.max(0, value) : 0;
    return online + add;
  }
  return online;
};

const CurrencySettingsTab: React.FC<Props> = ({
  currency, supportedLanguages, overview, onOverviewChange, onSaved,
}) => {
  const { t } = useTranslation();
  const [form] = Form.useForm();
  const [saving, setSaving] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [togglingLive, setTogglingLive] = useState(false);
  const [selectedRowKeys, setSelectedRowKeys] = useState<React.Key[]>([]);
  const [batchMode, setBatchMode] = useState<RateMode>('live');
  const [batchValue, setBatchValue] = useState<number | null>(null);

  const handleToggleLive = async (checked: boolean) => {
    setTogglingLive(true);
    form.setFieldsValue({ fx_live_enabled: checked });
    try {
      const payload = {
        currency: {
          fx: {
            live_enabled: checked,
          },
        },
      };
      const updatedSettings = await (request.post('/settings', payload) as any);
      message.destroy();
      message.success(checked ? '实时汇率已开启' : '实时汇率已关闭');
      onSaved(updatedSettings);
      if (checked) {
        request
          .post('/settings/currency/fx/refresh')
          .then((res: any) => onOverviewChange(res))
          .catch(() => {});
      }
    } catch (error: any) {
      console.error('Toggle fx_live_enabled error:', error);
      message.error(error?.message || '操作失败');
      form.setFieldsValue({ fx_live_enabled: !checked });
    } finally {
      setTogglingLive(false);
    }
  };

  const savedDefault = String(currency?.default_currency || 'CNY').toUpperCase();
  const watchedDefault: string = Form.useWatch('default_currency', form) || savedDefault;
  const watchedMode: string = Form.useWatch('fx_mode', form) || 'auto';
  const watchedLive: boolean = Form.useWatch('fx_live_enabled', form) === true;
  const watchedRatio = Form.useWatch('token_ratio', form);
  const watchedMap: Record<string, RateDraft> = Form.useWatch('fx_map', form) || {};

  const catalog = overview?.catalog || [];
  const metaOf = (code: string) => catalog.find((c) => c.code === code);
  const defaultChanged = watchedDefault !== savedDefault;
  const rateByCode = useMemo(
    () => new Map((overview?.rates || []).map((r) => [r.code, r])),
    [overview],
  );
  // 在线汇率以已保存的默认货币为基准，切换默认货币未保存前不可用
  const previewValid = overview?.default_currency === watchedDefault && !defaultChanged;
  const onlineRateOf = (code: string) => (previewValid ? rateByCode.get(code)?.auto_rate ?? null : null);

  useEffect(() => {
    if (!currency || catalog.length === 0) return;
    const fx = currency.fx || {};
    const stored: FxCurrencyConfig[] = Array.isArray(fx.currencies) ? fx.currencies : [];
    const globalManual = fx.mode === 'manual';
    const legacyMarkup = Number(fx.markup_percent) > 0 ? Number(fx.markup_percent) : 0;
    const fxMap: Record<string, RateDraft> = {};
    catalog.forEach((c) => {
      const s = stored.find((x) => String(x.code).toUpperCase() === c.code);
      const display = s ? s.display !== false : true;
      const explicit = s?.adjust === 'percent' || s?.adjust === 'amount' || s?.adjust === 'manual';
      if (s && (s.adjust === 'manual' || (!explicit && (globalManual || s.manual) && s.manual_rate > 0))) {
        // 旧的整站加价叠在手填汇率上；折进显示值，保存后不再另加一遍
        const rate = !explicit && legacyMarkup > 0 ? s.manual_rate * (1 + legacyMarkup / 100) : s.manual_rate;
        fxMap[c.code] = { mode: 'manual', value: rate, display };
      } else if (s?.adjust === 'percent' || (!explicit && legacyMarkup > 0)) {
        fxMap[c.code] = {
          mode: 'percent',
          value: s?.adjust === 'percent' ? (s.adjust_value ?? 100) : 100 + legacyMarkup,
          display,
        };
      } else if (s?.adjust === 'amount') {
        fxMap[c.code] = { mode: 'amount', value: s.adjust_value ?? 0, display };
      } else {
        fxMap[c.code] = { mode: 'live', value: null, display };
      }
    });
    form.setFieldsValue({
      default_currency: savedDefault,
      token_ratio: currency.token_ratio,
      quick_amounts: (currency.quick_amounts || [20, 50, 100, 500, 1000, 5000]).join(', '),
      min_recharge_amount: currency.min_recharge_amount ?? 5,
      max_recharge_amount: currency.max_recharge_amount ?? 10000,
      fx_live_enabled: fx.live_enabled === true,
      fx_mode: fx.mode === 'manual' ? 'manual' : 'auto',
      refresh_interval_minutes: fx.refresh_interval_minutes ?? 60,
      max_stale_hours: fx.max_stale_hours ?? 48,
      fx_map: fxMap,
    });
  }, [currency, catalog.length]);

  const currencyOptions = useMemo(() => {
    const enabled = new Set(supportedLanguages);
    const isLangActive = (lang: string) =>
      enabled.has(lang) ||
      (lang === 'zh-HK' && (enabled.has('zh-TW') || enabled.has('zh-HK'))) ||
      (lang === 'zh-TW' && (enabled.has('zh-TW') || enabled.has('zh-HK')));
    const sorted = [...catalog].sort((a, b) => {
      const ea = a.langs.some(isLangActive) ? 0 : 1;
      const eb = b.langs.some(isLangActive) ? 0 : 1;
      return ea - eb;
    });
    const options = sorted.map((c) => ({
      value: c.code,
      label: (
        <Space size={6}>
          <span><CurrencyFlag flag={c.flag} code={c.code} /></span>
          <Text strong>{c.code}</Text>
          <Text type="secondary">{c.name_zh} · {c.symbol}</Text>
          {c.langs.map((l) => (
            <Tag key={l} color={isLangActive(l) ? 'blue' : undefined} style={{ marginInlineEnd: 0 }}>{l}</Tag>
          ))}
        </Space>
      ),
    }));
    if (savedDefault && !catalog.some((c) => c.code === savedDefault)) {
      options.unshift({ value: savedDefault, label: <Text>{savedDefault}（旧配置，不在可选列表）</Text> });
    }
    return options;
  }, [catalog, supportedLanguages, savedDefault]);

  const selectedMeta = metaOf(watchedDefault);
  const symbol = selectedMeta?.symbol ?? currency?.currency_symbol ?? '';
  const unit = selectedMeta?.unit ?? currency?.currency_unit ?? '';

  const refreshNow = async () => {
    setRefreshing(true);
    try {
      const res = await (request.post('/settings/currency/fx/refresh') as any);
      onOverviewChange(res);
      message.success('汇率已更新');
    } catch (error) {
      console.error('Refresh fx error:', error);
    } finally {
      setRefreshing(false);
    }
  };

  const onFinish = async (values: any) => {
    setSaving(true);
    try {
      const quick_amounts = String(values.quick_amounts || '')
        .split(',')
        .map((x: string) => parseFloat(x.trim()))
        .filter((x: number) => !isNaN(x) && x > 0);
      const fxMap: Record<string, RateDraft> = values.fx_map || {};
      const liveEnabled = values.fx_live_enabled === true;
      const storedFx = currency?.fx || {};
      const globalManual = values.fx_mode === 'manual';
      const rows = catalog.filter((c) => c.code !== values.default_currency);
      if (liveEnabled && !globalManual) {
        const missing = rows.find((c) => {
          const v = fxMap[c.code] || {};
          const mode = v.mode || 'live';
          if (mode === 'manual') return !(Number(v.value) > 0);
          if (mode === 'percent') return !(Number(v.value) > 0);
          return false;
        });
        if (missing) {
          const mode = fxMap[missing.code]?.mode;
          message.error(mode === 'percent'
            ? `${missing.code} 的百分比要大于 0，100 表示按在线汇率`
            : `${missing.code} 已选手动汇率，请填写大于 0 的汇率`);
          setSaving(false);
          return;
        }
      }
      // 关闭时保留已保存的汇率表，避免把手动汇率写成 0；重新开启后仍可用
      const currencies: FxCurrencyConfig[] = liveEnabled
        ? rows.map((c) => {
            const v = fxMap[c.code] || {};
            const mode: RateMode = v.mode || 'live';
            const display = v.display !== false;
            const online = onlineRateOf(c.code);
            if (globalManual) {
              const frozen = previewRate(mode, v.value, online) ?? 0;
              return {
                code: c.code,
                manual: false,
                manual_rate: frozen,
                adjust: frozen > 0 ? 'manual' as const : '' as const,
                adjust_value: 0,
                display,
              };
            }
            if (mode === 'manual') {
              return {
                code: c.code,
                manual: true,
                manual_rate: Number(v.value),
                adjust: 'manual',
                adjust_value: 0,
                display,
              };
            }
            if (mode === 'percent' || mode === 'amount') {
              const raw = Number(v.value);
              const adjust_value = Number.isFinite(raw) ? Math.max(0, raw) : 0;
              return {
                code: c.code,
                manual: false,
                manual_rate: 0,
                adjust: mode,
                adjust_value,
                display,
              };
            }
            return {
              code: c.code,
              manual: false,
              manual_rate: 0,
              adjust: '' as const,
              adjust_value: 0,
              display,
            };
          })
        : (Array.isArray(storedFx.currencies) ? storedFx.currencies : []);
      const payload = {
        currency: {
          default_currency: values.default_currency,
          token_ratio: values.token_ratio,
          quick_amounts,
          min_recharge_amount: values.min_recharge_amount != null ? parseFloat(values.min_recharge_amount) : 5.0,
          max_recharge_amount: values.max_recharge_amount != null ? parseFloat(values.max_recharge_amount) : 10000.0,
          fx: {
            live_enabled: liveEnabled,
            mode: liveEnabled ? values.fx_mode : (storedFx.mode || 'auto'),
            refresh_interval_minutes: liveEnabled ? values.refresh_interval_minutes : (storedFx.refresh_interval_minutes ?? 60),
            // 加价改由各币种自己配置；旧的整站加价已折进表格
            markup_percent: liveEnabled ? 0 : (storedFx.markup_percent ?? 0),
            max_stale_hours: liveEnabled ? values.max_stale_hours : (storedFx.max_stale_hours ?? 48),
            currencies,
          },
        },
      };
      const updatedSettings = await (request.post('/settings', payload) as any);
      message.destroy();
      message.success(t('settings.save_success', '货币设置保存成功'));
      onSaved(updatedSettings);
    } catch (error) {
      console.error('Save currency error:', error);
    } finally {
      setSaving(false);
    }
  };

  const rateRows = catalog.filter((c) => c.code !== watchedDefault);
  const snapshot = overview?.snapshot;
  const globalManual = watchedMode === 'manual';

  useEffect(() => {
    setSelectedRowKeys((prev) => prev.filter((k) => k !== watchedDefault));
  }, [watchedDefault]);

  const targetCodes = useMemo(
    () => new Set(selectedRowKeys.map((k) => String(k))),
    [selectedRowKeys],
  );

  const handleBatchApplyRule = () => {
    if (targetCodes.size === 0) {
      message.warning('请先勾选需要批量修改的货币');
      return;
    }
    if (batchMode === 'percent' && !(typeof batchValue === 'number' && batchValue > 0)) {
      message.warning('请输入大于 0 的百分比（100 表示按在线汇率）');
      return;
    }
    const currentMap: Record<string, RateDraft> = form.getFieldValue('fx_map') || {};
    const nextMap: Record<string, RateDraft> = { ...currentMap };
    targetCodes.forEach((code) => {
      const prev = currentMap[code] || { display: true };
      const online = onlineRateOf(code);
      let nextVal: number | null = null;
      if (batchMode === 'percent') {
        nextVal = batchValue ?? 100;
      } else if (batchMode === 'amount') {
        nextVal = typeof batchValue === 'number' && Number.isFinite(batchValue) ? Math.max(0, batchValue) : 0;
      } else if (batchMode === 'manual') {
        nextVal = typeof batchValue === 'number' && batchValue > 0 ? batchValue : (online ?? prev.value ?? null);
      }
      nextMap[code] = {
        ...prev,
        mode: batchMode,
        value: nextVal,
      };
    });
    form.setFieldsValue({ fx_map: nextMap });
    message.success(`已批量更新 ${targetCodes.size} 个货币的汇率规则（点击下方「保存设置」后生效）`);
  };

  const handleBatchDisplay = (display: boolean) => {
    if (targetCodes.size === 0) {
      message.warning('请先勾选需要批量修改的货币');
      return;
    }
    const currentMap: Record<string, RateDraft> = form.getFieldValue('fx_map') || {};
    const nextMap: Record<string, RateDraft> = { ...currentMap };
    targetCodes.forEach((code) => {
      const prev = currentMap[code] || { mode: 'live', value: null };
      nextMap[code] = {
        ...prev,
        display,
      };
    });
    form.setFieldsValue({ fx_map: nextMap });
    message.success(`已批量${display ? '开启' : '关闭'} ${targetCodes.size} 个货币的展示状态（点击下方「保存设置」后生效）`);
  };

  const rateColumns = [
    {
      title: '货币',
      key: 'code',
      width: 160,
      render: (_: any, c: FxCatalogItem) => (
        <span style={{ whiteSpace: 'nowrap', fontSize: 13 }}>
          <span style={{ marginRight: 6 }}><CurrencyFlag flag={c.flag} code={c.code} /></span>
          <Text strong>{c.code}</Text>
          <Text type="secondary" style={{ fontSize: 12, marginLeft: 6 }}>{c.name_zh}</Text>
        </span>
      ),
    },
    {
      title: '汇率规则',
      key: 'mode',
      width: 196,
      render: (_: any, c: FxCatalogItem) => {
        const mode: RateMode = watchedMap[c.code]?.mode || 'live';
        const online = onlineRateOf(c.code);
        return (
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, whiteSpace: 'nowrap' }}>
            <Form.Item name={['fx_map', c.code, 'mode']} noStyle>
              <Select
                size="small"
                style={{ width: 108 }}
                disabled={defaultChanged}
                options={RATE_MODE_OPTIONS}
                onChange={(next: RateMode) => {
                  if (next === 'manual') {
                    form.setFieldValue(['fx_map', c.code, 'value'], online ?? null);
                  } else if (next === 'percent') {
                    form.setFieldValue(['fx_map', c.code, 'value'], 100);
                  } else if (next === 'live') {
                    form.setFieldValue(['fx_map', c.code, 'value'], null);
                  } else {
                    form.setFieldValue(['fx_map', c.code, 'value'], 0);
                  }
                }}
              />
            </Form.Item>
            {!globalManual && mode !== 'live' && !defaultChanged && (
              <Button
                type="link"
                size="small"
                style={{ padding: 0, height: 22, fontSize: 12 }}
                onClick={() => {
                  form.setFieldValue(['fx_map', c.code, 'mode'], 'live');
                  form.setFieldValue(['fx_map', c.code, 'value'], null);
                }}
              >
                恢复跟随
              </Button>
            )}
          </span>
        );
      },
    },
    {
      title: (
        <Tooltip title={`数据源报价，1 ${watchedDefault} = 该币种`}>
          <span>在线实时汇率</span>
        </Tooltip>
      ),
      key: 'online',
      width: 130,
      render: (_: any, c: FxCatalogItem) => (
        <Text>{defaultChanged ? '—' : formatRate(onlineRateOf(c.code))}</Text>
      ),
    },
    {
      title: (
        <Tooltip title="百分比：100 为在线汇率，110 为 1.1 倍，50 为一半。加数值：加在在线汇率上。手动：手填汇率。跟随实时时不用填。">
          <span>规则数值</span>
        </Tooltip>
      ),
      key: 'value',
      width: 160,
      render: (_: any, c: FxCatalogItem) => {
        const mode: RateMode = watchedMap[c.code]?.mode || 'live';
        if (mode === 'live') return <Text type="secondary">—</Text>;
        const online = onlineRateOf(c.code);
        const step = rateStep(mode === 'manual' ? (watchedMap[c.code]?.value ?? online) : online);
        return (
          <Form.Item name={['fx_map', c.code, 'value']} noStyle>
            <InputNumber
              size="small"
              style={{ width: 120 }}
              min={mode === 'percent' ? 0.01 : 0}
              max={mode === 'percent' ? 1000 : undefined}
              step={mode === 'percent' ? 1 : step}
              precision={mode === 'percent' ? 2 : stepDecimals(step) + 1}
              disabled={defaultChanged}
              suffix={mode === 'percent' ? '%' : undefined}
              placeholder={mode === 'percent' ? '100' : mode === 'manual' ? '填写汇率' : '0'}
            />
          </Form.Item>
        );
      },
    },
    {
      title: (
        <Tooltip title="接口返回的当前收款汇率，下单按这个数换算。改了规则还没保存时，下面会标出保存后的结果">
          <span>站点使用汇率</span>
        </Tooltip>
      ),
      key: 'used',
      width: 210,
      render: (_: any, c: FxCatalogItem) => {
        if (defaultChanged) return <Text type="secondary">—</Text>;
        const saved = rateByCode.get(c.code);
        const siteRate = saved?.site_rate ?? saved?.payment_rate ?? null;
        const mode: RateMode = watchedMap[c.code]?.mode || 'live';
        const draft = previewRate(mode, watchedMap[c.code]?.value, onlineRateOf(c.code));
        const drifted = draft != null && siteRate != null && Math.abs(draft - siteRate) > Math.max(siteRate, draft) * 1e-6;
        const draftOnly = draft != null && siteRate == null;
        return (
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, whiteSpace: 'nowrap' }}>
            <Text strong>{formatRate(siteRate)}</Text>
            {(drifted || draftOnly) && (
              <Text type="secondary" style={{ fontSize: 12 }}>保存后 {formatRate(draft)}</Text>
            )}
            {!siteRate && (
              <Tooltip title="在线汇率过期或缺失，且没有可用的手填汇率，需要换算的支付渠道暂停收款">
                <Tag color="error" style={{ marginInlineEnd: 0 }}>不可收款</Tag>
              </Tooltip>
            )}
          </span>
        );
      },
    },
    {
      title: (
        <Tooltip title="在模型广场、模型列表中作为辅助货币切换显示">
          <span>展示</span>
        </Tooltip>
      ),
      key: 'display',
      width: 70,
      align: 'center' as const,
      render: (_: any, c: FxCatalogItem) => (
        <Form.Item name={['fx_map', c.code, 'display']} valuePropName="checked" noStyle>
          <Switch size="small" />
        </Form.Item>
      ),
    },
  ];

  return (
    <div style={{ maxWidth: 1280 }}>
      <Form form={form} layout="vertical" onFinish={onFinish} autoComplete="off">
        <div style={{ maxWidth: 640 }}>
          <Form.Item
            label={t('settings.default_currency', '默认货币')}
            name="default_currency"
            rules={[{ required: true }]}
            extra="可选货币对应「站点基础设置 → 语言配置」中的全部语言，蓝色标签为站点已启用的语言。余额、计价与充值到账均以默认货币为准。"
          >
            <Select options={currencyOptions} loading={!overview} />
          </Form.Item>
          {defaultChanged && (
            <Alert
              type="warning"
              showIcon
              style={{ marginBottom: 16 }}
              message="切换默认货币不会换算现有用户余额与模型价格。保存后会清空手填汇率和加数值，百分比折算保留。"
            />
          )}
          <Space size={16} style={{ marginBottom: 16 }}>
            <Form.Item label={t('settings.currency_symbol', '货币符号')} style={{ margin: 0 }}>
              <Input value={symbol} disabled style={{ width: 120 }} />
            </Form.Item>
            <Form.Item label={t('settings.currency_unit', '货币单位')} style={{ margin: 0 }}>
              <Input value={unit} disabled style={{ width: 120 }} />
            </Form.Item>
          </Space>
          <Form.Item
            label={t('settings.token_ratio', '兑换比例')}
            name="token_ratio"
            rules={[{ required: true }]}
            extra={<Text type="secondary">{`1 ${watchedDefault} = ${watchedRatio ?? 'N'} Tokens`}</Text>}
          >
            <InputNumber style={{ width: '100%' }} min={0} step={0.0001} />
          </Form.Item>
        </div>

        <Divider>实时汇率</Divider>
        <div style={{ maxWidth: 640 }}>
          <Form.Item
            label="启用实时汇率"
            name="fx_live_enabled"
            valuePropName="checked"
            extra="国内单币种站点保持关闭：只按默认货币（一般为人民币）原额收款，也不请求外部汇率。海外站要让微信/支付宝收非人民币，或 Stripe 按其他货币收款时再打开。开启或关闭立即生效。"
          >
            <Switch loading={togglingLive} onChange={handleToggleLive} />
          </Form.Item>
        </div>
        {watchedLive ? (
        <>
        <Alert
          type="info"
          showIcon
          style={{
            padding: '6px 12px',
            fontSize: 12,
            marginBottom: 12,
            borderRadius: 6,
          }}
          message={
            <div style={{ fontSize: 12, lineHeight: 1.5 }}>
              <span><strong>汇率口径</strong>：1 默认货币 = N 目标货币。百分比按在线汇率折算（100% 为原值，110% 为 1.1 倍），下方规则保存后生效。</span>
              <span style={{ display: 'block', color: 'var(--ant-color-text-secondary, rgba(255,255,255,0.65))', marginTop: 2 }}>
                数据源来自 open.er-api.com 等公开免费源。渠道不收默认货币时按对应汇率换算并向上取整。
              </span>
            </div>
          }
        />
        <div style={{ maxWidth: 640 }}>
          <Form.Item
            label="汇率模式"
            name="fx_mode"
            extra="固定：保存时把下表算出的汇率锁定，之后不再跟随在线更新。"
          >
            <Radio.Group>
              <Radio value="auto">自动（下表按币种跟随、折算或手填）</Radio>
              <Radio value="manual">固定（按保存时的汇率锁定）</Radio>
            </Radio.Group>
          </Form.Item>
          <Space size={16} wrap align="start">
            <Form.Item label="自动刷新频率" name="refresh_interval_minutes" extra="10 ~ 1440 分钟">
              <InputNumber min={10} max={1440} step={10} precision={0} suffix="分钟" style={{ width: 160 }} />
            </Form.Item>
            <Form.Item
              label={<Tooltip title="自动汇率超过该时长未更新时，仍跟随实时汇率的币种暂停收款，防止按过期汇率少收。手填汇率不受影响"><span>过期保护</span></Tooltip>}
              name="max_stale_hours"
              extra="1 ~ 720 小时"
            >
              <InputNumber min={1} max={720} precision={0} suffix="小时" style={{ width: 140 }} />
            </Form.Item>
          </Space>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap', marginBottom: 12 }}>
          <Button icon={<ReloadOutlined />} loading={refreshing} onClick={refreshNow}>立即刷新</Button>
          {snapshot ? (
            <Text type="secondary" style={{ fontSize: 12 }}>
              数据源 {snapshot.source} · 发布于 {formatTime(snapshot.provider_updated_at)} · 拉取于 {formatTime(snapshot.fetched_at)}
              {' '}
              {snapshot.fresh ? <Tag color="success">有效</Tag> : <Tag color="error">已过期</Tag>}
            </Text>
          ) : (
            <Text type="secondary" style={{ fontSize: 12 }}>尚未获取到自动汇率</Text>
          )}
        </div>
        {overview?.last_error && (
          <Alert type="error" showIcon style={{ marginBottom: 12 }} message={`最近一次刷新失败：${overview.last_error}`} />
        )}
        <style>{`
          .fx-rate-table .ant-table-thead > tr > th,
          .fx-rate-table .ant-table-tbody > tr > td {
            padding: 4px 8px !important;
          }
        `}</style>
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            flexWrap: 'wrap',
            gap: 10,
            padding: '8px 12px',
            marginBottom: 10,
            borderRadius: 6,
            background: 'var(--ant-color-fill-quaternary)',
            border: '1px solid var(--ant-color-border-secondary)',
          }}
        >
          <Space size={8} wrap>
            <Text style={{ fontSize: 12 }}>
              已选 <Text strong>{selectedRowKeys.length}</Text> / {rateRows.length} 项
            </Text>
            <Button
              size="small"
              onClick={() => setSelectedRowKeys(rateRows.map((r) => r.code))}
              disabled={defaultChanged || selectedRowKeys.length === rateRows.length}
            >
              全选
            </Button>
            <Button
              size="small"
              onClick={() => setSelectedRowKeys([])}
              disabled={selectedRowKeys.length === 0}
            >
              清空选择
            </Button>
          </Space>

          <Space size={8} wrap>
            <Text type="secondary" style={{ fontSize: 12 }}>批量汇率规则：</Text>
            <Select
              size="small"
              style={{ width: 108 }}
              value={batchMode}
              disabled={defaultChanged}
              options={RATE_MODE_OPTIONS}
              onChange={(next: RateMode) => {
                setBatchMode(next);
                if (next === 'percent') setBatchValue(100);
                else if (next === 'amount') setBatchValue(0);
                else setBatchValue(null);
              }}
            />
            {batchMode !== 'live' && (
              <InputNumber
                size="small"
                style={{ width: 136 }}
                value={batchValue}
                onChange={(v) => setBatchValue(typeof v === 'number' ? v : null)}
                min={batchMode === 'percent' ? 0.01 : 0}
                max={batchMode === 'percent' ? 1000 : undefined}
                step={batchMode === 'percent' ? 1 : 0.01}
                precision={batchMode === 'percent' ? 2 : undefined}
                disabled={defaultChanged}
                suffix={batchMode === 'percent' ? '%' : undefined}
                placeholder={batchMode === 'percent' ? '100' : batchMode === 'manual' ? '留空取在线汇率' : '0'}
              />
            )}
            <Button
              size="small"
              type="primary"
              ghost
              disabled={defaultChanged || selectedRowKeys.length === 0}
              onClick={handleBatchApplyRule}
            >
              批量应用规则
            </Button>
            <Divider type="vertical" style={{ margin: '0 2px' }} />
            <Text type="secondary" style={{ fontSize: 12 }}>批量展示：</Text>
            <Button
              size="small"
              disabled={defaultChanged || selectedRowKeys.length === 0}
              onClick={() => handleBatchDisplay(true)}
            >
              开启展示
            </Button>
            <Button
              size="small"
              disabled={defaultChanged || selectedRowKeys.length === 0}
              onClick={() => handleBatchDisplay(false)}
            >
              关闭展示
            </Button>
          </Space>
        </div>
        <Table
          className="fx-rate-table"
          size="small"
          rowKey="code"
          rowSelection={{
            selectedRowKeys,
            onChange: (keys) => setSelectedRowKeys(keys),
            getCheckboxProps: () => ({ disabled: defaultChanged }),
          }}
          pagination={false}
          dataSource={rateRows}
          columns={rateColumns}
          scroll={{ x: 940 }}
          style={{ marginBottom: 24 }}
        />
        </>
        ) : (
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 16 }}
            message="关闭状态下：充值只按默认货币收款。支付渠道的币种与默认货币不一致时，该渠道暂停收款。"
          />
        )}

        <div style={{ maxWidth: 640 }}>
          <Divider>通用充值设置</Divider>
          <Form.Item
            label="快捷支付金额"
            name="quick_amounts"
            rules={[{ required: true, message: '请输入快捷支付金额' }]}
            extra="多个金额请用英文逗号分隔，例如：20, 50, 100, 500, 1000, 5000"
          >
            <Input placeholder="20, 50, 100, 500, 1000, 5000" />
          </Form.Item>

          <Form.Item
            label="最小充值金额限制"
            name="min_recharge_amount"
            rules={[{ required: true, message: '请输入最小充值金额' }]}
            extra="设置用户单次最小的充值金额。设置为 0 代表无限制，默认值为 5。支付渠道单独开启限额后，以该渠道的设置为准"
          >
            <InputNumber style={{ width: '100%' }} min={0} step={1} />
          </Form.Item>

          <Form.Item
            label="最大充值金额限制"
            name="max_recharge_amount"
            rules={[{ required: true, message: '请输入最大充值金额' }]}
            extra="设置用户单次最大的充值金额。设置为 0 代表无限制，默认值为 10000。支付渠道单独开启限额后，以该渠道的设置为准"
          >
            <InputNumber style={{ width: '100%' }} min={0} step={1} />
          </Form.Item>

          <Form.Item style={{ marginTop: 16 }}>
            <Button type="primary" htmlType="submit" loading={saving}>
              {t('common.save', '保存设置')}
            </Button>
          </Form.Item>
        </div>
      </Form>
    </div>
  );
};

export default CurrencySettingsTab;
