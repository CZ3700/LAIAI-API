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
        `同步完成：账号 ${s.accounts_processed ?? 0}，拉取任务 ${s.tasks_upserted ?? 0}，核销 ${s.bills_reconciled ?? 0}，熔断 ${s.endpoints_fused ?? 0}，恢复 ${s.endpoints_recovered ?? 0}`,
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
        `[${name}] 可用余额 ¥${Number(bal?.available_balance ?? 0).toFixed(2)}（现金 ¥${Number(bal?.cash_balance ?? 0).toFixed(2)}）`,
      );
    } catch {
      // handled by interceptor
    }
  };

  const handleCreateAccount = async () => {
    const values = await accountForm.validateFields();
    await request.post('/plugins/volcengine_ark_monitor/accounts', values);
    message.success('火山方舟主账号已保存');
    setAccountModalOpen(false);
    accountForm.resetFields();
    await loadData();
  };

  const handleCreateBinding = async () => {
    const values = await bindingForm.validateFields();
    await request.post('/plugins/volcengine_ark_monitor/bindings', values);
    message.success('接入点绑定已保存');
    setBindingModalOpen(false);
    bindingForm.resetFields();
    await loadData();
  };

  const handleToggleBinding = async (row: ArkBindingRow) => {
    const target = row.status === 1 ? 0 : 1;
    await request.post(`/plugins/volcengine_ark_monitor/bindings/${row.id}/toggle`, { status: target });
    message.success(target === 1 ? '接入点已恢复启用' : '接入点已手动停用（不会自动拉起）');
    await loadData();
  };

  const bindingColumns = [
    ...(mode === 'admin' ? [{ title: 'ID', dataIndex: 'id', width: 70 }] : []),
    { title: '接入点 ID (Endpoint)', dataIndex: 'endpoint_id', render: (v: string) => <Text code>{v}</Text> },
    ...(mode === 'admin'
      ? [
          { title: '主账号 ID', dataIndex: 'account_id', width: 100 },
          { title: '组织 ID', dataIndex: 'org_id', width: 90 },
          { title: 'API Key', dataIndex: 'api_key_mask', render: (v: string) => v || '-' },
        ]
      : []),
    {
      title: '累计用量 (CNY)',
      dataIndex: 'used_quota',
      render: (v: string | number) => `¥${Number(v || 0).toFixed(4)}`,
    },
    ...(mode === 'admin'
      ? [
          {
            title: '已入账锚点 (CNY)',
            dataIndex: 'wallet_charged_quota',
            render: (v: string | number) => `¥${Number(v || 0).toFixed(4)}`,
          },
          {
            title: '组织可用余额 (USD)',
            dataIndex: 'org_available_usd',
            render: (v: string | number) => `$${Number(v || 0).toFixed(4)}`,
          },
        ]
      : []),
    {
      title: '状态',
      dataIndex: 'status',
      render: (v: number, row: ArkBindingRow) =>
        v === 1 ? (
          <Tag color="green">运行中</Tag>
        ) : (
          <Tag color={row.fuse_reason === 'manual' ? 'default' : 'red'}>
            {row.fuse_reason === 'manual' ? '已手动停用' : '余额熔断停用'}
          </Tag>
        ),
    },
    ...(mode === 'admin'
      ? [
          {
            title: '操作',
            width: 170,
            render: (_: any, row: ArkBindingRow) => (
              <Space>
                <Button size="small" onClick={() => void handleToggleBinding(row)}>
                  {row.status === 1 ? '停用' : '启用'}
                </Button>
                <Popconfirm
                  title="确认删除该绑定?"
                  onConfirm={async () => {
                    await request.delete(`/plugins/volcengine_ark_monitor/bindings/${row.id}`);
                    message.success('已删除绑定');
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
    { title: '方舟任务 ID', dataIndex: 'task_id', render: (v: string) => <Text code>{v}</Text> },
    { title: '接入点', dataIndex: 'endpoint_id' },
    { title: '模型', dataIndex: 'model' },
    {
      title: '状态',
      dataIndex: 'status',
      render: (v: string) => (
        <Tag color={v === 'succeeded' || v === 'success' ? 'green' : v === 'failed' ? 'red' : 'blue'}>{v}</Tag>
      ),
    },
    { title: '规格', render: (_: any, r: ArkTaskRow) => `${r.resolution || '-'} / ${r.duration || 0}s` },
    { title: 'Token', dataIndex: 'total_tokens' },
    {
      title: '金额 (CNY)',
      dataIndex: 'split_amount',
      render: (v: string | number, r: ArkTaskRow) => (
        <Space size={4}>
          <span>¥{Number(v || 0).toFixed(4)}</span>
          <Tag color={r.is_estimated ? 'orange' : 'cyan'}>{r.is_estimated ? '估算中' : '账单确认'}</Tag>
        </Space>
      ),
    },
    { title: '方舟创建时间', dataIndex: 'created_time' },
  ];

  if (mode === 'user') {
    return (
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Row gutter={16}>
          <Col xs={24} sm={8}>
            <Card>
              <Statistic title="组织钱包可用余额 (USD)" value={Number(orgAvailableUSD || 0)} precision={4} prefix="$" />
            </Card>
          </Col>
          <Col xs={24} sm={8}>
            <Card>
              <Statistic title="已绑定接入点数" value={bindings.length} />
            </Card>
          </Col>
          <Col xs={24} sm={8}>
            <Card>
              <Statistic title="同步方舟任务数" value={tasks.length} />
            </Card>
          </Col>
        </Row>
        <Card
          title="我的接入点绑定与熔断状态"
          extra={
            <Button icon={<ReloadOutlined />} onClick={() => void loadData()} loading={loading}>
              刷新
            </Button>
          }
        >
          <Table rowKey="id" dataSource={bindings} columns={bindingColumns} pagination={false} loading={loading} />
        </Card>
        <Card title="火山方舟视频任务对账明细">
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
              火山方舟视频监控与对账熔断
            </Title>
            <Text type="secondary">
              管理火山方舟主账号 AK/SK、接入点 Endpoint 与组织钱包绑定、分账账单 1:1 核销及欠费自动 StopEndpoint 熔断
            </Text>
          </div>
          <Space>
            <Button icon={<ReloadOutlined />} onClick={() => void loadData()} loading={loading}>
              刷新
            </Button>
            <Button type="primary" icon={<SyncOutlined />} loading={syncing} onClick={() => void handleSyncNow()}>
              立即同步对账
            </Button>
          </Space>
        </div>
      </Card>

      <Card>
        <Tabs
          items={[
            {
              key: 'accounts',
              label: `主账号凭证 (${accounts.length})`,
              children: (
                <>
                  <div style={{ marginBottom: 12 }}>
                    <Button type="primary" icon={<PlusOutlined />} onClick={() => setAccountModalOpen(true)}>
                      新增主账号 AK/SK
                    </Button>
                  </div>
                  <Table
                    rowKey="id"
                    loading={loading}
                    dataSource={accounts}
                    pagination={{ pageSize: 20 }}
                    columns={[
                      { title: 'ID', dataIndex: 'id', width: 70 },
                      { title: '名称', dataIndex: 'name' },
                      { title: '火山账号 ID', dataIndex: 'volc_account_id', render: (v: string) => v || '-' },
                      { title: 'AccessKey', dataIndex: 'access_key' },
                      { title: 'SecretKey', dataIndex: 'secret_key_mask' },
                      { title: '区域', dataIndex: 'region' },
                      { title: '备注', dataIndex: 'remark' },
                      {
                        title: '操作',
                        width: 190,
                        render: (_: any, row: ArkAccountRow) => (
                          <Space>
                            <Button
                              size="small"
                              icon={<DollarOutlined />}
                              onClick={() => void handleCheckBalance(row.id, row.name)}
                            >
                              余额
                            </Button>
                            <Popconfirm
                              title="确认删除主账号及其绑定?"
                              onConfirm={async () => {
                                await request.delete(`/plugins/volcengine_ark_monitor/accounts/${row.id}`);
                                message.success('已删除主账号');
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
              label: `接入点绑定 (${bindings.length})`,
              children: (
                <>
                  <div style={{ marginBottom: 12 }}>
                    <Button type="primary" icon={<PlusOutlined />} onClick={() => setBindingModalOpen(true)}>
                      新增接入点绑定
                    </Button>
                  </div>
                  <Table rowKey="id" loading={loading} dataSource={bindings} columns={bindingColumns} pagination={{ pageSize: 20 }} />
                </>
              ),
            },
            {
              key: 'tasks',
              label: `方舟视频任务 (${tasks.length})`,
              children: (
                <>
                  <Space style={{ marginBottom: 12 }}>
                    <Input
                      placeholder="按接入点 ep-xxx 过滤"
                      value={endpointFilter}
                      onChange={(e) => setEndpointFilter(e.target.value)}
                      style={{ width: 240 }}
                      allowClear
                    />
                    <Button onClick={() => void loadData()}>查询</Button>
                  </Space>
                  <Table rowKey="id" loading={loading} dataSource={tasks} columns={taskColumns} pagination={{ pageSize: 20 }} />
                </>
              ),
            },
          ]}
        />
      </Card>

      <Modal
        title="新增火山方舟主账号"
        open={accountModalOpen}
        onOk={() => void handleCreateAccount()}
        onCancel={() => setAccountModalOpen(false)}
        destroyOnHidden
      >
        <Form form={accountForm} layout="vertical" initialValues={{ region: 'cn-beijing' }}>
          <Form.Item name="name" label="账号别名" rules={[{ required: true }]}>
            <Input placeholder="例如：主账号-华北" />
          </Form.Item>
          <Form.Item name="volc_account_id" label="火山官方 AccountID">
            <Input placeholder="可选，用于标识" />
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
                { value: 'cn-beijing', label: 'cn-beijing (国内)' },
                { value: 'ap-southeast-1', label: 'ap-southeast-1 (BytePlus 国际)' },
              ]}
            />
          </Form.Item>
          <Form.Item name="remark" label="备注">
            <Input />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title="新增 Endpoint 接入点绑定"
        open={bindingModalOpen}
        onOk={() => void handleCreateBinding()}
        onCancel={() => setBindingModalOpen(false)}
        destroyOnHidden
      >
        <Form form={bindingForm} layout="vertical" initialValues={{ org_id: 1 }}>
          <Form.Item name="account_id" label="所属主账号" rules={[{ required: true }]}>
            <Select options={accounts.map((a) => ({ value: a.id, label: `${a.name} (#${a.id})` }))} />
          </Form.Item>
          <Form.Item name="endpoint_id" label="接入点 Endpoint ID" rules={[{ required: true }]}>
            <Input placeholder="例如：ep-2026xxxx-xxxx" />
          </Form.Item>
          <Form.Item name="org_id" label="关联扣费组织 ID" rules={[{ required: true }]}>
            <InputNumber min={1} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="api_key" label="方舟静态 Bearer API Key" rules={[{ required: true }]}>
            <Input.Password placeholder="用于 ListVideos 拉取任务状态与 token 估算" />
          </Form.Item>
          <Form.Item name="remark" label="备注">
            <Input />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  );
};

export default ArkMonitorPage;
