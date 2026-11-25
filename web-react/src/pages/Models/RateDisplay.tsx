import React from 'react';
import { Space, Tag, Typography, Tooltip } from 'antd';
import { useTranslation } from 'react-i18next';
import { resolveFreeImageCount } from '../../utils/billingFreeImages';
import { formatRate, formatWeekdayRange, isAllWeekdays, isFreeTimeMultiplier } from '../../utils/timeMultipliers';
import { sortResolutionKeys } from './discountSchedule';

const ZH_WEEKDAY_LABELS = ['一', '二', '三', '四', '五', '六', '日'];
const EN_WEEKDAY_LABELS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
const JA_WEEKDAY_LABELS = ['月', '火', '水', '木', '金', '土', '日'];
const KO_WEEKDAY_LABELS = ['월', '화', '수', '목', '금', '토', '일'];
const VI_WEEKDAY_LABELS = ['T2', 'T3', 'T4', 'T5', 'T6', 'T7', 'CN'];
const TH_WEEKDAY_LABELS = ['จ.', 'อ.', 'พ.', 'พฤ.', 'ศ.', 'ส.', 'อา.'];
const MS_WEEKDAY_LABELS = ['Isn', 'Sel', 'Rab', 'Kha', 'Jum', 'Sab', 'Ahd'];
const FR_WEEKDAY_LABELS = ['Lun', 'Mar', 'Mer', 'Jeu', 'Ven', 'Sam', 'Dim'];
const DE_WEEKDAY_LABELS = ['Mo', 'Di', 'Mi', 'Do', 'Fr', 'Sa', 'So'];
const ES_WEEKDAY_LABELS = ['Lun', 'Mar', 'Mié', 'Jue', 'Vie', 'Sáb', 'Dom'];
const PT_WEEKDAY_LABELS = ['Seg', 'Ter', 'Qua', 'Qui', 'Sex', 'Sáb', 'Dom'];
const RU_WEEKDAY_LABELS = ['Пн', 'Вт', 'Ср', 'Чт', 'Пт', 'Сб', 'Вс'];
const AR_WEEKDAY_LABELS = ['إثنين', 'ثلاثاء', 'أربعاء', 'خميس', 'جمعة', 'سبت', 'أحد'];

const getWeekdayLabels = (lang?: string) => {
  if (lang?.startsWith('zh')) return ZH_WEEKDAY_LABELS;
  if (lang?.startsWith('ja')) return JA_WEEKDAY_LABELS;
  if (lang?.startsWith('ko')) return KO_WEEKDAY_LABELS;
  if (lang?.startsWith('vi')) return VI_WEEKDAY_LABELS;
  if (lang?.startsWith('th')) return TH_WEEKDAY_LABELS;
  if (lang?.startsWith('ms')) return MS_WEEKDAY_LABELS;
  if (lang?.startsWith('fr')) return FR_WEEKDAY_LABELS;
  if (lang?.startsWith('de')) return DE_WEEKDAY_LABELS;
  if (lang?.startsWith('es')) return ES_WEEKDAY_LABELS;
  if (lang?.startsWith('pt')) return PT_WEEKDAY_LABELS;
  if (lang?.startsWith('ru')) return RU_WEEKDAY_LABELS;
  if (lang?.startsWith('ar')) return AR_WEEKDAY_LABELS;
  return EN_WEEKDAY_LABELS;
};

const { Text } = Typography;

// 子规则名称映射
const RULE_LABELS: Record<string, string> = {
  standard: '标准计费',
  kimi_chat: 'Kimi 上下文缓存',
  glm_5_3: 'glm 5.3',
  multimodal: '多模态计费',
  gpt_billing: 'GPT图片计费',
  tiered: '阶梯计费',
  doubao_chat: '豆包聊天阶梯',
  volcengine: '火山多模态(旧)',
  'seedance2.0': 'Seedance 2 规则',
  'seedance1.5pro': 'Seedance 1.5 规则',
  'seedance1.0': 'Seedance 1 规则',
  volc_seedream_pro: '火山 Seedream 5.0 Pro',
  fixed: '固定费率',
  per_image: '按张收费',
  image_resolution: '分辨率 K',
  image_resolution_io: '分辨率 K(双向)',
  image_size_pixel: '分辨率像素',
  video_resolution: '分辨率阶梯',
  minimax_h3: '海螺 (秒价+参考图)',
  video_seconds_io: '双向秒价 + 参考图',
  video_seconds_ref: '双向秒价 + 参考视频',
  fal_ref_video: 'fal H3-MAX',
  video_quality: '画质与帧率',
  kling_video: '可灵视频',
  kling4_video: '可灵 4.0 视频',
  vidu_video: 'Vidu 视频',
  vidu_image: 'Vidu 图片',
  characters: '字符计费',
  volc_enhance_cascade: '火山级联封装',
};

interface BillingRuleInfo {
  name: string;
  billing_type: string;
  billing_rule: string;
  prompt_rate: number;
  completion_rate: number;
  cached_rate?: number;
  claude_cache_creation_rate?: number;
  claude_cache_read_rate?: number;
  fixed_rate: number;
  duration_rate: number;
  extended_config: string | Record<string, any>;
  pricing_tiers?: string | any[];
}

interface RateDisplayProps {
  rule: BillingRuleInfo;
  currencySymbol: string;
  formatPrice?: (price: number | string | undefined | null) => string;
  siteDiscount?: number;
  siteDiscountEnabled?: number | boolean;
  /** 隐藏计费规则名称（如「标准计费」），仅展示费率明细 */
  hideRuleLabel?: boolean;
}

