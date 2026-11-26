import React, { useEffect, useState } from 'react';
import { Table, Button, Space, Tag, Modal, Form, Input, InputNumber, message, Popconfirm, Card, Typography, Tooltip, Row, Col, Grid, theme, Spin, Dropdown, Progress, Checkbox, Select, Alert } from 'antd';
import AppSwitch from '../../components/AppSwitch';
import MobileCardList, { MobileCard, CardRow, CardActions } from '../../components/MobileCardList';
import { listPagination } from '../../components/ListPagination';
import { PlusOutlined, EditOutlined, DeleteOutlined, CopyOutlined, SyncOutlined, EyeOutlined, EyeInvisibleOutlined, KeyOutlined, CheckOutlined, ArrowLeftOutlined, DollarOutlined, BarChartOutlined, EllipsisOutlined, PieChartOutlined, InfoCircleOutlined, ClearOutlined, LockOutlined, StopOutlined, PlayCircleOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import request from '../../utils/request';
import { fetchActivePlugins } from '../../utils/activePlugins';
import { useThemeStore } from '../../store/theme';
import useSettingsStore from '../../store/settings';
import useAuthStore from '../../store/auth';
import type { ApiToken } from '../../types';
import dayjs from 'dayjs';
import { getPeriodicUsed, getQuotaRefreshText, hasPeriodicLimits } from './quotaUtils';
import { QUOTA_RING_BLUE, quotaRingPercent } from '../../utils/quotaPeriod';
import { resolveTimedisplay } from '../../utils/timedisplay';
import { shouldBlockTokenCreate, tokenBindBlockI18nKey } from '../../utils/bindPolicy';
import { copyToClipboard } from '../../utils/clipboard';

const { Title, Text } = Typography;
const { useBreakpoint } = Grid;

const PLAYGROUND_2026_DEDICATED_TOKEN_NAME = '创作中心2026';

function isPlayground2026DedicatedToken(record: Pick<ApiToken, 'name' | 'only_playground_2026'>): boolean {
  return record.only_playground_2026 === 1 && record.name === PLAYGROUND_2026_DEDICATED_TOKEN_NAME;
}

const MOBILE_APP_TOKEN_NAME = '移动端APP';
const MCP_TOKEN_NAME = 'MCP服务';

function isMcpToken(record: Pick<ApiToken, 'name'>): boolean {
  return record.name === MCP_TOKEN_NAME;
}

// 移动端 App 专用令牌：系统创建，不可编辑删除
function isMobileAppToken(record: Pick<ApiToken, 'name' | 'only_playground_2026'>): boolean {
  return record.only_playground_2026 === 1 && record.name === MOBILE_APP_TOKEN_NAME;
}

// 系统专用令牌（如创作中心2026、移动端APP）：不可编辑删除、灰度展示、不提供复制查看密钥按钮；与普通令牌一样可自行启用/禁用
function isSystemDedicatedToken(record: Pick<ApiToken, 'name' | 'only_playground_2026'>): boolean {
  return isPlayground2026DedicatedToken(record) || isMobileAppToken(record) || isMcpToken(record) || record.only_playground_2026 === 1;
}

function isTokenActive(record: Pick<ApiToken, 'is_active'>): boolean {
  return record.is_active === 1 || record.is_active === true;
}

const Tokens: React.FC = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { token: themeToken } = theme.useToken();
  const { themeMode } = useThemeStore();
  const { settings, fetchSettings } = useSettingsStore();
  const user = useAuthStore((s) => s.user);
  const userTimezone = user?.timezone;
  // 与后端令牌热路径一致：用户 timedisplay（个人时区 > 站点默认）
  const quotaTz = (userTimezone?.trim() || resolveTimedisplay() || settings?.site?.default_timezone || 'Asia/Shanghai');
  const isLight = themeMode === 'light';
  const [tokens, setTokens] = useState<ApiToken[]>([]);
  const [loading, setLoading] = useState(true);
  const [detailRefreshing, setDetailRefreshing] = useState(false);
  const [isModalVisible, setIsModalVisible] = useState(false);
  const [editingToken, setEditingToken] = useState<ApiToken | null>(null);
  const [form] = Form.useForm();
  const screens = useBreakpoint();
  const [saving, setSaving] = useState(false);
  const [enableModelFilter, setEnableModelFilter] = useState(false);
  const [availableModels, setAvailableModels] = useState<Array<{ model_id: string; name?: string }>>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [limitQuotaEnabled, setLimitQuotaEnabled] = useState(false);
  const [periodicQuotaEnabled, setPeriodicQuotaEnabled] = useState(false);
  const [ipFilterEnabled, setIpFilterEnabled] = useState(false);
  const [copiedId, setCopiedId] = useState<number | null>(null);

  // 高可用通道确认弹窗状态
  const [isHAConfirmModalOpen, setIsHAConfirmModalOpen] = useState(false);
  const [haConfirmDismiss, setHaConfirmDismiss] = useState(false);
  const [tempValues, setTempValues] = useState<any>(null);

  const sectionContainerStyle: React.CSSProperties = {
    marginBottom: '16px',
    padding: 0,
    background: 'transparent',
    border: 'none',
  };

  const sectionTitleStyle: React.CSSProperties = {
    fontSize: '18px',
    fontWeight: 600,
    color: isLight ? '#09090b' : '#fafafa',
    marginBottom: '16px',
    display: 'block',
  };

  const inputStyle: React.CSSProperties = {
    borderRadius: '6px',
    borderColor: isLight ? '#e4e4e7' : '#27272a',
    background: isLight ? '#ffffff' : '#141414',
    color: isLight ? '#09090b' : '#fafafa',
    fontSize: '14px',
  };



  // 密钥单次创建明文展示状态（合规红线 7：仅创建当次会话在内存中保留与展示）
  const [createdPlaintextModal, setCreatedPlaintextModal] = useState<{ name: string; kid?: string; key: string } | null>(null);
  const [revealedKeys, setRevealedKeys] = useState<Record<number, string>>({});
  const [visibleSessionKeys, setVisibleSessionKeys] = useState<Record<number, boolean>>({});

  // 令牌额度使用详情弹窗状态
  const [isDetailModalOpen, setIsDetailModalOpen] = useState(false);
  const [selectedToken, setSelectedToken] = useState<ApiToken | null>(null);
  const [clearingUsage, setClearingUsage] = useState(false);

  const refreshTokenInList = async (tokenId: number): Promise<ApiToken | null> => {
    const resp = await (request.get('/tokens', { params: { include_playground_2026: 1 } }) as unknown as Promise<{ data: ApiToken[] }>);
    setTokens(resp.data);
    return resp.data.find(t => t.id === tokenId) ?? null;
  };

  const handleShowDetails = async (record: ApiToken) => {
    setSelectedToken(record);
    setIsDetailModalOpen(true);
    setDetailRefreshing(true);
    try {
      const fresh = await refreshTokenInList(record.id);
      if (fresh) setSelectedToken(fresh);
    } catch (e) {
      console.error(e);
    } finally {
      setDetailRefreshing(false);
    }
  };

  const handleClearUsage = async () => {
    if (!selectedToken) return;
    if (isSystemDedicatedToken(selectedToken)) {
      message.warning(t('tokens.system_dedicated_locked', `${selectedToken.name || '系统'} 专用密钥不可修改或删除`));
      return;
    }
    setClearingUsage(true);
    try {
      const updated = await (request.post(`/tokens/${selectedToken.id}/reset-usage`) as unknown as Promise<ApiToken>);
      setSelectedToken(updated);
      setTokens(prev => prev.map(t => (t.id === updated.id ? updated : t)));
      message.success(t('tokens.clear_usage_success', '已清空该令牌的使用额度数据'));
    } catch (e: any) {
      message.error(e?.message || t('tokens.clear_usage_failed', '清空失败'));
    } finally {
      setClearingUsage(false);
    }
  };

  const baseUrl = window.location.origin;

  const [isHAPluginEnabled, setIsHAPluginEnabled] = useState(false);

  const fetchTokens = async () => {
    setLoading(true);
    try {
      const resp = await (request.get('/tokens', { params: { include_playground_2026: 1 } }) as unknown as Promise<{ data: ApiToken[] }>);
      setTokens(resp.data);
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  const fetchPluginsState = async () => {
    try {
      const pluginRes = await fetchActivePlugins();
      const isActiveHA = pluginRes?.active_plugins?.some((p: any) => p.name === 'high_availability_channel');
      setIsHAPluginEnabled(!!isActiveHA);
    } catch { /* ignore */ }
  };

  const fetchAvailableModels = async () => {
    setModelsLoading(true);
    try {
      // 与模型广场一致：仅加载广场中已展示的模型（隐藏/未启用的不出现）
      const res = await (request.get('/marketplace/public') as Promise<{
        models?: Array<{ model_id: string; name?: string }>;
      }>);
      const models = res?.models || [];
      const seen = new Set<string>();
      setAvailableModels(models.filter((m) => {
        if (!m.model_id || seen.has(m.model_id)) return false;
        seen.add(m.model_id);
        return true;
      }));
    } catch (e) {
      console.error(e);
      setAvailableModels([]);
    } finally {
      setModelsLoading(false);
    }
  };

  useEffect(() => {
    fetchTokens();
    fetchPluginsState();
    fetchSettings();
    fetchAvailableModels();
  }, []);

  const handleCopy = async (key: string, tokenId: number) => {
    const ok = await copyToClipboard(key);
    if (ok) {
      setCopiedId(tokenId);
      setTimeout(() => setCopiedId(prev => prev === tokenId ? null : prev), 2000);
    } else {
      message.error(t('tokens.copy_failed', '复制失败，请手动选择复制'));
    }
  };

  const handleAdd = () => {
    if (shouldBlockTokenCreate(settings?.registration, user)) {
      Modal.confirm({
        title: t('tokens.bind_required_title'),
        content: t(tokenBindBlockI18nKey(settings?.registration)),
        okText: t('tokens.go_bind'),
        cancelText: t('common.cancel'),
        onOk: () => navigate('/profile'),
      });
      return;
    }
    setEditingToken(null);
    setSaving(false);
    form.resetFields();
    form.setFieldsValue({
      is_active: true,
      high_availability: true,
      daily_quota_limit: -1,
      weekly_quota_limit: -1,
      monthly_quota_limit: -1,
    });
    setEnableModelFilter(false);
    setLimitQuotaEnabled(false);
    setPeriodicQuotaEnabled(false);
    setIpFilterEnabled(false);
    setIsModalVisible(true);
  };

  const handleEdit = (record: ApiToken) => {
    if (isSystemDedicatedToken(record)) {
      message.warning(t('tokens.system_dedicated_locked', `${record.name || '系统'} 专用密钥不可修改或删除`));
      return;
    }
    setEditingToken(record);
    setSaving(false);
    const models = record.allowed_models ? (typeof record.allowed_models === 'string' ? JSON.parse(record.allowed_models) : record.allowed_models) : [];
    const hasModels = Array.isArray(models) && models.length > 0;
    setEnableModelFilter(hasModels);
    setLimitQuotaEnabled(record.quota_limit >= 0);
    setPeriodicQuotaEnabled(hasPeriodicLimits(record));
    const hasIpLimit = record.allowed_ips !== undefined && record.allowed_ips !== null && record.allowed_ips.trim() !== '';
    setIpFilterEnabled(hasIpLimit);
    form.setFieldsValue({
      ...record,
      is_active: isTokenActive(record),
      quota_limit: record.quota_limit === -1 ? undefined : record.quota_limit,
      daily_quota_limit: record.daily_quota_limit === -1 ? undefined : record.daily_quota_limit,
      weekly_quota_limit: record.weekly_quota_limit === -1 ? undefined : record.weekly_quota_limit,
      monthly_quota_limit: record.monthly_quota_limit === -1 ? undefined : record.monthly_quota_limit,
      rps_limit: record.rps_limit === 0 ? undefined : record.rps_limit,
      rpm_limit: record.rpm_limit === 0 ? undefined : record.rpm_limit,
      high_availability: record.high_availability === 1,
      allowed_models: Array.isArray(models) ? models : [],
    });
    setIsModalVisible(true);
  };

  const handleDelete = async (id: number) => {
    const target = tokens.find((item) => item.id === id);
    if (target && isSystemDedicatedToken(target)) {
      message.warning(t('tokens.system_dedicated_locked', `${target.name || '系统'} 专用密钥不可修改或删除`));
      return;
    }
    try {
      await request.delete(`/tokens/${id}`);
      message.success(t('common.success'));
      fetchTokens();
    } catch (e) {
      console.error(e);
    }
  };

  const [togglingId, setTogglingId] = useState<number | null>(null);
  const [justToggledId, setJustToggledId] = useState<number | null>(null);

  // 令牌开关：只改归属人意图（is_active），账户欠费/过期/额度用尽由后端以 unavailable_reason 单独呈现
  const handleToggleActive = async (record: ApiToken, active: boolean) => {
    setTogglingId(record.id);
    try {
      const updated = await (request.put(`/tokens/${record.id}`, { is_active: active ? 1 : 0 }) as unknown as Promise<ApiToken>);
      setTokens(prev => prev.map(item => (item.id === updated.id ? updated : item)));
      setJustToggledId(record.id);
      setTimeout(() => {
        setJustToggledId(prev => (prev === record.id ? null : prev));
      }, 420);
    } catch (e) {
      console.error(e);
    } finally {
      setTogglingId(null);
    }
  };

  // 关闭需二次确认（防误触），开启直接生效
  const renderActiveSwitch = (record: ApiToken) => {
    const active = isTokenActive(record);
    const loading = togglingId === record.id;
    const isJustToggled = justToggledId === record.id;
    const btnClass = `token-toggle-active-btn ${active ? 'is-active' : 'is-disabled'}${isJustToggled ? (active ? ' is-just-toggled-on' : ' is-just-toggled-off') : ''}`;
    if (!active) {
      return (
        <Button
          size="small"
          className={btnClass}
          icon={<StopOutlined key="icon-stop" className="token-toggle-state-icon" />}
          danger
          loading={loading}
          onClick={() => handleToggleActive(record, true)}
        />
      );
    }
    return (
      <Popconfirm
        title={t('tokens.disable_confirm', '确认禁用该令牌？')}
        onConfirm={() => handleToggleActive(record, false)}
      >
        <Button
          size="small"
          className={btnClass}
          icon={<PlayCircleOutlined key="icon-play" className="token-toggle-state-icon" />}
          loading={loading}
        />
      </Popconfirm>
    );
  };

  const unavailableReasonLabel = (reason: ApiToken['unavailable_reason']): string | null => {
    switch (reason) {
      case 'arrears': return t('tokens.reason_arrears', '账户欠费');
      case 'no_funds': return t('tokens.reason_no_funds', '无可用额度');
      case 'expired': return t('tokens.reason_expired', '已过期');
      case 'quota_total': return t('tokens.reason_quota_total', '总额度用尽');
      case 'quota_daily': return t('tokens.reason_quota_daily', '今日额度用尽');
      case 'quota_weekly': return t('tokens.reason_quota_weekly', '本周额度用尽');
      case 'quota_monthly': return t('tokens.reason_quota_monthly', '本月额度用尽');
      default: return null;
    }
  };

  const unavailableReasonTip = (reason: ApiToken['unavailable_reason']): string => {
    switch (reason) {
      case 'arrears':
        return t('tokens.reason_arrears_tip', '账户当前处于欠费状态，调用将被拒绝；充值结清后自动恢复，无需重新开启');
      case 'no_funds':
        return t('tokens.reason_no_funds_tip', '账户当前无可用余额、赠送金、授信或有效卡券，调用将被拒绝；充值或获赠/兑券后自动恢复');
      default:
        return t('tokens.unavailable_tooltip', '令牌已启用，但当前调用会被拒绝');
    }
  };

  // 状态标签：启用/已禁用（归属人开关）+ 开关开启但调用会被拒绝时的原因标签 / 超 30 天闲置自动关闭标签
  const renderStatusTags = (record: ApiToken, isSystem: boolean) => {
    const isActive = isTokenActive(record);
    const isIdleDisabled = record.is_active === 3;
    const reasonLabel = isActive ? unavailableReasonLabel(record.unavailable_reason) : null;
    const tagStyle = { margin: 0, fontSize: '11px', padding: '0 4px', lineHeight: '16px' };
    return (
      <>
        <Tag color={isSystem ? 'default' : (isActive ? 'default' : 'error')} style={tagStyle}>
          {isActive ? t('common.active', '启用') : t('common.disabled', '禁用')}
        </Tag>
        {isIdleDisabled && (
          <Tooltip title={t('tokens.reason_inactive_30d_tip', '超过 30 天未调用已自动关闭，使用前请手动重新开启')}>
            <Tag color="warning" style={tagStyle}>{t('tokens.reason_inactive_30d', '闲置关闭')}</Tag>
          </Tooltip>
        )}
        {reasonLabel && (
          <Tooltip title={unavailableReasonTip(record.unavailable_reason)}>
            <Tag color="warning" style={tagStyle}>{reasonLabel}</Tag>
          </Tooltip>
        )}
      </>
    );
  };

  const executeSave = async (formValues: any, shouldDismissHA: boolean) => {
    const data = {
      ...formValues,
      is_active: formValues.is_active === undefined ? (editingToken ? (isTokenActive(editingToken) ? 1 : 0) : 1) : (formValues.is_active ? 1 : 0),
      quota_limit: (formValues.quota_limit === undefined || formValues.quota_limit === null) ? -1 : formValues.quota_limit,
      daily_quota_limit: (formValues.daily_quota_limit === undefined || formValues.daily_quota_limit === null) ? -1 : formValues.daily_quota_limit,
      weekly_quota_limit: (formValues.weekly_quota_limit === undefined || formValues.weekly_quota_limit === null) ? -1 : formValues.weekly_quota_limit,
      monthly_quota_limit: (formValues.monthly_quota_limit === undefined || formValues.monthly_quota_limit === null) ? -1 : formValues.monthly_quota_limit,
      rps_limit: (formValues.rps_limit === undefined || formValues.rps_limit === null) ? 0 : formValues.rps_limit,
      rpm_limit: (formValues.rpm_limit === undefined || formValues.rpm_limit === null) ? 0 : formValues.rpm_limit,
      allowed_ips: ipFilterEnabled ? (formValues.allowed_ips || '') : '',
      // 创作中心标识仅由创作中心创建令牌时自动写入；令牌管理页创建恒为 0，编辑时保留原值
      only_playground: editingToken ? editingToken.only_playground : 0,
      only_playground_2026: editingToken ? editingToken.only_playground_2026 : 0,
      high_availability: formValues.high_availability ? 1 : 0,
      allowed_models: enableModelFilter
        ? (Array.isArray(formValues.allowed_models)
            ? formValues.allowed_models.map((m: string) => String(m).trim()).filter(Boolean)
            : [])
        : [],
    };

    try {
      const res = editingToken
        ? await (request as any).put(`/tokens/${editingToken.id}`, data, { skipErrorHandler: true })
        : await (request as any).post('/tokens', data, { skipErrorHandler: true });
      if (shouldDismissHA) {
        const tokenId = editingToken?.id ?? res?.id;
        if (tokenId) localStorage.setItem(`ha_confirm_dismiss_${tokenId}`, 'true');
      }
      if (!editingToken && res?.key) {
        if (res?.id) {
          setRevealedKeys(prev => ({ ...prev, [res.id]: res.key }));
          setVisibleSessionKeys(prev => ({ ...prev, [res.id]: true }));
        }
        setCreatedPlaintextModal({ name: res.name || data.name, kid: res.kid, key: res.key });
      }
      message.success(t('common.success'));
      setIsModalVisible(false);
      fetchTokens();
    } catch (e: any) {
      const rawErr = e?.response?.data?.error;
      const serverMsg = (typeof rawErr === 'string' ? rawErr : rawErr?.message) || e?.message || t('tokens.create_failed', '创建失败');
      setIsModalVisible(false);
      message.error(serverMsg);
    } finally {
      setSaving(false);
    }
  };

  const handleSave = async (values: { allowed_models?: string[]; allowed_ips?: string; [key: string]: unknown }) => {
    const isDismissed = editingToken && localStorage.getItem(`ha_confirm_dismiss_${editingToken.id}`) === 'true';

    if (values.high_availability && !isDismissed) {
      setTempValues(values);
      setHaConfirmDismiss(false);
      setIsHAConfirmModalOpen(true);
    } else {
      executeSave(values, false);
    }
  };

  // 本次新建密钥仍在会话内存中时可切换明文/脱敏展示；历史密钥受红线 7 约束不再支持二次明文查看
  const toggleSessionKeyVisibility = (tokenId: number) => {
    if (!revealedKeys[tokenId]) return;
    setVisibleSessionKeys(prev => ({ ...prev, [tokenId]: !prev[tokenId] }));
  };

  // 渲染密钥列内容（桌面端）
  const renderTokenKey = (key: string, record: ApiToken) => {
    const isSystem = isSystemDedicatedToken(record);
    const sessionPlaintext = !isSystem ? revealedKeys[record.id] : undefined;
    const isRevealed = !!sessionPlaintext && !!visibleSessionKeys[record.id];
    const copyValue = sessionPlaintext || record.kid || key;
    return (
      <div style={{
        display: 'flex',
        alignItems: 'center',
        width: 'max-content',
      }}>
        {isRevealed ? (
          <Text
            style={{ fontFamily: 'monospace', fontSize: 13, color: themeToken.colorText, letterSpacing: '0.5px', userSelect: 'all' }}
            copyable={{ text: sessionPlaintext }}
          >
            {sessionPlaintext}
          </Text>
        ) : (
          <Text style={{ fontFamily: 'monospace', fontSize: 13, color: isSystem ? (isLight ? '#71717a' : '#a1a1aa') : themeToken.colorText, letterSpacing: '0.5px' }}>
            {key.length > 16 ? (
              <>
                {key.substring(0, 10)}<span style={{ color: isSystem ? (isLight ? '#a1a1aa' : '#52525b') : '#666', margin: '0 4px' }}>••••••••</span>{key.substring(key.length - 6)}
              </>
            ) : (
              key
            )}
          </Text>
        )}
        {!isSystem && (
          <>
            <Tooltip title={copiedId === record.id ? t('tokens.copied', '已复制!') : (sessionPlaintext ? t('tokens.copy_full_key', '复制完整密钥') : t('tokens.copy_kid_hint', '复制密钥 ID（完整密钥仅创建时可见）'))}>
              <Button 
                type="text" 
                icon={copiedId === record.id ? <CheckOutlined style={{ color: themeToken.colorText }} /> : <CopyOutlined />} 
                size="small" 
                onClick={() => handleCopy(copyValue, record.id)} 
                style={{ color: copiedId === record.id ? themeToken.colorText : '#888', marginLeft: 8 }} 
              />
            </Tooltip>
            {sessionPlaintext && (
              <Tooltip title={isRevealed ? t('tokens.hide_key', '隐藏密钥') : t('tokens.view_key', '查看完整密钥')}>
                <Button 
                  type="text" 
                  icon={isRevealed ? <EyeInvisibleOutlined /> : <EyeOutlined />} 
                  size="small" 
                  onClick={() => toggleSessionKeyVisibility(record.id)} 
                  style={{ color: isRevealed ? themeToken.colorText : '#888' }} 
                />
              </Tooltip>
            )}
          </>
        )}
      </div>
    );
  };

  const renderTokenQuotaRings = (record: ApiToken) => {
    const isSystem = isSystemDedicatedToken(record);
    const { dailyUsed, weeklyUsed, monthlyUsed } = getPeriodicUsed(record, quotaTz);
    const fmt = (n: number) => (Number.isInteger(n) ? String(n) : n.toFixed(6));
    const items: {
      key: keyof typeof QUOTA_RING_BLUE;
      label: string;
      tipLabel: string;
      used: number;
      limit: number;
    }[] = [
      { key: 'total', label: t('tokens.quota_ring_total', '总'), tipLabel: t('tokens.limit'), used: record.quota_used || 0, limit: record.quota_limit ?? -1 },
      { key: 'month', label: t('tokens.quota_ring_month', '月'), tipLabel: t('tokens.monthly_cap'), used: monthlyUsed, limit: record.monthly_quota_limit ?? -1 },
      { key: 'week', label: t('tokens.quota_ring_week', '周'), tipLabel: t('tokens.weekly_cap'), used: weeklyUsed, limit: record.weekly_quota_limit ?? -1 },
      { key: 'day', label: t('tokens.quota_ring_day', '日'), tipLabel: t('tokens.daily_cap'), used: dailyUsed, limit: record.daily_quota_limit ?? -1 },
    ];
    const hasAnyConfigured = items.some((item) => item.limit >= 0);

    const slotWidth = 28;
    const ringSize = 24;
    const ringStroke = 5;
    const slotStyle: React.CSSProperties = {
      width: slotWidth,
      display: 'flex',
      flexDirection: 'column',
      alignItems: 'center',
      gap: 1,
    };
    const labelStyle: React.CSSProperties = {
      fontSize: 9,
      color: isLight ? 'rgba(0,0,0,0.4)' : 'rgba(255,255,255,0.45)',
      lineHeight: 1,
      transform: 'scale(0.92)',
      maxWidth: slotWidth,
      overflow: 'hidden',
      textOverflow: 'ellipsis',
      whiteSpace: 'nowrap',
    };

    return (
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: `repeat(4, ${slotWidth}px)`,
          gap: 3,
          alignItems: 'center',
          justifyContent: 'start',
          width: 122,
        }}
      >
        {items.map((item, index) => {
          const configured = item.limit >= 0;
          const showUnlimited = !hasAnyConfigured && index === 0;
          const showRing = configured || showUnlimited;

          if (!showRing) {
            return (
              <div key={item.key} style={{ ...slotStyle, visibility: 'hidden' }} aria-hidden>
                <div style={{ width: ringSize, height: ringSize }} />
                <span style={labelStyle}>{item.label}</span>
              </div>
            );
          }

          const pct = configured ? quotaRingPercent(item.used, item.limit) : 0;
          const tip = showUnlimited
            ? `${t('tokens.limit')}: ${fmt(item.used)} / ∞ (${t('tokens.unlimited')})`
            : `${item.tipLabel}: ${fmt(item.used)} / ${fmt(item.limit)} (${pct}%)`;
          const stroke = isSystem
            ? (isLight ? '#a1a1aa' : 'rgba(255,255,255,0.3)')
            : (showUnlimited
              ? (isLight ? '#a1a1aa' : 'rgba(255,255,255,0.28)')
              : (pct >= 100 ? '#ef4444' : QUOTA_RING_BLUE[item.key]));

          return (
            <Tooltip key={item.key} title={tip}>
              <div style={slotStyle}>
                <Progress
                  type="circle"
                  percent={showUnlimited ? 100 : pct}
                  size={ringSize}
                  strokeWidth={ringStroke}
                  strokeColor={stroke}
                  trailColor={isLight ? '#e4e4e7' : 'rgba(255,255,255,0.12)'}
                  format={() => (
                    <span
                      style={{
                        fontSize: showUnlimited ? 9 : 8,
                        fontWeight: 600,
                        color: isLight ? 'rgba(0,0,0,0.72)' : 'rgba(255,255,255,0.88)',
                        lineHeight: 1,
                      }}
                    >
                      {showUnlimited ? '∞' : `${pct}%`}
                    </span>
                  )}
                />
                <span style={labelStyle}>{showUnlimited ? t('tokens.quota_ring_unlimited', '无限') : item.label}</span>
              </div>
            </Tooltip>
          );
        })}
      </div>
    );
  };

  const columns = [
    {
      title: t('tokens.kid'),
      dataIndex: 'kid',
      key: 'kid',
      width: 110,
      render: (kid: string) => kid ? <Tag color="default" style={{ fontFamily: 'monospace', fontSize: 12 }}>{kid}</Tag> : <Text type="secondary">-</Text>,
    },
    {
      title: t('tokens.name'),
      dataIndex: 'name',
      key: 'name',
      sorter: (a: ApiToken, b: ApiToken) => a.name.localeCompare(b.name),
      render: (text: string, record: ApiToken) => {
        const isSystem = isSystemDedicatedToken(record);
        const isPlayground = record.only_playground === 1;
        const isPlayground2026 = record.only_playground_2026 === 1;
        const isHA = record.high_availability === 1 || (record.high_availability as any) === true;
        const scopeLabel = isPlayground && isPlayground2026
          ? t('tokens.playground_both')
          : isPlayground2026
            ? t('tokens.playground_2026_only')
            : isPlayground
              ? t('tokens.playground_only')
              : t('tokens.general');
        return (
          <Space direction="vertical" size={2} style={{ display: 'flex' }}>
            <Text strong style={{ color: isSystem ? (isLight ? '#71717a' : '#a1a1aa') : undefined, lineHeight: 1.25 }}>{text}</Text>
            <Space size={4} wrap>
              <Tag color="default" style={{ margin: 0, fontSize: '11px', padding: '0 4px', lineHeight: '16px' }}>
                {scopeLabel}
              </Tag>
              {isPlayground2026DedicatedToken(record) && (
                <Tag color="default" style={{ margin: 0, fontSize: '11px', padding: '0 4px', lineHeight: '16px' }}>
                  {t('tokens.playground_2026_dedicated', '系统专用')}
                </Tag>
              )}
              {isMobileAppToken(record) && (
                <Tag color="default" style={{ margin: 0, fontSize: '11px', padding: '0 4px', lineHeight: '16px' }}>
                  {t('tokens.mobile_app_dedicated', '移动端专用')}
                </Tag>
              )}
              {isMcpToken(record) && (
                <Tag color="default" style={{ margin: 0, fontSize: '11px', padding: '0 4px', lineHeight: '16px' }}>
                  MCP 专用
                </Tag>
              )}
              {isHA && (
                <Tag color="default" style={{ margin: 0, fontSize: '11px', padding: '0 4px', lineHeight: '16px' }}>
                  HA
                </Tag>
              )}
              {renderStatusTags(record, isSystem)}
            </Space>
          </Space>
        );
      },
    },
    {
      title: t('tokens.key'),
      dataIndex: 'token_key',
      key: 'token_key',
      render: (key: string, record: ApiToken) => renderTokenKey(key, record),
    },
    {
      title: t('tokens.usage_quota'),
      key: 'usage',
      width: 150,
      sorter: (a: ApiToken, b: ApiToken) => a.quota_used - b.quota_used,
      render: (record: ApiToken) => renderTokenQuotaRings(record),
    },
    {
      title: t('tokens.limits'),
      key: 'limits',
      render: (record: ApiToken) => (
        <Space direction="vertical" size={1}>
          <Text type="secondary" style={{ fontSize: 12, lineHeight: 1.25 }}>RPS: {record.rps_limit || '∞'}</Text>
          <Text type="secondary" style={{ fontSize: 12, lineHeight: 1.25 }}>RPM: {record.rpm_limit || '∞'}</Text>
        </Space>
      ),
    },
    {
      title: t('tokens.time_info', '时间信息'),
      key: 'times',
      sorter: (a: ApiToken, b: ApiToken) => dayjs(a.created_at).unix() - dayjs(b.created_at).unix(),
      render: (record: ApiToken) => (
        <Space direction="vertical" size={1}>
          <Text type="secondary" style={{ fontSize: 12, lineHeight: 1.25 }}>
            {t('tokens.created_at', '创建: ')}{dayjs(record.created_at).format('YYYY-MM-DD HH:mm')}
          </Text>
          <Text type="secondary" style={{ fontSize: 12, lineHeight: 1.25 }}>
            {t('tokens.last_used_at', '最后使用: ')}{record.last_used_at ? record.last_used_at : t('tokens.never_used', '从未')}
          </Text>
        </Space>
      ),
    },
    {
      title: t('common.actions'),
      key: 'actions',
      render: (_: unknown, record: ApiToken) => {
        const isSystem = isSystemDedicatedToken(record);
        return (
          <Space size={4}>
            {renderActiveSwitch(record)}
            <Button size="small" icon={<PieChartOutlined />} onClick={() => handleShowDetails(record)} />
            {isSystem ? (
              <Tooltip title={t('tokens.system_dedicated_locked', `${record.name || '系统'} 专用密钥不可修改或删除`)}>
                <Button size="small" icon={<LockOutlined />} disabled />
              </Tooltip>
            ) : (
              <>
                <Button size="small" icon={<EditOutlined />} onClick={() => handleEdit(record)} />
                <Popconfirm title={t('common.confirm_delete')} onConfirm={() => handleDelete(record.id)}>
                  <Button size="small" icon={<DeleteOutlined />} />
                </Popconfirm>
              </>
            )}
          </Space>
        );
      },
    },
  ];

  return (
    <Card 
      variant="borderless" 
      style={{ 
        minHeight: '60vh', 
        display: 'flex', 
        flexDirection: 'column',
        background: 'transparent', 
        boxShadow: 'none'
      }} 
      styles={{ body: { flex: 1, display: 'flex', flexDirection: 'column', padding: screens.xs ? 0 : '16px 24px 24px' } }}
    >
      <style>{`
        .system-dedicated-token-row {
          opacity: 0.72;
        }
        .system-dedicated-token-row td {
          background-color: ${isLight ? 'rgba(0, 0, 0, 0.025)' : 'rgba(255, 255, 255, 0.025)'} !important;
          filter: grayscale(100%);
        }
        .system-dedicated-token-row:hover td {
          background-color: ${isLight ? 'rgba(0, 0, 0, 0.045)' : 'rgba(255, 255, 255, 0.045)'} !important;
        }

        /* 紧凑表格内边距与行间距优化（严格排除测量行以防止产生黑色空行间隙） */
        .tokens-compact-table.ant-table-wrapper .ant-table-thead > tr > th {
          padding: 8px 10px !important;
        }
        .tokens-compact-table.ant-table-wrapper .ant-table-tbody > tr:not(.ant-table-measure-row) > td {
          padding: 6px 10px !important;
        }
        .tokens-compact-table.ant-table-wrapper .ant-table-measure-row,
        .tokens-compact-table.ant-table-wrapper .ant-table-measure-row td,
        .tokens-compact-table.ant-table-wrapper tr.ant-table-measure-row,
        .tokens-compact-table.ant-table-wrapper tr.ant-table-measure-row td,
        .tokens-compact-table.ant-table-wrapper tr.ant-table-measure-row .ant-table-cell {
          padding: 0 !important;
          height: 0 !important;
          min-height: 0 !important;
          font-size: 0 !important;
          line-height: 0 !important;
          border: none !important;
          visibility: hidden !important;
        }

        /* 令牌列表底部最后一行左右圆角与表头圆角对齐 */
        .tokens-compact-table.ant-table-wrapper .ant-table,
        .tokens-compact-table.ant-table-wrapper .ant-table-container {
          border-radius: ${themeToken.borderRadius}px !important;
          overflow: hidden !important;
        }
        .tokens-compact-table.ant-table-wrapper .ant-table-tbody > tr.ant-table-row:last-child > td:first-child,
        .tokens-compact-table.ant-table-wrapper .ant-table-tbody > tr:not(.ant-table-measure-row):last-child > td:first-child {
          border-bottom-left-radius: ${themeToken.borderRadius}px !important;
          border-end-start-radius: ${themeToken.borderRadius}px !important;
        }
        .tokens-compact-table.ant-table-wrapper .ant-table-tbody > tr.ant-table-row:last-child > td:last-child,
        .tokens-compact-table.ant-table-wrapper .ant-table-tbody > tr:not(.ant-table-measure-row):last-child > td:last-child {
          border-bottom-right-radius: ${themeToken.borderRadius}px !important;
          border-end-end-radius: ${themeToken.borderRadius}px !important;
        }

        /* 醒目创建令牌按钮动效 */
        .create-token-action-btn {
          position: relative !important;
          overflow: hidden !important;
          transition: all 0.25s cubic-bezier(0.4, 0, 0.2, 1) !important;
          animation: tokenBtnPulse 2.8s ease-in-out infinite;
        }
        .create-token-action-btn::after {
          content: '';
          position: absolute;
          top: -50%;
          left: -75%;
          width: 50%;
          height: 200%;
          background: linear-gradient(
            90deg,
            transparent,
            rgba(255, 255, 255, ${isLight ? '0.35' : '0.5'}),
            transparent
          );
          transform: rotate(25deg);
          animation: tokenBtnShimmer 3s cubic-bezier(0.4, 0, 0.2, 1) infinite;
          pointer-events: none;
        }
        .create-token-action-btn:hover {
          transform: translateY(-1.5px) !important;
          box-shadow: 0 6px 18px ${isLight ? 'rgba(0, 0, 0, 0.28)' : 'rgba(255, 255, 255, 0.22)'} !important;
        }
        .create-token-action-btn:active {
          transform: translateY(0) !important;
        }
        .create-token-action-btn .anticon-key {
          display: inline-block;
          animation: tokenKeyWiggle 3.2s ease-in-out infinite;
        }
        @keyframes tokenBtnPulse {
          0%, 100% {
            box-shadow: 0 0 0 0 ${isLight ? 'rgba(9, 9, 11, 0.2)' : 'rgba(255, 255, 255, 0.25)'}, 0 2px 6px rgba(0, 0, 0, 0.08);
          }
          50% {
            box-shadow: 0 0 0 5px ${isLight ? 'rgba(9, 9, 11, 0.08)' : 'rgba(255, 255, 255, 0.12)'}, 0 4px 16px ${isLight ? 'rgba(0, 0, 0, 0.22)' : 'rgba(255, 255, 255, 0.24)'};
          }
        }
        @keyframes tokenBtnShimmer {
          0% {
            left: -75%;
            opacity: 0;
          }
          15% {
            opacity: 1;
          }
          45% {
            left: 140%;
            opacity: 0.85;
          }
          46%, 100% {
            left: 140%;
            opacity: 0;
          }
        }
        @keyframes tokenKeyWiggle {
          0%, 80%, 100% {
            transform: rotate(0deg);
          }
          85% {
            transform: rotate(-12deg);
          }
          90% {
            transform: rotate(10deg);
          }
          95% {
            transform: rotate(-6deg);
          }
        }

        /* 操作列启用/禁用令牌切换按钮动效 */
        .token-toggle-active-btn {
          transition:
            transform 0.18s ease-out,
            border-color 0.2s ease,
            background-color 0.2s ease,
            color 0.2s ease,
            box-shadow 0.2s ease !important;
        }
        .token-toggle-active-btn:not(:disabled):hover {
          transform: scale(1.03);
        }
        .token-toggle-active-btn:not(:disabled):active {
          transform: scale(0.96);
        }
        .token-toggle-active-btn .token-toggle-state-icon {
          display: inline-flex;
          transition: transform 0.18s ease-out;
        }
        .token-toggle-active-btn:not(:disabled):hover .token-toggle-state-icon {
          transform: scale(1.04);
        }
        .token-toggle-active-btn.is-just-toggled-on {
          animation: tokenTogglePulseOn 0.36s cubic-bezier(0.22, 1, 0.36, 1);
        }
        .token-toggle-active-btn.is-just-toggled-off {
          animation: tokenTogglePulseOff 0.36s cubic-bezier(0.22, 1, 0.36, 1);
        }
        .token-toggle-active-btn.is-just-toggled-on .token-toggle-state-icon {
          animation: tokenToggleIconFlipOn 0.32s cubic-bezier(0.22, 1, 0.36, 1);
        }
        .token-toggle-active-btn.is-just-toggled-off .token-toggle-state-icon {
          animation: tokenToggleIconFlipOff 0.32s cubic-bezier(0.22, 1, 0.36, 1);
        }
        @keyframes tokenTogglePulseOn {
          0% {
            transform: scale(0.95);
            box-shadow: 0 0 0 0 ${isLight ? 'rgba(24, 24, 27, 0.22)' : 'rgba(250, 250, 250, 0.24)'};
          }
          50% {
            transform: scale(1.03);
            box-shadow: 0 0 0 3px ${isLight ? 'rgba(24, 24, 27, 0)' : 'rgba(250, 250, 250, 0)'};
          }
          100% {
            transform: scale(1);
            box-shadow: 0 0 0 0 rgba(0, 0, 0, 0);
          }
        }
        @keyframes tokenTogglePulseOff {
          0% {
            transform: scale(0.95);
            box-shadow: 0 0 0 0 rgba(255, 77, 79, 0.26);
          }
          50% {
            transform: scale(1.03);
            box-shadow: 0 0 0 3px rgba(255, 77, 79, 0);
          }
          100% {
            transform: scale(1);
            box-shadow: 0 0 0 0 rgba(255, 77, 79, 0);
          }
        }
        @keyframes tokenToggleIconFlipOn {
          0% {
            opacity: 0.45;
            transform: scale(0.78) rotate(-24deg);
          }
          70% {
            opacity: 1;
            transform: scale(1.04) rotate(0deg);
          }
          100% {
            opacity: 1;
            transform: scale(1) rotate(0deg);
          }
        }
        @keyframes tokenToggleIconFlipOff {
          0% {
            opacity: 0.45;
            transform: scale(0.78) rotate(24deg);
          }
          70% {
            opacity: 1;
            transform: scale(1.04) rotate(0deg);
          }
          100% {
            opacity: 1;
            transform: scale(1) rotate(0deg);
          }
        }
      `}</style>
      {!isModalVisible ? (
        <>
          {loading && tokens.length === 0 ? (
        <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <Spin size="large" />
        </div>
      ) : tokens.length === 0 ? (
        <div style={{ display: 'flex', flexDirection: 'column', flex: 1 }}>
          <div style={{ marginBottom: 48 }}>
             <Title level={screens.xs ? 4 : 2} style={{ margin: 0, marginBottom: 8, display: 'flex', alignItems: 'baseline' }}>
               {t('tokens.title')}
               <Text type="secondary" style={{ fontSize: 14, marginLeft: 12, fontWeight: 'normal' }}>{t('tokens.subtitle')}</Text>
             </Title>
             <div style={{ marginTop: 16, display: 'inline-flex', alignItems: 'center', flexWrap: 'wrap', gap: 12, background: isLight ? '#f5f5f5' : 'rgba(255,255,255,0.04)', padding: '8px 16px', borderRadius: 8, border: `1px solid ${isLight ? '#e5e5e5' : '#333'}` }}>
                <Text type="secondary" style={{ fontSize: 13, fontWeight: 500, whiteSpace: 'nowrap' }}>{t('tokens.api_base_url', 'API Base URL:')}</Text>
                <Text 
                  style={{ fontFamily: 'monospace', fontSize: 14, fontWeight: 'bold', color: themeToken.colorText, wordBreak: 'break-all' }}
                  copyable={{ 
                    text: baseUrl,
                    icon: [
                      <CopyOutlined key="copy-icon" style={{ color: themeToken.colorTextSecondary }} />,
                      <CheckOutlined key="copied-icon" style={{ color: themeToken.colorText }} />
                    ]
                  }}
                >
                  {baseUrl}
                </Text>
             </div>
          </div>
          
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', paddingBottom: '10vh' }}>
             <div style={{
                width: 48, 
                height: 48, 
                background: 'rgba(255, 255, 255, 0.08)',
                borderRadius: 12,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                marginBottom: 24,
                boxShadow: '0 2px 8px rgba(0,0,0,0.1)'
             }}>
               <KeyOutlined style={{ fontSize: 24, color: themeToken.colorText }} />
             </div>
             <Title level={4} style={{ margin: 0, marginBottom: 12 }}>{t('tokens.empty_title')}</Title>
             <Text type="secondary" style={{ marginBottom: 24 }}>{t('tokens.empty_desc')}</Text>
             <Button type="primary" size="large" className="create-token-action-btn" onClick={handleAdd} style={{ padding: '0 32px' }}>
                {t('tokens.create')}
             </Button>
          </div>
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', flex: 1 }}>
          <div style={{ marginBottom: 24 }}>
            <Title level={screens.xs ? 4 : 2} style={{ margin: 0, display: 'flex', alignItems: 'baseline' }}>
              {t('tokens.title')}
              <Text type="secondary" style={{ fontSize: 13, marginLeft: 12, fontWeight: 'normal' }}>{t('tokens.subtitle')}</Text>
            </Title>
            <div style={{ marginTop: 12, display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 12 }}>
              <div style={{ display: 'inline-flex', alignItems: 'center', flexWrap: 'wrap', gap: 12, background: isLight ? '#f5f5f5' : 'rgba(255,255,255,0.04)', padding: '8px 16px', borderRadius: 8, border: `1px solid ${isLight ? '#e5e5e5' : '#333'}` }}>
                <Text type="secondary" style={{ fontSize: 13, fontWeight: 500, whiteSpace: 'nowrap' }}>{t('tokens.api_base_url', 'API Base URL:')}</Text>
                <Text 
                  style={{ fontFamily: 'monospace', fontSize: 14, fontWeight: 'bold', color: themeToken.colorText, wordBreak: 'break-all' }}
                  copyable={{ 
                    text: baseUrl,
                    icon: [
                      <CopyOutlined key="copy-icon" style={{ color: themeToken.colorTextSecondary }} />,
                      <CheckOutlined key="copied-icon" style={{ color: themeToken.colorText }} />
                    ]
                  }}
                >
                  {baseUrl}
                </Text>
              </div>
              <Button 
                type="primary" 
                className="create-token-action-btn"
                icon={<KeyOutlined style={{ fontSize: '15px' }} />} 
                onClick={handleAdd}
                style={{
                  borderRadius: '6px',
                  background: isLight ? '#09090b' : '#fafafa',
                  color: isLight ? '#ffffff' : '#09090b',
                  border: 'none',
                  fontSize: '14px',
                  fontWeight: 500,
                  height: '38px',
                  padding: '0 16px',
                  display: 'inline-flex',
                  alignItems: 'center'
                }}
              >
                {t('tokens.create_api_token')}
              </Button>
            </div>
          </div>

          {screens.xs ? (
            <MobileCardList
              dataSource={tokens}
              loading={loading}
              rowKey="id"
              compact={true}
              gap={8}
              pagination={tokens.length < 20 ? false : listPagination()}
              renderCard={(record: any) => {
                const isSystem = isSystemDedicatedToken(record);
                const sessionPlaintext = !isSystem ? revealedKeys[record.id] : undefined;
                const isRevealed = !!sessionPlaintext && !!visibleSessionKeys[record.id];
                const mobileCopyValue = sessionPlaintext || record.kid || record.token_key;
                const displayedKey = isRevealed 
                  ? sessionPlaintext 
                  : (record.token_key.length > 8 ? `...${record.token_key.slice(-6)}` : record.token_key);

                const cardTitle = (
                  <div style={{ display: 'flex', flexDirection: 'column' }}>
                    <Text
                      ellipsis={isRevealed ? false : { tooltip: false }}
                      style={{
                        fontSize: isRevealed ? 13 : 16,
                        fontWeight: '600',
                        color: isSystem ? (isLight ? '#71717a' : '#a1a1aa') : themeToken.colorText,
                        fontFamily: 'monospace',
                        cursor: isSystem ? 'default' : 'pointer',
                        wordBreak: 'break-all',
                        whiteSpace: isRevealed ? 'normal' : 'nowrap',
                        display: 'block'
                      }}
                      onClick={isSystem ? undefined : () => handleCopy(mobileCopyValue, record.id)}
                    >
                      {displayedKey}
                    </Text>
                    <Text type="secondary" style={{ fontSize: 12, marginTop: 4, fontWeight: 'normal', color: isSystem ? (isLight ? '#71717a' : '#a1a1aa') : undefined }}>
                      {record.name}
                    </Text>
                  </div>
                );

                const cardExtra = (
                  <Space size={2} align="center">
                    {!isSystem && (
                      <>
                        <Tooltip title={copiedId === record.id ? t('tokens.copied', '已复制!') : (sessionPlaintext ? t('tokens.copy_full_key', '复制完整密钥') : t('tokens.copy_kid_hint', '复制密钥 ID（完整密钥仅创建时可见）'))}>
                          <Button 
                            type="text" 
                            size="small" 
                            icon={copiedId === record.id ? <CheckOutlined style={{ color: themeToken.colorText }} /> : <CopyOutlined style={{ fontSize: 15 }} />} 
                            onClick={() => handleCopy(mobileCopyValue, record.id)}
                            style={{ color: copiedId === record.id ? themeToken.colorText : undefined }}
                          />
                        </Tooltip>
                        {sessionPlaintext && (
                          <Tooltip title={isRevealed ? t('tokens.hide_key', '隐藏密钥') : t('tokens.view_key', '查看完整密钥')}>
                            <Button 
                              type="text" 
                              size="small" 
                              icon={isRevealed ? <EyeInvisibleOutlined style={{ fontSize: 15 }} /> : <EyeOutlined style={{ fontSize: 15 }} />} 
                              onClick={() => toggleSessionKeyVisibility(record.id)}
                              style={{ color: isRevealed ? themeToken.colorText : undefined }}
                            />
                          </Tooltip>
                        )}
                      </>
                    )}
                    <Link to="/logs">
                      <Tooltip title={t('tokens.view_usage_logs')}>
                        <Button 
                          type="text" 
                          size="small" 
                          icon={<BarChartOutlined style={{ fontSize: 15 }} />} 
                        />
                      </Tooltip>
                    </Link>
                    <Dropdown
                      menu={{
                        items: [
                          {
                            key: 'details',
                            label: t('tokens.quota_details'),
                            icon: <PieChartOutlined />,
                            onClick: () => handleShowDetails(record)
                          },
                          {
                            key: 'toggle',
                            label: isTokenActive(record) ? t('tokens.disable_token', '禁用令牌') : t('tokens.enable_token', '启用令牌'),
                            icon: isTokenActive(record) ? <StopOutlined /> : <PlayCircleOutlined />,
                            disabled: togglingId === record.id,
                            onClick: () => {
                              if (!isTokenActive(record)) {
                                handleToggleActive(record, true);
                                return;
                              }
                              Modal.confirm({
                                title: t('tokens.disable_confirm', '确认禁用该令牌？'),
                                okText: t('common.confirm'),
                                cancelText: t('common.cancel'),
                                onOk: () => handleToggleActive(record, false)
                              });
                            }
                          },
                          ...(isSystem
                            ? []
                            : [
                                {
                                  key: 'edit',
                                  label: t('tokens.edit_token'),
                                  icon: <EditOutlined />,
                                  onClick: () => handleEdit(record)
                                },
                                {
                                  key: 'delete',
                                  label: <Text>{t('tokens.delete_token')}</Text>,
                                  icon: <DeleteOutlined />,
                                  onClick: () => {
                                    Modal.confirm({
                                      title: t('common.confirm_delete'),
                                      okText: t('common.confirm'),
                                      okType: 'danger',
                                      cancelText: t('common.cancel'),
                                      onOk: () => handleDelete(record.id)
                                    });
                                  }
                                }
                              ])
                        ]
                      }}
                      trigger={['click']}
                    >
                      <Button type="text" size="small" icon={<EllipsisOutlined style={{ fontSize: 15 }} />} />
                    </Dropdown>
                  </Space>
                );

                return (
                  <MobileCard
                    compact={true}
                    style={{
                      background: isLight ? (isSystem ? '#f9fafb' : '#fff') : (isSystem ? '#18181b' : '#141414'),
                      border: isSystem ? (isLight ? '1px dashed #e4e4e7' : '1px dashed #27272a') : 'none',
                      borderRadius: 12,
                      padding: '10px 12px',
                      filter: isSystem ? 'grayscale(100%)' : undefined,
                      opacity: isSystem ? 0.72 : undefined,
                    }}
                    title={cardTitle}
                    extra={cardExtra}
                  >
                    <div style={{ marginTop: 8, display: 'flex', flexDirection: 'column', gap: 4 }}>
                      <CardRow label={t('tokens.kid')} compact={true}>
                        {record.kid ? <Tag color="default" style={{ fontFamily: 'monospace', fontSize: 11, margin: 0 }}>{record.kid}</Tag> : <Text type="secondary">-</Text>}
                      </CardRow>
                      <CardRow label={t('tokens.key_status')} compact={true}>
                        <Space size={4} wrap>{renderStatusTags(record, isSystem)}</Space>
                      </CardRow>
                      <CardRow label={t('tokens.available_quota')} compact={true}>
                        <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
                          {renderTokenQuotaRings(record)}
                        </div>
                      </CardRow>
                      <CardRow label={t('tokens.usage_scope')} compact={true}>
                        <Space size={4} wrap>
                          <Tag color="default" style={{ fontSize: 11, margin: 0, padding: '0 4px', lineHeight: '16px' }}>
                            {record.only_playground === 1 && record.only_playground_2026 === 1
                              ? t('tokens.playground_both')
                              : record.only_playground_2026 === 1
                                ? t('tokens.playground_2026_only')
                                : record.only_playground === 1
                                  ? t('tokens.playground_only')
                                  : t('tokens.general')}
                          </Tag>
                          {isPlayground2026DedicatedToken(record) && (
                            <Tag color="default" style={{ fontSize: 11, margin: 0, padding: '0 4px', lineHeight: '16px' }}>
                              {t('tokens.playground_2026_dedicated', '系统专用')}
                            </Tag>
                          )}
                          {isMobileAppToken(record) && (
                            <Tag color="default" style={{ fontSize: 11, margin: 0, padding: '0 4px', lineHeight: '16px' }}>
                              {t('tokens.mobile_app_dedicated', '移动端专用')}
                            </Tag>
                          )}
                          {isMcpToken(record) && (
                            <Tag color="default" style={{ fontSize: 11, margin: 0, padding: '0 4px', lineHeight: '16px' }}>
                              MCP 专用
                            </Tag>
                          )}
                          {(record.high_availability === 1 || (record.high_availability as any) === true) && (
                            <Tag color="default" style={{ fontSize: 11, margin: 0, padding: '0 4px', lineHeight: '16px' }}>
                              HA
                            </Tag>
                          )}
                        </Space>
                      </CardRow>
                      <CardRow label={t('tokens.rate_limits')} compact={true}>
                        <Text type="secondary" style={{ fontSize: 12 }}>RPS: {record.rps_limit || '∞'} · RPM: {record.rpm_limit || '∞'}</Text>
                      </CardRow>
                      <CardRow label={t('tokens.created_date')} compact={true}>
                        <Text type="secondary" style={{ fontSize: 12 }}>{dayjs(record.created_at).format('YYYY-MM-DD')}</Text>
                      </CardRow>
                    </div>
                  </MobileCard>
                );
              }}
            />
          ) : (
            <Table
              className="tokens-compact-table compact-table"
              size="small"
              dataSource={tokens}
              columns={columns}
              rowKey="id"
              loading={loading}
              pagination={tokens.length < 20 ? false : listPagination()}
              scroll={{ x: 'max-content' }}
              rowClassName={(record) => isSystemDedicatedToken(record) ? 'system-dedicated-token-row' : ''}
            />
          )}
        </div>
      )}
        </>
      ) : (
        <div style={{ 
          width: '100%',
          padding: screens.xs ? '8px 0' : '4px 0 24px 0',
          animation: 'fadeIn 0.25s ease'
        }}>
          {/* 注入符合 shadcn 风格的 input 样式与 focus ring */}
          <style>{`
            /* 覆盖 Ant Design 输入框默认聚焦轮廓线 */
            .ant-input.shadcn-input:focus,
            .ant-input-focused.shadcn-input,
            .ant-input-number.shadcn-input:focus,
            .ant-input-number-focused.shadcn-input,
            .ant-input-affix-wrapper-focused.shadcn-input,
            .ant-input-affix-wrapper.shadcn-input:focus,
            .ant-input-textarea.shadcn-input:focus,
            .ant-input-textarea-focused.shadcn-input {
              border-color: ${isLight ? '#09090b' : '#fafafa'} !important;
              box-shadow: 0 0 0 2px ${isLight ? 'rgba(9, 9, 11, 0.1)' : 'rgba(250, 250, 250, 0.15)'} !important;
              outline: 0 !important;
            }
            
            /* 输入框 Placeholder 样式 */
            .shadcn-input::placeholder,
            .shadcn-input .ant-input::placeholder {
              color: ${isLight ? '#a1a1aa' : '#52525b'} !important;
              opacity: 1;
            }
            
            /* Antd input 悬浮状态 */
            .ant-input.shadcn-input:hover,
            .ant-input-number.shadcn-input:hover,
            .ant-input-affix-wrapper.shadcn-input:hover {
              border-color: ${isLight ? '#d4d4d8' : '#3f3f46'} !important;
            }
            
            /* 覆写 antd input addonBefore 背景及边框 */
            .ant-input-group-wrapper.shadcn-input .ant-input-group-addon {
              background-color: ${isLight ? '#fafafa' : '#18181b'} !important;
              border-color: ${isLight ? '#e4e4e7' : '#27272a'} !important;
              border-radius: 6px 0 0 6px !important;
            }
            
            /* 覆写 antd textarea */
            .ant-input-textarea.shadcn-input textarea {
              background: transparent !important;
              color: ${isLight ? '#09090b' : '#fafafa'} !important;
            }
            .ant-input-textarea.shadcn-input textarea:focus {
              border-color: ${isLight ? '#09090b' : '#fafafa'} !important;
              box-shadow: 0 0 0 2px ${isLight ? 'rgba(9, 9, 11, 0.1)' : 'rgba(250, 250, 250, 0.15)'} !important;
            }
            
            /* 自定义中央弹出动画 */
            .center-popup-modal .ant-modal-content {
              animation: centerZoomIn 0.28s cubic-bezier(0.34, 1.56, 0.64, 1) forwards !important;
            }
            @keyframes centerZoomIn {
              0% {
                opacity: 0;
                transform: scale(0.85);
              }
              100% {
                opacity: 1;
                transform: scale(1);
              }
            }
            .center-popup-modal .ant-modal-close:focus-visible,
            .center-popup-modal .ant-modal-close:focus,
            .center-popup-modal .ant-modal-close:hover {
              outline: none !important;
              box-shadow: none !important;
            }
            .center-popup-modal .ant-modal-header {
              background: transparent !important;
              border-bottom: none !important;
            }
          `}</style>

          {/* 返回按钮与辅助信息 */}
          <div style={{ 
            display: 'flex', 
            alignItems: 'center', 
            justifyContent: 'space-between',
            marginBottom: '8px' 
          }}>
            <Button 
              type="text" 
              icon={<ArrowLeftOutlined style={{ fontSize: '15px' }} />} 
              onClick={() => setIsModalVisible(false)}
              style={{ 
                display: 'flex', 
                alignItems: 'center', 
                color: isLight ? '#71717a' : '#a1a1aa',
                padding: '4px 8px',
                height: 'auto',
                fontSize: '16px',
                fontWeight: 500
              }}
            >
              {t('tokens.back_to_list')}
            </Button>
            
          </div>

          {/* 表单主区域平铺 */}
          <div
            style={{
              padding: screens.xs ? '8px 0 16px 0' : '12px 0 24px 0'
            }}
          >
            {/* 头部说明 */}
            <div style={{ marginBottom: '28px' }}>
              <Title level={3} style={{ margin: '0 0 6px 0', fontSize: '24px', fontWeight: 600, color: isLight ? '#09090b' : '#fafafa', letterSpacing: '-0.3px' }}>
                {editingToken ? t('tokens.edit') : t('tokens.create')}
              </Title>
              <Text style={{ fontSize: '14px', lineHeight: '1.5', display: 'block', color: isLight ? '#71717a' : '#a1a1aa' }}>
                {editingToken 
                  ? t('tokens.edit_desc') 
                  : t('tokens.create_desc')}
              </Text>
            </div>

            <Form form={form} layout="vertical" onFinish={handleSave} onFinishFailed={() => setSaving(false)}>
              
              {/* 区块一：基础配置 */}
              <div style={sectionContainerStyle}>

                <Row gutter={16}>
                  <Col span={12}>
                    <Form.Item 
                      name="name" 
                      label={<Text style={{ fontSize: '14px', fontWeight: 500, color: isLight ? '#09090b' : '#fafafa' }}>{t('tokens.name')}</Text>} 
                      rules={[
                        { required: true, message: t('tokens.name_required') },
                        { max: 24, message: t('tokens.name_max_length') },
                        {
                          pattern: /^[\p{L}\p{N}\s_-]+$/u,
                          message: t('tokens.name_invalid_chars'),
                        }
                      ]} 
                      initialValue="default"
                      style={{ marginBottom: '20px' }}
                    >
                      <Input placeholder="e.g. Project A" size="large" style={inputStyle} className="shadcn-input" maxLength={24} />
                    </Form.Item>
                  </Col>

                  <Col span={6}>
                    <Form.Item 
                      name="rps_limit" 
                      label={
                        <Space size={4}>
                          <Text style={{ fontSize: '14px', fontWeight: 500, color: isLight ? '#09090b' : '#fafafa' }}>{t('tokens.rps_limit')}</Text>
                          <Tooltip title={t('tokens.rps_limit_tooltip')}>
                            <span style={{ cursor: 'pointer', color: isLight ? '#71717a' : '#a1a1aa', fontSize: '13px' }}>ⓘ</span>
                          </Tooltip>
                        </Space>
                      } 
                      style={{ marginBottom: '20px' }}
                    >
                      <InputNumber min={1} size="large" style={{ ...inputStyle, width: '100%' }} className="shadcn-input" placeholder={t('tokens.unlimited')} />
                    </Form.Item>
                  </Col>

                  <Col span={6}>
                    <Form.Item 
                      name="rpm_limit" 
                      label={
                        <Space size={4}>
                          <Text style={{ fontSize: '14px', fontWeight: 500, color: isLight ? '#09090b' : '#fafafa' }}>{t('tokens.rpm_limit')}</Text>
                          <Tooltip title={t('tokens.rpm_limit_tooltip')}>
                            <span style={{ cursor: 'pointer', color: isLight ? '#71717a' : '#a1a1aa', fontSize: '13px' }}>ⓘ</span>
                          </Tooltip>
                        </Space>
                      } 
                      style={{ marginBottom: '20px' }}
                    >
                      <InputNumber min={1} size="large" style={{ ...inputStyle, width: '100%' }} className="shadcn-input" placeholder={t('tokens.unlimited')} />
                    </Form.Item>
                  </Col>
                </Row>

                {/* 启用/禁用令牌开关（编辑模式） */}
                {editingToken && (
                  <div style={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    boxSizing: 'border-box',
                    padding: '8px 0',
                    marginBottom: '12px'
                  }}>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '2px', paddingRight: '12px' }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                        <Text style={{ fontSize: '14px', fontWeight: 500, color: isLight ? '#09090b' : '#fafafa' }}>
                          {t('tokens.enable_token', '启用令牌')}
                        </Text>
                        {editingToken.is_active === 3 && (
                          <Tooltip title={t('tokens.reason_inactive_30d_tip', '超过 30 天未调用已自动关闭，使用前请手动重新开启')}>
                            <Tag color="warning" style={{ margin: 0, fontSize: '11px', padding: '0 4px', lineHeight: '16px' }}>
                              {t('tokens.reason_inactive_30d', '闲置关闭')}
                            </Tag>
                          </Tooltip>
                        )}
                      </div>
                      <Text style={{ fontSize: '13px', lineHeight: '1.4', color: isLight ? '#71717a' : '#a1a1aa' }}>
                        {t('tokens.active_status_desc', '开启后该令牌可正常调用接口；关闭后使用该令牌的请求将被拒绝')}
                      </Text>
                    </div>
                    <Form.Item name="is_active" valuePropName="checked" noStyle>
                      <AppSwitch />
                    </Form.Item>
                  </div>
                )}

                {/* 高可用通道开关 */}
                {isHAPluginEnabled && (
                  <div style={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    boxSizing: 'border-box',
                    padding: '8px 0',
                    marginBottom: '12px'
                  }}>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '2px', paddingRight: '12px' }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                        <Text style={{ fontSize: '14px', fontWeight: 500, color: isLight ? '#09090b' : '#fafafa' }}>{t('tokens.high_availability_label')}</Text>
                        <Tooltip title={t('tokens.high_availability_tooltip')}>
                          <span style={{ cursor: 'pointer', color: isLight ? '#09090b' : '#fafafa', fontSize: '13px' }}>ⓘ</span>
                        </Tooltip>
                      </div>
                      <Text style={{ fontSize: '13px', lineHeight: '1.4', color: isLight ? '#71717a' : '#a1a1aa' }}>
                        {t('tokens.high_availability_desc')}
                      </Text>
                    </div>
                    <Form.Item name="high_availability" valuePropName="checked" noStyle>
                      <AppSwitch />
                    </Form.Item>
                  </div>
                )}

                {/* 限制总额度开关 */}
                <div style={{
                  display: 'flex',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  boxSizing: 'border-box',
                  padding: '8px 0',
                  marginBottom: limitQuotaEnabled ? '12px' : '0px'
                }}>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '2px', paddingRight: '12px' }}>
                    <Text style={{ fontSize: '14px', fontWeight: 500, color: isLight ? '#09090b' : '#fafafa' }}>{t('tokens.limit_total_quota')}</Text>
                    <Text style={{ fontSize: '13px', lineHeight: '1.4', color: isLight ? '#71717a' : '#a1a1aa' }}>
                      {limitQuotaEnabled ? t('tokens.limited_quota_desc') : t('tokens.unlimited_quota_desc')}
                    </Text>
                  </div>
                  <AppSwitch
                    checked={limitQuotaEnabled}
                    onChange={(checked) => {
                      setLimitQuotaEnabled(checked);
                      if (!checked) {
                        form.setFieldsValue({ quota_limit: -1 });
                      } else {
                        const currentVal = form.getFieldValue('quota_limit');
                        if (currentVal === -1) {
                          form.setFieldsValue({ quota_limit: undefined });
                        }
                      }
                    }}
                  />
                </div>

                {limitQuotaEnabled && (
                  <Form.Item 
                    name="quota_limit" 
                    style={{ marginBottom: 0 }}
                  >
                    <InputNumber 
                      min={0}
                      size="large"
                      style={{ ...inputStyle, width: '100%' }} 
                      className="shadcn-input"
                      placeholder={t('tokens.unlimited')}
                    />
                  </Form.Item>
                )}

                {!limitQuotaEnabled && (
                  <Form.Item name="quota_limit" initialValue={-1} hidden>
                    <InputNumber />
                  </Form.Item>
                )}

                {/* 周期额度限制开关 */}
                <div style={{
                  display: 'flex',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  boxSizing: 'border-box',
                  padding: '8px 0',
                  marginTop: '16px',
                  marginBottom: periodicQuotaEnabled ? '12px' : '0px'
                }}>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '2px', paddingRight: '12px' }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                      <Text style={{ fontSize: '14px', fontWeight: 500, color: isLight ? '#09090b' : '#fafafa' }}>{t('tokens.periodic_quota_limits')}</Text>
                      <Tooltip title={t('tokens.periodic_quota_tooltip')}>
                        <span style={{ cursor: 'pointer', color: isLight ? '#71717a' : '#a1a1aa', fontSize: '13px' }}>ⓘ</span>
                      </Tooltip>
                    </div>
                    <Text style={{ fontSize: '13px', lineHeight: '1.4', color: isLight ? '#71717a' : '#a1a1aa' }}>
                      {t('tokens.periodic_quota_desc')}
                    </Text>
                  </div>
                  <AppSwitch
                    checked={periodicQuotaEnabled}
                    onChange={(checked) => {
                      setPeriodicQuotaEnabled(checked);
                      if (!checked) {
                        form.setFieldsValue({
                          daily_quota_limit: -1,
                          weekly_quota_limit: -1,
                          monthly_quota_limit: -1,
                        });
                      } else {
                        const dailyVal = form.getFieldValue('daily_quota_limit');
                        const weeklyVal = form.getFieldValue('weekly_quota_limit');
                        const monthlyVal = form.getFieldValue('monthly_quota_limit');
                        
                        form.setFieldsValue({
                          daily_quota_limit: (dailyVal === undefined || dailyVal === -1) ? undefined : dailyVal,
                          weekly_quota_limit: (weeklyVal === undefined || weeklyVal === -1) ? undefined : weeklyVal,
                          monthly_quota_limit: (monthlyVal === undefined || monthlyVal === -1) ? undefined : monthlyVal,
                        });
                      }
                    }}
                  />
                </div>

                {periodicQuotaEnabled && (
                  <div style={{ animation: 'fadeIn 0.25s ease', marginBottom: '16px' }}>
                    <Row gutter={16}>
                      <Col xs={24} sm={8}>
                        <Form.Item 
                          name="daily_quota_limit" 
                          label={<Text style={{ fontSize: '13px', fontWeight: 500, color: isLight ? '#09090b' : '#fafafa' }}>{t('tokens.daily_limit')}</Text>} 
                          style={{ marginBottom: screens.xs ? '12px' : 0 }}
                        >
                          <InputNumber 
                            min={0}
                            size="large"
                            style={{ ...inputStyle, width: '100%' }} 
                            className="shadcn-input"
                            placeholder={t('tokens.unlimited')}
                          />
                        </Form.Item>
                      </Col>
                      
                      <Col xs={24} sm={8}>
                        <Form.Item 
                          name="weekly_quota_limit" 
                          label={<Text style={{ fontSize: '13px', fontWeight: 500, color: isLight ? '#09090b' : '#fafafa' }}>{t('tokens.weekly_limit')}</Text>} 
                          style={{ marginBottom: screens.xs ? '12px' : 0 }}
                        >
                          <InputNumber 
                            min={0}
                            size="large"
                            style={{ ...inputStyle, width: '100%' }} 
                            className="shadcn-input"
                            placeholder={t('tokens.unlimited')}
                          />
                        </Form.Item>
                      </Col>
                      
                      <Col xs={24} sm={8}>
                        <Form.Item 
                          name="monthly_quota_limit" 
                          label={<Text style={{ fontSize: '13px', fontWeight: 500, color: isLight ? '#09090b' : '#fafafa' }}>{t('tokens.monthly_limit')}</Text>} 
                          style={{ marginBottom: 0 }}
                        >
                          <InputNumber 
                            min={0}
                            size="large"
                            style={{ ...inputStyle, width: '100%' }} 
                            className="shadcn-input"
                            placeholder={t('tokens.unlimited')}
                          />
                        </Form.Item>
                      </Col>
                    </Row>
                  </div>
                )}

                {!periodicQuotaEnabled && (
                  <>
                    <Form.Item name="daily_quota_limit" initialValue={-1} hidden>
                      <InputNumber />
                    </Form.Item>
                    <Form.Item name="weekly_quota_limit" initialValue={-1} hidden>
                      <InputNumber />
                    </Form.Item>
                    <Form.Item name="monthly_quota_limit" initialValue={-1} hidden>
                      <InputNumber />
                    </Form.Item>
                  </>
                )}

              </div>

              {/* 区块二：模型与路由 */}
              <div style={sectionContainerStyle}>

                {/* 指定模型开关行 */}
                <div style={{
                  display: 'flex',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  padding: '8px 0',
                  marginBottom: enableModelFilter ? '16px' : '0px',
                  transition: 'all 0.2s',
                }}>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '2px', paddingRight: '12px' }}>
                    <Text style={{ fontSize: '14px', fontWeight: 500, color: isLight ? '#09090b' : '#fafafa' }}>{t('tokens.limit_models_switch')}</Text>
                    <Text style={{ fontSize: '13px', lineHeight: '1.4', color: isLight ? '#71717a' : '#a1a1aa' }}>
                      {enableModelFilter ? t('tokens.models_enabled_desc') : t('tokens.models_disabled_desc')}
                    </Text>
                  </div>
                  <AppSwitch
                    checked={enableModelFilter}
                    onChange={(checked) => {
                      setEnableModelFilter(checked);
                      if (!checked) {
                        form.setFieldsValue({ allowed_models: [] });
                      } else if (availableModels.length === 0) {
                        fetchAvailableModels();
                      }
                    }}
                  />
                </div>

                {enableModelFilter && (
                  <div style={{ 
                    animation: 'fadeIn 0.25s ease',
                    marginBottom: '0px',
                    marginTop: '16px'
                  }}>

                    <Form.Item
                      name="allowed_models"
                      style={{ marginBottom: 0 }}
                      extra={
                        <div style={{ fontSize: 12, color: isLight ? '#71717a' : '#a1a1aa', lineHeight: 1.6, marginTop: 6 }}>
                          {t('tokens.models_hint_1', '可从下方列表选择站点可用模型，也可直接输入模型 ID 后按回车添加。')}
                        </div>
                      }
                    >
                      <Select
                        mode="tags"
                        allowClear
                        showSearch
                        loading={modelsLoading}
                        optionFilterProp="label"
                        tokenSeparators={[',', '\n']}
                        style={{ width: '100%', ...inputStyle }}
                        className="shadcn-input"
                        placeholder={t('tokens.models_placeholder', '选择或输入允许使用的模型 ID')}
                        options={availableModels.map((m) => ({
                          value: m.model_id,
                          label: m.name && m.name !== m.model_id
                            ? `${m.name} (${m.model_id})`
                            : m.model_id,
                        }))}
                      />
                    </Form.Item>
                  </div>
                )}

                {/* IP 白名单开关 */}
                <div style={{
                  display: 'flex',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  boxSizing: 'border-box',
                  padding: '8px 0',
                  marginTop: '16px',
                  marginBottom: ipFilterEnabled ? '12px' : '0px'
                }}>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '2px', paddingRight: '12px' }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                      <Text style={{ fontSize: '14px', fontWeight: 500, color: isLight ? '#09090b' : '#fafafa' }}>{t('tokens.ip_whitelist')}</Text>
                      <Tooltip title={t('tokens.ip_whitelist_tooltip')}>
                        <span style={{ cursor: 'pointer', color: isLight ? '#71717a' : '#a1a1aa', fontSize: '13px' }}>ⓘ</span>
                      </Tooltip>
                    </div>
                    <Text style={{ fontSize: '13px', lineHeight: '1.4', color: isLight ? '#71717a' : '#a1a1aa' }}>
                      {t('tokens.ip_whitelist_desc')}
                    </Text>
                  </div>
                  <AppSwitch
                    checked={ipFilterEnabled}
                    onChange={(checked) => {
                      setIpFilterEnabled(checked);
                      if (!checked) {
                        form.setFieldsValue({ allowed_ips: '' });
                      }
                    }}
                  />
                </div>

                {ipFilterEnabled && (
                  <div style={{ animation: 'fadeIn 0.25s ease', marginTop: '12px' }}>
                    <Form.Item 
                      name="allowed_ips" 
                      style={{ marginBottom: 0 }}
                    >
                      <Input placeholder="e.g. 192.168.1.1, 10.0.0.1" size="large" style={inputStyle} className="shadcn-input" />
                    </Form.Item>
                  </div>
                )}

                {!ipFilterEnabled && (
                  <Form.Item name="allowed_ips" initialValue="" hidden>
                    <Input />
                  </Form.Item>
                )}
              </div>

              {/* 底部操作按钮区域 */}
              <div style={{ 
                marginTop: '28px', 
                display: 'flex', 
                justifyContent: 'flex-end',
                gap: '12px',
                paddingTop: '0px'
              }}>
                <Button 
                  onClick={() => setIsModalVisible(false)} 
                  size="large"
                  style={{ 
                    borderRadius: '6px', 
                    minWidth: '100px',
                    borderColor: isLight ? '#e4e4e7' : '#27272a',
                    background: isLight ? '#ffffff' : '#09090b',
                    color: isLight ? '#09090b' : '#fafafa',
                    fontSize: '14px',
                  }}
                >
                  {t('common.cancel', '取消')}
                </Button>
                <Button
                  loading={saving}
                  size="large"
                  onClick={() => {
                    if (saving) return;
                    setSaving(true);
                    form.submit();
                  }}
                  style={{ 
                    borderRadius: '6px', 
                    minWidth: '120px',
                    background: isLight ? '#09090b' : '#fafafa',
                    color: isLight ? '#ffffff' : '#09090b',
                    border: 'none',
                    fontSize: '14px',
                  }}
                >
                  {t('common.confirm', '确认')}
                </Button>
              </div>
            </Form>
          </div>
        </div>
      )}

      {/* 新建令牌单次明文保存提示弹窗（红线 7：仅创建时返回一次完整密钥） */}
      <Modal
        title={t('tokens.created_key_title', 'API 密钥已创建（请立即保存）')}
        open={!!createdPlaintextModal}
        onCancel={() => setCreatedPlaintextModal(null)}
        footer={[
          <Button
            key="copy"
            icon={<CopyOutlined />}
            onClick={async () => {
              if (!createdPlaintextModal?.key) return;
              const ok = await copyToClipboard(createdPlaintextModal.key);
              if (ok) {
                message.success(t('tokens.copied', '已复制!'));
              } else {
                message.error(t('tokens.copy_failed', '复制失败，请手动选择复制'));
              }
            }}
          >
            {t('tokens.copy_full_key', '复制完整密钥')}
          </Button>,
          <Button key="ok" type="primary" onClick={() => setCreatedPlaintextModal(null)}>
            {t('tokens.saved_confirm', '我已保存密钥')}
          </Button>,
        ]}
        destroyOnClose
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('tokens.created_key_warning', '出于安全考虑，完整密钥仅在创建时展示这一次，关闭后将无法再次查看。请立即复制并妥善保管。')}
        />
        {createdPlaintextModal?.name && (
          <div style={{ marginBottom: 8, fontSize: 13, color: '#71717a' }}>
            {createdPlaintextModal.name}
            {createdPlaintextModal.kid ? ` (KID: ${createdPlaintextModal.kid})` : ''}
          </div>
        )}
        <div
          style={{
            padding: '12px 14px',
            borderRadius: 8,
            background: isLight ? '#f4f4f5' : '#18181b',
            border: `1px solid ${isLight ? '#e4e4e7' : '#27272a'}`,
            fontFamily: 'monospace',
            fontSize: 13,
            wordBreak: 'break-all',
            userSelect: 'all',
          }}
        >
          {createdPlaintextModal?.key}
        </div>
      </Modal>

      {/* 额度使用详情弹窗 */}
      <Modal
        title={
          <div style={{ 
            display: 'flex', 
            alignItems: 'center', 
            gap: '8px', 
            fontSize: '16px', 
            fontWeight: 600,
            color: isLight ? '#09090b' : '#fafafa',
            paddingBottom: '4px'
          }}>
            <span style={{ 
              fontFamily: 'monospace', 
              background: isLight ? '#f4f4f5' : '#18181b', 
              padding: '2px 8px', 
              borderRadius: '6px', 
              fontSize: '13px', 
              fontWeight: 500,
              border: `1px solid ${isLight ? '#e4e4e7' : '#27272a'}`,
              color: isLight ? '#27272a' : '#e4e4e7' 
            }}>
              {selectedToken?.name}
            </span>
            <span>{t('tokens.quota_details')}</span>
            <Tooltip title={t('tokens.quota_details_tooltip')}>
              <InfoCircleOutlined style={{ fontSize: '13px', color: isLight ? '#71717a' : '#a1a1aa', cursor: 'pointer', marginLeft: '2px' }} />
            </Tooltip>
            {detailRefreshing && <SyncOutlined spin style={{ fontSize: 13, color: isLight ? '#71717a' : '#a1a1aa' }} />}
          </div>
        }
        open={isDetailModalOpen}
        onCancel={() => setIsDetailModalOpen(false)}
        footer={null}
        destroyOnClose
        centered
        width={500}
        transitionName=""
        className="center-popup-modal"
        styles={{
          body: {
            background: isLight ? '#ffffff' : '#09090b',
            border: `1px solid ${isLight ? '#e4e4e7' : '#27272a'}`,
            borderRadius: '12px',
            padding: '24px',
          }
        }}
      >
        <div style={{ 
          marginTop: '16px', 
          display: 'flex', 
          flexDirection: 'column',
          background: isLight ? '#fafafa' : '#141414',
          border: `1px solid ${isLight ? '#e4e4e7' : '#27272a'}`,
          borderRadius: '10px',
          padding: '0 20px',
          boxSizing: 'border-box'
        }}>
          {(() => {
            if (!selectedToken) return null;

            const { dailyUsed, weeklyUsed, monthlyUsed } = getPeriodicUsed(selectedToken, quotaTz);

            const items = [];
            const hasDailyLimit = selectedToken.daily_quota_limit >= 0;
            const hasWeeklyLimit = selectedToken.weekly_quota_limit >= 0;
            const hasMonthlyLimit = selectedToken.monthly_quota_limit >= 0;

            const totalItems = 2 + 
              (hasWeeklyLimit ? 1 : 0) + 
              (hasMonthlyLimit ? 1 : 0);
            
            let count = 0;

            // 1. 总额度
            count++;
            items.push(
              <QuotaProgressItem
                key="total"
                label={t('tokens.total_quota_limit')}
                used={selectedToken.quota_used}
                limit={selectedToken.quota_limit}
                isLight={isLight}
                isLast={count === totalItems}
              />
            );

            // 2. 日额度 (始终展示每日额度使用情况)
            count++;
            items.push(
              <QuotaProgressItem
                key="daily"
                label={hasDailyLimit ? t('tokens.daily_quota_limit_label') : t('tokens.daily_quota_label', '日额度')}
                used={dailyUsed}
                limit={selectedToken.daily_quota_limit}
                refreshText={getQuotaRefreshText('day', quotaTz, t)}
                isLight={isLight}
                isLast={count === totalItems}
              />
            );

            // 3. 周额度
            if (selectedToken.weekly_quota_limit >= 0) {
              count++;
              items.push(
                <QuotaProgressItem
                  key="weekly"
                  label={t('tokens.weekly_quota_limit_label')}
                  used={weeklyUsed}
                  limit={selectedToken.weekly_quota_limit}
                  refreshText={getQuotaRefreshText('week', quotaTz, t)}
                  isLight={isLight}
                  isLast={count === totalItems}
                />
              );
            }

            // 4. 月额度
            if (selectedToken.monthly_quota_limit >= 0) {
              count++;
              items.push(
                <QuotaProgressItem
                  key="monthly"
                  label={t('tokens.monthly_quota_limit_label')}
                  used={monthlyUsed}
                  limit={selectedToken.monthly_quota_limit}
                  refreshText={getQuotaRefreshText('month', quotaTz, t)}
                  isLight={isLight}
                  isLast={count === totalItems}
                />
              );
            }

            return items;
          })()}
        </div>

        {selectedToken && !isSystemDedicatedToken(selectedToken) ? (
        <div style={{ marginTop: 16, display: 'flex', justifyContent: 'flex-end' }}>
          <Popconfirm
            title={t('tokens.clear_usage_confirm', '确认清空该令牌的已用额度数据？')}
            description={t('tokens.clear_usage_confirm_desc', '将重置总额度与日/周/月已用量为 0，不影响额度上限与账户余额。')}
            onConfirm={handleClearUsage}
            okText={t('common.confirm', '确认')}
            cancelText={t('common.cancel', '取消')}
            okButtonProps={{ danger: true, loading: clearingUsage }}
          >
            <Button
              danger
              icon={<ClearOutlined />}
              loading={clearingUsage}
              disabled={!selectedToken}
            >
              {t('tokens.clear_usage', '清空使用额度')}
            </Button>
          </Popconfirm>
        </div>
        ) : null}
      </Modal>

      {/* 开启高可用通道确认弹窗 */}
      <Modal
        title={t('tokens.ha_confirm_title')}
        open={isHAConfirmModalOpen}
        onCancel={() => {
          setIsHAConfirmModalOpen(false);
          setSaving(false);
        }}
        onOk={() => {
          setIsHAConfirmModalOpen(false);
          executeSave(tempValues, haConfirmDismiss);
        }}
        okText={t('common.confirm')}
        cancelText={t('common.cancel')}
      >
        <div style={{ marginTop: '12px' }}>
          <p style={{ margin: 0, lineHeight: '1.6', fontSize: '14px', color: isLight ? '#3f3f46' : '#d4d4d8' }}>
            {t('tokens.ha_confirm_body')}
          </p>
          <div style={{ marginTop: '16px' }}>
            <Checkbox checked={haConfirmDismiss} onChange={(e) => setHaConfirmDismiss(e.target.checked)}>
              {t('tokens.ha_confirm_dont_ask')}
            </Checkbox>
          </div>
        </div>
      </Modal>
    </Card>
  );
};

