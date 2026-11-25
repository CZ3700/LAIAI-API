import React, { useEffect, useState } from 'react';
import {
  Alert, Button, Card, Descriptions, Form, Input, InputNumber, Modal, Popconfirm, Segmented, Select, Space, Switch, Table, Tag, Typography, message,
} from 'antd';
import {
  AuditOutlined, CreditCardOutlined, DeleteOutlined, FundOutlined, NotificationOutlined, PlusOutlined, SaveOutlined, SettingOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request, { coreApi } from '../../utils/request';
import { formatApiDateTime } from '../../utils/timedisplay';

function flatten(obj: any, prefix = ''): [string, string][] {
  const out: [string, string][] = [];
  if (obj == null || typeof obj !== 'object') return out;
  for (const [k, v] of Object.entries(obj)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (v && typeof v === 'object' && !Array.isArray(v)) out.push(...flatten(v, key));
    else out.push([key, Array.isArray(v) ? `[${v.length} items]` : String(v)]);
  }
  return out;
}

export const RechargesAdminPage: React.FC = () => {
  const { t } = useTranslation();
  const [days, setDays] = useState(14);
  const [data, setData] = useState<any>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    coreApi.get('/admin/analytics', { params: { days } })
      .then((r: any) => setData(r))
      .finally(() => setLoading(false));
  }, [days]);

  return (
    <Card loading={loading} title={<Space><FundOutlined />{t('admin.recharges', '财务分析')}</Space>}
      extra={<Segmented value={days} onChange={(v) => setDays(Number(v))} options={[{ label: '7D', value: 7 }, { label: '14D', value: 14 }, { label: '30D', value: 30 }]} />}>
      <Descriptions bordered size="small" column={2}>
        {flatten(data).map(([k, v]) => (<Descriptions.Item key={k} label={k}>{v}</Descriptions.Item>))}
      </Descriptions>
    </Card>
  );
};

export const PaymentChannelsPage: React.FC = () => {
  const { t } = useTranslation();
  const [rows, setRows] = useState<any[]>([]);
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm();

  const load = async () => {
    const r: any = await coreApi.get('/admin/payment-channels');
    setRows(Array.isArray(r?.channels) ? r.channels : []);
  };
  useEffect(() => { void load(); }, []);

  const onSave = async () => {
    const v = await form.validateFields();
    let config: Record<string, string> | undefined;
    if (v.config_json?.trim()) {
      try { config = JSON.parse(v.config_json); } catch { message.error('config 必须是 JSON 对象（字符串值）'); return; }
    }
    await coreApi.post('/admin/payment-channels', {
      code: v.code.trim(), display_name: v.display_name, protocol: v.protocol, priority: v.priority ?? 0,
      pay_methods: v.pay_methods || '', currency: v.currency || '', enabled: Boolean(v.enabled), ...(config ? { config } : {}),
    });
    message.success(t('common.saved', '已保存'));
    setOpen(false);
    await load();
  };

  return (
    <Card title={<Space><CreditCardOutlined />{t('admin.payment_channels', '支付渠道')}</Space>}
      extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => { form.resetFields(); form.setFieldsValue({ enabled: false, priority: 0, protocol: 'epay' }); setOpen(true); }}>{t('common.add', '新建')}</Button>}>
      <Alert type="info" showIcon style={{ marginBottom: 12 }} message="新建渠道默认禁用；测试连通后再启用。密钥只写不回显。微信/支付宝直连按需后续补充。" />
      <Table rowKey="id" dataSource={rows} pagination={false}
        columns={[
          { title: 'Code', dataIndex: 'code' },
          { title: '名称', dataIndex: 'display_name' },
          { title: '协议', dataIndex: 'protocol', render: (v: string) => <Tag>{v}</Tag> },
          { title: '优先级', dataIndex: 'priority', width: 80 },
          { title: '状态', dataIndex: 'enabled', width: 80, render: (v: any) => <Tag color={v ? 'green' : 'default'}>{v ? 'ON' : 'OFF'}</Tag> },
          { title: t('common.actions', '操作'), width: 190, render: (_: unknown, r: any) => (
            <Space size={4}>
              <Button size="small" onClick={async () => { await coreApi.post(`/admin/payment-channels/${r.id}/test`); message.success('测试通过'); }}>测试</Button>
              <Button size="small" onClick={() => {
                form.setFieldsValue({ code: r.code, display_name: r.display_name, protocol: r.protocol, priority: r.priority, pay_methods: r.pay_methods, currency: r.currency, enabled: Boolean(r.enabled), config_json: '' });
                setOpen(true);
              }}>编辑</Button>
              <Popconfirm title="确认删除？" onConfirm={async () => { await coreApi.delete(`/admin/payment-channels/${r.id}`); await load(); }}>
                <Button size="small" danger icon={<DeleteOutlined />} />
              </Popconfirm>
            </Space>
          ) },
        ]} />
      <Modal title="支付渠道" open={open} onCancel={() => setOpen(false)} onOk={onSave}>
        <Form form={form} layout="vertical">
          <Form.Item name="code" label="Code（唯一，协议不可改）" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="display_name" label="显示名称"><Input /></Form.Item>
          <Form.Item name="protocol" label="协议" rules={[{ required: true }]}>
            <Select options={['stripe', 'epay', 'crypto', 'webhook', 'custom', 'paypal'].map((p) => ({ value: p, label: p }))} />
          </Form.Item>
          <Space size={12}>
            <Form.Item name="priority" label="优先级"><InputNumber /></Form.Item>
            <Form.Item name="currency" label="币种"><Input style={{ width: 100 }} /></Form.Item>
            <Form.Item name="pay_methods" label="支付方式"><Input placeholder="alipay,wxpay" /></Form.Item>
            <Form.Item name="enabled" label="启用" valuePropName="checked"><Switch /></Form.Item>
          </Space>
          <Form.Item name="config_json" label="配置 JSON（留空保持原配置）"><Input.TextArea rows={4} placeholder='{"api_key":"..."}' /></Form.Item>
        </Form>
      </Modal>
    </Card>
  );
};

