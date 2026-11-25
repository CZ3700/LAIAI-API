import React, { useState } from 'react';
import { Button, Card, Form, Input, Space, Typography, message } from 'antd';
import { LockOutlined, MailOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import useAuthStore from '../../store/auth';
import useSettingsStore from '../../store/settings';
import { useThemeStore } from '../../store/theme';
import { solidAccent } from '../../theme/tokens';

export const Login: React.FC = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const { setAuth } = useAuthStore();
  const { settings } = useSettingsStore();
  const { themeMode } = useThemeStore();

  const [loading, setLoading] = useState(false);
  const [twofaToken, setTwofaToken] = useState<string | null>(null);
  const [totpCode, setTotpCode] = useState('');

  const isLight = themeMode === 'light';
  const accent = solidAccent(isLight ? 'light' : 'dark');
  const siteTitle = settings?.site?.title || 'LAIAI';
  const from = (location.state as { from?: string } | null)?.from || '/dashboard';

  const onFinish = async (values: { email: string; password: string }) => {
    setLoading(true);
    try {
      const res: any = await request.post('/auth/login', {
        email: values.email.trim(),
        password: values.password,
      });
      if (res?.requires_2fa && res?.token) {
        setTwofaToken(res.token);
        message.info(t('auth.enter_2fa', '请输入两步验证码'));
        return;
      }
      if (res?.token) {
        setAuth(res.user ?? null, res.token);
        message.success(t('auth.login_success', '登录成功'));
        navigate(from, { replace: true });
      }
    } catch {
      // handled by interceptor
    } finally {
      setLoading(false);
    }
  };

  const onVerify2FA = async () => {
    if (!twofaToken || !totpCode.trim()) return;
    setLoading(true);
    try {
      const res: any = await request.post('/auth/2fa', {
        token: twofaToken,
        code: totpCode.trim(),
      });
      const tok = res?.access_token || res?.token;
      if (tok) {
        setAuth(res.user ?? null, tok);
        const boot: any = await request.get('/console/bootstrap');
        if (boot?.user) {
          setAuth(boot.user, tok);
        }
        message.success(t('auth.login_success', '登录成功'));
        navigate(from, { replace: true });
      }
    } catch {
      // handled by interceptor
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
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
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
              {siteTitle}
            </Typography.Title>
            <Typography.Text type="secondary">
              {t('auth.login_subtitle', '登录您的 AI Gateway 控制台')}
            </Typography.Text>
          </div>

          {!twofaToken ? (
            <Form layout="vertical" onFinish={onFinish} size="large">
              <Form.Item
                name="email"
                label={t('auth.email', '邮箱')}
                rules={[{ required: true, message: t('auth.email_required', '请输入邮箱') }]}
              >
                <Input prefix={<MailOutlined />} placeholder="name@company.com" autoComplete="email" />
              </Form.Item>
              <Form.Item
                name="password"
                label={t('auth.password', '密码')}
                rules={[{ required: true, message: t('auth.password_required', '请输入密码') }]}
              >
                <Input.Password prefix={<LockOutlined />} placeholder="••••••••" autoComplete="current-password" />
              </Form.Item>
              <Button type="primary" htmlType="submit" loading={loading} block>
                {t('auth.sign_in', '登录')}
              </Button>
            </Form>
          ) : (
            <Space direction="vertical" size={12} style={{ width: '100%' }}>
              <Input
                size="large"
                prefix={<SafetyCertificateOutlined />}
                placeholder={t('auth.totp_placeholder', '输入 6 位 TOTP 或恢复码')}
                value={totpCode}
                onChange={(e) => setTotpCode(e.target.value)}
                onPressEnter={onVerify2FA}
              />
              <Button type="primary" size="large" loading={loading} block onClick={onVerify2FA}>
                {t('auth.verify_2fa', '验证并登录')}
              </Button>
              <Button type="text" block onClick={() => setTwofaToken(null)}>
                {t('common.back', '返回')}
              </Button>
            </Space>
          )}

          <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 13 }}>
            <Link to="/home/models">{t('menu.marketplace', '模型广场')}</Link>
            <Space size={12}>
              <Link to="/docs/quickstart/doc0003">{t('menu.docs', '接口文档')}</Link>
              <Link to="/register">{t('auth.create_account', '注册账号')}</Link>
            </Space>
          </div>
        </Space>
      </Card>
    </div>
  );
};

export default Login;
