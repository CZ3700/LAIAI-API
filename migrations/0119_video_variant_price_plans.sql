-- Administrative price plans only. These rows are never used for admission,
-- reservation or public pricing; the variants still have no adapter capability.
CREATE TABLE video_variant_price_plans (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 channel_id BIGINT UNSIGNED NOT NULL,
 model VARCHAR(128) NOT NULL,
 upstream_model VARCHAR(128) NOT NULL,
 canonical_model VARCHAR(64) NOT NULL,
 provider_scope_id BIGINT UNSIGNED NOT NULL,
 version VARCHAR(64) NOT NULL,
 created_by BIGINT UNSIGNED NOT NULL,
 idempotency_key VARCHAR(128) NOT NULL,
 body_hash CHAR(64) NOT NULL,
 proposal_json LONGTEXT NOT NULL,
 context_json LONGTEXT NOT NULL,
 created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_variant_plan_idempotency(created_by,idempotency_key),
 UNIQUE KEY uk_variant_plan_version(channel_id,model,version),
 KEY idx_variant_plan_current(channel_id,model,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
