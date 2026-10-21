-- Ark video: durable video_pricing_rules
CREATE TABLE video_pricing_rules (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 rule_id VARCHAR(64) NOT NULL, version VARCHAR(64) NOT NULL,
 provider_scope_id BIGINT UNSIGNED NOT NULL, model VARCHAR(64) NOT NULL,
 mode VARCHAR(32) NOT NULL, resolution VARCHAR(32) NOT NULL,
 input_video BOOLEAN NOT NULL, service_tier VARCHAR(32) NOT NULL, draft BOOLEAN NOT NULL,
 retail_usd_per_1m DECIMAL(24,12) NOT NULL, supplier_cny_per_1m DECIMAL(24,12) NOT NULL,
 minimum_tokens BIGINT UNSIGNED NOT NULL, minimum_rule_version VARCHAR(64) NOT NULL,
 risk_multiplier DECIMAL(24,12) NOT NULL, formula_version VARCHAR(64) NOT NULL,
 source_hash CHAR(64) NOT NULL, effective_from DATETIME(3) NOT NULL,
 effective_until DATETIME(3) NULL, enabled BOOLEAN NOT NULL,
 UNIQUE KEY uk_video_price_version(rule_id,version),
 KEY idx_video_price_lookup(provider_scope_id,model,enabled,effective_from)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
