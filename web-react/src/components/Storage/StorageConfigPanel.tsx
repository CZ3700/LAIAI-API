import React, { useState, useEffect, useMemo, useRef } from 'react';
import {
  Card, Form, Input, Button, Radio, Select, Alert, Typography,
  Row, Col, Space, Divider, message, type FormInstance
} from 'antd';
import {
  ApiOutlined, LinkOutlined, SaveOutlined
} from '@ant-design/icons';
import { useThemeStore } from '../../store/theme';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  STORAGE_PROVIDERS,
  getStorageProvider,
  tosOptionIdFromSaved,
} from './providers';
import type { TestConnectionResult, ListedStorageBucket } from './types';
import request from '../../utils/request';
import { RegionFlagSvg } from '../../pages/Admin/CurrencySettingsTab';

const { Text } = Typography;

const endpointUrl = (host?: string) => {
  const bare = (host || '').trim().replace(/^https?:\/\//, '').replace(/\/$/, '');
  return bare ? `https://${bare}` : '';
};

export interface StorageConfigPanelProps {
  form: FormInstance;
  namePrefix?: (string | number)[];
  standalone?: boolean;
  pluginName?: string;
  onSaveStandalone?: () => Promise<void>;
  savingStandalone?: boolean;
  maskedSecrets?: Record<string, string>;
  extraBottomContent?: React.ReactNode;
  /** 站点存储已配置时传入，插件页展示「跟随站点设置」卡片（只读） */
  globalSnapshot?: {
    provider: string;
    bucket: string;
    endpoint?: string;
    pathPrefix?: string;
    region?: string;
  };
  siteSettingsHref?: string;
}

const GLOBAL_KEY = 'global';

const SITE_STORAGE_DIRS: { dir: string; descKey: string; desc: string }[] = [
  {
    dir: 'users/{UID}/kyc/',
    descKey: 'storage_dir_site_kyc',
    desc: '用户实名认证（KYC）证件，按用户 UID 归档',
  },
  {
    dir: 'users/{UID}/avatar/',
    descKey: 'storage_dir_site_avatar',
    desc: '用户头像；后续其它用户数据同样放在 users/{UID}/ 下',
  },
  {
    dir: 'settings/',
    descKey: 'storage_dir_site_settings',
    desc: '管理后台基础设置上传的图片，如站点 Logo',
  },
  {
    dir: 'channel_cache/{渠道ID}/',
    descKey: 'storage_dir_site_channel_cache',
    desc: '渠道开启「对象存储转存」后保存的模型生成图片 / 视频，按渠道设置的保存天数自动清理',
  },
  {
    dir: 'plugins/{插件名}/',
    descKey: 'storage_dir_site_plugins',
    desc: '插件跟随站点存储（或独立配置不完整回落站点）时的文件，如 plugins/docs_api/images/ API 文档插图、plugins/asset_manager/ 素材管理、plugins/playground_2026/ 创作中心2026、plugins/site_icons/icons/custom/ 手动添加的站点图标；插件单独配置存储时不在此处，直接放在插件自己的路径前缀下',
  },
];

const PLUGIN_STORAGE_DIRS: { dir: string; descKey: string; desc: string }[] = [
  {
    dir: 'images/',
    descKey: 'storage_dir_plugin_images',
    desc: '插件生成的图片与静态配图资源',
  },
  {
    dir: 'videos/',
    descKey: 'storage_dir_plugin_videos',
    desc: '插件生成与转存的音视频多媒体资源',
  },
  {
    dir: 'assets/',
    descKey: 'storage_dir_plugin_assets',
    desc: '插件专属静态素材与上传文件',
  },
  {
    dir: 'cache/',
    descKey: 'storage_dir_plugin_cache',
    desc: '插件运行过程中的临时与转存缓存文件',
  },
];

const toDir = (raw?: string) => {
  const p = String(raw ?? '').trim().replace(/^\/+|\/+$/g, '');
  return p ? `${p}/` : '';
};

export const StorageConfigPanel: React.FC<StorageConfigPanelProps> = ({
  form,
  namePrefix = ['storage'],
  standalone = false,
  pluginName,
  onSaveStandalone,
  savingStandalone = false,
  maskedSecrets = {},
  extraBottomContent,
  globalSnapshot,
  siteSettingsHref,
}) => {
  const { t } = useTranslation('playground_2026');
  const { themeMode } = useThemeStore();
  const isLight = themeMode === 'light';

  // 辅助函数：构造表单字段名
  const getFieldPath = (field: string): any => {
    return namePrefix && namePrefix.length > 0 ? [...namePrefix, field] : field;
  };

  // 安全地获取表单值
  const getStorageValue = (field: string) => {
    try {
      if (namePrefix && namePrefix.length > 0) {
        return form.getFieldValue([...namePrefix, field]);
      }
      return form.getFieldValue(field);
    } catch {
      return undefined;
    }
  };

  // 安全地设置表单值
  const setStorageValue = (field: string, val: any) => {
    try {
      if (namePrefix && namePrefix.length > 0) {
        form.setFieldValue([...namePrefix, field], val);
      } else {
        form.setFieldValue(field, val);
      }
    } catch (e) {
      console.error('setStorageValue error', e);
    }
  };

  // 监听默认提供商与存储配置字段，保持响应式
  const watchedDefault = Form.useWatch(getFieldPath('default_provider'), form);
  const watchedStorageValues = Form.useWatch(namePrefix && namePrefix.length > 0 ? namePrefix : [], form);

  const rawDefault = watchedDefault ?? getStorageValue('default_provider');
  const normalizedDefault = String(rawDefault ?? '').trim().toLowerCase();
  const isNoneDefault = !normalizedDefault || normalizedDefault === 'none';
  const isGlobalDefault = !isNoneDefault && normalizedDefault === GLOBAL_KEY;
  const globalVendorMeta = getStorageProvider(globalSnapshot?.provider || 'tos');
  const [selectedProviderKey, setSelectedProviderKey] = useState<string>(() =>
    !isNoneDefault && (normalizedDefault !== GLOBAL_KEY || !!globalSnapshot)
      ? normalizedDefault
      : 'tos'
  );
  const userPickedPanel = useRef(false);
  const showGlobalPanel = selectedProviderKey === GLOBAL_KEY && !!globalSnapshot;

  const selectProviderPanel = (key: string) => {
    userPickedPanel.current = true;
    setSelectedProviderKey(key);
  };

  const activeProvider = useMemo(() => {
    return getStorageProvider(selectedProviderKey);
  }, [selectedProviderKey]);

  const isTos = activeProvider.key === 'tos';
  const isCos = activeProvider.key === 'cos';
  const isListBucketsSupported = isTos || isCos;

  // 动态监听当前激活存储商的核心字段
  const watchedActiveBucket = Form.useWatch(getFieldPath(activeProvider.fieldKeys.bucket), form);
  const watchedActiveRegion = Form.useWatch(getFieldPath(activeProvider.fieldKeys.region), form);
  const watchedActiveEndpoint = Form.useWatch(getFieldPath(activeProvider.fieldKeys.endpoint), form);
  const watchedActivePrefix = Form.useWatch(getFieldPath(activeProvider.fieldKeys.pathPrefix), form);

  // 网络类型状态管理 (按提供商 key 隔离)
  const [networkTypes, setNetworkTypes] = useState<Record<string, 'external' | 'internal'>>({
    tos: 'external',
    cos: 'external',
  });

  // 测试连接状态与结果 (按提供商 key 隔离)
  const [bucketsMap, setBucketsMap] = useState<Record<string, ListedStorageBucket[]>>({
    tos: [],
    cos: [],
  });
  const [listingBuckets, setListingBuckets] = useState<Record<string, boolean>>({});
  const [testingKey, setTestingKey] = useState<string | null>(null);
  const [testResults, setTestResults] = useState<Record<string, TestConnectionResult | null>>({});

  // 表单加载后跟到已保存的默认；用户点过卡片后不再抢选中态
  useEffect(() => {
    if (selectedProviderKey === GLOBAL_KEY && !globalSnapshot) {
      userPickedPanel.current = false;
      setSelectedProviderKey(
        !isNoneDefault && normalizedDefault !== GLOBAL_KEY ? normalizedDefault : 'tos'
      );
      return;
    }
    if (userPickedPanel.current) return;
    const next =
      !isNoneDefault && (normalizedDefault !== GLOBAL_KEY || !!globalSnapshot)
        ? normalizedDefault
        : 'tos';
    if (next !== selectedProviderKey) setSelectedProviderKey(next);
  }, [isNoneDefault, normalizedDefault, globalSnapshot, selectedProviderKey]);

  // 旧国际版把地域存成 bp- 前缀；签名要用官方地域代码
  useEffect(() => {
    if (isTos && typeof watchedActiveRegion === 'string' && watchedActiveRegion.startsWith('bp-')) {
      setStorageValue('tos_region', watchedActiveRegion.slice(3));
    }
  }, [isTos, watchedActiveRegion]);

  // 根据当前 endpoint 自动判断网络类型
  useEffect(() => {
    const ep = watchedActiveEndpoint || getStorageValue(activeProvider.fieldKeys.endpoint);
    if (ep) {
      setNetworkTypes(prev => ({
        ...prev,
        [activeProvider.key]: activeProvider.detectNetworkType(ep),
      }));
    }
  }, [watchedActiveEndpoint, activeProvider]);

  // 设为默认提供商
  const handleSetDefaultProvider = (providerKey: string) => {
    setStorageValue('default_provider', providerKey);
  };

  const applyBucket = (
    providerKey: string,
    bucket: ListedStorageBucket,
    netType?: 'external' | 'internal'
  ) => {
    const provider = getStorageProvider(providerKey);
    const targetNetType = netType || networkTypes[providerKey] || 'external';
    setNetworkTypes(prev => ({ ...prev, [providerKey]: targetNetType }));
    setStorageValue(provider.fieldKeys.bucket, bucket.name);
    if (bucket.location) {
      setStorageValue(provider.fieldKeys.region, bucket.location);
    }
    const host = targetNetType === 'internal' ? bucket.intranet_endpoint : bucket.extranet_endpoint;
    const endpoint = endpointUrl(host) || provider.resolveEndpoint(
      bucket.location,
      targetNetType,
      host,
    );
    if (endpoint) {
      setStorageValue(provider.fieldKeys.endpoint, endpoint);
    }
  };

  const handleListBuckets = async (providerKey: string) => {
    const provider = getStorageProvider(providerKey);
    const values = getAllStorageValues();
    const idVal = String(values[provider.fieldKeys.keyId] || '').trim();
    const secretVal = String(values[provider.fieldKeys.keySecret] || '').trim();
    const maskedVal = maskedSecrets[provider.fieldKeys.keySecret];

    if (!idVal || (!secretVal && !maskedVal)) {
      message.warning(t('storage_fill_keys_first', {
        defaultValue: '请先填写 {{idLabel}} 和 {{secretLabel}}',
        idLabel: provider.fieldKeys.keyIdLabel,
        secretLabel: provider.fieldKeys.keySecretLabel,
      }));
      return;
    }
    setListingBuckets(prev => ({ ...prev, [providerKey]: true }));
    try {
      const url = pluginName ? `/plugins/${pluginName}/list-buckets` : '/settings/storage/buckets';
      const payload: Record<string, any> = {
        provider: providerKey,
        [provider.fieldKeys.keyId]: idVal,
        [provider.fieldKeys.keySecret]: secretVal,
        [provider.fieldKeys.endpoint]: values[provider.fieldKeys.endpoint] || '',
      };
      const res = await (request.post(url, payload) as Promise<{
        success?: boolean;
        message?: string;
        buckets?: ListedStorageBucket[];
      }>);
      if (!res?.success) {
        message.error(res?.message || t('storage_list_buckets_failed', '获取存储桶失败'));
        return;
      }
      const buckets = Array.isArray(res.buckets) ? res.buckets : [];
      setBucketsMap(prev => ({ ...prev, [providerKey]: buckets }));
      if (buckets.length === 0) {
        message.warning(t('storage_no_buckets', '该账号下没有存储桶'));
        return;
      }
      message.success(t('storage_list_buckets_succ', { defaultValue: '已获取 {{count}} 个存储桶，请点击选择', count: buckets.length }));
    } catch (error: any) {
      message.error(error?.response?.data?.error?.message || error?.message || t('storage_list_buckets_failed', '获取存储桶失败'));
    } finally {
      setListingBuckets(prev => ({ ...prev, [providerKey]: false }));
    }
  };

  // 切换内外网类型时自动更新 Endpoint
  const handleNetworkTypeChange = (providerKey: string, netType: 'external' | 'internal') => {
    setNetworkTypes(prev => ({ ...prev, [providerKey]: netType }));
    const provider = getStorageProvider(providerKey);
    const currentBucketName = String(getStorageValue(provider.fieldKeys.bucket) || '');
    const buckets = bucketsMap[providerKey] || [];
    const found = buckets.find(b => b.name === currentBucketName);
    if (found) {
      const host = netType === 'internal' ? found.intranet_endpoint : found.extranet_endpoint;
      const endpoint = endpointUrl(host) || provider.resolveEndpoint(found.location, netType, host);
      if (endpoint) {
        setStorageValue(provider.fieldKeys.endpoint, endpoint);
        return;
      }
    }
    const currentRegion = getStorageValue(provider.fieldKeys.region);

    if (currentRegion) {
      const newEndpoint = provider.resolveEndpoint(
        currentRegion,
        netType,
        getStorageValue(provider.fieldKeys.endpoint),
      );
      if (newEndpoint) {
        setStorageValue(provider.fieldKeys.endpoint, newEndpoint);
      }
    }
  };

  // 获取全部存储配置对象以供测试连接使用
  const getAllStorageValues = (): Record<string, any> => {
    try {
      if (namePrefix && namePrefix.length > 0) {
        return form.getFieldValue(namePrefix) || {};
      }
      return form.getFieldsValue(true) || {};
    } catch {
      return {};
    }
  };

  // 测试连接
  const handleTestConnection = async (providerKey: string) => {
    setTestingKey(providerKey);
    setTestResults(prev => ({ ...prev, [providerKey]: null }));

    try {
      const values = getAllStorageValues();
      let res: TestConnectionResult;

      if (pluginName) {
        // 插件独立存储测试
        res = await (request.post(`/plugins/${pluginName}/test-connection`, {
          ...values,
          provider: providerKey,
        }) as any);
      } else {
        // 系统全局存储测试
        res = await (request.post('/settings/storage/test', {
          ...values,
          provider: providerKey,
        }) as any);
      }

      setTestResults(prev => ({ ...prev, [providerKey]: res }));
    } catch (error: any) {
      const errMsg = error?.response?.data?.error?.message || error?.message || t('storage_test_failed', '测试连接失败');
      setTestResults(prev => ({
        ...prev,
        [providerKey]: { success: false, message: errMsg }
      }));
    } finally {
      setTestingKey(null);
    }
  };

  const tosOptionId = tosOptionIdFromSaved(
    typeof watchedActiveRegion === 'string' ? watchedActiveRegion : undefined,
    typeof watchedActiveEndpoint === 'string' ? watchedActiveEndpoint : undefined,
  );

  const prefixDir = toDir(
    watchedActivePrefix ?? getStorageValue(activeProvider.fieldKeys.pathPrefix)
  );

  const currentBucketValue = watchedActiveBucket ?? getStorageValue(activeProvider.fieldKeys.bucket);

  const currentBuckets = bucketsMap[activeProvider.key] || [];
  const isListingCurrent = !!listingBuckets[activeProvider.key];

  const currentRegionValue = isTos
    ? tosOptionId
    : (watchedActiveRegion ?? getStorageValue(activeProvider.fieldKeys.region));

  const rootDirText = t('storage_bucket_root', '存储桶根目录/');
  const prefixLayoutHint = pluginName
    ? t('storage_prefix_hint_plugin', { defaultValue: '本插件独立存储，文件直接存放在 {{dir}}', dir: prefixDir || rootDirText })
    : t('storage_prefix_hint_site', { defaultValue: '所有文件存放在 {{dir}} 下，目录用途见下方「存储目录说明」', dir: prefixDir || rootDirText });

  const currentDefaultMeta = useMemo(() => {
    if (isNoneDefault || isGlobalDefault) return null;
    return getStorageProvider(normalizedDefault);
  }, [isNoneDefault, isGlobalDefault, normalizedDefault]);

  // 组装存储提供商卡片列表
  const providerCards = useMemo(() => {
    const cards = [];

    if (globalSnapshot) {
      const isSelected = selectedProviderKey === GLOBAL_KEY;
      const isDefault = !isNoneDefault && isGlobalDefault;
      const vendorName = t(`storage_provider_${globalVendorMeta.key}_name`, globalVendorMeta.name);
      cards.push({
        key: GLOBAL_KEY,
        name: t('storage_follow_site', '跟随站点'),
        shortName: 'Site',
        brand: `${vendorName}${globalSnapshot.bucket ? ` · ${globalSnapshot.bucket}` : ''}`,
        isSelected,
        isDefault,
        isGlobal: true,
        isConfigured: true,
        testResult: testResults[GLOBAL_KEY],
      });
    }

    const allValues = getAllStorageValues();

    for (const p of STORAGE_PROVIDERS) {
      const isSelected = selectedProviderKey === p.key;
      const isDefault = !isNoneDefault && normalizedDefault === p.key;
      const configured = p.isConfigured(allValues);

      cards.push({
        key: p.key,
        name: t(`storage_provider_${p.key}_name`, p.name),
        shortName: p.shortName,
        brand: t(`storage_provider_${p.key}_brand`, p.brand),
        isSelected,
        isDefault,
        isGlobal: false,
        isConfigured: configured,
        testResult: testResults[p.key],
      });
    }

    return cards;
  }, [
    globalSnapshot, selectedProviderKey, isNoneDefault, isGlobalDefault,
    globalVendorMeta, normalizedDefault, watchedDefault, watchedStorageValues,
    testResults, t
  ]);

  const renderTestAlert = (key: string) => {
    const res = testResults[key];
    if (!res) return null;
    return (
      <Alert
        type={res.success ? 'success' : 'error'}
        showIcon
        style={{ marginBottom: 16, borderRadius: 6 }}
        message={res.success ? t('storage_test_succ', '连接测试成功') : t('storage_test_failed', '连接测试失败')}
        description={res.message}
        closable
        onClose={() => setTestResults(prev => ({ ...prev, [key]: null }))}
      />
    );
  };

  const renderActionFooter = (targetKey: string) => (
    <div style={{
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'flex-end',
      marginTop: 14,
      paddingTop: 12,
      borderTop: `1px solid ${isLight ? '#f4f4f5' : '#1f1f23'}`,
      gap: 10
    }}>
      <Space size="middle">
        <Button
          icon={<ApiOutlined />}
          onClick={() => handleTestConnection(targetKey)}
          loading={testingKey === targetKey}
          style={{
            borderRadius: 6,
            borderColor: isLight ? '#e4e4e7' : '#27272a',
            color: isLight ? '#18181b' : '#fafafa',
            background: isLight ? '#ffffff' : '#18181b',
          }}
        >
          {t('storage_test_btn', '测试连接')}
        </Button>
        {standalone && onSaveStandalone && (
          <Button
            icon={<SaveOutlined />}
            loading={savingStandalone}
            onClick={onSaveStandalone}
            style={{
              borderRadius: 6,
              background: isLight ? '#18181b' : '#fafafa',
              borderColor: isLight ? '#18181b' : '#fafafa',
              color: isLight ? '#fafafa' : '#18181b',
            }}
          >
            {t('storage_save_btn', '保存配置')}
          </Button>
        )}
      </Space>
    </div>
  );

  const panelContent = (
    <div style={{ maxWidth: 840 }}>
      {/* 隐藏表单项，保证 default_provider 字段正常提交 */}
      <Form.Item name={getFieldPath('default_provider')} hidden>
        <Input />
      </Form.Item>

      {/* 顶部：当前默认存储状态栏 (shadcn 极简黑白灰) */}
      <div style={{
        background: isLight ? '#fafafa' : '#18181b',
        border: `1px solid ${isLight ? '#e4e4e7' : '#27272a'}`,
        borderRadius: 8,
        padding: '10px 16px',
        marginBottom: 16,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        flexWrap: 'wrap',
        gap: 12
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <Text style={{ fontSize: 13, color: isLight ? '#71717a' : '#a1a1aa' }}>
            {t('storage_default_label', '默认存储')}
          </Text>
          {isNoneDefault ? (
            <span style={{
              fontSize: 12,
              padding: '2px 8px',
              borderRadius: 6,
              background: isLight ? '#f4f4f5' : '#27272a',
              color: isLight ? '#71717a' : '#a1a1aa',
              border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}`,
              fontWeight: 500,
            }}>
              {t('storage_default_none', '未设置 (停用对象存储)')}
            </span>
          ) : (
            <span style={{
              fontSize: 12,
              padding: '2px 8px',
              borderRadius: 6,
              background: isLight ? '#18181b' : '#fafafa',
              color: isLight ? '#fafafa' : '#18181b',
              fontWeight: 600,
            }}>
              {isGlobalDefault
                ? (globalSnapshot ? `${t('storage_follow_site', '跟随站点')} (${t(`storage_provider_${globalVendorMeta.key}_name`, globalVendorMeta.name)})` : t('storage_follow_site', '跟随站点'))
                : (currentDefaultMeta ? t(`storage_provider_${currentDefaultMeta.key}_name`, currentDefaultMeta.name) : '')}
            </span>
          )}
        </div>

        {!isNoneDefault && (
          <Button
            size="small"
            type="text"
            onClick={() => handleSetDefaultProvider('none')}
            style={{
              fontSize: 12,
              height: 24,
              color: isLight ? '#71717a' : '#a1a1aa',
              padding: '0 8px',
            }}
          >
            {t('storage_unset_default', '取消默认')}
          </Button>
        )}
      </div>

      {/* 存储服务商切换卡片栏 */}
      <div style={{ marginBottom: 16 }}>
        <div style={{ marginBottom: 8 }}>
          <Text strong style={{ fontSize: 13, color: isLight ? '#09090b' : '#fafafa' }}>
            {t('storage_providers_title', '存储服务商')}
          </Text>
        </div>

        <div style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fill, minmax(240px, 1fr))',
          gap: 10,
        }}>
          {providerCards.map(card => {
            const isSelected = card.isSelected;
            return (
              <div
                key={card.key}
                onClick={() => selectProviderPanel(card.key)}
                style={{
                  border: isSelected
                    ? (isLight ? '2px solid #18181b' : '2px solid #fafafa')
                    : (isLight ? '1px solid #e4e4e7' : '1px solid #27272a'),
                  background: isSelected
                    ? (isLight ? '#fafafa' : '#121215')
                    : (isLight ? '#ffffff' : '#09090b'),
                  borderRadius: 8,
                  padding: '12px 14px',
                  cursor: 'pointer',
                  transition: 'all 0.15s ease',
                  display: 'flex',
                  flexDirection: 'column',
                  justifyContent: 'space-between',
                  minHeight: 100,
                }}
              >
                <div>
                  <div style={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    gap: 8,
                    marginBottom: 4,
                  }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0, flex: 1 }}>
                      <div style={{
                        width: 26,
                        height: 26,
                        borderRadius: 6,
                        background: isLight ? '#f4f4f5' : '#27272a',
                        color: isLight ? '#18181b' : '#fafafa',
                        border: `1px solid ${isLight ? '#e4e4e7' : '#3f3f46'}`,
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                        fontSize: 12,
                        fontWeight: 600,
                        flexShrink: 0,
                      }}>
                        {card.shortName.slice(0, 1)}
                      </div>
                      <span style={{
                        fontWeight: 600,
                        fontSize: 13,
                        color: isLight ? '#09090b' : '#fafafa',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                      }}>
                        {card.name}
                      </span>
                    </div>

                    <div style={{ flexShrink: 0 }} onClick={(e) => e.stopPropagation()}>
                      {card.isDefault ? (
                        <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                          <span style={{
                            fontSize: 11,
                            padding: '1px 6px',
                            borderRadius: 4,
                            background: isLight ? '#18181b' : '#fafafa',
                            color: isLight ? '#fafafa' : '#18181b',
                            fontWeight: 500,
                          }}>
                            {t('storage_badge_default', '默认')}
                          </span>
                          <Button
                            size="small"
                            type="text"
                            onClick={() => handleSetDefaultProvider('none')}
                            style={{
                              fontSize: 11,
                              height: 22,
                              padding: '0 4px',
                              color: isLight ? '#71717a' : '#a1a1aa',
                            }}
                          >
                            {t('common_cancel', '取消')}
                          </Button>
                        </div>
                      ) : (
                        <Button
                          size="small"
                          onClick={() => handleSetDefaultProvider(card.key)}
                          style={{
                            fontSize: 11,
                            height: 22,
                            padding: '0 6px',
                            borderRadius: 4,
                            background: isLight ? '#ffffff' : '#18181b',
                            borderColor: isLight ? '#e4e4e7' : '#27272a',
                            color: isLight ? '#18181b' : '#fafafa',
                          }}
                        >
                          {t('storage_set_default', '设为默认')}
                        </Button>
                      )}
                    </div>
                  </div>

                  <div style={{
                    fontSize: 11,
                    color: isLight ? '#71717a' : '#71717a',
                    paddingLeft: 34,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}>
                    {card.brand}
                  </div>
                </div>

                <div style={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  marginTop: 8,
                  paddingTop: 8,
                  borderTop: `1px solid ${isLight ? '#f4f4f5' : '#1f1f23'}`,
                  fontSize: 11,
                }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                    <span style={{
                      width: 6,
                      height: 6,
                      borderRadius: '50%',
                      background: card.isConfigured ? '#22c55e' : (isLight ? '#d4d4d8' : '#3f3f46'),
                      display: 'inline-block',
                    }} />
                    <span style={{
                      color: card.isConfigured
                        ? (isLight ? '#18181b' : '#fafafa')
                        : (isLight ? '#a1a1aa' : '#52525b')
                    }}>
                      {card.isConfigured ? t('storage_configured', '已配置') : t('storage_unconfigured', '未配置')}
                    </span>
                  </div>

                  {isSelected && (
                    <span style={{
                      fontSize: 10,
                      color: isLight ? '#71717a' : '#a1a1aa',
                    }}>
                      {t('storage_current_editing', '当前配置')}
                    </span>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      </div>

      {/* 聚焦的配置面板 */}
      {showGlobalPanel && globalSnapshot ? (
        <Card
          bordered
          style={{
            borderRadius: 8,
            background: isLight ? '#ffffff' : '#09090b',
            borderColor: isLight ? '#e4e4e7' : '#27272a',
          }}
          title={
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '2px 0' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <Text strong style={{ fontSize: 14, color: isLight ? '#09090b' : '#fafafa' }}>
                  {t('storage_global_card_title', '站点全局存储 (跟随只读)')}
                </Text>
                {isGlobalDefault && (
                  <span style={{
                    fontSize: 11,
                    padding: '1px 6px',
                    borderRadius: 4,
                    background: isLight ? '#18181b' : '#fafafa',
                    color: isLight ? '#fafafa' : '#18181b',
                    fontWeight: 500
                  }}>
                    {t('storage_badge_default', '默认')}
                  </span>
                )}
              </div>
              {siteSettingsHref && (
                <Link
                  to={siteSettingsHref}
                  style={{
                    fontSize: 12,
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 4,
                    color: isLight ? '#18181b' : '#fafafa',
                  }}
                >
                  <LinkOutlined /> {t('storage_site_settings_link', '站点设置')}
                </Link>
              )}
            </div>
          }
        >
          {renderTestAlert(GLOBAL_KEY)}

          <div style={{
            background: isLight ? '#fafafa' : '#18181b',
            border: `1px solid ${isLight ? '#e4e4e7' : '#27272a'}`,
            borderRadius: 6,
            padding: '12px 16px',
            fontSize: 13,
            lineHeight: 1.8,
          }}>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: '8px 24px' }}>
              <div>
                <span style={{ color: isLight ? '#71717a' : '#a1a1aa' }}>{t('storage_active_vendor', '生效厂商：')}</span>
                <Text strong style={{ color: isLight ? '#09090b' : '#fafafa' }}>{t(`storage_provider_${globalVendorMeta.key}_name`, globalVendorMeta.name)}</Text>
              </div>
              <div>
                <span style={{ color: isLight ? '#71717a' : '#a1a1aa' }}>{t('storage_bucket_label', '存储桶：')}</span>
                <Text code>{globalSnapshot.bucket || '—'}</Text>
              </div>
              {globalSnapshot.region && (
                <div>
                  <span style={{ color: isLight ? '#71717a' : '#a1a1aa' }}>{t('storage_region_label', '地域：')}</span>
                  <Text code>{globalSnapshot.region}</Text>
                </div>
              )}
              {globalSnapshot.endpoint && (
                <div>
                  <span style={{ color: isLight ? '#71717a' : '#a1a1aa' }}>{t('storage_endpoint_short_label', '节点：')}</span>
                  <Text code>{globalSnapshot.endpoint}</Text>
                </div>
              )}
              {globalSnapshot.pathPrefix && (
                <div>
                  <span style={{ color: isLight ? '#71717a' : '#a1a1aa' }}>{t('storage_path_dir_label', '存放目录：')}</span>
                  <Text code>{toDir(globalSnapshot.pathPrefix)}</Text>
                </div>
              )}
            </div>
          </div>

          {renderActionFooter(GLOBAL_KEY)}
        </Card>
      ) : (
      <Card
        bordered
        style={{
          borderRadius: 8,
          background: isLight ? '#fff' : '#09090b',
          borderColor: isLight ? '#e4e4e7' : '#27272a',
        }}
        title={
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '2px 0' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <Text strong style={{ fontSize: 14, color: isLight ? '#09090b' : '#fafafa' }}>
                {t(`storage_provider_${activeProvider.key}_name`, activeProvider.name)}
              </Text>
              {normalizedDefault === activeProvider.key && (
                <span style={{
                  fontSize: 11,
                  padding: '1px 6px',
                  borderRadius: 4,
                  background: isLight ? '#18181b' : '#fafafa',
                  color: isLight ? '#fafafa' : '#18181b',
                  fontWeight: 500
                }}>
                  {t('storage_badge_default', '默认')}
                </span>
              )}
            </div>
            {activeProvider.docUrl && (
              <a
                href={activeProvider.docUrl}
                target="_blank"
                rel="noreferrer"
                style={{
                  fontSize: 12,
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 4,
                  color: isLight ? '#71717a' : '#a1a1aa',
                }}
              >
                <LinkOutlined /> {t('storage_console_link', '控制台')}
              </a>
            )}
          </div>
        }
      >
        {renderTestAlert(activeProvider.key)}

        {/* 模块 1: 访问凭证 */}
        <div style={{ marginBottom: 16 }}>
          <div style={{ marginBottom: 10 }}>
            <Text strong style={{ fontSize: 13, color: isLight ? '#09090b' : '#fafafa' }}>
              {t('storage_credentials_title', '访问凭证')}
            </Text>
          </div>
          <Row gutter={16}>
            <Col xs={24} sm={12}>
              <Form.Item
                label={activeProvider.fieldKeys.keyIdLabel}
                name={getFieldPath(activeProvider.fieldKeys.keyId)}
              >
                <Input placeholder={t(`storage_${activeProvider.key}_key_id_ph`, activeProvider.fieldKeys.keyIdPlaceholder)} />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12}>
              <Form.Item
                label={activeProvider.fieldKeys.keySecretLabel}
                name={getFieldPath(activeProvider.fieldKeys.keySecret)}
                extra={
                  maskedSecrets[activeProvider.fieldKeys.keySecret] ? (
                    <span style={{ fontSize: 11, color: isLight ? '#71717a' : '#a1a1aa' }}>
                      {t('storage_current_masked_secret', {
                        defaultValue: '当前: {{masked}} (留空不修改)',
                        masked: maskedSecrets[activeProvider.fieldKeys.keySecret],
                      })}
                    </span>
                  ) : undefined
                }
              >
                <Input.Password placeholder={t(`storage_${activeProvider.key}_key_secret_ph`, activeProvider.fieldKeys.keySecretPlaceholder)} />
              </Form.Item>
            </Col>
            {isListBucketsSupported && (
              <Col xs={24}>
                <Form.Item name={getFieldPath(activeProvider.fieldKeys.bucket)} hidden>
                  <Input />
                </Form.Item>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8 }}>
                  <Text strong style={{ fontSize: 14 }}>{t('storage_bucket_title', '存储桶')}</Text>
                  <Button size="small" loading={isListingCurrent} onClick={() => handleListBuckets(activeProvider.key)}>
                    {t('storage_fetch_buckets_btn', '获取存储桶')}
                  </Button>
                </div>
                {typeof currentBucketValue === 'string' && currentBucketValue && (
                  <div style={{ fontSize: 12, color: isLight ? '#71717a' : '#a1a1aa', marginBottom: 8 }}>
                    {t('storage_current_bucket', { defaultValue: '当前：{{bucket}}', bucket: currentBucketValue })}
                  </div>
                )}
                {currentBuckets.length > 0 && (
                  <div style={{
                    height: 168,
                    overflow: 'auto',
                    border: `1px solid ${isLight ? '#e4e4e7' : '#27272a'}`,
                    borderRadius: 8,
                    padding: 8,
                    background: isLight ? '#fafafa' : '#09090b',
                  }}>
                    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))', gap: 8 }}>
                      {currentBuckets.map(b => {
                        const selected = currentBucketValue === b.name;
                        return (
                          <Button
                            key={b.name}
                            type="text"
                            onClick={() => applyBucket(activeProvider.key, b)}
                            style={{
                              width: '100%',
                              height: 36,
                              padding: '0 10px',
                              display: 'flex',
                              justifyContent: 'flex-start',
                              overflow: 'hidden',
                              borderRadius: 6,
                              border: `1px solid ${selected ? (isLight ? '#18181b' : '#fafafa') : (isLight ? '#e4e4e7' : '#27272a')}`,
                              background: selected ? (isLight ? '#18181b' : '#fafafa') : (isLight ? '#ffffff' : '#18181b'),
                              color: selected ? (isLight ? '#fafafa' : '#18181b') : (isLight ? '#18181b' : '#fafafa'),
                            }}
                          >
                            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                              {b.name}{b.location ? ` (${b.location})` : ''}
                            </span>
                          </Button>
                        );
                      })}
                    </div>
                  </div>
                )}
                <div style={{ fontSize: 11, color: isLight ? '#71717a' : '#a1a1aa', marginTop: 8 }}>
                  {t('storage_bucket_click_hint', '点击存储桶后，按公网自动填入下方地域和请求地址。')}
                </div>
              </Col>
            )}
          </Row>
        </div>

        <Divider style={{ margin: '8px 0 16px', borderColor: isLight ? '#f4f4f5' : '#27272a' }} />

        {/* 模块 2: 存储桶与地域 */}
        <div style={{ marginBottom: 16 }}>
          <div style={{ marginBottom: 10 }}>
            <Text strong style={{ fontSize: 13, color: isLight ? '#09090b' : '#fafafa' }}>
              {t('storage_bucket_region_title', '存储桶与地域')}
            </Text>
          </div>
          <Row gutter={16}>
            <Col xs={24} sm={12}>
              <Form.Item
                label={t('storage_network_type', '网络类型')}
                extra={
                  isListBucketsSupported ? (
                    <span style={{ fontSize: 11, color: isLight ? '#71717a' : '#a1a1aa' }}>
                      {isTos
                        ? t('storage_internal_net_hint_tos', '选择内网时，火山引擎服务器必须与对象存储在同一地域。')
                        : t('storage_internal_net_hint_cos', '选择内网时，云服务器必须与对象存储在同一地域。')}
                    </span>
                  ) : undefined
                }
              >
                <Radio.Group
                  value={networkTypes[activeProvider.key] || 'external'}
                  optionType="button"
                  buttonStyle="solid"
                  style={{ width: '100%', display: 'flex' }}
                  onChange={(e) => handleNetworkTypeChange(activeProvider.key, e.target.value)}
                >
                  <Radio.Button value="external" style={{ flex: 1, textAlign: 'center' }}>{t('storage_net_external', '公网')}</Radio.Button>
                  <Radio.Button value="internal" style={{ flex: 1, textAlign: 'center' }}>{t('storage_net_internal', '内网')}</Radio.Button>
                </Radio.Group>
              </Form.Item>
            </Col>

            <Col xs={24} sm={12}>
              <Form.Item name={getFieldPath(activeProvider.fieldKeys.region)} hidden>
                <Input />
              </Form.Item>
              <Form.Item
                label={t('storage_data_region', '数据地域')}
              >
                <Select
                  placeholder={t('storage_auto_fill_after_bucket', '选择存储桶后自动填入')}
                  showSearch
                  disabled={true}
                  optionFilterProp="label"
                  value={currentRegionValue}
                >
                  {currentRegionValue && !activeProvider.allRegions.some(r => (r.id ?? r.region) === currentRegionValue || r.region === currentRegionValue) && (
                    <Select.Option key={currentRegionValue} value={currentRegionValue} label={currentRegionValue}>
                      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
                        <span>{currentRegionValue}</span>
                        <span style={{ color: isLight ? '#a1a1aa' : '#52525b', fontSize: 11 }}>
                          {getStorageValue(activeProvider.fieldKeys.endpoint) || ''}
                        </span>
                      </div>
                    </Select.Option>
                  )}
                  {activeProvider.regionGroups.map(g => (
                    <Select.OptGroup
                      key={g.group}
                      label={
                        <span style={{ fontWeight: 600, fontSize: 12, display: 'inline-flex', alignItems: 'center', gap: 5 }}>
                          {g.group.startsWith('🇨🇳') ? <RegionFlagSvg code="CNY" /> : null}
                          <span>{g.group.replace(/^🇨🇳\s*/, '')}</span>
                        </span>
                      }
                    >
                      {g.regions.map(r => {
                        const presetHost = (networkTypes[activeProvider.key] === 'internal' ? r.endpointInternal : r.endpointExternal)
                          .replace(/^https?:\/\//, '');
                        const optionValue = r.id ?? r.region;
                        return (
                          <Select.Option
                            key={optionValue}
                            value={optionValue}
                            label={`${r.label} ${r.region} ${presetHost}`}
                          >
                            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
                              <span>
                                {r.label}
                                <span style={{ marginLeft: 6, color: isLight ? '#a1a1aa' : '#52525b', fontSize: 11 }}>{r.region}</span>
                              </span>
                              <span style={{ color: isLight ? '#a1a1aa' : '#52525b', fontSize: 11 }}>
                                {presetHost}
                              </span>
                            </div>
                          </Select.Option>
                        );
                      })}
                    </Select.OptGroup>
                  ))}
                </Select>
              </Form.Item>
            </Col>

            <Col xs={24} sm={12}>
              <Form.Item
                label={t('storage_endpoint_label', '网络请求地址')}
                name={getFieldPath(activeProvider.fieldKeys.endpoint)}
                extra={
                  <span style={{ fontSize: 11, color: isLight ? '#71717a' : '#a1a1aa' }}>
                    {t('storage_endpoint_auto_hint', '由存储桶与网络类型自动填入，无需手填')}
                  </span>
                }
              >
                <Input
                  placeholder={t('storage_auto_fill_after_bucket', '选择存储桶后自动填入')}
                  readOnly={true}
                />
              </Form.Item>
            </Col>
          </Row>
        </div>

        <Divider style={{ margin: '8px 0 16px', borderColor: isLight ? '#f4f4f5' : '#27272a' }} />

        {/* 模块 3: 域名与路径 (选填) */}
        <div style={{ marginBottom: 8 }}>
          <div style={{ marginBottom: 10 }}>
            <Text strong style={{ fontSize: 13, color: isLight ? '#09090b' : '#fafafa' }}>
              {t('storage_domain_path_title', '域名与路径 (选填)')}
            </Text>
          </div>
          <Row gutter={16}>
            <Col xs={24} sm={12}>
              <Form.Item
                label={t('storage_path_prefix_label', '路径前缀')}
                name={getFieldPath(activeProvider.fieldKeys.pathPrefix)}
                extra={
                  <span style={{ fontSize: 11, color: isLight ? '#71717a' : '#a1a1aa' }}>
                    {prefixLayoutHint}
                  </span>
                }
              >
                <Input placeholder={t('storage_path_prefix_ph', '如 assets/')} />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12}>
              <Form.Item
                label={t('storage_custom_domain_label', '自定义域名 / CDN')}
                name={getFieldPath(activeProvider.fieldKeys.customDomain)}
              >
                <Input placeholder={t('storage_custom_domain_ph', '如 https://cdn.example.com')} />
              </Form.Item>
            </Col>
          </Row>
        </div>

        {renderActionFooter(activeProvider.key)}
      </Card>
      )}

      {/* 存储目录说明：支持所有存储方式与插件场景 */}
      <div style={{
        marginTop: 16,
        border: `1px solid ${isLight ? '#e4e4e7' : '#27272a'}`,
        borderRadius: 8,
        background: isLight ? '#ffffff' : '#09090b',
        padding: '14px 16px',
      }}>
        <Text strong style={{ fontSize: 13, color: isLight ? '#09090b' : '#fafafa' }}>
          {t('storage_dir_guide_title', '存储目录说明')}
        </Text>
        <div style={{ fontSize: 12, color: isLight ? '#71717a' : '#a1a1aa', margin: '4px 0 12px' }}>
          {pluginName && selectedProviderKey === GLOBAL_KEY ? (
            <span>
              {t('storage_dir_guide_follow_site_1', '当前跟随站点存储，本插件文件按规范保存在站点存储的 ')}
              <Text code>{`plugins/${pluginName}/`}</Text>
              {t('storage_dir_guide_follow_site_2', ' 目录下。站点整体存储目录规范如下：')}
            </span>
          ) : (
            <span>
              {pluginName
                ? t('storage_dir_guide_plugin_1', '插件独立存储按以下默认文件夹归类保存，目录前会自动带上路径前缀 ')
                : t('storage_dir_guide_site_1', '站点存储按以下默认文件夹归类保存，目录前会自动带上路径前缀 ')}
              {prefixDir ? <Text code>{prefixDir}</Text> : t('storage_dir_guide_no_prefix', '（当前未填，即存储桶根目录）')}
              {t('storage_dir_guide_2', '。规则对新上传的文件生效，此前上传的文件保留在原路径，照常可访问。')}
            </span>
          )}
        </div>
        {(pluginName && selectedProviderKey !== GLOBAL_KEY ? PLUGIN_STORAGE_DIRS : SITE_STORAGE_DIRS).map((item, idx) => {
          const displayDir = pluginName && selectedProviderKey === GLOBAL_KEY
            ? item.dir
            : `${prefixDir}${item.dir}`;
          return (
            <div
              key={item.dir}
              style={{
                display: 'grid',
                gridTemplateColumns: 'minmax(180px, 240px) 1fr',
                gap: 12,
                padding: '8px 0',
                borderTop: idx === 0 ? 'none' : `1px solid ${isLight ? '#f4f4f5' : '#1f1f23'}`,
                fontSize: 12,
                lineHeight: 1.7,
              }}
            >
              <Text code style={{ wordBreak: 'break-all' }}>{displayDir}</Text>
              <span style={{ color: isLight ? '#3f3f46' : '#d4d4d8' }}>{t(item.descKey, item.desc)}</span>
            </div>
          );
        })}
      </div>

      {/* 附加内容（如配额设置、CORS 指引等） */}
      {extraBottomContent}
    </div>
  );

  if (standalone) {
    return (
      <Form form={form} layout="vertical" requiredMark={false}>
        {panelContent}
      </Form>
    );
  }

  return panelContent;
};
