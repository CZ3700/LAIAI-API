// Types for the Tkeapi frontend

export interface User {
  /** LAIAI: 当前活动组织 ID 与平台角色（superadmin/admin/user） */
  org_id?: number;
  plat_role?: string;
  id: string;
  uid: string;
  username: string;
  email: string;
  nickname?: string;
  /** 名 */
  first_name?: string;
  /** 姓氏 */
  last_name?: string;
  /** 出生年月，格式 YYYY-MM */
  birth_ym?: string;
  mobile?: string;
  wechat_id?: string;
  wechat_name?: string;
  google_id?: string;
  google_name?: string;
  role: 'admin' | 'user';
  balance: number;
  used_quota: number;
  user_group: string;
  admin_group_id?: number;
  permissions?: string[];
  edit_permissions?: string[];
  is_active: boolean;
  level_name?: string;
  level_id?: number;
  created_at: string;
  updated_at: string;
  register_ip?: string;
  /** 最近一次登录或接口请求的客户端 IP */
  last_active_ip?: string;
  admin_remark?: string;
  referral_history?: string;
  referred_by?: string;
  allow_view_log_details?: number;
  gift_balance?: number;
  gift_used_quota?: number;
  /** 佣金钱包余额。列表仅在大于 0 时展示 */
  commission_balance?: number;
  /** 用户模型单独折扣(JSON: {"mid": discount})，优先于等级折扣 */
  model_discounts?: string;
  timezone?: string;
  /** 信控额度 */
  credit_limit?: number;
  /** 是否允许在线支付：1-允许，0-禁止 */
  pay_enabled?: number;
  /** 用户头像 URL */
  avatar?: string;
  /** 用户通知订阅偏好(JSON) */
  notification_preferences?: string;
  invoice_enabled?: number;
  invoice_mode?: string;
  invoice_config?: string;
  /** 等级是否开启专属推广：1 开启 */
  marketing_enabled?: number;
  /** 等级返利比例，0 到 1 */
  commission_ratio?: number;
  /** 是否已通过个人实名（用户列表接口填充） */
  kyc_personal?: boolean;
  /** 是否已通过企业实名（用户列表接口填充） */
  kyc_enterprise?: boolean;
  /** 个人实名最新审核状态（用户列表接口填充） */
  kyc_personal_status?: UserKycStatus | string | null;
  /** 企业实名最新审核状态（用户列表接口填充） */
  kyc_enterprise_status?: UserKycStatus | string | null;
}

export interface UserLevel {
  id: number;
  name: string;
  group_key: string;
  discount: number;
  /** 折扣模式: 0=不选择(跟随系统全站与等级), 1=使用全站折扣, 2=使用等级折扣 */
  discount_type?: number;
  commission_ratio: number;
  /** consumption=按实际消费，recharge=按充值 */
  commission_basis?: string;
  /** 佣金钱包单次划转下限，0 为不限制 */
  commission_transfer_min?: number;
  /** 佣金钱包单次划转上限，0 为不限制 */
  commission_transfer_max?: number;
  invite_reward_inviter: number;
  invite_reward_invitee: number;
  daily_invite_limit: number;
  marketing_enabled: number;
  /** 专属推广模式下通过邀请链接注册自动分配的目标用户等级 group_key（空字符串表示跟随系统默认注册等级） */
  invite_target_group_key?: string;
  /** 受邀分配等级有效期模式: permanent=永久有效, days=按天有效, hours=按小时有效 */
  invite_target_duration_mode?: 'permanent' | 'days' | 'hours' | string;
  invite_target_duration_days?: number;
  invite_target_duration_hours?: number;
  /** 专属推广模式下通过邀请链接进入注册页是否单独支持用户名注册 (1=支持, 0=跟随全局) */
  invite_allow_username_reg?: number;
  /** 专属推广模式下通过邀请链接进入注册页是否单独支持邮箱注册 (1=支持, 0=跟随全局) */
  invite_allow_email_reg?: number;
  is_default: number;
  /** 1=可以在会员卡券方案里被选为升级目标 */
  purchasable?: number;
  max_token_count: number;
  allow_view_log_details: number;
  invoice_enabled?: number;
  invoice_mode?: string;
  invoice_config?: string;
  description: string;
  sort_order?: number;
  created_at: string;
  user_count?: number;
}

export interface SellerInvoiceSubject {
  id: string;
  company_name: string;
  tax_id: string;
  contact_email?: string;
  company_address?: string;
  company_phone?: string;
  company_website?: string;
  website?: string;
  bank_name?: string;
  bank_account?: string;
  support_normal: boolean; // 支持增值税普通发票
  support_special: boolean; // 支持增值税专用发票
  is_default?: boolean;
  enabled?: boolean;
}

