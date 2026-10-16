-- Ark video: durable invocation_payload_staging
CREATE TABLE invocation_payload_staging (
 payload_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 ciphertext LONGBLOB NOT NULL, attempts INT NOT NULL DEFAULT 0,
 next_attempt_at DATETIME(3) NOT NULL, lease_owner VARCHAR(64) NOT NULL DEFAULT '',
 lease_expires_at DATETIME(3) NULL, fencing_token BIGINT UNSIGNED NOT NULL DEFAULT 0,
 created_at DATETIME(3) NOT NULL,
 KEY idx_video_payload_upload(next_attempt_at,lease_expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
