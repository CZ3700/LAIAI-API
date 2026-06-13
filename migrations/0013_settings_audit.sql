-- 0013_settings_audit.sql
-- 运营设置（运行时可配项，替代部分 env-only 死配置）+ 审计日志（admin 操作留痕）。

-- 运营设置：简单 KV，value 存字符串（布尔用 'true'/'false'，文本直接存）。
-- 已知键：register_enabled / email_verification_enabled / announcement /
--   maintenance_mode / terms_of_service / privacy_policy / site_name。
CREATE TABLE ext_settings (
    name  VARCHAR(64) PRIMARY KEY,
    value TEXT NOT NULL
);

-- 审计日志：谁(actor)在何时对什么(target)做了什么(action)。
-- actor 是 admin 操作来源标识（admin-token 操作记 'admin'，未来 root 用户记 user_id）。
CREATE TABLE audit_logs (
    id         BIGINT AUTO_INCREMENT PRIMARY KEY,
    actor      VARCHAR(64) NOT NULL,
    action     VARCHAR(64) NOT NULL,
    target     VARCHAR(128) NOT NULL DEFAULT '',
    detail     TEXT NOT NULL,
    ip         VARCHAR(64) NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    INDEX idx_audit_created (created_at),
    INDEX idx_audit_action (action)
);
