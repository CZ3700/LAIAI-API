-- 0164_invocations_rule_tracking.sql
-- 调用流水增加命中的转发规则 EID、计费规则 PID 与上游进货成本倍率，支撑规则排障与毛利核算。
ALTER TABLE invocations
    ADD COLUMN forward_rule_eid    VARCHAR(16)    NOT NULL DEFAULT '',
    ADD COLUMN billing_rule_pid    VARCHAR(16)    NOT NULL DEFAULT '',
    ADD COLUMN upstream_cost_ratio DECIMAL(10, 4) NOT NULL DEFAULT 1.0000;
