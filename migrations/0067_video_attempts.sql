CREATE TABLE video_attempts (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, org_id BIGINT UNSIGNED NOT NULL,
 task_id VARCHAR(64) NOT NULL, attempt_id VARCHAR(64) NOT NULL DEFAULT '', request_id VARCHAR(64) NOT NULL,
 operation_id VARCHAR(64) NOT NULL DEFAULT '', phase VARCHAR(32) NOT NULL,
 http_status INT NOT NULL DEFAULT 0, upstream_request_id VARCHAR(256) NOT NULL DEFAULT '',
 evidence_ref VARCHAR(64) NOT NULL DEFAULT '', error_code VARCHAR(64) NOT NULL DEFAULT '', observed_at DATETIME(3) NOT NULL,
 KEY idx_video_attempt_task(org_id,task_id,id), KEY idx_video_attempt_operation(operation_id,phase)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
