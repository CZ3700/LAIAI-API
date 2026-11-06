-- 终态且账务完结的任务永远不会再被轮询。旧数据把 next_poll_at 留在过去，
-- 让轮询候选查询的扫描范围随历史只增不减（每 2 秒全量重扫）。回置 NULL；
-- reducer 现在在同一批转换上直接写 NULL（poll/generation/派发终态路径）。
UPDATE video_task_states v
JOIN tasks t ON t.org_id = v.org_id AND t.task_id = v.task_id
SET v.next_poll_at = NULL
WHERE v.deleted_at IS NULL
  AND v.next_poll_at IS NOT NULL
  AND t.status IN ('succeeded', 'failed', 'cancelled', 'expired')
  AND v.billing_state IN ('settled', 'released');
