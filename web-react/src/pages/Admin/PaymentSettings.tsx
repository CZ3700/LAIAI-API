import React, { useEffect, useMemo, useState } from 'react';
import {
  Card, Form, Input, Button, message, Typography, Tabs, Switch, Alert, Divider,
  InputNumber, Table, Space, Image, Row, Col, Tag, Radio, Badge, Popconfirm, Tooltip,
  Modal, Checkbox, Select,
} from 'antd';
import {
  WechatOutlined, AlipayCircleOutlined, LinkOutlined, SafetyCertificateOutlined,
  DollarOutlined, CreditCardOutlined, ThunderboltOutlined, PlusOutlined, DeleteOutlined,
  SettingOutlined, BankOutlined, ArrowLeftOutlined, EditOutlined, CheckOutlined, CloseOutlined,
  FileTextOutlined, CheckCircleOutlined, ReloadOutlined, StopOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { useSearchParams } from 'react-router-dom';
import request from '../../utils/request';
import { formatRate } from '../../utils/timeMultipliers';
import useSettingsStore from '../../store/settings';
import { useThemeStore } from '../../store/theme';
import type { SellerInvoiceSubject } from '../../types';
import {
  getChannelMeta,
  mergeChannelList,
  resolveChannelName,
  resolveChannelSubtitle,
  type PaymentChannelUiItem,
} from '../../constants/paymentChannels';
import CurrencySettingsTab, { RegionFlagSvg, type FxOverview } from './CurrencySettingsTab';

const { Text } = Typography;

function formatChannelRechargeLimit(record: PaymentChannelUiItem): string {
  const min = Number(record.min_recharge_amount) || 0;
  const max = Number(record.max_recharge_amount) || 0;
  if (min > 0 && max > 0) return `${min} – ${max}`;
  if (min > 0) return `≥ ${min}`;
  if (max > 0) return `≤ ${max}`;
  return '不限制';
}

const PaymentSettings: React.FC = () => {
  const { t } = useTranslation();
  const { updateStoreSettings } = useSettingsStore();
  const { themeMode } = useThemeStore();
  const isLight = themeMode === 'light';

  const [searchParams, setSearchParams] = useSearchParams();
  const urlTab = searchParams.get('tab');
  const [activeTab, setActiveTab] = useState(urlTab || 'currency');

  useEffect(() => {
    if (urlTab && urlTab !== activeTab) {
      setActiveTab(urlTab);
    }
  }, [urlTab]);

  const handleTabChange = (key: string) => {
    setActiveTab(key);
    setSearchParams({ tab: key }, { replace: true });
  };

  const [formDisplay] = Form.useForm();
  const rechargeLimitOn = !!Form.useWatch('recharge_limit_enabled', formDisplay);
  const feeOn = !!Form.useWatch('fee_enabled', formDisplay);
  const [formGateway] = Form.useForm();
  const [formInvoices] = Form.useForm();
  const [formSeller] = Form.useForm();
  const [fullSettings, setFullSettings] = useState<any>(null);
  const watchedInvoiceEnabled = Form.useWatch('invoice_enabled', formInvoices);
  const isInvoiceServiceEnabled = watchedInvoiceEnabled !== undefined
    ? !!watchedInvoiceEnabled
    : (fullSettings?.invoices?.invoice_enabled !== false);
  const watchedInvoiceMode = Form.useWatch('invoice_mode', formInvoices);
  const isChinaMode = (watchedInvoiceMode !== undefined
    ? watchedInvoiceMode
    : fullSettings?.invoices?.invoice_mode) === 'china';

  const [sellerModalOpen, setSellerModalOpen] = useState(false);
  const [editingSeller, setEditingSeller] = useState<SellerInvoiceSubject | null>(null);
  const [sellerSubjects, setSellerSubjects] = useState<SellerInvoiceSubject[]>([]);
  const [savingSellerModal, setSavingSellerModal] = useState(false);

  const [fxOverview, setFxOverview] = useState<FxOverview | null>(null);
  const [loadingChannels, setLoadingChannels] = useState(false);
  const [savingInvoices, setSavingInvoices] = useState(false);
  const [userLevels, setUserLevels] = useState<any[]>([]);
  const [loadingUserLevels, setLoadingUserLevels] = useState(false);
  const [updatingLevelId, setUpdatingLevelId] = useState<number | null>(null);
  const [batchUpdating, setBatchUpdating] = useState(false);
  const [selectedLevelKeys, setSelectedLevelKeys] = useState<React.Key[]>([]);
  const [showLevelDetails, setShowLevelDetails] = useState(false);

  const totalLevelsCount = userLevels.length;
  const enabledLevelsCount = useMemo(
    () => userLevels.filter((l) => l.invoice_enabled === 1).length,
    [userLevels]
  );
  const allLevelsEnabled = totalLevelsCount > 0 && enabledLevelsCount === totalLevelsCount;
  const noneLevelsEnabled = totalLevelsCount > 0 && enabledLevelsCount === 0;
  const [savingDrawer, setSavingDrawer] = useState(false);
  const [channels, setChannels] = useState<PaymentChannelUiItem[]>([]);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editingSubtitleField, setEditingSubtitleField] = useState<string | null>(null);
  const [editingSubtitleVal, setEditingSubtitleVal] = useState<string>('');

  const siteOrigin = window.location.origin;
  const notifyUrl = (path: string) => `${siteOrigin}/api/v1/finance/pay/notify/${path}`;

  useEffect(() => { fetchSettings(); }, []);

  const fetchUserLevels = async () => {
    setLoadingUserLevels(true);
    try {
      const resp = await (request.get('/user_levels') as any);
      const list = resp?.data || resp || [];
      if (Array.isArray(list)) {
        setUserLevels(list);
      }
    } catch (e) {
      console.error('Failed to fetch user levels:', e);
    } finally {
      setLoadingUserLevels(false);
    }
  };

  const fetchFxOverview = async () => {
    try {
      setFxOverview(await (request.get('/settings/currency/fx') as any));
    } catch (e) {
      console.error('Failed to fetch fx overview:', e);
    }
  };

  const fetchSettings = async () => {
    setLoadingChannels(true);
    try {
      fetchUserLevels();
      fetchFxOverview();
      const response = await (request.get('/settings/full') as any);
      setFullSettings(response);
      if (response?.invoices) {
        formInvoices.setFieldsValue({
          ...response.invoices,
          invoice_enabled: response.invoices.invoice_enabled !== false,
          invoice_show_invoices: response.invoices.invoice_show_invoices !== false,
          invoice_show_receipts: response.invoices.invoice_show_receipts !== false,
        });
        if (response.invoices.invoice_sellers && Array.isArray(response.invoices.invoice_sellers) && response.invoices.invoice_sellers.length > 0) {
          setSellerSubjects(response.invoices.invoice_sellers);
        } else if (response.invoices.invoice_company_name) {
          setSellerSubjects([
            {
              id: 'seller_default',
              company_name: response.invoices.invoice_company_name,
              tax_id: response.invoices.invoice_tax_id || '',
              contact_email: response.invoices.invoice_contact_email || '',
              company_address: response.invoices.invoice_company_address || '',
              company_phone: '',
              bank_name: '',
              bank_account: '',
              support_normal: true,
              support_special: true,
              is_default: true,
              enabled: true,
            },
          ]);
        } else {
          setSellerSubjects([]);
        }
      } else {
        formInvoices.setFieldsValue({
          invoice_enabled: true,
          invoice_mode: 'international',
          invoice_company_name: '',
          invoice_company_address: '',
          invoice_tax_id: '',
          invoice_contact_email: '',
          invoice_currency_symbol: '$',
          invoice_prefix: 'INV-',
          receipt_prefix: 'REC-',
          invoice_notes: 'Thank you for your business! Payment has been processed in full.',
          invoice_show_invoices: true,
          invoice_show_receipts: true,
          china_min_amount: 100,
          china_invoice_content: '*信息技术服务*软件技术服务费',
          china_tax_rate: 6,
          china_invoice_notice: '增值税普通发票将在审核通过后发送至填写的邮箱。',
        });
        setSellerSubjects([]);
      }
      setChannels(mergeChannelList(response?.payment_channels_ui?.channels));
    } catch (error) {
      console.error('Failed to fetch payment settings:', error);
    } finally {
      setLoadingChannels(false);
    }
  };

  const persistChannels = async (
    nextChannels: PaymentChannelUiItem[],
    extraPayload: Record<string, any> = {},
  ) => {
    const payload = {
      payment_channels_ui: { channels: nextChannels },
      ...extraPayload,
    };
    const updatedSettings = await (request.post('/settings', payload) as any);
    updateStoreSettings(updatedSettings);
    setFullSettings((prev: any) => ({
      ...(prev || {}),
      ...extraPayload,
      payment_channels_ui: { channels: nextChannels },
      ...(updatedSettings || {}),
    }));
    setChannels(mergeChannelList(nextChannels));
    return updatedSettings;
  };

  const syncGatewayEnabled = (channelId: string, enabled: boolean, _nextChannels: PaymentChannelUiItem[]) => {
    const meta = getChannelMeta(channelId);
    if (!meta) return {};
    return {
      [meta.gatewayKey]: {
        ...(fullSettings?.[meta.gatewayKey] || {}),
        enabled,
      },
    };
  };

  const onToggleChannel = async (id: string, enabled: boolean) => {
    const next = channels.map((c) => {
      if (c.id !== id) return c;
      const updated: PaymentChannelUiItem = { ...c, enabled };
      if (id === 'allinpay' && enabled && !c.allinpay_wechat_enabled && !c.allinpay_alipay_enabled) {
        updated.allinpay_wechat_enabled = true;
        updated.allinpay_alipay_enabled = true;
      }
      return updated;
    });
    try {
      await persistChannels(next, syncGatewayEnabled(id, enabled, next));
      message.destroy();
      message.success(enabled ? '已开启' : '已关闭');
    } catch (e) {
      console.error(e);
      await fetchSettings();
    }
  };

  const onSortChange = async (id: string, sort_order: number) => {
    const next = channels.map((c) => (c.id === id ? { ...c, sort_order } : c));
    setChannels(mergeChannelList(next));
    try {
      await persistChannels(next);
    } catch (e) {
      console.error(e);
      await fetchSettings();
    }
  };

  const onFieldCommit = async (
    id: string,
    field: 'display_name' | 'display_name_en' | 'subtitle' | 'subtitle_en',
    raw: string,
  ) => {
    const meta = getChannelMeta(id);
    const defaultVal =
      field === 'display_name'
        ? meta?.defaultName || ''
        : field === 'display_name_en'
        ? meta?.defaultNameEn || ''
        : field === 'subtitle'
        ? meta?.defaultSubtitle || ''
        : meta?.defaultSubtitleEn || '';

    const trimmed = raw.trim();
    const val = !trimmed || trimmed === defaultVal ? null : trimmed;
    const currentItem = channels.find((c) => c.id === id);
    const prev = (currentItem?.[field] || '').trim() || null;
    const prevNorm = !prev || prev === defaultVal ? null : prev;

    if (prevNorm === val) return;

    const next = channels.map((c) => (c.id === id ? { ...c, [field]: val } : c));
    setChannels(mergeChannelList(next));
    try {
      await persistChannels(next);
    } catch (e) {
      console.error(e);
      await fetchSettings();
    }
  };

  const openConfig = (id: string) => {
    const item = channels.find((c) => c.id === id);
    const meta = getChannelMeta(id);
    if (!item || !meta) return;
    setEditingId(id);
    formDisplay.setFieldsValue({
      enabled: item.enabled,
      sort_order: item.sort_order,
      subtitle: item.subtitle || '',
      subtitle_en: item.subtitle_en || '',
      logo_url: item.logo_url || '',
      allinpay_wechat_enabled: item.allinpay_wechat_enabled !== false,
      allinpay_alipay_enabled: item.allinpay_alipay_enabled !== false,
      currencies: item.currencies || [],
      recharge_limit_enabled: !!item.recharge_limit_enabled,
      min_recharge_amount: Number(item.min_recharge_amount) || 0,
      max_recharge_amount: Number(item.max_recharge_amount) || 0,
      fee_enabled: !!item.fee_enabled,
      fee_percent: Number(item.fee_percent) || 0,
    });
    const gw = fullSettings?.[meta.gatewayKey] || {};
    formGateway.setFieldsValue({ ...gw });
  };

  const closeEditor = () => {
    setEditingId(null);
    setActiveTab('channels');
    formDisplay.resetFields();
    formGateway.resetFields();
  };

  const onSaveDrawer = async () => {
    if (!editingId) return;
    const meta = getChannelMeta(editingId);
    if (!meta) return;
    try {
      const displayValues = await formDisplay.validateFields();
      const gatewayValues = await formGateway.validateFields();

      if (editingId === 'allinpay') {
        const wechatOn = !!displayValues.allinpay_wechat_enabled;
        const alipayOn = !!displayValues.allinpay_alipay_enabled;
        if (!wechatOn && !alipayOn) {
          message.error('请至少开启一个通联子渠道（微信或支付宝）');
          return;
        }
      }

      setSavingDrawer(true);

      const nextChannels = channels.map((c) => {
        if (c.id !== editingId) return c;
        const updated: PaymentChannelUiItem = {
          ...c,
          enabled: !!displayValues.enabled,
          sort_order: Number(displayValues.sort_order) || 0,
          subtitle: (displayValues.subtitle || '').trim() || null,
          subtitle_en: (displayValues.subtitle_en || '').trim() || null,
          logo_url: (displayValues.logo_url || '').trim() || null,
        };
        if (editingId === 'stripe') {
          const picked: string[] = displayValues.currencies || [];
          updated.currencies = picked.length > 0 ? picked : null;
        }
        if (editingId === 'allinpay') {
          updated.allinpay_wechat_enabled = !!displayValues.allinpay_wechat_enabled;
          updated.allinpay_alipay_enabled = !!displayValues.allinpay_alipay_enabled;
        }
        updated.recharge_limit_enabled = !!displayValues.recharge_limit_enabled;
        updated.min_recharge_amount = Number(displayValues.min_recharge_amount) || 0;
        updated.max_recharge_amount = Number(displayValues.max_recharge_amount) || 0;
        updated.fee_enabled = !!displayValues.fee_enabled;
        updated.fee_percent = Number(displayValues.fee_percent) || 0;
        return updated;
      });

      let gatewayPayload: Record<string, any> = {};
      if (meta.gatewayKey === 'payment_wechat') {
        gatewayPayload = {
          payment_wechat: {
            ...(fullSettings?.payment_wechat || {}),
            enabled: !!displayValues.enabled,
            mchid: gatewayValues.mchid || '',
            appid: gatewayValues.appid || '',
            api_v3_key: gatewayValues.api_v3_key || '',
            cert_serial_no: gatewayValues.cert_serial_no || '',
            private_key: gatewayValues.private_key || '',
          },
        };
      } else if (meta.gatewayKey === 'payment_alipay') {
        gatewayPayload = {
          payment_alipay: {
            ...(fullSettings?.payment_alipay || {}),
            enabled: !!displayValues.enabled,
            app_id: gatewayValues.app_id || '',
            private_key: gatewayValues.private_key || '',
            alipay_public_key: gatewayValues.alipay_public_key || '',
            sign_type: 'RSA2',
          },
        };
      } else if (meta.gatewayKey === 'payment_stripe') {
        gatewayPayload = {
          payment_stripe: {
            ...(fullSettings?.payment_stripe || {}),
            enabled: !!displayValues.enabled,
            secret_key: gatewayValues.secret_key || '',
            publishable_key: gatewayValues.publishable_key || '',
            webhook_secret: gatewayValues.webhook_secret || '',
          },
        };
      } else if (meta.gatewayKey === 'payment_hyperbc') {
        gatewayPayload = {
          payment_hyperbc: {
            ...(fullSettings?.payment_hyperbc || {}),
            enabled: !!displayValues.enabled,
            app_id: gatewayValues.app_id || '',
            merchant_private_key: gatewayValues.merchant_private_key || '',
            hyperbc_public_key: gatewayValues.hyperbc_public_key || '',
            api_url: gatewayValues.api_url || 'https://api.cipherbc.com/shopapi',
            crypto_exchange_rate: gatewayValues.crypto_exchange_rate || 1.0,
          },
        };
      } else if (meta.gatewayKey === 'payment_allinpay') {
        gatewayPayload = {
          payment_allinpay: {
            enabled: !!displayValues.enabled,
            cusid: gatewayValues.cusid || '',
            appid: gatewayValues.appid || '',
            merchant_private_key: gatewayValues.merchant_private_key || '',
            allinpay_public_key: gatewayValues.allinpay_public_key || '',
            sign_type: 'RSA',
            api_url: gatewayValues.api_url || 'https://vsp.allinpay.com/apiweb',
            version: gatewayValues.version || '11',
          },
        };
      } else if (meta.gatewayKey === 'payment_anyway') {
        gatewayPayload = {
          payment_anyway: {
            ...(fullSettings?.payment_anyway || {}),
            enabled: !!displayValues.enabled,
            api_key: (gatewayValues.api_key || '').trim(),
          },
        };
      }

      await persistChannels(nextChannels, gatewayPayload);
      message.destroy();
      message.success('支付渠道配置已保存');
      closeEditor();
      await fetchSettings();
    } catch (error: any) {
      if (error?.errorFields) return;
      console.error('Save channel error:', error);
    } finally {
      setSavingDrawer(false);
    }
  };

  const onCurrencySaved = (updatedSettings: any) => {
    updateStoreSettings(updatedSettings);
    setFullSettings((prev: any) => ({ ...(prev || {}), ...(updatedSettings || {}) }));
    fetchFxOverview();
  };

  const notifyUrlBlock = (url: string, label: string) => (
    <div style={{
      display: 'flex', alignItems: 'center', gap: 8,
      background: 'rgba(22, 119, 255, 0.06)',
      border: '1px dashed rgba(22, 119, 255, 0.3)',
      borderRadius: 8, padding: '10px 14px', marginBottom: 8,
    }}>
      <LinkOutlined style={{ color: '#1677ff', flexShrink: 0 }} />
      <div style={{ flex: 1, minWidth: 0 }}>
        <Text type="secondary" style={{ fontSize: 11, display: 'block' }}>{label}</Text>
        <Text copyable={{ text: url }} style={{ fontFamily: 'monospace', fontSize: 12, wordBreak: 'break-all' }}>{url}</Text>
      </div>
    </div>
  );

  const channelIcon = (id: string, logoUrl?: string | null, size = 22) => {
    const url = (logoUrl || '').trim();
    if (url) {
      return <Image src={url} width={size} height={size} preview={false} style={{ objectFit: 'contain', borderRadius: 4 }} />;
    }
    const meta = getChannelMeta(id);
    const color = meta?.accent || '#666';
    if (id === 'alipay') return <AlipayCircleOutlined style={{ fontSize: size, color }} />;
    if (id === 'wechat') return <WechatOutlined style={{ fontSize: size, color }} />;
    if (id === 'allinpay') return <ThunderboltOutlined style={{ fontSize: size, color }} />;
    if (id === 'stripe' || id === 'anyway') return <CreditCardOutlined style={{ fontSize: size, color }} />;
    if (id === 'hyperbc') return <span style={{ fontSize: size - 2, fontWeight: 'bold', color }}>₿</span>;
    return <BankOutlined style={{ fontSize: size, color }} />;
  };

  const sortedChannels = useMemo(
    () => [...channels].sort((a, b) => (b.sort_order || 0) - (a.sort_order || 0) || a.id.localeCompare(b.id)),
    [channels],
  );

  const editingMeta = editingId ? getChannelMeta(editingId) : undefined;
  const editingFx = fxOverview?.channels?.find((c) => c.id === editingId);

  const renderGatewayFields = () => {
    if (!editingMeta) return null;
    const key = editingMeta.gatewayKey;

    if (key === 'payment_wechat') {
      return (
        <>
          <Alert type="info" showIcon icon={<SafetyCertificateOutlined />} style={{ borderRadius: 8 }}
            message="微信支付 API v3 接入指引"
            description={<div style={{ fontSize: 13, lineHeight: 1.8 }}>
              <div>1. 登录 <a href="https://pay.weixin.qq.com" target="_blank" rel="noreferrer">微信支付商户平台</a> 获取商户号 / API v3 密钥 / 证书</div>
              <div>2. 开通 Native 支付，并绑定公众号/小程序 AppID</div>
            </div>}
          />
          {notifyUrlBlock(notifyUrl('wechat'), '微信支付异步回调通知地址')}
          <Form.Item label="商户号 (MCHID)" name="mchid" rules={[{ required: true, message: '请输入微信支付商户号' }]}>
            <Input placeholder="例如：1900000109" />
          </Form.Item>
          <Form.Item label="应用 AppID" name="appid" rules={[{ required: true, message: '请输入绑定的 AppID' }]}>
            <Input placeholder="例如：wx8888888888888888" />
          </Form.Item>
          <Form.Item label="API v3 密钥" name="api_v3_key" rules={[{ required: true, message: '请输入 API v3 密钥' }]}>
            <Input.Password placeholder="32位字符串密钥" />
          </Form.Item>
          <Form.Item label="商户证书序列号" name="cert_serial_no" rules={[{ required: true, message: '请输入商户证书序列号' }]}>
            <Input placeholder="例如：7F5C2B3A..." />
          </Form.Item>
          <Form.Item label="商户私钥 (apiclient_key.pem)" name="private_key" rules={[{ required: true, message: '请粘贴私钥全部内容' }]}>
            <Input.TextArea rows={5} placeholder="-----BEGIN PRIVATE KEY-----" style={{ fontFamily: 'monospace', fontSize: 12 }} />
          </Form.Item>
        </>
      );
    }

    if (key === 'payment_alipay') {
      return (
        <>
          <Alert type="info" showIcon style={{ borderRadius: 8 }}
            message="支付宝电脑网站支付接入指引"
            description={<div style={{ fontSize: 13, lineHeight: 1.8 }}>
              <div>1. 登录 <a href="https://open.alipay.com" target="_blank" rel="noreferrer">支付宝开放平台</a> 创建应用并获取 App ID</div>
              <div>2. 配置 RSA2 密钥，签约电脑网站支付</div>
            </div>}
          />
          {notifyUrlBlock(notifyUrl('alipay'), '支付宝异步回调通知地址')}
          <Form.Item label="App ID" name="app_id" rules={[{ required: true, message: '请输入支付宝应用 AppID' }]}>
            <Input placeholder="例如：2021000000000000" />
          </Form.Item>
          <Form.Item label="应用私钥" name="private_key" rules={[{ required: true, message: '请输入应用私钥' }]}>
            <Input.TextArea rows={5} placeholder="粘贴 RSA2 应用私钥" style={{ fontFamily: 'monospace', fontSize: 12 }} />
          </Form.Item>
          <Form.Item label="支付宝公钥" name="alipay_public_key" rules={[{ required: true, message: '请输入支付宝公钥' }]}>
            <Input.TextArea rows={4} placeholder="粘贴支付宝公钥" style={{ fontFamily: 'monospace', fontSize: 12 }} />
          </Form.Item>
        </>
      );
    }

    if (key === 'payment_stripe') {
      return (
        <>
          <Alert type="info" showIcon style={{ borderRadius: 8 }}
            message="Stripe Checkout 接入指引"
            description={<div style={{ fontSize: 13, lineHeight: 1.8 }}>
              <div>1. 在 Stripe Dashboard 获取 Secret Key / Publishable Key</div>
              <div>2. Webhooks 监听 checkout.session.completed，填入下方回调地址</div>
            </div>}
          />
          {notifyUrlBlock(notifyUrl('stripe'), 'Stripe Webhook 回调地址')}
          <Form.Item label="Secret Key" name="secret_key" rules={[{ required: true, message: '请输入 Stripe Secret Key' }]}>
            <Input.Password placeholder="sk_live_xxxx 或 sk_test_xxxx" />
          </Form.Item>
          <Form.Item label="Publishable Key" name="publishable_key" rules={[{ required: true, message: '请输入 Stripe Publishable Key' }]}>
            <Input placeholder="pk_live_xxxx 或 pk_test_xxxx" />
          </Form.Item>
          <Form.Item label="Webhook Signing Secret" name="webhook_secret" rules={[{ required: true, message: '请输入 Webhook Secret' }]}>
            <Input.Password placeholder="whsec_xxxx" />
          </Form.Item>
        </>
      );
    }

    if (key === 'payment_hyperbc') {
      return (
        <>
          <Alert type="info" showIcon style={{ borderRadius: 8 }}
            message="HyperBC 接入指引"
            description={<div style={{ fontSize: 13, lineHeight: 1.8 }}>在 HyperBC 商户后台获取 APP_ID 与密钥，并配置回调地址</div>}
          />
          {notifyUrlBlock(notifyUrl('hyperbc'), 'HyperBC 异步回调通知地址')}
          <Form.Item label="APP_ID" name="app_id" rules={[{ required: true, message: '请输入 APP_ID' }]}>
            <Input />
          </Form.Item>
          <Form.Item label="商户 RSA 私钥" name="merchant_private_key" rules={[{ required: true, message: '请输入商户私钥' }]}>
            <Input.TextArea rows={4} style={{ fontFamily: 'monospace', fontSize: 12 }} />
          </Form.Item>
          <Form.Item label="HyperBC 平台公钥" name="hyperbc_public_key" rules={[{ required: true, message: '请输入平台公钥' }]}>
            <Input.TextArea rows={4} style={{ fontFamily: 'monospace', fontSize: 12 }} />
          </Form.Item>
          <Form.Item label="API 接口地址" name="api_url">
            <Input placeholder="https://api.cipherbc.com/shopapi" />
          </Form.Item>
          <Form.Item label="加密货币汇率" name="crypto_exchange_rate" rules={[{ required: true }]}>
            <InputNumber min={0.01} step={0.1} style={{ width: '100%' }} />
          </Form.Item>
        </>
      );
    }

    if (key === 'payment_allinpay') {
      return (
        <>
          <Alert type="info" showIcon style={{ borderRadius: 8 }}
            message="通联收银宝接入指引"
            description={<div style={{ fontSize: 13, lineHeight: 1.8 }}>
              <div>通联支付为单一通道，可在上方分别开启微信 / 支付宝子渠道；用户端默认看到「通联支付」。</div>
            </div>}
          />
          {notifyUrlBlock(notifyUrl('allinpay'), '通联支付异步回调通知地址')}
          <Form.Item label="商户号 (cusid)" name="cusid" rules={[{ required: true, message: '请输入通联商户号' }]}>
            <Input />
          </Form.Item>
          <Form.Item label="应用ID (appid)" name="appid" rules={[{ required: true, message: '请输入通联应用ID' }]}>
            <Input />
          </Form.Item>
          <Form.Item label="商户 RSA 私钥" name="merchant_private_key" rules={[{ required: true, message: '请输入商户私钥' }]}>
            <Input.TextArea rows={4} style={{ fontFamily: 'monospace', fontSize: 12 }} />
          </Form.Item>
          <Form.Item label="通联 RSA 公钥" name="allinpay_public_key" rules={[{ required: true, message: '请输入通联平台公钥' }]}>
            <Input.TextArea rows={4} style={{ fontFamily: 'monospace', fontSize: 12 }} />
          </Form.Item>
          <Form.Item label="API 接口网关地址" name="api_url" rules={[{ required: true }]} initialValue="https://vsp.allinpay.com/apiweb">
            <Input />
          </Form.Item>
          <Form.Item label="协议版本号" name="version" rules={[{ required: true }]} initialValue="11">
            <Input />
          </Form.Item>
        </>
      );
    }

    if (key === 'payment_anyway') {
      return (
        <>
          <Alert type="info" showIcon style={{ borderRadius: 8 }}
            message="Anyway Checkout 接入指引"
            description={<div style={{ fontSize: 13, lineHeight: 1.8 }}>
              <div>1. 在 <a href="https://app.anyway.sh" target="_blank" rel="noreferrer">Anyway</a> 完成企业认证与账户审核，开通银行卡 / 美元收款</div>
              <div>2. Business → Developers → API Keys 创建 Merchant API Key</div>
              <div>3. Developers → Webhooks 添加下方地址并订阅 order.paid；验签使用平台公钥，无需填写密钥</div>
              <div>4. 只收 USD：默认货币不是 USD 时，需在货币设置里开启实时汇率或配置手动汇率</div>
            </div>}
          />
          {notifyUrlBlock(notifyUrl('anyway'), 'Anyway Webhook 回调地址')}
          <Form.Item label="Merchant API Key" name="api_key" rules={[{ required: true, message: '请输入 Anyway Merchant API Key' }]}>
            <Input.Password placeholder="ak_xxxx" />
          </Form.Item>
        </>
      );
    }

    return null;
  };

  const channelColumns = [
    {
      title: 'Logo',
      key: 'logo',
      width: 68,
      align: 'center' as const,
      render: (_: unknown, record: PaymentChannelUiItem) => (
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          {channelIcon(record.id, record.logo_url, 26)}
        </div>
      ),
    },
    {
      title: '中文配置',
      key: 'zh_config',
      width: 220,
      render: (_: unknown, record: PaymentChannelUiItem) => {
        const meta = getChannelMeta(record.id);
        const nameZh = resolveChannelName(record, 'zh');
        const subHint = record.id === 'allinpay'
          ? [
              record.allinpay_wechat_enabled !== false ? '微信' : null,
              record.allinpay_alipay_enabled !== false ? '支付宝' : null,
            ].filter(Boolean).join(' / ') || '未开子渠道'
          : '';
        const isEditing = editingSubtitleField === `${record.id}:subtitle`;
        const subZhDisplay = record.subtitle || meta?.defaultSubtitle || '无';

        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            <div style={{ fontWeight: 600, fontSize: 14 }}>{nameZh}</div>
            {isEditing ? (
              <div style={{ display: 'flex', alignItems: 'center', gap: 4, marginTop: 2 }}>
                <Input
                  size="small"
                  autoFocus
                  value={editingSubtitleVal}
                  placeholder={meta?.defaultSubtitle || '无'}
                  maxLength={32}
                  allowClear
                  onChange={(e) => setEditingSubtitleVal(e.target.value)}
                  onPressEnter={() => {
                    onFieldCommit(record.id, 'subtitle', editingSubtitleVal);
                    setEditingSubtitleField(null);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === 'Escape') setEditingSubtitleField(null);
                  }}
                  style={{ width: 130 }}
                />
                <Button
                  size="small"
                  type="text"
                  icon={<CheckOutlined style={{ color: '#52c41a' }} />}
                  onClick={() => {
                    onFieldCommit(record.id, 'subtitle', editingSubtitleVal);
                    setEditingSubtitleField(null);
                  }}
                  title="保存"
                />
                <Button
                  size="small"
                  type="text"
                  icon={<CloseOutlined style={{ color: '#ff4d4f' }} />}
                  onClick={() => setEditingSubtitleField(null)}
                  title="取消"
                />
              </div>
            ) : (
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginTop: 2 }}>
                <Text
                  type="secondary"
                  style={{
                    fontSize: 13,
                    maxWidth: 160,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}
                >
                  {subZhDisplay}
                </Text>
                <Button
                  type="text"
                  size="small"
                  icon={<EditOutlined style={{ fontSize: 13, color: '#1677ff' }} />}
                  onClick={() => {
                    setEditingSubtitleField(`${record.id}:subtitle`);
                    setEditingSubtitleVal(record.subtitle ?? meta?.defaultSubtitle ?? '');
                  }}
                  title="编辑副标题"
                  style={{ padding: '0 2px', height: 20 }}
                />
              </div>
            )}
            {subHint ? (
              <Text type="secondary" style={{ fontSize: 11, display: 'block', marginTop: 2 }}>
                子渠道：{subHint}
              </Text>
            ) : null}
            {record.recharge_limit_enabled ? (
              <Text type="secondary" style={{ fontSize: 11, display: 'block', marginTop: 2 }}>
                单独限额：{formatChannelRechargeLimit(record)}
              </Text>
            ) : null}
            {record.fee_enabled && Number(record.fee_percent) > 0 ? (
              <Text type="secondary" style={{ fontSize: 11, display: 'block', marginTop: 2 }}>
                手续费：{Number(record.fee_percent)}%
              </Text>
            ) : null}
          </div>
        );
      },
    },
    {
      title: '英文配置',
      key: 'en_config',
      width: 220,
      render: (_: unknown, record: PaymentChannelUiItem) => {
        const meta = getChannelMeta(record.id);
        const nameEn = resolveChannelName(record, 'en');
        const isEditing = editingSubtitleField === `${record.id}:subtitle_en`;
        const subEnDisplay = record.subtitle_en || meta?.defaultSubtitleEn || 'None';

        return (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            <div style={{ fontWeight: 600, fontSize: 14 }}>{nameEn}</div>
            {isEditing ? (
              <div style={{ display: 'flex', alignItems: 'center', gap: 4, marginTop: 2 }}>
                <Input
                  size="small"
                  autoFocus
                  value={editingSubtitleVal}
                  placeholder={meta?.defaultSubtitleEn || 'None'}
                  maxLength={32}
                  allowClear
                  onChange={(e) => setEditingSubtitleVal(e.target.value)}
                  onPressEnter={() => {
                    onFieldCommit(record.id, 'subtitle_en', editingSubtitleVal);
                    setEditingSubtitleField(null);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === 'Escape') setEditingSubtitleField(null);
                  }}
                  style={{ width: 130 }}
                />
                <Button
                  size="small"
                  type="text"
                  icon={<CheckOutlined style={{ color: '#52c41a' }} />}
                  onClick={() => {
                    onFieldCommit(record.id, 'subtitle_en', editingSubtitleVal);
                    setEditingSubtitleField(null);
                  }}
                  title="Save"
                />
                <Button
                  size="small"
                  type="text"
                  icon={<CloseOutlined style={{ color: '#ff4d4f' }} />}
                  onClick={() => setEditingSubtitleField(null)}
                  title="Cancel"
                />
              </div>
            ) : (
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginTop: 2 }}>
                <Text
                  type="secondary"
                  style={{
                    fontSize: 13,
                    maxWidth: 160,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}
                >
                  {subEnDisplay}
                </Text>
                <Button
                  type="text"
                  size="small"
                  icon={<EditOutlined style={{ fontSize: 13, color: '#1677ff' }} />}
                  onClick={() => {
                    setEditingSubtitleField(`${record.id}:subtitle_en`);
                    setEditingSubtitleVal(record.subtitle_en ?? meta?.defaultSubtitleEn ?? '');
                  }}
                  title="Edit English subtitle"
                  style={{ padding: '0 2px', height: 20 }}
                />
              </div>
            )}
          </div>
        );
      },
    },
    {
      title: (
        <Tooltip title="渠道可收款币种，蓝色为按默认货币下单时的实际收款币种；渠道不收默认货币时按实时汇率换算收款">
          <span>支持货币</span>
        </Tooltip>
      ),
      key: 'currencies',
      render: (_: unknown, record: PaymentChannelUiItem) => {
        const row = fxOverview?.channels?.find((c) => c.id === record.id);
        if (!row) return <Text type="secondary">—</Text>;
        const shown = row.currencies.slice(0, 4);
        const rest = row.currencies.slice(4);
        return (
          <Space size={[4, 4]} wrap>
            {shown.map((code) => (
              <Tag key={code} color={code === row.pay_currency ? 'blue' : undefined} style={{ marginInlineEnd: 0 }}>{code}</Tag>
            ))}
            {rest.length > 0 && (
              <Tooltip title={rest.join(', ')}><Tag style={{ marginInlineEnd: 0 }}>+{rest.length}</Tag></Tooltip>
            )}
            {row.needs_conversion && (row.pay_rate
              ? (
                <Tooltip title={`按 1 ${fxOverview?.default_currency} = ${row.pay_rate} ${row.pay_currency} 换算收款（含加价）`}>
                  <Tag color="orange" style={{ marginInlineEnd: 0 }}>换算收款</Tag>
                </Tooltip>
              )
              : (
                <Tooltip title="实时汇率未开启，或没有可用汇率。用户端暂不展示该渠道">
                  <Tag color="error" style={{ marginInlineEnd: 0 }}>缺汇率</Tag>
                </Tooltip>
              ))}
          </Space>
        );
      },
    },
    {
      title: '排序',
      key: 'sort_order',
      width: 110,
      sorter: (a: PaymentChannelUiItem, b: PaymentChannelUiItem) => (a.sort_order || 0) - (b.sort_order || 0),
      defaultSortOrder: 'descend' as const,
      render: (_: unknown, record: PaymentChannelUiItem) => (
        <InputNumber
          size="small"
          min={0}
          max={9999}
          value={record.sort_order || 0}
          onChange={(val) => onSortChange(record.id, val ?? 0)}
          style={{ width: 88 }}
        />
      ),
    },
    {
      title: '状态',
      key: 'enabled',
      width: 90,
      render: (_: unknown, record: PaymentChannelUiItem) => (
        <Switch checked={!!record.enabled} onChange={(v) => onToggleChannel(record.id, v)} />
      ),
    },
    {
      title: '操作',
      key: 'action',
      width: 90,
      render: (_: unknown, record: PaymentChannelUiItem) => (
        <Button type="link" icon={<SettingOutlined />} onClick={() => openConfig(record.id)}>
          配置
        </Button>
      ),
    },
  ];

  const handleToggleLevelInvoice = async (record: any, checked: boolean) => {
    setUpdatingLevelId(record.id);
    const targetVal = checked ? 1 : 0;
    try {
      await request.put(`/user_levels/${record.id}`, {
        invoice_enabled: targetVal,
      });
      message.success(`已${checked ? '开启' : '关闭'}等级「${record.name}」的发票与收据权限`);
      setUserLevels((prev) =>
        prev.map((l) => (l.id === record.id ? { ...l, invoice_enabled: targetVal } : l))
      );
    } catch (e: any) {
      console.error('Failed to toggle invoice enabled for user level:', e);
      message.error(e?.message || '更新用户等级发票权限失败');
    } finally {
      setUpdatingLevelId(null);
    }
  };

  const handleToggleAllLevels = async (enable: boolean) => {
    if (userLevels.length === 0) return;
    const targetVal = enable ? 1 : 0;
    const needUpdate = userLevels.filter((l) => (l.invoice_enabled ?? 0) !== targetVal);
    if (needUpdate.length === 0) {
      message.info(enable ? '所有用户等级已全部处于开启状态' : '所有用户等级已全部处于关闭状态');
      return;
    }

    setBatchUpdating(true);
    try {
      // 乐观更新前端状态
      setUserLevels((prev) => prev.map((l) => ({ ...l, invoice_enabled: targetVal })));
      if (enable) {
        setShowLevelDetails(false);
      }
      // 并发持久化至后端数据库
      await Promise.all(
        needUpdate.map((l) =>
          request.put(`/user_levels/${l.id}`, {
            invoice_enabled: targetVal,
          })
        )
      );
      message.success(
        enable
          ? `已成功开启全部用户等级的发票与收据权限（共 ${totalLevelsCount} 个等级）`
          : `已成功关闭全部用户等级的发票与收据权限（共 ${totalLevelsCount} 个等级）`
      );
    } catch (e: any) {
      console.error('Failed to toggle all user levels invoice permission:', e);
      message.error(e?.message || '批量更新用户等级开票权限失败，正在重新获取最新状态');
      await fetchUserLevels();
    } finally {
      setBatchUpdating(false);
    }
  };

  const handleBatchToggleSelectedLevels = async (enable: boolean) => {
    if (selectedLevelKeys.length === 0) {
      message.warning('请先勾选需要批量操作的用户等级');
      return;
    }
    const targetVal = enable ? 1 : 0;
    const selectedLevels = userLevels.filter((l) => selectedLevelKeys.includes(l.id));
    const needUpdate = selectedLevels.filter((l) => (l.invoice_enabled ?? 0) !== targetVal);

    if (needUpdate.length === 0) {
      message.info(enable ? '所选等级已全部处于开启状态' : '所选等级已全部处于关闭状态');
      return;
    }

    setBatchUpdating(true);
    try {
      setUserLevels((prev) =>
        prev.map((l) => (selectedLevelKeys.includes(l.id) ? { ...l, invoice_enabled: targetVal } : l))
      );
      await Promise.all(
        needUpdate.map((l) =>
          request.put(`/user_levels/${l.id}`, {
            invoice_enabled: targetVal,
          })
        )
      );
      message.success(
        enable
          ? `已批量开启所选 ${selectedLevelKeys.length} 个等级的发票权限`
          : `已批量关闭所选 ${selectedLevelKeys.length} 个等级的发票权限`
      );
      setSelectedLevelKeys([]);
    } catch (e: any) {
      console.error('Failed to batch toggle selected levels invoice permission:', e);
      message.error(e?.message || '批量更新选中等级开票权限失败');
      await fetchUserLevels();
    } finally {
      setBatchUpdating(false);
    }
  };

  // ── 多开票主体（销方资质）管理 ──
  const handleOpenAddSeller = () => {
    setEditingSeller(null);
    formSeller.resetFields();
    formSeller.setFieldsValue({
      contact_email: '',
      company_phone: '',
      company_address: '',
      company_website: '',
      support_normal: true,
      support_special: !isChinaMode,
      is_default: sellerSubjects.length === 0,
      enabled: true,
    });
    setSellerModalOpen(true);
  };

  const handleOpenEditSeller = (seller: SellerInvoiceSubject) => {
    setEditingSeller(seller);
    formSeller.setFieldsValue({
      company_name: seller.company_name,
      tax_id: seller.tax_id,
      contact_email: seller.contact_email || '',
      company_phone: seller.company_phone || '',
      company_address: seller.company_address || '',
      company_website: seller.company_website || seller.website || '',
      support_normal: seller.support_normal !== false,
      support_special: !!seller.support_special,
      is_default: !!seller.is_default,
      enabled: seller.enabled !== false,
    });
    setSellerModalOpen(true);
  };

  const persistSellerSubjects = async (nextList: SellerInvoiceSubject[]) => {
    const currentInvoicesValues = formInvoices.getFieldsValue();
    const defaultSeller = nextList.find((s) => s.is_default) || nextList[0];
    const payload = {
      invoices: {
        ...(fullSettings?.invoices || {}),
        ...currentInvoicesValues,
        invoice_enabled: currentInvoicesValues.invoice_enabled !== false,
        invoice_sellers: nextList,
        invoice_company_name: defaultSeller?.company_name || '',
        invoice_tax_id: defaultSeller?.tax_id || '',
        invoice_contact_email: defaultSeller?.contact_email || '',
        invoice_company_address: defaultSeller?.company_address || '',
        invoice_company_phone: defaultSeller?.company_phone || '',
        invoice_company_website: defaultSeller?.company_website || defaultSeller?.website || '',
      },
    };
    const updatedSettings = await (request.post('/settings', payload) as any);
    updateStoreSettings(updatedSettings);
    setFullSettings((prev: any) => ({
      ...(prev || {}),
      invoices: payload.invoices,
    }));
    setSellerSubjects(nextList);
    return updatedSettings;
  };

  const handleSaveSellerModal = async () => {
    try {
      const vals = await formSeller.validateFields();
      if (isChinaMode && !vals.support_normal && !vals.support_special) {
        message.error('请至少选择支持一种发票类型（普通发票或专用发票）');
        return;
      }
      setSavingSellerModal(true);
      const isDef = !!vals.is_default || sellerSubjects.length === 0;
      const normalizedVals = {
        ...vals,
        contact_email: vals.contact_email?.trim() || '',
        company_phone: vals.company_phone?.trim() || '',
        company_address: vals.company_address?.trim() || '',
        company_website: (vals.company_website || vals.website)?.trim() || '',
        support_normal: isChinaMode ? !!vals.support_normal : true,
        support_special: isChinaMode ? !!vals.support_special : true,
      };
      let nextList: SellerInvoiceSubject[];
      if (editingSeller) {
        nextList = sellerSubjects.map((s) => {
          if (s.id === editingSeller.id) {
            return {
              ...s,
              ...normalizedVals,
              id: s.id,
              is_default: isDef,
            };
          }
          return isDef ? { ...s, is_default: false } : s;
        });
      } else {
        const newSubject: SellerInvoiceSubject = {
          id: `seller_${Date.now()}`,
          ...normalizedVals,
          is_default: isDef,
        };
        nextList = isDef
          ? [...sellerSubjects.map((s) => ({ ...s, is_default: false })), newSubject]
          : [...sellerSubjects, newSubject];
      }

      await persistSellerSubjects(nextList);
      setSellerModalOpen(false);
      message.success(editingSeller ? '开票主体已保存' : '开票主体已添加并保存');
    } catch (err: any) {
      if (err?.errorFields) return;
      console.error(err);
      message.error(err?.message || '保存失败');
    } finally {
      setSavingSellerModal(false);
    }
  };

  const handleDeleteSeller = async (id: string) => {
    if (sellerSubjects.length <= 1) {
      message.warning('系统需保留至少一个开票主体');
      return;
    }
    const deleted = sellerSubjects.find((s) => s.id === id);
    let next = sellerSubjects.filter((s) => s.id !== id);
    if (deleted?.is_default && next.length > 0) {
      next[0].is_default = true;
    }
    try {
      await persistSellerSubjects(next);
      message.success('已移除开票主体');
    } catch (e: any) {
      console.error(e);
      message.error(e?.message || '删除失败');
    }
  };

  const handleSetDefaultSeller = async (id: string) => {
    const next = sellerSubjects.map((s) => ({ ...s, is_default: s.id === id }));
    try {
      await persistSellerSubjects(next);
    } catch (e: any) {
      console.error(e);
      message.error(e?.message || '切换失败');
    }
  };

  const handleSaveInvoices = async (values: any) => {
    setSavingInvoices(true);
    try {
      const defaultSeller = sellerSubjects.find((s) => s.is_default) || sellerSubjects[0];
      const payload = {
        invoices: {
          ...values,
          invoice_enabled: values.invoice_enabled !== false,
          invoice_show_invoices: values.invoice_show_invoices !== false,
          invoice_show_receipts: values.invoice_show_receipts !== false,
          invoice_sellers: sellerSubjects,
          // 兼容旧字段
          invoice_company_name: defaultSeller?.company_name || values.invoice_company_name || '',
          invoice_tax_id: defaultSeller?.tax_id || values.invoice_tax_id || '',
          invoice_contact_email: defaultSeller?.contact_email || values.invoice_contact_email || '',
          invoice_company_address: defaultSeller?.company_address || values.invoice_company_address || '',
        },
      };
      const updatedSettings = await (request.post('/settings', payload) as any);
      updateStoreSettings(updatedSettings);
      setFullSettings((prev: any) => ({
        ...(prev || {}),
        invoices: payload.invoices,
      }));
      message.success('Invoices 发票与收据全局配置保存成功');
    } catch (e: any) {
      console.error('Failed to save invoices settings:', e);
      message.error(e?.message || '保存发票设置失败');
    } finally {
      setSavingInvoices(false);
    }
  };

  const userLevelColumns = [
    {
      title: 'ID',
      dataIndex: 'id',
      key: 'id',
      width: 70,
    },
    {
      title: '用户等级名称',
      dataIndex: 'name',
      key: 'name',
      render: (name: string, record: any) => (
        <Space>
          <span style={{ fontWeight: 600 }}>{name}</span>
          {record.is_default === 1 && <Tag color="green">默认等级</Tag>}
        </Space>
      ),
    },
    {
      title: '分组标识 (group_key)',
      dataIndex: 'group_key',
      key: 'group_key',
      render: (val: string) => <Tag color="blue">{val}</Tag>,
    },
    {
      title: '专属折扣',
      dataIndex: 'discount',
      key: 'discount',
      render: (val: number, record: any) => {
        const dt = record.discount_type ?? 0;
        const mask = dt >= 16 ? (dt & 15) : (dt === 1 ? 6 : dt === 2 ? 3 : 15);
        const hasLevelDisc = (mask & 1) !== 0;
        const num = Number(val ?? 1);
        return hasLevelDisc ? (
          <span style={{ fontFamily: 'monospace' }}>{formatRate(num)}x</span>
        ) : (
          <span style={{ fontFamily: 'monospace', color: '#94a3b8' }} title="未勾选用户等级折扣，此等级倍率未启用">
            <span style={{ textDecoration: 'line-through' }}>{formatRate(num)}x</span>
            <span style={{ fontSize: 11, marginLeft: 4 }}>(未启用)</span>
          </span>
        );
      },
    },
    {
      title: '发票开具权限状态',
      dataIndex: 'invoice_enabled',
      key: 'invoice_enabled',
      width: 160,
      render: (enabled: number) => {
        const isOk = enabled === 1;
        return (
          <span
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 6,
              padding: '2px 10px',
              borderRadius: 9999,
              fontSize: 12,
              fontWeight: 500,
              backgroundColor: isOk ? 'rgba(16, 185, 129, 0.1)' : 'rgba(148, 163, 184, 0.12)',
              color: isOk ? '#059669' : '#64748b',
              border: `1px solid ${isOk ? 'rgba(16, 185, 129, 0.28)' : 'rgba(148, 163, 184, 0.25)'}`,
            }}
          >
            <span
              style={{
                width: 6,
                height: 6,
                borderRadius: '50%',
                backgroundColor: isOk ? '#10b981' : '#94a3b8',
              }}
            />
            {isOk ? '已开通使用' : '已关闭隐藏'}
          </span>
        );
      },
    },
    {
      title: '快捷开启/关闭发票功能',
      key: 'action',
      width: 180,
      render: (_: any, record: any) => (
        <Switch
          checked={record.invoice_enabled === 1}
          disabled={!isInvoiceServiceEnabled}
          loading={updatingLevelId === record.id || batchUpdating}
          checkedChildren="已开启"
          unCheckedChildren="已关闭"
          onChange={(checked) => handleToggleLevelInvoice(record, checked)}
        />
      ),
    },
  ];

  const tabItems = [
    {
      key: 'currency',
      label: t('menu.currency_settings', '货币设置'),
      children: (
        <CurrencySettingsTab
          currency={fullSettings?.currency}
          supportedLanguages={fullSettings?.site?.supported_languages || []}
          overview={fxOverview}
          onOverviewChange={setFxOverview}
          onSaved={onCurrencySaved}
        />
      ),
    },
    {
      key: 'channels',
      label: '支付渠道',
      children: (
        <div>
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 16, borderRadius: 8 }}
            message="支付渠道列表"
            description="排序数字越大越靠前。中文与英文副标题默认直接展示，点击后方编辑按钮可进行修改，留空则使用系统默认。点击「配置」可修改网关参数、Logo及子渠道。用户端中文站点显示中文配置，其他语言显示英文配置。"
          />
          <Table
            rowKey="id"
            loading={loadingChannels}
            dataSource={sortedChannels}
            columns={channelColumns}
            pagination={false}
            size="middle"
          />
        </div>
      ),
    },
    {
      key: 'invoices',
      label: '收据与发票',
      children: (
        <div>
          {/* 1. 发票与收据全局参数设置 */}
          <div>
            <div style={{ marginBottom: 16 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <FileTextOutlined style={{ color: '#722ed1', fontSize: 16 }} />
                <span style={{ fontWeight: 600, fontSize: 14 }}>发票与收据全局业务参数配置</span>
              </div>
            </div>
            <Form
              form={formInvoices}
              layout="vertical"
              onFinish={handleSaveInvoices}
              autoComplete="off"
            >
              <div style={{ marginBottom: 20, padding: '14px 16px', background: 'var(--ant-color-fill-quaternary)', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'space-between', border: '1px solid var(--ant-color-border-secondary)' }}>
                <div>
                  <div style={{ fontWeight: 600, fontSize: 14 }}>开启 Invoices 收据与发票服务</div>
                </div>
                <Form.Item name="invoice_enabled" valuePropName="checked" noStyle>
                  <Switch checkedChildren="已开启" unCheckedChildren="已关闭" />
                </Form.Item>
              </div>

              <Form.Item
                name="invoice_mode"
                label="发票模式选择"
                rules={[{ required: true, message: '请选择发票模式' }]}
              >
                <Radio.Group optionType="button" buttonStyle="solid">
                  <Radio.Button value="international">🌐 国际模式 (海外模式 - Invoices & Receipts)</Radio.Button>
                  <Radio.Button value="china">
                    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                      <RegionFlagSvg code="CNY" />
                      <span>中国大陆境内模式 (增值税发票申请)</span>
                    </span>
                  </Radio.Button>
                </Radio.Group>
              </Form.Item>

              <Divider style={{ margin: '24px 0 20px' }} />

              {/* 2. 各用户等级开票权限快捷配置（实时双向同步） */}
          <div style={{ opacity: isInvoiceServiceEnabled ? 1 : 0.65, transition: 'opacity 0.2s ease-in-out', marginBottom: 28 }}>
            {!isInvoiceServiceEnabled && (
              <Alert
                type="warning"
                showIcon
                style={{ marginBottom: 16, borderRadius: 8 }}
                message="发票与收据全局服务未开启"
                description="需在上方开启「Invoices 收据与发票服务」总开关，下方方可配置各用户等级的开票权限。"
              />
            )}
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                flexWrap: 'wrap',
                gap: 12,
                marginBottom: 14,
              }}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  <CheckCircleOutlined style={{ color: '#10b981', fontSize: 16 }} />
                  <span style={{ fontWeight: 600, fontSize: 14 }}>各用户等级开票权限配置（实时双向同步）</span>
                </div>
                <span
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 6,
                    padding: '1px 8px',
                    borderRadius: 9999,
                    fontSize: 12,
                    fontWeight: 500,
                    backgroundColor: allLevelsEnabled
                      ? 'rgba(16, 185, 129, 0.1)'
                      : noneLevelsEnabled
                      ? 'rgba(148, 163, 184, 0.12)'
                      : 'rgba(59, 130, 246, 0.1)',
                    color: allLevelsEnabled ? '#059669' : noneLevelsEnabled ? '#64748b' : '#2563eb',
                    border: `1px solid ${
                      allLevelsEnabled
                        ? 'rgba(16, 185, 129, 0.28)'
                        : noneLevelsEnabled
                        ? 'rgba(148, 163, 184, 0.25)'
                        : 'rgba(59, 130, 246, 0.28)'
                    }`,
                  }}
                >
                  <span
                    style={{
                      width: 6,
                      height: 6,
                      borderRadius: '50%',
                      backgroundColor: allLevelsEnabled ? '#10b981' : noneLevelsEnabled ? '#94a3b8' : '#3b82f6',
                    }}
                  />
                  已开启 {enabledLevelsCount} / {totalLevelsCount} 个等级
                </span>
              </div>
              <span style={{ fontSize: 12, color: 'var(--ant-color-text-secondary)' }}>
                开启后，属于该等级的用户登录后即可在左侧「用户设置」访问「财务发票」二级菜单
              </span>
            </div>
            {/* 顶部总控栏 (shadcn 风格) */}
            <div
              style={{
                display: 'flex',
                flexWrap: 'wrap',
                alignItems: 'center',
                justifyContent: 'space-between',
                gap: 12,
                padding: '12px 16px',
                marginBottom: 16,
                background: 'var(--ant-color-fill-quaternary)',
                border: '1px solid var(--ant-color-border-secondary)',
                borderRadius: 8,
              }}
            >
              {/* 左侧：全部等级统一开关 + 状态 */}
              <div style={{ display: 'flex', alignItems: 'center', gap: 14, flexWrap: 'wrap' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span style={{ fontWeight: 600, fontSize: 13 }}>全部等级统一开启</span>
                  <Switch
                    checked={allLevelsEnabled}
                    loading={batchUpdating}
                    disabled={!isInvoiceServiceEnabled}
                    checkedChildren="已全开"
                    unCheckedChildren="未全开"
                    onChange={(checked) => handleToggleAllLevels(checked)}
                  />
                </div>
                {allLevelsEnabled && (
                  <Button
                    type="link"
                    size="small"
                    onClick={() => setShowLevelDetails((prev) => !prev)}
                    style={{ fontSize: 12, padding: 0 }}
                  >
                    {showLevelDetails ? '收起各等级明细' : '按等级单独配置 / 查看明细'}
                  </Button>
                )}
              </div>

              {/* 右侧：一键全部开启 / 一键全部关闭 快捷操作组 */}
              <Space size={8} wrap>
                <Button
                  size="small"
                  type="primary"
                  icon={<CheckCircleOutlined />}
                  loading={batchUpdating}
                  disabled={!isInvoiceServiceEnabled || allLevelsEnabled || totalLevelsCount === 0}
                  onClick={() => handleToggleAllLevels(true)}
                  style={{
                    backgroundColor: '#10b981',
                    borderColor: '#10b981',
                    borderRadius: 6,
                    fontWeight: 500,
                  }}
                >
                  一键全部开启
                </Button>

                <Popconfirm
                  title="确认关闭全部用户等级的发票权限？"
                  description="关闭后，所有等级的普通用户将无法在个人中心使用发票与交易凭单功能（管理员不受影响）。"
                  okText="确定关闭"
                  cancelText="取消"
                  okButtonProps={{ danger: true }}
                  disabled={!isInvoiceServiceEnabled || noneLevelsEnabled || totalLevelsCount === 0 || batchUpdating}
                  onConfirm={() => handleToggleAllLevels(false)}
                >
                  <Button
                    size="small"
                    danger
                    icon={<StopOutlined />}
                    loading={batchUpdating}
                    disabled={!isInvoiceServiceEnabled || noneLevelsEnabled || totalLevelsCount === 0}
                    style={{ borderRadius: 6, fontWeight: 500 }}
                  >
                    一键全部关闭
                  </Button>
                </Popconfirm>

                <Button
                  size="small"
                  icon={<ReloadOutlined />}
                  loading={loadingUserLevels || batchUpdating}
                  onClick={fetchUserLevels}
                  style={{ borderRadius: 6 }}
                >
                  刷新列表
                </Button>
              </Space>
            </div>

            {(!allLevelsEnabled || showLevelDetails) ? (
              <>
                {/* 多选批量操作工具栏 */}
                {selectedLevelKeys.length > 0 && (
                  <div
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      padding: '8px 14px',
                      marginBottom: 12,
                      background: 'rgba(59, 130, 246, 0.08)',
                      border: '1px solid rgba(59, 130, 246, 0.25)',
                      borderRadius: 8,
                    }}
                  >
                    <span style={{ fontSize: 13, color: '#2563eb', fontWeight: 500 }}>
                      已勾选 {selectedLevelKeys.length} 个用户等级
                    </span>
                    <Space size={8}>
                      <Button
                        size="small"
                        type="primary"
                        icon={<CheckOutlined />}
                        loading={batchUpdating}
                        disabled={!isInvoiceServiceEnabled}
                        onClick={() => handleBatchToggleSelectedLevels(true)}
                        style={{ borderRadius: 6, fontSize: 12 }}
                      >
                        批量开启所选
                      </Button>
                      <Button
                        size="small"
                        danger
                        icon={<CloseOutlined />}
                        loading={batchUpdating}
                        disabled={!isInvoiceServiceEnabled}
                        onClick={() => handleBatchToggleSelectedLevels(false)}
                        style={{ borderRadius: 6, fontSize: 12 }}
                      >
                        批量关闭所选
                      </Button>
                      <Button
                        size="small"
                        type="text"
                        onClick={() => setSelectedLevelKeys([])}
                        style={{ fontSize: 12 }}
                      >
                        取消选择
                      </Button>
                    </Space>
                  </div>
                )}

                <Table
                  rowKey="id"
                  loading={loadingUserLevels || batchUpdating}
                  dataSource={userLevels}
                  columns={userLevelColumns}
                  pagination={false}
                  size="middle"
                  rowSelection={{
                    selectedRowKeys: selectedLevelKeys,
                    onChange: (keys) => setSelectedLevelKeys(keys),
                    getCheckboxProps: () => ({
                      disabled: !isInvoiceServiceEnabled,
                    }),
                  }}
                />
              </>
            ) : (
              <div
                style={{
                  padding: '12px 16px',
                  borderRadius: 8,
                  backgroundColor: 'rgba(16, 185, 129, 0.05)',
                  border: '1px dashed rgba(16, 185, 129, 0.28)',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  flexWrap: 'wrap',
                  gap: 10,
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <CheckCircleOutlined style={{ color: '#10b981', fontSize: 14 }} />
                  <span style={{ fontSize: 13, color: 'var(--ant-color-text-secondary)' }}>
                    已开启「全部等级统一开启」，所有用户等级（共 {totalLevelsCount} 个）均已默认开通发票与收据权限，各等级明细已自动精简折叠。
                  </span>
                </div>
                <Button
                  type="link"
                  size="small"
                  onClick={() => setShowLevelDetails(true)}
                  style={{ fontSize: 12, padding: 0 }}
                >
                  按等级自定义配置
                </Button>
              </div>
            )}
          </div>

              <Divider style={{ margin: '24px 0 20px' }} />

              {/* 🏢 开票主体与销方基础信息 (支持多主体及普票/专票资质配置) */}
              <div
                style={{
                  background: 'var(--ant-color-bg-container)',
                  border: '1px solid var(--ant-color-border-secondary)',
                  borderRadius: 8,
                  padding: '16px 18px',
                  marginTop: 14,
                  marginBottom: 16,
                }}
              >
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    flexWrap: 'wrap',
                    gap: 10,
                    marginBottom: 14,
                  }}
                >
                  <div>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontWeight: 600, fontSize: 14 }}>
                      <BankOutlined style={{ color: '#1677ff' }} />
                      <span>{isChinaMode ? '开票主体与销方基础信息 (Company & Seller Info)' : '开票主体信息 (Issuer / Seller Info)'}</span>
                      <span
                        style={{
                          fontSize: 12,
                          fontWeight: 'normal',
                          color: 'var(--ant-color-text-secondary)',
                          marginLeft: 4,
                        }}
                      >
                        （共 {sellerSubjects.length} 个主体）
                      </span>
                    </div>
                    <div style={{ fontSize: 12, color: 'var(--ant-color-text-secondary)', marginTop: 2 }}>
                      {isChinaMode
                        ? '配置对外开票的销方主体。支持增设多个主体，并独立配置是否支持开具增值税普通发票与增值税专用发票资质。'
                        : '配置对外开具 Receipt & Invoice 的收款销方主体，支持增设多个主体并设置默认开票主体。'}
                    </div>
                  </div>

                  <Button
                    type="primary"
                    size="small"
                    icon={<PlusOutlined />}
                    onClick={handleOpenAddSeller}
                    style={{
                      borderRadius: 6,
                      background: isLight ? '#09090b' : '#fafafa',
                      color: isLight ? '#fafafa' : '#09090b',
                    }}
                  >
                    添加开票主体
                  </Button>
                </div>

                {/* 主体列表 */}
                <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                  {sellerSubjects.map((seller) => {
                    const isDef = !!seller.is_default;
                    return (
                      <div
                        key={seller.id}
                        style={{
                          padding: '12px 16px',
                          borderRadius: 6,
                          border: isDef
                            ? (isLight ? '1px solid #10b981' : '1px solid #059669')
                            : '1px solid var(--ant-color-border-secondary)',
                          background: isDef
                            ? (isLight ? 'rgba(16, 185, 129, 0.04)' : 'rgba(16, 185, 129, 0.08)')
                            : 'var(--ant-color-fill-quaternary)',
                          display: 'flex',
                          alignItems: 'center',
                          justifyContent: 'space-between',
                          flexWrap: 'wrap',
                          gap: 12,
                        }}
                      >
                        {/* 左侧主体信息与资质标签 */}
                        <div style={{ flex: 1, minWidth: 260 }}>
                          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginBottom: 4 }}>
                            <span style={{ fontWeight: 600, fontSize: 14 }}>{seller.company_name}</span>
                            {isDef && (
                              <Tag color="success" style={{ borderRadius: 4, fontSize: 11, margin: 0 }}>
                                默认主体
                              </Tag>
                            )}
                            {/* 境内模式展示普票/专票支持资质标签 */}
                            {isChinaMode && (
                              <>
                                <Tag
                                  style={{
                                    borderRadius: 4,
                                    fontSize: 11,
                                    margin: 0,
                                    background: seller.support_normal
                                      ? (isLight ? '#eff6ff' : '#1e293b')
                                      : (isLight ? '#f4f4f5' : '#27272a'),
                                    color: seller.support_normal ? '#2563eb' : '#a1a1aa',
                                    border: seller.support_normal
                                      ? '1px solid #bfdbfe'
                                      : '1px solid var(--ant-color-border-secondary)',
                                  }}
                                >
                                  {seller.support_normal ? '✓ 增值税普通发票' : '✕ 不支持普票'}
                                </Tag>
                                <Tag
                                  style={{
                                    borderRadius: 4,
                                    fontSize: 11,
                                    margin: 0,
                                    background: seller.support_special
                                      ? (isLight ? '#faf5ff' : '#3b0764')
                                      : (isLight ? '#f4f4f5' : '#27272a'),
                                    color: seller.support_special ? '#9333ea' : '#a1a1aa',
                                    border: seller.support_special
                                      ? '1px solid #e9d5ff'
                                      : '1px solid var(--ant-color-border-secondary)',
                                  }}
                                >
                                  {seller.support_special ? '★ 增值税专用发票' : '✕ 不支持专票'}
                                </Tag>
                              </>
                            )}
                          </div>

                          <div
                            style={{
                              display: 'flex',
                              alignItems: 'center',
                              gap: 16,
                              fontSize: 12,
                              color: 'var(--ant-color-text-secondary)',
                              flexWrap: 'wrap',
                            }}
                          >
                            <span>
                              {isChinaMode ? '税号: ' : '营业执照号码: '}
                              <Text code style={{ fontSize: 11 }}>{seller.tax_id || '未配置'}</Text>
                            </span>
                            {seller.contact_email && (
                              <span>
                                邮箱: <Text style={{ fontSize: 12 }}>{seller.contact_email}</Text>
                              </span>
                            )}
                            {seller.company_phone && (
                              <span>
                                电话: <Text style={{ fontSize: 12 }}>{seller.company_phone}</Text>
                              </span>
                            )}
                            {seller.company_address && (
                              <span>
                                地址: <Text style={{ fontSize: 12 }}>{seller.company_address}</Text>
                              </span>
                            )}
                            {(seller.company_website || (seller as any).website) && (
                              <span>
                                网站: <Text style={{ fontSize: 12 }}>{seller.company_website || (seller as any).website}</Text>
                              </span>
                            )}
                          </div>
                        </div>

                        {/* 右侧操作按钮组 */}
                        <Space size={8}>
                          {!isDef && (
                            <Button
                              size="small"
                              onClick={() => handleSetDefaultSeller(seller.id)}
                              style={{ borderRadius: 4, fontSize: 12 }}
                            >
                              设为默认
                            </Button>
                          )}
                          <Button
                            size="small"
                            icon={<EditOutlined />}
                            onClick={() => handleOpenEditSeller(seller)}
                            style={{ borderRadius: 4, fontSize: 12 }}
                          >
                            编辑
                          </Button>
                          <Popconfirm
                            title="确定删除此开票主体？"
                            description="删除后，该主体将从开票主体列表中移除。"
                            okText="删除"
                            cancelText="取消"
                            okButtonProps={{ danger: true }}
                            onConfirm={() => handleDeleteSeller(seller.id)}
                          >
                            <Button
                              size="small"
                              danger
                              type="text"
                              icon={<DeleteOutlined />}
                              disabled={sellerSubjects.length <= 1}
                              style={{ borderRadius: 4, fontSize: 12 }}
                            />
                          </Popconfirm>
                        </Space>
                      </div>
                    );
                  })}
                </div>
              </div>

              <Form.Item noStyle dependencies={['invoice_mode']}>
                {({ getFieldValue }) => {
                  const mode = getFieldValue('invoice_mode') || 'international';
                  if (mode === 'china') {
                    return (
                      <Card
                        size="small"
                        title={
                          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                            <RegionFlagSvg code="CNY" />
                            <span>中国大陆境内发票设置 (China Domestic Invoice Settings)</span>
                          </span>
                        }
                        style={{ background: 'var(--ant-color-bg-container)', marginTop: 12, marginBottom: 16 }}
                      >
                        <Row gutter={16}>
                          <Col xs={24} md={12}>
                            <Form.Item
                              name="china_min_amount"
                              label="最低开票金额限制 (元)"
                              extra="单次申请开票的最低充值消费金额门槛"
                            >
                              <InputNumber style={{ width: '100%' }} min={0} step={10} precision={2} />
                            </Form.Item>
                          </Col>
                          <Col xs={24} md={12}>
                            <Form.Item
                              name="china_tax_rate"
                              label="发票税率 (%)"
                              extra="通常为 6% 或 3%"
                            >
                              <InputNumber style={{ width: '100%' }} min={0} max={100} step={1} precision={2} />
                            </Form.Item>
                          </Col>
                        </Row>
                        <Form.Item
                          name="china_invoice_content"
                          label="发票内容 / 货物或应税劳务名称"
                          extra="如：*信息技术服务*软件技术服务费"
                        >
                          <Input placeholder="*信息技术服务*软件技术服务费" />
                        </Form.Item>
                        <Form.Item
                          name="china_invoice_notice"
                          label="开票说明与注意事项"
                          extra="展示在用户端申请开票界面的温馨提示"
                        >
                          <Input.TextArea rows={3} placeholder="增值税普通发票将在审核通过后发送至填写的邮箱..." />
                        </Form.Item>
                      </Card>
                    );
                  }

                  return (
                    <Card size="small" title="🌐 国际模式凭单与展示配置 (International Format & Card Settings)" style={{ background: 'var(--ant-color-bg-container)', marginTop: 12, marginBottom: 16 }}>
                      <div style={{ marginBottom: 16, fontSize: 13, color: 'var(--ant-color-text-secondary)' }}>
                        💡 以下配置信息将直接呈现在用户端商业发票（Commercial Invoice）与付款收据（Payment Receipt）的打印/预览文件中。
                      </div>
                      <Row gutter={16}>
                        <Col xs={24} md={8}>
                          <Form.Item
                            name="invoice_currency_symbol"
                            label="货币符号 (Currency Symbol)"
                          >
                            <Input placeholder="$" />
                          </Form.Item>
                        </Col>
                        <Col xs={24} md={8}>
                          <Form.Item
                            name="invoice_prefix"
                            label="发票编号前缀 (Invoice No. Prefix)"
                          >
                            <Input placeholder="INV-" />
                          </Form.Item>
                        </Col>
                        <Col xs={24} md={8}>
                          <Form.Item
                            name="receipt_prefix"
                            label="收据编号前缀 (Receipt No. Prefix)"
                          >
                            <Input placeholder="REC-" />
                          </Form.Item>
                        </Col>
                      </Row>
                      <Form.Item
                        name="invoice_notes"
                        label="底部附言与条款 (Default Notes & Terms)"
                        extra="将显示在发票和收据底部的感谢语或业务须知"
                      >
                        <Input.TextArea rows={2} placeholder="Thank you for your business! Payment has been processed in full." />
                      </Form.Item>
                      <Space size={24} style={{ marginTop: 8 }}>
                        <Form.Item
                          name="invoice_show_invoices"
                          label="展示 Invoices 账单列表卡片"
                          valuePropName="checked"
                        >
                          <Switch />
                        </Form.Item>
                        <Form.Item
                          name="invoice_show_receipts"
                          label="展示 Receipts 交易凭证卡片"
                          valuePropName="checked"
                        >
                          <Switch />
                        </Form.Item>
                      </Space>
                    </Card>
                  );
                }}
              </Form.Item>

              <Form.Item style={{ marginTop: 16 }}>
                <Button type="primary" htmlType="submit" loading={savingInvoices}>
                  {t('common.save', '保存发票全局设置')}
                </Button>
              </Form.Item>
            </Form>
          </div>
        </div>
      ),
    },
  ];

  const getPageTitle = () => {
    if (editingId) {
      return (
        <Space size={12}>
          <Button icon={<ArrowLeftOutlined />} onClick={closeEditor} size="small">
            返回
          </Button>
          <span>配置支付渠道 · {editingMeta?.defaultName || editingId}</span>
        </Space>
      );
    }
    return t('menu.payment_settings', '支付设置');
  };

  return (
    <Card
      bordered={false}
      title={getPageTitle()}
      style={{ borderRadius: 12 }}
      styles={editingId ? { body: { padding: '4px 20px 8px' } } : undefined}
    >
      <style>{`
        .settings-compact-form .ant-form-item {
          margin-bottom: 12px;
        }
        .settings-compact-form .ant-form-item-label {
          padding-bottom: 2px;
        }
        .settings-compact-form .ant-form-item-label > label {
          font-size: 13px;
          font-weight: 500;
        }
        .settings-compact-form .ant-form-item-extra {
          font-size: 12px;
          margin-top: 2px;
          line-height: 1.4;
        }
        .settings-compact-form .ant-card-body {
          padding: 16px 20px;
        }
        .settings-compact-form .ant-tabs-nav {
          margin-bottom: 14px;
        }
        .channel-edit-compact .ant-form-item {
          margin-bottom: 8px;
        }
        .channel-edit-compact .ant-divider-horizontal {
          margin: 8px 0;
        }
        .channel-edit-compact .ant-alert {
          margin-bottom: 8px;
        }
      `}</style>
      {!editingId ? (
        <div className="settings-compact-form">
          <Tabs activeKey={activeTab} onChange={handleTabChange} items={tabItems} />
        </div>
      ) : (
        <div style={{ animation: 'fadeIn 0.3s' }} className="settings-compact-form channel-edit-compact">
          <div style={{ maxWidth: 760, width: '100%' }}>
            <Form form={formDisplay} layout="vertical" autoComplete="off">
              <Divider plain>用户端展示</Divider>

              <div style={{
                display: 'flex',
                flexWrap: 'wrap',
                alignItems: 'center',
                gap: 16,
                marginBottom: 8,
              }}>
                <Form.Item label="启用该渠道" name="enabled" valuePropName="checked" style={{ marginBottom: 0 }}>
                  <Switch />
                </Form.Item>
                {editingId === 'allinpay' && (
                  <>
                    <Form.Item
                      label="微信子渠道"
                      name="allinpay_wechat_enabled"
                      valuePropName="checked"
                      style={{ marginBottom: 0 }}
                    >
                      <Switch />
                    </Form.Item>
                    <Form.Item
                      label="支付宝子渠道"
                      name="allinpay_alipay_enabled"
                      valuePropName="checked"
                      style={{ marginBottom: 0 }}
                    >
                      <Switch />
                    </Form.Item>
                  </>
                )}
                <Form.Item
                  label="排序权重"
                  name="sort_order"
                  style={{ marginBottom: 0 }}
                >
                  <InputNumber min={0} max={9999} style={{ width: 100 }} placeholder="0" />
                </Form.Item>
              </div>

              <Row gutter={16}>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={
                      <Space size={4}>
                        <span>{editingMeta?.defaultName || editingId}</span>
                        <Text type="secondary" style={{ fontSize: 12, fontWeight: 'normal' }}>（中文副标题）</Text>
                      </Space>
                    }
                    name="subtitle"
                    extra={`留空使用默认：${editingMeta?.defaultSubtitle || '无'}`}
                  >
                    <Input placeholder={editingMeta?.defaultSubtitle} allowClear maxLength={32} />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={
                      <Space size={4}>
                        <span>{editingMeta?.defaultNameEn || editingMeta?.defaultName || editingId}</span>
                        <Text type="secondary" style={{ fontSize: 12, fontWeight: 'normal' }}>（英文副标题）</Text>
                      </Space>
                    }
                    name="subtitle_en"
                    extra={`留空使用默认：${editingMeta?.defaultSubtitleEn || 'None'}`}
                  >
                    <Input placeholder={editingMeta?.defaultSubtitleEn} allowClear maxLength={32} />
                  </Form.Item>
                </Col>
              </Row>

              <Form.Item label="Logo 图片 URL" name="logo_url" extra="留空则使用系统默认图标">
                <Input placeholder="https://..." allowClear />
              </Form.Item>

              <Form.Item
                label="收款币种"
                name="currencies"
                extra={editingId === 'stripe'
                  ? '按顺序优先：包含站点默认货币时直接按默认货币收款，否则需在货币设置中开启实时汇率后按第一个币种换算。留空表示支持全部。'
                  : `该渠道固定按 ${(editingFx?.capable || []).join(' / ') || '—'} 收款。与默认货币不同时，需在货币设置中开启实时汇率后才换算。`}
              >
                <Select
                  mode="multiple"
                  allowClear
                  disabled={editingId !== 'stripe'}
                  placeholder={editingId === 'stripe' ? '全部币种' : (editingFx?.capable || []).join(' / ')}
                  options={(editingFx?.capable || []).map((code) => ({ value: code, label: code }))}
                />
              </Form.Item>

              <Divider plain>充值金额限制</Divider>
              <Form.Item
                label="单独限制充值金额"
                name="recharge_limit_enabled"
                valuePropName="checked"
                extra={editingId === 'allinpay'
                  ? '关闭时使用「通用充值设置」的最小/最大金额。开启后，通联微信和支付宝都按下面的金额校验钱包充值。0 表示这一侧不限制。'
                  : '关闭时使用「通用充值设置」的最小/最大金额。开启后，只按下面的金额校验该渠道的钱包充值。0 表示这一侧不限制。'}
              >
                <Switch />
              </Form.Item>
              <Row gutter={16} style={{ display: rechargeLimitOn ? undefined : 'none' }}>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label="最小充值金额"
                    name="min_recharge_amount"
                    rules={[{
                      validator: async (_, value) => {
                        if (!formDisplay.getFieldValue('recharge_limit_enabled')) return;
                        if (value === undefined || value === null || value === '') {
                          throw new Error('请输入最小充值金额');
                        }
                      },
                    }]}
                  >
                    <InputNumber style={{ width: '100%' }} min={0} step={1} />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label="最大充值金额"
                    name="max_recharge_amount"
                    rules={[{
                      validator: async (_, value) => {
                        if (!formDisplay.getFieldValue('recharge_limit_enabled')) return;
                        if (value === undefined || value === null || value === '') {
                          throw new Error('请输入最大充值金额');
                        }
                        const min = Number(formDisplay.getFieldValue('min_recharge_amount')) || 0;
                        const max = Number(value) || 0;
                        if (min > 0 && max > 0 && min > max) {
                          throw new Error('最小充值金额不能大于最大充值金额');
                        }
                      },
                    }]}
                  >
                    <InputNumber style={{ width: '100%' }} min={0} step={1} />
                  </Form.Item>
                </Col>
              </Row>

              <Divider plain>支付手续费</Divider>
              <Form.Item
                label="收取手续费"
                name="fee_enabled"
                valuePropName="checked"
                extra={editingId === 'allinpay'
                  ? '开启后，通联微信和支付宝的钱包充值都在充值金额之外按百分比加收手续费，到账金额不变，手续费写入充值记录备注。卡券订单不收。'
                  : '开启后，该渠道的钱包充值在充值金额之外按百分比加收手续费，到账金额不变，手续费写入充值记录备注。卡券订单不收。'}
              >
                <Switch />
              </Form.Item>
              <Form.Item
                label="手续费百分比"
                name="fee_percent"
                style={{ display: feeOn ? undefined : 'none' }}
                extra="按充值金额计算，向上取整到分。例如 3 表示充值 100 需支付 103。"
                rules={[{
                  validator: async (_, value) => {
                    if (!formDisplay.getFieldValue('fee_enabled')) return;
                    const n = Number(value);
                    if (!(n > 0 && n <= 50)) {
                      throw new Error('手续费百分比须大于 0 且不超过 50');
                    }
                  },
                }]}
              >
                <InputNumber style={{ width: '100%' }} min={0} max={50} step={0.1} precision={2} addonAfter="%" />
              </Form.Item>
            </Form>

            <Form form={formGateway} layout="vertical" autoComplete="off">
              <Divider plain>网关参数</Divider>
              {renderGatewayFields()}
            </Form>

            <Space style={{ marginTop: 8, marginBottom: 8 }}>
              <Button type="primary" loading={savingDrawer} onClick={onSaveDrawer}>
                {t('common.save', '保存配置')}
              </Button>
              <Button onClick={closeEditor}>
                {t('common.cancel', '取消')}
              </Button>
            </Space>
          </div>
        </div>
      )}

      {/* 添加 / 编辑开票主体与销方资质 Modal */}
      <Modal
        title={
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <BankOutlined style={{ color: '#1677ff' }} />
            <span>
              {editingSeller
                ? (isChinaMode ? '编辑开票主体与销方资质' : '编辑开票主体')
                : '添加开票主体'}
            </span>
          </div>
        }
        open={sellerModalOpen}
        onCancel={() => setSellerModalOpen(false)}
        onOk={handleSaveSellerModal}
        confirmLoading={savingSellerModal}
        okText="确定"
        cancelText="取消"
        width={540}
        destroyOnClose
      >
        <Form form={formSeller} layout="vertical" style={{ marginTop: 16 }}>
          <Form.Item
            name="company_name"
            label="开票主体 / 销方公司名称"
            rules={[{ required: true, message: '请输入开票主体公司名称' }]}
          >
            <Input placeholder={isChinaMode ? "如：北京数字未来信息技术有限公司" : "如：FUSION ELECTRONICS TECHNOLOGY LIMITED"} />
          </Form.Item>

          <Form.Item
            name="tax_id"
            label={isChinaMode ? "纳税人识别号 / 统一社会信用代码" : "营业执照号码"}
            rules={[{ required: true, message: isChinaMode ? '请输入纳税人识别号' : '请输入营业执照号码' }]}
          >
            <Input placeholder={isChinaMode ? "如：91110108MA01XXXXXX" : "如：No.77440427 或商业登记证号"} />
          </Form.Item>

          <Row gutter={16}>
            <Col xs={24} sm={12}>
              <Form.Item
                name="contact_email"
                label="联系邮箱"
                rules={[
                  {
                    type: 'email',
                    message: '请输入有效的邮箱地址',
                    transform: (value) => (value ? value.trim() : value),
                  },
                ]}
              >
                <Input placeholder="如：billing@company.com" allowClear />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12}>
              <Form.Item
                name="company_phone"
                label="联系电话"
              >
                <Input placeholder="如：+1 555-0199 或 010-88888888" allowClear />
              </Form.Item>
            </Col>
          </Row>

          <Form.Item
            name="company_address"
            label="公司地址"
          >
            <Input placeholder={isChinaMode ? "如：北京市海淀区中关村南大街1号" : "如：100 Innovation Way, Suite 300, San Francisco, CA 94107"} allowClear />
          </Form.Item>

          <Form.Item
            name="company_website"
            label="网站地址"
          >
            <Input placeholder="如：https://www.example.com" allowClear />
          </Form.Item>

          {/* 境内模式：核心资质配置属性 */}
          {isChinaMode && (
            <div
              style={{
                padding: '12px 14px',
                background: 'var(--ant-color-fill-quaternary)',
                border: '1px solid var(--ant-color-border-secondary)',
                borderRadius: 6,
                marginBottom: 16,
              }}
            >
              <div style={{ fontWeight: 600, fontSize: 13, marginBottom: 8 }}>
                支持的发票类型资质 (至少勾选一项)
              </div>
              <Row gutter={[16, 10]}>
                <Col xs={24} sm={12}>
                  <Form.Item name="support_normal" valuePropName="checked" noStyle>
                    <Checkbox>增值税普通发票</Checkbox>
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item name="support_special" valuePropName="checked" noStyle>
                    <Checkbox>增值税专用发票</Checkbox>
                  </Form.Item>
                </Col>
              </Row>
            </div>
          )}

          <Form.Item name="is_default" valuePropName="checked" style={{ marginTop: 8, marginBottom: 0 }}>
            <Checkbox>设为默认开票主体（系统优先采用此主体进行开票）</Checkbox>
          </Form.Item>
        </Form>
      </Modal>
    </Card>
  );
};

export default PaymentSettings;
