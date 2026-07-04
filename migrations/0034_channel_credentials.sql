-- 0034_channel_credentials.sql
-- Multi-key Channel（商用迭代指令 §3.5；设计 §4.4）：单渠道挂多个上游 key，
-- 401/invalid 只禁当前 key 不误杀整个渠道。**绝不把多 key 塞进单字段字符串**（守则）。
--   - channel_credentials：每 key 一行；credential_enc 信封加密（与单 key 同款密文）；
--     fingerprint=明文 key SHA-256 前 12 hex（后台展示不泄明文）；status 1=active 2=disabled；
--     mode_weight 供 weighted 模式加权随机。
--   - channels.credential_mode：random|polling|weighted，每请求为渠道挑一个启用 key。
-- 向后兼容：幂等 DML 把每个现有渠道的 credential_enc 回填进子表（WHERE NOT EXISTS 防半应用重跑
--   双插）。channels.credential_enc 列保留不删作 legacy fallback。

CREATE TABLE channel_credentials (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    channel_id      BIGINT UNSIGNED NOT NULL,
    credential_enc  TEXT         NOT NULL COMMENT '信封加密（密文头带 key_id），与单 key 同款',
    fingerprint     VARCHAR(32)  NOT NULL DEFAULT '' COMMENT '明文 key SHA-256 前 12 hex，展示用',
    status          TINYINT      NOT NULL DEFAULT 1 COMMENT '1=active 2=disabled',
    disabled_reason VARCHAR(255) NOT NULL DEFAULT '',
    disabled_at     DATETIME(3)  NULL,
    last_used_at    DATETIME(3)  NULL,
    mode_weight     INT          NOT NULL DEFAULT 1 COMMENT 'weighted 模式加权',
    created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_cc_channel_active (channel_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE channels ADD COLUMN credential_mode VARCHAR(16) NOT NULL DEFAULT 'random';

INSERT INTO channel_credentials (channel_id, credential_enc, mode_weight)
    SELECT c.id, c.credential_enc, 1 FROM channels c
    WHERE c.credential_enc <> ''
      AND NOT EXISTS (SELECT 1 FROM channel_credentials cc WHERE cc.channel_id = c.id);
