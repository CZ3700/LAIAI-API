import React, { Component, type ReactNode, type ErrorInfo } from 'react';
import { Result, Button } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';

interface Props {
  children?: ReactNode;
  fallbackTitle?: string;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

export class ErrorBoundary extends Component<Props, State> {
  public state: State = {
    hasError: false,
    error: null,
  };

  public static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  public componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    console.error('ErrorBoundary caught an error:', error, errorInfo);
    const msg = String(error?.message || '');
    // 针对 Vite 开发环境下依赖重编译后客户端保留旧模块哈希导致的 React 双实例 / 钩子未绑定异常，执行自动防抖重载
    if (
      msg.includes('useSyncExternalStore') ||
      msg.includes('Invalid hook call') ||
      msg.includes('Outdated Optimize Dep') ||
      msg.includes('Failed to fetch dynamically imported module')
    ) {
      const key = '__tb_vite_dep_auto_reload__';
      const last = sessionStorage.getItem(key);
      const now = Date.now();
      if (!last || now - parseInt(last, 10) > 4000) {
        sessionStorage.setItem(key, String(now));
        const url = new URL(window.location.href);
        url.searchParams.set('_tb_ts', String(now));
        window.location.replace(url.toString());
      }
    }
  }

  private handleReload = () => {
    const url = new URL(window.location.href);
    url.searchParams.set('_tb_ts', String(Date.now()));
    window.location.replace(url.toString());
  };

  private handleReset = () => {
    this.setState({ hasError: false, error: null });
  };

  public render() {
    if (this.state.hasError) {
      return (
        <div style={{ padding: '32px 16px', display: 'flex', justifyContent: 'center' }}>
          <Result
            status="error"
            title={this.props.fallbackTitle || '页面加载异常'}
            subTitle={
              this.state.error?.message ||
              '当前页面加载失败，可能是组件缓存或资源更新导致，请点击刷新重试。'
            }
            extra={[
              <Button
                type="primary"
                key="reload"
                icon={<ReloadOutlined />}
                onClick={this.handleReload}
              >
                刷新页面
              </Button>,
              <Button key="retry" onClick={this.handleReset}>
                重试加载
              </Button>,
            ]}
          />
        </div>
      );
    }

    return this.props.children;
  }
}

export default ErrorBoundary;
