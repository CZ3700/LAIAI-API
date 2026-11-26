import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import LanguageDetector from 'i18next-browser-languagedetector';
import { getUserLanguagePreference, normalizeLanguage } from './utils/language';

import enTranslation from './locales/en.json';
import zhTranslation from './locales/zh.json';
import zhTWTranslation from './locales/zh-TW.json';

export const resources: Record<string, Record<string, Record<string, any>>> = {
  en: { translation: { ...enTranslation } },
  zh: { translation: { ...zhTranslation } },
  zhTW: { translation: { ...zhTWTranslation } },
  'zh-TW': { translation: { ...zhTWTranslation } },
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
  const targetLang = normalizeLanguage(rawLang);
  if (!resources[targetLang]) continue;
  const content = (mod && typeof mod === 'object' && 'default' in mod ? mod.default : mod) as Record<string, any>;
  if (content && typeof content === 'object') {
    const ns = PLUGIN_NS_MAP[pluginDir] || pluginDir.toLowerCase();
    const targets = targetLang === 'zhTW' ? ['zhTW', 'zh-TW'] : [targetLang];
    for (const lng of targets) {
      resources[lng][ns] = {
        ...(resources[lng][ns] || {}),
        ...content,
      };
      resources[lng].translation = {
        ...resources[lng].translation,
        ...content,
      };
    }
  }
}

const initialUserLang = getUserLanguagePreference();

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources,
    lng: initialUserLang ? normalizeLanguage(initialUserLang) : undefined,
    fallbackLng: 'zh',
    supportedLngs: ['zh', 'zhTW', 'zh-TW', 'en'],
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
