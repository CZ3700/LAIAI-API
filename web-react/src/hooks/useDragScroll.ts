import { useRef, useEffect } from 'react';

interface UseDragScrollOptions {
  /**
   * 拖拽灵敏度倍率，默认 1.25
   */
  speed?: number;
  /**
   * 是否启用，默认 true
   */
  enabled?: boolean;
}

/**
 * 通用鼠标按住拖拽左右滑动 Hook
 * 保持鼠标原本样式，仅在按住鼠标左键横向拖拽时平滑滑动表格
 */
export function useDragScroll<T extends HTMLElement = HTMLDivElement>(options: UseDragScrollOptions = {}) {
  const { speed = 1.25, enabled = true } = options;
  const containerRef = useRef<T | null>(null);

  useEffect(() => {
    if (!enabled) return;
    const container = containerRef.current;
    if (!container) return;

    let isDown = false;
    let startX = 0;
    let scrollLeft = 0;
    let hasMoved = false;

    // 获取实际产生横向滚动的子元素（兼容 Ant Design Table 与普通容器）
    const getScrollEl = (): HTMLElement => {
      const antdContent = container.querySelector('.ant-table-content') as HTMLElement | null;
      const antdBody = container.querySelector('.ant-table-body') as HTMLElement | null;
      if (antdContent && antdContent.scrollWidth > antdContent.clientWidth) return antdContent;
      if (antdBody && antdBody.scrollWidth > antdBody.clientWidth) return antdBody;
      if (container.scrollWidth > container.clientWidth) return container;
      return antdContent || antdBody || container;
    };

    const onMouseDown = (e: MouseEvent) => {
      // 忽略鼠标右键/中键
      if (e.button !== 0) return;

      const target = e.target as HTMLElement | null;
      // 忽略在输入框、按钮、代码展示区、展开行详情、下拉菜单、Modal 等内部的操作，保证原生文本划选与交互
      if (target?.closest('input, textarea, select, pre, code, button, a, .ant-table-expanded-row, .ant-pagination, .ant-dropdown, .ant-modal, .ant-select')) {
        return;
      }

      const scrollEl = getScrollEl();
      if (!scrollEl || scrollEl.scrollWidth <= scrollEl.clientWidth + 2) {
        return;
      }

      isDown = true;
      hasMoved = false;
      startX = e.pageX - scrollEl.getBoundingClientRect().left;
      scrollLeft = scrollEl.scrollLeft;
    };

    const onMouseMove = (e: MouseEvent) => {
      if (!isDown) return;
      const scrollEl = getScrollEl();
      if (!scrollEl) return;

      const currentX = e.pageX - scrollEl.getBoundingClientRect().left;
      const walk = (currentX - startX) * speed;

      // 移动超过 4px 判定为真实横向拖拽滑动行为
      if (!hasMoved && Math.abs(currentX - startX) > 4) {
        hasMoved = true;
        // 仅在真实拖拽位移时临时禁用文字选中，避免拉动表格时误选全屏文本
        document.body.style.userSelect = 'none';
      }

      if (hasMoved) {
        scrollEl.scrollLeft = scrollLeft - walk;
      }
    };

    const onMouseUp = () => {
      if (!isDown) return;
      isDown = false;
      document.body.style.removeProperty('user-select');

      if (hasMoved) {
        hasMoved = false;
        // 如果产生了明显的拖拽移动，拦截当次触发的 click 事件以防误点按钮/链接
        const preventClick = (e: MouseEvent) => {
          e.stopPropagation();
          e.preventDefault();
          window.removeEventListener('click', preventClick, true);
        };
        window.addEventListener('click', preventClick, true);
        setTimeout(() => {
          window.removeEventListener('click', preventClick, true);
        }, 80);
      }
    };

    container.addEventListener('mousedown', onMouseDown);
    window.addEventListener('mousemove', onMouseMove);
    window.addEventListener('mouseup', onMouseUp);

    return () => {
      container.removeEventListener('mousedown', onMouseDown);
      window.removeEventListener('mousemove', onMouseMove);
      window.removeEventListener('mouseup', onMouseUp);
      document.body.style.removeProperty('user-select');
    };
  }, [enabled, speed]);

  return containerRef;
}
