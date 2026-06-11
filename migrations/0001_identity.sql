-- 0001_identity.sql
-- Identity 首发 schema：Organization 一等公民（设计文档 §4.1/§4.2）。
-- 红线：email_canonical 在 schema 层 UNIQUE —— 薅羊毛漏洞在数据库层面消灭，不依赖应用层检查。
-- 约定：版本化 SQL only，本平台永久禁用 AutoMigrate。

CREATE TABLE organizations (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    slug            VARCHAR(64)  NOT NULL,
    name            VARCHAR(128) NOT NULL,
    kind            TINYINT      NOT NULL DEFAULT 0 COMMENT '0=personal 1=team',
    white_label     TINYINT      NOT NULL DEFAULT 0 COMMENT 'entitlement 开关，付费能力',
    status          TINYINT      NOT NULL DEFAULT 1 COMMENT '1=active 0=disabled',
    created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_org_slug (slug)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE users (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    email           VARCHAR(320) NOT NULL COMMENT '原始邮箱（展示用）',
    email_canonical VARCHAR(320) NOT NULL COMMENT 'canonical 化（gmail dots/+alias 折叠）写库前置',
    password_hash   VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'Argon2id；OAuth-only 用户为空',
    display_name    VARCHAR(64)  NOT NULL DEFAULT '',
    totp_secret_enc VARCHAR(255) NOT NULL DEFAULT '' COMMENT '信封加密，密文头带 key_id',
    status          TINYINT      NOT NULL DEFAULT 1 COMMENT '1=active 0=disabled 2=pending_verify',
    created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_users_email_canonical (email_canonical)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE org_members (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    org_id      BIGINT UNSIGNED NOT NULL,
    user_id     BIGINT UNSIGNED NOT NULL,
    role        VARCHAR(16)  NOT NULL DEFAULT 'member' COMMENT 'owner|admin|member|billing',
    created_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_member_org_user (org_id, user_id),
    KEY idx_member_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE api_keys (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    org_id      BIGINT UNSIGNED NOT NULL,
    user_id     BIGINT UNSIGNED NULL COMMENT 'per-member key 归属；org 级 key 为 NULL',
    name        VARCHAR(64)  NOT NULL DEFAULT '',
    prefix      VARCHAR(16)  NOT NULL COMMENT '展示用前缀 sk-laiai-xxxx',
    key_hash    CHAR(64)     NOT NULL COMMENT 'SHA-256(full key)，明文只显示一次',
    scopes      VARCHAR(255) NOT NULL DEFAULT '' COMMENT '逗号分隔 scope',
    status      TINYINT      NOT NULL DEFAULT 1 COMMENT '1=active 0=revoked',
    expires_at  DATETIME(3)  NULL,
    created_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_api_key_hash (key_hash),
    KEY idx_api_keys_org (org_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
