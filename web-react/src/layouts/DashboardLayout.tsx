import React, { useEffect, useMemo, useState } from 'react';
import {
  Avatar,
  Badge,
  Button,
  ConfigProvider,
  Drawer,
  Dropdown,
  Layout,
  List,
  Menu,
  Select,
  Space,
  Tag,
  Tooltip,
  Typography,
  theme,
} from 'antd';
import type { MenuProps } from 'antd';
import {
  ApiOutlined,
  AuditOutlined,
  BellOutlined,
  BookOutlined,
  CloudServerOutlined,
  ControlOutlined,
  CreditCardOutlined,
  DashboardOutlined,
  DollarOutlined,
  FileTextOutlined,
  FundOutlined,
  GlobalOutlined,
  InfoCircleOutlined,
  KeyOutlined,
  LineChartOutlined,
  LogoutOutlined,
  MailOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  MoonOutlined,
  NodeIndexOutlined,
  NotificationOutlined,
  PayCircleOutlined,
  PictureOutlined,
  SafetyCertificateOutlined,
  SettingOutlined,
  ShopOutlined,
  SlidersOutlined,
  SunOutlined,
  SwapOutlined,
  TeamOutlined,
  UnorderedListOutlined,
  UserOutlined,
  VideoCameraOutlined,
  WalletOutlined,
} from '@ant-design/icons';
import { Outlet, useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import useAuthStore from '../store/auth';
import useSettingsStore from '../store/settings';
import { useThemeStore } from '../store/theme';
import request, { coreApi } from '../utils/request';
import { LANG_NAME_MAP, persistUserLanguagePreference, resolveContentLang } from '../utils/language';
import { getAnnouncementDisplayTime } from '../utils/announcement';
import { formatApiDateTime } from '../utils/timedisplay';
import { getSiderMenuTokens, solidAccent } from '../theme/tokens';
import type { Announcement } from '../types';

const { Header, Sider, Content } = Layout;

interface OrgOption {
  id: number;
  name: string;
  role?: string;
}

interface DashboardLayoutProps {
  mode: 'user' | 'admin';
}

export const DashboardLayout: React.FC<DashboardLayoutProps> = ({ mode }) => {
  const { token: antdToken } = theme.useToken();
  const { themeMode, toggleTheme } = useThemeStore();
  const { settings, fetchSettings } = useSettingsStore();
  const { user, setUser, setToken, logout } = useAuthStore();
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();

  const [collapsed, setCollapsed] = useState(false);
  const [orgs, setOrgs] = useState<OrgOption[]>([]);
  const [switchingOrg, setSwitchingOrg] = useState(false);
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const [announceOpen, setAnnounceOpen] = useState(false);

  const adminPath = settings?.site?.admin_path || localStorage.getItem('laiai_admin_path') || 'admin';
  const siteTitle = settings?.site?.title || settings?.site?.name || 'LAIAI';
  const isAdmin = user?.role === 'admin' || user?.plat_role === 'admin' || user?.plat_role === 'superadmin';
  const isLight = themeMode === 'light';
  const accent = solidAccent(isLight ? 'light' : 'dark');
  const siderMenuTokens = useMemo(() => getSiderMenuTokens(isLight ? 'light' : 'dark'), [isLight]);

  useEffect(() => {
    void fetchSettings();
  }, [fetchSettings]);

  useEffect(() => {
    let alive = true;
    request
      .get('/console/bootstrap')
      .then((res: any) => {
        if (!alive) return;
        if (res?.user) {
          setUser(res.user);
        }
        if (Array.isArray(res?.announcements)) {
          setAnnouncements(res.announcements);
        }
      })
      .catch(() => {
        request
          .get('/announcements/public')
          .then((list: any) => {
            if (alive && Array.isArray(list)) setAnnouncements(list);
          })
          .catch(() => {});
      });

    coreApi
      .get('/orgs')
      .then((res: any) => {
        if (!alive) return;
        const rows = Array.isArray(res?.orgs) ? res.orgs : Array.isArray(res) ? res : [];
        setOrgs(rows);
      })
      .catch(() => {});

    return () => {
      alive = false;
    };
  }, [setUser]);

  const handleSwitchOrg = async (orgId: number) => {
    if (!orgId || orgId === user?.org_id) return;
    setSwitchingOrg(true);
    try {
      const res: any = await coreApi.post('/auth/switch', { org_id: orgId });
      if (res?.access_token) {
        setToken(res.access_token);
      }
      const boot: any = await request.get('/console/bootstrap');
      if (boot?.user) {
        setUser(boot.user);
      }
      window.location.reload();
    } catch {
      // handled by interceptor
    } finally {
      setSwitchingOrg(false);
    }
  };

  const handleLogout = () => {
    logout();
    navigate(mode === 'admin' ? `/${adminPath}` : '/login');
  };

  const userMenuItems: MenuProps['items'] = useMemo(
    () => [
      { key: '/dashboard', icon: <DashboardOutlined />, label: t('menu.dashboard', '概览') },
      { key: '/apikeys', icon: <KeyOutlined />, label: t('menu.apikeys', 'API 令牌') },
      {
        key: 'grp-user-logs',
        type: 'group',
        label: collapsed ? undefined : t('menu.usage_group', '日志与任务'),
        children: [
          { key: '/logs', icon: <UnorderedListOutlined />, label: t('menu.logs', '调用日志') },
          { key: '/task-logs', icon: <VideoCameraOutlined />, label: t('menu.task_logs', '异步任务') },
        ],
      },
      {
        key: 'grp-user-billing',
        type: 'group',
        label: collapsed ? undefined : t('menu.billing_group', '账务与组织'),
        children: [
          { key: '/wallet', icon: <WalletOutlined />, label: t('menu.wallet', '钱包与资金明细') },
          { key: '/team', icon: <TeamOutlined />, label: t('menu.team', '团队与 BYOK') },
          { key: '/profile', icon: <UserOutlined />, label: t('menu.profile', '个人设置') },
        ],
      },
      {
        key: 'grp-user-resources',
        type: 'group',
        label: collapsed ? undefined : t('menu.resources', '模型与文档'),
        children: [
          { key: '/home/models', icon: <ShopOutlined />, label: t('menu.marketplace', '模型广场') },
          { key: '/ark-video-monitor', icon: <VideoCameraOutlined />, label: t('menu.ark_video_monitor', '方舟视频监控') },
          { key: '/docs/quickstart/doc0003', icon: <BookOutlined />, label: t('menu.docs', '接口文档') },
        ],
      },
    ],
    [collapsed, t],
  );

  const adminBase = `/${adminPath}`;
  const adminMenuItems: MenuProps['items'] = useMemo(
    () => [
      { key: `${adminBase}/dashboard`, icon: <DashboardOutlined />, label: t('menu.admin_dashboard', '系统概览') },
      { key: `${adminBase}/apikeys`, icon: <KeyOutlined />, label: t('menu.tokens', '令牌管理') },
      { key: `${adminBase}/logs`, icon: <UnorderedListOutlined />, label: t('menu.admin_logs', '日志记录') },
      { key: `${adminBase}/task-logs`, icon: <VideoCameraOutlined />, label: t('menu.admin_task_logs', '任务列表') },
      {
        key: 'sub-channels',
        icon: <CloudServerOutlined />,
        label: t('menu.channel_management', '渠道管理'),
        children: [
          { key: `${adminBase}/channels`, icon: <CloudServerOutlined />, label: t('menu.channel_list', '渠道列表') },
          { key: `${adminBase}/channel-configs`, icon: <SlidersOutlined />, label: t('menu.channel_configs', '渠道配置') },
          { key: `${adminBase}/payment-channels`, icon: <CreditCardOutlined />, label: t('admin.payment_channels', '支付通道') },
          { key: `${adminBase}/ark-monitor`, icon: <VideoCameraOutlined />, label: t('menu.ark_monitor', '火山方舟监控') },
          { key: `${adminBase}/volc-assets`, icon: <PictureOutlined />, label: t('menu.volc_assets', '火山素材转换') },
        ],
      },
      {
        key: 'sub-models',
        icon: <ApiOutlined />,
        label: t('menu.model_management', '模型管理'),
        children: [
          { key: `${adminBase}/models`, icon: <ApiOutlined />, label: t('menu.model_list', '模型列表') },
          { key: `${adminBase}/billing-rules`, icon: <DollarOutlined />, label: t('menu.billing_rules', '计费规则') },
          { key: `${adminBase}/forward-rules`, icon: <NodeIndexOutlined />, label: t('menu.forward_rules', '转发规则') },
        ],
      },
      {
        key: 'sub-marketing',
        icon: <NotificationOutlined />,
        label: t('menu.marketing_group', '营销通知'),
        children: [
          { key: `${adminBase}/marketing/announcements`, icon: <NotificationOutlined />, label: t('menu.announcements', '系统公告') },
        ],
      },
      {
        key: 'sub-users',
        icon: <TeamOutlined />,
        label: t('menu.user_management', '用户管理'),
        children: [
          { key: `${adminBase}/users`, icon: <TeamOutlined />, label: t('admin.users', '用户与组织') },
          { key: `${adminBase}/group-ratios`, icon: <DollarOutlined />, label: t('admin.group_ratios', '分组倍率') },
        ],
      },
      {
        key: 'sub-finance',
        icon: <FundOutlined />,
        label: t('menu.finance_management', '财务管理'),
        children: [
          { key: `${adminBase}/finance/recharges`, icon: <WalletOutlined />, label: t('menu.recharge_records', '充值与调账记录') },
          { key: `${adminBase}/finance/orders`, icon: <CreditCardOutlined />, label: t('menu.order_details', '充值订单明细') },
          { key: `${adminBase}/finance/analysis`, icon: <LineChartOutlined />, label: t('menu.finance_analysis', '财务数据分析') },
        ],
      },
      {
        key: 'sub-settings',
        icon: <SettingOutlined />,
        label: t('menu.site_settings', '站点设置'),
        children: [
          { key: `${adminBase}/settings`, icon: <SettingOutlined />, label: t('menu.system_settings', '基础与安全设置') },
          { key: `${adminBase}/payment-settings`, icon: <PayCircleOutlined />, label: t('menu.payment_settings', '收款支付设置') },
          { key: `${adminBase}/message-notification`, icon: <MailOutlined />, label: t('menu.message_notification', '消息通知配置') },
          { key: `${adminBase}/oauth-settings`, icon: <SafetyCertificateOutlined />, label: t('menu.oauth_settings', '第三方登录(OAuth)') },
          { key: `${adminBase}/docs-manager`, icon: <FileTextOutlined />, label: t('admin.docs', 'API文档管理') },
          { key: `${adminBase}/audit-logs`, icon: <AuditOutlined />, label: t('admin.audit_logs', '审计日志') },
          { key: `${adminBase}/about`, icon: <InfoCircleOutlined />, label: t('menu.about', '关于系统') },
        ],
      },
    ],
    [adminBase, t],
  );

  const items = mode === 'admin' ? adminMenuItems : userMenuItems;

  const selectedKey = useMemo(() => {
    const p = location.pathname;
    if (mode === 'admin') {
      if (p.includes('/dashboard')) return `${adminBase}/dashboard`;
      if (p.includes('/apikeys')) return `${adminBase}/apikeys`;
      if (p.includes('/task-logs')) return `${adminBase}/task-logs`;
      if (p.includes('/audit-logs')) return `${adminBase}/audit-logs`;
      if (p.includes('/logs')) return `${adminBase}/logs`;
      if (p.includes('/channel-configs')) return `${adminBase}/channel-configs`;
      if (p.includes('/payment-channels')) return `${adminBase}/payment-channels`;
      if (p.includes('/ark-monitor')) return `${adminBase}/ark-monitor`;
      if (p.includes('/volc-assets')) return `${adminBase}/volc-assets`;
      if (p.includes('/channels')) return `${adminBase}/channels`;
      if (p.includes('/billing-rules')) return `${adminBase}/billing-rules`;
      if (p.includes('/forward-rules')) return `${adminBase}/forward-rules`;
      if (p.includes('/models')) return `${adminBase}/models`;
      if (p.includes('/announcements')) return `${adminBase}/marketing/announcements`;
      if (p.includes('/recharges') || p.includes('/finance/recharges')) return `${adminBase}/finance/recharges`;
      if (p.includes('/finance/orders')) return `${adminBase}/finance/orders`;
      if (p.includes('/finance/analysis')) return `${adminBase}/finance/analysis`;
      if (p.includes('/docs')) return `${adminBase}/docs-manager`;
      return p;
    }
    if (p.startsWith('/dashboard')) return '/dashboard';
    if (p.startsWith('/task-logs')) return '/task-logs';
    if (p.startsWith('/logs')) return '/logs';
    return p;
  }, [adminBase, location.pathname, mode]);

  const activeSubKey = useMemo(() => {
    if (mode !== 'admin') return '';
    if (selectedKey.includes('/channels') || selectedKey.includes('/channel-configs') || selectedKey.includes('/payment-channels') || selectedKey.includes('/ark-monitor') || selectedKey.includes('/volc-assets')) {
      return 'sub-channels';
    }
    if (selectedKey.includes('/models') || selectedKey.includes('/billing-rules') || selectedKey.includes('/forward-rules')) {
      return 'sub-models';
    }
    if (selectedKey.includes('/marketing/')) return 'sub-marketing';
    if (selectedKey.includes('/users') || selectedKey.includes('/group-ratios')) return 'sub-users';
    if (selectedKey.includes('/finance/')) return 'sub-finance';
    if (
      selectedKey.includes('/settings') ||
      selectedKey.includes('/payment-settings') ||
      selectedKey.includes('/message-notification') ||
      selectedKey.includes('/oauth-settings') ||
      selectedKey.includes('/docs-manager') ||
      selectedKey.includes('/audit-logs') ||
      selectedKey.includes('/about')
    ) {
      return 'sub-settings';
    }
    return '';
  }, [mode, selectedKey]);

  const [openKeys, setOpenKeys] = useState<string[]>(() => (activeSubKey ? [activeSubKey] : []));

  useEffect(() => {
    if (activeSubKey) {
      setOpenKeys((prev) => (prev.includes(activeSubKey) ? prev : [...prev, activeSubKey]));
    }
  }, [activeSubKey]);

  const activeLangCode = resolveContentLang(i18n.language);
  const langMenu: MenuProps['items'] = Object.entries(LANG_NAME_MAP).map(([code, label]) => ({
    key: code,
    label,
    onClick: () => {
      persistUserLanguagePreference(code);
      void i18n.changeLanguage(code === 'zh-TW' ? 'zhTW' : code);
    },
  }));

  const outletCtx = useMemo(
    () => ({
      announcements,
      notifyConsolePageReady: () => {},
    }),
    [announcements],
  );

  return (
    <Layout style={{ minHeight: '100vh', background: antdToken.colorBgLayout }}>
      <Sider
        collapsible
        collapsed={collapsed}
        onCollapse={setCollapsed}
        trigger={null}
        width={244}
        collapsedWidth={68}
        style={{
          background: antdToken.colorBgContainer,
          borderRight: `1px solid ${antdToken.colorBorderSecondary}`,
          position: 'sticky',
          top: 0,
          height: '100vh',
          overflowY: 'auto',
        }}
      >
        <div
          onClick={() => navigate(mode === 'admin' ? `${adminBase}/dashboard` : '/dashboard')}
          style={{
            height: 60,
            padding: collapsed ? '0 14px' : '0 18px',
            display: 'flex',
            alignItems: 'center',
            gap: 10,
            cursor: 'pointer',
            borderBottom: `1px solid ${antdToken.colorBorderSecondary}`,
          }}
        >
          <div
            style={{
              width: 32,
              height: 32,
              borderRadius: 9,
              background: accent.background,
              color: accent.color,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              fontWeight: 700,
              fontSize: 15,
              flexShrink: 0,
            }}
          >
            L
          </div>
          {!collapsed && (
            <div style={{ minWidth: 0, display: 'flex', alignItems: 'center', gap: 6 }}>
              <Typography.Text strong ellipsis style={{ fontSize: 15 }}>
                {siteTitle}
              </Typography.Text>
              <Tag color={mode === 'admin' ? 'indigo' : 'default'} style={{ margin: 0, fontSize: 11 }}>
                {mode === 'admin' ? t('menu.badge_admin', '管理端') : t('menu.badge_console', '控制台')}
              </Tag>
            </div>
          )}
        </div>

        <ConfigProvider theme={{ components: { Menu: siderMenuTokens } }}>
          <Menu
            mode="inline"
            selectedKeys={[selectedKey]}
            openKeys={collapsed ? undefined : openKeys}
            onOpenChange={(keys) => setOpenKeys(keys)}
            items={items}
            onClick={({ key }) => {
              if (typeof key === 'string' && key.startsWith('/')) {
                navigate(key);
              }
            }}
            style={{ borderInlineEnd: 'none', padding: '8px 4px' }}
          />
        </ConfigProvider>
      </Sider>

      <Layout style={{ background: antdToken.colorBgLayout, minWidth: 0 }}>
        <Header
          style={{
            height: 60,
            padding: '0 20px',
            background: antdToken.colorBgContainer,
            borderBottom: `1px solid ${antdToken.colorBorderSecondary}`,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            position: 'sticky',
            top: 0,
            zIndex: 40,
          }}
        >
          <Space size={12}>
            <Button
              type="text"
              icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
              onClick={() => setCollapsed((v) => !v)}
            />
            <Button type="text" icon={<ShopOutlined />} onClick={() => navigate('/home/models')}>
              {t('menu.marketplace', '模型广场')}
            </Button>
            <Button type="text" icon={<BookOutlined />} onClick={() => navigate('/docs/quickstart/doc0003')}>
              {t('menu.docs', '接口文档')}
            </Button>
            {isAdmin && (
              <Button
                type="default"
                size="small"
                icon={<ControlOutlined />}
                onClick={() => navigate(mode === 'admin' ? '/dashboard' : `${adminBase}/dashboard`)}
              >
                {mode === 'admin' ? t('menu.back_user_console', '用户控制台') : t('menu.enter_admin', '管理后台')}
              </Button>
            )}
          </Space>

          <Space size={10}>
            {orgs.length > 1 && (
              <Select
                size="small"
                loading={switchingOrg}
                value={user?.org_id}
                onChange={handleSwitchOrg}
                style={{ minWidth: 150 }}
                suffixIcon={<SwapOutlined />}
                options={orgs.map((o: any) => ({
                  value: o.org_id ?? o.id,
                  label: o.name,
                }))}
              />
            )}

            <Tooltip title={t('menu.announcements', '系统公告')}>
              <Badge count={announcements.length} size="small" offset={[-4, 4]}>
                <Button type="text" icon={<BellOutlined />} onClick={() => setAnnounceOpen(true)} />
              </Badge>
            </Tooltip>

            <Tooltip title={isLight ? t('theme.dark', '深色模式') : t('theme.light', '浅色模式')}>
              <Button type="text" icon={isLight ? <MoonOutlined /> : <SunOutlined />} onClick={toggleTheme} />
            </Tooltip>

            <Dropdown menu={{ items: langMenu, selectable: true, selectedKeys: [activeLangCode] }} placement="bottomRight">
              <Button type="text" icon={<GlobalOutlined />} />
            </Dropdown>

            <Dropdown
              placement="bottomRight"
              menu={{
                items: [
                  {
                    key: 'profile',
                    icon: <UserOutlined />,
                    label: `${user?.username || user?.email || 'User'}`,
                    onClick: () => navigate('/profile'),
                  },
                  {
                    key: 'wallet',
                    icon: <WalletOutlined />,
                    label: t('menu.wallet_ledger', '钱包流水'),
                    onClick: () => navigate('/wallet'),
                  },
                  { type: 'divider' },
                  {
                    key: 'logout',
                    icon: <LogoutOutlined />,
                    danger: true,
                    label: t('menu.logout', '退出登录'),
                    onClick: handleLogout,
                  },
                ],
              }}
            >
              <Space style={{ cursor: 'pointer', padding: '4px 8px', borderRadius: 8, maxWidth: 168 }}>
                <Avatar size={28} style={{ backgroundColor: accent.background, color: accent.color, flexShrink: 0 }}>
                  {(user?.username || user?.email || 'U').slice(0, 1).toUpperCase()}
                </Avatar>
                <Typography.Text style={{ fontSize: 13, maxWidth: 108 }} ellipsis>
                  {user?.username || user?.email || 'User'}
                </Typography.Text>
              </Space>
            </Dropdown>
          </Space>
        </Header>

        <Content style={{ padding: 20, maxWidth: 1640, width: '100%', margin: '0 auto' }}>
          <Outlet context={outletCtx} />
        </Content>
      </Layout>

      <Drawer
        title={t('menu.announcements', '系统公告')}
        open={announceOpen}
        onClose={() => setAnnounceOpen(false)}
        width={420}
      >
        <List
          dataSource={announcements}
          locale={{ emptyText: t('common.no_announcements', '暂无公告') }}
          renderItem={(item) => (
            <List.Item>
              <List.Item.Meta
                title={
                  <Space>
                    {item.is_pinned ? <Tag color="indigo">{t('menu.pinned', '置顶')}</Tag> : null}
                    <Typography.Text strong>{item.title}</Typography.Text>
                  </Space>
                }
                description={
                  <div>
                    <Typography.Paragraph style={{ whiteSpace: 'pre-wrap', marginBottom: 6 }}>
                      {item.content}
                    </Typography.Paragraph>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {formatApiDateTime(getAnnouncementDisplayTime(item))}
                    </Typography.Text>
                  </div>
                }
              />
            </List.Item>
          )}
        />
      </Drawer>
    </Layout>
  );
};

export default DashboardLayout;
