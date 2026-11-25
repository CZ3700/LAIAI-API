/** 推广邀请 / 团队邀请：未清浏览器缓存时 3 天内注册仍生效；首次写入后 1 天内不被覆盖 */

const AFF_STORAGE_KEY = 'tokensbyte_affiliate_code';
const TEAM_STORAGE_KEY = 'tokensbyte_team_invite';
/** 注册归因有效期 */
const INVITE_TTL_MS = 3 * 24 * 60 * 60 * 1000;
/** 首次点击后锁定时长：期间不接受其他推广链接覆盖 */
const INVITE_LOCK_MS = 1 * 24 * 60 * 60 * 1000;

type InviteStoredRecord = {
  value: string;
  /** 注册仍可使用该邀请的截止时间 */
  expiry: number;
  /** 此时间前不允许被其他邀请码覆盖 */
  lockedUntil: number;
};

function nowMs(): number {
  return Date.now();
}

function persistInviteValue(key: string, value: string): void {
  if (!value) return;
  const now = nowMs();
  const record: InviteStoredRecord = {
    value,
    expiry: now + INVITE_TTL_MS,
    lockedUntil: now + INVITE_LOCK_MS,
  };
  try {
    localStorage.setItem(key, JSON.stringify(record));
  } catch {
    /* private mode / quota */
  }
  const expires = new Date(record.expiry).toUTCString();
  const maxAge = Math.floor(INVITE_TTL_MS / 1000);
  document.cookie = `${key}=${encodeURIComponent(value)}; path=/; expires=${expires}; Max-Age=${maxAge}; SameSite=Lax`;
}

/** 读取未过期记录；过期则清理。兼容旧版无 lockedUntil 的数据（视为未锁定）。 */
function getStoredInviteRecord(key: string): InviteStoredRecord | null {
  try {
    const stored = localStorage.getItem(key);
    if (stored) {
      try {
        const data = JSON.parse(stored);
        if (!data?.value) {
          localStorage.removeItem(key);
          return null;
        }
        const expiry = Number(data.expiry) || 0;
        if (nowMs() > expiry) {
          localStorage.removeItem(key);
          return null;
        }
        const lockedUntil =
          data.lockedUntil != null && Number.isFinite(Number(data.lockedUntil))
            ? Number(data.lockedUntil)
            : 0;
        return { value: String(data.value), expiry, lockedUntil };
      } catch {
        /* ignore corrupt */
      }
    }
  } catch {
    /* ignore */
  }

  const match = document.cookie.match(new RegExp(`(?:^|;\\s*)${key}=([^;]*)`));
  if (match) {
    try {
      const value = decodeURIComponent(match[1]);
      if (!value) return null;
      // cookie 无锁信息：仅作兜底读取，视为未锁定
      return { value, expiry: nowMs() + INVITE_TTL_MS, lockedUntil: 0 };
    } catch {
      const value = match[1];
      if (!value) return null;
      return { value, expiry: nowMs() + INVITE_TTL_MS, lockedUntil: 0 };
    }
  }
  return null;
}

function getStoredInviteValue(key: string): string {
  return getStoredInviteRecord(key)?.value || '';
}

function isInviteLocked(record: InviteStoredRecord | null | undefined): boolean {
  return !!record && nowMs() <= record.lockedUntil;
}

/**
 * 尝试写入邀请参数。
 * - 锁定期内且已有不同值：拒绝覆盖
 * - 锁定期内且同值：保持原记录（不刷新计时）
 * - 锁定已过或无记录：写入并开启新的 1 天锁 + 3 天有效期
 */
function tryCaptureInviteValue(key: string, value: string): boolean {
  const next = value?.trim();
  if (!next) return false;
  const existing = getStoredInviteRecord(key);
  if (existing && isInviteLocked(existing)) {
    return existing.value === next;
  }
  persistInviteValue(key, next);
  return true;
}

/** URL / 存储择优：锁定期内优先用已锁定的存储，避免地址栏新链接抢归因 */
function pickInviteValue(urlValue: string, stored: InviteStoredRecord | null): string {
  const url = urlValue?.trim() || '';
  if (stored && isInviteLocked(stored)) {
    return stored.value;
  }
  if (url) return url;
  if (stored) return stored.value;
  return '';
}

/** 从当前（或指定）查询串捕获并写入（受 1 天锁定约束） */
export function captureInviteFromSearch(search?: string): { aff: string; team: string } {
  const params = new URLSearchParams(
    search ?? (typeof window !== 'undefined' ? window.location.search : ''),
  );
  const aff = params.get('aff')?.trim() || '';
  const team = params.get('team')?.trim() || '';
  if (aff) tryCaptureInviteValue(AFF_STORAGE_KEY, aff);
  if (team) tryCaptureInviteValue(TEAM_STORAGE_KEY, team);
  return {
    aff: getStoredInviteValue(AFF_STORAGE_KEY) || aff,
    team: getStoredInviteValue(TEAM_STORAGE_KEY) || team,
  };
}

/** 锁定期内以存储为准；否则 URL 优先，再回退存储 */
export function resolveInviteParams(search?: string): { aff: string; team: string } {
  const params = new URLSearchParams(
    search ?? (typeof window !== 'undefined' ? window.location.search : ''),
  );
  const urlAff = params.get('aff')?.trim() || '';
  const urlTeam = params.get('team')?.trim() || '';
  const aff = pickInviteValue(urlAff, getStoredInviteRecord(AFF_STORAGE_KEY));
  const team = pickInviteValue(urlTeam, getStoredInviteRecord(TEAM_STORAGE_KEY));
  return { aff, team };
}

export function buildRegisterPath(search?: string): string {
  const params = new URLSearchParams(
    search ?? (typeof window !== 'undefined' ? window.location.search : ''),
  );
  const urlAff = params.get('aff')?.trim() || '';
  const urlTeam = params.get('team')?.trim() || '';
  const next = new URLSearchParams();
  if (urlAff) next.set('aff', urlAff);
  if (urlTeam) next.set('team', urlTeam);
  const qs = next.toString();
  return qs ? `/register?${qs}` : '/register';
}

export type MarketingLinkType = 'invite' | 'team_invite' | 'theme_promo';

/**
 * 上报营销链接访问（失败静默）。
 * 后端按 IP + 站点自然日去重；已登录推广员访问自己的链接不计次。
 */
export async function trackMarketingLinkClick(opts: {
  linkType: MarketingLinkType;
  aff: string;
  linkKey?: string;
}): Promise<void> {
  const aff = (opts.aff || '').trim();
  if (!aff) return;
  try {
    const base = import.meta.env.VITE_API_BASE || '/api/v1';
    const headers: Record<string, string> = { 'Content-Type': 'application/json' };
    const token =
      (typeof sessionStorage !== 'undefined' && sessionStorage.getItem('token')) ||
      (typeof localStorage !== 'undefined' && localStorage.getItem('token')) ||
      '';
    if (token) headers.Authorization = `Bearer ${token}`;
    await fetch(`${base}/team-marketing/link-clicks`, {
      method: 'POST',
      headers,
      body: JSON.stringify({
        link_type: opts.linkType,
        link_key: opts.linkKey || undefined,
        aff,
      }),
      keepalive: true,
    });
  } catch {
    // 统计失败不影响页面
  }
}

