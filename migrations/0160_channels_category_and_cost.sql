-- 0160_channels_category_and_cost.sql
-- 渠道扩展分类关联与采购成本倍率（单条原子 ALTER TABLE，MySQL 安全可重放）。
ALTER TABLE channels
    ADD COLUMN category_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    ADD COLUMN upstream_cost_ratio DECIMAL(10,4) NOT NULL DEFAULT 1.0000,
    ADD KEY idx_channels_category (category_id, status);
