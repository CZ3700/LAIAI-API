import React, { useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Typography,
  message,
} from 'antd';
import {
  ApiOutlined,
  DeleteOutlined,
  PlusOutlined,
  ReloadOutlined,
  SearchOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import request from '../../utils/request';

const { Title, Text } = Typography;

interface UarBindingRow {
  id: number;
  rid: string;
  name: string;
  source_type: string;
  channel_config_id: number;
  asset_base_path: string;
  asset_protocol: string;
  volc_ak: string;
  volc_sk_mask: string;
  volc_project_name: string;
  volc_region: string;
  forward_rule_id: number;
  group_id: string;
  is_active: number;
  remark: string;
}

interface ChannelCfgOption {
  id: number;
  name: string;
  provider_type: string;
  base_url: string;
  yid?: string;
}

interface PluginAssetRow {
  id: number;
  org_id: number;
  asset_type: string;
  source: string;
  file_name: string;
  file_url: string;
  asset_id: string;
  meta_fingerprint: string;
  plugin_ns: string;
  created_at: string;
}

const VolcAssetPage: React.FC = () => {
  const [loading, setLoading] = useState(false);
  const [uarBindings, setUarBindings] = useState<UarBindingRow[]>([]);
  const [channelConfigs, setChannelConfigs] = useState<ChannelCfgOption[]>([]);
  const [assets, setAssets] = useState<PluginAssetRow[]>([]);
  const [sourceFilter, setSourceFilter] = useState('');

  const [assetNS, setAssetNS] = useState<'asset_manager' | 'asset_manager_intl'>('asset_manager');
  const [cfgForm] = Form.useForm();
  const [uarForm] = Form.useForm();
  const [uarModalOpen, setUarModalOpen] = useState(false);
  const sourceTypeWatch = Form.useWatch('source_type', uarForm);

  const [modForm] = Form.useForm();
  const [modResult, setModResult] = useState<string>('');

  const loadAll = async () => {
    setLoading(true);
    try {
      const [uarRes, cfgRes, assetsRes, chCfgRes]: any[] = await Promise.all([
        request.get('/plugins/upstream_asset_relay/bindings'),
        request.get('/assets/admin/config', { params: { ns: assetNS } }),
        request.get('/assets/admin/list', { params: sourceFilter ? { source: sourceFilter } : undefined }),
        request.get('/channel-configs'),
      ]);
      setUarBindings(Array.isArray(uarRes?.data) ? uarRes.data : []);
      cfgForm.setFieldsValue({
        volc_access_key: cfgRes?.volc_access_key || '',
        volc_secret_key: '',
        volc_project_name: cfgRes?.volc_project_name || 'default',
        volc_region: cfgRes?.volc_region || (assetNS === 'asset_manager_intl' ? 'ap-southeast-1' : 'cn-beijing'),
        volc_group_id: cfgRes?.volc_group_id || '',
      });
      setAssets(Array.isArray(assetsRes?.data) ? assetsRes.data : []);
      const chRows = Array.isArray(chCfgRes) ? chCfgRes : Array.isArray(chCfgRes?.data) ? chCfgRes.data : [];
      setChannelConfigs(chRows);
    } catch {
      // handled by interceptor
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void loadAll();
  }, [assetNS, sourceFilter]);

  const handleSaveAssetConfig = async () => {
    const values = await cfgForm.validateFields();
    await request.put('/assets/admin/config', { ns: assetNS, ...values });
    message.success(`素材管理凭证 (${assetNS}) 已保存`);
    await loadAll();
  };

  const handleCreateUarBinding = async () => {
    const values = await uarForm.validateFields();
    await request.post('/plugins/upstream_asset_relay/bindings', values);
    message.success('上游视频转素材绑定已创建');
    setUarModalOpen(false);
    uarForm.resetFields();
    await loadAll();
  };

  const handleGenerateRule = async (id: number) => {
    const res: any = await request.post(`/plugins/upstream_asset_relay/bindings/${id}/generate-rule`);
    message.success(`已生成转发规则 #${res?.data?.id ?? ''} (${res?.data?.eid ?? ''})`);
    await loadAll();
  };

  const handleQueryModeration = async () => {
    const values = await modForm.validateFields();
    const res: any = await request.post('/assets/admin/moderation-result', values);
    setModResult(JSON.stringify(res, null, 2));
  };

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 12 }}>
          <div>
            <Title level={4} style={{ margin: 0 }}>
              火山方舟素材转换与代理中心
            </Title>
            <Text type="secondary">
              支持 `asset_convert`（主账号 AK/SK）与 `upstream_asset_relay`（渠道 Bearer 代理 + `/v1/plugin/{'{rid}'}/assets`）自动将请求媒体 URL 转为 `asset://`
            </Text>
          </div>
          <Button icon={<ReloadOutlined />} onClick={() => void loadAll()} loading={loading}>
            刷新
          </Button>
        </div>
      </Card>

      <Card>
        <Tabs
          items={[
            {
              key: 'uar_bindings',
              label: `火山视频转素材ID (${uarBindings.length})`,
              children: (
                <>
                  <Space style={{ marginBottom: 12 }}>
                    <Button type="primary" icon={<PlusOutlined />} onClick={() => setUarModalOpen(true)}>
                      新增上游素材绑定
                    </Button>
                  </Space>
                  <Table
                    rowKey="id"
                    loading={loading}
                    dataSource={uarBindings}
                    pagination={{ pageSize: 20 }}
                    columns={[
                      { title: 'ID', dataIndex: 'id', width: 65 },
                      { title: '名称', dataIndex: 'name' },
                      {
                        title: '代理端点 (RID)',
                        dataIndex: 'rid',
                        render: (v: string) => <Text copyable code>{`/v1/plugin/${v}/assets`}</Text>,
                      },
                      {
                        title: '来源模式',
                        dataIndex: 'source_type',
                        render: (v: string, r: UarBindingRow) =>
                          v === 'volc_account' ? (
                            <Tag color="blue">直连官方AK</Tag>
                          ) : (
                            <Tag color="purple">渠道配置 #{r.channel_config_id}</Tag>
                          ),
                      },
                      { title: '协议', dataIndex: 'asset_protocol' },
                      { title: '素材组 GroupId', dataIndex: 'group_id', render: (v: string) => v || '自动创建' },
                      {
                        title: '生成转发规则',
                        dataIndex: 'forward_rule_id',
                        render: (v: number) => (v > 0 ? <Tag color="green">规则 #{v}</Tag> : <Tag>未生成</Tag>),
                      },
                      {
                        title: '操作',
                        width: 210,
                        render: (_: any, row: UarBindingRow) => (
                          <Space>
                            <Button
                              size="small"
                              type="primary"
                              ghost
                              icon={<ThunderboltOutlined />}
                              onClick={() => void handleGenerateRule(row.id)}
                            >
                              生成规则
                            </Button>
                            <Popconfirm
                              title="确认删除该上游绑定?"
                              onConfirm={async () => {
                                await request.delete(`/plugins/upstream_asset_relay/bindings/${row.id}`);
                                message.success('已删除绑定');
                                await loadAll();
                              }}
                            >
                              <Button size="small" danger icon={<DeleteOutlined />} />
                            </Popconfirm>
                          </Space>
                        ),
                      },
                    ]}
                  />
                </>
              ),
            },
            {
              key: 'asset_config',
              label: '系统素材审核凭证',
              children: (
                <Space direction="vertical" size={16} style={{ width: '100%', maxWidth: 640 }}>
                  <Alert
                    type="info"
                    showIcon
                    message="转发规则开启 asset_convert=true 时使用此凭证调用 CreateAsset / GetAsset，支持标准国内版 (cn-beijing) 与国际版 (ap-southeast-1)。"
                  />
                  <Space>
                    <Text strong>命名空间：</Text>
                    <Select
                      value={assetNS}
                      onChange={(v) => setAssetNS(v)}
                      style={{ width: 260 }}
                      options={[
                        { value: 'asset_manager', label: '国内版 (asset_manager)' },
                        { value: 'asset_manager_intl', label: '国际版 (asset_manager_intl)' },
                      ]}
                    />
                  </Space>
                  <Form form={cfgForm} layout="vertical">
                    <Form.Item name="volc_access_key" label="AccessKey ID" rules={[{ required: true }]}>
                      <Input />
                    </Form.Item>
                    <Form.Item name="volc_secret_key" label="SecretAccessKey（留空保持不变）">
                      <Input.Password placeholder="新建时必填，更新时留空保留原密钥" />
                    </Form.Item>
                    <Form.Item name="volc_project_name" label="方舟 ProjectName">
                      <Input placeholder="default" />
                    </Form.Item>
                    <Form.Item name="volc_region" label="接入区域 Region">
                      <Input placeholder="cn-beijing 或 ap-southeast-1" />
                    </Form.Item>
                    <Form.Item name="volc_group_id" label="默认素材组 GroupId（留空自动 CreateAssetGroup）">
                      <Input />
                    </Form.Item>
                    <Button type="primary" onClick={() => void handleSaveAssetConfig()}>
                      保存配置
                    </Button>
                  </Form>
                </Space>
              ),
            },
            {
              key: 'asset_cache',
              label: `素材缓存记录 (${assets.length})`,
              children: (
                <>
                  <Space style={{ marginBottom: 12 }}>
                    <Select
                      allowClear
                      placeholder="按来源过滤"
                      style={{ width: 220 }}
                      value={sourceFilter || undefined}
                      onChange={(v) => setSourceFilter(v || '')}
                      options={[
                        { value: 'relay_convert', label: '网关自动转换 (relay_convert)' },
                        { value: 'upstream_relay_convert', label: '上游中转转换 (upstream_relay_convert)' },
                        { value: 'api_proxy', label: 'API 透传创建 (api_proxy)' },
                      ]}
                    />
                  </Space>
                  <Table
                    rowKey="id"
                    loading={loading}
                    dataSource={assets}
                    pagination={{ pageSize: 20 }}
                    columns={[
                      { title: 'ID', dataIndex: 'id', width: 65 },
                      { title: '组织', dataIndex: 'org_id', width: 70 },
                      { title: '命名空间', dataIndex: 'plugin_ns', width: 140 },
                      { title: '来源', dataIndex: 'source', width: 170 },
                      { title: '类型', dataIndex: 'asset_type', width: 80 },
                      {
                        title: '方舟素材 ID',
                        dataIndex: 'asset_id',
                        render: (v: string) => <Text copyable code>{`asset://${v}`}</Text>,
                      },
                      { title: '原媒体 URL', dataIndex: 'file_url', ellipsis: true },
                      {
                        title: '操作',
                        width: 80,
                        render: (_: any, row: PluginAssetRow) => (
                          <Popconfirm
                            title="确认删除缓存记录?"
                            onConfirm={async () => {
                              await request.delete(`/assets/admin/${row.id}`);
                              message.success('已删除');
                              await loadAll();
                            }}
                          >
                            <Button size="small" danger icon={<DeleteOutlined />} />
                          </Popconfirm>
                        ),
                      },
                    ]}
                  />
                </>
              ),
            },
            {
              key: 'moderation',
              label: '风控拦截查询',
              children: (
                <Space direction="vertical" size={16} style={{ width: '100%', maxWidth: 640 }}>
                  <Form
                    form={modForm}
                    layout="vertical"
                    initialValues={{ ns: 'asset_manager', type: 'asset_id' }}
                  >
                    <Form.Item name="ns" label="插件命名空间">
                      <Select
                        options={[
                          { value: 'asset_manager', label: '国内版 (asset_manager)' },
                          { value: 'asset_manager_intl', label: '国际版 (asset_manager_intl)' },
                        ]}
                      />
                    </Form.Item>
                    <Form.Item name="type" label="ID 类型">
                      <Select
                        options={[
                          { value: 'asset_id', label: 'asset_id' },
                          { value: 'task_id', label: 'task_id' },
                          { value: 'request_id', label: 'request_id' },
                        ]}
                      />
                    </Form.Item>
                    <Form.Item name="id" label="目标 ID" rules={[{ required: true }]}>
                      <Input placeholder="输入 asset-xxxx 或 cgt-xxxx" />
                    </Form.Item>
                    <Button type="primary" icon={<SearchOutlined />} onClick={() => void handleQueryModeration()}>
                      查询审核原因 (GetModerationResult)
                    </Button>
                  </Form>
                  {modResult && (
                    <pre style={{ padding: 12, borderRadius: 8, background: 'rgba(127,127,127,0.08)', overflow: 'auto' }}>
                      {modResult}
                    </pre>
                  )}
                </Space>
              ),
            },
          ]}
        />
      </Card>

      <Modal
        title="新增上游素材中转绑定"
        open={uarModalOpen}
        onOk={() => void handleCreateUarBinding()}
        onCancel={() => setUarModalOpen(false)}
        destroyOnHidden
      >
        <Form
          form={uarForm}
          layout="vertical"
          initialValues={{ source_type: 'channel', asset_protocol: 'ark_action', volc_project_name: 'default', volc_region: 'cn-beijing' }}
        >
          <Form.Item name="name" label="绑定名称" rules={[{ required: true }]}>
            <Input placeholder="例如：火山转素材主通道" />
          </Form.Item>
          <Form.Item name="source_type" label="凭证来源模式">
            <Select
              options={[
                { value: 'channel', label: '关联上游渠道配置 (channel_configs Bearer)' },
                { value: 'volc_account', label: '直连火山官方账号 (AK/SK)' },
              ]}
            />
          </Form.Item>
          <Form.Item name="asset_protocol" label="素材协议">
            <Select
              options={[
                { value: 'ark_action', label: 'ark_action (火山方舟 CreateAsset)' },
                { value: 'portrait_rest', label: 'portrait_rest (Seedance Portrait REST)' },
                { value: 'tencent_vod', label: 'tencent_vod (腾讯云 VOD 素材)' },
              ]}
            />
          </Form.Item>
          {sourceTypeWatch !== 'volc_account' ? (
            <>
              <Form.Item name="channel_config_id" label="关联上游渠道配置" rules={[{ required: true }]}>
                <Select
                  showSearch
                  optionFilterProp="label"
                  options={channelConfigs.map((c) => ({
                    value: c.id,
                    label: `${c.yid || `Y${1000 + c.id}`} · ${c.name} (${c.provider_type})`,
                  }))}
                />
              </Form.Item>
              <Form.Item name="asset_base_path" label="素材基础路径（可选）">
                <Input placeholder="留空直接拼接 ?Action=CreateAsset" />
              </Form.Item>
            </>
          ) : (
            <>
              <Form.Item name="volc_ak" label="火山 AccessKey" rules={[{ required: true }]}>
                <Input />
              </Form.Item>
              <Form.Item name="volc_sk" label="火山 SecretKey" rules={[{ required: true }]}>
                <Input.Password />
              </Form.Item>
              <Form.Item name="volc_project_name" label="ProjectName">
                <Input />
              </Form.Item>
              <Form.Item name="volc_region" label="Region">
                <Input />
              </Form.Item>
            </>
          )}
        </Form>
      </Modal>
    </Space>
  );
};

export default VolcAssetPage;
