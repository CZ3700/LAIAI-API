import React, { useEffect, useState } from 'react';
import {
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
  Tabs,
  Tag,
  Typography,
  message,
} from 'antd';
import { BellOutlined, CloudOutlined, DeleteOutlined, PlusOutlined, TeamOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { coreApi } from '../../utils/request';
import useAuthStore from '../../store/auth';

export const TeamByokPage: React.FC = () => {
  const { t } = useTranslation();
  const { user } = useAuthStore();
  const [loading, setLoading] = useState(true);
  const [orgs, setOrgs] = useState<any[]>([]);
  const [members, setMembers] = useState<any[]>([]);
  const [byoks, setByoks] = useState<any[]>([]);
  const [orgModalOpen, setOrgModalOpen] = useState(false);
  const [memberModalOpen, setMemberModalOpen] = useState(false);
  const [byokModalOpen, setByokModalOpen] = useState(false);
  const [orgForm] = Form.useForm();
  const [memberForm] = Form.useForm();
  const [byokForm] = Form.useForm();
  const [alertForm] = Form.useForm();

  const loadAll = async () => {
    setLoading(true);
    try {
      const [oRes, mRes, bRes, aRes] = await Promise.all([
        coreApi.get('/orgs').catch(() => ({ orgs: [] })),
        coreApi.get('/org/members').catch(() => ({ members: [] })),
        coreApi.get('/org/byok-channels').catch(() => ({ channels: [] })),
        coreApi.get('/org/balance-alert').catch(() => ({})),
      ]);
      setOrgs(Array.isArray((oRes as any)?.orgs) ? (oRes as any).orgs : Array.isArray(oRes) ? oRes : []);
      setMembers(Array.isArray((mRes as any)?.members) ? (mRes as any).members : Array.isArray(mRes) ? mRes : []);
      setByoks(Array.isArray((bRes as any)?.channels) ? (bRes as any).channels : Array.isArray(bRes) ? bRes : []);
      alertForm.setFieldsValue(aRes || {});
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void loadAll();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [user?.org_id]);

  const onCreateOrg = async () => {
    const vals = await orgForm.validateFields();
    await coreApi.post('/orgs', { name: vals.name.trim() });
    message.success(t('team.org_created', '组织已创建'));
    setOrgModalOpen(false);
    orgForm.resetFields();
    await loadAll();
  };

  const onAddMember = async () => {
    const vals = await memberForm.validateFields();
    await coreApi.post('/org/members', { email: vals.email.trim(), role: vals.role });
    message.success(t('team.member_invited', '成员邀请已发送/添加'));
    setMemberModalOpen(false);
    memberForm.resetFields();
    await loadAll();
  };

  const onCreateByok = async () => {
    const vals = await byokForm.validateFields();
    await coreApi.post('/org/byok-channels', {
      name: vals.name.trim(),
      provider: vals.provider,
      base_url: vals.base_url.trim(),
      api_key: vals.api_key.trim(),
      models: vals.models?.trim() || '*',
      enabled: true,
    });
    message.success(t('team.byok_created', 'BYOK 私有渠道已添加'));
    setByokModalOpen(false);
    byokForm.resetFields();
    await loadAll();
  };

  const onSaveAlert = async () => {
    const vals = await alertForm.validateFields();
    await coreApi.put('/org/balance-alert', vals);
    message.success(t('common.saved', '余额预警配置已保存'));
  };

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card
        title={
          <Space>
            <TeamOutlined />
            <span>{t('menu.team', '团队组织与 BYOK 自有渠道')}</span>
          </Space>
        }
      >
        <Tabs
          items={[
            {
              key: 'members',
              label: t('team.members', '团队成员与组织'),
              children: (
                <Space direction="vertical" size={16} style={{ width: '100%' }}>
                  <Space>
                    <Button type="primary" icon={<PlusOutlined />} onClick={() => setMemberModalOpen(true)}>
                      {t('team.invite_member', '邀请成员')}
                    </Button>
                    <Button icon={<PlusOutlined />} onClick={() => setOrgModalOpen(true)}>
                      {t('team.create_org', '新建组织')}
                    </Button>
                  </Space>
                  <Table
                    loading={loading}
                    rowKey="user_id"
                    dataSource={members}
                    columns={[
                      { title: 'User ID', dataIndex: 'user_id', width: 90 },
                      { title: t('auth.email', '邮箱'), dataIndex: 'email' },
                      { title: t('common.role', '角色'), dataIndex: 'role', render: (v: string) => <Tag>{v}</Tag> },
                      {
                        title: t('common.actions', '操作'),
                        width: 100,
                        render: (_: unknown, row: any) =>
                          row.role !== 'owner' ? (
                            <Popconfirm
                              title={t('common.confirm_delete', '确认移除该成员？')}
                              onConfirm={async () => {
                                await coreApi.delete(`/org/members/${row.user_id}`);
                                await loadAll();
                              }}
                            >
                              <Button size="small" danger icon={<DeleteOutlined />} />
                            </Popconfirm>
                          ) : null,
                      },
                    ]}
                  />
                  <Typography.Title level={5}>{t('team.my_orgs', '我的组织')}</Typography.Title>
                  <Space wrap>
                    {orgs.map((o) => (
                      <Tag key={o.id} color={o.id === user?.org_id ? 'indigo' : 'default'}>
                        {o.name} (ID: {o.id})
                      </Tag>
                    ))}
                  </Space>
                </Space>
              ),
            },
            {
              key: 'byok',
              label: (
                <span>
                  <CloudOutlined /> BYOK 私有渠道
                </span>
              ),
              children: (
                <Space direction="vertical" size={12} style={{ width: '100%' }}>
                  <Button type="primary" icon={<PlusOutlined />} onClick={() => setByokModalOpen(true)}>
                    {t('team.add_byok', '添加 BYOK 渠道')}
                  </Button>
                  <Table
                    loading={loading}
                    rowKey="id"
                    dataSource={byoks}
                    columns={[
                      { title: t('common.name', '名称'), dataIndex: 'name' },
                      { title: 'Provider', dataIndex: 'provider', render: (v: string) => <Tag>{v}</Tag> },
                      { title: 'Base URL', dataIndex: 'base_url', ellipsis: true },
                      { title: 'Models', dataIndex: 'models', ellipsis: true },
                      {
                        title: t('common.status', '启用'),
                        dataIndex: 'enabled',
                        render: (v: boolean) => <Tag color={v ? 'green' : 'default'}>{v ? 'ON' : 'OFF'}</Tag>,
                      },
                      {
                        title: t('common.actions', '操作'),
                        width: 90,
                        render: (_: unknown, row: any) => (
                          <Popconfirm
                            title={t('common.confirm_delete', '确认删除 BYOK 渠道？')}
                            onConfirm={async () => {
                              await coreApi.delete(`/org/byok-channels/${row.id}`);
                              await loadAll();
                            }}
                          >
                            <Button size="small" danger icon={<DeleteOutlined />} />
                          </Popconfirm>
                        ),
                      },
                    ]}
                  />
                </Space>
              ),
            },
            {
              key: 'alert',
              label: (
                <span>
                  <BellOutlined /> {t('team.balance_alert', '余额预警')}
                </span>
              ),
              children: (
                <Form form={alertForm} layout="vertical" style={{ maxWidth: 460 }}>
                  <Form.Item name="enabled" label={t('common.enabled', '开启余额预警')} valuePropName="checked">
                    <Switch />
                  </Form.Item>
                  <Form.Item name="threshold_usd" label={t('team.threshold_usd', '预警阈值 ($)')}>
                    <InputNumber min={0} step={1} style={{ width: '100%' }} />
                  </Form.Item>
                  <Form.Item name="webhook_url" label="Webhook URL">
                    <Input placeholder="https://hooks.example.com/..." />
                  </Form.Item>
                  <Button type="primary" onClick={onSaveAlert}>
                    {t('common.save', '保存配置')}
                  </Button>
                </Form>
              ),
            },
          ]}
        />
      </Card>

      <Modal title={t('team.create_org', '新建组织')} open={orgModalOpen} onCancel={() => setOrgModalOpen(false)} onOk={onCreateOrg}>
        <Form form={orgForm} layout="vertical">
          <Form.Item name="name" label={t('team.org_name', '组织名称')} rules={[{ required: true }]}>
            <Input />
          </Form.Item>
        </Form>
      </Modal>

      <Modal title={t('team.invite_member', '邀请成员')} open={memberModalOpen} onCancel={() => setMemberModalOpen(false)} onOk={onAddMember}>
        <Form form={memberForm} layout="vertical" initialValues={{ role: 'member' }}>
          <Form.Item name="email" label={t('auth.email', '成员邮箱')} rules={[{ required: true, type: 'email' }]}>
            <Input />
          </Form.Item>
          <Form.Item name="role" label={t('common.role', '角色')}>
            <Select options={[{ value: 'member', label: 'Member' }, { value: 'admin', label: 'Org Admin' }]} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal title={t('team.add_byok', '添加 BYOK 渠道')} open={byokModalOpen} onCancel={() => setByokModalOpen(false)} onOk={onCreateByok}>
        <Form form={byokForm} layout="vertical" initialValues={{ provider: 'openai' }}>
          <Form.Item name="name" label={t('common.name', '渠道名称')} rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="provider" label="Provider" rules={[{ required: true }]}>
            <Select
              options={[
                { value: 'openai', label: 'OpenAI' },
                { value: 'anthropic', label: 'Anthropic' },
                { value: 'gemini', label: 'Google Gemini' },
                { value: 'volcengine', label: 'Volcengine / Seedance' },
              ]}
            />
          </Form.Item>
          <Form.Item name="base_url" label="Base URL" rules={[{ required: true }]}>
            <Input placeholder="https://api.openai.com/v1" />
          </Form.Item>
          <Form.Item name="api_key" label="API Key" rules={[{ required: true }]}>
            <Input.Password />
          </Form.Item>
          <Form.Item name="models" label={t('team.models', '支持模型 (逗号分隔)')}>
            <Input placeholder="gpt-4o,gpt-4o-mini" />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  );
};

export default TeamByokPage;
