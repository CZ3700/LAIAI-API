import React, { useEffect, useMemo, useState } from 'react';
import {
  Avatar,
  Badge,
  Button,
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
  FolderOutlined,
  FundOutlined,
  GlobalOutlined,
  KeyOutlined,
  LogoutOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  MoonOutlined,
  NodeIndexOutlined,
  NotificationOutlined,
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
import { LANG_NAME_MAP, persistUserLanguagePreference } from '../utils/language';
import { getAnnouncementDisplayTime, getAnnouncementLabel } from '../utils/announcement';
import { formatApiDateTime } from '../utils/timedisplay';
import { solidAccent } from '../theme/tokens';

const { Header, Sider, Content } = Layout;

interface OrgOption {
  id: number;
  name: string;
  role?: string;
}

interface AnnouncementItem {
  id: number;
  title: string;
  content: string;
  is_pinned?: number;
  created_at?: string;
  updated_at?: string;
  display_time_mode?: string;
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
  const [announcements, setAnnouncements] = useState<AnnouncementItem[]>([]);
  const [announceOpen, setAnnounceOpen] = useState(false);

  const adminPath = settings?.site?.admin_path || 'admin';
  const siteTitle = settings?.site?.title || 'LAIAI';
  const isAdmin = user?.role === 'admin' || user?.plat_role === 'admin' || user?.plat_role === 'superadmin';
  const isLight = themeMode === 'light';
  const accent = solidAccent(isLight ? 'light' : 'dark');

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
        // fallback to public announcements
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
      { key: '/dashboard', icon: <DashboardOutlined />, label: t('menu.dashboard', '概览仪表盘') },
      { key: '/apikeys', icon: <KeyOutlined />, label: t('menu.apikeys', 'API Key 令牌') },
      {
        key: 'grp-logs',
        type: 'group',
        label: collapsed ? undefined : t('menu.usage_group', '用量与任务'),
        children: [
          { key: '/logs', icon: <UnorderedListOutlined />, label: t('menu.logs', '请求日志') },
          { key: '/task-logs', icon: <VideoCameraOutlined />, label: t('menu.task_logs', '异步视频任务') },
        ],
      },
      {
        key: 'grp-billing',
        type: 'group',
        label: collapsed ? undefined : t('menu.billing_group', '账务与组织'),
        children: [
          { key: '/wallet', icon: <WalletOutlined />, label: t('menu.wallet', '钱包充值与流水') },
          { key: '/team', icon: <TeamOutlined />, label: t('menu.team', '团队与 BYOK') },
          { key: '/profile', icon: <UserOutlined />, label: t('menu.profile', '个人资料与 2FA') },
        ],
      },
      {
        key: 'grp-ecosystem',
        type: 'group',
        label: collapsed ? undefined : t('menu.resources', '模型与文档'),
        children: [
          { key: '/home/models', icon: <ShopOutlined />, label: t('menu.marketplace', '模型广场') },
          { key: '/docs/quickstart/doc0003', icon: <BookOutlined />, label: t('menu.docs', '接口文档') },
        ],
      },
    ],
    [collapsed, t],
  );

  const adminBase = `/${adminPath}`;
  const adminMenuItems: MenuProps['items'] = useMemo(
    () => [
      { key: `${adminBase}/dashboard`, icon: <DashboardOutlined />, label: t('admin.dashboard', '运营概览') },
      {
        key: 'grp-routing',
        type: 'group',
        label: collapsed ? undefined : t('admin.routing_group', '模型与计价转发'),
        children: [
          { key: `${adminBase}/models`, icon: <ApiOutlined />, label: t('admin.models', '模型管理') },
          { key: `${adminBase}/billing-rules`, icon: <SlidersOutlined />, label: t('admin.billing_rules', '计价规则') },
          { key: `${adminBase}/forward-rules`, icon: <NodeIndexOutlined />, label: t('admin.forward_rules', '转发规则') },
          { key: `${adminBase}/channels`, icon: <CloudServerOutlined />, label: t('admin.channels', '渠道管理') },
          { key: `${adminBase}/channel-categories`, icon: <FolderOutlined />, label: t('admin.channel_categories', '渠道分组') },
        ],
      },
      {
        key: 'grp-users',
        type: 'group',
        label: collapsed ? undefined : t('admin.users_group', '用户与费率'),
        children: [
          { key: `${adminBase}/users`, icon: <TeamOutlined />, label: t('admin.users', '用户与组织') },
          { key: `${adminBase}/group-ratios`, icon: <DollarOutlined />, label: t('admin.group_ratios', '分组倍率') },
          { key: `${adminBase}/recharges`, icon: <FundOutlined />, label: t('admin.recharges', '财务分析') },
          { key: `${adminBase}/payment-channels`, icon: <CreditCardOutlined />, label: t('admin.payment_channels', '支付渠道') },
        ],
      },
      {
        key: 'grp-ops',
        type: 'group',
        label: collapsed ? undefined : t('admin.ops_group', '日志与运维'),
        children: [
          { key: `${adminBase}/logs`, icon: <UnorderedListOutlined />, label: t('admin.logs', '全局调用日志') },
          { key: `${adminBase}/task-logs`, icon: <VideoCameraOutlined />, label: t('admin.task_logs', '全局异步任务') },
          { key: `${adminBase}/docs`, icon: <FileTextOutlined />, label: t('admin.docs', '接口文档管理') },
          { key: `${adminBase}/announcements`, icon: <NotificationOutlined />, label: t('admin.announcements', '系统公告') },
          { key: `${adminBase}/audit-logs`, icon: <AuditOutlined />, label: t('admin.audit_logs', '审计日志') },
          { key: `${adminBase}/settings`, icon: <SettingOutlined />, label: t('admin.settings', '系统设置') },
        ],
      },
    ],
    [adminBase, collapsed, t],
  );

  const items = mode === 'admin' ? adminMenuItems : userMenuItems;

  const selectedKey = useMemo(() => {
    const p = location.pathname;
    if (mode === 'admin') {
      if (p.includes('/billing-rules')) return `${adminBase}/billing-rules`;
      if (p.includes('/forward-rules')) return `${adminBase}/forward-rules`;
      return p;
    }
    return p;
  }, [adminBase, location.pathname, mode]);

  const langMenu: MenuProps['items'] = Object.entries(LANG_NAME_MAP).map(([code, label]) => ({
    key: code,
    label,
    onClick: () => {
      persistUserLanguagePreference(code);
      void i18n.changeLanguage(code === 'zh-TW' ? 'zhTW' : code);
    },
  }));

  return (
    <Layout style={{ minHeight: '100vh', background: antdToken.colorBgLayout }}>
      <Sider
        collapsible
        collapsed={collapsed}
        onCollapse={setCollapsed}
        trigger={null}
        width={236}
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
                {mode === 'admin' ? 'Admin' : 'Console'}
              </Tag>
            </div>
          )}
        </div>

        <Menu
          mode="inline"
          selectedKeys={[selectedKey]}
          items={items}
          onClick={({ key }) => navigate(key)}
          style={{ borderInlineEnd: 'none', padding: '8px 6px' }}
        />
      </Sider>

      <Layout style={{ background: antdToken.colorBgLayout }}>
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
                options={orgs.map((o) => ({
                  value: o.id,
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

            <Dropdown menu={{ items: langMenu }} placement="bottomRight">
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
                    label: t('menu.wallet', '钱包流水'),
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
              <Space style={{ cursor: 'pointer', padding: '4px 8px', borderRadius: 8 }}>
                <Avatar size={28} style={{ backgroundColor: accent.background, color: accent.color }}>
                  {(user?.username || user?.email || 'U').slice(0, 1).toUpperCase()}
                </Avatar>
                <Typography.Text style={{ fontSize: 13 }} ellipsis>
                  {user?.username || user?.email || 'User'}
                </Typography.Text>
              </Space>
            </Dropdown>
          </Space>
        </Header>

        <Content style={{ padding: 20, maxWidth: 1600, width: '100%', margin: '0 auto' }}>
          <Outlet />
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
          locale={{ emptyText: t('common.no_data', '暂无公告') }}
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
