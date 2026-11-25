import React, { useCallback, useEffect, useState } from 'react';
import {
  Table,
  Card,
  Typography,
  Space,
  Input,
  Button,
  Tag,
  Select,
  Grid,
  List,
  Modal,
  Popconfirm,
  Tooltip,
  Alert,
  message,
  theme,
} from 'antd';
import { listPagination, useListPager } from '../../components/ListPagination';
import {
  SyncOutlined,
  SearchOutlined,
  BarChartOutlined,
  CheckCircleOutlined,
  ExclamationCircleOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import useSettingsStore from '../../store/settings';
import { formatApiDateTime } from '../../utils/timedisplay';
import { defaultMonthRange, toTimeRangeParams } from '../../utils/dateRangeParams';
import LogDateTimeRangePicker from '../../components/LogDateTimeRangePicker';
import type { Dayjs } from 'dayjs';

const { Title, Text } = Typography;

interface OrderRecord {
  id: number;
  out_trade_no: string;
  user_id: string;
  username: string;
  uid: string;
  payment_method: string;
  amount: number;
  /** 支付手续费（默认货币），已含在实付里，不入余额 */
  fee_amount: number;
  /** 渠道实付金额（收款币种）；旧订单为空时等于 amount */
  pay_amount?: number | null;
  pay_currency?: string | null;
  status: string;
  trade_no: string | null;
  created_at: string;
  paid_at: string | null;
  audit_remark?: string | null;
  audited_by?: string | null;
  last_polled_at?: string | null;
}

const statusMap: Record<string, { color: string; label: string }> = {
  pending: { color: 'processing', label: '待支付' },
  third_party_paid: { color: 'warning', label: '机构已支付(未入账)' },
  paid: { color: 'success', label: '已支付' },
  closed: { color: 'default', label: '已关闭' },
};

const methodMap: Record<string, { color: string; label: string }> = {
  wechat: { color: '#07c160', label: '微信支付' },
  alipay: { color: '#1677ff', label: '支付宝' },
  stripe: { color: '#635bff', label: 'Stripe' },
  bonuspay: { color: '#ff6a00', label: 'BonusPay' },
  hyperbc: { color: '#8b5cf6', label: 'HyperBC' },
  allinpay_wechat: { color: '#07c160', label: '通联微信' },
  'allinpay wechat': { color: '#07c160', label: '通联微信' },
  allinpay_alipay: { color: '#1677ff', label: '通联支付宝' },
  'allinpay alipay': { color: '#1677ff', label: '通联支付宝' },
};

const methodOrder = [
  'wechat',
  'alipay',
  'allinpay_wechat',
  'allinpay_alipay',
  'stripe',
  'hyperbc',
  'bonuspay',
];

const formatOrderTime = (
  timeStr: string | null,
  _refTimeStr: string | null,
  _outTradeNo: string,
  _isCreatedAt: boolean
): string => {
  if (!timeStr) return '';
  return formatApiDateTime(timeStr);
};

const formatPaidAmount = (record: OrderRecord, currencySymbol: string): string => {
  const paid = (record.pay_amount ?? record.amount).toFixed(2);
  return `${currencySymbol}${paid}`;
};

const OrderDetails: React.FC = () => {
  const { token } = theme.useToken();
  const { t } = useTranslation();
  const screens = Grid.useBreakpoint();
  const { settings } = useSettingsStore();
  const currencySymbol = settings?.currency?.currency_symbol || '¥';
  const [data, setData] = useState<OrderRecord[]>([]);
  const [loading, setLoading] = useState(true);
  const [total, setTotal] = useState(0);
  const [totalAmount, setTotalAmount] = useState<number>(0);
  const [totalFee, setTotalFee] = useState<number>(0);
  const [paymentMethods, setPaymentMethods] = useState<string[]>([]);
  const { page, pageSize, onChange } = useListPager();
  const [search, setSearch] = useState('');
  const [statusFilter, setStatusFilter] = useState<string | undefined>();
  const [methodFilter, setMethodFilter] = useState<string | undefined>();
  const [dateRange, setDateRange] = useState<[Dayjs, Dayjs] | null>(() => defaultMonthRange());

  // 操作状态
  const [actionLoadingId, setActionLoadingId] = useState<string | null>(null);
  const [settledModalOpen, setSettledModalOpen] = useState(false);
  const [settlingRecord, setSettlingRecord] = useState<OrderRecord | null>(null);
  const [settledRemark, setSettledRemark] = useState('');
  const [submittingSettled, setSubmittingSettled] = useState(false);

  const fetchData = useCallback(async () => {
    setLoading(true);
    try {
      const resp = await (request.get('/finance/orders', {
        params: {
          page,
          per_page: pageSize,
          user_id: search || undefined,
          status: statusFilter,
          payment_method: methodFilter,
          ...toTimeRangeParams(dateRange),
        },
      }) as unknown as Promise<{
        data: OrderRecord[];
        total: number;
        total_amount: number;
        total_fee_amount?: number;
        payment_methods?: string[];
      }>);
      setData(resp.data);
      setTotal(resp.total);
      setTotalAmount(resp.total_amount || 0);
      setTotalFee(resp.total_fee_amount || 0);
      if (Array.isArray(resp.payment_methods)) {
        setPaymentMethods(resp.payment_methods);
      } else if (!methodFilter) {
        setPaymentMethods(
          Array.from(new Set((resp.data || []).map((r) => r.payment_method).filter(Boolean)))
        );
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  }, [page, pageSize, search, statusFilter, methodFilter, dateRange]);

  const handleQueryRemote = async (record: OrderRecord) => {
    setActionLoadingId(record.out_trade_no);
    try {
      const resp: any = await request.post(`/finance/orders/${record.out_trade_no}/query-remote`);
      const status = resp?.data?.status;
      const msg = resp?.data?.message || '查单完成';
      if (status === 'third_party_paid') {
        message.warning(msg);
      } else if (status === 'paid') {
        message.success(msg);
      } else {
        message.info(msg);
      }
      fetchData();
    } catch (err: any) {
      message.error(err?.response?.data?.message || err?.message || '向支付机构查单失败');
    } finally {
      setActionLoadingId(null);
    }
  };

  const handleConfirmCredit = async (record: OrderRecord) => {
    setActionLoadingId(record.out_trade_no);
    try {
      await request.post(`/finance/orders/${record.out_trade_no}/confirm-credit`, {});
      message.success('已成功确认入账并充值至用户钱包');
      fetchData();
    } catch (err: any) {
      message.error(err?.response?.data?.message || err?.message || '确认入账失败');
    } finally {
      setActionLoadingId(null);
    }
  };

  const openMarkSettledModal = (record: OrderRecord) => {
    setSettlingRecord(record);
    setSettledRemark('');
    setSettledModalOpen(true);
  };

  const handleConfirmMarkSettled = async () => {
    if (!settlingRecord) return;
    setSubmittingSettled(true);
    try {
      await request.post(`/finance/orders/${settlingRecord.out_trade_no}/mark-settled`, {
        remark: settledRemark.trim() || undefined,
      });
      message.success('已标记平账（未向用户钱包增加余额，已防重复到账）');
      setSettledModalOpen(false);
      setSettlingRecord(null);
      fetchData();
    } catch (err: any) {
      message.error(err?.response?.data?.message || err?.message || '标记平账失败');
    } finally {
      setSubmittingSettled(false);
    }
  };

  const methodOptions = React.useMemo(() => {
    const normalizeMethod = (m: string) => {
      if (m === 'allinpay wechat') return 'allinpay_wechat';
      if (m === 'allinpay alipay') return 'allinpay_alipay';
      return m;
    };
    const seen = new Set<string>();
    const list: string[] = [];
    for (const raw of paymentMethods) {
      const m = normalizeMethod(raw);
      if (m && !seen.has(m)) {
        seen.add(m);
        list.push(m);
      }
    }
    list.sort((a, b) => {
      const idxA = methodOrder.indexOf(a);
      const idxB = methodOrder.indexOf(b);
      if (idxA !== -1 && idxB !== -1) return idxA - idxB;
      if (idxA !== -1) return -1;
      if (idxB !== -1) return 1;
      return a.localeCompare(b);
    });
    return list.map((m) => ({
      label: methodMap[m]?.label || m,
      value: m,
    }));
  }, [paymentMethods]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const renderActionButtons = (record: OrderRecord) => {
    if (record.status === 'pending') {
      return (
        <Button
          size="small"
          type="link"
          style={{ padding: '0 4px', fontSize: 12 }}
          icon={<SyncOutlined spin={actionLoadingId === record.out_trade_no} />}
          disabled={actionLoadingId === record.out_trade_no}
          onClick={() => handleQueryRemote(record)}
        >
          核单
        </Button>
      );
    }
    if (record.status === 'third_party_paid') {
      return (
        <Space size={6} wrap>
          <Popconfirm
            title="确认入账充值到用户钱包？"
            description={`确认将 ${currencySymbol}${record.amount.toFixed(2)} 充值到用户 [${record.username || record.user_id}] 的钱包余额吗？`}
            okText="确认充值入账"
            cancelText="取消"
            onConfirm={() => handleConfirmCredit(record)}
          >
            <Button
              size="small"
              type="primary"
              style={{ backgroundColor: '#52c41a', borderColor: '#52c41a', fontSize: 12 }}
              loading={actionLoadingId === record.out_trade_no}
            >
              确认入账
            </Button>
          </Popconfirm>
          <Button
            size="small"
            style={{ fontSize: 12 }}
            loading={actionLoadingId === record.out_trade_no}
            onClick={() => openMarkSettledModal(record)}
          >
            仅平账
          </Button>
        </Space>
      );
    }
    if (record.audit_remark || record.audited_by) {
      return (
        <Tooltip title={`处理人: ${record.audited_by || '-'} | 说明: ${record.audit_remark || '-'}`}>
          <Tag color="cyan" style={{ cursor: 'pointer', margin: 0 }}>
            已审计平账
          </Tag>
        </Tooltip>
      );
    }
    return <Text type="secondary" style={{ fontSize: 12 }}>-</Text>;
  };

  const columns = [
    {
      title: t('finance.order_no', { defaultValue: '订单号' }),
      dataIndex: 'out_trade_no',
      key: 'out_trade_no',
      width: 220,
      render: (text: string) => (
        <Text
          copyable
          style={{
            display: 'inline-flex',
            alignItems: 'center',
            whiteSpace: 'nowrap',
            fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
            fontSize: 11,
            letterSpacing: '-0.2px',
          }}
        >
          {text}
        </Text>
      ),
    },
    {
      title: t('finance.user_info'),
      key: 'user',
      width: 150,
      render: (_: unknown, record: OrderRecord) => (
        <Space vertical size={0}>
          <Text strong>{record.username || '-'}</Text>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {record.uid ? `UID: ${record.uid}` : '-'}
          </Text>
        </Space>
      ),
    },
    {
      title: '支付方式',
      dataIndex: 'payment_method',
      key: 'payment_method',
      width: 100,
      render: (method: string) => {
        const m = methodMap[method] || { color: 'default', label: method };
        return <Tag color={m.color}>{m.label}</Tag>;
      },
    },
    {
      title: '实付',
      key: 'pay_amount',
      width: 130,
      render: (_: unknown, record: OrderRecord) => (
        <Text style={{ color: '#ff4d4f', whiteSpace: 'nowrap' }}>
          {formatPaidAmount(record, currencySymbol)}
        </Text>
      ),
    },
    {
      title: '到账金额',
      dataIndex: 'amount',
      key: 'amount',
      width: 140,
      render: (val: number) => (
        <Text strong style={{ color: '#ff4d4f', whiteSpace: 'nowrap' }}>
          {currencySymbol}
          {val.toFixed(6)}
        </Text>
      ),
    },
    {
      title: '手续费',
      dataIndex: 'fee_amount',
      key: 'fee_amount',
      width: 100,
      render: (val: number) =>
        val > 0 ? (
          <Text style={{ color: '#ff4d4f', whiteSpace: 'nowrap' }}>
            {currencySymbol}
            {val.toFixed(2)}
          </Text>
        ) : (
          <Text type="secondary">-</Text>
        ),
    },
    {
      title: t('finance.status'),
      dataIndex: 'status',
      key: 'status',
      width: 150,
      render: (status: string) => {
        const s = statusMap[status] || { color: 'default', label: status };
        if (status === 'third_party_paid') {
          return (
            <Tooltip title="第三方支付机构已扣款成功，但系统未收到回调通知。请核实是否已手动给用户加款后再行处理。">
              <Tag color={s.color} style={{ cursor: 'help' }}>
                <ExclamationCircleOutlined style={{ marginRight: 4 }} />
                {s.label}
              </Tag>
            </Tooltip>
          );
        }
        return <Tag color={s.color}>{s.label}</Tag>;
      },
    },
    {
      title: '第三方交易号',
      dataIndex: 'trade_no',
      key: 'trade_no',
      render: (text: string | null) =>
        text ? (
          <Text
            copyable
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              whiteSpace: 'nowrap',
              fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
              fontSize: 11,
              letterSpacing: '-0.2px',
            }}
          >
            {text}
          </Text>
        ) : (
          <Text type="secondary" style={{ fontSize: 11 }}>
            -
          </Text>
        ),
    },
    {
      title: '创建时间',
      dataIndex: 'created_at',
      key: 'created_at',
      width: 170,
      render: (text: string, record: OrderRecord) =>
        formatOrderTime(text, record.paid_at, record.out_trade_no, true),
    },
    {
      title: '支付时间',
      dataIndex: 'paid_at',
      key: 'paid_at',
      width: 170,
      render: (text: string | null, record: OrderRecord) =>
        text ? (
          formatOrderTime(text, record.created_at, record.out_trade_no, false)
        ) : (
          <Text type="secondary">-</Text>
        ),
    },
    {
      title: '操作 / 审计',
      key: 'action',
      width: 160,
      fixed: 'right' as const,
      render: (_: unknown, record: OrderRecord) => renderActionButtons(record),
    },
  ];

  return (
    <Card bordered={false}>
      <div
        style={{
          display: 'flex',
          flexDirection: screens.xs ? 'column' : 'row',
          flexWrap: 'wrap',
          justifyContent: 'space-between',
          marginBottom: 12,
          alignItems: screens.xs ? 'flex-start' : 'center',
          gap: 16,
        }}
      >
        <Space size="small" align="center" wrap>
          <BarChartOutlined style={{ fontSize: 24, color: '#52c41a' }} />
          <Title level={2} style={{ margin: 0, fontSize: screens.xs ? 20 : 24 }}>
            {t('finance.order_title')}
          </Title>
          <Text type="secondary" style={{ marginLeft: screens.xs ? 0 : 8 }}>
            (已支付) 合计:{' '}
            <Text strong style={{ color: '#52c41a', fontSize: 16 }}>
              {currencySymbol}
              {totalAmount.toFixed(6)}
            </Text>
            <span style={{ marginLeft: 12 }}>
              手续费合计:{' '}
              <Text strong style={{ color: '#52c41a', fontSize: 16 }}>
                {currencySymbol}
                {totalFee.toFixed(2)}
              </Text>
            </span>
          </Text>
        </Space>
        <Space wrap style={{ width: screens.xs ? '100%' : 'auto' }}>
          <LogDateTimeRangePicker
            value={dateRange}
            onChange={setDateRange}
            isAdmin
            className="font-size-12"
          />
          <Select
            placeholder="支付状态"
            allowClear
            value={statusFilter}
            onChange={setStatusFilter}
            style={{ width: 145 }}
            options={[
              { label: '待支付', value: 'pending' },
              { label: '机构已支付(未入账)', value: 'third_party_paid' },
              { label: '已支付', value: 'paid' },
              { label: '已关闭', value: 'closed' },
            ]}
          />
          <Select
            placeholder="支付方式"
            allowClear
            value={methodFilter}
            onChange={setMethodFilter}
            style={{ width: 120 }}
            options={methodOptions}
          />
          <Input
            placeholder="搜索 用户名 / UID / 订单号 / 第三方交易号"
            prefix={<SearchOutlined />}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onPressEnter={fetchData}
            style={{ width: 260 }}
          />
          <Button icon={<SyncOutlined />} onClick={fetchData}>
            {t('common.refresh')}
          </Button>
        </Space>
      </div>

      {screens.xs ? (
        <List
          dataSource={data}
          loading={loading}
          pagination={listPagination({
            total,
            current: page,
            pageSize,
            onChange,
            size: 'small',
          })}
          renderItem={(record) => {
            const statusInfo = statusMap[record.status] || {
              color: 'default',
              label: record.status,
            };
            const methodInfo = methodMap[record.payment_method] || {
              color: 'default',
              label: record.payment_method,
            };
            return (
              <List.Item style={{ padding: '0 0 8px 0', border: 'none' }}>
                <Card
                  size="small"
                  style={{
                    width: '100%',
                    borderRadius: 8,
                    boxShadow: '0 1px 2px rgba(0,0,0,0.05)',
                  }}
                  title={<Text strong>{record.username || '-'}</Text>}
                  extra={<Tag color={statusInfo.color}>{statusInfo.label}</Tag>}
                >
                  <div
                    style={{
                      display: 'flex',
                      justifyContent: 'space-between',
                      alignItems: 'center',
                      marginBottom: 4,
                    }}
                  >
                    <Text type="secondary" style={{ fontSize: 12 }}>
                      订单号
                    </Text>
                    <Text
                      copyable
                      style={{
                        display: 'inline-flex',
                        alignItems: 'center',
                        whiteSpace: 'nowrap',
                        fontFamily:
                          'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
                        fontSize: 11,
                        letterSpacing: '-0.2px',
                      }}
                    >
                      {record.out_trade_no}
                    </Text>
                  </div>
                  <div
                    style={{
                      display: 'flex',
                      justifyContent: 'space-between',
                      marginBottom: 4,
                    }}
                  >
                    <Text type="secondary" style={{ fontSize: 12 }}>
                      UID
                    </Text>
                    <Text style={{ fontSize: 12 }}>{record.uid || '-'}</Text>
                  </div>
                  <div
                    style={{
                      display: 'flex',
                      justifyContent: 'space-between',
                      marginBottom: 4,
                    }}
                  >
                    <Text type="secondary" style={{ fontSize: 12 }}>
                      支付方式
                    </Text>
                    <Tag color={methodInfo.color} style={{ margin: 0 }}>
                      {methodInfo.label}
                    </Tag>
                  </div>
                  <div
                    style={{
                      display: 'flex',
                      justifyContent: 'space-between',
                      marginBottom: 4,
                    }}
                  >
                    <Text type="secondary" style={{ fontSize: 12 }}>
                      实付
                    </Text>
                    <Text style={{ fontSize: 12, color: '#ff4d4f' }}>
                      {formatPaidAmount(record, currencySymbol)}
                    </Text>
                  </div>
                  <div
                    style={{
                      display: 'flex',
                      justifyContent: 'space-between',
                      marginBottom: 4,
                    }}
                  >
                    <Text type="secondary" style={{ fontSize: 12 }}>
                      到账金额
                    </Text>
                    <Text strong style={{ color: '#ff4d4f' }}>
                      {currencySymbol}
                      {record.amount.toFixed(6)}
                    </Text>
                  </div>
                  {record.fee_amount > 0 && (
                    <div
                      style={{
                        display: 'flex',
                        justifyContent: 'space-between',
                        marginBottom: 4,
                      }}
                    >
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        手续费
                      </Text>
                      <Text style={{ fontSize: 12, color: '#ff4d4f' }}>
                        {currencySymbol}
                        {record.fee_amount.toFixed(2)}
                      </Text>
                    </div>
                  )}
                  {record.trade_no && (
                    <div
                      style={{
                        display: 'flex',
                        justifyContent: 'space-between',
                        alignItems: 'center',
                        marginBottom: 4,
                      }}
                    >
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        第三方交易号
                      </Text>
                      <Text
                        copyable
                        style={{
                          display: 'inline-flex',
                          alignItems: 'center',
                          whiteSpace: 'nowrap',
                          fontFamily:
                            'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
                          fontSize: 11,
                          letterSpacing: '-0.2px',
                        }}
                      >
                        {record.trade_no}
                      </Text>
                    </div>
                  )}
                  <div
                    style={{
                      display: 'flex',
                      justifyContent: 'space-between',
                      marginBottom: 4,
                    }}
                  >
                    <Text type="secondary" style={{ fontSize: 12 }}>
                      创建时间
                    </Text>
                    <Text style={{ fontSize: 12 }}>
                      {formatOrderTime(
                        record.created_at,
                        record.paid_at,
                        record.out_trade_no,
                        true
                      )}
                    </Text>
                  </div>
                  {record.paid_at && (
                    <div
                      style={{
                        display: 'flex',
                        justifyContent: 'space-between',
                        marginBottom: 4,
                      }}
                    >
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        支付时间
                      </Text>
                      <Text style={{ fontSize: 12 }}>
                        {formatOrderTime(
                          record.paid_at,
                          record.created_at,
                          record.out_trade_no,
                          false
                        )}
                      </Text>
                    </div>
                  )}
                  <div
                    style={{
                      display: 'flex',
                      justifyContent: 'space-between',
                      alignItems: 'center',
                      marginTop: 8,
                      paddingTop: 8,
                      borderTop: `1px dashed ${token.colorBorderSecondary}`,
                    }}
                  >
                    <Text type="secondary" style={{ fontSize: 12 }}>
                      操作
                    </Text>
                    <div>{renderActionButtons(record)}</div>
                  </div>
                </Card>
              </List.Item>
            );
          }}
        />
      ) : (
        <Table
          dataSource={data}
          columns={columns}
          rowKey="id"
          className="compact-table"
          loading={loading}
          pagination={listPagination({
            total,
            current: page,
            pageSize,
            onChange,
          })}
          size="small"
          scroll={{ x: 'max-content' }}
        />
      )}

      {/* 仅平账确认弹窗（防重复充值） */}
      <Modal
        title={
          <Space size={6}>
            <CheckCircleOutlined style={{ color: token.colorPrimary }} />
            <span>确认仅平账（不加用户钱包余额）</span>
          </Space>
        }
        open={settledModalOpen}
        onCancel={() => {
          if (!submittingSettled) {
            setSettledModalOpen(false);
            setSettlingRecord(null);
          }
        }}
        onOk={handleConfirmMarkSettled}
        confirmLoading={submittingSettled}
        okText="确认平账并置为已支付"
        cancelText="取消"
        destroyOnClose
      >
        <Space vertical size={12} style={{ width: '100%', marginTop: 8 }}>
          <Alert
            type="warning"
            showIcon
            message="财务平账说明"
            description="仅将订单状态变更为【已支付】完成对账，不会向用户钱包增加余额，避免重复入账。"
          />
          {settlingRecord && (
            <div
              style={{
                background: token.colorFillAlter,
                border: `1px solid ${token.colorBorderSecondary}`,
                borderRadius: token.borderRadiusSM,
                padding: '8px 12px',
                fontSize: 12,
                display: 'flex',
                flexDirection: 'column',
                gap: 4,
              }}
            >
              <div>
                <Text type="secondary">订单号：</Text>
                <Text strong>{settlingRecord.out_trade_no}</Text>
              </div>
              <div>
                <Text type="secondary">充值用户：</Text>
                <Text strong>{settlingRecord.username || settlingRecord.user_id}</Text>
              </div>
              <div>
                <Text type="secondary">订单金额：</Text>
                <Text strong style={{ color: token.colorError }}>
                  {currencySymbol}{settlingRecord.amount.toFixed(2)}
                </Text>
              </div>
            </div>
          )}
          <div>
            <Text type="secondary" style={{ fontSize: 12 }}>
              平账备注（可选）：
            </Text>
            <Input.TextArea
              rows={3}
              placeholder="选填，例如：线下已收款或已补款，仅做平账对齐"
              value={settledRemark}
              onChange={(e) => setSettledRemark(e.target.value)}
              maxLength={200}
              showCount
              style={{ marginTop: 4 }}
            />
          </div>
        </Space>
      </Modal>
    </Card>
  );
};

export default OrderDetails;
