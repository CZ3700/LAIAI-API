import React, { useEffect } from 'react';
import { ConfigProvider, theme, App } from 'antd';
import { useThemeStore } from '../store/theme';
import zhCN from 'antd/locale/zh_CN';
import zhTW from 'antd/locale/zh_TW';
import enUS from 'antd/locale/en_US';
import jaJP from 'antd/locale/ja_JP';
import koKR from 'antd/locale/ko_KR';
import viVN from 'antd/locale/vi_VN';
import thTH from 'antd/locale/th_TH';
import msMY from 'antd/locale/ms_MY';
import frFR from 'antd/locale/fr_FR';
import deDE from 'antd/locale/de_DE';
import esES from 'antd/locale/es_ES';
import ptBR from 'antd/locale/pt_BR';
import ruRU from 'antd/locale/ru_RU';
import arEG from 'antd/locale/ar_EG';
import { useTranslation } from 'react-i18next';
import dayjs from 'dayjs';
import 'dayjs/locale/zh-cn';
import 'dayjs/locale/zh-tw';
import 'dayjs/locale/en';
import 'dayjs/locale/ja';
import 'dayjs/locale/ko';
import 'dayjs/locale/vi';
import 'dayjs/locale/th';
import 'dayjs/locale/ms';
import 'dayjs/locale/fr';
import 'dayjs/locale/de';
import 'dayjs/locale/es';
import 'dayjs/locale/pt';
import 'dayjs/locale/ru';
import 'dayjs/locale/ar';
import { AppMessageBridge } from './AppMessageBridge';
import { getAntdComponentTokens, getAntdThemeTokens } from '../theme/tokens';
import { resolveContentLang } from '../utils/language';

export const AppThemeProvider = ({ children }: { children: React.ReactNode }) => {
  const { themeMode, themePreference, setThemePreference } = useThemeStore();
  const { i18n } = useTranslation();

  useEffect(() => {
    // 设置原生 DOM 的 data-theme，用于 CSS 变量切换
    document.body.setAttribute('data-theme', themeMode);
    document.documentElement.setAttribute('data-theme', themeMode);
  }, [themeMode]);

  // Synchronize dayjs locale with react-i18next language
  useEffect(() => {
    const lang = i18n.language || 'zh';
    const contentLang = resolveContentLang(lang);
    if (contentLang === 'zh-TW') {
      dayjs.locale('zh-tw');
    } else if (contentLang === 'zh' || lang.startsWith('zh')) {
      dayjs.locale('zh-cn');
    } else if (lang.startsWith('ja')) {
      dayjs.locale('ja');
    } else if (lang.startsWith('ko')) {
      dayjs.locale('ko');
    } else if (lang.startsWith('vi')) {
      dayjs.locale('vi');
    } else if (lang.startsWith('th')) {
      dayjs.locale('th');
    } else if (lang.startsWith('ms')) {
      dayjs.locale('ms');
    } else if (lang.startsWith('fr')) {
      dayjs.locale('fr');
    } else if (lang.startsWith('de')) {
      dayjs.locale('de');
    } else if (lang.startsWith('es')) {
      dayjs.locale('es');
    } else if (lang.startsWith('pt')) {
      dayjs.locale('pt');
    } else if (lang.startsWith('ru')) {
      dayjs.locale('ru');
    } else if (lang.startsWith('ar')) {
      dayjs.locale('ar');
    } else {
      dayjs.locale('en');
    }
  }, [i18n.language]);

  useEffect(() => {
    if (themePreference !== 'system') return;

    const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
    const handleChange = () => {
      setThemePreference('system');
    };

    mediaQuery.addEventListener('change', handleChange);
    return () => mediaQuery.removeEventListener('change', handleChange);
  }, [themePreference, setThemePreference]);

  const mode = themeMode === 'light' ? 'light' : 'dark';
  const token = getAntdThemeTokens(mode);
  const components = getAntdComponentTokens(mode);

  const getAntdLocale = (lang: string) => {
    const contentLang = resolveContentLang(lang);
    if (contentLang === 'zh-TW') return zhTW;
    if (contentLang === 'zh' || lang.startsWith('zh')) return zhCN;
    if (lang.startsWith('en')) return enUS;
    if (lang.startsWith('ja')) return jaJP;
    if (lang.startsWith('ko')) return koKR;
    if (lang.startsWith('vi')) return viVN;
    if (lang.startsWith('th')) return thTH;
    if (lang.startsWith('ms')) return msMY;
    if (lang.startsWith('fr')) return frFR;
    if (lang.startsWith('de')) return deDE;
    if (lang.startsWith('es')) return esES;
    if (lang.startsWith('pt')) return ptBR;
    if (lang.startsWith('ru')) return ruRU;
    if (lang.startsWith('ar')) return arEG;
    return zhCN;
  };

  const locale = getAntdLocale(i18n.language || 'zh');

  return (
    <ConfigProvider
      locale={locale}
      theme={{
        algorithm: mode === 'dark' ? theme.darkAlgorithm : theme.defaultAlgorithm,
        token,
        components,
      }}
    >
      <App>
        <AppMessageBridge>
          {children}
        </AppMessageBridge>
      </App>
    </ConfigProvider>
  );
};
