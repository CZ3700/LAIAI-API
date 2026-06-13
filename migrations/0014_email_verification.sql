-- 0014_email_verification.sql
-- 邮箱验证码：注册前验证邮箱可达，堵住同邮箱重复注册薅羊毛（配合 email_canonical 唯一）。
-- 一个 canonical email 一行（PK），重发覆盖；code 短时效。

CREATE TABLE email_verifications (
    email_canonical VARCHAR(255) PRIMARY KEY,
    code            VARCHAR(16) NOT NULL,
    expires_at      BIGINT NOT NULL,
    created_at      BIGINT NOT NULL
);
