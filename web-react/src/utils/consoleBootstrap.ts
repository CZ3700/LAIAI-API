import type { Announcement, User } from '../types';
import { primeActivePluginsCache } from './activePlugins';
import { coalesceAsync } from './coalesceAsync';
import request from './request';

type ConsoleBootstrap = {
  user: User;
  announcements: Announcement[];
  active_plugins: any[];
};

const CACHE_KEY = 'console:bootstrap';
const RECENT_MS = 8_000;

function bootstrapCacheKey(): string {
  const token = sessionStorage.getItem('token') || localStorage.getItem('token') || '';
  return `${CACHE_KEY}:${token}`;
}

export function fetchConsoleBootstrap(): Promise<ConsoleBootstrap> {
  return coalesceAsync(
    bootstrapCacheKey(),
    async () => {
      const data = (await request.get('/console/bootstrap')) as ConsoleBootstrap;
      if (Array.isArray(data?.active_plugins)) {
        primeActivePluginsCache({ active_plugins: data.active_plugins });
      }
      return data;
    },
    { recentMs: RECENT_MS },
  );
}
