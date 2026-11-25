/**
 * LAIAI 主题设计令牌：保留 Shadcn / Zinc 黑白灰极简质感，融入 Indigo-Slate 细微品牌强调色
 */
const PALETTE = {
  zinc50: '#fafafa',
  zinc100: '#f4f4f5',
  zinc200: '#e4e4e7',
  zinc300: '#d4d4d8',
  zinc400: '#a1a1aa',
  zinc500: '#71717a',
  zinc600: '#52525b',
  zinc700: '#3f3f46',
  zinc800: '#27272a',
  zinc900: '#18181b',
  zinc950: '#09090b',
  indigo500: '#4f46e5',
  indigo400: '#818cf8',
} as const;

export type ThemeMode = 'light' | 'dark';

export function getAntdThemeTokens(mode: ThemeMode) {
  if (mode === 'light') {
    return {
      colorPrimary: PALETTE.zinc900,
      colorPrimaryHover: PALETTE.zinc800,
      colorPrimaryActive: PALETTE.zinc950,
      colorPrimaryBg: 'rgba(79, 70, 229, 0.06)',
      colorPrimaryBgHover: 'rgba(79, 70, 229, 0.10)',
      colorPrimaryBorder: 'rgba(79, 70, 229, 0.22)',
      colorPrimaryBorderHover: 'rgba(79, 70, 229, 0.35)',
      colorLink: PALETTE.indigo500,
      colorInfo: PALETTE.indigo500,
      colorTextLightSolid: PALETTE.zinc50,
      borderRadius: 8,
      colorBgLayout: '#f8fafc',
      colorBgContainer: '#ffffff',
      colorBgElevated: '#ffffff',
      colorTextBase: PALETTE.zinc900,
      colorBorder: PALETTE.zinc300,
      colorBorderSecondary: PALETTE.zinc200,
      controlOutline: 'rgba(79, 70, 229, 0.14)',
      controlItemBgActive: 'rgba(79, 70, 229, 0.07)',
      controlItemBgActiveHover: 'rgba(79, 70, 229, 0.11)',
      controlItemBgHover: 'rgba(24, 24, 27, 0.04)',
    };
  }

  return {
    colorPrimary: PALETTE.zinc50,
    colorPrimaryHover: PALETTE.zinc200,
    colorPrimaryActive: '#ffffff',
    colorPrimaryBg: 'rgba(129, 140, 248, 0.10)',
    colorPrimaryBgHover: 'rgba(129, 140, 248, 0.15)',
    colorPrimaryBorder: 'rgba(129, 140, 248, 0.26)',
    colorPrimaryBorderHover: 'rgba(129, 140, 248, 0.38)',
    colorLink: PALETTE.indigo400,
    colorInfo: PALETTE.indigo400,
    colorTextLightSolid: '#ffffff',
    borderRadius: 8,
    colorBgLayout: PALETTE.zinc950,
    colorBgContainer: '#141416',
    colorBgElevated: '#1c1c20',
    controlOutline: 'rgba(129, 140, 248, 0.20)',
    controlItemBgActive: 'rgba(255, 255, 255, 0.12)',
    controlItemBgActiveHover: 'rgba(255, 255, 255, 0.16)',
    controlItemBgHover: 'rgba(255, 255, 255, 0.08)',
  };
}

