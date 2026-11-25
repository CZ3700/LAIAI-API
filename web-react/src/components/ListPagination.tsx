import React, { useCallback, useState } from 'react';
import { Pagination } from 'antd';
import type { PaginationProps } from 'antd';
import i18n from '../i18n';

const LIST_DEFAULT_PAGE_SIZE = 20;

/** Table / List 共用分页配置（改条数、跳页、共 N 条） */
export function listPagination(overrides?: PaginationProps): PaginationProps {
  return {
    showSizeChanger: true,
    showQuickJumper: true,
    pageSize: LIST_DEFAULT_PAGE_SIZE,
    pageSizeOptions: ['10', '20', '50', '100'],
    showTotal: (total) => i18n.t('common.total_records', { total }),
    ...overrides,
  };
}

/** 页码 / 每页条数；`onChange` 可直接交给 `listPagination` / `ListPagination` */
export function useListPager(initialPageSize?: number) {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(initialPageSize ?? LIST_DEFAULT_PAGE_SIZE);
  const onChange = useCallback((p: number, s: number) => {
    setPage(p);
    setPageSize(s);
  }, []);
  return { page, pageSize, setPage, setPageSize, onChange };
}

/** 卡片网格等非 Table 场景 */
const ListPagination: React.FC<PaginationProps> = (props) => (
  <Pagination {...listPagination(props)} />
);

export default ListPagination;
