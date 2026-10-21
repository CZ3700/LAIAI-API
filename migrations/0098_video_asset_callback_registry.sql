CREATE TABLE video_asset_callback_registry (
 id VARCHAR(64) NOT NULL PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL,
 url_hmac CHAR(64) NOT NULL,
 value_cipher TEXT NOT NULL,
 evidence_ref VARCHAR(64) NOT NULL,
 expires_at DATETIME(3) NOT NULL,
 revoked_at DATETIME(3) NULL,
 created_by BIGINT UNSIGNED NOT NULL,
 created_at DATETIME(3) NOT NULL,
 KEY idx_video_asset_callback_lookup(org_id,url_hmac,expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
