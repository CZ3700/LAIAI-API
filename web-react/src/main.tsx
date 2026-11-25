import React from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import App from './App.tsx';
import './utils/timezoneInterceptor';
import './utils/patchAntdMessage';
import './i18n';
import './index.css';
import { AppThemeProvider } from './components/AppThemeProvider';

// Vite 动态分片加载异常自动恢复（替代内联 script 以严格符合 Caddy CSP）
window.addEventListener(
  'error',
  (e) => {
    const msg = (e && (e.message || (e.error && e.error.message))) || '';
    if (typeof msg === 'string' && msg.includes('Failed to fetch dynamically imported module')) {
      const key = '__laiai_vite_dep_reload__';
      const last = sessionStorage.getItem(key);
      const now = Date.now();
      if (!last || now - Number.parseInt(last, 10) > 3000) {
        sessionStorage.setItem(key, String(now));
        window.location.reload();
      }
    }
  },
  true,
);

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <AppThemeProvider>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </AppThemeProvider>
  </React.StrictMode>,
);
