-- Ark video: durable invocation_payloads
CREATE TABLE invocation_payloads (
 payload_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL,
 request_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 attempt_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 correlation_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 direction VARCHAR(32) NOT NULL, purpose VARCHAR(16) NOT NULL,
 schema_version INT UNSIGNED NOT NULL, body_format VARCHAR(32) NOT NULL,
 encryption_key_id VARCHAR(128) NOT NULL DEFAULT '', wrapped_key TEXT NOT NULL,
 nonce VARBINARY(32) NOT NULL, ciphertext_sha256 VARCHAR(64) NOT NULL DEFAULT '',
 plaintext_bytes BIGINT NOT NULL DEFAULT 0, redaction_version VARCHAR(32) NOT NULL,
 capture_status VARCHAR(16) NOT NULL, object_ref VARCHAR(256) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL, expires_at DATETIME(3) NOT NULL,
 cleanup_after DATETIME(3) NULL, object_deleted_at DATETIME(3) NULL,
 UNIQUE KEY uk_video_payload_correlation(correlation_key),
 KEY idx_video_payload_org_request(org_id,request_id),
 KEY idx_video_payload_expiry(capture_status,expires_at),
 KEY idx_video_payload_cleanup(capture_status,object_deleted_at,cleanup_after)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
