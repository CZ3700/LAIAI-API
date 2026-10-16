-- Ark S2: durable video_portrait_authorizations
CREATE TABLE video_portrait_authorizations (
 session_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, owner_user_id BIGINT UNSIGNED NOT NULL, created_by_key_id BIGINT UNSIGNED NOT NULL,
 scope_id VARCHAR(64) NOT NULL, scope_version BIGINT UNSIGNED NOT NULL,
 account_id VARCHAR(64) NOT NULL, region VARCHAR(32) NOT NULL, project VARCHAR(64) NOT NULL,
 session_status VARCHAR(32) NOT NULL, license_status VARCHAR(16) NOT NULL,
 create_operation_id VARCHAR(64) NOT NULL, callback_target_id VARCHAR(64) NOT NULL, secret_ref VARCHAR(64) NOT NULL,
 callback_state_hash VARCHAR(64) NOT NULL, callback_state_version VARCHAR(64) NOT NULL, callback_used_at DATETIME(3) NULL,
 session_expires_at DATETIME(3) NULL, group_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 valid_from DATETIME(3) NULL, valid_until DATETIME(3) NULL, license_scope VARCHAR(64) NOT NULL,
 license_version BIGINT UNSIGNED NOT NULL, license_evidence_ref VARCHAR(64) NOT NULL, license_source VARCHAR(32) NOT NULL,
 last_verified_at DATETIME(3) NULL, reverify_due_at DATETIME(3) NULL, revoked_at DATETIME(3) NULL,
 row_version BIGINT UNSIGNED NOT NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_portrait_org(org_id,session_id), UNIQUE KEY uk_video_portrait_group(group_id),
 KEY idx_video_portrait_poll(session_status,session_expires_at,updated_at),
 KEY idx_video_portrait_license(license_status,reverify_due_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
