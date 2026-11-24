-- 0162_forward_rules.sql
-- 数据驱动转发规则模板表：声明入口路径匹配、目标适配器 target_type、上游路径模板、鉴权方式与异步轮询配置。
CREATE TABLE forward_rules (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    eid         VARCHAR(16)  NOT NULL,
    name        VARCHAR(128) NOT NULL,
    rule_type   VARCHAR(64)  NOT NULL DEFAULT 'openai',
    category    VARCHAR(32)  NOT NULL DEFAULT '聊天',
    config_json LONGTEXT     NOT NULL DEFAULT ('{}'),
    description VARCHAR(512) NOT NULL DEFAULT '',
    remark      VARCHAR(512) NOT NULL DEFAULT '',
    is_active   TINYINT      NOT NULL DEFAULT 1,
    is_system   TINYINT      NOT NULL DEFAULT 0,
    sort_order  INT          NOT NULL DEFAULT 0,
    created_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_forward_rules_eid (eid),
    UNIQUE KEY uk_forward_rules_name (name),
    KEY idx_forward_rules_active (is_active, category, sort_order, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
