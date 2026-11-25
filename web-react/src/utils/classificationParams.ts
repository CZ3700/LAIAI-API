/** 运营模型 / 模型仓库 → 列表与 stats 共用的查询参数 */
export type ModelSourceFilter = 'custom' | 'library';

/** id=0：未分类芯片，与后端 `Some(0)` / IS NULL 一致 */
export function unclassifiedChip(count = 0) {
  return { id: 0, name: '未分类', name_en: 'Uncategorized', count };
}

export function buildClassificationParams(
  providerId: number | null | undefined,
  apiProviderId: number | null | undefined,
  typeId: number | null | undefined,
  source?: ModelSourceFilter | null,
): Record<string, string | number> {
  const params: Record<string, string | number> = {};
  if (providerId != null) params.provider_id = providerId;
  if (apiProviderId != null) params.api_provider_id = apiProviderId;
  if (typeId != null) params.type_id = typeId;
  if (source === 'library') params.source = 'library';
  else if (source === 'custom') params.source = 'custom';
  return params;
}
