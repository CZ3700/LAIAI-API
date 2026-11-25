import React, { useEffect, useMemo, useRef, useState } from 'react';
import {
  Card,
  Table,
  Button,
  Modal,
  Form,
  Input,
  InputNumber,
  Switch,
  DatePicker,
  message,
  Space,
  Tag,
  Popconfirm,
  Tabs,
  Spin,
  Typography,
  Divider,
  Alert,
  Segmented,
  Tooltip,
  Checkbox,
  ColorPicker,
} from 'antd';
import {
  PlusOutlined,
  EditOutlined,
  DeleteOutlined,
  SaveOutlined,
  SendOutlined,
  UndoOutlined,
  CodeOutlined,
  EyeOutlined,
  WalletOutlined,
  DesktopOutlined,
  ExperimentOutlined,
  GlobalOutlined,
} from '@ant-design/icons';
import ReactQuill from 'react-quill-new';
import 'react-quill-new/dist/quill.snow.css';
import dayjs from 'dayjs';
import type { Dayjs } from 'dayjs';
import request from '../../../utils/request';
import { fetchAdminPlugins } from '../../../utils/adminPlugins';
import { listPagination } from '../../../components/ListPagination';
import { apiErrMsg, SKIP_ERR } from '../../../utils/apiErr';
import type { Announcement, Plugin } from '../../../types';
import { formatApiDateTime } from '../../../utils/timedisplay';

const DEFAULT_EXPIRE_DAYS = 5;
const PERMANENT_EXPIRE = 'permanent';

function defaultExpireDate(): Dayjs {
  return dayjs().add(DEFAULT_EXPIRE_DAYS, 'day');
}

function expireToDayjs(raw?: string | null): Dayjs {
  if (!raw) return defaultExpireDate();
  const d = dayjs(raw);
  return d.isValid() ? d : defaultExpireDate();
}

function expireModeFromRecord(raw?: string | null): 'permanent' | 'date' {
  return raw ? 'date' : 'permanent';
}

function expireToPayload(mode: unknown, value: unknown): string {
  if (mode === 'permanent') return PERMANENT_EXPIRE;
  if (!value) return PERMANENT_EXPIRE;
  const d = dayjs(value as string | Dayjs);
  return d.isValid() ? d.endOf('day').toISOString() : PERMANENT_EXPIRE;
}

function formatExpireLabel(raw?: string | null): string {
  if (!raw) return '长期';
  const d = dayjs(raw);
  return d.isValid() ? d.format('YYYY-MM-DD') : '长期';
}

type FlagExpireRowProps = {
  switchName: string;
  modeName: string;
  expireName: string;
  label: string;
};

const FlagExpireRow: React.FC<FlagExpireRowProps> = ({
  switchName,
  modeName,
  expireName,
  label,
}) => {
  const form = Form.useFormInstance();
  const mode = Form.useWatch(modeName, form) as 'permanent' | 'date' | undefined;
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 16, minHeight: 32 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, width: 124, flexShrink: 0 }}>
        <span style={{ fontSize: 13, color: 'var(--ant-color-text)', userSelect: 'none' }}>{label}</span>
        <Form.Item name={switchName} valuePropName="checked" style={{ margin: 0 }}>
          <Switch />
        </Form.Item>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <span style={{ fontSize: 13, color: 'var(--ant-color-text-secondary)', userSelect: 'none' }}>有效期</span>
        <Form.Item name={modeName} style={{ margin: 0 }}>
          <Segmented
            size="small"
            options={[
              { label: '长期', value: 'permanent' },
              { label: '指定日期', value: 'date' },
            ]}
            onChange={(v) => {
              if (v === 'date' && !form.getFieldValue(expireName)) {
                form.setFieldValue(expireName, defaultExpireDate());
              }
            }}
          />
        </Form.Item>
        {mode !== 'permanent' && (
          <Form.Item
            name={expireName}
            style={{ margin: 0 }}
            rules={[{ required: true, message: '请选择有效期' }]}
          >
            <DatePicker
              size="small"
              allowClear={false}
              format="YYYY-MM-DD"
              style={{ width: 140 }}
              disabledDate={(current) => !!current && current < dayjs().startOf('day')}
            />
          </Form.Item>
        )}
      </div>
    </div>
  );
};

const { Text, Title } = Typography;
const { TextArea } = Input;

const DEFAULT_EMAIL_SUBJECT = '【{{site_name}}】账户余额不足提醒';
const DEFAULT_EMAIL_HTML = `<div style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; max-width: 600px; margin: 0 auto; border: 1px solid #e8e8e8; border-radius: 8px;">
  <div style="padding: 30px;">
    <h2 style="color: #fa8c16; margin: 0 0 24px 0; font-size: 22px; font-weight: 600;">余额不足提醒</h2>
    <p style="color: #333; font-size: 16px; margin: 0 0 16px 0;">您好！</p>
    <p style="color: #333; font-size: 16px; margin: 0 0 24px 0;">您的账户可用余额已低于设定阈值，请及时充值以免影响服务使用。</p>
    <div style="background-color: #f5f5f5; padding: 20px; border-radius: 6px; margin-bottom: 24px;">
      <p style="color: #666; font-size: 14px; margin: 0 0 8px 0;">当前余额：<strong style="color: #fa541c; font-size: 18px;">{{balance}}</strong></p>
      <p style="color: #666; font-size: 14px; margin: 0;">提醒阈值：<strong>{{threshold}}</strong></p>
    </div>
    <div style="border-top: 1px dashed #e8e8e8; margin-top: 24px; padding-top: 16px;">
      <p style="color: #999; font-size: 12px; margin: 0;">此邮件由 {{site_name}} 系统根据您的通知订阅设置自动发送。</p>
    </div>
  </div>
</div>`;

