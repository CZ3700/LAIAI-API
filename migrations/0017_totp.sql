-- 0017_totp.sql
-- 2FA（TOTP）启用标志。totp_secret_enc 列 0001 已预留（信封加密存共享密钥）。

ALTER TABLE users ADD COLUMN totp_enabled TINYINT NOT NULL DEFAULT 0;
