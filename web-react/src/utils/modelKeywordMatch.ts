/** 管理端模型本地搜索字段（名称 / ID / MID / 别名 / 备注） */
export type ModelKeywordFields = {
  name?: string | null;
  model_id?: string | null;
  mid?: string | null;
  model_id_alias?: string | null;
  remark?: string | null;
};

/** 空关键词视为全匹配；比较不区分大小写 */
export function modelMatchesKeyword(m: ModelKeywordFields, keyword: string): boolean {
  const kw = keyword.trim().toLowerCase();
  if (!kw) return true;
  return (
    (m.name || '').toLowerCase().includes(kw) ||
    (m.model_id || '').toLowerCase().includes(kw) ||
    (m.mid || '').toLowerCase().includes(kw) ||
    (m.model_id_alias || '').toLowerCase().includes(kw) ||
    (m.remark || '').toLowerCase().includes(kw)
  );
}
