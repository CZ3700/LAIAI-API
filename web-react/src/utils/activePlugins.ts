/**
 * 活跃插件列表：全站共用一次请求合并，避免刷新时 PluginRoute / Layout / 页面多处重复打 /plugins/active
 */
import request from './request';
import { coalesceAsync, invalidateCoalesce, peekCoalesce, primeCoalesce } from './coalesceAsync';

export type ActivePluginsResponse = {
  active_plugins?: any[];
  [key: string]: unknown;
};

const CACHE_KEY = 'plugins:active';
/** 同页多次挂载 / user 变更触发的重复拉取共用缓存 */
const RECENT_MS = 8_000;

export function fetchActivePlugins(): Promise<ActivePluginsResponse> {
  return coalesceAsync(
    CACHE_KEY,
    () =>
      request.get('/plugins/active', {
        skipErrorHandler: true,
      } as any) as Promise<ActivePluginsResponse>,
    { recentMs: RECENT_MS },
  );
}

/** 后台启用/停用插件后调用，使下次拉取走网络 */
export function invalidateActivePluginsCache(): void {
  invalidateCoalesce(CACHE_KEY);
}

/** 控制台 bootstrap 已带回插件列表时预热，避免 PluginRoute 再打一次 */
export function primeActivePluginsCache(response: ActivePluginsResponse): void {
  primeCoalesce(CACHE_KEY, response);
}

/** 登录落地已经拉过活跃插件时，侧栏首屏直接用这份列表，避免先画一版再换 */
export function peekActivePlugins(): any[] | null {
  const cached = peekCoalesce<ActivePluginsResponse>(CACHE_KEY);
  if (!cached || !Array.isArray(cached.active_plugins)) return null;
  return cached.active_plugins;
}
