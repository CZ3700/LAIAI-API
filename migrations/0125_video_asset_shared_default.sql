-- Explicitly published infrastructure only; resource ownership remains private.
CREATE TABLE video_asset_shared_defaults (
 id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
 revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
 provider_scope_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
 scope_version BIGINT UNSIGNED NOT NULL DEFAULT 0,
 enabled BOOLEAN NOT NULL DEFAULT FALSE,
 allow_async BOOLEAN NOT NULL DEFAULT FALSE,
 updated_by BIGINT UNSIGNED NOT NULL DEFAULT 0,
 updated_at DATETIME(3) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

INSERT INTO video_asset_shared_defaults(id,revision,provider_scope_id,scope_version,enabled,allow_async,updated_by,updated_at)
VALUES(1,0,0,0,FALSE,FALSE,0,'2026-10-22 00:00:00.000');
