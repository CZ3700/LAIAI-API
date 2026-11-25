import React, { useEffect, useState } from 'react';
import { Button, Card, Form, Input, InputNumber, Modal, Popconfirm, Space, Switch, Table, Tag, message } from 'antd';
import { DeleteOutlined, EditOutlined, FolderOutlined, PlusOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';

export const ChannelCategoriesPage: React.FC = () => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [rows, setRows] = useState<any[]>([]);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<any | null>(null);
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try {
      const res: any = await request.get('/channel-categories');
      setRows(Array.isArray(res?.categories) ? res.categories : []);
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { void load(); }, []);

  const openEdit = (row: any | null) => {
    setEditing(row);
    form.resetFields();
    form.setFieldsValue(row ? { ...row, is_active: row.is_active === 1 } : { sort_order: 0, is_active: true });
    setOpen(true);
  };

  const onSave = async () => {
    const v = await form.validateFields();
    const body = { name: v.name.trim(), description: v.description || '', sort_order: v.sort_order ?? 0, is_active: v.is_active ? 1 : 0 };
    if (editing) await request.put(`/channel-categories/${editing.id}`, body);
    else await request.post('/channel-categories', body);
    message.success(t('common.saved', '已保存'));
    setOpen(false);
    await load();
  };

  return (
    <Card title={<Space><FolderOutlined />{t('admin.channel_categories', '渠道分组')}</Space>}
      extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => openEdit(null)}>{t('common.add', '新建')}</Button>}>
      <Table loading={loading} rowKey="id" dataSource={rows} pagination={false}
        columns={[
          { title: 'ID', dataIndex: 'id', width: 70 },
          { title: t('common.name', '名称'), dataIndex: 'name' },
          { title: t('common.remark', '描述'), dataIndex: 'description', ellipsis: true },
          { title: t('common.sort', '排序'), dataIndex: 'sort_order', width: 80 },
          { title: t('common.status', '状态'), dataIndex: 'is_active', width: 90,
            render: (v: number) => <Tag color={v === 1 ? 'green' : 'default'}>{v === 1 ? 'ON' : 'OFF'}</Tag> },
          { title: t('common.actions', '操作'), width: 120, render: (_: unknown, r: any) => (
            <Space size={4}>
              <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(r)} />
              <Popconfirm title={t('common.confirm_delete', '确认删除？')} onConfirm={async () => { await request.delete(`/channel-categories/${r.id}`); await load(); }}>
                <Button size="small" danger icon={<DeleteOutlined />} />
              </Popconfirm>
            </Space>
          ) },
        ]} />
      <Modal title={editing ? '编辑分组' : '新建分组'} open={open} onCancel={() => setOpen(false)} onOk={onSave}>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label={t('common.name', '名称')} rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="description" label={t('common.remark', '描述')}><Input.TextArea rows={2} /></Form.Item>
          <Form.Item name="sort_order" label={t('common.sort', '排序')}><InputNumber /></Form.Item>
          <Form.Item name="is_active" label={t('common.enabled', '启用')} valuePropName="checked"><Switch /></Form.Item>
        </Form>
      </Modal>
    </Card>
  );
};

export default ChannelCategoriesPage;
