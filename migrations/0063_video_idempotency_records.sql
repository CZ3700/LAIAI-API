CREATE TABLE video_idempotency_records (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, org_id BIGINT UNSIGNED NOT NULL,
 method VARCHAR(8) NOT NULL, operation VARCHAR(160) NOT NULL,
 key_hmac CHAR(64) NOT NULL, body_hmac CHAR(64) NOT NULL, hash_version VARCHAR(32) NOT NULL,
 resource_id VARCHAR(64) NOT NULL, operation_id VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_idempotency(org_id,method,operation,key_hmac)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
