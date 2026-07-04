-- 0035_task_credential.sql
-- 任务提交时选定的上游 key id（0034 Multi-key 配套，对抗 review 修复）：
-- 多 key 渠道下提交与轮询若各自独立选 key，可能用不同 key 查任务 → 上游 not-found →
-- 任务永不终态 → 超时全退，但上游已生成并计费（功能损失 + 平台吃成本）。
-- 提交时把选定 credential id 钉在任务行，轮询固定用它（不受后续该 key 禁用影响）。
-- 0 = legacy 单 key（无子表行），轮询回退 channels.credential_enc。
ALTER TABLE tasks ADD COLUMN credential_id BIGINT UNSIGNED NOT NULL DEFAULT 0;
