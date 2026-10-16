CREATE TABLE video_operations (
 id VARCHAR(64) PRIMARY KEY, org_id BIGINT UNSIGNED NOT NULL, resource_id VARCHAR(64) NOT NULL,
 kind VARCHAR(32) NOT NULL, status VARCHAR(16) NOT NULL, version BIGINT UNSIGNED NOT NULL,
 attempt_id VARCHAR(64) NOT NULL DEFAULT '', payload_ref VARCHAR(64) NOT NULL DEFAULT '',
 receipt_ref VARCHAR(64) NOT NULL DEFAULT '', http_status INT NOT NULL DEFAULT 0,
 receipt_applied_at DATETIME(3) NULL, received_outcome VARCHAR(32) NOT NULL DEFAULT '',
 received_task_id VARCHAR(128) NOT NULL DEFAULT '', lease_until DATETIME(3) NULL,
 error_code VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 KEY idx_video_operation_dispatch(status,created_at), KEY idx_video_operation_org(org_id,resource_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
