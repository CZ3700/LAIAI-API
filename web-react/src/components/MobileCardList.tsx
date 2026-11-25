import React from 'react';
import { Spin, Empty, Typography } from 'antd';
import type { PaginationProps } from 'antd';
import { useThemeStore } from '../store/theme';
import ListPagination, { useListPager } from './ListPagination';

const { Text } = Typography;

interface MobileCardListProps<T> {
  dataSource: T[];
  loading?: boolean;
  renderCard: (item: T, index: number) => React.ReactNode;
  rowKey: string | ((item: T) => string | number);
  pagination?: false | PaginationProps;
  compact?: boolean;
  gap?: number;
}

function MobileCardList<T extends Record<string, any>>({ 
  dataSource, 
  loading, 
  renderCard, 
  rowKey,
  pagination,
  compact,
  gap
}: MobileCardListProps<T>) {
  const pager = pagination === false ? undefined : pagination;
  const controlled = pager?.current != null;
  const local = useListPager(pager?.pageSize);
  const current = controlled ? pager.current! : local.page;
  const size = controlled ? (pager.pageSize ?? local.pageSize) : local.pageSize;

  const getKey = (item: T, index: number): string | number => {
    if (typeof rowKey === 'function') return rowKey(item);
    return item[rowKey] ?? index;
  };

  React.useEffect(() => {
    if (controlled) return;
    const maxPage = Math.max(1, Math.ceil((dataSource?.length || 0) / size));
    if (local.page > maxPage) local.setPage(1);
  }, [controlled, dataSource?.length, size, local.page, local.setPage]);

  // pagination !== false 即启用；无 current 时本地切片
  const displayData = pagination !== false && !controlled
    ? dataSource.slice((current - 1) * size, current * size)
    : dataSource;

  const handlePageChange = (page: number, pageSize: number) => {
    if (!controlled) local.onChange(page, pageSize);
    pager?.onChange?.(page, pageSize);
  };

  if (loading) {
    return (
      <div style={{ textAlign: 'center', padding: '48px 0' }}>
        <Spin size="large" />
      </div>
    );
  }

  if (!dataSource || dataSource.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无数据" />;
  }

  return (
    <div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: gap !== undefined ? gap : (compact ? 4 : 6) }}>
        {displayData.map((item, index) => (
          <div key={getKey(item, index)}>
            {renderCard(item, index)}
          </div>
        ))}
      </div>
      {pagination !== false && dataSource.length > 0 && (
        <div style={{ textAlign: 'center', marginTop: 16 }}>
          <ListPagination
            size="small"
            {...pager}
            current={current}
            pageSize={size}
            total={pager?.total || dataSource.length}
            onChange={handlePageChange}
          />
        </div>
      )}
    </div>
  );
}

// ---- Reusable building blocks for card content ----

export const CardRow: React.FC<{ label: string; children: React.ReactNode; style?: React.CSSProperties; compact?: boolean }> = ({ label, children, style, compact }) => (
  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: compact ? '1px 0' : '2px 0', ...style }}>
    <Text type="secondary" style={{ fontSize: compact ? 11 : 13, flexShrink: 0, marginRight: 8 }}>{label}</Text>
    <div style={{ textAlign: 'right', fontSize: compact ? 11 : 13 }}>{children}</div>
  </div>
);

export const CardActions: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const { themeMode } = useThemeStore();
  const isLight = themeMode === 'light';
  return (
    <div style={{ 
      borderTop: isLight ? '1px solid #e8e8e8' : '1px solid #303030', 
      marginTop: 8, 
      paddingTop: 8, 
      display: 'flex', 
      justifyContent: 'flex-end', 
      gap: 8 
    }}>
      {children}
    </div>
  );
};

export const MobileCard: React.FC<{ 
  title: React.ReactNode; 
  extra?: React.ReactNode; 
  children: React.ReactNode;
  compact?: boolean;
  style?: React.CSSProperties;
}> = ({ title, extra, children, compact, style }) => {
  const { themeMode } = useThemeStore();
  const isLight = themeMode === 'light';
  return (
    <div style={{
      background: isLight ? '#fff' : '#1d1d1d',
      border: isLight ? '1px solid #d4d4d8' : '1px solid rgba(255, 255, 255, 0.1)',
      borderRadius: compact ? 8 : 12,
      padding: compact ? '5px 8px' : '10px 12px',
      ...style
    }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: compact ? 4 : 6, gap: 8 }}>
        <div style={{ fontWeight: 600, fontSize: compact ? 13 : 14, flex: 1, minWidth: 0 }}>{title}</div>
        {extra && <div style={{ flexShrink: 0 }}>{extra}</div>}
      </div>
      {children}
    </div>
  );
};

export default MobileCardList;