export interface InvoiceConfig {
  company_name?: string;
  company_address?: string;
  tax_id?: string;
  contact_email?: string;
  company_phone?: string;
  company_website?: string;
  website?: string;
  currency_symbol?: string;
  invoice_prefix?: string;
  receipt_prefix?: string;
  notes?: string;
  show_invoices?: boolean;
  show_receipts?: boolean;
  china_min_amount?: number;
  china_invoice_content?: string;
  china_tax_rate?: number;
  china_invoice_notice?: string;
  sellers?: SellerInvoiceSubject[];
}

export interface FundTypeStats {
  total_real_paid: number;
  total_gift_recharged: number;
  total_real_consumed: number;
  total_gift_consumed: number;
  current_balance: number;
  current_gift_balance: number;
  current_credit_limit: number;
  total_invoiced_amount: number;
  remaining_invoiceable_amount: number;
}

export interface InvoiceItem {
  id: string;
  order_id?: number;
  date: string;
  raw_date: string;
  payment: string;
  amount: number;
  currency: string;
  payment_method: string;
  description: string;
  fund_type?: string;
  fund_type_label?: string;
  is_invoiceable?: boolean;
}

export interface ReceiptItem {
  id: string;
  order_id?: number;
  date: string;
  raw_date: string;
  amount: number;
  currency: string;
  payment_method: string;
  transaction_id?: string;
  fund_type?: string;
  fund_type_label?: string;
}

export interface InvoiceRequestRecord {
  id: number;
  request_no?: string;
  user_id: string;
  username?: string;
  user_email?: string;
  user_nickname?: string;
  user_uid?: string;
  invoice_type: string;
  title_type: string;
  title: string;
  tax_number?: string;
  company_address?: string;
  company_phone?: string;
  bank_name?: string;
  bank_account?: string;
  amount: number;
  email: string;
  remark?: string;
  status: string;
  reject_reason?: string;
  invoice_file_url?: string;
  invoice_code?: string;
  invoice_number?: string;
  admin_remark?: string;
  issued_at?: string;
  operator_id?: string;
  operator_name?: string;
  created_at: string;
  updated_at: string;
}


export interface AdminGroup {
  id: number;
  name: string;
  permissions: string; // JSON string from backend
  description?: string;
  sort_order?: number;
  user_count?: number;
  created_at: string;
  updated_at: string;
}

export interface ModelModel {
  id: number;
  mid: string;
  name: string;
  model_id: string;
  original_id?: string;
  model_id_alias?: string;  // 模型ID别名映射值
  provider_id?: number;
  api_provider_id?: number;
  type_id?: number;
  group_ratios: string;
  billing_rule_id?: number | null;
  pre_deduction?: number;
  forward_rule_ids?: string;
  is_active: number;
  enable_log_content?: number;
  site_discount?: number;
  site_discount_enabled?: number;
  global_discount?: number;
  global_discount_enabled?: number;
  discount_schedule?: string;
  logo?: string;
  remark?: string;
  description?: string;
  feature_attributes?: string;
  is_system?: number;
  library_mid?: string | null;
  is_listed?: number;
  listed_count?: number;
  library_kind?: 'catalog' | 'unlisted' | string | null;
  sort_order?: number;
  created_at: string;
  updated_at?: string;
}

export interface ModelProvider {
  id: number;
  name: string;
  name_en?: string;
  sort_order: number;
  is_active: number | boolean;
  is_system?: number;
  remark?: string;
  logo?: string;
  model_count?: number;
  created_at: string;
  updated_at: string;
}

export interface ModelType {
  id: number;
  name: string;
  name_en?: string;
  sort_order: number;
  is_active: number | boolean;
  is_system?: number;
  logo?: string;
  default_features?: string;
  model_count?: number;
  created_at: string;
  updated_at: string;
}

export interface ClassificationCount {
  id: number | null;
  name: string;
  name_en?: string;
  count: number;
  logo?: string;
  is_system?: number;
}

export interface RechargeRecord {
  id: number;
  amount: number;
  recharge_type: string;
  order_no?: string;
  remark?: string;
  wallet_type?: string;
  /** 在线充值支付手续费（不入余额），非在线充值为 0 */
  fee_amount?: number;
  created_at: string;
}

export interface UserDiscountedModel {
  mid: string;
  name: string;
  model_id: string;
  actual_discount: number;
  discount?: number;
  limit?: number;
  delisted?: boolean;
  is_disabled?: boolean;
}

export interface WalletStats {
  balance: number;
  gift_balance: number;
  credit_limit: number;
  total_consumption: number;
  commission_balance: number;
  total_referred: number;
  marketing_enabled: boolean;
  commission_ratio: number;
  /** consumption=按实际消费，recharge=按充值 */
  commission_basis?: string;
  /** 单次划转下限，0 为不限制 */
  commission_transfer_min?: number;
  /** 单次划转上限，0 为不限制 */
  commission_transfer_max?: number;
  /** 是否允许在线支付 */
  pay_enabled?: boolean;
  /** 用户单独优惠模型列表（仅包含页面展示字段） */
  discounted_models?: UserDiscountedModel[];
}

