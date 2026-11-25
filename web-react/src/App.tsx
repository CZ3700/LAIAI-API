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
const BillingRules = lazy(() => import('./pages/Models/BillingRules'));
const ForwardRules = lazy(() => import('./pages/Models/ForwardRules'));

const DashboardPage = lazy(() => import('./pages/Console/DashboardPage'));
const ApiKeysPage = lazy(() => import('./pages/Console/ApiKeysPage'));
const LogsPage = lazy(() => import('./pages/Console/LogsPage'));
const TaskLogsPage = lazy(() => import('./pages/Console/TaskLogsPage'));
const WalletPage = lazy(() => import('./pages/Console/WalletPage'));
const TeamByokPage = lazy(() => import('./pages/Console/TeamByokPage'));
const ProfilePage = lazy(() => import('./pages/Console/ProfilePage'));

const ChannelsPage = lazy(() => import('./pages/Admin/ChannelsPage'));
const ChannelCategoriesPage = lazy(() => import('./pages/Admin/ChannelCategoriesPage'));
const ModelsAdminPage = lazy(() => import('./pages/Admin/ModelsAdminPage'));
const UsersOrgsPage = lazy(() => import('./pages/Admin/UsersOrgsPage'));
const GroupRatiosPage = lazy(() => import('./pages/Admin/GroupRatiosPage'));
const Finance = import('./pages/Admin/FinanceOpsPages');
const RechargesAdminPage = lazy(() => Finance.then((m) => ({ default: m.RechargesAdminPage })));
const PaymentChannelsPage = lazy(() => Finance.then((m) => ({ default: m.PaymentChannelsPage })));
const AnnouncementsAdminPage = lazy(() => Finance.then((m) => ({ default: m.AnnouncementsAdminPage })));
const AuditLogsPage = lazy(() => Finance.then((m) => ({ default: m.AuditLogsPage })));
const SystemSettingsPage = lazy(() => Finance.then((m) => ({ default: m.SystemSettingsPage })));

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

        <Route element={<RequireAuth adminPath={adminPath}><DashboardLayout mode="user" /></RequireAuth>}>
          <Route path="/dashboard" element={<DashboardPage />} />
          <Route path="/apikeys" element={<ApiKeysPage />} />
          <Route path="/logs" element={<LogsPage />} />
          <Route path="/task-logs" element={<TaskLogsPage />} />
          <Route path="/wallet" element={<WalletPage />} />
          <Route path="/team" element={<TeamByokPage />} />
          <Route path="/profile" element={<ProfilePage />} />
        </Route>

        <Route path={`/${adminPath}`} element={<AdminLogin />} />
        <Route
          path={`/${adminPath}/*`}
          element={a(<DashboardLayout mode="admin" />)}
        >
          <Route path="dashboard" element={<DashboardPage scope="platform" />} />
          <Route path="models" element={<ModelsAdminPage />} />
          <Route path="billing-rules" element={<BillingRules />} />
          <Route path="billing-rules/new" element={<BillingRules />} />
          <Route path="billing-rules/edit/:id" element={<BillingRules />} />
          <Route path="forward-rules" element={<ForwardRules />} />
          <Route path="forward-rules/new" element={<ForwardRules />} />
          <Route path="forward-rules/edit/:id" element={<ForwardRules />} />
          <Route path="channels" element={<ChannelsPage />} />
          <Route path="channel-categories" element={<ChannelCategoriesPage />} />
          <Route path="users" element={<UsersOrgsPage />} />
          <Route path="group-ratios" element={<GroupRatiosPage />} />
          <Route path="recharges" element={<RechargesAdminPage />} />
          <Route path="payment-channels" element={<PaymentChannelsPage />} />
          <Route path="logs" element={<LogsPage isAdmin />} />
          <Route path="task-logs" element={<TaskLogsPage isAdmin />} />
          <Route path="docs" element={<DocsManager />} />
          <Route path="announcements" element={<AnnouncementsAdminPage />} />
          <Route path="audit-logs" element={<AuditLogsPage />} />
          <Route path="settings" element={<SystemSettingsPage />} />
          <Route index element={<Navigate to="dashboard" replace />} />
        </Route>

        <Route path="*" element={<Navigate to="/home/models" replace />} />
      </Routes>
    </Suspense>
  );
};

export default App;
