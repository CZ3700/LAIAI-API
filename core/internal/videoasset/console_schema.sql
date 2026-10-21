-- Draft for the root migration owner; split into sequential formal migrations.
CREATE TABLE video_asset_details (
 resource_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, description VARCHAR(1200) NOT NULL DEFAULT '', source VARCHAR(32) NOT NULL DEFAULT '',
 last_synced_at DATETIME(3) NULL, updated_at DATETIME(3) NOT NULL,
 KEY idx_video_asset_details_org(org_id,resource_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE video_portrait_labels (
 session_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, name VARCHAR(256) NOT NULL DEFAULT '', description VARCHAR(1200) NOT NULL DEFAULT '',
 name_synced BOOLEAN NOT NULL DEFAULT FALSE, KEY idx_video_portrait_label_org(org_id,session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE video_asset_sync_jobs (
 id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY, org_id BIGINT UNSIGNED NOT NULL,
 scope_id VARCHAR(64) NOT NULL, scope_version BIGINT UNSIGNED NOT NULL,
 account_id VARCHAR(64) NOT NULL, region VARCHAR(32) NOT NULL, project VARCHAR(64) NOT NULL,
 mode VARCHAR(16) NOT NULL, kind VARCHAR(16) NOT NULL, phase VARCHAR(16) NOT NULL, status VARCHAR(16) NOT NULL,
 group_id VARCHAR(64) NOT NULL DEFAULT '', after_group_id VARCHAR(64) NOT NULL DEFAULT '', checkpoint_id VARCHAR(64) NOT NULL DEFAULT '',
 seen_hashes MEDIUMTEXT NOT NULL, idempotency_hash CHAR(64) NOT NULL,
 pages BIGINT NOT NULL DEFAULT 0, items BIGINT NOT NULL DEFAULT 0, fence BIGINT UNSIGNED NOT NULL DEFAULT 0,
 lease_until DATETIME(3) NULL, error_code VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 request_id VARCHAR(64) NOT NULL DEFAULT '', http_status INT NOT NULL DEFAULT 0, upstream_request_id VARCHAR(128) NOT NULL DEFAULT '',
 attempts BIGINT UNSIGNED NOT NULL DEFAULT 0, retry_count BIGINT UNSIGNED NOT NULL DEFAULT 0, next_attempt_at DATETIME(3) NULL,
 last_request_payload_id VARCHAR(64) NOT NULL DEFAULT '', last_response_payload_id VARCHAR(64) NOT NULL DEFAULT '',
 UNIQUE KEY uk_video_asset_sync_idem(org_id,idempotency_hash), KEY idx_video_asset_sync_work(status,lease_until), KEY idx_video_asset_sync_scope(scope_id,status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE video_asset_discoveries (
 id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 scope_id VARCHAR(64) NOT NULL, scope_version BIGINT UNSIGNED NOT NULL,
 account_id VARCHAR(64) NOT NULL, region VARCHAR(32) NOT NULL, project VARCHAR(64) NOT NULL,
 provider_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 kind VARCHAR(16) NOT NULL, name VARCHAR(256) NOT NULL DEFAULT '', description VARCHAR(1200) NOT NULL DEFAULT '',
 status VARCHAR(16) NOT NULL, assigned_org BIGINT UNSIGNED NOT NULL DEFAULT 0, group_id VARCHAR(64) NOT NULL DEFAULT '',
 version BIGINT UNSIGNED NOT NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_asset_discovery(scope_id,provider_id), KEY idx_video_asset_discovery_scope(scope_id,status,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
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

-- Integration migration 0118 (separate from the five tables above).
CREATE TABLE video_asset_sync_gates (
 account_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 version BIGINT UNSIGNED NOT NULL DEFAULT 0,
 next_dispatch_at DATETIME(3) NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