export interface Channel {
  id: number;
  name: string;
  provider_type: string;
  base_url: string;
  models: string[];
  model_mapping: Record<string, string>;
  user_groups: string[];
  exclude_user_groups?: string[];
  group_aid?: string;
  preset_id?: number | null;
  category_id?: number | null;
  sort_order: number;
  priority: number;
  weight: number;
  status: number; // 1=active, 0=disabled
  balance?: number;
  quota_limit: number; // -1 = unlimited
  quota_used: number;
  daily_quota_limit?: number;
  daily_quota_used?: number;
  weekly_quota_limit?: number;
  weekly_quota_used?: number;
  monthly_quota_limit?: number;
  monthly_quota_used?: number;
  last_reset_day?: string;
  last_reset_week?: string;
  last_reset_month?: string;
  config?: string | any;
  rate: number;
  created_at: string;
  updated_at?: string;
  last_used_at?: string | null;
}

export interface ChannelCategory {
  id: number;
  name: string;
  name_en?: string;
  sort_order: number;
  is_active: number | boolean;
  is_system?: number;
  created_at: string;
  updated_at: string;
}

export interface ApiToken {
  id: number;
  user_id: string;
  token_key: string;
  key?: string;
  kid?: string;
  name: string;
  expires_at?: string | null;
  quota_limit: number;
  quota_used: number;
  allowed_models: string[];
  allowed_ips: string;
  rps_limit: number;
  rpm_limit: number;
  billing_rule: string;
  forward_rule_ids?: string;
  is_active: number | boolean;
  only_playground?: number;
  only_playground_2026?: number;
  high_availability?: number;

  last_used_at?: string;
  created_at?: string;

  daily_quota_limit: number;
  daily_quota_used: number;
  weekly_quota_limit: number;
  weekly_quota_used: number;
  monthly_quota_limit: number;
  monthly_quota_used: number;
  last_reset_day?: string;
  last_reset_week?: string;
  last_reset_month?: string;
  /** 后端按站点时区计算的当期有效已用 */
  current_daily_quota_used?: number;
  current_weekly_quota_used?: number;
  current_monthly_quota_used?: number;
  /** 开关开启但网关会拒绝的原因（后端派生）；开关关闭或可用时为空 */
  unavailable_reason?: 'arrears' | 'no_funds' | 'expired' | 'quota_total' | 'quota_daily' | 'quota_weekly' | 'quota_monthly' | null;
}

export interface Redemption {
  id: number;
  name: string;
  code: string;
  quota: number;
  /** 余额兑换码的赠送金余额面值（计入 users.gift_balance） */
  gift_quota?: number;
  /** 模型卡券面额价值（展示价值） */
  worth_quota?: number;
  /** 模型卡券默认模型折扣倍率（1.0=原价） */
  voucher_discount?: number;
  /** 模型卡券单模型专属折扣 JSON 字符串 */
  model_discounts?: string;
  is_used: number;
  used_at?: string;
  used_by?: string;
  created_at: string;
  updated_at: string;
  /** 过期时间，空/null = 长期有效 */
  expires_at?: string | null;
  /** 开始生效时间，空/null = 生成后立即生效 */
  starts_at?: string | null;
  /** 单兑换码兑换次数，-1 = 不限（兼容历史 0） */
  max_uses?: number;
  /** 已兑换次数（按单个兑换码累计） */
  used_count?: number;
  /** 单兑换码单用户兑换次数，-1 = 不限（兼容历史 0） */
  per_user_limit?: number;
  /** 同一活动下单用户可兑换次数，-1 = 不限 */
  per_user_activity_limit?: number;
  /** 状态: 1=正常, 0=禁用, -1=作废, -2=已退换兑换余额 */
  status?: number;
  /** balance | model_voucher */
  code_type?: string;
  /** 模型卡券可消耗的模型 ID，JSON 数组字符串 */
  allowed_models?: string;
  /** 兑换后有效天数，0=长期有效 */
  valid_days?: number;
  /** 活动 ID，4 位数字 */
  activity_id?: string;
  /** 0=全部满足，1=满足其中一项 */
  condition_match?: number;
  cond_min_balance?: number;
  cond_month_spend?: number;
  cond_total_spend?: number;
  cond_month_recharge?: number;
  cond_total_recharge?: number;
  /** 活动简介 / 广告语，发放与兑换时展示 */
  description?: string;
  /** 管理员备注说明，仅后台管理者可见 */
  remark?: string;
  redeemers?: Array<{
    user_id?: string;
    username: string;
    uid: string;
    remark: string;
    referrer_username: string;
    referrer_uid: string;
    /** 该用户兑换此码的次数，含已退换 */
    times?: number;
    /** 该用户尚未退换的系统余额入账合计 */
    amount?: number;
    /** 该用户尚未退换的赠送金余额入账合计 */
    gift_amount?: number;
    uses?: Array<{
      /** redemption_logs.id，0 表示没有逐次日志的历史单次兑换 */
      id: number;
      amount: number;
      gift_amount?: number;
      created_at?: string | null;
      /** 0=未退换，1=已退换这一次 */
      refunded?: number;
    }>;
  }>;
}

