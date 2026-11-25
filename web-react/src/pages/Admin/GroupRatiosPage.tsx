import React, { useEffect, useState } from 'react';
import { Button, Card, Form, Input, InputNumber, Popconfirm, Space, Table, Tabs } from 'antd';
import { DeleteOutlined, DollarOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { coreApi } from '../../utils/request';

export const GroupRatiosPage: React.FC = () => {
  const { t } = useTranslation();
  const [groups, setGroups] = useState<any[]>([]);
  const [mgr, setMgr] = useState<any[]>([]);
  const [gForm] = Form.useForm();
  const [mForm] = Form.useForm();

  const load = async () => {
    const [g, m]: any[] = await Promise.all([coreApi.get('/admin/group-ratios'), coreApi.get('/admin/model-group-ratios')]);
    setGroups(Array.isArray(g?.groups) ? g.groups : []);
    setMgr(Array.isArray(m?.items) ? m.items : []);
  };
  useEffect(() => { void load(); }, []);

  return (
    <Card title={<Space><DollarOutlined />{t('admin.group_ratios', '分组倍率')}</Space>}>
      <Tabs items={[
        {
          key: 'g', label: '分组倍率',
          children: (
            <Space direction="vertical" style={{ width: '100%' }}>
              <Form form={gForm} layout="inline" onFinish={async (v) => {
                await coreApi.post('/admin/group-ratios', { name: v.name.trim(), ratio: String(v.ratio) });
                gForm.resetFields();
                await load();
              }}>
                <Form.Item name="name" rules={[{ required: true }]}><Input placeholder="分组名" /></Form.Item>
                <Form.Item name="ratio" rules={[{ required: true }]}><InputNumber min={0} step={0.01} placeholder="倍率" /></Form.Item>
                <Button type="primary" htmlType="submit">保存 / 更新</Button>
              </Form>
              <Table rowKey="name" size="small" pagination={false} dataSource={groups}
                columns={[
                  { title: '分组', dataIndex: 'name' },
                  { title: '倍率', dataIndex: 'ratio', render: (v: any) => Number(v) },
                  { title: '', width: 80, render: (_: unknown, r: any) => r.name !== 'default' && (
                    <Popconfirm title="确认删除？" onConfirm={async () => { await coreApi.delete(`/admin/group-ratios/${encodeURIComponent(r.name)}`); await load(); }}>
                      <Button size="small" danger icon={<DeleteOutlined />} />
                    </Popconfirm>
                  ) },
                ]} />
            </Space>
          ),
        },
        {
          key: 'm', label: '(分组, 模型) 二层倍率',
          children: (
            <Space direction="vertical" style={{ width: '100%' }}>
              <Form form={mForm} layout="inline" onFinish={async (v) => {
                await coreApi.post('/admin/model-group-ratios', { group_name: v.group_name.trim(), model: v.model.trim(), ratio: String(v.ratio) });
                mForm.resetFields();
                await load();
              }}>
                <Form.Item name="group_name" rules={[{ required: true }]}><Input placeholder="分组名" /></Form.Item>
                <Form.Item name="model" rules={[{ required: true }]}><Input placeholder="模型" /></Form.Item>
                <Form.Item name="ratio" rules={[{ required: true }]}><InputNumber min={0} step={0.01} placeholder="倍率" /></Form.Item>
                <Button type="primary" htmlType="submit">保存 / 更新</Button>
              </Form>
              <Table rowKey={(r: any) => `${r.group_name}/${r.model}`} size="small" pagination={false} dataSource={mgr}
                columns={[
                  { title: '分组', dataIndex: 'group_name' },
                  { title: '模型', dataIndex: 'model' },
                  { title: '倍率', dataIndex: 'ratio', render: (v: any) => Number(v) },
                  { title: '', width: 80, render: (_: unknown, r: any) => (
                    <Popconfirm title="确认删除？" onConfirm={async () => {
                      await coreApi.delete('/admin/model-group-ratios', { params: { group_name: r.group_name, model: r.model } });
                      await load();
                    }}>
                      <Button size="small" danger icon={<DeleteOutlined />} />
                    </Popconfirm>
                  ) },
                ]} />
            </Space>
          ),
        },
      ]} />
    </Card>
  );
};

export default GroupRatiosPage;
