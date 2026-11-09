-- 回调 hint 从"每次推送一行"改为"每任务一行（最新）"——应用层在 UPSERT 前
-- 删除该 task_id 的旧行（无 DDL 变更，唯一键不动）。本文件只做存量去重：
-- 保留每个 task_id 的最新一条，旧重复行清除。
DELETE h1 FROM video_callback_hints h1
JOIN video_callback_hints h2
  ON h1.task_id = h2.task_id
 AND (h1.received_at < h2.received_at OR (h1.received_at = h2.received_at AND h1.id < h2.id));
