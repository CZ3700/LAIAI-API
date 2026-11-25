import i18n from '../i18n';
import type { Plugin } from '../types';
import { coalesceAsync, invalidateCoalesce, peekCoalesce } from './coalesceAsync';
import request from './request';
import { pluginAdminMenuTabKey } from './pluginAdminTabs';

type AdminPluginsResponse = {
  plugins?: Plugin[];
};

const CACHE_KEY = 'plugins:admin-list';
const RECENT_MS = 8_000;
const listeners = new Set<() => void>();

export function fetchAdminPlugins(): Promise<AdminPluginsResponse> {
  return coalesceAsync(
    CACHE_KEY,
    () => request.get('/plugins') as Promise<AdminPluginsResponse>,
    { recentMs: RECENT_MS },
  );
}

export function invalidateAdminPluginsCache(): void {
  invalidateCoalesce(CACHE_KEY);
  listeners.forEach((fn) => fn());
}

export function subscribeAdminPluginsChange(cb: () => void): () => void {
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
  };
}

/** 同步读取已缓存的管理端插件列表；没有则返回 null */
export function peekAdminPlugins(): Plugin[] | null {
  const cached = peekCoalesce<AdminPluginsResponse>(CACHE_KEY);
  if (!cached || !Array.isArray(cached.plugins)) return null;
  return cached.plugins;
}

export function selectAdminSidebarPlugins(
  plugins: Plugin[],
  canSee: (name: string) => boolean,
): Plugin[] {
  return plugins
    .filter((p) => p.is_enabled === 1 && p.show_in_admin_menu === 1 && canSee(p.name))
    .sort((a, b) => {
      const byWeight = (b.admin_menu_sort ?? 0) - (a.admin_menu_sort ?? 0);
      if (byWeight !== 0) return byWeight;
      return (a.id ?? 0) - (b.id ?? 0);
    });
}

export function adminSidebarPluginLabel(
  plugin: Plugin,
  fallback: (name: string, title: string) => string,
): string {
  const custom = (plugin.admin_menu_title || '').trim();
  const lang = (i18n.language || 'zh').toLowerCase();
  const isSimplified =
    lang.startsWith('zh') && lang !== 'zh-tw' && lang !== 'zh-hk' && lang !== 'zh-mo';
  if (custom && (isSimplified || !/[\u4e00-\u9fa5]/.test(custom))) return custom;
  return fallback(plugin.name, plugin.title);
}

export function adminSidebarPluginHref(plugin: Plugin): string {
  const tab = pluginAdminMenuTabKey(plugin.name, plugin.admin_menu_default_tab);
  return `/admin0755/plugins/${plugin.name}/config#${tab}`;
}
