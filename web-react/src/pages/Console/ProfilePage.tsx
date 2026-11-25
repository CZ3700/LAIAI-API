import React, { useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Form,
  Input,
  Modal,
  Space,
  Tag,
  Typography,
  message,
} from 'antd';
import { LockOutlined, SafetyCertificateOutlined, UserOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import request, { coreApi } from '../../utils/request';
import useAuthStore from '../../store/auth';

export const ProfilePage: React.FC = () => {
  const { t } = useTranslation();
  const { user, setUser } = useAuthStore();
  const [profile, setProfile] = useState<any>(user);
  const [savingProfile, setSavingProfile] = useState(false);
  const [savingPwd, setSavingPwd] = useState(false);
  const [setupData, setSetupData] = useState<{ secret?: string; otpauth_url?: string } | null>(null);
  const [totpCode, setTotpCode] = useState('');
  const [profileForm] = Form.useForm();
  const [pwdForm] = Form.useForm();

  const loadProfile = async () => {
    try {
      const res: any = await request.get('/user/profile');
      setProfile(res);
      setUser(res);
      profileForm.setFieldsValue({ display_name: res?.display_name || res?.username });
    } catch {
      // handled
    }
  };

  useEffect(() => {
    void loadProfile();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const onSaveProfile = async (vals: { display_name: string }) => {
    setSavingProfile(true);
    try {
      const res: any = await request.put('/user/profile', { display_name: vals.display_name.trim() });
      setProfile(res);
      setUser(res);
      message.success(t('common.saved', '个人资料已更新'));
    } finally {
      setSavingProfile(false);
    }
  };

  const onChangePassword = async (vals: { old_password: string; new_password: string }) => {
    setSavingPwd(true);
    try {
      await coreApi.post('/me/password', {
        current_password: vals.old_password,
        new_password: vals.new_password,
      });
      pwdForm.resetFields();
      message.success(t('profile.password_changed', '密码已更新'));
    } finally {
      setSavingPwd(false);
    }
  };

  const onSetup2FA = async () => {
    const res: any = await coreApi.post('/me/2fa/setup');
    setSetupData(res);
  };

  const onEnable2FA = async () => {
    await coreApi.post('/me/2fa/enable', { code: totpCode.trim() });
    message.success(t('profile.2fa_enabled', '两步验证已开启'));
    setSetupData(null);
    setTotpCode('');
    await loadProfile();
  };

  const onDisable2FA = async () => {
    const code = window.prompt(t('profile.enter_2fa_disable', '请输入当前 TOTP 验证码以关闭 2FA'));
    if (!code) return;
    await coreApi.post('/me/2fa/disable', { code: code.trim() });
    message.success(t('profile.2fa_disabled', '两步验证已关闭'));
    await loadProfile();
  };

  return (
    <Space direction="vertical" size={16} style={{ width: '100%', maxWidth: 820 }}>
      <Card
        title={
          <Space>
            <UserOutlined />
            <span>{t('menu.profile', '账号与安全设置')}</span>
          </Space>
        }
      >
        <Descriptions bordered size="small" column={2} style={{ marginBottom: 20 }}>
          <Descriptions.Item label="UID">{profile?.uid || profile?.id}</Descriptions.Item>
          <Descriptions.Item label={t('auth.email', '邮箱')}>{profile?.email}</Descriptions.Item>
          <Descriptions.Item label="Role">
            <Tag color="indigo">{profile?.plat_role || profile?.role || 'user'}</Tag>
          </Descriptions.Item>
          <Descriptions.Item label="2FA">
            <Tag color={profile?.totp_enabled ? 'green' : 'default'}>
              {profile?.totp_enabled ? 'Enabled' : 'Disabled'}
            </Tag>
          </Descriptions.Item>
        </Descriptions>

        <Form form={profileForm} layout="inline" onFinish={onSaveProfile}>
          <Form.Item name="display_name" label={t('auth.display_name', '显示名称')} rules={[{ required: true }]}>
            <Input style={{ width: 240 }} />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit" loading={savingProfile}>
              {t('common.save', '保存资料')}
            </Button>
          </Form.Item>
        </Form>
      </Card>

      <Card
        title={
          <Space>
            <LockOutlined />
            <span>{t('profile.change_password', '修改密码')}</span>
          </Space>
        }
      >
        <Form form={pwdForm} layout="vertical" style={{ maxWidth: 400 }} onFinish={onChangePassword}>
          <Form.Item name="old_password" label={t('profile.current_password', '当前密码')} rules={[{ required: true }]}>
            <Input.Password />
          </Form.Item>
          <Form.Item
            name="new_password"
            label={t('profile.new_password', '新密码')}
            rules={[{ required: true }, { min: 8 }]}
          >
            <Input.Password />
          </Form.Item>
          <Button type="primary" htmlType="submit" loading={savingPwd}>
            {t('profile.update_password', '更新密码')}
          </Button>
        </Form>
      </Card>

      <Card
        title={
          <Space>
            <SafetyCertificateOutlined />
            <span>{t('profile.2fa_title', '两步验证 (TOTP 2FA)')}</span>
          </Space>
        }
      >
        {profile?.totp_enabled ? (
          <Button danger onClick={onDisable2FA}>
            {t('profile.disable_2fa', '关闭 2FA')}
          </Button>
        ) : (
          <Button type="primary" onClick={onSetup2FA}>
            {t('profile.setup_2fa', '绑定 TOTP 验证器')}
          </Button>
        )}
      </Card>

      <Modal
        title={t('profile.setup_2fa', '配置 TOTP 两步验证')}
        open={Boolean(setupData)}
        onCancel={() => setSetupData(null)}
        onOk={onEnable2FA}
      >
        {setupData && (
          <Space direction="vertical" size={12} style={{ width: '100%' }}>
            <Alert
              type="info"
              message={t('profile.2fa_hint', '请在 Google Authenticator / 1Password 中添加密钥并输入 6 位验证码确认。')}
            />
            <Typography.Paragraph copyable code>
              {setupData.secret}
            </Typography.Paragraph>
            {setupData.otpauth_url && (
              <Typography.Text type="secondary" style={{ fontSize: 12, wordBreak: 'break-all' }}>
                {setupData.otpauth_url}
              </Typography.Text>
            )}
            <Input
              placeholder={t('auth.totp_placeholder', '6 位验证码')}
              value={totpCode}
              onChange={(e) => setTotpCode(e.target.value)}
            />
          </Space>
        )}
      </Modal>
    </Space>
  );
};

export default ProfilePage;
