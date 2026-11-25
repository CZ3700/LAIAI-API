import React, { useEffect, useState } from 'react';
import {
  Button, Card, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tag, message,
} from 'antd';
import { ApiOutlined, DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import useSettingsStore from '../../store/settings';

export const ModelsAdminPage: React.FC = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const adminPath = useSettingsStore((s) => s.settings?.site?.admin_path) || 'admin';
  const [loading, setLoading] = useState(true);
  const [rows, setRows] = useState<any[]>([]);
  const [billing, setBilling] = useState<any[]>([]);
  const [forwards, setForwards] = useState<any[]>([]);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<any | null>(null);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try {
      const [m, b, f]: any[] = await Promise.all([
        request.get('/models'),
        request.get('/billing-rules').catch(() => []),
        request.get('/forward-rules').catch(() => []),
      ]);
      setRows(Array.isArray(m?.data) ? m.data : []);
      setBilling(Array.isArray(b) ? b : []);
      setForwards(Array.isArray(f) ? f : []);
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { void load(); }, []);

  const openEdit = (row: any | null) => {
    setEditing(row);
    form.resetFields();
    form.setFieldsValue(row ? {
      model: row.model, name: row.name, vendor: row.vendor, category: row.category, description: row.description,
      billing_rule_id: row.billing_rule_id || 0, forward_rule_ids: row.forward_rule_ids || [],
      is_listed: row.is_listed, is_active: row.is_active, sort_order: row.sort_order,
      input_usd_per_1m: Number(row.input_usd_per_1m || 0), output_usd_per_1m: Number(row.output_usd_per_1m || 0),
      per_call_usd: Number(row.per_call_usd || 0),
    } : { is_listed: true, is_active: true, billing_rule_id: 0, forward_rule_ids: [], sort_order: 0 });
    setOpen(true);
  };

  const onSave = async () => {
    const v = await form.validateFields();
    const body = {
      ...v,
      model: v.model.trim(),
      input_usd_per_1m: v.input_usd_per_1m != null ? String(v.input_usd_per_1m) : undefined,
      output_usd_per_1m: v.output_usd_per_1m != null ? String(v.output_usd_per_1m) : undefined,
      per_call_usd: v.per_call_usd != null ? String(v.per_call_usd) : undefined,
    };
    setSaving(true);
    try {
      if (editing) await request.put(`/models/${encodeURIComponent(editing.model)}`, body);
      else await request.post('/models', body);
      message.success(t('common.saved', '已保存'));
      setOpen(false);
      await load();
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card
      title={<Space><ApiOutlined />{t('admin.models', '模型管理')}</Space>}
      extra={
        <Space>
          <Button onClick={() => navigate(`/${adminPath}/billing-rules`)}>{t('admin.billing_rules', '计价规则')}</Button>
          <Button onClick={() => navigate(`/${adminPath}/forward-rules`)}>{t('admin.forward_rules', '转发规则')}</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => openEdit(null)}>{t('common.add', '新建模型')}</Button>
        </Space>
      }
    >
      <Table loading={loading} rowKey="model" dataSource={rows} scroll={{ x: 1000 }}
        columns={[
          { title: t('common.model', '模型'), dataIndex: 'model', render: (v: string) => <Tag>{v}</Tag> },
          { title: t('admin.vendor', '厂商'), dataIndex: 'vendor', width: 110 },
          { title: t('admin.billing_rules', '计价规则'), dataIndex: 'billing_rule_name', render: (v: string, r: any) => v ? `${v} (${r.billing_rule_pid})` : '-' },
          { title: t('admin.forward_rules', '转发规则'), dataIndex: 'forward_rule_names', render: (v: string[]) => (v || []).map((n) => <Tag key={n} color="purple">{n}</Tag>) },
          { title: 'In/Out $/1M', width: 130, render: (_: unknown, r: any) => `${Number(r.input_usd_per_1m || 0)} / ${Number(r.output_usd_per_1m || 0)}` },
          { title: t('common.status', '上架'), width: 90, render: (_: unknown, r: any) => <Tag color={r.is_listed && r.is_active ? 'green' : 'default'}>{r.is_listed && r.is_active ? 'ON' : 'OFF'}</Tag> },
          { title: t('common.actions', '操作'), width: 110, fixed: 'right', render: (_: unknown, r: any) => (
            <Space size={4}>
              <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(r)} />
              <Popconfirm title={t('common.confirm_delete', '确认删除？')} onConfirm={async () => { await request.delete(`/models/${encodeURIComponent(r.model)}`); await load(); }}>
                <Button size="small" danger icon={<DeleteOutlined />} />
              </Popconfirm>
            </Space>
          ) },
        ]} />
      <Modal title={editing ? '编辑模型' : '新建模型'} open={open} onCancel={() => setOpen(false)} onOk={onSave} confirmLoading={saving} width={640}>
        <Form form={form} layout="vertical">
          <Form.Item name="model" label="Model ID" rules={[{ required: true }]}><Input disabled={Boolean(editing)} /></Form.Item>
          <Space size={12} wrap>
            <Form.Item name="name" label="展示名"><Input /></Form.Item>
            <Form.Item name="vendor" label="厂商"><Input /></Form.Item>
            <Form.Item name="category" label="类型"><Input placeholder="chat / image / video" /></Form.Item>
          </Space>
          <Form.Item name="description" label="描述"><Input.TextArea rows={2} /></Form.Item>
          <Form.Item name="billing_rule_id" label="计价规则">
            <Select options={[{ value: 0, label: '不绑定（使用基础价）' }, ...billing.map((b: any) => ({ value: b.id, label: `${b.name} (${b.pid})` }))]} />
          </Form.Item>
          <Form.Item name="forward_rule_ids" label="转发规则（可多选）">
            <Select mode="multiple" options={forwards.map((f: any) => ({ value: f.id, label: `${f.name} (${f.eid})` }))} />
          </Form.Item>
          <Space size={12} wrap>
            <Form.Item name="input_usd_per_1m" label="输入 $/1M"><InputNumber min={0} step={0.01} /></Form.Item>
            <Form.Item name="output_usd_per_1m" label="输出 $/1M"><InputNumber min={0} step={0.01} /></Form.Item>
            <Form.Item name="per_call_usd" label="按次 $"><InputNumber min={0} step={0.001} /></Form.Item>
            <Form.Item name="sort_order" label="排序"><InputNumber /></Form.Item>
          </Space>
          <Space size={24}>
            <Form.Item name="is_listed" label="模型广场展示" valuePropName="checked"><Switch /></Form.Item>
            <Form.Item name="is_active" label="启用" valuePropName="checked"><Switch /></Form.Item>
          </Space>
        </Form>
      </Modal>
    </Card>
  );
};

export default ModelsAdminPage;
