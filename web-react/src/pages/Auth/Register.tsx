import React, { useState } from 'react';
import { Button, Card, Form, Input, Space, Typography, message } from 'antd';
import { LockOutlined, MailOutlined, SafetyOutlined, UserOutlined } from '@ant-design/icons';
import { Link, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import useAuthStore from '../../store/auth';
import useSettingsStore from '../../store/settings';
import { useThemeStore } from '../../store/theme';
import { solidAccent } from '../../theme/tokens';

export const Register: React.FC = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { setAuth } = useAuthStore();
  const { settings } = useSettingsStore();
  const { themeMode } = useThemeStore();
  const [form] = Form.useForm();

  const [loading, setLoading] = useState(false);
  const [sendingCode, setSendingCode] = useState(false);

  const isLight = themeMode === 'light';
  const accent = solidAccent(isLight ? 'light' : 'dark');
  const emailVerification = Boolean(settings?.registration?.email_verification_enabled);

  const onSendCode = async () => {
    const email = String(form.getFieldValue('email') || '').trim();
    if (!email) {
      message.warning(t('auth.email_required', '请先输入邮箱'));
      return;
    }
    setSendingCode(true);
    try {
      await request.post('/auth/send-code', { email });
      message.success(t('auth.code_sent', '验证码已发送'));
    } catch {
      // handled
    } finally {
      setSendingCode(false);
    }
  };

  const onFinish = async (values: {
    email: string;
    password: string;
    display_name?: string;
    code?: string;
  }) => {
    setLoading(true);
    try {
      const res: any = await request.post('/auth/register', {
        email: values.email.trim(),
        password: values.password,
        display_name: values.display_name?.trim(),
        code: values.code?.trim(),
      });
      const tok = res?.token || res?.access_token;
      if (tok) {
        setAuth(res.user ?? null, tok);
        message.success(t('auth.register_success', '注册成功'));
        navigate('/dashboard', { replace: true });
      } else {
        message.success(t('auth.register_success', '注册成功，请登录'));
        navigate('/login');
      }
    } catch {
      // handled
    } finally {
      setLoading(false);
    }
  };

  return (
    <div
      style={{
        minHeight: '100vh',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        padding: 24,
      }}
    >
      <Card style={{ width: '100%', maxWidth: 420 }} styles={{ body: { padding: 32 } }}>
        <Space orientation="vertical" size={16} style={{ width: '100%' }}>
          <div style={{ textAlign: 'center' }}>
            <div
              style={{
                width: 44,
                height: 44,
                borderRadius: 12,
                background: accent.background,
                color: accent.color,
                display: 'inline-flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontWeight: 700,
                fontSize: 20,
                marginBottom: 12,
              }}
            >
              L
            </div>
            <Typography.Title level={3} style={{ margin: 0 }}>
              {t('auth.create_account', '创建账号')}
            </Typography.Title>
          </div>

          <Form form={form} layout="vertical" onFinish={onFinish} size="large">
            <Form.Item
              name="email"
              label={t('auth.email', '邮箱')}
              rules={[
                { required: true, message: t('auth.email_required', '请输入邮箱') },
                { type: 'email', message: t('auth.email_invalid', '邮箱格式不正确') },
              ]}
            >
              <Input prefix={<MailOutlined />} placeholder="name@company.com" autoComplete="email" />
            </Form.Item>

            <Form.Item name="display_name" label={t('auth.display_name', '昵称')}>
              <Input prefix={<UserOutlined />} placeholder={t('auth.display_name_placeholder', '可选')} />
            </Form.Item>

            {emailVerification && (
              <Form.Item name="code" label={t('auth.verification_code', '邮箱验证码')} rules={[{ required: true }]}>
                <Space.Compact style={{ width: '100%' }}>
                  <Input prefix={<SafetyOutlined />} placeholder="6 位验证码" />
                  <Button loading={sendingCode} onClick={onSendCode}>
                    {t('auth.send_code', '发送')}
                  </Button>
                </Space.Compact>
              </Form.Item>
            )}

            <Form.Item
              name="password"
              label={t('auth.password', '密码')}
              rules={[
                { required: true, message: t('auth.password_required', '请输入密码') },
                { min: 8, message: t('auth.password_min', '至少 8 位字符') },
              ]}
            >
              <Input.Password prefix={<LockOutlined />} placeholder="••••••••" autoComplete="new-password" />
            </Form.Item>

            <Button type="primary" htmlType="submit" loading={loading} block>
              {t('auth.register', '注册')}
            </Button>
          </Form>

          <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 13 }}>
            <Link to="/home/models">{t('menu.marketplace', '模型广场')}</Link>
            <Link to="/login">{t('auth.back_to_login', '已有账号？登录')}</Link>
          </div>
        </Space>
      </Card>
    </div>
  );
};

export default Register;
