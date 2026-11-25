import React, { useLayoutEffect, useMemo, useRef, useState } from 'react';
import { Image as ImageIcon, MessageSquare, Wrench, LayoutGrid, Cuboid, ListOrdered, Mic } from 'lucide-react';

type LogTypeTabOption = {
  value: string;
  label: string;
  icon: React.ReactNode;
};

export function buildLogTypeTabOptions(t: any): LogTypeTabOption[] {
  return [
    { value: '视觉', label: t('logs.type_vision', '视觉'), icon: <ImageIcon size={14} /> },
    { value: '聊天', label: t('logs.type_chat', '聊天'), icon: <MessageSquare size={14} /> },
    { value: '音频', label: t('logs.type_audio', '音频'), icon: <Mic size={14} /> },
    { value: '向量', label: t('logs.type_embedding', '向量'), icon: <Cuboid size={14} /> },
    { value: '排序', label: t('logs.type_rerank', '排序'), icon: <ListOrdered size={14} /> },
    { value: '其它', label: t('logs.type_other', '其它'), icon: <Wrench size={14} /> },
    { value: '全部', label: t('logs.type_all', '全部'), icon: <LayoutGrid size={14} /> },
  ];
}

function orderWithDefaultFirst(options: LogTypeTabOption[], defaultFirst: string): LogTypeTabOption[] {
  const idx = options.findIndex((o) => o.value === defaultFirst);
  if (idx <= 0) return options;
  return [options[idx], ...options.slice(0, idx), ...options.slice(idx + 1)];
}

type LogTypeTabsProps = {
  value: string;
  onChange: (value: string) => void;
  options: LogTypeTabOption[];
  defaultFirst: string;
  isLight: boolean;
  themeToken: { colorFillAlter?: string; colorTextSecondary?: string };
};

export function LogTypeTabs({ value, onChange, options, defaultFirst, isLight, themeToken }: LogTypeTabsProps) {
  const ordered = useMemo(() => orderWithDefaultFirst(options, defaultFirst), [options, defaultFirst]);
  const containerRef = useRef<HTMLDivElement>(null);
  const btnRefs = useRef<(HTMLDivElement | null)[]>([]);
  const measuredOnceRef = useRef(false);
  const [indicator, setIndicator] = useState({ x: 0, width: 0, animate: false });

  useLayoutEffect(() => {
    const update = () => {
      const idx = ordered.findIndex((o) => o.value === value);
      const el = btnRefs.current[idx];
      if (!el) return;
      const animate = measuredOnceRef.current;
      measuredOnceRef.current = true;
      setIndicator({ x: el.offsetLeft, width: el.offsetWidth, animate });
    };

    update();
    const parent = containerRef.current;
    if (!parent || typeof ResizeObserver === 'undefined') return undefined;
    const ro = new ResizeObserver(update);
    ro.observe(parent);
    return () => ro.disconnect();
  }, [value, ordered]);

  return (
    <div
      ref={containerRef}
      style={{
        position: 'relative',
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        borderRadius: '8px',
        background: themeToken?.colorFillAlter || (isLight ? '#fafafa' : '#1d1d1d'),
        padding: '4px',
        height: '32px',
      }}
    >
      <div
        aria-hidden
        style={{
          position: 'absolute',
          top: 4,
          left: 0,
          height: 'calc(100% - 8px)',
          width: indicator.width,
          borderRadius: 6,
          background: 'rgb(72, 72, 72)',
          boxShadow: '0 1px 2px 0 rgba(0, 0, 0, 0.05)',
          transform: `translateX(${indicator.x}px)`,
          transition: indicator.animate
            ? 'transform 0.28s cubic-bezier(0.4, 0, 0.2, 1), width 0.28s cubic-bezier(0.4, 0, 0.2, 1)'
            : 'none',
          pointerEvents: 'none',
          zIndex: 0,
        }}
      />
      {ordered.map((opt, i) => {
        const isActive = value === opt.value;
        return (
          <div
            key={opt.value}
            ref={(node) => { btnRefs.current[i] = node; }}
            onClick={() => onChange(opt.value)}
            style={{
              position: 'relative',
              zIndex: 1,
              display: 'inline-flex',
              alignItems: 'center',
              justifyContent: 'center',
              whiteSpace: 'nowrap',
              borderRadius: '6px',
              padding: '0 16px',
              height: '100%',
              fontSize: '14px',
              fontWeight: 500,
              transition: 'color 0.2s',
              cursor: 'pointer',
              background: 'transparent',
              color: isActive ? '#fff' : (themeToken?.colorTextSecondary || (isLight ? '#71717a' : '#a1a1aa')),
              gap: '6px',
            }}
          >
            {opt.icon}
            {opt.label}
          </div>
        );
      })}
    </div>
  );
}
