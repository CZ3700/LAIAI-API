-- 0163_model_rule_bindings.sql
-- 模型与计费/转发规则绑定表：向后兼容既有 model_prices 与 model_meta，支持 0 发版热挂接计费模板与转发规则。
CREATE TABLE model_rule_bindings (
    model                   VARCHAR(128)  NOT NULL PRIMARY KEY,
    display_name            VARCHAR(128)  NOT NULL DEFAULT '',
    model_alias             VARCHAR(128)  NOT NULL DEFAULT '',
    billing_rule_id         BIGINT UNSIGNED NOT NULL DEFAULT 0,
    forward_rule_ids        TEXT          NOT NULL DEFAULT ('[]'),
    pre_deduction           DECIMAL(20,8) NOT NULL DEFAULT 0,
    enable_log_content      TINYINT       NOT NULL DEFAULT 0,
    site_discount           DECIMAL(10,4) NOT NULL DEFAULT 1.0000,
    site_discount_enabled   TINYINT       NOT NULL DEFAULT 0,
    global_discount         DECIMAL(10,4) NOT NULL DEFAULT 1.0000,
    global_discount_enabled TINYINT       NOT NULL DEFAULT 0,
    discount_schedule       LONGTEXT      NOT NULL DEFAULT ('{}'),
    is_listed               TINYINT       NOT NULL DEFAULT 1,
    is_active               TINYINT       NOT NULL DEFAULT 1,
    sort_order              INT           NOT NULL DEFAULT 0,
    created_at              DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at              DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    KEY idx_mrb_billing_rule (billing_rule_id, is_listed, is_active),
    KEY idx_mrb_listed (is_listed, is_active, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