function renderTemplate(
  tpl: string,
  vars: { site_name: string; balance: string; threshold: string },
) {
  return tpl
    .replaceAll('{{site_name}}', vars.site_name)
    .replaceAll('{{balance}}', vars.balance)
    .replaceAll('{{threshold}}', vars.threshold);
}

const NotificationSettingsForm: React.FC = () => {
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [siteName, setSiteName] = useState('laiai');
  const [previewBalance, setPreviewBalance] = useState('88.0000');
  const [previewThreshold, setPreviewThreshold] = useState('100.000000');
  const [testEmail, setTestEmail] = useState('');
  const [testMobile, setTestMobile] = useState('');
  const [testEmailLoading, setTestEmailLoading] = useState(false);
  const [testSmsLoading, setTestSmsLoading] = useState(false);
  const [bodyMode, setBodyMode] = useState<'edit' | 'preview'>('edit');
  const [hasBalanceSmsTemplate, setHasBalanceSmsTemplate] = useState(false);

  const siteNotificationEnabled = Form.useWatch('site_notification_enabled', form);
  const smsBalanceEnabled = Form.useWatch('sms_balance_notification', form);
  const emailSubject = Form.useWatch('low_balance_email_subject', form) || '';
  const emailHtml = Form.useWatch('low_balance_email_html', form) || '';
  const smsTemplateMissing = !!smsBalanceEnabled && !hasBalanceSmsTemplate;

  const previewVars = useMemo(
    () => ({
      site_name: siteName || 'laiai',
      balance: previewBalance || '88.0000',
      threshold: previewThreshold || '100.000000',
    }),
    [siteName, previewBalance, previewThreshold],
  );

  const previewSubject = useMemo(
    () => renderTemplate(emailSubject || DEFAULT_EMAIL_SUBJECT, previewVars),
    [emailSubject, previewVars],
  );
  const previewHtml = useMemo(
    () => renderTemplate(emailHtml || DEFAULT_EMAIL_HTML, previewVars),
    [emailHtml, previewVars],
  );

  const fetchSettings = async () => {
    setLoading(true);
    try {
      const response = await (request.get('/settings/full') as any);
      const n = response.notification || {};
      const smtpName = response.smtp?.from_name || response.site?.name || 'laiai';
      setSiteName(smtpName);
      setHasBalanceSmsTemplate(!!response.sms?.balance_template_id?.trim());
      form.setFieldsValue({
        site_notification_enabled: n.site_notification_enabled || false,
        sms_balance_notification: n.sms_balance_notification || false,
        email_balance_notification: n.email_balance_notification || false,
        web_notification_enabled: n.web_notification_enabled !== false,
        push_notification_enabled: n.push_notification_enabled !== false,
        do_not_disturb_enabled: n.do_not_disturb_enabled !== false,
        low_balance_threshold:
          n.low_balance_threshold != null && n.low_balance_threshold > 0
            ? n.low_balance_threshold
            : 100,
        low_balance_email_subject:
          n.low_balance_email_subject?.trim() || DEFAULT_EMAIL_SUBJECT,
        low_balance_email_html: n.low_balance_email_html?.trim() || DEFAULT_EMAIL_HTML,
      });
      if (n.low_balance_threshold != null && n.low_balance_threshold > 0) {
        setPreviewThreshold(String(n.low_balance_threshold));
      }
    } catch (error) {
      console.error('Failed to fetch settings:', error);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchSettings();
  }, []);

  const handleSave = async () => {
    if (submitting) return;
    setSubmitting(true);
    try {
      const values = await form.validateFields();
      if (values.sms_balance_notification && !hasBalanceSmsTemplate) {
        message.error('开启短信余额提醒前，请先在「消息通知 → 短信」配置余额提醒模板 ID');
        return;
      }
      await request.post('/settings', { notification: values }, SKIP_ERR as any);
      message.success('保存配置成功');
    } catch (error: any) {
      if (error?.errorFields) return;
      console.error('Failed to save settings:', error);
      message.error(apiErrMsg(error, '保存配置失败'));
    } finally {
      setSubmitting(false);
    }
  };

  const handleResetTemplate = () => {
    form.setFieldsValue({
      low_balance_email_subject: DEFAULT_EMAIL_SUBJECT,
      low_balance_email_html: DEFAULT_EMAIL_HTML,
    });
    message.info('已恢复默认邮件模版（需点击保存才会生效）');
  };

  const handleTestEmail = async () => {
    if (testEmailLoading) return;
    const to = testEmail.trim();
    if (!to) {
      message.warning('请输入测试收件邮箱');
      return;
    }
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(to)) {
      message.warning('请输入有效的邮箱地址');
      return;
    }
    setTestEmailLoading(true);
    try {
      const values = form.getFieldsValue();
      const res = await (request.post(
        '/settings/notification/test',
        {
          channel: 'email',
          to,
          balance: previewBalance,
          threshold: previewThreshold,
          subject: values.low_balance_email_subject || DEFAULT_EMAIL_SUBJECT,
          html: values.low_balance_email_html || DEFAULT_EMAIL_HTML,
        },
        SKIP_ERR as any,
      ) as any);
      res.success ? message.success(res.message) : message.error(res.message || '发送失败');
    } catch (e: any) {
      message.error(apiErrMsg(e, '测试邮件发送失败'));
    } finally {
      setTestEmailLoading(false);
    }
  };

  const handleTestSms = async () => {
    if (testSmsLoading) return;
    const mobile = testMobile.trim();
    if (!mobile) {
      message.warning('请输入测试手机号');
      return;
    }
    if (!/^\+?\d{6,15}$/.test(mobile)) {
      message.warning('请输入有效的手机号（可含国家码，如 +86138…）');
      return;
    }
    if (!hasBalanceSmsTemplate) {
      message.warning('请先在「消息通知 → 短信」配置余额提醒模板 ID');
      return;
    }
    setTestSmsLoading(true);
    try {
      const res = await (request.post(
        '/settings/notification/test',
        { channel: 'sms', mobile },
        SKIP_ERR as any,
      ) as any);
      res.success ? message.success(res.message) : message.error(res.message || '发送失败');
    } catch (e: any) {
      message.error(apiErrMsg(e, '测试短信发送失败'));
    } finally {
      setTestSmsLoading(false);
    }
  };

  if (loading) return <Spin style={{ display: 'block', margin: '40px auto' }} />;

  return (
    <Form
      form={form}
      layout="vertical"
      style={{ maxWidth: 960 }}
      initialValues={{
        web_notification_enabled: true,
        push_notification_enabled: true,
        do_not_disturb_enabled: true,
        low_balance_threshold: 100,
        low_balance_email_subject: DEFAULT_EMAIL_SUBJECT,
        low_balance_email_html: DEFAULT_EMAIL_HTML,
      }}
    >
      <Form.Item
        name="site_notification_enabled"
        label="站点提示（总开关）"
        valuePropName="checked"
        tooltip="开启后用户可以在个人中心订阅以下通知功能"
        style={{ marginBottom: 12 }}
      >
        <Switch />
      </Form.Item>

      {siteNotificationEnabled && (
        <Card size="small" style={{ marginBottom: 16, backgroundColor: 'var(--ant-color-bg-layout)' }}>
          <Form.Item
            name="do_not_disturb_enabled"
            label="勿扰模式"
            valuePropName="checked"
            tooltip="开启后用户可在通知订阅中使用勿扰开关"
            style={{ marginBottom: 10 }}
          >
            <Switch />
          </Form.Item>
          <Form.Item
            name="web_notification_enabled"
            label="Web 站内通知"
            valuePropName="checked"
            tooltip="开启后用户可订阅控制台站内通知（铃铛/公告）"
            style={{ marginBottom: 10 }}
          >
            <Switch />
          </Form.Item>
          <Form.Item
            name="push_notification_enabled"
            label="Push 浏览器推送"
            valuePropName="checked"
            tooltip="开启后用户可订阅浏览器系统推送"
            style={{ marginBottom: 10 }}
          >
            <Switch />
          </Form.Item>
          <Form.Item
            name="sms_balance_notification"
            label="短信余额提示"
            valuePropName="checked"
            extra="模板 ID 在「站点设置 → 消息通知 → 短信」配置"
            style={{ marginBottom: 10 }}
          >
            <Switch />
          </Form.Item>
          {smsTemplateMissing && (
            <Alert
              type="warning"
              showIcon
              style={{ marginBottom: 10 }}
              message="尚未配置余额提醒模板 ID，请先到「消息通知 → 短信」填写后再开启并保存"
            />
          )}
          <Form.Item
            name="email_balance_notification"
            label="邮件余额提示"
            valuePropName="checked"
            style={{ marginBottom: 10 }}
          >
            <Switch />
          </Form.Item>
          <Form.Item
            name="low_balance_threshold"
            label="余额不足提醒阈值（全局默认）"
            rules={[{ required: true, message: '请设置阈值' }]}
            extra="用户未自定义时使用该阈值，默认为 100"
            style={{ marginBottom: 4 }}
          >
            <InputNumber min={0} step={1} precision={6} style={{ width: '100%' }} placeholder="100" />
          </Form.Item>
        </Card>
      )}

      <Divider style={{ margin: '14px 0' }} />

      <Title level={5} style={{ marginTop: 0 }}>
        余额不足提醒 · 邮件模版
      </Title>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="可用变量"
        description={
          <span>
            <Text code>{'{{site_name}}'}</Text> 站点/发件人名称 ·{' '}
            <Text code>{'{{balance}}'}</Text> 当前余额 ·{' '}
            <Text code>{'{{threshold}}'}</Text> 提醒阈值
          </span>
        }
      />

      <Form.Item
        name="low_balance_email_subject"
        label="邮件主题"
        rules={[{ required: true, message: '请填写邮件主题' }]}
      >
        <Input placeholder={DEFAULT_EMAIL_SUBJECT} />
      </Form.Item>

      <div style={{ marginBottom: 24 }}>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', justifyContent: 'space-between', gap: 8, marginBottom: 8 }}>
          <Text style={{ fontWeight: 500 }}>邮件正文</Text>
          <Space wrap size="small">
            <Segmented
              size="small"
              value={bodyMode}
              onChange={(v) => setBodyMode(v as 'edit' | 'preview')}
              options={[
                { label: '编辑 HTML', value: 'edit', icon: <CodeOutlined /> },
                { label: '实时预览', value: 'preview', icon: <EyeOutlined /> },
              ]}
            />
            <Button size="small" icon={<UndoOutlined />} onClick={handleResetTemplate}>
              恢复默认
            </Button>
          </Space>
        </div>
        <Text type="secondary" style={{ display: 'block', fontSize: 12, marginBottom: 8 }}>
          支持 HTML。预览模式会实时替换变量，发送给用户时使用同一套渲染逻辑。
        </Text>
        <Form.Item
          name="low_balance_email_html"
          rules={[{ required: true, message: '请填写邮件正文' }]}
          style={{ display: bodyMode === 'edit' ? 'block' : 'none', marginBottom: 0 }}
        >
          <TextArea
            rows={14}
            style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', fontSize: 12 }}
          />
        </Form.Item>
        {bodyMode === 'preview' && (
          <div
            style={{
              border: '1px solid var(--ant-color-border)',
              borderRadius: 8,
              padding: 12,
              background: '#fff',
              minHeight: 280,
              overflow: 'auto',
            }}
          >
            <Text type="secondary" style={{ display: 'block', marginBottom: 8, fontSize: 12 }}>
              主题：{previewSubject}
            </Text>
            <div dangerouslySetInnerHTML={{ __html: previewHtml }} />
          </div>
        )}
      </div>

      <Title level={5}>测试发送</Title>
      <Text type="secondary" style={{ display: 'block', marginBottom: 12 }}>
        测试余额/阈值仅用于邮件预览与试发；短信走「消息通知 → 短信」已配置的无变量模板，无需先保存。
      </Text>
      <Space wrap style={{ marginBottom: 12 }}>
        <Text type="secondary" style={{ fontSize: 12 }}>测试余额（邮件）</Text>
        <Input
          value={previewBalance}
          onChange={(e) => setPreviewBalance(e.target.value)}
          style={{ width: 120 }}
          size="small"
        />
        <Text type="secondary" style={{ fontSize: 12 }}>测试阈值（邮件）</Text>
        <Input
          value={previewThreshold}
          onChange={(e) => setPreviewThreshold(e.target.value)}
          style={{ width: 120 }}
          size="small"
        />
      </Space>
      <Space direction="vertical" style={{ width: '100%', marginBottom: 24 }} size="middle">
        <Space wrap>
          <Input
            placeholder="测试收件邮箱"
            value={testEmail}
            onChange={(e) => setTestEmail(e.target.value)}
            style={{ width: 280 }}
          />
          <Button
            htmlType="button"
            icon={<SendOutlined />}
            loading={testEmailLoading}
            onClick={handleTestEmail}
          >
            发送测试邮件
          </Button>
        </Space>
        <Space wrap>
          <Input
            placeholder="测试手机号"
            value={testMobile}
            onChange={(e) => setTestMobile(e.target.value)}
            style={{ width: 280 }}
          />
          <Button
            htmlType="button"
            icon={<SendOutlined />}
            loading={testSmsLoading}
            onClick={handleTestSms}
            disabled={!hasBalanceSmsTemplate}
            title={
              hasBalanceSmsTemplate
                ? undefined
                : '请先在「消息通知 → 短信」配置余额提醒模板 ID'
            }
          >
            发送测试短信
          </Button>
        </Space>
      </Space>

      <Button
        type="primary"
        htmlType="button"
        icon={<SaveOutlined />}
        onClick={handleSave}
        loading={submitting}
      >
        保存配置
      </Button>
    </Form>
  );
};

