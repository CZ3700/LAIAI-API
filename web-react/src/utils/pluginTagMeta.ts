/** 从详情 plugin_tag 解析展开展示字段（列表不返回该列） */
export function parsePluginTagMeta(pluginTag?: string | null): {
  clientCt?: string;
  cascadeS1TaskId?: string;
  upstreamTaskId?: string;
} {
  if (!pluginTag) return {};
  try {
    const tag = JSON.parse(pluginTag);
    const s1 = tag?.cascade?.s1_task_id;
    const upstream = tag?.upstream_task;
    return {
      clientCt: typeof tag?.client_ct === 'string' && tag.client_ct ? tag.client_ct : undefined,
      cascadeS1TaskId: typeof s1 === 'string' && s1 ? s1 : undefined,
      upstreamTaskId: typeof upstream === 'string' && upstream ? upstream : undefined,
    };
  } catch {
    return {};
  }
}