export function getAntdComponentTokens(mode: ThemeMode) {
  const isLight = mode === 'light';
  const primary = isLight ? PALETTE.zinc900 : PALETTE.zinc50;
  const onPrimary = isLight ? PALETTE.zinc50 : PALETTE.zinc900;
  const darkMenuBg = '#141416';
  const darkMenuElevated = '#1c1c20';

  return {
    Layout: {
      siderBg: isLight ? '#fcfcfd' : darkMenuBg,
      headerBg: isLight ? '#ffffff' : darkMenuBg,
      bodyBg: isLight ? '#f8fafc' : PALETTE.zinc950,
    },
    Menu: {
      itemHeight: 44,
      iconSize: 18,
      itemMarginInline: 10,
      darkItemBg: darkMenuBg,
      darkPopupBg: darkMenuElevated,
      darkSubMenuItemBg: darkMenuBg,
      darkItemColor: 'rgba(255, 255, 255, 0.65)',
      darkItemHoverColor: '#ffffff',
      darkItemHoverBg: 'rgba(255, 255, 255, 0.08)',
      darkItemSelectedBg: 'rgba(129, 140, 248, 0.14)',
      darkItemSelectedColor: '#ffffff',
      darkGroupTitleColor: 'rgba(255, 255, 255, 0.45)',
      itemHoverBg: isLight ? 'rgba(0, 0, 0, 0.04)' : 'rgba(255, 255, 255, 0.08)',
      itemSelectedBg: isLight ? 'rgba(79, 70, 229, 0.08)' : 'rgba(129, 140, 248, 0.14)',
      itemActiveBg: isLight ? 'rgba(79, 70, 229, 0.12)' : 'rgba(129, 140, 248, 0.18)',
      itemSelectedColor: isLight ? PALETTE.indigo500 : '#ffffff',
      itemHoverColor: isLight ? PALETTE.zinc900 : '#ffffff',
    },
    Button: {
      primaryShadow: 'none',
      defaultShadow: 'none',
      primaryColor: onPrimary,
    },
    Tooltip: {
      colorTextLightSolid: '#ffffff',
    },
    Radio: {
      buttonSolidCheckedBg: primary,
      buttonSolidCheckedColor: onPrimary,
      buttonSolidCheckedHoverBg: isLight ? PALETTE.zinc800 : PALETTE.zinc200,
    },
    Card: {
      colorBorderSecondary: isLight ? PALETTE.zinc200 : '#2b2b30',
    },
    Select: {
      optionSelectedBg: isLight ? 'rgba(79, 70, 229, 0.07)' : 'rgba(255, 255, 255, 0.12)',
      optionSelectedColor: isLight ? PALETTE.zinc900 : '#ffffff',
      optionActiveBg: isLight ? 'rgba(24, 24, 27, 0.04)' : 'rgba(255, 255, 255, 0.08)',
    },
  };
}

export function getSiderMenuTokens(mode: ThemeMode) {
  const isLight = mode === 'light';
  const darkMenuBg = '#141416';
  const darkMenuElevated = '#1c1c20';
  return {
    itemHeight: 38,
    itemMarginInline: 8,
    itemMarginBlock: 2,
    itemHoverBg: isLight ? 'rgba(0, 0, 0, 0.04)' : 'rgba(255, 255, 255, 0.08)',
    itemSelectedBg: isLight ? 'rgba(79, 70, 229, 0.08)' : 'rgba(129, 140, 248, 0.16)',
    itemActiveBg: isLight ? 'rgba(79, 70, 229, 0.12)' : 'rgba(129, 140, 248, 0.22)',
    itemSelectedColor: isLight ? PALETTE.indigo500 : '#ffffff',
    itemHoverColor: isLight ? PALETTE.zinc900 : '#ffffff',
    itemColor: isLight ? PALETTE.zinc600 : 'rgba(255, 255, 255, 0.65)',
    darkItemBg: darkMenuBg,
    darkPopupBg: darkMenuElevated,
    darkSubMenuItemBg: darkMenuBg,
    darkItemSelectedBg: 'rgba(129, 140, 248, 0.16)',
    darkItemSelectedColor: '#ffffff',
    darkItemHoverBg: 'rgba(255, 255, 255, 0.08)',
    darkItemHoverColor: '#ffffff',
    darkItemColor: 'rgba(255, 255, 255, 0.65)',
    darkGroupTitleColor: 'rgba(255, 255, 255, 0.45)',
  };
}

export function softAccent(mode: ThemeMode) {
  if (mode === 'light') {
    return {
      background: 'rgba(79, 70, 229, 0.08)',
      color: PALETTE.indigo500,
      border: '1px solid rgba(79, 70, 229, 0.18)',
    };
  }
  return {
    background: 'rgba(129, 140, 248, 0.14)',
    color: '#c7d2fe',
    border: '1px solid rgba(129, 140, 248, 0.28)',
  };
}

export function solidAccent(mode: ThemeMode) {
  return mode === 'light'
    ? { background: PALETTE.zinc900, color: PALETTE.zinc50 }
    : { background: PALETTE.zinc200, color: PALETTE.zinc900 };
}