const InlineSortCell: React.FC<{
  value: number;
  onSave: (val: number) => Promise<void>;
}> = ({ value: initialValue, onSave }) => {
  const [editing, setEditing] = useState(false);
  const [val, setVal] = useState<number | null>(initialValue ?? 0);
  const [saving, setSaving] = useState(false);
  const inputRef = useRef<any>(null);

  useEffect(() => {
    setVal(initialValue ?? 0);
  }, [initialValue]);

  useEffect(() => {
    if (editing) {
      setTimeout(() => {
        inputRef.current?.focus?.();
      }, 50);
    }
  }, [editing]);

  const handleCommit = async () => {
    if (saving) return;
    const nextVal = val === null || isNaN(val) ? 0 : Number(val);
    if (nextVal === (initialValue ?? 0)) {
      setEditing(false);
      return;
    }
    setSaving(true);
    try {
      await onSave(nextVal);
      setEditing(false);
    } catch {
      setVal(initialValue ?? 0);
      setEditing(false);
    } finally {
      setSaving(false);
    }
  };

  const handleCancel = () => {
    setVal(initialValue ?? 0);
    setEditing(false);
  };

  if (!editing) {
    return (
      <Space size={6} align="center" style={{ justifyContent: 'center' }}>
        <Text style={{ fontSize: 13, minWidth: 16 }}>{initialValue ?? 0}</Text>
        <Tooltip title="快捷编辑排序">
          <Button
            type="text"
            size="small"
            icon={<EditOutlined style={{ fontSize: 12, color: 'var(--ant-color-primary, #1677ff)' }} />}
            onClick={() => setEditing(true)}
            style={{
              width: 22,
              height: 22,
              padding: 0,
              display: 'inline-flex',
              alignItems: 'center',
              justifyContent: 'center',
            }}
          />
        </Tooltip>
      </Space>
    );
  }

  return (
    <Space size={4} align="center" style={{ justifyContent: 'center' }}>
      <InputNumber
        ref={inputRef}
        size="small"
        min={-999999}
        max={99999999}
        value={val}
        disabled={saving}
        onChange={(v) => setVal(v)}
        onPressEnter={handleCommit}
        onBlur={handleCommit}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation();
            handleCancel();
          }
        }}
        style={{ width: 80 }}
      />
    </Space>
  );
};

