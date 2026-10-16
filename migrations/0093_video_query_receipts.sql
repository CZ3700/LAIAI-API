CREATE TABLE video_query_receipts (
 attempt_id VARCHAR(64) PRIMARY KEY, org_id BIGINT UNSIGNED NOT NULL,
 task_id VARCHAR(64) NOT NULL, sequence BIGINT UNSIGNED NOT NULL, task_row_version BIGINT UNSIGNED NOT NULL,
 http_status INT NOT NULL, outcome VARCHAR(32) NOT NULL, provider_request_id VARCHAR(256) NOT NULL DEFAULT '',
 observation_json TEXT NOT NULL, has_video BOOLEAN NOT NULL DEFAULT FALSE,
 has_last_frame BOOLEAN NOT NULL DEFAULT FALSE, payload_ref VARCHAR(64) NOT NULL,
 received_at DATETIME(3) NOT NULL, applied_at DATETIME(3) NULL, apply_error_code VARCHAR(64) NOT NULL DEFAULT '',
 UNIQUE KEY uk_video_query_sequence(org_id,task_id,sequence),
 KEY idx_video_query_unapplied(applied_at,received_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
