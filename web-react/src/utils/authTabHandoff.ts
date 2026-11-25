/**
 * 跨标签页传递「仅存在于 sessionStorage」的登录态（代理登录）。
 * 新标签默认读不到 opener 的 sessionStorage，会落到 localStorage 的管理员身份，
 * 进而出现「工作流不存在」。
 */
const AUTH_HANDOFF_KEY = 'tb_auth_tab_handoff';
const HANDOFF_TTL_MS = 30_000;

type HandoffPayload = {
  token: string;
  user: string | null;
  exp: number;
};

/** 在打开新标签前写入短时 handoff（仅当当前是 session 登录时） */
function seedSessionAuthHandoff(): void {
  try {
    const token = sessionStorage.getItem('token');
    if (!token) return;
    const payload: HandoffPayload = {
      token,
      user: sessionStorage.getItem('user'),
      exp: Date.now() + HANDOFF_TTL_MS,
    };
    localStorage.setItem(AUTH_HANDOFF_KEY, JSON.stringify(payload));
  } catch {
    /* ignore quota / privacy mode */
  }
}

/** 新标签启动时消费 handoff，写入本页 sessionStorage（须在读 auth 前调用） */
export function consumeSessionAuthHandoff(): void {
  try {
    const raw = localStorage.getItem(AUTH_HANDOFF_KEY);
    if (!raw) return;
    localStorage.removeItem(AUTH_HANDOFF_KEY);
    const data = JSON.parse(raw) as HandoffPayload;
    if (!data?.token || typeof data.exp !== 'number' || Date.now() > data.exp) return;
    sessionStorage.setItem('token', data.token);
    if (data.user) sessionStorage.setItem('user', data.user);
  } catch {
    try {
      localStorage.removeItem(AUTH_HANDOFF_KEY);
    } catch {
      /* ignore */
    }
  }
}

function clearAuthHandoff(): void {
  try {
    localStorage.removeItem(AUTH_HANDOFF_KEY);
  } catch {
    /* ignore */
  }
}

/**
 * 新标签打开工作流编辑页，并尽量带上当前 session 登录态。
 * 弹窗被拦截时回退为同页跳转（此时本页已有正确身份）。
 * @returns 是否成功打开了新标签
 */
export function openWorkflowInNewTab(path: string): boolean {
  const url = path.startsWith('http')
    ? path
    : `${window.location.origin}${path.startsWith('/') ? path : `/${path}`}`;

  seedSessionAuthHandoff();

  const win = window.open('about:blank', '_blank');
  if (!win) {
    clearAuthHandoff();
    window.location.assign(url);
    return false;
  }

  try {
    const token = sessionStorage.getItem('token');
    const user = sessionStorage.getItem('user');
    if (token) {
      win.sessionStorage.setItem('token', token);
      if (user) win.sessionStorage.setItem('user', user);
    }
  } catch {
    /* about:blank 写入失败时仍依赖 localStorage handoff */
  }

  win.location.href = url;
  return true;
}
