import React, { useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  message,
} from 'antd';
import { CopyOutlined, DeleteOutlined, EditOutlined, KeyOutlined, PlusOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import { copyToClipboard } from '../../utils/clipboard';
import { formatApiDateTime } from '../../utils/timedisplay';

interface TokenItem {
  id: number;
  name: string;
  key_prefix: string;
  masked_key: string;
  is_active: number;
  status: number;
  scopes: string;
  quota_limit: string | number;
  quota_limit_f: number;
  used_amount: string | number;
  allowed_models: string[];
  allowed_ips: string;
  rpm_limit: number;
  route_policy: string;
  expires_at?: string | null;
}

export const ApiKeysPage: React.FC = () => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [items, setItems] = useState<TokenItem[]>([]);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<TokenItem | null>(null);
  const [saving, setSaving] = useState(false);
  const [createdKey, setCreatedKey] = useState<string | null>(null);
  const [form] = Form.useForm();

  const loadTokens = async () => {
    setLoading(true);
    try {
      const res: any = await request.get('/tokens');
      setItems(Array.isArray(res?.data) ? res.data : []);
    } catch {
      // handled
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void loadTokens();
  }, []);

  const openCreate = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({
      route_policy: 'order',
      rpm_limit: 0,
      quota_limit: 0,
      expires_in_days: 0,
      enable_video_async: false,
    });
    setModalOpen(true);
  };

  const openEdit = (row: TokenItem) => {
    setEditing(row);
    form.resetFields();
    form.setFieldsValue({
      name: row.name,
      quota_limit: Number(row.quota_limit || 0),
      rpm_limit: row.rpm_limit || 0,
      route_policy: row.route_policy || 'order',
      allowed_models_text: (row.allowed_models || []).join(','),
      allowed_ips: row.allowed_ips || '',
    });
    setModalOpen(true);
  };

  const onSave = async () => {
    const vals = await form.validateFields();
    const models = String(vals.allowed_models_text || '')
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);

    setSaving(true);
    try {
      if (editing) {
        await request.put(`/tokens/${editing.id}`, {
          name: vals.name?.trim(),
          quota_limit: vals.quota_limit ?? 0,
          rpm_limit: vals.rpm_limit ?? 0,
          route_policy: vals.route_policy || 'order',
          allowed_models: models,
          allowed_ips: String(vals.allowed_ips || '').trim(),
        });
        message.success(t('common.saved', '已保存'));
        setModalOpen(false);
      } else {
        const res: any = await request.post('/tokens', {
          name: vals.name?.trim(),
          quota_limit: vals.quota_limit ?? 0,
          rpm_limit: vals.rpm_limit ?? 0,
          route_policy: vals.route_policy || 'order',
          allowed_models: models,
          allowed_ips: String(vals.allowed_ips || '').trim(),
          expires_in_days: vals.expires_in_days || 0,
          enable_video_async: Boolean(vals.enable_video_async),
        });
        setModalOpen(false);
        if (res?.key) {
          setCreatedKey(res.key);
        }
      }
      await loadTokens();
    } catch {
      // handled
    } finally {
      setSaving(false);
    }
  };

  const onToggleActive = async (row: TokenItem, active: boolean) => {
    try {
      await request.put(`/tokens/${row.id}`, { is_active: active ? 1 : 0 });
      message.success(t('common.updated', '状态已更新'));
      await loadTokens();
    } catch {
      // handled
    }
  };

  const onDelete = async (row: TokenItem) => {
    try {
      await request.delete(`/tokens/${row.id}`);
      message.success(row.status !== 0 ? t('apikeys.revoked', '已吊销令牌') : t('apikeys.deleted', '已删除令牌'));
      await loadTokens();
    } catch {
      // handled
    }
  };

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Alert
        type="info"
        showIcon
        message={t(
          'apikeys.security_notice',
          '安全策略：API Key 仅在创建成功时展示一次明文，平台仅持久化不可逆哈希，不支持二次查看。请妥善保存。',
        )}
      />

      <Card
        title={
          <Space>
            <KeyOutlined />
            <span>{t('menu.apikeys', 'API Key 令牌管理')}</span>
          </Space>
        }
        extra={
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            {t('apikeys.create', '创建令牌')}
          </Button>
        }
      >
        <Table
          loading={loading}
          rowKey="id"
          dataSource={items}
          scroll={{ x: 960 }}
          columns={[
            {
              title: t('common.name', '名称'),
              dataIndex: 'name',
              render: (v: string) => <Typography.Text strong>{v || '-'}</Typography.Text>,
            },
            {
              title: t('apikeys.masked', '令牌前缀'),
              dataIndex: 'masked_key',
              render: (v: string) => <Typography.Text code>{v}</Typography.Text>,
            },
            {
              title: t('common.status', '状态'),
              dataIndex: 'status',
              width: 100,
              render: (_: unknown, row: TokenItem) => (
                <Switch
                  checked={row.status === 1}
                  onChange={(checked) => onToggleActive(row, checked)}
                />
              ),
            },
            {
              title: t('apikeys.quota', '已用 / 限额 ($)'),
              render: (_: unknown, row: TokenItem) => {
                const used = Number(row.used_amount || 0).toFixed(4);
                const lim = Number(row.quota_limit || 0);
                return lim > 0 ? `$${used} / $${lim.toFixed(2)}` : `$${used} / ${t('common.unlimited', '无限')}`;
              },
            },
            {
              title: t('apikeys.route_policy', '路由策略'),
              dataIndex: 'route_policy',
              width: 110,
              render: (v: string) => <Tag>{v || 'order'}</Tag>,
            },
            {
              title: t('apikeys.rpm', 'RPM 限流'),
              dataIndex: 'rpm_limit',
              width: 100,
              render: (v: number) => (v > 0 ? v : t('common.unlimited', '不限')),
            },
            {
              title: t('apikeys.expires_at', '过期时间'),
              dataIndex: 'expires_at',
              render: (v: string | null) => (v ? formatApiDateTime(v) : t('apikeys.never_expires', '永不过期')),
            },
            {
              title: t('common.actions', '操作'),
              width: 150,
              fixed: 'right',
              render: (_: unknown, row: TokenItem) => (
                <Space size={4}>
                  <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(row)} />
                  <Popconfirm
                    title={row.status !== 0 ? t('apikeys.confirm_revoke', '确认吊销该令牌？') : t('apikeys.confirm_delete', '确认彻底删除已吊销令牌？')}
                    onConfirm={() => onDelete(row)}
                  >
                    <Button size="small" danger icon={<DeleteOutlined />} />
                  </Popconfirm>
                </Space>
              ),
            },
          ]}
        />
      </Card>

      <Modal
        title={editing ? t('apikeys.edit', '编辑令牌') : t('apikeys.create', '创建令牌')}
        open={modalOpen}
        onCancel={() => setModalOpen(false)}
        onOk={onSave}
        confirmLoading={saving}
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label={t('common.name', '名称')} rules={[{ required: true }]}>
            <Input placeholder="Production Key" />
          </Form.Item>
          <Form.Item name="quota_limit" label={t('apikeys.quota_usd', '额度上限 ($，0 表示不限)')}>
            <InputNumber min={0} step={1} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="rpm_limit" label={t('apikeys.rpm', '每分钟请求限制 (RPM，0 表示不限)')}>
            <InputNumber min={0} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="route_policy" label={t('apikeys.route_policy', '渠道选择策略')}>
            <Select
              options={[
                { value: 'order', label: t('apikeys.route_order', '优先级顺序 (order)') },
                { value: 'latency', label: t('apikeys.route_latency', '最低首字延迟 (latency)') },
                { value: 'throughput', label: t('apikeys.route_throughput', '最高吞吐量 (throughput)') },
                { value: 'price', label: t('apikeys.route_price', '最低成本 (price)') },
              ]}
            />
          </Form.Item>
          <Form.Item name="allowed_models_text" label={t('apikeys.allowed_models', '模型白名单 (逗号分隔，留空不限)')}>
            <Input placeholder="gpt-4o,claude-sonnet-4-6" />
          </Form.Item>
          <Form.Item name="allowed_ips" label={t('apikeys.allowed_ips', 'IP 白名单 (逗号分隔 CIDR/IP，留空不限)')}>
            <Input placeholder="10.0.0.0/8,1.2.3.4" />
          </Form.Item>
          {!editing && (
            <>
              <Form.Item name="expires_in_days" label={t('apikeys.expires_in_days', '有效天数 (0 为永不过期)')}>
                <InputNumber min={0} style={{ width: '100%' }} />
              </Form.Item>
              <Form.Item name="enable_video_async" valuePropName="checked" label={t('apikeys.video_async', '授予视频异步生成权限 (video:write)')}>
                <Switch />
              </Form.Item>
            </>
          )}
        </Form>
      </Modal>

      <Modal
        title={t('apikeys.created_title', '请立即复制并保存您的 API Key')}
        open={Boolean(createdKey)}
        onCancel={() => setCreatedKey(null)}
        footer={[
          <Button
            key="copy"
            type="primary"
            icon={<CopyOutlined />}
            onClick={async () => {
              if (createdKey) {
                await copyToClipboard(createdKey);
                message.success(t('common.copied', '已复制到剪贴板'));
              }
            }}
          >
            {t('common.copy', '复制明文令牌')}
          </Button>,
          <Button key="close" onClick={() => setCreatedKey(null)}>
            {t('common.close', '我已保存')}
          </Button>,
        ]}
      >
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Alert
            type="warning"
            showIcon
            message={t('apikeys.once_warning', '关闭此弹窗后将无法再次查看完整明文！')}
          />
          <Typography.Paragraph copyable code style={{ padding: 12, borderRadius: 8, wordBreak: 'break-all' }}>
            {createdKey}
          </Typography.Paragraph>
        </Space>
      </Modal>
    </Space>
  );
};

export default ApiKeysPage;
