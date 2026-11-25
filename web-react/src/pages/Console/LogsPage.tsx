import React, { useEffect, useState } from 'react';
import {
  Button,
  Card,
  Descriptions,
  Drawer,
  Input,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd';
import { EyeOutlined, ReloadOutlined, UnorderedListOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import { formatApiDateTime } from '../../utils/timedisplay';

interface InvocationLog {
  id: number;
  request_id: string;
  model: string;
  modality: string;
  protocol: string;
  status: string;
  status_code: number;
  is_stream: boolean;
  prompt_tokens: number;
  completion_tokens: number;
  cached_tokens: number;
  reasoning_tokens: number;
  cost_usd: string | number;
  usage_source: string;
  frt_ms: number;
  duration_ms: number;
  finish_reason?: string;
  error_brief?: string;
  forward_rule_eid?: string;
  billing_rule_pid?: string;
  created_at: string;
  org_id?: number;
  channel_id?: number;
  provider?: string;
  upstream_cost_ratio?: string | number;
  upstream_request_id?: string;
}

interface LogsPageProps {
  isAdmin?: boolean;
}

export const LogsPage: React.FC<LogsPageProps> = ({ isAdmin = false }) => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [rows, setRows] = useState<InvocationLog[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [keyword, setKeyword] = useState('');
  const [statusFilter, setStatusFilter] = useState<string>('');
  const [detail, setDetail] = useState<any | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);

  const loadLogs = async (p = page, ps = pageSize) => {
    setLoading(true);
    try {
      const res: any = await request.get('/logs', {
        params: {
          page: p,
          per_page: ps,
          ...(isAdmin ? { admin: 1 } : {}),
          ...(keyword.trim() ? { q: keyword.trim() } : {}),
          ...(statusFilter ? { status: statusFilter } : {}),
        },
      });
      setRows(Array.isArray(res?.data) ? res.data : []);
      setTotal(Number(res?.total || 0));
    } catch {
      // handled
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void loadLogs(1, pageSize);
    setPage(1);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isAdmin, statusFilter]);

  const openDetail = async (id: number) => {
    try {
      const res: any = await request.get(`/logs/${id}/detail`);
      setDetail(res);
      setDetailOpen(true);
    } catch {
      // handled
    }
  };

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card
        title={
          <Space>
            <UnorderedListOutlined />
            <span>{isAdmin ? t('admin.logs', '全局调用日志') : t('menu.logs', '请求日志')}</span>
          </Space>
        }
      >
        <div style={{ display: 'flex', justifyContent: 'flex-end', flexWrap: 'wrap', gap: 8, marginBottom: 12 }}>
          <Input.Search
            allowClear
            placeholder={t('logs.search_placeholder', 'Request ID / 模型')}
            style={{ width: 220 }}
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            onSearch={() => {
              setPage(1);
              void loadLogs(1, pageSize);
            }}
          />
          <Select
            allowClear
            placeholder={t('common.status', '状态')}
            style={{ width: 120 }}
            value={statusFilter || undefined}
            onChange={(v) => setStatusFilter(v || '')}
            options={[
              { value: 'success', label: 'Success' },
              { value: 'failed', label: 'Failed' },
            ]}
          />
          <Button icon={<ReloadOutlined />} onClick={() => loadLogs(page, pageSize)} />
        </div>
        <Table
          loading={loading}
          rowKey="id"
          dataSource={rows}
          scroll={{ x: 1100 }}
          pagination={{
            current: page,
            pageSize,
            total,
            showSizeChanger: true,
            onChange: (p, ps) => {
              setPage(p);
              setPageSize(ps);
              void loadLogs(p, ps);
            },
          }}
          columns={[
            {
              title: t('common.time', '时间'),
              dataIndex: 'created_at',
              width: 170,
              render: (v: string) => formatApiDateTime(v),
            },
            {
              title: 'Request ID',
              dataIndex: 'request_id',
              width: 170,
              render: (v: string) => <Typography.Text code ellipsis>{v}</Typography.Text>,
            },
            {
              title: t('common.model', '模型'),
              dataIndex: 'model',
              render: (v: string, row) => (
                <Space size={4}>
                  <Tag>{v}</Tag>
                  {row.protocol ? <Tag color="blue">{row.protocol}</Tag> : null}
                </Space>
              ),
            },
            {
              title: t('common.status', '状态'),
              dataIndex: 'status',
              width: 110,
              render: (v: string, row) => (
                <Tag color={v === 'success' ? 'green' : 'red'}>
                  {v} ({row.status_code})
                </Tag>
              ),
            },
            {
              title: 'Tokens (In / Out / Cached)',
              render: (_: unknown, row) =>
                `${row.prompt_tokens} / ${row.completion_tokens}${row.cached_tokens ? ` (${row.cached_tokens} cached)` : ''}`,
            },
            {
              title: 'TTFT / Total',
              width: 130,
              render: (_: unknown, row) => `${row.frt_ms}ms / ${row.duration_ms}ms`,
            },
            {
              title: t('common.cost', '费用 ($)'),
              dataIndex: 'cost_usd',
              width: 110,
              align: 'right',
              render: (v: string | number) => `$${Number(v || 0).toFixed(6)}`,
            },
            ...(isAdmin
              ? [
                  {
                    title: 'Channel / Org',
                    width: 130,
                    render: (_: unknown, row: InvocationLog) => (
                      <Space size={4}>
                        <Tag>O:{row.org_id}</Tag>
                        <Tag color="purple">C:{row.channel_id}</Tag>
                      </Space>
                    ),
                  },
                ]
              : []),
            {
              title: t('common.actions', '详情'),
              width: 80,
              fixed: 'right',
              render: (_: unknown, row) => (
                <Button size="small" icon={<EyeOutlined />} onClick={() => openDetail(row.id)} />
              ),
            },
          ]}
        />
      </Card>

      <Drawer
        title={t('logs.detail_title', '请求调用详情')}
        open={detailOpen}
        onClose={() => setDetailOpen(false)}
        width={540}
      >
        {detail && (
          <Descriptions column={1} bordered size="small">
            <Descriptions.Item label="Request ID">{detail.request_id}</Descriptions.Item>
            <Descriptions.Item label={t('common.model', '模型')}>{detail.model}</Descriptions.Item>
            <Descriptions.Item label="Modality / Protocol">
              {detail.modality} / {detail.protocol} {detail.is_stream ? '(stream)' : ''}
            </Descriptions.Item>
            <Descriptions.Item label={t('common.status', '状态')}>
              {detail.status} ({detail.status_code})
            </Descriptions.Item>
            <Descriptions.Item label="Tokens">
              Prompt: {detail.prompt_tokens}, Completion: {detail.completion_tokens}, Cached: {detail.cached_tokens}, Reasoning: {detail.reasoning_tokens}
            </Descriptions.Item>
            <Descriptions.Item label={t('common.cost', '计费金额')}>
              ${Number(detail.cost_usd || 0).toFixed(6)} ({detail.usage_source})
            </Descriptions.Item>
            <Descriptions.Item label="Billing PID / Forward EID">
              {detail.billing_rule_pid || '-'} / {detail.forward_rule_eid || '-'}
            </Descriptions.Item>
            <Descriptions.Item label="Timing">
              TTFT: {detail.frt_ms}ms / Duration: {detail.duration_ms}ms
            </Descriptions.Item>
            {isAdmin && (
              <>
                <Descriptions.Item label="Org / Channel / Provider">
                  Org {detail.org_id} / Channel {detail.channel_id} ({detail.provider || '-'})
                </Descriptions.Item>
                <Descriptions.Item label="Upstream Cost Ratio">
                  {detail.upstream_cost_ratio ?? '-'}
                </Descriptions.Item>
              </>
            )}
            {detail.error_brief && (
              <Descriptions.Item label="Error Brief">
                <Typography.Text type="danger">{detail.error_brief}</Typography.Text>
              </Descriptions.Item>
            )}
          </Descriptions>
        )}
      </Drawer>
    </Space>
  );
};

export default LogsPage;
