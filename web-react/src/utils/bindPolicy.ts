import type { RegistrationSettings, User } from '../types';

type BindEnforcement = 'all' | 'any' | 'prompt_all' | 'prompt_any' | 'prompt_only';

function enforcementMode(reg?: RegistrationSettings | null): BindEnforcement {
  const mode = reg?.bind_enforcement;
  if (mode === 'any' || mode === 'prompt_all' || mode === 'prompt_any' || mode === 'prompt_only') {
    return mode;
  }
  return 'all';
}

/** 只弹窗，不拦截创建令牌。prompt_only 为旧值，等同仅弹窗提示绑定全部 */
function isPromptEnforcement(mode: BindEnforcement): boolean {
  return mode === 'prompt_only' || mode === 'prompt_all' || mode === 'prompt_any';
}

/** 勾选项目里绑定一项即满足 */
function requiresAnyBind(mode: BindEnforcement): boolean {
  return mode === 'any' || mode === 'prompt_any';
}

export function hasValidMobile(mobile?: string | null): boolean {
  return !!(mobile && mobile.trim());
}

export function hasValidEmail(email?: string | null): boolean {
  const e = (email || '').trim();
  return !!e && !e.endsWith('@tokensbyte.local');
}

export function isBindPolicyActive(reg?: RegistrationSettings | null): boolean {
  return !!(reg?.require_bind_mobile || reg?.require_bind_email);
}

/** 用户绑定是否已满足策略（用于弹窗与前端预检） */
export function isBindSatisfied(
  reg?: RegistrationSettings | null,
  user?: Pick<User, 'email' | 'mobile'> | null,
): boolean {
  if (!isBindPolicyActive(reg)) return true;
  const needMobile = !!reg?.require_bind_mobile;
  const needEmail = !!reg?.require_bind_email;
  const hasMobile = hasValidMobile(user?.mobile);
  const hasEmail = hasValidEmail(user?.email);
  if (needMobile && needEmail) {
    return requiresAnyBind(enforcementMode(reg)) ? hasMobile || hasEmail : hasMobile && hasEmail;
  }
  if (needMobile) return hasMobile;
  if (needEmail) return hasEmail;
  return true;
}

/** 创建令牌是否应被硬拦截 */
export function shouldBlockTokenCreate(
  reg?: RegistrationSettings | null,
  user?: Pick<User, 'email' | 'mobile' | 'role'> | null,
): boolean {
  if (user?.role === 'admin') return false;
  if (!isBindPolicyActive(reg)) return false;
  if (isPromptEnforcement(enforcementMode(reg))) return false;
  return !isBindSatisfied(reg, user);
}

export function tokenBindBlockI18nKey(reg?: RegistrationSettings | null): string {
  const needMobile = !!reg?.require_bind_mobile;
  const needEmail = !!reg?.require_bind_email;
  if (needMobile && needEmail) {
    return requiresAnyBind(enforcementMode(reg))
      ? 'tokens.bind_need_mobile_or_email'
      : 'tokens.bind_need_mobile_and_email';
  }
  if (needMobile) return 'tokens.bind_need_mobile';
  if (needEmail) return 'tokens.bind_need_email';
  return 'tokens.bind_need_account';
}

export function tokenBindBlockMessage(reg?: RegistrationSettings | null): string {
  const needMobile = !!reg?.require_bind_mobile;
  const needEmail = !!reg?.require_bind_email;
  if (needMobile && needEmail) {
    return requiresAnyBind(enforcementMode(reg))
      ? '创建 API 令牌前请先绑定手机号或邮箱'
      : '创建 API 令牌前请先绑定手机号和邮箱';
  }
  if (needMobile) return '创建 API 令牌前请先绑定手机号';
  if (needEmail) return '创建 API 令牌前请先绑定邮箱';
  return '创建 API 令牌前请先完成账号绑定';
}

export function bindPromptDescription(reg?: RegistrationSettings | null): string {
  const needMobile = !!reg?.require_bind_mobile;
  const needEmail = !!reg?.require_bind_email;
  const mode = enforcementMode(reg);
  const soft = isPromptEnforcement(mode);
  let need = '';
  if (needMobile && needEmail) {
    need = requiresAnyBind(mode) ? '手机号或邮箱' : '手机号和邮箱';
  } else if (needMobile) {
    need = '手机号';
  } else if (needEmail) {
    need = '邮箱';
  }
  if (soft) {
    return `为保障账号安全，建议绑定${need}。可稍后处理，关闭后当天不再提示。`;
  }
  return `为保障账号安全，请绑定${need}。可稍后处理，关闭后当天不再提示；创建 API 令牌前需完成绑定。`;
}

/** 站点时区下的自然日 YYYY-MM-DD */
function calendarDateInTz(timeZone?: string): string {
  try {
    return new Intl.DateTimeFormat('en-CA', {
      timeZone: timeZone || Intl.DateTimeFormat().resolvedOptions().timeZone,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
    }).format(new Date());
  } catch {
    return new Date().toISOString().slice(0, 10);
  }
}

function dismissStorageKey(userId: string, date: string): string {
  return `tokensbyte_bind_prompt_dismiss_${userId}_${date}`;
}

export function isBindPromptDismissedToday(userId: string, timeZone?: string): boolean {
  const date = calendarDateInTz(timeZone);
  try {
    return localStorage.getItem(dismissStorageKey(userId, date)) === '1';
  } catch {
    return false;
  }
}

export function dismissBindPromptToday(userId: string, timeZone?: string): void {
  const date = calendarDateInTz(timeZone);
  try {
    localStorage.setItem(dismissStorageKey(userId, date), '1');
  } catch {
    // ignore
  }
}
