-- Ark video: durable video_quota_policies
CREATE TABLE video_quota_policies (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, scope VARCHAR(16) NOT NULL,
 owner_id BIGINT UNSIGNED NOT NULL, model VARCHAR(64) NOT NULL,
 metric VARCHAR(16) NOT NULL, period VARCHAR(16) NOT NULL, timezone VARCHAR(64) NOT NULL,
 limit_amount DECIMAL(20,8) NULL, limit_units BIGINT UNSIGNED NULL,
 version VARCHAR(64) NOT NULL, enabled BOOLEAN NOT NULL,
 UNIQUE KEY uk_video_quota_policy(org_id,scope,owner_id,model,metric,period,version),
 KEY idx_video_quota_lookup(org_id,scope,owner_id,enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
