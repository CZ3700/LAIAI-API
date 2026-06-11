-- 0003_relay.sql
-- Relay 内核 P1（设计 §4.4）：上游渠道 Key 池 + 模型定价。
-- credential_enc 为信封加密（enc:v1:<key_id>:<...>），密钥环见 internal/crypto——
-- 密文自带 key_id，轮换无顺序依赖（KYC 轮换陷阱的结构性消解，设计 §5.5）。

CREATE TABLE channels (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    name            VARCHAR(64)  NOT NULL,
    provider        VARCHAR(32)  NOT NULL COMMENT 'openai|anthropic|modelark|dashscope|...',
    base_url        VARCHAR(255) NOT NULL COMMENT '上游 API base，如 https://api.openai.com/v1',
    credential_enc  TEXT         NOT NULL COMMENT '上游 API Key，信封加密，永不明文',
    models          TEXT         NOT NULL COMMENT '逗号分隔的可服务模型清单',
    weight          INT          NOT NULL DEFAULT 1 COMMENT '路由权重，大者优先',
    status          TINYINT      NOT NULL DEFAULT 1 COMMENT '1=active 0=disabled（禁用即退出调度池）',
    created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_channels_status (status, weight)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE model_prices (
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    model               VARCHAR(128)  NOT NULL,
    input_usd_per_1m    DECIMAL(12,6) NOT NULL COMMENT '每百万输入 token 的 USD 价',
    output_usd_per_1m   DECIMAL(12,6) NOT NULL,
    updated_at          DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_model_prices_model (model)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
