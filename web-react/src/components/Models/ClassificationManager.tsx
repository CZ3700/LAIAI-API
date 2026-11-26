import React, { useState, useEffect } from 'react';
import { Modal, Table, Button, Space, Form, Input, InputNumber, Switch, message, Popconfirm, Tag, Select } from 'antd';
import { PlusOutlined, EditOutlined, DeleteOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import { type ModelProvider, type ModelType } from '../../types';
import IconPicker from '../IconPicker';
import SmartSvgIcon from '../SmartSvgIcon';
import { Image as ImageIcon, Video, AudioLines, MessageSquare, Cuboid, ListOrdered, Sparkles } from 'lucide-react';

interface ClassificationItem {
  id: number;
  name: string;
  name_en?: string;
  sort_order: number;
  is_active: number | boolean;
  is_system?: number;
  logo?: string;
  default_features?: string;
  remark?: string;
  model_count?: number;
}

interface ClassificationManagerProps {
  type: 'provider' | 'api_provider' | 'type';
  visible: boolean;
  onClose: () => void;
  onUpdate: () => void;
}

const ClassificationManager: React.FC<ClassificationManagerProps> = ({
  type,
  visible,
  onClose,
  onUpdate,
}) => {
  const { t, i18n } = useTranslation();
  const isEn = i18n.language === 'en';
  const [items, setItems] = useState<ClassificationItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [updatingIds, setUpdatingIds] = useState<Set<number>>(new Set());
  const [isEditModalVisible, setIsEditModalVisible] = useState(false);
  const [editingItem, setEditingItem] = useState<ClassificationItem | null>(null);
  const [form] = Form.useForm();

  const apiPath = type === 'provider' ? '/model-providers' : (type === 'api_provider' ? '/model-api-providers' : '/model-types');

  const fetchItems = async () => {
    setLoading(true);
    try {
      const resp = await (request.get(apiPath) as any);
      setItems(resp);
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
  }, [visible]);

  const handleToggleActive = async (record: ClassificationItem, checked: boolean) => {
    setUpdatingIds(prev => new Set(prev).add(record.id));
    try {
      const payload = {
        name: record.name,
        name_en: record.name_en || '',
        sort_order: record.sort_order ?? 0,
        is_active: checked ? 1 : 0,
        logo: record.logo || '',
        default_features: record.default_features,
        remark: record.remark,
      };
      await request.put(`${apiPath}/${record.id}`, payload);
      setItems(prev => prev.map(item => (item.id === record.id ? { ...item, is_active: checked } : item)));
      message.success(checked ? t('common.active', '已启用') : t('common.disabled', '已禁用'));
      onUpdate();
    } catch (e: any) {
      console.error(e);
      message.error(t('common.action_failed', '操作失败') + (e?.message ? `: ${e.message}` : ''));
    } finally {
      setUpdatingIds(prev => {
        const next = new Set(prev);
        next.delete(record.id);
        return next;
      });
    }
  };

  const handleAdd = () => {
    setEditingItem(null);
    form.resetFields();
    setIsEditModalVisible(true);
  };

  const handleEdit = (item: ClassificationItem) => {
    setEditingItem(item);
    let parsedFeatures: string[] = [];
    if (item.default_features) {
      try {
        const parsed = JSON.parse(item.default_features);
        if (Array.isArray(parsed)) parsedFeatures = parsed.filter((v) => typeof v === 'string');
      } catch {
        parsedFeatures = [];
      }
    }
    form.setFieldsValue({
      ...item,
      logo: item.logo || undefined,
      default_features_list: parsedFeatures,
    });
    setIsEditModalVisible(true);
  };

  const handleDelete = async (id: number) => {
    try {
      await request.delete(`${apiPath}/${id}`);
      message.success(t('common.success'));
      fetchItems();
      onUpdate();
    } catch (e) {
      console.error(e);
    }
  };

  const handleSave = async (values: any) => {
    try {
      const { default_features_list, ...rest } = values;
      const payload: Record<string, any> = {
        ...rest,
        is_active: values.is_active ? 1 : 0,
      };
      if (type === 'type') {
        if (editingItem?.is_system === 1 && editingItem.default_features) {
          payload.default_features = editingItem.default_features;
        } else if (Array.isArray(default_features_list)) {
          payload.default_features = JSON.stringify(default_features_list);
        }
      }
      if (editingItem) {
        await request.put(`${apiPath}/${editingItem.id}`, payload);
      } else {
        await request.post(apiPath, payload);
      }
      message.success(t('common.success'));
      setIsEditModalVisible(false);
      fetchItems();
      onUpdate();
    } catch (e) {
      console.error(e);
    }
  };

  const renderSystemIcon = (name: string) => {
    const lowerName = name.toLowerCase();
    if (lowerName.includes('封装') || lowerName.includes('encapsulation')) return <Sparkles size={18} />;
    if (lowerName.includes('图片') || lowerName.includes('image')) return <ImageIcon size={18} />;
    if (lowerName.includes('视频') || lowerName.includes('video')) return <Video size={18} />;
    if (lowerName.includes('音频') || lowerName.includes('audio')) return <AudioLines size={18} />;
    if (lowerName.includes('聊天') || lowerName.includes('chat') || lowerName.includes('text')) return <MessageSquare size={18} />;
    if (lowerName.includes('embedding') || lowerName.includes('向量')) return <Cuboid size={18} />;
    if (lowerName.includes('rerank') || lowerName.includes('排序')) return <ListOrdered size={18} />;
    return null;
  };

  const columns = [
    {
      title: 'Logo',
      dataIndex: 'logo',
      key: 'logo',
      width: 50,
      render: (logo: string, record: ClassificationItem) => {
        const sysIcon = renderSystemIcon(record.name_en || record.name);
        if (sysIcon) {
          return <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', width: 22, height: 22, opacity: 0.85 }}>{sysIcon}</div>;
        }
        return logo ? (
          <SmartSvgIcon src={`/assets/icons/lobe/${logo}.svg`} alt="" style={{ width: 22, height: 22, objectFit: 'contain' }} onError={e => { (e.target as HTMLImageElement).style.display = 'none'; }} />
        ) : <span style={{ color: 'rgba(255,255,255,0.15)' }}>—</span>;
      },
    },
    {
      title: t('common.name'),
      dataIndex: 'name',
      key: 'name',
      render: (_: any, record: ClassificationItem) => isEn && record.name_en ? record.name_en : record.name,
    },
    {
      title: t('models.model_count', '模型数量'),
      dataIndex: 'model_count',
      key: 'model_count',
      width: 100,
      render: (count: number) => (
        <Tag color={(count && count > 0) ? 'blue' : 'default'} style={{ borderRadius: 10 }}>
          {count ?? 0}
        </Tag>
      ),
    },
    {
      title: t('models.sort_order'),
      dataIndex: 'sort_order',
      key: 'sort_order',
      width: 100,
    },
    {
      title: t('common.status'),
      dataIndex: 'is_active',
      key: 'is_active',
      render: (active: boolean, record: ClassificationItem) => (
        <Switch 
          checked={Boolean(active)} 
          loading={updatingIds.has(record.id)}
          onChange={(checked) => handleToggleActive(record, checked)}
          size="small"
        />
      ),
      width: 100,
    },
    {
      title: t('common.actions'),
      key: 'actions',
      render: (_: any, record: ClassificationItem) => {
        return (
          <Space>
            <Button icon={<EditOutlined />} onClick={() => handleEdit(record)} size="small" />
            <Popconfirm 
              title={t('common.confirm_delete')} 
              onConfirm={() => handleDelete(record.id)}
              disabled={record.is_system === 1}
            >
              <Button icon={<DeleteOutlined />} danger size="small" disabled={record.is_system === 1} />
            </Popconfirm>
          </Space>
        );
      },
      width: 120,
    },
  ];

  return (
    <>
      <Modal
        title={type === 'provider' ? t('models.manage_providers') : (type === 'api_provider' ? t('models.manage_api_providers', '管理API服务商') : t('models.manage_types'))}
        open={visible}
        onCancel={onClose}
        footer={[
          <Button key="close" onClick={onClose}>{t('common.close')}</Button>
        ]}
        width={680}
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
            <Input placeholder="例如：OpenAI" disabled={editingItem?.is_system === 1} />
          </Form.Item>
          <Form.Item name="name_en" label={t('common.name_en', '英文名称')}>
            <Input placeholder="例如：OpenAI" />
          </Form.Item>
          <Form.Item name="logo" label="Logo 图标">
            <IconPicker
              value={form.getFieldValue('logo')}
              onChange={(icon) => form.setFieldsValue({ logo: icon?.name || undefined })}
              placeholder={`选择${type === 'provider' ? '服务商' : '类型'}图标`}
            />
          </Form.Item>
          <Form.Item name="sort_order" label={t('models.sort_order')}>
            <InputNumber style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="is_active" label={t('common.status')} valuePropName="checked">
            <Switch />
          </Form.Item>
          {type === 'type' && (() => {
            let presetTags: string[] = [];
            if (editingItem?.default_features) {
              try {
                const parsed = JSON.parse(editingItem.default_features);
                if (Array.isArray(parsed)) presetTags = parsed.filter((v) => typeof v === 'string');
              } catch {
                presetTags = [];
              }
            }
            if (editingItem?.is_system === 1 && presetTags.length > 0) {
              return (
                <Form.Item label="二级功能默认属性">
                  <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
                    {presetTags.map((label) => (
                      <Tag key={label} style={{ margin: 0, padding: '4px 12px', fontSize: 13, lineHeight: '22px' }}>
                        {label}
                      </Tag>
                    ))}
                  </div>
                  <div style={{ marginTop: 8, color: 'var(--text-secondary)', fontSize: 12 }}>
                    系统预制，不可修改。模型编辑时可从中勾选或自定义补充。
                  </div>
                </Form.Item>
              );
            }
            return (
              <Form.Item name="default_features_list" label="二级功能默认属性">
                <Select
                  mode="tags"
                  tokenSeparators={[',']}
                  placeholder="输入二级功能属性标签后回车添加，如：文生图"
                  style={{ width: '100%' }}
                />
              </Form.Item>
            );
          })()}
        </Form>
      </Modal>
    </>
  );
};

export default ClassificationManager;