export interface RedemptionGroup {
  name: string;
  total_count: number;
  total_quota: number;
  total_gift_quota?: number;
  total_worth_quota?: number;
  voucher_discount?: number;
  model_discounts?: string;
  created_at: string;
  expires_at?: string | null;
  /** 开始生效时间，空/null = 生成后立即生效 */
  starts_at?: string | null;
  total_used_count: number;
  max_uses: number;
  per_user_limit: number;
  /** 同一活动下单用户可兑换次数，-1 = 不限 */
  per_user_activity_limit?: number;
  code_type?: string;
  allowed_models?: string;
  valid_days?: number;
  activity_id?: string;
  condition_match?: number;
  cond_min_balance?: number;
  cond_month_spend?: number;
  cond_total_spend?: number;
  cond_month_recharge?: number;
  cond_total_recharge?: number;
  /** 活动简介 / 广告语，发放与兑换时展示 */
  description?: string;
  /** 管理员备注说明，仅后台管理者可见 */
  remark?: string;
}

export interface RequestLog {
  id: number;
  log_id?: string;
  user_id: string;
  channel_id?: number;
  token_id?: number;
  model: string;
  prompt_tokens: number;
  completion_tokens: number;
  /** 缓存命中的 Token 数量（属于输入的子集） */
  cached_tokens?: number;
  cost: number;
  latency_ms: number;
  status_code: number;
  endpoint: string;
  error_message?: string;
  upstream_url?: string;
  upstream_request_id?: string;
  channel_group_aid?: string;
  /** 请求当时是否走 HA 组（写路径快照） */
  is_ha?: number;
  yid?: string;
  /** 视频模型输入是否包含视频(1=含视频, 0=无视频) */
  has_video?: number;
  /** 任务动作类型(如 '视频', '图片', '聊天' 等) */
  action_type?: string;
  /** 列表 JOIN models.mid */
  mid?: string;
  channel_name?: string;
  user_nickname?: string;
  /** 用户管理员备注（管理端可见） */
  user_admin_remark?: string;
  user_uid?: string;
  user_group?: string;
  user_level_name?: string;
  token_name?: string;
  token_kid?: string;
  /** 令牌当前是否开启高可用（JOIN 令牌表，非 logs.is_ha 渠道快照） */
  token_ha?: number;
  /** 是否创作中心专用令牌（only_playground / only_playground_2026） */
  token_pg?: number;
  request_content?: string;
  response_content?: string;
  post_response?: string;
  upstream_req_content?: string;
  is_stream?: number;
  /** 列表通常不返回；展开 detail 拉取 */
  billing_detail?: string;
  /** 列表轻量标记，替代传输 billing_detail 全文 */
  billing_refunded?: boolean;
  billing_failed?: boolean;
  /** 列表用量：缓存创建量（后端从计费明细解析）；缓存读取用 cached_tokens */
  billing_cache_creation?: number;
  billing_web_search?: number;
  billing_pid?: string;
  forward_eid?: string;
  /** 列表不返回；仅展开 `/logs/{id}/detail` */
  plugin_tag?: string;
  channel_config_id?: number;
  sub_channel_name?: string;
  task_id?: string;
  created_at: string;
  /** 归档时间（仅 logs_archive 冷表包含） */
  archived_at?: string;
}

export interface DashboardStats {
  total_requests: number;
  total_tokens: number;
  total_cost: number;
  total_users: number;
  total_channels: number;
  total_api_tokens: number;
  today_requests: number;
  today_tokens: number;
  today_cost: number;
  today_active_tokens: number;
  yesterday_requests: number;
  yesterday_tokens: number;
  yesterday_cost: number;
  yesterday_active_tokens: number;
  recent_logs: RequestLog[];
  model_stats: {
    model: string;
    count: number;
    total_tokens: number;
    total_cost: number;
    last_three_days?: {
      date: string;
      count: number;
      total_cost: number;
    }[];
  }[];
    daily_trends?: {
    date: string;
    requests: number;
    cost: number;
  }[];
  /** 看板首屏随统计一并返回，不走 180s SWR */
  live_metrics?: LiveMetricsSnapshot;
}

/** 实时吞吐：QPS / RPM / TPM / Task */
export interface LiveMetricsSnapshot {
  qps: number;
  rpm: number;
  tpm: number;
  task: number;
  pending_tasks?: number;
}

export interface LiveMetricsResponse {
  metrics: LiveMetricsSnapshot;
  scope: 'global' | 'self' | string;
  ts: number;
}

interface ModelStat30d {
  model: string;
  count: number;
  total_tokens: number;
  total_cost: number;
}

