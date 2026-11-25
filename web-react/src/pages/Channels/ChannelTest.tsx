import React, { useEffect, useState } from 'react';
import { useParams, useNavigate, useLocation, useSearchParams } from 'react-router-dom';
import { Card, Table, Typography, Tag, Button, Space, message, Row, Col, Divider, Select, Tooltip, Modal, Input } from 'antd';
import { SyncOutlined, ArrowLeftOutlined, CalculatorOutlined, SettingOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import type { Channel } from '../../types';
import { ChannelBillingSimulator } from './components/ChannelBillingSimulator';
import { channelEditPath, channelListPath } from './channelPaths';

const isVolcImageToolMid = (mid: string | null | undefined): boolean =>
  mid === 'vie-en' || mid === 'vir-bg';
const isVolcImageToolModel = (
  m: { mid?: string; model_id?: string } | null | undefined,
): boolean =>
  Boolean(
    m &&
      (isVolcImageToolMid(m.mid) ||
        m.model_id === 'volc_image_enhance' ||
        m.model_id === 'volc_image_remove_bg'),
  );

const { Title, Text } = Typography;

type TestStatus = 'idle' | 'testing' | 'success' | 'error';
interface TestResult {
  status: TestStatus;
  latency?: number;
  message?: string;
  request_data?: any;
  response_data?: any;
  curl_command?: string;
  timestamp?: string;
  matched_rule?: {
    eid?: string;
    target_type?: string;
    upstream_path?: string;
    auth_type?: string;
    is_cascade?: boolean;
    cascade_engine?: string;
    asset_convert?: boolean;
    fast_response?: boolean;
  };
  cascade_preflight?: {
    engine?: string;
    ready?: boolean;
    message?: string;
    crop_480p?: boolean;
    crop_timing?: string;
  };
  entry_path?: string;
  scenario?: string;
}

const SCENARIO_LABELS: Record<string, string> = {
  default: '默认场景',
  stream: '流式对话 (SSE)',
  anthropic_messages: 'Anthropic 原生',
  gemini_native: 'Gemini 原生',
  openai_responses: 'Responses 协议',
  image_i2i: '图生图',
  video_720p: '文生视频 (720p)',
  video_1080p: '文生视频 (1080p)',
  video_i2v: '图生视频',
  stt: '语音转录 (STT)',
};

const SCENARIOS_BY_CATEGORY: Record<string, { label: string; value: string }[]> = {
  图片: [
    { label: '文生图 (T2I)', value: 'default' },
    { label: '图生图 (I2I/素材转换)', value: 'image_i2i' },
  ],
  视频: [
    { label: '文生视频 (480p)', value: 'default' },
    { label: '文生视频 (720p)', value: 'video_720p' },
    { label: '文生视频 (1080p)', value: 'video_1080p' },
    { label: '图生视频 (I2V/素材转换)', value: 'video_i2v' },
  ],
  封装: [
    { label: '文生视频 (480p)', value: 'default' },
    { label: '文生视频 (720p)', value: 'video_720p' },
    { label: '文生视频 (1080p)', value: 'video_1080p' },
    { label: '图生视频 (I2V/素材转换)', value: 'video_i2v' },
  ],
  音频: [
    { label: '语音合成 (TTS)', value: 'default' },
    { label: '语音转录 (STT)', value: 'stt' },
  ],
  向量: [{ label: '向量嵌入 (Embedding)', value: 'default' }],
  排序: [{ label: '文本重排 (Rerank)', value: 'default' }],
};

const DEFAULT_CHAT_SCENARIOS = [
  { label: '常规对话 (Chat)', value: 'default' },
  { label: '流式对话 (SSE)', value: 'stream' },
  { label: 'Anthropic 原生 (/v1/messages)', value: 'anthropic_messages' },
  { label: 'Gemini 原生 (/v1beta)', value: 'gemini_native' },
  { label: 'Responses 协议 (/v1/responses)', value: 'openai_responses' },
];

const getAvailableScenarios = (category: string = '聊天') => SCENARIOS_BY_CATEGORY[category] || DEFAULT_CHAT_SCENARIOS;

const isImageToolModel = (gModel?: any, rawKey?: string) =>
  isVolcImageToolModel(gModel) ||
  isVolcImageToolMid(gModel?.library_mid) ||
  isVolcImageToolMid(rawKey);

const resolveTestCategory = (gModel?: any, rawKey?: string, ruleCategory?: string) => {
  const cat = ruleCategory || gModel?.type_name || '聊天';
  return cat === '封装' && isImageToolModel(gModel, rawKey) ? '图片' : cat;
};

const PAYLOAD_PLACEHOLDERS: Record<string, string> = {
  图片: '{\n  "prompt": "一只可爱的小猫，高清画质",\n  "size": "1024x1024"\n}',
  视频: '{\n  "prompt": "海浪拍打沙滩，夕阳西下",\n  "resolution": "480p",\n  "duration": 5\n}',
  封装: '{\n  "prompt": "海浪拍打沙滩，夕阳西下",\n  "resolution": "480p",\n  "duration": 5\n}',
  音频: '{\n  "input": "欢迎使用laiai系统",\n  "voice": "alloy"\n}',
};

const getDefaultPayloadTemplate = (category: string) =>
  PAYLOAD_PLACEHOLDERS[category] || '{\n  "messages": [\n    {"role": "user", "content": "你好"}\n  ],\n  "temperature": 0.7\n}';

const ChannelTest: React.FC = () => {
    const { t } = useTranslation();
    const params = useParams<{ id: string }>();
    const id = params.id;
    const navigate = useNavigate();
    const location = useLocation();
    const [searchParams] = useSearchParams();
    const adminPath = localStorage.getItem('laiai_admin_path') || 'admin';
    const fromEdit = searchParams.get('from') === 'edit' || (location.state as { from?: string } | null)?.from === 'edit';
    const origin = (location.state as { origin?: string } | null)?.origin;
    const [channel, setChannel] = useState<Channel | null>(null);
    const [loading, setLoading] = useState(true);
    const [testStatuses, setTestStatuses] = useState<Record<string, TestResult>>({});
    const [selectedTestModels, setSelectedTestModels] = useState<React.Key[]>([]);
    
    // New states for Rules evaluation
    const [globalModels, setGlobalModels] = useState<Record<string, any>>({});
    const [forwardRules, setForwardRules] = useState<Record<number, any>>({});
    const [selectedRules, setSelectedRules] = useState<Record<string, number>>({});
    const [selectedScenarios, setSelectedScenarios] = useState<Record<string, string>>({});
    const [customPayloads, setCustomPayloads] = useState<Record<string, string>>({});
    const [editingPayloadModel, setEditingPayloadModel] = useState<string | null>(null);
    const [editingPayloadDraft, setEditingPayloadDraft] = useState<string>('');
    
    const [activeModelLog, setActiveModelLog] = useState<string | null>(null);
    const [batchTesting, setBatchTesting] = useState(false);

    // 高可用子渠道列表相关状态
    const [subChannels, setSubChannels] = useState<Channel[]>([]);
    const [isHaMode, setIsHaMode] = useState<boolean>(false);
    const [expandedRowKeys, setExpandedRowKeys] = useState<React.Key[]>([]);

    // 计费全链路仿真状态
    const [simulatorOpen, setSimulatorOpen] = useState(false);
    const [simModel, setSimModel] = useState<string | undefined>(undefined);

    useEffect(() => {
        const fetchChannelData = async () => {
            try {
                // Parallel fetch resources needed for UI mappings
                const [channelResp, modelsResp, configsResp, rulesResp] = await Promise.all([
                    request.get('/channels') as unknown as Promise<{ data: Channel[] }>,
                    request.get('/models') as unknown as Promise<{ data: any[] }>,
                    request.get('/channel-configs') as unknown as Promise<{ data: any[] }>,
                    request.get('/forward-rules') as unknown as Promise<any[]>
                ]);
                
                const modMap: Record<string, any> = {};
                modelsResp.data.forEach(m => { 
                    modMap[String(m.model_id)] = m; 
                    if (m.mid) {
                        modMap[String(m.mid)] = m;
                    }
                });
                setGlobalModels(modMap);

                const rMap: Record<number, any> = {};
                rulesResp.forEach(r => { rMap[r.id] = r; });
                setForwardRules(rMap);

                const target = channelResp.data.find(c => c.id === Number(id));
                if (target) {
                    setChannel(target);

                    const isHa = target.provider_type === 'high_availability_group';
                    setIsHaMode(isHa);

                    let subChs: Channel[] = [];
                    if (isHa) {
                        let parsed: any = {};
                        try {
                            parsed = target.config ? (typeof target.config === 'string' ? JSON.parse(target.config) : target.config) : {};
                        } catch {}
                        const subIds: number[] = Array.isArray(parsed.sub_channels) ? parsed.sub_channels.map(Number) : [];
                        const overrides = parsed.upstream_overrides && typeof parsed.upstream_overrides === 'object' ? parsed.upstream_overrides : {};
                        subChs = (configsResp.data || [])
                            .filter((c: any) => subIds.includes(Number(c.id)))
                            .map((c: any) => {
                                const ov = overrides[String(c.id)];
                                const effPriority = ov && typeof ov.priority === 'number' && Number.isFinite(ov.priority) ? ov.priority : Number(c.priority ?? 0);
                                const effWeight = ov && typeof ov.weight === 'number' && Number.isFinite(ov.weight) && ov.weight >= 1 ? ov.weight : Math.max(1, Number(c.weight ?? 1));
                                const effStatus = (c.status ?? 1) !== 1 || ov?.status === 0 ? 0 : 1;
                                return {
                                    ...c,
                                    priority: effPriority,
                                    weight: effWeight,
                                    status: effStatus,
                                };
                            })
                            .sort((a: any, b: any) => {
                                if (b.priority !== a.priority) return b.priority - a.priority;
                                return subIds.indexOf(Number(a.id)) - subIds.indexOf(Number(b.id));
                            });
                        setSubChannels(subChs);
                    }
                    
                    const initialStatuses: Record<string, TestResult> = {};
                    const initSelRules: Record<string, number> = {};
                    
                    target.models.forEach(m => {
                        if (isHa) {
                            subChs.forEach(sc => {
                                initialStatuses[`${m}_${sc.id}`] = { status: 'idle' };
                            });
                        } else {
                            initialStatuses[m] = { status: 'idle' };
                        }

                        const gModel = modMap[String(m)];
                        if (gModel && gModel.forward_rule_ids) {
                            try {
                                const rIds = JSON.parse(gModel.forward_rule_ids);
                                if (Array.isArray(rIds) && rIds.length > 0) {
                                    initSelRules[m] = rIds[0];
                                }
                            } catch(e) {}
                        }
                    });
                    setTestStatuses(initialStatuses);
                    setSelectedRules(initSelRules);
                    if (isHa) {
                        setExpandedRowKeys(target.models);
                    }
                } else {
                    message.error('渠道并未找到，请检查！');
                    navigate(channelListPath(adminPath));
                }
            } catch (e) {
                console.error(e);
            } finally {
                setLoading(false);
            }
        };
        fetchChannelData();
    }, [id, navigate, adminPath]);

    const runSingleModelTest = async (channelId: number, model: string, ruleId?: number, autoFocus: boolean = true, statusKey?: string, subChannelId?: number) => {
        const key = statusKey || model;
        if (autoFocus) {
            setActiveModelLog(key);
        }
        setTestStatuses(prev => ({ ...prev, [key]: { status: 'testing', timestamp: new Date().toISOString() } }));
        try {
            const gModel = globalModels[String(model)];
            const actualModelId = gModel ? gModel.model_id : model;
            const scenario = selectedScenarios[model];
            const customStr = customPayloads[model];
            let custom_body: any = undefined;
            if (customStr && customStr.trim()) {
                try {
                    custom_body = JSON.parse(customStr);
                } catch {
                    message.warning('自定义请求体非合法 JSON，已按默认场景发送');
                }
            }

            const payload: any = { 
                model: actualModelId, 
                forward_rule_id: ruleId,
                scenario: scenario && scenario !== 'default' ? scenario : undefined,
                custom_body: custom_body,
            };
            if (subChannelId) payload.sub_channel_id = subChannelId;
            const resp = await (request.post(`/channels/${channelId}/test`, payload) as unknown as Promise<{ 
                success: boolean; 
                err_msg?: string; 
                latency?: number; 
                request_data?: any; 
                response_data?: any; 
                curl_command?: string;
                matched_rule?: any;
                cascade_preflight?: any;
                entry_path?: string;
                scenario?: string;
            }>);
            if (resp.success) {
                setTestStatuses(prev => ({ 
                    ...prev, 
                    [key]: { 
                        status: 'success', 
                        latency: resp.latency, 
                        request_data: resp.request_data, 
                        response_data: resp.response_data, 
                        curl_command: resp.curl_command, 
                        matched_rule: resp.matched_rule,
                        cascade_preflight: resp.cascade_preflight,
                        entry_path: resp.entry_path,
                        scenario: resp.scenario,
                        timestamp: new Date().toISOString() 
                    } 
                }));
            } else {
                setTestStatuses(prev => ({ 
                    ...prev, 
                    [key]: { 
                        status: 'error', 
                        message: resp.err_msg, 
                        latency: resp.latency, 
                        request_data: resp.request_data, 
                        response_data: resp.response_data, 
                        curl_command: resp.curl_command, 
                        matched_rule: resp.matched_rule,
                        cascade_preflight: resp.cascade_preflight,
                        entry_path: resp.entry_path,
                        scenario: resp.scenario,
                        timestamp: new Date().toISOString() 
                    } 
                }));
            }
        } catch (e: any) {
            setTestStatuses(prev => ({ 
                ...prev, 
                [key]: { status: 'error', message: e.message || '网关连接断开或超时', timestamp: new Date().toISOString() } 
            }));
        }
    };

    const handleBatchTest = async () => {
        if (!channel || selectedTestModels.length === 0) return;
        
        setBatchTesting(true);
        try {
            const promises: Promise<void>[] = [];
            
            if (isHaMode) {
                if (subChannels.length > 0) {
                    setActiveModelLog(`${selectedTestModels[0]}_${subChannels[0].id}`);
                }
                
                selectedTestModels.forEach(modelKey => {
                    const model = modelKey as string;
                    subChannels.forEach(sc => {
                        promises.push(runSingleModelTest(channel!.id, model, selectedRules[model], false, `${model}_${sc.id}`, sc.id));
                    });
                });
            } else {
                setActiveModelLog(selectedTestModels[0] as string);
                selectedTestModels.forEach(modelKey => {
                    const model = modelKey as string;
                    promises.push(runSingleModelTest(channel.id, model, selectedRules[model], false));
                });
            }

            await Promise.all(promises);
            message.success('勾选模型的批量拨测已完成');
        } catch (error) {
            message.error('批量拨测发生异常');
        } finally {
            setBatchTesting(false);
        }
    };

    // UI renderer for logs
    const _isLight = document.documentElement.getAttribute('data-theme') !== 'dark';
    const activeLogData = activeModelLog ? testStatuses[activeModelLog] : null;

    let displayLogTarget = activeModelLog || '';
    let displaySubChannelName = '';
    if (activeModelLog && isHaMode) {
        const parts = activeModelLog.split('_');
        if (parts.length >= 2) {
            const modelName = parts.slice(0, -1).join('_');
            const subId = Number(parts[parts.length - 1]);
            displayLogTarget = modelName;
            const sc = subChannels.find(c => c.id === subId);
            if (sc) {
                displaySubChannelName = ` (物理上游: ${sc.name})`;
            }
        }
    }

    const handleBackAction = () => {
        if (fromEdit && id) {
            navigate(channelEditPath(adminPath, id), origin ? { state: { from: origin } } : undefined);
        } else {
            navigate(channelListPath(adminPath));
        }
    };

    return (
        <Card bordered={false} styles={{ body: { padding: 24 } }}>
            <div style={{ marginBottom: 24, display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 16 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
                    <Button icon={<ArrowLeftOutlined />} onClick={handleBackAction}>
                        {fromEdit ? '返回编辑' : '返回列表'}
                    </Button>
                    <div>
                        <Title level={3} style={{ margin: 0 }}>
                            {channel?.name ? `渠道日志抓取分析：${channel.name}` : '渠道日志抓取分析'}
                        </Title>
                        {channel?.base_url && (
                            <Text type="secondary" style={{ marginTop: 2, display: 'block', fontSize: 12 }}>
                                Base URL: {channel.base_url}
                            </Text>
                        )}
                    </div>
                </div>
                <Button
                    type="primary"
                    icon={<CalculatorOutlined />}
                    onClick={() => {
                        const targetModel = displayLogTarget
                            ? (globalModels[displayLogTarget]?.model_id || displayLogTarget)
                            : undefined;
                        setSimModel(targetModel);
                        setSimulatorOpen(true);
                    }}
                >
                    {t('channels.simulate_billing', '计费全链路仿真')}
                </Button>
            </div>
            
            <Row gutter={24}>
                {/* Left Panel: Models Table */}
                <Col xs={24} lg={12} xl={14} style={{ marginBottom: 24 }}>
                    <Card size="small" title="模型拨测队列" extra={
                        <Button type="primary" onClick={handleBatchTest} disabled={selectedTestModels.length === 0 || batchTesting} loading={batchTesting}>
                            {batchTesting ? '批量拨测中...' : `批量拨测 ${selectedTestModels.length} 个模型`}
                        </Button>
                    }>
                       <Table
                            dataSource={channel?.models.map(m => ({ model: m })) || []}
                            rowKey="model"
                            loading={loading}
                            pagination={false}
                            scroll={{ x: 'max-content', y: 'calc(100vh - 350px)' }}
                            onRow={(record) => {
                                return {
                                    onClick: () => {
                                        if (isHaMode) {
                                            if (subChannels.length > 0) {
                                                setActiveModelLog(`${record.model}_${subChannels[0].id}`);
                                            }
                                        } else {
                                            setActiveModelLog(record.model);
                                        }
                                    },
                                    style: { cursor: 'pointer' }
                                };
                            }}
                            rowSelection={{
                                selectedRowKeys: selectedTestModels,
                                onChange: (newSelectedRowKeys) => setSelectedTestModels(newSelectedRowKeys),
                            }}
                            expandable={isHaMode ? {
                                expandedRowKeys: expandedRowKeys,
                                onExpandedRowsChange: (keys) => setExpandedRowKeys(keys as React.Key[]),
                                expandedRowRender: (record) => {
                                    return (
                                        <div style={{ padding: '8px 12px', background: _isLight ? 'rgba(0,0,0,0.01)' : 'rgba(255,255,255,0.01)', borderRadius: 6, border: _isLight ? '1px dashed rgba(0,0,0,0.08)' : '1px dashed rgba(255,255,255,0.08)' }}>
                                            <div style={{ fontSize: 11, color: 'var(--text-secondary)', marginBottom: 6, fontWeight: 'bold' }}>
                                                ↳ 物理上游子通道拨测队列 ({subChannels.length})
                                            </div>
                                            <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                                                {subChannels.map(sc => {
                                                    const statusKey = `${record.model}_${sc.id}`;
                                                    const st = testStatuses[statusKey];
                                                    const isTesting = st?.status === 'testing';
                                                    
                                                    return (
                                                        <div 
                                                            key={sc.id} 
                                                            onClick={(e) => {
                                                                e.stopPropagation();
                                                                setActiveModelLog(statusKey);
                                                            }}
                                                            style={{ 
                                                                display: 'flex', 
                                                                justifyContent: 'space-between', 
                                                                alignItems: 'center', 
                                                                padding: '6px 12px', 
                                                                background: activeModelLog === statusKey ? (_isLight ? 'rgba(22,119,255,0.08)' : 'rgba(22,119,255,0.15)') : 'transparent', 
                                                                border: activeModelLog === statusKey ? '1px solid #91caff' : (_isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'),
                                                                borderRadius: 6,
                                                                cursor: 'pointer',
                                                                transition: 'all 0.15s'
                                                            }}
                                                        >
                                                            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flex: 1, minWidth: 0, flexWrap: 'wrap' }}>
                                                                <span style={{ fontSize: 12, fontWeight: 600 }}>{sc.name}</span>
                                                                <span style={{ fontSize: 10, color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
                                                                    {sc.provider_type ? `(${sc.provider_type})` : ''} {(sc as any).yid ? `YID: ${(sc as any).yid}` : `ID: ${sc.id}`}
                                                                </span>
                                                                {(sc as any).status !== undefined && (sc as any).status !== 1 && (
                                                                    <Tag color="error" style={{ margin: 0, fontSize: 10, borderRadius: 4 }}>已禁用</Tag>
                                                                )}
                                                                <Space size={4} style={{ marginLeft: 8 }}>
                                                                    <Tag color="cyan" style={{ margin: 0, fontSize: 10, borderRadius: 4 }}>倍率: {(sc as any).rate ?? 1}x</Tag>
                                                                    <Tag color="blue" style={{ margin: 0, fontSize: 10, borderRadius: 4 }}>优先级: {(sc as any).priority ?? 0}</Tag>
                                                                    <Tag color="cyan" style={{ margin: 0, fontSize: 10, borderRadius: 4 }}>权重: {(sc as any).weight ?? 1}</Tag>
                                                                </Space>
                                                            </div>
                                                            
                                                            <Space size={8} style={{ flexShrink: 0 }}>
                                                                {!st || st.status === 'idle' ? (
                                                                    <Tag color="default" style={{ margin: 0, fontSize: 10, borderRadius: 3 }}>未开始</Tag>
                                                                ) : isTesting ? (
                                                                    <Tag color="processing" icon={<SyncOutlined spin />} style={{ margin: 0, fontSize: 10, borderRadius: 3 }}>拨测中</Tag>
                                                                ) : st.status === 'success' ? (
                                                                    <Tag color="success" style={{ margin: 0, fontSize: 10, borderRadius: 3 }}>成功 ({st.latency}ms)</Tag>
                                                                ) : (
                                                                    <Tooltip title={st.message}>
                                                                        <Tag color="error" style={{ margin: 0, fontSize: 10, borderRadius: 3, maxWidth: 120, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                                                            失败: {st.message}
                                                                        </Tag>
                                                                    </Tooltip>
                                                                )}
                                                                
                                                                <Button
                                                                    size="small"
                                                                    type="dashed"
                                                                    style={{ fontSize: 11 }}
                                                                    loading={isTesting}
                                                                    disabled={(sc as any).status !== undefined && (sc as any).status !== 1}
                                                                    onClick={(e) => {
                                                                        e.stopPropagation();
                                                                        if ((sc as any).status !== undefined && (sc as any).status !== 1) {
                                                                            message.warning('该上游渠道配置已禁用，无法拨测');
                                                                            return;
                                                                        }
                                                                        runSingleModelTest(channel!.id, record.model, selectedRules[record.model], true, statusKey, sc.id);
                                                                    }}
                                                                >
                                                                    发起拨测
                                                                </Button>
                                                            </Space>
                                                        </div>
                                                    );
                                                })}
                                            </div>
                                        </div>
                                    );
                                },
                                rowExpandable: () => true,
                                defaultExpandAllRows: true,
                            } : undefined}
                            columns={[
                                {
                                    title: '接入模型',
                                    dataIndex: 'model',
                                    key: 'model',
                                    render: (text) => {
                                        const gModel = globalModels[String(text)];
                                        if (gModel) {
                                            return (
                                                <Space direction="vertical" size={0}>
                                                    <Text strong>{gModel.name || gModel.model_id}</Text>
                                                    <Tag color="blue" style={{ fontSize: 10, margin: 0, marginTop: 4, borderRadius: 3 }}>{gModel.model_id}</Tag>
                                                </Space>
                                            );
                                        }
                                        return <Text strong>{text}</Text>;
                                    }
                                },
                                {
                                    title: '探测状态',
                                    key: 'status',
                                    render: (_, record) => {
                                        if (isHaMode) {
                                            const statuses = subChannels.map(sc => testStatuses[`${record.model}_${sc.id}`]?.status);
                                            if (statuses.every(s => s === 'idle' || !s)) return <Tag color="default" style={{ borderRadius: 3 }}>未开始</Tag>;
                                            if (statuses.some(s => s === 'testing')) return <Tag color="processing" icon={<SyncOutlined spin />} style={{ borderRadius: 3 }}>拨测中</Tag>;
                                            if (statuses.every(s => s === 'success')) return <Tag color="success" style={{ borderRadius: 3 }}>全部通道成功</Tag>;
                                            if (statuses.every(s => s === 'error')) return <Tag color="error" style={{ borderRadius: 3 }}>全部通道异常</Tag>;
                                            if (statuses.some(s => s === 'error')) {
                                                const errCount = statuses.filter(s => s === 'error').length;
                                                return <Tag color="warning" style={{ borderRadius: 3 }}>部分通道异常 ({errCount}失败)</Tag>;
                                            }
                                            return <Tag color="success" style={{ borderRadius: 3 }}>部分通道成功</Tag>;
                                        }

                                        const st = testStatuses[record.model];
                                        if (!st || st.status === 'idle') return <Tag color="default" style={{ borderRadius: 3 }}>未开始</Tag>;
                                        if (st.status === 'testing') return <Tag color="processing" icon={<SyncOutlined spin />} style={{ borderRadius: 3 }}>拨测中</Tag>;
                                        if (st.status === 'success') return <Tag color="success" style={{ borderRadius: 3 }}>成功 ({st.latency}ms)</Tag>;
                                        return (
                                            <Tooltip title={st.message}>
                                                <Tag color="error" style={{ maxWidth: 150, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', verticalAlign: 'middle', borderRadius: 3 }}>
                                                    失败: {st.message}
                                                </Tag>
                                            </Tooltip>
                                        );
                                    }
                                },
                                {
                                    title: '操作',
                                    key: 'action',
                                    render: (_, record) => {
                                        const gModel = globalModels[String(record.model)];
                                        const curRuleId = selectedRules[record.model];
                                        const category = resolveTestCategory(gModel, record.model, curRuleId ? forwardRules[curRuleId]?.category : undefined);
                                        const scenarioOptions = getAvailableScenarios(category);

                                        let ruleOptions: {label: string, value: number}[] = [];
                                        if (gModel && gModel.forward_rule_ids) {
                                            try {
                                                const rIds = typeof gModel.forward_rule_ids === 'string' ? JSON.parse(gModel.forward_rule_ids) : gModel.forward_rule_ids;
                                                ruleOptions = (Array.isArray(rIds) ? rIds : []).map((rid: number) => {
                                                    const r = forwardRules[rid];
                                                    return r ? { label: `${r.category}: ${r.name}`, value: rid } : null;
                                                }).filter(Boolean) as {label: string, value: number}[];
                                            } catch(e) {}
                                        }

                                        const isAnySubTesting = isHaMode 
                                            ? subChannels.some(sc => testStatuses[`${record.model}_${sc.id}`]?.status === 'testing') 
                                            : testStatuses[record.model]?.status === 'testing';

                                        return (
                                            <Space direction="horizontal" size="small" wrap>
                                                {/* 1. 先定转发规则：明晰调用协议与网关链路 */}
                                                {ruleOptions.length > 0 && (
                                                    <Tooltip title="选择拨测所匹配的转发规则（决定调用协议与网关链路）">
                                                        <Select
                                                            size="small"
                                                            style={{ minWidth: 160 }}
                                                            placeholder="选择转发规则"
                                                            value={curRuleId || undefined}
                                                            onChange={(val) => {
                                                                setSelectedRules({ ...selectedRules, [record.model]: val });
                                                                const nextCat = resolveTestCategory(gModel, record.model, val ? forwardRules[val]?.category : undefined);
                                                                const nextScenarios = getAvailableScenarios(nextCat);
                                                                if (!nextScenarios.some(s => s.value === selectedScenarios[record.model])) {
                                                                    setSelectedScenarios(prev => ({ ...prev, [record.model]: 'default' }));
                                                                }
                                                            }}
                                                            options={ruleOptions}
                                                            onClick={(e) => e.stopPropagation()}
                                                        />
                                                    </Tooltip>
                                                )}
                                                {/* 2. 再选规则分类下的业务场景预设 */}
                                                <Tooltip title={`当前「${category}」分类业务场景预设`}>
                                                    <Select
                                                        size="small"
                                                        style={{ minWidth: 140 }}
                                                        placeholder="场景预设"
                                                        value={selectedScenarios[record.model] || 'default'}
                                                        onChange={(val) => setSelectedScenarios({ ...selectedScenarios, [record.model]: val })}
                                                        options={scenarioOptions}
                                                        onClick={(e) => e.stopPropagation()}
                                                    />
                                                </Tooltip>
                                                {/* 3. 微调请求入参 */}
                                                <Tooltip title={customPayloads[record.model] ? '已配置自定义入参 (点击修改)' : '微调请求入参报文 (JSON)'}>
                                                    <Button
                                                        size="small"
                                                        type={customPayloads[record.model] ? 'primary' : 'default'}
                                                        icon={<SettingOutlined />}
                                                        onClick={(e) => {
                                                            e.stopPropagation();
                                                            const existing = customPayloads[record.model];
                                                            setEditingPayloadDraft(existing && existing.trim() ? existing : getDefaultPayloadTemplate(category));
                                                            setEditingPayloadModel(record.model);
                                                        }}
                                                    />
                                                </Tooltip>
                                                {/* 4. 发起拨测 */}
                                                <Button 
                                                    size="small" 
                                                    type="dashed"
                                                    loading={isAnySubTesting}
                                                    onClick={(e) => {
                                                        e.stopPropagation();
                                                        if (isHaMode) {
                                                            subChannels.forEach(sc => {
                                                                runSingleModelTest(channel!.id, record.model, selectedRules[record.model], true, `${record.model}_${sc.id}`, sc.id);
                                                            });
                                                        } else {
                                                            runSingleModelTest(channel!.id, record.model, selectedRules[record.model]);
                                                        }
                                                    }}
                                                >
                                                    {isHaMode ? '全部拨测' : '发起拨测'}
                                                </Button>
                                            </Space>
                                        );
                                    }
                                }
                            ]}
                        />
                    </Card>
                </Col>

                {/* Right Panel: Data Layout Rendering */}
                <Col xs={24} lg={12} xl={10}>
                    <Card size="small" title={activeModelLog ? `链路解包跟踪 - ${displayLogTarget}${displaySubChannelName}` : '链路解包跟踪'}>
                        {!activeModelLog ? (
                            <div style={{ padding: 40, textAlign: 'center', color: '#666', display: 'flex', flexDirection: 'column', justifyContent: 'center', height: 'calc(100vh - 350px)' }}>
                                <div style={{ fontSize: 16, marginBottom: 12 }}>等待查看底层链路</div>
                                <div>请点击左侧任一模型，或发起拨测，<br/>即可实时显示底层发出与接收的真实 Payload。</div>
                            </div>
                        ) : (
                            <div style={{
                                backgroundColor: '#1e1e1e',
                                color: '#d4d4d4',
                                padding: 16,
                                borderRadius: 8,
                                minHeight: 'calc(100vh - 310px)',
                                maxHeight: 'calc(100vh - 310px)',
                                overflowY: 'auto',
                                fontFamily: 'monospace',
                                fontSize: 13,
                            }}>
                                <div style={{ marginBottom: 16, color: '#4ec9b0', fontWeight: 'bold' }}>
                                    {`[${activeLogData?.timestamp || '队列外'}]`} 【Target】: {globalModels[String(displayLogTarget)]?.model_id || displayLogTarget}{displaySubChannelName}
                                </div>
                                
                                {activeLogData?.status === 'idle' && (
                                    <div style={{ color: '#ce9178' }}>... 等待指令，暂无建立 of TPC/HTTP 数据交换记录 ...</div>
                                )}
                                
                                {activeLogData?.status === 'testing' && (
                                    <div style={{ color: '#569cd6' }}>&gt; 正在拨号并执行网关校验... 
                                      <span style={{ color: '#9cdcfe', paddingLeft: 8, fontSize: 12 }}>(若是视频/异步任务，可能自动轮询，预计 1-3 分钟，请耐心等待)</span>
                                    </div>
                                )}

                                {(activeLogData?.status === 'success' || activeLogData?.status === 'error') && (
                                    <>
                                        {/* 命中的转发规则元信息徽章 */}
                                        {activeLogData.matched_rule && (
                                            <div style={{ marginBottom: 12, display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                                                <Tag color="purple">协议: {activeLogData.matched_rule.target_type || 'openai'}</Tag>
                                                <Tag color="cyan">鉴权: {activeLogData.matched_rule.auth_type || 'bearer'}</Tag>
                                                {activeLogData.entry_path && (
                                                    <Tag color="geekblue">入口: {activeLogData.entry_path}</Tag>
                                                )}
                                                {activeLogData.matched_rule.upstream_path && (
                                                    <Tag color="blue">上游: {activeLogData.matched_rule.upstream_path}</Tag>
                                                )}
                                                {activeLogData.matched_rule.is_cascade && (
                                                    <Tag color="magenta">级联: {activeLogData.matched_rule.cascade_engine}</Tag>
                                                )}
                                                {activeLogData.matched_rule.asset_convert && (
                                                    <Tag color="orange">素材转换开启</Tag>
                                                )}
                                                {activeLogData.scenario && (
                                                    <Tag color="green">场景: {SCENARIO_LABELS[activeLogData.scenario] || activeLogData.scenario}</Tag>
                                                )}
                                            </div>
                                        )}

                                        {/* 级联阶段二预检提示 */}
                                        {activeLogData.cascade_preflight && (
                                            <div style={{
                                                marginBottom: 12,
                                                padding: '6px 12px',
                                                borderRadius: 6,
                                                background: activeLogData.cascade_preflight.ready ? 'rgba(82, 196, 26, 0.15)' : 'rgba(250, 173, 20, 0.15)',
                                                border: `1px solid ${activeLogData.cascade_preflight.ready ? '#52c41a' : '#faad14'}`
                                            }}>
                                                <div style={{ fontSize: 12, color: activeLogData.cascade_preflight.ready ? '#73d13d' : '#ffc53d', fontWeight: 600 }}>
                                                    {activeLogData.cascade_preflight.ready ? '✓' : '⚠'} 级联超分预检: {activeLogData.cascade_preflight.message}
                                                </div>
                                                <div style={{ fontSize: 11, color: '#aaa', marginTop: 2 }}>
                                                    裁剪 480p: {activeLogData.cascade_preflight.crop_480p ? '开启' : '关闭'} | 时机: {activeLogData.cascade_preflight.crop_timing}
                                                </div>
                                            </div>
                                        )}

                                        {/* 排查诊断建议 */}
                                        {activeLogData.status === 'error' && (
                                            <div style={{ marginTop: 8, marginBottom: 16, padding: '8px 12px', borderRadius: 6, background: 'rgba(245, 34, 45, 0.15)', border: '1px solid #ff4d4f' }}>
                                                <div style={{ color: '#ff7875', fontWeight: 'bold', fontSize: 12, marginBottom: 4 }}>💡 拨测诊断建议:</div>
                                                <div style={{ color: '#ffccc7', fontSize: 12 }}>
                                                    {activeLogData.response_data?._upstream_status === 404 && 'HTTP 404: 上游未命中该请求路径。请检查转发规则中的路径重写 (path_rewrite) 是否匹配上游官方文档，或尝试切换场景为特定原生协议。'}
                                                    {activeLogData.response_data?._upstream_status === 401 && 'HTTP 401: 鉴权失败。请检查渠道的 API Key 是否有效，或转发规则配置的鉴权模式 (auth_type) 是否匹配。'}
                                                    {activeLogData.response_data?._upstream_status === 400 && 'HTTP 400: 入参不合法。可能是上游需要的入参格式不一致，请检查下方构造的请求报文，或尝试切换测试场景/自定义入参微调。'}
                                                    {activeLogData.response_data?._upstream_status === 403 && 'HTTP 403: 权限不足或被云厂商风控拦截。请检查上游账号额度、IP 白名单或免审核策略设置。'}
                                                    {(!activeLogData.response_data?._upstream_status || ![400, 401, 403, 404].includes(activeLogData.response_data?._upstream_status)) && (activeLogData.message || '网关上游未正常响应，请查看下方代理回包快照详情')}
                                                </div>
                                            </div>
                                        )}

                                        <div style={{ color: '#9cdcfe', marginTop: 8 }}>### [OUTGOING] 构造的请求报文 (Request Config) ###</div>
                                        <div style={{ borderLeft: '3px solid #569cd6', marginTop: 8, marginBottom: 24, background: '#252526', padding: 12 }}>
                                           <pre style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                                                {JSON.stringify(activeLogData.request_data || {}, null, 2)}
                                           </pre>
                                        </div>

                                        {activeLogData.curl_command && (
                                            <>
                                                <div style={{ color: '#ce9178', marginTop: 8 }}>### [CURL] 通道 REST 同构请求 (cURL Equivalent) ###</div>
                                                <div style={{ borderLeft: '3px solid #ce9178', marginTop: 8, marginBottom: 24, background: '#252526', padding: 12 }}>
                                                   <pre style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                                                         {activeLogData.curl_command}
                                                   </pre>
                                                </div>
                                            </>
                                        )}

                                        {/* 流式聚合文本展示 */}
                                        {activeLogData.response_data?.stream && activeLogData.response_data?.text && (
                                            <>
                                                <div style={{ color: '#6a9955', marginTop: 8 }}>### [STREAM] 流式聚合回答文本 ###</div>
                                                <div style={{ borderLeft: '3px solid #6a9955', marginTop: 8, marginBottom: 16, background: '#252526', padding: 12, color: '#ececec', whiteSpace: 'pre-wrap', maxHeight: 200, overflowY: 'auto' }}>
                                                    {activeLogData.response_data.text}
                                                </div>
                                            </>
                                        )}

                                        <div style={{ color: activeLogData.status === 'success' ? '#6a9955' : '#f44336' }}>
                                            ### [INCOMING] 代理回包快照 (Response Snapshot) ###
                                            <span style={{ marginLeft: 8, background: '#333', padding: '2px 8px', borderRadius: 4 }}>
                                                Round-Trip Time: {activeLogData.latency} ms
                                                {activeLogData.response_data?._upstream_status && ` | HTTP ${activeLogData.response_data._upstream_status}`}
                                            </span>
                                        </div>
                                        <div style={{ borderLeft: `3px solid ${activeLogData.status === 'success' ? '#6a9955' : '#f44336'}`, marginTop: 8, background: '#252526', padding: 12 }}>
                                           <pre style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                                                {JSON.stringify(activeLogData.response_data || { intercept_err: activeLogData.message }, null, 2)}
                                           </pre>
                                        </div>
                                    </>
                                )}
                            </div>
                        )}
                    </Card>
                </Col>
            </Row>

            <ChannelBillingSimulator
                open={simulatorOpen}
                onClose={() => setSimulatorOpen(false)}
                channelId={channel?.id || Number(id)}
                channelName={channel?.name}
                channelData={channel}
                initialModel={simModel}
            />

            <Modal
                title={`微调测试报文 - ${editingPayloadModel || ''}`}
                open={!!editingPayloadModel}
                onOk={() => {
                    if (editingPayloadModel) {
                        const val = editingPayloadDraft.trim();
                        if (val) {
                            try {
                                JSON.parse(val);
                            } catch {
                                message.error('请输入合法的 JSON 格式字符串');
                                return;
                            }
                            setCustomPayloads(prev => ({ ...prev, [editingPayloadModel]: editingPayloadDraft }));
                        } else {
                            setCustomPayloads(prev => {
                                const next = { ...prev };
                                delete next[editingPayloadModel];
                                return next;
                            });
                        }
                    }
                    setEditingPayloadModel(null);
                }}
                onCancel={() => setEditingPayloadModel(null)}
                okText="确定并保存"
                cancelText="取消"
                destroyOnClose
            >
                <div style={{ marginBottom: 8, fontSize: 12, color: 'var(--text-secondary)' }}>
                    支持在此输入覆盖或新增的 JSON 字段（例如自定义 prompt、size、duration、resolution、image 等），拨测将以深层合并方式组装并提交上游：
                </div>
                <Input.TextArea
                    rows={8}
                    value={editingPayloadDraft}
                    onChange={(e) => setEditingPayloadDraft(e.target.value)}
                    style={{ fontFamily: 'monospace', fontSize: 12 }}
                />
            </Modal>
        </Card>
    );
};

export default ChannelTest;
