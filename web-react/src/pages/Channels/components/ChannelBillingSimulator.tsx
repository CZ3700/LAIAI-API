import React, { useState, useEffect, useMemo } from 'react';
import {
  Drawer,
  Row,
  Col,
  Card,
  Form,
  Select,
  Input,
  InputNumber,
  Button,
  Space,
  Tag,
  Typography,
  Alert,
  Segmented,
  Tooltip,
  Divider,
  Spin,
  message,
  theme,
  Statistic,
  AutoComplete
} from 'antd';
import {
  CalculatorOutlined,
  ThunderboltOutlined,
  CheckCircleOutlined,
  WarningOutlined,
  CloseCircleOutlined,
  ReloadOutlined,
  CopyOutlined,
  CompassOutlined,
  SafetyCertificateOutlined,
  ArrowUpOutlined,
  ArrowDownOutlined,
  UserOutlined
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request from '../../../utils/request';
import { copyWithFeedback } from '../../../utils/clipboard';
import { formatRate } from '../../../utils/timeMultipliers';
import useSettingsStore from '../../../store/settings';
import type { Channel, UserLevel } from '../../../types';

const { Text, Title, Paragraph } = Typography;

export interface ChannelBillingSimulatorProps {
  open: boolean;
  onClose: () => void;
  channelId: number;
  channelName?: string;
  channelData?: Channel | null;
  initialModel?: string;
}

interface PipelineTraceStep {
  step: number;
  stage: string;
  title: string;
  formula: string;
  description: string;
  value?: number;
  status: 'normal' | 'pass' | 'warn' | 'danger';
}

interface SubChannelOption {
  id: number;
  name: string;
  rate: number;
  status: number;
  provider_type: string;
}

interface SimulateBillingResponse {
  success: boolean;
  channel_info: {
    channel_id: number;
    channel_name: string;
    channel_rate: number;
    provider_type: string;
    group_aid?: string;
    status: number;
    is_disabled: boolean;
    quota_warning?: string;
  };
  available_sub_channels: SubChannelOption[];
  routing: {
    requested_model: string;
    resolved_model: string;
    model_mapping_hit: boolean;
    mapping_source: string;
  };
  billing_rule: {
    rule_id?: number;
    rule_name: string;
    pricing_type: string;
    billing_type: string;
    is_default_fallback: boolean;
    rates: any;
  };
  pipeline_trace: PipelineTraceStep[];
  settlement: {
    pre_deducted: number;
    actual_cost: number;
    refund_amount: number;
    additional_deduct: number;
    billing_detail_text: string;
  };
  cost_and_margin: {
    upstream_name: string;
    upstream_rate: number;
    upstream_cost: number;
    gross_profit: number;
    margin_percent: number;
    is_loss_making: boolean;
    warning_message?: string;
  };
  warning?: string;
}

type ScenarioType = 'chat' | 'image' | 'video' | 'audio' | 'embedding';
type ModalityType = 'text' | 'image' | 'video' | 'audio' | 'embedding';

interface PresetFields {
  prompt_tokens: number;
  completion_tokens: number;
  cached_tokens: number;
  cache_write_tokens: number;
  web_search: number;
  audio_tokens: number;
  text_characters?: number;
  duration_seconds?: number;
  resolution?: string;
  image_count?: number;
}

const DEFAULT_PRESET_FIELDS: PresetFields = {
  prompt_tokens: 0,
  completion_tokens: 0,
  cached_tokens: 0,
  cache_write_tokens: 0,
  web_search: 0,
  audio_tokens: 0,
  text_characters: undefined,
  duration_seconds: undefined,
  resolution: undefined,
  image_count: undefined,
};

const SCENARIO_PRESETS: Record<ScenarioType, { modality: ModalityType; fields: Partial<PresetFields> }> = {
  chat: { modality: 'text', fields: { prompt_tokens: 1500, completion_tokens: 800, cached_tokens: 300 } },
  image: { modality: 'image', fields: { resolution: '1k', image_count: 1, prompt_tokens: 500, completion_tokens: 10000 } },
  video: { modality: 'video', fields: { duration_seconds: 5.0, resolution: '720p', prompt_tokens: 1000, completion_tokens: 45000 } },
  audio: { modality: 'audio', fields: { audio_tokens: 1000, text_characters: 500, duration_seconds: 15.0, prompt_tokens: 200 } },
  embedding: { modality: 'embedding', fields: { prompt_tokens: 800 } },
};

export const ChannelBillingSimulator: React.FC<ChannelBillingSimulatorProps> = ({
  open,
  onClose,
  channelId,
  channelName,
  channelData,
  initialModel,
}) => {
  const { t } = useTranslation();
  const { token } = theme.useToken();
  const { settings } = useSettingsStore();
  const currencyUnit = settings?.currency?.currency_unit || '¤';
  const currencySymbol = settings?.currency?.currency_symbol || '¥';

  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);
  const [userLevels, setUserLevels] = useState<UserLevel[]>([]);
  const [globalModels, setGlobalModels] = useState<any[]>([]);
  const [simResult, setSimResult] = useState<SimulateBillingResponse | null>(null);
  const [selectedSubChannels, setSelectedSubChannels] = useState<SubChannelOption[]>([]);
  const [modalityMode, setModalityMode] = useState<ModalityType>('text');
  const [selectedScenario, setSelectedScenario] = useState<ScenarioType>('chat');
  const watchedSubChannelId = Form.useWatch('sub_channel_id', form);

  // 解析渠道模型列表与映射关系
  const channelModels = useMemo<string[]>(() => {
    if (!channelData) return [];
    try {
      return typeof channelData.models === 'string'
        ? JSON.parse(channelData.models)
        : channelData.models || [];
    } catch {
      return [];
    }
  }, [channelData]);

  const channelModelMapping = useMemo<Record<string, string>>(() => {
    if (!channelData) return {};
    if (channelData.provider_type === 'high_availability_group') {
      try {
        const cfg = typeof channelData.config === 'string'
          ? JSON.parse(channelData.config || '{}')
          : (channelData.config || {});
        const activeSubId =
          watchedSubChannelId ??
          selectedSubChannels[0]?.id ??
          (Array.isArray(cfg?.sub_channels) ? cfg.sub_channels[0] : undefined);
        if (activeSubId === undefined || activeSubId === null) return {};
        const subKey = String(activeSubId);
        const haMap = cfg?.ha_model_mapping;
        if (!haMap || typeof haMap !== 'object') return {};
        const out: Record<string, string> = {};
        Object.entries(haMap as Record<string, Record<string, string>>).forEach(([k, subObj]) => {
          const alias = subObj?.[subKey];
          if (typeof alias === 'string' && alias.trim()) {
            out[k] = alias.trim();
          }
        });
        return out;
      } catch {
        return {};
      }
    }
    if (!channelData.model_mapping) return {};
    try {
      return typeof channelData.model_mapping === 'string'
        ? JSON.parse(channelData.model_mapping)
        : channelData.model_mapping;
    } catch {
      return {};
    }
  }, [channelData, watchedSubChannelId, selectedSubChannels]);

  // 加载系统用户等级列表与全局模型列表（用于将 mid 翻译为友好名称与 model_id）
  useEffect(() => {
    if (!open) return;
    const fetchMetadata = async () => {
      try {
        const [levelsResp, modelsResp] = await Promise.all([
          request.get('/user_levels') as unknown as Promise<{ data: UserLevel[] }>,
          request.get('/models') as unknown as Promise<{ data: any[] }>,
        ]);
        if (levelsResp?.data) setUserLevels(levelsResp.data);
        if (modelsResp?.data) setGlobalModels(modelsResp.data);
      } catch (err) {
        console.error('Failed to fetch user levels or models', err);
      }
    };
    void fetchMetadata();
  }, [open]);

  // 构建模型下拉选项：将单纯的 mid（如 300801）映射为真实 model_id、中文名及别名
  const modelOptions = useMemo(() => {
    if (!channelModels || channelModels.length === 0) return [];
    return channelModels.map((m) => {
      const strM = String(m);
      const matched = globalModels.find((gm) => String(gm.mid) === strM || String(gm.model_id) === strM);
      const actualModelId = matched ? matched.model_id : strM;
      const modelName = matched?.name && matched.name !== actualModelId ? matched.name : '';
      const midTag = matched?.mid ? matched.mid : (strM !== actualModelId ? strM : '');
      const mappedTarget = channelModelMapping[actualModelId] || channelModelMapping[strM];

      return {
        label: (
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, overflow: 'hidden', textOverflow: 'ellipsis' }}>
              <span style={{ fontWeight: 600 }}>{actualModelId}</span>
              {modelName && (
                <span style={{ fontSize: 12, color: token.colorTextSecondary }}>
                  ({modelName})
                </span>
              )}
              {midTag && (
                <Tag style={{ fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '16px' }} color="default">
                  {midTag}
                </Tag>
              )}
            </div>
            {mappedTarget && (
              <Tag style={{ fontSize: 10, margin: 0, padding: '0 4px', lineHeight: '16px' }} color="blue">
                ➞ {mappedTarget}
              </Tag>
            )}
          </div>
        ),
        value: actualModelId,
      };
    });
  }, [channelModels, globalModels, channelModelMapping, token]);

  // 获取当前默认首选模型
  const getDefaultModel = (): string => {
    if (initialModel) return initialModel;
    if (channelModels.length > 0) {
      const firstM = String(channelModels[0]);
      const matched = globalModels.find((gm) => String(gm.mid) === firstM || String(gm.model_id) === firstM);
      return matched ? matched.model_id : firstM;
    }
    return 'gpt-4o';
  };

  // 初始化与重置表单参数
  const applyScenario = (type: ScenarioType) => {
    const preset = SCENARIO_PRESETS[type];
    if (!preset) return;
    setSelectedScenario(type);
    setModalityMode(preset.modality);
    form.setFieldsValue({
      model: form.getFieldValue('model') || getDefaultModel(),
      ...DEFAULT_PRESET_FIELDS,
      ...preset.fields,
    });
  };

  const handleScenarioSelect = (type: ScenarioType) => {
    applyScenario(type);
    setTimeout(() => void executeSimulation(), 50);
  };

  // 抽屉开启时初始化
  useEffect(() => {
    if (open && channelId) {
      const defaultModel = getDefaultModel();
      setSelectedSubChannels([]);
      form.setFieldsValue({
        model: defaultModel,
        sub_channel_id: undefined,
        user_level_id: undefined,
        user_id: '',
        custom_upstream_rate: undefined,
        mock_user_balance: 10.0,
        simulated_time: undefined,
      });
      applyScenario('chat');

      // 延迟微秒发起首次默认试算
      const timer = setTimeout(() => {
        void executeSimulation();
      }, 60);
      return () => clearTimeout(timer);
    }
  }, [open, channelId, channelModels, globalModels, initialModel]);

  // 执行沙盒模拟请求
  const executeSimulation = async () => {
    try {
      const values = form.getFieldsValue(true);
      const curModel = values.model || getDefaultModel();

      setLoading(true);

      const payload: any = {
        model: String(curModel),
        sub_channel_id: values.sub_channel_id || null,
        user_level_id: values.user_level_id || null,
        user_id: values.user_id?.trim() || null,
        custom_discount: null,
        custom_upstream_rate: values.custom_upstream_rate ?? null,
        simulated_time: values.simulated_time || null,
        mock_user_balance: values.mock_user_balance ?? 10.0,
        usage: {
          prompt_tokens: Number(values.prompt_tokens || 0),
          completion_tokens: Number(values.completion_tokens || 0),
          cached_tokens: Number(values.cached_tokens || 0),
          cache_write_tokens: Number(values.cache_write_tokens || 0),
          audio_tokens: Number(values.audio_tokens || 0),
          audio_cached_tokens: 0,
        },
        features: {
          web_search: values.web_search ? Number(values.web_search) : 0,
          resolution: values.resolution || (modalityMode === 'image' ? '1k' : modalityMode === 'video' ? '720p' : null),
          duration_seconds: values.duration_seconds !== undefined && values.duration_seconds !== null && values.duration_seconds !== ''
            ? Number(values.duration_seconds)
            : (modalityMode === 'video' ? 5.0 : modalityMode === 'audio' ? 15.0 : null),
          image_count: values.image_count !== undefined && values.image_count !== null && values.image_count !== ''
            ? Math.max(1, Number(values.image_count))
            : 1,
          size: values.resolution || (modalityMode === 'image' ? '1k' : null),
          text_characters: values.text_characters ? Number(values.text_characters) : null,
        },
      };

      const resp = await (request.post(`/channels/${channelId}/simulate-billing`, payload) as unknown as Promise<SimulateBillingResponse>);
      if (resp) {
        setSimResult(resp);
        setSelectedSubChannels(resp.available_sub_channels || []);
      }
    } catch (err: any) {
      if (err?.errorFields) return; // 校验失败
      message.error(err?.message || '计费沙盒试算异常');
    } finally {
      setLoading(false);
    }
  };

  const getTraceItemDisplay = (trace: PipelineTraceStep) => {
    const title = trace.title.includes('按秒') ? trace.title : t(`channels.simulator.stage_${trace.stage}_title`, trace.title);
    let formula = trace.formula;
    let description = trace.description;

    if (simResult) {
      switch (trace.stage) {
        case 'routing': {
          formula = `${simResult.routing.requested_model} ➞ ${simResult.routing.resolved_model}`;
          const mappingSource = simResult.routing.mapping_source.includes('精确')
            ? t('channels.simulator.mapping_source_exact', '渠道模型映射(精确匹配)')
            : simResult.routing.mapping_source.includes('正则')
            ? t('channels.simulator.mapping_source_regex', '渠道模型映射(正则规则匹配)')
            : simResult.routing.mapping_source;
          description = simResult.routing.model_mapping_hit
            ? t('channels.simulator.desc_routing_hit', {
                source: mappingSource,
                model: simResult.routing.resolved_model,
                defaultValue: trace.description,
              })
            : t('channels.simulator.desc_routing_direct', '未配置模型重命名映射，直接按请求模型名直通转发上游');
          break;
        }
        case 'pre_deduct':
          formula = trace.formula || `${t('channels.simulator.pre_deducted', '预扣冻结')}: ${simResult.settlement.pre_deducted.toFixed(6)} ${currencyUnit}`;
          description = trace.description;
          break;
        case 'base_cost': {
          formula = `${t('channels.simulator.formula_base_cost', '基础算力原价')} = ${(trace.value ?? 0).toFixed(6)} ${currencyUnit}`;
          const ruleDisplayName = simResult.billing_rule?.is_default_fallback
            ? t('channels.simulator.default_rule_name', '系统默认兜底规则')
            : (simResult.billing_rule?.rule_name || t('channels.simulator.default_rule_name', '系统默认兜底规则'));
          description = simResult.billing_rule && !simResult.billing_rule.is_default_fallback
            ? t('channels.simulator.desc_base_cost_rule', {
                rule: ruleDisplayName,
                defaultValue: trace.description,
              })
            : t('channels.simulator.desc_base_cost_default', '未关联计费规则，触发系统统一默认兜底公式');
          break;
        }
        case 'discount':
          formula = trace.formula || `${t('channels.simulator.formula_discount', '有效折扣率')} = ${formatRate(trace.value ?? 1)}x`;
          description = trace.description || t('channels.simulator.desc_discount', '在用户等级已勾选的折扣项中匹配最低值（最大优惠）');
          break;
        case 'channel_rate_and_floor':
          formula = trace.status === 'warn'
            ? t('channels.simulator.formula_floor_clamped', {
                floor: formatRate(trace.value ?? 1),
                defaultValue: `触碰保底限价 ➞ 强制保底 ${formatRate(trace.value ?? 1)}x`,
              })
            : t('channels.simulator.formula_rate_normal', {
                rate: formatRate(trace.value ?? 1),
                defaultValue: `有效倍率 = ${formatRate(trace.value ?? 1)}x`,
              });
          description = trace.status === 'warn'
            ? t('channels.simulator.desc_floor_clamped', '⚠️ 触及模型设置的保底限价，系统自动触发安全拦截兜底防倒贴')
            : t('channels.simulator.desc_rate_normal', '匹配到的折扣倍率不低于模型保底限价，按该最低折扣倍率结算');
          break;
        case 'time_multiplier':
          formula = `${t('channels.simulator.formula_time', '时段倍率')} = ${formatRate(trace.value ?? 1)}x`;
          description = Math.abs((trace.value ?? 1) - 1.0) > 0.001
            ? t('channels.simulator.desc_time_active', {
                mult: formatRate(trace.value ?? 1),
                defaultValue: trace.description,
              })
            : t('channels.simulator.desc_time_default', '当前时段处于基准常价格区间或未启用时段倍率，无额外时段乘数');
          break;
        case 'settlement': {
          const delta = simResult.settlement.refund_amount > 0 ? simResult.settlement.refund_amount : simResult.settlement.additional_deduct;
          const sign = simResult.settlement.refund_amount > 0 ? '+' : delta > 0 ? '-' : '';
          formula = `${t('channels.simulator.formula_actual', '实扣金额')} = ${simResult.settlement.actual_cost.toFixed(6)} ${currencyUnit} (${t('channels.simulator.formula_delta', '预扣差额')}: ${sign}${delta.toFixed(6)} ${currencyUnit})`;
          description = simResult.settlement.refund_amount > 0
            ? t('channels.simulator.desc_settlement_refund', {
                refund: simResult.settlement.refund_amount.toFixed(6),
                unit: currencyUnit,
                defaultValue: trace.description,
              })
            : simResult.settlement.additional_deduct > 0
            ? t('channels.simulator.desc_settlement_deduct', {
                deduct: simResult.settlement.additional_deduct.toFixed(6),
                unit: currencyUnit,
                defaultValue: trace.description,
              })
            : t('channels.simulator.desc_settlement_even', '预扣金额与实际消费完全一致，无需额外退补');
          break;
        }
        case 'cost_and_margin': {
          const pSign = simResult.cost_and_margin.gross_profit >= 0 ? '+' : '';
          formula = `${t('channels.simulator.formula_revenue', '平台实收')} ${simResult.settlement.actual_cost.toFixed(6)} - ${t('channels.simulator.formula_cost', '上游成本')} ${simResult.cost_and_margin.upstream_cost.toFixed(6)} = ${t('channels.simulator.formula_profit', '毛利')} ${pSign}${simResult.cost_and_margin.gross_profit.toFixed(6)} ${currencyUnit} (${simResult.cost_and_margin.margin_percent.toFixed(2)}%)`;
          description = simResult.cost_and_margin.is_loss_making
            ? t('channels.simulator.desc_margin_loss', '⚠️ 出现负毛利倒贴！采购成本超出终端收取，建议调整渠道倍率')
            : t('channels.simulator.desc_margin_safe', '平台盈利健康，当前单次调用毛利与利润率处于合理安全空间');
          break;
        }
      }
    }
    return { title, formula, description };
  };

  const formatUpstreamName = (rawName: string) => {
    if (!rawName) return '';
    const isManualRate = rawName.includes('(手动指定倍率)');
    const cleanName = isManualRate ? rawName.replace(/\s*\(手动指定倍率\)/, '').trim() : rawName;

    const prefixes: Array<[string, string, string]> = [
      ['HA子渠道(默认首项): ', 'channels.simulator.ha_sub_default_prefix', 'HA子渠道(默认首项): '],
      ['HA子渠道: ', 'channels.simulator.ha_sub_prefix', 'HA子渠道: '],
      ['上游预设: ', 'channels.simulator.upstream_preset_prefix', '上游预设: '],
    ];

    let prefix = '';
    let target = cleanName;
    for (const [rawPrefix, key, def] of prefixes) {
      if (cleanName.startsWith(rawPrefix)) {
        prefix = t(key, def);
        target = cleanName.slice(rawPrefix.length);
        break;
      }
    }

    if (cleanName === '自定义上游') {
      target = t('channels.simulator.custom_upstream', '自定义上游');
    }

    const manualText = isManualRate ? ` (${t('channels.simulator.manual_rate_suffix', '手动指定倍率')})` : '';
    return `${prefix}${target}${manualText}`;
  };

  const copyBillingDetail = () => {
    if (!simResult?.settlement?.billing_detail_text) return;
    void copyWithFeedback(simResult.settlement.billing_detail_text, t('channels.simulator.copy_success', '已复制计费明细到剪贴板'));
  };

  return (
    <Drawer
      title={
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', width: '100%', paddingRight: 16 }}>
          <Space size="middle">
            <CalculatorOutlined style={{ fontSize: 18, color: token.colorPrimary }} />
            <div>
              <span style={{ fontWeight: 600, fontSize: 16 }}>
                {t('channels.simulator.title', '渠道计费模拟沙盒')}
              </span>
              <Tag color="blue" style={{ marginLeft: 8 }}>
                {channelName || `渠道 #${channelId}`}
              </Tag>
              {channelData?.rate && (
                <Tag color="purple">{t('channels.simulator.channel_rate_tag', '渠道倍率')}: {channelData.rate}x</Tag>
              )}
            </div>
          </Space>
          <Tag color="green" icon={<SafetyCertificateOutlined />}>
            {t('channels.simulator.safe_tag', '纯内存沙盒 · 零扣费')}
          </Tag>
        </div>
      }
      placement="right"
      width="min(1240px, 96vw)"
      open={open}
      onClose={onClose}
      styles={{
        body: {
          padding: 16,
          background: token.colorBgLayout,
          overflowY: 'auto',
        },
      }}
    >
      {/* 顶部典型场景快捷预设栏 */}
      <Card
        size="small"
        style={{ marginBottom: 16, borderRadius: 8, borderColor: token.colorBorder }}
        styles={{ body: { padding: '10px 14px' } }}
      >
        <Row justify="space-between" align="middle" gutter={[12, 12]}>
          <Col xs={24} lg={17} xl={18}>
            <Space size="small" wrap align="center">
              <Text type="secondary" style={{ fontSize: 13, marginRight: 4, whiteSpace: 'nowrap' }}>
                <ThunderboltOutlined style={{ color: token.colorWarning }} /> {t('channels.simulator.presets', '场景预设')}:
              </Text>
              <Segmented<ScenarioType>
                value={selectedScenario}
                onChange={handleScenarioSelect}
                options={[
                  { label: t('channels.simulator.chat', '对话'), value: 'chat' },
                  { label: t('channels.simulator.image', '图片'), value: 'image' },
                  { label: t('channels.simulator.video', '视频'), value: 'video' },
                  { label: t('channels.simulator.audio', '音频'), value: 'audio' },
                  { label: t('channels.simulator.embedding', '向量/重排'), value: 'embedding' },
                ]}
              />
            </Space>
          </Col>
          <Col xs={24} lg={7} xl={6} style={{ textAlign: 'right' }}>
            <Space size="small">
              <Button
                size="small"
                icon={<ReloadOutlined />}
                onClick={() => handleScenarioSelect('chat')}
              >
                {t('channels.simulator.reset', '重置')}
              </Button>
              <Button
                type="primary"
                size="small"
                icon={<CalculatorOutlined />}
                loading={loading}
                onClick={executeSimulation}
              >
                {t('channels.simulator.run', '立即试算')}
              </Button>
            </Space>
          </Col>
        </Row>
      </Card>

      {/* 主工作区双栏布局 */}
      <Row gutter={16}>
        {/* 左栏：仿真输入控制台 (提升至 10 列以保证充足横向宽度)，全栏共用一个根 Form */}
        <Col xs={24} lg={10} xl={10}>
          <Form form={form} layout="vertical">
            <Card
              title={<span style={{ fontSize: 14, fontWeight: 600 }}>{t('channels.simulator.model_settings', '1. 模型与上游设置')}</span>}
              size="small"
              style={{ marginBottom: 16, borderRadius: 8, borderColor: token.colorBorder }}
              styles={{ body: { padding: 14 } }}
            >
              <Form.Item
                label={t('channels.simulator.target_model', '请求模型')}
                name="model"
                rules={[{ required: true, message: t('channels.simulator.target_model_required', '请选择或输入模型') }]}
                tooltip={t('channels.simulator.target_model_tip', '选择已绑模型或手动输入')}
                style={{ marginBottom: 12 }}
              >
                <Select
                  showSearch
                  allowClear={false}
                  placeholder={t('channels.simulator.target_model_placeholder', '选择或输入模型')}
                  onChange={executeSimulation}
                  options={modelOptions}
                />
              </Form.Item>

              {/* 高可用子渠道下拉切换 */}
              {selectedSubChannels.length > 0 && (
                <Form.Item
                  label={t('channels.simulator.ha_subchannel', '高可用子上游')}
                  name="sub_channel_id"
                  tooltip={t('channels.simulator.ha_subchannel_tip', '指定具体子渠道测算采购成本')}
                  style={{ marginBottom: 12 }}
                >
                  <Select
                    allowClear
                    placeholder={t('channels.simulator.ha_subchannel_placeholder', '默认首项子渠道')}
                    onChange={executeSimulation}
                    options={selectedSubChannels.map((sub) => ({
                      label: `${sub.name} (上游倍率: ${sub.rate}x)`,
                      value: sub.id,
                    }))}
                  />
                </Form.Item>
              )}

              {/* 自定义上游采购费率 */}
              <Form.Item
                label={t('channels.simulator.upstream_rate', '上游费率覆盖')}
                name="custom_upstream_rate"
                tooltip={t('channels.simulator.upstream_rate_tip', '未绑预设默认 1.0x，填写可强制覆盖')}
                style={{ marginBottom: 4 }}
              >
                <InputNumber
                  min={0}
                  step={0.1}
                  precision={4}
                  placeholder={t('channels.simulator.upstream_rate_placeholder', '未绑默认 1.0x (留空自动读取)')}
                  style={{ width: '100%' }}
                  onChange={executeSimulation}
                />
              </Form.Item>
            </Card>

            <Card
              title={<span style={{ fontSize: 14, fontWeight: 600 }}>{t('channels.simulator.caller_identity', '2. 身份与环境上下文')}</span>}
              size="small"
              style={{ marginBottom: 16, borderRadius: 8, borderColor: token.colorBorder }}
              styles={{ body: { padding: 14 } }}
            >
              <Form.Item
                label={t('channels.simulator.user_level', '模拟用户等级')}
                name="user_level_id"
                tooltip={t('channels.simulator.user_level_tip', '按等级折扣演算（留空按原价 1.0x）')}
                style={{ marginBottom: 12 }}
              >
                <Select
                  allowClear
                  placeholder={t('channels.simulator.user_level_placeholder', '选择用户等级 (默认 1.0x)')}
                  onChange={executeSimulation}
                  options={userLevels.map((lvl) => {
                    const v = lvl.discount_type ?? 0;
                    const mask = v >= 16 ? (v & 15) : (v === 1 ? 6 : v === 2 ? 3 : 15);
                    const hasLevelDisc = (mask & 1) !== 0;
                    return {
                      label: `${lvl.name} (${hasLevelDisc ? `折扣: ${lvl.discount}x` : '等级折扣未启用'})`,
                      value: lvl.id,
                    };
                  })}
                />
              </Form.Item>

              <Form.Item
                label={t('channels.simulator.real_user', '真实用户 UID / 用户名')}
                name="user_id"
                tooltip={t('channels.simulator.real_user_tip', '输入 UID 自动匹配分组与专属模型折扣')}
                style={{ marginBottom: 12 }}
              >
                <Input
                  allowClear
                  prefix={<UserOutlined style={{ color: token.colorTextSecondary }} />}
                  placeholder={t('channels.simulator.real_user_placeholder', '选填真实 UID 或用户名')}
                  onBlur={executeSimulation}
                  onPressEnter={executeSimulation}
                  onChange={(e) => {
                    if (!e.target.value) {
                      void executeSimulation();
                    }
                  }}
                />
              </Form.Item>

              <Row gutter={8}>
                <Col span={12}>
                  <Form.Item
                    label={t('channels.simulator.mock_balance', '模拟钱包余额')}
                    name="mock_user_balance"
                    tooltip={t('channels.simulator.mock_balance_tip', '检验模型预扣金额是否充足')}
                    style={{ marginBottom: 4 }}
                  >
                    <InputNumber
                      min={0}
                      step={1}
                      precision={2}
                      prefix={currencySymbol}
                      style={{ width: '100%' }}
                      onChange={executeSimulation}
                    />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item
                    label={t('channels.simulator.simulated_time', '模拟调用时刻')}
                    name="simulated_time"
                    tooltip={t('channels.simulator.simulated_time_tip', '如 02:30，测试夜间谷时或高峰时段费率')}
                    style={{ marginBottom: 4 }}
                  >
                    <Select
                      allowClear
                      placeholder={t('channels.simulator.simulated_time_placeholder', '实时系统时间')}
                      onChange={(val) => {
                        form.setFieldsValue({ simulated_time: val });
                        void executeSimulation();
                      }}
                      options={[
                        { label: '当前系统时间 (实时)', value: '' },
                        { label: '凌晨谷时特惠 (02:30:00)', value: '02:30:00' },
                        { label: '早间平谷时段 (07:30:00)', value: '07:30:00' },
                        { label: '下午常规时段 (14:30:00)', value: '14:30:00' },
                        { label: '晚间高峰时段 (21:30:00)', value: '21:30:00' },
                      ]}
                    />
                  </Form.Item>
                </Col>
              </Row>
            </Card>

            <Card
              title={<span style={{ fontSize: 14, fontWeight: 600 }}>{t('channels.simulator.modality_features', '3. 算力用量与多模态')}</span>}
              size="small"
              style={{ borderRadius: 8, borderColor: token.colorBorder }}
              styles={{ body: { padding: 14 } }}
            >
              <Segmented<ModalityType>
                block
                size="small"
                value={modalityMode}
                onChange={(val) => {
                  setModalityMode(val);
                  const scenarioMap: Record<ModalityType, ScenarioType> = {
                    text: 'chat',
                    image: 'image',
                    video: 'video',
                    audio: 'audio',
                    embedding: 'embedding',
                  };
                  applyScenario(scenarioMap[val]);
                  setTimeout(() => void executeSimulation(), 50);
                }}
                options={[
                  { label: t('channels.simulator.tab_text', '文本'), value: 'text' },
                  { label: t('channels.simulator.tab_image', '图像'), value: 'image' },
                  { label: t('channels.simulator.tab_video', '视频'), value: 'video' },
                  { label: t('channels.simulator.tab_audio', '音频'), value: 'audio' },
                  { label: t('channels.simulator.tab_embedding', '向量/重排'), value: 'embedding' },
                ]}
                style={{ marginBottom: 14 }}
              />
              {modalityMode === 'text' && (
                <>
                  <Row gutter={8}>
                    <Col span={12}>
                      <Form.Item label={t('channels.simulator.prompt_tokens', '提示词 (Prompt)')} name="prompt_tokens" style={{ marginBottom: 10 }}>
                        <InputNumber min={0} step={100} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                    <Col span={12}>
                      <Form.Item label={t('channels.simulator.completion_tokens', '补全 (Completion)')} name="completion_tokens" style={{ marginBottom: 10 }}>
                        <InputNumber min={0} step={100} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                  </Row>
                  <Row gutter={8}>
                    <Col span={12}>
                      <Form.Item label={t('channels.simulator.cached_tokens', '缓存命中 (Cache Read)')} name="cached_tokens" style={{ marginBottom: 10 }}>
                        <InputNumber min={0} step={100} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                    <Col span={12}>
                      <Form.Item label={t('channels.simulator.cache_write_tokens', '缓存写入 (Cache Write)')} name="cache_write_tokens" style={{ marginBottom: 10 }}>
                        <InputNumber min={0} step={100} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                  </Row>
                  <Row gutter={8}>
                    <Col span={12}>
                      <Form.Item label={t('channels.simulator.web_search', '联网搜索 (次)')} name="web_search" style={{ marginBottom: 4 }}>
                        <InputNumber min={0} max={10} step={1} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                    <Col span={12}>
                      <Form.Item label={t('channels.simulator.audio_tokens', '音频 Tokens')} name="audio_tokens" tooltip={t('channels.simulator.audio_tokens_tip', '原生语音音频 Tokens 消耗')} style={{ marginBottom: 4 }}>
                        <InputNumber min={0} step={100} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                  </Row>
                </>
              )}

              {modalityMode === 'image' && (
                <>
                  <Row gutter={8}>
                    <Col span={16}>
                      <Form.Item
                        label={t('channels.simulator.image_resolution', '分辨率 / 尺寸')}
                        name="resolution"
                        tooltip={t('channels.simulator.image_resolution_tip', '可选 1k/2k/4k 或自定义输入尺寸')}
                        style={{ marginBottom: 10 }}
                      >
                        <AutoComplete
                          allowClear
                          filterOption={false}
                          defaultActiveFirstOption={false}
                          options={[
                            { value: '1k', label: '1K (1024x1024 常用方图)' },
                            { value: '2k', label: '2K (2048x2048 高清方图)' },
                            { value: '4k', label: '4K (4096x4096 极清像素)' },
                            { value: '1024x1024', label: '1024x1024 (标准 1:1)' },
                            { value: '1024x768', label: '1024x768 (横屏 4:3)' },
                            { value: '768x1024', label: '768x1024 (竖屏 3:4)' },
                            { value: '1280x720', label: '1280x720 (宽屏 16:9)' },
                            { value: '720x1280', label: '720x1280 (竖屏 9:16)' },
                          ]}
                          placeholder={t('channels.simulator.image_resolution_placeholder', '选择或输入尺寸 (如 1k, 1024x1024)')}
                          onChange={(val) => {
                            form.setFieldsValue({ resolution: val });
                            void executeSimulation();
                          }}
                          onSelect={(val) => {
                            form.setFieldsValue({ resolution: val });
                            void executeSimulation();
                          }}
                        />
                      </Form.Item>
                    </Col>
                    <Col span={8}>
                      <Form.Item
                        label={t('channels.simulator.image_count', '生成张数')}
                        name="image_count"
                        tooltip={t('channels.simulator.image_count_tip', '图片张数 (默认 1)')}
                        style={{ marginBottom: 10 }}
                      >
                        <InputNumber min={1} max={10} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                  </Row>
                  <Row gutter={8}>
                    <Col span={12}>
                      <Form.Item
                        label={t('channels.simulator.prompt_tokens', '提示词 (Prompt)')}
                        name="prompt_tokens"
                        tooltip={t('channels.simulator.image_prompt_tokens_tip', '用于 GPT-Image-2 / FLUX 等按 Token 计费的图像模型')}
                        style={{ marginBottom: 4 }}
                      >
                        <InputNumber min={0} step={100} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                    <Col span={12}>
                      <Form.Item
                        label={t('channels.simulator.completion_tokens', '生成/补全 (Completion)')}
                        name="completion_tokens"
                        tooltip={t('channels.simulator.image_completion_tokens_tip', '图片生成 Token 消耗（如 GPT-Image-2 生成 1K 图片通常约 10,000 Tokens）')}
                        style={{ marginBottom: 4 }}
                      >
                        <InputNumber min={0} step={1000} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                  </Row>
                </>
              )}

              {modalityMode === 'video' && (
                <>
                  <Row gutter={8}>
                    <Col span={12}>
                      <Form.Item
                        label={t('channels.simulator.duration_seconds', '视频时长 (秒)')}
                        name="duration_seconds"
                        tooltip={t('channels.simulator.duration_seconds_tip', '视频秒数 (默认 5 秒，按秒预扣与时长计费)')}
                        style={{ marginBottom: 10 }}
                      >
                        <InputNumber min={0.5} max={600} step={1} addonAfter="秒" style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                    <Col span={12}>
                      <Form.Item
                        label={t('channels.simulator.video_resolution', '视频清晰度')}
                        name="resolution"
                        tooltip={t('channels.simulator.video_resolution_tip', '支持 720p/1080p/4k 或自定义')}
                        style={{ marginBottom: 10 }}
                      >
                        <AutoComplete
                          allowClear
                          filterOption={false}
                          defaultActiveFirstOption={false}
                          options={[
                            { value: '480p', label: '480p (标清流畅)' },
                            { value: '720p', label: '720p (高清主流)' },
                            { value: '1080p', label: '1080p (全高清视效)' },
                            { value: '4k', label: '4K (超清电影级)' },
                          ]}
                          placeholder={t('channels.simulator.video_resolution_placeholder', '选择或输入清晰度 (如 720p, 1080p)')}
                          onChange={(val) => {
                            form.setFieldsValue({ resolution: val });
                            void executeSimulation();
                          }}
                          onSelect={(val) => {
                            form.setFieldsValue({ resolution: val });
                            void executeSimulation();
                          }}
                        />
                      </Form.Item>
                    </Col>
                  </Row>
                  <Row gutter={8}>
                    <Col span={12}>
                      <Form.Item
                        label={t('channels.simulator.prompt_tokens', '提示词 (Prompt)')}
                        name="prompt_tokens"
                        tooltip={t('channels.simulator.video_prompt_tokens_tip', '用于 Seedance / MiniMax 等按 Token 计费的视频模型')}
                        style={{ marginBottom: 4 }}
                      >
                        <InputNumber min={0} step={500} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                    <Col span={12}>
                      <Form.Item
                        label={t('channels.simulator.completion_tokens', '生成/补全 (Completion)')}
                        name="completion_tokens"
                        tooltip={t('channels.simulator.video_completion_tokens_tip', '视频生成 Token 消耗（如 5 秒 720p 视频通常约 45,000 Tokens）')}
                        style={{ marginBottom: 4 }}
                      >
                        <InputNumber min={0} step={5000} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                  </Row>
                </>
              )}

              {modalityMode === 'audio' && (
                <>
                  <Row gutter={8}>
                    <Col span={12}>
                      <Form.Item
                        label={t('channels.simulator.audio_tokens', '音频 Tokens')}
                        name="audio_tokens"
                        tooltip={t('channels.simulator.audio_tokens_tip', '原生语音音频 Tokens 消耗')}
                        style={{ marginBottom: 10 }}
                      >
                        <InputNumber min={0} step={100} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                    <Col span={12}>
                      <Form.Item
                        label={t('channels.simulator.text_characters', '文本字符数 (TTS)')}
                        name="text_characters"
                        tooltip={t('channels.simulator.text_characters_tip', '按字符计费的语音合成数量')}
                        style={{ marginBottom: 10 }}
                      >
                        <InputNumber min={0} step={100} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                  </Row>
                  <Row gutter={8}>
                    <Col span={12}>
                      <Form.Item
                        label={t('channels.simulator.audio_duration', '音频时长 (秒)')}
                        name="duration_seconds"
                        tooltip={t('channels.simulator.audio_duration_tip', '按秒/分计费的语音时长')}
                        style={{ marginBottom: 4 }}
                      >
                        <InputNumber min={0} step={1} addonAfter="秒" style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                    <Col span={12}>
                      <Form.Item
                        label={t('channels.simulator.prompt_tokens', '提示词 (Prompt)')}
                        name="prompt_tokens"
                        tooltip={t('channels.simulator.prompt_tokens', '提示词 (Prompt)')}
                        style={{ marginBottom: 4 }}
                      >
                        <InputNumber min={0} step={50} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                  </Row>
                </>
              )}

              {modalityMode === 'embedding' && (
                <>
                  <Row gutter={8}>
                    <Col span={12}>
                      <Form.Item
                        label={t('channels.simulator.embedding_tokens', '向量 Tokens (Prompt)')}
                        name="prompt_tokens"
                        tooltip={t('channels.simulator.embedding_tokens_tip', '向量化或重排输入 Tokens')}
                        style={{ marginBottom: 10 }}
                      >
                        <InputNumber min={1} step={100} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                    <Col span={12}>
                      <Form.Item
                        label={t('channels.simulator.cached_tokens', '缓存命中 (Cache Read)')}
                        name="cached_tokens"
                        style={{ marginBottom: 10 }}
                      >
                        <InputNumber min={0} step={100} style={{ width: '100%' }} onChange={executeSimulation} />
                      </Form.Item>
                    </Col>
                  </Row>
                  <Alert
                    type="info"
                    showIcon
                    style={{ fontSize: 12, padding: '6px 10px', marginTop: 4 }}
                    message={t('channels.simulator.embedding_notice', '向量嵌入与文档重排仅计算输入 Tokens，无需补全生成。')}
                  />
                </>
              )}
            </Card>
          </Form>
        </Col>

        {/* 右栏：计费推演流水线与综合对账单 */}
        <Col xs={24} lg={14} xl={14}>
          {loading ? (
            <div style={{ textAlign: 'center', padding: '100px 0' }}>
              <Spin size="large" tip={t('channels.simulator.simulating_spin', '正在推演试算...')} />
            </div>
          ) : simResult ? (
            <Space vertical size="middle" className="w-full">
              {/* 告警信息提示区 */}
              {simResult.warning && (
                <Alert
                  type="warning"
                  showIcon
                  message={
                    simResult.channel_info.is_disabled
                      ? t('channels.simulator.warn_disabled', '当前渠道处于禁用状态，线上调用将被拦截；此处仅演算理论计费。')
                      : simResult.warning
                  }
                  style={{ borderRadius: 6 }}
                />
              )}

              {/* 负毛利倒贴熔断告警卡片 */}
              {simResult.cost_and_margin.is_loss_making && (
                <Alert
                  type="error"
                  showIcon
                  message={<span style={{ fontWeight: 600 }}>{t('channels.simulator.negative_margin', '负毛利亏损倒贴')}</span>}
                  description={t('channels.simulator.negative_margin_desc', {
                    currency: currencySymbol,
                    actualCost: simResult.settlement.actual_cost.toFixed(6),
                    upstreamCost: simResult.cost_and_margin.upstream_cost.toFixed(6),
                    lossAmount: Math.abs(simResult.cost_and_margin.upstream_cost - simResult.settlement.actual_cost).toFixed(6),
                    marginPercent: simResult.cost_and_margin.margin_percent.toFixed(2),
                    defaultValue: `⚠️ 严重风险：当前终端收费 ${currencySymbol} ${simResult.settlement.actual_cost.toFixed(6)} 低于上游采购成本 ${currencySymbol} ${simResult.cost_and_margin.upstream_cost.toFixed(6)}，单次调用亏损 ${currencySymbol} ${Math.abs(simResult.cost_and_margin.upstream_cost - simResult.settlement.actual_cost).toFixed(6)}（毛利率 ${simResult.cost_and_margin.margin_percent.toFixed(2)}%）。请立即提高渠道倍率或提高模型保底限价！`,
                  })}
                  style={{ borderRadius: 6, border: `1px solid ${token.colorError}` }}
                />
              )}

              {/* 模块 A: 最终结算总览与平台毛利核心卡片 */}
              <Card size="small" style={{ borderRadius: 8, borderColor: token.colorBorder }} styles={{ body: { padding: 16 } }}>
                <Row gutter={[16, 16]}>
                  <Col span={8}>
                    <Statistic
                      title={
                        <span style={{ fontSize: 12 }}>
                          {t('channels.simulator.pre_deducted', '预扣冻结金额')}
                          {simResult.pipeline_trace.some((p) => p.stage === 'pre_deduct' && p.title.includes('按秒'))
                            ? ' (按秒预扣)'
                            : ''}
                        </span>
                      }
                      value={simResult.settlement.pre_deducted}
                      precision={6}
                      suffix={currencyUnit}
                      valueStyle={{ fontSize: 18 }}
                    />
                  </Col>
                  <Col span={8}>
                    <Statistic
                      title={<span style={{ fontSize: 12 }}>{t('channels.simulator.actual_cost', '实际结算扣费 (终端实收)')}</span>}
                      value={simResult.settlement.actual_cost}
                      precision={6}
                      suffix={currencyUnit}
                      valueStyle={{ fontSize: 18, color: token.colorPrimary, fontWeight: 600 }}
                    />
                  </Col>
                  <Col span={8}>
                    <Statistic
                      title={<span style={{ fontSize: 12 }}>{t('channels.simulator.refund', '预扣差额返还')}</span>}
                      value={simResult.settlement.refund_amount}
                      precision={6}
                      prefix="+"
                      suffix={currencyUnit}
                      valueStyle={{
                        fontSize: 18,
                        color: simResult.settlement.refund_amount > 0 ? token.colorSuccess : undefined,
                      }}
                    />
                  </Col>
                </Row>

                <Divider style={{ margin: '12px 0' }} />

                <Row gutter={[16, 16]}>
                  <Col span={8}>
                    <Statistic
                      title={<span style={{ fontSize: 12 }}>{t('channels.simulator.upstream_cost', '上游采购成本')} ({simResult.cost_and_margin.upstream_rate}x)</span>}
                      value={simResult.cost_and_margin.upstream_cost}
                      precision={6}
                      suffix={currencyUnit}
                      valueStyle={{ fontSize: 16 }}
                    />
                    <Text type="secondary" style={{ fontSize: 11 }}>
                      {formatUpstreamName(simResult.cost_and_margin.upstream_name)}
                    </Text>
                  </Col>
                  <Col span={8}>
                    <Statistic
                      title={<span style={{ fontSize: 12 }}>{t('channels.simulator.gross_profit', '平台毛利润')}</span>}
                      value={simResult.cost_and_margin.gross_profit}
                      precision={6}
                      prefix={simResult.cost_and_margin.gross_profit >= 0 ? '+' : ''}
                      suffix={currencyUnit}
                      valueStyle={{
                        fontSize: 16,
                        fontWeight: 600,
                        color: simResult.cost_and_margin.gross_profit < 0 ? token.colorError : token.colorSuccess,
                      }}
                    />
                    <Text type="secondary" style={{ fontSize: 11 }}>
                      {t('channels.simulator.gross_profit_hint', '终端实扣 - 采购成本')}
                    </Text>
                  </Col>
                  <Col span={8}>
                    <Statistic
                      title={<span style={{ fontSize: 12 }}>{t('channels.simulator.margin_percent', '单次综合毛利率')}</span>}
                      value={simResult.cost_and_margin.margin_percent}
                      precision={2}
                      prefix={simResult.cost_and_margin.margin_percent >= 0 ? <ArrowUpOutlined /> : <ArrowDownOutlined />}
                      suffix="%"
                      valueStyle={{
                        fontSize: 16,
                        fontWeight: 600,
                        color: simResult.cost_and_margin.is_loss_making ? token.colorError : token.colorSuccess,
                      }}
                    />
                    <Tag
                      color={
                        simResult.cost_and_margin.is_loss_making
                          ? 'error'
                          : simResult.cost_and_margin.margin_percent > 30
                          ? 'success'
                          : 'warning'
                      }
                      style={{ fontSize: 10, lineHeight: '16px', height: 16, margin: 0 }}
                    >
                      {simResult.cost_and_margin.is_loss_making
                        ? t('channels.simulator.margin_loss', '严重倒贴')
                        : simResult.cost_and_margin.margin_percent > 30
                        ? t('channels.simulator.margin_high', '健康高毛利')
                        : t('channels.simulator.margin_normal', '正常微利')}
                    </Tag>
                  </Col>
                </Row>
              </Card>

              {/* 模块 B: 全链路推演流水线 Step-by-Step Trace */}
              <Card
                title={
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                    <span style={{ fontSize: 14, fontWeight: 600 }}>
                      <CompassOutlined style={{ marginRight: 6, color: token.colorPrimary }} />
                      {t('channels.simulator.pipeline_trace', '计费推演流水线')}
                    </span>
                    <Text type="secondary" style={{ fontSize: 12 }}>
                      {t('channels.simulator.stage_count', { count: simResult.pipeline_trace.length, defaultValue: `共 ${simResult.pipeline_trace.length} 个演算阶段` })}
                    </Text>
                  </div>
                }
                size="small"
                style={{ borderRadius: 8, borderColor: token.colorBorder }}
                styles={{ body: { padding: '12px 16px' } }}
              >
                <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                  {simResult.pipeline_trace.map((trace) => {
                    const isDanger = trace.status === 'danger';
                    const isWarn = trace.status === 'warn';
                    const isPass = trace.status === 'pass';
                    const { title: stepTitle, formula: stepFormula, description: stepDescription } = getTraceItemDisplay(trace);

                    const stepTheme = isDanger
                      ? {
                          cardBg: token.colorErrorBg || 'rgba(255, 77, 79, 0.08)',
                          cardBorder: token.colorErrorBorder,
                          cardShadow: '0 1px 3px rgba(255, 77, 79, 0.12)',
                          badgeBg: token.colorErrorBg || 'rgba(255, 77, 79, 0.12)',
                          badgeBorder: token.colorErrorBorder,
                          badgeColor: token.colorError,
                        }
                      : isWarn
                      ? {
                          cardBg: token.colorWarningBg || 'rgba(250, 173, 20, 0.08)',
                          cardBorder: token.colorWarningBorder,
                          cardShadow: '0 1px 3px rgba(250, 173, 20, 0.12)',
                          badgeBg: token.colorWarningBg || 'rgba(250, 173, 20, 0.12)',
                          badgeBorder: token.colorWarningBorder,
                          badgeColor: token.colorWarning,
                        }
                      : isPass
                      ? {
                          cardBg: token.colorBgElevated,
                          cardBorder: token.colorBorder,
                          cardShadow: 'none',
                          badgeBg: token.colorSuccessBg || 'rgba(82, 196, 26, 0.12)',
                          badgeBorder: token.colorSuccessBorder,
                          badgeColor: token.colorSuccess,
                        }
                      : {
                          cardBg: token.colorBgElevated,
                          cardBorder: token.colorBorder,
                          cardShadow: 'none',
                          badgeBg: token.colorFillAlter,
                          badgeBorder: token.colorBorder,
                          badgeColor: token.colorText,
                        };

                    return (
                      <div
                        key={trace.step}
                        style={{
                          padding: '10px 12px',
                          borderRadius: 6,
                          background: stepTheme.cardBg,
                          border: `1px solid ${stepTheme.cardBorder}`,
                          boxShadow: stepTheme.cardShadow,
                        }}
                      >
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
                          <Space size={8} align="center">
                            <span
                              style={{
                                display: 'inline-flex',
                                alignItems: 'center',
                                justifyContent: 'center',
                                width: 22,
                                height: 22,
                                borderRadius: '50%',
                                fontSize: 11,
                                fontWeight: 600,
                                fontFamily: "'JetBrains Mono', 'Fira Code', monospace",
                                lineHeight: 1,
                                background: stepTheme.badgeBg,
                                border: `1px solid ${stepTheme.badgeBorder}`,
                                color: stepTheme.badgeColor,
                                flexShrink: 0,
                              }}
                            >
                              {trace.step}
                            </span>
                            <span style={{ fontWeight: 600, fontSize: 13, color: token.colorText }}>{stepTitle}</span>
                          </Space>

                          {isDanger ? (
                            <Tag color="error" icon={<CloseCircleOutlined />}>
                              {t('channels.simulator.status_danger', '亏损 / 拦截')}
                            </Tag>
                          ) : isWarn ? (
                            <Tag color="warning" icon={<WarningOutlined />}>
                              {t('channels.simulator.status_warn', '触发保底 / 警告')}
                            </Tag>
                          ) : isPass ? (
                            <Tag color="success" icon={<CheckCircleOutlined />}>
                              {t('channels.simulator.status_pass', '校验通过')}
                            </Tag>
                          ) : (
                            <Tag color="processing" icon={<CheckCircleOutlined />}>
                              {t('channels.simulator.status_normal', '正常演算')}
                            </Tag>
                          )}
                        </div>

                        <div
                          style={{
                            fontFamily: "'JetBrains Mono', 'Fira Code', ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace",
                            fontSize: 12,
                            fontWeight: 500,
                            color: token.colorText,
                            background: token.colorFillQuaternary,
                            border: `1px solid ${token.colorBorder}`,
                            padding: '5px 10px',
                            borderRadius: 6,
                            marginBottom: 6,
                            wordBreak: 'break-all',
                            letterSpacing: '0.2px',
                          }}
                        >
                          {stepFormula}
                        </div>

                        <div style={{ fontSize: 12, color: token.colorTextSecondary, lineHeight: 1.5 }}>
                          {stepDescription}
                        </div>
                      </div>
                    );
                  })}
                </div>
              </Card>

              {/* 模块 C: 真实日志明细文本与 JSON 预览 */}
              <Card
                title={
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                    <span style={{ fontSize: 14, fontWeight: 600 }}>{t('channels.simulator.billing_detail', '真实计费明细 (billing_detail)')}</span>
                    <Button size="small" icon={<CopyOutlined />} onClick={copyBillingDetail}>
                      {t('channels.simulator.copy_detail', '复制明细')}
                    </Button>
                  </div>
                }
                size="small"
                style={{ borderRadius: 8, borderColor: token.colorBorder }}
                styles={{ body: { padding: 12 } }}
              >
                <Paragraph
                  copyable={false}
                  style={{
                    margin: 0,
                    fontSize: 12,
                    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
                    background: token.colorFillAlter,
                    padding: '8px 12px',
                    borderRadius: 4,
                    border: `1px solid ${token.colorBorder}`,
                    wordBreak: 'break-all',
                  }}
                >
                  {simResult.settlement.billing_detail_text}
                </Paragraph>
              </Card>
            </Space>
          ) : (
            <Card style={{ textAlign: 'center', padding: '80px 0', borderRadius: 8, borderColor: token.colorBorder }}>
              <CalculatorOutlined style={{ fontSize: 48, color: token.colorTextTertiary, marginBottom: 16 }} />
              <Title level={4} style={{ margin: 0, color: token.colorTextSecondary }}>
                {t('channels.simulator.waiting_title', '等待开始计费沙盒试算')}
              </Title>
              <Text type="secondary" style={{ marginTop: 8, display: 'block' }}>
                {t('channels.simulator.waiting_desc', '在左侧配置参数后点击“立即试算”即可查看完整推导流水')}
              </Text>
            </Card>
          )}
        </Col>
      </Row>
    </Drawer>
  );
};

export default ChannelBillingSimulator;