interface ModelDailyStat {
  date: string;
  model: string;
  count: number;
  total_cost: number;
}

export interface ModelTrend30dResponse {
  top_models: ModelStat30d[];
  daily_data: ModelDailyStat[];
}

interface SiteSettings {
  name: string;
  title: string;
  intro?: string;
  keywords: string;
  description: string;
  favicon?: string;
  logo?: string;
  /** 控制台 Logo/站点名点击跳转地址，留空则不可点击 */
  logo_title_url?: string;
  login_title?: string;
  /** 登录页标题点击跳转地址；留空则回退到 logo_title_url，仍空则不可点击 */
  login_title_url?: string;
  login_subtitle?: string;
  enable_multilingual?: boolean;
  enable_theme_toggle?: boolean;
  default_theme?: 'light' | 'dark';
  copyright?: string;
  supported_languages?: string[];
  default_language?: string;
  admin_path?: string;
  login_style?: 'split' | 'classic';
  login_quote?: string;
  default_timezone?: string;
  /** 管理后台展示时区；空则回退站点运营时区 */
  admin_display_timezone?: string;
  show_timezone?: boolean;
  ip_blacklist_enabled?: boolean;
  ip_blacklist?: string[];
  ip_whitelist_enabled?: boolean;
  ip_whitelist?: string[];
}

interface AuxiliaryCurrency {
  code: string;
  symbol: string;
  exchange_rate: number;
  enabled: boolean;
}

interface CurrencySettings {
  default_currency: string;
  currency_symbol: string;
  currency_unit: string;
  token_ratio: number;
  /** 由实时 / 手动汇率推导（只读） */
  auxiliary_currencies?: AuxiliaryCurrency[];
  fx?: FxSettings;
  quick_amounts?: number[];
  min_recharge_amount?: number;
  max_recharge_amount?: number;
}

/** 单币种汇率：跟随实时、按百分比或数值加在实时汇率上，或手填固定汇率 */
export interface FxCurrencyConfig {
  code: string;
  manual: boolean;
  manual_rate: number;
  /** 空 = 跟随实时；percent / amount / manual */
  adjust?: '' | 'percent' | 'amount' | 'manual';
  adjust_value?: number;
  display: boolean;
}

interface FxSettings {
  /** 实时汇率总开关，缺省为关闭 */
  live_enabled?: boolean;
  mode: 'auto' | 'manual';
  refresh_interval_minutes: number;
  markup_percent: number;
  max_stale_hours: number;
  manual_base?: string;
  currencies: FxCurrencyConfig[];
}

interface LoginSettings {
  enable_username_login: boolean;
  enable_mobile_login: boolean;
  enable_email_login: boolean;
  enable_wechat_login: boolean;
  enable_google_login: boolean;
}

export interface RegistrationSettings {
  email_verification_enabled?: boolean;
  enable_username_registration: boolean;
  enable_email_registration: boolean;
  enable_mobile_registration: boolean;
  enable_password_recovery: boolean;
  /** 是否要求绑定手机号 */
  require_bind_mobile?: boolean;
  /** 是否要求绑定邮箱 */
  require_bind_email?: boolean;
  /** all=弹窗必须绑定全部 / any=弹窗必须绑定其一 / prompt_all=仅弹窗提示绑定全部 / prompt_any=仅弹窗提示绑定其一；prompt_only 为旧值，等同 prompt_all */
  bind_enforcement?: 'all' | 'any' | 'prompt_all' | 'prompt_any' | 'prompt_only';
  /** 是否开启站点用户实名认证（KYC） */
  enable_user_kyc?: boolean;
  /** 同一邮箱最多绑定用户数 */
  max_accounts_per_email?: number;
  /** 同一手机号最多绑定用户数 */
  max_accounts_per_mobile?: number;
  /** 同一微信最多绑定用户数 */
  max_accounts_per_wechat?: number;
  // 以下字段仅管理后台完整接口返回，公开接口不包含
  ip_rate_limit_enabled?: boolean;
  ip_daily_limit?: number;
  ip_window_days?: number;
  ip_window_limit?: number;
  email_validation_strict?: boolean;
  email_whitelist_enabled?: boolean;
  email_whitelist?: string[];
}

export type UserKycType = 'personal' | 'enterprise';
export type UserKycStatus = 'none' | 'pending' | 'approved' | 'rejected' | 'expired';
export type KycIdDocType = 'id_card' | 'passport' | 'driver_license' | 'other';
export type KycCompanyDocType = 'unified_social_credit_code' | 'business_license' | 'organization_code' | 'other';
export type KycValidityType = 'long_term' | 'expire_date';

