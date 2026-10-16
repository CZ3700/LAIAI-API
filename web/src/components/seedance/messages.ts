import { useI18n } from 'vue-i18n'

const en = {
  tab: 'Seedance video', general: 'General models', received: 'Request received', result: 'Task result', response: 'API response',
  request: 'Request ID', task: 'Task ID', model: 'Model', operation: 'Operation', http: 'HTTP status', taskStatus: 'Task status', billing: 'Billing', artifact: 'Artifact', amount: 'Related task charge (USD)', time: 'Time', key: 'Platform Key ID',
  search: 'Search', all: 'All operations', create_task: 'Create', get_task: 'Query', cancel_task: 'Cancel', delete_task: 'Delete', estimate: 'Estimate', asset_operation: 'Asset operation',
  next: 'Next page', previous: 'Previous', close: 'Close', loading: 'Loading…', empty: 'No requests found', failed: 'Could not load the request logs',
  read: 'Read JSON (audited)', copy: 'Copy JSON', download: 'Download JSON', copied: 'Copied', collapse: 'Collapse', expand: 'Expand', jsonSearch: 'Search JSON',
  available: 'Available', disabled: 'History retention disabled', expired: 'History expired', not_captured: 'Not captured', forbidden: 'Permission required', not_applicable: 'No body for this operation',
  notice: 'Each row is an HTTP request. Related task amounts are repeated for context and must not be added together. Period spending is available in fund activities.',
  pending: 'Unconfirmed', reserved: 'Reserved', settled: 'Captured', released: 'Released', net: 'Net charged', priceVersion: 'Pricing version', ruleVersion: 'Billing rule version',
  videoTokens: 'Video billable tokens', inputTokens: 'Input tokens: 0 (not separately metered by the provider)', asOf: 'Observed at', deleted: 'Task deleted; audit and billing remain available',
  outbound: 'Effective outbound JSON', upstream: 'Upstream response', attempts: 'Execution attempts', events: 'Task timeline', loadMore: 'Load more', reason: 'Access purpose',
  request_debugging: 'Request debugging', billing_review: 'Billing review', customer_support: 'Customer support', original: 'Original request', replay: 'Idempotency replay',
  duration: 'HTTP duration', receivedNotice: 'Business JSON after secret redaction; representation and retention are shown with each body.', responseNotice: 'The response returned for this HTTP request. A later task result does not replace it.',
  quote: 'Review quote', confirm: 'Confirm and generate', quoteAmount: 'Maximum authorized reserve (USD)', quoteNotice: 'The quote is an estimate. Final billing uses confirmed provider usage and the accepted pricing version.',
  retry: 'Retry the same submission', uncertain: 'Submission response is unknown. Retry with the same idempotency key; do not create another task.',
  logs: 'Open request log', currentResult: 'Current task result', refresh: 'Refresh task', cancel: 'Cancel task', remove: 'Delete task',
  artifactWaiting: 'Generation completed. Waiting for the private artifact.', billingWaiting: 'Usage or billing is still being confirmed.', noQuote: 'A valid quote is required before generation.',
  confirmDelete: 'Delete this task and hide its artifact? The financial record is retained.', generationState: 'Generation status', source: 'Representation', expires: 'Expires at',
  referenceVideo: 'Reference video', referenceAudio: 'Reference audio', mediaNotice: 'Use an authorized platform asset or a supported HTTPS reference. The server verifies media and uses trusted durations or configured upper bounds for pricing.', defaultOption: 'Model default', automaticDuration: 'Automatic duration', modelUnavailable: 'This model is not enabled and verified for this account.',
  reportedUsage: 'Provider-reported usage', metering: 'Creation-time estimate', meteringNotice: 'These are pricing inputs saved at creation. Final billable tokens come from confirmed provider usage.', estimatedTokens: 'Estimated tokens', unitPrice: 'USD per million tokens', inputVideoCount: 'Reference videos', inputDuration: 'Input video duration', outputDuration: 'Output duration', frames: 'Frames', dimensions: 'Dimensions', minimumTokens: 'Minimum billable tokens', unknown: 'Unknown', none: 'None', verified_media: 'Verified media', configured_upper_bound: 'Configured upper bound', draft_inherited: 'Inherited from Draft',
}
const zh = {
  tab: 'Seedance 视频', general: '通用模型', received: '请求接收', result: '任务结果', response: '接口响应',
  request: '请求 ID', task: '任务 ID', model: '模型', operation: '操作', http: 'HTTP 状态', taskStatus: '任务状态', billing: '账务状态', artifact: '产物状态', amount: '关联任务金额（USD）', time: '时间', key: '平台 Key ID',
  search: '查询', all: '全部操作', create_task: '创建', get_task: '查询', cancel_task: '取消', delete_task: '删除', estimate: '预估', asset_operation: '素材操作',
  next: '下一页', previous: '上一页', close: '关闭', loading: '加载中…', empty: '没有找到请求', failed: '请求日志加载失败',
  read: '查看 JSON（记录审计）', copy: '复制 JSON', download: '下载 JSON', copied: '已复制', collapse: '折叠', expand: '展开', jsonSearch: '搜索 JSON',
  available: '可查看', disabled: '未开启历史留存', expired: '已过期', not_captured: '未保存', forbidden: '无权限', not_applicable: '此操作没有正文',
  notice: '每行对应一次 HTTP 请求。关联任务金额可能重复展示，不能按日志行求和；期间消费请查看动账流水。',
  pending: '待确认', reserved: '冻结', settled: '实扣', released: '释放', net: '净扣款', priceVersion: '价格版本', ruleVersion: '计费规则版本',
  videoTokens: '视频计费 Token', inputTokens: '输入 Token：0（官方不单独计量）', asOf: '观测时间', deleted: '任务已删除，审计和账务记录仍保留',
  outbound: '有效出站 JSON', upstream: '上游响应', attempts: '执行尝试', events: '任务时间线', loadMore: '加载更多', reason: '访问用途',
  request_debugging: '请求排障', billing_review: '账单核对', customer_support: '客户支持', original: '原始请求', replay: '幂等重放',
  duration: 'HTTP 耗时', receivedNotice: '显示剔除秘密后的业务 JSON，每份正文标明保存形式和留存期限。', responseNotice: '此 HTTP 请求当时实际返回的响应，后续任务结果不会覆盖它。',
  quote: '查看报价', confirm: '确认并生成', quoteAmount: '最大授权冻结金额（USD）', quoteNotice: '报价是预估；最终按已确认的官方用量和受理时价格版本结算。',
  retry: '重试同一次提交', uncertain: '提交响应未知。请使用同一幂等键重试，不要创建另一个任务。',
  logs: '打开请求日志', currentResult: '任务当前结果', refresh: '刷新任务', cancel: '取消任务', remove: '删除任务',
  artifactWaiting: '生成已完成，正在等待私有产物就绪。', billingWaiting: '用量或账务仍待确认。', noQuote: '生成前需要有效报价。',
  confirmDelete: '删除此任务并隐藏产物？财务记录仍保留。', generationState: '生成状态', source: '保存形式', expires: '到期时间',
  referenceVideo: '参考视频', referenceAudio: '参考音频', mediaNotice: '使用已授权的平台素材或支持的 HTTPS 引用。服务端校验素材，并按可信时长或已配置的时长上限计价。', defaultOption: '模型默认值', automaticDuration: '自动时长', modelUnavailable: '此账户的模型尚未启用并完成核验。',
  reportedUsage: '原始上报用量', metering: '创建时计量预估', meteringNotice: '这里是创建时保存的计价输入，最终计费 Token 以已确认的官方用量为准。', estimatedTokens: '预估 Token', unitPrice: '每百万 Token 单价（USD）', inputVideoCount: '参考视频数量', inputDuration: '输入视频时长', outputDuration: '输出时长', frames: '帧数', dimensions: '尺寸', minimumTokens: '最低计费 Token', unknown: '未知', none: '无', verified_media: '已核验素材', configured_upper_bound: '配置时长上限', draft_inherited: '继承 Draft',
}
export const seedanceMessages = { en: { seedance: en }, 'zh-CN': { seedance: zh } }
export function useSeedanceI18n() {
  const i18n = useI18n({ useScope: 'global' })
  i18n.mergeLocaleMessage('en', seedanceMessages.en)
  i18n.mergeLocaleMessage('zh-CN', seedanceMessages['zh-CN'])
  return i18n
}
