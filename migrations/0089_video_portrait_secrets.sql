-- Ark S2: durable video_portrait_secrets
CREATE TABLE video_portrait_secrets (
 secret_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, session_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 value_cipher TEXT NOT NULL, expires_at DATETIME(3) NOT NULL, destroyed_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_portrait_secret(org_id,session_id), KEY idx_video_portrait_secret_expiry(expires_at,destroyed_at),
 CONSTRAINT fk_video_portrait_secret FOREIGN KEY(org_id,session_id) REFERENCES video_portrait_authorizations(org_id,session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
