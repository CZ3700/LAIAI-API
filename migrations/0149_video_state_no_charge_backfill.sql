-- 2026-11-06：生产模型配置的 no_charge_statuses 此前为空，failed 终态任务
-- 全部无法走 provider_no_charge 解冻（120 条冻结、每 2-3 分钟空转轮询）。
-- 配置已更正为 failed,cancelled,expired；本迁移把存量 pending_usage 任务的
-- 状态行（受理时冻结的配置副本）同步到正确名单，使下一次轮询即完成解冻并
-- 停止一切后续操作（reducer 会置 next_poll_at=NULL）。配置副本不是证据，
-- 与更正后的配置对齐是安全的。
UPDATE video_task_states v
JOIN tasks t ON t.org_id = v.org_id AND t.task_id = v.task_id
SET v.no_charge_statuses = 'failed,cancelled,expired'
WHERE v.deleted_at IS NULL
  AND v.billing_state = 'pending_usage'
  AND t.status IN ('failed', 'expired');
