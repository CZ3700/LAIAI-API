import i18n from '../i18n';

/** 列表级支付渠道 ID（通联聚合为 allinpay） */
type PaymentChannelId =
  | 'alipay'
  | 'wechat'
  | 'allinpay'
  | 'stripe'
  | 'anyway'
  | 'hyperbc';

/** 实际下单用的 payment_method（通联子渠道拆分） */
export type PaymentMethodId =
  | 'alipay'
  | 'wechat'
  | 'allinpay_alipay'
  | 'allinpay_wechat'
  | 'stripe'
  | 'anyway'
  | 'hyperbc';

export interface PaymentChannelUiItem {
  id: PaymentChannelId | string;
  sort_order: number;
  enabled: boolean;
  display_name?: string | null;
  display_name_en?: string | null;
  subtitle?: string | null;
  subtitle_en?: string | null;
  logo_url?: string | null;
  /** 通联子渠道：微信 */
  allinpay_wechat_enabled?: boolean;
  /** 通联子渠道：支付宝 */
  allinpay_alipay_enabled?: boolean;
  /** 公开接口：已开启的通联子渠道 payment_method */
  allinpay_methods?: string[];
  /** 渠道收款币种（按顺序优先）；后台未勾选为 null，公开接口为生效列表 */
  currencies?: string[] | null;
  /** 公开接口：按默认货币下单时渠道实际收款币种 */
  pay_currency?: string;
  pay_currency_symbol?: string;
  /** 公开接口：1 默认货币 = pay_rate 收款币种（跨币种时已含加价） */
  pay_rate?: number;
  /** 公开接口：收款币种最小单位小数位 */
  pay_decimals?: number;
  /** 开启后使用本渠道最小/最大充值，关闭则沿用全局通用充值设置 */
  recharge_limit_enabled?: boolean;
  /** 渠道最小充值（默认货币）。0 表示不限制，仅单独开启时生效 */
  min_recharge_amount?: number;
  /** 渠道最大充值（默认货币）。0 表示不限制，仅单独开启时生效 */
  max_recharge_amount?: number;
  /** 后台：钱包充值是否加收手续费 */
  fee_enabled?: boolean;
  /** 手续费百分比（3 表示 3%）；公开接口仅在开启且大于 0 时下发 */
  fee_percent?: number;
}

export interface PaymentChannelDefaultMeta {
  id: PaymentChannelId;
  /** 系统默认中文名称（未自定义时使用） */
  defaultName: string;
  /** 系统默认英文名称（非中文站点语言使用） */
  defaultNameEn: string;
  /** 系统默认中文副标题/角标 */
  defaultSubtitle: string;
  /** 系统默认英文副标题/角标 */
  defaultSubtitleEn: string;
  /** 所属网关配置 key */
  gatewayKey:
    | 'payment_wechat'
    | 'payment_alipay'
    | 'payment_stripe'
    | 'payment_hyperbc'
    | 'payment_allinpay'
    | 'payment_anyway';
  /** 默认排序，越大越靠前 */
  defaultSort: number;
  accent: string;
}

/** 内置渠道目录（与后端 payment_channel_catalog 保持一致） */
const PAYMENT_CHANNEL_CATALOG: PaymentChannelDefaultMeta[] = [
  { id: 'alipay', defaultName: '支付宝', defaultNameEn: 'Alipay', defaultSubtitle: '快捷', defaultSubtitleEn: 'code pay', gatewayKey: 'payment_alipay', defaultSort: 70, accent: '#1677ff' },
  { id: 'wechat', defaultName: '微信支付', defaultNameEn: 'WeChat Pay', defaultSubtitle: '快捷', defaultSubtitleEn: 'code pay', gatewayKey: 'payment_wechat', defaultSort: 60, accent: '#07c160' },
  { id: 'allinpay', defaultName: '通联支付', defaultNameEn: 'Allinpay', defaultSubtitle: '微信/支付宝/信用卡', defaultSubtitleEn: 'wechat/alipay', gatewayKey: 'payment_allinpay', defaultSort: 50, accent: '#1677ff' },
  { id: 'stripe', defaultName: 'Stripe 信用卡', defaultNameEn: 'Stripe Card', defaultSubtitle: '银行卡/支付宝', defaultSubtitleEn: 'Cards/Alipay', gatewayKey: 'payment_stripe', defaultSort: 30, accent: '#635bff' },
  { id: 'anyway', defaultName: 'Anyway 银行卡', defaultNameEn: 'Anyway Card', defaultSubtitle: '银行卡/Apple Pay', defaultSubtitleEn: 'Cards/Wallets', gatewayKey: 'payment_anyway', defaultSort: 20, accent: '#0f766e' },
  { id: 'hyperbc', defaultName: 'HyperBC', defaultNameEn: 'HyperBC', defaultSubtitle: 'Web3', defaultSubtitleEn: 'Web3', gatewayKey: 'payment_hyperbc', defaultSort: 10, accent: '#8b5cf6' },
];

export const getChannelMeta = (id: string): PaymentChannelDefaultMeta | undefined =>
  PAYMENT_CHANNEL_CATALOG.find((c) => c.id === id);

const isZhLocale = (lang?: string): boolean => (lang || '').toLowerCase().startsWith('zh');

