import React, { useEffect, useState } from 'react';

const ICON_VERSIONS_STORAGE_KEY = 'laiai_lobe_icon_versions';

let lobeIconVersions: Record<string, string> = (() => {
  try {
    const raw = localStorage.getItem(ICON_VERSIONS_STORAGE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw);
      if (parsed && typeof parsed === 'object') return parsed;
    }
  } catch {
    // ignore storage errors
  }
  return {};
})();

const versionListeners = new Set<() => void>();

export const setLobeIconVersions = (versions: Record<string, string>, replace = false) => {
  if (!versions || typeof versions !== 'object') return;
  const next: Record<string, string> = replace ? {} : { ...lobeIconVersions };
  let changed = replace;
  for (const [k, v] of Object.entries(versions)) {
    if (!k) continue;
    const val = String(v || '');
    if (next[k] !== val) {
      next[k] = val;
      changed = true;
    }
  }
  if (!changed) return;
  lobeIconVersions = next;
  try {
    localStorage.setItem(ICON_VERSIONS_STORAGE_KEY, JSON.stringify(lobeIconVersions));
  } catch {
    // ignore storage errors
  }
  versionListeners.forEach((fn) => fn());
};

export const withLobeIconVersion = (src: string): string => {
  if (!src || !src.startsWith('/assets/icons/')) return src;
  if (src.includes('v=')) return src;
  const file = src.split('/').pop() || '';
  const name = decodeURIComponent(file.replace(/\.svg(?:\?.*)?$/i, ''));
  const version = lobeIconVersions[name] || lobeIconVersions[name.toLowerCase()];
  if (!version) return src;
  return `${src}${src.includes('?') ? '&' : '?'}v=${encodeURIComponent(version)}`;
};

interface SmartSvgIconProps {
  src: string;
  alt?: string;
  style?: React.CSSProperties;
  className?: string;
  onError?: (e: React.SyntheticEvent<HTMLImageElement, Event>) => void;
}

/** 原样显示图标文件，与站点图标库预览一致，不按主题反色。 */
const SmartSvgIcon: React.FC<SmartSvgIconProps> = ({ src, alt = "", style = {}, className, onError }) => {
  const [, setTick] = useState(0);

  useEffect(() => {
    const onUpdate = () => setTick((t) => t + 1);
    versionListeners.add(onUpdate);
    return () => {
      versionListeners.delete(onUpdate);
    };
  }, []);

  return (
    <img
      src={withLobeIconVersion(src)}
      alt={alt}
      style={style}
      className={className}
      onError={onError}
    />
  );
};

export default SmartSvgIcon;
