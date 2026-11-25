import React, { useEffect, useState } from 'react';
import { Card, Col, Progress, Row, Segmented, Space, Statistic, Table, Tag, Typography } from 'antd';
import {
  ApiOutlined,
  CheckCircleOutlined,
  DollarOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';

interface DailyTrendPoint {
  date: string;
  cost_usd: string | number;
  requests: number;
}

interface ModelDistItem {
  model: string;
  requests: number;
  cost_usd: string | number;
}

interface DashboardStatsResponse {
  scope: string;
  balance_usd: string | number;
  held_usd: string | number;
  total_requests: number;
  success_requests: number;
  total_tokens: number;
  total_cost_usd: string | number;
  daily_trend: DailyTrendPoint[];
  model_distribution: ModelDistItem[];
  user_count?: number;
  org_count?: number;
  active_channel_count?: number;
}

interface DashboardPageProps {
  scope?: 'org' | 'platform';
}

export const DashboardPage: React.FC<DashboardPageProps> = ({ scope = 'org' }) => {
  const { t } = useTranslation();
  const [days, setDays] = useState<number>(14);
  const [loading, setLoading] = useState(true);
  const [stats, setStats] = useState<DashboardStatsResponse | null>(null);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    request
      .get('/dashboard/stats', {
        params: scope === 'platform' ? { days, scope: 'platform' } : { days },
      })
      .then((res: any) => {
        if (alive) setStats(res);
      })
      .catch(() => {})
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [days, scope]);

  const totalReq = Number(stats?.total_requests || 0);
  const successReq = Number(stats?.success_requests || 0);
  const successRate = totalReq > 0 ? Math.round((successReq / totalReq) * 1000) / 10 : 100;

  const chartData = (stats?.daily_trend || []).map((pt) => ({
    date: pt.date.slice(5),
    cost: Number(pt.cost_usd || 0),
    requests: Number(pt.requests || 0),
  }));

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 12 }}>
        <div>
          <Typography.Title level={4} style={{ margin: 0 }}>
            {scope === 'platform' ? t('admin.dashboard', '平台运营概览') : t('menu.dashboard', '控制台概览')}
          </Typography.Title>
          <Typography.Text type="secondary">
            {scope === 'platform'
              ? t('admin.dashboard_desc', '全平台请求量、营收消耗与活跃渠道状态')
              : t('dashboard.subtitle', '当前组织账户余额、近期用量趋势与热门模型')}
          </Typography.Text>
        </div>
        <Segmented
          value={days}
          onChange={(v) => setDays(Number(v))}
          options={[
            { label: '7D', value: 7 },
            { label: '14D', value: 14 },
            { label: '30D', value: 30 },
          ]}
        />
      </div>

      <Row gutter={[16, 16]}>
        <Col xs={24} sm={12} lg={6}>
          <Card loading={loading}>
            <Statistic
              title={scope === 'platform' ? t('admin.total_user_balance', '全平台账户余额 ($)') : t('wallet.balance', '可用余额 ($)')}
              value={Number(stats?.balance_usd || 0)}
              precision={4}
              prefix={<DollarOutlined />}
            />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('wallet.held', '冻结中')}: ${Number(stats?.held_usd || 0).toFixed(4)}
            </Typography.Text>
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card loading={loading}>
            <Statistic
              title={t('dashboard.period_cost', `近 ${days} 天消耗 ($)`)}
              value={Number(stats?.total_cost_usd || 0)}
              precision={4}
              prefix={<ThunderboltOutlined />}
            />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              Tokens: {Number(stats?.total_tokens || 0).toLocaleString()}
            </Typography.Text>
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card loading={loading}>
            <Statistic
              title={t('dashboard.requests', `近 ${days} 天请求数`)}
              value={totalReq}
              prefix={<ApiOutlined />}
            />
            <div style={{ marginTop: 6 }}>
              <Progress percent={successRate} size="small" status={successRate >= 95 ? 'success' : 'normal'} />
            </div>
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card loading={loading}>
            {scope === 'platform' ? (
              <>
                <Statistic
                  title={t('admin.active_channels', '在线渠道 / 用户')}
                  value={Number(stats?.active_channel_count || 0)}
                  suffix={`/ ${Number(stats?.user_count || 0)} users`}
                  prefix={<CheckCircleOutlined />}
                />
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {t('admin.org_count', '组织数')}: {Number(stats?.org_count || 0)}
                </Typography.Text>
              </>
            ) : (
              <>
                <Statistic
                  title={t('dashboard.success_rate', '请求成功率')}
                  value={successRate}
                  suffix="%"
                  prefix={<CheckCircleOutlined />}
                />
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {t('dashboard.success_count', '成功请求')}: {successReq.toLocaleString()}
                </Typography.Text>
              </>
            )}
          </Card>
        </Col>
      </Row>

      <Row gutter={[16, 16]}>
        <Col xs={24} lg={15}>
          <Card title={t('dashboard.daily_trend', '每日请求与消耗趋势')} loading={loading}>
            <div style={{ width: '100%', height: 290 }}>
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={chartData}>
                  <defs>
                    <linearGradient id="costGrad" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#6366f1" stopOpacity={0.35} />
                      <stop offset="95%" stopColor="#6366f1" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" opacity={0.15} />
                  <XAxis dataKey="date" />
                  <YAxis yAxisId="left" />
                  <YAxis yAxisId="right" orientation="right" />
                  <Tooltip />
                  <Area
                    yAxisId="left"
                    type="monotone"
                    dataKey="cost"
                    name="Cost ($)"
                    stroke="#6366f1"
                    fillOpacity={1}
                    fill="url(#costGrad)"
                  />
                  <Area
                    yAxisId="right"
                    type="monotone"
                    dataKey="requests"
                    name="Requests"
                    stroke="#10b981"
                    fillOpacity={0.08}
                    fill="#10b981"
                  />
                </AreaChart>
              </ResponsiveContainer>
            </div>
          </Card>
        </Col>
        <Col xs={24} lg={9}>
          <Card title={t('dashboard.top_models', '模型用量分布 (Top 10)')} loading={loading}>
            <Table
              size="small"
              pagination={false}
              rowKey="model"
              dataSource={stats?.model_distribution || []}
              columns={[
                {
                  title: t('common.model', '模型'),
                  dataIndex: 'model',
                  ellipsis: true,
                  render: (m: string) => <Tag>{m}</Tag>,
                },
                {
                  title: t('common.requests', '次数'),
                  dataIndex: 'requests',
                  width: 80,
                  align: 'right',
                },
                {
                  title: t('common.cost', '消耗 ($)'),
                  dataIndex: 'cost_usd',
                  width: 100,
                  align: 'right',
                  render: (v: string | number) => `$${Number(v || 0).toFixed(4)}`,
                },
              ]}
            />
          </Card>
        </Col>
      </Row>
    </Space>
  );
};

export default DashboardPage;
