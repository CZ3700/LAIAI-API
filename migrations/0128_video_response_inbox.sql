-- Expand only. Legacy claims do not manufacture an execution registration.
-- Shared resource IDs keep the existing tables' collation so indexed joins
-- do not require coercion. Protocol/version text retains exact NO PAD matching.
CREATE TABLE video_execution_attempts (
 attempt_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, task_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 operation_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL, action VARCHAR(32) NOT NULL,
 storage_protocol VARCHAR(32) NOT NULL,
 provider_scope_id BIGINT UNSIGNED NOT NULL,
 credential_snapshot_id BIGINT UNSIGNED NOT NULL,
 billing_snapshot_id BIGINT UNSIGNED NOT NULL,
 input_payload_ref VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 input_identity_hmac CHAR(64) NOT NULL, input_hmac_version VARCHAR(32) NOT NULL,
 runtime_version VARCHAR(64) NOT NULL, adapter_version VARCHAR(64) NOT NULL,
 protocol_contract_version VARCHAR(64) NOT NULL, billing_schema_version VARCHAR(64) NOT NULL,
 execution_schema_version INT UNSIGNED NOT NULL, observation_schema_version INT UNSIGNED NOT NULL,
 operation_claim_version BIGINT UNSIGNED NOT NULL, evidence_hmac_version VARCHAR(32) NOT NULL,
 capture_history BOOLEAN NOT NULL, retention_days INT NOT NULL,
 registered_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_execution_claim(org_id,operation_id,operation_claim_version),
 KEY idx_video_execution_operation(org_id,operation_id),
 KEY idx_video_execution_task(org_id,task_id,action,registered_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;

CREATE TABLE video_response_inbox (
 receipt_id VARCHAR(64) NOT NULL PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, task_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 operation_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL, attempt_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 action VARCHAR(32) NOT NULL, storage_protocol VARCHAR(32) NOT NULL,
 provider_scope_id BIGINT UNSIGNED NOT NULL,
 credential_snapshot_id BIGINT UNSIGNED NOT NULL, billing_snapshot_id BIGINT UNSIGNED NOT NULL,
 runtime_version VARCHAR(64) NOT NULL, adapter_version VARCHAR(64) NOT NULL,
 protocol_contract_version VARCHAR(64) NOT NULL, billing_schema_version VARCHAR(64) NOT NULL,
 execution_schema_version INT UNSIGNED NOT NULL, observation_schema_version INT UNSIGNED NOT NULL,
 evidence_hmac_version VARCHAR(32) NOT NULL, evidence_hmac CHAR(64) NOT NULL,
 http_status INT NOT NULL, outcome VARCHAR(32) NOT NULL, safe_error_code VARCHAR(64) NOT NULL,
 upstream_task_id VARCHAR(128) NOT NULL, provider_request_id VARCHAR(256) NOT NULL,
 truncated BOOLEAN NOT NULL, captured_body_bytes BIGINT NOT NULL,
 payload_ref VARCHAR(64) NOT NULL, received_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_inbox_evidence(org_id,attempt_id,evidence_hmac_version,evidence_hmac),
 KEY idx_video_inbox_attempt(org_id,attempt_id,received_at,receipt_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;

CREATE TABLE video_receipt_applications (
 receipt_id VARCHAR(64) NOT NULL PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, task_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL, attempt_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 state VARCHAR(32) NOT NULL, version BIGINT UNSIGNED NOT NULL,
 next_attempt_at DATETIME(3) NOT NULL, error_code VARCHAR(64) NOT NULL,
 applied_at DATETIME(3) NULL, applied_event_id VARCHAR(64) NOT NULL,
 billing_entry_id BIGINT UNSIGNED NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 KEY idx_video_receipt_apply(state,next_attempt_at,receipt_id),
 KEY idx_video_receipt_task(org_id,task_id,attempt_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
