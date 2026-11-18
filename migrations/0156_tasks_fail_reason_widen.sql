-- tasks.fail_reason 扩容 255→1024（2026-11-18）。
-- 审计 A08 的异步失败原因回填拼 code + ": " + message，火山 message 常带
-- request id（TaskTypeMismatch 等约 300+ 字符）超 varchar(255)，令整个
-- query apply 事务 1406 回滚、收据无限重试（2 个任务卡 3 小时、25835 次
-- apply 失败）。完整上游报文另存加密 invocation payload；此列是客户 GET
-- 可见的摘要，扩容以保留 request id 等排查信息，写入侧按 1024 截断兜底。
-- utf8mb4 下 1024 字符 = 全表最大 4096 字节，行内无溢出风险（DYNAMIC 行格式）。
ALTER TABLE tasks MODIFY COLUMN fail_reason VARCHAR(1024) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL;
-- migrate:ack-destructive 纯放宽 varchar 255→1024（utf8mb4），无数据截断/丢失；执行前走 backup.sh 全库快照 + 人工 runbook。
