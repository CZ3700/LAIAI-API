import i18n from '../i18n';

/** 历史脏值 → 规范 type */
const ALIAS: Record<string, string> = {
  'allinpay wechat': 'allinpay_wechat',
  'allinpay alipay': 'allinpay_alipay',
};

const BRAND: Record<string, string> = {
  stripe: 'Stripe',
  anyway: 'Anyway',
  hyperbc: 'HyperBC',
  bonuspay: 'BonusPay',
};

const COLOR: Record<string, string> = {
  manual: 'blue',
  gift: 'cyan',
  registration: 'magenta',
  commission: 'gold',
  alipay: 'blue',
  wechat: 'green',
  allinpay_wechat: 'green',
  allinpay_alipay: 'blue',
  stripe: 'purple',
  anyway: 'cyan',
  bonuspay: 'geekblue',
  hyperbc: 'purple',
  redemption: 'orange',
  redemption_refund: 'orange',
  model_voucher: 'gold',
  coupon_purchase: 'purple',
  coupon_pack: 'purple',
  coupon_subscription: 'blue',
  ark_video_consume: 'volcano',
  ark_video_refund: 'green',
};

const WALLET_RECHARGE_FILTERS = [
  'manual',
  'gift',
  'registration',
  'alipay',
  'wechat',
  'redemption',
  'model_voucher',
  'coupon_purchase',
  'coupon_pack',
  'coupon_subscription',
] as const;

function norm(type: string): string {
  return ALIAS[type] || type;
}

/** 充值类型文案（固定读 translation/finance.*，不依赖调用方 useTranslation 的 ns） */
export function rechargeTypeLabel(type: string, walletType?: string | null): string {
  if (!type) return '-';
  if (type === 'manual' && walletType === 'credit') {
    return i18n.t('finance.recharge_type_manual_credit');
  }
  const key = norm(type);
  if (BRAND[key]) return BRAND[key];
  const i18nKey = `finance.recharge_type_${key}`;
  const label = i18n.t(i18nKey);
  return !label || label === i18nKey ? type : label;
}

export function rechargeTypeColor(type: string): string {
  return COLOR[norm(type)] || 'default';
}

export function rechargeTypeFilters() {
  return WALLET_RECHARGE_FILTERS.map((value) => ({
    text: rechargeTypeLabel(value),
    value,
  }));
}

/**
 * 提取订单号：优先读取实体的 order_no 字段，若无则从 remark 中提取
 * 例如:
 *  - "微信支付充值 订单号:T20260907055127R9a047a0f" -> "T20260907055127R9a047a0f"
 *  - "支付宝充值 订单号：202609070551" -> "202609070551"
 *  - "T20260907055127R9a047a0f" -> "T20260907055127R9a047a0f"
 *  - 若无订单号返回 null
 */
export function extractOrderNo(
  input?: { order_no?: string | null; remark?: string | null } | string | null
): string | null {
  if (!input) return null;
  if (typeof input === 'object') {
    if (input.order_no && typeof input.order_no === 'string' && input.order_no.trim()) {
      return input.order_no.trim();
    }
    return extractOrderNo(input.remark);
  }
  const trimmed = input.trim();
  if (!trimmed) return null;

  // 1. 匹配带 "订单号:" 或 "Order No:" 等前缀的单号
  const match = trimmed.match(/(?:订单号|Order\s*(?:No|Number|Id)|单号)[\s:：]+([a-zA-Z0-9_-]+)/i);
  if (match && match[1]) {
    return match[1];
  }

  // 2. 匹配纯标准订单号 (如系统充值 T20260907... 或 BonusPay 的 BP... 或 赠送金 G20260907...)
  if (/^[TG]\d{14}[A-Za-z0-9_-]+$/.test(trimmed) || /^BP[A-Za-z0-9_-]+$/.test(trimmed)) {
    return trimmed;
  }

  return null;
}

const EXACT_REMARK_KEYS: Record<string, string> = {
  注册赠送: 'finance.sys_remark_registration_gift',
  走专属链接注册特权赠送: 'finance.sys_remark_invite_link_gift',
  邀请成功奖励: 'finance.sys_remark_invite_reward',
  佣金钱包划转到系统钱包: 'finance.sys_remark_commission_transfer',
  '管理员创建用户-系统余额': 'finance.sys_remark_admin_create_system',
  '管理员创建用户-赠送余额': 'finance.sys_remark_admin_create_gift',
  管理员调增系统余额: 'finance.sys_remark_admin_inc_system',
  管理员调减系统余额: 'finance.sys_remark_admin_dec_system',
  管理员调增赠送余额: 'finance.sys_remark_admin_inc_gift',
  管理员调减赠送余额: 'finance.sys_remark_admin_dec_gift',
  管理员调增信控额度: 'finance.sys_remark_admin_inc_credit',
  管理员调减信控额度: 'finance.sys_remark_admin_dec_credit',
  'Administrator Adjustment': 'finance.sys_remark_admin_adjustment',
  管理员调整: 'finance.sys_remark_admin_adjustment',
};

