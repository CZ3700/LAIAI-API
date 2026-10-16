CREATE TABLE video_asset_scope_configs (
 provider_scope_id BIGINT UNSIGNED NOT NULL,
 version BIGINT UNSIGNED NOT NULL,
 account_ref VARCHAR(64) NOT NULL,
 region VARCHAR(32) NOT NULL,
 project_name VARCHAR(64) NOT NULL,
 capabilities_json TEXT NOT NULL,
 credential_cipher MEDIUMTEXT NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT FALSE,
 created_by BIGINT UNSIGNED NOT NULL,
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 PRIMARY KEY(provider_scope_id,version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
