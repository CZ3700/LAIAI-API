/** 结算/展示：未配置时的免费张数（Seedream 历史首张免费=1；其余默认 5） */
export function resolveFreeImageCount(value: unknown, billingRule?: string): number {
  const n = Number(value);
  if (Number.isFinite(n) && n >= 0) return Math.trunc(n);
  if (billingRule === 'volc_seedream_pro') return 1;
  return 5;
}

/** 新建表单 / 保存兜底默认值（Seedream 新建默认 1） */
export function formDefaultFreeImageCount(billingRule: string): number {
  if (billingRule === 'volc_seedream_pro') return 1;
  if (billingRule === 'minimax_h3' || billingRule === 'video_seconds_io' || billingRule === 'video_seconds_ref') return 5;
  return 0;
}
