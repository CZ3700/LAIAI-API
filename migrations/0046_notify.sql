-- 0046_notify.sql
-- Notify 统一层 v1（设计 §4.9，2026-07-11；顶级审计修排名 9 异步 parity）：
--   notify_settings：每 org 一行的事件订阅 + 通道配置（webhook secret 信封加密，红线 7）。
--   notify_outbox：出站投递 outbox（at-least-once：leader 定时重扫 + 指数退避，attempts≥6 → dead）。
-- payload 元数据-only（task_id/model/status/cost——绝无 prompt/产物内容/key）。
CREATE TABLE IF NOT EXISTS notify_settings (
    org_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    email_enabled TINYINT NOT NULL DEFAULT 0 COMMENT '邮件通道开关（收件人=org owner 邮箱）',
    webhook_url VARCHAR(512) NOT NULL DEFAULT '' COMMENT '出站 webhook 目标（https-only，创建期 SSRF 校验）',
    webhook_secret_enc VARCHAR(512) NOT NULL DEFAULT '' COMMENT 'HMAC 签名密钥（信封加密，绝不明文回显）',
    events VARCHAR(255) NOT NULL DEFAULT '' COMMENT '订阅事件 JSON 数组，如 ["task.completed","task.failed","balance.low"]',
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS notify_outbox (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    org_id BIGINT UNSIGNED NOT NULL,
    event VARCHAR(32) NOT NULL,
    payload TEXT NOT NULL COMMENT '事件元数据 JSON（红线 7：无 prompt/PII/key）',
    channel VARCHAR(16) NOT NULL COMMENT 'email|webhook',
    target VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '投递目标快照（邮箱逗号表 / webhook URL）',
    status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT 'pending|sent|dead',
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at DATETIME(3) NOT NULL,
    sent_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    KEY idx_outbox_due (status, next_attempt_at),
    KEY idx_outbox_org (org_id, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