export interface UserKyc {
  id: number;
  user_id: string;
  kyc_type: UserKycType | string;
  status: UserKycStatus | string;
  real_name?: string | null;
  id_doc_type?: KycIdDocType | string | null;
  id_doc_number?: string | null;
  personal_email?: string | null;
  personal_phone?: string | null;
  id_doc_front_url?: string | null;
  id_doc_back_url?: string | null;
  company_name?: string | null;
  company_doc_type?: KycCompanyDocType | string | null;
  company_doc_number?: string | null;
  company_email?: string | null;
  company_phone?: string | null;
  business_license_url?: string | null;
  tax_registration_url?: string | null;
  legal_notarization_url?: string | null;
  validity_type: KycValidityType | string;
  expire_at?: string | null;
  reject_reason?: string | null;
  admin_remark?: string | null;
  is_default?: boolean;
  reviewed_by?: string | null;
  reviewed_at?: string | null;
  submitted_at?: string | null;
  created_at?: string;
  updated_at?: string;
}

export interface MarketingSettings {
  enable_registration_gift: boolean;
  /** 同一个 IP 注册不重复赠送 */
  gift_unique_ip?: boolean;
  /** 同一个 IP 注册不重复赠送限制小时数（默认 48 小时） */
  gift_unique_ip_hours?: number;
  /** 是否开启用户端兑换码功能 */
  enable_redemption?: boolean;
  // 以下字段仅管理后台完整接口返回，公开接口不包含
  gift_mode?: 'fixed' | 'random';
  fixed_amount?: number;
  min_amount?: number;
  max_amount?: number;
  /** 是否开启充值直升用户等级 */
  enable_recharge_level_upgrade?: boolean;
  /** 充值直升金额判定口径：single=单笔充值, cumulative=历史累计充值 */
  recharge_upgrade_basis?: 'single' | 'cumulative';
  /** 除默认等级外，额外允许被直升升级的原用户等级 group_key 列表 */
  recharge_upgrade_allowed_groups?: string[];
  /** 充值直升阶梯规则列表 */
  recharge_upgrade_rules?: Array<{
    rule_type?: 'single' | 'cumulative';
    min_amount: number;
    target_group_key: string;
  }>;
}

interface AgreementSettings {
  tos_mode: 'text' | 'link';
  tos_mode_en: 'text' | 'link';
  tos_content: string;
  tos_content_en: string;
  tos_link: string;
  tos_link_en: string;
  privacy_mode: 'text' | 'link';
  privacy_mode_en: 'text' | 'link';
  privacy_content: string;
  privacy_content_en: string;
  privacy_link: string;
  privacy_link_en: string;
  tos_enabled: boolean;
  privacy_enabled: boolean;
}

/**
 * 公开设置 — 对应后端 PublicSettings，仅包含 UI 渲染所需的安全数据。
 * 不含任何密钥、密码、Secret、数据库信息。
 * 管理后台设置页使用独立 request.get('/settings') 走 admin 路由获取完整数据。
 */
