-- Ark S2: durable video_provider_bill_bindings
CREATE TABLE video_provider_bill_bindings (
 line_id BIGINT UNSIGNED NOT NULL PRIMARY KEY, org_id BIGINT UNSIGNED NOT NULL,
 task_id VARCHAR(64) NOT NULL, binding_version VARCHAR(64) NOT NULL,
 created_at DATETIME(3) NOT NULL,
 KEY idx_video_bill_binding_task(org_id,task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
