-- 0161_billing_rules.sql
-- 数据驱动计费规则模板表：支持标准/阶梯/多模态/缓存5m&1h/视频时长与分辨率阶梯/峰谷分时倍率。
-- 全部金额与单价列采用 DECIMAL(20,8)，严禁 float 精度误差。
CREATE TABLE billing_rules (
    id                         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    pid                        VARCHAR(16)    NOT NULL,
    name                       VARCHAR(128)   NOT NULL,
    billing_type               VARCHAR(32)    NOT NULL DEFAULT 'tokens' COMMENT 'tokens|requests|duration',
    billing_rule               VARCHAR(64)    NOT NULL DEFAULT 'standard',
    pricing_type               VARCHAR(32)    NOT NULL DEFAULT 'tokens',
    prompt_rate                DECIMAL(20,8)  NOT NULL DEFAULT 0,
    completion_rate            DECIMAL(20,8)  NOT NULL DEFAULT 0,
    cached_rate                DECIMAL(20,8)  NOT NULL DEFAULT 0,
    claude_cache_creation_rate DECIMAL(20,8)  NOT NULL DEFAULT 0,
    claude_cache_read_rate     DECIMAL(20,8)  NOT NULL DEFAULT 0,
    fixed_rate                 DECIMAL(20,8)  NOT NULL DEFAULT 0,
    duration_rate              DECIMAL(20,8)  NOT NULL DEFAULT 0,
    pricing_tiers              LONGTEXT       NOT NULL DEFAULT ('[]'),
    extended_config            LONGTEXT       NOT NULL DEFAULT ('{}'),
    is_active                  TINYINT        NOT NULL DEFAULT 1,
    is_system                  TINYINT        NOT NULL DEFAULT 0,
    sort_order                 INT            NOT NULL DEFAULT 0,
    created_at                 DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at                 DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_billing_rules_pid (pid),
    UNIQUE KEY uk_billing_rules_name (name),
    KEY idx_billing_rules_active (is_active, sort_order, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