export interface AllSettings {
  is_open_source?: boolean;
  admin_path?: string;
  site: SiteSettings;
  currency: CurrencySettings;
  login: LoginSettings;
  registration: RegistrationSettings;
  marketing: MarketingSettings;
  /** 各支付渠道启用状态（仅布尔值，不含密钥） */
  payment?: {
    wechat_enabled: boolean;
    alipay_enabled: boolean;
    stripe_enabled: boolean;
    hyperbc_enabled: boolean;
    allinpay_enabled: boolean;
  };
  /** 支付渠道列表（排序/展示/启用，不含密钥） */
  payment_channels?: {
    id: string;
    sort_order: number;
    enabled: boolean;
    display_name?: string | null;
    display_name_en?: string | null;
    subtitle?: string | null;
    subtitle_en?: string | null;
    logo_url?: string | null;
    allinpay_methods?: string[];
    recharge_limit_enabled?: boolean;
    min_recharge_amount?: number;
    max_recharge_amount?: number;
    fee_percent?: number;
  }[];
  agreement?: AgreementSettings;
  /** 微信 OAuth app_id（前端扫码需要），公开接口仅返回此字段，不含 secret */
  wechat_oauth_app_id?: string;
  /** 微信 OAuth 授权回调主域名（如 https://old.com），用于多域名免审中继 */
  wechat_oauth_callback_domain?: string;
  /** Google OAuth client_id（前端 OAuth 跳转需要），公开接口仅返回此字段，不含 secret */
  google_oauth_client_id?: string;
  menu_config?: {
    items: {
      key: string;
      label_zh: string;
      label_en: string;
      icon: string;
      enabled: boolean;
      sort_order: number;
      allowed_levels: string;
    }[];
  };
  notification?: {
    site_notification_enabled: boolean;
    sms_balance_notification: boolean;
    email_balance_notification: boolean;
    web_notification_enabled?: boolean;
    push_notification_enabled?: boolean;
    do_not_disturb_enabled?: boolean;
    low_balance_threshold: number;
    /** 余额不足邮件主题，变量 {{site_name}} {{balance}} {{threshold}} */
    low_balance_email_subject?: string;
    /** 余额不足邮件 HTML，变量同上 */
    low_balance_email_html?: string;
  };
  database?: any;
  storage?: any;
  invoices?: {
    invoice_enabled?: boolean;
    invoice_mode?: string;
    invoice_company_name?: string;
    invoice_company_address?: string;
    invoice_tax_id?: string;
    invoice_contact_email?: string;
    invoice_currency_symbol?: string;
    invoice_prefix?: string;
    receipt_prefix?: string;
    invoice_notes?: string;
    invoice_show_invoices?: boolean;
    invoice_show_receipts?: boolean;
    china_min_amount?: number;
    china_invoice_content?: string;
    china_tax_rate?: number;
    china_invoice_notice?: string;
  };
  log_cleanup?: {
    log_retention_days?: number;
    log_row_retention_days?: number;
    error_log_retention_days?: number;
    daily_stats_hour?: number;
    daily_stats_minute?: number;
    clean_hour?: number;
    clean_minute?: number;
    archive_hour?: number;
    archive_minute?: number;
    error_clean_hour?: number;
    error_clean_minute?: number;
    storage_clean_hour?: number;
    storage_clean_minute?: number;
    maintenance_hour?: number;
    maintenance_minute?: number;
  };
  payment_wechat?: any;
  payment_alipay?: any;
  payment_stripe?: any;
  payment_hyperbc?: any;
  payment_anyway?: any;
  payment_allinpay?: any;
  payment_channels_ui?: {
    channels: {
      id: string;
      sort_order: number;
      enabled: boolean;
      display_name?: string | null;
      display_name_en?: string | null;
      subtitle?: string | null;
      subtitle_en?: string | null;
      logo_url?: string | null;
      allinpay_wechat_enabled?: boolean;
      allinpay_alipay_enabled?: boolean;
      recharge_limit_enabled?: boolean;
      min_recharge_amount?: number;
      max_recharge_amount?: number;
      fee_enabled?: boolean;
      fee_percent?: number;
    }[];
  };
  google_oauth?: any;
  wechat_oauth?: any;
  /** Relay 网关设置（仅管理端完整设置；公开接口不返回） */
  relay?: {
    /** 手动任务轮询是否请求上游；默认 true */
    manual_poll_upstream?: boolean;
    /** 后台 TaskPoller 周期（秒）；默认 30，范围 5–300 */
    poll_tick_secs?: number;
    /** 模型调用调试日志；默认关 */
    enable_debug_log?: boolean;
    /** SSE 流式响应上游空闲超时（秒）：0=关闭，范围 60–3600，默认 900 */
    stream_idle?: {
      chat_secs?: number;
      image_secs?: number;
    };
    /** 按类别的低余额在途限制（视频/图片/聊天/其它） */
    inflight_limits?: {
      video?: { enabled?: boolean; tiers?: { max_available?: number | null; max_inflight?: number }[] };
      image?: { enabled?: boolean; tiers?: { max_available?: number | null; max_inflight?: number }[] };
      chat?: { enabled?: boolean; tiers?: { max_available?: number | null; max_inflight?: number }[] };
      other?: { enabled?: boolean; tiers?: { max_available?: number | null; max_inflight?: number }[] };
    };
  };
}

export interface ChannelConfig {
  id: number;
  name: string;
  provider_type: string;
  base_url: string;
  api_key?: string;
  remark?: string;
  has_api_key?: boolean;
  sort_order?: number;
  rate?: number;
  priority?: number;
  weight?: number;
  yid?: string;
  quota_limit?: number;
  quota_used?: number;
  daily_quota_limit?: number;
  daily_quota_used?: number;
  weekly_quota_limit?: number;
  weekly_quota_used?: number;
  monthly_quota_limit?: number;
  monthly_quota_used?: number;
  last_reset_day?: string;
  last_reset_week?: string;
  last_reset_month?: string;
  /** 日额度刷新时（0-23），站点时区 */
  daily_reset_hour?: number;
  /** 日额度刷新分（0-59） */
  daily_reset_minute?: number;
  /** 到达刷新时刻后再冷却多少分钟才真正刷新（0=立即） */
  daily_reset_cooldown_minutes?: number;
  /** 1=启用, 0=禁用 */
  status?: number;
  /** 上游分类（channel_categories.id，向前兼容单分类） */
  category_id?: number | null;
  /** 上游多分类（channel_categories.id 列表，支持多选） */
  category_ids?: number[];
  /** 上游系统：兼容 / 官方 / newapi / Tkeapi / 火山引擎 / 阿里云 */
  upstream_system?: string;
  /** NewAPI 已选同步分组 */
  upstream_group?: string;
  /** 自动同步间隔分钟，0=关闭 */
  upstream_sync_interval_minutes?: number;
  /** 同步时叠加到分组倍率的增量 */
  upstream_sync_rate_add?: number;
  /** 上游计价货币单位，如 CNY/USD，空=同本站 */
  upstream_currency?: string;
  /** 本站计价货币单位，如 USD/CNY，空=同系统 */
  site_currency?: string;
  /** 货币换算汇率(例如 1 USD = 7.2 CNY 时填 7.2) */
  upstream_currency_rate?: number;
  /** NewAPI 上游用户数字 ID（作为 New-Api-User 请求头拉取私有/专属分组倍率） */
  upstream_user_id?: string;
  /** NewAPI 系统访问令牌（拉取 /api/user/self/groups 专属分组倍率） */
  upstream_access_token?: string;
  has_upstream_access_token?: boolean;
  upstream_synced_at?: string | null;
  /** 火山引擎 Access Key ID */
  volc_ak?: string;
  /** 火山引擎 Secret Access Key */
  volc_sk?: string;
  has_volc_credentials?: boolean;
  /** 上游账户可用余额（如火山引擎 QueryBalanceAcct 可用余额 CNY） */
  upstream_balance?: number | null;
  /** 上游账户余额最后查询时间 */
  upstream_balance_updated_at?: string | null;
  /** 绑定的模型渠道分组总数 */
  bound_channel_count?: number;
  /** 绑定的模型渠道分组摘要列表 */
  bound_channels?: BoundChannelSummary[];
  created_at: string;
  updated_at: string;
}

