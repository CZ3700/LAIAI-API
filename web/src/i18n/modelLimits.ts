// 视频模型级受理限额（Admin「视频模型限额」tab）。en 为源；zh-CN 一等翻译，
// 其余语言经 index fallback 回退 en（与 arkControl 模块同款约定）。
const en = {
  intro: 'Per-model admission limits mirroring the official Ark quotas: cap in-flight tasks and create requests per minute. 0 = unlimited (channel-level gate only).',
  model: 'Model',
  channel: 'Channel',
  concurrent: 'Concurrency cap',
  createRpm: 'Create RPM',
  enabled: 'enabled',
  disabled: 'disabled',
  unpublished: 'unpublished',
  hint: 'Applies at admission: rejected requests never reach Volcano and are never billed. Concurrency takes effect on the next request; RPM refreshes within 5 seconds.',
}

export const zhCN: typeof en = {
  intro: '按模型单独设置受理限额（对齐火山官方配额）：限制该模型的在途任务数与每分钟受理数。0 = 不限制（仅渠道总闸生效）。',
  model: '模型',
  channel: '渠道',
  concurrent: '并发上限',
  createRpm: '每分钟受理（RPM）',
  enabled: '已启用',
  disabled: '已停用',
  unpublished: '未发布',
  hint: '限额在受理侧生效：被拒请求不会到达火山，也不会产生任何计费。并发上限下一笔请求即生效；RPM 上限 5 秒内生效。',
}

export default en
