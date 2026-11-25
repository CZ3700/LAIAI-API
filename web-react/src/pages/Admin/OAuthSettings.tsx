import React, { useEffect, useState } from 'react';
import { Card, Form, Input, Button, message, Typography, Tabs, Space } from 'antd';
import { CopyOutlined, LinkOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import useSettingsStore from '../../store/settings';
import { copyWithFeedback } from '../../utils/clipboard';

const { Text } = Typography;

const OAuthSettings: React.FC = () => {
  const { t } = useTranslation();
  const { updateStoreSettings } = useSettingsStore();
  const [googleForm] = Form.useForm();
  const [wechatForm] = Form.useForm();
  const [loading, setLoading] = useState(false);
  const [siteUrl, setSiteUrl] = useState('');

  useEffect(() => {
    (async () => {
      try {
        const res = await (request.get('/settings/full') as any);
        if (res.google_oauth) googleForm.setFieldsValue(res.google_oauth);
        if (res.wechat_oauth) wechatForm.setFieldsValue(res.wechat_oauth);
        setSiteUrl(window.location.origin);
      } catch { /* ignore */ }
    })();
  }, []);

  const handleSaveGoogle = async () => {
    setLoading(true);
    try {
      const values = await googleForm.validateFields();
      const res = await (request.post('/settings', { google_oauth: values }) as any);
      message.success(t('settings.save_success'));
      updateStoreSettings(res);
    } catch { /* ignore */ }
    finally { setLoading(false); }
  };

  const handleSaveWechat = async () => {
    setLoading(true);
    try {
      const values = await wechatForm.validateFields();
      const res = await (request.post('/settings', { wechat_oauth: values }) as any);
      message.success(t('settings.save_success'));
      updateStoreSettings(res);
    } catch { /* ignore */ }
    finally { setLoading(false); }
  };

  const copyToClipboard = async (text: string) => {
    await copyWithFeedback(text, '已复制到剪贴板', '复制失败');
  };

  const googleCallbackUrl = `${siteUrl}/api/v1/auth/oauth/google/callback`;
  const wechatCallbackDomain = siteUrl.replace(/^https?:\/\//, '');

  const googleTab = (
    <Form form={googleForm} layout="vertical" autoComplete="off" style={{ maxWidth: 600 }}>
      <Form.Item label={t('settings.google_client_id')} name="client_id" rules={[{ required: true }]}>
        <Input placeholder="xxxx.apps.googleusercontent.com" />
      </Form.Item>
      <Form.Item label={t('settings.google_client_secret')} name="client_secret" rules={[{ required: true }]}>
        <Input.Password placeholder="请输入 Client Secret" />
      </Form.Item>
      <Form.Item label={t('settings.callback_url')}>
        <Space.Compact style={{ display: 'flex' }}>
          <Input value={googleCallbackUrl} readOnly />
          <Button icon={<CopyOutlined />} onClick={() => copyToClipboard(googleCallbackUrl)}>{t('settings.copy_callback_url')}</Button>
        </Space.Compact>
        <Text type="secondary" style={{ display: 'block', marginTop: 4 }}>请将此地址添加到 Google Cloud Console 的「已授权的重定向 URI」中</Text>
      </Form.Item>
      <Space>
        <Button type="primary" onClick={handleSaveGoogle} loading={loading}>{t('common.save')}</Button>
        <Button type="link" icon={<LinkOutlined />} href="https://console.cloud.google.com/apis/credentials" target="_blank">
          {t('settings.doc_link')}
        </Button>
      </Space>
    </Form>
  );

  const wechatTab = (
    <Form form={wechatForm} layout="vertical" autoComplete="off" style={{ maxWidth: 600 }}>
      <Form.Item label={t('settings.wechat_app_id')} name="app_id" rules={[{ required: true }]}>
        <Input placeholder="wxxxxxxxxxxx" />
      </Form.Item>
      <Form.Item label={t('settings.wechat_app_secret')} name="app_secret" rules={[{ required: true }]}>
        <Input.Password placeholder="请输入网站应用密钥" />
      </Form.Item>
      <Form.Item
        label={t('settings.wechat_callback_domain_config', '授权回调域名')}
        name="callback_domain"
        extra={
          <Text type="secondary" style={{ fontSize: 12, display: 'block', marginTop: 4 }}>
            当前访问域名：<Text copyable={{ text: wechatCallbackDomain }}>{wechatCallbackDomain}</Text>
            （<a onClick={() => wechatForm.setFieldValue('callback_domain', wechatCallbackDomain)}>填入</a>）。留空默认跟随当前访问域名；多域名站点请填微信开放平台绑定的老域名。
          </Text>
        }
      >
        <Input placeholder={wechatCallbackDomain} allowClear />
      </Form.Item>
      <Space>
        <Button type="primary" onClick={handleSaveWechat} loading={loading}>{t('common.save')}</Button>
        <Button type="link" icon={<LinkOutlined />} href="https://developers.weixin.qq.com/doc/oplatform/Website_App/WeChat_Login/Wechat_Login.html" target="_blank">
          {t('settings.doc_link')}
        </Button>
      </Space>
    </Form>
  );

  return (
    <Card bordered={false} title={t('settings.oauth_title')} style={{ borderRadius: 12 }}>
      <Tabs items={[
        { key: 'google', label: t('settings.google_oauth_tab'), children: googleTab },
        { key: 'wechat', label: t('settings.wechat_oauth_tab'), children: wechatTab },
      ]} />
    </Card>
  );
};

export default OAuthSettings;
