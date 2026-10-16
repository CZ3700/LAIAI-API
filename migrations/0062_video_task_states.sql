CREATE TABLE video_task_states (
 task_id VARCHAR(64) PRIMARY KEY, org_id BIGINT UNSIGNED NOT NULL,
 create_request_id VARCHAR(32) NOT NULL, provider_scope_id BIGINT UNSIGNED NOT NULL,
 credential_snapshot_id BIGINT UNSIGNED NOT NULL, provider_base_url VARCHAR(256) NOT NULL,
 config_version VARCHAR(64) NOT NULL, capability_version VARCHAR(64) NOT NULL,
 upstream_model VARCHAR(128) NOT NULL, no_charge_statuses VARCHAR(128) NOT NULL DEFAULT '',
 billing_state VARCHAR(32) NOT NULL, capture_history BOOLEAN NOT NULL,
 retention_days INT NOT NULL, sync_state VARCHAR(32) NOT NULL, artifact_status VARCHAR(32) NOT NULL,
 effective_payload_ref VARCHAR(64) NOT NULL, response_payload_ref VARCHAR(64) NOT NULL DEFAULT '',
 result_payload_ref VARCHAR(64) NOT NULL DEFAULT '', total_tokens BIGINT NULL,
 observed_completion_tokens BIGINT NULL, billing_snapshot_id BIGINT UNSIGNED NOT NULL,
 idempotency_record_id BIGINT UNSIGNED NOT NULL, parent_draft_task_id VARCHAR(64) NOT NULL DEFAULT '',
 row_version BIGINT UNSIGNED NOT NULL, event_sequence BIGINT UNSIGNED NOT NULL DEFAULT 0,
 last_synced_at DATETIME(3) NULL, next_poll_at DATETIME(3) NULL,
 review_due_at DATETIME(3) NULL, deleted_at DATETIME(3) NULL,
 deletion_operation_id VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_state_org(org_id,task_id), KEY idx_video_poll(next_poll_at,deleted_at),
 KEY idx_video_review(sync_state,review_due_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
