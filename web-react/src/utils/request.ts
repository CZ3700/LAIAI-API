import axios, { type AxiosInstance } from 'axios';
import { message } from 'antd';
import { resolveTimedisplay } from './timedisplay';
import { isAwaitingFreshSetup } from './freshSetup';

message.config({ maxCount: 3 });

function resolveAuthRedirectPath(): string | null {
  const pathname = window.location.pathname;
  const adminPath = localStorage.getItem('laiai_admin_path') || 'admin';
  const adminBase = `/${adminPath}`;
  const isAdminLogin = pathname === adminBase || pathname === `${adminBase}/`;
  const isUserAuthPage =
    pathname === '/login' ||
    pathname.startsWith('/login/') ||
    pathname === '/register' ||
    pathname === '/forgot-password';

  if (isAdminLogin || isUserAuthPage) return null;

  const isAdminArea = pathname === adminBase || pathname.startsWith(`${adminBase}/`);
  return isAdminArea ? adminBase : '/login';
}

function readStoredToken(): string | null {
  const direct = sessionStorage.getItem('token') || localStorage.getItem('token');
  if (direct) return direct;
  try {
    const raw = sessionStorage.getItem('laiai_auth') || localStorage.getItem('laiai_auth');
    if (raw) {
      const parsed = JSON.parse(raw) as { token?: string | null };
      if (parsed?.token) return parsed.token;
    }
  } catch {
    // ignore malformed storage
  }
  return null;
}

function clearStoredAuth() {
  sessionStorage.removeItem('token');
  sessionStorage.removeItem('user');
  sessionStorage.removeItem('laiai_auth');
  localStorage.removeItem('token');
  localStorage.removeItem('user');
  localStorage.removeItem('laiai_auth');
}

function attachInterceptors(instance: AxiosInstance) {
  instance.interceptors.request.use(
    (config) => {
      const token = readStoredToken();
      if (token && !config.headers.Authorization && !config.headers.authorization) {
        config.headers.Authorization = `Bearer ${token}`;
      }
      try {
        config.headers['x-timezone'] = resolveTimedisplay();
      } catch {
        // ignore timezone read error
      }
      return config;
    },
    (error) => Promise.reject(error),
  );

  instance.interceptors.response.use(
    (response) => response.data,
    (error) => {
      const { response, config } = error;
      if (
        axios.isCancel(error) ||
        error?.code === 'ERR_CANCELED' ||
        error?.name === 'CanceledError' ||
        error?.message === 'canceled' ||
        error?.message === 'Request aborted'
      ) {
        return Promise.reject(error);
      }

      if ((config as any)?.skipErrorHandler) {
        return Promise.reject(error);
      }

      if (response) {
        const { status, data } = response;
        const serverMsg =
          data?.error?.message ||
          data?.message ||
          (typeof data?.error === 'string' ? data.error : undefined) ||
          (typeof data === 'string' && !data.trimStart().startsWith('<') ? data : undefined);

        if (status === 429) {
          message.warning({
            content: serverMsg || '操作过于频繁，请稍后再试',
            key: 'global-rate-limit-warning',
            duration: 3,
          });
          return Promise.reject(error);
        }

        if (status === 401) {
          if (isAwaitingFreshSetup()) {
            return Promise.reject(error);
          }
          const hadToken = !!readStoredToken();
          if (hadToken) {
            clearStoredAuth();
          }
          const redirectTo = resolveAuthRedirectPath();
          if (redirectTo) {
            if (hadToken) {
              message.error('登录状态已过期，请重新登录');
            }
            window.location.href = redirectTo;
          } else if (serverMsg && serverMsg !== 'Authentication required') {
            message.error(serverMsg);
          }
          return Promise.reject(error);
        }

        message.error(serverMsg || 'Request failed');
      } else {
        message.error('Network error');
      }
      return Promise.reject(error);
    },
  );
}

const request = axios.create({
  baseURL: import.meta.env.VITE_API_BASE || '/api/v1',
  timeout: 120000,
});
attachInterceptors(request);

/** 直连 laiai 原生 /api/* 端点（渠道、账户、钱包、账本、充值、系统设置、用户/组织等） */
export const coreApi = axios.create({
  baseURL: '/api',
  timeout: 120000,
});
attachInterceptors(coreApi);

export default request;
