CREATE TABLE video_provider_rates (
 scope_id BIGINT UNSIGNED NOT NULL, action VARCHAR(32) NOT NULL, next_allowed_at DATETIME(3) NOT NULL,
 PRIMARY KEY(scope_id,action)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
