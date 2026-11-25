import React, { useEffect, useState } from 'react';
import { Alert, Button, Card, Form, Input, Space, Typography, message } from 'antd';
import { LockOutlined, MailOutlined, SafetyCertificateOutlined, UserOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import useAuthStore from '../../store/auth';
import useSettingsStore from '../../store/settings';
import { useThemeStore } from '../../store/theme';
import { clearAwaitingFreshSetup, fetchAdminInitStatus } from '../../utils/freshSetup';
import { solidAccent } from '../../theme/tokens';

export const AdminLogin: React.FC = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { setAuth } = useAuthStore();
  const { settings } = useSettingsStore();
  const { themeMode } = useThemeStore();

  const [initialized, setInitialized] = useState<boolean>(true);
  const [checking, setChecking] = useState(true);
  const [loading, setLoading] = useState(false);

  const adminPath = settings?.site?.admin_path || 'admin';
  const isLight = themeMode === 'light';
  const accent = solidAccent(isLight ? 'light' : 'dark');

  useEffect(() => {
    fetchAdminInitStatus()
      .then((init) => setInitialized(init))
      .catch(() => setInitialized(true))
      .finally(() => setChecking(false));
  }, []);

  const onFinish = async (values: { email: string; password: string; display_name?: string }) => {
    setLoading(true);
    try {
      if (!initialized) {
        const res: any = await request.post('/auth/init-admin', {
          email: values.email.trim(),
          password: values.password,
          display_name: values.display_name?.trim() || 'Administrator',
        });
        if (res?.token) {
          clearAwaitingFreshSetup();
          setAuth(res.user ?? null, res.token);
          message.success(t('admin.init_success', '超级管理员初始化成功'));
          navigate(`/${adminPath}/dashboard`, { replace: true });
        }
        return;
      }

      const res: any = await request.post('/auth/login', {
        email: values.email.trim(),
        password: values.password,
      });
      if (res?.token) {
        const u = res.user;
        if (u?.role !== 'admin' && u?.plat_role !== 'admin' && u?.plat_role !== 'superadmin') {
          message.error(t('admin.not_admin', '该账号没有管理后台权限'));
          return;
        }
        setAuth(u, res.token);
        message.success(t('auth.login_success', '登录成功'));
        navigate(`/${adminPath}/dashboard`, { replace: true });
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
      <Card style={{ width: '100%', maxWidth: 440 }} styles={{ body: { padding: 32 } }} loading={checking}>
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
              <SafetyCertificateOutlined />
            </div>
            <Typography.Title level={3} style={{ margin: 0 }}>
              {initialized ? t('admin.login_title', '管理后台登录') : t('admin.init_title', '初始化超级管理员')}
            </Typography.Title>
          </div>

          {!initialized && (
            <Alert
              type="info"
              showIcon
              message={t('admin.init_hint', '检测到平台尚未创建管理员账号，请创建首个超级管理员。')}
            />
          )}

          <Form layout="vertical" onFinish={onFinish} size="large">
            {!initialized && (
              <Form.Item name="display_name" label={t('auth.display_name', '管理员名称')} initialValue="Administrator">
                <Input prefix={<UserOutlined />} />
              </Form.Item>
            )}
            <Form.Item
              name="email"
              label={t('auth.email', '管理员邮箱')}
              rules={[{ required: true, message: t('auth.email_required', '请输入邮箱') }]}
            >
              <Input prefix={<MailOutlined />} placeholder="admin@company.com" autoComplete="email" />
            </Form.Item>
            <Form.Item
              name="password"
              label={t('auth.password', '密码')}
              rules={[{ required: true, message: t('auth.password_required', '请输入密码') }]}
            >
              <Input.Password prefix={<LockOutlined />} placeholder="••••••••" autoComplete="current-password" />
            </Form.Item>
            <Button type="primary" htmlType="submit" loading={loading} block>
              {initialized ? t('admin.enter', '进入管理后台') : t('admin.create_superadmin', '创建并登录')}
            </Button>
          </Form>
        </Space>
      </Card>
    </div>
  );
};

export default AdminLogin;