/**
 * 判断计费规则是否为 Claude 规则（支持新独立的 claude 规则与历史配置了 Claude 费率的老规则）
 */
export const isClaudeRule = (rule: any): boolean => {
  if (!rule) return false;
  if (rule.billing_rule === 'claude') return true;
  if (rule.billing_rule === 'standard') {
    let ext: any = {};
    if (typeof rule.extended_config === 'object' && rule.extended_config !== null) {
      ext = rule.extended_config;
    } else if (typeof rule.extended_config === 'string') {
      try { ext = JSON.parse(rule.extended_config); } catch {}
    }
    const cc = Number(rule.claude_cache_creation_rate) || Number(ext.claude_cache_creation_rate) || 0;
    const cr = Number(rule.claude_cache_read_rate) || Number(ext.claude_cache_read_rate) || 0;
    const cc1h = Number(ext.claude_cache_creation_1h_rate) || 0;
    return cc > 0 || cr > 0 || cc1h > 0;
  }
  return false;
};

/**
 * 统一费率展示组件：计费名 → 子规则标签 → 费率明细
 * 用于 Models 列表和 BillingRules 列表
 */
const RateDisplay: React.FC<RateDisplayProps> = ({ rule, currencySymbol, formatPrice, siteDiscount, siteDiscountEnabled, hideRuleLabel }) => {
  const { t, i18n } = useTranslation();
  const isZh = i18n.language?.startsWith('zh');
  const siteDiscountActive = !!(siteDiscountEnabled && siteDiscount !== undefined && siteDiscount !== null && siteDiscount >= 0 && siteDiscount < 1);
  const fpWithDiscount = (val: number | string | undefined | null, disc?: number) => {
    if (val === undefined || val === null || val === '') return '-';
    let origStr = formatPrice ? formatPrice(val) : `${currencySymbol}${val}`;
    const effDisc = disc !== undefined ? disc : siteDiscount;
    const isActive = !!(siteDiscountEnabled && effDisc !== undefined && effDisc !== null && effDisc >= 0 && effDisc < 1);
    if (isActive) {
      const num = Number(val);
      if (!isNaN(num)) {
         const dNum = num * effDisc!;
         const dStr = formatPrice ? formatPrice(Number.parseFloat(dNum.toFixed(6))) : `${currencySymbol}${Number.parseFloat(dNum.toFixed(6))}`;
         return <><Text delete type="secondary" style={{ fontSize: 'inherit', opacity: 0.65 }}>{origStr}</Text> <Text type="danger" style={{ fontSize: 'inherit', fontWeight: 500 }}>{dStr}</Text></>;
      }
    }
    return origStr;
  };
  const fp = (val: number | string | undefined | null) => fpWithDiscount(val);

  const getRuleLabel = (billingRule: string): string => {
    switch (billingRule) {
      case 'claude': return t('rate_display.claude', 'Claude 计费');
      case 'standard': {
        if (isClaudeRule(rule)) {
          return t('rate_display.claude', 'Claude 计费');
        }
        return t('rate_display.standard', '标准计费');
      }
      case 'kimi_chat': return t('rate_display.kimi_chat', 'Kimi 上下文缓存');
      case 'glm_5_3': return 'glm 5.3';
      case 'multimodal': return t('rate_display.multimodal', '多模态计费');
      case 'gpt_billing': return t('rate_display.gpt_billing', 'GPT图片计费');
      case 'tiered': return t('rate_display.tiered', '阶梯计费');
      case 'doubao_chat': return t('rate_display.doubao_chat', '豆包聊天阶梯');
      case 'volcengine': return t('rate_display.volcengine', '火山多模态(旧)');
      case 'seedance2.0': return 'Seedance 2 规则';
      case 'seedance1.5pro': return 'Seedance 1.5 规则';
      case 'seedance1.0': return 'Seedance 1 规则';
      case 'volc_seedream_pro': return '火山 Seedream 5.0 Pro';
      case 'fixed': return t('rate_display.fixed', '固定费率');
      case 'per_image': return t('rate_display.per_image_rule', '按张收费');
      case 'image_resolution': return t('rate_display.image_resolution', '分辨率 K');
      case 'image_resolution_io': return t('rate_display.image_resolution_io', '分辨率 K(双向)');
      case 'image_size_pixel': return t('rate_display.image_size_pixel', '分辨率像素');
      case 'video_resolution': return t('rate_display.video_resolution', '分辨率阶梯');
      case 'minimax_h3': return t('rate_display.minimax_h3', '海螺 (秒价+参考图)');
      case 'video_seconds_io': return t('rate_display.video_seconds_io', '双向秒价 + 参考图');
      case 'video_seconds_ref': return t('rate_display.video_seconds_ref', '双向秒价 + 参考视频');
      case 'fal_ref_video': return 'fal H3-MAX';
      case 'video_quality': return t('rate_display.video_quality', '画质与帧率');
      case 'kling_video': return t('rate_display.kling_video', '可灵视频');
      case 'kling4_video': return t('rate_display.kling4_video', '可灵 4.0 视频');
      case 'vidu_video': return t('rate_display.vidu_video', 'Vidu 视频');
      case 'vidu_image': return t('rate_display.vidu_image', 'Vidu 图片');
      case 'characters': return t('rate_display.characters', '字符计费');
      case 'volc_enhance_cascade': return t('rate_display.volc_enhance_cascade', '火山级联封装');
      default: return RULE_LABELS[billingRule] || billingRule;
    }
  };

  const ruleLabel = getRuleLabel(rule.billing_rule);
  const weekdayLabels = getWeekdayLabels(i18n.language);
  const s = { fontSize: '11px', lineHeight: 1.2 } as const;

  const discountLabel = isZh
    ? `${Number.parseFloat((siteDiscount! * 10).toFixed(2))}折`
    : `${Number(((1 - siteDiscount!) * 100).toFixed(1))}% OFF`;

  const discountTag = siteDiscountActive 
    ? <Tag color="error" variant="filled" style={{ margin: 0, padding: '0 4px', fontSize: 10, lineHeight: '14px', marginLeft: 6 }}>{discountLabel}</Tag> 
    : null;

  // 解析 extended_config
  let ext: Record<string, any> = {};
  try {
    if (typeof rule.extended_config === 'object' && rule.extended_config !== null) {
      ext = rule.extended_config;
    } else if (typeof rule.extended_config === 'string') {
      ext = JSON.parse(rule.extended_config);
    }
  } catch { }

  // 统一解析 pricing_tiers 数组
  const tiers = (() => {
    try {
      if (Array.isArray(rule.pricing_tiers)) {
        return rule.pricing_tiers;
      }
      if (typeof rule.pricing_tiers === 'string') {
        const parsed = JSON.parse(rule.pricing_tiers);
        if (Array.isArray(parsed)) return parsed;
      }
    } catch {}
    return [];
  })();

  // 查表类计费摘要（vidu_image / vidu_video 共用）
  const renderPriceTableSummary = (unit: string, extra?: React.ReactNode) => {
    const pt = ext.price_table || {};
    const disabledKeys: string[] = Array.isArray(ext.price_table_disabled) ? ext.price_table_disabled : [];
    const activeValues = Object.entries(pt).filter(([k]) => !disabledKeys.includes(k)).map(([, v]) => v as number).filter(v => typeof v === 'number');
    if (activeValues.length > 0) {
      const min = Math.min(...activeValues);
      const max = Math.max(...activeValues);
      const unitLabel = unit === '张' ? t('rate_display.per_image', '/ 张').replace(/^\/\s*/, '') : unit === 's' ? t('rate_display.per_second', '/秒').replace(/^\/\s*/, '') : unit;
      return (
        <Space direction="vertical" size={0}>
          <Text type="secondary" style={s}>
            {t('rate_display.table_summary', '查表')}: {fp(min)}~{fp(max)}/{unitLabel} ({t('rate_display.table_attr_res', '属性×分辨率')})
          </Text>
          {extra}
        </Space>
      );
    }
    return <Text type="secondary" style={s}>{t('rate_display.table_empty', '查表 (无有效条目)')}</Text>;
  };

  const renderDetails = () => {
    if (rule.billing_type === 'tokens') {
      if (rule.billing_rule === 'tiered') {
        if (tiers.length === 0) return <Text type="secondary" style={s}>{t('rate_display.tiered_empty', '阶梯定价 (无配置)')}</Text>;
        
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            {tiers.map((tierItem, idx) => (
              <Text key={idx} type="secondary" style={s}>
                {tierItem.max_prompt_tokens ? `≤${tierItem.max_prompt_tokens}k` : t('rate_display.unlimited', '不限')}{t('rate_display.in', '入')} | {tierItem.max_completion_tokens ? `≤${tierItem.max_completion_tokens}k` : t('rate_display.unlimited', '不限')}{t('rate_display.out', '出')} : {t('rate_display.input_label', '输入')} {fp(tierItem.prompt_rate)}/1M {t('rate_display.output_label', '输出')} {fp(tierItem.completion_rate)}/1M
                {tierItem.cached_rate ? <> {t('rate_display.read', '读缓')} {fp(tierItem.cached_rate)}/1M</> : null}
                {tierItem.cache_write_rate ? <> {t('rate_display.write', '写缓')} {fp(tierItem.cache_write_rate)}/1M</> : null}
              </Text>
            ))}
          </div>
        );
      }
      if (rule.billing_rule === 'doubao_chat') {
        if (tiers.length === 0) return <Text type="secondary" style={s}>{t('rate_display.doubao_empty', '豆包聊天阶梯 (无配置)')}</Text>;
        const hasFast = tiers.some((tierItem: any) => tierItem.fast_prompt_rate > 0 || tierItem.fast_completion_rate > 0);
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            {tiers.map((tierItem: any, idx: number) => {
              const range = tierItem.max_prompt_tokens ? `≤${tierItem.max_prompt_tokens}k` : t('rate_display.unlimited', '无限制');
              return (
                <Text key={idx} type="secondary" style={s}>
                  {range} | {t('rate_display.input_label', '输入')}:{fp(tierItem.prompt_rate)}/1M {t('rate_display.output_label', '输出')}:{fp(tierItem.completion_rate)}/1M
                  {tierItem.cached_rate > 0 ? <> {t('rate_display.cache_read', '读缓')}:{fp(tierItem.cached_rate)}/1M</> : null}
                  {tierItem.audio_prompt_rate > 0 ? <> {t('rate_display.audio_p', '音频输入')}:{fp(tierItem.audio_prompt_rate)}/1M</> : null}
                  {tierItem.audio_cached_rate > 0 ? <> {t('rate_display.audio_cache', '音频读缓')}:{fp(tierItem.audio_cached_rate)}/1M</> : null}
                  {hasFast ? (
                    <>
                      {' | '}{t('rate_display.fast_p', '极速输入')}:{fp(tierItem.fast_prompt_rate || tierItem.prompt_rate)}/1M {t('rate_display.fast_c', '极速输出')}:{fp(tierItem.fast_completion_rate || tierItem.completion_rate)}/1M
                      {tierItem.fast_cached_rate > 0 ? <> {t('rate_display.fast_cache', '极速读缓')}:{fp(tierItem.fast_cached_rate)}/1M</> : null}
                      {tierItem.fast_audio_prompt_rate > 0 ? <> {t('rate_display.fast_audio_p', '极速音频输入')}:{fp(tierItem.fast_audio_prompt_rate)}/1M</> : null}
                      {tierItem.fast_audio_cached_rate > 0 ? <> {t('rate_display.fast_audio_cache', '极速音频读缓')}:{fp(tierItem.fast_audio_cached_rate)}/1M</> : null}
                    </>
                  ) : null}
                </Text>
              );
            })}
          </div>
        );
      }
      if (rule.billing_rule === 'seedance2.0') {
        const rates = ext.resolution_rates || {};
        const activeRes = sortResolutionKeys(Object.keys(rates));
        const resDiscMap = (rule as any).resolution_discounts && typeof (rule as any).resolution_discounts === 'object'
          ? (rule as any).resolution_discounts as Record<string, number>
          : null;
        const baseDisc = (rule as any).base_global_discount !== undefined
          ? Number((rule as any).base_global_discount)
          : siteDiscount;

        if (activeRes.length === 0) {
          return <Text type="secondary" style={s}>{t('rate_display.seedance_fallback', '无独立分辨率设置(自动兜底计费)')}</Text>;
        }

        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            {activeRes.map(r => {
              const rDisc = resDiscMap
                ? (resDiscMap[r.toLowerCase()] !== undefined ? Number(resDiscMap[r.toLowerCase()]) : baseDisc)
                : siteDiscount;
              return (
                <Text key={r} type="secondary" style={s}>
                  {r}: {fpWithDiscount(rates[r].without_video, rDisc)}({t('rate_display.without_video', '无视')}) / {fpWithDiscount(rates[r].with_video, rDisc)}({t('rate_display.with_video', '含视')})
                </Text>
              );
            })}
          </div>
        );
      }
      if (rule.billing_rule === 'seedance1.5pro') {
        const discountStr = ext.offline_discount !== undefined ? `(${t('rate_display.offline_discount', '离线折扣')}: ${ext.offline_discount})` : '';
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            <Text type="secondary" style={s}>{t('rate_display.with_audio', '含语音')}: {fp(ext.audio_rate)}/1M {discountStr}</Text>
            <Text type="secondary" style={s}>{t('rate_display.without_audio', '无语音')}: {fp(ext.base_rate)}/1M {discountStr}</Text>
          </div>
        );
      }
      if (rule.billing_rule === 'seedance1.0') {
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            <Text type="secondary" style={s}>{t('rate_display.online', '在线')}: {fp(ext.online_rate)}/1M</Text>
            <Text type="secondary" style={s}>{t('rate_display.offline', '离线')}: {fp(ext.offline_rate)}/1M</Text>
          </div>
        );
      }
      if (rule.billing_rule === 'volcengine') {
        const lines: React.ReactNode[] = [];
        if (ext.volc_video_enabled) lines.push(<Text key="v" type="secondary" style={s}>{t('rate_display.with_video_full', '含视频')}: {fp(ext.volc_video_rate)}/1M</Text>);
        if (ext.volc_audio_enabled) lines.push(<Text key="a" type="secondary" style={s}>{t('rate_display.with_audio_full', '含音频')}: {fp(ext.volc_audio_rate)}/1M</Text>);
        if (ext.volc_base_enabled) lines.push(<Text key="b" type="secondary" style={s}>{t('rate_display.text_only', '纯文本')}: {fp(ext.volc_base_rate)}/1M</Text>);
        return <>{lines}</>;
      }
      if (rule.billing_rule === 'gpt_billing') {
        const gptConfig = (ext && typeof ext.gpt_config === 'object' && ext.gpt_config !== null) ? ext.gpt_config : {};
        const items = [
          { key: 'input_text', label: t('rate_display.gpt_input_text', '文输') },
          { key: 'cached_input_text', label: t('rate_display.gpt_cached_input_text', '文缓') },
          { key: 'input_image', label: t('rate_display.gpt_input_image', '图输') },
          { key: 'cached_input_image', label: t('rate_display.gpt_cached_input_image', '图缓') },
          { key: 'output_image', label: t('rate_display.gpt_output_image', '图出') },
        ];
        const lines: React.ReactNode[] = [];
        items.forEach(item => {
          const cfg = gptConfig[item.key];
          if (cfg && cfg.enabled) {
            lines.push(
              <Text key={item.key} type="secondary" style={s}>
                {item.label}: {fp(cfg.rate)}/1M
              </Text>
            );
          }
        });
        if (lines.length === 0) {
          return <Text type="secondary" style={s}>{t('rate_display.gpt_no_items', 'GPT图片计费(未启用计费项)')}</Text>;
        }
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            {lines}
          </div>
        );
      }
      if (rule.billing_rule === 'multimodal') {
        const imgRate = ext.image_prompt_rate || 0;
        const imgCompRate = ext.image_completion_rate || 0;
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            <Text type="secondary" style={s}>{t('rate_display.text_p', '文本输入')}: {fp(rule.prompt_rate)}/1M  {t('rate_display.image_p', '图片输入')}: {fp(imgRate)}/1M</Text>
            {(rule.completion_rate > 0 || imgCompRate > 0) && (
              <Text type="secondary" style={s}>{t('rate_display.text_c', '文本输出')}: {fp(rule.completion_rate)}/1M  {t('rate_display.image_c', '图片输出')}: {fp(imgCompRate)}/1M</Text>
            )}
          </div>
        );
      }
      // standard / kimi_chat / claude / glm 5.3 (统一标准与缓存 Tokens 渲染)
      const isClaude = isClaudeRule(rule);
      const isKimi = rule.billing_rule === 'kimi_chat';
      const readRate = isClaude
        ? (Number((rule as any).claude_cache_read_rate) || Number(ext.claude_cache_read_rate) || 0)
        : (Number(rule.cached_rate) || 0);
      const write5m = isKimi
        ? (ext.cache_write_rate ?? rule.prompt_rate)
        : (isClaude ? (Number((rule as any).claude_cache_creation_rate) || Number(ext.claude_cache_creation_rate) || 0) : 0);
      const write1h = isKimi
        ? (ext.cache_write_1h_rate ?? (rule.prompt_rate ? rule.prompt_rate * 2 : 0))
        : (isClaude ? (Number(ext.claude_cache_creation_1h_rate) || 0) : 0);

      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
          <Text type="secondary" style={s}>
            {t('rate_display.input_label', '输入')}: {fp(rule.prompt_rate)}/1M  {t('rate_display.output_label', '输出')}: {fp(rule.completion_rate)}/1M
            {readRate > 0 ? <> {t('rate_display.cache_read', '读缓')}: {fp(readRate)}/1M</> : null}
          </Text>
          {(write5m > 0 || write1h > 0) && (
            <Text type="secondary" style={s}>
              {write5m > 0 && (
                <>{write1h > 0 ? t('rate_display.cache_write_5m', '写缓(5m)') : t('rate_display.write', '写缓')}: {fp(write5m)}/1M</>
              )}
              {write5m > 0 && write1h > 0 && '  '}
              {write1h > 0 && (
                <>{t('rate_display.cache_write_1h', '写缓(1h)')}: {fp(write1h)}/1M</>
              )}
            </Text>
          )}
        </div>
      );
    }

    if (rule.billing_type === 'requests') {
      const imgRefStr = ext.image_ref_multiplier && ext.image_ref_multiplier !== 1 ? `${t('rate_display.img2img_mult', '图生图×')}${ext.image_ref_multiplier}` : '';
      const promptExtStr = ext.prompt_extend_multiplier && ext.prompt_extend_multiplier !== 1 ? `${t('rate_display.prompt_extend_mult', '扩写×')}${ext.prompt_extend_multiplier}` : '';
      // 合并倍率摘要（过滤空值，用空格分隔）
      const multiplierParts = [imgRefStr, promptExtStr].filter(Boolean);
      const multiplierStr = multiplierParts.length > 0 ? ` (${multiplierParts.join(' ')})` : '';
      if (rule.billing_rule === 'image_resolution') {
        const activeTiers = tiers.filter((item: any) => item.enabled !== false);
        if (activeTiers.length === 0) return <Text type="secondary" style={s}>{t('rate_display.resolution_k_empty', '按分辨率K (无有效配置)')}{multiplierStr}</Text>;
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            {activeTiers.map((tierItem: any, idx: number) => (
              <Text key={idx} type="secondary" style={s}>
                {tierItem.resolution}: {fp(tierItem.rate)} {t('rate_display.per_image', '/ 张')}
              </Text>
            ))}
            {multiplierStr && <Text type="secondary" style={s}>{multiplierStr}</Text>}
          </div>
        );
      }
      if (rule.billing_rule === 'image_resolution_io') {
        const activeTiers = tiers.filter((item: any) => item.enabled !== false);
        if (activeTiers.length === 0) return <Text type="secondary" style={s}>{t('rate_display.resolution_k_io_empty', '按分辨率K(输入/生成) (无有效配置)')}</Text>;
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            {activeTiers.map((tierItem: any, idx: number) => (
              <Text key={idx} type="secondary" style={s}>
                {tierItem.resolution}: {t('rate_display.input_label', '输入')} {fp(tierItem.input_rate)} {t('rate_display.per_image', '/ 张')} · {t('rate_display.gen_label', '生成')} {fp(tierItem.rate)} {t('rate_display.per_image', '/ 张')}
              </Text>
            ))}
          </div>
        );
      }
      if (rule.billing_rule === 'image_size_pixel') {
        const activeTiers = tiers.filter((item: any) => item.enabled !== false);
        if (activeTiers.length === 0) return <Text type="secondary" style={s}>{t('rate_display.resolution_pixel_empty', '按分辨率像素 (无有效配置)')}{multiplierStr}</Text>;
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            {activeTiers.map((tierItem: any, idx: number) => (
              <Text key={idx} type="secondary" style={s}>
                {tierItem.size}: {tierItem.quality_pricing
                  ? <>{t('rate_display.quality_low', '低')}{fp(tierItem.rate_low)} {t('rate_display.quality_med', '中')}{fp(tierItem.rate_high ? tierItem.rate_medium : tierItem.rate_medium)} {t('rate_display.quality_high', '高')}{fp(tierItem.rate_high)} {t('rate_display.per_image', '/ 张')}</>
                  : <>{fp(tierItem.rate)} {t('rate_display.per_image', '/ 张')}</>}
              </Text>
            ))}
            {multiplierStr && <Text type="secondary" style={s}>{multiplierStr}</Text>}
          </div>
        );
      }
      if (rule.billing_rule === 'volc_seedream_pro') {
        const activeTiers = tiers.filter((item: any) => item.enabled !== false);
        const freeCount = resolveFreeImageCount(ext.free_image_count, 'volc_seedream_pro');
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            <Text type="secondary" style={s}>
              {t('rate_display.free_within_prefix', { count: freeCount, defaultValue: `前 ${freeCount} 张免费，超出部分` })} {fp(rule.prompt_rate)}{t('rate_display.per_image', '/张')}
            </Text>
            {activeTiers.map((tierItem: any, idx: number) => (
              <Text key={idx} type="secondary" style={s}>
                {Number(tierItem.layer_rate) > 0 ? (
                  <>{t('rate_display.output_wan_pixels', { count: tierItem.max_pixels_wan, defaultValue: `输出 <= ${tierItem.max_pixels_wan}万像素` })}: {t('rate_display.single_image', '单图')} {fp(tierItem.rate)} {t('rate_display.per_image', '/ 张')} · {t('rate_display.layer', '图层')} {fp(tierItem.layer_rate)} {t('rate_display.per_image', '/ 张')}</>
                ) : (
                  <>{t('rate_display.output_wan_pixels', { count: tierItem.max_pixels_wan, defaultValue: `输出 <= ${tierItem.max_pixels_wan}万像素` })}: {fp(tierItem.rate)} {t('rate_display.per_image', '/ 张')}</>
                )}
              </Text>
            ))}
          </div>
        );
      }
      if (rule.billing_rule === 'per_image') return <Text type="secondary" style={s}>{fp(rule.fixed_rate)} {t('rate_display.per_image', '/ 张')}{multiplierStr}</Text>;
      if (rule.billing_rule === 'vidu_image') {
        return renderPriceTableSummary('张');
      }
      // 按字符计费（语音合成等），billing_rule = "characters"
      if (rule.billing_rule === 'characters') {
        return <Text type="secondary" style={s}>{fp(rule.fixed_rate)} {t('rate_display.per_10k_chars', '/ 万字符')}</Text>;
      }
      return <Text type="secondary" style={s}>{fp(rule.fixed_rate)} {t('rate_display.per_request', '/ 请求')}</Text>;
    }

    // duration
    if (rule.billing_rule === 'video_resolution') {
        const activeTiers = tiers.filter((item: any) => item.enabled !== false);
        if (activeTiers.length === 0) return <Text type="secondary" style={s}>{t('rate_display.video_duration_res_empty', '按视频时长及生成分辨率计费 (无有效配置)')}</Text>;
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            <Text type="secondary" style={s}>{t('rate_display.video_duration_res', '按视频时长及生成分辨率计费')}</Text>
            {activeTiers.map((tierItem: any, idx: number) => (
              <Text key={idx} type="secondary" style={s}>{tierItem.resolution} {fp(tierItem.rate)}{t('rate_display.per_second', '/秒')}</Text>
            ))}
          </div>
        );
    }
    if (rule.billing_rule === 'minimax_h3') {
      const activeTiers = tiers.filter((item: any) => item.enabled !== false);
      const freeCount = resolveFreeImageCount(ext.free_image_count, 'minimax_h3');
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
          <Text type="secondary" style={s}>
            {t('rate_display.free_within_prefix', { count: freeCount, defaultValue: `前 ${freeCount} 张免费，超出部分` })} {fp(rule.prompt_rate)}{t('rate_display.per_image', '/张')}
          </Text>
          <Text type="secondary" style={s}>{t('rate_display.video_duration_res', '按视频时长及生成分辨率计费')}</Text>
          {activeTiers.map((tierItem: any, idx: number) => (
            <Text key={idx} type="secondary" style={s}>{tierItem.resolution} {fp(tierItem.rate)}{t('rate_display.per_second', '/秒')}</Text>
          ))}
        </div>
      );
    }
    if (rule.billing_rule === 'video_seconds_io') {
      const activeTiers = tiers.filter((item: any) => item.enabled !== false);
      const freeCount = resolveFreeImageCount(ext.free_image_count, 'video_seconds_io');
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
          <Text type="secondary" style={s}>
            {t('rate_display.free_within_prefix', { count: freeCount, defaultValue: `前 ${freeCount} 张免费，超出部分` })} {fp(rule.prompt_rate)}{t('rate_display.per_image', '/张')}
          </Text>
          <Text type="secondary" style={s}>{t('rate_display.video_seconds_io_title', '按输入/生成视频时长及分辨率分别计费')}</Text>
          {activeTiers.map((tierItem: any, idx: number) => (
            <Text key={idx} type="secondary" style={s}>
              {tierItem.resolution} {t('rate_display.input_label', '输入')}:{fp(tierItem.input_rate ?? 0)}{t('rate_display.per_second', '/秒')} {t('rate_display.gen_label', '生成')}:{fp(tierItem.rate)}{t('rate_display.per_second', '/秒')}
            </Text>
          ))}
        </div>
      );
    }
    if (rule.billing_rule === 'video_seconds_ref') {
      const activeTiers = tiers.filter((item: any) => item.enabled !== false);
      const freeCount = resolveFreeImageCount(ext.free_image_count, 'video_seconds_ref');
      const isRefAware = ext.enable_video_ref !== false;
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
          <Text type="secondary" style={s}>
            {t('rate_display.free_within_prefix', { count: freeCount, defaultValue: `前 ${freeCount} 张免费，超出部分` })} {fp(rule.prompt_rate)}{t('rate_display.per_image', '/张')}
          </Text>
          <Text type="secondary" style={s}>
            {isRefAware ? t('rate_display.video_seconds_ref_title', '按有/无参考视频及分辨率分别计费') : t('rate_display.video_seconds_io_title', '按输入/生成视频时长及分辨率分别计费')}
          </Text>
          {activeTiers.map((tierItem: any, idx: number) => {
            if (isRefAware && (tierItem.rate_no_ref !== undefined || tierItem.rate_ref !== undefined)) {
              const noRefRate = tierItem.rate_no_ref ?? tierItem.rate;
              const refInRate = tierItem.input_rate_ref ?? tierItem.input_rate ?? 0;
              const refOutRate = tierItem.rate_ref ?? tierItem.rate;
              return (
                <Text key={idx} type="secondary" style={s}>
                  {tierItem.resolution} {t('rate_display.without_video', '无视')}:{fp(noRefRate)}{t('rate_display.per_second', '/秒')} | {t('rate_display.with_video', '含视')}({t('rate_display.input_label', '输入')}:{fp(refInRate)}{t('rate_display.per_second', '/秒')} {t('rate_display.gen_label', '生成')}:{fp(refOutRate)}{t('rate_display.per_second', '/秒')})
                </Text>
              );
            }
            return (
              <Text key={idx} type="secondary" style={s}>
                {tierItem.resolution} {t('rate_display.input_label', '输入')}:{fp(tierItem.input_rate ?? 0)}{t('rate_display.per_second', '/秒')} {t('rate_display.gen_label', '生成')}:{fp(tierItem.rate)}{t('rate_display.per_second', '/秒')}
              </Text>
            );
          })}
        </div>
      );
    }
    if (rule.billing_rule === 'fal_ref_video') {
      const freeTokens = ext.free_ref_tokens ?? 4096;
      const refRatePer1k = ext.ref_token_rate_per_1k ?? 0.02;
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
          <Text type="secondary" style={s}>{t('rate_display.video_output_gen', '成片生成')}：{fp(rule.duration_rate)}{t('rate_display.per_second', '/秒')}</Text>
          <Text type="secondary" style={s}>
            {t('rate_display.ref_assets', '参考素材')}：{t('rate_display.free_tokens', { tokens: freeTokens, defaultValue: `免 ${freeTokens} Token` })}，{t('rate_display.extra', '超出')} {fp(refRatePer1k)}{t('rate_display.per_1k_tokens', '/千Token')}
          </Text>
        </div>
      );
    }
    if (rule.billing_rule === 'video_quality') {
        const activeTiers = tiers.filter((item: any) => item.enabled !== false);
        if (activeTiers.length === 0) return <Text type="secondary" style={s}>{t('rate_display.video_quality_empty', '按视频画质及帧率阶梯 (无有效配置)')}</Text>;
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            {activeTiers.map((tierItem: any, idx: number) => (
              <Text key={idx} type="secondary" style={s}>
                {tierItem.resolution} | {tierItem.fps_range === '<=30' ? '≤30fps' : tierItem.fps_range === '>30' ? '>30fps' : tierItem.fps_range}: {fp(tierItem.rate)}{t('rate_display.per_second', '/秒')}
              </Text>
            ))}
          </div>
        );
    }
    if (rule.billing_rule === 'kling_video') {
      const pt = ext.price_table || {};
      const ptKeys = Object.keys(pt);
      if (ptKeys.length > 0) {
        // 精确查表模式：根据 enable 开关 + 逐条启用状态过滤
        const eMode = ext.enable_mode !== false;
        const eSound = ext.enable_sound !== false;
        const eVideo = ext.enable_video_ref === true;
        const disabledKeys: string[] = Array.isArray(ext.price_table_disabled) ? ext.price_table_disabled : [];
        const activeKeys = ptKeys.filter(key => {
          if (disabledKeys.includes(key)) return false;
          const parts = key.split('|');
          if (!eMode && parts[0] !== 'std') return false;
          if (!eSound && parts[1] !== 'off') return false;
          if (!eVideo && parts[2] !== 'no') return false;
          return true;
        });
        const activeValues = activeKeys.map(k => pt[k]).filter((v): v is number => typeof v === 'number');
        if (activeValues.length > 0) {
          const min = Math.min(...activeValues);
          const max = Math.max(...activeValues);
          const dims: string[] = [];
          if (eMode) dims.push(t('rate_display.dim_mode', '模式'));
          if (eSound) dims.push(t('rate_display.dim_sound', '音频'));
          if (eVideo) dims.push(t('rate_display.dim_video', '参考视频'));
          return (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
              <Text type="secondary" style={s}>{t('rate_display.exact_table', '精确查表')}: {fp(min)}~{fp(max)}{t('rate_display.per_second', '/秒')}</Text>
              {dims.length > 0 && <Text type="secondary" style={s}>{t('rate_display.dimensions', '维度')}: {dims.join('+')}</Text>}
            </div>
          );
        }
        return <Text type="secondary" style={s}>{t('rate_display.exact_table_empty', '精确查表 (无有效条目)')}</Text>;
      }
      const mm = ext.mode_multipliers || {};
      const sm = ext.sound_multipliers || {};
      const vm = ext.video_ref_multipliers || {};
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
          <Text type="secondary" style={s}>{t('rate_display.base_rate', '基准')}: {fp(rule.duration_rate)}{t('rate_display.per_second', '/秒')}</Text>
          <Text type="secondary" style={s}>mode: {t('rate_display.mode_std', '标准')}×{mm.std ?? 1} / {t('rate_display.mode_pro', '高品质')}×{mm.pro ?? 1.33} / 4k×{mm['4k'] ?? 2}</Text>
          <Text type="secondary" style={s}>sound: off×{sm.off ?? 1} / on×{sm.on ?? 1.5}</Text>
          <Text type="secondary" style={s}>{t('rate_display.dim_video', '参考视频')}: {t('rate_display.without_video', '无视')}×{vm.no ?? 1} / {t('rate_display.with_video', '含视')}×{vm.yes ?? 1.5}</Text>
        </div>
      );
    }
    if (rule.billing_rule === 'kling4_video') {
      const resRates = ext.resolution_rates || {};
      const entries = Object.entries(resRates);
      if (entries.length > 0) {
        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            {entries.map(([res, rates]: [string, any]) => (
              <Text key={res} type="secondary" style={s}>
                {res.toUpperCase()}: {fp(rates?.no_input ?? 0)}({t('rate_display.without_video', '无视')}) / {fp(rates?.with_input ?? 0)}({t('rate_display.with_video', '含视')}){t('rate_display.per_second', '/秒')}
              </Text>
            ))}
          </div>
        );
      }
      return <Text type="secondary" style={s}>{t('rate_display.kling4_default', '720P: 1.0(无视)/0.6(含视) /秒')}</Text>;
    }
    if (rule.billing_rule === 'vidu_video') {
      const discount = ext.offpeak_discount;
      const extraNode = discount !== undefined && discount !== 1
        ? <Text type="secondary" style={s}>{t('rate_display.offpeak', '错峰')}: ×{discount}</Text>
        : undefined;
      return renderPriceTableSummary('s', extraNode);
    }
    if (rule.billing_rule === 'volc_enhance_cascade') {
      return renderPriceTableSummary('s');
    }
    return <Text type="secondary" style={s}>{fp(rule.duration_rate)}{t('rate_display.per_second', '/秒')}</Text>;
  };

  const hasTimeMultipliers = ext?.enable_time_multipliers && Array.isArray(ext.time_multipliers) && ext.time_multipliers.length > 0;
  const invertRemainder = Number(ext?.invert_remainder_multiplier);
  const invertRemainderOk = ext?.invert_remainder_multiplier != null && Number.isFinite(invertRemainder) && invertRemainder >= 0;
  const titleLabel = hideRuleLabel ? t('rate_display.billing_title', '计费') : ruleLabel;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
        <span style={{ fontSize: '11px', lineHeight: 1.2, margin: 0, color: 'var(--text-secondary, #595959)' }}>{titleLabel}</span>
        {discountTag}
        {hasTimeMultipliers && (
          <Tooltip title={
            <div>
              <div style={{ fontWeight: 600, marginBottom: 4 }}>
                {t('rate_display.time_multiplier_title', { count: ext.time_multipliers.length, defaultValue: `已启用时段倍率 (共 ${ext.time_multipliers.length} 条，相对上方默认价)` })}
                {invertRemainderOk ? (
                  isFreeTimeMultiplier(invertRemainder)
                    ? t('rate_display.invert_remainder_free', { defaultValue: ' · 未规划 免费' })
                    : t('rate_display.invert_remainder', { ratio: formatRate(invertRemainder), defaultValue: ` · 未规划 ${formatRate(invertRemainder)} 倍` })
                ) : ''}
              </div>
              {ext.time_multipliers.map((tm: any, i: number) => {
                const daysText = isAllWeekdays(tm.days) ? t('rate_display.every_day', '每天') : formatWeekdayRange(tm.days, weekdayLabels);
                const ratio = Number(tm.multiplier);
                const allDay = !!tm.all_day;
                const timesUnit = t('rate_display.times', '倍');
                const ratioLabel = isFreeTimeMultiplier(ratio) ? t('rate_display.free', '免费') : `${formatRate(ratio)}${timesUnit}`;
                return (
                  <div key={i} style={{ fontSize: 11 }}>
                    {allDay ? t('rate_display.all_day', '全天') : `${tm.start} - ${tm.end}`} {daysText}: <b>{ratioLabel}</b>
                    {allDay ? t('rate_display.all_day_override', '（可被更细时段覆盖）') : ''}
                  </div>
                );
              })}
            </div>
          }>
            <Tag color="volcano" style={{ margin: 0, padding: '0 4px', fontSize: 10, lineHeight: '14px' }}>
              {t('rate_display.time_multiplier_tag', '时段倍率')}
            </Tag>
          </Tooltip>
        )}
      </div>
      {renderDetails()}
      {rule.billing_type === 'tokens' && rule.billing_rule !== 'glm_5_3' && ext.web_search_rate !== undefined && ext.web_search_rate > 0 && (
        <Text type="secondary" style={{ ...s, marginTop: 2 }}>{t('rate_display.web_search', '联网搜索')}: {fp(ext.web_search_rate)}{t('rate_display.per_1k_queries', '/千次')}</Text>
      )}
    </div>
  );
};

export default RateDisplay;