export const AnnouncementsAdminPage: React.FC = () => {
  const { t } = useTranslation();
  const [rows, setRows] = useState<any[]>([]);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<any | null>(null);
  const [form] = Form.useForm();

  const load = async () => {
    const r: any = await request.get('/announcements');
    setRows(Array.isArray(r?.data) ? r.data : []);
  };
  useEffect(() => { void load(); }, []);

  const onSave = async () => {
    const v = await form.validateFields();
    const body = { title: v.title.trim(), content: v.content, is_pinned: v.is_pinned ? 1 : 0, is_active: v.is_active ? 1 : 0, is_popup: v.is_popup ? 1 : 0 };
    if (editing) await request.put(`/announcements/${editing.id}`, body);
    else await request.post('/announcements', body);
    setOpen(false);
    await load();
  };

  return (
    <Card title={<Space><NotificationOutlined />{t('admin.announcements', '系统公告')}</Space>}
      extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => { setEditing(null); form.resetFields(); form.setFieldsValue({ is_active: true }); setOpen(true); }}>{t('common.add', '新建')}</Button>}>
      <Table rowKey="id" dataSource={rows} pagination={false}
        columns={[
          { title: 'ID', dataIndex: 'id', width: 60 },
          { title: '标题', dataIndex: 'title' },
          { title: '置顶', dataIndex: 'is_pinned', width: 70, render: (v: number) => (v ? <Tag color="indigo">PIN</Tag> : '-') },
          { title: '状态', dataIndex: 'is_active', width: 80, render: (v: number) => <Tag color={v ? 'green' : 'default'}>{v ? 'ON' : 'OFF'}</Tag> },
          { title: '更新时间', dataIndex: 'updated_at', width: 170, render: (v: string) => formatApiDateTime(v) },
          { title: t('common.actions', '操作'), width: 130, render: (_: unknown, r: any) => (
            <Space size={4}>
              <Button size="small" onClick={() => { setEditing(r); form.setFieldsValue({ ...r, is_pinned: !!r.is_pinned, is_active: !!r.is_active, is_popup: !!r.is_popup }); setOpen(true); }}>编辑</Button>
              <Popconfirm title="确认删除？" onConfirm={async () => { await request.delete(`/announcements/${r.id}`); await load(); }}>
                <Button size="small" danger icon={<DeleteOutlined />} />
              </Popconfirm>
            </Space>
          ) },
        ]} />
      <Modal title="公告" open={open} onCancel={() => setOpen(false)} onOk={onSave}>
        <Form form={form} layout="vertical">
          <Form.Item name="title" label="标题" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="content" label="内容" rules={[{ required: true }]}><Input.TextArea rows={5} /></Form.Item>
          <Space size={24}>
            <Form.Item name="is_pinned" label="置顶" valuePropName="checked"><Switch /></Form.Item>
            <Form.Item name="is_popup" label="弹窗" valuePropName="checked"><Switch /></Form.Item>
            <Form.Item name="is_active" label="启用" valuePropName="checked"><Switch /></Form.Item>
          </Space>
        </Form>
      </Modal>
    </Card>
  );
};