export const resolveChannelName = (item: PaymentChannelUiItem, lang?: string): string => {
  const meta = getChannelMeta(item.id);
  if (isZhLocale(lang)) {
    const custom = (item.display_name || '').trim();
    if (custom) return custom;
    return i18n.t(`recharge.channel_${item.id}_name`, { defaultValue: meta?.defaultName || item.id });
  }
  const customEn = (item.display_name_en || '').trim();
  if (customEn) return customEn;
  return i18n.t(`recharge.channel_${item.id}_name`, { defaultValue: meta?.defaultNameEn || meta?.defaultName || item.id });
};

export const resolveChannelSubtitle = (item: PaymentChannelUiItem, lang?: string): string => {
  const meta = getChannelMeta(item.id);
  if (isZhLocale(lang)) {
    const custom = (item.subtitle || '').trim();
    if (custom) return custom;
    return i18n.t(`recharge.channel_${item.id}_subtitle`, { defaultValue: meta?.defaultSubtitle || '' });
  }
  const customEn = (item.subtitle_en || '').trim();
  if (customEn) return customEn;
  return i18n.t(`recharge.channel_${item.id}_subtitle`, { defaultValue: meta?.defaultSubtitleEn || '' });
};

export const getAllinpayMethods = (item: PaymentChannelUiItem): PaymentMethodId[] => {
  // 公开接口若带了 allinpay_methods（含空数组），以它为准，避免回退成「双开」
  if (Array.isArray(item.allinpay_methods)) {
    return item.allinpay_methods.filter(
      (m): m is PaymentMethodId => m === 'allinpay_wechat' || m === 'allinpay_alipay',
    );
  }
  const methods: PaymentMethodId[] = [];
  if (item.allinpay_wechat_enabled !== false) methods.push('allinpay_wechat');
  if (item.allinpay_alipay_enabled !== false) methods.push('allinpay_alipay');
  return methods;
};

export const mergeChannelList = (
  saved?: PaymentChannelUiItem[] | null,
): PaymentChannelUiItem[] => {
  const raw = saved || [];
  const byId = new Map(raw.map((c) => [c.id, c]));

  // 兼容旧版拆分渠道
  const legacyWechat = byId.get('allinpay_wechat');
  const legacyAlipay = byId.get('allinpay_alipay');
  if (!byId.has('allinpay') && (legacyWechat || legacyAlipay)) {
    byId.set('allinpay', {
      id: 'allinpay',
      sort_order: Math.max(legacyWechat?.sort_order || 0, legacyAlipay?.sort_order || 0, 50),
      enabled: !!(legacyWechat?.enabled || legacyAlipay?.enabled),
      display_name: legacyWechat?.display_name || legacyAlipay?.display_name || null,
      display_name_en: legacyWechat?.display_name_en || legacyAlipay?.display_name_en || null,
      subtitle: legacyWechat?.subtitle || legacyAlipay?.subtitle || null,
      subtitle_en: legacyWechat?.subtitle_en || legacyAlipay?.subtitle_en || null,
      logo_url: legacyWechat?.logo_url || legacyAlipay?.logo_url || null,
      allinpay_wechat_enabled: legacyWechat ? !!legacyWechat.enabled : true,
      allinpay_alipay_enabled: legacyAlipay ? !!legacyAlipay.enabled : true,
    });
  }

  return PAYMENT_CHANNEL_CATALOG.map((meta) => {
    const existing = byId.get(meta.id);
    if (existing) {
      const wechatOn = existing.allinpay_wechat_enabled !== false;
      const alipayOn = existing.allinpay_alipay_enabled !== false;
      return {
        id: meta.id,
        sort_order: existing.sort_order ?? meta.defaultSort,
        enabled: !!existing.enabled,
        display_name: existing.display_name ?? null,
        display_name_en: existing.display_name_en ?? null,
        subtitle: existing.subtitle ?? null,
        subtitle_en: existing.subtitle_en ?? null,
        logo_url: existing.logo_url ?? null,
        allinpay_wechat_enabled: meta.id === 'allinpay' ? wechatOn : undefined,
        allinpay_alipay_enabled: meta.id === 'allinpay' ? alipayOn : undefined,
        currencies: existing.currencies ?? null,
        recharge_limit_enabled: !!existing.recharge_limit_enabled,
        min_recharge_amount: Number(existing.min_recharge_amount) || 0,
        max_recharge_amount: Number(existing.max_recharge_amount) || 0,
        fee_enabled: !!existing.fee_enabled,
        fee_percent: Number(existing.fee_percent) || 0,
      };
    }
    return {
      id: meta.id,
      sort_order: meta.defaultSort,
      enabled: false,
      display_name: null,
      display_name_en: null,
      subtitle: null,
      subtitle_en: null,
      logo_url: null,
      ...(meta.id === 'allinpay'
        ? { allinpay_wechat_enabled: true, allinpay_alipay_enabled: true }
        : {}),
      recharge_limit_enabled: false,
      min_recharge_amount: 0,
      max_recharge_amount: 0,
      fee_enabled: false,
      fee_percent: 0,
    };
  }).sort((a, b) => (b.sort_order || 0) - (a.sort_order || 0) || a.id.localeCompare(b.id));
};
