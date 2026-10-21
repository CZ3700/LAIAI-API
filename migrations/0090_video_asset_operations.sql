-- Ark S2: durable video_asset_operations
CREATE TABLE video_asset_operations (
 operation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, action VARCHAR(64) NOT NULL, resource_type VARCHAR(32) NOT NULL, resource_id VARCHAR(64) NOT NULL,
 request_id VARCHAR(64) NOT NULL, canonical_path VARCHAR(200) NOT NULL,
 hmac_key_version VARCHAR(64) NOT NULL, idempotency_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 body_hash CHAR(64) NOT NULL, scope_id VARCHAR(64) NOT NULL, scope_version BIGINT UNSIGNED NOT NULL,
 account_id VARCHAR(64) NOT NULL, region VARCHAR(32) NOT NULL, project VARCHAR(64) NOT NULL,
 status VARCHAR(16) NOT NULL, payload_id VARCHAR(64) NOT NULL, response_payload_id VARCHAR(64) NOT NULL,
 lease_owner VARCHAR(64) NOT NULL, lease_expires_at DATETIME(3) NULL, fence BIGINT UNSIGNED NOT NULL,
 http_status INT NOT NULL, upstream_request_id VARCHAR(128) NOT NULL, safe_error_code VARCHAR(64) NOT NULL, receipt TEXT NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL, next_attempt_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_asset_idempotency(org_id,canonical_path,idempotency_hash),
 KEY idx_video_asset_work(status,next_attempt_at), KEY idx_video_asset_lease(status,lease_expires_at),
 KEY idx_video_asset_operation_resource(org_id,resource_id,action,status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
