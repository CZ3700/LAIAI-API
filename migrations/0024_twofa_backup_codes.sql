-- 0024_twofa_backup_codes.sql
-- 2FA 备用码（gap #3，对标 NewAPI model/twofa.go）：TOTP 设备丢失时的账号恢复手段。
-- 只存归一化明文的 SHA-256（明文仅启用/重置时回显一次）；used_at 实现"用过即焚"。
CREATE TABLE twofa_backup_codes (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id    BIGINT UNSIGNED NOT NULL,
    code_hash  CHAR(64)        NOT NULL COMMENT 'sha256(归一化明文) hex',
    used_at    BIGINT          NULL COMMENT 'NULL=未用；unix 秒=已用',
    created_at BIGINT          NOT NULL,
    PRIMARY KEY (id),
    KEY idx_2fa_backup_user (user_id),
    UNIQUE KEY uk_2fa_backup (user_id, code_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
