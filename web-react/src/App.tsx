import React, { Suspense, lazy, useEffect } from 'react';
import { Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { Spin } from 'antd';
import useAuthStore from './store/auth';
import useSettingsStore from './store/settings';
import DashboardLayout from './layouts/DashboardLayout';

const Login = lazy(() => import('./pages/Auth/Login'));
const Register = lazy(() => import('./pages/Auth/Register'));
const AdminLogin = lazy(() => import('./pages/Auth/AdminLogin'));
const ModelMarketplace = lazy(() => import('./pages/Plugins/ModelMarketplace/ModelMarketplace'));
const RelayAPI = lazy(() => import('./pages/Plugins/DocsApi/RelayAPI'));
const DocsManager = lazy(() => import('./pages/Plugins/DocsApi/DocsManager'));

// Shared & User Console Pages
const Dashboard = lazy(() => import('./pages/Dashboard/Dashboard'));
const ModelDistribution30dPage = lazy(() => import('./pages/Dashboard/ModelDistribution30dPage'));
const ApiKeysPage = lazy(() => import('./pages/Console/ApiKeysPage'));
const Logs = lazy(() => import('./pages/Logs/Logs'));
const TaskLogs = lazy(() => import('./pages/Logs/TaskLogs'));
const WalletPage = lazy(() => import('./pages/Console/WalletPage'));
const TeamByokPage = lazy(() => import('./pages/Console/TeamByokPage'));
const ProfilePage = lazy(() => import('./pages/Console/ProfilePage'));

// Admin Console Refactored Pages (tkeapi aligned)
const Channels = lazy(() => import('./pages/Channels/Channels'));
const ChannelConfigs = lazy(() => import('./pages/Channels/ChannelConfigs'));
const ChannelTest = lazy(() => import('./pages/Channels/ChannelTest'));
const ModelChannelsDisplay = lazy(() => import('./pages/Channels/ModelChannelsDisplay'));
const Models = lazy(() => import('./pages/Models/Models'));
const BillingRules = lazy(() => import('./pages/Models/BillingRules'));
const ForwardRules = lazy(() => import('./pages/Models/ForwardRules'));
const RechargeRecords = lazy(() => import('./pages/Finance/RechargeRecords'));
const OrderDetails = lazy(() => import('./pages/Finance/OrderDetails'));
const FinanceDataAnalysis = lazy(() => import('./pages/Finance/FinanceDataAnalysis'));
const Settings = lazy(() => import('./pages/Admin/Settings'));
const PaymentSettings = lazy(() => import('./pages/Admin/PaymentSettings'));
const MessageNotification = lazy(() => import('./pages/Admin/MessageNotification'));
const OAuthSettings = lazy(() => import('./pages/Admin/OAuthSettings'));
const SystemAbout = lazy(() => import('./pages/Admin/SystemAbout'));
const Announcements = lazy(() => import('./pages/Admin/Marketing/Announcements'));

// Retained laiai-specific Admin Modules
const UsersOrgsPage = lazy(() => import('./pages/Admin/UsersOrgsPage'));
const GroupRatiosPage = lazy(() => import('./pages/Admin/GroupRatiosPage'));
const Finance = import('./pages/Admin/FinanceOpsPages');
const PaymentChannelsPage = lazy(() => Finance.then((m) => ({ default: m.PaymentChannelsPage })));
const AuditLogsPage = lazy(() => Finance.then((m) => ({ default: m.AuditLogsPage })));
const ArkMonitorPage = lazy(() => import('./pages/Admin/ArkMonitorPage'));
const VolcAssetPage = lazy(() => import('./pages/Admin/VolcAssetPage'));

const Loading = () => (
  <div style={{ display: 'flex', justifyContent: 'center', padding: 80 }}>
    <Spin size="large" />
  </div>
);

const RequireAuth: React.FC<{ children: React.ReactElement; admin?: boolean; adminPath: string }> = ({
  children,
  admin,
  adminPath,
}) => {
  const { token, user } = useAuthStore();
  const location = useLocation();
  if (!token) {
    return <Navigate to={admin ? `/${adminPath}` : '/login'} replace state={{ from: location.pathname }} />;
  }
  if (admin) {
    const ok = user?.role === 'admin' || user?.plat_role === 'admin' || user?.plat_role === 'superadmin';
    if (!ok) return <Navigate to="/dashboard" replace />;
  }
  return children;
};

const App: React.FC = () => {
  const { settings, fetchSettings } = useSettingsStore();
  useEffect(() => {
    void fetchSettings();
  }, [fetchSettings]);
  const adminPath = settings?.site?.admin_path || localStorage.getItem('laiai_admin_path') || 'admin';
  const a = (el: React.ReactElement) => (
    <RequireAuth admin adminPath={adminPath}>{el}</RequireAuth>
  );

  return (
    <Suspense fallback={<Loading />}>
      <Routes>
        <Route path="/" element={<Navigate to="/home/models" replace />} />
        <Route path="/login" element={<Login />} />
        <Route path="/register" element={<Register />} />

        <Route path="/home/models" element={<ModelMarketplace />} />
        <Route path="/models" element={<ModelMarketplace />} />
        <Route path="/docs" element={<Navigate to="/docs/quickstart/doc0003" replace />} />
        <Route path="/docs/:id" element={<RelayAPI />} />
        <Route path="/docs/:category/:id" element={<RelayAPI />} />
        <Route path="/console" element={<Navigate to="/dashboard" replace />} />
        <Route path="/console/*" element={<Navigate to="/dashboard" replace />} />

        <Route element={<RequireAuth adminPath={adminPath}><DashboardLayout mode="user" /></RequireAuth>}>
          <Route path="/dashboard" element={<Dashboard />} />
          <Route path="/dashboard/model-distribution" element={<ModelDistribution30dPage />} />
          <Route path="/apikeys" element={<ApiKeysPage />} />
          <Route path="/tokens" element={<Navigate to="/apikeys" replace />} />
          <Route path="/logs" element={<Logs />} />
          <Route path="/logs/:logType" element={<Logs />} />
          <Route path="/task-logs" element={<TaskLogs />} />
          <Route path="/task-logs/:logType" element={<TaskLogs />} />
          <Route path="/wallet" element={<WalletPage />} />
          <Route path="/team" element={<TeamByokPage />} />
          <Route path="/ark-video-monitor" element={<ArkMonitorPage mode="user" />} />
          <Route path="/profile" element={<ProfilePage />} />
        </Route>

        <Route path={`/${adminPath}`} element={<AdminLogin />} />
        <Route element={a(<DashboardLayout mode="admin" />)}>
          <Route path={`/${adminPath}/dashboard`} element={<Dashboard />} />
          <Route path={`/${adminPath}/dashboard/model-distribution`} element={<ModelDistribution30dPage />} />
          <Route path={`/${adminPath}/apikeys`} element={<ApiKeysPage />} />
          <Route path={`/${adminPath}/tokens`} element={<Navigate to={`/${adminPath}/apikeys`} replace />} />

          <Route path={`/${adminPath}/logs`} element={<Logs />} />
          <Route path={`/${adminPath}/logs/:logType`} element={<Logs />} />
          <Route path={`/${adminPath}/task-logs`} element={<TaskLogs />} />
          <Route path={`/${adminPath}/task-logs/:logType`} element={<TaskLogs />} />

          <Route path={`/${adminPath}/channels`} element={<Channels />} />
          <Route path={`/${adminPath}/channels/new`} element={<Channels />} />
          <Route path={`/${adminPath}/channels/edit/:id`} element={<Channels />} />
          <Route path={`/${adminPath}/channels/model-display`} element={<ModelChannelsDisplay />} />
          <Route path={`/${adminPath}/channels/:id/analysis`} element={<ChannelTest />} />
          <Route path={`/${adminPath}/channel-configs`} element={<ChannelConfigs />} />
          <Route path={`/${adminPath}/channel-configs/system/:systemKey`} element={<ChannelConfigs />} />
          <Route path={`/${adminPath}/payment-channels`} element={<PaymentChannelsPage />} />
          <Route path={`/${adminPath}/ark-monitor`} element={<ArkMonitorPage mode="admin" />} />
          <Route path={`/${adminPath}/volc-assets`} element={<VolcAssetPage />} />

          <Route path={`/${adminPath}/models`} element={<Models />} />
          <Route path={`/${adminPath}/models/new`} element={<Models />} />
          <Route path={`/${adminPath}/models/edit/:id`} element={<Models />} />
          <Route path={`/${adminPath}/models/library/edit/:id`} element={<Models />} />
          <Route path={`/${adminPath}/billing-rules`} element={<BillingRules />} />
          <Route path={`/${adminPath}/billing-rules/new`} element={<BillingRules />} />
          <Route path={`/${adminPath}/billing-rules/edit/:id`} element={<BillingRules />} />
          <Route path={`/${adminPath}/forward-rules`} element={<ForwardRules />} />
          <Route path={`/${adminPath}/forward-rules/new`} element={<ForwardRules />} />
          <Route path={`/${adminPath}/forward-rules/edit/:id`} element={<ForwardRules />} />

          <Route path={`/${adminPath}/marketing/announcements`} element={<Announcements />} />
          <Route path={`/${adminPath}/announcements`} element={<Navigate to={`/${adminPath}/marketing/announcements`} replace />} />

          <Route path={`/${adminPath}/users`} element={<UsersOrgsPage />} />
          <Route path={`/${adminPath}/group-ratios`} element={<GroupRatiosPage />} />

          <Route path={`/${adminPath}/finance/recharges`} element={<RechargeRecords />} />
          <Route path={`/${adminPath}/recharges`} element={<Navigate to={`/${adminPath}/finance/recharges`} replace />} />
          <Route path={`/${adminPath}/finance/orders`} element={<OrderDetails />} />
          <Route path={`/${adminPath}/finance/analysis`} element={<FinanceDataAnalysis />} />

          <Route path={`/${adminPath}/settings`} element={<Settings />} />
          <Route path={`/${adminPath}/payment-settings`} element={<PaymentSettings />} />
          <Route path={`/${adminPath}/message-notification`} element={<MessageNotification />} />
          <Route path={`/${adminPath}/oauth-settings`} element={<OAuthSettings />} />
          <Route path={`/${adminPath}/docs-manager`} element={<DocsManager />} />
          <Route path={`/${adminPath}/docs`} element={<Navigate to={`/${adminPath}/docs-manager`} replace />} />
          <Route path={`/${adminPath}/audit-logs`} element={<AuditLogsPage />} />
          <Route path={`/${adminPath}/about`} element={<SystemAbout />} />
        </Route>

        <Route path="*" element={<Navigate to="/home/models" replace />} />
      </Routes>
    </Suspense>
  );
};

export default App;
