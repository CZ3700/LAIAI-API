import React, { useEffect, useState } from 'react';
import {
  Button,
  Card,
  Col,
  Form,
  InputNumber,
  Modal,
  Row,
  Select,
  Space,
  Statistic,
  Table,
  Tabs,
  Tag,
  Typography,
  message,
} from 'antd';
import { DollarOutlined, PlusOutlined, WalletOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request, { coreApi } from '../../utils/request';
import { formatApiDateTime } from '../../utils/timedisplay';

export const WalletPage: React.FC = () => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [wallet, setWallet] = useState<any>({});
  const [recharges, setRecharges] = useState<any[]>([]);
  const [ledgerRows, setLedgerRows] = useState<any[]>([]);
  const [methods, setMethods] = useState<any[]>([]);
  const [topupOpen, setTopupOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [form] = Form.useForm();

  const loadData = async () => {
    setLoading(true);
    try {
      const [wRes, rRes, lRes, mRes] = await Promise.all([
        request.get('/user/wallet'),
        request.get('/user/recharge_records'),
        coreApi.get('/ledger').catch(() => ({ entries: [] })),
        coreApi.get('/payment/methods').catch(() => ({ methods: [] })),
      ]);
      setWallet(wRes || {});
      setRecharges(Array.isArray(rRes) ? rRes : []);
      const lList = Array.isArray((lRes as any)?.entries) ? (lRes as any).entries : Array.isArray(lRes) ? lRes : [];
      setLedgerRows(lList);
      const mList = Array.isArray((mRes as any)?.methods) ? (mRes as any).methods : Array.isArray(mRes) ? mRes : [];
      setMethods(mList);
    } catch {
      // handled
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void loadData();
  }, []);

  const handleTopup = async () => {
    const vals = await form.validateFields();
    setCreating(true);
    try {
      const res: any = await coreApi.post('/payment/create', {
        amount_usd: vals.amount_usd,
        provider: vals.provider,
      });
      setTopupOpen(false);
      if (res?.pay_url) {
        window.open(res.pay_url, '_blank', 'noopener,noreferrer');
      }
      message.success(t('wallet.order_created', '充值订单已创建'));
      await loadData();
    } catch {
      // handled
    } finally {
      setCreating(false);
    }
  };

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Row gutter={[16, 16]}>
        <Col xs={24} sm={12} lg={6}>
          <Card loading={loading}>
            <Statistic title={t('wallet.balance', '可用余额 ($)')} value={Number(wallet.balance || 0)} precision={4} prefix={<DollarOutlined />} />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card loading={loading}>
            <Statistic title={t('wallet.held', '任务预扣冻结 ($)')} value={Number(wallet.held || 0)} precision={4} />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card loading={loading}>
            <Statistic title={t('wallet.total_recharged', '累计充值 ($)')} value={Number(wallet.total_recharged || 0)} precision={2} />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card loading={loading}>
            <Statistic title={t('wallet.total_consumed', '累计消费 ($)')} value={Number(wallet.total_consumed || 0)} precision={4} />
          </Card>
        </Col>
      </Row>

      <Card
        title={
          <Space>
            <WalletOutlined />
            <span>{t('menu.wallet', '钱包与账务明细')}</span>
          </Space>
        }
        extra={
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setTopupOpen(true)}>
            {t('wallet.topup', '在线充值')}
          </Button>
        }
      >
        <Tabs
          items={[
            {
              key: 'recharges',
              label: t('wallet.recharge_records', '充值订单'),
              children: (
                <Table
                  loading={loading}
                  rowKey="id"
                  dataSource={recharges}
                  columns={[
                    { title: t('common.time', '创建时间'), dataIndex: 'created_at', render: (v: string) => formatApiDateTime(v) },
                    { title: 'Order No', dataIndex: 'order_no', render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
                    { title: t('wallet.provider', '支付渠道'), dataIndex: 'provider' },
                    {
                      title: t('wallet.amount', { unit: '$', defaultValue: '金额 ($)' }),
                      dataIndex: 'amount_usd',
                      render: (v: string | number) => `$${Number(v || 0).toFixed(2)}`,
                    },
                    {
                      title: t('common.status', '状态'),
                      dataIndex: 'status',
                      render: (v: string) => <Tag color={v === 'paid' ? 'green' : 'default'}>{v}</Tag>,
                    },
                  ]}
                />
              ),
            },
            {
              key: 'ledger',
              label: t('wallet.ledger', '资金流水 (Ledger)'),
              children: (
                <Table
                  loading={loading}
                  rowKey="id"
                  dataSource={ledgerRows}
                  columns={[
                    { title: t('common.time', '时间'), dataIndex: 'created_at', render: (v: string) => formatApiDateTime(v) },
                    { title: t('common.type', '类型'), dataIndex: 'entry_type', render: (v: string) => <Tag>{v}</Tag> },
                    {
                      title: t('wallet.delta', '变动 ($)'),
                      dataIndex: 'delta',
                      render: (v: string | number) => Number(v || 0).toFixed(6),
                    },
                    {
                      title: t('wallet.balance_after', '结余 ($)'),
                      dataIndex: 'balance_after',
                      render: (v: string | number) => Number(v || 0).toFixed(4),
                    },
                    { title: t('common.remark', '说明'), dataIndex: 'description' },
                  ]}
                />
              ),
            },
          ]}
        />
      </Card>

      <Modal
        title={t('wallet.topup', '在线充值')}
        open={topupOpen}
        onCancel={() => setTopupOpen(false)}
        onOk={handleTopup}
        confirmLoading={creating}
      >
        <Form form={form} layout="vertical" initialValues={{ amount_usd: 10 }}>
          <Form.Item name="amount_usd" label={t('wallet.amount_usd', '充值金额 ($)')} rules={[{ required: true }]}>
            <InputNumber min={1} max={10000} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="provider" label={t('wallet.method', '支付方式')} rules={[{ required: true }]}>
            <Select
              placeholder={t('wallet.select_method', '请选择在线支付渠道')}
              options={methods.map((m: any) => ({
                value: m.provider || m.name || m.id,
                label: m.name || m.provider,
              }))}
            />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  );
};

export default WalletPage;
