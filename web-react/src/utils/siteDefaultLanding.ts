import { fetchActivePlugins } from './activePlugins';

const PLAYGROUND_2026_HOME = '/playground-2026';

type LandingUser = {
  role?: string;
  user_group?: string;
  level_id?: string | number | null;
};

function levelAllowed(allowedLevels: string, user: LandingUser): boolean {
  if (allowedLevels === 'all') return true;
  const userGroup = user.user_group || '';
  const levelId = user.level_id != null ? String(user.level_id) : '';
  const levels = allowedLevels.split(',').map((s) => s.trim()).filter(Boolean);
  return levels.includes(userGroup) || (levelId !== '' && levels.includes(levelId));
}

/** 站点默认创作中心开启且当前用户可进入时，返回创作中心 2026 主页。 */
export async function playground2026LoginLanding(
  user: LandingUser | null | undefined,
): Promise<string | null> {
  if (!user || user.role === 'admin') return null;
  try {
    const res = await fetchActivePlugins();
    const pg = (res?.active_plugins || []).find((p) => p?.name === 'playground_2026');
    if (!pg || pg.site_default_enabled !== true) return null;
    if (!levelAllowed(String(pg.allowed_levels || ''), user)) return null;
    return PLAYGROUND_2026_HOME;
  } catch {
    return null;
  }
}
