-- 0011_group_ratio.sql
-- 分组差异化定价：每个组织有一个分组(group_name)，每个分组有一个计费倍率。
-- 最终费用 = 基础 token 费 × 分组倍率。default=1（不打折），VIP/企业可设 <1，
-- 试用/高风险分组可设 >1。列名用 group_name 避开 SQL 保留字 group 的跨方言引用问题。

ALTER TABLE organizations ADD COLUMN group_name VARCHAR(64) NOT NULL DEFAULT 'default';

CREATE TABLE group_ratios (
    name  VARCHAR(64) PRIMARY KEY,
    ratio DECIMAL(10,4) NOT NULL DEFAULT 1
);

INSERT INTO group_ratios (name, ratio) VALUES ('default', 1);
