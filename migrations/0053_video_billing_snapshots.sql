-- Ark video: durable video_billing_snapshots
CREATE TABLE video_billing_snapshots (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, api_key_id BIGINT UNSIGNED NOT NULL,
 task_id VARCHAR(64) NOT NULL, provider_scope_id BIGINT UNSIGNED NOT NULL,
 model VARCHAR(64) NOT NULL, rule_id VARCHAR(64) NOT NULL, rule_version VARCHAR(64) NOT NULL,
 formula_version VARCHAR(64) NOT NULL, minimum_rule_version VARCHAR(64) NOT NULL,
 capability_version VARCHAR(64) NOT NULL, request_hmac CHAR(64) NOT NULL,
 metering_json TEXT NOT NULL, hold_id BIGINT UNSIGNED NOT NULL,
 estimated_tokens BIGINT UNSIGNED NOT NULL, hold_usd DECIMAL(20,8) NOT NULL,
 retail_usd_per_1m DECIMAL(24,12) NOT NULL, supplier_cny_per_1m DECIMAL(24,12) NOT NULL,
 effective_ratio DECIMAL(24,12) NOT NULL, fx_usd_per_cny DECIMAL(24,12) NOT NULL,
 fx_version VARCHAR(64) NOT NULL, fx_observed_at DATETIME(3) NOT NULL,
 supplier_reserved_cny DECIMAL(20,8) NOT NULL, supplier_reserved_usd DECIMAL(20,8) NOT NULL,
 created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_snapshot_task(org_id,task_id), UNIQUE KEY uk_video_snapshot_hold(hold_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
