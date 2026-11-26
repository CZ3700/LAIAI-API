import React, { useEffect, useState } from 'react';
import { Button, Card, Form, Input, InputNumber, Modal, Radio, Space, Table, Tabs, Tag, message } from 'antd';
import { DollarOutlined, TeamOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { coreApi } from '../../utils/request';

export const UsersOrgsPage: React.FC = () => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [users, setUsers] = useState<any[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [kw, setKw] = useState('');
  const [orgs, setOrgs] = useState<any[]>([]);
  const [creditOrg, setCreditOrg] = useState<{ id: number; label: string } | null>(null);
  const [form] = Form.useForm();

  const loadUsers = async (p = page, q = kw) => {
    setLoading(true);
    try {
      const res: any = await coreApi.get('/admin/users', { params: { page: p, ...(q ? { q } : {}) } });
      setUsers(Array.isArray(res?.items) ? res.items : []);
      setTotal(Number(res?.total || 0));
    } finally {
      setLoading(false);
    }
  };
  const loadOrgs = async () => {
    const res: any = await coreApi.get('/admin/orgs');
    setOrgs(Array.isArray(res?.orgs) ? res.orgs : []);
  };

  useEffect(() => {
    void loadUsers(1, '');
    void loadOrgs();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const onCredit = async () => {
    if (!creditOrg) return;
    const v = await form.validateFields();
    await coreApi.post('/admin/credits', {
      org_id: creditOrg.id,
      mode: v.mode,
      amount_usd: String(v.amount_usd),
      reason: v.reason.trim(),
      idempotency_key: `ui-${creditOrg.id}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
    });
    message.success(t('common.saved', '调账成功'));
    setCreditOrg(null);
    form.resetFields();
    await Promise.all([loadUsers(), loadOrgs()]);
  };

  return (
    <Card title={<Space><TeamOutlined />{t('admin.users', '用户与组织')}</Space>}>
      <Tabs items={[
        {
          key: 'users', label: t('admin.users', '用户'),
          children: (
            <Space direction="vertical" style={{ width: '100%' }}>
              <Input.Search allowClear placeholder={t('admin.search_user_placeholder', '邮箱 / 昵称')} style={{ width: 260 }} value={kw}
                onChange={(e) => setKw(e.target.value)} onSearch={(v) => { setPage(1); void loadUsers(1, v); }} />
              <Table loading={loading} rowKey="id" dataSource={users} scroll={{ x: 900 }}
                pagination={{ current: page, pageSize: 30, total, showSizeChanger: false, onChange: (p) => { setPage(p); void loadUsers(p); } }}
                columns={[
                  { title: 'ID', dataIndex: 'id', width: 70 },
                  { title: t('auth.email', '邮箱'), dataIndex: 'email' },
                  { title: t('common.role', '角色'), dataIndex: 'role', width: 100, render: (v: string) => <Tag>{v}</Tag> },
                  { title: t('wallet.balance', '余额 ($)'), dataIndex: 'balance', width: 110, render: (v: any) => Number(v || 0).toFixed(4) },
                  { title: t('common.status', '状态'), dataIndex: 'status', width: 90, render: (v: number) => <Tag color={v === 1 ? 'green' : 'red'}>{v === 1 ? t('common.active', 'active') : t('admin.banned', 'banned')}</Tag> },
                  { title: t('common.actions', '操作'), width: 200, fixed: 'right', render: (_: unknown, r: any) => (
                    <Space size={4}>
                      <Button size="small" icon={<DollarOutlined />} disabled={!r.org_id}
                        onClick={() => setCreditOrg({ id: r.org_id, label: r.email })}>{t('admin.adjust', '调账')}</Button>
                      <Button size="small" danger={r.status === 1} onClick={async () => {
                        await coreApi.post(`/admin/users/${r.id}/manage`, { action: r.status === 1 ? 'ban' : 'unban' });
                        await loadUsers();
                      }}>{r.status === 1 ? t('admin.ban', '封禁') : t('admin.unban', '解封')}</Button>
                    </Space>
                  ) },
                ]} />
            </Space>
          ),
        },
        {
          key: 'orgs', label: t('admin.orgs', '组织'),
          children: (
            <Table rowKey="id" dataSource={orgs} scroll={{ x: 900 }}
              columns={[
                { title: 'ID', dataIndex: 'id', width: 70 },
                { title: t('common.name', '名称'), dataIndex: 'name' },
                { title: t('admin.owner', 'Owner'), dataIndex: 'owner_email' },
                { title: t('admin.group', 'Group'), dataIndex: 'group', width: 100, render: (v: string) => <Tag>{v}</Tag> },
                { title: t('wallet.balance', '余额 ($)'), dataIndex: 'balance', width: 110, render: (v: any) => Number(v || 0).toFixed(4) },
                { title: t('admin.spend', '累计消耗 ($)'), dataIndex: 'spend', width: 120, render: (v: any) => Number(v || 0).toFixed(4) },
                { title: t('admin.calls', '调用数'), dataIndex: 'calls', width: 90 },
                { title: t('common.actions', '操作'), width: 100, render: (_: unknown, r: any) => (
                  <Button size="small" icon={<DollarOutlined />} onClick={() => setCreditOrg({ id: r.id, label: r.name })}>{t('admin.adjust', '调账')}</Button>
                ) },
              ]} />
          ),
        },
      ]} />
      <Modal title={`${t('admin.adjust', '余额调账')} - ${creditOrg?.label || ''}`} open={Boolean(creditOrg)}
        onCancel={() => setCreditOrg(null)} onOk={onCredit}>
        <Form form={form} layout="vertical" initialValues={{ mode: 'add' }}>
          <Form.Item name="mode" label={t('admin.adjust_mode', '模式')}>
            <Radio.Group options={[
              { value: 'add', label: t('admin.adjust_add', '授信 (+)') },
              { value: 'subtract', label: t('admin.adjust_subtract', '扣减 (-)') },
              { value: 'override', label: t('admin.adjust_override', '订正为目标值') },
            ]} />
          </Form.Item>
          <Form.Item name="amount_usd" label={t('admin.amount_usd', '金额 ($)')} rules={[{ required: true }]}><InputNumber min={0} step={0.01} style={{ width: '100%' }} /></Form.Item>
          <Form.Item name="reason" label={t('admin.adjust_reason', '原因（写入审计）')} rules={[{ required: true }]}><Input /></Form.Item>
        </Form>
      </Modal>
    </Card>
  );
};

export default UsersOrgsPage;