interface BoundChannelSummary {
  id: number;
  name: string;
  group_aid?: string | null;
  is_ha?: boolean;
  /** 1=启用, 0=禁用 */
  status?: number;
  effective_priority?: number;
  effective_weight?: number;
  custom_priority?: number | null;
  custom_weight?: number | null;
  custom_status?: number | null;
  custom_rpm_limit?: number | null;
  has_custom_schedule?: boolean;
}

export interface Upstream {
  id: number;
  name: string;
  upstream_type: string;
  sort_order: number;
  is_active: boolean;
  remark?: string;
  config?: string; // JSON string
  balance?: number; // Fetched async
  created_at: string;
  updated_at: string;
}


export interface Plugin {
  id: number;
  name: string;
  title: string;
  description: string;
  is_enabled: number;
  allowed_levels: string;
  category: string;   // user=用户增强插件, system=系统增强插件, system_builtin=系统内置
  show_in_admin_menu?: number;
  admin_menu_sort?: number;
  admin_menu_title?: string;
  admin_menu_default_tab?: string;
  created_at: string;
  updated_at: string;
}

export interface PluginAsset {
  id: number;
  user_id: string;
  asset_type: 'image' | 'video' | 'audio';
  source: 'builtin' | 'user';
  status: 'uploaded' | 'pending' | 'processing' | 'approved' | 'rejected';
  file_name: string;
  file_url: string;
  mime_type?: string;
  size?: number;
  reject_reason?: string;
  category?: string;
  group_id?: string;
  asset_id?: string;
  sort_order?: number;
  created_at: string;
  updated_at: string;
}

export interface MarketingTeam {
  id: number;
  name: string;
  description?: string;
  invite_code: string;
  max_members: number;
  members_can_set_level?: number;
  members_can_set_pay?: number;
  members_can_view_logs?: number;
  leader_can_remove_members?: number;
  members_can_apply_invoice?: number;
  allowed_level_ids?: number[];
  allowed_member_level_ids?: number[];
  leaders: TeamMember[];
  members: TeamMember[];
  created_at: string;
  updated_at: string;
}

export interface TeamMember {
  user_id: string;
  username: string;
  uid: string;
}

export interface ReferralUser {
  id: string;
  uid: string;
  username: string;
  email: string;
  user_group: string;
  level_name?: string;
  balance: number;
  credit_limit?: number;
  used_quota?: number;
  gift_balance?: number;
  gift_used_quota?: number;
  is_active: number;
  created_at: string;
  updated_at: string;
  total_recharge: number;
  current_month_system_recharge?: number;
  current_month_gift_recharge?: number;
  current_month_system_cost?: number;
  current_month_gift_cost?: number;
  remark?: string;
  pay_enabled: number;
}

export interface ReferralRecharge {
  id: number;
  user_id: string;
  amount: number;
  recharge_type: string;
  remark?: string;
  wallet_type?: string;
  order_no?: string;
  operator?: string;
  created_at: string;
}

export interface Announcement {
  id: number;
  title: string;
  content: string;
  is_pinned: number;
  is_popup?: number;
  is_active: number;
  sort_order?: number;
  pin_expires_at?: string | null;
  popup_expires_at?: string | null;
  active_expires_at?: string | null;
  /** created=新建时间, updated=最后修改时间 */
  display_time_mode?: 'created' | 'updated' | string;
  /** 逗号分隔，可多选：pay_modal,console,playground_2026,portal_pro */
  target_module?: string;
  /** 创作中心顶栏背景色，#RRGGBB */
  playground_banner_bg?: string | null;
  /** 门户顶栏背景色，#RRGGBB */
  portal_banner_bg?: string | null;
  created_at: string;
  updated_at: string;
}
