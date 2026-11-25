import { create } from 'zustand';
import type { User } from '../types';

export type { User } from '../types';

const AUTH_STORAGE_KEY = 'laiai_auth';
const SESSION_TAB_KEY = 'laiai_session_tab_id';

function ensureTabId(): string {
  try {
    let id = sessionStorage.getItem(SESSION_TAB_KEY);
    if (!id) {
      id = `tab_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
      sessionStorage.setItem(SESSION_TAB_KEY, id);
    }
    return id;
  } catch {
    return 'default';
  }
}

export function getAuthFromStorage(): { user: User | null; token: string | null } {
  try {
    const directToken = sessionStorage.getItem('token') || localStorage.getItem('token');
    const directUserRaw = sessionStorage.getItem('user') || localStorage.getItem('user');
    const raw = sessionStorage.getItem(AUTH_STORAGE_KEY) || localStorage.getItem(AUTH_STORAGE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as { user?: User | null; token?: string | null };
      const tok = parsed.token ?? directToken ?? null;
      const usr = parsed.user ?? (directUserRaw ? (JSON.parse(directUserRaw) as User) : null);
      if (tok) return { user: usr, token: tok };
    }
    if (directToken) {
      return {
        user: directUserRaw ? (JSON.parse(directUserRaw) as User) : null,
        token: directToken,
      };
    }
    return { user: null, token: null };
  } catch {
    return { user: null, token: null };
  }
}

function saveAuthToStorage(user: User | null, token: string | null) {
  try {
    ensureTabId();
    if (!token) {
      sessionStorage.removeItem(AUTH_STORAGE_KEY);
      localStorage.removeItem(AUTH_STORAGE_KEY);
      sessionStorage.removeItem('token');
      sessionStorage.removeItem('user');
      localStorage.removeItem('token');
      localStorage.removeItem('user');
      return;
    }
    const payload = JSON.stringify({ user, token });
    sessionStorage.setItem(AUTH_STORAGE_KEY, payload);
    localStorage.setItem(AUTH_STORAGE_KEY, payload);
    localStorage.setItem('token', token);
    if (user) {
      localStorage.setItem('user', JSON.stringify(user));
    } else {
      localStorage.removeItem('user');
    }
  } catch {
    // ignore storage quota errors
  }
}

interface AuthState {
  user: User | null;
  token: string | null;
  isLoggedIn: boolean;
  setUser: (user: User | null, useSession?: boolean) => void;
  setToken: (token: string | null, useSession?: boolean) => void;
  setAuth: (user: User | null, token: string | null, useSession?: boolean) => void;
  logout: () => void;
}

const initial = getAuthFromStorage();

export const useAuthStore = create<AuthState>((set, get) => ({
  user: initial.user,
  token: initial.token,
  isLoggedIn: Boolean(initial.token),
  setUser: (user, useSession = false) => {
    const token = get().token;
    if (useSession && user) {
      sessionStorage.setItem('user', JSON.stringify(user));
    } else {
      saveAuthToStorage(user, token);
    }
    set({ user });
  },
  setToken: (token, useSession = false) => {
    const user = get().user;
    if (useSession && token) {
      sessionStorage.setItem('token', token);
    } else {
      saveAuthToStorage(user, token);
    }
    set({ token, isLoggedIn: Boolean(token) });
  },
  setAuth: (user, token, useSession = false) => {
    if (useSession && token) {
      sessionStorage.setItem('token', token);
      if (user) sessionStorage.setItem('user', JSON.stringify(user));
    } else {
      saveAuthToStorage(user, token);
    }
    set({ user, token, isLoggedIn: Boolean(token) });
  },
  logout: () => {
    saveAuthToStorage(null, null);
    set({ user: null, token: null, isLoggedIn: false });
  },
}));

export default useAuthStore;
