CREATE TABLE video_upstream_bindings (
 provider_scope_id BIGINT UNSIGNED NOT NULL, upstream_task_id VARCHAR(128) NOT NULL,
 org_id BIGINT UNSIGNED NOT NULL, task_id VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL,
 PRIMARY KEY(provider_scope_id,upstream_task_id), UNIQUE KEY uk_video_upstream_task(org_id,task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
