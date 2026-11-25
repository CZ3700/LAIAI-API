import React, { useState, useEffect } from 'react';
import { Modal, Table, Button, Space, Form, Input, InputNumber, Switch, message, Popconfirm, Tag } from 'antd';
import { PlusOutlined, EditOutlined, DeleteOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import type { ChannelCategory } from '../../types';

interface ChannelCategoryManagerProps {
  visible: boolean;
  onClose: () => void;
  onUpdate: () => void;
  mode?: 'category' | 'provider';
}

const ChannelCategoryManager: React.FC<ChannelCategoryManagerProps> = ({
  visible,
  onClose,
  onUpdate,
  mode = 'category',
}) => {
  const { t, i18n } = useTranslation();
  const isEn = i18n.language === 'en';
  const isProvider = mode === 'provider';
  const apiBase = isProvider ? '/channel-configs/providers' : '/channel-categories';
  const [items, setItems] = useState<ChannelCategory[]>([]);
  const [loading, setLoading] = useState(false);
  const [isEditModalVisible, setIsEditModalVisible] = useState(false);
  const [editingItem, setEditingItem] = useState<ChannelCategory | null>(null);
  const [form] = Form.useForm();

  const fetchItems = async () => {
    setLoading(true);
    try {
      const resp = await (request.get(apiBase) as any);
      setItems(Array.isArray(resp) ? resp : (resp?.data || []));
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (visible) {
      fetchItems();
    }
  }, [visible, apiBase]);

  const handleAdd = () => {
    setEditingItem(null);
    form.resetFields();
    setIsEditModalVisible(true);
  };

  const handleEdit = (item: ChannelCategory) => {
    setEditingItem(item);
    form.setFieldsValue({
      ...item,
      is_active: !!item.is_active,
    });
    setIsEditModalVisible(true);
  };

  const handleDelete = async (id: number) => {
    try {
      await request.delete(`${apiBase}/${id}`);
      message.success(t('common.success'));
      await fetchItems();
      onUpdate();
    } catch (e) {
      console.error(e);
    }
  };

  const handleSave = async (values: any) => {
    try {
      const payload = {
        ...values,
        is_active: values.is_active ? 1 : 0,
      };
      if (editingItem) {
        await request.put(`${apiBase}/${editingItem.id}`, payload);
      } else {
        await request.post(apiBase, payload);
      }
      message.success(t('common.success'));
      setIsEditModalVisible(false);
      await fetchItems();
      onUpdate();
    } catch (e) {
      console.error(e);
    }
  };

  const columns = [
    {
      title: t('common.name'),
      dataIndex: 'name',
      key: 'name',
      sorter: (a: ChannelCategory, b: ChannelCategory) => (a.name || '').localeCompare(b.name || '', 'zh'),
      render: (_: any, record: ChannelCategory) => (
        <Space>
          <span>{isEn && record.name_en ? record.name_en : record.name}</span>
          {record.is_system === 1 && <Tag color="blue">系统</Tag>}
        </Space>
      ),
    },
    {
      title: t('models.sort_order', '排序'),
      dataIndex: 'sort_order',
      key: 'sort_order',
      width: 100,
      sorter: (a: ChannelCategory, b: ChannelCategory) => (a.sort_order || 0) - (b.sort_order || 0),
    },
    {
      title: t('common.status'),
      dataIndex: 'is_active',
      key: 'is_active',
      width: 100,
      sorter: (a: ChannelCategory, b: ChannelCategory) => (Number(a.is_active || 0)) - (Number(b.is_active || 0)),
      render: (active: number | boolean) => (
        <Switch checked={!!active} disabled />
      ),
    },
    {
      title: t('common.actions'),
      key: 'actions',
      render: (_: any, record: ChannelCategory) => (
        <Space>
          <Button icon={<EditOutlined />} onClick={() => handleEdit(record)} size="small" />
          <Popconfirm
            title={t('common.confirm_delete')}
            onConfirm={() => handleDelete(record.id)}
            disabled={!isProvider && record.is_system === 1}
          >
            <Button icon={<DeleteOutlined />} danger size="small" disabled={!isProvider && record.is_system === 1} />
          </Popconfirm>
        </Space>
      ),
      width: 120,
    },
  ];

  return (
    <>
      <Modal
        title={isProvider ? '管理服务商' : '管理渠道分类'}
        open={visible}
        onCancel={onClose}
        footer={[
          <Button key="close" onClick={onClose}>{t('common.close')}</Button>
        ]}
        width={600}
      >
        <div style={{ marginBottom: 16 }}>
          <Button type="primary" icon={<PlusOutlined />} onClick={handleAdd}>
            {t('common.add')}
          </Button>
        </div>
        <Table
          dataSource={items}
          columns={columns}
          rowKey="id"
          loading={loading}
          pagination={false}
          size="small"
        />
      </Modal>

      <Modal
        title={editingItem ? t('common.edit') : t('common.add')}
        open={isEditModalVisible}
        onCancel={() => setIsEditModalVisible(false)}
        onOk={() => form.submit()}
      >
        <Form form={form} layout="vertical" onFinish={handleSave} initialValues={{ sort_order: 0, is_active: true }}>
          <Form.Item name="name" label={t('common.name')} rules={[{ required: true }]}>
            <Input placeholder={isProvider ? '例如：openai / custom' : '例如：图片'} disabled={!isProvider && editingItem?.is_system === 1} />
          </Form.Item>
          <Form.Item name="name_en" label={t('common.name_en', '英文名称')}>
            <Input placeholder={isProvider ? '例如：OpenAI' : '例如：Image'} />
          </Form.Item>
          <Form.Item name="sort_order" label={t('models.sort_order', '排序')}>
            <InputNumber style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="is_active" label={t('common.status')} valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
};

export default ChannelCategoryManager;
