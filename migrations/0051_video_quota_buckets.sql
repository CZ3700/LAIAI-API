-- Ark video: durable video_quota_buckets
CREATE TABLE video_quota_buckets (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 policy_id BIGINT UNSIGNED NOT NULL, scope_key CHAR(64) NOT NULL,
 timezone VARCHAR(64) NOT NULL, org_id BIGINT UNSIGNED NOT NULL,
 period_start DATETIME(3) NOT NULL, period_end DATETIME(3) NULL,
 limit_amount DECIMAL(20,8) NULL, limit_units BIGINT UNSIGNED NULL,
 reserved_amount DECIMAL(20,8) NOT NULL DEFAULT 0, consumed_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
 reserved_units BIGINT UNSIGNED NOT NULL DEFAULT 0, consumed_units BIGINT UNSIGNED NOT NULL DEFAULT 0,
 blocked BOOLEAN NOT NULL DEFAULT FALSE,
 UNIQUE KEY uk_video_quota_window(scope_key,period_start),
 KEY idx_video_quota_policy(policy_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
