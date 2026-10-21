CREATE TABLE video_model_configs (
 public_model VARCHAR(64) PRIMARY KEY, version VARCHAR(64) NOT NULL,
 provider_scope_id BIGINT UNSIGNED NOT NULL, channel_id BIGINT UNSIGNED NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT FALSE, capability_json TEXT NOT NULL,
 pricing_version VARCHAR(64) NOT NULL, no_charge_statuses VARCHAR(128) NOT NULL DEFAULT '',
 max_input_video_duration_ms BIGINT NOT NULL DEFAULT 0, bounds_json TEXT NOT NULL,
 verified_at DATETIME(3) NULL, updated_at DATETIME(3) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
