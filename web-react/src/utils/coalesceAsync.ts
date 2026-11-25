/**
 * 合并进行中的相同异步任务（如 React StrictMode 双挂载、并发调用）。
 * 可选短时缓存：避免 settings 等依赖刚到又立刻重打同一接口。
 */
const inflight = new Map<string, Promise<unknown>>();
const recent = new Map<string, { at: number; value: unknown }>();

const DEFAULT_RECENT_MS = 1200;

export function coalesceAsync<T>(
  key: string,
  factory: () => Promise<T>,
  options?: { recentMs?: number },
): Promise<T> {
  const recentMs = options?.recentMs ?? DEFAULT_RECENT_MS;
  if (recentMs > 0) {
    const cached = recent.get(key);
    if (cached && Date.now() - cached.at < recentMs) {
      return Promise.resolve(cached.value as T);
    }
  }

  const existing = inflight.get(key);
  if (existing) return existing as Promise<T>;

  const promise = factory()
    .then((value) => {
      if (recentMs > 0) {
        recent.set(key, { at: Date.now(), value });
      }
      return value;
    })
    .finally(() => {
      if (inflight.get(key) === promise) {
        inflight.delete(key);
      }
    });
  inflight.set(key, promise);
  return promise;
}

/** 清除指定 key 的短时缓存与进行中 Promise（后台改配置后强制刷新） */
export function invalidateCoalesce(key: string): void {
  recent.delete(key);
  inflight.delete(key);
}

/** 写入短时缓存，供后续 coalesce 命中（如 bootstrap 预热 /plugins/active） */
export function primeCoalesce<T>(key: string, value: T): void {
  recent.set(key, { at: Date.now(), value });
}

/** 同步读取短时缓存。默认 8s 内有效，过期视为未命中，避免侧栏先用旧列表再刷一次 */
export function peekCoalesce<T>(key: string, maxAgeMs = 8_000): T | null {
  const cached = recent.get(key);
  if (!cached) return null;
  if (maxAgeMs > 0 && Date.now() - cached.at > maxAgeMs) return null;
  return cached.value as T;
}