const NOTICE_MODULES = [
  { label: '控制台通知', value: 'console' },
  { label: '创作中心通知', value: 'playground_2026', plugin: 'playground_2026' },
  { label: '门户通知', value: 'portal_pro', plugin: 'site_portal_pro' },
  { label: '支付弹窗内横幅', value: 'pay_modal' },
] as const;

function isCoreNoticeModule(module: string): boolean {
  return module === 'console' || module === 'pay_modal';
}

function noticeModuleLabel(module?: string): string {
  return NOTICE_MODULES.find((item) => item.value === module)?.label || module || '控制台通知';
}

function parseNoticeModules(raw?: string | null): string[] {
  const list = String(raw || '')
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean);
  return list.length ? list : ['console'];
}

function bannerHex(color?: { cleared?: boolean; toHexString: () => string } | null): string {
  if (!color || color.cleared) return '';
  const hex = color.toHexString();
  return hex.length > 7 ? hex.slice(0, 7) : hex;
}

const Announcements: React.FC = () => {
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalVisible, setModalVisible] = useState(false);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [plugins, setPlugins] = useState<Plugin[]>([]);
  const [pluginsLoaded, setPluginsLoaded] = useState(false);
  const [form] = Form.useForm();
  const targetModules = Form.useWatch('target_modules', form) as string[] | undefined;
  const playgroundBg = Form.useWatch('playground_banner_bg', form) as string | undefined;
  const portalBg = Form.useWatch('portal_banner_bg', form) as string | undefined;
  const selectedModules = Array.isArray(targetModules) ? targetModules : [];

  const pluginOn = (name: string) => plugins.some((item) => item.name === name && item.is_enabled === 1);
  const playgroundOn = pluginsLoaded && pluginOn('playground_2026');
  const portalOn = pluginsLoaded && pluginOn('site_portal_pro');

  const availableNoticeModules = useMemo(() => [
    {
      value: 'console',
      label: '控制台通知',
      icon: <DesktopOutlined style={{ color: selectedModules.includes('console') ? 'var(--ant-color-text)' : 'var(--ant-color-text-secondary)', fontSize: 16 }} />,
    },
    ...(playgroundOn ? [{
      value: 'playground_2026',
      label: '创作中心通知',
      icon: <ExperimentOutlined style={{ color: selectedModules.includes('playground_2026') ? 'var(--ant-color-text)' : 'var(--ant-color-text-secondary)', fontSize: 16 }} />,
    }] : []),
    ...(portalOn ? [{
      value: 'portal_pro',
      label: '门户通知',
      icon: <GlobalOutlined style={{ color: selectedModules.includes('portal_pro') ? 'var(--ant-color-text)' : 'var(--ant-color-text-secondary)', fontSize: 16 }} />,
    }] : []),
    {
      value: 'pay_modal',
      label: '支付弹窗内横幅',
      icon: <WalletOutlined style={{ color: selectedModules.includes('pay_modal') ? 'var(--ant-color-text)' : 'var(--ant-color-text-secondary)', fontSize: 16 }} />,
    },
  ], [playgroundOn, portalOn, selectedModules]);

  useEffect(() => {
    if (!pluginsLoaded) return;
    const current = form.getFieldValue('target_modules');
    if (!Array.isArray(current)) return;
    const next = current.filter((module: string) => {
      if (module === 'playground_2026') return pluginOn('playground_2026');
      if (module === 'portal_pro') return pluginOn('site_portal_pro');
      return isCoreNoticeModule(module);
    });
    const normalized = next.length ? next : ['console'];
    if (normalized.join(',') !== current.join(',')) {
      form.setFieldValue('target_modules', normalized);
    }
  }, [pluginsLoaded, plugins, form]);

  const fetchAnnouncements = async () => {
    setLoading(true);
    try {
      const response = await (request.get('/announcements') as any);
      setAnnouncements(response.data || []);
    } catch (error) {
      console.error('Failed to fetch announcements:', error);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchAnnouncements();
    fetchAdminPlugins()
      .then((data) => {
        if (Array.isArray(data?.plugins)) setPlugins(data.plugins);
      })
      .catch((error) => {
        console.error('Failed to fetch plugins:', error);
      })
      .finally(() => setPluginsLoaded(true));
  }, []);

  const handleInlineSortSave = async (id: number, newSortOrder: number) => {
    try {
      await (request.put(`/announcements/${id}`, { sort_order: newSortOrder }, SKIP_ERR as any) as any);
      message.success('排序已更新');
      setAnnouncements((prev) =>
        prev
          .map((item) => (item.id === id ? { ...item, sort_order: newSortOrder } : item))
          .sort((a, b) => (b.sort_order || 0) - (a.sort_order || 0) || b.id - a.id),
      );
    } catch (error: any) {
      console.error('Failed to update sort order:', error);
      message.error(apiErrMsg(error, '更新排序失败'));
      fetchAnnouncements();
    }
  };

  const [togglingIds, setTogglingIds] = useState<number[]>([]);

  const handleToggleActive = async (record: Announcement, checked: boolean) => {
    setTogglingIds((prev) => [...prev, record.id]);
    try {
      const payload: any = { is_active: checked ? 1 : 0 };
      if (checked && record.active_expires_at && dayjs(record.active_expires_at).isBefore(dayjs())) {
        payload.active_expires_at = PERMANENT_EXPIRE;
      }
      const res: any = await request.put(`/announcements/${record.id}`, payload, SKIP_ERR as any);
      message.success(checked ? '已开启通知' : '已禁用通知');
      const updatedItem = res?.data;
      setAnnouncements((prev) =>
        prev.map((item) => {
          if (item.id !== record.id) return item;
          return updatedItem ? updatedItem : { ...item, is_active: checked ? 1 : 0 };
        })
      );
    } catch (error: any) {
      console.error('Failed to toggle announcement status:', error);
      message.error(apiErrMsg(error, '操作失败'));
      fetchAnnouncements();
    } finally {
      setTogglingIds((prev) => prev.filter((id) => id !== record.id));
    }
  };

  const handleAdd = () => {
    setEditingId(null);
    form.resetFields();
    const expire = defaultExpireDate();
    form.setFieldsValue({
      is_pinned: false,
      is_popup: false,
      is_active: true,
      sort_order: 0,
      target_modules: ['console'],
      playground_banner_bg: '',
      portal_banner_bg: '',
      display_time_mode: 'created',
      pin_expire_mode: 'date',
      popup_expire_mode: 'date',
      active_expire_mode: 'date',
      pin_expires_at: expire,
      popup_expires_at: expire,
      active_expires_at: expire,
    });
    setModalVisible(true);
  };

  const handleEdit = (record: Announcement) => {
    setEditingId(record.id);
    form.setFieldsValue({
      ...record,
      is_pinned: record.is_pinned === 1,
      is_popup: record.is_popup === 1,
      is_active: record.is_active === 1,
      sort_order: record.sort_order ?? 0,
      target_modules: parseNoticeModules(record.target_module),
      playground_banner_bg: record.playground_banner_bg || '',
      portal_banner_bg: record.portal_banner_bg || '',
      pin_expire_mode: expireModeFromRecord(record.pin_expires_at),
      popup_expire_mode: expireModeFromRecord(record.popup_expires_at),
      active_expire_mode: expireModeFromRecord(record.active_expires_at),
      pin_expires_at: expireToDayjs(record.pin_expires_at),
      popup_expires_at: expireToDayjs(record.popup_expires_at),
      active_expires_at: expireToDayjs(record.active_expires_at),
      display_time_mode: record.display_time_mode === 'updated' ? 'updated' : 'created',
    });
    setModalVisible(true);
  };

  const handleDelete = async (id: number) => {
    try {
      await (request.delete(`/announcements/${id}`) as any);
      message.success('删除成功');
      fetchAnnouncements();
    } catch (error) {
      console.error('Failed to delete announcement:', error);
    }
  };

  const handleSave = async () => {
    try {
      const values = await form.validateFields();
      const chosenModules = (Array.isArray(values.target_modules) ? values.target_modules : ['console']).filter((module: string) => {
        if (module === 'playground_2026') return playgroundOn;
        if (module === 'portal_pro') return portalOn;
        return isCoreNoticeModule(module);
      });
      const payload = {
        title: values.title,
        content: values.content,
        is_pinned: values.is_pinned ? 1 : 0,
        is_popup: values.is_popup ? 1 : 0,
        is_active: values.is_active ? 1 : 0,
        sort_order: Number(values.sort_order) || 0,
        target_module: (chosenModules.length ? chosenModules : ['console']).join(','),
        playground_banner_bg: values.playground_banner_bg || '',
        portal_banner_bg: values.portal_banner_bg || '',
        pin_expires_at: expireToPayload(values.pin_expire_mode, values.pin_expires_at),
        popup_expires_at: expireToPayload(values.popup_expire_mode, values.popup_expires_at),
        active_expires_at: expireToPayload(values.active_expire_mode, values.active_expires_at),
        display_time_mode: values.display_time_mode === 'updated' ? 'updated' : 'created',
      };

      if (editingId) {
        await (request.put(`/announcements/${editingId}`, payload) as any);
        message.success('更新成功');
      } else {
        await (request.post('/announcements', payload) as any);
        message.success('创建成功');
      }
      setModalVisible(false);
      fetchAnnouncements();
    } catch (error) {
      console.error('Failed to save announcement:', error);
    }
  };

  const columns = [
    {
      title: 'ID',
      dataIndex: 'id',
      key: 'id',
      width: 70,
      align: 'center' as const,
    },
    {
      title: '标题',
      dataIndex: 'title',
      key: 'title',
      render: (title: string, record: Announcement) => {
        const activeUntil = formatExpireLabel(record.active_expires_at);
        const pinUntil = formatExpireLabel(record.pin_expires_at);
        const popupUntil = formatExpireLabel(record.popup_expires_at);
        return (
          <Space vertical size={4} align="start">
            <span style={{ fontWeight: 500, wordBreak: 'break-word' }}>{title}</span>
            <Space size={6} wrap align="center">
              {record.is_active === 1 ? (
                <Tag color="success" style={{ margin: 0 }}>{`上架 · ${activeUntil}`}</Tag>
              ) : (
                <Tag color="default" style={{ margin: 0 }}>下架</Tag>
              )}
              {record.is_pinned === 1 && (
                <Tag color="blue" style={{ margin: 0 }}>{`置顶 · ${pinUntil}`}</Tag>
              )}
              {record.is_popup === 1 && (
                <Tag color="default" style={{ margin: 0 }}>{`弹窗通知 · ${popupUntil}`}</Tag>
              )}
            </Space>
          </Space>
        );
      },
    },
    {
      title: '展示模块',
      dataIndex: 'target_module',
      key: 'target_module',
      width: 180,
      render: (val: string) => (
        <Space vertical size={4} align="start">
          {parseNoticeModules(val).filter((module) => {
            if (!pluginsLoaded) return true;
            if (module === 'playground_2026') return pluginOn('playground_2026');
            if (module === 'portal_pro') return pluginOn('site_portal_pro');
            return isCoreNoticeModule(module);
          }).map((module) => (
            <Tag key={module} color={module === 'pay_modal' ? 'gold' : undefined} style={{ margin: 0 }}>{noticeModuleLabel(module)}</Tag>
          ))}
        </Space>
      ),
    },
    {
      title: '排序',
      dataIndex: 'sort_order',
      key: 'sort_order',
      width: 100,
      align: 'center' as const,
      sorter: (a: Announcement, b: Announcement) => (a.sort_order || 0) - (b.sort_order || 0),
      render: (val: number, record: Announcement) => (
        <InlineSortCell
          value={val ?? record.sort_order ?? 0}
          onSave={(newVal) => handleInlineSortSave(record.id, newVal)}
        />
      ),
    },
    {
      title: '状态',
      key: 'status',
      width: 100,
      align: 'center' as const,
      render: (_: any, record: Announcement) => (
        <Tooltip title={record.is_active === 1 ? '点击禁用下架' : '点击开启上架'}>
          <Switch
            checked={record.is_active === 1}
            checkedChildren="开启"
            unCheckedChildren="禁用"
            loading={togglingIds.includes(record.id)}
            onChange={(checked) => handleToggleActive(record, checked)}
          />
        </Tooltip>
      ),
    },
    {
      title: '时间',
      key: 'time',
      width: 290,
      render: (_: unknown, record: Announcement) => (
        <Space vertical size={2} style={{ display: 'flex' }}>
          <Space size={6} wrap={false} style={{ whiteSpace: 'nowrap' }}>
            <Text type="secondary" style={{ fontSize: 12 }}>新建:</Text>
            <span>{formatApiDateTime(record.created_at, 'YYYY-MM-DD HH:mm')}</span>
            {record.display_time_mode !== 'updated' && <Tag color="blue" style={{ margin: 0 }}>发布时间展示</Tag>}
          </Space>
          <Space size={6} wrap={false} style={{ whiteSpace: 'nowrap' }}>
            <Text type="secondary" style={{ fontSize: 12 }}>修改:</Text>
            <span>{formatApiDateTime(record.updated_at, 'YYYY-MM-DD HH:mm')}</span>
            {record.display_time_mode === 'updated' && <Tag color="blue" style={{ margin: 0 }}>发布时间展示</Tag>}
          </Space>
        </Space>
      ),
    },
    {
      title: '操作',
      key: 'action',
      width: 88,
      align: 'center' as const,
      render: (_: any, record: Announcement) => (
        <Space size={2} style={{ justifyContent: 'center', width: '100%' }}>
          <Tooltip title="编辑">
            <Button
              type="text"
              size="small"
              icon={<EditOutlined style={{ color: 'var(--ant-color-primary, #1677ff)' }} />}
              onClick={() => handleEdit(record)}
            />
          </Tooltip>
          <Popconfirm
            title="确定要删除该通知吗？"
            onConfirm={() => handleDelete(record.id)}
            okText="确定"
            cancelText="取消"
          >
            <Tooltip title="删除">
              <Button
                type="text"
                danger
                size="small"
                icon={<DeleteOutlined />}
              />
            </Tooltip>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  const items = [
    {
      key: 'list',
      label: '通知列表',
      children: (
        <>
          <div style={{ marginBottom: 12 }}>
            <Button type="primary" icon={<PlusOutlined />} onClick={handleAdd}>新增通知</Button>
          </div>
          <Table
            size="middle"
            columns={columns}
            dataSource={announcements}
            rowKey="id"
            loading={loading}
            pagination={listPagination()}
            scroll={{ x: 'max-content' }}
          />
        </>
      ),
    },
    {
      key: 'settings',
      label: '基础配置',
      children: <NotificationSettingsForm />,
    },
  ];

  return (
    <Card
      title="提示通知管理"
      style={{ borderRadius: 12 }}
      bordered={false}
    >
      <Tabs items={items} />

      <Modal
        title={editingId ? '编辑通知' : '新增通知'}
        open={modalVisible}
        onOk={handleSave}
        onCancel={() => setModalVisible(false)}
        width={1080}
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          <Form.Item name="title" label="通知标题" rules={[{ required: true, message: '请输入标题' }]} style={{ marginBottom: 14 }}>
            <Input placeholder="请输入通知标题" />
          </Form.Item>

          <Form.Item
            name="target_modules"
            label={
              <Space size={8} align="center">
                <span>通知模块</span>
                <span style={{ fontSize: 12, color: 'var(--ant-color-text-secondary)', fontWeight: 400 }}>
                  （请至少选择一个通知模块）
                </span>
              </Space>
            }
            rules={[{ required: true, type: 'array', min: 1, message: '请至少选择一个通知模块' }]}
            style={{ marginBottom: 16 }}
          >
            <Checkbox.Group style={{ width: '100%' }}>
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: 10 }}>
                {availableNoticeModules.map((item) => {
                  const isChecked = selectedModules.includes(item.value);
                  return (
                    <Checkbox
                      key={item.value}
                      value={item.value}
                      style={{
                        display: 'flex',
                        alignItems: 'center',
                        padding: '12px 16px',
                        borderRadius: 8,
                        border: `1px solid ${isChecked ? 'var(--ant-color-primary, #1677ff)' : 'var(--ant-color-border-secondary, #e5e7eb)'}`,
                        backgroundColor: isChecked ? 'var(--ant-color-primary-bg, rgba(22, 119, 255, 0.05))' : 'var(--ant-color-fill-quaternary, rgba(0, 0, 0, 0.02))',
                        transition: 'all 0.2s cubic-bezier(0.4, 0, 0.2, 1)',
                        margin: 0,
                        width: '100%',
                        boxSizing: 'border-box',
                      }}
                    >
                      <Space size={8} align="center" style={{ marginLeft: 6 }}>
                        {item.icon}
                        <span style={{ fontWeight: isChecked ? 600 : 400, fontSize: 13, color: 'var(--ant-color-text)' }}>
                          {item.label}
                        </span>
                      </Space>
                    </Checkbox>
                  );
                })}
              </div>
            </Checkbox.Group>
          </Form.Item>

          {playgroundOn && selectedModules.includes('playground_2026') && (
            <Form.Item label="创作中心顶栏背景色" style={{ marginBottom: 16 }}>
              <Space size={12} align="center">
                <ColorPicker
                  disabledAlpha
                  allowClear
                  showText
                  value={playgroundBg || undefined}
                  onChange={(color) => form.setFieldValue('playground_banner_bg', bannerHex(color))}
                />
                <span style={{ fontSize: 12, color: 'var(--ant-color-text-secondary)' }}>留空为创作中心默认背景</span>
              </Space>
            </Form.Item>
          )}
          {portalOn && selectedModules.includes('portal_pro') && (
            <Form.Item label="门户顶栏背景色" style={{ marginBottom: 16 }}>
              <Space size={12} align="center">
                <ColorPicker
                  disabledAlpha
                  allowClear
                  showText
                  value={portalBg || undefined}
                  onChange={(color) => form.setFieldValue('portal_banner_bg', bannerHex(color))}
                />
                <span style={{ fontSize: 12, color: 'var(--ant-color-text-secondary)' }}>留空为门户默认背景</span>
              </Space>
            </Form.Item>
          )}
          <Form.Item name="playground_banner_bg" hidden>
            <Input />
          </Form.Item>
          <Form.Item name="portal_banner_bg" hidden>
            <Input />
          </Form.Item>

          <Form.Item
            name="sort_order"
            label="排序权重"
            style={{ marginBottom: 16 }}
          >
            <InputNumber min={-999999} max={99999999} placeholder="0" style={{ width: 120 }} />
          </Form.Item>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginBottom: 18 }}>
            <FlagExpireRow
              switchName="is_pinned"
              modeName="pin_expire_mode"
              expireName="pin_expires_at"
              label="置顶显示"
            />
            <FlagExpireRow
              switchName="is_popup"
              modeName="popup_expire_mode"
              expireName="popup_expires_at"
              label="弹窗通知"
            />
            <FlagExpireRow
              switchName="is_active"
              modeName="active_expire_mode"
              expireName="active_expires_at"
              label="是否上架"
            />
          </div>

          <div
            style={{
              display: 'flex',
              flexDirection: 'column',
              gap: 10,
              marginBottom: 18,
              padding: '12px 14px',
              borderRadius: 8,
              background: 'var(--ant-color-fill-quaternary)',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 16, minHeight: 32, flexWrap: 'wrap' }}>
              <span style={{ fontSize: 13, color: 'var(--ant-color-text)', width: 88, flexShrink: 0 }}>新建时间</span>
              <span style={{ fontSize: 13, color: 'var(--ant-color-text-secondary)' }}>
                {editingId
                  ? formatApiDateTime(
                      announcements.find((a) => a.id === editingId)?.created_at,
                      'YYYY-MM-DD HH:mm:ss',
                    )
                  : '保存后生成'}
              </span>
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 16, minHeight: 32, flexWrap: 'wrap' }}>
              <span style={{ fontSize: 13, color: 'var(--ant-color-text)', width: 88, flexShrink: 0 }}>最后修改时间</span>
              <span style={{ fontSize: 13, color: 'var(--ant-color-text-secondary)' }}>
                {editingId
                  ? formatApiDateTime(
                      announcements.find((a) => a.id === editingId)?.updated_at,
                      'YYYY-MM-DD HH:mm:ss',
                    )
                  : '保存后生成'}
              </span>
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 16, minHeight: 32, flexWrap: 'wrap' }}>
              <span style={{ fontSize: 13, color: 'var(--ant-color-text)', width: 88, flexShrink: 0 }}>发布时间</span>
              <Form.Item name="display_time_mode" style={{ margin: 0 }}>
                <Segmented
                  options={[
                    { label: '新建时间', value: 'created' },
                    { label: '最后修改时间', value: 'updated' },
                  ]}
                />
              </Form.Item>
              <span style={{ fontSize: 12, color: 'var(--ant-color-text-secondary)' }}>
                用户端弹窗与右上角铃铛将显示所选时间
              </span>
            </div>
          </div>

          <Form.Item name="content" label="通知内容" rules={[{ required: true, message: '请输入通知内容' }]}>
            <ReactQuill
              theme="snow"
              style={{ height: 300, marginBottom: 40, backgroundColor: 'var(--ant-color-bg-container)', color: 'var(--ant-color-text)' }}
            />
          </Form.Item>
        </Form>
      </Modal>
    </Card>
  );
};

export default Announcements;
