-- 0032_task_admission.sql
-- Task/video 准入统一（指令 P0-1）配套快照列：
--   - hold_amount_usd：提交时冻结额快照 = key quota 预留额。settle 终态按它对账/退还
--     API Key used_amount（chat 的 ReserveKeyQuota→capture 对账同款语义）。
--     0 = 本迁移前的在途旧行（当时未预留），settle 对其不做任何 key 额度调整。
--   - group_ratio：提交时计费倍率快照（0011 一层 × 0027 二层）。hold 与结算取同一次值，
--     与 chat 的 hold/capture 同值纪律一致；修复视频任务不乘分组倍率导致 org 差异化定价失效。
ALTER TABLE tasks
    ADD COLUMN hold_amount_usd DECIMAL(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN group_ratio DECIMAL(20,8) NOT NULL DEFAULT 1;
