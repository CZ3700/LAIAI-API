// 去水印处理开关（Admin「视频模型限额」tab 顶部卡片）。en 为源；zh-CN 一等翻译，
// 其余语言经 index fallback 回退 en（与 modelLimits 模块同款约定）。
const en = {
  title: 'Watermark-removal processing',
  intro: 'Enable the master switch and select users below. Succeeded Seedance videos created with their personal API keys are processed asynchronously; the task video address then serves the processed copy.',
  enabled: 'Enabled',
  disabled: 'Disabled',
  notConfigured: 'Pipeline unavailable: media storage and ffmpeg must be configured first.',
  hint: 'Adding a user applies to future successful tasks. The default FFmpeg filter follows the documented scale, rotation and crop back to source size; verify its effect on the intended faint watermark. The master switch stops new processing. Removing a user stops new task registration and future task responses from serving their processed copy; existing signed links remain valid until they expire.',
  usersTitle: 'Enabled users',
  orgKeyNote: 'A user grant applies across their organizations. Only personal API keys are eligible; organization-level keys have no user owner. An empty list processes no tasks.',
  searchPlaceholder: 'Search by email or name',
  search: 'Search users',
  addUser: 'Enable for user',
  removeUser: 'Remove',
  selected: 'Enabled',
  userInactive: 'Inactive',
  noUsers: 'No users selected. The master switch alone does not start processing.',
}

export const zhCN: typeof en = {
  title: '去水印处理',
  intro: '开启总开关并指定用户后，该用户使用个人 API Key 创建的 Seedance 成功任务会异步生成去水印副本，任务视频地址优先返回副本。',
  enabled: '已启用',
  disabled: '已停用',
  notConfigured: '管线不可用：需先配置媒体存储与 ffmpeg。',
  hint: '加入用户后只处理后续成功任务。默认 FFmpeg 滤镜按文档进行缩放、旋转和回裁至原尺寸；针对目标暗水印仍需验收实际效果。关闭总开关会停止新任务处理。移除用户后，新任务不再登记，后续任务响应不再返回其已处理副本；已签发链接在到期前仍可访问。',
  usersTitle: '已启用用户',
  orgKeyNote: '用户授权适用于其加入的所有组织。仅个人 API Key 可按用户启用；组织级 Key 无用户归属。名单为空时不会处理任何任务。',
  searchPlaceholder: '搜索邮箱或姓名',
  search: '搜索用户',
  addUser: '为此用户启用',
  removeUser: '移除',
  selected: '已启用',
  userInactive: '已停用',
  noUsers: '尚未指定用户。仅开启总开关不会开始处理。',
}

export default en