const REFUND_MODE_KEYS: Record<string, string> = {
  按剩余面值比例退还购买款: 'finance.sys_remark_mode_pro_rata_price',
  强制全额退还购买款: 'finance.sys_remark_mode_full_price',
  购买款扣除已用面值后退款: 'finance.sys_remark_mode_deduct_used',
  按剩余时长比例退还购买款: 'finance.sys_remark_mode_pro_rata_time',
  剩余模型面值退入余额: 'finance.sys_remark_mode_remaining_face',
  卡券总面值强制退入余额: 'finance.sys_remark_mode_full_face',
  自定义金额退款: 'finance.sys_remark_mode_custom_refund',
  自定义金额退入余额: 'finance.sys_remark_mode_custom_balance',
};

function trKey(key: string, fallback: string, options?: Record<string, unknown>): string {
  const res = i18n.t(key, options);
  return !res || res === key ? fallback : res;
}

function translateRefundMode(mode: string): string {
  const trimmed = mode.trim();
  const key = REFUND_MODE_KEYS[trimmed];
  return key ? trKey(key, trimmed) : trimmed;
}

const PATTERN_REMARK_RULES: Array<{
  regex: RegExp;
  translate: (m: RegExpMatchArray, raw: string) => string;
}> = [
  {
    regex: /^下级\s+(.+?)\s+(余额调减|退款回冲)，返佣差额从系统钱包扣回$/,
    translate: (m, raw) =>
      trKey(
        m[2] === '余额调减'
          ? 'finance.sys_remark_sub_adjust_clawback'
          : 'finance.sys_remark_sub_refund_clawback',
        raw,
        { user: m[1] }
      ),
  },
  {
    regex: /^模型卡券\(大转盘\):\s*(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_wheel_model_voucher', raw, { name: m[1] }),
  },
  {
    regex: /^模型卡券:\s*(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_model_voucher', raw, { name: m[1] }),
  },
  {
    regex: /^兑换码\(赠送金\):\s*(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_redemption_gift', raw, { name: m[1] }),
  },
  {
    regex: /^兑换码:\s*(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_redemption', raw, { name: m[1] }),
  },
  {
    regex: /^退换兑换余额:\s*(.+?)\s*\(([^)]+)\)，单次\s*#(\d+)$/,
    translate: (m, raw) =>
      trKey('finance.sys_remark_redemption_refund_single', raw, {
        name: m[1],
        code: m[2],
        id: m[3],
      }),
  },
  {
    regex: /^退换兑换余额:\s*(.+?)\s*\(([^)]+)\)，指定用户$/,
    translate: (m, raw) =>
      trKey('finance.sys_remark_redemption_refund_user', raw, { name: m[1], code: m[2] }),
  },
  {
    regex: /^退换兑换余额:\s*(.+?)\s*\(([^)]+)\)$/,
    translate: (m, raw) =>
      trKey('finance.sys_remark_redemption_refund_all', raw, { name: m[1], code: m[2] }),
  },
  {
    regex: /^购买卡券加油包\s+(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_buy_coupon_pack', raw, { name: m[1] }),
  },
  {
    regex: /^续费卡券加油包\s+(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_renew_coupon_pack', raw, { name: m[1] }),
  },
  {
    regex: /^购买卡券会员订阅\s+(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_buy_coupon_sub', raw, { name: m[1] }),
  },
  {
    regex: /^续费卡券会员订阅\s+(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_renew_coupon_sub', raw, { name: m[1] }),
  },
  {
    regex: /^购买卡券\s+(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_buy_coupon', raw, { name: m[1] }),
  },
  {
    regex: /^续费卡券\s+(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_renew_coupon', raw, { name: m[1] }),
  },
  {
    regex: /^卡券加油包占用余额\s+(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_hold_coupon_pack', raw, { name: m[1] }),
  },
  {
    regex: /^卡券会员订阅占用余额\s+(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_hold_coupon_sub', raw, { name: m[1] }),
  },
  {
    regex: /^卡券占用余额\s+(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_hold_coupon', raw, { name: m[1] }),
  },
  {
    regex: /^退回卡券加油包占用余额\s+(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_release_coupon_pack', raw, { name: m[1] }),
  },
  {
    regex: /^退回卡券会员订阅占用余额\s+(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_release_coupon_sub', raw, { name: m[1] }),
  },
  {
    regex: /^退回卡券占用余额\s+(.+)$/,
    translate: (m, raw) => trKey('finance.sys_remark_release_coupon', raw, { name: m[1] }),
  },
  {
    regex: /^兑换卡券退款销毁\(([^)]+)\):\s*(.+)$/,
    translate: (m, raw) =>
      trKey('finance.sys_remark_redeem_voucher_refund_void', raw, {
        mode: translateRefundMode(m[1]),
        name: m[2],
      }),
  },
  {
    regex: /^卡券退款销毁\(([^)]+)\):\s*(.+)$/,
    translate: (m, raw) =>
      trKey('finance.sys_remark_coupon_refund_void', raw, {
        mode: translateRefundMode(m[1]),
        name: m[2],
      }),
  },
  {
    regex: /^购买游戏券:\s*(.+?)\s*\(\+(\d+)张游戏券,\s*([^)]+)\)$/,
    translate: (m, raw) =>
      trKey('finance.sys_remark_buy_game_ticket', raw, {
        name: m[1],
        count: m[2],
        orderNo: m[3],
      }),
  },
  {
    regex: /^退回未使用游戏券:\s*(.+?)\s*\(([^)]+)\)$/,
    translate: (m, raw) =>
      trKey('finance.sys_remark_refund_game_ticket', raw, { name: m[1], orderNo: m[2] }),
  },
  {
    regex: /^大转盘中奖兑换\(赠送金\):\s*(.+?)\s*·\s*(.+?)\s*\(([^)]+)\)$/,
    translate: (m, raw) =>
      trKey('finance.sys_remark_wheel_prize_gift', raw, {
        campaign: m[1],
        prize: m[2],
        code: m[3],
      }),
  },
  {
    regex: /^大转盘兑换码\(赠送金\):\s*(.+?)\s*\(([^)]+)\)$/,
    translate: (m, raw) =>
      trKey('finance.sys_remark_wheel_code_gift', raw, { name: m[1], code: m[2] }),
  },
  {
    regex: /^方舟视频消费\(赠送\)\s+binding=(\d+)\s+Δ=([0-9.+-]+)$/,
    translate: (m, raw) =>
      trKey('finance.sys_remark_ark_consume_gift', raw, { binding: m[1], delta: m[2] }),
  },
  {
    regex: /^方舟视频消费\s+binding=(\d+)\s+Δ=([0-9.+-]+)$/,
    translate: (m, raw) =>
      trKey('finance.sys_remark_ark_consume', raw, { binding: m[1], delta: m[2] }),
  },
  {
    regex: /^方舟视频账单回退\(赠送\)\s+binding=(\d+)\s+Δ=([0-9.+-]+)$/,
    translate: (m, raw) =>
      trKey('finance.sys_remark_ark_refund_gift', raw, { binding: m[1], delta: m[2] }),
  },
  {
    regex: /^方舟视频账单回退\s+binding=(\d+)\s+Δ=([0-9.+-]+)$/,
    translate: (m, raw) =>
      trKey('finance.sys_remark_ark_refund', raw, { binding: m[1], delta: m[2] }),
  },
];

function translateSystemRemark(text: string): string {
  if (!text) return '';
  const exactKey = EXACT_REMARK_KEYS[text];
  if (exactKey) {
    return trKey(exactKey, text);
  }
  for (const rule of PATTERN_REMARK_RULES) {
    const match = text.match(rule.regex);
    if (match) {
      return rule.translate(match, text);
    }
  }
  return text;
}

/**
 * 精简完善备注：去除备注中已被单独提取出订单号的冗余说明，并对系统自动生成的备注执行多语言翻译
 * 例如:
 *  - "微信支付充值 订单号:T20260907055127R9a047a0f" -> "" (订单号与类型已在单独列展示，备注清空为 -)
 *  - "支付宝充值 订单号:202609070551" -> ""
 *  - "T20260907055127R9a047a0f" -> ""
 *  - "BonusPay 充值 订单号:BP123 10 USDT (txHash: 0xabc)" -> "10 USDT (txHash: 0xabc)"
 *  - "初始龙套户 公司支付宝已收款5000元 充值可提" -> "初始龙套户 公司支付宝已收款5000元 充值可提" (业务备注完整保留)
 */
export function cleanRemark(remark?: string | null, _orderNo?: string | null): string {
  if (!remark || typeof remark !== 'string') return '';
  const text = remark.trim();
  if (!text) return '';

  // 1. 若纯粹是系统自动生成的充值/赠送单号说明（单号已提至订单号列，类型已有标签，无其它附言时直接清空）
  if (
    /^([^\s]+充值\s*)?(?:订单号|Order\s*(?:No|Id)|单号)[\s:：]*[a-zA-Z0-9_-]+$/i.test(text) ||
    /^[TG]\d{14}[A-Za-z0-9_-]+$/.test(text) ||
    /^BP[A-Za-z0-9_-]+$/.test(text)
  ) {
    return '';
  }

  // 2. 若备注中包含单号且还有其他附言信息，仅剔除冗余的单号前缀部分，保留真实业务附言
  const orderTokenRe = /(?:[^\s]+充值\s*)?(?:订单号|Order\s*(?:No|Id)|单号)[\s:：]*[a-zA-Z0-9_-]+/gi;
  const cleaned = orderTokenRe.test(text)
    ? text
        .replace(orderTokenRe, '')
        .replace(/\(\s*\)|（\s*）/g, '')
        .replace(/^[\s,，:：;；-]+|[\s,，:：;；-]+$/g, '')
        .trim()
    : text;

  return translateSystemRemark(cleaned);
}