export const AuditLogsPage: React.FC = () => {
  const { t } = useTranslation();
  const [rows, setRows] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    coreApi.get('/admin/audit-logs')
      .then((r: any) => setRows(Array.isArray(r?.items) ? r.items : Array.isArray(r?.logs) ? r.logs : Array.isArray(r) ? r : []))
      .finally(() => setLoading(false));
  }, []);

  return (
    <Card title={<Space><AuditOutlined />{t('admin.audit_logs', '审计日志')}</Space>}>
      <Table loading={loading} rowKey="id" dataSource={rows} scroll={{ x: 900 }}
        columns={[
          { title: '时间', dataIndex: 'created_at', width: 170, render: (v: number | string) => (typeof v === 'number' ? formatApiDateTime(new Date(v * (v < 1e12 ? 1000 : 1)).toISOString()) : formatApiDateTime(v)) },
          { title: 'Actor', dataIndex: 'actor', width: 120 },
          { title: 'Action', dataIndex: 'action', render: (v: string) => <Tag>{v}</Tag> },
          { title: 'Target', dataIndex: 'target', ellipsis: true },
          { title: 'Detail', dataIndex: 'detail', ellipsis: true },
          { title: 'IP', dataIndex: 'ip', width: 130 },
        ]} />
    </Card>
  );
};

export const SystemSettingsPage: React.FC = () => {
  const { t } = useTranslation();
  const [settings, setSettings] = useState<Record<string, string>>({});
  const [dirty, setDirty] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    coreApi.get('/admin/settings').then((r: any) => setSettings(r?.settings || {})).finally(() => setLoading(false));
  }, []);

  const onSave = async () => {
    if (Object.keys(dirty).length === 0) return;
    setSaving(true);
    try {
      await coreApi.put('/admin/settings', dirty);
      setSettings((s) => ({ ...s, ...dirty }));
      setDirty({});
      message.success(t('common.saved', '设置已保存'));
    } finally {
      setSaving(false);
    }
  };

  const keys = Object.keys(settings).sort();
  return (
    <Card loading={loading} title={<Space><SettingOutlined />{t('admin.settings', '系统设置')}</Space>}
      extra={<Button type="primary" icon={<SaveOutlined />} loading={saving} disabled={!Object.keys(dirty).length} onClick={onSave}>保存 ({Object.keys(dirty).length})</Button>}>
      <Space direction="vertical" style={{ width: '100%' }} size={8}>
        {keys.map((k) => (
          <div key={k} style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
            <Typography.Text code style={{ width: 280, flexShrink: 0 }}>{k}</Typography.Text>
            <Input value={dirty[k] ?? settings[k]} onChange={(e) => setDirty((d) => ({ ...d, [k]: e.target.value }))} />
          </div>
        ))}
      </Space>
    </Card>
  );
};
