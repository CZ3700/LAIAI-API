/** 日志筛选：失败态 status_code（不含 200 成功；与 proxy/error 落库一致） */
export const LOG_ERROR_STATUS_CODES = [
  '0', '400', '401', '402', '403', '404', '408', '422', '429', '500', '502', '503', '504',
] as const;

/**
 * HA 熔断配置项：与日志常见失败码对齐。
 * `5xx` 桶覆盖 500–599；401/402 在表单中合并为一项编辑。
 */
export const HA_MELT_CODES = [
  '429', '401', '402', '403', '404', '400', '408', '422', '5xx',
] as const;

const HA_MELT_LABELS: Record<string, string> = {
  '429': '限流',
  '401': '鉴权失效',
  '402': '欠费',
  '403': '禁止访问',
  '404': '未找到',
  '400': '请求/内容拦截',
  '408': '超时',
  '422': '参数无法处理',
  '5xx': '500–599 默认',
};

export function haMeltLabel(code: string): string {
  const name = HA_MELT_LABELS[code];
  return name ? `${code} ${name}` : code;
}

/** 规则列表「熔断摘要」 */
export function haMeltSummary(melt?: Record<string, number>): string {
  const m = melt || {};
  const parts = [
    m['429'] ? `429限流 ${m['429']}s` : null,
    (m['401'] || m['402']) ? `鉴权/欠费 ${m['401'] || m['402']}s` : null,
    m['403'] ? `403 ${m['403']}s` : null,
    m['404'] ? `404 ${m['404']}s` : null,
    m['400'] ? `400 ${m['400']}s` : null,
    m['408'] ? `408 ${m['408']}s` : null,
    m['422'] ? `422 ${m['422']}s` : null,
    m['5xx'] ? `5xx ${m['5xx']}s` : null,
  ].filter(Boolean);
  return parts.length ? parts.join(' · ') : '未启用';
}

/** 新建规则默认熔断（与后端 default_melt 一致） */
export const HA_DEFAULT_MELT: Record<string, number> = {
  '429': 60,
  '401': 1800,
  '402': 1800,
  '404': 3,
  '5xx': 300,
};
