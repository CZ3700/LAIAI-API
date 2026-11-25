import React, { useEffect, useState } from 'react';
import { Form, Input, Button, message, Typography, Space, Radio } from 'antd';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import useSettingsStore from '../../store/settings';
import LowBalanceSmsHint from '../../components/admin/TencentLowBalanceSmsHint';
import { apiErrMsg, SKIP_ERR } from '../../utils/apiErr';

type SmsProvider = 'tencent' | 'volcengine';

const SmsNotification: React.FC = () => {
  const { t } = useTranslation();
  const { updateStoreSettings } = useSettingsStore();
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);
  const [testMobile, setTestMobile] = useState('');
  const [testLoading, setTestLoading] = useState(false);
  const provider = (Form.useWatch('provider', form) || 'tencent') as SmsProvider;
  const isVolc = provider === 'volcengine';

  useEffect(() => {
    (async () => {
      try {
        const res = await (request.get('/settings/full') as any);
        if (res.sms) {
          form.setFieldsValue({
            ...res.sms,
            provider: res.sms.provider === 'volcengine' ? 'volcengine' : 'tencent',
            code_param: res.sms.code_param?.trim() || 'code',
          });
        } else {
          form.setFieldsValue({ provider: 'tencent', code_param: 'code' });
        }
      } catch { /* ignore */ }
    })();
  }, []);

  const handleSave = async () => {
    if (loading) return;
    setLoading(true);
    try {
      const values = await form.validateFields();
      const res = await (request.post('/settings', { sms: values }, SKIP_ERR as any) as any);
      message.success(t('settings.save_success'));
      updateStoreSettings(res);
    } catch (e: any) {
      if (e?.errorFields) return;
      message.error(apiErrMsg(e, t('common.error')));
    } finally {
      setLoading(false);
    }
  };

  const handleTest = async () => {
    if (testLoading) return;
    if (!testMobile) {
      message.warning(t('settings.test_mobile_placeholder'));
      return;
    }
    setTestLoading(true);
    try {
      const res = await (request.post(
        '/settings/sms/test',
        { mobile: testMobile },
        SKIP_ERR as any,
      ) as any);
      res.success ? message.success(res.message) : message.error(res.message);
    } catch (e: any) {
      message.error(apiErrMsg(e, '发送失败'));
    } finally {
      setTestLoading(false);
    }
  };

  return (
    <div style={{ paddingTop: 12 }}>
      <Form
        form={form}
        layout="vertical"
        autoComplete="off"
        style={{ maxWidth: 600 }}
        initialValues={{ provider: 'tencent', code_param: 'code' }}
      >
        <Form.Item
          label={t('settings.sms_provider', '短信服务商')}
          name="provider"
          rules={[{ required: true }]}
          extra={t(
            'settings.sms_provider_hint',
            '当前仅保存一套凭证；切换服务商后请填写该服务商的密钥与模板，发送以当前选中为准。',
          )}
        >
          <Radio.Group
            optionType="button"
            options={[
              { value: 'tencent', label: t('settings.sms_provider_tencent', '腾讯云') },
              { value: 'volcengine', label: t('settings.sms_provider_volcengine', '火山引擎') },
            ]}
          />
        </Form.Item>

        <Form.Item
          label={isVolc ? t('settings.sms_volc_ak', 'Access Key') : t('settings.sms_secret_id')}
          name="secret_id"
          rules={[{ required: true }]}
        >
          <Input placeholder={isVolc ? 'AKLT…' : 'AKIDxxxxxxxx'} />
        </Form.Item>
        <Form.Item
          label={isVolc ? t('settings.sms_volc_sk', 'Secret Key') : t('settings.sms_secret_key')}
          name="secret_key"
          rules={[{ required: true }]}
        >
          <Input.Password placeholder={isVolc ? '请输入 Secret Key' : '请输入 SecretKey'} />
        </Form.Item>
        <Form.Item
          label={isVolc
            ? t('settings.sms_volc_account', '消息组 ID（SmsAccount）')
            : t('settings.sms_sdk_app_id')}
          name="sdk_app_id"
          rules={[{ required: true }]}
          extra={isVolc
            ? t('settings.sms_volc_account_hint', '火山短信控制台 → 消息组列表中的短信账户 ID')
            : undefined}
        >
          <Input placeholder={isVolc ? 'A123****' : '1400000000'} />
        </Form.Item>
        <Form.Item label={t('settings.sms_sign_name')} name="sign_name" rules={[{ required: true }]}>
          <Input placeholder="已审核的短信签名" />
        </Form.Item>
        <Form.Item
          label={t('settings.sms_template_id')}
          name="template_id"
          rules={[{ required: true }]}
          extra={isVolc
            ? t('settings.sms_volc_code_tpl_hint', '验证码模板 ID，变量通过下方「验证码变量名」传入')
            : undefined}
        >
          <Input placeholder={isVolc ? 'ST_xxxx' : '123456'} />
        </Form.Item>
        {isVolc && (
          <Form.Item
            label={t('settings.sms_code_param', '验证码变量名')}
            name="code_param"
            rules={[{ required: true, message: '请填写模板变量名' }]}
            extra={t(
              'settings.sms_code_param_hint',
              '填变量名本身：控制台显示 ${1} 则填 1，显示 ${code} 则填 code（勿带 ${}）。发送示例 {"1":"666666"}',
            )}
          >
            <Input placeholder="1 或 code" />
          </Form.Item>
        )}
        <Form.Item
          label={t('settings.sms_balance_template_id', '余额提醒模板 ID')}
          name="balance_template_id"
          extra={t(
            'settings.sms_balance_template_hint',
            '可选。使用无变量固定正文模板。若「提示通知」已开启短信余额提醒，则此处必填，留空将无法保存。',
          )}
        >
          <Input placeholder={isVolc ? '可选，ST_xxxx' : '可选，余额不足提醒模板 ID'} />
        </Form.Item>
        <LowBalanceSmsHint
          provider={provider}
          title="推荐申请正文（点复制）"
          style={{ marginBottom: 24 }}
        />

        <Space style={{ marginBottom: 24 }}>
          <Button type="primary" htmlType="button" onClick={handleSave} loading={loading}>
            {t('common.save')}
          </Button>
        </Space>

        <Typography.Title level={5} style={{ marginTop: 16 }}>
          {t('settings.test_sms')}
        </Typography.Title>
        <Typography.Text type="secondary" style={{ display: 'block', fontSize: 12, marginBottom: 8 }}>
          请先保存。测试使用已保存配置发送验证码（变量值 666666）
        </Typography.Text>
        <Space>
          <Input
            placeholder={t('settings.test_mobile_placeholder')}
            value={testMobile}
            onChange={(e) => setTestMobile(e.target.value)}
            style={{ width: 300 }}
          />
          <Button htmlType="button" onClick={handleTest} loading={testLoading}>
            {t('settings.test_sms')}
          </Button>
        </Space>
      </Form>
    </div>
  );
};

export default SmsNotification;
