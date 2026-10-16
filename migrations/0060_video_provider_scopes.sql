CREATE TABLE video_provider_scopes (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, scope_key VARCHAR(128) NOT NULL,
 account_ref VARCHAR(128) NOT NULL, region VARCHAR(32) NOT NULL, project_name VARCHAR(128) NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT FALSE, fx_usd_per_cny DECIMAL(24,12) NOT NULL,
 fx_version VARCHAR(64) NOT NULL, fx_observed_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_provider_scope(scope_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
