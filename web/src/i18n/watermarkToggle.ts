// 去水印处理开关（Admin「视频模型限额」tab 顶部卡片）。en 为源；zh-CN 一等翻译，
// 其余语言经 index fallback 回退 en（与 modelLimits 模块同款约定）。
const en = {
  title: 'Watermark-removal processing',
  intro: 'When enabled, succeeded Seedance videos are asynchronously processed into a watermark-free copy; the task video address then serves the processed file. The original provider URL is kept as fallback and the task response shape never changes.',
  enabled: 'Enabled',
  disabled: 'Disabled',
  notConfigured: 'Pipeline unavailable: media storage and ffmpeg must be configured first.',
  hint: 'Processed copies are retained for a limited period and re-derivable; toggling off stops new processing immediately and already-processed tasks keep serving their processed copy.',
}

export const zhCN: typeof en = {
  title: '去水印处理',
  intro: '启用后，Seedance 任务成功视频会异步生成一份去水印副本；任务视频地址将优先返回去水印产物。火山原始直链保留兜底，任务响应结构不变。',
  enabled: '已启用',
  disabled: '已停用',
  notConfigured: '管线不可用：需先配置媒体存储与 ffmpeg。',
  hint: '去水印产物按保留期自动回收；关闭开关立即停止新的处理，已处理完成的任务继续返回去水印地址。',
}

export default en
