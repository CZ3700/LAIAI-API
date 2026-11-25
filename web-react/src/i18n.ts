import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import LanguageDetector from 'i18next-browser-languagedetector';
import { normalizeLanguage } from './utils/language';

import enTranslation from './locales/en.json';
import zhTranslation from './locales/zh.json';
import zhTWTranslation from './locales/zh-TW.json';

export const resources: Record<string, Record<string, Record<string, any>>> = {
  en: { translation: { ...enTranslation } },
  zh: { translation: { ...zhTranslation } },
  zhTW: { translation: { ...zhTWTranslation } },
};

const PLUGIN_NS_MAP: Record<string, string> = {
  DocsApi: 'docs_api',
  ModelMarketplace: 'model_marketplace',
};

const pluginLocaleModules = import.meta.glob('./pages/Plugins/*/locales/*.json', {
  eager: true,
}) as Record<string, { default?: Record<string, any> } | Record<string, any>>;

for (const [path, mod] of Object.entries(pluginLocaleModules)) {
  const match = path.match(/\.\/pages\/Plugins\/([^/]+)\/locales\/([^/]+)\.json$/);
  if (!match) continue;
  const pluginDir = match[1];
  const rawLang = match[2];
  const targetLang = rawLang === 'zh-TW' ? 'zhTW' : normalizeLanguage(rawLang);
  if (!resources[targetLang]) continue;
  const content = (mod && typeof mod === 'object' && 'default' in mod ? mod.default : mod) as Record<string, any>;
  if (content && typeof content === 'object') {
    const ns = PLUGIN_NS_MAP[pluginDir] || pluginDir.toLowerCase();
    resources[targetLang][ns] = {
      ...(resources[targetLang][ns] || {}),
      ...content,
    };
    resources[targetLang].translation = {
      ...resources[targetLang].translation,
      ...content,
    };
  }
}

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources,
    fallbackLng: 'zh',
    supportedLngs: ['zh', 'zhTW', 'en'],
    ns: ['translation', 'docs_api', 'model_marketplace'],
    defaultNS: 'translation',
    interpolation: {
      escapeValue: false,
    },
    detection: {
      order: ['localStorage', 'navigator'],
      caches: ['localStorage'],
      lookupLocalStorage: 'i18nextLng',
      convertDetectedLanguage: (lng: string) => normalizeLanguage(lng),
    },
  });

export default i18n;
