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
    const rawSession = sessionStorage.getItem(AUTH_STORAGE_KEY);
    const rawLocal = localStorage.getItem(AUTH_STORAGE_KEY);
    const raw = rawSession || rawLocal;
    if (!raw) return { user: null, token: null };
    const parsed = JSON.parse(raw) as { user?: User | null; token?: string | null };
    return {
      user: parsed.user ?? null,
      token: parsed.token ?? null,
    };
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
      return;
    }
    const payload = JSON.stringify({ user, token });
    sessionStorage.setItem(AUTH_STORAGE_KEY, payload);
    localStorage.setItem(AUTH_STORAGE_KEY, payload);
  } catch {
    // ignore storage quota errors
  }
}

interface AuthState {
  user: User | null;
  token: string | null;
  isLoggedIn: boolean;
  setUser: (user: User | null) => void;
  setToken: (token: string | null) => void;
  setAuth: (user: User | null, token: string | null) => void;
  logout: () => void;
}

const initial = getAuthFromStorage();

export const useAuthStore = create<AuthState>((set, get) => ({
  user: initial.user,
  token: initial.token,
  isLoggedIn: Boolean(initial.token),
  setUser: (user) => {
    const token = get().token;
    saveAuthToStorage(user, token);
    set({ user });
  },
  setToken: (token) => {
    const user = get().user;
    saveAuthToStorage(user, token);
    set({ token, isLoggedIn: Boolean(token) });
  },
  setAuth: (user, token) => {
    saveAuthToStorage(user, token);
    set({ user, token, isLoggedIn: Boolean(token) });
  },
  logout: () => {
    saveAuthToStorage(null, null);
    set({ user: null, token: null, isLoggedIn: false });
  },
}));

export default useAuthStore;