// 额度详情的单项进度条组件
const QuotaProgressItem: React.FC<{
  label: string;
  used: number;
  limit: number;
  refreshText?: string;
  isLight: boolean;
  isLast: boolean;
}> = ({ label, used, limit, refreshText, isLight, isLast }) => {
  const { t } = useTranslation();
  const isUnlimited = limit < 0;
  const percent = isUnlimited ? 0 : Math.min(100, (used / limit) * 100);
  
  // 决定进度条状态颜色
  let strokeColor = '#22c55e'; // 绿色
  if (!isUnlimited) {
    if (percent >= 90) {
      strokeColor = '#ef4444'; // 90% 以上红色
    } else if (percent >= 70) {
      strokeColor = '#eab308'; // 70% - 90% 黄色
    }
  }

  return (
    <div style={{
      display: 'flex',
      justifyContent: 'space-between',
      alignItems: 'center',
      padding: '20px 0',
      borderBottom: isLast ? 'none' : `1px solid ${isLight ? '#e4e4e7' : '#27272a'}`,
    }}>
      <div style={{ display: 'flex', flexDirection: 'column', flex: 1, paddingRight: '16px' }}>
        <Text style={{ 
          fontSize: '14px', 
          fontWeight: 600, 
          color: isLight ? '#09090b' : '#fafafa' 
        }}>
          {label}
        </Text>
        
        {/* 数据层级可视化排版 */}
        <div style={{ display: 'flex', alignItems: 'center', gap: '6px', marginTop: '6px' }}>
          <span style={{ fontSize: '11px', color: isLight ? '#71717a' : '#a1a1aa', fontWeight: 500 }}>{t('tokens.used_label')}</span>
          <span style={{ fontSize: '13px', fontWeight: 600, fontFamily: 'monospace', color: isLight ? '#09090b' : '#fafafa' }}>
            {used.toFixed(6)}
          </span>
          <span style={{ fontSize: '12px', color: isLight ? '#e4e4e7' : '#27272a' }}>/</span>
          <span style={{ fontSize: '11px', color: isLight ? '#71717a' : '#a1a1aa', fontWeight: 500 }}>{t('tokens.cap_label')}</span>
          <span style={{ fontSize: '13px', fontWeight: 600, fontFamily: 'monospace', color: isLight ? '#09090b' : '#fafafa' }}>
            {isUnlimited ? t('tokens.unlimited') : limit.toFixed(6)}
          </span>
        </div>

        {/* 刷新倒计时排版 */}
        {refreshText && (
          <div style={{ 
            display: 'flex', 
            alignItems: 'center', 
            gap: '4px', 
            marginTop: '6px', 
            fontSize: '11px', 
            color: isLight ? '#71717a' : '#a1a1aa'
          }}>
            <SyncOutlined spin style={{ fontSize: '10px', color: '#22c55e' }} />
            <span>{t('tokens.reset_countdown')}</span>
            <span style={{ fontWeight: 500, color: isLight ? '#09090b' : '#fafafa' }}>{refreshText}</span>
          </div>
        )}
      </div>
      
      <div style={{ display: 'flex', alignItems: 'center', gap: '12px', flexShrink: 0 }}>
        <Text style={{ 
          fontSize: '15px', 
          fontWeight: 700, 
          fontFamily: 'monospace',
          color: isLight ? '#09090b' : '#fafafa' 
        }}>
          {isUnlimited ? '∞' : `${Math.round(percent)}%`}
        </Text>
        <Progress 
          type="circle" 
          percent={isUnlimited ? 100 : percent} 
          width={36} 
          strokeWidth={8}
          strokeColor={isUnlimited ? '#3b82f6' : strokeColor} // 蓝/绿/黄/红
          trailColor={isLight ? '#e4e4e7' : '#27272a'}
          showInfo={false}
        />
      </div>
    </div>
  );
};

export default Tokens;
