-- Ark console private asset metadata.
CREATE TABLE video_media_uploads (
 id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY, org_id BIGINT UNSIGNED NOT NULL,
 user_id BIGINT UNSIGNED NOT NULL DEFAULT 0, key_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
 media_type VARCHAR(16) NOT NULL, status VARCHAR(16) NOT NULL, idempotency_hash CHAR(64) NOT NULL, body_hash CHAR(64) NOT NULL,
 object_ref VARCHAR(64) NOT NULL DEFAULT '', object_sha256 CHAR(64) NOT NULL DEFAULT '', mime VARCHAR(64) NOT NULL DEFAULT '',
 bytes BIGINT NOT NULL DEFAULT 0, duration_ms BIGINT NOT NULL DEFAULT 0,
 width INT NOT NULL DEFAULT 0, height INT NOT NULL DEFAULT 0, fps_num INT NOT NULL DEFAULT 0, fps_den INT NOT NULL DEFAULT 0,
 error_code VARCHAR(64) NOT NULL DEFAULT '', expires_at DATETIME(3) NOT NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_media_upload_idem(org_id,idempotency_hash), KEY idx_video_media_upload_expiry(status,expires_at),KEY idx_video_media_upload_org(org_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
