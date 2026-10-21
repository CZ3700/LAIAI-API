CREATE TABLE video_outbox (
 event_id VARCHAR(64) PRIMARY KEY, org_id BIGINT UNSIGNED NOT NULL, task_id VARCHAR(64) NOT NULL,
 topic VARCHAR(64) NOT NULL, status VARCHAR(16) NOT NULL, attempts INT NOT NULL DEFAULT 0,
 next_attempt_at DATETIME(3) NOT NULL, created_at DATETIME(3) NOT NULL,
 KEY idx_video_outbox_ready(status,next_attempt_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
