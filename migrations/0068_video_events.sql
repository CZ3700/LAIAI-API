CREATE TABLE video_events (
 id VARCHAR(64) PRIMARY KEY, org_id BIGINT UNSIGNED NOT NULL, task_id VARCHAR(64) NOT NULL,
 sequence BIGINT UNSIGNED NOT NULL, type VARCHAR(64) NOT NULL, request_id VARCHAR(64) NOT NULL,
 from_status VARCHAR(32) NOT NULL DEFAULT '', to_status VARCHAR(32) NOT NULL DEFAULT '',
 billing_state VARCHAR(32) NOT NULL, evidence_ref VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_event_sequence(org_id,task_id,sequence)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
