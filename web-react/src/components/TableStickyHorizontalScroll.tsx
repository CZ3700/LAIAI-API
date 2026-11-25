import React, { useCallback, useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';

interface TableStickyHorizontalScrollProps {
  isLight: boolean;
  children: React.ReactNode;
}

export const TableStickyHorizontalScroll: React.FC<TableStickyHorizontalScrollProps> = ({
  isLight,
  children,
}) => {
  const wrapperRef = useRef<HTMLDivElement>(null);
  const contentElRef = useRef<HTMLElement | null>(null);
  const trackRef = useRef<HTMLDivElement>(null);
  const [portalTarget, setPortalTarget] = useState<HTMLElement | null>(null);
  const [metrics, setMetrics] = useState({
    clientWidth: 0,
    scrollWidth: 0,
    scrollLeft: 0,
  });
  const [isDragging, setIsDragging] = useState(false);
  const [isHoveringThumb, setIsHoveringThumb] = useState(false);
  const dragStateRef = useRef<{
    startX: number;
    startThumbLeft: number;
    maxThumbLeft: number;
    maxScrollLeft: number;
  } | null>(null);

  const syncMetrics = useCallback(() => {
    const el = contentElRef.current;
    if (!el) return;
    const nextClientWidth = el.clientWidth;
    const nextScrollWidth = el.scrollWidth;
    const nextScrollLeft = el.scrollLeft;
    setMetrics((prev) => {
      if (
        prev.clientWidth === nextClientWidth &&
        prev.scrollWidth === nextScrollWidth &&
        Math.abs(prev.scrollLeft - nextScrollLeft) < 0.5
      ) {
        return prev;
      }
      return {
        clientWidth: nextClientWidth,
        scrollWidth: nextScrollWidth,
        scrollLeft: nextScrollLeft,
      };
    });
  }, []);

  useEffect(() => {
    const wrapper = wrapperRef.current;
    if (!wrapper) return;

    let currentContentEl: HTMLElement | null = null;
    let currentTableEl: HTMLElement | null = null;
    let resizeObserver: ResizeObserver | null = null;

    const bindElements = () => {
      const antTable = wrapper.querySelector('.ant-table') as HTMLElement | null;
      const contentEl = wrapper.querySelector(
        '.ant-table-content, .ant-table-body'
      ) as HTMLElement | null;
      const innerTable = contentEl?.querySelector('table') as HTMLElement | null;

      setPortalTarget((prev) => (prev === antTable ? prev : antTable));

      if (contentEl !== currentContentEl || innerTable !== currentTableEl) {
        if (currentContentEl) {
          currentContentEl.removeEventListener('scroll', syncMetrics);
        }
        resizeObserver?.disconnect();

        currentContentEl = contentEl;
        currentTableEl = innerTable;
        contentElRef.current = contentEl;

        if (contentEl) {
          contentEl.addEventListener('scroll', syncMetrics, { passive: true });
          resizeObserver = new ResizeObserver(() => {
            syncMetrics();
          });
          resizeObserver.observe(contentEl);
          if (innerTable) {
            resizeObserver.observe(innerTable);
          }
        }
      }
      syncMetrics();
    };

    bindElements();

    const mutationObserver = new MutationObserver(() => {
      bindElements();
    });
    mutationObserver.observe(wrapper, { childList: true, subtree: true });
    window.addEventListener('resize', syncMetrics);

    return () => {
      mutationObserver.disconnect();
      resizeObserver?.disconnect();
      window.removeEventListener('resize', syncMetrics);
      if (currentContentEl) {
        currentContentEl.removeEventListener('scroll', syncMetrics);
      }
    };
  }, [syncMetrics]);

  const { clientWidth, scrollWidth, scrollLeft } = metrics;
  const hasHorizontalOverflow = scrollWidth > clientWidth + 1 && clientWidth > 0;
  const trackPadding = 12; // 6px left + 6px right
  const usableTrackWidth = Math.max(0, clientWidth - trackPadding);
  const thumbWidth = hasHorizontalOverflow
    ? Math.max(48, Math.round((clientWidth / scrollWidth) * usableTrackWidth))
    : 0;
  const maxScrollLeft = Math.max(0, scrollWidth - clientWidth);
  const maxThumbLeft = Math.max(0, usableTrackWidth - thumbWidth);
  const thumbLeft =
    maxScrollLeft > 0 ? (scrollLeft / maxScrollLeft) * maxThumbLeft : 0;

  useEffect(() => {
    if (!isDragging) return;

    const handleMouseMove = (e: MouseEvent) => {
      const st = dragStateRef.current;
      const el = contentElRef.current;
      if (!st || !el) return;
      const deltaX = e.clientX - st.startX;
      const nextThumbLeft = Math.max(
        0,
        Math.min(st.maxThumbLeft, st.startThumbLeft + deltaX)
      );
      if (st.maxThumbLeft > 0) {
        el.scrollLeft = (nextThumbLeft / st.maxThumbLeft) * st.maxScrollLeft;
      }
    };

    const handleMouseUp = () => {
      setIsDragging(false);
      dragStateRef.current = null;
    };

    window.addEventListener('mousemove', handleMouseMove);
    window.addEventListener('mouseup', handleMouseUp);
    return () => {
      window.removeEventListener('mousemove', handleMouseMove);
      window.removeEventListener('mouseup', handleMouseUp);
    };
  }, [isDragging]);

  const handleThumbMouseDown = (e: React.MouseEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.stopPropagation();
    dragStateRef.current = {
      startX: e.clientX,
      startThumbLeft: thumbLeft,
      maxThumbLeft,
      maxScrollLeft,
    };
    setIsDragging(true);
  };

  const handleTrackMouseDown = (e: React.MouseEvent<HTMLDivElement>) => {
    if (e.target !== trackRef.current) return;
    const el = contentElRef.current;
    const track = trackRef.current;
    if (!el || !track || maxThumbLeft <= 0) return;
    const rect = track.getBoundingClientRect();
    const clickX = e.clientX - rect.left - 6;
    const targetThumbLeft = Math.max(
      0,
      Math.min(maxThumbLeft, clickX - thumbWidth / 2)
    );
    el.scrollLeft = (targetThumbLeft / maxThumbLeft) * maxScrollLeft;
  };

  const scrollbarNode =
    portalTarget && hasHorizontalOverflow
      ? createPortal(
          <div
            ref={trackRef}
            onMouseDown={handleTrackMouseDown}
            style={{
              width: '100%',
              height: 14,
              padding: '3px 6px',
              boxSizing: 'border-box',
              background: isLight
                ? 'rgba(249, 250, 251, 0.96)'
                : 'rgba(24, 24, 27, 0.96)',
              borderTop: `1px solid ${
                isLight ? 'rgba(0, 0, 0, 0.06)' : 'rgba(255, 255, 255, 0.08)'
              }`,
              borderBottom: `1px solid ${
                isLight ? 'rgba(0, 0, 0, 0.06)' : 'rgba(255, 255, 255, 0.08)'
              }`,
              display: 'flex',
              alignItems: 'center',
              cursor: 'pointer',
              userSelect: 'none',
            }}
          >
            <div
              onMouseDown={handleThumbMouseDown}
              onMouseEnter={() => setIsHoveringThumb(true)}
              onMouseLeave={() => setIsHoveringThumb(false)}
              style={{
                width: thumbWidth,
                height: 8,
                borderRadius: 4,
                transform: `translate3d(${thumbLeft}px, 0, 0)`,
                background:
                  isDragging || isHoveringThumb
                    ? isLight
                      ? 'rgba(0, 0, 0, 0.48)'
                      : 'rgba(255, 255, 255, 0.55)'
                    : isLight
                      ? 'rgba(0, 0, 0, 0.28)'
                      : 'rgba(255, 255, 255, 0.35)',
                cursor: isDragging ? 'grabbing' : 'grab',
                transition: isDragging ? 'none' : 'background 0.15s ease',
              }}
            />
          </div>,
          portalTarget
        )
      : null;

  return (
    <div ref={wrapperRef} style={{ width: '100%' }}>
      {children}
      {scrollbarNode}
    </div>
  );
};
