import React, { useEffect, useState } from 'react';
import {
  Button, Card, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Table, Tag, Tooltip, Typography, message,
} from 'antd';
import {
  CloudServerOutlined, DeleteOutlined, EditOutlined, KeyOutlined, PlayCircleOutlined, PlusOutlined, StopOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request, { coreApi } from '../../utils/request';

interface Channel {
  id: number;
  name: string;
  provider: string;
  base_url: string;
  models: string;
  model_count: number;
  weight: number;
  priority: number;
  status: number;
  calls: number;
  success_rate: number;
  avg_latency_ms: number;
  key_count: number;
  category_id: number;
  upstream_cost_ratio?: string | number;
  model_mapping?: Record<string, string> | string;
}

export const ChannelsPage: React.FC = () => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [rows, setRows] = useState<Channel[]>([]);
  const [providers, setProviders] = useState<any[]>([]);
  const [categories, setCategories] = useState<any[]>([]);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<Channel | null>(null);
  const [saving, setSaving] = useState(false);
  const [credChannel, setCredChannel] = useState<Channel | null>(null);
  const [creds, setCreds] = useState<any[]>([]);
  const [credForm] = Form.useForm();
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try {
      const [c, p, cat]: any[] = await Promise.all([
        coreApi.get('/admin/channels'),
        coreApi.get('/admin/providers').catch(() => ({ providers: [] })),
        request.get('/channel-categories').catch(() => ({ categories: [] })),
      ]);
      setRows(Array.isArray(c?.channels) ? c.channels : []);
      setProviders(Array.isArray(p?.providers) ? p.providers : []);
      setCategories(Array.isArray(cat?.categories) ? cat.categories : []);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const providerName = (p: any) => (typeof p === 'string' ? p : p?.name || p?.id || p?.provider);

  const openEdit = (row: Channel | null) => {
    setEditing(row);
    form.resetFields();
    if (row) {
      form.setFieldsValue({
        name: row.name,
        provider: row.provider,
        base_url: row.base_url,
        models: row.models,
        weight: row.weight,
        priority: row.priority,
        category_id: row.category_id || 0,
        upstream_cost_ratio: Number(row.upstream_cost_ratio ?? 1),
      });
    } else {
      form.setFieldsValue({ weight: 1, priority: 0, category_id: 0, upstream_cost_ratio: 1 });
    }
    setOpen(true);
  };

  const onSave = async () => {
    const v = await form.validateFields();
    const body: Record<string, unknown> = {
      name: v.name.trim(),
      provider: v.provider,
      base_url: (v.base_url || '').trim(),
      models: String(v.models || '').split(',').map((s: string) => s.trim()).filter(Boolean),
      weight: v.weight ?? 1,
      priority: v.priority ?? 0,
      category_id: v.category_id || 0,
      upstream_cost_ratio: String(v.upstream_cost_ratio ?? 1),
    };
    if (v.api_key) body.api_key = v.api_key;
    setSaving(true);
    try {
      if (editing) {
        await coreApi.put(`/admin/channels/${editing.id}`, body);
      } else {
        await coreApi.post('/admin/channels', body);
      }
      message.success(t('common.saved', '已保存'));
      setOpen(false);
      await load();
    } finally {
      setSaving(false);
    }
  };

  const act = async (fn: () => Promise<unknown>, ok: string) => {
    await fn();
    message.success(ok);
    await load();
  };

  const loadCreds = async (ch: Channel) => {
    setCredChannel(ch);
    const res: any = await coreApi.get(`/admin/channels/${ch.id}/credentials`);
    setCreds(Array.isArray(res?.credentials) ? res.credentials : Array.isArray(res?.keys) ? res.keys : []);
  };

  return (
    <Card
      title={<Space><CloudServerOutlined />{t('admin.channels', '渠道管理')}</Space>}
      extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => openEdit(null)}>{t('admin.add_channel', '新建渠道')}</Button>}
    >
      <Table
        loading={loading}
        rowKey="id"
        dataSource={rows}
        scroll={{ x: 1100 }}
        columns={[
          { title: 'ID', dataIndex: 'id', width: 60 },
          { title: t('common.name', '名称'), dataIndex: 'name' },
          { title: 'Provider', dataIndex: 'provider', render: (v: string) => <Tag>{v}</Tag> },
          { title: 'Base URL', dataIndex: 'base_url', ellipsis: true },
          { title: t('admin.models', '模型数'), dataIndex: 'model_count', width: 80,
            render: (v: number, r) => <Tooltip title={r.models}><span>{v}</span></Tooltip> },
          { title: 'W / P', width: 80, render: (_: unknown, r) => `${r.weight} / ${r.priority}` },
          { title: t('admin.cost_ratio', '进货倍率'), dataIndex: 'upstream_cost_ratio', width: 90, render: (v: any) => Number(v ?? 1) },
          { title: t('admin.success_rate', '成功率'), dataIndex: 'success_rate', width: 90, render: (v: number) => `${(v * 100).toFixed(1)}%` },
          { title: t('common.status', '状态'), dataIndex: 'status', width: 80,
            render: (v: number) => <Tag color={v === 1 ? 'green' : 'default'}>{v === 1 ? 'ON' : 'OFF'}</Tag> },
          {
            title: t('common.actions', '操作'), width: 260, fixed: 'right',
            render: (_: unknown, r: Channel) => (
              <Space size={4}>
                <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(r)} />
                <Button size="small" icon={<KeyOutlined />} onClick={() => loadCreds(r)} />
                <Button size="small" icon={<PlayCircleOutlined />}
                  onClick={() => act(() => coreApi.post(`/admin/channels/${r.id}/test`), t('admin.test_ok', '测试请求已发送'))} />
                {r.status === 1 ? (
                  <Button size="small" icon={<StopOutlined />}
                    onClick={() => act(() => coreApi.post(`/admin/channels/${r.id}/disable`), t('common.updated', '已禁用'))} />
                ) : (
                  <Button size="small" type="primary"
                    onClick={() => act(() => coreApi.post(`/admin/channels/${r.id}/enable`), t('common.updated', '已启用'))}>ON</Button>
                )}
                <Popconfirm title={t('common.confirm_delete', '确认删除？')}
                  onConfirm={() => act(() => coreApi.delete(`/admin/channels/${r.id}`), t('common.deleted', '已删除'))}>
                  <Button size="small" danger icon={<DeleteOutlined />} />
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />

      <Modal title={editing ? t('admin.edit_channel', '编辑渠道') : t('admin.add_channel', '新建渠道')}
        open={open} onCancel={() => setOpen(false)} onOk={onSave} confirmLoading={saving} width={620}>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label={t('common.name', '名称')} rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="provider" label="Provider" rules={[{ required: true }]}>
            <Select showSearch options={providers.map((p) => ({ value: providerName(p), label: providerName(p) }))} />
          </Form.Item>
          <Form.Item name="base_url" label="Base URL"><Input placeholder="留空使用厂商默认端点" /></Form.Item>
          <Form.Item name="api_key" label="API Key" rules={editing ? [] : [{ required: true }]}>
            <Input.Password placeholder={editing ? '留空保持不变（凭证永不回显）' : ''} />
          </Form.Item>
          <Form.Item name="models" label={t('admin.models_csv', '模型列表（逗号分隔）')} rules={[{ required: true }]}><Input.TextArea rows={2} /></Form.Item>
          <Space size={12} wrap>
            <Form.Item name="weight" label={t('admin.weight', '权重')}><InputNumber min={0} /></Form.Item>
            <Form.Item name="priority" label={t('admin.priority', '优先级')}><InputNumber /></Form.Item>
            <Form.Item name="upstream_cost_ratio" label={t('admin.cost_ratio', '进货成本倍率')}><InputNumber min={0} step={0.01} /></Form.Item>
            <Form.Item name="category_id" label={t('admin.channel_categories', '渠道分组')}>
              <Select style={{ width: 160 }} options={[{ value: 0, label: '未分组' }, ...categories.map((c: any) => ({ value: c.id, label: c.name }))]} />
            </Form.Item>
          </Space>
        </Form>
      </Modal>

      <Modal title={`${t('admin.credentials', '渠道凭证')} - ${credChannel?.name || ''}`} open={Boolean(credChannel)}
        onCancel={() => setCredChannel(null)} footer={null} width={640}>
        <Space direction="vertical" style={{ width: '100%' }}>
          <Form form={credForm} layout="inline" onFinish={async (v) => {
            if (!credChannel) return;
            await coreApi.post(`/admin/channels/${credChannel.id}/credentials`, { api_key: v.api_key, weight: v.weight || 1 });
            credForm.resetFields();
            await loadCreds(credChannel);
          }}>
            <Form.Item name="api_key" rules={[{ required: true }]}><Input.Password placeholder="新增 API Key" style={{ width: 280 }} /></Form.Item>
            <Form.Item name="weight"><InputNumber min={1} placeholder="权重" /></Form.Item>
            <Button type="primary" htmlType="submit">{t('common.add', '添加')}</Button>
          </Form>
          <Table size="small" rowKey="id" pagination={false} dataSource={creds}
            columns={[
              { title: 'ID', dataIndex: 'id', width: 60 },
              { title: 'Hint', dataIndex: 'key_hint', render: (v: string) => <Typography.Text code>{v || '••••'}</Typography.Text> },
              { title: 'Status', dataIndex: 'status', render: (v: number) => <Tag color={v === 1 ? 'green' : 'default'}>{v === 1 ? 'active' : 'off'}</Tag> },
              { title: '', width: 160, render: (_: unknown, c: any) => credChannel && (
                <Space size={4}>
                  <Button size="small" onClick={async () => {
                    await coreApi.post(`/admin/channels/${credChannel.id}/credentials/${c.id}/${c.status === 1 ? 'disable' : 'enable'}`);
                    await loadCreds(credChannel);
                  }}>{c.status === 1 ? '禁用' : '启用'}</Button>
                  <Button size="small" danger icon={<DeleteOutlined />} onClick={async () => {
                    await coreApi.delete(`/admin/channels/${credChannel.id}/credentials/${c.id}`);
                    await loadCreds(credChannel);
                  }} />
                </Space>
              ) },
            ]} />
        </Space>
      </Modal>
    </Card>
  );
};

export default ChannelsPage;
