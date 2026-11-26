import { create } from 'zustand';
import type { AllSettings } from '../types';
import i18n from '../i18n';
import request from '../utils/request';
import { getUserLanguagePreference, normalizeLanguage } from '../utils/language';
import { useThemeStore } from './theme';

interface SettingsState {
  settings: AllSettings | null;
  fetchSettings: (force?: boolean) => Promise<void>;
  updateStoreSettings: (settings: AllSettings) => void;
}

const applySiteSettings = (site?: AllSettings['site']) => {
  if (!site) return;
  if (site.title) {
    document.title = site.title;
    localStorage.setItem('laiai_site_title', site.title);
  }
  if (site.admin_path) {
    localStorage.setItem('laiai_admin_path', site.admin_path);
  }
  const themeStore = useThemeStore.getState();
  const storedTheme = localStorage.getItem('laiai-theme-storage');
  if (!storedTheme && site.default_theme) {
    themeStore.setTheme(site.default_theme);
  }
  const userLang = getUserLanguagePreference();
  const targetRaw = userLang || site.default_language;
  if (targetRaw) {
    const target = normalizeLanguage(targetRaw);
    if (normalizeLanguage(i18n.language) !== target) {
      void i18n.changeLanguage(target);
    }
  }
};

const cachedTitle = localStorage.getItem('laiai_site_title');
if (cachedTitle) document.title = cachedTitle;

let _fetchingPromise: Promise<void> | null = null;

const useSettingsStore = create<SettingsState>((set, get) => ({
  settings: null,
  fetchSettings: async (force = false) => {
    if (!force && get().settings) return;
    if (_fetchingPromise) return _fetchingPromise;
    _fetchingPromise = (async () => {
      try {
        const response = (await request.get('/settings')) as AllSettings;
        applySiteSettings(response?.site);
        set({ settings: response });
      } catch (error) {
        console.error('Failed to fetch settings:', error);
      } finally {
        _fetchingPromise = null;
      }
    })();
    return _fetchingPromise;
  },
  updateStoreSettings: (settings) => {
    applySiteSettings(settings.site);
    set({ settings });
  },
}));

export default useSettingsStore;
