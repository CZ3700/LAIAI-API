-- 0165_tasks_rule_tracking.sql
-- 异步任务表增加规则追踪与多模态特征/峰谷倍率/计费明细快照，实现 Seedance/视频任务按 billing_rules 与 forward_rules 引擎结算。
ALTER TABLE tasks
    ADD COLUMN forward_rule_eid VARCHAR(16)    NOT NULL DEFAULT '',
    ADD COLUMN billing_rule_pid VARCHAR(16)    NOT NULL DEFAULT '',
    ADD COLUMN features_json    TEXT           NULL,
    ADD COLUMN time_multiplier  DECIMAL(10, 4) NOT NULL DEFAULT 1.0000,
    ADD COLUMN billing_detail   VARCHAR(512)   NOT NULL DEFAULT '';
