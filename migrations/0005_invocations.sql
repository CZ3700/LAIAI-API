-- 0005_invocations.sql
-- Invocation Store 元数据层（设计 §6.1；字段集蓝本 = NewAPI logs 表，知识库 §4.4）。
-- 本表只存元数据，payload 体走 R2 信封加密（后续阶段，object_key 列已预留）。
-- 保留期 30 天滚动：当前用批量 DELETE 清理（PurgeOlderThan）；
-- 量级上来后升级为 RANGE 按日分区 + DROP PARTITION（需配套分区预建 job，见设计 §6.1）。

CREATE TABLE invocations (
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    org_id              BIGINT UNSIGNED NOT NULL,
    user_id             BIGINT UNSIGNED NULL COMMENT 'per-member key 的归属成员',
    api_key_id          BIGINT UNSIGNED NOT NULL,
    request_id          VARCHAR(32)  NOT NULL COMMENT '平台请求 id（X-Request-Id 回传客户端，支持工单锚点）',
    model               VARCHAR(128) NOT NULL,
    channel_id          BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '最终命中渠道（0=未到达任何渠道）',
    provider            VARCHAR(32)  NOT NULL DEFAULT '',
    status              VARCHAR(24)  NOT NULL COMMENT 'success|upstream_error|no_channel|insufficient_balance',
    status_code         INT          NOT NULL DEFAULT 0 COMMENT '返回客户端的 HTTP 状态',
    is_stream           TINYINT      NOT NULL DEFAULT 0,
    attempts            INT          NOT NULL DEFAULT 0 COMMENT '尝试过的渠道数（含失败重试）',
    prompt_tokens       BIGINT       NOT NULL DEFAULT 0,
    completion_tokens   BIGINT       NOT NULL DEFAULT 0,
    cost_usd            DECIMAL(20,8) NOT NULL DEFAULT 0 COMMENT '实收（journal 口径）',
    usage_source        VARCHAR(16)  NOT NULL DEFAULT '' COMMENT 'upstream|estimated（三档抽取来源）',
    frt_ms              INT          NOT NULL DEFAULT 0 COMMENT '首字节延迟 first-response-time（NewAPI frt）',
    duration_ms         INT          NOT NULL DEFAULT 0,
    upstream_request_id VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '厂商侧 request-id（找上游开工单的唯一凭证，rc.6）',
    error_brief         VARCHAR(255) NOT NULL DEFAULT '' COMMENT '截断后的错误摘要（禁 payload/PII）',
    payload_object_key  VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'R2 对象指针（payload 阶段启用，信封加密）',
    created_at          DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_inv_request (request_id),
    KEY idx_inv_org (org_id, id),
    KEY idx_inv_org_model (org_id, model, id),
    KEY idx_inv_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
