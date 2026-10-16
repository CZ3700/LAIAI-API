-- Keep per-HTTP-request identity separate from the asynchronous task state.
-- Historical requests without a context remain explicitly unlinked.
CREATE TABLE video_request_contexts (
 request_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL,
 operation VARCHAR(32) NOT NULL,
 public_model VARCHAR(128) NOT NULL DEFAULT '',
 task_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 operation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 idempotency_record_id BIGINT UNSIGNED NULL,
 replay_state VARCHAR(16) NOT NULL DEFAULT 'unknown',
 origin_request_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 KEY idx_video_request_context_task (org_id, task_id, request_id),
 KEY idx_video_request_context_model (org_id, public_model, request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
