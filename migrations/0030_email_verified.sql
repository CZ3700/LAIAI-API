-- 0030_email_verified.sql
-- 持久化邮箱验证态：注册时过验证码即置 1（绑定真实邮箱，供后续邮件推送 / 未验证门控）。
-- 单条 DDL（原子，re-run 干净）。现存用户默认 0（未验证）；如需可由运营批量回填。

ALTER TABLE users ADD COLUMN email_verified TINYINT NOT NULL DEFAULT 0 COMMENT '邮箱是否已验证（注册过验证码即 1）';
