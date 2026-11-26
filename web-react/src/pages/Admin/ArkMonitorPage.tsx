import React, { useEffect, useState } from 'react';
import {
  Button,
  Card,
  Col,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Row,
  Select,
  Space,
  Statistic,
  Table,
  Tabs,
  Tag,
  Typography,
  message,
} from 'antd';
import {
  DeleteOutlined,
  DollarOutlined,
  PlusOutlined,
  ReloadOutlined,
  SyncOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';

const { Title, Text } = Typography;

interface ArkAccountRow {
  id: number;
  name: string;
  volc_account_id: string;
  access_key: string;
  secret_key_mask: string;
  region: string;
  remark: string;
  created_at: string;
}

interface ArkBindingRow {
  id: number;
  account_id: number;
  endpoint_id: string;
  org_id: number;
  user_uid: string;
  api_key_mask: string;
  used_quota: string | number;
  wallet_charged_quota: string | number;
  org_available_usd?: string | number;
  status: number;
  fuse_reason: string;
  remark: string;
  updated_at: string;
}

interface ArkTaskRow {
  id: number;
  account_id: number;
  endpoint_id: string;
  task_id: string;
  model: string;
  status: string;
  duration: number;
  resolution: string;
  created_time: string;
  split_amount: string | number;
  is_estimated: boolean;
  total_tokens: number;
}

const ArkMonitorPage: React.FC<{ mode?: 'admin' | 'user' }> = ({ mode = 'admin' }) => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [accounts, setAccounts] = useState<ArkAccountRow[]>([]);
  const [bindings, setBindings] = useState<ArkBindingRow[]>([]);
  const [tasks, setTasks] = useState<ArkTaskRow[]>([]);
  const [orgAvailableUSD, setOrgAvailableUSD] = useState<string>('0');
  const [endpointFilter, setEndpointFilter] = useState('');

  const [accountModalOpen, setAccountModalOpen] = useState(false);
  const [bindingModalOpen, setBindingModalOpen] = useState(false);
  const [accountForm] = Form.useForm();
  const [bindingForm] = Form.useForm();

  const loadData = async () => {
    setLoading(true);
    try {
      if (mode === 'user') {
        const res: any = await request.get('/ark-monitor/overview');
        setOrgAvailableUSD(String(res?.org_available_usd ?? '0'));
        setBindings(Array.isArray(res?.bindings) ? res.bindings : []);
        setTasks(Array.isArray(res?.tasks) ? res.tasks : []);
        return;
      }
      const [accRes, bindRes, taskRes]: any[] = await Promise.all([
        request.get('/plugins/volcengine_ark_monitor/accounts'),
        request.get('/plugins/volcengine_ark_monitor/bindings'),
        request.get('/plugins/volcengine_ark_monitor/tasks', {
          params: endpointFilter ? { endpoint_id: endpointFilter.trim() } : undefined,
        }),
      ]);
      setAccounts(Array.isArray(accRes?.data) ? accRes.data : []);
      setBindings(Array.isArray(bindRes?.data) ? bindRes.data : []);
      setTasks(Array.isArray(taskRes?.data) ? taskRes.data : []);
    } catch {
      // handled by interceptor
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void loadData();
  }, [mode]);

  const handleSyncNow = async () => {
    setSyncing(true);
    try {
      const res: any = await request.post('/plugins/volcengine_ark_monitor/sync');
      const s = res?.summary || {};
      message.success(
        t('ark_monitor.sync_summary', {
          accounts: s.accounts_processed ?? 0,
          tasks: s.tasks_upserted ?? 0,
          bills: s.bills_reconciled ?? 0,
          fused: s.endpoints_fused ?? 0,
          recovered: s.endpoints_recovered ?? 0,
          defaultValue: `同步完成：账号 ${s.accounts_processed ?? 0}，拉取任务 ${s.tasks_upserted ?? 0}，核销 ${s.bills_reconciled ?? 0}，熔断 ${s.endpoints_fused ?? 0}，恢复 ${s.endpoints_recovered ?? 0}`,
        }),
      );
      await loadData();
    } catch {
      // handled by interceptor
    } finally {
      setSyncing(false);
    }
  };

  const handleCheckBalance = async (id: number, name: string) => {
    try {
      const res: any = await request.post(`/plugins/volcengine_ark_monitor/accounts/${id}/balance`);
      const bal = res?.balance;
      message.info(
        t('ark_monitor.balance_info', {
          name,
          available: Number(bal?.available_balance ?? 0).toFixed(2),
          cash: Number(bal?.cash_balance ?? 0).toFixed(2),
          defaultValue: `[${name}] 可用余额 ¥${Number(bal?.available_balance ?? 0).toFixed(2)}（现金 ¥${Number(bal?.cash_balance ?? 0).toFixed(2)}）`,
        }),
      );
    } catch {
      // handled by interceptor
    }
  };

  const handleCreateAccount = async () => {
    const values = await accountForm.validateFields();
    await request.post('/plugins/volcengine_ark_monitor/accounts', values);
    message.success(t('ark_monitor.account_saved', '火山方舟主账号已保存'));
    setAccountModalOpen(false);
    accountForm.resetFields();
    await loadData();
  };

  const handleCreateBinding = async () => {
    const values = await bindingForm.validateFields();
    await request.post('/plugins/volcengine_ark_monitor/bindings', values);
    message.success(t('ark_monitor.binding_saved', '接入点绑定已保存'));
    setBindingModalOpen(false);
    bindingForm.resetFields();
    await loadData();
  };

  const handleToggleBinding = async (row: ArkBindingRow) => {
    const target = row.status === 1 ? 0 : 1;
    await request.post(`/plugins/volcengine_ark_monitor/bindings/${row.id}/toggle`, { status: target });
    message.success(
      target === 1
        ? t('ark_monitor.binding_enabled', '接入点已恢复启用')
        : t('ark_monitor.binding_disabled', '接入点已手动停用（不会自动拉起）'),
    );
    await loadData();
  };

  const bindingColumns = [
    ...(mode === 'admin' ? [{ title: 'ID', dataIndex: 'id', width: 70 }] : []),
    { title: t('ark_monitor.col_endpoint_id', '接入点 ID (Endpoint)'), dataIndex: 'endpoint_id', render: (v: string) => <Text code>{v}</Text> },
    ...(mode === 'admin'
      ? [
          { title: t('ark_monitor.col_account_id', '主账号 ID'), dataIndex: 'account_id', width: 100 },
          { title: t('ark_monitor.col_org_id', '组织 ID'), dataIndex: 'org_id', width: 90 },
          { title: 'API Key', dataIndex: 'api_key_mask', render: (v: string) => v || '-' },
        ]
      : []),
    {
      title: t('ark_monitor.col_used_quota', '累计用量 (CNY)'),
      dataIndex: 'used_quota',
      render: (v: string | number) => `¥${Number(v || 0).toFixed(4)}`,
    },
    ...(mode === 'admin'
      ? [
          {
            title: t('ark_monitor.col_wallet_anchor', '已入账锚点 (CNY)'),
            dataIndex: 'wallet_charged_quota',
            render: (v: string | number) => `¥${Number(v || 0).toFixed(4)}`,
          },
          {
            title: t('ark_monitor.col_org_available', '组织可用余额 (USD)'),
            dataIndex: 'org_available_usd',
            render: (v: string | number) => `$${Number(v || 0).toFixed(4)}`,
          },
        ]
      : []),
    {
      title: t('common.status', '状态'),
      dataIndex: 'status',
      render: (v: number, row: ArkBindingRow) =>
        v === 1 ? (
          <Tag color="green">{t('ark_monitor.status_running', '运行中')}</Tag>
        ) : (
          <Tag color={row.fuse_reason === 'manual' ? 'default' : 'red'}>
            {row.fuse_reason === 'manual'
              ? t('ark_monitor.status_manual_stopped', '已手动停用')
              : t('ark_monitor.status_fused', '余额熔断停用')}
          </Tag>
        ),
    },
    ...(mode === 'admin'
      ? [
          {
            title: t('common.actions', '操作'),
            width: 170,
            render: (_: any, row: ArkBindingRow) => (
              <Space>
                <Button size="small" onClick={() => void handleToggleBinding(row)}>
                  {row.status === 1 ? t('ark_monitor.btn_disable', '停用') : t('ark_monitor.btn_enable', '启用')}
                </Button>
                <Popconfirm
                  title={t('ark_monitor.confirm_delete_binding', '确认删除该绑定?')}
                  onConfirm={async () => {
                    await request.delete(`/plugins/volcengine_ark_monitor/bindings/${row.id}`);
                    message.success(t('ark_monitor.binding_deleted', '已删除绑定'));
                    await loadData();
                  }}
                >
                  <Button size="small" danger icon={<DeleteOutlined />} />
                </Popconfirm>
              </Space>
            ),
          },
        ]
      : []),
  ];

  const taskColumns = [
    { title: t('ark_monitor.col_task_id', '方舟任务 ID'), dataIndex: 'task_id', render: (v: string) => <Text code>{v}</Text> },
    { title: t('ark_monitor.col_endpoint', '接入点'), dataIndex: 'endpoint_id' },
    { title: t('common.model', '模型'), dataIndex: 'model' },
    {
      title: t('common.status', '状态'),
      dataIndex: 'status',
      render: (v: string) => (
        <Tag color={v === 'succeeded' || v === 'success' ? 'green' : v === 'failed' ? 'red' : 'blue'}>{v}</Tag>
      ),
    },
    { title: t('ark_monitor.col_spec', '规格'), render: (_: any, r: ArkTaskRow) => `${r.resolution || '-'} / ${r.duration || 0}s` },
    { title: 'Token', dataIndex: 'total_tokens' },
    {
      title: t('ark_monitor.col_amount_cny', '金额 (CNY)'),
      dataIndex: 'split_amount',
      render: (v: string | number, r: ArkTaskRow) => (
        <Space size={4}>
          <span>¥{Number(v || 0).toFixed(4)}</span>
          <Tag color={r.is_estimated ? 'orange' : 'cyan'}>
            {r.is_estimated ? t('ark_monitor.tag_estimated', '估算中') : t('ark_monitor.tag_reconciled', '账单确认')}
          </Tag>
        </Space>
      ),
    },
    { title: t('ark_monitor.col_created_time', '方舟创建时间'), dataIndex: 'created_time' },
  ];

  if (mode === 'user') {
    return (
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Row gutter={16}>
          <Col xs={24} sm={8}>
            <Card>
              <Statistic title={t('ark_monitor.user_org_balance', '组织钱包可用余额 (USD)')} value={Number(orgAvailableUSD || 0)} precision={4} prefix="$" />
            </Card>
          </Col>
          <Col xs={24} sm={8}>
            <Card>
              <Statistic title={t('ark_monitor.user_binding_count', '已绑定接入点数')} value={bindings.length} />
            </Card>
          </Col>
          <Col xs={24} sm={8}>
            <Card>
              <Statistic title={t('ark_monitor.user_task_count', '同步方舟任务数')} value={tasks.length} />
            </Card>
          </Col>
        </Row>
        <Card
          title={t('ark_monitor.user_bindings_title', '我的接入点绑定与熔断状态')}
          extra={
            <Button icon={<ReloadOutlined />} onClick={() => void loadData()} loading={loading}>
              {t('common.refresh', '刷新')}
            </Button>
          }
        >
          <Table rowKey="id" dataSource={bindings} columns={bindingColumns} pagination={false} loading={loading} />
        </Card>
        <Card title={t('ark_monitor.user_tasks_title', '火山方舟视频任务对账明细')}>
          <Table rowKey="id" dataSource={tasks} columns={taskColumns} loading={loading} pagination={{ pageSize: 20 }} />
        </Card>
      </Space>
    );
  }

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 12 }}>
          <div>
            <Title level={4} style={{ margin: 0 }}>
              {t('ark_monitor.page_title', '火山方舟视频监控与对账熔断')}
            </Title>
            <Text type="secondary">
              {t('ark_monitor.page_desc', '管理火山方舟主账号 AK/SK、接入点 Endpoint 与组织钱包绑定、分账账单 1:1 核销及欠费自动 StopEndpoint 熔断')}
            </Text>
          </div>
          <Space>
            <Button icon={<ReloadOutlined />} onClick={() => void loadData()} loading={loading}>
              {t('common.refresh', '刷新')}
            </Button>
            <Button type="primary" icon={<SyncOutlined />} loading={syncing} onClick={() => void handleSyncNow()}>
              {t('ark_monitor.sync_now', '立即同步对账')}
            </Button>
          </Space>
        </div>
      </Card>

      <Card>
        <Tabs
          items={[
            {
              key: 'accounts',
              label: `${t('ark_monitor.tab_accounts', '主账号凭证')} (${accounts.length})`,
              children: (
                <>
                  <div style={{ marginBottom: 12 }}>
                    <Button type="primary" icon={<PlusOutlined />} onClick={() => setAccountModalOpen(true)}>
                      {t('ark_monitor.add_account', '新增主账号 AK/SK')}
                    </Button>
                  </div>
                  <Table
                    rowKey="id"
                    loading={loading}
                    dataSource={accounts}
                    pagination={{ pageSize: 20 }}
                    columns={[
                      { title: 'ID', dataIndex: 'id', width: 70 },
                      { title: t('common.name', '名称'), dataIndex: 'name' },
                      { title: t('ark_monitor.col_volc_account_id', '火山账号 ID'), dataIndex: 'volc_account_id', render: (v: string) => v || '-' },
                      { title: 'AccessKey', dataIndex: 'access_key' },
                      { title: 'SecretKey', dataIndex: 'secret_key_mask' },
                      { title: t('ark_monitor.col_region', '区域'), dataIndex: 'region' },
                      { title: t('common.remark', '备注'), dataIndex: 'remark' },
                      {
                        title: t('common.actions', '操作'),
                        width: 190,
                        render: (_: any, row: ArkAccountRow) => (
                          <Space>
                            <Button
                              size="small"
                              icon={<DollarOutlined />}
                              onClick={() => void handleCheckBalance(row.id, row.name)}
                            >
                              {t('ark_monitor.btn_balance', '余额')}
                            </Button>
                            <Popconfirm
                              title={t('ark_monitor.confirm_delete_account', '确认删除主账号及其绑定?')}
                              onConfirm={async () => {
                                await request.delete(`/plugins/volcengine_ark_monitor/accounts/${row.id}`);
                                message.success(t('ark_monitor.account_deleted', '已删除主账号'));
                                await loadData();
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
              key: 'bindings',
              label: `${t('ark_monitor.tab_bindings', '接入点绑定')} (${bindings.length})`,
              children: (
                <>
                  <div style={{ marginBottom: 12 }}>
                    <Button type="primary" icon={<PlusOutlined />} onClick={() => setBindingModalOpen(true)}>
                      {t('ark_monitor.add_binding', '新增接入点绑定')}
                    </Button>
                  </div>
                  <Table rowKey="id" loading={loading} dataSource={bindings} columns={bindingColumns} pagination={{ pageSize: 20 }} />
                </>
              ),
            },
            {
              key: 'tasks',
              label: `${t('ark_monitor.tab_tasks', '方舟视频任务')} (${tasks.length})`,
              children: (
                <>
                  <Space style={{ marginBottom: 12 }}>
                    <Input
                      placeholder={t('ark_monitor.filter_endpoint_placeholder', '按接入点 ep-xxx 过滤')}
                      value={endpointFilter}
                      onChange={(e) => setEndpointFilter(e.target.value)}
                      style={{ width: 240 }}
                      allowClear
                    />
                    <Button onClick={() => void loadData()}>{t('common.search', '查询')}</Button>
                  </Space>
                  <Table rowKey="id" loading={loading} dataSource={tasks} columns={taskColumns} pagination={{ pageSize: 20 }} />
                </>
              ),
            },
          ]}
        />
      </Card>

      <Modal
        title={t('ark_monitor.modal_add_account', '新增火山方舟主账号')}
        open={accountModalOpen}
        onOk={() => void handleCreateAccount()}
        onCancel={() => setAccountModalOpen(false)}
        destroyOnHidden
      >
        <Form form={accountForm} layout="vertical" initialValues={{ region: 'cn-beijing' }}>
          <Form.Item name="name" label={t('ark_monitor.field_account_alias', '账号别名')} rules={[{ required: true }]}>
            <Input placeholder={t('ark_monitor.ph_account_alias', '例如：主账号-华北')} />
          </Form.Item>
          <Form.Item name="volc_account_id" label={t('ark_monitor.field_volc_account_id', '火山官方 AccountID')}>
            <Input placeholder={t('ark_monitor.ph_volc_account_id', '可选，用于标识')} />
          </Form.Item>
          <Form.Item name="access_key" label="AccessKey (AK)" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="secret_key" label="SecretKey (SK)" rules={[{ required: true }]}>
            <Input.Password />
          </Form.Item>
          <Form.Item name="region" label="Region">
            <Select
              options={[
                { value: 'cn-beijing', label: t('ark_monitor.region_cn', 'cn-beijing (国内)') },
                { value: 'ap-southeast-1', label: t('ark_monitor.region_intl', 'ap-southeast-1 (BytePlus 国际)') },
              ]}
            />
          </Form.Item>
          <Form.Item name="remark" label={t('common.remark', '备注')}>
            <Input />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={t('ark_monitor.modal_add_binding', '新增 Endpoint 接入点绑定')}
        open={bindingModalOpen}
        onOk={() => void handleCreateBinding()}
        onCancel={() => setBindingModalOpen(false)}
        destroyOnHidden
      >
        <Form form={bindingForm} layout="vertical" initialValues={{ org_id: 1 }}>
          <Form.Item name="account_id" label={t('ark_monitor.field_parent_account', '所属主账号')} rules={[{ required: true }]}>
            <Select options={accounts.map((a) => ({ value: a.id, label: `${a.name} (#${a.id})` }))} />
          </Form.Item>
          <Form.Item name="endpoint_id" label={t('ark_monitor.field_endpoint_id', '接入点 Endpoint ID')} rules={[{ required: true }]}>
            <Input placeholder="e.g. ep-2026xxxx-xxxx" />
          </Form.Item>
          <Form.Item name="org_id" label={t('ark_monitor.field_org_id', '关联扣费组织 ID')} rules={[{ required: true }]}>
            <InputNumber min={1} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="api_key" label={t('ark_monitor.field_api_key', '方舟静态 Bearer API Key')} rules={[{ required: true }]}>
            <Input.Password placeholder={t('ark_monitor.ph_api_key', '用于 ListVideos 拉取任务状态与 token 估算')} />
          </Form.Item>
          <Form.Item name="remark" label={t('common.remark', '备注')}>
            <Input />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  );
};

export default ArkMonitorPage;
