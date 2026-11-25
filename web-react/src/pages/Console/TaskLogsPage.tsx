import React, { useEffect, useState } from 'react';
import {
  Button,
  Card,
  Descriptions,
  Drawer,
  Input,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  message,
} from 'antd';
import {
  EyeOutlined,
  ReloadOutlined,
  StopOutlined,
  SyncOutlined,
  VideoCameraOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import { formatApiDateTime } from '../../utils/timedisplay';

interface TaskRow {
  id: number;
  task_id: string;
  model: string;
  action_type?: string;
  status: string;
  fail_reason?: string;
  result_url?: string;
  preview_urls?: string[];
  est_tokens: number;
  actual_tokens: number;
  hold_amount_usd: string | number;
  cost_usd: string | number;
  forward_rule_eid?: string;
  billing_rule_pid?: string;
  billing_detail?: string;
  duration_ms: number;
  submitted_at: string;
  finished_at?: string;
  org_id?: number;
  channel_id?: number;
  provider?: string;
  upstream_task_id?: string;
}

const STATUS_COLOR: Record<string, string> = {
  queued: 'default',
  pending: 'default',
  submitted: 'processing',
  running: 'processing',
  succeeded: 'green',
  success: 'green',
  failed: 'red',
  cancelled: 'orange',
};

const CANCELLABLE_STATUSES = new Set(['queued', 'pending', 'submitted', 'running']);

interface TaskLogsPageProps {
  isAdmin?: boolean;
}

export const TaskLogsPage: React.FC<TaskLogsPageProps> = ({ isAdmin = false }) => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [rows, setRows] = useState<TaskRow[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [keyword, setKeyword] = useState('');
  const [statusFilter, setStatusFilter] = useState('');
  const [syncingId, setSyncingId] = useState<string | null>(null);
  const [cancellingId, setCancellingId] = useState<string | null>(null);
  const [detail, setDetail] = useState<TaskRow | null>(null);

  const loadTasks = async (p = page, ps = pageSize) => {
    setLoading(true);
    try {
      const res: any = await request.get('/task_logs', {
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
    void loadTasks(1, pageSize);
    setPage(1);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isAdmin, statusFilter]);

  const handleSync = async (taskId: string) => {
    setSyncingId(taskId);
    try {
      await request.post(`/task_logs/${encodeURIComponent(taskId)}/sync`);
      message.success(t('tasks.synced', '任务状态已同步'));
      await loadTasks(page, pageSize);
    } catch {
      // handled
    } finally {
      setSyncingId(null);
    }
  };

  const handleCancel = async (taskId: string) => {
    setCancellingId(taskId);
    try {
      await request.post(`/task_logs/${encodeURIComponent(taskId)}/cancel`);
      message.success(t('tasks.cancelled', '任务已取消并退还预扣额度'));
      await loadTasks(page, pageSize);
    } catch {
      // handled
    } finally {
      setCancellingId(null);
    }
  };

  const openDetail = async (taskId: string) => {
    try {
      const res: any = await request.get(`/task_logs/${encodeURIComponent(taskId)}/detail`);
      setDetail(res);
    } catch {
      // handled
    }
  };

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card
        title={
          <Space>
            <VideoCameraOutlined />
            <span>{isAdmin ? t('admin.task_logs', '全局异步视频任务') : t('menu.task_logs', '异步视频任务日志')}</span>
          </Space>
        }
        extra={
          <Space wrap>
            <Input.Search
              allowClear
              placeholder={t('tasks.search_placeholder', 'Task ID / 模型')}
              style={{ width: 220 }}
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
              onSearch={() => {
                setPage(1);
                void loadTasks(1, pageSize);
              }}
            />
            <Select
              allowClear
              placeholder={t('common.status', '状态')}
              style={{ width: 130 }}
              value={statusFilter || undefined}
              onChange={(v) => setStatusFilter(v || '')}
              options={[
                { value: 'queued', label: 'queued' },
                { value: 'running', label: 'running' },
                { value: 'succeeded', label: 'succeeded' },
                { value: 'failed', label: 'failed' },
                { value: 'cancelled', label: 'cancelled' },
              ]}
            />
            <Button icon={<ReloadOutlined />} onClick={() => loadTasks(page, pageSize)} />
          </Space>
        }
      >
        <Table
          loading={loading}
          rowKey="task_id"
          dataSource={rows}
          scroll={{ x: 1120 }}
          pagination={{
            current: page,
            pageSize,
            total,
            showSizeChanger: true,
            onChange: (p, ps) => {
              setPage(p);
              setPageSize(ps);
              void loadTasks(p, ps);
            },
          }}
          columns={[
            {
              title: t('common.time', '提交时间'),
              dataIndex: 'submitted_at',
              width: 170,
              render: (v: string) => formatApiDateTime(v),
            },
            {
              title: 'Task ID',
              dataIndex: 'task_id',
              width: 190,
              render: (v: string) => <Typography.Text code ellipsis>{v}</Typography.Text>,
            },
            {
              title: t('common.model', '模型'),
              dataIndex: 'model',
              render: (v: string) => <Tag>{v}</Tag>,
            },
            {
              title: t('common.status', '状态'),
              dataIndex: 'status',
              width: 110,
              render: (v: string) => <Tag color={STATUS_COLOR[v] || 'default'}>{v}</Tag>,
            },
            {
              title: t('tasks.billing', '预扣 / 结算 ($)'),
              width: 150,
              render: (_: unknown, row) =>
                `$${Number(row.hold_amount_usd || 0).toFixed(4)} / $${Number(row.cost_usd || 0).toFixed(4)}`,
            },
            {
              title: t('tasks.duration', '耗时'),
              dataIndex: 'duration_ms',
              width: 90,
              render: (v: number) => (v > 0 ? `${(v / 1000).toFixed(1)}s` : '-'),
            },
            ...(isAdmin
              ? [
                  {
                    title: 'Org / Channel',
                    width: 140,
                    render: (_: unknown, row: TaskRow) => (
                      <Space size={4}>
                        <Tag>O:{row.org_id}</Tag>
                        <Tag color="purple">C:{row.channel_id}</Tag>
                      </Space>
                    ),
                  },
                ]
              : []),
            {
              title: t('common.actions', '操作'),
              width: 210,
              fixed: 'right',
              render: (_: unknown, row) => (
                <Space size={4}>
                  <Button size="small" icon={<EyeOutlined />} onClick={() => openDetail(row.task_id)}>
                    {t('common.detail', '详情')}
                  </Button>
                  <Button
                    size="small"
                    icon={<SyncOutlined spin={syncingId === row.task_id} />}
                    loading={syncingId === row.task_id}
                    onClick={() => handleSync(row.task_id)}
                  >
                    {t('tasks.sync', '同步')}
                  </Button>
                  {CANCELLABLE_STATUSES.has(row.status) && (
                    <Popconfirm
                      title={t('tasks.confirm_cancel', '确认取消任务并退还预扣金额？')}
                      onConfirm={() => handleCancel(row.task_id)}
                    >
                      <Button
                        size="small"
                        danger
                        icon={<StopOutlined />}
                        loading={cancellingId === row.task_id}
                      >
                        {t('tasks.cancel', '取消')}
                      </Button>
                    </Popconfirm>
                  )}
                </Space>
              ),
            },
          ]}
        />
      </Card>

      <Drawer
        title={t('tasks.detail_title', '视频任务详情')}
        open={Boolean(detail)}
        onClose={() => setDetail(null)}
        width={560}
      >
        {detail && (
          <Space direction="vertical" size={16} style={{ width: '100%' }}>
            {detail.result_url && (
              <Card size="small" title={t('tasks.preview', '生成结果预览')}>
                <video
                  src={detail.result_url}
                  controls
                  style={{ width: '100%', maxHeight: 320, borderRadius: 8, background: '#000' }}
                />
                <div style={{ marginTop: 8 }}>
                  <a href={detail.result_url} target="_blank" rel="noreferrer">
                    {detail.result_url}
                  </a>
                </div>
              </Card>
            )}
            <Descriptions column={1} bordered size="small">
              <Descriptions.Item label="Task ID">{detail.task_id}</Descriptions.Item>
              <Descriptions.Item label={t('common.model', '模型')}>{detail.model}</Descriptions.Item>
              <Descriptions.Item label={t('common.status', '状态')}>
                <Tag color={STATUS_COLOR[detail.status] || 'default'}>{detail.status}</Tag>
              </Descriptions.Item>
              <Descriptions.Item label="Tokens (Est / Actual)">
                {detail.est_tokens} / {detail.actual_tokens}
              </Descriptions.Item>
              <Descriptions.Item label={t('tasks.billing', '预扣 / 结算')}>
                Hold ${Number(detail.hold_amount_usd || 0).toFixed(4)} / Cost ${Number(detail.cost_usd || 0).toFixed(4)}
              </Descriptions.Item>
              <Descriptions.Item label="Billing PID / Forward EID">
                {detail.billing_rule_pid || '-'} / {detail.forward_rule_eid || '-'}
              </Descriptions.Item>
              {detail.fail_reason && (
                <Descriptions.Item label={t('tasks.fail_reason', '失败原因')}>
                  <Typography.Text type="danger">{detail.fail_reason}</Typography.Text>
                </Descriptions.Item>
              )}
              {isAdmin && (
                <Descriptions.Item label="Org / Channel / Provider">
                  Org {detail.org_id} / Channel {detail.channel_id} ({detail.provider || '-'})
                </Descriptions.Item>
              )}
            </Descriptions>
          </Space>
        )}
      </Drawer>
    </Space>
  );
};

export default TaskLogsPage;
